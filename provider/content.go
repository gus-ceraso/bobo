package provider

import (
	"encoding/base64"
	"mime"
	"net/http"
	"net/url"
	"strings"
)

// Part is a MIME-typed function result. Text is encoded as input_text, images as
// input_image, and other content as input_file. Content is copied into requests;
// callers must not mutate it concurrently with Prompt.
type Part struct {
	ContentType string
	Content     []byte
}

func encodeParts(parts []Part) []any {
	content := make([]any, 0, len(parts))
	for _, p := range parts {
		contentType, _, err := mime.ParseMediaType(p.ContentType)
		if err != nil {
			p.ContentType = http.DetectContentType(p.Content)
			contentType, _, _ = mime.ParseMediaType(p.ContentType)
		}
		if contentType == "text/plain" {
			content = append(content, map[string]any{
				"type": "input_text",
				"text": string(p.Content),
			})
			continue
		}
		data := "data:" + p.ContentType + ";base64," + base64.StdEncoding.EncodeToString(p.Content)
		if strings.HasPrefix(contentType, "image/") {
			content = append(content, map[string]any{
				"type":      "input_image",
				"image_url": data,
			})
			continue
		}
		content = append(content, map[string]any{
			"type":      "input_file",
			"file_data": data,
		})
	}
	return content
}

func decodeParts(content any) []Part {
	if text, ok := content.(string); ok {
		return []Part{{ContentType: "text/plain", Content: []byte(text)}}
	}
	items, _ := content.([]any)
	var parts []Part
	for _, value := range items {
		c, ok := value.(map[string]any)
		if !ok {
			continue
		}
		t, _ := c["type"].(string)
		switch t {
		case "input_text", "output_text", "refusal":
			key := "text"
			if t == "refusal" {
				key = "refusal"
			}
			text, _ := c[key].(string)
			parts = append(parts, Part{ContentType: "text/plain", Content: []byte(text)})
		case "input_image", "input_file":
			key := "image_url"
			if t == "input_file" {
				key = "file_data"
			}
			data, ok := c[key].(string)
			if !ok {
				continue
			}
			var p Part
			var err error
			if strings.HasPrefix(data, "data:") {
				header, body, ok := strings.Cut(data[len("data:"):], ",")
				if !ok {
					continue
				}
				p.ContentType = strings.TrimSuffix(header, ";base64")
				if strings.HasSuffix(header, ";base64") {
					p.Content, err = base64.StdEncoding.DecodeString(body)
				} else {
					var text string
					text, err = url.PathUnescape(body)
					p.Content = []byte(text)
				}
			} else if t == "input_file" {
				p.Content, err = base64.StdEncoding.DecodeString(data)
			} else {
				continue
			}
			if err == nil {
				if _, _, err := mime.ParseMediaType(p.ContentType); err != nil {
					p.ContentType = http.DetectContentType(p.Content)
				}
				parts = append(parts, p)
			}
		}
	}
	return parts
}
