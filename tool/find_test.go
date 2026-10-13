package tool

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

type findMeta struct {
	Error   string `json:"error"`
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
	Offset  int    `json:"offset"`
	Limit   int    `json:"limit"`
	Total   int    `json:"total"`
}

// makeFindTree creates the tree used by the find tests in the current
// directory.
func makeFindTree(t *testing.T) {
	t.Helper()
	for _, f := range []string{
		"a.go",
		"b.txt",
		"sub/c.go",
		"sub/deep/d.go",
		"x/.hidden.go",
		".secret/e.go",
	} {
		writeTestFile(t, f, []byte("x\n"))
	}
}

func findPaths(t *testing.T, parts []map[string]any) []string {
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

func TestFindMissingPattern(t *testing.T) {
	t.Chdir(t.TempDir())
	meta, parts := parseResult[findMeta](t, (&Find{}).Call(map[string]any{}))
	checkError(t, meta.Error, "missing regular expression pattern")
	if len(parts) != 0 {
		t.Errorf("got %d content parts, want none", len(parts))
	}
}

func TestFindInvalidPattern(t *testing.T) {
	t.Chdir(t.TempDir())
	meta, parts := parseResult[findMeta](t, (&Find{}).Call(map[string]any{"pattern": "("}))
	checkError(t, meta.Error, "compile regular expression pattern")
	if len(parts) != 0 {
		t.Errorf("got %d content parts, want none", len(parts))
	}
}

func TestFindPathRestrictions(t *testing.T) {
	t.Chdir(t.TempDir())
	makeFindTree(t)

	t.Run("hidden path rejected", func(t *testing.T) {
		meta, _ := parseResult[findMeta](t, (&Find{}).Call(map[string]any{"pattern": ".", "path": ".secret"}))
		checkError(t, meta.Error, "hidden path not allowed")
	})
	t.Run("hidden path allowed", func(t *testing.T) {
		meta, parts := parseResult[findMeta](t, (&Find{HiddenFiles: true}).Call(map[string]any{"pattern": ".", "path": ".secret"}))
		checkNoError(t, meta.Error)
		if got := findPaths(t, parts); len(got) != 1 || got[0] != "e.go" {
			t.Errorf("matches = %q, want [e.go]", got)
		}
	})
	t.Run("non-local path rejected", func(t *testing.T) {
		meta, _ := parseResult[findMeta](t, (&Find{LocalFiles: true}).Call(map[string]any{"pattern": ".", "path": ".."}))
		checkError(t, meta.Error, "non-local path not allowed")
	})
	t.Run("absolute path rejected when local", func(t *testing.T) {
		meta, _ := parseResult[findMeta](t, (&Find{LocalFiles: true}).Call(map[string]any{"pattern": ".", "path": os.TempDir()}))
		checkError(t, meta.Error, "non-local path not allowed")
	})
}

func TestFindMissingAndNonDirectoryPath(t *testing.T) {
	t.Chdir(t.TempDir())
	makeFindTree(t)
	r := &Find{LocalFiles: true}

	meta, _ := parseResult[findMeta](t, r.Call(map[string]any{"pattern": ".", "path": "nope"}))
	checkError(t, meta.Error, "stat:")

	meta, _ = parseResult[findMeta](t, r.Call(map[string]any{"pattern": ".", "path": "a.go"}))
	checkError(t, meta.Error, "is not a directory")
}

func TestFindMatchesInLexicalOrder(t *testing.T) {
	t.Chdir(t.TempDir())
	makeFindTree(t)
	meta, parts := parseResult[findMeta](t, (&Find{LocalFiles: true}).Call(map[string]any{"pattern": `\.go$`}))
	checkNoError(t, meta.Error)
	if meta.Total != 3 {
		t.Errorf("total = %d, want 3", meta.Total)
	}
	want := []string{"a.go", "sub/c.go", "sub/deep/d.go"}
	if got := findPaths(t, parts); !equalStrings(got, want) {
		t.Errorf("matches = %q, want %q", got, want)
	}
}

func TestFindIncludesHiddenWhenAllowed(t *testing.T) {
	t.Chdir(t.TempDir())
	makeFindTree(t)
	meta, parts := parseResult[findMeta](t, (&Find{HiddenFiles: true}).Call(map[string]any{"pattern": `\.go$`}))
	checkNoError(t, meta.Error)
	want := []string{".secret/e.go", "a.go", "sub/c.go", "sub/deep/d.go", "x/.hidden.go"}
	if got := findPaths(t, parts); !equalStrings(got, want) {
		t.Errorf("matches = %q, want %q", got, want)
	}
	if meta.Total != 5 {
		t.Errorf("total = %d, want 5", meta.Total)
	}
}

func TestFindPatternMatchesRelativePath(t *testing.T) {
	t.Chdir(t.TempDir())
	makeFindTree(t)
	r := &Find{LocalFiles: true}

	meta, parts := parseResult[findMeta](t, r.Call(map[string]any{"pattern": `^sub/`}))
	checkNoError(t, meta.Error)
	if want := []string{"sub/c.go", "sub/deep/d.go"}; !equalStrings(findPaths(t, parts), want) {
		t.Errorf("matches = %q, want %q", findPaths(t, parts), want)
	}

	// Directories are never listed, even when their names match.
	meta, parts = parseResult[findMeta](t, r.Call(map[string]any{"pattern": `^sub$`}))
	checkNoError(t, meta.Error)
	if got := findPaths(t, parts); len(got) != 0 || meta.Total != 0 {
		t.Errorf("matches = %q, total = %d, want none", got, meta.Total)
	}
}

func TestFindSearchesGivenDirectory(t *testing.T) {
	t.Chdir(t.TempDir())
	makeFindTree(t)
	meta, parts := parseResult[findMeta](t, (&Find{LocalFiles: true}).Call(map[string]any{"pattern": `\.go$`, "path": "sub"}))
	checkNoError(t, meta.Error)
	if meta.Path != "sub" {
		t.Errorf("path = %q, want sub", meta.Path)
	}
	want := []string{"c.go", "deep/d.go"}
	if got := findPaths(t, parts); !equalStrings(got, want) {
		t.Errorf("matches = %q, want %q (relative to search directory)", got, want)
	}
}

func TestFindSymlinksListedNotFollowed(t *testing.T) {
	t.Chdir(t.TempDir())
	makeFindTree(t)
	if err := os.Symlink("a.go", "link.go"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("sub", "ldir"); err != nil {
		t.Fatal(err)
	}
	r := &Find{LocalFiles: true}

	meta, parts := parseResult[findMeta](t, r.Call(map[string]any{"pattern": `^link\.go$`}))
	checkNoError(t, meta.Error)
	if want := []string{"link.go"}; !equalStrings(findPaths(t, parts), want) {
		t.Errorf("matches = %q, want %q", findPaths(t, parts), want)
	}

	meta, parts = parseResult[findMeta](t, r.Call(map[string]any{"pattern": `^ldir`}))
	checkNoError(t, meta.Error)
	if want := []string{"ldir"}; !equalStrings(findPaths(t, parts), want) {
		t.Errorf("matches = %q, want %q (symlink listed, target not walked)", findPaths(t, parts), want)
	}
}

func TestFindPaging(t *testing.T) {
	t.Chdir(t.TempDir())
	makeFindTree(t)
	r := &Find{LocalFiles: true}
	tests := []struct {
		name   string
		offset int
		limit  int
		want   []string
	}{
		{"first two", 1, 2, []string{"a.go", "sub/c.go"}},
		{"from second", 2, 10, []string{"sub/c.go", "sub/deep/d.go"}},
		{"past end", 4, 10, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := map[string]any{"pattern": `\.go$`, "offset": num(tt.offset), "limit": num(tt.limit)}
			meta, parts := parseResult[findMeta](t, r.Call(args))
			checkNoError(t, meta.Error)
			if meta.Total != 3 {
				t.Errorf("total = %d, want 3", meta.Total)
			}
			if got := findPaths(t, parts); !equalStrings(got, tt.want) {
				t.Errorf("matches = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFindDefaultLimit(t *testing.T) {
	t.Chdir(t.TempDir())
	for i := 0; i < 1005; i++ {
		writeTestFile(t, fmt.Sprintf("f%04d.txt", i), []byte(""))
	}
	meta, parts := parseResult[findMeta](t, (&Find{LocalFiles: true}).Call(map[string]any{"pattern": `\.txt$`}))
	checkNoError(t, meta.Error)
	if meta.Total != 1005 {
		t.Errorf("total = %d, want 1005", meta.Total)
	}
	if got := findPaths(t, parts); len(got) != 1000 {
		t.Errorf("returned %d matches, want 1000", len(got))
	}
}

func TestFindEmptyDirectory(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.Mkdir("empty", 0o755); err != nil {
		t.Fatal(err)
	}
	meta, parts := parseResult[findMeta](t, (&Find{LocalFiles: true}).Call(map[string]any{"pattern": ".", "path": "empty"}))
	checkNoError(t, meta.Error)
	if meta.Total != 0 {
		t.Errorf("total = %d, want 0", meta.Total)
	}
	if got := findPaths(t, parts); len(got) != 0 {
		t.Errorf("matches = %q, want none", got)
	}
}

func TestFindOffsetAndLimitMustBePositive(t *testing.T) {
	t.Chdir(t.TempDir())
	makeFindTree(t)
	r := &Find{LocalFiles: true}
	for _, key := range []string{"offset", "limit"} {
		for _, v := range []any{num(0), num(-1), "2"} {
			t.Run(fmt.Sprintf("%s=%v", key, v), func(t *testing.T) {
				meta, parts := parseResult[findMeta](t, r.Call(map[string]any{"pattern": `\.go$`, key: v}))
				checkError(t, meta.Error, key+" must be an integer >= 1")
				if len(parts) != 0 {
					t.Errorf("got %d content parts, want none", len(parts))
				}
			})
		}
	}
}
