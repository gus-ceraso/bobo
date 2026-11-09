package function

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

func FuzzReadPagination(f *testing.F) {
	for _, seed := range []struct {
		content string
		limit   uint8
		numbers bool
	}{
		{"", 1, false},
		{"one\ntwo\nlast", 1, true},
		{"\n\r\n世界🙂\x00\n", 2, false},
		{strings.Repeat("a", 20000) + "\n" + strings.Repeat("b", 20000) + "\nlast", 31, false},
		{strings.Repeat("世", 5000) + "\n" + strings.Repeat("🙂", 5000) + "\n", 31, true},
		{strings.Repeat("a", (1<<15)-3) + "\nlast", 1, true},
	} {
		f.Add(seed.content, seed.limit, seed.numbers)
	}
	f.Fuzz(func(t *testing.T, content string, rawLimit uint8, numbers bool) {
		if len(content) > 1<<16 || !utf8.ValidString(content) {
			t.Skip()
		}
		// Model complete logical lines independently of bufio, including the
		// added newline on an unterminated final line and line-number overhead.
		var lines []string
		if content != "" {
			lines = strings.SplitAfter(content, "\n")
			if lines[len(lines)-1] == "" {
				lines = lines[:len(lines)-1]
			}
		}
		if len(lines) > 256 {
			t.Skip() // Bound filesystem calls for arbitrary fuzz inputs.
		}
		for i, line := range lines {
			if !strings.HasSuffix(line, "\n") {
				line += "\n"
			}
			if numbers {
				line = strconv.Itoa(i+1) + ":" + line
			}
			if len(line) > 1<<15 {
				t.Skip() // Overlong-line rejection has separate example tests.
			}
			lines[i] = line
		}

		r, _, _, _ := NewFileFunctions()
		path := filepath.Join(t.TempDir(), "input")
		writeFixture(t, path, []byte(content))
		limit := int(rawLimit%32) + 1
		var assembled strings.Builder
		for first := 0; ; {
			var page strings.Builder
			next := first
			for next < len(lines) && next-first < limit && page.Len()+len(lines[next]) <= 1<<15 {
				page.WriteString(lines[next])
				next++
			}
			result := invoke(t, r, context.Background(), map[string]any{
				"path": path, "offset": number(first + 1), "limit": number(limit), "show_line_numbers": numbers,
			})
			requireSuccess(t, result)
			if result.body != "\n"+page.String() {
				t.Fatalf("page at line %d differs from model: got %d bytes, want %d", first+1, len(result.body)-1, page.Len())
			}
			assembled.WriteString(strings.TrimPrefix(result.body, "\n"))
			if next == len(lines) {
				if value, present := result.attrs["next_offset"]; present {
					t.Fatalf("unexpected next_offset at EOF: %q", value)
				}
				break
			}
			assertAttribute(t, result, "next_offset", fmt.Sprint(next+1))
			if next <= first {
				t.Fatal("pagination did not make progress")
			}
			first = next
		}
		if assembled.String() != strings.Join(lines, "") {
			t.Fatal("pagination lost or duplicated content")
		}
		requireEmptyRegistry(t, r.locks)
	})
}
