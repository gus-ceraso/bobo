package function

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"ceraso.dev/bobo/provider"
)

type Edit struct {
	locks *fileLocks
}

func (*Edit) Schema() map[string]any {
	return map[string]any{
		"type":        "function",
		"name":        "edit",
		"description": "Replace exactly one occurrence of old_text with new_text in an existing text file. Matching is literal and case-sensitive, including whitespace and line breaks. The edit fails if old_text isn't found or occurs more than once. Include surrounding lines to make it unique.",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path to the file.",
				},
				"old_text": map[string]any{
					"type":        "string",
					"description": "Nonempty exact text to replace. Must occur exactly once in the file.",
				},
				"new_text": map[string]any{
					"type":        "string",
					"description": "Replacement text. Use an empty string to delete old_text.",
				},
			},
			"required":             []string{"path", "old_text", "new_text"},
			"additionalProperties": false,
		},
		"strict": true,
	}
}

func (e *Edit) Call(ctx context.Context, args map[string]any) []provider.Part {
	out := fmt.Sprintf("<edit date=%q", time.Now().Format(time.RFC3339))
	if err := ctx.Err(); err != nil {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", err.Error()))}}
	}
	path, err := nonemptyStringArgument(args, "path")
	if err != nil {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", err.Error()))}}
	}
	out += fmt.Sprintf(" path=%q", path)
	oldText, err := nonemptyStringArgument(args, "old_text")
	if err != nil {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", err.Error()))}}
	}
	preview := fmt.Sprintf("%.80s", oldText)
	if len(preview) < len(oldText) {
		preview += "..."
	}
	out += fmt.Sprintf(" old_text=%q", preview)
	if len(oldText) > 1<<15 {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + " error=\"invalid arguments: old_text exceeds 32 KiB\"/>")}}
	}
	newText, err := stringArgument(args, "new_text")
	if err != nil {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", err.Error()))}}
	}
	preview = fmt.Sprintf("%.80s", newText)
	if len(preview) < len(newText) {
		preview += "..."
	}
	out += fmt.Sprintf(" new_text=%q", preview)
	if len(newText) > 1<<15 {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + " error=\"invalid arguments: new_text exceeds 32 KiB\"/>")}}
	}

	path, unlock, err := e.locks.acquire(ctx, path)
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

	stream := info.Size() > 32<<20
	var content []byte
	if !stream {
		content, err = io.ReadAll(io.LimitReader(file, (32<<20)+1))
		if err != nil {
			return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", err.Error()))}}
		}
		if len(content) > 32<<20 {
			stream = true
			content = nil
			if _, err := file.Seek(0, io.SeekStart); err != nil {
				return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", err.Error()))}}
			}
		}
	}
	old := []byte(oldText)
	size, match := int64(len(content)), int64(-1)
	if !stream {
		if !utf8.Valid(content) {
			return []provider.Part{{ContentType: "text/plain", Content: []byte(out + " error=\"file content must be valid utf-8\"/>")}}
		}
		i := bytes.Index(content, old)
		if i >= 0 {
			if bytes.Contains(content[i+1:], old) {
				return []provider.Part{{ContentType: "text/plain", Content: []byte(out + " error=\"old_text must occur exactly once in the file\"/>")}}
			}
			match = int64(i)
		}
	} else {
		buf := make([]byte, (1<<15)+max(len(old)-1, utf8.UTFMax-1))
		keep, valid := 0, 0
		for {
			if err := ctx.Err(); err != nil {
				return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", err.Error()))}}
			}
			n, err := io.ReadFull(file, buf[keep:keep+(1<<15)])
			if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
				return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", err.Error()))}}
			}
			size += int64(n)
			total := keep + n
			end := total
			if err == nil {
				for i := max(valid, total-utf8.UTFMax+1); i < total; i++ {
					if !utf8.FullRune(buf[i:total]) {
						end = i
						break
					}
				}
			}
			if !utf8.Valid(buf[valid:end]) {
				return []provider.Part{{ContentType: "text/plain", Content: []byte(out + " error=\"file content must be valid utf-8\"/>")}}
			}
			for start := max(0, keep-len(old)+1); start < total; {
				i := bytes.Index(buf[start:total], old)
				if i == -1 {
					break
				}
				if match != -1 {
					return []provider.Part{{ContentType: "text/plain", Content: []byte(out + " error=\"old_text must occur exactly once in the file\"/>")}}
				}
				match = size - int64(total) + int64(start+i)
				start += i + 1
			}
			if err != nil {
				break
			}
			keep = min(total, max(len(old)-1, utf8.UTFMax-1))
			valid = end - total + keep
			copy(buf, buf[total-keep:total])
		}
	}
	if err := ctx.Err(); err != nil {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", err.Error()))}}
	}
	if match == -1 {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + " error=\"old_text must occur exactly once in the file\"/>")}}
	}
	if oldText == newText {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + " error=\"\"/>")}}
	}

	temp, err := os.CreateTemp(filepath.Dir(path), ".bobo-*")
	if err != nil {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", err.Error()))}}
	}
	defer os.Remove(temp.Name())
	defer temp.Close()
	var replacement io.Reader
	if stream {
		tail := match + int64(len(old))
		replacement = io.MultiReader(
			io.NewSectionReader(file, 0, match),
			strings.NewReader(newText),
			io.NewSectionReader(file, tail, size-tail),
		)
	} else {
		replacement = bytes.NewReader(bytes.Replace(content, old, []byte(newText), 1))
	}
	buf := make([]byte, 1<<15)
	for {
		if err := ctx.Err(); err != nil {
			return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", err.Error()))}}
		}
		n, readErr := replacement.Read(buf)
		if n > 0 {
			if _, err := temp.Write(buf[:n]); err != nil {
				return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", err.Error()))}}
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", readErr.Error()))}}
		}
	}
	if err := temp.Chmod(info.Mode()); err != nil {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", err.Error()))}}
	}
	if err := temp.Close(); err != nil {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", err.Error()))}}
	}
	if err := ctx.Err(); err != nil {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", err.Error()))}}
	}
	if err := os.Rename(temp.Name(), path); err != nil {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", err.Error()))}}
	}
	return []provider.Part{{ContentType: "text/plain", Content: []byte(out + " error=\"\"/>")}}
}
