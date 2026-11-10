//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"ceraso.dev/bobo/internal/provider"
	"github.com/creack/pty"
	"github.com/hinshun/vt10x"
	"golang.org/x/sys/unix"
)

func TestOptions(t *testing.T) {
	o, err := parseOptions(nil, io.Discard)
	if err != nil || o.model != "gpt-6.1-sol" || o.effort != "xhigh" || o.tier != "default" || o.compactThreshold != 128000 || o.signin || o.logPath != "" {
		t.Fatalf("%+v %v", o, err)
	}
	o, err = parseOptions([]string{"-signin", "-model", "other", "-effort", "low", "-compact-threshold", "10", "-service-tier", "flex", "-log", "events.log"}, io.Discard)
	if err != nil || !o.signin || o.model != "other" || o.effort != "low" || o.compactThreshold != 10 || o.tier != "flex" || o.logPath != "events.log" {
		t.Fatalf("%+v %v", o, err)
	}
	for _, args := range [][]string{{"hello"}, {"-compact-threshold", "0"}, {"-model", ""}, {"-unknown"}} {
		if _, err := parseOptions(args, io.Discard); err == nil {
			t.Fatal(args)
		}
	}
	var help bytes.Buffer
	if _, err := parseOptions([]string{"-h"}, &help); err == nil || !strings.Contains(help.String(), "gpt-6.1-sol") {
		t.Fatalf("%q %v", help.String(), err)
	}
}

func TestCredentialStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "credentials.json")
	store := &credentialStore{path: path, fallback: "environment-secret"}
	ctx := t.Context()
	got, err := store.Load(ctx)
	if err != nil || got.FallbackToken != "environment-secret" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("environment-only Load created configuration")
	}
	want := provider.Credential{ClientID: "registration", AccessToken: "access", RefreshToken: "refresh", ExpiresAt: time.Now().Add(time.Hour).UTC(), FallbackToken: "saved-secret"}
	got, err = store.Update(ctx, func(current provider.Credential) (provider.Credential, error) {
		if current.FallbackToken != "environment-secret" {
			t.Fatal(current)
		}
		return want, nil
	})
	if err != nil || got != want {
		t.Fatalf("%+v %v", got, err)
	}
	got, err = store.Load(ctx)
	if err != nil || got != want {
		t.Fatalf("%+v %v", got, err)
	}
	for _, p := range []string{path, path + ".lock"} {
		info, err := os.Stat(p)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v %v", p, info, err)
		}
	}
	failure := errors.New("callback failed")
	_, err = store.Update(ctx, func(provider.Credential) (provider.Credential, error) { return provider.Credential{}, failure })
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
	got, err = store.Load(ctx)
	if err != nil || got != want {
		t.Fatal("callback failure changed credentials")
	}
	writeTestFile(t, path, `{"fallback_token": "secret", "invalid": secret}`)
	if _, err := store.Load(ctx); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("invalid credentials or secrets in diagnostics: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(ctx); err == nil {
		t.Fatal("insecure credentials accepted")
	}
}

func TestCredentialLockCancellationAndSerialization(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	store := &credentialStore{path: path}
	var calls atomic.Int64
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			s := &credentialStore{path: path}
			_, err := s.Update(t.Context(), func(c provider.Credential) (provider.Credential, error) {
				c.FallbackToken += "x"
				calls.Add(1)
				return c, nil
			})
			if err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	c, err := store.Load(t.Context())
	if err != nil || len(c.FallbackToken) != 12 || calls.Load() != 12 {
		t.Fatalf("%+v %v %d", c, err, calls.Load())
	}
	lock, err := os.OpenFile(path+".lock", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, err = store.Update(ctx, func(c provider.Credential) (provider.Credential, error) {
		t.Error("called callback while lock held")
		return c, nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestEditor(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	script := filepath.Join(dir, "editor")
	writeTestFile(t, script, "#!/bin/sh\n[ \"$1\" = --test ] || exit 1\n[ \"$(stat -c %a \"$2\")\" = 600 ] || exit 2\nprintf 'edited\\n世界\\n' > \"$2\"\n")
	if err := os.Chmod(script, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VISUAL", "'"+script+"' --test")
	got, err := editPrompt(t.Context(), "original $(not a command)")
	if err != nil || got != "edited\n世界" {
		t.Fatalf("%q %v", got, err)
	}
	t.Setenv("VISUAL", "true")
	got, err = editPrompt(t.Context(), "")
	if err != nil || got != "" {
		t.Fatalf("%q %v", got, err)
	}
	t.Setenv("VISUAL", "false")
	if _, err := editPrompt(t.Context(), "original"); err == nil {
		t.Fatal("editor failure ignored")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "bobo-prompt-*"))
	if len(files) != 0 {
		t.Fatal("temporary files leaked", files)
	}
}

func TestPathMatches(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"alpha.txt", "alpine.txt", "with space.txt", "世界.txt", ".hidden", "quote'file", "cash$file"} {
		writeTestFile(t, filepath.Join(root, name), "")
	}
	if err := os.Mkdir(filepath.Join(root, "directory"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	t.Setenv("HOME", root)
	for _, tt := range []struct {
		line string
		want []string
	}{
		{"explain al", []string{"alpha.txt", "alpine.txt"}},
		{"explain ./al", []string{"./alpha.txt", "./alpine.txt"}},
		{"first\n世", []string{"世界.txt"}},
		{"show with", []string{`with\ space.txt`}},
		{`show "with`, []string{`"with space.txt"`}},
		{"show 'quote", []string{`'quote'\''file'`}},
		{"show ~/dir", []string{"~/directory/"}},
		{"show .h", []string{".hidden"}},
		{"show cash", []string{`cash\$file`}},
	} {
		t.Run(tt.line, func(t *testing.T) {
			_, _, got := pathMatches([]rune(tt.line), len([]rune(tt.line)))
			if fmt.Sprint(got) != fmt.Sprint(tt.want) {
				t.Fatalf("got %q want %q", got, tt.want)
			}
		})
	}
	start, end, got := pathMatches([]rune("read alphaBAD then"), len([]rune("read alph")))
	if start != 5 || end != 13 || len(got) != 1 || got[0] != "alpha.txt" {
		t.Fatalf("%d %d %q", start, end, got)
	}
}

func TestPathMatchesApostrophes(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"alpha.txt", "quote'file", "with space.txt", "Cafe\u0301's note.txt"} {
		writeTestFile(t, filepath.Join(root, name), "")
	}
	dir := filepath.Join(root, "dir", "with space")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(dir, "alpha.txt"), "")
	t.Chdir(root)
	for _, prefix := range []string{
		"What's in ", "Don't forget ", "It's in ", "users' files ",
		"l'été\n", "Cafe\u0301's files ", "version2's files ",
		"What's in 'with space.txt' then ",
	} {
		t.Run(prefix, func(t *testing.T) {
			line := prefix + "alphBAD later"
			start, end, got := pathMatches([]rune(line), len([]rune(prefix+"alph")))
			if start != len([]rune(prefix)) || end != len([]rune(prefix+"alphBAD")) || len(got) != 1 || got[0] != "alpha.txt" {
				t.Fatalf("start=%d end=%d matches=%q", start, end, got)
			}
		})
	}
	for _, tt := range []struct{ typed, rest, want string }{
		{"quo", "te'file", `quote\'file`},
		{"quote'fi", "le", `quote\'file`},
		{`quote\'fi`, "le", `quote\'file`},
		{"Cafe\u0301's", "", "Cafe\u0301\\'s\\ note.txt"},
		{`'quote'\''fi`, `le'`, `'quote'\''file'`},
		{"'with sp", "ace.txt'", "'with space.txt'"},
		{`"with sp`, `ace.txt"`, `"with space.txt"`},
		{`"quote'fi`, `le"`, `"quote'file"`},
		{`./dir/'with space'/alph`, "BAD", `./dir/with\ space/alpha.txt`},
		{`'./dir/with space'/alph`, "BAD", `'./dir/with space/alpha.txt'`},
	} {
		t.Run(tt.typed, func(t *testing.T) {
			line := "read " + tt.typed + tt.rest + " later"
			start, end, got := pathMatches([]rune(line), len([]rune("read "+tt.typed)))
			if start != 5 || end != len([]rune("read "+tt.typed+tt.rest)) || len(got) != 1 || got[0] != tt.want {
				t.Fatalf("start=%d end=%d matches=%q, want %q", start, end, got, tt.want)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func echoClient() *provider.Client {
	client := provider.NewClient(&credentialStore{path: "/nonexistent/bobo-test-credentials", fallback: "test"})
	client.HTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var body struct {
			Input []map[string]any `json:"input"`
		}
		data, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &body); err != nil {
			return nil, err
		}
		text := ""
		for _, e := range body.Input {
			if e["role"] == "user" {
				text, _ = e["content"].(string)
			}
		}
		if text == "slow" || text == "slow fail" {
			path := os.Getenv("BOBO_TEST_RELEASE")
			if path == "" {
				return nil, errors.New("missing test release path")
			}
			if err := os.WriteFile(path+".started", nil, 0o600); err != nil {
				return nil, err
			}
			ticker := time.NewTicker(5 * time.Millisecond)
			defer ticker.Stop()
			for {
				if _, err := os.Stat(path); err == nil {
					break
				}
				select {
				case <-req.Context().Done():
					return nil, req.Context().Err()
				case <-ticker.C:
				}
			}
		}
		if text == "fail" || text == "slow fail" {
			return nil, errors.New("mock failure")
		}
		if text == "wait" {
			<-req.Context().Done()
			return nil, req.Context().Err()
		}
		payload, err := json.Marshal(map[string]any{
			"type": "response.completed", "response": map[string]any{
				"id": "response_test", "status": "completed", "output": []any{
					map[string]any{"type": "message", "role": "assistant", "phase": "final_answer", "content": []any{map[string]any{"type": "output_text", "text": "ANSWER:" + text + ":END"}}},
				},
			},
		})
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: " + string(payload) + "\n\n"))}, nil
	})}
	return client
}

func TestPipe(t *testing.T) {
	c := echoClient()
	s := provider.NewSession("test", "low", "", nil)
	var out bytes.Buffer
	if err := pipe(t.Context(), c, s, strings.NewReader("  hello\n世界\n"), &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "ANSWER:  hello\n世界\n:END\n" || strings.Contains(out.String(), "\a") || len(s.Events) != 2 {
		t.Fatalf("%q %+v", out.String(), s.Events)
	}
	if err := pipe(t.Context(), c, s, strings.NewReader("fail"), &out); err == nil || len(s.Events) != 3 {
		t.Fatalf("%v %+v", err, s.Events)
	}
}

func TestWriteAnswer(t *testing.T) {
	for _, tt := range []struct {
		text string
		bell bool
		want string
	}{
		{"hello", false, "hello\n"},
		{"hello\n", false, "hello\n"},
		{"世界", true, "世界\n\a"},
	} {
		var out bytes.Buffer
		if err := writeAnswer(t.Context(), &out, tt.text, tt.bell); err != nil || out.String() != tt.want {
			t.Fatalf("%q %v", out.String(), err)
		}
	}
}

func answerStream(t *testing.T, kind string) (*os.File, *os.File) {
	t.Helper()
	switch kind {
	case "pipe":
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		return r, w
	case "socket":
		fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
		if err != nil {
			t.Fatal(err)
		}
		return os.NewFile(uintptr(fds[0]), "reader"), os.NewFile(uintptr(fds[1]), "writer")
	case "tty":
		r, w, err := pty.Open()
		if err != nil {
			t.Fatal(err)
		}
		return r, w
	}
	t.Fatalf("unknown stream %q", kind)
	return nil, nil
}

func TestWriteAnswerStreamCancellation(t *testing.T) {
	for _, kind := range []string{"pipe", "socket", "tty"} {
		t.Run(kind, func(t *testing.T) {
			r, w := answerStream(t, kind)
			defer w.Close()
			defer r.Close()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			fd := w.Fd() // Start with a genuinely blocking caller descriptor.
			flags, err := unix.FcntlInt(fd, unix.F_GETFL, 0)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- writeAnswer(ctx, w, strings.Repeat("x", 1<<20), true) }()
			deadline := time.Now().Add(3 * time.Second)
			for {
				fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLOUT}}
				if _, err := unix.Poll(fds, 0); err != nil {
					t.Fatal(err)
				}
				if fds[0].Revents == 0 {
					break
				}
				select {
				case err := <-done:
					t.Fatalf("write finished before blocking: %v", err)
				default:
				}
				if time.Now().After(deadline) {
					t.Fatal("output never blocked")
				}
				time.Sleep(5 * time.Millisecond)
			}
			if current, err := unix.FcntlInt(fd, unix.F_GETFL, 0); err != nil || current != flags {
				t.Fatalf("writer changed shared descriptor flags: %x -> %x: %v", flags, current, err)
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("blocked answer write ignored cancellation")
			}
			if current, err := unix.FcntlInt(fd, unix.F_GETFL, 0); err != nil || current != flags {
				t.Fatalf("caller descriptor was closed or changed: %x -> %x: %v", flags, current, err)
			}
		})
	}
}

func TestWriteAnswerStreamComplete(t *testing.T) {
	for _, kind := range []string{"pipe", "socket"} {
		t.Run(kind, func(t *testing.T) {
			r, w := answerStream(t, kind)
			defer w.Close()
			defer r.Close()
			answer := strings.Repeat("世界é", 1<<15)
			done := make(chan error, 1)
			go func() { done <- writeAnswer(t.Context(), w, answer, true) }()
			got := make([]byte, len(answer)+2)
			if _, err := io.ReadFull(r, got); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if string(got) != answer+"\n\a" {
				t.Fatal("partial stream writes changed the answer")
			}
			// Successful writes must not close the caller's descriptor either.
			if _, err := w.Write([]byte("tail")); err != nil {
				t.Fatal(err)
			}
			if _, err := io.ReadFull(r, got[:4]); err != nil || string(got[:4]) != "tail" {
				t.Fatalf("%q: %v", got[:4], err)
			}
		})
	}
}

func TestWriteAnswerRegularFiles(t *testing.T) {
	for _, appendMode := range []bool{false, true} {
		t.Run(strconv.FormatBool(appendMode), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "answer")
			writeTestFile(t, path, "abcdef")
			flags := os.O_WRONLY
			if appendMode {
				flags |= os.O_APPEND
			}
			file, err := os.OpenFile(path, flags, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			if _, err := file.Seek(2, io.SeekStart); err != nil {
				t.Fatal(err)
			}
			if err := writeAnswer(t.Context(), file, "Z", false); err != nil {
				t.Fatal(err)
			}
			want, offset := "abZ\nef", int64(4)
			if appendMode {
				want, offset = "abcdefZ\n", 8
			}
			body, err := os.ReadFile(path)
			if err != nil || string(body) != want {
				t.Fatalf("%q, want %q: %v", body, want, err)
			}
			if pos, err := file.Seek(0, io.SeekCurrent); err != nil || pos != offset {
				t.Fatalf("offset %d, want %d: %v", pos, offset, err)
			}
		})
	}
}

func TestWriteAnswerErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var out bytes.Buffer
	if err := writeAnswer(ctx, &out, "canceled", true); !errors.Is(err, context.Canceled) || out.Len() != 0 {
		t.Fatalf("%q: %v", out.String(), err)
	}
	short := terminalWriterFunc(func(p []byte) (int, error) { return len(p) - 1, nil })
	if err := writeAnswer(t.Context(), short, "short", false); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal(err)
	}
	file, err := os.OpenFile("/dev/full", os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := writeAnswer(t.Context(), file, "full", false); !errors.Is(err, unix.ENOSPC) {
		t.Fatal(err)
	}
	null, err := os.OpenFile("/dev/null", os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	if err := writeAnswer(t.Context(), null, "discarded", false); err != nil {
		t.Fatal(err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if err := writeAnswer(t.Context(), r, "read-only", false); !errors.Is(err, unix.EBADF) {
		t.Fatal(err)
	}
}

func TestStartupAndLog(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("OPENAI_API_KEY", "")
	var stderr bytes.Buffer
	if err := run(t.Context(), nil, os.Stdin, io.Discard, &stderr); !errors.Is(err, provider.ErrNoCredentials) {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "events.log")
	writeTestFile(t, logPath, "existing\n")
	if err := run(t.Context(), []string{"-log", logPath}, os.Stdin, io.Discard, &stderr); err == nil {
		t.Fatal("missing credential error ignored")
	}
	body, _ := os.ReadFile(logPath)
	if !strings.HasPrefix(string(body), "existing\n") || !strings.Contains(string(body), "application.failed") {
		t.Fatalf("%q", body)
	}
	if err := run(t.Context(), []string{"-log", filepath.Join(root, "absent", "file")}, os.Stdin, io.Discard, &stderr); err == nil || !strings.Contains(err.Error(), "open log") {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	file, err := os.OpenFile("/dev/full", os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	sink := &logSink{file: file, cancel: cancel}
	if _, err := sink.Write([]byte("event")); err == nil || sink.failure() == nil || ctx.Err() == nil {
		t.Fatal("log write failure ignored")
	}
}

// Run the actual readline loop in a subprocess; never replace process-global
// stdin/stdout in the test runner. The HTTP transport is local and deterministic.
func TestConsoleHelper(t *testing.T) {
	if os.Getenv("BOBO_TEST_CONSOLE") != "1" {
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	prefill, _ := strconv.Atoi(os.Getenv("BOBO_TEST_PREFILL"))
	for i := range prefill {
		fmt.Fprintf(os.Stdout, "shell line %d\n", i+1)
	}
	err := console(ctx, cancellationTestClient(), provider.NewSession("test", "low", "", cancellationTestFunctions()), os.Stdout, os.Stderr)
	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

// renderedConsole models what a user sees, not just bytes that happened to be
// emitted. vt10x also answers DSR queries at the exact rendered cursor position.
type renderedConsole struct {
	t            *testing.T
	tty          *os.File
	term         vt10x.Terminal
	cmd          *exec.Cmd
	done         chan struct{}
	ready        atomic.Bool
	release      string
	rawMu        sync.Mutex
	raw          bytes.Buffer
	pauseAnswer  atomic.Bool
	outputPaused chan struct{}
	resumeOutput chan struct{}
}

type terminalWriterFunc func([]byte) (int, error)

func (f terminalWriterFunc) Write(p []byte) (int, error) { return f(p) }

func startRenderedConsole(t *testing.T, cols, rows, prefill int, reply ...func(*os.File, []byte) (int, error)) *renderedConsole {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestConsoleHelper$")
	release := filepath.Join(t.TempDir(), "release")
	cmd.Env = append(os.Environ(), "BOBO_TEST_CONSOLE=1", "TERM=xterm", "INPUTRC=/dev/null", "BOBO_TEST_RELEASE="+release, fmt.Sprintf("BOBO_TEST_PREFILL=%d", prefill))
	tty, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	var writer io.Writer = tty
	if len(reply) > 0 {
		writer = terminalWriterFunc(func(p []byte) (int, error) { return reply[0](tty, p) })
	}
	c := &renderedConsole{t: t, tty: tty, cmd: cmd, release: release, done: make(chan struct{}), outputPaused: make(chan struct{}), resumeOutput: make(chan struct{}), term: vt10x.New(vt10x.WithSize(cols, rows), vt10x.WithWriter(writer))}
	go func() {
		defer close(c.done)
		buf := make([]byte, 4096)
		var pending []byte
		for {
			n, err := tty.Read(buf)
			if n > 0 {
				c.ready.Store(false)
				pending = append(pending, buf[:n]...)
				complete := 0
				for complete < len(pending) && utf8.FullRune(pending[complete:]) {
					_, size := utf8.DecodeRune(pending[complete:])
					complete += size
				}
				_, _ = c.term.Write(pending[:complete])
				pending = pending[complete:]
				c.rawMu.Lock()
				c.raw.Write(buf[:n])
				raw := c.raw.Bytes()
				show := bytes.LastIndex(raw, []byte("\x1b[?25h"))
				c.ready.Store(show > bytes.LastIndex(raw, []byte("\x1b[6n")) && show > bytes.LastIndex(raw, []byte("\x1b[?25l")))
				pause := c.pauseAnswer.Load() && bytes.Contains(raw, []byte("OUTPUT-BEGIN"))
				c.rawMu.Unlock()
				if pause && c.pauseAnswer.CompareAndSwap(true, false) {
					close(c.outputPaused)
					select {
					case <-c.resumeOutput:
					case <-ctx.Done():
						return
					}
				}
			}
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		cancel()
		_ = tty.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		<-c.done
	})
	return c
}

func (c *renderedConsole) screen() string {
	lines := strings.Split(c.term.String(), "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}
	return strings.Join(lines, "\n")
}

func (c *renderedConsole) send(text string) {
	c.t.Helper()
	c.ready.Store(false)
	if _, err := io.WriteString(c.tty, text); err != nil {
		c.t.Fatal(err)
	}
}

func (c *renderedConsole) wait(text string) string {
	c.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		screen := c.screen()
		if c.ready.Load() && strings.Contains(screen, text) {
			return screen
		}
		if time.Now().After(deadline) {
			c.t.Fatalf("waiting for %q on rendered screen:\n%s", text, screen)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (c *renderedConsole) output() string {
	c.rawMu.Lock()
	defer c.rawMu.Unlock()
	return c.raw.String()
}

func (c *renderedConsole) waitStarted() {
	c.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(c.release + ".started"); err == nil {
			return
		}
		if time.Now().After(deadline) {
			c.t.Fatal("mock request did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestConsoleRenderedCursorReplyTypeahead(t *testing.T) {
	for _, split := range []bool{false, true} {
		t.Run(fmt.Sprint(split), func(t *testing.T) {
			first := true
			c := startRenderedConsole(t, 60, 8, 0, func(tty *os.File, reply []byte) (int, error) {
				if !first {
					return tty.Write(reply)
				}
				first = false
				// Reproduce typing coalesced with a DSR reply, including a
				// cursor report split across reads. No keys may be lost.
				if split {
					if _, err := tty.Write(append([]byte("before"), reply[:len(reply)/2]...)); err != nil {
						return 0, err
					}
					time.Sleep(10 * time.Millisecond)
					_, err := tty.Write(append(append([]byte{}, reply[len(reply)/2:]...), []byte("after")...))
					return len(reply), err
				}
				_, err := tty.Write(append(append([]byte("before"), reply...), []byte("after")...))
				return len(reply), err
			})
			c.wait("> beforeafter")
			c.send("\r")
			c.wait("ANSWER:beforeafter:END")
		})
	}
}

func (c *renderedConsole) waitOutput(text string) {
	c.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(c.output(), text) {
		if time.Now().After(deadline) {
			c.t.Fatalf("waiting for raw output %q: %q", text, c.output())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (c *renderedConsole) waitExit() {
	c.t.Helper()
	done := make(chan error, 1)
	go func() { done <- c.cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			c.t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		c.t.Fatal("cancellation did not unblock terminal input")
	}
	state, err := unix.IoctlGetTermios(int(c.tty.Fd()), unix.TCGETS)
	if err != nil || state.Lflag&(unix.ICANON|unix.ECHO) != unix.ICANON|unix.ECHO {
		c.t.Fatalf("terminal was not restored: %+v %v", state, err)
	}
}

func TestConsoleRenderedAccentedInput(t *testing.T) {
	c := startRenderedConsole(t, 60, 10, 0)
	c.wait(">")
	for _, text := range []string{"café", "ação", "naïve mañana", "ÆØå ©µ", "e\u0301cole"} {
		c.send(text + "\r")
		c.waitOutput("ANSWER:" + text + ":END")
		c.wait(">")
	}
}

func TestConsoleRenderedCompletionAfterContraction(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "alpha.txt"), "")
	c := startRenderedConsole(t, 60, 10, 0)
	c.wait(">")
	prefix := "What's in " + root + "/"
	c.send(prefix + "alph\t\r")
	c.waitOutput("ANSWER:" + prefix + "alpha.txt:END")
	c.wait(">")
	// Completion replaces the entire path token, not text following it.
	c.send(prefix + "alphBAD later" + strings.Repeat("\x1b[D", len("BAD later")) + "\t\r")
	c.waitOutput("ANSWER:" + prefix + "alpha.txt later:END")
}

func TestConsoleRenderedUnicodeAtCursor(t *testing.T) {
	c := startRenderedConsole(t, 60, 10, 0)
	c.wait(">")
	for _, text := range []string{"世", "e\u0301"} {
		c.send("ab\x1b[D" + text + "X\r")
		c.waitOutput("ANSWER:a" + text + "Xb:END")
		c.wait(">")
	}
}

func TestConsoleRenderedSplitUTF8(t *testing.T) {
	for _, busy := range []bool{false, true} {
		for _, char := range []string{"世", "é"} {
			for split := 1; split < len(char); split++ {
				t.Run(fmt.Sprintf("%t/%s/%d", busy, char, split), func(t *testing.T) {
					c := startRenderedConsole(t, 60, 10, 0)
					c.wait(">")
					if busy {
						c.send("slow\r")
						c.waitStarted()
					}
					c.send(char[:split])
					time.Sleep(25 * time.Millisecond)
					if busy {
						writeTestFile(t, c.release, "")
						c.waitOutput("ANSWER:slow:END")
						c.wait(">")
					}
					c.send(char[split:] + " done\r")
					c.waitOutput("ANSWER:" + char + " done:END")
				})
			}
		}
	}
}

func TestConsoleCancellationDuringCursorQuery(t *testing.T) {
	c := startRenderedConsole(t, 60, 10, 0, func(_ *os.File, report []byte) (int, error) {
		return len(report), nil // This terminal never answers DSR.
	})
	c.waitOutput("\x1b[6n")
	if err := c.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	c.waitExit()
}

func TestConsoleCursorQueryTimeoutPreservesInput(t *testing.T) {
	c := startRenderedConsole(t, 60, 10, 0, func(_ *os.File, report []byte) (int, error) {
		return len(report), nil
	})
	c.waitOutput("\x1b[6n")
	c.send("hello\r")
	c.waitOutput("ANSWER:hello:END")
}

func TestConsoleCancellationDuringPaste(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT} {
		t.Run(sig.String(), func(t *testing.T) {
			c := startRenderedConsole(t, 60, 10, 0)
			c.wait(">")
			c.send("\x1b[200~unfinished paste")
			time.Sleep(30 * time.Millisecond)
			if err := c.cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			if sig == syscall.SIGTERM {
				c.waitExit()
			} else {
				c.wait(">")
				c.send("fresh\r")
				c.waitOutput("ANSWER:fresh:END")
			}
		})
	}
}

func TestConsoleKeyboardInterruptDuringPaste(t *testing.T) {
	for _, prefix := range []string{"unfinished paste", "split UTF-8 \xe4"} {
		t.Run(fmt.Sprintf("%x", prefix), func(t *testing.T) {
			c := startRenderedConsole(t, 60, 10, 0)
			c.wait(">")
			c.send("draft\x1b[200~" + prefix)
			time.Sleep(30 * time.Millisecond)
			// Send the actual keyboard byte, not an out-of-band SIGINT.
			// Buffered paste/typeahead after it must not submit a new turn.
			if prefix == "unfinished paste" {
				c.send("\x03") // No closing paste delimiter ever arrives.
			} else {
				c.send("\x03discarded\r\x1b[201~")
			}
			c.waitOutput("\x1b[?2004l")
			c.wait(">")
			c.send("fresh\r")
			c.waitOutput("ANSWER:fresh:END")
			if strings.Contains(c.output(), "ANSWER:discarded") || strings.Contains(c.output(), "ANSWER:draft") {
				t.Fatal("canceled paste was submitted")
			}
		})
	}
}

func TestConsoleRejectsRedirectedStdout(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestConsoleHelper$")
	cmd.Env = append(os.Environ(), "BOBO_TEST_CONSOLE=1", "TERM=xterm", "INPUTRC=/dev/null")
	file, err := os.CreateTemp(t.TempDir(), "answers")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	cmd.Stdout = file
	tty, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 10, Cols: 60})
	if err != nil {
		t.Fatal(err)
	}
	defer tty.Close()
	defer cmd.Process.Kill()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("redirected interactive output was not rejected")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("redirected stdout hung in a cursor query")
	}
	output, _ := io.ReadAll(tty)
	if !strings.Contains(string(output), "terminal stdout") {
		t.Fatalf("missing actionable error: %q", output)
	}
	body, err := os.ReadFile(file.Name())
	if err != nil || len(body) != 0 {
		t.Fatalf("UI traffic leaked to redirected stdout: %q %v", body, err)
	}
}

func TestConsoleRenderedAltBackspace(t *testing.T) {
	for _, key := range []string{"\x1b\x7f", "\x1b\x08"} {
		t.Run(fmt.Sprintf("%x", key), func(t *testing.T) {
			c := startRenderedConsole(t, 60, 10, 0)
			c.wait(">")
			c.send("one two" + key + "three\r")
			c.wait("ANSWER:one three:END")
			c.send("one two tail" + strings.Repeat("\x1b[D", 5) + key + "three\r")
			c.wait("ANSWER:one three tail:END")
			c.send("unicode 世界" + key + "three\r")
			c.wait("ANSWER:unicode three:END")
			c.send(key + "empty\r")
			c.wait("ANSWER:empty:END")
			for i, backspace := range []string{"\x7f", "\x08"} {
				prefix := fmt.Sprintf("backspace%d ", i)
				c.send(prefix + "two" + backspace + "X\r")
				c.wait("ANSWER:" + prefix + "twX:END")
			}
		})
	}
}

func TestConsoleRenderedPlainTyping(t *testing.T) {
	c := startRenderedConsole(t, 60, 8, 7)
	c.wait(">")
	probes := strings.Count(c.output(), "\x1b[6n")
	if probes == 0 {
		t.Fatal("initial cursor-position query was disabled")
	}
	text := ""
	for _, r := range "hello # ordinary text" {
		text += string(r)
		c.send(string(r))
		c.wait("> " + strings.TrimRight(text, " "))
		if count := strings.Count(c.output(), "\x1b[6n"); count != probes {
			t.Fatalf("typing split a repaint with a cursor query: %d -> %d", probes, count)
		}
	}
	c.term.Lock()
	for x := 2; x < 2+len(text); x++ {
		cell := c.term.Cell(x, 6) // One row is reserved below the bottom prompt.
		if cell.FG != vt10x.DefaultFG {
			c.term.Unlock()
			t.Fatalf("input character %d has foreground %v instead of the terminal default", x, cell.FG)
		}
	}
	c.term.Unlock()
	c.send("\r")
	c.wait("ANSWER:hello # ordinary text:END")
	c.send("next")
	c.wait("> next")
	for style := 0; style <= 6; style++ {
		if strings.Contains(c.output(), fmt.Sprintf("\x1b[%d q", style)) {
			t.Fatalf("editing or submitting changed the terminal cursor style to %d", style)
		}
	}
}

func TestConsoleRenderedResize(t *testing.T) {
	c := startRenderedConsole(t, 60, 10, 9)
	c.wait(">")
	text := strings.Repeat("x", 48)
	c.send(text)
	c.wait("> " + text)
	probes := strings.Count(c.output(), "\x1b[6n")
	c.term.Resize(40, 10)
	if err := pty.Setsize(c.tty, &pty.Winsize{Cols: 40, Rows: 10}); err != nil {
		t.Fatal(err)
	}
	c.wait("> " + strings.Repeat("x", 38) + "\n" + strings.Repeat("x", 10))
	if strings.Count(c.output(), "\x1b[6n") <= probes {
		t.Fatal("resize did not invalidate the cursor-position cache")
	}
	c.send("\r")
	c.wait(strings.Repeat("x", 15) + ":END")
	c.send("next")
	c.wait("> next")
}

func TestConsoleRenderedBusyTypeahead(t *testing.T) {
	for _, prefill := range []int{0, 9} {
		t.Run(fmt.Sprint(prefill), func(t *testing.T) {
			c := startRenderedConsole(t, 60, 10, prefill)
			c.wait(">")
			c.send("slow\r")
			c.waitStarted()
			state, err := unix.IoctlGetTermios(int(c.tty.Fd()), unix.TCGETS)
			if err != nil || state.Lflag&(unix.ECHO|unix.ICANON|unix.ISIG) != unix.ECHO|unix.ICANON|unix.ISIG {
				t.Fatalf("busy terminal is not cooked with echo/signals: %+v %v", state, err)
			}
			prompts := strings.Count(c.output(), "> ")
			probes := strings.Count(c.output(), "\x1b[6n")
			c.send("next 世界")
			c.wait("next 世界")
			if strings.Count(c.output(), "> ") != prompts || strings.Count(c.output(), "\x1b[6n") != probes {
				t.Fatal("a live prompt/editor appeared while the request was running")
			}
			writeTestFile(t, c.release, "")
			c.wait("ANSWER:slow:END")
			c.wait("> next 世界")
			if strings.Contains(c.output(), "ANSWER:next 世界") {
				t.Fatal("partial typeahead submitted without Enter")
			}
			c.send("\r")
			c.wait("ANSWER:next 世界:END")
			if bells := strings.Count(c.output(), "\a"); bells != 2 {
				t.Fatalf("got %d completion bells", bells)
			}
		})
	}
}

func TestConsoleRenderedBusyEnter(t *testing.T) {
	c := startRenderedConsole(t, 60, 12, 0)
	c.wait(">")
	c.send("slow\r")
	c.waitStarted()
	c.send("first\rsecond\runsent")
	c.wait("unsent")
	if strings.Contains(c.output(), "ANSWER:first") || strings.Contains(c.output(), "> first") {
		t.Fatal("typeahead was consumed before the foreground turn finished")
	}
	writeTestFile(t, c.release, "")
	c.wait("ANSWER:second:END\n> unsent")
	raw := c.output()
	if strings.Index(raw, "ANSWER:slow:END") > strings.Index(raw, "ANSWER:first:END") || strings.Index(raw, "ANSWER:first:END") > strings.Index(raw, "ANSWER:second:END") {
		t.Fatal("buffered Enter presses were not processed in order")
	}
	if strings.Contains(raw, "ANSWER:unsent") {
		t.Fatal("partial typeahead was submitted")
	}
}

func TestConsoleRenderedBusyCancelAndFailure(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			c := startRenderedConsole(t, 100, 12, 11)
			c.wait(">")
			if failure {
				c.send("slow fail\r")
			} else {
				// Include bytes already in readline's buffer before entering
				// cooked mode; Ctrl+C must discard these as well as tty input.
				c.send("slow\rdiscard buffered\r")
			}
			c.waitStarted()
			c.send("typeahead")
			c.wait("typeahead")
			if failure {
				writeTestFile(t, c.release, "")
				c.wait("mock failure\n> typeahead")
				c.send("\r")
				c.wait("ANSWER:typeahead:END")
			} else {
				c.send("\x03")
				c.wait("context canceled\n>")
				if strings.Contains(c.output(), "\a") {
					t.Fatal("canceled turn rang the bell")
				}
				c.send("fresh\r")
				c.wait("ANSWER:fresh:END")
				if strings.Contains(c.output(), "ANSWER:discard buffered") || strings.Contains(c.output(), "ANSWER:typeahead") {
					t.Fatal("canceled typeahead was submitted")
				}
			}
			if bells := strings.Count(c.output(), "\a"); bells != 1 {
				t.Fatalf("got %d completion bells", bells)
			}
		})
	}
}

func TestConsoleRenderedBusyEOF(t *testing.T) {
	c := startRenderedConsole(t, 60, 8, 0)
	c.wait(">")
	c.send("slow\r")
	c.waitStarted()
	c.send("\x04")
	// The request remains blocked: EOF is tty input, not an editor command
	// or an instruction to cancel an active request.
	time.Sleep(30 * time.Millisecond)
	state, err := unix.IoctlGetTermios(int(c.tty.Fd()), unix.TCGETS)
	if err != nil || state.Lflag&(unix.ECHO|unix.ICANON) != unix.ECHO|unix.ICANON {
		t.Fatalf("EOF left the foreground turn: %+v %v", state, err)
	}
	if strings.Contains(c.output(), "context canceled") {
		t.Fatal("EOF canceled the request")
	}
	writeTestFile(t, c.release, "")
	c.wait("ANSWER:slow:END")
	c.send("fresh\r")
	c.wait("ANSWER:fresh:END")
}

func TestConsoleRenderedBusyExit(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			c := startRenderedConsole(t, 60, 8, 0)
			c.wait(">")
			c.send("slow\r")
			c.waitStarted()
			if err := c.cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- c.cmd.Wait() }()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("exit did not cancel the active request")
			}
			state, err := unix.IoctlGetTermios(int(c.tty.Fd()), unix.TCGETS)
			if err != nil || state.Lflag&(unix.ICANON|unix.ECHO) != unix.ICANON|unix.ECHO {
				t.Fatalf("terminal was not restored: %+v %v", state, err)
			}
		})
	}
}

func TestConsoleRenderedBottom(t *testing.T) {
	c := startRenderedConsole(t, 60, 8, 7)
	c.wait(">")
	c.send("hello")
	c.wait("> hello")
	c.send("\r")
	c.wait("ANSWER:hello:END")
	c.send("next")
	screen := c.wait("> next")
	if !strings.Contains(screen, "> hello\nANSWER:hello:END\n> next") {
		t.Fatalf("editing the next prompt overwrote the previous turn:\n%s", screen)
	}
}

func TestConsoleRenderedEmptyContinuation(t *testing.T) {
	c := startRenderedConsole(t, 40, 10, 9)
	c.wait(">")
	c.send("first\n")
	c.wait("> first\n>\n")
	c.send("\nthird")
	c.wait("> first\n>\n> third")
	c.send("\r")
	c.wait("ANSWER:first\n\nthird:END")
}

func TestConsoleRenderedContinuationPrompts(t *testing.T) {
	for _, prefill := range []int{0, 9} {
		t.Run(fmt.Sprint(prefill), func(t *testing.T) {
			c := startRenderedConsole(t, 40, 10, prefill)
			c.wait(">")
			// Logical newlines get >; a visual wrap does not.
			c.send(strings.Repeat("x", 48) + "\nsecond\nthird")
			c.wait("> " + strings.Repeat("x", 38) + "\n" + strings.Repeat("x", 10) + "\n> second\n> third")
			c.send("\x1b[D!")
			c.wait("> thir!d")
			c.send("\x01\x7f")
			screen := c.wait("> secondthir!d")
			if strings.Contains(screen, "> third") || strings.Contains(screen, "> thir!d") {
				t.Fatalf("joined line left a stale prompt:\n%s", screen)
			}
			c.send("\r")
			c.wait("secondthir!d:END")
			c.send("next")
			c.wait("> next")
		})
	}
}

func TestConsoleRenderedWrappingAndMultiline(t *testing.T) {
	for _, prefill := range []int{0, 9} {
		t.Run(fmt.Sprint(prefill), func(t *testing.T) {
			c := startRenderedConsole(t, 40, 10, prefill)
			c.wait(">")
			text := strings.Repeat("x", 48)
			c.send(text)
			c.wait("> " + strings.Repeat("x", 38) + "\n" + strings.Repeat("x", 10))
			// Edit the wrapped line before inserting a logical second line.
			c.send("\x7fY\nsecond")
			c.wait("second")
			c.send("\r")
			c.wait("second:END")
			c.send("next")
			screen := c.wait("> next")
			if !strings.Contains(screen, "second:END\n> next") || !strings.Contains(screen, "Y") {
				t.Fatalf("multiline submission/redraw damaged the answer:\n%s", screen)
			}
		})
	}
}

func TestConsolePTY(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestConsoleHelper$")
	cmd.Env = append(os.Environ(), "BOBO_TEST_CONSOLE=1", "TERM=xterm")
	cmd.Dir = t.TempDir()
	writeTestFile(t, filepath.Join(cmd.Dir, "a space.txt"), "")
	writeTestFile(t, filepath.Join(cmd.Dir, "世界.txt"), "")
	// Use a tiny executable so Ctrl+G exercises the real editor handoff.
	editor := filepath.Join(t.TempDir(), "editor")
	writeTestFile(t, editor, "#!/bin/sh\nprintf 'edited\\nprompt\\n' > \"$1\"\n")
	if err := os.Chmod(editor, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd.Env = append(cmd.Env, "VISUAL="+editor)
	tty, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 100})
	if err != nil {
		t.Fatal(err)
	}
	defer tty.Close()
	waited := false
	defer func() {
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	// Answer real cursor-position queries instead of disabling the production
	// terminal behavior just to make a bare PTY work.
	terminal := vt10x.New(vt10x.WithSize(100, 40), vt10x.WithWriter(tty))
	chunks := make(chan string, 128)
	go func() {
		defer close(chunks)
		buf := make([]byte, 4096)
		for {
			n, err := tty.Read(buf)
			if n > 0 {
				_, _ = terminal.Write(buf[:n])
				chunks <- string(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	var received strings.Builder
	waitFor := func(want string) {
		t.Helper()
		// A response or primary prompt can be printed before readline has
		// finished its following DSR/redraw. Wait for the completed frame too,
		// rather than typing into that transition and racing terminal replies.
		ready := func() bool {
			text := received.String()
			index := strings.Index(text, want)
			return index >= 0 && strings.Contains(text[index+len(want):], "\x1b[?25h")
		}
		for !ready() {
			select {
			case chunk, ok := <-chunks:
				if !ok {
					t.Fatalf("closed waiting for %q: %q", want, received.String())
				}
				received.WriteString(chunk)
			case <-ctx.Done():
				t.Fatalf("timeout waiting for %q: %q", want, received.String())
			}
		}
		received.Reset()
	}
	send := func(text string) {
		t.Helper()
		if _, err := io.WriteString(tty, text); err != nil {
			t.Fatal(err)
		}
	}
	waitFor("> ")
	send("first\nsecond\r")
	waitFor("ANSWER:first\r\nsecond:END\r\n\a")
	send("\x1b[200~paste\nlines\x1b[201~")
	time.Sleep(50 * time.Millisecond)
	send("\r")
	waitFor("ANSWER:paste\r\nlines:END\r\n\a")
	send("discard")
	waitFor("discard")
	send("\x03")
	waitFor("> ")
	send("fresh\r")
	waitFor("ANSWER:fresh:END\r\n\a")
	send("read a\t\r")
	waitFor("ANSWER:read a\\ space.txt:END\r\n\a")
	send("世\t\r")
	waitFor("ANSWER:世界.txt:END\r\n\a")
	send("draft\x07")
	waitFor("edited")
	send("\r")
	waitFor("ANSWER:edited\r\nprompt:END\r\n\a")
	send("wait\r")
	time.Sleep(100 * time.Millisecond)
	send("\x03")
	waitFor("context canceled")
	send("after cancel\r")
	waitFor("ANSWER:after cancel:END\r\n\a")
	send("fail\r")
	waitFor("mock failure")
	send("after failure\r")
	waitFor("ANSWER:after failure:END\r\n\a")
	send("\x04")
	err = cmd.Wait()
	waited = true
	if err != nil {
		t.Fatal(err)
	}
	state, err := unix.IoctlGetTermios(int(tty.Fd()), unix.TCGETS)
	if err != nil || state.Lflag&(unix.ICANON|unix.ECHO) != unix.ICANON|unix.ECHO {
		t.Fatalf("terminal was not restored: %+v %v", state, err)
	}
}

func TestConsoleTermination(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestConsoleHelper$")
	cmd.Env = append(os.Environ(), "BOBO_TEST_CONSOLE=1", "TERM=xterm")
	tty, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 100})
	if err != nil {
		t.Fatal(err)
	}
	defer tty.Close()
	defer cmd.Process.Kill()
	terminal := vt10x.New(vt10x.WithSize(100, 40), vt10x.WithWriter(tty))
	ready := make(chan struct{})
	readDone := make(chan struct{})
	var output bytes.Buffer
	go func() {
		defer close(readDone)
		buf := make([]byte, 4096)
		announced := false
		for {
			n, err := tty.Read(buf)
			_, _ = terminal.Write(buf[:n])
			output.Write(buf[:n])
			if !announced && bytes.Contains(output.Bytes(), []byte("> ")) {
				close(ready)
				announced = true
			}
			if err != nil {
				return
			}
		}
	}()
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("no prompt")
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	waitErr := cmd.Wait()
	<-readDone
	if waitErr != nil {
		t.Fatalf("%v: %s", waitErr, output.String())
	}
	state, err := unix.IoctlGetTermios(int(tty.Fd()), unix.TCGETS)
	if err != nil || state.Lflag&(unix.ICANON|unix.ECHO) != unix.ICANON|unix.ECHO {
		t.Fatalf("termination did not restore terminal: %+v %v", state, err)
	}
}

func TestPipeOutputHelper(t *testing.T) {
	if os.Getenv("BOBO_TEST_PIPE_OUTPUT") != "1" {
		return
	}
	// Exercise the real entry point, including cancellation/error reporting.
	// Every HTTP request is intercepted; no API credentials or network are used.
	http.DefaultClient = echoClient().HTTPClient
	os.Args = []string{"bobo"}
	main()
	os.Exit(0)
}

func TestPipeOutputCancellation(t *testing.T) {
	for _, tt := range []struct {
		sig    syscall.Signal
		merged bool
	}{
		{syscall.SIGTERM, false}, {syscall.SIGHUP, false}, {syscall.SIGINT, false},
		{syscall.SIGTERM, true},
	} {
		t.Run(fmt.Sprintf("%s/merged=%t", tt.sig, tt.merged), func(t *testing.T) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			defer w.Close()
			cmd := exec.Command(os.Args[0], "-test.run=^TestPipeOutputHelper$")
			cmd.Env = append(os.Environ(), "BOBO_TEST_PIPE_OUTPUT=1", "XDG_CONFIG_HOME="+t.TempDir(), "OPENAI_API_KEY=test")
			cmd.Stdin = strings.NewReader(strings.Repeat("x", 1<<20))
			cmd.Stdout = w
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if tt.merged {
				cmd.Stderr = w
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer cmd.Process.Kill()
			_ = w.Close()
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			// Do not drain stdout. Wait for the output pipe to be completely full
			// so the signal is delivered during a blocked write, not model work.
			fd := int(r.Fd())
			capacity, err := unix.FcntlInt(uintptr(fd), unix.F_GETPIPE_SZ, 0)
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(5 * time.Second)
			for {
				available, err := unix.IoctlGetInt(fd, unix.TIOCINQ)
				if err != nil {
					t.Fatal(err)
				}
				if available == capacity {
					break
				}
				select {
				case err := <-done:
					t.Fatalf("exited before filling stdout: %v: %s", err, stderr.String())
				default:
				}
				if time.Now().After(deadline) {
					t.Fatal("stdout pipe never filled")
				}
				time.Sleep(5 * time.Millisecond)
			}
			if err := cmd.Process.Signal(tt.sig); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 130 {
					t.Fatalf("expected cancellation exit status 130, got %v: %s", err, stderr.String())
				}
			case <-time.After(2 * time.Second):
				t.Fatal("signal did not interrupt the blocked answer write")
			}
		})
	}
}

func TestPipeReadCancellation(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	fd := r.Fd() // Match an inherited blocking stdin, not Go's pollable os.Pipe.
	flags, err := unix.FcntlInt(fd, unix.F_GETFL, 0)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- pipe(ctx, echoClient(), provider.NewSession("test", "low", "", nil), r, io.Discard)
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not interrupt blocking input")
	}
	if current, err := unix.FcntlInt(fd, unix.F_GETFL, 0); err != nil || current != flags {
		t.Fatalf("caller descriptor was closed or changed: %x -> %x: %v", flags, current, err)
	}
}

func TestCredentialProcessHelper(t *testing.T) {
	path := os.Getenv("BOBO_TEST_CREDENTIAL_PATH")
	if path == "" {
		return
	}
	s := &credentialStore{path: path}
	for range 3 {
		_, err := s.Update(t.Context(), func(c provider.Credential) (provider.Credential, error) {
			time.Sleep(time.Millisecond)
			c.FallbackToken += "x"
			return c, nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestCredentialProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	var commands []*exec.Cmd
	for range 4 {
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestCredentialProcessHelper$")
		cmd.Env = append(os.Environ(), "BOBO_TEST_CREDENTIAL_PATH="+path)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		commands = append(commands, cmd)
	}
	for _, cmd := range commands {
		if err := cmd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	got, err := (&credentialStore{path: path}).Load(t.Context())
	if err != nil || got.FallbackToken != strings.Repeat("x", 12) {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestCredentialRejectedFormatsAndCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	s := &credentialStore{path: path, fallback: "environment"}
	for _, body := range []string{"null", "  null\n", "[]", `{"fallback_token": 4}`, `{"fallback_token":"a","fallback_token":"b"}`} {
		writeTestFile(t, path, body)
		if _, err := s.Load(t.Context()); err == nil {
			t.Fatalf("accepted %q", body)
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	_, err := s.Update(ctx, func(c provider.Credential) (provider.Credential, error) {
		cancel()
		c.FallbackToken = "not committed"
		return c, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("canceled update was committed", err)
	}
}

type urlWriter struct{ urls chan string }

func (w urlWriter) Write(p []byte) (int, error) {
	w.urls <- strings.TrimSpace(string(p))
	return len(p), nil
}

func TestSignInThenPrompt(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	store := &credentialStore{path: filepath.Join(t.TempDir(), "credentials.json"), fallback: "fallback"}
	client := provider.NewClient(store)
	echo := echoClient().HTTPClient.Transport
	client.HTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/api/accounts/oauth/token" {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"access_token":"access","refresh_token":"refresh","expires_in":3600,"scope":"chatgpt.tokens.use.direct"}`))}, nil
		}
		return echo.RoundTrip(req)
	})}
	urls := make(chan string, 1)
	finished := make(chan error, 1)
	go func() { finished <- signIn(ctx, client, urlWriter{urls}) }()
	var raw string
	select {
	case raw = <-urls:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	authURL, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	params := authURL.Query()
	callback, err := url.Parse(params.Get("redirect_uri"))
	if err != nil {
		t.Fatal(err)
	}
	callback.RawQuery = url.Values{"state": {params.Get("state")}, "code": {"test-code"}, "client_id": {"issued-client"}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, callback.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(ctx)
	if err != nil || got.AccessToken != "access" || got.FallbackToken != "fallback" {
		t.Fatalf("%+v %v", got, err)
	}
	var out bytes.Buffer
	if err := pipe(ctx, client, provider.NewSession("test", "low", "", nil), strings.NewReader("after signin"), &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "ANSWER:after signin:END\n" {
		t.Fatal(out.String())
	}
}
