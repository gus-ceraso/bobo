package tool

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type Grep struct {
	HiddenFiles bool
	LocalFiles  bool
}

func (t *Grep) Schema() map[string]any {
	return map[string]any{
		"name":        "grep",
		"description": "Recursively search text files for lines matching a regular expression. Returns one match per line as path:line_number:line, with paths relative to the search directory, in lexical order. Symlinks, files that aren't text, and files with lines longer than 64 KiB are skipped. The result includes the total match count for paging.",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{
					"type":        "string",
					"description": "RE2 regular expression matched against each line. It matches anywhere in the line unless anchored with ^ or $. Matching is case-sensitive; use (?i) to ignore case.",
				},
				"path": map[string]any{
					"type":        "string",
					"description": pathDescription("Directory to search. Defaults to \".\".", t.LocalFiles, t.HiddenFiles),
				},
				"path_pattern": map[string]any{
					"type":        "string",
					"description": "RE2 regular expression that limits the search to files whose relative paths match, such as \"\\.go$\". Matches anywhere in the path unless anchored. Defaults to all files.",
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

func (t *Grep) Call(args map[string]any) any {
	var metadata struct {
		Error       string `json:"error,omitempty"`
		Pattern     string `json:"pattern,omitempty"`
		Path        string `json:"path,omitempty"`
		PathPattern string `json:"path_pattern,omitempty"`
		Offset      int    `json:"offset,omitempty"`
		Limit       int    `json:"limit,omitempty"`
		Total       int    `json:"total,omitempty"`
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
	metadata.PathPattern = argString(args, "path_pattern")
	if metadata.PathPattern == "" {
		metadata.PathPattern = ".*"
	}
	pathRE, err := regexp.Compile(metadata.PathPattern)
	if err != nil {
		metadata.Error = fmt.Sprintf("compile regular expression path pattern: %s", err)
		return enc(metadata)
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
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		relPath, err := filepath.Rel(metadata.Path, p)
		if err != nil {
			return err
		}
		if !pathRE.MatchString(relPath) {
			return nil
		}
		file, err := os.Open(p)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) {
				return nil
			}
			return fmt.Errorf("open: %s", err)
		}
		defer file.Close()
		mediaType, err := detectMediaType(file)
		if err != nil {
			return fmt.Errorf("detect media type: %s", err)
		}
		if !strings.Contains(mediaType, "text/") {
			return nil
		}
		s := bufio.NewScanner(file)
		var matches []string
		for j := 1; s.Scan(); j += 1 {
			fileLine := s.Text()
			if re.MatchString(fileLine) {
				matches = append(matches, fmt.Sprintf("%s:%d:%s", relPath, j, fileLine))
			}
		}
		if err := s.Err(); errors.Is(err, bufio.ErrTooLong) {
			return nil
		} else if err != nil {
			return fmt.Errorf("scanning text: %s", err)
		}
		for _, match := range matches {
			if i >= metadata.Offset && len(lines) < metadata.Limit {
				lines = append(lines, match)
			}
			i += 1
		}
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
