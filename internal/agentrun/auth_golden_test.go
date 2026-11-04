package agentrun

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/claudeauth"
)

// agent-run reads the keeper's projection with its own small parser and keeps
// hand-written fixtures. This pins those fixtures to the document the shared
// claudeauth package produces and accepts, so a format change breaks a test here
// and not a pod. The fixture uses only synthetic values.
func TestAuthClaudeFixtureMatchesTheKeeperDocument(t *testing.T) {
	exp := time.Date(2026, 11, 4, 20, 0, 0, 0, time.UTC)
	fixture := authClaudeDoc(7, exp)
	access := claudeauth.Access{Generation: 7, AccessToken: authCanary, ExpiresAt: exp, Scopes: []string{"user:inference"},
		SubscriptionType: "max", RateLimitTier: "t", AccountUUID: "a", OrganizationUUID: "o"}
	golden, err := claudeauth.Encode(access, exp.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	keys := func(raw []byte) []string {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	if !reflect.DeepEqual(keys([]byte(fixture)), keys(golden)) {
		t.Fatalf("fixture members %v differ from the keeper document %v", keys([]byte(fixture)), keys(golden))
	}
	got, err := claudeauth.Decode([]byte(fixture), exp.Add(-time.Hour))
	if err != nil || got.Generation != 7 || !got.ExpiresAt.Equal(exp) || got.AccessToken != authCanary {
		t.Fatalf("the shared decoder does not read the fixture: %v", err)
	}
}
