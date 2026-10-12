package openrouter

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
)

type Client struct {
	Token string
	Log   *log.Logger
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
	req, err := http.NewRequest(method, "https://openrouter.ai/api/v1"+path, reqBody)
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
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&errObj); err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("JSON decoding error response object: %s", err)
		}
		return fmt.Errorf("%d %s: %s", resp.StatusCode, http.StatusText(resp.StatusCode), errObj.Error.Message)
	}
	if resObj != nil {
		if err := json.NewDecoder(resp.Body).Decode(resObj); err != nil {
			return fmt.Errorf("JSON decoding response object: %s", err)
		}
	}
	return nil
}
