package openrouter

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func affordError(afford int) string {
	return fmt.Sprintf(`{"error":{"message":"This request requires more credits, or fewer max_tokens. You requested up to 4096 tokens, but can only afford %d. To increase, visit https://openrouter.ai/settings/credits and add more credits"}}`, afford)
}

func sentMaxTokens(t *testing.T, api *fakeAPI) []int {
	t.Helper()
	var got []int
	for _, req := range api.requests {
		got = append(got, parseChatRequest(t, req.Body).MaxCompletionTokens)
	}
	return got
}

func TestPromptRetriesWithAffordableMaxTokens(t *testing.T) {
	api := stubAPI(t, func(call int, _ capturedRequest) *http.Response {
		if call == 0 {
			return jsonResponse(http.StatusPaymentRequired, affordError(3000))
		}
		return assistantText("ok")
	})
	s := &Session{Model: "m", MaxCompletionTokens: 4096}
	c := &Client{Token: "test-token"}

	got, err := c.Prompt(s, "hi")
	if err != nil {
		t.Fatalf("Prompt returned error: %v", err)
	}
	if got != "ok" {
		t.Errorf("Prompt = %q, want %q", got, "ok")
	}
	sent := sentMaxTokens(t, api)
	if want := []int{4096, 3000}; !reflect.DeepEqual(sent, want) {
		t.Errorf("max_completion_tokens sent = %v, want %v", sent, want)
	}
	if s.MaxCompletionTokens != 3000 {
		t.Errorf("session MaxCompletionTokens = %d, want 3000", s.MaxCompletionTokens)
	}
}

func TestPromptDoesNotRetryWhenClampCannotHelp(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"affordable equals sent", affordError(4096)},
		{"affordable exceeds sent", affordError(5000)},
		{"affordable is zero", affordError(0)},
		{"no affordable amount", `{"error":{"message":"Insufficient credits. Add more at https://openrouter.ai/settings/credits"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := stubAPI(t, func(int, capturedRequest) *http.Response {
				return jsonResponse(http.StatusPaymentRequired, tt.body)
			})
			s := &Session{Model: "m", MaxCompletionTokens: 4096}
			c := &Client{Token: "test-token"}

			_, err := c.Prompt(s, "hi")
			checkErrorPrefix(t, err, "sending prompt: 402 Payment Required: ")
			if want := []int{4096}; !reflect.DeepEqual(sentMaxTokens(t, api), want) {
				t.Errorf("max_completion_tokens sent = %v, want %v", sentMaxTokens(t, api), want)
			}
			if s.MaxCompletionTokens != 4096 {
				t.Errorf("session MaxCompletionTokens = %d, want 4096", s.MaxCompletionTokens)
			}
		})
	}
}

func TestPromptStopsRetryingAtCap(t *testing.T) {
	api := stubAPI(t, func(call int, _ capturedRequest) *http.Response {
		return jsonResponse(http.StatusPaymentRequired, affordError(4000-100*call))
	})
	s := &Session{Model: "m", MaxCompletionTokens: 4096}
	c := &Client{Token: "test-token"}

	_, err := c.Prompt(s, "hi")
	if err == nil {
		t.Fatal("Prompt returned nil error")
	}
	want := []int{4096}
	for i := 0; i < maxAffordRetries; i++ {
		want = append(want, 4000-100*i)
	}
	if got := sentMaxTokens(t, api); !reflect.DeepEqual(got, want) {
		t.Errorf("max_completion_tokens sent = %v, want %v", got, want)
	}
	if last := want[len(want)-1]; s.MaxCompletionTokens != last {
		t.Errorf("session MaxCompletionTokens = %d, want %d", s.MaxCompletionTokens, last)
	}
}

func TestPromptSendsRequest(t *testing.T) {
	api := stubAPI(t, func(int, capturedRequest) *http.Response {
		return assistantText("ok")
	})
	tool := &fakeTool{name: "echo"}
	s := &Session{
		Model:               "openai/gpt-4o",
		Effort:              "high",
		Cache:               true,
		MaxCompletionTokens: 1000,
		Tools:               []Tool{tool},
		Messages:            []Message{{Role: "system", Content: "be nice"}},
	}
	c := &Client{Token: "test-token"}
	if _, err := c.Prompt(s, "hi"); err != nil {
		t.Fatalf("Prompt returned error: %v", err)
	}

	req := onlyRequest(t, api)
	if req.Method != http.MethodPost {
		t.Errorf("method = %q, want POST", req.Method)
	}
	if want := "https://openrouter.ai/api/v1/chat/completions"; req.URL != want {
		t.Errorf("URL = %q, want %q", req.URL, want)
	}
	if got, want := req.Header.Get("Authorization"), "Bearer test-token"; got != want {
		t.Errorf("Authorization = %q, want %q", got, want)
	}
	if got, want := req.Header.Get("Content-Type"), "application/json"; got != want {
		t.Errorf("Content-Type = %q, want %q", got, want)
	}

	sent := parseChatRequest(t, req.Body)
	if sent.Model != "openai/gpt-4o" {
		t.Errorf("model = %q, want %q", sent.Model, "openai/gpt-4o")
	}
	if sent.ReasoningEffort == nil || *sent.ReasoningEffort != "high" {
		t.Errorf("reasoning_effort = %v, want high", sent.ReasoningEffort)
	}
	if sent.CacheControl == nil || sent.CacheControl.Type != "ephemeral" || sent.CacheControl.TTL != "5m" {
		t.Errorf("cache_control = %+v, want ephemeral with ttl 5m", sent.CacheControl)
	}
	if sent.MaxCompletionTokens != 1000 {
		t.Errorf("max_completion_tokens = %d, want 1000", sent.MaxCompletionTokens)
	}
	if len(sent.Messages) != 2 || sent.Messages[0].Role != "system" || sent.Messages[1].Role != "user" || sent.Messages[1].Content != "hi" {
		t.Errorf("messages = %+v, want system message then user message hi", sent.Messages)
	}
	if len(sent.Tools) != 1 {
		t.Fatalf("tools = %d, want 1", len(sent.Tools))
	}
	if sent.Tools[0].Type != "function" {
		t.Errorf("tool type = %q, want function", sent.Tools[0].Type)
	}
	if !reflect.DeepEqual(sent.Tools[0].Function, tool.Schema()) {
		t.Errorf("tool function = %v, want %v", sent.Tools[0].Function, tool.Schema())
	}
	if !sent.ParallelToolCalls {
		t.Error("parallel_tool_calls = false, want true")
	}
}

func TestPromptOmitsOptionalFields(t *testing.T) {
	api := stubAPI(t, func(int, capturedRequest) *http.Response {
		return assistantText("ok")
	})
	s := &Session{Model: "m", MaxCompletionTokens: 10}
	c := &Client{Token: "test-token"}
	if _, err := c.Prompt(s, "hi"); err != nil {
		t.Fatalf("Prompt returned error: %v", err)
	}
	sent := parseChatRequest(t, onlyRequest(t, api).Body)
	if sent.ReasoningEffort != nil {
		t.Errorf("reasoning_effort = %q, want omitted", *sent.ReasoningEffort)
	}
	if sent.CacheControl != nil {
		t.Errorf("cache_control = %+v, want omitted", sent.CacheControl)
	}
}

func TestPromptReturnsText(t *testing.T) {
	tests := []struct {
		name    string
		content any
		want    string
	}{
		{"string content", "hello", "hello"},
		{"object with text", map[string]any{"text": "hi"}, "hi"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubAPI(t, func(int, capturedRequest) *http.Response {
				return chatResponse(map[string]any{"role": "assistant", "content": tt.content}, 0)
			})
			s := &Session{Model: "m", Messages: []Message{{Role: "system", Content: "sys"}}}
			c := &Client{Token: "test-token"}

			got, err := c.Prompt(s, "hi")
			if err != nil {
				t.Fatalf("Prompt returned error: %v", err)
			}
			if got != tt.want {
				t.Errorf("Prompt = %q, want %q", got, tt.want)
			}
			if len(s.Messages) != 3 {
				t.Fatalf("session messages = %d, want 3", len(s.Messages))
			}
			if last := s.Messages[2]; last.Role != "assistant" || !reflect.DeepEqual(last.Content, tt.content) {
				t.Errorf("assistant message = %+v, want content %v", last, tt.content)
			}
		})
	}
}

func TestPromptNoChoices(t *testing.T) {
	stubAPI(t, func(int, capturedRequest) *http.Response {
		return jsonResponse(http.StatusOK, `{"choices":[]}`)
	})
	c := &Client{Token: "test-token"}
	_, err := c.Prompt(&Session{Model: "m"}, "hi")
	checkError(t, err, "no choices")
}

func TestPromptAPIError(t *testing.T) {
	stubAPI(t, func(int, capturedRequest) *http.Response {
		return jsonResponse(http.StatusInternalServerError, `{"error":{"message":"boom"}}`)
	})
	c := &Client{Token: "test-token"}
	_, err := c.Prompt(&Session{Model: "m"}, "hi")
	checkError(t, err, "sending prompt: 500 Internal Server Error: boom")
}

func TestPromptRunsToolCall(t *testing.T) {
	tool := &fakeTool{name: "echo", result: "echoed"}
	api := stubAPI(t, func(call int, _ capturedRequest) *http.Response {
		if call == 0 {
			return chatResponse(map[string]any{
				"role":       "assistant",
				"content":    nil,
				"tool_calls": []any{toolCall("call_1", "echo", `{"text":"hi","n":3}`)},
			}, 0)
		}
		return assistantText("done")
	})
	s := &Session{Model: "m", Tools: []Tool{tool}, Limit: 1000}
	c := &Client{Token: "test-token"}

	got, err := c.Prompt(s, "go")
	if err != nil {
		t.Fatalf("Prompt returned error: %v", err)
	}
	if got != "done" {
		t.Errorf("Prompt = %q, want %q", got, "done")
	}
	if len(tool.calls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(tool.calls))
	}
	if tool.calls[0]["text"] != "hi" {
		t.Errorf("text argument = %v, want %q", tool.calls[0]["text"], "hi")
	}
	if tool.calls[0]["n"] != json.Number("3") {
		t.Errorf("n argument = %#v, want json.Number(\"3\")", tool.calls[0]["n"])
	}

	if len(api.requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(api.requests))
	}
	msgs := parseChatRequest(t, api.requests[1].Body).Messages
	if len(msgs) != 3 {
		t.Fatalf("messages in second request = %d, want 3", len(msgs))
	}
	if calls := msgs[1].ToolCalls; len(calls) != 1 || calls[0].ID != "call_1" || calls[0].Tool.Name != "echo" {
		t.Errorf("assistant tool calls = %+v, want one call_1 to echo", calls)
	}
	if msgs[2].Role != "tool" || msgs[2].ToolCallID != "call_1" || msgs[2].Content != "echoed" {
		t.Errorf("tool message = %+v, want result for call_1", msgs[2])
	}
	if len(s.Messages) != 4 {
		t.Errorf("session messages = %d, want 4", len(s.Messages))
	}
}

func TestPromptRunsEveryToolCall(t *testing.T) {
	tool := &fakeTool{name: "echo", result: "r"}
	api := stubAPI(t, func(call int, _ capturedRequest) *http.Response {
		if call == 0 {
			return chatResponse(map[string]any{
				"role":    "assistant",
				"content": nil,
				"tool_calls": []any{
					toolCall("call_a", "echo", `{"text":"a"}`),
					toolCall("call_b", "echo", `{"text":"b"}`),
				},
			}, 0)
		}
		return assistantText("done")
	})
	s := &Session{Model: "m", Tools: []Tool{tool}, Limit: 1000}
	c := &Client{Token: "test-token"}

	got, err := c.Prompt(s, "go")
	if err != nil {
		t.Fatalf("Prompt returned error: %v", err)
	}
	if got != "done" {
		t.Errorf("Prompt = %q, want %q", got, "done")
	}
	if len(tool.calls) != 2 || tool.calls[0]["text"] != "a" || tool.calls[1]["text"] != "b" {
		t.Errorf("tool calls = %v, want args a then b", tool.calls)
	}
	if len(api.requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(api.requests))
	}
	msgs := parseChatRequest(t, api.requests[1].Body).Messages
	if len(msgs) != 4 {
		t.Fatalf("messages in second request = %d, want 4", len(msgs))
	}
	if msgs[2].ToolCallID != "call_a" || msgs[3].ToolCallID != "call_b" {
		t.Errorf("tool call IDs = %q, %q, want call_a, call_b", msgs[2].ToolCallID, msgs[3].ToolCallID)
	}
	for _, m := range msgs[2:] {
		if m.Content != "r" {
			t.Errorf("tool message content = %v, want r", m.Content)
		}
	}
}

func TestPromptReportsToolCallErrors(t *testing.T) {
	tests := []struct {
		name      string
		toolName  string
		arguments string
		want      string
	}{
		{"unknown tool", "missing", "{}", `{"error": "tool not found"}`},
		{"malformed arguments", "echo", `{"text":`, `{"error": "invalid arguments: JSON decoding arguments"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := &fakeTool{name: "echo", result: "r"}
			api := stubAPI(t, func(call int, _ capturedRequest) *http.Response {
				if call == 0 {
					return chatResponse(map[string]any{
						"role":       "assistant",
						"content":    nil,
						"tool_calls": []any{toolCall("call_1", tt.toolName, tt.arguments)},
					}, 0)
				}
				return assistantText("done")
			})
			s := &Session{Model: "m", Tools: []Tool{tool}, Limit: 1000}
			c := &Client{Token: "test-token"}

			got, err := c.Prompt(s, "go")
			if err != nil {
				t.Fatalf("Prompt returned error: %v", err)
			}
			if got != "done" {
				t.Errorf("Prompt = %q, want %q", got, "done")
			}
			if len(tool.calls) != 0 {
				t.Errorf("tool called %d times, want 0", len(tool.calls))
			}
			if len(api.requests) != 2 {
				t.Fatalf("requests = %d, want 2", len(api.requests))
			}
			msgs := parseChatRequest(t, api.requests[1].Body).Messages
			if len(msgs) != 3 {
				t.Fatalf("messages in second request = %d, want 3", len(msgs))
			}
			if msgs[2].Role != "tool" || msgs[2].ToolCallID != "call_1" || msgs[2].Content != tt.want {
				t.Errorf("tool message = %+v, want content %q for call_1", msgs[2], tt.want)
			}
		})
	}
}

func TestPromptDeveloperNoticeNearLimit(t *testing.T) {
	tests := []struct {
		name   string
		total  int
		notice bool
	}{
		{"usage at limit", 100, true},
		{"usage above limit", 150, true},
		{"usage below limit", 99, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := &fakeTool{name: "echo", result: "r"}
			api := stubAPI(t, func(call int, _ capturedRequest) *http.Response {
				if call == 0 {
					return chatResponse(map[string]any{
						"role":       "assistant",
						"content":    nil,
						"tool_calls": []any{toolCall("call_1", "echo", "{}")},
					}, tt.total)
				}
				return assistantText("done")
			})
			s := &Session{Model: "m", Tools: []Tool{tool}, Limit: 100}
			c := &Client{Token: "test-token"}

			if _, err := c.Prompt(s, "go"); err != nil {
				t.Fatalf("Prompt returned error: %v", err)
			}
			if len(api.requests) != 2 {
				t.Fatalf("requests = %d, want 2", len(api.requests))
			}
			msgs := parseChatRequest(t, api.requests[1].Body).Messages
			if len(msgs) == 0 {
				t.Fatal("second request has no messages")
			}
			last := msgs[len(msgs)-1]
			if !tt.notice {
				if last.Role != "tool" {
					t.Errorf("last message role = %q, want tool", last.Role)
				}
				return
			}
			content, _ := last.Content.(string)
			if last.Role != "developer" || !strings.HasPrefix(content, "Context is nearly full.") {
				t.Errorf("last message = %+v, want developer notice", last)
			}
		})
	}
}

func TestAddMessageLogsJSON(t *testing.T) {
	var buf bytes.Buffer
	c := &Client{Log: log.New(&buf, "", 0)}
	s := &Session{}
	c.addMessage(s, &Message{Role: "user", Content: "hi"})
	if len(s.Messages) != 1 || s.Messages[0].Content != "hi" {
		t.Errorf("Messages = %+v, want one user message", s.Messages)
	}
	if got, want := buf.String(), `{"role":"user","content":"hi"}`+"\n"; got != want {
		t.Errorf("log output = %q, want %q", got, want)
	}
}

func TestAddMessageWithoutLogger(t *testing.T) {
	c := &Client{}
	s := &Session{}
	c.addMessage(s, &Message{Role: "user", Content: "hi"})
	if len(s.Messages) != 1 {
		t.Errorf("Messages = %d, want 1", len(s.Messages))
	}
}

func TestMessageJSON(t *testing.T) {
	tests := []struct {
		name string
		msg  Message
		want string
	}{
		{"user message", Message{Role: "user", Content: "hi"}, `{"role":"user","content":"hi"}`},
		{"tool result", Message{Role: "tool", Content: "r", ToolCallID: "call_1"}, `{"role":"tool","content":"r","tool_call_id":"call_1"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := json.Marshal(tt.msg)
			if err != nil {
				t.Fatalf("marshaling message: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("json = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestPromptReturnsEmptyTextForOtherContent(t *testing.T) {
	tests := []struct {
		name    string
		content any
	}{
		{"nil content", nil},
		{"object without text", map[string]any{"image": "x"}},
		{"object with non-string text", map[string]any{"text": 5}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubAPI(t, func(int, capturedRequest) *http.Response {
				return chatResponse(map[string]any{"role": "assistant", "content": tt.content}, 0)
			})
			c := &Client{Token: "test-token"}
			got, err := c.Prompt(&Session{Model: "m"}, "hi")
			if err != nil {
				t.Fatalf("Prompt returned error: %v", err)
			}
			if got != "" {
				t.Errorf("Prompt = %q, want empty string", got)
			}
		})
	}
}

func TestPromptDoesNotRetryWhenAffordableOverflows(t *testing.T) {
	api := stubAPI(t, func(int, capturedRequest) *http.Response {
		return jsonResponse(http.StatusPaymentRequired, `{"error":{"message":"can only afford 99999999999999999999999999"}}`)
	})
	s := &Session{Model: "m", MaxCompletionTokens: 4096}
	c := &Client{Token: "test-token"}

	_, err := c.Prompt(s, "hi")
	checkErrorPrefix(t, err, "sending prompt: 402 Payment Required: ")
	if want := []int{4096}; !reflect.DeepEqual(sentMaxTokens(t, api), want) {
		t.Errorf("max_completion_tokens sent = %v, want %v", sentMaxTokens(t, api), want)
	}
}
