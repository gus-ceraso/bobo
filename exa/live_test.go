//go:build live

package exa

import (
	"net/url"
	"os"
	"strings"
	"testing"
)

// The tests in this file call the real Exa API and the live web. They are
// only compiled with the "live" build tag and are skipped unless EXA_API_KEY
// is set. Run them with: make live-check

func liveKey(t *testing.T) string {
	t.Helper()
	key := os.Getenv("EXA_API_KEY")
	if key == "" {
		t.Skip("EXA_API_KEY is not set; skipping live test")
	}
	return key
}

func liveClient(t *testing.T) *Client {
	t.Helper()
	return NewClient(liveKey(t))
}

func TestLiveSearch(t *testing.T) {
	c := liveClient(t)
	results, err := c.Search("Go programming language")
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("Search returned no results")
	}
	var withHighlights int
	for i, r := range results {
		if r.Title == "" {
			t.Errorf("result %d has an empty title", i)
		}
		if u, err := url.Parse(r.URL); err != nil || u.Host == "" {
			t.Errorf("result %d has invalid URL %q", i, r.URL)
		}
		if len(r.Highlights) > 0 {
			withHighlights++
		}
	}
	if withHighlights == 0 {
		t.Error("no result had highlights, but highlights were requested")
	}
}

func TestLiveFetchHTML(t *testing.T) {
	c := liveClient(t)
	r, err := c.Fetch("https://example.com/")
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if !strings.HasPrefix(r.MediaType, "text/html") {
		t.Errorf("MediaType = %q, want text/html", r.MediaType)
	}
	if !strings.Contains(string(r.Content), "Example Domain") {
		t.Errorf("extracted text does not contain %q: %q", "Example Domain", r.Content)
	}
}

func TestLiveFetchPlainText(t *testing.T) {
	c := liveClient(t)
	r, err := c.Fetch("https://www.rfc-editor.org/rfc/rfc2616.txt")
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if !strings.HasPrefix(r.MediaType, "text/plain") {
		t.Errorf("MediaType = %q, want text/plain", r.MediaType)
	}
	if !strings.Contains(string(r.Content), "Hypertext Transfer Protocol") {
		t.Error("body does not contain the RFC 2616 title")
	}
}

func TestLiveFetchNotFound(t *testing.T) {
	c := liveClient(t)
	_, err := c.Fetch("https://example.com/does-not-exist-xyz")
	if err == nil {
		t.Fatal("Fetch returned nil error for a missing page")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error = %q, want it to mention 404", err)
	}
}

func TestLiveInvalidKeyRejected(t *testing.T) {
	// Still gated on EXA_API_KEY so the whole suite behaves the same way.
	_ = liveKey(t)
	c := NewClient("invalid-key-for-testing")
	if _, err := c.Search("Go programming language"); err == nil {
		t.Error("Search with an invalid key returned no error")
	}
}
