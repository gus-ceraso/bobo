package tool

import (
	"fmt"
	"os"
	"path/filepath"
)

type Write struct {
	HiddenFiles bool
	LocalFiles  bool
}

func (t *Write) Schema() map[string]any {
	return map[string]any{
		"name":        "write",
		"description": "Create a file or replace an existing file's contents with text. Missing parent directories are created. Prefer edit for small changes to existing files.",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": pathDescription("Path to the file.", t.LocalFiles, t.HiddenFiles),
				},
				"text": map[string]any{
					"type":        "string",
					"description": "Complete new contents of the file.",
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
