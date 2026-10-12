package exa

import (
	"fmt"
	"io"
	"net/http"
	"strings"
)

func (c *Client) Fetch(url string) (Resource, error) {
	r, ok := c.Cache.Get(url)
	if ok {
		return r, nil
	}
	resp, err := c.HTTP.Get(url)
	if err != nil {
		return r, fmt.Errorf("GET %s: %s", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return r, fmt.Errorf("GET %s: %d %s (not OK)", url, resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	r.MediaType = strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(r.MediaType, "text/html") {
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
			return r, fmt.Errorf("contents: %s", err)
		}
		if len(resObj.Results) == 0 {
			return r, nil
		}
		r.Content = []byte(resObj.Results[0].Text)
		c.Cache.Add(url, r)
		return r, nil
	}
	r.Content, err = io.ReadAll(resp.Body)
	if err != nil {
		return r, fmt.Errorf("read: %s", err)
	}
	c.Cache.Add(url, r)
	return r, nil
}
