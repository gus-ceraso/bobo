package tool

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"ceraso.dev/bobo/exa"
)

type searchMeta struct {
	Error string `json:"error"`
	Query string `json:"query"`
	Total int    `json:"total"`
}

// newSearch returns a Search tool with its own exa client, which sends API
// requests to baseURL.
func newSearch(baseURL string) *Search {
	c := exa.NewClient("test-token")
	c.BaseURL = baseURL
	return &Search{Client: c}
}

func TestSearchMissingQuery(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
	}{
		{"missing", map[string]any{}},
		{"empty", map[string]any{"query": ""}},
		{"non-string", map[string]any{"query": 5}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta, parts := parseResult[searchMeta](t, (&Search{}).Call(tt.args))
			checkError(t, meta.Error, "missing query string")
			if len(parts) != 0 {
				t.Errorf("got %d content parts, want none", len(parts))
			}
		})
	}
}

func TestSearchReturnsResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/search" {
			t.Errorf("request = %s %s, want POST /search", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q, want %q", got, "Bearer test-token")
		}
		var req struct {
			Query    string `json:"query"`
			Contents struct {
				Highlights bool `json:"highlights"`
			} `json:"contents"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decoding search request: %v", err)
		}
		if req.Query != "Go 1.24 release notes" || !req.Contents.Highlights {
			t.Errorf("search request = %+v, want the query with highlights", req)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"results": [{"title": "Go 1.24 Release Notes", "url": "https://go.dev/doc/go1.24", "highlights": ["Go 1.24 adds"]}, {"title": "Blog", "url": "https://go.dev/blog", "highlights": []}]}`)
	}))
	t.Cleanup(srv.Close)

	meta, parts := parseResult[searchMeta](t, newSearch(srv.URL).Call(map[string]any{"query": "Go 1.24 release notes"}))
	checkNoError(t, meta.Error)
	if meta.Query != "Go 1.24 release notes" {
		t.Errorf("query = %q, want %q", meta.Query, "Go 1.24 release notes")
	}
	if meta.Total != 2 {
		t.Errorf("total = %d, want 2", meta.Total)
	}
	if len(parts) != 1 {
		t.Fatalf("got %d content parts after metadata, want 1 results part", len(parts))
	}
	var results []exa.SearchResult
	if err := json.Unmarshal([]byte(partText(t, parts, 0)), &results); err != nil {
		t.Fatalf("decoding results: %v", err)
	}
	want := []exa.SearchResult{
		{Title: "Go 1.24 Release Notes", URL: "https://go.dev/doc/go1.24", Highlights: []string{"Go 1.24 adds"}},
		{Title: "Blog", URL: "https://go.dev/blog", Highlights: []string{}},
	}
	if !reflect.DeepEqual(results, want) {
		t.Errorf("results = %+v, want %+v", results, want)
	}
}

func TestSearchNoResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"results": []}`)
	}))
	t.Cleanup(srv.Close)
	meta, parts := parseResult[searchMeta](t, newSearch(srv.URL).Call(map[string]any{"query": "nothing matches"}))
	checkNoError(t, meta.Error)
	if meta.Total != 0 {
		t.Errorf("total = %d, want 0", meta.Total)
	}
	if len(parts) != 0 {
		t.Errorf("got %d content parts, want none", len(parts))
	}
}

func TestSearchAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error": "invalid api key"}`)
	}))
	t.Cleanup(srv.Close)
	meta, parts := parseResult[searchMeta](t, newSearch(srv.URL).Call(map[string]any{"query": "q"}))
	checkError(t, meta.Error, "exa search:")
	checkError(t, meta.Error, "401")
	checkError(t, meta.Error, "invalid api key")
	if len(parts) != 0 {
		t.Errorf("got %d content parts, want none", len(parts))
	}
}

func TestSearchTransportError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	baseURL := srv.URL
	srv.Close()
	meta, _ := parseResult[searchMeta](t, newSearch(baseURL).Call(map[string]any{"query": "q"}))
	checkError(t, meta.Error, "exa search:")
}
