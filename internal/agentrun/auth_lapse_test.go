package agentrun

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func authLoginDoc(lapse time.Time) string {
	return `{"version":1,"loginLapsesAt":"` + lapse.UTC().Format(time.RFC3339) + `"}`
}

// The keeper publishes login.json beside access.json; auth status reads it.
func TestAuthStatusShowsTheLoginLapse(t *testing.T) {
	cases := []struct {
		name    string
		login   string // content of login.json, "" for no file
		want    int
		contain []string
		absent  []string
	}{
		{"far away", authLoginDoc(testNow.Add(20*24*time.Hour + time.Hour)), ExitOK, []string{"login lapses 2026-10-26T22:00:00Z (20 days left)"}, []string{"renew from the standby"}},
		{"exactly 7 days", authLoginDoc(testNow.Add(7 * 24 * time.Hour)), 1, []string{"login lapses 2026-10-13T21:00:00Z (7 days left)", "login lapses within 7 days: renew from the standby"}, nil},
		{"just over 7 days", authLoginDoc(testNow.Add(7*24*time.Hour + time.Minute)), ExitOK, []string{"(7 days left)"}, []string{"renew from the standby"}},
		{"under a day", authLoginDoc(testNow.Add(5 * time.Hour)), 1, []string{"(under a day left)", "within 7 days"}, nil},
		{"lapsed", authLoginDoc(testNow.Add(-time.Hour)), 1, []string{"(lapsed)", "login has lapsed: renew from the standby"}, nil},
		{"no file is Unknown", "", ExitOK, []string{"claude  Ready", "login lapses Unknown"}, []string{"login lapses 20"}},
		{"unreadable document is Unknown", `{"version":1,"loginLapsesAt":`, ExitOK, []string{"login lapses Unknown"}, nil},
		{"zero date is Unknown", `{"version":1,"loginLapsesAt":"0001-01-01T00:00:00Z"}`, ExitOK, []string{"login lapses Unknown"}, nil},
		{"unknown member is Unknown", `{"version":1,"loginLapsesAt":"2030-01-01T00:00:00Z","x":1}`, ExitOK, []string{"login lapses Unknown"}, []string{"2030"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			hermeticAuth(t, h)
			h.vars["AGENTD_CLAUDE_LIVE_FILE"] = h.write("claude/access.json", authClaudeDoc(7, testNow.Add(5*time.Hour)))
			if tc.login != "" {
				h.write("claude/login.json", tc.login)
			}
			h.mustRun(tc.want, "auth", "status")
			out := h.stdout.String()
			for _, w := range tc.contain {
				if !strings.Contains(out, w) {
					t.Errorf("stdout lacks %q:\n%s", w, out)
				}
			}
			for _, w := range tc.absent {
				if strings.Contains(out, w) {
					t.Errorf("stdout has %q:\n%s", w, out)
				}
			}
			h.mustRun(tc.want, "auth", "status", "-o", "json")
			var rep authReport
			if err := json.Unmarshal(h.stdout.Bytes(), &rep); err != nil {
				t.Fatal(err)
			}
			c := rep.Logins[0]
			if tc.contain[0] == "login lapses Unknown" || strings.Contains(strings.Join(tc.contain, " "), "lapses Unknown") {
				if c.LoginLapse != "Unknown" || c.LoginLapsesAt != nil || c.LoginDaysLeft != nil {
					t.Errorf("Unknown row = %+v", c)
				}
			} else if c.LoginLapsesAt == nil || c.LoginDaysLeft == nil {
				t.Errorf("known row = %+v", c)
			}
			if strings.Contains(out+h.stdout.String()+h.stderr.String(), authCanary) {
				t.Fatal("a token value was printed")
			}
		})
	}
}

func TestAuthStatusCodexLapseIsUnknown(t *testing.T) {
	h := newHarness(t)
	hermeticAuth(t, h)
	h.vars["AGENTD_CODEX_ACCESS_FILE"] = h.write("codex/access.json", authCodexDoc(3, testNow.Add(48*time.Hour), testNow))
	// A login.json beside Codex's file must not be read as its lapse.
	h.write("codex/login.json", authLoginDoc(testNow.Add(time.Hour)))
	h.mustRun(ExitOK, "auth", "status")
	if out := h.stdout.String(); !strings.Contains(out, "codex   Ready") || !strings.Contains(out, "login lapses Unknown") || strings.Contains(out, "within 7 days") {
		t.Fatalf("codex row:\n%s", out)
	}
}

func TestKeeperLoginStatusLapseExitCode(t *testing.T) {
	status := func(login *time.Time) func(w http.ResponseWriter, r *http.Request, _ []byte) {
		access := testNow.Add(3 * time.Hour)
		return func(w http.ResponseWriter, r *http.Request, _ []byte) {
			writeTestJSON(w, http.StatusOK, apiv1.KeeperLoginStatus{Provider: "claude", State: "Ready", Generation: 2, AccessExpiresAt: &access, LoginExpiresAt: login})
		}
	}
	h := newHarness(t)
	far := testNow.Add(20 * 24 * time.Hour)
	h.api.handle = status(&far)
	h.mustRun(ExitOK, "keeper-login", "claude", "status")
	if strings.Contains(h.stdout.String(), "renew it from the standby") {
		t.Fatal("renewal hint shown for a far lapse")
	}
	soon := testNow.Add(7 * 24 * time.Hour)
	h.api.handle = status(&soon)
	h.mustRun(1, "keeper-login", "claude", "status")
	contains(t, "7 days", h.stdout.String(), "login expires", "in 7 days", "renew it from the standby: agent-run keeper-login claude url")
	past := testNow.Add(-time.Hour)
	h.api.handle = status(&past)
	h.mustRun(1, "keeper-login", "claude", "status")
	contains(t, "lapsed", h.stdout.String(), "lapsed")
	h.mustRun(1, "keeper-login", "claude", "status", "-o", "json")

	// Ready with no date: Unknown, never guessed, and not a failure.
	h.api.handle = status(nil)
	h.mustRun(ExitOK, "keeper-login", "claude", "status")
	contains(t, "unknown", h.stdout.String(), "login expires        Unknown (the provider gave no date)")
}
