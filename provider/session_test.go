package provider

import (
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

type testTransport func(*http.Request) (*http.Response, error)

func (f testTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func completedResponse(output []map[string]any) *http.Response {
	body, err := json.Marshal(map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"id": "resp_test", "status": "completed", "output": output,
		},
	})
	if err != nil {
		panic(err)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader("data: " + string(body) + "\n\n")),
	}
}

type testFunction struct{}

func (testFunction) Schema() map[string]any {
	return map[string]any{"type": "function", "name": "echo"}
}

func (testFunction) Call(_ context.Context, args map[string]any) []Part {
	return []Part{{ContentType: "text/plain", Content: []byte(args["text"].(string))}}
}

func TestPromptFunctionRoundTrip(t *testing.T) {
	calls := []map[string]any{
		{"type": "function_call", "call_id": "one", "name": "echo", "arguments": `{"text":"first"}`},
		{"type": "function_call", "call_id": "two", "name": "echo", "arguments": `{"text":"second"}`},
		{"type": "function_call", "call_id": "three", "name": "echo", "arguments": `{`},
		{"type": "function_call", "call_id": "four", "name": "missing", "arguments": `{}`},
	}
	final := []map[string]any{{
		"type": "message", "role": "assistant",
		"content": []any{map[string]any{"type": "output_text", "text": "done"}},
	}}
	c := NewClient(func(context.Context) (string, error) { return "token", nil })
	requests := 0
	c.HTTPClient = &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
		requests++
		if requests > 2 {
			return nil, errors.New("unexpected extra request")
		}
		var req struct {
			Input []map[string]any `json:"input"`
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatal(err)
		}
		if requests == 1 {
			if len(req.Input) != 1 || req.Input[0]["content"] != "hello" {
				t.Fatalf("unexpected initial input: %#v", req.Input)
			}
			return completedResponse(calls), nil
		}
		if len(req.Input) != 9 || !reflect.DeepEqual(req.Input[1:5], calls) {
			t.Fatalf("function calls missing from input: %#v", req.Input)
		}
		for i, call := range calls {
			output := req.Input[5+i]
			if output["type"] != "function_call_output" || output["call_id"] != call["call_id"] {
				t.Fatalf("output %d does not match call: %#v", i, output)
			}
			if i < 2 {
				parts := decodeParts(output["output"])
				want := []string{"first", "second"}[i]
				if len(parts) != 1 || string(parts[0].Content) != want {
					t.Fatalf("output %d: %#v", i, output)
				}
			} else if text, ok := output["output"].(string); !ok || text == "" {
				t.Fatalf("missing function error: %#v", output)
			}
		}
		return completedResponse(final), nil
	})}
	s := NewSession("model", "low", "", []Function{testFunction{}})
	got, err := c.Prompt(context.Background(), s, "hello")
	if err != nil || got != "done" {
		t.Fatalf("Prompt() = %q, %v", got, err)
	}
	if requests != 2 || len(s.Events) != 10 {
		t.Fatalf("requests = %d, events = %d", requests, len(s.Events))
	}
}

func TestResponsesCompaction(t *testing.T) {
	s := NewSession("model", "low", "", nil)
	s.Events = []map[string]any{
		{"type": "message", "role": "user", "content": "old"},
		{"type": "compaction", "encrypted_content": "summary"},
		{"type": "message", "role": "user", "content": "new"},
	}
	output := []map[string]any{{"type": "message", "role": "assistant", "content": "answer"}}
	c := NewClient(func(context.Context) (string, error) { return "token", nil })
	c.HTTPClient = &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
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
		if !reflect.DeepEqual(req.Input, s.Events[1:]) {
			t.Fatalf("unexpected compacted input: %#v", req.Input)
		}
		return completedResponse(output), nil
	})}
	got, err := c.responses(context.Background(), s)
	if err != nil || !reflect.DeepEqual(got, output) {
		t.Fatalf("responses() = %#v, %v", got, err)
	}
	if len(s.Events) != 3 {
		t.Fatal("responses mutated session history")
	}
}
