// Package codexauth is the access-only publication boundary shared by the keeper
// and agentd. No refresh token is representable in its public document.
package codexauth

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"
)

const (
	MaxBytes      = 64 << 10
	MaxTokenBytes = 16 << 10
	MinLifetime   = 30 * time.Minute
	LiveKey       = "access.json"
)

var ErrInvalid = errors.New("Codex access material is invalid")

// Access contains bearer material. Generic formatting and JSON are redacted;
// Encode and NativeJSON are the two explicit publication boundaries.
type Access struct {
	Generation  uint64
	AccountID   string
	IDToken     string
	AccessToken string
	ExpiresAt   time.Time
	LastRefresh time.Time
}

func (Access) Format(f fmt.State, _ rune)   { _, _ = io.WriteString(f, "[REDACTED]") }
func (Access) MarshalJSON() ([]byte, error) { return json.Marshal("[REDACTED]") }
func (Access) LogValue() slog.Value         { return slog.StringValue("[REDACTED]") }
func (Access) MarshalLog() any              { return "[REDACTED]" }

type accessWire struct {
	Version     int       `json:"version"`
	Generation  uint64    `json:"generation"`
	AccountID   string    `json:"account_id"`
	IDToken     string    `json:"id_token"`
	AccessToken string    `json:"access_token"`
	ExpiresAt   time.Time `json:"exp"`
	LastRefresh time.Time `json:"last_refresh"`
}

// DecodeStrict rejects unknown fields and trailing documents. Its error never
// contains input bytes (including errors from encoding/json).
func DecodeStrict(raw []byte, out any) error {
	if len(raw) == 0 || len(raw) > MaxBytes {
		return ErrInvalid
	}
	if uniqueJSON(raw) != nil {
		return ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || !errors.Is(d.Decode(new(any)), io.EOF) {
		return ErrInvalid
	}
	return nil
}

// Duplicate member names make a credential generation ambiguous even when
// encoding/json would silently select the last member. Bound nesting as well.
func uniqueJSON(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > 16 {
			return ErrInvalid
		}
		t, err := d.Token()
		if err != nil {
			return ErrInvalid
		}
		delim, ok := t.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				s, ok := key.(string)
				if err != nil || !ok || seen[s] {
					return ErrInvalid
				}
				seen[s] = true
				if value(depth+1) != nil {
					return ErrInvalid
				}
			}
			end, err := d.Token()
			if err != nil || end != json.Delim('}') {
				return ErrInvalid
			}
		case '[':
			for d.More() {
				if value(depth+1) != nil {
					return ErrInvalid
				}
			}
			end, err := d.Token()
			if err != nil || end != json.Delim(']') {
				return ErrInvalid
			}
		default:
			return ErrInvalid
		}
		return nil
	}
	if value(0) != nil {
		return ErrInvalid
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return ErrInvalid
	}
	return nil
}

// Claims only parses already trusted provider material; it does not authenticate
// arbitrary JWTs. Both caller boundaries are the pinned provider or private login.
func Claims(token string) (string, time.Time, error) {
	parts := strings.Split(token, ".")
	if len(token) > MaxTokenBytes || len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", time.Time{}, ErrInvalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", time.Time{}, ErrInvalid
	}
	var c struct {
		Exp  int64 `json:"exp"`
		Auth struct {
			AccountID string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
	}
	if uniqueJSON(raw) != nil || json.Unmarshal(raw, &c) != nil || c.Exp <= 0 || c.Exp > 253402300799 || !validAccount(c.Auth.AccountID) {
		return "", time.Time{}, ErrInvalid
	}
	return c.Auth.AccountID, time.Unix(c.Exp, 0).UTC(), nil
}

func validAccount(s string) bool {
	return s != "" && len(s) <= 256 && strings.IndexFunc(s, func(r rune) bool { return r < 33 || r > 126 }) == -1
}

func (a Access) Validate(now time.Time) error {
	account, exp, err := Claims(a.AccessToken)
	idAccount, _, idErr := Claims(a.IDToken)
	if err != nil || idErr != nil || a.Generation == 0 || account != a.AccountID || idAccount != a.AccountID || !exp.Equal(a.ExpiresAt) || !now.Before(exp) || a.LastRefresh.IsZero() || a.LastRefresh.After(now.Add(time.Minute)) {
		return ErrInvalid
	}
	return nil
}

func Encode(a Access, now time.Time) ([]byte, error) {
	if a.Validate(now) != nil {
		return nil, ErrInvalid
	}
	return json.Marshal(accessWire{1, a.Generation, a.AccountID, a.IDToken, a.AccessToken, a.ExpiresAt.UTC(), a.LastRefresh.UTC()})
}

func Decode(raw []byte, now time.Time) (Access, error) {
	var w accessWire
	if DecodeStrict(raw, &w) != nil || w.Version != 1 {
		return Access{}, ErrInvalid
	}
	a := Access{w.Generation, w.AccountID, w.IDToken, w.AccessToken, w.ExpiresAt, w.LastRefresh}
	if a.Validate(now) != nil {
		return Access{}, ErrInvalid
	}
	return a, nil
}

// NativeJSON implements the pinned CLI 0.160.1 file schema proven by S-3. The
// refresh_token member must exist and is always empty; never invoke native login
// or --with-access-token in an agent pod to install ordinary ChatGPT OAuth auth.
func NativeJSON(a Access, now time.Time) ([]byte, error) {
	if a.Validate(now) != nil {
		return nil, ErrInvalid
	}
	return json.Marshal(struct {
		AuthMode string  `json:"auth_mode"`
		APIKey   *string `json:"OPENAI_API_KEY"`
		Tokens   struct {
			ID      string `json:"id_token"`
			Access  string `json:"access_token"`
			Refresh string `json:"refresh_token"`
			Account string `json:"account_id"`
		} `json:"tokens"`
		LastRefresh time.Time `json:"last_refresh"`
	}{AuthMode: "chatgpt", Tokens: struct {
		ID      string `json:"id_token"`
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
		Account string `json:"account_id"`
	}{ID: a.IDToken, Access: a.AccessToken, Account: a.AccountID}, LastRefresh: a.LastRefresh.UTC()})
}
