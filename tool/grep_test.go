package tool

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

type grepMeta struct {
	Error       string `json:"error"`
	Pattern     string `json:"pattern"`
	Path        string `json:"path"`
	PathPattern string `json:"path_pattern"`
	Offset      int    `json:"offset"`
	Limit       int    `json:"limit"`
	Total       int    `json:"total"`
}

// grepLines returns the match lines of a grep result, or nil if there are none.
func grepLines(t *testing.T, parts []map[string]any) []string {
	t.Helper()
	if len(parts) != 1 {
		t.Fatalf("got %d content parts, want 1", len(parts))
	}
	content := partText(t, parts, 0)
	if content == "" {
		return nil
	}
	return strings.Split(content, "\n")
}

// makeGrepTree creates the tree used by the grep tests in the current
// directory.
func makeGrepTree(t *testing.T) {
	t.Helper()
	writeTestFile(t, "a.txt", []byte("foo one\nbar\nfoo two\n"))
	writeTestFile(t, "sub/b.go", []byte("package x\n// foo\n"))
	writeTestFile(t, "sub/deep/c.txt", []byte("nothing here\n"))
	writeTestFile(t, ".hidden.txt", []byte("foo hidden\n"))
	writeTestFile(t, "sub/.cache/d.txt", []byte("foo cached\n"))
}

func TestGrepMissingPattern(t *testing.T) {
	t.Chdir(t.TempDir())
	meta, parts := parseResult[grepMeta](t, (&Grep{}).Call(map[string]any{}))
	checkError(t, meta.Error, "missing regular expression pattern")
	if len(parts) != 0 {
		t.Errorf("got %d content parts, want none", len(parts))
	}
}

func TestGrepInvalidPatterns(t *testing.T) {
	t.Chdir(t.TempDir())
	r := &Grep{}
	meta, _ := parseResult[grepMeta](t, r.Call(map[string]any{"pattern": "["}))
	checkError(t, meta.Error, "compile regular expression pattern")

	meta, _ = parseResult[grepMeta](t, r.Call(map[string]any{"pattern": "x", "path_pattern": "("}))
	checkError(t, meta.Error, "compile regular expression path pattern")
}

func TestGrepPathRestrictions(t *testing.T) {
	t.Chdir(t.TempDir())
	makeGrepTree(t)

	meta, _ := parseResult[grepMeta](t, (&Grep{}).Call(map[string]any{"pattern": "foo", "path": ".cache"}))
	checkError(t, meta.Error, "hidden path not allowed")

	meta, parts := parseResult[grepMeta](t, (&Grep{HiddenFiles: true}).Call(map[string]any{"pattern": "foo", "path": "sub/.cache"}))
	checkNoError(t, meta.Error)
	if want := []string{"d.txt:1:foo cached"}; !equalStrings(grepLines(t, parts), want) {
		t.Errorf("matches = %q, want %q", grepLines(t, parts), want)
	}

	r := &Grep{LocalFiles: true}
	meta, _ = parseResult[grepMeta](t, r.Call(map[string]any{"pattern": "foo", "path": "../"}))
	checkError(t, meta.Error, "non-local path not allowed")

	meta, _ = parseResult[grepMeta](t, r.Call(map[string]any{"pattern": "foo", "path": "nope"}))
	checkError(t, meta.Error, "stat:")

	meta, _ = parseResult[grepMeta](t, r.Call(map[string]any{"pattern": "foo", "path": "a.txt"}))
	checkError(t, meta.Error, "is not a directory")
}

func TestGrepOutputFormatAndOrder(t *testing.T) {
	t.Chdir(t.TempDir())
	makeGrepTree(t)
	meta, parts := parseResult[grepMeta](t, (&Grep{LocalFiles: true}).Call(map[string]any{"pattern": "foo"}))
	checkNoError(t, meta.Error)
	want := []string{
		"a.txt:1:foo one",
		"a.txt:3:foo two",
		"sub/b.go:2:// foo",
	}
	if got := grepLines(t, parts); !equalStrings(got, want) {
		t.Errorf("matches = %q, want %q", got, want)
	}
	if meta.Total != 3 {
		t.Errorf("total = %d, want 3 (matching lines, not files)", meta.Total)
	}
}

func TestGrepSearchesGivenDirectoryWithRelativePaths(t *testing.T) {
	t.Chdir(t.TempDir())
	makeGrepTree(t)
	meta, parts := parseResult[grepMeta](t, (&Grep{LocalFiles: true}).Call(map[string]any{"pattern": "foo", "path": "sub"}))
	checkNoError(t, meta.Error)
	if want := []string{"b.go:2:// foo"}; !equalStrings(grepLines(t, parts), want) {
		t.Errorf("matches = %q, want %q", grepLines(t, parts), want)
	}
}

func TestGrepPathPattern(t *testing.T) {
	t.Chdir(t.TempDir())
	makeGrepTree(t)
	r := &Grep{LocalFiles: true}

	meta, parts := parseResult[grepMeta](t, r.Call(map[string]any{"pattern": "foo", "path_pattern": `\.go$`}))
	checkNoError(t, meta.Error)
	if want := []string{"sub/b.go:2:// foo"}; !equalStrings(grepLines(t, parts), want) {
		t.Errorf("matches = %q, want %q", grepLines(t, parts), want)
	}

	// path_pattern is matched against paths relative to the search directory.
	meta, parts = parseResult[grepMeta](t, r.Call(map[string]any{"pattern": "foo", "path": "sub", "path_pattern": `^deep/`}))
	checkNoError(t, meta.Error)
	if got := grepLines(t, parts); len(got) != 0 || meta.Total != 0 {
		t.Errorf("matches = %q, total = %d, want none", got, meta.Total)
	}
}

func TestGrepPaging(t *testing.T) {
	t.Chdir(t.TempDir())
	makeGrepTree(t)
	r := &Grep{LocalFiles: true}
	tests := []struct {
		name   string
		offset int
		limit  int
		want   []string
	}{
		{"first", 1, 1, []string{"a.txt:1:foo one"}},
		{"last two", 2, 10, []string{"a.txt:3:foo two", "sub/b.go:2:// foo"}},
		{"past end", 4, 10, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := map[string]any{"pattern": "foo", "offset": num(tt.offset), "limit": num(tt.limit)}
			meta, parts := parseResult[grepMeta](t, r.Call(args))
			checkNoError(t, meta.Error)
			if meta.Total != 3 {
				t.Errorf("total = %d, want 3", meta.Total)
			}
			if got := grepLines(t, parts); !equalStrings(got, tt.want) {
				t.Errorf("matches = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGrepDefaultLimit(t *testing.T) {
	t.Chdir(t.TempDir())
	var b strings.Builder
	for i := 0; i < 1005; i++ {
		fmt.Fprintf(&b, "match %d\n", i)
	}
	writeTestFile(t, "many.txt", []byte(b.String()))
	meta, parts := parseResult[grepMeta](t, (&Grep{LocalFiles: true}).Call(map[string]any{"pattern": "match"}))
	checkNoError(t, meta.Error)
	if meta.Total != 1005 {
		t.Errorf("total = %d, want 1005", meta.Total)
	}
	if got := grepLines(t, parts); len(got) != 1000 {
		t.Errorf("returned %d matches, want 1000", len(got))
	}
}

func TestGrepSkipsNonTextFiles(t *testing.T) {
	t.Chdir(t.TempDir())
	// A NUL byte makes the content binary, so the pattern must not match.
	writeTestFile(t, "bin.dat", []byte("foo\x00foo\n"))
	// PNG signature followed by the pattern.
	writeTestFile(t, "pic.png", append([]byte("\x89PNG\r\n\x1a\n"), []byte("foo\n")...))
	writeTestFile(t, "ok.txt", []byte("foo\n"))

	meta, parts := parseResult[grepMeta](t, (&Grep{LocalFiles: true}).Call(map[string]any{"pattern": "foo"}))
	checkNoError(t, meta.Error)
	if want := []string{"ok.txt:1:foo"}; !equalStrings(grepLines(t, parts), want) {
		t.Errorf("matches = %q, want %q", grepLines(t, parts), want)
	}
}

func TestGrepSkipsFilesWithLongLines(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, "wide.txt", []byte("foo short\n"+strings.Repeat("x", 70*1024)+"\nfoo again\n"))
	writeTestFile(t, "ok.txt", []byte("foo\n"))
	meta, parts := parseResult[grepMeta](t, (&Grep{LocalFiles: true}).Call(map[string]any{"pattern": "foo"}))
	checkNoError(t, meta.Error)
	if want := []string{"ok.txt:1:foo"}; !equalStrings(grepLines(t, parts), want) {
		t.Errorf("matches = %q, want %q (whole file with long line skipped)", grepLines(t, parts), want)
	}
}

func TestGrepSkipsSymlinks(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, "real.txt", []byte("foo\n"))
	if err := os.Symlink("real.txt", "link.txt"); err != nil {
		t.Fatal(err)
	}
	meta, parts := parseResult[grepMeta](t, (&Grep{LocalFiles: true}).Call(map[string]any{"pattern": "foo"}))
	checkNoError(t, meta.Error)
	if want := []string{"real.txt:1:foo"}; !equalStrings(grepLines(t, parts), want) {
		t.Errorf("matches = %q, want %q", grepLines(t, parts), want)
	}
}

func TestGrepCRLFLinesMatchWithoutCR(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, "crlf.txt", []byte("foo\r\nbar\r\n"))
	meta, parts := parseResult[grepMeta](t, (&Grep{LocalFiles: true}).Call(map[string]any{"pattern": "foo$"}))
	checkNoError(t, meta.Error)
	if want := []string{"crlf.txt:1:foo"}; !equalStrings(grepLines(t, parts), want) {
		t.Errorf("matches = %q, want %q", grepLines(t, parts), want)
	}
}

func TestGrepNoMatches(t *testing.T) {
	t.Chdir(t.TempDir())
	makeGrepTree(t)
	meta, parts := parseResult[grepMeta](t, (&Grep{LocalFiles: true}).Call(map[string]any{"pattern": "zzz"}))
	checkNoError(t, meta.Error)
	if meta.Total != 0 || len(grepLines(t, parts)) != 0 {
		t.Errorf("total = %d, want 0 with no matches", meta.Total)
	}
}

func TestGrepOffsetAndLimitMustBePositive(t *testing.T) {
	t.Chdir(t.TempDir())
	makeGrepTree(t)
	r := &Grep{LocalFiles: true}
	for _, key := range []string{"offset", "limit"} {
		for _, v := range []any{num(0), num(-1), "2"} {
			t.Run(fmt.Sprintf("%s=%v", key, v), func(t *testing.T) {
				meta, parts := parseResult[grepMeta](t, r.Call(map[string]any{"pattern": "foo", key: v}))
				checkError(t, meta.Error, key+" must be an integer >= 1")
				if len(parts) != 0 {
					t.Errorf("got %d content parts, want none", len(parts))
				}
			})
		}
	}
}
