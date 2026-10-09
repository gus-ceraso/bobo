package procedure

import (
	"bytes"
	"fmt"
	_ "golang.org/x/image/webp"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"strings"

	"ceraso.dev/bobo/openrouter"
)

type ReadImage struct {
	HiddenFiles bool
	LocalFiles  bool
}

func (r *ReadImage) Schema() map[string]any {
	return map[string]any{
		"name":        "read_image",
		"description": "Visualize the contents of a local image file.",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "File path to view.",
				},
			},
			"required": []string{"path"},
		},
	}
}

func (r *ReadImage) Call(args map[string]any) any {
	var o struct {
		Path      string `json:"path,omitempty"`
		Error     string `json:"error,omitempty"`
		MediaType string `json:"media_type,omitempty"`
		Width     int    `json:"width,omitempty"`
		Height    int    `json:"height,omitempty"`
	}

	var path string
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
	if !strings.Contains(mediaType, "image/jpeg") || !strings.Contains(mediaType, "image/png") || !strings.Contains(mediaType, "image/webp") {
		o.Error = fmt.Sprintf("'%s' is not a supported image file", path)
		return enc(o)
	}
	o.MediaType = mediaType

	content, err := io.ReadAll(file)
	if err != nil {
		o.Error = fmt.Sprintf("read: %s", err)
		return enc(o)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(content))
	if err != nil {
		o.Error = fmt.Sprintf("decoding image: %s", err)
		return enc(o)
	}
	o.Width = cfg.Width
	o.Height = cfg.Height

	return enc([]any{
		map[string]any{
			"type": "text",
			"text": enc(o),
		},
		map[string]any{
			"type": "image_url",
			"image_url": map[string]any{
				"url": openrouter.ImageToURL(openrouter.Image{MediaType: mediaType, Content: content}),
			},
		},
	})
}
