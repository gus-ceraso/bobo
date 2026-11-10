package function

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type readerFunc func([]byte) (int, error)

func (r readerFunc) Read(p []byte) (int, error) { return r(p) }

func TestReplaceFileRejectsIncompleteOutput(t *testing.T) {
	for _, existing := range []bool{false, true} {
		for _, stage := range []string{"read-error", "canceled-read", "canceled-EOF"} {
			t.Run(fmt.Sprintf("%s/existing=%t", stage, existing), func(t *testing.T) {
				dir := t.TempDir()
				path := filepath.Join(dir, "file")
				var before os.FileInfo
				if existing {
					writeFixture(t, path, []byte("original"))
					var err error
					before, err = os.Stat(path)
					if err != nil {
						t.Fatal(err)
					}
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				readErr := errors.New("source failed")
				want := readErr
				if stage != "read-error" {
					want = context.Canceled
				}
				source := readerFunc(func(p []byte) (int, error) {
					if stage == "read-error" {
						return copy(p, "partial"), readErr
					}
					cancel()
					if stage == "canceled-EOF" {
						return copy(p, "complete"), io.EOF
					}
					return copy(p, "partial"), nil
				})
				if err := replaceFile(ctx, path, before, source); !errors.Is(err, want) {
					t.Fatalf("replaceFile = %v, want %v", err, want)
				}
				if existing {
					after, err := os.Stat(path)
					if err != nil || !os.SameFile(before, after) || string(readFixture(t, path)) != "original" {
						t.Fatalf("failed replacement changed the destination: %v", err)
					}
				} else if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatalf("failed replacement created a destination: %v", err)
				}
				requireCleanDirectory(t, dir)
			})
		}
	}
}

func TestReplaceFileKeepsIncompleteReplacementPrivate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file")
	writeFixture(t, path, []byte("original"))
	if err := os.Chmod(path, 0o751); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	reader := strings.NewReader("replacement")
	source := readerFunc(func(p []byte) (int, error) {
		matches, err := filepath.Glob(filepath.Join(dir, ".bobo-*"))
		if err != nil || len(matches) != 1 {
			t.Fatalf("temporary files = %v, %v", matches, err)
		}
		info, err := os.Stat(matches[0])
		if err != nil || info.Mode().Perm()&^0o600 != 0 {
			t.Fatalf("incomplete output permissions = %v, %v", info, err)
		}
		return reader.Read(p)
	})
	if err := replaceFile(context.Background(), path, before, source); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil || after.Mode().Perm() != 0o751 || string(readFixture(t, path)) != "replacement" {
		t.Fatalf("replacement lost contents or permissions: %v, %v", after, err)
	}
	requireCleanDirectory(t, dir)
}
