package provider

import (
	"context"
	"net/http"
)

type Client struct {
	Token      func(context.Context) (string, error)
	BaseURL    string
	HTTPClient *http.Client
}

func NewClient(token func(context.Context) (string, error)) *Client {
	return &Client{
		Token:      token,
		BaseURL:    "https://api.openai.com/v1",
		HTTPClient: http.DefaultClient,
	}
}
