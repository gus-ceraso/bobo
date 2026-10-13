package tool

import (
	"os"
	"path/filepath"
	"testing"
)

type writeMeta struct {
	Error string `json:"error"`
	Path  string `json:"path"`
}

func TestWriteCreatesFile(t *testing.T) {
	t.Chdir(t.TempDir())
	w := &Write{LocalFiles: true}
	meta, parts := parseResult[writeMeta](t, w.Call(map[string]any{"path": "new.txt", "text": "hello\n"}))
	checkNoError(t, meta.Error)
	if len(parts) != 0 {
		t.Errorf("got %d content parts, want none", len(parts))
	}
	got, err := os.ReadFile("new.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello\n" {
		t.Errorf("file = %q, want %q", got, "hello\n")
	}
}

func TestWriteCreatesParentDirectories(t *testing.T) {
	t.Chdir(t.TempDir())
	w := &Write{LocalFiles: true}
	meta, _ := parseResult[writeMeta](t, w.Call(map[string]any{"path": "a/b/c.txt", "text": "deep"}))
	checkNoError(t, meta.Error)
	got, err := os.ReadFile(filepath.Join("a", "b", "c.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "deep" {
		t.Errorf("file = %q, want %q", got, "deep")
	}
}

func TestWriteReplacesExistingContent(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, "f.txt", []byte("a much longer original content\n"))
	w := &Write{LocalFiles: true}
	meta, _ := parseResult[writeMeta](t, w.Call(map[string]any{"path": "f.txt", "text": "short"}))
	checkNoError(t, meta.Error)
	got, err := os.ReadFile("f.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "short" {
		t.Errorf("file = %q, want %q (no leftover bytes)", got, "short")
	}
}

func TestWriteEmptyTextEmptiesFile(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, "f.txt", []byte("old"))
	w := &Write{LocalFiles: true}
	meta, _ := parseResult[writeMeta](t, w.Call(map[string]any{"path": "f.txt", "text": ""}))
	checkNoError(t, meta.Error)
	got, err := os.ReadFile("f.txt")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("file = %q, want empty", got)
	}
}

func TestWriteMissingPath(t *testing.T) {
	t.Chdir(t.TempDir())
	meta, _ := parseResult[writeMeta](t, (&Write{}).Call(map[string]any{"text": "x"}))
	checkError(t, meta.Error, "missing path")
}

func TestWriteHiddenPath(t *testing.T) {
	t.Chdir(t.TempDir())
	meta, _ := parseResult[writeMeta](t, (&Write{}).Call(map[string]any{"path": ".env", "text": "X=1"}))
	checkError(t, meta.Error, "hidden path not allowed")
	if _, err := os.Stat(".env"); !os.IsNotExist(err) {
		t.Errorf("stat .env = %v, want not exist", err)
	}

	meta, _ = parseResult[writeMeta](t, (&Write{HiddenFiles: true}).Call(map[string]any{"path": ".env", "text": "X=1"}))
	checkNoError(t, meta.Error)
	if got, _ := os.ReadFile(".env"); string(got) != "X=1" {
		t.Errorf(".env = %q, want %q", got, "X=1")
	}
}

func TestWriteLocalFiles(t *testing.T) {
	t.Chdir(t.TempDir())
	outside := filepath.Join(t.TempDir(), "outside.txt")
	w := &Write{LocalFiles: true}
	for _, p := range []string{"../escape.txt", "a/../../escape.txt", outside} {
		meta, _ := parseResult[writeMeta](t, w.Call(map[string]any{"path": p, "text": "x"}))
		checkError(t, meta.Error, "non-local path not allowed")
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Errorf("outside file exists or stat failed: %v", err)
	}
}

func TestWriteToDirectoryFails(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, "dir/inner.txt", []byte("keep"))
	meta, _ := parseResult[writeMeta](t, (&Write{LocalFiles: true}).Call(map[string]any{"path": "dir", "text": "x"}))
	checkError(t, meta.Error, "write:")
	if got, _ := os.ReadFile("dir/inner.txt"); string(got) != "keep" {
		t.Errorf("dir/inner.txt = %q, want %q", got, "keep")
	}
}

// The schema requires text. A call without it is invalid and must not change
// the file, as the other tools reject calls missing required strings.
func TestWriteMissingTextLeavesFileUnchanged(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, "keep.txt", []byte("precious\n"))
	meta, _ := parseResult[writeMeta](t, (&Write{LocalFiles: true}).Call(map[string]any{"path": "keep.txt"}))
	if meta.Error == "" {
		t.Error("error = none, want an invalid arguments error for missing text")
	}
	got, err := os.ReadFile("keep.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "precious\n" {
		t.Errorf("file = %q, want unchanged %q", got, "precious\n")
	}
}

func TestWriteNonStringTextRejected(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, "keep.txt", []byte("precious\n"))
	meta, _ := parseResult[writeMeta](t, (&Write{LocalFiles: true}).Call(map[string]any{"path": "keep.txt", "text": 5}))
	checkError(t, meta.Error, "missing text string")
	got, err := os.ReadFile("keep.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "precious\n" {
		t.Errorf("file = %q, want unchanged %q", got, "precious\n")
	}
}

// A parent that is a regular file makes mkdir fail, and the file is left alone.
func TestWriteParentIsFile(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, "blocker", []byte("file"))
	meta, _ := parseResult[writeMeta](t, (&Write{LocalFiles: true}).Call(map[string]any{"path": "blocker/x.txt", "text": "x"}))
	checkError(t, meta.Error, "mkdir -p:")
	if got, _ := os.ReadFile("blocker"); string(got) != "file" {
		t.Errorf("blocker = %q, want unchanged", got)
	}
}
