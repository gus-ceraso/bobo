package exa

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newTestServer starts an httptest server running h and closes it when the
// test finishes.
func newTestServer(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// newTestClient returns a Client whose API requests go to the server at base.
func newTestClient(t *testing.T, base string) *Client {
	t.Helper()
	c := NewClient("test-token")
	c.BaseURL = base
	return c
}

func TestNewClient(t *testing.T) {
	c := NewClient("abc")
	if c.Token != "abc" {
		t.Errorf("Token = %q, want %q", c.Token, "abc")
	}
	if c.BaseURL != "https://api.exa.ai" {
		t.Errorf("BaseURL = %q, want %q", c.BaseURL, "https://api.exa.ai")
	}
	if c.Cache == nil {
		t.Error("Cache is nil")
	}
	if c.HTTP == nil {
		t.Fatal("HTTP is nil")
	}
	if c.HTTP.Timeout != 120*time.Second {
		t.Errorf("HTTP.Timeout = %v, want %v", c.HTTP.Timeout, 120*time.Second)
	}
}

func TestDoPostEncodesRequestAndDecodesResponse(t *testing.T) {
	type reqBody struct {
		Name string `json:"name"`
	}
	type resBody struct {
		Count int `json:"count"`
	}
	srv := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if r.URL.Path != "/thing" {
			t.Errorf("path = %q, want /thing", r.URL.Path)
		}
		if got, want := r.Header.Get("Authorization"), "Bearer test-token"; got != want {
			t.Errorf("Authorization = %q, want %q", got, want)
		}
		if got, want := r.Header.Get("Content-Type"), "application/json"; got != want {
			t.Errorf("Content-Type = %q, want %q", got, want)
		}
		var in reqBody
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		if in.Name != "widget" {
			t.Errorf("name = %q, want %q", in.Name, "widget")
		}
		w.Write([]byte(`{"count": 42}`))
	}))

	c := newTestClient(t, srv.URL)
	var out resBody
	if err := c.do(http.MethodPost, "/thing", &reqBody{Name: "widget"}, &out); err != nil {
		t.Fatalf("do returned error: %v", err)
	}
	if out.Count != 42 {
		t.Errorf("Count = %d, want 42", out.Count)
	}
}

func TestDoGetWithoutBodyOrResult(t *testing.T) {
	srv := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "" {
			t.Errorf("Content-Type = %q, want none", ct)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading request body: %v", err)
		}
		if len(body) != 0 {
			t.Errorf("request body = %q, want empty", body)
		}
		w.Write([]byte("ignored"))
	}))

	c := newTestClient(t, srv.URL)
	if err := c.do(http.MethodGet, "/status", nil, nil); err != nil {
		t.Fatalf("do returned error: %v", err)
	}
}

func TestDoErrorStatusWithJSONErrorField(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr string
	}{
		{"bad request", http.StatusBadRequest, `{"error":"bad query"}`, "400 Bad Request: bad query"},
		{"empty body", http.StatusUnauthorized, "", "401 Unauthorized: "},
		{"rate limited", http.StatusTooManyRequests, `{"error":"slow down"}`, "429 Too Many Requests: slow down"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				w.Write([]byte(tt.body))
			}))
			c := newTestClient(t, srv.URL)
			err := c.do(http.MethodPost, "/x", nil, nil)
			if err == nil {
				t.Fatal("do returned nil error")
			}
			if err.Error() != tt.wantErr {
				t.Errorf("error = %q, want %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestDoErrorStatusWithUndecodableBody(t *testing.T) {
	srv := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte("<html>bad gateway</html>"))
	}))
	c := newTestClient(t, srv.URL)
	err := c.do(http.MethodGet, "/x", nil, nil)
	if err == nil {
		t.Fatal("do returned nil error")
	}
	if want := "JSON decoding error response object"; !strings.HasPrefix(err.Error(), want) {
		t.Errorf("error = %q, want prefix %q", err.Error(), want)
	}
}

func TestDoMalformedSuccessBody(t *testing.T) {
	srv := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"count":`))
	}))
	c := newTestClient(t, srv.URL)
	var out struct {
		Count int `json:"count"`
	}
	err := c.do(http.MethodGet, "/x", nil, &out)
	if err == nil {
		t.Fatal("do returned nil error")
	}
	if want := "JSON decoding response object"; !strings.HasPrefix(err.Error(), want) {
		t.Errorf("error = %q, want prefix %q", err.Error(), want)
	}
}

func TestDoIgnoresResponseBodyWhenNoResultObject(t *testing.T) {
	srv := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json at all"))
	}))
	c := newTestClient(t, srv.URL)
	if err := c.do(http.MethodGet, "/x", nil, nil); err != nil {
		t.Fatalf("do returned error: %v", err)
	}
}

func TestDoEncodeError(t *testing.T) {
	srv := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("server should not be contacted when request encoding fails")
	}))
	c := newTestClient(t, srv.URL)
	// Channels cannot be marshaled to JSON.
	err := c.do(http.MethodPost, "/x", make(chan int), nil)
	if err == nil {
		t.Fatal("do returned nil error")
	}
	if want := "JSON encoding request object"; !strings.HasPrefix(err.Error(), want) {
		t.Errorf("error = %q, want prefix %q", err.Error(), want)
	}
}

func TestDoInvalidMethod(t *testing.T) {
	c := newTestClient(t, "http://127.0.0.1:1")
	err := c.do("BAD METHOD", "/x", nil, nil)
	if err == nil {
		t.Fatal("do returned nil error")
	}
	if want := "constructing http request"; !strings.HasPrefix(err.Error(), want) {
		t.Errorf("error = %q, want prefix %q", err.Error(), want)
	}
}

func TestDoTransportError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL
	srv.Close() // nothing listens here any more

	c := newTestClient(t, base)
	err := c.do(http.MethodGet, "/x", nil, nil)
	if err == nil {
		t.Fatal("do returned nil error")
	}
	if want := "doing http request"; !strings.HasPrefix(err.Error(), want) {
		t.Errorf("error = %q, want prefix %q", err.Error(), want)
	}
}
