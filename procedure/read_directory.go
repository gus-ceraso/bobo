package procedure

import (
	"fmt"
	"os"
	"path/filepath"
)

type ReadDirectory struct {
	HiddenFiles bool
	LocalFiles  bool
}

func (r *ReadDirectory) Schema() map[string]any {
	return map[string]any{
		"name":        "read_directory",
		"description": "List the contents of a local directory.",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "File path to read.",
				},
				"offset": map[string]any{
					"type":        "number",
					"description": "1-based entry number to start. Defaults to 1.",
				},
				"limit": map[string]any{
					"type":        "number",
					"description": "Maximum number of entries to list. Defaults to 1000.",
				},
			},
			"required": []string{"path"},
		},
	}
}

func (r *ReadDirectory) Call(args map[string]any) any {
	var o struct {
		Path                 string   `json:"path,omitempty"`
		Offset               int      `json:"offset,omitempty"`
		Limit                int      `json:"limit,omitempty"`
		EntriesListed        int      `json:"num_entries,omitempty"`
		Error                string   `json:"error,omitempty"`
		Entries              []string `json:"entries,omitempty"`
		TotalNumberOfEntries int      `json:"total_number_of_entries,omitempty"`
	}

	var path string
	var offset int = 1
	var limit int = 1000
	pathArg, ok := args["path"]
	if !ok {
		o.Error = "invalid arguments: missing path"
		return enc(o)
	}
	path, ok = pathArg.(string)
	if !ok {
		o.Error = "invalid arguments: path is not a string"
		return enc(o)
	}
	o.Path = path
	offsetArg, ok := args["offset"]
	if ok {
		offset, ok = offsetArg.(int)
		if !ok {
			o.Error = "invalid arguments: offset is not a number"
			return enc(o)
		}
		if offset < 1 {
			o.Error = "invalid arguments: offset is less than 1"
			return enc(o)
		}
	}
	o.Offset = offset
	limitArg, ok := args["limit"]
	if ok {
		limit, ok = limitArg.(int)
		if !ok {
			o.Error = "invalid arguments: limit is not a number"
			return enc(o)
		}
		if limit < 1 {
			o.Error = "invalid arguments: limit is less than 1"
			return enc(o)
		}
	}
	o.Limit = limit

	if !r.HiddenFiles && hasHiddenComponent(path) {
		o.Error = "invalid arguments: path not found"
		return enc(o)
	}
	if r.LocalFiles && !filepath.IsLocal(path) {
		o.Error = "invalid arguments: path not found"
		return enc(o)
	}

	file, err := os.Open(path)
	if err != nil {
		o.Error = fmt.Sprintf("open: %s", err)
		return enc(o)
	}
	defer file.Close()

	entries, err := file.ReadDir(0)
	if err != nil {
		o.Error = fmt.Sprintf("readdir: %s", err)
		return enc(o)
	}
	for i, entry := range entries {
		if i+1 < offset {
			continue
		}
		name := entry.Name()
		if entry.IsDir() {
			name += "/"
		}
		o.Entries = append(o.Entries, name)
		if len(o.Entries) == limit {
			break
		}
	}
	o.EntriesListed = len(o.Entries)
	o.TotalNumberOfEntries = len(entries)
	return enc(o)
}
