package function

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"ceraso.dev/bobo/internal/provider"
	"github.com/disintegration/imaging"
)

func TestViewAndWriteSerialize(t *testing.T) {
	for _, first := range []string{"view", "write"} {
		t.Run(first+" first", func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r, v, w, _ := NewFileFunctions()
				input := sampleImage(4, 3)
				path := pngFixture(t, input)
				paused, resume := make(chan struct{}), make(chan struct{})
				var firstParts, secondParts []provider.Part
				firstCtx := &observingContext{Context: context.Background(), onErr: func() {
					if first == "view" && pathReserved(r.locks, path) {
						select {
						case <-paused:
						default:
							close(paused)
							<-resume
						}
					}
					if first == "write" {
						matches, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".bobo-*"))
						if len(matches) != 0 {
							select {
							case <-paused:
							default:
								close(paused)
								<-resume
							}
						}
					}
				}}
				start := time.Now()
				if first == "view" {
					go func() { firstParts = v.Call(firstCtx, map[string]any{"path": path, "horizontal_flip": true}) }()
				} else {
					go func() { firstParts = w.Call(firstCtx, map[string]any{"path": path, "text": "now a text file"}) }()
				}
				<-paused
				secondDone := make(chan struct{})
				go func() {
					defer close(secondDone)
					if first == "view" {
						secondParts = w.Call(context.Background(), map[string]any{"path": path, "text": "now a text file"})
					} else {
						secondParts = v.Call(context.Background(), map[string]any{"path": path})
					}
				}()
				synctest.Wait()
				select {
				case <-secondDone:
					t.Error("second operation did not wait for the entire first operation")
				default:
				}
				close(resume)
				synctest.Wait()
				if first == "view" {
					view := inspectResult(t, v, firstParts, start, time.Now())
					requireImageEqual(t, decodeView(t, view), imaging.FlipH(input))
					requireSuccess(t, inspectResult(t, w, secondParts, start, time.Now()))
				} else {
					requireSuccess(t, inspectResult(t, w, firstParts, start, time.Now()))
					requireError(t, inspectResult(t, v, secondParts, start, time.Now()), "unsupported format")
				}
				if string(readFixture(t, path)) != "now a text file" {
					t.Fatal("write did not replace the image")
				}
				requireEmptyRegistry(t, r.locks)
				requireCleanDirectory(t, filepath.Dir(path))
			})
		})
	}
}

func TestAtomicReplacementsVisibleToExternalReaders(t *testing.T) {
	_, _, w, e := NewFileFunctions()
	path := filepath.Join(t.TempDir(), "file")
	suffix := strings.Repeat("x", 1<<16)
	old, replacement := []byte("old"+suffix), []byte("new"+suffix)
	writeFixture(t, path, old)
	ctx, cancel := context.WithCancel(context.Background())
	done, started := make(chan struct{}), make(chan struct{})
	readErrors := make(chan error, 1)
	go func() {
		defer close(done)
		first := true
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			data, err := os.ReadFile(path)
			if first {
				close(started)
				first = false
			}
			if err != nil {
				readErrors <- err
				return
			}
			if !bytes.Equal(data, old) && !bytes.Equal(data, replacement) {
				readErrors <- fmt.Errorf("external reader observed a partial replacement (%d bytes)", len(data))
				return
			}
		}
	}()
	t.Cleanup(func() { cancel(); <-done })
	<-started
	for range 20 {
		requireSuccess(t, invoke(t, e, context.Background(), map[string]any{"path": path, "old_text": "old", "new_text": "new"}))
		requireSuccess(t, invoke(t, w, context.Background(), map[string]any{"path": path, "text": string(old)}))
	}
	cancel()
	<-done
	select {
	case err := <-readErrors:
		t.Fatal(err)
	default:
	}
	requireCleanDirectory(t, filepath.Dir(path))
}

func TestViewSymlinkDotDot(t *testing.T) {
	root := t.TempDir()
	outer, target := filepath.Join(root, "outer"), filepath.Join(root, "target")
	if err := os.MkdirAll(filepath.Join(target, "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outer, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(target, "child"), filepath.Join(outer, "link")); err != nil {
		t.Fatal(err)
	}
	input := sampleImage(3, 2)
	source := pngFixture(t, input)
	writeFixture(t, filepath.Join(target, "image"), readFixture(t, source))
	writeFixture(t, filepath.Join(outer, "image"), []byte("decoy"))
	_, v, _, _ := NewFileFunctions()
	got := decodeView(t, invoke(t, v, context.Background(), map[string]any{"path": outer + "/link/../image"}))
	requireImageEqual(t, got, image.Image(input))
}
