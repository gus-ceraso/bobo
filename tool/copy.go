package tool

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

type Copy struct {
	HiddenFiles bool
	LocalFiles  bool
}

func (t *Copy) Schema() map[string]any {
	return map[string]any{
		"name":        "copy",
		"description": "Copy a file, directory, or symlink. Missing parent directories of new_path are created. Directories are copied recursively and merged into an existing new_path. Existing files are overwritten. Symlinks are copied as links, not followed, and file permissions are preserved. new_path is the copy itself, not a directory to copy into, and it can't be old_path or inside it. Copying stops at the first error, which can leave a partial copy.",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"old_path": map[string]any{
					"type":        "string",
					"description": pathDescription("Path to copy.", t.LocalFiles, t.HiddenFiles),
				},
				"new_path": map[string]any{
					"type":        "string",
					"description": pathDescription("Path of the copy.", t.LocalFiles, t.HiddenFiles),
				},
			},
			"required": []string{"old_path", "new_path"},
		},
	}
}

func (t *Copy) Call(args map[string]any) any {
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
	inside, err := isInside(metadata.OldPath, metadata.NewPath)
	if err != nil {
		metadata.Error = fmt.Sprintf("invalid arguments: %s", err)
		return enc(metadata)
	}
	if inside {
		metadata.Error = "invalid arguments: new_path can't be old_path or inside it"
		return enc(metadata)
	}

	err = filepath.WalkDir(metadata.OldPath, func(old string, d fs.DirEntry, e error) error {
		if e != nil {
			if d != nil && (errors.Is(e, fs.ErrNotExist) || errors.Is(e, fs.ErrPermission)) {
				return nil
			}
			return e
		}
		rel, err := filepath.Rel(metadata.OldPath, old)
		if err != nil {
			return err
		}
		new := filepath.Join(metadata.NewPath, rel)
		if rel == "." {
			if err := os.MkdirAll(filepath.Dir(new), 0o777); err != nil {
				return err
			}
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		mode := info.Mode()
		switch {
		case mode.IsRegular():
			return copyFile(new, old)
		case mode.IsDir():
			return os.MkdirAll(new, mode.Perm())
		case mode&fs.ModeSymlink != 0:
			target, err := os.Readlink(old)
			if err != nil {
				return err
			}
			if err := os.Remove(new); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			return os.Symlink(target, new)
		default:
			return fmt.Errorf("copy '%s' to '%s': unsupported file type '%s'", old, new, mode.Type())
		}
	})
	if err != nil {
		metadata.Error = fmt.Sprintf("walk: %s", err)
		return enc(metadata)
	}
	return enc(metadata)
}

func copyFile(newPath, oldPath string) error {
	oldFile, err := os.Open(oldPath)
	if err != nil {
		return fmt.Errorf("open: %s", err)
	}
	defer oldFile.Close()
	oldFileInfo, err := oldFile.Stat()
	if err != nil {
		return fmt.Errorf("stat: %s", err)
	}
	newFile, err := os.OpenFile(newPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0)
	if err != nil {
		return fmt.Errorf("open: %s", err)
	}
	defer newFile.Close()
	if err := newFile.Chmod(oldFileInfo.Mode().Perm()); err != nil {
		return fmt.Errorf("chmod: %s", err)
	}
	if _, err := io.Copy(newFile, oldFile); err != nil {
		return err
	}
	return newFile.Sync()
}

func isInside(parent, path string) (bool, error) {
	parent, err := filepath.Abs(parent)
	if err != nil {
		return false, err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return false, err
	}
	rel, err := filepath.Rel(parent, path)
	if err != nil {
		return false, err
	}
	return filepath.IsLocal(rel), nil
}
