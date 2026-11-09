package provider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func chatGPTCredential() Credential {
	return Credential{
		ClientID:      "issued-client",
		AccessToken:   "chatgpt-secret",
		RefreshToken:  "refresh-secret",
		ExpiresAt:     time.Now().Add(time.Hour),
		FallbackToken: "api-secret",
	}
}

func authContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func assertCredentialLogs(t *testing.T, logs string, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if secret != "" && strings.Contains(logs, secret) {
			t.Errorf("credential log exposed a secret: %q", logs)
		}
	}
	if strings.Contains(logs, ".started") || strings.Contains(logs, ".start ") {
		t.Errorf("logged operation start: %q", logs)
	}
	for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
		if line == "" {
			continue
		}
		if !strings.Contains(line, ".completed ") && !strings.Contains(line, ".failed ") && !strings.Contains(line, ".switched ") {
			t.Errorf("unexpected credential event: %q", line)
		}
	}
}

func TestCredentialSelection(t *testing.T) {
	// Environment tokens are intentionally irrelevant to this package.
	t.Setenv("OPENAI_API_KEY", "environment-secret")
	for _, tt := range []struct {
		name       string
		credential Credential
		nilStore   bool
		want       string
		fallback   bool
		wantErr    error
	}{
		{"nil store", Credential{}, true, "", false, ErrNoCredentials},
		{"missing record", Credential{}, false, "", false, ErrNoCredentials},
		{"API only", Credential{FallbackToken: "api-secret"}, false, "api-secret", true, nil},
		{"ChatGPT preferred", chatGPTCredential(), false, "chatgpt-secret", false, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var store CredentialStore = newMemoryStore(tt.credential)
			if tt.nilStore {
				store = nil
			}
			c := NewClient(store)
			c.HTTPClient = &http.Client{Transport: testTransport(func(*http.Request) (*http.Response, error) {
				t.Fatal("unexpected network request")
				return nil, nil
			})}
			var logs bytes.Buffer
			c.Logger = log.New(&logs, "", 0)
			for range 2 {
				auth, err := c.accessToken(t.Context())
				if !errors.Is(err, tt.wantErr) || auth.token != tt.want || auth.fallback != tt.fallback {
					t.Fatalf("accessToken() = %#v, %v", auth, err)
				}
			}
			if tt.wantErr == nil && strings.Count(logs.String(), "load.completed") != 1 {
				t.Fatalf("unchanged loads logged repeatedly: %s", &logs)
			}
			assertCredentialLogs(t, logs.String(), "environment-secret", "chatgpt-secret", "refresh-secret", "api-secret")
		})
	}
}

func TestCredentialLoadErrors(t *testing.T) {
	want := errors.New("store failed with api-secret")
	store := newMemoryStore(chatGPTCredential())
	store.loadErr = want
	c := NewClient(store)
	var logs bytes.Buffer
	c.Logger = log.New(&logs, "", 0)
	if _, err := c.accessToken(t.Context()); !errors.Is(err, want) || c.fallback {
		t.Fatalf("storage failure triggered fallback: %v", err)
	}
	assertCredentialLogs(t, logs.String(), "api-secret")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	loads := store.loads.Load()
	if _, err := c.accessToken(ctx); !errors.Is(err, context.Canceled) || store.loads.Load() != loads {
		t.Fatalf("canceled operation touched store: %v", err)
	}
}

func TestCredentialRefresh(t *testing.T) {
	for _, rotated := range []bool{false, true} {
		t.Run(fmt.Sprint("rotated=", rotated), func(t *testing.T) {
			initial := chatGPTCredential()
			initial.ExpiresAt = time.Now().Add(10 * time.Second) // Refresh before expiry.
			store := newMemoryStore(initial)
			c := NewClient(store)
			var logs bytes.Buffer
			c.Logger = log.New(&logs, "", 0)
			requests := 0
			c.HTTPClient = &http.Client{Transport: testTransport(func(req *http.Request) (*http.Response, error) {
				requests++
				if req.URL.String() != tokenURL || req.Method != "POST" || req.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
					t.Fatalf("unexpected token request: %v", req)
				}
				if err := req.ParseForm(); err != nil {
					t.Fatal(err)
				}
				for key, want := range map[string]string{
					"grant_type": "refresh_token", "client_id": initial.ClientID,
					"refresh_token": initial.RefreshToken, "resource": resource,
				} {
					if req.PostForm.Get(key) != want {
						t.Fatalf("form field %s incorrect", key)
					}
				}
				if len(req.PostForm) != 4 || req.Header.Get("Authorization") != "" {
					t.Fatal("refresh sent extra grants or authorization")
				}
				body := `{"access_token":"renewed-secret","expires_in":3600,"token_type":"Bearer"}`
				if rotated {
					body = `{"access_token":"renewed-secret","refresh_token":"rotated-secret","expires_in":3600,"scope":"` + planScope + `"}`
				}
				return rawResponse(200, "application/json", body), nil
			})}
			before := time.Now()
			for range 2 {
				auth, err := c.accessToken(t.Context())
				if err != nil || auth.token != "renewed-secret" || auth.fallback {
					t.Fatalf("accessToken() = %#v, %v", auth, err)
				}
			}
			got, err := store.Load(t.Context())
			if err != nil || requests != 1 || store.updates.Load() != 1 || got.ClientID != initial.ClientID || got.FallbackToken != initial.FallbackToken {
				t.Fatalf("refresh did not preserve record: %#v, %v; requests=%d", got, err, requests)
			}
			wantRefresh := initial.RefreshToken
			if rotated {
				wantRefresh = "rotated-secret"
			}
			if got.RefreshToken != wantRefresh || got.ExpiresAt.Before(before.Add(time.Hour)) || got.ExpiresAt.After(time.Now().Add(time.Hour)) {
				t.Fatalf("bad refresh token or expiry: %#v", got)
			}
			if strings.Count(logs.String(), "refresh.completed") != 1 || strings.Contains(logs.String(), "fallback.switched") {
				t.Fatalf("unexpected logs: %s", &logs)
			}
			assertCredentialLogs(t, logs.String(), initial.AccessToken, initial.RefreshToken, initial.FallbackToken, "renewed-secret", "rotated-secret")
		})
	}
}

func TestRefreshFailures(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
		body   string
		is     error
	}{
		{"HTTP failure", 401, `{"error":"refresh-secret api-secret"}`, ErrAuthentication},
		{"malformed", 200, `{"access_token":"leaked-secret",`, ErrAuthentication},
		{"missing access", 200, `{"expires_in":3600}`, ErrAuthentication},
		{"empty access", 200, `{"access_token":"","expires_in":3600}`, ErrAuthentication},
		{"invalid bearer", 200, `{"access_token":"bad\r\nsecret","expires_in":3600}`, ErrAuthentication},
		{"missing expiry", 200, `{"access_token":"leaked-secret"}`, ErrAuthentication},
		{"negative expiry", 200, `{"access_token":"leaked-secret","expires_in":-1}`, ErrAuthentication},
		{"overflow expiry", 200, `{"access_token":"leaked-secret","expires_in":9223372036854775807}`, ErrAuthentication},
		{"duplicate fields", 200, `{"access_token":"leaked-secret","access_token":"other","expires_in":3600}`, ErrAuthentication},
		{"wrong token type", 200, `{"access_token":"leaked-secret","expires_in":3600,"token_type":"MAC"}`, ErrAuthentication},
		{"trailing JSON", 200, `{"access_token":"leaked-secret","expires_in":3600} {}`, ErrAuthentication},
		{"oversize", 200, strings.Repeat("leaked-secret", (1<<20)/13+1), ErrAuthentication},
		{"scope denied", 200, `{"access_token":"leaked-secret","expires_in":3600,"scope":"openid"}`, ErrPermissionDenied},
	} {
		for _, fallback := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/fallback=%t", tt.name, fallback), func(t *testing.T) {
				initial := chatGPTCredential()
				initial.ExpiresAt = time.Now().Add(-time.Hour)
				if !fallback {
					initial.FallbackToken = ""
				}
				store := newMemoryStore(initial)
				c := NewClient(store)
				var logs bytes.Buffer
				c.Logger = log.New(&logs, "", 0)
				requests := 0
				c.HTTPClient = &http.Client{Transport: testTransport(func(*http.Request) (*http.Response, error) {
					requests++
					return rawResponse(tt.status, "application/json", tt.body), nil
				})}
				auth, err := c.accessToken(t.Context())
				if fallback {
					if err != nil || auth.token != initial.FallbackToken || !auth.fallback {
						t.Fatalf("fallback failed: %#v, %v", auth, err)
					}
					if _, err := c.accessToken(t.Context()); err != nil || requests != 1 {
						t.Fatalf("fallback was not sticky: %v; requests=%d", err, requests)
					}
				} else if !errors.Is(err, tt.is) || c.fallback {
					t.Fatalf("refresh error = %v, want %v", err, tt.is)
				}
				if store.credential != initial || requests != 1 || !strings.Contains(logs.String(), "refresh.failed") {
					t.Fatal("failed refresh mutated record, retried, or was not logged")
				}
				assertCredentialLogs(t, logs.String(), "refresh-secret", "api-secret", "leaked-secret")
			})
		}
	}
}

func TestPromptFallsBackAfterRefreshFailure(t *testing.T) {
	credential := chatGPTCredential()
	credential.ExpiresAt = time.Time{}
	store := newMemoryStore(credential)
	c := NewClient(store)
	refreshes, responses := 0, 0
	c.HTTPClient = &http.Client{Transport: testTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() == tokenURL {
			refreshes++
			return rawResponse(401, "application/json", `{"error":"invalid_grant"}`), nil
		}
		responses++
		if req.Header.Get("Authorization") != "Bearer "+credential.FallbackToken {
			t.Fatal("failed renewal did not select API authentication")
		}
		return completedResponse([]map[string]any{{"type": "message", "content": "answer"}}), nil
	})}
	s := NewSession("model", "low", "", nil)
	for _, prompt := range []string{"first", "second"} {
		if answer, err := c.Prompt(t.Context(), s, prompt); err != nil || answer != "answer" {
			t.Fatalf("Prompt = %q, %v", answer, err)
		}
	}
	if refreshes != 1 || responses != 2 || len(s.Events) != 4 || store.credential != credential {
		t.Fatalf("fallback retried renewal, changed history, or persisted the switch: refreshes=%d responses=%d", refreshes, responses)
	}
}

func TestRefreshTransportFailureAndCancellation(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprint("canceled=", canceled), func(t *testing.T) {
			initial := chatGPTCredential()
			initial.ExpiresAt = time.Time{}
			store := newMemoryStore(initial)
			c := NewClient(store)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			c.HTTPClient = &http.Client{Transport: testTransport(func(req *http.Request) (*http.Response, error) {
				if canceled {
					cancel()
					return nil, req.Context().Err()
				}
				return nil, errors.New("network failed with refresh-secret")
			})}
			var logs bytes.Buffer
			c.Logger = log.New(&logs, "", 0)
			auth, err := c.accessToken(ctx)
			if canceled {
				if !errors.Is(err, context.Canceled) || c.fallback {
					t.Fatalf("cancellation switched fallback: %v", err)
				}
			} else if err != nil || !auth.fallback || auth.token != initial.FallbackToken {
				t.Fatalf("transport failure did not fall back: %#v, %v", auth, err)
			}
			assertCredentialLogs(t, logs.String(), "refresh-secret")
		})
	}
}

func TestRefreshStoreFailures(t *testing.T) {
	for _, commit := range []bool{false, true} {
		t.Run(fmt.Sprint("commit=", commit), func(t *testing.T) {
			initial := chatGPTCredential()
			initial.ExpiresAt = time.Time{}
			store := newMemoryStore(initial)
			want := errors.New("storage failed with api-secret")
			if commit {
				store.commitErr = want
			} else {
				store.updateErr = want
			}
			c := NewClient(store)
			requests := 0
			c.HTTPClient = &http.Client{Transport: testTransport(func(*http.Request) (*http.Response, error) {
				requests++
				return rawResponse(200, "application/json", `{"access_token":"new-secret","refresh_token":"rotated-secret","expires_in":3600}`), nil
			})}
			var logs bytes.Buffer
			c.Logger = log.New(&logs, "", 0)
			_, err := c.accessToken(t.Context())
			if !errors.Is(err, want) || c.fallback || store.credential != initial {
				t.Fatalf("store error was masked or record mutated: %v", err)
			}
			if (requests == 1) != commit || strings.Contains(logs.String(), "refresh.completed") {
				t.Fatalf("unexpected refresh/persistence result: requests=%d logs=%s", requests, &logs)
			}
			assertCredentialLogs(t, logs.String(), "api-secret", "new-secret", "rotated-secret")
		})
	}
}

func TestIndependentClientsRefreshOnce(t *testing.T) {
	ctx := authContext(t)
	initial := chatGPTCredential()
	initial.ExpiresAt = time.Time{}
	backing := newMemoryStore(initial)
	loaded := make(chan struct{})
	var loads atomic.Int32
	// Force both clients to read the old record before either enters Update.
	store := credentialStoreFuncs{
		load: func(ctx context.Context) (Credential, error) {
			credential, err := backing.Load(ctx)
			if loads.Add(1) == 2 {
				close(loaded)
			}
			select {
			case <-loaded:
			case <-ctx.Done():
				return Credential{}, ctx.Err()
			}
			return credential, err
		},
		update: backing.Update,
	}
	var exchanges atomic.Int32
	transport := testTransport(func(*http.Request) (*http.Response, error) {
		exchanges.Add(1)
		return rawResponse(200, "application/json", `{"access_token":"renewed","refresh_token":"rotated","expires_in":3600}`), nil
	})
	results := make(chan error, 2)
	for range 2 {
		c := NewClient(store)
		c.HTTPClient = &http.Client{Transport: transport}
		go func() {
			auth, err := c.accessToken(ctx)
			if err == nil && (auth.token != "renewed" || auth.fallback) {
				err = errors.New("client did not use renewed token")
			}
			results <- err
		}()
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if exchanges.Load() != 1 || backing.updates.Load() != 2 || backing.credential.RefreshToken != "rotated" {
		t.Fatalf("double refresh: exchanges=%d updates=%d", exchanges.Load(), backing.updates.Load())
	}
}

func TestAuthGateCancellation(t *testing.T) {
	ctx := authContext(t)
	initial := chatGPTCredential()
	initial.ExpiresAt = time.Time{}
	c := NewClient(newMemoryStore(initial))
	started, release := make(chan struct{}), make(chan struct{})
	c.HTTPClient = &http.Client{Transport: testTransport(func(req *http.Request) (*http.Response, error) {
		close(started)
		select {
		case <-release:
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
		return rawResponse(200, "application/json", `{"access_token":"renewed","expires_in":3600}`), nil
	})}
	finished := make(chan error, 1)
	go func() { _, err := c.accessToken(ctx); finished <- err }()
	<-started
	waiting, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := c.accessToken(waiting); !errors.Is(err, context.Canceled) {
		t.Fatalf("gate wait ignored cancellation: %v", err)
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatalf("waiting caller canceled active refresh: %v", err)
	}
}

func TestUsageFallbackPreservesCompletedTurn(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint("stream=", stream), func(t *testing.T) {
			initial := chatGPTCredential()
			store := newMemoryStore(initial)
			c := NewClient(store)
			calls := 0
			f := callbackFunction{call: func(context.Context, map[string]any) []Part {
				calls++
				return []Part{{ContentType: "text/plain", Content: []byte("side effect completed")}}
			}}
			requests := 0
			var failedBody []byte
			var failedResponse *observedBody
			c.HTTPClient = &http.Client{Transport: testTransport(func(req *http.Request) (*http.Response, error) {
				requests++
				body, _ := io.ReadAll(req.Body)
				wantToken := initial.AccessToken
				if requests >= 3 {
					wantToken = initial.FallbackToken
				}
				if req.Header.Get("Authorization") != "Bearer "+wantToken {
					t.Fatalf("wrong source on request %d", requests)
				}
				switch requests {
				case 1:
					return completedResponse([]map[string]any{{"type": "function_call", "name": "inspect", "call_id": "side-effect", "arguments": "{}"}}), nil
				case 2:
					failedBody = body
					res := rawResponse(429, "application/json", `{"error":{"code":"usage_limit_reached"}}`)
					if stream {
						res = streamResponse(`{"type":"response.failed","response":{"error":{"code":"subscription_sharing_usage_limit_exceeded"}}}`)
					}
					failedResponse = &observedBody{Reader: res.Body}
					res.Body = failedResponse
					return res, nil
				case 3:
					if !bytes.Equal(body, failedBody) || calls != 1 || !failedResponse.closed {
						t.Fatal("fallback changed input, repeated tool, or leaked response")
					}
					return completedResponse([]map[string]any{{"type": "message", "content": "done"}}), nil
				case 4:
					return completedResponse([]map[string]any{{"type": "message", "content": "next"}}), nil
				default:
					return nil, errors.New("unexpected extra request")
				}
			})}
			s := NewSession("model", "low", "", []Function{f})
			got, err := c.Prompt(t.Context(), s, "perform side effect")
			if err != nil || got != "done" || calls != 1 || len(s.Events) != 4 {
				t.Fatalf("Prompt = %q, %v; calls=%d events=%#v", got, err, calls, s.Events)
			}
			got, err = c.Prompt(t.Context(), s, "next turn")
			if err != nil || got != "next" || requests != 4 || calls != 1 || store.credential != initial {
				t.Fatalf("sticky fallback changed record or repeated calls: %q, %v", got, err)
			}
			freshClient := NewClient(store)
			auth, err := freshClient.accessToken(t.Context())
			if err != nil || auth.fallback || auth.token != initial.AccessToken {
				t.Fatal("fallback choice leaked into a new client")
			}
		})
	}
}

func TestUsageFallbackTerminalErrors(t *testing.T) {
	for _, tt := range []struct {
		name     string
		code     string
		fallback bool
		apiOnly  bool
		want     error
		requests int
	}{
		{"no fallback", "usage_limit_reached", false, false, ErrUsageLimitReached, 1},
		{"fallback also limited", "usage_limit_reached", true, false, ErrUsageLimitReached, 2},
		{"API only limited", "usage_limit_reached", true, true, ErrUsageLimitReached, 1},
		{"quota", "insufficient_quota", true, false, ErrQuotaExceeded, 1},
		{"authentication", "invalid_token", true, false, ErrAuthentication, 1},
		{"permission", "permission_denied", true, false, ErrPermissionDenied, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			credential := chatGPTCredential()
			if !tt.fallback {
				credential.FallbackToken = ""
			}
			if tt.apiOnly {
				credential = Credential{FallbackToken: credential.FallbackToken}
			}
			c := NewClient(newMemoryStore(credential))
			requests := 0
			c.HTTPClient = &http.Client{Transport: testTransport(func(*http.Request) (*http.Response, error) {
				requests++
				return rawResponse(429, "application/json", `{"error":{"code":"`+tt.code+`"}}`), nil
			})}
			_, err := c.Prompt(t.Context(), NewSession("model", "low", "", nil), "hello")
			if !errors.Is(err, tt.want) || requests != tt.requests {
				t.Fatalf("error=%v requests=%d", err, requests)
			}
		})
	}
}

func TestChatGPTRateLimitDoesNotSwitch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		credential := chatGPTCredential()
		c := NewClient(newMemoryStore(credential))
		requests := 0
		c.HTTPClient = &http.Client{Transport: testTransport(func(req *http.Request) (*http.Response, error) {
			requests++
			if req.Header.Get("Authorization") != "Bearer "+credential.AccessToken {
				t.Fatal("ordinary rate limit changed source")
			}
			if requests == 1 {
				return rawResponse(429, "application/json", `{"error":{"code":"rate_limit_exceeded"}}`), nil
			}
			return completedResponse([]map[string]any{}), nil
		})}
		if _, err := c.responses(t.Context(), NewSession("model", "low", "", nil)); err != nil || requests != 2 || c.fallback {
			t.Fatalf("rate-limit handling failed: %v", err)
		}
	})
}

func TestUsageFallbackStoreFailure(t *testing.T) {
	initial := chatGPTCredential()
	store := newMemoryStore(initial)
	want := errors.New("cannot reload store")
	c := NewClient(store)
	c.HTTPClient = &http.Client{Transport: testTransport(func(*http.Request) (*http.Response, error) {
		store.loadErr = want
		return rawResponse(429, "application/json", `{"error":{"code":"usage_limit_reached"}}`), nil
	})}
	_, err := c.responses(t.Context(), NewSession("model", "low", "", nil))
	if !errors.Is(err, want) || c.fallback || !reflect.DeepEqual(store.credential, initial) {
		t.Fatalf("store failure masked during switch: %v", err)
	}
}
