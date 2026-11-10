package provider

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

const (
	tokenURL     = "https://auth.openai.com/api/accounts/oauth/token"
	resource     = "https://api.openai.com/v1"
	planScope    = "chatgpt.tokens.use.direct"
	refreshAhead = 30 * time.Second
)

// requestAuth remembers which source and registration authorized an in-flight
// request. ClientID survives token refreshes but changes on a new registration;
// generation also protects SignIn on this client, even if an ID were reused.
type requestAuth struct {
	token      string
	clientID   string
	fallback   bool
	generation uint64
}

// refreshFailure marks an error from the OAuth exchange inside Update. Store
// errors can match the same context sentinels, so their provenance must not be
// inferred from errors.Is alone, especially after a successful token rotation.
type refreshFailure struct{ err error }

func (e *refreshFailure) Error() string { return e.err.Error() }
func (e *refreshFailure) Unwrap() error { return e.err }

func (c *Client) lockAuth(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case c.authGate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-c.authGate
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) unlockAuth() { <-c.authGate }

func (c *Client) logCredential(event, reason string) {
	if c.Logger != nil {
		// Both arguments are internal constants, never errors, tokens, or bodies.
		c.Logger.Printf("event=credential.%s reason=%s", event, reason)
	}
}

func (c *Client) observeCredential(credential Credential) {
	if c.observed == nil || *c.observed != credential {
		c.logCredential("load.completed", "loaded")
		c.observed = &credential
	}
}

func (c *Client) loadCredential(ctx context.Context) (Credential, error) {
	credential, err := c.store.Load(ctx)
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		reason := "store_error"
		if errors.Is(err, ErrNoCredentials) {
			reason = "no_credentials"
		} else if ctx.Err() != nil {
			reason = "canceled"
		}
		c.logCredential("load.failed", reason)
		return Credential{}, err
	}
	c.observeCredential(credential)
	return credential, nil
}

func hasChatGPT(credential Credential) bool {
	return credential.AccessToken != "" || credential.RefreshToken != "" ||
		credential.ClientID != "" || !credential.ExpiresAt.IsZero()
}

func freshCredential(credential Credential) bool {
	return credential.AccessToken != "" && time.Now().Add(refreshAhead).Before(credential.ExpiresAt)
}

// accessToken rereads storage for each request attempt, but logs a successful load
// only when the record changes. Update rechecks expiry under the store's lock, so
// independent clients do not refresh the same rotating token concurrently.
func (c *Client) accessToken(ctx context.Context) (requestAuth, error) {
	if err := ctx.Err(); err != nil {
		return requestAuth{}, err
	}
	if c.store == nil {
		c.logCredential("load.failed", "no_credentials")
		return requestAuth{}, ErrNoCredentials
	}
	if err := c.lockAuth(ctx); err != nil {
		return requestAuth{}, err
	}
	defer c.unlockAuth()

	credential, err := c.loadCredential(ctx)
	if err != nil {
		return requestAuth{}, err
	}
	if !c.fallback && hasChatGPT(credential) && !freshCredential(credential) {
		attempted := false
		latest := credential
		credential, err = c.store.Update(ctx, func(current Credential) (Credential, error) {
			// Use the latest record, including its fallback token, on failure.
			latest = current
			if err := ctx.Err(); err != nil {
				return Credential{}, err
			}
			if freshCredential(current) || !hasChatGPT(current) {
				return current, nil
			}
			attempted = true
			next, err := c.refreshCredential(ctx, current)
			if err != nil {
				return Credential{}, &refreshFailure{err: err}
			}
			return next, nil
		})
		if err != nil {
			if ctx.Err() != nil {
				c.logCredential("refresh.failed", "canceled")
				return requestAuth{}, ctx.Err()
			}
			var failure *refreshFailure
			if !errors.As(err, &failure) {
				c.logCredential("update.failed", "store_error")
				return requestAuth{}, fmt.Errorf("updating credential: %w", err)
			}
			// A detached cancellation still propagates. A deadline from the
			// OAuth request (including HTTPClient.Timeout), however, is a
			// refresh failure when the caller's context above remains active.
			if errors.Is(failure, context.Canceled) {
				c.logCredential("refresh.failed", "canceled")
				return requestAuth{}, err
			}
			c.logCredential("refresh.failed", "refresh_error")
			credential = latest
			if !c.switchFallback(credential, "refresh_failed") {
				return requestAuth{}, fmt.Errorf("refreshing credential: %w", err)
			}
		} else {
			if attempted {
				c.logCredential("refresh.completed", "renewed")
				c.observed = &credential
			} else {
				c.observeCredential(credential)
			}
		}
	}
	if !c.fallback && !hasChatGPT(credential) {
		c.switchFallback(credential, "no_chatgpt_credential")
	}
	auth := requestAuth{clientID: credential.ClientID, generation: c.generation, fallback: c.fallback}
	if c.fallback {
		auth.token = credential.FallbackToken
	} else {
		auth.token = credential.AccessToken
	}
	if auth.token == "" {
		c.logCredential("selection.failed", "no_token")
		return requestAuth{}, ErrNoCredentials
	}
	return auth, nil
}

// switchFallback requires authGate. The chosen source is never persisted.
func (c *Client) switchFallback(credential Credential, reason string) bool {
	if credential.FallbackToken == "" {
		return false
	}
	if !c.fallback {
		c.fallback = true
		c.logCredential("fallback.switched", reason)
	}
	return true
}

// usageFallback returns whether the failed request should be retried. Fallback
// requests never enter this path, so an API usage-limit error cannot loop.
func (c *Client) usageFallback(ctx context.Context, auth requestAuth) (bool, error) {
	if auth.fallback {
		return false, nil
	}
	if err := c.lockAuth(ctx); err != nil {
		return false, err
	}
	defer c.unlockAuth()
	if c.generation != auth.generation || c.fallback {
		return true, nil
	}
	credential, err := c.loadCredential(ctx)
	if err != nil {
		return false, err
	}
	if credential.ClientID != auth.clientID {
		// Another client/process replaced the registration. Retry with its
		// credential rather than applying the old registration's usage limit.
		return true, nil
	}
	return c.switchFallback(credential, "usage_limit_reached"), nil
}

func (c *Client) refreshCredential(ctx context.Context, credential Credential) (Credential, error) {
	if credential.ClientID == "" || credential.ClientID == "dynamic_agent_client" || credential.RefreshToken == "" {
		return Credential{}, fmt.Errorf("%w: credential cannot be refreshed", ErrAuthentication)
	}
	tokens, err := c.exchangeToken(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {credential.ClientID},
		"refresh_token": {credential.RefreshToken},
		"resource":      {resource},
	})
	if err != nil {
		return Credential{}, err
	}
	if tokens.Scope != "" && !slices.Contains(strings.Fields(tokens.Scope), planScope) {
		return Credential{}, fmt.Errorf("%w: ChatGPT plan usage was not authorized", ErrPermissionDenied)
	}
	credential.AccessToken = tokens.AccessToken
	if tokens.RefreshToken != "" {
		credential.RefreshToken = tokens.RefreshToken
	}
	credential.ExpiresAt = tokens.expiresAt
	return credential, nil
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
	TokenType    string `json:"token_type"`
	expiresAt    time.Time
}

func (c *Client) exchangeToken(ctx context.Context, form url.Values) (tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := c.HTTPClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return tokenResponse{}, ctx.Err()
		}
		return tokenResponse{}, fmt.Errorf("requesting OAuth token: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return tokenResponse{}, fmt.Errorf("%w: token endpoint HTTP %d", ErrAuthentication, res.StatusCode)
	}
	receivedAt := time.Now()
	body, err := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if err != nil {
		return tokenResponse{}, fmt.Errorf("reading OAuth token response: %w", err)
	}
	var tokens tokenResponse
	// Do not put response bodies or decoder diagnostics in errors or logs: OAuth
	// responses contain secrets, even when malformed or returned with an error.
	if len(body) > 1<<20 || json.Unmarshal(body, &tokens) != nil ||
		tokens.AccessToken == "" || strings.ContainsAny(tokens.AccessToken, "\r\n\t ") ||
		tokens.ExpiresIn <= 0 || tokens.ExpiresIn > int64((1<<63-1)/time.Second) ||
		(tokens.TokenType != "" && !strings.EqualFold(tokens.TokenType, "Bearer")) {
		return tokenResponse{}, fmt.Errorf("%w: invalid OAuth token response", ErrAuthentication)
	}
	if err := ctx.Err(); err != nil {
		return tokenResponse{}, err
	}
	tokens.expiresAt = receivedAt.Add(time.Duration(tokens.ExpiresIn) * time.Second)
	return tokens, nil
}
