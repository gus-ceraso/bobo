package provider

import (
	"log"
	"net/http"
)

// Client runs conversations using ChatGPT credentials or a fallback API token.
// Construct clients with NewClient and configure the exported fields before use.
// A client may serve concurrent requests for distinct sessions. Do not copy it.
// Authentication state is private; credential persistence belongs to its store.
type Client struct {
	// BaseURL is the Responses API root, without a trailing slash.
	BaseURL string
	// HTTPClient handles both Responses API and OAuth token requests.
	HTTPClient *http.Client
	// Logger receives session events and credential completions, failures, and
	// authentication switches. Nil disables logging. Credential logs omit secrets;
	// session logs contain conversation and function content.
	Logger *log.Logger

	store      CredentialStore
	authGate   chan struct{}
	fallback   bool
	generation uint64
	observed   *Credential
}

// NewClient constructs a client without performing storage or network I/O.
// The store is the only credential source; no environment variables are read.
// A nil store is allowed, but Prompt and SignIn then return ErrNoCredentials.
func NewClient(store CredentialStore) *Client {
	return &Client{
		BaseURL:    "https://api.openai.com/v1",
		HTTPClient: http.DefaultClient,
		store:      store,
		authGate:   make(chan struct{}, 1),
	}
}
