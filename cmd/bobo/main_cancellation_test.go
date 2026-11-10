//go:build linux

package main

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"ceraso.dev/bobo/internal/function"
	"ceraso.dev/bobo/internal/provider"
	"golang.org/x/sys/unix"
)

func cancellationTestFunctions() []provider.Function {
	return []provider.Function{&function.Bash{}}
}

// Special responses for cancellation regressions; all other prompts use the
// ordinary echo mock. No request leaves this process.
func cancellationTestClient() *provider.Client {
	client := echoClient()
	fallback := client.HTTPClient.Transport
	client.HTTPClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		data, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		var body struct {
			Input []map[string]any `json:"input"`
		}
		if err := json.Unmarshal(data, &body); err != nil {
			return nil, err
		}
		text := ""
		for _, event := range body.Input {
			if event["role"] == "user" {
				text, _ = event["content"].(string)
			}
		}
		var event map[string]any
		switch text {
		case "large answer":
			event = map[string]any{"type": "message", "role": "assistant", "phase": "final_answer", "content": "OUTPUT-BEGIN" + strings.Repeat("x", 1<<20) + "OUTPUT-END"}
		case "after output cancel":
			// Canceling delivery must not remove the completed model response.
			found := false
			for _, e := range body.Input {
				if e["role"] == "assistant" {
					encoded, _ := json.Marshal(e)
					found = found || bytes.Contains(encoded, []byte("OUTPUT-END"))
				}
			}
			if !found {
				return nil, errors.New("completed answer lost from conversation history")
			}
		case "bash wait":
			path := os.Getenv("BOBO_TEST_BASH_PATH")
			if path == "" {
				return nil, errors.New("missing Bash test path")
			}
			quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
			command := quote(os.Args[0]) + " -test.run=^TestBashDescendantHelper$ >/dev/null 2>&1 & child=$!; printf '%s' \"$$ $child\" > " + quote(path+".pids") + "; wait"
			args, _ := json.Marshal(map[string]string{"command": command})
			event = map[string]any{"type": "function_call", "name": "bash", "call_id": "bash_cancel", "arguments": string(args)}
		}
		if event == nil {
			req.Body = io.NopCloser(bytes.NewReader(data))
			return fallback.RoundTrip(req)
		}
		payload, err := json.Marshal(map[string]any{
			"type": "response.completed", "response": map[string]any{
				"id": "response_cancel", "status": "completed", "output": []any{event},
			},
		})
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("data: " + string(payload) + "\n\n"))}, nil
	})
	return client
}

func TestConsoleAnswerCancellation(t *testing.T) {
	for _, keyboard := range []bool{true, false} {
		t.Run(fmt.Sprintf("keyboard=%t", keyboard), func(t *testing.T) {
			c := startRenderedConsole(t, 80, 12, 0)
			c.wait(">")
			c.pauseAnswer.Store(true)
			c.send("large answer\r")
			select {
			case <-c.outputPaused:
			case <-time.After(5 * time.Second):
				t.Fatal("answer never started")
			}
			// The reader is now paused; a 1 MiB answer cannot fit in the PTY.
			state, err := unix.IoctlGetTermios(int(c.tty.Fd()), unix.TCGETS)
			if err != nil || state.Lflag&(unix.ICANON|unix.ISIG) != unix.ICANON|unix.ISIG {
				t.Fatalf("answer delivery left busy terminal mode: %+v %v", state, err)
			}
			c.send("discard typeahead\r")
			if keyboard {
				c.send("\x03")
			} else if err := c.cmd.Process.Signal(syscall.SIGINT); err != nil {
				t.Fatal(err)
			}
			// Observe cancellation before draining the output again. Otherwise
			// a broken implementation could finish writing and pass by timing.
			deadline := time.Now().Add(2 * time.Second)
			for {
				state, err := unix.IoctlGetTermios(int(c.tty.Fd()), unix.TCGETS)
				if err != nil {
					t.Fatal(err)
				}
				if state.Lflag&unix.ICANON == 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("Ctrl+C did not interrupt stalled answer delivery")
				}
				time.Sleep(5 * time.Millisecond)
			}
			close(c.resumeOutput)
			c.send("after output cancel\r")
			c.wait("ANSWER:after output cancel:END")
			raw := c.output()
			if strings.Contains(raw, "OUTPUT-END") || strings.Contains(raw, "ANSWER:discard typeahead") || strings.Contains(raw, "context canceled") {
				t.Fatal("canceled answer continued, submitted typeahead, or printed a cancellation diagnostic")
			}
			if bells := strings.Count(raw, "\a"); bells != 1 {
				t.Fatalf("got %d completion bells; want only the next turn's bell", bells)
			}
		})
	}
}

func TestBashDescendantHelper(t *testing.T) {
	path := os.Getenv("BOBO_TEST_BASH_PATH")
	if path == "" {
		return
	}
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	defer signal.Stop(interrupts)
	if err := os.WriteFile(path+".started", nil, 0o600); err != nil {
		os.Exit(1)
	}
	select {
	case <-interrupts:
		_ = os.WriteFile(path+".interrupted", nil, 0o600)
	case <-time.After(20 * time.Second):
	}
	os.Exit(0)
}

func TestConsoleKeyboardInterruptLeavesBashDescendant(t *testing.T) {
	path := filepath.Join(t.TempDir(), "descendant")
	t.Setenv("BOBO_TEST_BASH_PATH", path)
	c := startRenderedConsole(t, 80, 12, 0)
	t.Cleanup(func() {
		data, _ := os.ReadFile(path + ".pids")
		for _, field := range strings.Fields(string(data)) {
			if pid, err := strconv.Atoi(field); err == nil {
				_ = unix.Kill(pid, unix.SIGKILL)
			}
		}
	})
	c.wait(">")
	c.send("bash wait\r")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(path + ".started"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Bash descendant did not start: %s", c.output())
		}
		time.Sleep(5 * time.Millisecond)
	}
	data, err := os.ReadFile(path + ".pids")
	if err != nil {
		t.Fatal(err)
	}
	var shell, child int
	if _, err := fmt.Sscanf(string(data), "%d %d", &shell, &child); err != nil || shell <= 0 || child <= 0 {
		t.Fatalf("invalid process IDs %q: %v", data, err)
	}
	pgid, err := unix.Getpgid(child)
	if err != nil || pgid != shell || pgid == c.cmd.Process.Pid {
		t.Fatalf("Bash descendant not isolated from terminal foreground group: shell=%d child=%d pgid=%d: %v", shell, child, pgid, err)
	}
	c.send("\x03")
	c.wait("context canceled\n>")
	c.send("fresh\r")
	c.wait("ANSWER:fresh:END")
	if _, err := os.Stat(path + ".interrupted"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("keyboard Ctrl+C reached a Bash descendant: %v", err)
	}
	if err := unix.Kill(child, 0); err != nil {
		t.Fatalf("shell cancellation killed its descendant: %v", err)
	}
}

func TestInheritedStdinCancellation(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			defer w.Close() // Keep the producer alive until the child exits.
			if _, err := io.WriteString(w, "unfinished prompt"); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestPipeOutputHelper$")
			cmd.Env = append(os.Environ(), "BOBO_TEST_PIPE_OUTPUT=1", "XDG_CONFIG_HOME="+t.TempDir(), "OPENAI_API_KEY=test")
			cmd.Stdin = r
			var output bytes.Buffer
			cmd.Stdout, cmd.Stderr = &output, &output
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer cmd.Process.Kill()
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			// Consumption proves the real entry point has installed its signal
			// handlers and reached the blocking inherited stdin, without EOF.
			deadline := time.Now().Add(5 * time.Second)
			for {
				n, err := unix.IoctlGetInt(int(r.Fd()), unix.TIOCINQ)
				if err != nil {
					t.Fatal(err)
				}
				if n == 0 {
					break
				}
				select {
				case err := <-done:
					t.Fatalf("exited before reading stdin: %v: %s", err, output.String())
				default:
				}
				if time.Now().After(deadline) {
					t.Fatal("child did not read stdin")
				}
				time.Sleep(5 * time.Millisecond)
			}
			if err := cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 130 || output.Len() != 0 {
					t.Fatalf("want silent cancellation exit 130, got %v: %s", err, output.String())
				}
			case <-time.After(2 * time.Second):
				t.Fatal("signal did not interrupt inherited stdin without producer EOF")
			}
		})
	}
}

func TestInheritedStdoutReopenUnsupported(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root may bypass pipe reopen permissions")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if err := w.Chmod(0); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("direct write works")); err != nil {
		t.Fatal(err)
	}
	err = writeAnswer(t.Context(), w, "unsupported", false)
	if !errors.Is(err, unix.EACCES) || !strings.Contains(err.Error(), "unsupported stdout") || !strings.Contains(err.Error(), "/proc/self/fd") {
		t.Fatalf("missing explicit unsupported-output error: %v", err)
	}
}
