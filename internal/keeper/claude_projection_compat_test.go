package keeper

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd"
	"github.com/thaynes43/dev-env/internal/claudeauth"
)

// These are the v1 wire bytes for the synthetic adoption and first refresh.
// Keep them independent of the live encoders.
const (
	adoptedAccessGolden   = `{"version":1,"generation":1,"accessToken":"login-access-synthetic-canary","expiresAt":"2026-10-19T20:00:00Z","scopes":["user:profile","user:inference"],"subscriptionType":"max","rateLimitTier":"20x","accountUuid":"acct-1","organizationUuid":"org-1"}`
	adoptedLoginGolden    = `{"version":1,"loginLapsesAt":"2026-11-18T12:00:00Z"}`
	refreshedAccessGolden = `{"version":1,"generation":2,"accessToken":"access-synthetic-canary-2","expiresAt":"2026-10-20T02:00:00Z","scopes":["user:profile","user:inference"],"subscriptionType":"max","rateLimitTier":"20x","accountUuid":"acct-1","organizationUuid":"org-1"}`
	refreshedLoginGolden  = `{"version":1,"loginLapsesAt":"2026-11-18T18:00:00Z"}`
)

func TestClaudeProvenanceProjectionGoldenAndLegacyAgentd(t *testing.T) {
	f := newClaudeFix(t)
	if got := adoptClaudeFixture(t, f); got.Code != "Adopted" {
		t.Fatal("fixture adoption failed")
	}
	home := t.TempDir()
	projection := t.TempDir()
	settings := agentd.Settings{
		ClaudeLiveFile:  filepath.Join(projection, claudeauth.LiveKey),
		ClaudeConfigDir: filepath.Join(home, ".claude"),
		StateDir:        filepath.Join(home, "state"),
		ClaudeState:     filepath.Join(home, ".claude.json"),
	}
	for _, tc := range []struct {
		generation uint64
		access     string
		login      string
	}{
		{1, adoptedAccessGolden, adoptedLoginGolden},
		{2, refreshedAccessGolden, refreshedLoginGolden},
	} {
		if tc.generation == 2 {
			f.clk.Step(6 * time.Hour)
			f.tick()
		}
		journal := readClaudeProvenance(t, f)
		if journal[len(journal)-1].Generation != tc.generation {
			t.Fatal("published generation lacks private provenance")
		}
		live := f.liveSecret(t)
		if len(live.Data) != 2 || !bytes.Equal(live.Data[claudeauth.LiveKey], []byte(tc.access)) ||
			!bytes.Equal(live.Data[claudeauth.LoginKey], []byte(tc.login)) {
			t.Fatal("v1 projection bytes or key set changed")
		}
		for key, raw := range live.Data {
			if err := os.WriteFile(filepath.Join(projection, key), raw, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		// This is the production agentd reader from the pre-slice base.
		// It consumes both generations with the keeper's journal key present.
		if err := agentd.SyncClaudeAccess(settings, f.clk.Now()); err != nil {
			t.Fatal("legacy agentd refused the published access projection")
		}
		if info, err := os.Stat(filepath.Join(settings.ClaudeConfigDir, ".credentials.json")); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatal("legacy agentd did not materialize private credentials")
		}
		if lapse, err := claudeauth.DecodeLogin(live.Data[claudeauth.LoginKey]); err != nil ||
			!lapse.Equal(f.clk.Now().Add(30*24*time.Hour)) {
			t.Fatal("legacy login decoder refused the lapse projection")
		}
	}
}
