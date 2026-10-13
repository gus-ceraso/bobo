package tool

import (
	"os"
	"path/filepath"
	"testing"
)

type moveMeta struct {
	Error   string `json:"error"`
	OldPath string `json:"old_path"`
	NewPath string `json:"new_path"`
}

func TestMoveMissingPaths(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, "a.txt", []byte("a"))
	tests := []struct {
		name    string
		args    map[string]any
		wantErr string
	}{
		{"missing old_path", map[string]any{"new_path": "b.txt"}, "missing old_path string"},
		{"missing new_path", map[string]any{"old_path": "a.txt"}, "missing new_path string"},
		{"empty new_path", map[string]any{"old_path": "a.txt", "new_path": ""}, "missing new_path string"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta, parts := parseResult[moveMeta](t, (&Move{LocalFiles: true}).Call(tt.args))
			checkError(t, meta.Error, tt.wantErr)
			if len(parts) != 0 {
				t.Errorf("got %d content parts, want none", len(parts))
			}
			if _, err := os.Stat("a.txt"); err != nil {
				t.Errorf("a.txt stat = %v, want unchanged", err)
			}
		})
	}
}

func TestMovePathRestrictions(t *testing.T) {
	t.Chdir(t.TempDir())
	outside := filepath.Join(t.TempDir(), "outside.txt")
	writeTestFile(t, outside, []byte("o"))

	t.Run("hidden old_path rejected", func(t *testing.T) {
		writeTestFile(t, ".env", []byte("A=1"))
		meta, _ := parseResult[moveMeta](t, (&Move{}).Call(map[string]any{"old_path": ".env", "new_path": "env.txt"}))
		checkError(t, meta.Error, "hidden old_path not allowed")
		if _, err := os.Stat(".env"); err != nil {
			t.Errorf(".env stat = %v, want unchanged", err)
		}
	})
	t.Run("hidden new_path rejected", func(t *testing.T) {
		writeTestFile(t, "a.txt", []byte("a"))
		meta, _ := parseResult[moveMeta](t, (&Move{}).Call(map[string]any{"old_path": "a.txt", "new_path": ".hidden.txt"}))
		checkError(t, meta.Error, "hidden new_path not allowed")
		if _, err := os.Stat("a.txt"); err != nil {
			t.Errorf("a.txt stat = %v, want unchanged", err)
		}
	})
	t.Run("hidden paths allowed", func(t *testing.T) {
		writeTestFile(t, "b.txt", []byte("b"))
		meta, _ := parseResult[moveMeta](t, (&Move{HiddenFiles: true}).Call(map[string]any{"old_path": "b.txt", "new_path": ".b.txt"}))
		checkNoError(t, meta.Error)
		if got, err := os.ReadFile(".b.txt"); err != nil || string(got) != "b" {
			t.Errorf(".b.txt = %q, %v, want %q", got, err, "b")
		}
	})
	t.Run("non-local paths rejected", func(t *testing.T) {
		writeTestFile(t, "c.txt", []byte("c"))
		r := &Move{LocalFiles: true}
		meta, _ := parseResult[moveMeta](t, r.Call(map[string]any{"old_path": "../c.txt", "new_path": "d.txt"}))
		checkError(t, meta.Error, "non-local old_path not allowed")
		meta, _ = parseResult[moveMeta](t, r.Call(map[string]any{"old_path": "c.txt", "new_path": outside}))
		checkError(t, meta.Error, "non-local new_path not allowed")
		if _, err := os.Stat("c.txt"); err != nil {
			t.Errorf("c.txt stat = %v, want unchanged", err)
		}
		if got, _ := os.ReadFile(outside); string(got) != "o" {
			t.Errorf("outside file = %q, want unchanged", got)
		}
	})
}

func TestMoveRenamesFile(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, "a.txt", []byte("alpha"))
	meta, parts := parseResult[moveMeta](t, (&Move{LocalFiles: true}).Call(map[string]any{"old_path": "a.txt", "new_path": "b.txt"}))
	checkNoError(t, meta.Error)
	if meta.OldPath != "a.txt" || meta.NewPath != "b.txt" {
		t.Errorf("old_path, new_path = %q, %q, want a.txt, b.txt", meta.OldPath, meta.NewPath)
	}
	if len(parts) != 0 {
		t.Errorf("got %d content parts, want none", len(parts))
	}
	if _, err := os.Stat("a.txt"); !os.IsNotExist(err) {
		t.Errorf("a.txt stat = %v, want not exist", err)
	}
	got, err := os.ReadFile("b.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "alpha" {
		t.Errorf("file = %q, want %q", got, "alpha")
	}
}

func TestMoveCreatesParentDirectories(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, "a.txt", []byte("deep"))
	meta, _ := parseResult[moveMeta](t, (&Move{LocalFiles: true}).Call(map[string]any{"old_path": "a.txt", "new_path": "p/q/r.txt"}))
	checkNoError(t, meta.Error)
	got, err := os.ReadFile(filepath.Join("p", "q", "r.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "deep" {
		t.Errorf("file = %q, want %q", got, "deep")
	}
}

func TestMoveOverwritesExistingFile(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, "a.txt", []byte("new"))
	writeTestFile(t, "x/b.txt", []byte("an old and much longer file"))
	meta, _ := parseResult[moveMeta](t, (&Move{LocalFiles: true}).Call(map[string]any{"old_path": "a.txt", "new_path": "x/b.txt"}))
	checkNoError(t, meta.Error)
	got, err := os.ReadFile("x/b.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Errorf("file = %q, want %q", got, "new")
	}
	if _, err := os.Stat("a.txt"); !os.IsNotExist(err) {
		t.Errorf("a.txt stat = %v, want not exist", err)
	}
}

// A directory moves with its contents, and a symlink moves as a link without
// touching its target.
func TestMoveDirectoryAndSymlink(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, "dir/inner/f.txt", []byte("inner"))
	r := &Move{LocalFiles: true}
	meta, _ := parseResult[moveMeta](t, r.Call(map[string]any{"old_path": "dir", "new_path": "moved/dir2"}))
	checkNoError(t, meta.Error)
	if got, err := os.ReadFile("moved/dir2/inner/f.txt"); err != nil || string(got) != "inner" {
		t.Errorf("moved file = %q, %v, want %q", got, err, "inner")
	}
	if _, err := os.Stat("dir"); !os.IsNotExist(err) {
		t.Errorf("dir stat = %v, want not exist", err)
	}

	if err := os.Symlink("moved/dir2/inner/f.txt", "link"); err != nil {
		t.Fatal(err)
	}
	meta, _ = parseResult[moveMeta](t, r.Call(map[string]any{"old_path": "link", "new_path": "link2"}))
	checkNoError(t, meta.Error)
	target, err := os.Readlink("link2")
	if err != nil {
		t.Fatal(err)
	}
	if target != "moved/dir2/inner/f.txt" {
		t.Errorf("link target = %q, want %q", target, "moved/dir2/inner/f.txt")
	}
	if _, err := os.Stat("moved/dir2/inner/f.txt"); err != nil {
		t.Errorf("link target stat = %v, want unchanged", err)
	}
}

func TestMoveMissingSourceCreatesNothing(t *testing.T) {
	t.Chdir(t.TempDir())
	meta, _ := parseResult[moveMeta](t, (&Move{LocalFiles: true}).Call(map[string]any{"old_path": "nope.txt", "new_path": "new/dir/b.txt"}))
	checkError(t, meta.Error, "stat:")
	if _, err := os.Stat("new"); !os.IsNotExist(err) {
		t.Errorf("new stat = %v, want not exist", err)
	}
}

func TestMoveFailures(t *testing.T) {
	t.Chdir(t.TempDir())
	r := &Move{LocalFiles: true}

	t.Run("file onto directory", func(t *testing.T) {
		writeTestFile(t, "a.txt", []byte("a"))
		if err := os.Mkdir("d", 0o755); err != nil {
			t.Fatal(err)
		}
		meta, _ := parseResult[moveMeta](t, r.Call(map[string]any{"old_path": "a.txt", "new_path": "d"}))
		checkError(t, meta.Error, "rename:")
		if _, err := os.Stat("a.txt"); err != nil {
			t.Errorf("a.txt stat = %v, want unchanged", err)
		}
	})
	t.Run("directory into itself", func(t *testing.T) {
		writeTestFile(t, "tree/inner.txt", []byte("x"))
		meta, _ := parseResult[moveMeta](t, r.Call(map[string]any{"old_path": "tree", "new_path": "tree/sub/x"}))
		checkError(t, meta.Error, "rename:")
		if got, err := os.ReadFile("tree/inner.txt"); err != nil || string(got) != "x" {
			t.Errorf("tree/inner.txt = %q, %v, want unchanged", got, err)
		}
	})
	t.Run("parent is a file", func(t *testing.T) {
		writeTestFile(t, "blocker", []byte("file"))
		writeTestFile(t, "c.txt", []byte("c"))
		meta, _ := parseResult[moveMeta](t, r.Call(map[string]any{"old_path": "c.txt", "new_path": "blocker/c.txt"}))
		checkError(t, meta.Error, "mkdir:")
		if _, err := os.Stat("c.txt"); err != nil {
			t.Errorf("c.txt stat = %v, want unchanged", err)
		}
	})
}
