// Package provider implements a blocking OpenAI Responses API agent loop with
// ChatGPT sign-in, credential renewal, and regular API-token fallback.
//
// # Credentials
//
// Construct a Client with NewClient and a caller-supplied CredentialStore. The
// constructor performs no I/O. The package does not read environment variables,
// choose credential paths, implement file storage, or open a browser. The store
// owns permissions, atomic persistence, and synchronization across users of the
// same backing record. Its Update operation must serialize the whole
// read-refresh-save exchange, including the network request; independently locked
// reads and writes cannot safely renew rotating refresh tokens.
//
// A Credential contains a ChatGPT registration and an optional FallbackToken.
// A record containing only FallbackToken supports ordinary API-only operation.
// Missing records return ErrNoCredentials; other storage failures are reported,
// not treated as an invitation to use a fallback. All three tokens are secrets
// and should be persisted together in protected storage.
//
// Client.SignIn returns a URL for the caller to present, together with a buffered
// completion channel. It uses a temporary IPv4 loopback listener, state validation,
// and PKCE. Credentials are saved only after the granted scope permits ChatGPT
// plan usage. Sign-in preserves the latest fallback token in the store and makes
// ChatGPT preferred again. Callback request reads/writes are bounded; the accepted
// response is flushed before closing all listener connections. Unrelated clients
// cannot indefinitely delay sign-in or its cleanup, and completion joins the
// listener and connection goroutines. Use a context deadline to bound an abandoned
// attempt. Repeated sign-ins create registrations; deleting local data does not
// revoke them.
//
// # Renewal and fallback
//
// Before each request attempt the client loads credentials. It prefers ChatGPT,
// renewing access tokens within 30 seconds of expiry. Renewal rechecks the latest
// credential inside Update; another client may already have renewed it. An omitted
// refresh_token in a successful refresh response retains the existing token.
//
// A failed refresh switches to FallbackToken when available. OAuth request timeouts
// (including HTTPClient.Timeout) are refresh failures if the caller's context is
// still active. Actual caller cancellation/deadline expiry, as well as a detached
// context.Canceled error from the exchange, propagate without switching.
// ErrUsageLimitReached from an HTTP or streamed API failure likewise switches
// authentication and retries the same serialized request. Usage failures are tied
// to the issued ClientID used by that request: if another client/process replaced
// the registration, retry its credential instead of applying the old limit. Normal
// token rotation within the same registration does not discard its usage limit.
// These retries neither append another user message nor repeat completed function
// calls. Fallback remains selected for that client until a successful explicit
// SignIn; external registration replacement does not reset an existing fallback.
// The selection is never persisted. A new client prefers ChatGPT again. API quota
// errors, authentication errors from the Responses API, and ordinary rate limits
// do not trigger this switch. A fallback request cannot fall back again.
//
// Store failures, including failure to commit a successful renewal, are returned
// without fallback. OAuth token rotation cannot be rolled back if a store commit
// fails; the saved refresh token may be stale. Fix the store and sign in again.
//
// # Conversations and execution
//
// NewSession specifies model, reasoning effort, instructions, and local Functions.
// Prompt appends a user message, consumes a streaming response, executes all local
// function calls concurrently, and continues until a final message is available.
// It returns only that message's text. The API's built-in web search is also
// enabled. Function batches have a 30-minute deadline, bounded by the caller's
// context. Functions must honor cancellation and implement their own execution
// permissions; the provider does not sandbox or request approval for them.
//
// Session.Events retains the full native history. Requests replay from the latest
// compaction event. The caller owns persistence and must serialize access to each
// session. One client may be shared by distinct sessions; configure its exported
// fields before starting concurrent calls. Failed turns retain completed events;
// another Prompt call appends another user message rather than resuming the turn.
//
// Transient HTTP/API failures retry with jittered exponential backoff and server
// retry hints until success or context cancellation. Terminal failures support
// errors.Is with the package's error sentinels. Cross-credential replay of opaque
// native events is subject to the API's compatibility rules.
//
// # Logging
//
// A nil Client.Logger disables logging, and the package writes no terminal output.
// Credential logs record only completions, failures, and source switches, never
// operation starts, bearer-token values, OAuth codes, or response bodies. Successful
// loads of an unchanged record are not logged repeatedly. Sign-in and refresh
// completion include persistence; there is no separate successful-save message.
// Store/transport error strings are deliberately omitted from credential logs.
// Session event logs, however, include conversation and function content and may
// contain sensitive user data. Protect logs accordingly.
package provider
