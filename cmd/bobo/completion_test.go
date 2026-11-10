//go:build linux

package main

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

func TestPathToken(t *testing.T) {
	for _, tt := range []struct{ prefix, typed, suffix, rest, word string }{
		{"read ", "alph", "BAD", " later", "alph"},
		{"What's in ", "alph", "BAD", " later", "alph"},
		{"Cafe\u0301's files ", "世界", ".txt", " later", "世界"},
		{"read ", `"with sp`, `ace.txt"`, " later", "with sp"},
		{"read ", `'with sp`, `ace.txt'`, " later", "with sp"},
		{"read ", `with\ sp`, "ace.txt", " later", "with sp"},
		{"read ", `./dir/'with space'/alph`, "BAD", " later", "./dir/with space/alph"},
		{"read ", `'quote'\''fi`, `le'`, " later", "quote'fi"},
		{"read ", `trailing\`, "", "", `trailing\`},
		{"read ", `"quoted\`, "", "", `quoted\`},
		{"read ", `'single\`, "", "", `single\`},
		{"read ", `half\`, " space", " later", `half\`},
		{"read\n", "", "", " later", ""},
		{"", "", "", "", ""},
	} {
		line := []rune(tt.prefix + tt.typed + tt.suffix + tt.rest)
		cursor := len([]rune(tt.prefix + tt.typed))
		start, end, word := pathToken(line, cursor)
		if start != len([]rune(tt.prefix)) || end != cursor+len([]rune(tt.suffix)) || word != tt.word {
			t.Fatalf("pathToken(%q, %d) = (%d,%d,%q)", string(line), cursor, start, end, word)
		}
	}
}

func FuzzQuotePathRoundTrip(f *testing.F) {
	for _, name := range []string{"alpha.txt", "with space.txt", "quote'file", "世界🙂", "Cafe\u0301's note", `back\slash`, `cash$and` + "`" + `quote"`, ""} {
		f.Add(name)
	}
	f.Fuzz(func(t *testing.T, name string) {
		if len(name) > 4096 || !utf8.ValidString(name) || strings.ContainsFunc(name, unicode.IsControl) {
			t.Skip()
		}
		// Match the filename constraints enforced by pathMatches.
		for _, quote := range []rune{0, '\'', '"'} {
			encoded := quotePath(name, quote, false)
			line := []rune("read " + encoded + " later")
			cursor := len([]rune("read " + encoded))
			start, end, word := pathToken(line, cursor)
			if start != 5 || end != cursor || word != name {
				t.Fatalf("round trip %q quote %q -> %q -> (%d,%d,%q)", name, quote, encoded, start, end, word)
			}
			// Truncating a quoted directory must keep its contents in the token.
			directory := quotePath(strings.TrimSuffix(name, "/")+"/", quote, true)
			_, _, word = pathToken([]rune("read "+directory), len([]rune("read "+directory)))
			if word != strings.TrimSuffix(name, "/")+"/" {
				t.Fatalf("directory round trip: %q -> %q", directory, word)
			}
		}
	})
}
