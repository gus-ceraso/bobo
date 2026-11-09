package provider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

const signInTokens = `{"access_token":"signin-secret","refresh_token":"signin-refresh","expires_in":3600,"scope":"openid offline_access chatgpt.tokens.use.direct","token_type":"Bearer"}`

func beginSignIn(t *testing.T, ctx context.Context, c *Client) (url.Values, string, <-chan error) {
	t.Helper()
	authorizationURL, done := c.SignIn(ctx)
	u, err := url.Parse(authorizationURL)
	if err != nil || u.Scheme != "https" || u.Host != "auth.openai.com" || u.Path != "/api/accounts/authorize" {
		t.Fatalf("invalid sign-in URL: %q, %v", authorizationURL, err)
	}
	query := u.Query()
	callback, err := url.Parse(query.Get("redirect_uri"))
	if err != nil || callback.Scheme != "http" || callback.Hostname() != "127.0.0.1" || callback.Port() == "" || callback.Path != "/auth/callback" {
		t.Fatalf("callback is not IPv4 loopback: %q", query.Get("redirect_uri"))
	}
	return query, callback.String(), done
}

func sendSignInCallback(t *testing.T, ctx context.Context, callback string, query url.Values, method string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, method, callback+"?"+query.Encode(), nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if _, err := io.Copy(io.Discard, res.Body); err != nil {
		t.Fatal(err)
	}
	if res.Header.Get("Cache-Control") != "no-store" && method == "GET" {
		t.Error("callback response may be cached")
	}
	return res.StatusCode
}

func awaitSignIn(t *testing.T, ctx context.Context, done <-chan error, callback string) error {
	t.Helper()
	var err error
	select {
	case result, ok := <-done:
		if !ok {
			t.Fatal("sign-in result channel closed without a result")
		}
		err = result
	case <-ctx.Done():
		// A canceled login still needs to publish its cancellation result.
		select {
		case err = <-done:
		case <-authContext(t).Done():
			t.Fatal("sign-in did not finish cleanup")
		}
	}
	select {
	case _, ok := <-done:
		if ok {
			t.Fatal("sign-in returned more than one result")
		}
	case <-authContext(t).Done():
		t.Fatal("sign-in did not close its result channel")
	}
	if callback != "" {
		u, parseErr := url.Parse(callback)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		dialer := net.Dialer{}
		conn, dialErr := dialer.DialContext(authContext(t), "tcp4", u.Host)
		if dialErr == nil {
			conn.Close()
			t.Error("sign-in completed before stopping its listener")
		}
	}
	return err
}

func TestSignInSuccess(t *testing.T) {
	ctx := authContext(t)
	store := newMemoryStore(Credential{FallbackToken: "original-fallback"})
	// SignIn must not Load; it preserves the record through Update at commit.
	c := NewClient(store)
	var logs bytes.Buffer
	c.Logger = log.New(&logs, "", 0)
	var exchangeForm url.Values
	exchanges := 0
	responseBody := &observedBody{Reader: strings.NewReader(signInTokens)}
	c.HTTPClient = &http.Client{Transport: testTransport(func(req *http.Request) (*http.Response, error) {
		exchanges++
		if req.URL.String() != tokenURL || req.Method != "POST" || req.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Fatalf("unexpected token exchange: %v", req)
		}
		if err := req.ParseForm(); err != nil {
			t.Fatal(err)
		}
		exchangeForm = req.PostForm
		// Another writer can change fallback while the browser/code exchange runs.
		if _, err := store.Update(ctx, func(current Credential) (Credential, error) {
			current.FallbackToken = "latest-fallback"
			return current, nil
		}); err != nil {
			t.Fatal(err)
		}
		res := rawResponse(200, "application/json", "")
		res.Body = responseBody
		return res, nil
	})}
	if auth, err := c.accessToken(ctx); err != nil || !auth.fallback {
		t.Fatalf("initial fallback setup failed: %v", err)
	}
	loads := store.loads.Load()
	query, callback, done := beginSignIn(t, ctx, c)
	for key, want := range map[string]string{
		"client_id": "dynamic_agent_client", "agent_name_hint": "bobo",
		"response_type": "code", "resource": resource, "code_challenge_method": "S256",
	} {
		if query.Get(key) != want {
			t.Fatalf("authorization field %s = %q", key, query.Get(key))
		}
	}
	if query.Get("state") == "" || len(query.Get("code_challenge")) != 43 || !strings.Contains(query.Get("scope"), planScope) {
		t.Fatal("missing state, challenge, or requested scope")
	}
	if !regexp.MustCompile(`^urn:uuid:[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(query.Get("ext_agent_host_id")) {
		t.Fatal("invalid registration host ID")
	}
	// Neither waiting for the browser nor rejected callbacks consume auth/store locks.
	if _, err := c.accessToken(ctx); err != nil {
		t.Fatalf("browser wait blocked authentication: %v", err)
	}
	loads++
	if status := sendSignInCallback(t, ctx, callback, url.Values{"state": {"wrong"}}, "GET"); status != 400 {
		t.Fatalf("wrong state accepted: %d", status)
	}
	if status := sendSignInCallback(t, ctx, callback, url.Values{"state": {query.Get("state")}}, "POST"); status != 405 {
		t.Fatalf("wrong method accepted: %d", status)
	}
	if status := sendSignInCallback(t, ctx, callback, url.Values{"state": {query.Get("state"), query.Get("state")}}, "GET"); status != 400 {
		t.Fatalf("duplicate state accepted: %d", status)
	}
	select {
	case err := <-done:
		t.Fatalf("invalid callback ended sign-in: %v", err)
	default:
	}
	status := sendSignInCallback(t, ctx, callback, url.Values{
		"state": {query.Get("state")}, "code": {"authorization-secret"},
		"client_id": {"new-issued-client"}, "scope": {"untrusted-callback-scope"},
	}, "GET")
	if status != 200 {
		t.Fatalf("valid callback rejected: %d", status)
	}
	if err := awaitSignIn(t, ctx, done, callback); err != nil {
		t.Fatal(err)
	}
	if exchanges != 1 || !responseBody.closed || store.loads.Load() != loads {
		t.Fatal("unexpected code exchange count, body leak, or store Load")
	}
	for key, want := range map[string]string{
		"grant_type": "authorization_code", "client_id": "new-issued-client",
		"code": "authorization-secret", "redirect_uri": callback, "resource": resource,
	} {
		if exchangeForm.Get(key) != want {
			t.Fatalf("incorrect exchange field %s", key)
		}
	}
	verifier := exchangeForm.Get("code_verifier")
	challenge := sha256.Sum256([]byte(verifier))
	if len(verifier) < 43 || len(verifier) > 128 || base64.RawURLEncoding.EncodeToString(challenge[:]) != query.Get("code_challenge") || len(exchangeForm) != 6 {
		t.Fatal("PKCE verifier does not match challenge")
	}
	credential := store.credential
	if credential.ClientID != "new-issued-client" || credential.AccessToken != "signin-secret" || credential.RefreshToken != "signin-refresh" || credential.FallbackToken != "latest-fallback" {
		t.Fatalf("incorrect committed credential: %#v", credential)
	}
	if c.fallback || c.generation != 1 {
		t.Fatal("successful sign-in did not restore ChatGPT")
	}
	auth, err := c.accessToken(ctx)
	if err != nil || auth.fallback || auth.token != "signin-secret" {
		t.Fatalf("new credential was not selected: %#v, %v", auth, err)
	}
	if strings.Count(logs.String(), "signin.completed") != 1 || !strings.Contains(logs.String(), "chatgpt.switched") {
		t.Fatalf("missing completion/switch events: %s", &logs)
	}
	assertCredentialLogs(t, logs.String(), "original-fallback", "latest-fallback", "signin-secret", "signin-refresh", "authorization-secret", query.Get("state"), verifier, callback)
}

func TestSignInCreatesMissingRecord(t *testing.T) {
	ctx := authContext(t)
	store := newMemoryStore(Credential{})
	c := NewClient(store)
	c.HTTPClient = &http.Client{Transport: testTransport(func(*http.Request) (*http.Response, error) {
		return rawResponse(200, "application/json", signInTokens), nil
	})}
	query, callback, done := beginSignIn(t, ctx, c)
	sendSignInCallback(t, ctx, callback, url.Values{
		"state": {query.Get("state")}, "code": {"authorization-secret"}, "client_id": {"new-client"},
	}, "GET")
	if err := awaitSignIn(t, ctx, done, callback); err != nil {
		t.Fatal(err)
	}
	if !store.present || store.loads.Load() != 0 || store.credential.FallbackToken != "" {
		t.Fatal("sign-in could not create a missing record without loading it")
	}
	if auth, err := c.accessToken(ctx); err != nil || auth.fallback || auth.token != "signin-secret" {
		t.Fatalf("new registration cannot authenticate: %#v %v", auth, err)
	}
}

func TestSignInSetupErrors(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "nil store", true: "canceled"}[canceled], func(t *testing.T) {
			c := NewClient(nil)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			want := ErrNoCredentials
			if canceled {
				cancel()
				want = context.Canceled
			}
			var logs bytes.Buffer
			c.Logger = log.New(&logs, "", 0)
			u, done := c.SignIn(ctx)
			if u != "" {
				t.Fatal("setup failure returned a URL")
			}
			if err := awaitSignIn(t, authContext(t), done, ""); !errors.Is(err, want) {
				t.Fatalf("SignIn error = %v, want %v", err, want)
			}
			if strings.Count(logs.String(), "signin.failed") != 1 {
				t.Fatalf("setup failure not logged: %s", &logs)
			}
		})
	}
}

func TestSignInFailures(t *testing.T) {
	for _, tt := range []struct {
		name       string
		status     int
		body       string
		callback   url.Values
		storeError bool
		commit     bool
		want       error
	}{
		{"denied", 200, signInTokens, url.Values{"error": {"denied-authorization-secret"}}, false, false, nil},
		{"missing code", 200, signInTokens, url.Values{"code": {""}}, false, false, nil},
		{"missing client", 200, signInTokens, url.Values{"client_id": {""}}, false, false, nil},
		{"registration placeholder", 200, signInTokens, url.Values{"client_id": {"dynamic_agent_client"}}, false, false, nil},
		{"duplicate code", 200, signInTokens, url.Values{"code": {"one", "two"}}, false, false, nil},
		{"duplicate client", 200, signInTokens, url.Values{"client_id": {"one", "two"}}, false, false, nil},
		{"token HTTP failure", 401, `{"error":"signin-secret"}`, nil, false, false, ErrAuthentication},
		{"malformed token", 200, `{"access_token":"signin-secret",`, nil, false, false, ErrAuthentication},
		{"missing refresh", 200, `{"access_token":"signin-secret","expires_in":3600,"scope":"` + planScope + `"}`, nil, false, false, ErrAuthentication},
		{"scope denied", 200, `{"access_token":"signin-secret","refresh_token":"signin-refresh","expires_in":3600,"scope":"openid"}`, nil, false, false, ErrPermissionDenied},
		{"update failure", 200, signInTokens, nil, true, false, nil},
		{"commit failure", 200, signInTokens, nil, true, true, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := authContext(t)
			initial := chatGPTCredential()
			store := newMemoryStore(initial)
			store.loadErr = errors.New("SignIn should not call Load")
			storeErr := errors.New("store failed with signin-secret")
			if tt.storeError {
				if tt.commit {
					store.commitErr = storeErr
				} else {
					store.updateErr = storeErr
				}
			}
			c := NewClient(store)
			c.fallback = true
			var logs bytes.Buffer
			c.Logger = log.New(&logs, "", 0)
			exchanges := 0
			c.HTTPClient = &http.Client{Transport: testTransport(func(*http.Request) (*http.Response, error) {
				exchanges++
				return rawResponse(tt.status, "application/json", tt.body), nil
			})}
			query, callback, done := beginSignIn(t, ctx, c)
			callbackQuery := url.Values{
				"state": {query.Get("state")}, "code": {"authorization-secret"},
				"client_id": {"new-client"}, "scope": {planScope}, // Must not grant permission.
			}
			for key, values := range tt.callback {
				callbackQuery[key] = values
			}
			if status := sendSignInCallback(t, ctx, callback, callbackQuery, "GET"); status != 200 {
				t.Fatalf("callback delivery failed: %d", status)
			}
			err := awaitSignIn(t, ctx, done, callback)
			if err == nil || (tt.want != nil && !errors.Is(err, tt.want)) || (tt.storeError && !errors.Is(err, storeErr)) {
				t.Fatalf("SignIn error = %v, want %v", err, tt.want)
			}
			if store.credential != initial || !c.fallback || c.generation != 0 || store.loads.Load() != 0 {
				t.Fatal("failed sign-in mutated credentials/authentication or loaded storage")
			}
			if (exchanges == 0) != (tt.callback != nil) || exchanges > 1 {
				t.Fatalf("unexpected exchange count: %d", exchanges)
			}
			if strings.Count(logs.String(), "signin.failed") != 1 || strings.Contains(logs.String(), "signin.completed") {
				t.Fatalf("incorrect terminal logging: %s", &logs)
			}
			assertCredentialLogs(t, logs.String(), "signin-secret", "signin-refresh", "authorization-secret", query.Get("state"))
		})
	}
}

func TestSignInCancellation(t *testing.T) {
	for _, stage := range []string{"browser", "exchange", "store"} {
		t.Run(stage, func(t *testing.T) {
			parent := authContext(t)
			ctx, cancel := context.WithCancel(parent)
			defer cancel()
			initial := chatGPTCredential()
			backing := newMemoryStore(initial)
			var store CredentialStore = backing
			started := make(chan struct{})
			if stage == "store" {
				// Hold the backing lock to exercise cancellation of commit waiting.
				if err := backing.lock(parent); err != nil {
					t.Fatal(err)
				}
				defer func() { <-backing.gate }()
				store = credentialStoreFuncs{load: backing.Load, update: func(ctx context.Context, update func(Credential) (Credential, error)) (Credential, error) {
					close(started)
					return backing.Update(ctx, update)
				}}
			}
			c := NewClient(store)
			var logs bytes.Buffer
			c.Logger = log.New(&logs, "", 0)
			c.HTTPClient = &http.Client{Transport: testTransport(func(req *http.Request) (*http.Response, error) {
				if stage == "exchange" {
					close(started)
					<-req.Context().Done()
					return nil, req.Context().Err()
				}
				return rawResponse(200, "application/json", signInTokens), nil
			})}
			query, callback, done := beginSignIn(t, ctx, c)
			if stage != "browser" {
				if status := sendSignInCallback(t, parent, callback, url.Values{
					"state": {query.Get("state")}, "code": {"authorization-secret"}, "client_id": {"issued-client"},
				}, "GET"); status != 200 {
					t.Fatalf("callback failed: %d", status)
				}
				select {
				case <-started:
				case <-parent.Done():
					t.Fatal("sign-in did not reach cancellation point")
				}
			}
			cancel()
			if err := awaitSignIn(t, parent, done, callback); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation error = %v", err)
			}
			if backing.credential != initial || c.fallback || c.generation != 0 {
				t.Fatal("cancellation changed credentials")
			}
			if !strings.Contains(logs.String(), "signin.failed reason=canceled") {
				t.Fatalf("cancellation was not logged: %s", &logs)
			}
			assertCredentialLogs(t, logs.String(), "signin-secret", "signin-refresh", "authorization-secret")
		})
	}
}

func TestSignInAttemptsAreIndependent(t *testing.T) {
	parent := authContext(t)
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	c := NewClient(newMemoryStore(Credential{}))
	first, callback1, done1 := beginSignIn(t, ctx, c)
	second, callback2, done2 := beginSignIn(t, ctx, c)
	for _, key := range []string{"state", "code_challenge", "ext_agent_host_id", "redirect_uri"} {
		if first.Get(key) == second.Get(key) {
			t.Fatalf("sign-in attempts reused %s", key)
		}
	}
	cancel()
	for i, done := range []<-chan error{done1, done2} {
		callback := []string{callback1, callback2}[i]
		if err := awaitSignIn(t, parent, done, callback); !errors.Is(err, context.Canceled) {
			t.Fatalf("attempt cancellation = %v", err)
		}
	}
}

func TestLateUsageFailureDoesNotOverrideSignIn(t *testing.T) {
	ctx := authContext(t)
	c := NewClient(newMemoryStore(chatGPTCredential()))
	oldAuth, err := c.accessToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c.HTTPClient = &http.Client{Transport: testTransport(func(*http.Request) (*http.Response, error) {
		return rawResponse(200, "application/json", signInTokens), nil
	})}
	query, callback, done := beginSignIn(t, ctx, c)
	sendSignInCallback(t, ctx, callback, url.Values{
		"state": {query.Get("state")}, "code": {"authorization-secret"}, "client_id": {"new-client"},
	}, "GET")
	if err := awaitSignIn(t, ctx, done, callback); err != nil {
		t.Fatal(err)
	}
	retry, err := c.usageFallback(ctx, oldAuth)
	if err != nil || !retry || c.fallback {
		t.Fatalf("late failure overrode new registration: retry=%t err=%v", retry, err)
	}
	auth, err := c.accessToken(ctx)
	if err != nil || auth.token != "signin-secret" || auth.fallback {
		t.Fatalf("retry did not select new registration: %#v %v", auth, err)
	}
}
