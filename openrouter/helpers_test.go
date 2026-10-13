package openrouter

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

const requestLimit = 20

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

// stubTransport replaces the transport of http.DefaultClient until the test
// ends, so tests that use it must not run in parallel.
func stubTransport(t *testing.T, rt roundTripFunc) {
	t.Helper()
	old := http.DefaultClient.Transport
	http.DefaultClient.Transport = rt
	t.Cleanup(func() { http.DefaultClient.Transport = old })
}

type capturedRequest struct {
	Method string
	URL    string
	Header http.Header
	Body   []byte
}

type fakeAPI struct {
	requests []capturedRequest
}

// stubAPI records each request sent through http.DefaultClient and answers it
// with reply. Once more than requestLimit requests arrive, the test fails and
// the request is rejected, so a non-terminating loop cannot hang the test.
func stubAPI(t *testing.T, reply func(call int, req capturedRequest) *http.Response) *fakeAPI {
	t.Helper()
	api := &fakeAPI{}
	stubTransport(t, func(r *http.Request) (*http.Response, error) {
		var body []byte
		if r.Body != nil {
			var err error
			if body, err = io.ReadAll(r.Body); err != nil {
				t.Errorf("reading request body: %v", err)
			}
		}
		req := capturedRequest{Method: r.Method, URL: r.URL.String(), Header: r.Header.Clone(), Body: body}
		api.requests = append(api.requests, req)
		if len(api.requests) > requestLimit {
			t.Errorf("more than %d requests were sent", requestLimit)
			return nil, errors.New("request limit exceeded")
		}
		return reply(len(api.requests)-1, req), nil
	})
	return api
}

func onlyRequest(t *testing.T, api *fakeAPI) capturedRequest {
	t.Helper()
	if len(api.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(api.requests))
	}
	return api.requests[0]
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func jsonValue(status int, v any) *http.Response {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return jsonResponse(status, string(b))
}

func chatResponse(message map[string]any, totalTokens int) *http.Response {
	return jsonValue(http.StatusOK, map[string]any{
		"choices": []any{map[string]any{"message": message}},
		"usage":   map[string]any{"total_tokens": totalTokens},
	})
}

func assistantText(text string) *http.Response {
	return chatResponse(map[string]any{"role": "assistant", "content": text}, 0)
}

func toolCall(id, name, arguments string) map[string]any {
	return map[string]any{
		"id":   id,
		"type": "function",
		"function": map[string]any{
			"name":      name,
			"arguments": arguments,
		},
	}
}

type chatRequest struct {
	Model           string  `json:"model"`
	ReasoningEffort *string `json:"reasoning_effort"`
	CacheControl    *struct {
		Type string `json:"type"`
		TTL  string `json:"ttl"`
	} `json:"cache_control"`
	MaxCompletionTokens int       `json:"max_completion_tokens"`
	Messages            []Message `json:"messages"`
	Tools               []struct {
		Type     string         `json:"type"`
		Function map[string]any `json:"function"`
	} `json:"tools"`
	ParallelToolCalls bool `json:"parallel_tool_calls"`
}

func parseChatRequest(t *testing.T, body []byte) chatRequest {
	t.Helper()
	var req chatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("decoding chat request: %v", err)
	}
	return req
}

type fakeTool struct {
	name   string
	result any
	calls  []map[string]any
}

func (f *fakeTool) Schema() map[string]any {
	return map[string]any{
		"name":        f.name,
		"description": "test tool",
		"parameters":  map[string]any{"type": "object"},
	}
}

func (f *fakeTool) Call(args map[string]any) any {
	f.calls = append(f.calls, args)
	return f.result
}

func checkError(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want %q", want)
	}
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func checkErrorPrefix(t *testing.T, err error, prefix string) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want prefix %q", prefix)
	}
	if !strings.HasPrefix(err.Error(), prefix) {
		t.Errorf("error = %q, want prefix %q", err.Error(), prefix)
	}
}
