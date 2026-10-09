package exa

import "fmt"

type SearchResult struct {
	Title      string `json:"title"`
	URL        string `json:"url"`
	Highlights string `json:"highlights"`
	Text       string `json:"text"`
}

func (c *Client) Search(query string) ([]SearchResult, error) {
	var reqObj struct {
		Query    string `json:"query"`
		Contents struct {
			Text       bool `json:"text"`
			Highlights bool `json:"highlights"`
		} `json:"contents"`
	}
	reqObj.Query = query
	reqObj.Contents.Text = true
	reqObj.Contents.Highlights = true
	var resObj struct {
		Results []SearchResult `json:"results"`
	}
	if err := c.do("POST", "/search", &reqObj, &resObj); err != nil {
		return nil, fmt.Errorf("searching: %s", err)
	}
	return resObj.Results, nil
}
