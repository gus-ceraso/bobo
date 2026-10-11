package procedure

import (
	"fmt"
	"os"
	"path/filepath"
)

type Write struct {
	HiddenFiles bool
	LocalFiles  bool
}

func (_ *Write) Schema() map[string]any {
	return map[string]any{
		"name":        "write",
		"description": "Write text content to a file. If the file already exists, then overwrite it; else create the file and parent directories. Return empty error on success.",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "File path to write.",
				},
				"text": map[string]any{
					"type":        "string",
					"description": "UTF-8 text content to write.",
				},
			},
			"required": []string{"path", "text"},
		},
	}
}

func (t *Write) Call(args map[string]any) any {
	var metadata struct {
		Error string `json:"error,omitempty"`
		Path  string `json:"path,omitempty"`
	}

	metadata.Path = argString(args, "path")
	if metadata.Path == "" {
		metadata.Error = "invalid arguments: missing path string"
		return enc(metadata)
	}
	content := argString(args, "text")

	if !t.HiddenFiles && hasHiddenComponent(metadata.Path) {
		metadata.Error = "invalid arguments: hidden path not allowed"
		return enc(metadata)
	}
	if t.LocalFiles && !filepath.IsLocal(metadata.Path) {
		metadata.Error = "invalid arguments: non-local path not allowed"
		return enc(metadata)
	}

	if err := os.MkdirAll(filepath.Dir(metadata.Path), 0o755); err != nil {
		metadata.Error = fmt.Sprintf("mkdir -p: %s", err)
		return enc(metadata)
	}
	if err := os.WriteFile(metadata.Path, []byte(content), 0o644); err != nil {
		metadata.Error = fmt.Sprintf("write: %s", err)
	}
	return enc(metadata)
}
