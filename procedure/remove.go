package procedure

import (
	"os"
	"path/filepath"
)

type Remove struct {
	HiddenFiles bool
	LocalFiles  bool
}

func (_ *Remove) Schema() map[string]any {
	return map[string]any{
		"name":        "removemove",
		"description": "Remove a file or directory recursively. Return empty error on success.",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path of file or directory to remove.",
				},
			},
			"required": []string{"path"},
		},
	}
}

func (t *Remove) Call(args map[string]any) any {
	var metadata struct {
		Error string `json:"error,omitempty"`
		Path  string `json:"path,omitempty"`
	}

	metadata.Path = argString(args, "path")
	if len(metadata.Path) == 0 {
		metadata.Error = "invalid arguments: missing path string"
		return enc(metadata)
	}

	if !t.HiddenFiles && hasHiddenComponent(metadata.Path) {
		metadata.Error = "invalid arguments: hidden path not allowed"
		return enc(metadata)
	}
	if t.LocalFiles && !filepath.IsLocal(metadata.Path) {
		metadata.Error = "invalid arguments: non-local path not allowed"
		return enc(metadata)
	}

	if err := os.RemoveAll(metadata.Path); err != nil {
		metadata.Error = err.Error()
	}
	return enc(metadata)
}
