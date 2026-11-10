package provider

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SignIn returns a ChatGPT authorization URL and a buffered completion channel.
// The caller opens the URL; SignIn never opens a browser.
//
// done receives one error (nil on success), then closes. All failures arrive
// through done; setup failures return an empty URL. Success means credentials
// are saved, ChatGPT is preferred again, and the temporary loopback listener has
// stopped. Sign-in preserves the store's latest fallback token.
//
// Keep ctx alive until completion. Use a deadline to bound abandoned attempts;
// closing the browser does not cancel sign-in. Cancellation stops the listener,
// token exchange, and storage operation.
//
// Each call creates a registration without revoking earlier ones. Deleting local
// credentials does not revoke registrations. This authorizes API use, not an
// application user identity.
func (c *Client) SignIn(ctx context.Context) (authorizationURL string, done <-chan error) {
	result := make(chan error, 1)
	fail := func(err error) (string, <-chan error) {
		c.logCredential("signin.failed", "setup_error")
		result <- err
		close(result)
		return "", result
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if c.store == nil {
		return fail(ErrNoCredentials)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return fail(fmt.Errorf("starting sign-in listener: %w", err))
	}

	var id [16]byte
	rand.Read(id[:])
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	hostID := fmt.Sprintf("urn:uuid:%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:])
	state := rand.Text()
	var random [32]byte
	rand.Read(random[:])
	verifier := base64.RawURLEncoding.EncodeToString(random[:])
	challenge := sha256.Sum256([]byte(verifier))
	redirectURI := "http://" + listener.Addr().String() + "/auth/callback"
	params := url.Values{
		"client_id":             {"dynamic_agent_client"},
		"agent_name_hint":       {"bobo"},
		"ext_agent_host_id":     {hostID},
		"redirect_uri":          {redirectURI},
		"response_type":         {"code"},
		"scope":                 {"openid profile email offline_access resource.invoke " + planScope},
		"resource":              {resource},
		"state":                 {state},
		"code_challenge_method": {"S256"},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(challenge[:])},
	}

	callback := make(chan url.Values, 1)
	var once sync.Once
	mux := http.NewServeMux()
	mux.HandleFunc("GET /auth/callback", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || len(query["state"]) != 1 || query.Get("state") != state {
			http.Error(w, "Invalid sign-in state.", http.StatusBadRequest)
			return
		}
		accepted := false
		once.Do(func() { accepted = true })
		if !accepted {
			http.Error(w, "Sign-in callback already received.", http.StatusConflict)
			return
		}
		// Complete the response framing and flush before notifying the flow.
		// Connection: close prevents net/http from draining an unread request
		// body before writing. No global graceful shutdown is needed afterward.
		const message = "Sign-in received. Return to the application for the result.\n"
		w.Header().Set("Connection", "close")
		w.Header().Set("Content-Length", strconv.Itoa(len(message)))
		_, _ = io.WriteString(w, message)
		_ = http.NewResponseController(w).Flush()
		// A disconnected browser must not discard an otherwise valid approval.
		callback <- query
	})
	var connections sync.WaitGroup
	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
		IdleTimeout:       5 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ConnState: func(_ net.Conn, state http.ConnState) {
			switch state {
			case http.StateNew:
				connections.Add(1)
			case http.StateClosed, http.StateHijacked:
				connections.Done()
			}
		},
		// Server diagnostics can include callback details. Report terminal
		// failures through done and credential logs, not the default stderr log.
		ErrorLog: log.New(io.Discard, "", 0),
	}
	server.SetKeepAlivesEnabled(false)

	go func() {
		var err error
		reason := "authorization_error"
		// Completion follows cleanup, including cancellation's server closure.
		defer func() {
			if err == nil {
				c.logCredential("signin.completed", "saved")
			} else {
				if ctx.Err() != nil {
					err = ctx.Err()
					reason = "canceled"
				}
				c.logCredential("signin.failed", reason)
			}
			result <- err
			close(result)
		}()
		serveErr := make(chan error, 1)
		serveDone := make(chan struct{})
		go func() {
			defer close(serveDone)
			serveErr <- server.Serve(listener)
		}()
		var closeOnce sync.Once
		closeServer := func() {
			closeOnce.Do(func() {
				_ = server.Close()
				_ = listener.Close() // Also covers cancellation before Serve starts.
				<-serveDone
				// Serve has stopped adding connections. Close unblocks request
				// reads/writes; join their goroutines, not just the accept loop.
				connections.Wait()
			})
		}
		cancelClosed := make(chan struct{})
		stop := context.AfterFunc(ctx, func() {
			defer close(cancelClosed)
			closeServer()
		})
		defer func() {
			if !stop() {
				<-cancelClosed
			}
			closeServer()
		}()

		var query url.Values
		select {
		case query = <-callback:
			// The accepted response has already been flushed. Force-close all
			// connections instead of waiting on unrelated clients' bodies.
			closeServer()
			if err = ctx.Err(); err != nil {
				return
			}
		case err = <-serveErr:
			return
		case <-ctx.Done():
			err = ctx.Err()
			return
		}
		if query.Get("error") != "" {
			// Do not echo untrusted callback text, which may contain secrets.
			err = errors.New("ChatGPT sign-in was denied")
			return
		}
		clientID := query.Get("client_id")
		if len(query["code"]) != 1 || query.Get("code") == "" ||
			len(query["client_id"]) != 1 || clientID == "" || clientID == "dynamic_agent_client" {
			err = errors.New("sign-in callback is missing a unique authorization code or issued client ID")
			return
		}
		reason = "token_error"
		var tokens tokenResponse
		tokens, err = c.exchangeToken(ctx, url.Values{
			"grant_type":    {"authorization_code"},
			"client_id":     {clientID},
			"code":          {query.Get("code")},
			"code_verifier": {verifier},
			"redirect_uri":  {redirectURI},
			"resource":      {resource},
		})
		if err != nil {
			return
		}
		if tokens.RefreshToken == "" {
			err = fmt.Errorf("%w: sign-in response has no refresh token", ErrAuthentication)
			return
		}
		if !slices.Contains(strings.Fields(tokens.Scope), planScope) {
			err = fmt.Errorf("%w: ChatGPT plan usage was not authorized", ErrPermissionDenied)
			return
		}
		reason = "store_error"
		if err = c.lockAuth(ctx); err != nil {
			return
		}
		defer c.unlockAuth()
		var credential Credential
		credential, err = c.store.Update(ctx, func(current Credential) (Credential, error) {
			if err := ctx.Err(); err != nil {
				return Credential{}, err
			}
			return Credential{
				ClientID:      clientID,
				AccessToken:   tokens.AccessToken,
				RefreshToken:  tokens.RefreshToken,
				ExpiresAt:     tokens.expiresAt,
				FallbackToken: current.FallbackToken,
			}, nil
		})
		if err != nil {
			err = fmt.Errorf("saving sign-in credential: %w", err)
			return
		}
		if c.fallback {
			c.logCredential("chatgpt.switched", "sign_in")
		}
		c.fallback = false
		c.generation++
		c.observed = &credential
	}()

	return "https://auth.openai.com/api/accounts/authorize?" + params.Encode(), result
}
