package provider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

// This body observes the context installed by http.Client.Timeout, not just the
// caller's context. It exercises timeouts after headers, while reading JSON.
type timeoutTokenBody struct {
	ctx    context.Context
	closed bool
}

func (b *timeoutTokenBody) Read([]byte) (int, error) {
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

func (b *timeoutTokenBody) Close() error { b.closed = true; return nil }

func TestOAuthClientTimeoutUsesFallback(t *testing.T) {
	for _, stage := range []string{"headers", "body"} {
		for _, fallback := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/fallback=%t", stage, fallback), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					parent, cancel := context.WithTimeout(t.Context(), 10*time.Second)
					defer cancel()
					credential := chatGPTCredential()
					credential.ExpiresAt = time.Time{}
					if !fallback {
						credential.FallbackToken = ""
					}
					store := newMemoryStore(credential)
					c := NewClient(store)
					var logs bytes.Buffer
					c.Logger = log.New(&logs, "", 0)
					exchanges := 0
					var body *timeoutTokenBody
					c.HTTPClient = &http.Client{
						Timeout: time.Second,
						Transport: testTransport(func(req *http.Request) (*http.Response, error) {
							exchanges++
							if stage == "headers" {
								<-req.Context().Done()
								return nil, req.Context().Err()
							}
							body = &timeoutTokenBody{ctx: req.Context()}
							response := rawResponse(200, "application/json", "")
							response.Body = body
							return response, nil
						}),
					}
					start := time.Now()
					auth, err := c.accessToken(parent)
					if parent.Err() != nil || time.Since(start) != time.Second || exchanges != 1 {
						t.Fatalf("not an independent client timeout: parent=%v elapsed=%v exchanges=%d", parent.Err(), time.Since(start), exchanges)
					}
					if fallback {
						if err != nil || !auth.fallback || auth.token != credential.FallbackToken {
							t.Fatalf("OAuth timeout did not select fallback: %#v %v", auth, err)
						}
						if _, err := c.accessToken(parent); err != nil || exchanges != 1 {
							t.Fatalf("timeout fallback was not sticky: exchanges=%d err=%v", exchanges, err)
						}
					} else if !errors.Is(err, context.DeadlineExceeded) || c.fallback {
						t.Fatalf("missing-fallback timeout = %v", err)
					}
					if body != nil && !body.closed {
						t.Fatal("timed-out OAuth body leaked")
					}
					if store.credential != credential || !strings.Contains(logs.String(), "refresh.failed reason=refresh_error") || strings.Contains(logs.String(), "reason=canceled") {
						t.Fatalf("timeout was persisted or misclassified: %s", &logs)
					}
					assertCredentialLogs(t, logs.String(), credential.AccessToken, credential.RefreshToken, credential.FallbackToken)
				})
			})
		}
	}
}

func TestOAuthTimeoutPreservesCallerDeadline(t *testing.T) {
	for _, stage := range []string{"headers", "body"} {
		t.Run(stage, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				parent, cancel := context.WithTimeout(t.Context(), time.Second)
				defer cancel()
				credential := chatGPTCredential()
				credential.ExpiresAt = time.Time{}
				c := NewClient(newMemoryStore(credential))
				var body *timeoutTokenBody
				c.HTTPClient = &http.Client{
					Timeout: 10 * time.Second,
					Transport: testTransport(func(req *http.Request) (*http.Response, error) {
						if stage == "headers" {
							<-req.Context().Done()
							return nil, req.Context().Err()
						}
						body = &timeoutTokenBody{ctx: req.Context()}
						response := rawResponse(200, "application/json", "")
						response.Body = body
						return response, nil
					}),
				}
				if _, err := c.accessToken(parent); !errors.Is(err, context.DeadlineExceeded) || parent.Err() != context.DeadlineExceeded || c.fallback {
					t.Fatalf("caller deadline was treated as OAuth timeout: %v", err)
				}
				if body != nil && !body.closed {
					t.Fatal("canceled OAuth body leaked")
				}
			})
		})
	}
}

func TestContextShapedStoreFailuresDoNotUseFallback(t *testing.T) {
	for _, cause := range []error{context.DeadlineExceeded, context.Canceled} {
		for _, stage := range []string{"load", "update", "commit"} {
			t.Run(fmt.Sprintf("%s/%v", stage, cause), func(t *testing.T) {
				credential := chatGPTCredential()
				credential.ExpiresAt = time.Time{}
				store := newMemoryStore(credential)
				want := fmt.Errorf("store operation failed: %w", cause)
				switch stage {
				case "load":
					store.loadErr = want
				case "update":
					store.updateErr = want
				case "commit":
					store.commitErr = want
				}
				c := NewClient(store)
				var logs bytes.Buffer
				c.Logger = log.New(&logs, "", 0)
				exchanges := 0
				c.HTTPClient = &http.Client{Transport: testTransport(func(*http.Request) (*http.Response, error) {
					exchanges++
					return rawResponse(200, "application/json", `{"access_token":"rotated-access","refresh_token":"rotated-refresh","expires_in":3600}`), nil
				})}
				_, err := c.accessToken(t.Context())
				if !errors.Is(err, want) || c.fallback || t.Context().Err() != nil || store.credential != credential {
					t.Fatalf("store error triggered fallback or was masked: %v", err)
				}
				if (exchanges == 1) != (stage == "commit") {
					t.Fatalf("unexpected token exchange before store failure: %d", exchanges)
				}
				if stage != "load" && !strings.Contains(err.Error(), "updating credential") {
					t.Fatalf("lost storage-error provenance: %v", err)
				}
				if strings.Contains(logs.String(), "reason=canceled") || strings.Contains(logs.String(), "refresh.completed") || strings.Contains(logs.String(), "fallback.switched") {
					t.Fatalf("store failure logged as cancellation or renewal: %s", &logs)
				}
				assertCredentialLogs(t, logs.String(), credential.AccessToken, credential.RefreshToken, credential.FallbackToken, "rotated-access", "rotated-refresh")
			})
		}
	}
}

func TestWrappedRefreshFailureKeepsProvenance(t *testing.T) {
	credential := chatGPTCredential()
	credential.ExpiresAt = time.Time{}
	backing := newMemoryStore(credential)
	c := NewClient(credentialStoreFuncs{
		load: backing.Load,
		update: func(ctx context.Context, update func(Credential) (Credential, error)) (Credential, error) {
			current, err := backing.Update(ctx, update)
			if err != nil {
				return Credential{}, fmt.Errorf("store callback: %w", err)
			}
			return current, nil
		},
	})
	c.HTTPClient = &http.Client{Transport: testTransport(func(*http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("OAuth request deadline: %w", context.DeadlineExceeded)
	})}
	if auth, err := c.accessToken(t.Context()); err != nil || !auth.fallback {
		t.Fatalf("store wrapper hid OAuth error provenance: %#v %v", auth, err)
	}
}
