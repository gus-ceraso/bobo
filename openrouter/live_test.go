//go:build live

package openrouter

import (
	"os"
	"strings"
	"testing"
)

// The tests in this file call the real OpenRouter API. They are only compiled
// with the "live" build tag and are skipped unless OPENROUTER_API_KEY is set.
// They use the same default model as the bobo command and keep prompts tiny
// so that a run costs very little. Run them with: make live-check

// liveModel is the default model used by the bobo command.
const liveModel = "~anthropic/claude-haiku-latest"

func liveKey(t *testing.T) string {
	t.Helper()
	key := os.Getenv("OPENROUTER_API_KEY")
	if key == "" {
		t.Skip("OPENROUTER_API_KEY is not set; skipping live test")
	}
	return key
}

func liveClient(t *testing.T) *Client {
	t.Helper()
	return &Client{Token: liveKey(t)}
}

func liveSession(t *testing.T, c *Client, tools []Tool, cache bool) *Session {
	t.Helper()
	s, err := c.NewSession(liveModel, "low", "You are a terse test assistant. Follow the user's instructions exactly.", cache, tools)
	if err != nil {
		t.Fatalf("NewSession returned error: %v", err)
	}
	return s
}

func TestLiveNewSession(t *testing.T) {
	c := liveClient(t)
	s := liveSession(t, c, nil, false)
	if s.Model != liveModel {
		t.Errorf("Model = %q, want %q", s.Model, liveModel)
	}
	if s.Limit <= 0 {
		t.Errorf("Limit = %d, want > 0", s.Limit)
	}
	if s.MaxCompletionTokens <= 0 {
		t.Errorf("MaxCompletionTokens = %d, want > 0", s.MaxCompletionTokens)
	}
	if len(s.Messages) != 1 || s.Messages[0].Role != "system" {
		t.Errorf("Messages = %+v, want one system message", s.Messages)
	}
}

func TestLiveNewSessionRejectsUnknownModel(t *testing.T) {
	c := liveClient(t)
	_, err := c.NewSession("anthropic/no-such-model-xyz", "", "sys", false, nil)
	checkErrorPrefix(t, err, "fetching model information: 404 ")
}

func TestLiveNewSessionRejectsUnsupportedEffort(t *testing.T) {
	c := liveClient(t)
	_, err := c.NewSession(liveModel, "not-a-real-effort", "sys", false, nil)
	checkError(t, err, "invalid arguments: effort not supported by model")
}

func TestLivePromptReturnsText(t *testing.T) {
	c := liveClient(t)
	s := liveSession(t, c, nil, false)
	got, err := c.Prompt(s, "Reply with exactly one word: pong")
	if err != nil {
		t.Fatalf("Prompt returned error: %v", err)
	}
	if !strings.Contains(strings.ToLower(got), "pong") {
		t.Errorf("Prompt = %q, want it to contain pong", got)
	}
	if len(s.Messages) != 3 || s.Messages[2].Role != "assistant" {
		t.Errorf("session has %d messages, want system, user, assistant", len(s.Messages))
	}
}

func TestLivePromptKeepsContext(t *testing.T) {
	c := liveClient(t)
	s := liveSession(t, c, nil, false)
	if _, err := c.Prompt(s, "Remember this word for later: walrus. Reply only with OK."); err != nil {
		t.Fatalf("first Prompt returned error: %v", err)
	}
	got, err := c.Prompt(s, "What word did I ask you to remember? Reply with only that word.")
	if err != nil {
		t.Fatalf("second Prompt returned error: %v", err)
	}
	if !strings.Contains(strings.ToLower(got), "walrus") {
		t.Errorf("Prompt = %q, want it to contain walrus", got)
	}
}

func TestLivePromptWithCacheControl(t *testing.T) {
	c := liveClient(t)
	s := liveSession(t, c, nil, true)
	got, err := c.Prompt(s, "Reply with exactly one word: pong")
	if err != nil {
		t.Fatalf("Prompt returned error: %v", err)
	}
	if !strings.Contains(strings.ToLower(got), "pong") {
		t.Errorf("Prompt = %q, want it to contain pong", got)
	}
}

func TestLivePromptRunsToolCall(t *testing.T) {
	c := liveClient(t)
	tool := &fakeTool{name: "get_secret_code", result: "7319"}
	s := liveSession(t, c, []Tool{tool}, false)
	got, err := c.Prompt(s, "Call the get_secret_code tool, then reply with only the code it returned.")
	if err != nil {
		t.Fatalf("Prompt returned error: %v", err)
	}
	if len(tool.calls) == 0 {
		t.Fatal("tool was never called")
	}
	if !strings.Contains(got, "7319") {
		t.Errorf("Prompt = %q, want it to contain 7319", got)
	}
}

func TestLivePromptRejectedWithInvalidKey(t *testing.T) {
	c := liveClient(t)
	s := liveSession(t, c, nil, false)
	c.Token = "invalid-key-for-testing"
	_, err := c.Prompt(s, "hi")
	checkErrorPrefix(t, err, "sending prompt: 401 ")
}

func TestLivePromptDeveloperNoticeNearLimit(t *testing.T) {
	c := liveClient(t)
	tool := &fakeTool{name: "get_secret_code", result: "7319"}
	s := liveSession(t, c, []Tool{tool}, false)
	// Limit is a Session field. Every real response reports some tokens used,
	// so a limit of 1 is always reached after the first tool round.
	s.Limit = 1
	got, err := c.Prompt(s, "Call the get_secret_code tool, then reply with only the code it returned.")
	// A nil error also means the API accepted the developer message in the
	// follow-up request.
	if err != nil {
		t.Fatalf("Prompt returned error: %v", err)
	}
	if len(tool.calls) == 0 {
		t.Fatal("tool was never called")
	}
	var notice *Message
	for i := range s.Messages {
		if s.Messages[i].Role == "developer" {
			notice = &s.Messages[i]
			break
		}
	}
	if notice == nil {
		t.Fatal("no developer notice was added to the session")
	}
	if content, _ := notice.Content.(string); !strings.HasPrefix(content, "Context is nearly full.") {
		t.Errorf("developer notice = %q, want it to start with the nearly-full warning", content)
	}
	if last := s.Messages[len(s.Messages)-1]; last.Role != "assistant" {
		t.Errorf("last message role = %q, want assistant", last.Role)
	}
	if !strings.Contains(got, "7319") {
		t.Errorf("Prompt = %q, want it to contain 7319", got)
	}
}
