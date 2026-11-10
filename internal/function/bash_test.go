package function

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"ceraso.dev/bobo/internal/provider"
)

func TestBash(t *testing.T) {
	for _, tc := range []struct {
		name, command, body, exit, err string
	}{
		{"empty command", "", "\n", "0", ""},
		{"stdout", "printf hello", "\nhello\n", "0", ""},
		{"newline preserved", "printf 'hello\\n'", "\nhello\n", "0", ""},
		{"combined stderr", "printf stdout; printf stderr >&2", "\nstdoutstderr\n", "0", ""},
		{"nonzero exit", "printf failed; exit 7", "\nfailed\n", "7", "exit status 7"},
		{"bash features", "a=(one two); printf '%s' \"${a[1]}\"", "\ntwo\n", "0", ""},
		{"Unicode", "printf '%s' '世界🙂'", "\n世界🙂\n", "0", ""},
		{"NUL output", "printf 'a\\0b'", "\na\x00b\n", "0", ""},
		{"invalid UTF8", "printf '\\377A\\300\\257'", "\n\uFFFDA\uFFFD\n", "0", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &Bash{}
			ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
			defer cancel()
			result := invoke(t, b, ctx, map[string]any{"command": tc.command})
			assertAttribute(t, result, "command", wantPreview(tc.command))
			assertAttribute(t, result, "exit_code", tc.exit)
			assertAttribute(t, result, "truncated", "false")
			assertAttribute(t, result, "error", tc.err)
			if result.body != tc.body {
				t.Fatalf("body = %q, want %q", result.body, tc.body)
			}
		})
	}
}

func TestBashArgumentErrors(t *testing.T) {
	for _, args := range []map[string]any{nil, {"command": nil}, {"command": 1}, {"command": "\xff"}, {"command": "printf ignored\x00"}} {
		result := invoke(t, &Bash{}, context.Background(), args)
		requireError(t, result, "invalid arguments")
		assertAttribute(t, result, "exit_code", "-1")
		assertAttribute(t, result, "truncated", "false")
	}
}

func TestBashOutputRing(t *testing.T) {
	for _, size := range []int{(1 << 15) - 1, 1 << 15, (1 << 15) + 1, 3 * (1 << 15)} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			command := fmt.Sprintf("printf '%%0%ddEND' 0", size-3)
			result := invoke(t, &Bash{}, context.Background(), map[string]any{"command": command})
			requireSuccess(t, result)
			expected := strings.Repeat("0", size-3) + "END"
			truncated := size > 1<<15
			if truncated {
				expected = expected[len(expected)-(1<<15):]
			}
			if len(expected) < 1<<15 {
				expected += "\n"
			}
			assertAttribute(t, result, "truncated", fmt.Sprint(truncated))
			if result.body != "\n"+expected {
				t.Fatalf("ring mismatch: length=%d, want=%d", len(result.body), len(expected)+1)
			}
		})
	}
}

func TestBashUTF8RepairAndTruncation(t *testing.T) {
	for _, output := range []string{
		"x" + strings.Repeat("🙂", (1<<15)/4) + "!",
		strings.Repeat("🙂", (1<<15)/4-1),
	} {
		result := invoke(t, &Bash{}, context.Background(), map[string]any{"command": "printf '%s' " + shellQuote(output)})
		requireSuccess(t, result)
		expected := []byte(output)
		truncated := len(expected) > 1<<15
		if truncated {
			expected = expected[len(expected)-(1<<15):]
		}
		expected = bytes.ToValidUTF8(expected, []byte("\uFFFD"))
		if len(expected) > 1<<15 {
			start := len(expected) - (1 << 15)
			for !utf8.RuneStart(expected[start]) {
				start++
			}
			expected = expected[start:]
		}
		if len(expected) < 1<<15 {
			expected = append(expected, '\n')
		}
		if result.body != "\n"+string(expected) || !utf8.ValidString(result.body) {
			t.Fatal("UTF8 repair corrupted the retained suffix")
		}
		assertAttribute(t, result, "truncated", fmt.Sprint(truncated))
	}
	for _, command := range []string{
		"printf -v s '%016384d' 0; printf '%s' \"${s//0/$'\\xffA'}\"",
		"printf -v s '%010922d' 0; printf XX; printf '%s' \"${s//0/$'\\xffAA'}\"",
	} {
		result := invoke(t, &Bash{}, context.Background(), map[string]any{"command": command})
		requireSuccess(t, result)
		assertAttribute(t, result, "truncated", "true")
		if !utf8.ValidString(result.body) || len(strings.TrimPrefix(result.body, "\n")) > 1<<15 {
			t.Fatal("post-repair output exceeds the byte cap or contains invalid UTF8")
		}
	}
}

func TestBashCommandPreviews(t *testing.T) {
	for _, command := range []string{":" + strings.Repeat(" ", 78), ":" + strings.Repeat(" ", 79), ":" + strings.Repeat(" ", 80), ": #" + strings.Repeat("🙂", 90), "printf '%s' 'quoted \"text\" \\ >'"} {
		result := invoke(t, &Bash{}, context.Background(), map[string]any{"command": command})
		requireSuccess(t, result)
		assertAttribute(t, result, "command", wantPreview(command))
	}
}

func TestBashCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	marker := filepath.Join(t.TempDir(), "started")
	command := "printf started > " + shellQuote(marker) + "; exec sleep 30"
	start := time.Now()
	done := make(chan []provider.Part, 1)
	go func() { done <- (&Bash{}).Call(ctx, map[string]any{"command": command}) }()
	deadline := time.NewTimer(testTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
wait:
	for {
		select {
		case <-ticker.C:
			if _, err := os.Stat(marker); err == nil {
				break wait
			}
		case parts := <-done:
			t.Fatalf("command exited before cancellation: %#v", parts)
		case <-deadline.C:
			t.Fatal("command never started")
		}
	}
	cancel()
	select {
	case parts := <-done:
		result := inspectResult(t, &Bash{}, parts, start, time.Now())
		requireError(t, result, context.Canceled.Error())
		assertAttribute(t, result, "exit_code", "-1")
	case <-time.After(testTimeout):
		t.Fatal("command did not stop after cancellation")
	}
}

func TestBashDeadlineAndPreCanceledContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	result := invoke(t, &Bash{}, ctx, map[string]any{"command": "exec sleep 30"})
	requireError(t, result, context.DeadlineExceeded.Error())
	assertAttribute(t, result, "exit_code", "124")
	ctx, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	result = invoke(t, &Bash{}, ctx, map[string]any{"command": "printf should-not-run"})
	requireError(t, result, context.Canceled.Error())
	assertAttribute(t, result, "exit_code", "-1")
	if result.body != "\n" {
		t.Fatalf("pre-canceled command ran: %q", result.body)
	}
}
