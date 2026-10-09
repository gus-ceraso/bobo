package exa

import "fmt"

func (c *Client) Fetch(url string) (string, error) {
	if text, ok := c.Cache.Get(url); ok {
		return text, nil
	}
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
		return "", fmt.Errorf("fetching: %s", err)
	}
	if len(resObj.Results) == 0 {
		return "", nil
	}
	c.Cache.Add(url, resObj.Results[0].Text)
	return resObj.Results[0].Text, nil
}
