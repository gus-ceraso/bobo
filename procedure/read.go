package procedure

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/gen2brain/go-fitz"
)

type Read struct {
	HiddenFiles bool
	LocalFiles  bool
}

func (_ *Read) Schema() map[string]any {
	return map[string]any{
		"name":        "read",
		"description": "Read the contents of a local file or directory. Supports text, PDF, and image.",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "File path to read.",
				},
				"offset": map[string]any{
					"type":        "number",
					"description": "1-based line number or PDF page to start. Defaults to 1.",
				},
				"limit": map[string]any{
					"type":        "number",
					"description": "Maximum number of lines or PDF pages to read. Defaults to 1000 lines or 5 pages.",
				},
			},
			"required": []string{"path"},
		},
	}
}

func (t *Read) Call(args map[string]any) any {
	var metadata struct {
		Error     string `json:"error,omitempty"`
		Path      string `json:"path,omitempty"`
		MediaType string `json:"media_type,omitempty"`
		Offset    int    `json:"offset,omitempty"`
		Limit     int    `json:"limit,omitempty"`
		Total     int    `json:"total,omitempty"`
	}

	metadata.Path = argString(args, "path")
	if metadata.Path == "" {
		metadata.Error = "invalid arguments: missing path string"
		return enc(metadata)
	}
	metadata.Offset = argInt(args, "offset")
	metadata.Limit = argInt(args, "limit")

	if !t.HiddenFiles && hasHiddenComponent(metadata.Path) {
		metadata.Error = "invalid arguments: hidden path not allowed"
		return enc(metadata)
	}
	if t.LocalFiles && !filepath.IsLocal(metadata.Path) {
		metadata.Error = "invalid arguments: non-local path not allowed"
		return enc(metadata)
	}

	file, err := os.Open(metadata.Path)
	if err != nil {
		metadata.Error = fmt.Sprintf("open: %s", err)
		return enc(metadata)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		metadata.Error = fmt.Sprintf("stat: %s", err)
		return enc(metadata)
	}

	if info.IsDir() {
		if metadata.Offset == 0 {
			metadata.Offset = 1
		}
		if metadata.Limit == 0 {
			metadata.Limit = 1000
		}
		metadata.MediaType = "inode/directory"
		entries, err := file.ReadDir(0)
		if err != nil {
			metadata.Error = fmt.Sprintf("readdir: %s", err)
			return enc(metadata)
		}
		metadata.Total = len(entries)
		var lines []string
		for i, entry := range entries {
			if i+1 < metadata.Offset {
				continue
			}
			name := entry.Name()
			if entry.IsDir() {
				name += "/"
			}
			lines = append(lines, name)
			if len(lines) == metadata.Limit {
				break
			}
		}
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

	metadata.MediaType, err = detectMediaType(file)
	if err != nil {
		metadata.Error = fmt.Sprintf("detect media type: %s", err)
		return enc(metadata)
	}
	if !containsAny(metadata.MediaType, "text/", "application/json", "application/pdf", "image/jpeg", "image/png", "image/webp") {
		metadata.Error = "file media type not supported"
		return enc(metadata)
	}

	if containsAny(metadata.MediaType, "image/jpeg", "image/png", "image/webp") {
		content, err := io.ReadAll(file)
		if err != nil {
			metadata.Error = fmt.Sprintf("read: %s", err)
			return enc(metadata)
		}
		return []map[string]any{
			map[string]any{
				"type": "text",
				"text": enc(metadata),
			},
			map[string]any{
				"type": "image_url",
				"image_url": map[string]any{
					"url": encImage(metadata.MediaType, content),
				},
			},
		}
	}

	if strings.Contains(metadata.MediaType, "application/pdf") {
		if metadata.Offset == 0 {
			metadata.Offset = 1
		}
		if metadata.Limit == 0 {
			metadata.Limit = 5
		}
		doc, err := fitz.NewFromReader(file)
		if err != nil {
			metadata.Error = fmt.Sprintf("load PDF: %s", err)
			return enc(metadata)
		}
		defer doc.Close()
		metadata.Total = doc.NumPage()
		var pages [][]byte
		for i := 1; i <= metadata.Total; i++ {
			if i < metadata.Offset {
				continue
			}
			page, err := doc.ImagePNG(i-1, 150.0)
			if err != nil {
				metadata.Error = fmt.Sprintf("extract PDF page %d: %s", i, err)
				return enc(metadata)
			}
			pages = append(pages, page)
			if len(pages) == metadata.Limit {
				break
			}
		}
		ret := []map[string]any{
			map[string]any{
				"type": "text",
				"text": enc(metadata),
			},
		}
		for _, page := range pages {
			ret = append(ret, map[string]any{
				"type": "image_url",
				"image_url": map[string]any{
					"url": encImage("image/png", page),
				},
			})
		}
		return ret
	}

	if metadata.Offset == 0 {
		metadata.Offset = 1
	}
	if metadata.Limit == 0 {
		metadata.Limit = 1000
	}
	var lines []string
	s := bufio.NewScanner(file)
	var n int
	for n = 1; s.Scan(); n += 1 {
		if n < metadata.Offset {
			continue
		}
		if len(lines) < metadata.Limit {
			lines = append(lines, s.Text())
		}
	}
	if err := s.Err(); err != nil {
		metadata.Error = fmt.Sprintf("scanning text: %s", err)
		return enc(metadata)
	}
	metadata.Total = n - 1
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
