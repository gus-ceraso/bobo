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

// CredentialStore stores one credential record. Implementations must protect
// tokens, persist updates atomically, and honor context cancellation, including
// while waiting for exclusive access. Updates must coordinate all users of the
// record, including other store instances and processes.
//
// A failed commit cannot undo remote token rotation. If saving renewed credentials
// fails, fix the store and sign in again.
type CredentialStore interface {
	// Load returns a snapshot, or ErrNoCredentials if the record does not exist.
	// A fallback-only record must be returned normally, not as ErrNoCredentials.
	Load(ctx context.Context) (Credential, error)

	// Update holds exclusive access across reading, calling update, and committing
	// its result. It returns the committed credential. Pass a zero Credential to
	// update when the record is absent. The callback may perform network I/O and
	// must not reenter the store. Call it at most once: it may rotate a token.
	// On callback failure, leave storage unchanged and return its error (possibly
	// wrapped). On commit failure, return the storage error.
	Update(ctx context.Context, update func(Credential) (Credential, error)) (Credential, error)
}
