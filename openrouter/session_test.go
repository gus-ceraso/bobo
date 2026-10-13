package openrouter

import (
	"fmt"
	"net/http"
	"testing"
)

func modelInfo(inputModalities []string, contextLength, maxCompletionTokens int, efforts []string) map[string]any {
	return map[string]any{
		"data": map[string]any{
			"architecture": map[string]any{"input_modalities": inputModalities},
			"top_provider": map[string]any{
				"context_length":        contextLength,
				"max_completion_tokens": maxCompletionTokens,
			},
			"reasoning": map[string]any{"supported_efforts": efforts},
		},
	}
}

func stubModel(t *testing.T, info map[string]any) *fakeAPI {
	t.Helper()
	return stubAPI(t, func(int, capturedRequest) *http.Response {
		return jsonValue(http.StatusOK, info)
	})
}

func TestNewSessionRequiresModel(t *testing.T) {
	api := stubAPI(t, func(int, capturedRequest) *http.Response {
		return jsonResponse(http.StatusOK, `{}`)
	})
	c := &Client{Token: "test-token"}
	_, err := c.NewSession("", "", "sys", false, nil)
	checkError(t, err, "invalid arguments: empty model")
	if len(api.requests) != 0 {
		t.Errorf("requests = %d, want 0", len(api.requests))
	}
}

func TestNewSessionFetchesModel(t *testing.T) {
	api := stubModel(t, modelInfo([]string{"image"}, 1000, 100, []string{}))
	c := &Client{Token: "test-token"}
	if _, err := c.NewSession("openai/gpt-4o", "", "sys", false, nil); err != nil {
		t.Fatalf("NewSession returned error: %v", err)
	}
	req := onlyRequest(t, api)
	if req.Method != http.MethodGet {
		t.Errorf("method = %q, want GET", req.Method)
	}
	if want := "https://openrouter.ai/api/v1/model/openai/gpt-4o"; req.URL != want {
		t.Errorf("URL = %q, want %q", req.URL, want)
	}
	if got, want := req.Header.Get("Authorization"), "Bearer test-token"; got != want {
		t.Errorf("Authorization = %q, want %q", got, want)
	}
}

func TestNewSessionFetchError(t *testing.T) {
	stubAPI(t, func(int, capturedRequest) *http.Response {
		return jsonResponse(http.StatusNotFound, `{"error":{"message":"Resource not found"}}`)
	})
	c := &Client{Token: "test-token"}
	_, err := c.NewSession("openai/missing", "", "sys", false, nil)
	checkError(t, err, "fetching model information: 404 Not Found: Resource not found")
}

func TestNewSessionEffort(t *testing.T) {
	tests := []struct {
		name    string
		effort  string
		efforts []string
		wantErr string
	}{
		{"no effort requested", "", []string{}, ""},
		{"supported effort", "high", []string{"low", "high"}, ""},
		{"unsupported effort", "xhigh", []string{"low", "high"}, "invalid arguments: effort not supported by model"},
		{"model without reasoning", "low", []string{}, "invalid arguments: effort not supported by model"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubModel(t, modelInfo([]string{"text", "image"}, 1000, 100, tt.efforts))
			c := &Client{Token: "test-token"}
			_, err := c.NewSession("m", tt.effort, "sys", false, nil)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("NewSession returned error: %v", err)
				}
				return
			}
			checkError(t, err, tt.wantErr)
		})
	}
}

func TestNewSessionRequiresImageInput(t *testing.T) {
	tests := []struct {
		name       string
		modalities []string
		wantErr    string
	}{
		{"text and image", []string{"text", "image"}, ""},
		{"text only", []string{"text"}, "invalid arguments: images not supported by model"},
		{"no modalities", []string{}, "invalid arguments: images not supported by model"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubModel(t, modelInfo(tt.modalities, 1000, 100, []string{}))
			c := &Client{Token: "test-token"}
			_, err := c.NewSession("m", "", "sys", false, nil)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("NewSession returned error: %v", err)
				}
				return
			}
			checkError(t, err, tt.wantErr)
		})
	}
}

func TestNewSessionSetsFields(t *testing.T) {
	stubModel(t, modelInfo([]string{"text", "image"}, 8193, 4096, []string{"low", "high"}))
	tool := &fakeTool{name: "echo"}
	c := &Client{Token: "test-token"}
	s, err := c.NewSession("openai/gpt-4o", "high", "be nice", true, []Tool{tool})
	if err != nil {
		t.Fatalf("NewSession returned error: %v", err)
	}
	if s.Model != "openai/gpt-4o" {
		t.Errorf("Model = %q, want %q", s.Model, "openai/gpt-4o")
	}
	if s.Effort != "high" {
		t.Errorf("Effort = %q, want %q", s.Effort, "high")
	}
	if !s.Cache {
		t.Error("Cache = false, want true")
	}
	if s.Limit != 6145 {
		t.Errorf("Limit = %d, want 6145", s.Limit)
	}
	if s.MaxCompletionTokens != 4096 {
		t.Errorf("MaxCompletionTokens = %d, want 4096", s.MaxCompletionTokens)
	}
	if len(s.Tools) != 1 || s.Tools[0] != Tool(tool) {
		t.Errorf("Tools = %v, want [tool]", s.Tools)
	}
	if len(s.Messages) != 1 || s.Messages[0].Role != "system" || s.Messages[0].Content != "be nice" {
		t.Errorf("Messages = %+v, want one system message", s.Messages)
	}
}

func TestNewSessionLimit(t *testing.T) {
	tests := []struct {
		contextLength int
		want          int
	}{
		{1, 1},
		{2, 1},
		{3, 2},
		{4, 3},
		{8192, 6144},
		{8193, 6145},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("context %d", tt.contextLength), func(t *testing.T) {
			stubModel(t, modelInfo([]string{"image"}, tt.contextLength, 0, []string{}))
			c := &Client{Token: "test-token"}
			s, err := c.NewSession("m", "", "sys", false, nil)
			if err != nil {
				t.Fatalf("NewSession returned error: %v", err)
			}
			if s.Limit != tt.want {
				t.Errorf("Limit = %d, want %d", s.Limit, tt.want)
			}
		})
	}
}

func TestNewSessionCopiesToolSlice(t *testing.T) {
	stubModel(t, modelInfo([]string{"image"}, 1000, 100, []string{}))
	first := &fakeTool{name: "first"}
	tools := []Tool{first}
	c := &Client{Token: "test-token"}
	s, err := c.NewSession("m", "", "sys", false, tools)
	if err != nil {
		t.Fatalf("NewSession returned error: %v", err)
	}
	tools[0] = &fakeTool{name: "replaced"}
	if s.Tools[0] != Tool(first) {
		t.Error("session tool changed after the input slice was modified")
	}
}
