package broker

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"
)

var (
	ErrConsoleUnauthenticated = errors.New("authentication required")
	ErrConsoleUnauthorized    = errors.New("owner access required")
)

// ConsoleIdentity comes from the isolated Authentik forward-auth path, never
// from a form or a session's operator API token (D-67).
type ConsoleIdentity struct {
	Username string
	AuthTime time.Time
}

func (i ConsoleIdentity) fresh(now time.Time) bool {
	return !i.AuthTime.IsZero() && !i.AuthTime.After(now) && now.Sub(i.AuthTime) <= 5*time.Minute
}

// ForwardAuthAuthenticator trusts the network and header-replacement boundary
// in D-67. The proxy provider's HS256 token cannot be verified without its
// private client secret. This parses its claims; it does NOT verify a signature.
type ForwardAuthAuthenticator struct {
	Owner, Issuer, Audience string
	Now                     func() time.Time
}

func NewForwardAuthAuthenticator(owner, issuer, audience string) (*ForwardAuthAuthenticator, error) {
	if owner == "" || len(owner) > 240 || strings.TrimSpace(owner) != owner || strings.ContainsAny(owner, ",/") || strings.ContainsFunc(owner, unicode.IsControl) {
		return nil, errors.New("an explicit owner username is required")
	}
	return &ForwardAuthAuthenticator{Owner: owner, Issuer: issuer, Audience: audience}, nil
}

// Authenticate accepts one raw upstream token and one exact owner username.
// Issuer and Audience can additionally constrain a dedicated proxy provider.
func (a *ForwardAuthAuthenticator) Authenticate(r *http.Request) (ConsoleIdentity, error) {
	var none ConsoleIdentity
	users, tokens := r.Header.Values("X-authentik-username"), r.Header.Values("X-authentik-jwt")
	if len(users) != 1 || len(tokens) != 1 || users[0] == "" || tokens[0] == "" || len(tokens[0]) > 16<<10 {
		return none, ErrConsoleUnauthenticated
	}
	if users[0] != a.Owner {
		return none, ErrConsoleUnauthorized
	}
	parts := strings.Split(tokens[0], ".")
	if len(parts) != 3 {
		return none, ErrConsoleUnauthenticated
	}
	header, err := tokenObject(parts[0])
	if err != nil || stringClaim(header, "alg") != "HS256" {
		return none, ErrConsoleUnauthenticated
	}
	signature, err := tokenPart(parts[2])
	if err != nil || len(signature) != 32 {
		return none, ErrConsoleUnauthenticated
	}
	claims, err := tokenObject(parts[1])
	if err != nil {
		return none, ErrConsoleUnauthenticated
	}
	now := time.Now()
	if a.Now != nil {
		now = a.Now()
	}
	exp, err := integerClaim(claims, "exp")
	if err != nil || exp <= now.Unix() || exp > 253402300799 {
		return none, ErrConsoleUnauthenticated
	}
	if _, ok := claims["nbf"]; ok {
		nbf, err := integerClaim(claims, "nbf")
		if err != nil || nbf < 0 || nbf > now.Unix() {
			return none, ErrConsoleUnauthenticated
		}
	}
	if a.Issuer != "" && stringClaim(claims, "iss") != a.Issuer {
		return none, ErrConsoleUnauthenticated
	}
	if a.Audience != "" && !audienceContains(claims["aud"], a.Audience) {
		return none, ErrConsoleUnauthenticated
	}
	identity := ConsoleIdentity{Username: users[0]}
	if _, ok := claims["auth_time"]; ok {
		login, err := integerClaim(claims, "auth_time")
		if err != nil {
			return none, ErrConsoleUnauthenticated
		}
		if login > 0 && login <= 253402300799 {
			identity.AuthTime = time.Unix(login, 0)
		}
	}
	return identity, nil
}

func tokenPart(s string) ([]byte, error) {
	b, err := base64.RawURLEncoding.Strict().DecodeString(s)
	if err != nil || base64.RawURLEncoding.EncodeToString(b) != s {
		return nil, ErrConsoleUnauthenticated
	}
	return b, nil
}

// tokenObject rejects repeated claim names and trailing JSON. All data is
// bounded by the upstream token limit before it reaches this decoder.
func tokenObject(part string) (map[string]json.RawMessage, error) {
	b, err := tokenPart(part)
	if err != nil {
		return nil, err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	t, err := d.Token()
	if err != nil || t != json.Delim('{') {
		return nil, ErrConsoleUnauthenticated
	}
	obj := make(map[string]json.RawMessage)
	for d.More() {
		t, err = d.Token()
		key, ok := t.(string)
		if err != nil || !ok {
			return nil, ErrConsoleUnauthenticated
		}
		if _, exists := obj[key]; exists {
			return nil, ErrConsoleUnauthenticated
		}
		var value json.RawMessage
		if err := d.Decode(&value); err != nil {
			return nil, ErrConsoleUnauthenticated
		}
		obj[key] = value
	}
	if t, err = d.Token(); err != nil || t != json.Delim('}') {
		return nil, ErrConsoleUnauthenticated
	}
	if _, err = d.Token(); !errors.Is(err, io.EOF) {
		return nil, ErrConsoleUnauthenticated
	}
	return obj, nil
}

func stringClaim(obj map[string]json.RawMessage, key string) string {
	var value string
	_ = json.Unmarshal(obj[key], &value)
	return value
}

func integerClaim(obj map[string]json.RawMessage, key string) (int64, error) {
	var value int64
	if err := json.Unmarshal(obj[key], &value); err != nil || bytes.Equal(bytes.TrimSpace(obj[key]), []byte("null")) {
		return 0, ErrConsoleUnauthenticated
	}
	return value, nil
}

func audienceContains(raw json.RawMessage, expected string) bool {
	var single string
	if json.Unmarshal(raw, &single) == nil {
		return single == expected
	}
	var multiple []string
	if json.Unmarshal(raw, &multiple) != nil {
		return false
	}
	for _, value := range multiple {
		if value == expected {
			return true
		}
	}
	return false
}
