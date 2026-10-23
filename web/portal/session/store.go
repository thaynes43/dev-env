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
}

// Store has no production in-memory fallback. TakeLogin must atomically check
// state/expiry and consume exactly once, returning ErrDenied on absence/replay.
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
// adapter, never a URL from a request. It returns only the server-side ID token,
// not an access/refresh token or userinfo payload.
type CodeClient interface {
	Binding() CodeBinding
	AuthorizationURL(state, nonce, verifier string) (string, error)
	Exchange(context.Context, string, string) (string, error)
}

type CodeBinding struct{ Issuer, ClientID, Callback, AuthorizationEndpoint string }
