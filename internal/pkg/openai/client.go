// Package openai supports ChatGPT sign-in, model listing, and explicit blocking
// conversation continuation. Callers own user input, tool approval/execution,
// session trees, and persistence; the package doesn't run an automatic agent loop.
// It reports plan-usage errors but doesn't query remaining allowance or reset times.
package openai

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	neturl "net/url"
	"slices"
	"strings"
	"time"
)

// ErrNoCredentials should be returned by CredentialsStore.Load when no
// credentials are stored. NewClient propagates it so callers can use errors.Is
// to detect when sign-in is needed.
var ErrNoCredentials = errors.New("no credentials")

// Credentials holds one registration's tokens, client ID, and access-token
// expiry. Tokens are secrets; ClientID isn't an authentication credential.
type Credentials struct {
	ClientID     string    // Issued registration ID, not "dynamic_agent_client".
	AccessToken  string    // Bearer credential for eligible Responses API requests.
	RefreshToken string    // Credential for renewing access without browser sign-in.
	ExpiresAt    time.Time // Access-token expiry calculated from the token response.
}

// CredentialsStore persists credentials. Implementations must protect the tokens
// from disclosure and honor context cancellation. File-backed stores should use
// owner-only permissions and keep credentials out of source control.
type CredentialsStore interface {
	Load(context.Context) (Credentials, error)
	Save(context.Context, Credentials) error
}

// Client uses a saved registration and renews expired credentials on demand.
// Credential access is serialized within this instance only; don't share a
// credential record across independently refreshing clients or processes.
//
// If saving renewed credentials fails, the request returns the save error but
// replacement tokens remain in memory: the old refresh token may already be
// invalid, so stored credentials may be stale.
type Client struct {
	baseURL         string
	http            *http.Client
	creds           *Credentials
	store           CredentialsStore
	credentialsGate chan struct{}
}

// NewClient loads saved credentials without signing in or refreshing tokens.
// Load errors are returned unchanged; use errors.Is(err, ErrNoCredentials) to
// decide when to sign in. Models and Next renew expired tokens on demand.
func NewClient(ctx context.Context, store CredentialsStore) (*Client, error) {
	creds, err := store.Load(ctx)
	if err != nil {
		return nil, err
	}
	return &Client{
		baseURL:         "https://api.openai.com/v1",
		http:            http.DefaultClient,
		creds:           &creds,
		store:           store,
		credentialsGate: make(chan struct{}, 1),
	}, nil
}

// accessToken returns the current access token, renewing it if expired. The
// gate serializes the expiry check, renewal, saving, and token selection, so
// callers waiting on another renewal use its replacement token instead of
// renewing again. ctx can cancel waiting for the gate without interrupting
// another caller's renewal. The caller's API request runs after gate release.
// Renewal doesn't retry failures and preserves the registration's client ID.
func (c *Client) accessToken(ctx context.Context) (string, error) {
	select {
	case c.credentialsGate <- struct{}{}:
		defer func() { <-c.credentialsGate }()
	case <-ctx.Done():
		return "", ctx.Err()
	}

	creds := *c.creds
	if time.Now().Before(creds.ExpiresAt) {
		return creds.AccessToken, nil
	}
	// Omitting scope retains the existing grant. Refresh doesn't need a host
	// ID, browser callback, or PKCE verifier.
	form := neturl.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {creds.ClientID},
		"refresh_token": {creds.RefreshToken},
		"resource":      {"https://api.openai.com/v1"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://auth.openai.com/api/accounts/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("refreshing credentials: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("refreshing credentials: %s", resp.Status)
	}

	receivedAt := time.Now()
	var tokens struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokens); err != nil {
		return "", fmt.Errorf("decoding refreshed tokens: %w", err)
	}
	creds.AccessToken = tokens.AccessToken
	creds.RefreshToken = tokens.RefreshToken
	creds.ExpiresAt = receivedAt.Add(time.Duration(tokens.ExpiresIn) * time.Second)
	c.creds = &creds
	if err := c.store.Save(ctx, creds); err != nil {
		return "", fmt.Errorf("saving refreshed credentials: %w", err)
	}
	return creds.AccessToken, nil
}

// SignIn obtains credentials for using a ChatGPT plan. It binds a temporary
// HTTP listener to an available port on 127.0.0.1 and returns an OpenAI
// authorization URL. The caller must present or open that URL; SignIn doesn't
// open a browser.
//
// After the user approves access, OpenAI redirects the browser to the local
// listener with an authorization code and issued client ID. SignIn exchanges
// the code over HTTPS, checks plan permission, and calls store.Save. The browser
// carries the code back to this process; OpenAI never connects to the listener.
//
// # Completion and cancellation
//
// done receives exactly one result, then closes. A nil result means credentials
// were saved and the listener has stopped. All errors arrive through done,
// including setup errors, which return an empty URL. The result is buffered, so
// the caller can wait later or choose not to receive it.
//
// Keep ctx alive until completion. Canceling it stops the listener and cancels
// the token request and store operation. There is no built-in timeout; callers
// should supply a deadline if an abandoned login must expire. Closing the browser
// doesn't cancel the attempt.
//
// # Registration and trust
//
// Each call generates a new UUID host ID and starts a new registration. The host
// ID isn't saved because refresh doesn't need it. SignIn never calls store.Load,
// reuses an existing registration, refreshes tokens, or revokes old registrations.
// Discarding local credentials doesn't delete a registration in ChatGPT settings;
// repeated sign-ins can leave multiple registrations there.
//
// SignIn rejects mismatched callback state without ending the attempt and uses
// PKCE to bind code redemption to this process. It otherwise trusts OpenAI's HTTPS
// token response: it doesn't validate or retain an ID token, send an OIDC nonce,
// or establish an authenticated user identity for the app.
//
// See https://developers.openai.com/siwc/token-sharing-open-source/sign-in.
func SignIn(ctx context.Context, store CredentialsStore) (url string, done <-chan error) {
	result := make(chan error, 1)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		result <- fmt.Errorf("starting sign-in listener: %w", err)
		close(result)
		return "", result
	}

	// This flow treats every attempt as a disposable host. The UUID identifies
	// that host, not the user, and doesn't prove possession of a credential.
	var id [16]byte
	rand.Read(id[:])
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	hostID := fmt.Sprintf("urn:uuid:%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:])

	// state ties the callback to this attempt. PKCE keeps the verifier local
	// and sends only its SHA-256 challenge through the browser. Encoding 32
	// random bytes produces a verifier meeting PKCE's 43-character minimum.
	state := rand.Text()
	var random [32]byte
	rand.Read(random[:])
	verifier := base64.RawURLEncoding.EncodeToString(random[:])
	challenge := sha256.Sum256([]byte(verifier))
	redirectURI := "http://" + listener.Addr().String() + "/auth/callback"

	// dynamic_agent_client starts registration; the callback supplies the
	// issued client ID that must be used for the token exchange and storage.
	params := neturl.Values{
		"client_id":             {"dynamic_agent_client"},
		"agent_name_hint":       {"bobo"},
		"ext_agent_host_id":     {hostID},
		"redirect_uri":          {redirectURI},
		"response_type":         {"code"},
		"scope":                 {"openid profile email offline_access resource.invoke chatgpt.tokens.use.direct"},
		"resource":              {"https://api.openai.com/v1"},
		"state":                 {state},
		"code_challenge_method": {"S256"},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(challenge[:])},
	}

	// The callback only delivers the browser's result to the background flow.
	// Receiving it isn't success yet: code exchange and saving can still fail.
	callback := make(chan neturl.Values, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /auth/callback", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if query.Get("state") != state {
			http.Error(w, "Invalid sign-in state.", http.StatusBadRequest)
			return
		}
		select {
		case callback <- query:
			fmt.Fprintln(w, "Sign-in received. Return to the application for the result.")
		default:
			http.Error(w, "Sign-in callback already received.", http.StatusConflict)
		}
	})
	server := &http.Server{Handler: mux}

	go func() {
		var err error
		// Registered first, this defer runs last: completion follows cleanup.
		defer func() {
			result <- err
			close(result)
		}()
		defer listener.Close()
		defer server.Close()
		stop := context.AfterFunc(ctx, func() { server.Close() })
		defer stop()

		serveErr := make(chan error, 1)
		go func() { serveErr <- server.Serve(listener) }()

		var query neturl.Values
		select {
		case query = <-callback:
			// Let the callback page finish before closing its connection.
			if err = server.Shutdown(ctx); err != nil {
				return
			}
		case err = <-serveErr:
			if ctx.Err() != nil {
				err = ctx.Err()
			}
			return
		case <-ctx.Done():
			err = ctx.Err()
			return
		}
		if code := query.Get("error"); code != "" {
			err = fmt.Errorf("sign-in denied: %s", code)
			return
		}
		clientID := query.Get("client_id")
		if query.Get("code") == "" || clientID == "" || clientID == "dynamic_agent_client" {
			err = errors.New("sign-in callback is missing an authorization code or issued client ID")
			return
		}

		// Exchange the code directly; tokens never pass through the browser.
		form := neturl.Values{
			"grant_type":    {"authorization_code"},
			"client_id":     {clientID},
			"code":          {query.Get("code")},
			"code_verifier": {verifier},
			"redirect_uri":  {redirectURI},
			"resource":      {"https://api.openai.com/v1"},
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			"https://auth.openai.com/api/accounts/oauth/token", strings.NewReader(form.Encode()))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			err = fmt.Errorf("exchanging sign-in code: %w", err)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			err = fmt.Errorf("exchanging sign-in code: %s", resp.Status)
			return
		}

		receivedAt := time.Now()
		var tokens struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			ExpiresIn    int64  `json:"expires_in"`
			Scope        string `json:"scope"`
		}
		if err = json.NewDecoder(resp.Body).Decode(&tokens); err != nil {
			err = fmt.Errorf("decoding sign-in tokens: %w", err)
			return
		}
		// Successful authentication doesn't necessarily include permission to
		// use the plan. Use the token response's grant, not the callback's scope.
		if !slices.Contains(strings.Fields(tokens.Scope), "chatgpt.tokens.use.direct") {
			err = errors.New("ChatGPT plan usage was not authorized")
			return
		}
		if err = store.Save(ctx, Credentials{
			ClientID:     clientID,
			AccessToken:  tokens.AccessToken,
			RefreshToken: tokens.RefreshToken,
			ExpiresAt:    receivedAt.Add(time.Duration(tokens.ExpiresIn) * time.Second),
		}); err != nil {
			err = fmt.Errorf("saving sign-in credentials: %w", err)
		}
	}()

	return "https://auth.openai.com/api/accounts/authorize?" + params.Encode(), result
}
