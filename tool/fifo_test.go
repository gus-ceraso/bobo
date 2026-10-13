package tool

import (
	"os"
	"syscall"
	"testing"
	"time"
)

// fifoTimeout is how long a tool may take before a call on a named pipe counts
// as hung.
const fifoTimeout = 5 * time.Second

// callWithTimeout runs f in a goroutine. It returns f's result and true, or nil
// and false if f hasn't returned after fifoTimeout. A hung goroutine is left
// behind, so the caller should release it with releaseFIFO.
func callWithTimeout(f func() any) (any, bool) {
	done := make(chan any, 1)
	go func() { done <- f() }()
	select {
	case v := <-done:
		return v, true
	case <-time.After(fifoTimeout):
		return nil, false
	}
}

// releaseFIFO opens the write end of the named pipe. A goroutine blocked in
// open for reading then completes, so it doesn't outlive the test.
func releaseFIFO(path string) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		if f, err := os.OpenFile(path, os.O_WRONLY, 0); err == nil {
			f.Close()
		}
	}()
	select {
	case <-done:
	case <-time.After(fifoTimeout):
	}
}

// Opening a named pipe for reading blocks until a writer appears. A pipe is
// not a text file, so grep must skip it without opening it.
func TestGrepSkipsNamedPipe(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, "a.txt", []byte("foo\n"))
	if err := syscall.Mkfifo("pipe", 0o644); err != nil {
		t.Fatal(err)
	}
	v, ok := callWithTimeout(func() any {
		return (&Grep{LocalFiles: true}).Call(map[string]any{"pattern": "foo"})
	})
	if !ok {
		releaseFIFO("pipe")
		t.Fatal("grep hung opening the named pipe")
	}
	meta, parts := parseResult[grepMeta](t, v)
	checkNoError(t, meta.Error)
	if want := []string{"a.txt:1:foo"}; !equalStrings(grepLines(t, parts), want) {
		t.Errorf("matches = %q, want %q", grepLines(t, parts), want)
	}
}
