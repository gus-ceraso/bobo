package function

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"ceraso.dev/bobo/provider"
)

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
	out := fmt.Sprintf("<write date=%q", time.Now().Format(time.RFC3339))
	if err := ctx.Err(); err != nil {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", err.Error()))}}
	}
	path, err := nonemptyStringArgument(args, "path")
	if err != nil {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", err.Error()))}}
	}
	out += fmt.Sprintf(" path=%q", path)
	text, err := stringArgument(args, "text")
	if err != nil {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", err.Error()))}}
	}
	preview := fmt.Sprintf("%.80s", text)
	if len(preview) < len(text) {
		preview += "..."
	}
	out += fmt.Sprintf(" text=%q", preview)
	if err := ctx.Err(); err != nil {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", err.Error()))}}
	}
	path, unlock, err := w.locks.acquire(ctx, path)
	if err != nil {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", err.Error()))}}
	}
	defer unlock()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", fmt.Sprintf("create parent directories for %s: %v", path, err)))}}
	}
	info, err := os.Lstat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", err.Error()))}}
	}
	if err == nil && !info.Mode().IsRegular() {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", path+" is not a regular file"))}}
	}
	mode := os.FileMode(0o644)
	if info != nil {
		mode = 0o600
	}
	tempPath := filepath.Join(filepath.Dir(path), ".bobo-"+rand.Text())
	temp, err := os.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", err.Error()))}}
	}
	defer os.Remove(tempPath)
	defer temp.Close()
	for offset := 0; offset < len(text); {
		if err := ctx.Err(); err != nil {
			return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", err.Error()))}}
		}
		end := offset + min(1<<15, len(text)-offset)
		if _, err := temp.WriteString(text[offset:end]); err != nil {
			return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", err.Error()))}}
		}
		offset = end
	}
	if info != nil {
		if err := temp.Chmod(info.Mode()); err != nil {
			return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", err.Error()))}}
		}
	}
	if err := temp.Close(); err != nil {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", err.Error()))}}
	}
	if err := ctx.Err(); err != nil {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", err.Error()))}}
	}
	if err := os.Rename(tempPath, path); err != nil {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", err.Error()))}}
	}
	return []provider.Part{{ContentType: "text/plain", Content: []byte(out + " error=\"\"/>")}}
}
