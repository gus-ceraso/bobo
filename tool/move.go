package tool

import (
	"fmt"
	"os"
	"path/filepath"
)

type Move struct {
	HiddenFiles bool
	LocalFiles  bool
}

func (t *Move) Schema() map[string]any {
	return map[string]any{
		"name":        "move",
		"description": "Move or rename a file, directory, or symlink. new_path is the new name, not a directory to move into. Missing parent directories of new_path are created. An existing file at new_path is overwritten.",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"old_path": map[string]any{
					"type":        "string",
					"description": pathDescription("Path to move.", t.LocalFiles, t.HiddenFiles),
				},
				"new_path": map[string]any{
					"type":        "string",
					"description": pathDescription("New path.", t.LocalFiles, t.HiddenFiles),
				},
			},
			"required": []string{"old_path", "new_path"},
		},
	}
}

func (t *Move) Call(args map[string]any) any {
	var metadata struct {
		Error   string `json:"error,omitempty"`
		OldPath string `json:"old_path,omitempty"`
		NewPath string `json:"new_path,omitempty"`
	}

	metadata.OldPath = argString(args, "old_path")
	if len(metadata.OldPath) == 0 {
		metadata.Error = "invalid arguments: missing old_path string"
		return enc(metadata)
	}
	metadata.NewPath = argString(args, "new_path")
	if len(metadata.NewPath) == 0 {
		metadata.Error = "invalid arguments: missing new_path string"
		return enc(metadata)
	}

	if !t.HiddenFiles && hasHiddenComponent(metadata.OldPath) {
		metadata.Error = "invalid arguments: hidden old_path not allowed"
		return enc(metadata)
	}
	if t.LocalFiles && !filepath.IsLocal(metadata.OldPath) {
		metadata.Error = "invalid arguments: non-local old_path not allowed"
		return enc(metadata)
	}
	if !t.HiddenFiles && hasHiddenComponent(metadata.NewPath) {
		metadata.Error = "invalid arguments: hidden new_path not allowed"
		return enc(metadata)
	}
	if t.LocalFiles && !filepath.IsLocal(metadata.NewPath) {
		metadata.Error = "invalid arguments: non-local new_path not allowed"
		return enc(metadata)
	}

	if _, err := os.Lstat(metadata.OldPath); err != nil {
		metadata.Error = fmt.Sprintf("stat: %s", err)
		return enc(metadata)
	}
	if err := os.MkdirAll(filepath.Dir(metadata.NewPath), 0o777); err != nil {
		metadata.Error = fmt.Sprintf("mkdir: %s", err)
		return enc(metadata)
	}
	if err := os.Rename(metadata.OldPath, metadata.NewPath); err != nil {
		metadata.Error = fmt.Sprintf("rename: %s", err)
		return enc(metadata)
	}
	return enc(metadata)
}
