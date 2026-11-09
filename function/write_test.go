package function

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ceraso.dev/bobo/provider"
)

func TestWriteCreatesAndReplaces(t *testing.T) {
	_, _, w, _ := NewFileFunctions()
	dir := t.TempDir()
	path := filepath.Join(dir, "new", "nested", "file")
	for _, text := range []string{"first\n世界🙂", "", "longer\x00\nreplacement", "x"} {
		result := invoke(t, w, context.Background(), map[string]any{"path": path, "text": text})
		requireSuccess(t, result)
		assertAttribute(t, result, "text", wantPreview(text))
		if string(readFixture(t, path)) != text {
			t.Fatal("write did not replace complete contents")
		}
		requireCleanDirectory(t, filepath.Dir(path))
		requireEmptyRegistry(t, w.locks)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm()&^0o644 != 0 {
		t.Fatalf("unexpected new-file mode: %v, %v", info, err)
	}
}

func TestWriteAtomicityAndPermissions(t *testing.T) {
	_, _, w, _ := NewFileFunctions()
	dir := t.TempDir()
	path := filepath.Join(dir, "file")
	writeFixture(t, path, []byte("original"))
	if err := os.Chmod(path, 0o751); err != nil {
		t.Fatal(err)
	}
	original, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	before, err := original.Stat()
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "hardlink")
	if err := os.Link(path, link); err != nil {
		t.Fatal(err)
	}
	requireSuccess(t, invoke(t, w, context.Background(), map[string]any{"path": path, "text": "replacement"}))
	after, err := os.Stat(path)
	if err != nil || os.SameFile(before, after) || after.Mode().Perm() != 0o751 {
		t.Fatalf("replacement lost atomicity or permissions: %v, %v", after, err)
	}
	old, err := io.ReadAll(original)
	if err != nil || string(old) != "original" || string(readFixture(t, link)) != "original" {
		t.Fatal("atomic replacement modified the old inode")
	}
	if string(readFixture(t, path)) != "replacement" {
		t.Fatal("replacement content missing")
	}
	requireCleanDirectory(t, dir)
}

func TestWriteLarge(t *testing.T) {
	if testing.Short() {
		t.Skip("large-file write")
	}
	_, _, w, _ := NewFileFunctions()
	path := filepath.Join(t.TempDir(), "file")
	text := strings.Repeat("世界🙂\n", (32<<20)/11+1)
	if len(text) <= 32<<20 {
		t.Fatal("fixture must exceed the former size cap")
	}
	result := invoke(t, w, context.Background(), map[string]any{"path": path, "text": text})
	requireSuccess(t, result)
	assertAttribute(t, result, "text", wantPreview(text))
	if !bytes.Equal(readFixture(t, path), []byte(text)) {
		t.Fatal("large write was capped or corrupted")
	}
	requireCleanDirectory(t, filepath.Dir(path))
}

func TestWriteCancellationLeavesDestinationIntact(t *testing.T) {
	for _, existing := range []bool{false, true} {
		for checkpoint := 1; checkpoint <= 11; checkpoint++ {
			t.Run(fmt.Sprintf("existing=%t/checkpoint=%d", existing, checkpoint), func(t *testing.T) {
				_, _, w, _ := NewFileFunctions()
				path := filepath.Join(t.TempDir(), "file")
				if existing {
					writeFixture(t, path, []byte("original"))
				}
				ctx, cancel := cancellationAt(checkpoint)
				defer cancel()
				text := strings.Repeat("x", 3<<15)
				result := invoke(t, w, ctx, map[string]any{"path": path, "text": text})
				if ctx.calls.Load() >= int32(checkpoint) {
					requireError(t, result, context.Canceled.Error())
					if existing {
						if string(readFixture(t, path)) != "original" {
							t.Fatal("cancellation partially replaced the file")
						}
					} else if _, err := os.Lstat(path); !os.IsNotExist(err) {
						t.Fatalf("canceled write created destination: %v", err)
					}
				} else {
					requireSuccess(t, result)
				}
				requireCleanDirectory(t, filepath.Dir(path))
				requireEmptyRegistry(t, w.locks)
			})
		}
	}
}

func TestWriteRenameFailureCleansUp(t *testing.T) {
	_, _, w, _ := NewFileFunctions()
	dir := t.TempDir()
	path := filepath.Join(dir, "file")
	var setupErr error
	changed := false
	ctx := &checkpointContext{Context: context.Background(), onErr: func(int) {
		matches, err := filepath.Glob(filepath.Join(dir, ".bobo-*"))
		if err != nil {
			setupErr = err
		}
		if !changed && len(matches) != 0 {
			changed = true
			setupErr = os.Mkdir(path, 0o755)
		}
	}}
	result := invoke(t, w, ctx, map[string]any{"path": path, "text": "replacement"})
	if setupErr != nil || !changed {
		t.Fatalf("failure setup did not run: %v", setupErr)
	}
	requireError(t, result, "")
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		t.Fatalf("conflicting directory was changed: %v", err)
	}
	requireCleanDirectory(t, dir)
	requireEmptyRegistry(t, w.locks)
}

func TestWriteParentIsFile(t *testing.T) {
	_, _, w, _ := NewFileFunctions()
	parent := filepath.Join(t.TempDir(), "parent")
	writeFixture(t, parent, []byte("original"))
	requireError(t, invoke(t, w, context.Background(), map[string]any{"path": parent + "/file", "text": "new"}), "")
	if string(readFixture(t, parent)) != "original" {
		t.Fatal("write changed a nondirectory parent")
	}
}

func TestPermissionErrors(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses Unix permission checks")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "file")
	writeFixture(t, path, []byte("old"))
	r, v, w, e := NewFileFunctions()
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(path, 0o600) })
	for _, fn := range []provider.Function{r, v, e} {
		requireError(t, invoke(t, fn, context.Background(), functionArgs(path)), "permission denied")
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700)
	for _, fn := range []provider.Function{w, e} {
		requireError(t, invoke(t, fn, context.Background(), functionArgs(path)), "permission denied")
	}
}
