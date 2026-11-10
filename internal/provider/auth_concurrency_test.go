package provider

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestAuthWaitingCancellation(t *testing.T) {
	for _, stage := range []string{"auth", "load", "usage switch"} {
		t.Run(stage, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				store := newMemoryStore(chatGPTCredential())
				c := NewClient(store)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if stage == "load" {
					store.gate <- struct{}{}
					defer func() { <-store.gate }()
				} else {
					c.authGate <- struct{}{}
					defer c.unlockAuth()
				}
				finished := make(chan error, 1)
				go func() {
					var err error
					if stage == "usage switch" {
						_, err = c.usageFallback(ctx, requestAuth{})
					} else {
						_, err = c.accessToken(ctx)
					}
					finished <- err
				}()
				synctest.Wait() // The operation is actually blocked, not pre-canceled.
				cancel()
				if err := <-finished; !errors.Is(err, context.Canceled) || c.fallback {
					t.Fatalf("waiting cancellation changed authentication: %v", err)
				}
			})
		})
	}
}

func TestConcurrentUsageSwitchesOnce(t *testing.T) {
	ctx := authContext(t)
	credential := chatGPTCredential()
	c := NewClient(newMemoryStore(credential))
	var logs bytes.Buffer
	c.Logger = log.New(&logs, "", 0)
	var primaryRequests, fallbackRequests atomic.Int32
	bothPrimary := make(chan struct{})
	c.HTTPClient = &http.Client{Transport: testTransport(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Authorization") == "Bearer "+credential.AccessToken {
			if primaryRequests.Add(1) == 2 {
				close(bothPrimary)
			}
			select {
			case <-bothPrimary:
			case <-req.Context().Done():
				return nil, req.Context().Err()
			}
			return rawResponse(429, "application/json", `{"error":{"code":"usage_limit_reached","message":"api-secret"}}`), nil
		}
		if req.Header.Get("Authorization") != "Bearer "+credential.FallbackToken {
			t.Error("unexpected authorization source")
		}
		fallbackRequests.Add(1)
		return completedResponse([]map[string]any{}), nil
	})}
	finished := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := c.responses(ctx, NewSession("model", "low", "", nil))
			finished <- err
		}()
	}
	for range 2 {
		if err := <-finished; err != nil {
			t.Fatal(err)
		}
	}
	if primaryRequests.Load() != 2 || fallbackRequests.Load() != 2 || strings.Count(logs.String(), "fallback.switched") != 1 {
		t.Fatalf("concurrent switch was not shared: primary=%d fallback=%d logs=%s", primaryRequests.Load(), fallbackRequests.Load(), &logs)
	}
	assertCredentialLogs(t, logs.String(), credential.AccessToken, credential.RefreshToken, credential.FallbackToken)
}

func TestRefreshUsesLatestStoreRecord(t *testing.T) {
	for _, mode := range []string{"already renewed", "API only", "refresh failed"} {
		t.Run(mode, func(t *testing.T) {
			stale := chatGPTCredential()
			stale.ExpiresAt = time.Time{}
			latest := stale
			latest.FallbackToken = "latest-api"
			want, fallback := "latest-api", true
			if mode == "already renewed" {
				latest.AccessToken = "latest-chatgpt"
				latest.ExpiresAt = time.Now().Add(time.Hour)
				want, fallback = latest.AccessToken, false
			} else if mode == "API only" {
				latest = Credential{FallbackToken: latest.FallbackToken}
			}
			backing := newMemoryStore(latest)
			store := credentialStoreFuncs{
				load:   func(context.Context) (Credential, error) { return stale, nil },
				update: backing.Update,
			}
			c := NewClient(store)
			exchanges := 0
			c.HTTPClient = &http.Client{Transport: testTransport(func(*http.Request) (*http.Response, error) {
				exchanges++
				return rawResponse(401, "application/json", "denied"), nil
			})}
			auth, err := c.accessToken(t.Context())
			if err != nil || auth.token != want || auth.fallback != fallback {
				t.Fatalf("used stale record: %#v, %v", auth, err)
			}
			if (exchanges == 1) != (mode == "refresh failed") || backing.credential != latest {
				t.Fatal("unexpected renewal or record mutation")
			}
		})
	}
}

func TestUnrefreshableCredential(t *testing.T) {
	for _, credential := range []Credential{
		{ClientID: "issued-client", AccessToken: "expired", FallbackToken: "api"},
		{RefreshToken: "refresh", FallbackToken: "api"},
		{ClientID: "dynamic_agent_client", RefreshToken: "refresh", FallbackToken: "api"},
	} {
		c := NewClient(newMemoryStore(credential))
		c.HTTPClient = &http.Client{Transport: testTransport(func(*http.Request) (*http.Response, error) {
			t.Fatal("unrefreshable record reached network")
			return nil, nil
		})}
		auth, err := c.accessToken(t.Context())
		if err != nil || !auth.fallback || auth.token != "api" {
			t.Fatalf("unrefreshable credential did not use fallback: %#v %v", auth, err)
		}
	}
}

func TestRefreshReadFailureAndDetachedCancellation(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		credential := chatGPTCredential()
		credential.ExpiresAt = time.Time{}
		c := NewClient(newMemoryStore(credential))
		c.HTTPClient = &http.Client{Transport: testTransport(func(*http.Request) (*http.Response, error) {
			if canceled {
				return nil, context.Canceled
			}
			res := rawResponse(200, "application/json", "")
			res.Body = &observedBody{Reader: errorReader{errors.New("read failure with refresh-secret")}}
			return res, nil
		})}
		var logs bytes.Buffer
		c.Logger = log.New(&logs, "", 0)
		auth, err := c.accessToken(t.Context())
		if canceled {
			if !errors.Is(err, context.Canceled) || c.fallback {
				t.Fatalf("cancellation from transport triggered fallback: %v", err)
			}
		} else if err != nil || !auth.fallback {
			t.Fatalf("read failure did not trigger fallback: %#v %v", auth, err)
		}
		assertCredentialLogs(t, logs.String(), "refresh-secret", credential.AccessToken, credential.FallbackToken)
	}
}

func TestCredentialRemovedWhileUsingFallback(t *testing.T) {
	store := newMemoryStore(Credential{FallbackToken: "api"})
	c := NewClient(store)
	if _, err := c.accessToken(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(t.Context(), func(Credential) (Credential, error) {
		credential := chatGPTCredential()
		credential.FallbackToken = ""
		return credential, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.accessToken(t.Context()); !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("sticky client silently switched back to ChatGPT: %v", err)
	}
}
