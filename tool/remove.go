package tool

import (
	"os"
	"path/filepath"
)

type Remove struct {
	HiddenFiles bool
	LocalFiles  bool
}

func (t *Remove) Schema() map[string]any {
	return map[string]any{
		"name":        "remove",
		"description": "Permanently delete a file, symlink, or directory with all of its contents. Symlinks are deleted, not followed. Succeeds if path doesn't exist.",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": pathDescription("Path to delete. Can't be the working directory.", t.LocalFiles, t.HiddenFiles),
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

	if filepath.Clean(metadata.Path) == "." {
		metadata.Error = "invalid arguments: path can't be the working directory"
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
