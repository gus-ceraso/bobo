package provider

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"log"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestConstructors(t *testing.T) {
	token := func(context.Context) (string, error) { return "secret", nil }
	c := NewClient(token)
	if got, err := c.Token(context.Background()); got != "secret" || err != nil {
		t.Fatalf("Token() = %q, %v", got, err)
	}
	if c.BaseURL != "https://api.openai.com/v1" || c.HTTPClient != http.DefaultClient || c.Logger != nil {
		t.Fatalf("unexpected client defaults: %#v", c)
	}
	functions := []Function{testFunction{}}
	s := NewSession("model", "high", "instructions", functions)
	functions[0] = nil
	if s.ID == "" || s.ID == NewSession("", "", "", nil).ID || s.Model != "model" || s.Effort != "high" || s.Instructions != "instructions" || s.CompactThreshold != 128000 || s.ServiceTier != "auto" || s.Events != nil || s.Functions[0] == nil {
		t.Fatalf("unexpected session defaults or un-cloned functions: %#v", s)
	}
}

func TestPromptFinalMessage(t *testing.T) {
	for _, tt := range []struct {
		name   string
		output []map[string]any
		want   string
	}{
		{"empty", []map[string]any{}, ""},
		{"commentary only", []map[string]any{{"type": "message", "phase": "commentary", "content": "ignore"}}, ""},
		{"last non commentary", []map[string]any{
			{"type": "reasoning", "summary": []any{}},
			{"type": "message", "content": "old"},
			{"type": "message", "phase": "final_answer", "content": []any{
				map[string]any{"type": "output_text", "text": "first"},
				map[string]any{"type": "input_image", "image_url": "data:image/png;base64,AAEC"},
				map[string]any{"type": "refusal", "refusal": "second"},
			}},
			{"type": "message", "phase": "commentary", "content": "ignore"},
		}, "first\n\nsecond"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := mockClient(func(*http.Request) (*http.Response, error) { return completedResponse(tt.output), nil })
			s := NewSession("model", "low", "", nil)
			got, err := c.Prompt(context.Background(), s, "hello")
			if err != nil || got != tt.want || len(s.Events) != 1+len(tt.output) {
				t.Fatalf("Prompt() = %q, %v; events = %#v", got, err, s.Events)
			}
		})
	}
}

func TestPromptError(t *testing.T) {
	want := errors.New("token unavailable")
	c := NewClient(func(context.Context) (string, error) { return "", want })
	s := NewSession("model", "low", "", nil)
	if got, err := c.Prompt(context.Background(), s, "hello"); got != "" || !errors.Is(err, want) {
		t.Fatalf("Prompt() = %q, %v", got, err)
	}
	if len(s.Events) != 1 || s.Events[0]["content"] != "hello" {
		t.Fatalf("prompt not preserved after error: %#v", s.Events)
	}
}

func TestPromptUTF8Validation(t *testing.T) {
	for _, tt := range []struct {
		name   string
		prompt string
		valid  bool
	}{
		{"empty", "", true},
		{"ASCII", "hello", true},
		{"Unicode", "hello 世界🙂", true},
		{"null byte", "hello\x00world", true},
		{"replacement character", "\uFFFD", true},
		{"invalid byte", "hello\xffworld", false},
		{"lone continuation", "\x80", false},
		{"overlong encoding", "\xc0\xaf", false},
		{"truncated rune", "\xe2\x82", false},
		{"surrogate", "\xed\xa0\x80", false},
		{"out of range", "\xf4\x90\x80\x80", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, withHistory := range []bool{false, true} {
				t.Run(strconv.FormatBool(withHistory), func(t *testing.T) {
					var logs bytes.Buffer
					tokens, requests := 0, 0
					c := mockClient(func(*http.Request) (*http.Response, error) {
						requests++
						return completedResponse([]map[string]any{{"type": "message", "role": "assistant", "content": "done"}}), nil
					})
					c.Token = func(context.Context) (string, error) {
						tokens++
						return "token", nil
					}
					c.Logger = log.New(&logs, "", 0)
					s := NewSession("model", "low", "", nil)
					var history []map[string]any
					if withHistory {
						history = []map[string]any{{"type": "message", "role": "user", "content": "previous"}}
						s.Events = append(s.Events, history...)
					}
					got, err := c.Prompt(context.Background(), s, tt.prompt)
					if tt.valid {
						if err != nil || got != "done" {
							t.Fatalf("Prompt() = %q, %v", got, err)
						}
					} else {
						if got != "" || err == nil || !strings.Contains(err.Error(), "invalid utf-8") {
							t.Fatalf("Prompt() = %q, %v; want invalid UTF-8 error", got, err)
						}
						if tokens != 0 || requests != 0 || logs.Len() != 0 || !reflect.DeepEqual(s.Events, history) {
							t.Fatalf("invalid prompt had side effects: tokens=%d requests=%d logs=%q events=%#v", tokens, requests, logs.String(), s.Events)
						}
						// A rejected prompt must not poison later turns in the same session.
						got, err = c.Prompt(context.Background(), s, "valid 世界🙂")
						if err != nil || got != "done" {
							t.Fatalf("Prompt() after rejection = %q, %v", got, err)
						}
					}
					if tokens != 1 || requests != 1 || len(s.Events) != len(history)+2 {
						t.Fatalf("tokens=%d requests=%d events=%#v", tokens, requests, s.Events)
					}
					wantPrompt := tt.prompt
					if !tt.valid {
						wantPrompt = "valid 世界🙂"
					}
					if s.Events[len(history)]["content"] != wantPrompt {
						t.Fatalf("prompt changed: %#v", s.Events[len(history)])
					}
				})
			}
		})
	}
}

func TestAppendEventLogging(t *testing.T) {
	var buf bytes.Buffer
	c := NewClient(nil)
	c.Logger = log.New(&buf, "", 0)
	s := NewSession("model", "low", "", nil)
	s.ID = "test-session"
	e := map[string]any{"type": "message", "content": "hello\nworld"}
	c.appendEvent(s, e)
	logged := buf.String()
	prefix := "session=test-session model=\"model\" service_tier=\"auto\" event="
	if !strings.HasPrefix(logged, prefix) || strings.Count(logged, "\n") != 1 {
		t.Fatalf("unexpected log format: %q", logged)
	}
	encoded, err := strconv.Unquote(strings.TrimSuffix(strings.TrimPrefix(logged, prefix), "\n"))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(encoded), &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, e) {
		t.Fatalf("logged event = %#v, want %#v", got, e)
	}
	c.appendEvent(s, map[string]any{"bad": make(chan int)})
	if len(s.Events) != 2 || buf.String() != logged {
		t.Fatal("unserializable event must be stored without logging")
	}
}

type contextFunction struct{ call func(context.Context) []Part }

func (contextFunction) Schema() map[string]any {
	return map[string]any{"type": "function", "name": "wait"}
}
func (f contextFunction) Call(ctx context.Context, _ map[string]any) []Part { return f.call(ctx) }

func TestPromptFunctionTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var callContext context.Context
		f := contextFunction{call: func(ctx context.Context) []Part {
			callContext = ctx
			<-ctx.Done()
			return []Part{{ContentType: "text/plain", Content: []byte(ctx.Err().Error())}}
		}}
		attempts := 0
		c := mockClient(func(*http.Request) (*http.Response, error) {
			attempts++
			if attempts == 1 {
				return completedResponse([]map[string]any{{"type": "function_call", "call_id": "one", "name": "wait", "arguments": "{}"}}), nil
			}
			return completedResponse([]map[string]any{}), nil
		})
		start := time.Now()
		if _, err := c.Prompt(context.Background(), NewSession("model", "low", "", []Function{f}), "hello"); err != nil {
			t.Fatal(err)
		}
		if callContext == nil || !errors.Is(callContext.Err(), context.DeadlineExceeded) || time.Since(start) != 5*time.Minute {
			t.Fatalf("function timeout context = %v, elapsed = %v", callContext, time.Since(start))
		}
	})
}

func TestPromptFunctionsRunConcurrently(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		firstStarted := make(chan struct{})
		secondFinished := make(chan struct{})
		f := callbackFunction{call: func(_ context.Context, args map[string]any) []Part {
			text := args["text"].(string)
			if text == "first" {
				close(firstStarted)
				<-secondFinished
			} else {
				<-firstStarted
				defer close(secondFinished)
			}
			return []Part{{ContentType: "text/plain", Content: []byte(text)}}
		}}
		requests := 0
		c := mockClient(func(r *http.Request) (*http.Response, error) {
			requests++
			if requests == 1 {
				return completedResponse([]map[string]any{
					{"type": "function_call", "call_id": "one", "name": "inspect", "arguments": `{"text":"first"}`},
					{"type": "function_call", "call_id": "two", "name": "inspect", "arguments": `{"text":"second"}`},
				}), nil
			}
			if requests > 2 {
				return nil, errors.New("unexpected extra request")
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			var req struct {
				Input []map[string]any `json:"input"`
			}
			if err := json.Unmarshal(body, &req); err != nil {
				t.Fatal(err)
			}
			if len(req.Input) != 5 {
				t.Fatalf("unexpected input: %#v", req.Input)
			}
			for i, want := range []struct{ id, text string }{{"one", "first"}, {"two", "second"}} {
				output := req.Input[3+i]
				parts := decodeParts(output["output"])
				if output["type"] != "function_call_output" || output["call_id"] != want.id || len(parts) != 1 || string(parts[0].Content) != want.text {
					t.Fatalf("output order changed when functions completed in reverse: %#v", req.Input[3:])
				}
			}
			return completedResponse([]map[string]any{{"type": "message", "content": "done"}}), nil
		})
		got, err := c.Prompt(context.Background(), NewSession("model", "low", "", []Function{f}), "hello")
		if err != nil || got != "done" || requests != 2 {
			t.Fatalf("Prompt() = %q, %v; requests = %d", got, err, requests)
		}
	})
}

func TestPromptFunctionCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var callContext context.Context
	f := callbackFunction{call: func(ctx context.Context, _ map[string]any) []Part {
		callContext = ctx
		cancel()
		<-ctx.Done()
		return []Part{{ContentType: "text/plain", Content: []byte(ctx.Err().Error())}}
	}}
	requests := 0
	c := mockClient(func(*http.Request) (*http.Response, error) {
		requests++
		return completedResponse([]map[string]any{{"type": "function_call", "call_id": "one", "name": "inspect", "arguments": `{}`}}), nil
	})
	s := NewSession("model", "low", "", []Function{f})
	got, err := c.Prompt(ctx, s, "hello")
	if got != "" || !errors.Is(err, context.Canceled) || requests != 1 {
		t.Fatalf("Prompt() = %q, %v; requests = %d", got, err, requests)
	}
	if callContext == nil || !errors.Is(callContext.Err(), context.Canceled) {
		t.Fatalf("function context was not canceled: %v", callContext)
	}
	if len(s.Events) != 3 || s.Events[2]["type"] != "function_call_output" || s.Events[2]["call_id"] != "one" {
		t.Fatalf("function output missing after cancellation: %#v", s.Events)
	}
	parts := decodeParts(s.Events[2]["output"])
	if len(parts) != 1 || string(parts[0].Content) != context.Canceled.Error() {
		t.Fatalf("unexpected canceled function result: %#v", s.Events[2])
	}
}
