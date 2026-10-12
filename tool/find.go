package tool

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type Find struct {
	HiddenFiles bool
	LocalFiles  bool
}

func (t *Find) Schema() map[string]any {
	return map[string]any{
		"name":        "find",
		"description": "Recursively find files whose paths match a regular expression. Returns one path per line, relative to the search directory, in lexical order. Directories aren't listed, and symlinks are listed but not followed. The result includes the total match count for paging.",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{
					"type":        "string",
					"description": "RE2 regular expression matched against each relative path, such as \"\\.go$\". It matches anywhere in the path unless anchored with ^ or $. Matching is case-sensitive; use (?i) to ignore case.",
				},
				"path": map[string]any{
					"type":        "string",
					"description": pathDescription("Directory to search. Defaults to \".\".", t.LocalFiles, t.HiddenFiles),
				},
				"offset": map[string]any{
					"type":        "integer",
					"description": "1-based match to start from. Defaults to 1.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum matches to return. Defaults to 1000.",
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
		metadata.Error = "invalid arguments: missing regular expression pattern string"
		return enc(metadata)
	}
	re, err := regexp.Compile(metadata.Pattern)
	if err != nil {
		metadata.Error = fmt.Sprintf("compile regular expression pattern: %s", err)
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
	info, err := os.Lstat(metadata.Path)
	if err != nil {
		metadata.Error = fmt.Sprintf("stat: %s", err)
		return enc(metadata)
	}
	if !info.IsDir() {
		metadata.Error = fmt.Sprintf("'%s' is not a directory", metadata.Path)
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
		if !re.MatchString(line) {
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
