package agentrun

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// An access token that expired before the keeper's next generation is visible
// is a pending refresh, not a login a person must renew (tracker story 385).
// Times are relative to t0, the access expiry; documents are synthetic.
func TestAuthStatusPendingRefreshIsNotNeedsLogin(t *testing.T) {
	cases := []struct {
		name    string
		expired time.Duration // access expired this long ago
		lapse   string        // login.json, "" for none
		state   string
		reason  string
	}{
		{"one second", time.Second, authLoginDoc(testNow.Add(20 * 24 * time.Hour)), "Refreshing", "expired 1s ago; the keeper's next generation is pending"},
		{"59 seconds", 59 * time.Second, authLoginDoc(testNow.Add(20 * 24 * time.Hour)), "Refreshing", "expired 59s ago; the keeper's next generation is pending"},
		{"lapse unknown", 59 * time.Second, "", "Refreshing", "the keeper's next generation is pending"},
		{"past the pending window", 10 * time.Minute, authLoginDoc(testNow.Add(20 * 24 * time.Hour)), "NeedsLogin", "access has expired and the keeper has published no newer generation"},
		{"login lapsed", 59 * time.Second, authLoginDoc(testNow.Add(-time.Hour)), "NeedsLogin", "login has lapsed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			hermeticAuth(t, h)
			h.vars["AGENTD_CLAUDE_LIVE_FILE"] = h.write("claude/access.json", authClaudeDoc(7, testNow.Add(-tc.expired)))
			if tc.lapse != "" {
				h.write("claude/login.json", tc.lapse)
			}
			h.mustRun(1, "auth", "status")
			out := h.stdout.String()
			if !strings.Contains(out, "claude  "+tc.state) || !strings.Contains(out, tc.reason) {
				t.Errorf("want %s / %q:\n%s", tc.state, tc.reason, out)
			}
			h.mustRun(1, "auth", "status", "-o", "json")
			var rep authReport
			if err := json.Unmarshal(h.stdout.Bytes(), &rep); err != nil || rep.Logins[0].State != tc.state {
				t.Errorf("json state %v: %v", rep.Logins, err)
			}
			if strings.Contains(out+h.stdout.String(), authCanary) {
				t.Fatal("a token value was printed")
			}
		})
	}
}

// The generation that lands clears the pending state with no other change.
func TestAuthStatusNextGenerationClearsPending(t *testing.T) {
	h := newHarness(t)
	hermeticAuth(t, h)
	h.vars["AGENTD_CLAUDE_LIVE_FILE"] = h.write("claude/access.json", authClaudeDoc(7, testNow.Add(-59*time.Second)))
	h.write("claude/login.json", authLoginDoc(testNow.Add(20*24*time.Hour)))
	h.mustRun(1, "auth", "status")
	h.write("claude/access.json", authClaudeDoc(8, testNow.Add(5*time.Hour)))
	h.mustRun(0, "auth", "status")
	if out := h.stdout.String(); !strings.Contains(out, "claude  Ready  generation 8") {
		t.Errorf("after generation 8:\n%s", out)
	}
}
