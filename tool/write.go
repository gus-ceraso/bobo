package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"ceraso.dev/bobo/provider"
)

type Write struct{}

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

func (*Write) Call(ctx context.Context, in string) []provider.Part {
	out := "<write"
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
	v, ok = args["text"]
	if !ok {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + " error=\"invalid arguments: missing text\"/>")}}
	}
	text, ok := v.(string)
	if !ok {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + " error=\"invalid arguments: text is not a string\"/>")}}
	}

	if err := ctx.Err(); err != nil {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", err.Error()))}}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", fmt.Sprintf("create parent directories for %s: %v", path, err)))}}
	}
	info, err := os.Stat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", fmt.Sprintf("stat %s: %v", path, err)))}}
	}
	if err == nil && !info.Mode().IsRegular() {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", path+" is not a regular file"))}}
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE, 0o644)
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
	if err := ctx.Err(); err != nil {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", err.Error()))}}
	}
	if _, err := file.WriteString(text); err != nil {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", fmt.Sprintf("write %s: %v", path, err)))}}
	}
	if err := file.Truncate(int64(len(text))); err != nil {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", fmt.Sprintf("truncate %s: %v", path, err)))}}
	}
	if err := file.Close(); err != nil {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" error=%q/>", fmt.Sprintf("close %s: %v", path, err)))}}
	}
	return []provider.Part{{ContentType: "text/plain", Content: []byte(out + " error=\"\"/>")}}
}
