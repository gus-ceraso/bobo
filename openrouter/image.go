package openrouter

import (
	"encoding/base64"
	"fmt"
)

type Image struct {
	MediaType string
	Content   []byte
}

func ImageToURL(img Image) string {
	return "data:" + img.MediaType + ";base64;" + base64.StdEncoding.EncodeToString(img.Content)
}

func (c *Client) GenerateImage(model, prompt, format, quality, size string, refImgs []Image) (Image, error) {
	var reqObj struct {
		Model           string `json:"model"`
		Prompt          string `json:"prompt"`
		Format          string `json:"output_format"`
		Quality         string `json:"quality"`
		Size            string `json:"size"`
		InputReferences []struct {
			Type     string `json:"type"`
			ImageURL struct {
				URL string `json:"url"`
			} `json:"image_url"`
		} `json:"input_references"`
	}
	reqObj.Model = model
	reqObj.Prompt = prompt
	reqObj.Format = format
	reqObj.Quality = quality
	reqObj.Size = size
	reqObj.InputReferences = make([]struct {
		Type     string `json:"type"`
		ImageURL struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}, len(refImgs))
	for i := 0; i < len(refImgs); i++ {
		reqObj.InputReferences[i].Type = "image_url"
		reqObj.InputReferences[i].ImageURL.URL = ImageToURL(refImgs[i])
	}
	var resObj struct {
		Data []struct {
			MediaType string `json:"media_type"`
			Content   string `json:"b64_json"`
		} `json:"data"`
	}
	if err := c.do("POST", "/image", &reqObj, &resObj); err != nil {
		return Image{}, fmt.Errorf("sending prompt: %s", err)
	}
	if len(resObj.Data) == 0 {
		return Image{}, fmt.Errorf("empty response")
	}
	content, err := base64.StdEncoding.DecodeString(resObj.Data[0].Content)
	if err != nil {
		return Image{}, fmt.Errorf("base64 decoding image response: %s", err)
	}
	return Image{MediaType: resObj.Data[0].MediaType, Content: content}, nil
}
