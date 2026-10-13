package tool

import (
	"os"
	"path/filepath"
	"testing"
)

type removeMeta struct {
	Error string `json:"error"`
	Path  string `json:"path"`
}

func TestRemoveMissingPath(t *testing.T) {
	t.Chdir(t.TempDir())
	tests := []struct {
		name string
		args map[string]any
	}{
		{"missing", map[string]any{}},
		{"empty", map[string]any{"path": ""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta, parts := parseResult[removeMeta](t, (&Remove{}).Call(tt.args))
			checkError(t, meta.Error, "missing path string")
			if len(parts) != 0 {
				t.Errorf("got %d content parts, want none", len(parts))
			}
		})
	}
}

// The working directory can't be removed, whichever way the path is written.
func TestRemoveWorkingDirectoryRejected(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, "keep.txt", []byte("keep"))
	tests := []struct {
		name string
		path string
	}{
		{"dot", "."},
		{"dot slash", "./"},
		{"up and back", "sub/.."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta, _ := parseResult[removeMeta](t, (&Remove{LocalFiles: true}).Call(map[string]any{"path": tt.path}))
			checkError(t, meta.Error, "working directory")
		})
	}
	if _, err := os.Stat("keep.txt"); err != nil {
		t.Errorf("keep.txt stat = %v, want unchanged", err)
	}
}

func TestRemoveHiddenAndLocalPaths(t *testing.T) {
	t.Chdir(t.TempDir())
	outside := filepath.Join(t.TempDir(), "outside.txt")
	writeTestFile(t, outside, []byte("o"))
	writeTestFile(t, ".env", []byte("A=1"))

	meta, _ := parseResult[removeMeta](t, (&Remove{}).Call(map[string]any{"path": ".env"}))
	checkError(t, meta.Error, "hidden path not allowed")
	if _, err := os.Stat(".env"); err != nil {
		t.Errorf(".env stat = %v, want unchanged", err)
	}

	meta, _ = parseResult[removeMeta](t, (&Remove{HiddenFiles: true}).Call(map[string]any{"path": ".env"}))
	checkNoError(t, meta.Error)
	if _, err := os.Stat(".env"); !os.IsNotExist(err) {
		t.Errorf(".env stat = %v, want not exist", err)
	}

	r := &Remove{LocalFiles: true}
	for _, p := range []string{"../outside.txt", outside} {
		meta, _ = parseResult[removeMeta](t, r.Call(map[string]any{"path": p}))
		checkError(t, meta.Error, "non-local path not allowed")
	}
	if got, _ := os.ReadFile(outside); string(got) != "o" {
		t.Errorf("outside file = %q, want unchanged", got)
	}
}

func TestRemoveFileDirectoryAndSymlink(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, "f.txt", []byte("x"))
	writeTestFile(t, "tree/a/b.txt", []byte("x"))
	writeTestFile(t, "target/keep.txt", []byte("keep"))
	if err := os.Symlink("target", "link"); err != nil {
		t.Fatal(err)
	}
	r := &Remove{LocalFiles: true}
	for _, p := range []string{"f.txt", "tree", "link"} {
		meta, _ := parseResult[removeMeta](t, r.Call(map[string]any{"path": p}))
		checkNoError(t, meta.Error)
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("%s lstat = %v, want not exist", p, err)
		}
	}
	if got, err := os.ReadFile("target/keep.txt"); err != nil || string(got) != "keep" {
		t.Errorf("symlink target = %q, %v, want it untouched", got, err)
	}
}

func TestRemoveMissingPathSucceeds(t *testing.T) {
	t.Chdir(t.TempDir())
	meta, _ := parseResult[removeMeta](t, (&Remove{LocalFiles: true}).Call(map[string]any{"path": "nope/none.txt"}))
	checkNoError(t, meta.Error)
}

// An os.RemoveAll error is reported as its own text, with no prefix.
func TestRemoveReportsRemoveAllError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can remove files in read-only directories")
	}
	t.Chdir(t.TempDir())
	writeTestFile(t, "ro/f.txt", []byte("x"))
	if err := os.Chmod("ro", 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod("ro", 0o755); err != nil {
			t.Errorf("restoring ro: %v", err)
		}
	})
	want := os.RemoveAll("ro/f.txt")
	if want == nil {
		t.Fatal("os.RemoveAll succeeded, want a permission error")
	}
	meta, _ := parseResult[removeMeta](t, (&Remove{LocalFiles: true}).Call(map[string]any{"path": "ro/f.txt"}))
	if meta.Error != want.Error() {
		t.Errorf("error = %q, want %q", meta.Error, want.Error())
	}
	if _, err := os.Stat("ro/f.txt"); err != nil {
		t.Errorf("ro/f.txt stat = %v, want unchanged", err)
	}
}
