package tool

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

type copyMeta struct {
	Error   string `json:"error"`
	OldPath string `json:"old_path"`
	NewPath string `json:"new_path"`
}

// mustSymlink creates a symbolic link at link pointing to target.
func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func TestCopyMissingPaths(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, "a.txt", []byte("a"))
	tests := []struct {
		name    string
		args    map[string]any
		wantErr string
	}{
		{"missing old_path", map[string]any{"new_path": "b.txt"}, "missing old_path string"},
		{"empty old_path", map[string]any{"old_path": "", "new_path": "b.txt"}, "missing old_path string"},
		{"missing new_path", map[string]any{"old_path": "a.txt"}, "missing new_path string"},
		{"empty new_path", map[string]any{"old_path": "a.txt", "new_path": ""}, "missing new_path string"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta, parts := parseResult[copyMeta](t, (&Copy{LocalFiles: true}).Call(tt.args))
			checkError(t, meta.Error, tt.wantErr)
			if len(parts) != 0 {
				t.Errorf("got %d content parts, want none", len(parts))
			}
		})
	}
}

func TestCopyPathRestrictions(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, ".env", []byte("A=1"))
	writeTestFile(t, "a.txt", []byte("a"))
	outside := filepath.Join(t.TempDir(), "outside.txt")
	writeTestFile(t, outside, []byte("o"))

	t.Run("hidden old_path rejected", func(t *testing.T) {
		meta, _ := parseResult[copyMeta](t, (&Copy{}).Call(map[string]any{"old_path": ".env", "new_path": "copy.env"}))
		checkError(t, meta.Error, "hidden old_path not allowed")
	})
	t.Run("hidden new_path rejected", func(t *testing.T) {
		meta, _ := parseResult[copyMeta](t, (&Copy{}).Call(map[string]any{"old_path": "a.txt", "new_path": ".copy"}))
		checkError(t, meta.Error, "hidden new_path not allowed")
	})
	t.Run("hidden paths allowed", func(t *testing.T) {
		meta, _ := parseResult[copyMeta](t, (&Copy{HiddenFiles: true}).Call(map[string]any{"old_path": ".env", "new_path": "copy.env"}))
		checkNoError(t, meta.Error)
		if got, _ := os.ReadFile("copy.env"); string(got) != "A=1" {
			t.Errorf("copy.env = %q, want %q", got, "A=1")
		}
	})
	t.Run("non-local paths rejected", func(t *testing.T) {
		r := &Copy{LocalFiles: true}
		meta, _ := parseResult[copyMeta](t, r.Call(map[string]any{"old_path": "../a.txt", "new_path": "b.txt"}))
		checkError(t, meta.Error, "non-local old_path not allowed")
		meta, _ = parseResult[copyMeta](t, r.Call(map[string]any{"old_path": "a.txt", "new_path": outside}))
		checkError(t, meta.Error, "non-local new_path not allowed")
		if got, _ := os.ReadFile(outside); string(got) != "o" {
			t.Errorf("outside file = %q, want unchanged", got)
		}
	})
}

// Copies keep the file's permission bits, even when the umask would clear them.
func TestCopyFileKeepsPermissions(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, "run.sh", []byte("#!/bin/sh\n"))
	if err := os.Chmod("run.sh", 0o751); err != nil {
		t.Fatal(err)
	}
	meta, parts := parseResult[copyMeta](t, (&Copy{LocalFiles: true}).Call(map[string]any{"old_path": "run.sh", "new_path": "copy.sh"}))
	checkNoError(t, meta.Error)
	if len(parts) != 0 {
		t.Errorf("got %d content parts, want none", len(parts))
	}
	info, err := os.Stat("copy.sh")
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o751 {
		t.Errorf("mode = %v, want %v", info.Mode().Perm(), os.FileMode(0o751))
	}
	if got, _ := os.ReadFile("copy.sh"); string(got) != "#!/bin/sh\n" {
		t.Errorf("content = %q, want the original", got)
	}
	if _, err := os.Stat("run.sh"); err != nil {
		t.Errorf("run.sh stat = %v, want unchanged", err)
	}
}

func TestCopyOverwritesExistingFile(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, "a.txt", []byte("new"))
	writeTestFile(t, "b.txt", []byte("an old and much longer file"))
	meta, _ := parseResult[copyMeta](t, (&Copy{LocalFiles: true}).Call(map[string]any{"old_path": "a.txt", "new_path": "b.txt"}))
	checkNoError(t, meta.Error)
	if got, _ := os.ReadFile("b.txt"); string(got) != "new" {
		t.Errorf("b.txt = %q, want %q", got, "new")
	}
}

func TestCopyCreatesParentDirectories(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, "a.txt", []byte("deep"))
	meta, _ := parseResult[copyMeta](t, (&Copy{LocalFiles: true}).Call(map[string]any{"old_path": "a.txt", "new_path": "x/y/b.txt"}))
	checkNoError(t, meta.Error)
	if got, err := os.ReadFile(filepath.Join("x", "y", "b.txt")); err != nil || string(got) != "deep" {
		t.Errorf("x/y/b.txt = %q, %v, want %q", got, err, "deep")
	}
}

func TestCopyDirectoryRecursively(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, "src/a.txt", []byte("a"))
	writeTestFile(t, "src/sub/deep/b.txt", []byte("b"))
	if err := os.MkdirAll("src/empty", 0o755); err != nil {
		t.Fatal(err)
	}
	meta, _ := parseResult[copyMeta](t, (&Copy{LocalFiles: true}).Call(map[string]any{"old_path": "src", "new_path": "dst"}))
	checkNoError(t, meta.Error)
	for name, want := range map[string]string{"a.txt": "a", "sub/deep/b.txt": "b"} {
		if got, err := os.ReadFile(filepath.Join("dst", name)); err != nil || string(got) != want {
			t.Errorf("dst/%s = %q, %v, want %q", name, got, err, want)
		}
	}
	if info, err := os.Stat("dst/empty"); err != nil || !info.IsDir() {
		t.Errorf("dst/empty = %v, %v, want a directory", info, err)
	}
	if got, _ := os.ReadFile("src/sub/deep/b.txt"); string(got) != "b" {
		t.Errorf("src/sub/deep/b.txt = %q, want unchanged", got)
	}
}

// Copying into an existing directory merges: files with the same name are
// overwritten, and other files are kept.
func TestCopyMergesIntoExistingDirectory(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, "src/a.txt", []byte("new"))
	writeTestFile(t, "src/sub/c.txt", []byte("c"))
	writeTestFile(t, "dst/a.txt", []byte("old"))
	writeTestFile(t, "dst/keep.txt", []byte("keep"))
	meta, _ := parseResult[copyMeta](t, (&Copy{LocalFiles: true}).Call(map[string]any{"old_path": "src", "new_path": "dst"}))
	checkNoError(t, meta.Error)
	for name, want := range map[string]string{"a.txt": "new", "keep.txt": "keep", "sub/c.txt": "c"} {
		if got, err := os.ReadFile(filepath.Join("dst", name)); err != nil || string(got) != want {
			t.Errorf("dst/%s = %q, %v, want %q", name, got, err, want)
		}
	}
}

// Symbolic links are copied as links, not followed, including dangling ones.
func TestCopySymlinksCopiedAsLinks(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, "src/a.txt", []byte("a"))
	mustSymlink(t, "a.txt", "src/link")
	mustSymlink(t, "missing.txt", "src/dangling")
	meta, _ := parseResult[copyMeta](t, (&Copy{LocalFiles: true}).Call(map[string]any{"old_path": "src", "new_path": "dst"}))
	checkNoError(t, meta.Error)
	for name, want := range map[string]string{"link": "a.txt", "dangling": "missing.txt"} {
		info, err := os.Lstat(filepath.Join("dst", name))
		if err != nil {
			t.Errorf("dst/%s lstat = %v", name, err)
			continue
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Errorf("dst/%s mode = %v, want a symbolic link", name, info.Mode())
			continue
		}
		if target, _ := os.Readlink(filepath.Join("dst", name)); target != want {
			t.Errorf("dst/%s target = %q, want %q", name, target, want)
		}
	}
}

// A symlink given as old_path is copied as a link to the same target.
func TestCopySymlinkRootIsNotFollowed(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, "target/f.txt", []byte("f"))
	mustSymlink(t, "target", "link")
	meta, _ := parseResult[copyMeta](t, (&Copy{LocalFiles: true}).Call(map[string]any{"old_path": "link", "new_path": "copy"}))
	checkNoError(t, meta.Error)
	if target, err := os.Readlink("copy"); err != nil || target != "target" {
		t.Errorf("copy = link to %q, %v, want %q", target, err, "target")
	}
}

// new_path is the copy itself. It can't be old_path or inside it.
func TestCopyNewPathInsideOldPathRejected(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, "d/a.txt", []byte("a"))
	writeTestFile(t, "f.txt", []byte("f"))
	tests := []struct {
		name    string
		oldPath string
		newPath string
	}{
		{"same directory", "d", "d"},
		{"inside directory", "d", "d/sub"},
		{"same file", "f.txt", "f.txt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta, _ := parseResult[copyMeta](t, (&Copy{LocalFiles: true}).Call(map[string]any{"old_path": tt.oldPath, "new_path": tt.newPath}))
			checkError(t, meta.Error, "can't be old_path or inside it")
		})
	}
	if _, err := os.Stat("d/sub"); !os.IsNotExist(err) {
		t.Errorf("d/sub stat = %v, want not exist", err)
	}
}

func TestCopyMissingSourceCreatesNothing(t *testing.T) {
	t.Chdir(t.TempDir())
	meta, _ := parseResult[copyMeta](t, (&Copy{LocalFiles: true}).Call(map[string]any{"old_path": "nope", "new_path": "new/copy"}))
	checkError(t, meta.Error, "walk:")
	if _, err := os.Stat("new"); !os.IsNotExist(err) {
		t.Errorf("new stat = %v, want not exist", err)
	}
}

// Special files such as named pipes can't be copied. Nothing is opened, so
// the copy fails at once.
func TestCopyUnsupportedFileType(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := syscall.Mkfifo("pipe", 0o644); err != nil {
		t.Fatal(err)
	}
	meta, _ := parseResult[copyMeta](t, (&Copy{LocalFiles: true}).Call(map[string]any{"old_path": "pipe", "new_path": "copy"}))
	checkError(t, meta.Error, "unsupported file type")
	if _, err := os.Lstat("copy"); !os.IsNotExist(err) {
		t.Errorf("copy lstat = %v, want not exist", err)
	}
}

// Copying stops at the first error, which can leave a partial copy. The walk
// visits a.txt, then the pipe, so c.txt is never copied.
func TestCopyStopsAtFirstError(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, "src/a.txt", []byte("a"))
	if err := syscall.Mkfifo("src/b.pipe", 0o644); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, "src/c.txt", []byte("c"))
	meta, _ := parseResult[copyMeta](t, (&Copy{LocalFiles: true}).Call(map[string]any{"old_path": "src", "new_path": "dst"}))
	checkError(t, meta.Error, "unsupported file type")
	if got, err := os.ReadFile("dst/a.txt"); err != nil || string(got) != "a" {
		t.Errorf("dst/a.txt = %q, %v, want the partial copy", got, err)
	}
	if _, err := os.Lstat("dst/c.txt"); !os.IsNotExist(err) {
		t.Errorf("dst/c.txt lstat = %v, want not exist", err)
	}
}

// A regular file can't be created under a parent that is a file. The error
// stops the copy and leaves the parent alone.
func TestCopyParentIsFile(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, "a.txt", []byte("a"))
	writeTestFile(t, "blocker", []byte("file"))
	meta, _ := parseResult[copyMeta](t, (&Copy{LocalFiles: true}).Call(map[string]any{"old_path": "a.txt", "new_path": "blocker/c.txt"}))
	checkError(t, meta.Error, "not a directory")
	if got, _ := os.ReadFile("blocker"); string(got) != "file" {
		t.Errorf("blocker = %q, want unchanged", got)
	}
}

// A symlink can't replace a non-empty directory. The error stops the copy.
func TestCopySymlinkOverNonEmptyDirectory(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, "src/a.txt", []byte("a"))
	mustSymlink(t, "a.txt", "src/link")
	writeTestFile(t, "dst/link/keep.txt", []byte("keep"))
	meta, _ := parseResult[copyMeta](t, (&Copy{LocalFiles: true}).Call(map[string]any{"old_path": "src", "new_path": "dst"}))
	checkError(t, meta.Error, "directory not empty")
	if got, _ := os.ReadFile("dst/link/keep.txt"); string(got) != "keep" {
		t.Errorf("dst/link/keep.txt = %q, want unchanged", got)
	}
}

// A regular file can't be copied over an existing directory.
func TestCopyFileOntoDirectory(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, "a.txt", []byte("a"))
	writeTestFile(t, "dir/keep.txt", []byte("keep"))
	meta, _ := parseResult[copyMeta](t, (&Copy{LocalFiles: true}).Call(map[string]any{"old_path": "a.txt", "new_path": "dir"}))
	checkError(t, meta.Error, "is a directory")
	if got, _ := os.ReadFile("dir/keep.txt"); string(got) != "keep" {
		t.Errorf("dir/keep.txt = %q, want unchanged", got)
	}
}

// A file that the process can't read stops the copy with an error.
func TestCopyUnreadableFileReportsError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read files with mode 0")
	}
	t.Chdir(t.TempDir())
	writeTestFile(t, "src/secret.txt", []byte("secret"))
	if err := os.Chmod("src/secret.txt", 0); err != nil {
		t.Fatal(err)
	}
	meta, _ := parseResult[copyMeta](t, (&Copy{LocalFiles: true}).Call(map[string]any{"old_path": "src", "new_path": "dst"}))
	checkError(t, meta.Error, "permission denied")
}
