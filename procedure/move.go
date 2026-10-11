package procedure

import (
	"fmt"
	"os"
	"path/filepath"
)

type Move struct {
	HiddenFiles bool
	LocalFiles  bool
}

func (_ *Move) Schema() map[string]any {
	return map[string]any{
		"name":        "move",
		"description": "Move a file or directory. Return empty error on success.",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"old_path": map[string]any{
					"type":        "string",
					"description": "Old path.",
				},
				"new_path": map[string]any{
					"type":        "string",
					"description": "New path.",
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

	if err := os.Rename(metadata.OldPath, metadata.NewPath); err != nil {
		metadata.Error = fmt.Sprintf("rename: %s", err)
		return enc(metadata)
	}
	return enc(metadata)
}
