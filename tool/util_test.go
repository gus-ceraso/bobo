package tool

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestArgString(t *testing.T) {
	args := map[string]any{"s": "hello", "n": json.Number("3"), "e": ""}
	tests := []struct {
		name   string
		key    string
		want   string
		wantOK bool
	}{
		{"string value", "s", "hello", true},
		{"empty string value", "e", "", true},
		{"missing key", "missing", "", false},
		{"non-string value", "n", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := argString(args, tt.key)
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("argString(%q) = %q, %v, want %q, %v", tt.key, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestArgInt(t *testing.T) {
	args := map[string]any{
		"pos":   json.Number("42"),
		"neg":   json.Number("-7"),
		"zero":  json.Number("0"),
		"frac":  json.Number("1.5"),
		"word":  json.Number("abc"),
		"float": float64(9),
		"str":   "5",
		"big":   json.Number("99999999999999999999999"),
	}
	tests := []struct {
		name   string
		key    string
		want   int
		wantOK bool
	}{
		{"positive", "pos", 42, true},
		{"negative", "neg", -7, true},
		{"zero is present", "zero", 0, true},
		{"fraction", "frac", 0, false},
		{"non-numeric", "word", 0, false},
		{"plain float64", "float", 0, false},
		{"string", "str", 0, false},
		{"out of int64 range", "big", 0, false},
		{"missing key", "missing", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := argInt(args, tt.key)
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("argInt(%q) = %d, %v, want %d, %v", tt.key, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestEnc(t *testing.T) {
	got := enc(map[string]any{"b": 1, "a": "x"})
	if want := `{"a":"x","b":1}`; got != want {
		t.Errorf("enc = %q, want %q", got, want)
	}
	if got := enc("a<b"); got != `"a\u003cb"` {
		t.Errorf("enc does not escape HTML characters: got %q", got)
	}
}

func TestEncPanicsOnUnencodableValue(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("enc did not panic on a value JSON cannot encode")
		}
	}()
	enc(make(chan int))
}

func TestHasHiddenComponent(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"", false},
		{".", false},
		{"..", false},
		{"a", false},
		{"a/b.txt", false},
		{"./a", false},
		{"../a", false},
		{"a/", false},
		{"a/..", false},
		{"a/b/../c", false},
		{".a", true},
		{"..a", true},
		{"a/.b", true},
		{"a/.b/c", true},
		{"a/.b/", true},
		{"/x/.y", true},
		{"../.a", true},
		{".git/config", true},
	}
	for _, tt := range tests {
		if got := hasHiddenComponent(tt.path); got != tt.want {
			t.Errorf("hasHiddenComponent(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestDetectMediaType(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0x0d}
	tests := []struct {
		name    string
		content []byte
		want    string
	}{
		{"plain text", []byte("hello world\n"), "text/plain; charset=utf-8"},
		{"html", []byte("<!DOCTYPE html><html></html>"), "text/html; charset=utf-8"},
		{"empty", nil, "text/plain; charset=utf-8"},
		{"png", png, "image/png"},
		{"pdf", []byte("%PDF-1.4\n%\xe2\xe3"), "application/pdf"},
		{"binary", []byte{0, 1, 2, 3, 0, 0, 0xff}, "application/octet-stream"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := os.Open(write(tt.name, tt.content))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			got, err := detectMediaType(f)
			if err != nil {
				t.Fatalf("detectMediaType returned error: %v", err)
			}
			if got != tt.want {
				t.Errorf("media type = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDetectMediaTypeRewindsFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f.txt")
	content := []byte("first line\nsecond line\n")
	if err := os.WriteFile(p, content, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := detectMediaType(f); err != nil {
		t.Fatalf("detectMediaType returned error: %v", err)
	}
	got, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("content after detectMediaType = %q, want full content %q", got, content)
	}
}

// failingSeeker fails the failAt-th Seek call and every call after it.
type failingSeeker struct {
	io.ReadSeeker
	seeks  int
	failAt int
}

func (f *failingSeeker) Seek(offset int64, whence int) (int64, error) {
	f.seeks++
	if f.seeks >= f.failAt {
		return 0, errors.New("seek failed")
	}
	return f.ReadSeeker.Seek(offset, whence)
}

func TestDetectMediaTypeSeekErrors(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		f := &failingSeeker{ReadSeeker: bytes.NewReader([]byte("x")), failAt: failAt}
		_, err := detectMediaType(f)
		if err == nil {
			t.Errorf("failAt %d: detectMediaType returned nil error", failAt)
			continue
		}
		if want := "lseek: seek failed"; err.Error() != want {
			t.Errorf("failAt %d: error = %q, want %q", failAt, err.Error(), want)
		}
	}
}

func TestContainsAny(t *testing.T) {
	tests := []struct {
		s    string
		subs []string
		want bool
	}{
		{"text/html", []string{"text/"}, true},
		{"image/png", []string{"text/", "image/png"}, true},
		{"image/gif", []string{"text/", "image/png"}, false},
		{"", []string{"text/"}, false},
		{"anything", nil, false},
	}
	for _, tt := range tests {
		if got := containsAny(tt.s, tt.subs...); got != tt.want {
			t.Errorf("containsAny(%q, %q) = %v, want %v", tt.s, tt.subs, got, tt.want)
		}
	}
}

func TestEncImage(t *testing.T) {
	got := encImage("image/png", []byte{1, 2, 3})
	if want := "data:image/png;base64,AQID"; got != want {
		t.Errorf("encImage = %q, want %q", got, want)
	}
}

func TestPathDescription(t *testing.T) {
	const base = "Path to the file."
	const local = " Must be a relative path inside the working directory."
	const hidden = " Hidden files and directories (names starting with \".\") aren't allowed."
	tests := []struct {
		localFiles, hiddenFiles bool
		want                    string
	}{
		{false, false, base + hidden},
		{true, false, base + local + hidden},
		{false, true, base},
		{true, true, base + local},
	}
	for _, tt := range tests {
		if got := pathDescription(base, tt.localFiles, tt.hiddenFiles); got != tt.want {
			t.Errorf("pathDescription(local=%v, hidden=%v) = %q, want %q", tt.localFiles, tt.hiddenFiles, got, tt.want)
		}
	}
}

func TestArgOptionalPositive(t *testing.T) {
	args := map[string]any{
		"one":  num(1),
		"five": num(5),
		"zero": num(0),
		"neg":  num(-1),
		"str":  "2",
		"frac": json.Number("1.5"),
	}
	tests := []struct {
		name    string
		key     string
		want    int
		wantErr bool
	}{
		{"absent", "missing", 0, false},
		{"one", "one", 1, false},
		{"five", "five", 5, false},
		{"zero", "zero", 0, true},
		{"negative", "neg", 0, true},
		{"string", "str", 0, true},
		{"fraction", "frac", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := argOptionalPositive(args, tt.key)
			if got != tt.want {
				t.Errorf("value = %d, want %d", got, tt.want)
			}
			if tt.wantErr {
				if err == nil {
					t.Fatal("error = nil, want error")
				}
				if want := "invalid arguments: " + tt.key + " must be an integer >= 1"; err.Error() != want {
					t.Errorf("error = %q, want %q", err.Error(), want)
				}
			} else if err != nil {
				t.Errorf("error = %v, want nil", err)
			}
		})
	}
}

// failingReader fails every Read. Seek works as usual.
type failingReader struct {
	io.ReadSeeker
}

func (failingReader) Read([]byte) (int, error) {
	return 0, errors.New("read failed")
}

func TestDetectMediaTypeReadError(t *testing.T) {
	_, err := detectMediaType(failingReader{ReadSeeker: bytes.NewReader([]byte("x"))})
	if err == nil {
		t.Fatal("detectMediaType returned nil error")
	}
	if want := "read: read failed"; err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}
