package function

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"ceraso.dev/bobo/internal/provider"
)

// Write creates or replaces a UTF-8 text file, creating missing parent directories.
// It preserves an existing file's permissions. Create Write with NewFileFunctions.
type Write struct {
	locks *fileLocks
}

func (*Write) Schema() map[string]any {
	return map[string]any{
		"type":        "function",
		"name":        "write",
		"description": "Create a file or replace an existing file's contents with text. Missing parent directories are created. Prefer edit for small changes to existing files.",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path to the file.",
				},
				"text": map[string]any{
					"type":        "string",
					"description": "Complete new contents of the file.",
				},
			},
			"required":             []string{"path", "text"},
			"additionalProperties": false,
		},
		"strict": true,
	}
}

func (w *Write) Call(ctx context.Context, args map[string]any) []provider.Part {
	out := newToolResult("write")
	err := w.write(ctx, args, &out)
	return out.status(err)
}

func (w *Write) write(ctx context.Context, args map[string]any, out *toolResult) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := nonemptyStringArgument(args, "path")
	if err != nil {
		return err
	}
	out.attribute("path", path)
	text, err := stringArgument(args, "text")
	if err != nil {
		return err
	}
	preview := previewText(text)
	out.attribute("text", preview)
	if err := ctx.Err(); err != nil {
		return err
	}
	path, unlock, err := w.locks.acquire(ctx, path)
	if err != nil {
		return err
	}
	defer unlock()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create parent directories for %s: %v", path, err)
	}
	info, err := os.Lstat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil && !info.Mode().IsRegular() {
		return errors.New(path + " is not a regular file")
	}
	return replaceFile(ctx, path, info, strings.NewReader(text))
}
