//go:build linux

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func inputDescriptor(t *testing.T, file *os.File) uintptr {
	t.Helper()
	raw, err := file.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var fd uintptr
	if err := raw.Control(func(source uintptr) { fd = source }); err != nil {
		t.Fatal(err)
	}
	return fd
}

func TestReadAllStreamCancellation(t *testing.T) {
	for _, kind := range []string{"pipe", "socket"} {
		for _, blocking := range []bool{false, true} {
			name := kind + "/nonblocking"
			if blocking {
				name = kind + "/blocking"
			}
			t.Run(name, func(t *testing.T) {
				r, w := answerStream(t, kind)
				defer r.Close()
				defer w.Close()
				fd := inputDescriptor(t, r)
				if err := unix.SetNonblock(int(fd), !blocking); err != nil {
					t.Fatal(err)
				}
				flags, err := unix.FcntlInt(fd, unix.F_GETFL, 0)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := io.WriteString(w, "prefix"); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				type result struct {
					body []byte
					err  error
				}
				done := make(chan result, 1)
				go func() {
					body, err := readAllContext(ctx, r)
					done <- result{body, err}
				}()
				deadline := time.Now().Add(3 * time.Second)
				for {
					n, err := unix.IoctlGetInt(int(fd), unix.TIOCINQ)
					if err != nil {
						t.Fatal(err)
					}
					if n == 0 {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("reader never consumed the available prefix")
					}
					time.Sleep(time.Millisecond)
				}
				if current, err := unix.FcntlInt(fd, unix.F_GETFL, 0); err != nil || current != flags {
					t.Fatalf("read changed shared descriptor flags: %x -> %x: %v", flags, current, err)
				}
				cancel()
				select {
				case got := <-done:
					if !errors.Is(got.err, context.Canceled) || string(got.body) != "prefix" {
						t.Fatalf("read = %q, %v", got.body, got.err)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("stream read ignored cancellation")
				}
				if current, err := unix.FcntlInt(fd, unix.F_GETFL, 0); err != nil || current != flags {
					t.Fatalf("caller descriptor closed or changed: %x -> %x: %v", flags, current, err)
				}
				if _, err := io.WriteString(w, "after"); err != nil {
					t.Fatal(err)
				}
				buf := make([]byte, 5)
				if n, err := unix.Read(int(fd), buf); err != nil || n != 5 || string(buf) != "after" {
					t.Fatalf("caller cannot continue reading: %q, %d, %v", buf, n, err)
				}
			})
		}
	}
}

func TestReadAllStreamComplete(t *testing.T) {
	for _, kind := range []string{"pipe", "socket"} {
		t.Run(kind, func(t *testing.T) {
			r, w := answerStream(t, kind)
			defer r.Close()
			defer w.Close()
			if kind == "pipe" {
				// Unlike output, input pipes do not require reopen permission.
				if err := r.Chmod(0); err != nil {
					t.Fatal(err)
				}
			}
			_ = r.Fd() // Inherited stdin is blocking.
			want := []byte(strings.Repeat("prefix🙂\x00\xff", 1<<13))
			done := make(chan error, 1)
			go func() {
				_, err := w.Write(want)
				_ = w.Close()
				done <- err
			}()
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			got, err := readAllContext(ctx, r)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("read %d bytes, want %d: %v", len(got), len(want), err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReadAllFilesAndErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "input")
	writeTestFile(t, path, "skip prompt")
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.Seek(5, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	body, err := readAllContext(t.Context(), file)
	if err != nil || string(body) != "prompt" {
		t.Fatalf("offset not preserved: %q, %v", body, err)
	}
	if offset, err := file.Seek(0, io.SeekCurrent); err != nil || offset != 11 {
		t.Fatalf("caller file offset = %d: %v", offset, err)
	}
	r, w := answerStream(t, "pipe")
	defer w.Close()
	if _, err := readAllContext(t.Context(), w); !errors.Is(err, unix.EBADF) {
		t.Fatalf("write-only descriptor: %v", err)
	}
	_ = r.Close()
	if _, err := readAllContext(t.Context(), r); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("closed descriptor: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	reader := strings.NewReader("untouched")
	if _, err := readAllContext(ctx, reader); !errors.Is(err, context.Canceled) || reader.Len() != 9 {
		t.Fatalf("canceled read consumed input: %d, %v", reader.Len(), err)
	}
	null, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	if body, err := readAllContext(t.Context(), null); err != nil || len(body) != 0 {
		t.Fatalf("character device EOF: %q, %v", body, err)
	}
}
