package agentrun

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// authCanary stands in for every token value a projection carries. It must
// never reach stdout or stderr.
const authCanary = "CANARY-token-9f3a7c21"

func authClaudeDoc(gen int, exp time.Time) string {
	return fmt.Sprintf(`{"version":1,"generation":%d,"accessToken":%q,"expiresAt":%q,"scopes":["user:inference"],`+
		`"subscriptionType":"max","rateLimitTier":"t","accountUuid":"a","organizationUuid":"o"}`,
		gen, authCanary, exp.UTC().Format(time.RFC3339))
}

func authCodexDoc(gen int, exp, refreshed time.Time) string {
	return fmt.Sprintf(`{"version":1,"generation":%d,"account_id":"acct","id_token":%q,"access_token":%q,"exp":%q,"last_refresh":%q}`,
		gen, authCanary, authCanary, exp.UTC().Format(time.RFC3339), refreshed.UTC().Format(time.RFC3339))
}

func TestAuthStatus(t *testing.T) {
	cases := []struct {
		name         string
		claude, cdx  func(h *harness) string // the file path, "" for unset
		want         int
		wantContains []string
	}{
		{
			name:   "both fresh",
			claude: func(h *harness) string { return h.write("claude.json", authClaudeDoc(7, testNow.Add(5*time.Hour))) },
			cdx: func(h *harness) string {
				return h.write("codex.json", authCodexDoc(3, testNow.Add(48*time.Hour), testNow.Add(-time.Hour)))
			},
			want: ExitOK,
			wantContains: []string{
				"claude  Ready  generation 7  access expires 2026-10-07T02:00:00Z (5h0m0s left)",
				"codex   Ready  generation 3  access expires 2026-10-08T21:00:00Z (48h0m0s left)  keeper last refresh 2026-10-06T20:00:00Z",
			},
		},
		{
			name:         "near expiry",
			claude:       func(h *harness) string { return h.write("claude.json", authClaudeDoc(7, testNow.Add(10*time.Minute))) },
			cdx:          func(h *harness) string { return "" },
			want:         1,
			wantContains: []string{"claude  Ready", "10m0s left", "within the warning window", "codex   NotConfigured"},
		},
		{
			name:   "expired",
			claude: func(h *harness) string { return "" },
			cdx: func(h *harness) string {
				return h.write("codex.json", authCodexDoc(3, testNow.Add(-2*time.Hour), testNow.Add(-50*time.Hour)))
			},
			want:         1,
			wantContains: []string{"codex   NeedsLogin", "expired 2h0m0s ago", "claude  NotConfigured"},
		},
		{
			name:   "expired outranks missing",
			claude: func(h *harness) string { return filepath.Join(h.dir, "absent.json") },
			cdx: func(h *harness) string {
				return h.write("codex.json", authCodexDoc(3, testNow.Add(-time.Minute), testNow.Add(-time.Hour)))
			},
			want:         1,
			wantContains: []string{"claude  Unknown", "projection file is missing", "codex   NeedsLogin"},
		},
		{
			name:   "missing",
			claude: func(h *harness) string { return filepath.Join(h.dir, "absent.json") },
			cdx: func(h *harness) string {
				return h.write("codex.json", authCodexDoc(3, testNow.Add(24*time.Hour), testNow))
			},
			want:         2,
			wantContains: []string{"claude  Unknown", "projection file is missing", "codex   Ready"},
		},
		{
			name:         "unreadable directory",
			claude:       func(h *harness) string { return h.dir },
			cdx:          func(h *harness) string { return "" },
			want:         2,
			wantContains: []string{"claude  Unknown", "projection file is unreadable"},
		},
		{
			name:         "invalid document",
			claude:       func(h *harness) string { return h.write("claude.json", `{"version":2,"accessToken":"`+authCanary+`"`) },
			cdx:          func(h *harness) string { return "" },
			want:         2,
			wantContains: []string{"claude  Unknown", "not a valid version 1 document"},
		},
		{
			name:         "no generation",
			claude:       func(h *harness) string { return h.write("claude.json", authClaudeDoc(0, testNow.Add(time.Hour))) },
			cdx:          func(h *harness) string { return "" },
			want:         2,
			wantContains: []string{"claude  Unknown", "not a valid version 1 document"},
		},
		{
			name:         "nothing configured",
			claude:       func(h *harness) string { return "" },
			cdx:          func(h *harness) string { return "" },
			want:         2,
			wantContains: []string{"claude  NotConfigured", "codex   NotConfigured"},
		},
	}
	for _, tc := range cases {
		for _, viaEnv := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s env=%v", tc.name, viaEnv), func(t *testing.T) {
				h := newHarness(t)
				claude, codex := tc.claude(h), tc.cdx(h)
				args := []string{"auth", "status"}
				if viaEnv {
					h.vars["AGENTD_CLAUDE_LIVE_FILE"], h.vars["AGENTD_CODEX_ACCESS_FILE"] = claude, codex
				} else {
					if claude != "" {
						args = append(args, "--claude-file", claude)
					}
					if codex != "" {
						args = append(args, "--codex-file", codex)
					}
				}
				h.mustRun(tc.want, args...)
				out := h.stdout.String()
				for _, w := range tc.wantContains {
					if !strings.Contains(out, w) {
						t.Errorf("stdout lacks %q:\n%s", w, out)
					}
				}
				if strings.Contains(out+h.stderr.String(), authCanary) {
					t.Fatalf("a token value was printed:\n%s\n%s", out, h.stderr.String())
				}
				// JSON carries the same facts and also no token.
				h.mustRun(tc.want, append(args, "-o", "json")...)
				var rep authReport
				if err := json.Unmarshal(h.stdout.Bytes(), &rep); err != nil || len(rep.Logins) != 2 {
					t.Fatalf("json output: %v\n%s", err, h.stdout.String())
				}
				if strings.Contains(h.stdout.String()+h.stderr.String(), authCanary) {
					t.Fatal("a token value was printed in JSON")
				}
				if h.api.requests() != nil {
					t.Error("auth status called the API")
				}
			})
		}
	}
}

func TestAuthStatusWarnWindow(t *testing.T) {
	h := newHarness(t)
	h.vars["AGENTD_CLAUDE_LIVE_FILE"] = h.write("claude.json", authClaudeDoc(1, testNow.Add(2*time.Hour)))
	h.mustRun(ExitOK, "auth", "status")
	h.mustRun(1, "auth", "status", "--warn", "3h")
	h.mustRun(ExitUsage, "auth", "status", "--warn", "-1m")
}

func TestAuthStatusJSONFields(t *testing.T) {
	h := newHarness(t)
	h.vars["AGENTD_CODEX_ACCESS_FILE"] = h.write("codex.json", authCodexDoc(4, testNow.Add(time.Hour), testNow.Add(-time.Minute)))
	h.mustRun(ExitOK, "auth", "status", "-o", "json")
	var rep authReport
	if err := json.Unmarshal(h.stdout.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	c := rep.Logins[1]
	if c.Provider != "codex" || c.State != "Ready" || c.Generation != 4 || *c.SecondsLeft != 3600 || c.LastRefresh == nil {
		t.Fatalf("codex row: %+v", c)
	}
	if rep.Logins[0].State != "NotConfigured" {
		t.Fatalf("claude row: %+v", rep.Logins[0])
	}
}

func TestAuthStatusUsage(t *testing.T) {
	h := newHarness(t)
	h.mustRun(ExitUsage, "auth")
	h.mustRun(ExitUsage, "auth", "login")
	h.mustRun(ExitUsage, "auth", "status", "extra")
	h.mustRun(ExitOK, "auth", "-h")
	if !strings.Contains(h.stdout.String(), "agent-run auth status") {
		t.Fatalf("no help:\n%s", h.stdout.String())
	}
}

func TestAuthStatusRefusesOversizeAndFollowsLinks(t *testing.T) {
	h := newHarness(t)
	big := h.write("big.json", strings.Repeat(" ", authMaxBytes+1))
	h.vars["AGENTD_CLAUDE_LIVE_FILE"] = big
	h.mustRun(2, "auth", "status")
	// A mounted Secret is a link into its data directory.
	real := h.write("data/access.json", authClaudeDoc(2, testNow.Add(time.Hour)))
	link := filepath.Join(h.dir, "access.json")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	h.vars["AGENTD_CLAUDE_LIVE_FILE"] = link
	h.mustRun(ExitOK, "auth", "status")
}
