package exa

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestFetchPlainText(t *testing.T) {
	srv := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("hello world"))
	}))

	c := newTestClient(t, srv.URL)
	r, err := c.Fetch(srv.URL + "/doc")
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if got, want := string(r.Content), "hello world"; got != want {
		t.Errorf("Content = %q, want %q", got, want)
	}
	if got, want := r.MediaType, "text/plain"; got != want {
		t.Errorf("MediaType = %q, want %q", got, want)
	}
}

func TestFetchBinaryContentIsUnchanged(t *testing.T) {
	want := []byte{0x00, 0x01, 0xfe, 0xff, '\n', 0x7f}
	srv := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(want)
	}))

	c := newTestClient(t, srv.URL)
	r, err := c.Fetch(srv.URL + "/blob")
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if !bytes.Equal(r.Content, want) {
		t.Errorf("Content = %x, want %x", r.Content, want)
	}
	if got, wantType := r.MediaType, "application/octet-stream"; got != wantType {
		t.Errorf("MediaType = %q, want %q", got, wantType)
	}
}

func TestFetchLowercasesMediaType(t *testing.T) {
	srv := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "Text/Plain; Charset=UTF-8")
		w.Write([]byte("x"))
	}))

	c := newTestClient(t, srv.URL)
	r, err := c.Fetch(srv.URL + "/doc")
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if want := "text/plain; charset=utf-8"; r.MediaType != want {
		t.Errorf("MediaType = %q, want %q", r.MediaType, want)
	}
}

func TestFetchMissingContentTypeIsNotHTML(t *testing.T) {
	srv := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A nil value suppresses the server's automatic content sniffing.
		w.Header()["Content-Type"] = nil
		w.Write([]byte("raw body"))
	}))

	c := newTestClient(t, srv.URL)
	r, err := c.Fetch(srv.URL + "/doc")
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if r.MediaType != "" {
		t.Errorf("MediaType = %q, want empty", r.MediaType)
	}
	if got, want := string(r.Content), "raw body"; got != want {
		t.Errorf("Content = %q, want %q", got, want)
	}
}

func TestFetchCachesSuccessfulResult(t *testing.T) {
	var hits atomic.Int32
	srv := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("cached body"))
	}))

	c := newTestClient(t, srv.URL)
	u := srv.URL + "/doc"
	first, err := c.Fetch(u)
	if err != nil {
		t.Fatalf("first Fetch returned error: %v", err)
	}
	second, err := c.Fetch(u)
	if err != nil {
		t.Fatalf("second Fetch returned error: %v", err)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("server hit %d times, want 1", n)
	}
	if string(second.Content) != string(first.Content) || second.MediaType != first.MediaType {
		t.Errorf("cached result = %+v, want %+v", second, first)
	}
}

func TestFetchCacheIsKeyedByURL(t *testing.T) {
	srv := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "body of %s", r.URL.Path)
	}))

	c := newTestClient(t, srv.URL)
	for _, p := range []string{"/a", "/b"} {
		r, err := c.Fetch(srv.URL + p)
		if err != nil {
			t.Fatalf("Fetch(%s) returned error: %v", p, err)
		}
		if want := "body of " + p; string(r.Content) != want {
			t.Errorf("Fetch(%s) Content = %q, want %q", p, r.Content, want)
		}
	}
}

func TestFetchHTMLUsesContentsAPI(t *testing.T) {
	var contentsCalls atomic.Int32
	srv := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/page":
			w.Header().Set("Content-Type", "text/html; charset=UTF-8")
			w.Write([]byte("<html><body>Hi</body></html>"))
		case "/contents":
			contentsCalls.Add(1)
			if r.Method != http.MethodPost {
				t.Errorf("contents method = %q, want POST", r.Method)
			}
			if got, want := r.Header.Get("Authorization"), "Bearer test-token"; got != want {
				t.Errorf("Authorization = %q, want %q", got, want)
			}
			var body struct {
				URLs []string `json:"urls"`
				Text bool     `json:"text"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decoding contents request: %v", err)
			}
			wantURL := "http://" + r.Host + "/page"
			if len(body.URLs) != 1 || body.URLs[0] != wantURL {
				t.Errorf("urls = %v, want [%s]", body.URLs, wantURL)
			}
			if !body.Text {
				t.Error("text = false, want true")
			}
			w.Write([]byte(`{"results":[{"text":"Hi"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))

	c := newTestClient(t, srv.URL)
	page := srv.URL + "/page"
	r, err := c.Fetch(page)
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if got, want := string(r.Content), "Hi"; got != want {
		t.Errorf("Content = %q, want %q", got, want)
	}
	if got, want := r.MediaType, "text/html; charset=utf-8"; got != want {
		t.Errorf("MediaType = %q, want %q", got, want)
	}

	cached, ok := c.Cache.Get(page)
	if !ok {
		t.Fatal("successful HTML fetch was not cached")
	}
	if string(cached.Content) != "Hi" {
		t.Errorf("cached Content = %q, want %q", cached.Content, "Hi")
	}

	// A second fetch should be served from the cache.
	if _, err := c.Fetch(page); err != nil {
		t.Fatalf("second Fetch returned error: %v", err)
	}
	if n := contentsCalls.Load(); n != 1 {
		t.Errorf("contents API called %d times, want 1", n)
	}
}

func TestFetchHTMLWithNoExtractedTextIsNotCached(t *testing.T) {
	var contentsCalls atomic.Int32
	srv := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/page":
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte("<html></html>"))
		case "/contents":
			contentsCalls.Add(1)
			w.Write([]byte(`{"results":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))

	c := newTestClient(t, srv.URL)
	page := srv.URL + "/page"
	r, err := c.Fetch(page)
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if r.Content != nil {
		t.Errorf("Content = %q, want nil", r.Content)
	}
	if _, ok := c.Cache.Get(page); ok {
		t.Error("empty HTML result was cached")
	}

	if _, err := c.Fetch(page); err != nil {
		t.Fatalf("second Fetch returned error: %v", err)
	}
	if n := contentsCalls.Load(); n != 2 {
		t.Errorf("contents API called %d times, want 2", n)
	}
}

func TestFetchHTTPErrorStatus(t *testing.T) {
	srv := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))

	c := newTestClient(t, srv.URL)
	u := srv.URL + "/missing"
	r, err := c.Fetch(u)
	if err == nil {
		t.Fatal("Fetch returned nil error")
	}
	if want := fmt.Sprintf("GET %s: 404 Not Found (not OK)", u); err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
	if r.Content != nil {
		t.Errorf("Content = %q, want nil", r.Content)
	}
	if _, ok := c.Cache.Get(u); ok {
		t.Error("failed fetch was cached")
	}
}

func TestFetchContentsAPIError(t *testing.T) {
	var pageCalls atomic.Int32
	srv := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/page":
			pageCalls.Add(1)
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte("<html></html>"))
		case "/contents":
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error":"boom"}`))
		default:
			http.NotFound(w, r)
		}
	}))

	c := newTestClient(t, srv.URL)
	page := srv.URL + "/page"
	_, err := c.Fetch(page)
	if err == nil {
		t.Fatal("Fetch returned nil error")
	}
	if want := "contents: 500 Internal Server Error: boom"; err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
	if _, ok := c.Cache.Get(page); ok {
		t.Error("failed fetch was cached")
	}
}

func TestFetchTransportError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	u := srv.URL + "/gone"
	srv.Close() // nothing listens here any more

	c := NewClient("test-token")
	_, err := c.Fetch(u)
	if err == nil {
		t.Fatal("Fetch returned nil error")
	}
	if want := "GET " + u + ": "; !strings.HasPrefix(err.Error(), want) {
		t.Errorf("error = %q, want prefix %q", err.Error(), want)
	}
}

func TestFetchTruncatedBodyReadError(t *testing.T) {
	srv := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		defer conn.Close()
		// Promise 100 body bytes, send 5, then hang up.
		buf.WriteString("HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: 100\r\n\r\nshort")
		buf.Flush()
	}))

	c := NewClient("test-token")
	_, err := c.Fetch(srv.URL + "/truncated")
	if err == nil {
		t.Fatal("Fetch returned nil error")
	}
	if want := "read: "; !strings.HasPrefix(err.Error(), want) {
		t.Errorf("error = %q, want prefix %q", err.Error(), want)
	}
}
