package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"ceraso.dev/bobo/provider"
)

type Edit struct{}

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

func (*Edit) Call(ctx context.Context, in string) []provider.Part {
	out := "<edit"
	if err := ctx.Err(); err != nil {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", err.Error()))}}
	}
	if !utf8.ValidString(in) {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + " error=\"invalid arguments: invalid utf-8\"/>")}}
	}
	dec := json.NewDecoder(strings.NewReader(in))
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
	v, ok = args["old_text"]
	if !ok {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + " error=\"invalid arguments: missing old_text\"/>")}}
	}
	oldText, ok := v.(string)
	if !ok {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + " error=\"invalid arguments: old_text is not a string\"/>")}}
	}
	if oldText == "" {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + " error=\"invalid arguments: old_text is empty\"/>")}}
	}
	if len(oldText) > 1<<15 {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + " error=\"invalid arguments: old_text exceeds 32 KiB\"/>")}}
	}
	v, ok = args["new_text"]
	if !ok {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + " error=\"invalid arguments: missing new_text\"/>")}}
	}
	newText, ok := v.(string)
	if !ok {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + " error=\"invalid arguments: new_text is not a string\"/>")}}
	}
	if len(newText) > 1<<15 {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + " error=\"invalid arguments: new_text exceeds 32 KiB\"/>")}}
	}

	info, err := os.Stat(path)
	if err != nil {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", fmt.Sprintf("stat %s: %v", path, err)))}}
	}
	if !info.Mode().IsRegular() {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", path+" is not a regular file"))}}
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
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
	old := []byte(oldText)
	buf := make([]byte, (1<<15)+max(len(old)-1, utf8.UTFMax-1))
	keep, valid := 0, 0
	size, match := int64(0), int64(-1)
	for {
		if err := ctx.Err(); err != nil {
			return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", err.Error()))}}
		}
		n, err := io.ReadFull(file, buf[keep:keep+(1<<15)])
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", fmt.Sprintf("read %s: %v", path, err)))}}
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
	if err := ctx.Err(); err != nil {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", err.Error()))}}
	}
	if match == -1 {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + " error=\"old_text must occur exactly once in the file\"/>")}}
	}
	if oldText != newText {
		delta := int64(len(newText) - len(oldText))
		tail := match + int64(len(oldText))
		for moved := int64(0); delta != 0 && moved < size-tail; {
			n := min(int64(1<<15), size-tail-moved)
			pos := tail + moved
			if delta > 0 {
				pos = size - moved - n
			}
			if _, err := file.ReadAt(buf[:n], pos); err != nil {
				return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", fmt.Sprintf("read %s: %v", path, err)))}}
			}
			if _, err := file.WriteAt(buf[:n], pos+delta); err != nil {
				return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", fmt.Sprintf("write %s: %v", path, err)))}}
			}
			moved += n
		}
		if _, err := file.WriteAt([]byte(newText), match); err != nil {
			return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", fmt.Sprintf("write %s: %v", path, err)))}}
		}
		if delta != 0 {
			if err := file.Truncate(size + delta); err != nil {
				return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", fmt.Sprintf("truncate %s: %v", path, err)))}}
			}
		}
	}
	if err := file.Close(); err != nil {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", fmt.Sprintf("close %s: %v", path, err)))}}
	}
	return []provider.Part{{ContentType: "text/plain", Content: []byte(out + " error=\"\"/>")}}
}
