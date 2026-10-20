package claudeauth

import (
	"strings"
	"testing"
	"time"
)

func TestLoginDocumentRoundTrip(t *testing.T) {
	lapse := time.Date(2026, 11, 16, 17, 51, 0, 0, time.UTC)
	raw, err := EncodeLogin(lapse)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"version":1,"loginLapsesAt":"2026-11-16T17:51:00Z"}` {
		t.Fatalf("canonical form changed: %s", raw)
	}
	got, err := DecodeLogin(raw)
	if err != nil || !got.Equal(lapse) {
		t.Fatalf("round trip: %v %v", got, err)
	}
	if strings.Contains(strings.ToLower(string(raw)), "refresh") {
		t.Fatal("the document must not name a refresh member")
	}
}

func TestLoginDocumentRefusals(t *testing.T) {
	if _, err := EncodeLogin(time.Time{}); err == nil {
		t.Fatal("an unknown lapse must not be encoded")
	}
	if _, err := EncodeLogin(time.Unix(0, 0)); err == nil {
		t.Fatal("the epoch must not be encoded")
	}
	for name, raw := range map[string]string{
		"empty":          ``,
		"version":        `{"version":2,"loginLapsesAt":"2026-11-16T17:51:00Z"}`,
		"no time":        `{"version":1}`,
		"zero time":      `{"version":1,"loginLapsesAt":"0001-01-01T00:00:00Z"}`,
		"unknown member": `{"version":1,"loginLapsesAt":"2026-11-16T17:51:00Z","other":1}`,
		"refresh member": `{"version":1,"loginLapsesAt":"2026-11-16T17:51:00Z","refreshToken":"x"}`,
		"duplicate":      `{"version":1,"loginLapsesAt":"2026-11-16T17:51:00Z","loginLapsesAt":"2026-11-17T17:51:00Z"}`,
		"trailing":       `{"version":1,"loginLapsesAt":"2026-11-16T17:51:00Z"} {}`,
	} {
		if _, err := DecodeLogin([]byte(raw)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// The lapse document is a second key precisely so access.json is unchanged for
// an agentd that decodes it strictly: the access document must still refuse a
// loginLapsesAt member, and must stay byte-identical with or without a lapse.
func TestAccessDocumentIsUnchangedByTheLapseKey(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	a := Access{Generation: 3, AccessToken: "tok", ExpiresAt: now.Add(8 * time.Hour), Scopes: []string{"user:profile"}, AccountUUID: "a", OrganizationUUID: "o"}
	raw, err := Encode(a, now)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "loginLapsesAt") {
		t.Fatal("access.json must not carry the lapse date")
	}
	withLapse := strings.Replace(string(raw), `"version":1`, `"version":1,"loginLapsesAt":"2026-11-16T17:51:00Z"`, 1)
	if _, err := Decode([]byte(withLapse), now); err == nil {
		t.Fatal("an older strict decoder semantics changed: access.json must keep refusing unknown members")
	}
}
