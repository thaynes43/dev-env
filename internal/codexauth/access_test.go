package codexauth

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestCodexAccessRejectsAmbiguousAndBoundedDocuments(t *testing.T) {
	for _, raw := range []string{`{"x":1,"x":2}`, `{"x":{"y":1,"y":2}}`, `{"x":1} {}`, strings.Repeat("[", 20) + "1" + strings.Repeat("]", 20), strings.Repeat("a", MaxBytes+1)} {
		var x struct {
			X any `json:"x"`
		}
		if DecodeStrict([]byte(raw), &x) == nil {
			t.Fatal("ambiguous or unbounded input accepted")
		}
	}
	for _, payload := range []string{`{"exp":1,"exp":2,"https://api.openai.com/auth":{"chatgpt_account_id":"synthetic"}}`, `{"exp":-1}`, `{"exp":"synthetic-canary"}`} {
		jwt := "e30." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".synthetic"
		if _, _, err := Claims(jwt); err == nil || strings.Contains(err.Error(), "canary") {
			t.Fatal("invalid JWT accepted or leaked")
		}
	}
}

func TestCodexAccessExpiryAndMetadataSkew(t *testing.T) {
	now := time.Date(2026, 10, 9, 20, 0, 0, 0, time.UTC)
	claims, _ := json.Marshal(map[string]any{"exp": now.Add(time.Hour).Unix(), "https://api.openai.com/auth": map[string]string{"chatgpt_account_id": "synthetic"}})
	jwt := "e30." + base64.RawURLEncoding.EncodeToString(claims) + ".synthetic"
	a := Access{Generation: 1, AccountID: "synthetic", IDToken: jwt, AccessToken: jwt, ExpiresAt: now.Add(time.Hour), LastRefresh: now}
	if a.Validate(now) != nil {
		t.Fatal("valid access rejected")
	}
	a.LastRefresh = now.Add(2 * time.Minute)
	if a.Validate(now) == nil {
		t.Fatal("future metadata accepted")
	}
	a.LastRefresh = now
	a.ExpiresAt = now.Add(2 * time.Hour)
	if a.Validate(now) == nil {
		t.Fatal("expiry disagreed with JWT")
	}
	a.ExpiresAt = now.Add(time.Hour)
	if a.Validate(now.Add(time.Hour)) == nil {
		t.Fatal("expired access accepted")
	}
}
