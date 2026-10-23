package session

import (
	"context"
	"time"

	"github.com/thaynes43/dev-env/web/portal"
)

// Handle is a SHA-256 digest of a cryptographically random browser handle.
// Storage must be shared and authoritative across replicas before live wiring.
type Handle [32]byte

type Login struct {
	State, Nonce, Verifier string
	ExpiresAt              time.Time
}

type Record struct {
	Principal                           portal.Principal
	CSRF                                string
	CreatedAt, IdleExpiresAt, ExpiresAt time.Time
	// Private to the server package, persisted only inside AEAD payloads.
	accessToken    string
	tokenExpiresAt time.Time
}

// Store has no production in-memory fallback. TakeLogin must atomically check
// state/expiry and consume exactly once, returning ErrDenied on absence/replay.
// A wrong-state attempt must not consume the bound unexpired transaction.
// Revocation must be durable and visible to every GetSession before success.
// Implementations must not log handles, state, nonce, verifier or CSRF values.
type Store interface {
	PutLogin(context.Context, Handle, Login) error
	TakeLogin(context.Context, Handle, string, time.Time) (Login, error)
	PutSession(context.Context, Handle, Record) error
	GetSession(context.Context, Handle) (Record, error)
	RevokeSession(context.Context, Handle) error
}

// CodeClient receives only fixed configured destinations through its concrete
// adapter, never a URL from a request. M5B retains only the exact session's
// short-lived access token for live UserInfo; no refresh operation exists.
type CodeClient interface {
	Binding() CodeBinding
	AuthorizationURL(state, nonce, verifier string) (string, error)
	Exchange(context.Context, string, string) (codeTokens, error)
}

type codeTokens struct {
	idToken, accessToken string
	expiresAt            time.Time
}

func (codeTokens) String() string { return "[redacted portal exchange]" }
func (Record) String() string     { return "[redacted portal session]" }
func (Login) String() string      { return "[redacted portal login]" }

type CodeBinding struct{ Issuer, ClientID, Callback, AuthorizationEndpoint string }

func (c codeTokens) GoString() string { return c.String() }
func (r Record) GoString() string     { return r.String() }
func (l Login) GoString() string      { return l.String() }
