package procedure

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
)

type Find struct {
	HiddenFiles bool
	LocalFiles  bool
}

func (_ *Find) Schema() map[string]any {
	return map[string]any{
		"name":        "find",
		"description": "Search for file paths matching a regular expression pattern recursively. Return one match per line.",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{
					"type":        "string",
					"description": "Regular expression pattern (RE2 syntax) to match file paths against. Use ^ and $ to anchor.",
				},
				"path": map[string]any{
					"type":        "string",
					"description": "Directory path to search in. Defaults to \".\".",
				},
				"offset": map[string]any{
					"type":        "number",
					"description": "1-based path line number to start. Defaults to 1.",
				},
				"limit": map[string]any{
					"type":        "number",
					"description": "Maximum number of path lines to return. Defaults to 1000 lines.",
				},
			},
			"required": []string{"pattern"},
		},
	}
}

func (t *Find) Call(args map[string]any) any {
	var metadata struct {
		Error   string `json:"error,omitempty"`
		Pattern string `json:"pattern,omitempty"`
		Path    string `json:"path,omitempty"`
		Offset  int    `json:"offset,omitempty"`
		Limit   int    `json:"limit,omitempty"`
		Total   int    `json:"total,omitempty"`
	}

	metadata.Pattern = argString(args, "pattern")
	if metadata.Pattern == "" {
		metadata.Error = "invalid arguments: missing regexp pattern string"
		return enc(metadata)
	}
	re, err := regexp.Compile(metadata.Pattern)
	if err != nil {
		metadata.Error = fmt.Sprintf("compile regexp pattern: %s", err)
		return enc(metadata)
	}
	metadata.Path = argString(args, "path")
	if metadata.Path == "" {
		metadata.Path = "."
	}
	metadata.Offset = argInt(args, "offset")
	if metadata.Offset == 0 {
		metadata.Offset = 1
	}
	metadata.Limit = argInt(args, "limit")
	if metadata.Limit == 0 {
		metadata.Limit = 1000
	}

	if !t.HiddenFiles && hasHiddenComponent(metadata.Path) {
		metadata.Error = "invalid arguments: hidden path not allowed"
		return enc(metadata)
	}
	if t.LocalFiles && !filepath.IsLocal(metadata.Path) {
		metadata.Error = "invalid arguments: non-local path not allowed"
		return enc(metadata)
	}

	var lines []string
	i := 1
	err = filepath.WalkDir(metadata.Path, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			if d != nil && (errors.Is(e, fs.ErrNotExist) || errors.Is(e, fs.ErrPermission)) {
				return nil
			}
			return e
		}
		if d.IsDir() {
			if !t.HiddenFiles && hasHiddenComponent(p) {
				return fs.SkipDir
			}
			return nil
		}
		if !t.HiddenFiles && hasHiddenComponent(p) {
			return nil
		}
		line, err := filepath.Rel(metadata.Path, p)
		if err != nil {
			return err
		}
		if !re.MatchString(p) {
			return nil
		}
		if i >= metadata.Offset && len(lines) < metadata.Limit {
			lines = append(lines, line)
		}
		i += 1
		return nil
	})
	if err != nil {
		metadata.Error = fmt.Sprintf("walk: %s", err)
		return enc(metadata)
	}
	metadata.Total = i - 1
	content := strings.Join(lines, "\n")
	return []map[string]any{
		map[string]any{
			"type": "text",
			"text": enc(metadata),
		},
		map[string]any{
			"type": "text",
			"text": content,
		},
	}
}
