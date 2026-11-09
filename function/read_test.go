package function

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestRead(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		args          map[string]any
		body, next    string
		err           string
	}{
		{name: "empty"},
		{name: "default", content: "one\ntwo\n", body: "one\ntwo\n"},
		{name: "unterminated", content: "last", body: "last\n"},
		{name: "blank and CRLF", content: "\n\r\nlast\r\n", body: "\n\r\nlast\r\n"},
		{name: "Unicode and NUL", content: "世界🙂\x00\n", body: "世界🙂\x00\n"},
		{name: "offset and limit", content: "one\ntwo\nthree\n", args: map[string]any{"offset": number(2), "limit": number(1)}, body: "two\n", next: "3"},
		{name: "line numbers", content: "one\ntwo\nthree", args: map[string]any{"offset": number(2), "show_line_numbers": true}, body: "2:two\n3:three\n"},
		{name: "limit at EOF", content: "one\n", args: map[string]any{"limit": number(1)}, body: "one\n"},
		{name: "offset beyond EOF", content: "one\n", args: map[string]any{"offset": number(20)}},
		{name: "null defaults", content: "one\n", args: map[string]any{"offset": nil, "limit": nil, "show_line_numbers": nil}, body: "one\n"},
		{name: "default line cap", content: strings.Repeat("x\n", 1001), body: strings.Repeat("x\n", 1000), next: "1001"},
		{name: "exact byte cap", content: strings.Repeat("a", (1<<15)-1) + "\n", body: strings.Repeat("a", (1<<15)-1) + "\n"},
		{name: "exact cap with more", content: strings.Repeat("a", (1<<15)-1) + "\nnext\n", body: strings.Repeat("a", (1<<15)-1) + "\n", next: "2"},
		{name: "byte pagination", content: strings.Repeat("a", 20000) + "\n" + strings.Repeat("b", 20000) + "\n", body: strings.Repeat("a", 20000) + "\n", next: "2"},
		{name: "unterminated at cap", content: strings.Repeat("a", (1<<15)-1), body: strings.Repeat("a", (1<<15)-1) + "\n"},
		{name: "overlong line", content: strings.Repeat("a", 1<<15) + "\n", err: "32 KiB line limit"},
		{name: "overlong unterminated", content: strings.Repeat("a", 1<<15), err: "32 KiB line limit"},
		{name: "line number overhead", content: strings.Repeat("a", (1<<15)-1) + "\n", args: map[string]any{"show_line_numbers": true}, err: "32 KiB line limit"},
		{name: "invalid UTF8", content: "one\n\xff\n", err: "invalid utf-8"},
		{name: "skip huge invalid line", content: strings.Repeat("\xff", 3<<15) + "\nlast\n", args: map[string]any{"offset": number(2)}, body: "last\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _, _, _ := NewFileFunctions()
			path := filepath.Join(t.TempDir(), "input")
			writeFixture(t, path, []byte(tc.content))
			args := map[string]any{"path": path}
			for key, value := range tc.args {
				args[key] = value
			}
			result := invoke(t, r, context.Background(), args)
			if tc.err != "" {
				requireError(t, result, tc.err)
			} else {
				requireSuccess(t, result)
				if result.body != "\n"+tc.body {
					t.Fatalf("body length = %d, want %d; body prefix = %.100q", len(result.body), len(tc.body)+1, result.body)
				}
				if result.attrs["next_offset"] != tc.next {
					t.Fatalf("next_offset = %q, want %q", result.attrs["next_offset"], tc.next)
				}
				assertAttribute(t, result, "path", path)
			}
			requireEmptyRegistry(t, r.locks)
		})
	}
}

func TestReadPaginationReassemblesFile(t *testing.T) {
	r, _, _, _ := NewFileFunctions()
	var source strings.Builder
	for i := range 2100 {
		fmt.Fprintf(&source, "%04d 世界🙂 %s\n", i, strings.Repeat("x", i%70))
	}
	path := filepath.Join(t.TempDir(), "input")
	writeFixture(t, path, []byte(source.String()))
	var got strings.Builder
	offset := 1
	for page := 0; ; page++ {
		if page > 2100 {
			t.Fatal("pagination failed to terminate")
		}
		result := invoke(t, r, context.Background(), map[string]any{"path": path, "offset": number(offset), "limit": number(87)})
		requireSuccess(t, result)
		got.WriteString(strings.TrimPrefix(result.body, "\n"))
		next := result.attrs["next_offset"]
		if next == "" {
			break
		}
		var n int
		if _, err := fmt.Sscan(next, &n); err != nil || n <= offset {
			t.Fatalf("invalid next offset %q", next)
		}
		offset = n
	}
	if got.String() != source.String() {
		t.Fatal("pagination lost or duplicated content")
	}
}

func TestReadCancellationCheckpoints(t *testing.T) {
	for _, content := range []string{"one\ntwo\n", strings.Repeat("x", 4<<15) + "\nlast\n"} {
		for checkpoint := 1; checkpoint <= 10; checkpoint++ {
			t.Run(fmt.Sprintf("%d/%d", len(content), checkpoint), func(t *testing.T) {
				r, _, _, _ := NewFileFunctions()
				path := filepath.Join(t.TempDir(), "input")
				writeFixture(t, path, []byte(content))
				ctx, cancel := cancellationAt(checkpoint)
				defer cancel()
				result := invoke(t, r, ctx, map[string]any{"path": path, "offset": number(2)})
				if ctx.calls.Load() >= int32(checkpoint) {
					requireError(t, result, context.Canceled.Error())
				} else {
					requireSuccess(t, result)
				}
				requireEmptyRegistry(t, r.locks)
			})
		}
	}
}
