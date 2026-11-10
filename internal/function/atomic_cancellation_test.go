package function

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ceraso.dev/bobo/internal/provider"
)

func TestCancellationBeforeCommit(t *testing.T) {
	for _, operation := range []string{"create", "replace", "edit", "large-edit"} {
		for _, stage := range []string{"partial-output", "complete-output"} {
			t.Run(operation+"/"+stage, func(t *testing.T) {
				if operation == "large-edit" && testing.Short() {
					t.Skip("large-file cancellation")
				}
				_, _, w, e := NewFileFunctions()
				dir := t.TempDir()
				path := filepath.Join(dir, "file")
				size := 1 << 14
				if operation == "large-edit" {
					size = (32<<20)/len("original\n") + 1
				}
				suffix := strings.Repeat("original\n", size)
				original, want := "old"+suffix, "new"+suffix
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
				var observed []byte
				// Observe partial or complete output before the atomic rename.
				ctx := &observingContext{Context: parent, onErr: func() {
					if observed != nil {
						return
					}
					matches, err := filepath.Glob(filepath.Join(dir, ".bobo-*"))
					if err != nil {
						t.Fatal(err)
					}
					if len(matches) != 1 {
						return
					}
					info, err := os.Stat(matches[0])
					if err != nil {
						t.Fatal(err)
					}
					ready := info.Size() > 0 && info.Size() < int64(len(want))
					if stage == "complete-output" {
						ready = info.Size() == int64(len(want))
					}
					if ready {
						observed = readFixture(t, matches[0])
						cancel()
					}
				}}
				var fn provider.Function = w
				args := map[string]any{"path": path, "text": want}
				if operation == "edit" || operation == "large-edit" {
					fn = e
					args = map[string]any{"path": path, "old_text": "old", "new_text": "new"}
				}
				result := invoke(t, fn, ctx, args)
				if observed == nil {
					t.Fatal("operation never checked cancellation at " + stage)
				}
				if !bytes.Equal(observed, []byte(want[:len(observed)])) {
					t.Fatal("unexpected temporary output")
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
}
