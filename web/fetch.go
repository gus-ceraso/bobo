package exa

import (
	"fmt"
	"io"
	"net/http"
	"strings"
)

var supportedMediaTypes []string = []string{
	"text/plain",
	"text/markdown",
	"text/csv",
	"text/html",
	"application/json",
	"application/pdf",
	"image/jpeg",
	"image/png",
	"image/webp",
}

func isSupported(mediaType string) bool {
	for _, supported := range supportedMediaTypes {
		if strings.Contains(mediaType, supported) {
			return true
		}
	}
	return false
}

func (c *Client) Fetch(url string) (Resource, error) {
	if r, ok := c.Cache.Get(url); ok {
		return r, nil
	}

	resp, err := http.Head(url)
	if err != nil {
		return Resource{}, fmt.Errorf("HEAD %s: %s", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return Resource{}, fmt.Errorf("HEAD %s: %d %s (not OK)", url, resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	mediaType := strings.ToLower(resp.Header.Get("Content-Type"))
	if !isSupported(mediaType) {
		return Resource{}, fmt.Errorf("media type not supported")
	}

	if strings.Contains(mediaType, "text/html") {
		var reqObj struct {
			URLs []string `json:"urls"`
			Text bool     `json:"text"`
		}
		reqObj.URLs = []string{url}
		reqObj.Text = true
		var resObj struct {
			Results []struct {
				Text string `json:"text"`
			} `json:"results"`
		}
		if err := c.do("POST", "/contents", &reqObj, &resObj); err != nil {
			return Resource{}, fmt.Errorf("contents: %s", err)
		}
		if len(resObj.Results) == 0 {
			return Resource{}, nil
		}
		r := Resource{"text/plain; charset=utf-8", []byte(resObj.Results[0].Text)}
		c.Cache.Add(url, r)
		return r, nil
	}

	resp, err = http.Get(url)
	if err != nil {
		return Resource{}, fmt.Errorf("GET %s: %s", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return Resource{}, fmt.Errorf("GET %s: %d %s (not OK)", url, resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	content, err := io.ReadAll(resp.Body)
	if err != nil {
		return Resource{}, fmt.Errorf("read: %s", err)
	}
	mediaType = http.DetectContentType(content)
	if !isSupported(mediaType) {
		return Resource{}, fmt.Errorf("media type not supported")
	}
	r := Resource{mediaType, content}
	c.Cache.Add(url, r)
	return r, nil
}
