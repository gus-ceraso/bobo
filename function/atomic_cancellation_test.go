package function

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ceraso.dev/bobo/provider"
)

func TestCancellationAfterPartialTemporaryWrite(t *testing.T) {
	for _, operation := range []string{"create", "replace", "edit"} {
		t.Run(operation, func(t *testing.T) {
			_, _, w, e := NewFileFunctions()
			dir := t.TempDir()
			path := filepath.Join(dir, "file")
			original := "old" + strings.Repeat("original\n", 1<<14)
			want := "new" + strings.Repeat("original\n", 1<<14)
			if operation != "create" {
				writeFixture(t, path, []byte(original))
			}
			var before os.FileInfo
			if operation != "create" {
				var err error
				before, err = os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
			}
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			var partial []byte
			// Observe a semantic checkpoint instead of a particular Err-call
			// number. Cancellation must occur after some output was written,
			// but before the replacement was complete or could be committed.
			ctx := &checkpointContext{Context: parent, onErr: func(int) {
				if partial != nil {
					return
				}
				matches, err := filepath.Glob(filepath.Join(dir, ".bobo-*"))
				if err != nil {
					t.Fatal(err)
				}
				if len(matches) != 1 {
					return
				}
				data := readFixture(t, matches[0])
				if len(data) > 0 && len(data) < len(want) {
					partial = data
					cancel()
				}
			}}
			var fn provider.Function = w
			args := map[string]any{"path": path, "text": want}
			if operation == "edit" {
				fn = e
				args = map[string]any{"path": path, "old_text": "old", "new_text": "new"}
			}
			result := invoke(t, fn, ctx, args)
			if partial == nil {
				t.Fatal("operation never checked cancellation during a partial temporary write")
			}
			if !bytes.Equal(partial, []byte(want[:len(partial)])) {
				t.Fatal("temporary file did not contain the expected output prefix")
			}
			requireError(t, result, context.Canceled.Error())
			if operation == "create" {
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatalf("canceled create left a destination: %v", err)
				}
			} else {
				if string(readFixture(t, path)) != original {
					t.Fatal("canceled operation changed the destination contents")
				}
				after, err := os.Stat(path)
				if err != nil || !os.SameFile(before, after) {
					t.Fatalf("canceled operation replaced the destination inode: %v", err)
				}
			}
			requireCleanDirectory(t, dir)
			requireEmptyRegistry(t, w.locks)
		})
	}
}
