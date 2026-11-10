// Package provider runs OpenAI Responses API conversations with local functions,
// ChatGPT sign-in, and optional API-token fallback.
//
// # Getting started
//
// Construct a Client with NewClient and a caller-supplied CredentialStore. Create
// a conversation with NewSession, then call Client.Prompt for each user message.
// The package does not choose storage paths, read environment variables, open a
// browser, or write terminal output. Configure the client before use; it can serve
// distinct sessions concurrently, but each session requires exclusive access.
//
// # Authentication
//
// A Credential can hold a ChatGPT registration, a FallbackToken, or both. The
// client prefers ChatGPT and renews expiring tokens automatically. Client.SignIn
// returns a URL for the caller to open and saves the resulting credentials.
// CredentialStore implementations must protect tokens and serialize updates
// across all users of the same record; see CredentialStore.Update for its contract.
//
// A refresh failure or ErrUsageLimitReached selects FallbackToken when available.
// The client retries the request without repeating completed function calls.
// Fallback stays selected until a successful SignIn; a new client prefers ChatGPT
// again. Ordinary rate limits, API quota errors, and Responses API authentication
// errors do not select fallback. Caller cancellation and storage errors return
// without fallback. If saving a renewed credential fails, fix the store and sign
// in again: the previously saved refresh token may no longer work.
//
// # Conversations
//
// Prompt returns final assistant text and retains conversation history in
// Session.Events. The caller owns persistence. After an error, another Prompt
// starts a new turn rather than resuming the failed one. Transient API failures
// retry until success or context cancellation; terminal API errors support
// errors.Is with the package's error sentinels.
//
// Local functions execute concurrently with a 30-minute timeout per batch,
// bounded by the caller's context. They must honor cancellation and enforce their
// own permissions; this package provides no sandbox or approval step. The API's
// built-in web search is also enabled.
//
// # Logging
//
// Client.Logger is nil by default. Credential logs omit token values and OAuth
// secrets, but session logs contain conversation and function content. Protect
// logs as sensitive data.
package provider
