package exa

import "fmt"

type SearchResult struct {
	Title      string
	URL        string
	Highlights string
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
		Results []struct {
			Title      string `json:"title"`
			URL        string `json:"url"`
			Highlights string `json:"highlights"`
			Text       string `json:"text"`
		} `json:"results"`
	}
	if err := c.do("POST", "/search", &reqObj, &resObj); err != nil {
		return nil, fmt.Errorf("searching: %s", err)
	}
	var results []SearchResult
	for _, r := range resObj.Results {
		c.Cache.Add(r.URL, Resource{"text/plain; charset=utf-8", []byte(r.Text)})
		results = append(results, SearchResult{r.Title, r.URL, r.Highlights})
	}
	return results, nil
}
