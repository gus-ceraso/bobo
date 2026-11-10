package provider

import (
	"log"
	"net/http"
)

// Client runs conversations using ChatGPT credentials or a fallback API token.
// Use NewClient and configure its fields before starting requests. A client may
// serve distinct sessions concurrently. Do not copy it.
type Client struct {
	// BaseURL is the Responses API root, without a trailing slash.
	BaseURL string
	// HTTPClient handles both Responses API and OAuth token requests.
	HTTPClient *http.Client
	// Logger receives session and authentication events. Nil disables logging.
	// Session logs contain sensitive conversation and function content.
	Logger *log.Logger

	store      CredentialStore
	authGate   chan struct{}
	fallback   bool
	generation uint64
	observed   *Credential
}

// NewClient constructs a client without I/O, using store as its only credential
// source. With a nil store, Prompt and SignIn return ErrNoCredentials.
func NewClient(store CredentialStore) *Client {
	return &Client{
		BaseURL:    "https://api.openai.com/v1",
		HTTPClient: http.DefaultClient,
		store:      store,
		authGate:   make(chan struct{}, 1),
	}
}
