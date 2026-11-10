//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/reeflective/readline"
)

// pathLexer shares quote/escape rules between token boundaries and decoding.
// Prompts are prose: apostrophes after word characters do not open quotes.
type pathLexer struct {
	quote   rune
	escaped bool
}

// step reports whether this rune is literal content or a token separator.
func (l *pathLexer) step(line []rune, i int) (literal, separator bool) {
	r := line[i]
	switch {
	case l.escaped:
		l.escaped = false
		return true, false
	case r == '\\' && l.quote != '\'':
		l.escaped = true
	case r == l.quote:
		l.quote = 0
	case l.quote != 0:
		return true, false
	case r == '"' || r == '\'' && opensPathQuote(line, i):
		l.quote = r
	default:
		return true, unicode.IsSpace(r)
	}
	return false, false
}

func opensPathQuote(line []rune, i int) bool {
	if i == 0 {
		return true
	}
	previous := line[i-1]
	return !(unicode.IsLetter(previous) || unicode.IsNumber(previous) || unicode.IsMark(previous) || previous == '_')
}

// pathToken returns the complete token's bounds and its decoded prefix at cursor.
// The caller has already checked that cursor lies within line.
func pathToken(line []rune, cursor int) (start, end int, word string) {
	var lexer pathLexer
	for i := 0; i < cursor; i++ {
		if _, separator := lexer.step(line, i); separator {
			start = i + 1
		}
	}
	end = cursor
	tail := lexer
	for end < len(line) {
		if _, separator := tail.step(line, end); separator {
			break
		}
		end++
	}
	var decoded strings.Builder
	lexer = pathLexer{}
	for i := start; i < cursor; i++ {
		if literal, _ := lexer.step(line, i); literal {
			decoded.WriteRune(line[i])
		}
	}
	if lexer.escaped {
		decoded.WriteByte('\\')
	}
	return start, end, decoded.String()
}

// pathMatches returns replacements for the complete token containing the cursor.
// It understands quotes/backslash escapes, but never performs shell expansion.
func pathMatches(line []rune, cursor int) (start, end int, matches []string) {
	if cursor < 0 || cursor > len(line) {
		return 0, 0, nil
	}
	start, end, word := pathToken(line, cursor)
	dir, prefix := filepath.Split(word)
	lookup := dir
	if lookup == "" {
		lookup = "."
	} else if strings.HasPrefix(lookup, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return start, end, nil
		}
		lookup = filepath.Join(home, lookup[2:])
	}
	entries, err := os.ReadDir(lookup)
	if err != nil {
		return start, end, nil
	}
	for _, entry := range entries {
		name := entry.Name()
		if !utf8.ValidString(name) || strings.ContainsFunc(name, unicode.IsControl) || !strings.HasPrefix(name, prefix) || (strings.HasPrefix(name, ".") && !strings.HasPrefix(prefix, ".")) {
			continue
		}
		path := dir + name
		isDir := entry.IsDir()
		if entry.Type()&os.ModeSymlink != 0 {
			info, err := os.Stat(filepath.Join(lookup, name))
			isDir = err == nil && info.IsDir()
		}
		if isDir {
			path += "/"
		}
		style := rune(0)
		if start < cursor && (line[start] == '\'' || line[start] == '"') {
			style = line[start]
		}
		matches = append(matches, quotePath(path, style, isDir))
	}
	return start, end, matches
}

func quotePath(path string, quote rune, directory bool) string {
	var out strings.Builder
	if quote != 0 {
		out.WriteRune(quote)
	}
	for _, r := range path {
		switch {
		case quote == '\'' && r == '\'':
			out.WriteString("'\\''")
		case quote == '"' && strings.ContainsRune("\\\"$`", r):
			out.WriteRune('\\')
			out.WriteRune(r)
		case quote == 0 && (unicode.IsSpace(r) || strings.ContainsRune("\\\"'`$;&|<>()[]{}!*?", r)):
			out.WriteRune('\\')
			out.WriteRune(r)
		default:
			out.WriteRune(r)
		}
	}
	if quote != 0 && !directory {
		out.WriteRune(quote)
	}
	return out.String()
}

func completePrompt(rl *readline.Shell) {
	start, end, matches := pathMatches(*rl.Line(), rl.Cursor().Pos())
	if len(matches) == 0 {
		return
	}
	common := []rune(matches[0])
	for _, match := range matches[1:] {
		runes := []rune(match)
		i := 0
		for i < len(common) && i < len(runes) && common[i] == runes[i] {
			i++
		}
		common = common[:i]
	}
	// Do not insert half of a backslash escape shared by two candidates.
	if len(matches) > 1 {
		n := 0
		for i := len(common) - 1; i >= 0 && common[i] == '\\'; i-- {
			n++
		}
		if n%2 != 0 {
			common = common[:len(common)-1]
		}
	}
	if len(matches) == 1 || len(common) > rl.Cursor().Pos()-start {
		rl.History.Save()
		rl.Line().Cut(start, end)
		rl.Cursor().Set(start)
		rl.Cursor().InsertAt(common...)
		return
	}
	// Print from the input goroutine, never concurrently with readline redraws.
	rl.Printf("%s", strings.Join(matches, "\n"))
}
