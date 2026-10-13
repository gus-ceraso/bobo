package tool

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

type readMeta struct {
	Error     string `json:"error"`
	Path      string `json:"path"`
	MediaType string `json:"media_type"`
	Offset    int    `json:"offset"`
	Limit     int    `json:"limit"`
	Total     int    `json:"total"`
}

func TestReadMissingPath(t *testing.T) {
	t.Chdir(t.TempDir())
	meta, parts := parseResult[readMeta](t, (&Read{}).Call(map[string]any{}))
	checkError(t, meta.Error, "missing path")
	if len(parts) != 0 {
		t.Errorf("got %d content parts, want none", len(parts))
	}
}

func TestReadHiddenPathNotAllowed(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, ".env", []byte("SECRET=1\n"))
	writeTestFile(t, "dir/.cache/x.txt", []byte("x\n"))
	for _, p := range []string{".env", "./.env", "dir/.cache/x.txt", "."} {
		t.Run(p, func(t *testing.T) {
			r := &Read{}
			meta, parts := parseResult[readMeta](t, r.Call(map[string]any{"path": p}))
			if p == "." {
				checkNoError(t, meta.Error)
				return
			}
			checkError(t, meta.Error, "hidden path not allowed")
			if len(parts) != 0 {
				t.Errorf("got %d content parts, want none", len(parts))
			}
		})
	}
}

func TestReadHiddenPathAllowed(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, ".env", []byte("SECRET=1\n"))
	r := &Read{HiddenFiles: true}
	meta, parts := parseResult[readMeta](t, r.Call(map[string]any{"path": ".env"}))
	checkNoError(t, meta.Error)
	if got := partText(t, parts, 0); got != "SECRET=1" {
		t.Errorf("content = %q, want %q", got, "SECRET=1")
	}
}

func TestReadLocalFiles(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, "f.txt", []byte("hi\n"))
	outside := filepath.Join(t.TempDir(), "outside.txt")
	writeTestFile(t, outside, []byte("outside\n"))

	r := &Read{LocalFiles: true}
	for _, p := range []string{"/etc/hostname", outside, "../f.txt", "a/../../f.txt"} {
		t.Run("rejected "+p, func(t *testing.T) {
			meta, _ := parseResult[readMeta](t, r.Call(map[string]any{"path": p}))
			checkError(t, meta.Error, "non-local path not allowed")
		})
	}

	if err := os.Mkdir("sub", 0o755); err != nil {
		t.Fatal(err)
	}
	meta, parts := parseResult[readMeta](t, r.Call(map[string]any{"path": "sub/../f.txt"}))
	checkNoError(t, meta.Error)
	if got := partText(t, parts, 0); got != "hi" {
		t.Errorf("content = %q, want %q", got, "hi")
	}

	r = &Read{}
	meta, parts = parseResult[readMeta](t, r.Call(map[string]any{"path": outside}))
	checkNoError(t, meta.Error)
	if got := partText(t, parts, 0); got != "outside" {
		t.Errorf("content = %q, want %q", got, "outside")
	}
}

func TestReadMissingFile(t *testing.T) {
	t.Chdir(t.TempDir())
	meta, parts := parseResult[readMeta](t, (&Read{LocalFiles: true}).Call(map[string]any{"path": "nope.txt"}))
	checkError(t, meta.Error, "open:")
	if len(parts) != 0 {
		t.Errorf("got %d content parts, want none", len(parts))
	}
}

func TestReadTextFile(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, "notes.txt", []byte("alpha\nbeta\ngamma\n"))
	r := &Read{LocalFiles: true}
	meta, parts := parseResult[readMeta](t, r.Call(map[string]any{"path": "notes.txt"}))
	checkNoError(t, meta.Error)
	if meta.MediaType != "text/plain; charset=utf-8" {
		t.Errorf("media_type = %q, want text/plain; charset=utf-8", meta.MediaType)
	}
	if meta.Total != 3 {
		t.Errorf("total = %d, want 3", meta.Total)
	}
	if meta.Offset != 1 || meta.Limit != 1000 {
		t.Errorf("offset, limit = %d, %d, want 1, 1000", meta.Offset, meta.Limit)
	}
	if len(parts) != 1 {
		t.Fatalf("got %d content parts, want 1", len(parts))
	}
	if got := partText(t, parts, 0); got != "alpha\nbeta\ngamma" {
		t.Errorf("content = %q, want %q", got, "alpha\nbeta\ngamma")
	}
}

func TestReadTextPaging(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, "four.txt", []byte("one\ntwo\nthree\nfour\n"))
	tests := []struct {
		name    string
		offset  int
		limit   int
		want    string
		wantOff int
	}{
		{"middle lines", 2, 2, "two\nthree", 2},
		{"limit one", 1, 1, "one", 1},
		{"offset on last line", 4, 10, "four", 4},
		{"offset past end", 5, 10, "", 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := map[string]any{"path": "four.txt", "offset": num(tt.offset), "limit": num(tt.limit)}
			meta, parts := parseResult[readMeta](t, (&Read{LocalFiles: true}).Call(args))
			checkNoError(t, meta.Error)
			if meta.Total != 4 {
				t.Errorf("total = %d, want 4", meta.Total)
			}
			if meta.Offset != tt.wantOff || meta.Limit != tt.limit {
				t.Errorf("offset, limit = %d, %d, want %d, %d", meta.Offset, meta.Limit, tt.wantOff, tt.limit)
			}
			if got := partText(t, parts, 0); got != tt.want {
				t.Errorf("content = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReadTextLineEdgeCases(t *testing.T) {
	t.Chdir(t.TempDir())
	tests := []struct {
		name    string
		content string
		want    string
		wantTot int
	}{
		{"empty file", "", "", 0},
		{"no trailing newline", "a\nb", "a\nb", 2},
		{"blank line kept", "a\n\nb\n", "a\n\nb", 3},
		{"CRLF line endings", "a\r\nb\r\n", "a\nb", 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writeTestFile(t, "f.txt", []byte(tt.content))
			meta, parts := parseResult[readMeta](t, (&Read{LocalFiles: true}).Call(map[string]any{"path": "f.txt"}))
			checkNoError(t, meta.Error)
			if meta.Total != tt.wantTot {
				t.Errorf("total = %d, want %d", meta.Total, tt.wantTot)
			}
			if got := partText(t, parts, 0); got != tt.want {
				t.Errorf("content = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReadTextDefaultLimit(t *testing.T) {
	t.Chdir(t.TempDir())
	var b strings.Builder
	for i := 1; i <= 1005; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	writeTestFile(t, "long.txt", []byte(b.String()))
	meta, parts := parseResult[readMeta](t, (&Read{LocalFiles: true}).Call(map[string]any{"path": "long.txt"}))
	checkNoError(t, meta.Error)
	if meta.Total != 1005 {
		t.Errorf("total = %d, want 1005", meta.Total)
	}
	if got := len(strings.Split(partText(t, parts, 0), "\n")); got != 1000 {
		t.Errorf("returned %d lines, want 1000", got)
	}
}

func TestReadTextLineTooLong(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, "wide.txt", []byte(strings.Repeat("x", 70*1024)+"\n"))
	meta, parts := parseResult[readMeta](t, (&Read{LocalFiles: true}).Call(map[string]any{"path": "wide.txt"}))
	checkError(t, meta.Error, "scanning text")
	if len(parts) != 0 {
		t.Errorf("got %d content parts, want none", len(parts))
	}
}

func TestReadBinaryFileNotSupported(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, "data.bin", []byte{0, 1, 2, 3, 0, 0, 0xff})
	meta, parts := parseResult[readMeta](t, (&Read{LocalFiles: true}).Call(map[string]any{"path": "data.bin"}))
	checkError(t, meta.Error, "file media type not supported")
	if meta.MediaType != "application/octet-stream" {
		t.Errorf("media_type = %q, want application/octet-stream", meta.MediaType)
	}
	if len(parts) != 0 {
		t.Errorf("got %d content parts, want none", len(parts))
	}
}

func TestReadImages(t *testing.T) {
	t.Chdir(t.TempDir())
	tests := []struct {
		name      string
		content   []byte
		mediaType string
	}{
		{"png", []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), "image/png"},
		{"jpeg", []byte("\xff\xd8\xff\xe0\x00\x10JFIF"), "image/jpeg"},
		{"webp", []byte("RIFF\x24\x00\x00\x00WEBPVP8 "), "image/webp"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writeTestFile(t, "pic", tt.content)
			args := map[string]any{"path": "pic", "offset": num(7), "limit": num(2)}
			meta, parts := parseResult[readMeta](t, (&Read{LocalFiles: true}).Call(args))
			checkNoError(t, meta.Error)
			if meta.MediaType != tt.mediaType {
				t.Errorf("media_type = %q, want %q", meta.MediaType, tt.mediaType)
			}
			if len(parts) != 1 {
				t.Fatalf("got %d content parts, want 1", len(parts))
			}
			if got := decodeImagePart(t, parts[0], tt.mediaType); !bytes.Equal(got, tt.content) {
				t.Errorf("image bytes = %q, want %q", got, tt.content)
			}
		})
	}
}

func TestReadPDFPages(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, "doc.pdf", minimalPDF(3))
	r := &Read{LocalFiles: true}

	meta, parts := parseResult[readMeta](t, r.Call(map[string]any{"path": "doc.pdf"}))
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

	meta, parts = parseResult[readMeta](t, r.Call(map[string]any{"path": "doc.pdf", "offset": num(2), "limit": num(1)}))
	checkNoError(t, meta.Error)
	if meta.Total != 3 {
		t.Errorf("total = %d, want 3", meta.Total)
	}
	if len(parts) != 1 {
		t.Fatalf("got %d page images, want 1", len(parts))
	}

	meta, parts = parseResult[readMeta](t, r.Call(map[string]any{"path": "doc.pdf", "offset": num(4)}))
	checkNoError(t, meta.Error)
	if meta.Total != 3 || len(parts) != 0 {
		t.Errorf("offset past last page: total = %d, images = %d, want 3, 0", meta.Total, len(parts))
	}
}

func TestReadPDFDefaultLimit(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, "doc.pdf", minimalPDF(7))
	meta, parts := parseResult[readMeta](t, (&Read{LocalFiles: true}).Call(map[string]any{"path": "doc.pdf"}))
	checkNoError(t, meta.Error)
	if meta.Total != 7 {
		t.Errorf("total = %d, want 7", meta.Total)
	}
	if len(parts) != 5 {
		t.Errorf("got %d page images, want 5", len(parts))
	}
}

func TestReadDirectory(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, "a.txt", []byte("a"))
	writeTestFile(t, "c.go", []byte("c"))
	writeTestFile(t, "b/inner.txt", []byte("x"))
	if err := os.Mkdir("empty", 0o755); err != nil {
		t.Fatal(err)
	}
	r := &Read{LocalFiles: true}

	meta, parts := parseResult[readMeta](t, r.Call(map[string]any{"path": "."}))
	checkNoError(t, meta.Error)
	if meta.MediaType != "inode/directory" {
		t.Errorf("media_type = %q, want inode/directory", meta.MediaType)
	}
	if meta.Total != 4 {
		t.Errorf("total = %d, want 4", meta.Total)
	}
	if len(parts) != 1 {
		t.Fatalf("got %d content parts, want 1", len(parts))
	}
	got := strings.Split(partText(t, parts, 0), "\n")
	sort.Strings(got)
	want := []string{"a.txt", "b/", "c.go", "empty/"}
	if !equalStrings(got, want) {
		t.Errorf("entries = %q, want %q", got, want)
	}

	// Paging through one entry at a time must visit every entry exactly once.
	var seen []string
	for off := 1; off <= 4; off++ {
		meta, parts := parseResult[readMeta](t, r.Call(map[string]any{"path": ".", "offset": num(off), "limit": num(1)}))
		checkNoError(t, meta.Error)
		if meta.Total != 4 {
			t.Errorf("offset %d: total = %d, want 4", off, meta.Total)
		}
		if len(parts) != 1 {
			t.Fatalf("offset %d: got %d content parts, want 1", off, len(parts))
		}
		seen = append(seen, partText(t, parts, 0))
	}
	sort.Strings(seen)
	if !equalStrings(seen, want) {
		t.Errorf("paged entries = %q, want %q", seen, want)
	}
}

func TestReadEmptyDirectory(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.Mkdir("d", 0o755); err != nil {
		t.Fatal(err)
	}
	meta, parts := parseResult[readMeta](t, (&Read{LocalFiles: true}).Call(map[string]any{"path": "d"}))
	checkNoError(t, meta.Error)
	if meta.Total != 0 {
		t.Errorf("total = %d, want 0", meta.Total)
	}
	if got := partText(t, parts, 0); got != "" {
		t.Errorf("content = %q, want empty", got)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestReadOffsetAndLimitMustBePositive(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, "f.txt", []byte("one\ntwo\n"))
	r := &Read{LocalFiles: true}
	for _, key := range []string{"offset", "limit"} {
		for _, v := range []any{num(0), num(-1), "2"} {
			t.Run(fmt.Sprintf("%s=%v", key, v), func(t *testing.T) {
				meta, parts := parseResult[readMeta](t, r.Call(map[string]any{"path": "f.txt", key: v}))
				checkError(t, meta.Error, key+" must be an integer >= 1")
				if len(parts) != 0 {
					t.Errorf("got %d content parts, want none", len(parts))
				}
			})
		}
	}
}

// Directory listings leave out hidden entries, as find and grep do, unless
// HiddenFiles is set.
func TestReadDirectoryListingOmitsHiddenEntries(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, "a.txt", []byte("a"))
	writeTestFile(t, ".env", []byte("A=1"))
	writeTestFile(t, ".cache/x.txt", []byte("x"))

	meta, parts := parseResult[readMeta](t, (&Read{LocalFiles: true}).Call(map[string]any{"path": "."}))
	checkNoError(t, meta.Error)
	if got := strings.Split(partText(t, parts, 0), "\n"); !equalStrings(got, []string{"a.txt"}) {
		t.Errorf("entries = %q, want %q", got, []string{"a.txt"})
	}

	meta, parts = parseResult[readMeta](t, (&Read{LocalFiles: true, HiddenFiles: true}).Call(map[string]any{"path": "."}))
	checkNoError(t, meta.Error)
	got := strings.Split(partText(t, parts, 0), "\n")
	sort.Strings(got)
	if want := []string{".cache/", ".env", "a.txt"}; !equalStrings(got, want) {
		t.Errorf("entries with HiddenFiles = %q, want %q", got, want)
	}
}

// Symlinks are followed, so a symlink to a regular file can be read.
func TestReadFollowsSymlinkToFile(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, "target.txt", []byte("hello\n"))
	mustSymlink(t, "target.txt", "link.txt")
	meta, parts := parseResult[readMeta](t, (&Read{LocalFiles: true}).Call(map[string]any{"path": "link.txt"}))
	checkNoError(t, meta.Error)
	if got := partText(t, parts, 0); got != "hello" {
		t.Errorf("content = %q, want %q", got, "hello")
	}
}

// A file with a PDF header that isn't a valid PDF fails to load.
func TestReadInvalidPDF(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, "bad.pdf", []byte("%PDF-1.4\nnot a real document\n"))
	meta, parts := parseResult[readMeta](t, (&Read{LocalFiles: true}).Call(map[string]any{"path": "bad.pdf"}))
	checkError(t, meta.Error, "load PDF")
	if len(parts) != 0 {
		t.Errorf("got %d content parts, want none", len(parts))
	}
}
