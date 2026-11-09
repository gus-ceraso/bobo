package provider

import (
	"context"
	"errors"
	"time"
)

// ErrNoCredentials means that no ChatGPT credential or fallback token is
// available. CredentialStore.Load should return it when its record is absent.
var ErrNoCredentials = errors.New("no credentials")

// Credential holds one ChatGPT registration and an optional regular API token.
// AccessToken, RefreshToken, and FallbackToken are secrets. A credential containing
// only FallbackToken is a valid API-only configuration. The zero value is empty.
type Credential struct {
	ClientID      string    // Issued registration identity; stable across refreshes.
	AccessToken   string    // ChatGPT bearer token for the Responses API.
	RefreshToken  string    // Renews the ChatGPT access token.
	ExpiresAt     time.Time // Expiry of AccessToken only.
	FallbackToken string    // Regular API bearer token; not refreshed by Client.
}

// CredentialStore owns the storage and synchronization of one credential record.
// Implementations must protect all tokens and honor context cancellation,
// including while waiting for exclusive access. File-backed stores are responsible
// for permissions, locking, and atomic replacement; none are provided here.
//
// Update must coordinate every user of the same backing record, including other
// store instances or processes. Locking Load and a separate save independently is
// insufficient: refreshing a rotating token requires exclusive read-refresh-save.
// Callbacks perform network I/O while that exclusive access is held.
//
// Atomicity covers local persistence, not the remote OAuth exchange. If committing
// a renewed credential fails, the previously saved refresh token may already be
// invalid. Client reports the storage error rather than silently using fallback;
// fix the store and sign in again to recover.
type CredentialStore interface {
	// Load returns a snapshot, or ErrNoCredentials if the record does not exist.
	// A fallback-only record must be returned normally, not as ErrNoCredentials.
	Load(ctx context.Context) (Credential, error)

	// Update exclusively reads, modifies, and commits the record, returning the
	// committed credential. An absent record is passed as the zero Credential.
	// The callback runs at most once. On callback failure, leave storage unchanged
	// and return that error (possibly wrapped). On commit failure, return the
	// storage error. Never automatically retry a callback: it may rotate a token.
	// An unchanged result need not be rewritten. Callbacks must not reenter this
	// store. The exclusive scope ends before Update returns.
	Update(ctx context.Context, update func(Credential) (Credential, error)) (Credential, error)
}
