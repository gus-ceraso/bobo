package tool

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"unicode/utf8"

	"ceraso.dev/bobo/provider"
)

type Read struct{}

func (*Read) Schema() map[string]any {
	return map[string]any{
		"type":        "function",
		"name":        "read",
		"description": "Read a text file. Returns at most limit lines or 32KiB (whichever is hit first).",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path to the file.",
				},
				"show_line_numbers": map[string]any{
					"type":        []string{"boolean", "null"},
					"description": "If true, prefix every line by its number.",
				},
				"offset": map[string]any{
					"type":        []string{"integer", "null"},
					"minimum":     1,
					"description": "1-based line number to start from. Defaults to 1.",
				},
				"limit": map[string]any{
					"type":        []string{"integer", "null"},
					"minimum":     1,
					"description": "Maximum number of lines to return. Defaults to 1000.",
				},
			},
			"required":             []string{"path", "show_line_numbers", "offset", "limit"},
			"additionalProperties": false,
		},
		"strict": true,
	}
}

func (*Read) Call(in string) []provider.Part {
	out := "<read"
	if !utf8.ValidString(in) {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + " error=\"invalid arguments: invalid utf-8\"/>")}}
	}
	dec := json.NewDecoder(strings.NewReader(in))
	dec.UseNumber()
	var args map[string]any
	if err := dec.Decode(&args); err != nil {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", "invalid arguments: "+err.Error()))}}
	}
	v, ok := args["path"]
	if !ok {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + " error=\"invalid arguments: missing path\"/>")}}
	}
	path, ok := v.(string)
	if !ok {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + " error=\"invalid arguments: path is not a string\"/>")}}
	}
	if path == "" {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + " error=\"invalid arguments: path is empty\"/>")}}
	}
	out += fmt.Sprintf(" path=%q", path)
	showLineNumbers := false
	v, ok = args["show_line_numbers"]
	if ok && v != nil {
		showLineNumbers, ok = v.(bool)
		if !ok {
			return []provider.Part{{ContentType: "text/plain", Content: []byte(out + " error=\"invalid arguments: show_line_numbers is not a bool\"/>")}}
		}
	}
	out += fmt.Sprintf(" show_line_numbers=\"%t\"", showLineNumbers)
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
	if !info.Mode().IsRegular() {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", path+" is not a regular file"))}}
	}
	file, err := os.Open(path)
	if err != nil {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", fmt.Sprintf("open %s: %v", path, err)))}}
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", fmt.Sprintf("stat %s: %v", path, err)))}}
	}
	if !info.Mode().IsRegular() {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", path+" is not a regular file"))}}
	}

	body := make([]byte, 0, 1<<15)
	reader := bufio.NewReaderSize(file, 1<<15)
	nextOffset := 0
	for i := 1; ; i++ {
		if len(body) == 1<<15 || (i >= offset && i-offset >= limit) {
			if _, err := reader.Peek(1); err == nil {
				nextOffset = i
			} else if !errors.Is(err, io.EOF) {
				return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", fmt.Sprintf("read %s: %v", path, err)))}}
			}
			break
		}
		line, err := reader.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", fmt.Sprintf("%s:%d exceeds the 32 KiB line limit", path, i)))}}
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", fmt.Sprintf("read %s: %v", path, err)))}}
		}
		if len(line) == 0 {
			break
		}
		if i < offset {
			continue
		}
		prefix := ""
		if showLineNumbers {
			prefix = fmt.Sprintf("%d:", i)
		}
		n := len(prefix) + len(line)
		if line[len(line)-1] != '\n' {
			n++
		}
		if n > 1<<15 {
			return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", fmt.Sprintf("%s:%d exceeds the 32 KiB line limit", path, i)))}}
		}
		if len(body)+n > 1<<15 {
			nextOffset = i
			break
		}
		if !utf8.Valid(line) {
			return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", fmt.Sprintf("%s:%d has invalid utf-8", path, i)))}}
		}
		body = append(body, prefix...)
		body = append(body, line...)
		if body[len(body)-1] != '\n' {
			body = append(body, '\n')
		}
	}
	if nextOffset != 0 {
		out += fmt.Sprintf(" next_offset=\"%d\"", nextOffset)
	}
	return []provider.Part{{ContentType: "text/plain", Content: []byte(out + " error=\"\">\n" + string(body) + "</read>")}}
}
