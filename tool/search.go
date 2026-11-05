package tool

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"ceraso.dev/bobo/provider"
)

type Search struct{}

func (*Search) Schema() map[string]any {
	return map[string]any{
		"type":        "function",
		"name":        "search",
		"description": "Search files recursively. Returns at most limit results or 32KiB (whichever is hit first).",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        []string{"string", "null"},
					"description": "File or directory to search. Defaults to \".\".",
				},
				"path_pattern": map[string]any{
					"type":        []string{"string", "null"},
					"description": "RE2 path pattern: relative for directories, as supplied for files. Use (?i) to ignore case and ^…$ to anchor. Defaults to \".*\".",
				},
				"line_pattern": map[string]any{
					"type":        []string{"string", "null"},
					"description": "RE2 pattern to match against file lines. Use (?i) to ignore case and ^…$ to anchor. Null or empty gives only paths that match path_pattern.",
				},
				"hidden_paths": map[string]any{
					"type":        []string{"boolean", "null"},
					"description": "Include hidden descendants. Defaults to false.",
				},
				"offset": map[string]any{
					"type":        []string{"integer", "null"},
					"minimum":     1,
					"description": "1-based result to start from. Defaults to 1.",
				},
				"limit": map[string]any{
					"type":        []string{"integer", "null"},
					"minimum":     1,
					"description": "Maximum number of results to return. Defaults to 1000.",
				},
			},
			"required":             []string{"path", "path_pattern", "line_pattern", "hidden_paths", "offset", "limit"},
			"additionalProperties": false,
		},
		"strict": true,
	}
}

func (*Search) Call(ctx context.Context, in string) []provider.Part {
	out := "<search"
	if err := ctx.Err(); err != nil {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", err.Error()))}}
	}
	if !utf8.ValidString(in) {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + " error=\"invalid arguments: invalid utf-8\"/>")}}
	}
	dec := json.NewDecoder(strings.NewReader(in))
	dec.UseNumber()
	var args map[string]any
	if err := dec.Decode(&args); err != nil {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", "invalid arguments: "+err.Error()))}}
	}
	path := ""
	v, ok := args["path"]
	if ok && v != nil {
		path, ok = v.(string)
		if !ok {
			return []provider.Part{{ContentType: "text/plain", Content: []byte(out + " error=\"invalid arguments: path is not a string\"/>")}}
		}
	}
	if path == "" {
		path = "."
	}
	out += fmt.Sprintf(" path=%q", path)
	pathPattern := ""
	v, ok = args["path_pattern"]
	if ok && v != nil {
		pathPattern, ok = v.(string)
		if !ok {
			return []provider.Part{{ContentType: "text/plain", Content: []byte(out + " error=\"invalid arguments: path_pattern is not a string\"/>")}}
		}
	}
	if pathPattern == "" {
		pathPattern = ".*"
	}
	out += fmt.Sprintf(" path_pattern=%q", pathPattern)
	pre, err := regexp.Compile(pathPattern)
	if err != nil {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", "invalid arguments: invalid path_pattern: "+err.Error()))}}
	}
	linePattern := ""
	v, ok = args["line_pattern"]
	if ok && v != nil {
		linePattern, ok = v.(string)
		if !ok {
			return []provider.Part{{ContentType: "text/plain", Content: []byte(out + " error=\"invalid arguments: line_pattern is not a string\"/>")}}
		}
	}
	out += fmt.Sprintf(" line_pattern=%q", linePattern)
	re, err := regexp.Compile(linePattern)
	if err != nil {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", "invalid arguments: invalid line_pattern: "+err.Error()))}}
	}
	hiddenPaths := false
	v, ok = args["hidden_paths"]
	if ok && v != nil {
		hiddenPaths, ok = v.(bool)
		if !ok {
			return []provider.Part{{ContentType: "text/plain", Content: []byte(out + " error=\"invalid arguments: hidden_paths is not a bool\"/>")}}
		}
	}
	out += fmt.Sprintf(" hidden_paths=\"%t\"", hiddenPaths)
	offset := 1
	v, ok = args["offset"]
	if ok && v != nil {
		n, ok := v.(json.Number)
		if !ok {
			return []provider.Part{{ContentType: "text/plain", Content: []byte(out + " error=\"invalid arguments: offset is not an integer\"/>")}}
		}
		i, err := n.Int64()
		if err != nil {
			return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", "invalid arguments: offset is not a valid integer: "+err.Error()))}}
		}
		if i < 1 || i > math.MaxInt {
			return []provider.Part{{ContentType: "text/plain", Content: []byte(out + " error=\"invalid arguments: offset is out of range\"/>")}}
		}
		offset = int(i)
	}
	out += fmt.Sprintf(" offset=\"%d\"", offset)
	limit := 1000
	v, ok = args["limit"]
	if ok && v != nil {
		n, ok := v.(json.Number)
		if !ok {
			return []provider.Part{{ContentType: "text/plain", Content: []byte(out + " error=\"invalid arguments: limit is not an integer\"/>")}}
		}
		i, err := n.Int64()
		if err != nil {
			return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", "invalid arguments: limit is not a valid integer: "+err.Error()))}}
		}
		if i < 1 || i > math.MaxInt {
			return []provider.Part{{ContentType: "text/plain", Content: []byte(out + " error=\"invalid arguments: limit is out of range\"/>")}}
		}
		limit = int(i)
	}
	out += fmt.Sprintf(" limit=\"%d\"", limit)

	info, err := os.Stat(path)
	if err != nil {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", fmt.Sprintf("stat %s: %v", path, err)))}}
	}
	isDir := info.IsDir()
	if !isDir && !info.Mode().IsRegular() {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", path+" is not a regular file or directory"))}}
	}
	dir, root := path, "."
	if !isDir {
		dir, root = filepath.Split(path)
		if dir == "" {
			dir = "."
		}
	}
	fsys := os.DirFS(dir)

	body := make([]byte, 0, 1<<15)
	prefix := make([]byte, 0, 128)
	reader := bufio.NewReaderSize(nil, 1<<15)
	skip := offset - 1
	remaining := limit
	nextOffset := 0
	err = fs.WalkDir(fsys, root, func(p string, d fs.DirEntry, err error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err != nil {
			if !isDir {
				return err
			}
			return nil
		}
		if isDir {
			if p == "." {
				return nil
			}
			name := d.Name()
			if (!hiddenPaths && strings.HasPrefix(name, ".")) || !utf8.ValidString(name) || strings.ContainsAny(name, "\r\n") {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() || !pre.MatchString(p) {
				return nil
			}
		} else if !utf8.ValidString(path) || strings.ContainsAny(path, "\r\n") || !pre.MatchString(path) {
			return nil
		}
		if linePattern == "" {
			name := p
			if !isDir {
				name = path
			}
			n := len(name) + 1
			if n >= 1<<15 {
				return nil
			}
			if skip > 0 {
				skip--
				return nil
			}
			if remaining == 0 || len(body)+n >= 1<<15 {
				nextOffset = offset + (limit - remaining)
				return fs.SkipAll
			}
			body = append(body, name...)
			body = append(body, '\n')
			remaining--
			return nil
		}
		file, err := fsys.Open(p)
		if err != nil {
			if !isDir {
				return fmt.Errorf("open %s: %w", path, err)
			}
			return nil
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			if !isDir {
				return fmt.Errorf("stat %s: %w", path, err)
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			if !isDir {
				return fmt.Errorf("%s is not a regular file", path)
			}
			return nil
		}
		reader.Reset(file)
		i := int64(0)
		discard := false
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			line, err := reader.ReadSlice('\n')
			if errors.Is(err, bufio.ErrBufferFull) {
				discard = true
				continue
			}
			if err != nil && !errors.Is(err, io.EOF) {
				if !isDir {
					return fmt.Errorf("read %s: %w", path, err)
				}
				return nil
			}
			if len(line) == 0 {
				break
			}
			i++
			if discard {
				discard = false
				continue
			}
			if !utf8.Valid(line) || bytes.IndexByte(line, 0) >= 0 {
				continue
			}
			text := line
			if text[len(text)-1] == '\n' {
				text = text[:len(text)-1]
				if len(text) > 0 && text[len(text)-1] == '\r' {
					text = text[:len(text)-1]
				}
			}
			if !re.Match(text) {
				continue
			}
			prefix = prefix[:0]
			if isDir {
				prefix = append(prefix, p...)
				prefix = append(prefix, ':')
			}
			prefix = strconv.AppendInt(prefix, i, 10)
			prefix = append(prefix, ':')
			n := len(prefix) + len(line)
			if line[len(line)-1] != '\n' {
				n++
			}
			if n >= 1<<15 {
				continue
			}
			if skip > 0 {
				skip--
				continue
			}
			if remaining == 0 || len(body)+n >= 1<<15 {
				nextOffset = offset + (limit - remaining)
				return fs.SkipAll
			}
			body = append(body, prefix...)
			body = append(body, line...)
			if body[len(body)-1] != '\n' {
				body = append(body, '\n')
			}
			remaining--
		}
		return nil
	})
	if err != nil {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", err.Error()))}}
	}
	if nextOffset != 0 {
		out += fmt.Sprintf(" next_offset=\"%d\"", nextOffset)
	}
	return []provider.Part{{ContentType: "text/plain", Content: []byte(out + " error=\"\">\n" + string(body) + "</search>")}}
}
