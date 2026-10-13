package exa

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestSearchSendsQueryWithHighlights(t *testing.T) {
	srv := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if r.URL.Path != "/search" {
			t.Errorf("path = %q, want /search", r.URL.Path)
		}
		if got, want := r.Header.Get("Authorization"), "Bearer test-token"; got != want {
			t.Errorf("Authorization = %q, want %q", got, want)
		}
		var body struct {
			Query    string `json:"query"`
			Contents struct {
				Highlights bool `json:"highlights"`
			} `json:"contents"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		if body.Query != "golang generics" {
			t.Errorf("query = %q, want %q", body.Query, "golang generics")
		}
		if !body.Contents.Highlights {
			t.Error("contents.highlights = false, want true")
		}
		w.Write([]byte(`{"results":[]}`))
	}))

	c := newTestClient(t, srv.URL)
	if _, err := c.Search("golang generics"); err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
}

func TestSearchReturnsResults(t *testing.T) {
	srv := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{
			"results": [
				{"title": "First", "url": "https://first.example", "highlights": ["a", "b"]},
				{"title": "Second", "url": "https://second.example", "highlights": []}
			]
		}`))
	}))

	c := newTestClient(t, srv.URL)
	got, err := c.Search("q")
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	want := []SearchResult{
		{Title: "First", URL: "https://first.example", Highlights: []string{"a", "b"}},
		{Title: "Second", URL: "https://second.example", Highlights: []string{}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Search results = %+v, want %+v", got, want)
	}
}

func TestSearchNoResults(t *testing.T) {
	srv := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{}`))
	}))

	c := newTestClient(t, srv.URL)
	got, err := c.Search("nothing matches this")
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Search results = %+v, want none", got)
	}
}

func TestSearchAPIError(t *testing.T) {
	srv := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"invalid api key"}`))
	}))

	c := newTestClient(t, srv.URL)
	got, err := c.Search("q")
	if err == nil {
		t.Fatal("Search returned nil error")
	}
	if want := "searching: 401 Unauthorized: invalid api key"; err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
	if got != nil {
		t.Errorf("results = %+v, want nil", got)
	}
}

func TestSearchMalformedResponse(t *testing.T) {
	srv := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))

	c := newTestClient(t, srv.URL)
	_, err := c.Search("q")
	if err == nil {
		t.Fatal("Search returned nil error")
	}
	if want := "searching: JSON decoding response object"; !strings.HasPrefix(err.Error(), want) {
		t.Errorf("error = %q, want prefix %q", err.Error(), want)
	}
}
