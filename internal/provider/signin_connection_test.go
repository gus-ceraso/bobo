package provider

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

// Keep these connections open until after done: closing them in the test before
// completion would hide the shutdown deadlock this fixture reproduces.
func stalledSignInConnections(t *testing.T, ctx context.Context, callback string) []net.Conn {
	t.Helper()
	u, err := url.Parse(callback)
	if err != nil {
		t.Fatal(err)
	}
	requests := []string{
		// Complete headers, invalid state, and an intentionally withheld body.
		fmt.Sprintf("GET %s?state=unrelated HTTP/1.1\r\nHost: %s\r\nContent-Length: 1\r\n\r\n", u.Path, u.Host),
		// An unread chunked body and a different method/path exercise handlers
		// outside the legitimate callback as well as fixed-length body drains.
		fmt.Sprintf("POST /unrelated HTTP/1.1\r\nHost: %s\r\nTransfer-Encoding: chunked\r\n\r\n1\r\n", u.Host),
		fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nX-Incomplete: ", u.Path, u.Host),
		"", // Accepted connection that has not sent any headers.
	}
	var connections []net.Conn
	for _, request := range requests {
		conn, err := (&net.Dialer{}).DialContext(ctx, "tcp4", u.Host)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		connections = append(connections, conn)
		if _, err := io.WriteString(conn, request); err != nil {
			t.Fatal(err)
		}
	}
	return connections
}

func assertSignInConnectionsClosed(t *testing.T, connections []net.Conn) {
	t.Helper()
	for i, conn := range connections {
		if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		// A closed connection can have a buffered HTTP error response or report
		// a reset instead of EOF. A read timeout means it was leaked, however.
		_, err := io.Copy(io.Discard, conn)
		if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
			t.Errorf("callback connection %d remained open after done", i)
		}
	}
}

func TestSignInIgnoresUnrelatedStalledConnections(t *testing.T) {
	// No caller deadline: successful cleanup must not rely on one expiring.
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	guard := time.AfterFunc(2*time.Second, cancel)
	defer guard.Stop()
	c := NewClient(newMemoryStore(Credential{FallbackToken: "api"}))
	var exchanges atomic.Int32
	c.HTTPClient = &http.Client{Transport: testTransport(func(*http.Request) (*http.Response, error) {
		exchanges.Add(1)
		return rawResponse(200, "application/json", signInTokens), nil
	})}
	query, callback, done := beginSignIn(t, ctx, c)
	connections := stalledSignInConnections(t, authContext(t), callback)
	request, err := http.NewRequestWithContext(authContext(t), "GET", callback+"?"+url.Values{
		"state": {query.Get("state")}, "code": {"code"}, "client_id": {"new-registration"},
	}.Encode(), nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil || response.StatusCode != 200 || string(body) != "Sign-in received. Return to the application for the result.\n" {
		t.Fatalf("callback response was interrupted: status=%d body=%q err=%v", response.StatusCode, body, readErr)
	}
	if err := awaitSignIn(t, authContext(t), done, callback); err != nil {
		t.Fatalf("unrelated connections blocked valid sign-in: %v", err)
	}
	if ctx.Err() != nil || exchanges.Load() != 1 {
		t.Fatalf("sign-in depended on cancellation or repeated the exchange: %v, exchanges=%d", ctx.Err(), exchanges.Load())
	}
	assertSignInConnectionsClosed(t, connections)
}

func TestSignInCancellationClosesStalledConnections(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	c := NewClient(newMemoryStore(Credential{}))
	var exchanges atomic.Int32
	c.HTTPClient = &http.Client{Transport: testTransport(func(*http.Request) (*http.Response, error) {
		exchanges.Add(1)
		return rawResponse(200, "application/json", signInTokens), nil
	})}
	_, callback, done := beginSignIn(t, ctx, c)
	connections := stalledSignInConnections(t, authContext(t), callback)
	cancel()
	if err := awaitSignIn(t, authContext(t), done, callback); err != context.Canceled {
		t.Fatalf("canceled sign-in = %v", err)
	}
	if exchanges.Load() != 0 {
		t.Fatal("unrelated callback started a code exchange")
	}
	assertSignInConnectionsClosed(t, connections)
}
