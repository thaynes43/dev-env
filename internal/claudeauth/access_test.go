package claudeauth

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// Obvious fake values: nothing here is a credential.
const (
	canaryAccess  = "sk-canary-access-0123456789abcdef"
	canaryRefresh = "sk-canary-refresh-0123456789abcdef"
	canaryMCP     = "canary-mcp-token"
)

var now = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

func access(gen uint64, token string) Access {
	return Access{Generation: gen, AccessToken: token, ExpiresAt: now.Add(8 * time.Hour),
		Scopes: []string{"user:inference", "user:profile"}, SubscriptionType: "max", RateLimitTier: "default_claude_max_5x",
		AccountUUID: "11111111-1111-4111-8111-111111111111", OrganizationUUID: "22222222-2222-4222-8222-222222222222"}
}

func TestEncodeDecodeRoundTripAndValidation(t *testing.T) {
	raw, err := Encode(access(3, canaryAccess), now)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(raw, now)
	if err != nil || got.Generation != 3 || got.AccessToken != canaryAccess || got.OrganizationUUID == "" || len(got.Scopes) != 2 {
		t.Fatalf("round trip: %v", err)
	}
	bad := map[string]func(*Access){
		"zero generation": func(a *Access) { a.Generation = 0 },
		"no token":        func(a *Access) { a.AccessToken = "" },
		"space in token":  func(a *Access) { a.AccessToken = "a b" },
		"expired":         func(a *Access) { a.ExpiresAt = now },
		"no scopes":       func(a *Access) { a.Scopes = nil },
		"blank scope":     func(a *Access) { a.Scopes = []string{""} },
		"no account":      func(a *Access) { a.AccountUUID = "" },
		"no org":          func(a *Access) { a.OrganizationUUID = "" },
	}
	for name, mut := range bad {
		a := access(1, canaryAccess)
		mut(&a)
		if _, err := Encode(a, now); err == nil {
			t.Errorf("%s: encoded", name)
		}
	}
	// A document that lapsed after publication is refused at decode time.
	if _, err := Decode(raw, now.Add(9*time.Hour)); err == nil {
		t.Error("expired document decoded")
	}
}

func TestDecodeRefusesRefreshTokensAndUnknownMembers(t *testing.T) {
	raw, _ := Encode(access(1, canaryAccess), now)
	for _, member := range []string{"refreshToken", "refresh_token", "RefreshTokenExpiresAt"} {
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		m[member] = canaryRefresh
		doc, _ := json.Marshal(m)
		_, err := Decode(doc, now)
		if !errors.Is(err, ErrRefreshToken) {
			t.Errorf("%s: %v", member, err)
		}
		if err != nil && strings.Contains(err.Error(), canaryRefresh) {
			t.Error("the error carries the token")
		}
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	m["extra"] = 1
	doc, _ := json.Marshal(m)
	if _, err := Decode(doc, now); !errors.Is(err, ErrInvalid) {
		t.Errorf("unknown member: %v", err)
	}
	dup := strings.Replace(string(raw), `"generation":1`, `"generation":1,"generation":2`, 1)
	if _, err := Decode([]byte(dup), now); !errors.Is(err, ErrInvalid) {
		t.Errorf("duplicate member: %v", err)
	}
	if _, err := Decode([]byte(string(raw)+"{}"), now); !errors.Is(err, ErrInvalid) {
		t.Errorf("trailing document: %v", err)
	}
	if _, err := Decode(nil, now); !errors.Is(err, ErrInvalid) {
		t.Errorf("empty: %v", err)
	}
}

func TestAccessIsRedacted(t *testing.T) {
	a := access(1, canaryAccess)
	var sb strings.Builder
	slog.New(slog.NewTextHandler(&sb, nil)).Info("x", "a", a)
	j, _ := json.Marshal(map[string]any{"a": a})
	out := fmt.Sprintf("%v %+v %#v %s", a, a, a, a) + sb.String() + string(j)
	if strings.Contains(out, canaryAccess) || !strings.Contains(out, "REDACTED") {
		t.Errorf("not redacted: %s", out)
	}
}

const podCredentials = `{"claudeAiOauth":{"accessToken":"old","expiresAt":1,"scopes":["x"],"subscriptionType":"pro","rateLimitTier":"t","cliOwned":{"keep":true}},"mcpOAuth":{"srv":{"accessToken":"` + canaryMCP + `"}},"unknownTop":[1,2]}`

func TestMergeKeepsCLIOwnedFieldsAndWritesNoRefresh(t *testing.T) {
	out, changed, err := MergeCredentials([]byte(podCredentials), access(2, canaryAccess))
	if err != nil || !changed {
		t.Fatalf("merge: changed %v err %v", changed, err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(out, &top); err != nil {
		t.Fatal(err)
	}
	var oauth map[string]json.RawMessage
	_ = json.Unmarshal(top["claudeAiOauth"], &oauth)
	if string(oauth["accessToken"]) != `"`+canaryAccess+`"` || string(oauth["expiresAt"]) != fmt.Sprint(now.Add(8*time.Hour).UnixMilli()) ||
		string(oauth["scopes"]) != `["user:inference","user:profile"]` || string(oauth["subscriptionType"]) != `"max"` ||
		string(oauth["rateLimitTier"]) != `"default_claude_max_5x"` {
		t.Errorf("access fields: %v", oauth)
	}
	if string(oauth["cliOwned"]) != `{"keep":true}` || !strings.Contains(string(top["mcpOAuth"]), canaryMCP) || string(top["unknownTop"]) != `[1,2]` {
		t.Errorf("CLI-owned members lost: %s", out)
	}
	if strings.Contains(strings.ToLower(string(out)), "refresh") {
		t.Errorf("a refresh member reached the file: %s", out)
	}
	// Same generation again changes nothing; a new file starts from nothing.
	again, changed, err := MergeCredentials(out, access(2, canaryAccess))
	if err != nil || changed || string(again) != string(out) {
		t.Errorf("not idempotent: changed %v err %v", changed, err)
	}
	fresh, changed, err := MergeCredentials(nil, access(2, canaryAccess))
	if err != nil || !changed || !strings.Contains(string(fresh), canaryAccess) {
		t.Errorf("new file: %v", err)
	}
}

func TestMergeRefusesAPodOwnedLogin(t *testing.T) {
	owned := `{"claudeAiOauth":{"accessToken":"old","refreshToken":"` + canaryRefresh + `"}}`
	out, changed, err := MergeCredentials([]byte(owned), access(1, canaryAccess))
	if !errors.Is(err, ErrPodOwnedLogin) || changed || out != nil || !HoldsRefreshToken([]byte(owned)) {
		t.Fatalf("pod-owned: %v changed %v", err, changed)
	}
	if strings.Contains(err.Error(), canaryRefresh) {
		t.Error("the error carries the token")
	}
	for _, empty := range []string{`"refreshToken":""`, `"refreshToken":null`} {
		doc := `{"claudeAiOauth":{` + empty + `}}`
		if HoldsRefreshToken([]byte(doc)) {
			t.Errorf("%s counts as a login", empty)
		}
		if _, _, err := MergeCredentials([]byte(doc), access(1, canaryAccess)); err != nil {
			t.Errorf("%s: %v", empty, err)
		}
	}
	if _, _, err := MergeCredentials([]byte(`not json`), access(1, canaryAccess)); !errors.Is(err, ErrInvalid) {
		t.Errorf("garbage: %v", err)
	}
}
