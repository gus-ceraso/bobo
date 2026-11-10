package provider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestExternalSignInDoesNotApplyStaleUsageLimit(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint("stream=", stream), func(t *testing.T) {
			ctx := authContext(t)
			initial := chatGPTCredential()
			backing := newMemoryStore(initial)
			// Distinct clients and store handles, sharing one synchronized record.
			a := NewClient(credentialStoreFuncs{load: backing.Load, update: backing.Update})
			b := NewClient(credentialStoreFuncs{load: backing.Load, update: backing.Update})
			b.HTTPClient = &http.Client{Transport: testTransport(func(*http.Request) (*http.Response, error) {
				return rawResponse(200, "application/json", signInTokens), nil
			})}
			var logs bytes.Buffer
			a.Logger = log.New(&logs, "", 0)
			functions := 0
			function := callbackFunction{call: func(context.Context, map[string]any) []Part {
				functions++
				return []Part{{ContentType: "text/plain", Content: []byte("completed side effect")}}
			}}
			requests := 0
			var limitedBody []byte
			var limitedResponse *observedBody
			a.HTTPClient = &http.Client{Transport: testTransport(func(req *http.Request) (*http.Response, error) {
				requests++
				body, err := io.ReadAll(req.Body)
				if err != nil {
					t.Fatal(err)
				}
				wantToken := initial.AccessToken
				if requests >= 3 {
					wantToken = "signin-secret"
				}
				if req.Header.Get("Authorization") != "Bearer "+wantToken {
					t.Fatalf("request %d applied the old limit to the new registration", requests)
				}
				switch requests {
				case 1:
					return completedResponse([]map[string]any{{"type": "function_call", "name": "inspect", "call_id": "side-effect", "arguments": "{}"}}), nil
				case 2:
					limitedBody = body
					// A's request is already authorized with X. B commits Y before
					// that request's usage-limit failure is delivered to A.
					query, callback, done := beginSignIn(t, ctx, b)
					sendSignInCallback(t, ctx, callback, url.Values{
						"state": {query.Get("state")}, "code": {"new-code"}, "client_id": {"registration-Y"},
					}, "GET")
					if err := awaitSignIn(t, ctx, done, callback); err != nil {
						t.Fatal(err)
					}
					response := rawResponse(429, "application/json", `{"error":{"code":"usage_limit_reached"}}`)
					if stream {
						response = streamResponse(`{"type":"response.failed","response":{"error":{"code":"usage_limit_reached"}}}`)
					}
					limitedResponse = &observedBody{Reader: response.Body}
					response.Body = limitedResponse
					return response, nil
				case 3:
					if !bytes.Equal(body, limitedBody) || functions != 1 || !limitedResponse.closed {
						t.Fatal("registration retry changed the request, repeated a function, or leaked the response")
					}
					return completedResponse([]map[string]any{{"type": "message", "content": "done"}}), nil
				case 4:
					return completedResponse([]map[string]any{{"type": "message", "content": "next"}}), nil
				default:
					return nil, errors.New("unbounded registration retry")
				}
			})}
			session := NewSession("model", "low", "", []Function{function})
			answer, err := a.Prompt(ctx, session, "perform the side effect")
			if err != nil || answer != "done" || functions != 1 || len(session.Events) != 4 || a.fallback || a.generation != 0 {
				t.Fatalf("stale usage failure was not isolated: answer=%q err=%v calls=%d events=%d fallback=%t", answer, err, functions, len(session.Events), a.fallback)
			}
			if answer, err := a.Prompt(ctx, session, "next turn"); err != nil || answer != "next" || requests != 4 || functions != 1 {
				t.Fatalf("registration retry did not remain on ChatGPT: answer=%q err=%v requests=%d", answer, err, requests)
			}
			if strings.Contains(logs.String(), "fallback.switched") || backing.credential.ClientID != "registration-Y" {
				t.Fatalf("stale failure switched or overwrote Y: %s", &logs)
			}
		})
	}
}

func TestUsageFailureTracksRegistrationNotTokens(t *testing.T) {
	for _, replaceRegistration := range []bool{false, true} {
		t.Run(fmt.Sprint("replaced=", replaceRegistration), func(t *testing.T) {
			initial := chatGPTCredential()
			store := newMemoryStore(initial)
			c := NewClient(store)
			auth, err := c.accessToken(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			latest, err := store.Update(t.Context(), func(current Credential) (Credential, error) {
				if replaceRegistration {
					// Identity changes even if bearer tokens happen to be equal.
					current.ClientID = "new-registration"
				} else {
					// Ordinary renewal rotates both tokens within the same ID.
					current.AccessToken = "renewed-access"
					current.RefreshToken = "renewed-refresh"
					current.ExpiresAt = time.Now().Add(2 * time.Hour)
				}
				return current, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			retry, err := c.usageFallback(t.Context(), auth)
			if err != nil || !retry || c.fallback == replaceRegistration {
				t.Fatalf("usage failure compared the wrong identity: retry=%t err=%v fallback=%t", retry, err, c.fallback)
			}
			selected, err := c.accessToken(t.Context())
			want := latest.AccessToken
			if !replaceRegistration {
				want = latest.FallbackToken
			}
			if err != nil || selected.token != want {
				t.Fatalf("incorrect retry credential: %#v %v", selected, err)
			}
			if replaceRegistration {
				// A real limit from Y now applies to Y. It must switch once,
				// rather than repeatedly retrying the replacement registration.
				if retry, err := c.usageFallback(t.Context(), selected); err != nil || !retry || !c.fallback {
					t.Fatalf("new registration's own limit was ignored: retry=%t err=%v", retry, err)
				}
				selected, err = c.accessToken(t.Context())
				if err != nil || !selected.fallback {
					t.Fatalf("replacement did not fall back: %#v %v", selected, err)
				}
			}
			if retry, err := c.usageFallback(t.Context(), selected); err != nil || retry {
				t.Fatalf("fallback request retried again: retry=%t err=%v", retry, err)
			}
		})
	}
}

func TestExternalRegistrationDoesNotResetStickyFallback(t *testing.T) {
	store := newMemoryStore(chatGPTCredential())
	c := NewClient(store)
	oldAuth, err := c.accessToken(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if retry, err := c.usageFallback(t.Context(), oldAuth); err != nil || !retry || !c.fallback {
		t.Fatalf("initial fallback switch failed: retry=%t err=%v", retry, err)
	}
	if _, err := store.Update(t.Context(), func(current Credential) (Credential, error) {
		current.ClientID = "externally-replaced"
		current.AccessToken = "new-access"
		current.FallbackToken = "new-api"
		return current, nil
	}); err != nil {
		t.Fatal(err)
	}
	if retry, err := c.usageFallback(t.Context(), oldAuth); err != nil || !retry || !c.fallback {
		t.Fatalf("external registration reset an existing fallback: retry=%t err=%v", retry, err)
	}
	auth, err := c.accessToken(t.Context())
	if err != nil || !auth.fallback || auth.token != "new-api" {
		t.Fatalf("sticky fallback was not retained: %#v %v", auth, err)
	}
	if retry, err := c.usageFallback(t.Context(), auth); err != nil || retry {
		t.Fatalf("a fallback usage error tried to fall back again: retry=%t err=%v", retry, err)
	}
}
