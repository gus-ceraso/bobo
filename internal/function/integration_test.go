package function

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ceraso.dev/bobo/internal/provider"
)

type functionTestTransport func(*http.Request) (*http.Response, error)

func (f functionTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type functionTestCredentialStore struct{}

func (functionTestCredentialStore) Load(ctx context.Context) (provider.Credential, error) {
	return provider.Credential{FallbackToken: "test-token"}, ctx.Err()
}

func (functionTestCredentialStore) Update(context.Context, func(provider.Credential) (provider.Credential, error)) (provider.Credential, error) {
	return provider.Credential{}, fmt.Errorf("unexpected credential update")
}

func completedFunctionResponse(t testing.TB, output []map[string]any) *http.Response {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"type":     "response.completed",
		"response": map[string]any{"id": "response_test", "status": "completed", "output": output},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"text/event-stream"}},
		Body:       io.NopCloser(bytes.NewReader(append(append([]byte("data: "), body...), '\n', '\n'))),
	}
}

func functionCall(t testing.TB, id, name string, args map[string]any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{"type": "function_call", "call_id": id, "name": name, "arguments": string(encoded)}
}

func decodeFunctionOutput(t testing.TB, event map[string]any, id string) []provider.Part {
	t.Helper()
	if event["type"] != "function_call_output" || event["call_id"] != id {
		t.Fatalf("wrong function output: %#v", event)
	}
	items, ok := event["output"].([]any)
	if !ok {
		t.Fatalf("missing encoded parts: %#v", event)
	}
	var parts []provider.Part
	for _, item := range items {
		part := item.(map[string]any)
		switch part["type"] {
		case "input_text":
			parts = append(parts, provider.Part{ContentType: "text/plain", Content: []byte(part["text"].(string))})
		case "input_image":
			url := part["image_url"].(string)
			if !strings.HasPrefix(url, "data:image/png;base64,") {
				t.Fatalf("unexpected image encoding: %s", url)
			}
			data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(url, "data:image/png;base64,"))
			if err != nil {
				t.Fatal(err)
			}
			parts = append(parts, provider.Part{ContentType: "image/png", Content: data})
		default:
			t.Fatalf("unexpected encoded part: %#v", part)
		}
	}
	return parts
}

func TestProviderSessionWithRealFunctions(t *testing.T) {
	r, v, w, e := NewFileFunctions()
	b := &Bash{}
	functions := []provider.Function{r, v, w, e, b}
	path := filepath.Join(t.TempDir(), "file")
	imagePath := pngFixture(t, sampleImage(4, 3))
	text := strings.Repeat("世", 81) + "\nanchor\n"
	started := time.Now()
	requests := 0
	client := provider.NewClient(functionTestCredentialStore{})
	client.HTTPClient = &http.Client{Transport: functionTestTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "api.openai.com" || req.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("unexpected request: %v", req)
		}
		var body struct {
			Input []map[string]any `json:"input"`
			Tools []struct {
				Tools []map[string]any `json:"tools"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.Tools) != 2 || len(body.Tools[0].Tools) != len(functions) {
			t.Fatalf("missing function schemas: %#v", body.Tools)
		}
		for i, name := range []string{"read", "view", "write", "edit", "bash"} {
			if body.Tools[0].Tools[i]["name"] != name {
				t.Fatalf("wrong schema order: %#v", body.Tools)
			}
		}
		requests++
		switch requests {
		case 1:
			return completedFunctionResponse(t, []map[string]any{
				functionCall(t, "write", "write", map[string]any{"path": path, "text": text}),
				functionCall(t, "view", "view", map[string]any{"path": imagePath}),
			}), nil
		case 2:
			if len(body.Input) != 5 {
				t.Fatalf("unexpected first turn history: %#v", body.Input)
			}
			write := inspectResult(t, w, decodeFunctionOutput(t, body.Input[3], "write"), started, time.Now())
			requireSuccess(t, write)
			assertAttribute(t, write, "text", wantPreview(text))
			view := inspectResult(t, v, decodeFunctionOutput(t, body.Input[4], "view"), started, time.Now())
			requireImageEqual(t, decodeView(t, view), sampleImage(4, 3))
			return completedFunctionResponse(t, []map[string]any{functionCall(t, "edit", "edit", map[string]any{"path": path, "old_text": "anchor", "new_text": "edited"})}), nil
		case 3:
			edit := inspectResult(t, e, decodeFunctionOutput(t, body.Input[len(body.Input)-1], "edit"), started, time.Now())
			requireSuccess(t, edit)
			assertAttribute(t, edit, "old_text", "anchor")
			return completedFunctionResponse(t, []map[string]any{
				functionCall(t, "read", "read", map[string]any{"path": path, "offset": 2, "limit": 1}),
				functionCall(t, "bash", "bash", map[string]any{"command": "printf bashdone"}),
			}), nil
		case 4:
			read := inspectResult(t, r, decodeFunctionOutput(t, body.Input[len(body.Input)-2], "read"), started, time.Now())
			requireSuccess(t, read)
			if read.body != "\nedited\n" {
				t.Fatalf("provider did not preserve json.Number arguments: %q", read.body)
			}
			bash := inspectResult(t, b, decodeFunctionOutput(t, body.Input[len(body.Input)-1], "bash"), started, time.Now())
			requireSuccess(t, bash)
			if bash.body != "\nbashdone\n" {
				t.Fatalf("unexpected bash output: %q", bash.body)
			}
			return completedFunctionResponse(t, []map[string]any{{"type": "message", "role": "assistant", "content": []map[string]any{{"type": "output_text", "text": "done"}}}}), nil
		default:
			return nil, fmt.Errorf("unexpected request %d", requests)
		}
	})}
	session := provider.NewSession("test-model", "low", "test instructions", functions)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	answer, err := client.Prompt(ctx, session, "exercise all functions")
	if err != nil || answer != "done" || requests != 4 || len(session.Events) != 12 {
		t.Fatalf("Prompt = %q, %v; requests=%d, events=%d", answer, err, requests, len(session.Events))
	}
	if string(readFixture(t, path)) != strings.Replace(text, "anchor", "edited", 1) {
		t.Fatal("session did not perform the complete write/edit/read workflow")
	}
	requireEmptyRegistry(t, r.locks)
}
