package broker

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// This is a shaped upstream token, not a cryptographic fixture: authentication
// belongs to the isolated Authentik path in D-67, not this payload parser.
func consoleToken(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	return compactConsoleToken(`{"alg":"HS256","typ":"JWT"}`, string(b))
}

func compactConsoleToken(header, payload string) string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(header)) + "." + enc([]byte(payload)) + "." + enc(make([]byte, 32))
}

func TestForwardAuthAuthenticator(t *testing.T) {
	now := time.Date(2026, 10, 8, 1, 0, 0, 0, time.UTC)
	a, err := NewForwardAuthAuthenticator("owner", "https://auth.example.com/application/o/approvals/", "proxy-client")
	if err != nil {
		t.Fatal(err)
	}
	a.Now = func() time.Time { return now }
	claims := map[string]any{"exp": now.Add(time.Hour).Unix(), "auth_time": now.Add(-time.Minute).Unix(), "iss": a.Issuer, "aud": a.Audience}
	payload, _ := json.Marshal(claims)
	duplicatePayload := strings.TrimSuffix(string(payload), "}") + fmt.Sprintf(",\"exp\":%d}", now.Add(time.Hour).Unix())
	for _, tc := range []struct {
		name string
		edit func(map[string]any)
		ok   bool
	}{
		{"valid", func(map[string]any) {}, true},
		{"audience array", func(c map[string]any) { c["aud"] = []string{"another", a.Audience} }, true},
		{"missing login is not fresh", func(c map[string]any) { delete(c, "auth_time") }, true},
		{"missing expiry", func(c map[string]any) { delete(c, "exp") }, false},
		{"null expiry", func(c map[string]any) { c["exp"] = nil }, false},
		{"expired", func(c map[string]any) { c["exp"] = now.Unix() }, false},
		{"fractional expiry", func(c map[string]any) { c["exp"] = 1.5 }, false},
		{"wrong issuer", func(c map[string]any) { c["iss"] = "https://another.example.com/" }, false},
		{"wrong audience", func(c map[string]any) { c["aud"] = "another-client" }, false},
		{"not yet valid", func(c map[string]any) { c["nbf"] = now.Add(time.Second).Unix() }, false},
		{"malformed login", func(c map[string]any) { c["auth_time"] = "recent" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := make(map[string]any, len(claims))
			for k, v := range claims {
				c[k] = v
			}
			tc.edit(c)
			r := httptest.NewRequest("GET", "/approvals", nil)
			r.Header.Set("X-authentik-username", "owner")
			r.Header.Set("X-authentik-jwt", consoleToken(c))
			i, err := a.Authenticate(r)
			if (err == nil) != tc.ok {
				t.Fatalf("accepted=%v, want %v", err == nil, tc.ok)
			}
			if tc.name == "valid" && (!i.fresh(now) || i.Username != "owner") {
				t.Errorf("identity %+v", i)
			}
			if tc.name == "missing login is not fresh" && i.fresh(now) {
				t.Error("missing auth_time was fresh")
			}
		})
	}
	for _, tc := range []struct {
		name  string
		token string
	}{
		{"wrong algorithm", compactConsoleToken(`{"alg":"none"}`, string(payload))},
		{"duplicate header claim", compactConsoleToken(`{"alg":"HS256","alg":"HS256"}`, string(payload))},
		{"duplicate payload claim", compactConsoleToken(`{"alg":"HS256"}`, duplicatePayload)},
		{"trailing payload", compactConsoleToken(`{"alg":"HS256"}`, string(payload)+`{}`)},
		{"oversized", strings.Repeat("x", (16<<10)+1)},
		{"invalid compact token", "not-a-token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/approvals", nil)
			r.Header.Set("X-authentik-username", "owner")
			r.Header.Set("X-authentik-jwt", tc.token)
			if _, err := a.Authenticate(r); err == nil {
				t.Error("malformed upstream token accepted")
			}
		})
	}
	for _, header := range []string{"X-authentik-username", "X-authentik-jwt"} {
		r := httptest.NewRequest("GET", "/approvals", nil)
		r.Header.Set("X-authentik-username", "owner")
		r.Header.Set("X-authentik-jwt", consoleToken(claims))
		r.Header.Add(header, r.Header.Get(header))
		if _, err := a.Authenticate(r); err == nil {
			t.Errorf("duplicate %s accepted", header)
		}
	}
}

func TestConsoleFreshLogin(t *testing.T) {
	now := time.Date(2026, 10, 8, 1, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		at   time.Time
		want bool
	}{
		{time.Time{}, false}, {now.Add(-5 * time.Minute), true}, {now.Add(-5*time.Minute - time.Second), false},
		{now.Add(time.Second), false}, {now, true},
	} {
		if got := (ConsoleIdentity{AuthTime: tc.at}).fresh(now); got != tc.want {
			t.Errorf("login %s: fresh=%v, want %v", tc.at, got, tc.want)
		}
	}
}
