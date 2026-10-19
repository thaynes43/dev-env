// Package claudeauth is the access-only publication boundary for the keeper's
// shared Claude login (plan 03, Q-11/D-11): the projection document the keeper
// publishes as dev-env-claude-live and agentd consumes, and the merge of its
// access fields into a pod's own .credentials.json. No refresh token is
// representable in the document, and a document that carries one is refused.
// The package is pure: no filesystem, network or clock access.
package claudeauth

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/internal/codexauth"
)

const (
	// LiveKey is the key of the projection document inside the Secret.
	LiveKey = "access.json"
	// MaxBytes bounds the projection document.
	MaxBytes      = 64 << 10
	MaxTokenBytes = 16 << 10
	maxScopes     = 32
	maxFieldBytes = 256
)

var (
	// ErrInvalid is every refusal of a projection document. It never carries
	// input bytes.
	ErrInvalid = errors.New("claude access material is invalid")
	// ErrRefreshToken: the document carries a refresh token, which only the
	// keeper may hold.
	ErrRefreshToken = errors.New("claude access projection carries a refresh token")
	// ErrPodOwnedLogin: the pod's credentials already hold a refresh token, so
	// the pod owns a login and must not also follow the keeper's.
	ErrPodOwnedLogin = errors.New("claude credentials hold a pod-owned login")
)

// Access is one published generation. Generic formatting and JSON are
// redacted; Encode and MergeCredentials are the explicit boundaries.
type Access struct {
	Generation       uint64
	AccessToken      string
	ExpiresAt        time.Time
	Scopes           []string
	SubscriptionType string
	RateLimitTier    string
	AccountUUID      string
	OrganizationUUID string
}

func (Access) Format(f fmt.State, _ rune)   { _, _ = io.WriteString(f, "[REDACTED]") }
func (Access) MarshalJSON() ([]byte, error) { return json.Marshal("[REDACTED]") }
func (Access) LogValue() slog.Value         { return slog.StringValue("[REDACTED]") }
func (Access) MarshalLog() any              { return "[REDACTED]" }

// accessWire is the document. The uuid member names match agentd's
// oauthAccount seed file, so the same file seeds .claude.json.
type accessWire struct {
	Version          int       `json:"version"`
	Generation       uint64    `json:"generation"`
	AccessToken      string    `json:"accessToken"`
	ExpiresAt        time.Time `json:"expiresAt"`
	Scopes           []string  `json:"scopes"`
	SubscriptionType string    `json:"subscriptionType"`
	RateLimitTier    string    `json:"rateLimitTier"`
	AccountUUID      string    `json:"accountUuid"`
	OrganizationUUID string    `json:"organizationUuid"`
}

func printable(s string, max int) bool {
	return len(s) <= max && strings.IndexFunc(s, func(r rune) bool { return r < 33 || r > 126 }) == -1
}

// Validate checks a generation at now. An expired access token is invalid.
func (a Access) Validate(now time.Time) error {
	if a.Generation == 0 || a.AccessToken == "" || !printable(a.AccessToken, MaxTokenBytes) ||
		a.ExpiresAt.IsZero() || !now.Before(a.ExpiresAt) ||
		len(a.Scopes) == 0 || len(a.Scopes) > maxScopes ||
		!printable(a.SubscriptionType, maxFieldBytes) || !printable(a.RateLimitTier, maxFieldBytes) ||
		a.AccountUUID == "" || !printable(a.AccountUUID, maxFieldBytes) ||
		a.OrganizationUUID == "" || !printable(a.OrganizationUUID, maxFieldBytes) {
		return ErrInvalid
	}
	for _, sc := range a.Scopes {
		if sc == "" || !printable(sc, maxFieldBytes) {
			return ErrInvalid
		}
	}
	return nil
}

// Encode is the canonical publication form of a valid generation.
func Encode(a Access, now time.Time) ([]byte, error) {
	if a.Validate(now) != nil {
		return nil, ErrInvalid
	}
	return json.Marshal(accessWire{1, a.Generation, a.AccessToken, a.ExpiresAt.UTC(), a.Scopes,
		a.SubscriptionType, a.RateLimitTier, a.AccountUUID, a.OrganizationUUID})
}

// Decode parses and validates one document. A member that names a refresh
// token, in any case, is refused as ErrRefreshToken before anything else; any
// other unknown or duplicate member is ErrInvalid.
func Decode(raw []byte, now time.Time) (Access, error) {
	if len(raw) == 0 || len(raw) > MaxBytes {
		return Access{}, ErrInvalid
	}
	var members map[string]json.RawMessage
	if json.Unmarshal(raw, &members) == nil {
		for k := range members {
			if strings.Contains(strings.ToLower(k), "refresh") {
				return Access{}, ErrRefreshToken
			}
		}
	}
	var w accessWire
	if codexauth.DecodeStrict(raw, &w) != nil || w.Version != 1 {
		return Access{}, ErrInvalid
	}
	a := Access{w.Generation, w.AccessToken, w.ExpiresAt, w.Scopes, w.SubscriptionType, w.RateLimitTier, w.AccountUUID, w.OrganizationUUID}
	if a.Validate(now) != nil {
		return Access{}, ErrInvalid
	}
	return a, nil
}

// MergeCredentials sets claudeAiOauth.{accessToken, expiresAt, scopes,
// subscriptionType, rateLimitTier} in a credentials file's bytes and keeps
// every other member, the CLI's mcpOAuth and unknown keys included. Empty
// existing bytes start a new file. changed is false when those fields already
// hold exactly a's values, so a caller can skip the write. It never writes a
// refresh token; existing refresh material is ErrPodOwnedLogin and nothing is
// merged. Errors carry no input bytes.
func MergeCredentials(existing []byte, a Access) (out []byte, changed bool, err error) {
	top := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(existing)) > 0 && json.Unmarshal(existing, &top) != nil {
		return nil, false, ErrInvalid
	}
	oauth := map[string]json.RawMessage{}
	if cur, ok := top["claudeAiOauth"]; ok && string(bytes.TrimSpace(cur)) != "null" {
		if json.Unmarshal(cur, &oauth) != nil {
			return nil, false, ErrInvalid
		}
	}
	if podOwned(oauth) {
		return nil, false, ErrPodOwnedLogin
	}
	scopes, _ := json.Marshal(a.Scopes)
	want := map[string]json.RawMessage{
		"accessToken":      mustJSON(a.AccessToken),
		"expiresAt":        mustJSON(a.ExpiresAt.UnixMilli()),
		"scopes":           scopes,
		"subscriptionType": nullable(a.SubscriptionType),
		"rateLimitTier":    nullable(a.RateLimitTier),
	}
	for k, v := range want {
		if cur, ok := oauth[k]; !ok || !sameJSON(cur, v) {
			changed = true
		}
		oauth[k] = v
	}
	if !changed {
		return existing, false, nil
	}
	merged, err := json.Marshal(oauth)
	if err != nil {
		return nil, false, ErrInvalid
	}
	top["claudeAiOauth"] = merged
	out, err = json.Marshal(top)
	if err != nil {
		return nil, false, ErrInvalid
	}
	return out, true, nil
}

// HoldsRefreshToken reports whether credentials bytes carry refresh material,
// which marks a pod-owned login. Unparseable bytes report false.
func HoldsRefreshToken(existing []byte) bool {
	var top struct {
		O map[string]json.RawMessage `json:"claudeAiOauth"`
	}
	return json.Unmarshal(existing, &top) == nil && podOwned(top.O)
}

func podOwned(oauth map[string]json.RawMessage) bool {
	v, ok := oauth["refreshToken"]
	if !ok {
		return false
	}
	switch strings.TrimSpace(string(v)) {
	case "", `""`, "null":
		return false
	}
	return true
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func nullable(s string) json.RawMessage {
	if s == "" {
		return json.RawMessage("null")
	}
	return mustJSON(s)
}

func sameJSON(a, b json.RawMessage) bool {
	var x, y bytes.Buffer
	return json.Compact(&x, a) == nil && json.Compact(&y, b) == nil && x.String() == y.String()
}
