package procedure

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"

	"github.com/gen2brain/go-fitz"

	web "ceraso.dev/bobo/web"
)

type Fetch struct {
	Client *web.Client
}

func (_ *Fetch) Schema() map[string]any {
	return map[string]any{
		"name":        "fetch",
		"description": "Fetch a URL with HTTP GET. HTML pages return their extracted text, other text and JSON return lines, PDFs return pages as images, and JPEG, PNG, and WebP images are returned as is. The result includes the total line or page count for paging. Responses are cached for 30 minutes. Text with lines longer than 64 KiB isn't supported.",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{
					"type":        "string",
					"description": "HTTP or HTTPS URL to fetch.",
				},
				"offset": map[string]any{
					"type":        "integer",
					"description": "1-based line or PDF page to start from. Defaults to 1. Ignored for images.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum lines or PDF pages to return. Defaults to 1000 lines or 5 pages. Ignored for images.",
				},
			},
			"required": []string{"url"},
		},
	}
}

func (t *Fetch) Call(args map[string]any) any {
	var metadata struct {
		Error     string `json:"error,omitempty"`
		URL       string `json:"url,omitempty"`
		MediaType string `json:"media_type,omitempty"`
		Offset    int    `json:"offset,omitempty"`
		Limit     int    `json:"limit,omitempty"`
		Total     int    `json:"total,omitempty"`
	}

	metadata.URL = argString(args, "url")
	if metadata.URL == "" {
		metadata.Error = "invalid arguments: missing url string"
		return enc(metadata)
	}
	metadata.Offset = argInt(args, "offset")
	metadata.Limit = argInt(args, "limit")

	resource, err := t.Client.Fetch(metadata.URL)
	if err != nil {
		metadata.Error = fmt.Sprintf("fetch '%s': %s", metadata.URL, err)
		return enc(metadata)
	}
	metadata.MediaType = resource.MediaType
	if !containsAny(metadata.MediaType, "text/", "application/json", "application/pdf", "image/jpeg", "image/png", "image/webp") {
		metadata.Error = "media type not supported"
		return enc(metadata)
	}

	if containsAny(metadata.MediaType, "image/jpeg", "image/png", "image/webp") {
		return []map[string]any{
			map[string]any{
				"type": "text",
				"text": enc(metadata),
			},
			map[string]any{
				"type": "image_url",
				"image_url": map[string]any{
					"url": encImage(metadata.MediaType, resource.Content),
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
		doc, err := fitz.NewFromMemory(resource.Content)
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
	s := bufio.NewScanner(bytes.NewReader(resource.Content))
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
