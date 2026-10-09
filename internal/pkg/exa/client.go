package exa

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/hashicorp/golang-lru/v2/expirable"
)

type Client struct {
	Token string
	Cache *expirable.LRU[string, string]
}

func NewClient(token string) *Client {
	return &Client{
		Token: token,
		Cache: expirable.NewLRU[string, string](100, nil, 30*time.Minute),
	}
}

func (c *Client) do(method, path string, reqObj, resObj any) error {
	var reqBody io.Reader
	if reqObj != nil {
		var buf bytes.Buffer
		if err := json.NewEncoder(&buf).Encode(reqObj); err != nil {
			return fmt.Errorf("JSON encoding request object: %s", err)
		}
		reqBody = &buf
	}
	req, err := http.NewRequest(method, "https://api.exa.ai"+path, reqBody)
	if err != nil {
		return fmt.Errorf("constructing http request: %s", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("doing http request: %s", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		var errObj struct {
			Error string `json:"error"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&errObj); err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("JSON decoding error response object: %s", err)
		}
		return fmt.Errorf("%d %s: %s", resp.StatusCode, http.StatusText(resp.StatusCode), errObj.Error)
	}
	if resObj != nil {
		if err := json.NewDecoder(resp.Body).Decode(resObj); err != nil {
			return fmt.Errorf("JSON decoding response object: %s", err)
		}
	}
	return nil
}
