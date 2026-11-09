package provider

import (
	"bufio"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
	"testing/synctest"
	"time"
)

func mockClient(fn testTransport) *Client {
	c := NewClient(func(context.Context) (string, error) { return "token", nil })
	c.HTTPClient = &http.Client{Transport: fn}
	return c
}

func rawResponse(status int, contentType, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     http.Header{"Content-Type": {contentType}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func streamResponse(event string) *http.Response {
	return rawResponse(200, "text/event-stream", "data: "+event+"\n\n")
}

type observedBody struct {
	io.Reader
	closed  bool
	onClose func()
}

func (b *observedBody) Close() error {
	b.closed = true
	if b.onClose != nil {
		b.onClose()
	}
	return nil
}

type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }

func TestResponsesRequest(t *testing.T) {
	s := NewSession("model", "high", "instructions", []Function{testFunction{}})
	s.ServiceTier = "priority"
	s.CompactThreshold = 1234
	s.Events = []map[string]any{{"type": "message", "role": "user", "content": "hello"}}
	c := mockClient(func(r *http.Request) (*http.Response, error) {
		if r.Method != "POST" || r.URL.String() != "https://api.openai.com/v1/responses" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL)
		}
		for name, want := range map[string]string{
			"Authorization": "Bearer token", "Content-Type": "application/json", "Accept": "text/event-stream",
		} {
			if got := r.Header.Get(name); got != want {
				t.Errorf("%s = %q, want %q", name, got, want)
			}
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		wantJSON := `{
			"model":"model", "instructions":"instructions", "service_tier":"priority",
			"reasoning":{"summary":"detailed","effort":"high"},
			"context_management":[{"type":"compaction","compact_threshold":1234}],
			"store":false,"stream":true,
			"tools":[{"type":"namespace","name":"default","description":"Default","tools":[{"type":"function","name":"echo"}]},{"type":"web_search"}],
			"input":[{"type":"message","role":"user","content":"hello"}]
		}`
		var want map[string]any
		if err := json.Unmarshal([]byte(wantJSON), &want); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("request = %s, want %s", body, wantJSON)
		}
		return completedResponse([]map[string]any{}), nil
	})
	got, err := c.responses(context.Background(), s)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("responses() = %#v, %v", got, err)
	}
}

func TestResponsesSetupErrors(t *testing.T) {
	sentinel := errors.New("test failure")
	for _, tt := range []struct {
		name     string
		setup    func(*Client, *Session) context.Context
		contains string
		is       error
	}{
		{"encode", func(_ *Client, s *Session) context.Context {
			s.Events = []map[string]any{{"bad": make(chan int)}}
			return context.Background()
		}, "encoding request object", nil},
		{"canceled", func(_ *Client, _ *Session) context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx
		}, "", context.Canceled},
		{"token", func(c *Client, _ *Session) context.Context {
			c.Token = func(context.Context) (string, error) { return "", sentinel }
			return context.Background()
		}, "getting client access token", sentinel},
		{"url", func(c *Client, _ *Session) context.Context {
			c.BaseURL = ":invalid"
			return context.Background()
		}, "constructing HTTP request", nil},
		{"transport", func(c *Client, _ *Session) context.Context {
			c.HTTPClient = &http.Client{Transport: testTransport(func(*http.Request) (*http.Response, error) { return nil, sentinel })}
			return context.Background()
		}, "doing request", sentinel},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := mockClient(func(*http.Request) (*http.Response, error) {
				t.Fatal("unexpected request")
				return nil, nil
			})
			s := NewSession("model", "low", "", nil)
			ctx := tt.setup(c, s)
			got, err := c.responses(ctx, s)
			if got != nil || err == nil || !strings.Contains(err.Error(), tt.contains) || (tt.is != nil && !errors.Is(err, tt.is)) {
				t.Fatalf("responses() = %#v, %v; want %q, %v", got, err, tt.contains, tt.is)
			}
		})
	}
}

func TestResponsesStreams(t *testing.T) {
	for _, tt := range []struct {
		name, body, contentType, contains string
		is                                error
	}{
		{"content type", "", "application/json", "unexpected response Content-Type", nil},
		{"invalid content type", "", "invalid;", "unexpected response Content-Type", nil},
		{"malformed event", "data: {\n\n", "text/event-stream", "decoding response event", nil},
		{"wrong event type", "data: {\"type\":42}\n\n", "text/event-stream", "decoding response event", nil},
		{"malformed terminal event", "data: {\"type\":\"response.completed\",\"response\":42}\n\n", "text/event-stream", "decoding response event", nil},
		{"missing response", "data: {\"type\":\"response.completed\"}\n\n", "text/event-stream", "invalid completed response", nil},
		{"incomplete missing response", "data: {\"type\":\"response.incomplete\"}\n\n", "text/event-stream", "missing response", ErrResponseIncomplete},
		{"incomplete reason", "data: {\"type\":\"response.incomplete\",\"response\":{\"incomplete_details\":{\"reason\":\"max_output_tokens\"}}}\n\n", "text/event-stream", "max_output_tokens", ErrResponseIncomplete},
		{"empty stream", "", "text/event-stream", "ended before completion", io.ErrUnexpectedEOF},
		{"unterminated event", "data: {\"type\":\"response.completed\"}\n", "text/event-stream", "ended before completion", io.ErrUnexpectedEOF},
		{"unknown event", ": keepalive\n\nevent: response.created\ndata: {\"type\":\"response.created\"}\n\n", "text/event-stream", "ended before completion", io.ErrUnexpectedEOF},
		{"scanner line limit", strings.Repeat("x", 16<<20), "text/event-stream", "reading response stream", bufio.ErrTooLong},
		{"event size limit", "data: " + strings.Repeat("x", 8<<20) + "\ndata: " + strings.Repeat("x", 8<<20) + "\n\n", "text/event-stream", "response event exceeds 16 MiB", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := &observedBody{Reader: strings.NewReader(tt.body)}
			c := mockClient(func(*http.Request) (*http.Response, error) {
				r := rawResponse(200, tt.contentType, "")
				r.Body = body
				return r, nil
			})
			got, err := c.responses(context.Background(), NewSession("model", "low", "", nil))
			if got != nil || err == nil || !strings.Contains(err.Error(), tt.contains) || (tt.is != nil && !errors.Is(err, tt.is)) {
				t.Fatalf("responses() = %#v, %v; want %q, %v", got, err, tt.contains, tt.is)
			}
			if !body.closed {
				t.Error("response body was not closed")
			}
		})
	}
}

func TestResponsesCompletedValidation(t *testing.T) {
	for _, response := range []string{
		`null`,
		`{"status":"completed","output":[]}`,
		`{"id":"r","status":"in_progress","output":[]}`,
		`{"id":"r","status":"completed","error":{"code":"bad"},"output":[]}`,
		`{"id":"r","status":"completed"}`,
		`{"id":"r","status":"completed","output":null}`,
	} {
		t.Run(response, func(t *testing.T) {
			c := mockClient(func(*http.Request) (*http.Response, error) {
				return streamResponse(`{"type":"response.completed","response":` + response + `}`), nil
			})
			if _, err := c.responses(context.Background(), NewSession("model", "low", "", nil)); err == nil || err.Error() != "invalid completed response" {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestResponsesMultilineSSE(t *testing.T) {
	body := ": comment\r\n\r\nevent: response.created\r\ndata: {\"type\":\"response.created\"}\r\n\r\n" +
		"data:{\"type\":\"response.completed\",\r\n" +
		"data: \"response\":{\"id\":\"r\",\"status\":\"completed\",\"output\":[]}}\r\n\r\n"
	c := mockClient(func(*http.Request) (*http.Response, error) {
		return rawResponse(200, "text/event-stream; charset=utf-8", body), nil
	})
	got, err := c.responses(context.Background(), NewSession("model", "low", "", nil))
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("responses() = %#v, %v", got, err)
	}
}

func TestResponsesChunkedStream(t *testing.T) {
	output := []map[string]any{{
		"type": "message", "role": "assistant", "content": strings.Repeat("世界🙂", 1024),
	}}
	res := completedResponse(output)
	completed, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	prefix := ": keepalive\r\n\r\nevent: response.created\r\ndata: {\"type\":\"response.created\"}\r\n\r\n"
	for _, tt := range []struct {
		name string
		wrap func(io.Reader) io.Reader
	}{
		{"one byte", iotest.OneByteReader},
		{"partial reads", iotest.HalfReader},
		{"data and EOF together", iotest.DataErrReader},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := &observedBody{Reader: tt.wrap(io.MultiReader(strings.NewReader(prefix), strings.NewReader(string(completed))))}
			c := mockClient(func(*http.Request) (*http.Response, error) {
				r := rawResponse(200, "text/event-stream", "")
				r.Body = body
				return r, nil
			})
			got, err := c.responses(context.Background(), NewSession("model", "low", "", nil))
			if err != nil || !reflect.DeepEqual(got, output) {
				t.Fatalf("responses() did not preserve fragmented Unicode output: %v", err)
			}
			if !body.closed {
				t.Error("completed response body was not closed")
			}
		})
	}
}

func TestResponsesReadErrorsAndCancellation(t *testing.T) {
	readErr := errors.New("broken body")
	for _, tt := range []struct {
		name          string
		status        int
		cancelOnClose bool
		want          error
		contains      string
	}{
		{"stream read", 200, false, readErr, "reading response stream"},
		{"HTTP body read", 418, false, nil, "reading body: broken body"},
		{"stream cancellation", 200, true, context.Canceled, ""},
		{"HTTP cancellation", 503, true, context.Canceled, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			body := &observedBody{Reader: errorReader{readErr}}
			if tt.cancelOnClose {
				body.onClose = cancel
			}
			c := mockClient(func(*http.Request) (*http.Response, error) {
				r := rawResponse(tt.status, "text/event-stream", "")
				r.Body = body
				return r, nil
			})
			_, err := c.responses(ctx, NewSession("model", "low", "", nil))
			if err == nil || !strings.Contains(err.Error(), tt.contains) || (tt.want != nil && !errors.Is(err, tt.want)) {
				t.Fatalf("error = %v, want %q, %v", err, tt.contains, tt.want)
			}
			if !body.closed {
				t.Error("response body was not closed")
			}
		})
	}
}

func TestResponsesErrorClassification(t *testing.T) {
	for _, tt := range []struct {
		status int
		want   error
	}{
		{400, ErrInvalidRequest}, {404, ErrInvalidRequest}, {405, ErrInvalidRequest},
		{413, ErrInvalidRequest}, {415, ErrInvalidRequest}, {422, ErrInvalidRequest}, {431, ErrInvalidRequest},
		{401, ErrAuthentication}, {403, ErrPermissionDenied},
	} {
		t.Run(fmt.Sprint(tt.status), func(t *testing.T) {
			c := mockClient(func(*http.Request) (*http.Response, error) {
				return rawResponse(tt.status, "application/json", `{"error":{}}`), nil
			})
			if _, err := c.responses(context.Background(), NewSession("model", "low", "", nil)); !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
		})
	}
	for _, tt := range []struct {
		code string
		want error
	}{
		{"insufficient_quota", ErrQuotaExceeded},
		{"usage_limit_reached", ErrUsageLimitReached},
		{"invalid_api_key", ErrAuthentication},
		{"permission_denied", ErrPermissionDenied},
		{"context_length_exceeded", ErrContextLengthExceeded},
		{"invalid_prompt", ErrInvalidRequest},
	} {
		for _, source := range []string{"http code", "http type", "stream code", "stream error", "stream response error"} {
			t.Run(tt.code+"/"+source, func(t *testing.T) {
				c := mockClient(func(*http.Request) (*http.Response, error) {
					switch source {
					case "http code":
						return rawResponse(418, "application/json", `{"error":{"code":"`+tt.code+`"}}`), nil
					case "http type":
						return rawResponse(418, "application/json", `{"error":{"code":"unknown","type":"`+tt.code+`"}}`), nil
					case "stream code":
						return streamResponse(`{"type":"error","code":"` + tt.code + `"}`), nil
					case "stream error":
						return streamResponse(`{"type":"error","error":{"code":"` + tt.code + `"}}`), nil
					default:
						return streamResponse(`{"type":"response.failed","response":{"error":{"code":"` + tt.code + `"}}}`), nil
					}
				})
				if _, err := c.responses(context.Background(), NewSession("model", "low", "", nil)); !errors.Is(err, tt.want) {
					t.Fatalf("error = %v, want %v", err, tt.want)
				}
			})
		}
	}
}

func TestResponsesRetry(t *testing.T) {
	for _, tt := range []struct {
		name             string
		status           int
		headers          http.Header
		body             string
		minWait, maxWait time.Duration
	}{
		{"timeout", 408, nil, "", 375 * time.Millisecond, 500 * time.Millisecond},
		{"conflict", 409, nil, "", 375 * time.Millisecond, 500 * time.Millisecond},
		{"rate limit", 429, nil, "", 375 * time.Millisecond, 500 * time.Millisecond},
		{"server", 503, nil, "", 375 * time.Millisecond, 500 * time.Millisecond},
		{"retry header", 418, http.Header{"X-Should-Retry": {"true"}}, "", 375 * time.Millisecond, 500 * time.Millisecond},
		{"retry milliseconds", 418, http.Header{"Retry-After-Ms": {"bad", "-1", "NaN", "Inf", " 2000 ", "1000"}}, "", 2 * time.Second, 2 * time.Second},
		{"retry seconds", 418, http.Header{"Retry-After": {"bad", "-1", "NaN", "Inf", " 2 ", "1"}}, "", 2 * time.Second, 2 * time.Second},
		{"retry date", 418, http.Header{"Retry-After": {"Sat, 01 Jan 2000 00:00:02 GMT"}}, "", 2 * time.Second, 2 * time.Second},
		{"milliseconds precedence", 418, http.Header{"Retry-After-Ms": {"1000"}, "Retry-After": {"10"}}, "", time.Second, time.Second},
		{"server code", 200, nil, `{"type":"error","code":"server_error"}`, 375 * time.Millisecond, 500 * time.Millisecond},
		{"policy transient", 200, nil, `{"type":"error","error":{"code":"server_error","type":"policy_error"}}`, 375 * time.Millisecond, 500 * time.Millisecond},
		{"stream retry headers", 200, nil, `{"type":"error","error":{"headers":{"Retry-After-Ms":"2000","X-Should-Retry":"true"}}}`, 2 * time.Second, 2 * time.Second},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				attempts := 0
				var bodies []string
				var firstBody *observedBody
				c := mockClient(func(r *http.Request) (*http.Response, error) {
					attempts++
					b, err := io.ReadAll(r.Body)
					if err != nil {
						t.Fatal(err)
					}
					bodies = append(bodies, string(b))
					if attempts == 2 {
						return completedResponse([]map[string]any{}), nil
					}
					if attempts > 2 {
						return nil, errors.New("too many retries")
					}
					res := rawResponse(tt.status, "application/json", tt.body)
					if tt.status == 200 {
						res = streamResponse(tt.body)
					}
					for key, values := range tt.headers {
						res.Header[key] = values
					}
					firstBody = &observedBody{Reader: res.Body}
					res.Body = firstBody
					return res, nil
				})
				start := time.Now()
				got, err := c.responses(context.Background(), NewSession("model", "low", "", nil))
				if err != nil || got == nil || attempts != 2 {
					t.Fatalf("responses() = %#v, %v; attempts = %d", got, err, attempts)
				}
				if wait := time.Since(start); wait < tt.minWait || wait > tt.maxWait {
					t.Fatalf("wait = %v, want [%v, %v]", wait, tt.minWait, tt.maxWait)
				}
				if bodies[0] != bodies[1] {
					t.Fatal("retry changed request body")
				}
				if !firstBody.closed {
					t.Error("failed response was not closed")
				}
			})
		})
	}
}

func TestResponsesNoRetry(t *testing.T) {
	for _, tt := range []struct {
		name     string
		response func() *http.Response
	}{
		{"unknown HTTP failure", func() *http.Response { return rawResponse(418, "application/json", "not JSON") }},
		{"unknown stream failure", func() *http.Response { return streamResponse(`{"type":"error","code":"unknown"}`) }},
		{"explicit false", func() *http.Response {
			r := rawResponse(503, "application/json", "failure")
			r.Header["X-Should-Retry"] = []string{"true", "false"}
			return r
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			attempts := 0
			c := mockClient(func(*http.Request) (*http.Response, error) { attempts++; return tt.response(), nil })
			got, err := c.responses(context.Background(), NewSession("model", "low", "", nil))
			if got != nil || err == nil || attempts != 1 {
				t.Fatalf("responses() = %#v, %v; attempts = %d", got, err, attempts)
			}
		})
	}
}

func TestResponsesRetryCancellation(t *testing.T) {
	for _, header := range []string{"Retry-After-Ms", "Retry-After"} {
		for _, value := range []string{"1e9999", "1e20"} {
			t.Run(header+"/"+value, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					ctx, cancel := context.WithTimeout(context.Background(), time.Second)
					defer cancel()
					attempts := 0
					c := mockClient(func(*http.Request) (*http.Response, error) {
						attempts++
						r := rawResponse(429, "application/json", "busy")
						r.Header[header] = []string{value}
						return r, nil
					})
					start := time.Now()
					got, err := c.responses(ctx, NewSession("model", "low", "", nil))
					if got != nil || !errors.Is(err, context.DeadlineExceeded) || attempts != 1 || time.Since(start) != time.Second {
						t.Fatalf("responses() = %#v, %v; attempts = %d, elapsed = %v", got, err, attempts, time.Since(start))
					}
				})
			})
		}
	}
}

func TestResponsesRetryBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var attempts []time.Time
		c := mockClient(func(*http.Request) (*http.Response, error) {
			attempts = append(attempts, time.Now())
			if len(attempts) == 10 {
				return completedResponse([]map[string]any{}), nil
			}
			return rawResponse(503, "application/json", "busy"), nil
		})
		tokens := 0
		c.Token = func(context.Context) (string, error) { tokens++; return fmt.Sprint(tokens), nil }
		if _, err := c.responses(context.Background(), NewSession("model", "low", "", nil)); err != nil {
			t.Fatal(err)
		}
		if tokens != 10 {
			t.Fatalf("token calls = %d, want 10", tokens)
		}
		delay := 500 * time.Millisecond
		for i := 1; i < len(attempts); i++ {
			wait := attempts[i].Sub(attempts[i-1])
			if wait < delay*3/4 || wait > delay {
				t.Fatalf("retry %d wait = %v, delay = %v", i, wait, delay)
			}
			delay = min(delay*2, 30*time.Second)
		}
	})
}
