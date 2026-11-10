package function

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"

	"ceraso.dev/bobo/internal/provider"
)

// Read returns pages of UTF-8 text, capped at 32 KiB. Results include next_offset
// when more lines remain. Each returned line must fit within the cap.
// Create Read with NewFileFunctions.
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
	out := newToolResult("read")
	body, err := r.read(ctx, args, &out)
	if err != nil {
		return out.status(err)
	}
	return out.text(body, nil)
}

func (r *Read) read(ctx context.Context, args map[string]any, out *toolResult) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	path, err := nonemptyStringArgument(args, "path")
	if err != nil {
		return "", err
	}
	out.attribute("path", path)
	showLineNumbers, err := boolArgument(args, "show_line_numbers")
	if err != nil {
		return "", err
	}
	out.attribute("show_line_numbers", showLineNumbers)
	offset, err := positiveIntArgument(args, "offset", 1)
	if err != nil {
		return "", err
	}
	out.attribute("offset", offset)
	limit, err := positiveIntArgument(args, "limit", 1000)
	if err != nil {
		return "", err
	}
	out.attribute("limit", limit)

	path, unlock, err := r.locks.acquire(ctx, path)
	if err != nil {
		return "", err
	}
	defer unlock()

	file, _, err := openRegular(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	body := make([]byte, 0, 1<<15)
	reader := bufio.NewReaderSize(file, 1<<15)
	nextOffset := 0
	for i := 1; ; i++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if len(body) == 1<<15 || (i >= offset && i-offset >= limit) {
			if _, err := reader.Peek(1); err == nil {
				nextOffset = i
			} else if !errors.Is(err, io.EOF) {
				return "", fmt.Errorf("read %s: %v", path, err)
			}
			break
		}
		line, err := reader.ReadSlice('\n')
		for i < offset && errors.Is(err, bufio.ErrBufferFull) {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			line, err = reader.ReadSlice('\n')
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			return "", fmt.Errorf("%s:%d exceeds the 32 KiB line limit", path, i)
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return "", fmt.Errorf("read %s: %v", path, err)
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
			return "", fmt.Errorf("%s:%d exceeds the 32 KiB line limit", path, i)
		}
		if len(body)+n > 1<<15 {
			nextOffset = i
			break
		}
		if !utf8.Valid(line) {
			return "", fmt.Errorf("%s:%d has invalid utf-8", path, i)
		}
		body = append(body, prefix...)
		body = append(body, line...)
		if body[len(body)-1] != '\n' {
			body = append(body, '\n')
		}
	}
	if nextOffset != 0 {
		out.attribute("next_offset", nextOffset)
	}
	return string(body), nil
}
