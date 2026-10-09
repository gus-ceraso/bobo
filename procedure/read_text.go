package procedure

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type ReadText struct {
	HiddenFiles bool
	LocalFiles  bool
}

func (r *ReadText) Schema() map[string]any {
	return map[string]any{
		"name":        "read_text",
		"description": "Read the contents of a local text file.",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "File path to read.",
				},
				"offset": map[string]any{
					"type":        "number",
					"description": "1-based line number to start. Defaults to 1.",
				},
				"limit": map[string]any{
					"type":        "number",
					"description": "Maximum number of lines to read. Defaults to 1000.",
				},
			},
			"required": []string{"path"},
		},
	}
}

func (r *ReadText) Call(args map[string]any) any {
	var o struct {
		Path               string `json:"path,omitempty"`
		Offset             int    `json:"offset,omitempty"`
		Limit              int    `json:"limit,omitempty"`
		LinesRead          int    `json:"lines_read,omitempty"`
		Error              string `json:"error,omitempty"`
		MediaType          string `json:"media_type,omitempty"`
		Content            string `json:"content,omitempty"`
		TotalNumberOfLines int    `json:"total_number_of_lines,omitempty"`
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

	mediaType, err := mediaType(file)
	if err != nil {
		o.Error = fmt.Sprintf("media type detection: %s", err)
		return enc(o)
	}
	if !strings.Contains(mediaType, "text/plain") {
		o.Error = fmt.Sprintf("'%s' is not a text file", path)
		return enc(o)
	}
	o.MediaType = mediaType

	var lines []string
	s := bufio.NewScanner(file)
	var n int
	for n = 1; s.Scan(); n += 1 {
		if n < offset {
			continue
		}
		if len(lines) < limit {
			lines = append(lines, s.Text())
		}
	}
	if err := s.Err(); err != nil {
		o.Error = fmt.Sprintf("scanning text: %s", err)
		return enc(o)
	}
	o.Content = strings.Join(lines, "\n")
	o.LinesRead = len(lines)
	o.TotalNumberOfLines = n - 1
	return enc(o)
}
