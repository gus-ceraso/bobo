package function

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"ceraso.dev/bobo/internal/provider"
)

// Edit replaces exactly one literal occurrence in a UTF-8 text file, preserving
// file permissions. It streams the scan and replacement regardless of file size.
// old_text must be nonempty; old_text and new_text are each limited to 32 KiB.
// Create Edit with NewFileFunctions.
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
	out := newToolResult("edit")
	err := e.edit(ctx, args, &out)
	return out.status(err)
}

func (e *Edit) edit(ctx context.Context, args map[string]any, out *toolResult) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := nonemptyStringArgument(args, "path")
	if err != nil {
		return err
	}
	out.attribute("path", path)
	oldText, err := nonemptyStringArgument(args, "old_text")
	if err != nil {
		return err
	}
	preview := previewText(oldText)
	out.attribute("old_text", preview)
	if len(oldText) > 1<<15 {
		return errors.New("invalid arguments: old_text exceeds 32 KiB")
	}
	newText, err := stringArgument(args, "new_text")
	if err != nil {
		return err
	}
	preview = previewText(newText)
	out.attribute("new_text", preview)
	if len(newText) > 1<<15 {
		return errors.New("invalid arguments: new_text exceeds 32 KiB")
	}

	path, unlock, err := e.locks.acquire(ctx, path)
	if err != nil {
		return err
	}
	defer unlock()

	file, info, err := openRegular(path)
	if err != nil {
		return err
	}
	defer file.Close()

	old := []byte(oldText)
	var size int64
	match := int64(-1)
	buf := make([]byte, (1<<15)+max(len(old)-1, utf8.UTFMax-1))
	keep, valid := 0, 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := io.ReadFull(file, buf[keep:keep+(1<<15)])
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return err
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
			return errors.New("file content must be valid utf-8")
		}
		for start := max(0, keep-len(old)+1); start < total; {
			i := bytes.Index(buf[start:total], old)
			if i == -1 {
				break
			}
			if match != -1 {
				return errors.New("could not find old_text exactly once in the file. old_text must match exactly including all whitespace and newlines.")
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
		return err
	}
	if match == -1 {
		return errors.New("could not find old_text exactly once in the file. old_text must match exactly including all whitespace and newlines.")
	}
	if oldText == newText {
		return nil
	}

	tail := match + int64(len(old))
	replacement := io.MultiReader(
		io.NewSectionReader(file, 0, match),
		strings.NewReader(newText),
		io.NewSectionReader(file, tail, size-tail),
	)
	return replaceFile(ctx, path, info, replacement)
}
