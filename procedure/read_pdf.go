package procedure

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gen2brain/go-fitz"

	"ceraso.dev/bobo/openrouter"
)

type ReadPDF struct {
	HiddenFiles bool
	LocalFiles  bool
}

func (r *ReadPDF) Schema() map[string]any {
	return map[string]any{
		"name":        "read_pdf",
		"description": "Visualize the contents of a local PDF file.",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "File path to read.",
				},
				"page": map[string]any{
					"type":        "number",
					"description": "Page number to view. Defaults to 1.",
				},
			},
			"required": []string{"path"},
		},
	}
}

func (r *ReadPDF) Call(args map[string]any) any {
	var o struct {
		Path               string `json:"path,omitempty"`
		Page               int    `json:"page,omitempty"`
		Error              string `json:"error,omitempty"`
		MediaType          string `json:"media_type,omitempty"`
		TextContent        string `json:"text_content,omitempty"`
		TotalNumberOfPages int    `json:"total_number_of_pages,omitempty"`
	}

	var path string
	var page int = 1
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
	pageArg, ok := args["page"]
	if ok {
		page, ok = pageArg.(int)
		if !ok {
			o.Error = "invalid arguments: page is not a number"
			return enc(o)
		}
		if page < 1 {
			o.Error = "invalid arguments: page is less than 1"
			return enc(o)
		}
	}
	o.Page = page

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
	if !strings.Contains(mediaType, "application/pdf") {
		o.Error = fmt.Sprintf("'%s' is not a PDF file", path)
		return enc(o)
	}
	o.MediaType = mediaType

	doc, err := fitz.NewFromReader(file)
	if err != nil {
		o.Error = fmt.Sprintf("parse PDF: %s", err)
		return enc(o)
	}
	defer doc.Close()
	o.TotalNumberOfPages = doc.NumPage()
	imageContent, err := doc.ImagePNG(page, 300.0)
	if err != nil {
		o.Error = fmt.Sprintf("extract PDF page image: %s", err)
		return enc(o)
	}
	textContent, err := doc.Text(page)
	if err != nil {
		o.Error = fmt.Sprintf("extract PDF page text: %s", err)
		return enc(o)
	}
	o.TextContent = textContent

	return enc([]any{
		map[string]any{
			"type": "text",
			"text": enc(o),
		},
		map[string]any{
			"type": "image_url",
			"image_url": map[string]any{
				"url": openrouter.ImageToURL(openrouter.Image{MediaType: "image/png", Content: imageContent}),
			},
		},
	})
}
