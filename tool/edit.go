package tool

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type Edit struct {
	HiddenFiles bool
	LocalFiles  bool
}

func (t *Edit) Schema() map[string]any {
	return map[string]any{
		"name":        "edit",
		"description": "Replace exactly one occurrence of old_text with new_text in an existing text file. Matching is literal and case-sensitive, including whitespace and line breaks. The edit fails if old_text isn't found or occurs more than once. Include surrounding lines to make it unique.",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": pathDescription("Path to the file.", t.LocalFiles, t.HiddenFiles),
				},
				"old_text": map[string]any{
					"type":        "string",
					"description": "Exact text to replace. Must occur exactly once in the file.",
				},
				"new_text": map[string]any{
					"type":        "string",
					"description": "Replacement text. Use an empty string to delete old_text.",
				},
			},
			"required": []string{"path", "old_text", "new_text"},
		},
	}
}

func (t *Edit) Call(args map[string]any) any {
	var metadata struct {
		Error string `json:"error,omitempty"`
		Path  string `json:"path,omitempty"`
	}

	metadata.Path, _ = argString(args, "path")
	if metadata.Path == "" {
		metadata.Error = "invalid arguments: missing path string"
		return enc(metadata)
	}
	oldText, _ := argString(args, "old_text")
	old := []byte(oldText)
	if len(old) == 0 {
		metadata.Error = "invalid arguments: missing old_text string"
		return enc(metadata)
	}
	newText, ok := argString(args, "new_text")
	if !ok {
		metadata.Error = "invalid arguments: missing new_text string"
		return enc(metadata)
	}
	new := []byte(newText)

	if !t.HiddenFiles && hasHiddenComponent(metadata.Path) {
		metadata.Error = "invalid arguments: hidden path not allowed"
		return enc(metadata)
	}
	if t.LocalFiles && !filepath.IsLocal(metadata.Path) {
		metadata.Error = "invalid arguments: non-local path not allowed"
		return enc(metadata)
	}

	file, err := os.OpenFile(metadata.Path, os.O_RDWR, 0)
	if err != nil {
		metadata.Error = fmt.Sprintf("open: %s", err)
		return enc(metadata)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		metadata.Error = fmt.Sprintf("stat: %s", err)
		return enc(metadata)
	}
	if !info.Mode().IsRegular() {
		metadata.Error = fmt.Sprintf("'%s' is not a text file", metadata.Path)
		return enc(metadata)
	}
	mediaType, err := detectMediaType(file)
	if err != nil {
		metadata.Error = fmt.Sprintf("detect media type: %s", err)
		return enc(metadata)
	}
	if !strings.Contains(mediaType, "text/") {
		metadata.Error = fmt.Sprintf("'%s' is not a text file", metadata.Path)
		return enc(metadata)
	}

	content, err := io.ReadAll(file)
	if err != nil {
		metadata.Error = fmt.Sprintf("read: %s", err)
		return enc(metadata)
	}
	if bytes.Contains(content, []byte("\r\n")) {
		old = crlf(old)
		new = crlf(new)
	}
	i := bytes.Index(content, old)
	if i == -1 {
		metadata.Error = fmt.Sprintf("'%s' does not contain old_text", metadata.Path)
		return enc(metadata)
	}
	if bytes.LastIndex(content, old) != i {
		metadata.Error = fmt.Sprintf("'%s' does not contain old_text exactly once", metadata.Path)
		return enc(metadata)
	}
	content = bytes.Replace(content, old, new, 1)
	if _, err := file.WriteAt(content, 0); err != nil {
		metadata.Error = fmt.Sprintf("write: %s", err)
		return enc(metadata)
	}
	if err := file.Truncate(int64(len(content))); err != nil {
		metadata.Error = fmt.Sprintf("truncate: %s", err)
		return enc(metadata)
	}
	return enc(metadata)
}

func crlf(b []byte) []byte {
	b = bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
	return bytes.ReplaceAll(b, []byte("\n"), []byte("\r\n"))
}
