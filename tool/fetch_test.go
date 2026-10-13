package tool

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"ceraso.dev/bobo/exa"
)

type fetchMeta struct {
	Error     string `json:"error"`
	URL       string `json:"url"`
	MediaType string `json:"media_type"`
	Offset    int    `json:"offset"`
	Limit     int    `json:"limit"`
	Total     int    `json:"total"`
}

// fetchPage is a response served by newFetchServer.
type fetchPage struct {
	contentType string
	body        []byte
}

// newFetchServer serves pages by URL path. Other paths get a 404.
func newFetchServer(t *testing.T, pages map[string]fetchPage) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, ok := pages[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", page.contentType)
		w.Write(page.body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newFetch returns a Fetch tool with its own exa client. Contents API requests
// go to baseURL. The cache lives on the client, so each test gets its own.
func newFetch(baseURL string) *Fetch {
	c := exa.NewClient("test-token")
	c.BaseURL = baseURL
	return &Fetch{Client: c}
}

// Argument checks run before the request, so no client is needed here.
func TestFetchOffsetAndLimitMustBePositive(t *testing.T) {
	for _, key := range []string{"offset", "limit"} {
		for _, v := range []any{num(0), num(-1), "2"} {
			t.Run(fmt.Sprintf("%s=%v", key, v), func(t *testing.T) {
				meta, parts := parseResult[fetchMeta](t, (&Fetch{}).Call(map[string]any{"url": "https://example.com/", key: v}))
				checkError(t, meta.Error, key+" must be an integer >= 1")
				if len(parts) != 0 {
					t.Errorf("got %d content parts, want none", len(parts))
				}
			})
		}
	}
}

func TestFetchTextLines(t *testing.T) {
	srv := newFetchServer(t, map[string]fetchPage{
		"/notes.txt": {"text/plain; charset=utf-8", []byte("one\ntwo\nthree\nfour\n")},
	})
	tests := []struct {
		name    string
		args    map[string]any
		want    string
		wantOff int
		wantLim int
	}{
		{"defaults", nil, "one\ntwo\nthree\nfour", 1, 1000},
		{"middle lines", map[string]any{"offset": num(2), "limit": num(2)}, "two\nthree", 2, 2},
		{"offset past end", map[string]any{"offset": num(5)}, "", 5, 1000},
		{"limit past end", map[string]any{"offset": num(4), "limit": num(10)}, "four", 4, 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := map[string]any{"url": srv.URL + "/notes.txt"}
			for k, v := range tt.args {
				args[k] = v
			}
			meta, parts := parseResult[fetchMeta](t, newFetch(srv.URL).Call(args))
			checkNoError(t, meta.Error)
			if meta.MediaType != "text/plain; charset=utf-8" {
				t.Errorf("media_type = %q, want text/plain; charset=utf-8", meta.MediaType)
			}
			if meta.Total != 4 {
				t.Errorf("total = %d, want 4", meta.Total)
			}
			if meta.Offset != tt.wantOff || meta.Limit != tt.wantLim {
				t.Errorf("offset, limit = %d, %d, want %d, %d", meta.Offset, meta.Limit, tt.wantOff, tt.wantLim)
			}
			if got := partText(t, parts, 0); got != tt.want {
				t.Errorf("content = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFetchJSONIsTextLines(t *testing.T) {
	srv := newFetchServer(t, map[string]fetchPage{
		"/data.json": {"application/json", []byte(`{"a": 1, "b": 2}`)},
	})
	meta, parts := parseResult[fetchMeta](t, newFetch(srv.URL).Call(map[string]any{"url": srv.URL + "/data.json"}))
	checkNoError(t, meta.Error)
	if meta.MediaType != "application/json" {
		t.Errorf("media_type = %q, want application/json", meta.MediaType)
	}
	if meta.Total != 1 {
		t.Errorf("total = %d, want 1", meta.Total)
	}
	if got := partText(t, parts, 0); got != `{"a": 1, "b": 2}` {
		t.Errorf("content = %q, want the JSON text", got)
	}
}

// HTML pages go to the contents API, and the extracted text is then paged
// like a text file. The result is cached, so a second fetch makes no second
// contents request.
func TestFetchHTMLUsesContentsAPI(t *testing.T) {
	var contents atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/page":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			io.WriteString(w, "<html><body><p>Hello</p></body></html>")
		case "/contents":
			contents.Add(1)
			if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
				t.Errorf("Authorization = %q, want %q", got, "Bearer test-token")
			}
			var req struct {
				URLs []string `json:"urls"`
				Text bool     `json:"text"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decoding contents request: %v", err)
			}
			if !req.Text || len(req.URLs) != 1 || !strings.HasSuffix(req.URLs[0], "/page") {
				t.Errorf("contents request = %+v, want text for the page URL", req)
			}
			io.WriteString(w, `{"results": [{"text": "Hello from page\nsecond line"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	f := newFetch(srv.URL)
	args := map[string]any{"url": srv.URL + "/page"}
	meta, parts := parseResult[fetchMeta](t, f.Call(args))
	checkNoError(t, meta.Error)
	if meta.MediaType != "text/html; charset=utf-8" {
		t.Errorf("media_type = %q, want text/html; charset=utf-8", meta.MediaType)
	}
	if meta.Total != 2 {
		t.Errorf("total = %d, want 2", meta.Total)
	}
	if got := partText(t, parts, 0); got != "Hello from page\nsecond line" {
		t.Errorf("content = %q, want %q", got, "Hello from page\nsecond line")
	}

	meta, parts = parseResult[fetchMeta](t, f.Call(args))
	checkNoError(t, meta.Error)
	if got := partText(t, parts, 0); got != "Hello from page\nsecond line" {
		t.Errorf("cached content = %q, want %q", got, "Hello from page\nsecond line")
	}
	if n := contents.Load(); n != 1 {
		t.Errorf("contents requests = %d, want 1", n)
	}
}

func TestFetchHTMLContentsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/contents" {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error": "invalid key"}`)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, "<p>Hello</p>")
	}))
	t.Cleanup(srv.Close)
	meta, parts := parseResult[fetchMeta](t, newFetch(srv.URL).Call(map[string]any{"url": srv.URL + "/page"}))
	checkError(t, meta.Error, "contents:")
	checkError(t, meta.Error, "invalid key")
	if len(parts) != 0 {
		t.Errorf("got %d content parts, want none", len(parts))
	}
}

func TestFetchImagesReturnedAsIs(t *testing.T) {
	tests := []struct {
		name      string
		mediaType string
		body      []byte
	}{
		{"png", "image/png", []byte("fake png bytes")},
		{"jpeg", "image/jpeg", []byte("fake jpeg bytes")},
		{"webp", "image/webp", []byte("fake webp bytes")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newFetchServer(t, map[string]fetchPage{"/img": {tt.mediaType, tt.body}})
			// Offset and limit are ignored for images.
			args := map[string]any{"url": srv.URL + "/img", "offset": num(3), "limit": num(2)}
			meta, parts := parseResult[fetchMeta](t, newFetch(srv.URL).Call(args))
			checkNoError(t, meta.Error)
			if meta.MediaType != tt.mediaType {
				t.Errorf("media_type = %q, want %q", meta.MediaType, tt.mediaType)
			}
			if len(parts) != 1 {
				t.Fatalf("got %d content parts, want 1", len(parts))
			}
			if got := decodeImagePart(t, parts[0], tt.mediaType); !bytes.Equal(got, tt.body) {
				t.Errorf("image bytes = %q, want %q", got, tt.body)
			}
		})
	}
}

func TestFetchPDFPages(t *testing.T) {
	srv := newFetchServer(t, map[string]fetchPage{
		"/doc.pdf": {"application/pdf", minimalPDF(3)},
	})
	pageURL := srv.URL + "/doc.pdf"

	meta, parts := parseResult[fetchMeta](t, newFetch(srv.URL).Call(map[string]any{"url": pageURL}))
	checkNoError(t, meta.Error)
	if meta.MediaType != "application/pdf" {
		t.Errorf("media_type = %q, want application/pdf", meta.MediaType)
	}
	if meta.Total != 3 || meta.Offset != 1 || meta.Limit != 5 {
		t.Errorf("total, offset, limit = %d, %d, %d, want 3, 1, 5", meta.Total, meta.Offset, meta.Limit)
	}
	if len(parts) != 3 {
		t.Fatalf("got %d page images, want 3", len(parts))
	}
	for i, part := range parts {
		if img := decodeImagePart(t, part, "image/png"); !bytes.HasPrefix(img, []byte("\x89PNG")) {
			t.Errorf("page %d is not a PNG", i+1)
		}
	}

	args := map[string]any{"url": pageURL, "offset": num(2), "limit": num(1)}
	meta, parts = parseResult[fetchMeta](t, newFetch(srv.URL).Call(args))
	checkNoError(t, meta.Error)
	if meta.Total != 3 || meta.Offset != 2 || meta.Limit != 1 {
		t.Errorf("total, offset, limit = %d, %d, %d, want 3, 2, 1", meta.Total, meta.Offset, meta.Limit)
	}
	if len(parts) != 1 {
		t.Fatalf("got %d page images, want 1", len(parts))
	}

	args = map[string]any{"url": pageURL, "offset": num(4)}
	meta, parts = parseResult[fetchMeta](t, newFetch(srv.URL).Call(args))
	checkNoError(t, meta.Error)
	if meta.Total != 3 || len(parts) != 0 {
		t.Errorf("offset past last page: total = %d, images = %d, want 3, 0", meta.Total, len(parts))
	}
}

func TestFetchUnsupportedMediaType(t *testing.T) {
	srv := newFetchServer(t, map[string]fetchPage{
		"/a.zip": {"application/zip", []byte("PK\x03\x04")},
		"/a.gif": {"image/gif", []byte("GIF89a")},
	})
	tests := []struct {
		name      string
		path      string
		mediaType string
	}{
		{"zip", "/a.zip", "application/zip"},
		{"gif", "/a.gif", "image/gif"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta, parts := parseResult[fetchMeta](t, newFetch(srv.URL).Call(map[string]any{"url": srv.URL + tt.path}))
			checkError(t, meta.Error, "media type not supported")
			if meta.MediaType != tt.mediaType {
				t.Errorf("media_type = %q, want %q", meta.MediaType, tt.mediaType)
			}
			if len(parts) != 0 {
				t.Errorf("got %d content parts, want none", len(parts))
			}
		})
	}
}

func TestFetchErrors(t *testing.T) {
	srv := newFetchServer(t, nil)
	t.Run("status 404", func(t *testing.T) {
		url := srv.URL + "/missing"
		meta, parts := parseResult[fetchMeta](t, newFetch(srv.URL).Call(map[string]any{"url": url}))
		checkError(t, meta.Error, "fetch '"+url+"'")
		checkError(t, meta.Error, "404")
		if len(parts) != 0 {
			t.Errorf("got %d content parts, want none", len(parts))
		}
	})
	t.Run("connection refused", func(t *testing.T) {
		dead := httptest.NewServer(http.NotFoundHandler())
		deadURL := dead.URL
		dead.Close()
		url := deadURL + "/x"
		meta, _ := parseResult[fetchMeta](t, newFetch(deadURL).Call(map[string]any{"url": url}))
		checkError(t, meta.Error, "fetch '"+url+"'")
	})
}

// Successful results are cached on the client for 30 minutes.
func TestFetchCachesResults(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "hit %d\n", n)
	}))
	t.Cleanup(srv.Close)

	f := newFetch(srv.URL)
	args := map[string]any{"url": srv.URL + "/data.txt"}
	for i := 1; i <= 2; i++ {
		_, parts := parseResult[fetchMeta](t, f.Call(args))
		if got := partText(t, parts, 0); got != "hit 1" {
			t.Errorf("call %d content = %q, want cached %q", i, got, "hit 1")
		}
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("server hits = %d, want 1", n)
	}

	// A new client has an empty cache, so it fetches the URL again.
	_, parts := parseResult[fetchMeta](t, newFetch(srv.URL).Call(args))
	if got := partText(t, parts, 0); got != "hit 2" {
		t.Errorf("new client content = %q, want %q", got, "hit 2")
	}
}

func TestFetchMissingURL(t *testing.T) {
	for _, args := range []map[string]any{{}, {"url": ""}} {
		meta, parts := parseResult[fetchMeta](t, (&Fetch{}).Call(args))
		checkError(t, meta.Error, "missing url string")
		if len(parts) != 0 {
			t.Errorf("got %d content parts, want none", len(parts))
		}
	}
}

// Text lines longer than 64 KiB aren't supported.
func TestFetchTextLineTooLong(t *testing.T) {
	srv := newFetchServer(t, map[string]fetchPage{
		"/wide.txt": {"text/plain; charset=utf-8", []byte(strings.Repeat("x", 70*1024) + "\n")},
	})
	meta, parts := parseResult[fetchMeta](t, newFetch(srv.URL).Call(map[string]any{"url": srv.URL + "/wide.txt"}))
	checkError(t, meta.Error, "scanning text")
	if len(parts) != 0 {
		t.Errorf("got %d content parts, want none", len(parts))
	}
}

// A PDF that fails to load is reported, and no pages are returned.
func TestFetchInvalidPDF(t *testing.T) {
	srv := newFetchServer(t, map[string]fetchPage{
		"/bad.pdf": {"application/pdf", []byte("%PDF-1.4\nnot a real document\n")},
	})
	meta, parts := parseResult[fetchMeta](t, newFetch(srv.URL).Call(map[string]any{"url": srv.URL + "/bad.pdf"}))
	checkError(t, meta.Error, "load PDF")
	if len(parts) != 0 {
		t.Errorf("got %d content parts, want none", len(parts))
	}
}
