package function

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"
	"unicode/utf8"

	"ceraso.dev/bobo/provider"
)

type Read struct {
	locks *fileLocks
}

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

func (r *Read) Call(ctx context.Context, args map[string]any) []provider.Part {
	out := fmt.Sprintf("<read date=%q", time.Now().Format(time.RFC3339))
	if err := ctx.Err(); err != nil {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", err.Error()))}}
	}
	path, err := nonemptyStringArgument(args, "path")
	if err != nil {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", err.Error()))}}
	}
	out += fmt.Sprintf(" path=%q", path)
	showLineNumbers, err := boolArgument(args, "show_line_numbers")
	if err != nil {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", err.Error()))}}
	}
	out += fmt.Sprintf(" show_line_numbers=\"%t\"", showLineNumbers)
	offset, err := positiveIntArgument(args, "offset", 1)
	if err != nil {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", err.Error()))}}
	}
	out += fmt.Sprintf(" offset=\"%d\"", offset)
	limit, err := positiveIntArgument(args, "limit", 1000)
	if err != nil {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", err.Error()))}}
	}
	out += fmt.Sprintf(" limit=\"%d\"", limit)

	path, unlock, err := r.locks.acquire(ctx, path)
	if err != nil {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", err.Error()))}}
	}
	defer unlock()

	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", err.Error()))}}
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", err.Error()))}}
	}
	if !info.Mode().IsRegular() {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", path+" is not a regular file"))}}
	}

	body := make([]byte, 0, 1<<15)
	reader := bufio.NewReaderSize(file, 1<<15)
	nextOffset := 0
	for i := 1; ; i++ {
		if err := ctx.Err(); err != nil {
			return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", err.Error()))}}
		}
		if len(body) == 1<<15 || (i >= offset && i-offset >= limit) {
			if _, err := reader.Peek(1); err == nil {
				nextOffset = i
			} else if !errors.Is(err, io.EOF) {
				return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", fmt.Sprintf("read %s: %v", path, err)))}}
			}
			break
		}
		line, err := reader.ReadSlice('\n')
		for i < offset && errors.Is(err, bufio.ErrBufferFull) {
			if err := ctx.Err(); err != nil {
				return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", err.Error()))}}
			}
			line, err = reader.ReadSlice('\n')
		}
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
