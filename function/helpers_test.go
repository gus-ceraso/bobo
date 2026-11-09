package function

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ceraso.dev/bobo/provider"
)

const testTimeout = 5 * time.Second

var tagPattern = regexp.MustCompile(`^<([a-z]+)((?: [a-z_]+="(?:[^"\\]|\\.)*")*)(/?)>`)
var attributePattern = regexp.MustCompile(` ([a-z_]+)=("(?:[^"\\]|\\.)*")`)

type functionResult struct {
	parts []provider.Part
	attrs map[string]string
	body  string
}

func inspectResult(t testing.TB, fn provider.Function, parts []provider.Part, start, end time.Time) functionResult {
	t.Helper()
	if len(parts) == 0 || parts[0].ContentType != "text/plain" {
		t.Fatalf("missing text result: %#v", parts)
	}
	text := string(parts[0].Content)
	header := tagPattern.FindStringSubmatch(text)
	if len(header) != 4 || header[1] != fn.Schema()["name"] {
		t.Fatalf("invalid opening tag: %q", text)
	}
	attrs := make(map[string]string)
	for _, match := range attributePattern.FindAllStringSubmatch(header[2], -1) {
		value, err := strconv.Unquote(match[2])
		if err != nil {
			t.Fatalf("invalid attribute %q: %v", match[0], err)
		}
		if _, duplicate := attrs[match[1]]; duplicate {
			t.Fatalf("duplicate attribute %q", match[1])
		}
		attrs[match[1]] = value
	}
	date, err := time.Parse(time.RFC3339, attrs["date"])
	if err != nil || date.Before(start.Truncate(time.Second)) || date.After(end) {
		t.Fatalf("date = %q, %v; call interval [%v, %v]", attrs["date"], err, start, end)
	}
	if _, ok := attrs["error"]; !ok {
		t.Fatal("missing error attribute")
	}
	body := strings.TrimPrefix(text, header[0])
	if header[3] == "/" {
		if len(parts) != 1 || body != "" {
			t.Fatalf("invalid self-closing result: %#v", parts)
		}
	} else if len(parts) == 1 {
		closing := "</" + header[1] + ">"
		if !strings.HasSuffix(body, closing) {
			t.Fatalf("missing closing tag: %q", text)
		}
		body = strings.TrimSuffix(body, closing)
	} else if header[1] != "view" || len(parts) != 3 || body != "\n" || parts[1].ContentType != "image/png" || parts[2].ContentType != "text/plain" || string(parts[2].Content) != "\n</view>" {
		t.Fatalf("invalid multipart result: %#v", parts)
	}
	return functionResult{parts: parts, attrs: attrs, body: body}
}

func invoke(t testing.TB, fn provider.Function, ctx context.Context, args map[string]any) functionResult {
	t.Helper()
	start := time.Now()
	parts := fn.Call(ctx, args)
	return inspectResult(t, fn, parts, start, time.Now())
}

func requireSuccess(t testing.TB, result functionResult) {
	t.Helper()
	if result.attrs["error"] != "" {
		t.Fatalf("function failed: %s", result.parts[0].Content)
	}
}

func requireError(t testing.TB, result functionResult, contains string) {
	t.Helper()
	if result.attrs["error"] == "" || !strings.Contains(result.attrs["error"], contains) {
		t.Fatalf("error = %q, want substring %q", result.attrs["error"], contains)
	}
}

func writeFixture(t testing.TB, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFixture(t testing.TB, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func requireCleanDirectory(t testing.TB, dir string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, ".bobo-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temporary files leaked: %v, %v", matches, err)
	}
}

func requireEmptyRegistry(t testing.TB, locks *fileLocks) {
	t.Helper()
	locks.mu.Lock()
	defer locks.mu.Unlock()
	if len(locks.entries) != 0 {
		t.Fatalf("registry entries leaked: %v", locks.entries)
	}
}

func wantPreview(s string) string {
	runes := []rune(s)
	if len(runes) > 80 {
		return string(runes[:80]) + "..."
	}
	return s
}

type checkpointContext struct {
	context.Context
	calls atomic.Int32
	onErr func(int)
}

func (ctx *checkpointContext) Err() error {
	n := int(ctx.calls.Add(1))
	ctx.onErr(n)
	return ctx.Context.Err()
}

func cancellationAt(n int) (*checkpointContext, context.CancelFunc) {
	parent, cancel := context.WithCancel(context.Background())
	return &checkpointContext{Context: parent, onErr: func(call int) {
		if call == n {
			cancel()
		}
	}}, cancel
}

func number(n int) any { return json.Number(strconv.Itoa(n)) }

func assertAttribute(t testing.TB, result functionResult, name, want string) {
	t.Helper()
	if got, ok := result.attrs[name]; !ok || got != want {
		t.Fatalf("%s = %q (present=%t), want %q", name, got, ok, want)
	}
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }

func functionArgs(path string) map[string]any {
	return map[string]any{"path": path, "text": "text", "old_text": "old", "new_text": "new"}
}

func functionName(fn provider.Function) string { return fmt.Sprint(fn.Schema()["name"]) }
