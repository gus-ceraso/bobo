package function

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"ceraso.dev/bobo/internal/provider"
)

func TestFileFunctionGroups(t *testing.T) {
	r, v, w, e := NewFileFunctions()
	r2, v2, w2, e2 := NewFileFunctions()
	if r.locks != v.locks || r.locks != w.locks || r.locks != e.locks || r.locks == r2.locks || r2.locks != v2.locks || r2.locks != w2.locks || r2.locks != e2.locks {
		t.Fatal("incorrect shared registry ownership")
	}
	path := filepath.Join(t.TempDir(), "file")
	_, release, err := r.locks.acquire(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	_, release2, err := r2.locks.acquire(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	release2()
	requireEmptyRegistry(t, r2.locks)
}

func TestFilePathResolution(t *testing.T) {
	root := t.TempDir()
	outer := filepath.Join(root, "outer")
	target := filepath.Join(root, "target")
	if err := os.MkdirAll(filepath.Join(target, "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outer, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(target, "child"), filepath.Join(outer, "absolute")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../target/child", filepath.Join(outer, "relative")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("absent", filepath.Join(outer, "dangling")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("loop", filepath.Join(outer, "loop")); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(outer, "regular"), []byte("data"))
	writeFixture(t, filepath.Join(target, "file"), []byte("actual"))
	if err := os.Symlink(filepath.Join(target, "file"), filepath.Join(outer, "final-link")); err != nil {
		t.Fatal(err)
	}
	t.Chdir(outer)
	for _, tc := range []struct {
		path, want string
	}{
		{"file", outer + "/file"},
		{"./file", outer + "/file"},
		{"absolute/../file", target + "/file"},
		{"relative/../file", target + "/file"},
		{outer + "/absolute/../file", target + "/file"},
		{outer + "//relative/..//file", target + "/file"},
		{"absolute/../missing/nested/file", target + "/missing/nested/file"},
		{"absolute/../missing/./file", target + "/missing/file"},
		{"final-link", outer + "/final-link"},
		{"/file", "/file"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			r, _, _, _ := NewFileFunctions()
			got, release, err := r.locks.acquire(context.Background(), tc.path)
			if err != nil {
				t.Fatal(err)
			}
			release()
			if got != tc.want {
				t.Fatalf("resolved %q to %q, want %q", tc.path, got, tc.want)
			}
			requireEmptyRegistry(t, r.locks)
		})
	}
	for _, path := range []string{".", "..", "/", "absolute/", "dangling/file", "dangling/child/file", "loop/file", "regular/child", "regular/../file", "missing/../file", "bad\x00/child"} {
		t.Run("invalid/"+path, func(t *testing.T) {
			r, _, _, _ := NewFileFunctions()
			_, release, err := r.locks.acquire(context.Background(), path)
			if release != nil {
				release()
			}
			if err == nil {
				t.Fatalf("accepted invalid path %q", path)
			}
			requireEmptyRegistry(t, r.locks)
		})
	}
}

func TestSymlinkDotDotOperations(t *testing.T) {
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
	actual, decoy := filepath.Join(target, "file"), filepath.Join(outer, "file")
	writeFixture(t, actual, []byte("old"))
	writeFixture(t, decoy, []byte("decoy"))
	r, _, w, e := NewFileFunctions()
	raw := outer + "/link/../file"
	result := invoke(t, r, context.Background(), map[string]any{"path": raw})
	requireSuccess(t, result)
	assertAttribute(t, result, "path", raw)
	if result.body != "\nold\n" {
		t.Fatalf("read wrong file: %q", result.body)
	}
	requireSuccess(t, invoke(t, e, context.Background(), functionArgs(raw)))
	requireSuccess(t, invoke(t, w, context.Background(), map[string]any{"path": raw, "text": "written"}))
	if string(readFixture(t, actual)) != "written" || string(readFixture(t, decoy)) != "decoy" {
		t.Fatal("symlink/.. operations targeted the lexical rather than physical parent")
	}
	requireSuccess(t, invoke(t, w, context.Background(), map[string]any{"path": outer + "/link/../missing/nested/file", "text": "created"}))
	if string(readFixture(t, filepath.Join(target, "missing", "nested", "file"))) != "created" {
		t.Fatal("missing parent resolution targeted the wrong directory")
	}
}

func TestAllFileFunctionsWaitForSamePath(t *testing.T) {
	r, v, w, e := NewFileFunctions()
	dir := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "new", "file")
	for _, fn := range []provider.Function{r, v, w, e} {
		t.Run(functionName(fn), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				_, release, err := r.locks.acquire(context.Background(), path)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				start := time.Now()
				var parts []provider.Part
				done := make(chan struct{})
				go func() {
					defer close(done)
					parts = fn.Call(ctx, functionArgs(filepath.Join(alias, "new", "file")))
				}()
				synctest.Wait()
				select {
				case <-done:
					t.Error("function bypassed held lock")
				default:
				}
				r.locks.mu.Lock()
				refs := r.locks.entries[path].refs
				r.locks.mu.Unlock()
				if refs != 2 {
					t.Errorf("references = %d, want holder and waiter", refs)
				}
				cancel()
				<-done
				release()
				result := inspectResult(t, fn, parts, start, time.Now())
				requireError(t, result, context.Canceled.Error())
				requireEmptyRegistry(t, r.locks)
			})
		})
	}
}

func TestLockDeadlineAndIndependentPaths(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, _, _, _ := NewFileFunctions()
		dir := t.TempDir()
		path := filepath.Join(dir, "file")
		_, release, err := r.locks.acquire(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
		defer cancel()
		var waitErr error
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, unlock, err := r.locks.acquire(ctx, path)
			waitErr = err
			if unlock != nil {
				unlock()
			}
		}()
		synctest.Wait()
		_, otherRelease, err := r.locks.acquire(context.Background(), filepath.Join(dir, "other"))
		if err != nil {
			t.Fatal(err)
		}
		otherRelease()
		<-done
		if !errors.Is(waitErr, context.DeadlineExceeded) {
			t.Errorf("waiting error = %v", waitErr)
		}
		release()
		requireEmptyRegistry(t, r.locks)
	})
}

func TestDatesUseCallStart(t *testing.T) {
	for _, name := range []string{"read", "view", "write", "edit"} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r, v, w, e := NewFileFunctions()
				fn := map[string]provider.Function{"read": r, "view": v, "write": w, "edit": e}[name]
				path := filepath.Join(t.TempDir(), "file")
				_, release, err := r.locks.acquire(context.Background(), path)
				if err != nil {
					t.Fatal(err)
				}
				start := time.Now()
				var parts []provider.Part
				go func() { parts = fn.Call(context.Background(), functionArgs(path)) }()
				synctest.Wait()
				time.Sleep(time.Hour)
				release()
				synctest.Wait()
				result := inspectResult(t, fn, parts, start, time.Now())
				assertAttribute(t, result, "date", start.Format(time.RFC3339))
				requireEmptyRegistry(t, r.locks)
			})
		})
	}
}

func TestRegistryContentionAndCancellation(t *testing.T) {
	r, _, _, _ := NewFileFunctions()
	path := filepath.Join(t.TempDir(), "file")
	start := make(chan struct{})
	var wg sync.WaitGroup
	counter := 0
	errorsCh := make(chan error, 64)
	for i := range 64 {
		wg.Go(func() {
			<-start
			for j := range 20 {
				ctx, cancel := context.WithCancel(context.Background())
				if (i+j)%3 == 0 {
					go cancel()
				}
				_, release, err := r.locks.acquire(ctx, path)
				if err == nil {
					counter++
					release()
				} else if !errors.Is(err, context.Canceled) {
					errorsCh <- err
					cancel()
					return
				}
				cancel()
			}
		})
	}
	close(start)
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		t.Error(err)
	}
	if counter < 800 {
		t.Fatalf("too few successful acquisitions: %d", counter)
	}
	requireEmptyRegistry(t, r.locks)
}

func TestCancellationAfterPathReservation(t *testing.T) {
	for _, name := range []string{"lock", "read", "view", "write", "edit"} {
		t.Run(name, func(t *testing.T) {
			r, v, w, e := NewFileFunctions()
			path := pngFixture(t, sampleImage(8, 6))
			original := readFixture(t, path)
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			reserved := false
			ctx := &observingContext{Context: parent, onErr: func() {
				if pathReserved(r.locks, path) {
					reserved = true
					cancel()
				}
			}}
			if name == "lock" {
				_, release, err := r.locks.acquire(ctx, path)
				if release != nil {
					release()
				}
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("acquire = %v", err)
				}
			} else {
				fn := map[string]provider.Function{"read": r, "view": v, "write": w, "edit": e}[name]
				requireError(t, invoke(t, fn, ctx, functionArgs(path)), context.Canceled.Error())
			}
			if !reserved {
				t.Fatal("operation did not reserve the path before cancellation")
			}
			after, err := os.Stat(path)
			if err != nil || !os.SameFile(before, after) || string(readFixture(t, path)) != string(original) {
				t.Fatalf("canceled operation changed the destination: %v", err)
			}
			requireEmptyRegistry(t, r.locks)
			requireCleanDirectory(t, filepath.Dir(path))
		})
	}
}

func TestDeletedWorkingDirectory(t *testing.T) {
	oldPWD := os.Getenv("PWD")
	t.Setenv("PWD", "")
	t.Chdir(t.TempDir())
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(cwd); err != nil {
		t.Fatal(err)
	}
	r, _, _, _ := NewFileFunctions()
	_, release, err := r.locks.acquire(context.Background(), "file")
	if release != nil {
		release()
	}
	if err == nil || !strings.Contains(err.Error(), "getwd") {
		t.Fatalf("deleted working directory error = %v (original PWD %q)", err, oldPWD)
	}
}
