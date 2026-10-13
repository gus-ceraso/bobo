package openrouter

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

func TestDoGetWithoutBody(t *testing.T) {
	api := stubAPI(t, func(int, capturedRequest) *http.Response {
		return jsonResponse(http.StatusOK, `{"ok":true}`)
	})
	c := &Client{Token: "test-token"}
	if err := c.do(http.MethodGet, "/thing", nil, nil); err != nil {
		t.Fatalf("do returned error: %v", err)
	}
	req := onlyRequest(t, api)
	if req.Method != http.MethodGet {
		t.Errorf("method = %q, want GET", req.Method)
	}
	if want := "https://openrouter.ai/api/v1/thing"; req.URL != want {
		t.Errorf("URL = %q, want %q", req.URL, want)
	}
	if got, want := req.Header.Get("Authorization"), "Bearer test-token"; got != want {
		t.Errorf("Authorization = %q, want %q", got, want)
	}
	if got := req.Header.Get("Content-Type"); got != "" {
		t.Errorf("Content-Type = %q, want none", got)
	}
	if len(req.Body) != 0 {
		t.Errorf("body = %q, want empty", req.Body)
	}
}

func TestDoPostEncodesBodyAndDecodesResponse(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}
	type result struct {
		Count int `json:"count"`
	}
	api := stubAPI(t, func(int, capturedRequest) *http.Response {
		return jsonResponse(http.StatusOK, `{"count":42}`)
	})
	c := &Client{Token: "test-token"}
	var out result
	if err := c.do(http.MethodPost, "/thing", &payload{Name: "widget"}, &out); err != nil {
		t.Fatalf("do returned error: %v", err)
	}
	req := onlyRequest(t, api)
	if req.Method != http.MethodPost {
		t.Errorf("method = %q, want POST", req.Method)
	}
	if got, want := req.Header.Get("Content-Type"), "application/json"; got != want {
		t.Errorf("Content-Type = %q, want %q", got, want)
	}
	var sent payload
	if err := json.Unmarshal(req.Body, &sent); err != nil {
		t.Fatalf("decoding request body: %v", err)
	}
	if sent.Name != "widget" {
		t.Errorf("sent name = %q, want %q", sent.Name, "widget")
	}
	if out.Count != 42 {
		t.Errorf("Count = %d, want 42", out.Count)
	}
}

func TestDoIgnoresBodyWithoutResultObject(t *testing.T) {
	stubAPI(t, func(int, capturedRequest) *http.Response {
		return jsonResponse(http.StatusOK, "not json at all")
	})
	c := &Client{Token: "test-token"}
	if err := c.do(http.MethodGet, "/x", nil, nil); err != nil {
		t.Fatalf("do returned error: %v", err)
	}
}

func TestDoErrorStatus(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"bad request", http.StatusBadRequest, `{"error":{"message":"bad query"}}`, "400 Bad Request: bad query"},
		{"unauthorized with empty body", http.StatusUnauthorized, "", "401 Unauthorized: "},
		{"payment required", http.StatusPaymentRequired, `{"error":{"message":"out of credits"}}`, "402 Payment Required: out of credits"},
		{"rate limited", http.StatusTooManyRequests, `{"error":{"code":429,"message":"slow down"}}`, "429 Too Many Requests: slow down"},
		{"error object without message", http.StatusInternalServerError, `{"error":{"code":500}}`, "500 Internal Server Error: "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubAPI(t, func(int, capturedRequest) *http.Response {
				return jsonResponse(tt.status, tt.body)
			})
			c := &Client{Token: "test-token"}
			checkError(t, c.do(http.MethodGet, "/x", nil, nil), tt.want)
		})
	}
}

func TestDoReturnsErrorForNonJSONErrorBody(t *testing.T) {
	stubAPI(t, func(int, capturedRequest) *http.Response {
		return jsonResponse(http.StatusBadGateway, "<html>bad gateway</html>")
	})
	c := &Client{Token: "test-token"}
	if err := c.do(http.MethodGet, "/x", nil, nil); err == nil {
		t.Error("do returned nil error")
	}
}

func TestDoMalformedResponseBody(t *testing.T) {
	stubAPI(t, func(int, capturedRequest) *http.Response {
		return jsonResponse(http.StatusOK, `{"count":`)
	})
	c := &Client{Token: "test-token"}
	var out struct {
		Count int `json:"count"`
	}
	checkErrorPrefix(t, c.do(http.MethodGet, "/x", nil, &out), "JSON decoding response object")
}

func TestDoEncodeError(t *testing.T) {
	api := stubAPI(t, func(int, capturedRequest) *http.Response {
		return jsonResponse(http.StatusOK, `{}`)
	})
	c := &Client{Token: "test-token"}
	checkErrorPrefix(t, c.do(http.MethodPost, "/x", make(chan int), nil), "JSON encoding request object")
	if len(api.requests) != 0 {
		t.Errorf("requests = %d, want 0", len(api.requests))
	}
}

func TestDoInvalidMethod(t *testing.T) {
	api := stubAPI(t, func(int, capturedRequest) *http.Response {
		return jsonResponse(http.StatusOK, `{}`)
	})
	c := &Client{Token: "test-token"}
	checkErrorPrefix(t, c.do("BAD METHOD", "/x", nil, nil), "constructing http request")
	if len(api.requests) != 0 {
		t.Errorf("requests = %d, want 0", len(api.requests))
	}
}

func TestDoTransportError(t *testing.T) {
	stubTransport(t, func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	})
	c := &Client{Token: "test-token"}
	checkErrorPrefix(t, c.do(http.MethodGet, "/x", nil, nil), "doing http request")
}
