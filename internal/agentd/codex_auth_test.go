package agentd

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/codexauth"
)

func syntheticAgentCodexAccess(t *testing.T, now time.Time, generation uint64) codexauth.Access {
	t.Helper()
	// JWT exp has second precision; its publication timestamp must match exactly.
	exp := now.Add(10 * 24 * time.Hour).Truncate(time.Second)
	raw, _ := json.Marshal(map[string]any{"exp": exp.Unix(), "https://api.openai.com/auth": map[string]string{"chatgpt_account_id": "synthetic-account"}})
	jwt := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"synthetic"}`)) + "." + base64.RawURLEncoding.EncodeToString(raw) + ".synthetic-signature"
	return codexauth.Access{Generation: generation, AccountID: "synthetic-account", IDToken: jwt, AccessToken: jwt, ExpiresAt: exp, LastRefresh: now}
}

func TestCodexAccessAtomicHotSwapPreservesProviderState(t *testing.T) {
	home := t.TempDir()
	s := Settings{Home: home, StateDir: filepath.Join(home, ".agentd"), CodexHome: filepath.Join(home, ".codex"), CodexAccessFile: filepath.Join(home, "projection", "access.json")}
	if err := os.MkdirAll(s.CodexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"state.sqlite", "config.toml", "models_cache.json", "remote-enrollment"} {
		writeFile(t, filepath.Join(s.CodexHome, name), "provider-state-synthetic-canary")
	}
	now := time.Date(2026, 10, 9, 20, 0, 0, 0, time.UTC)
	one := syntheticAgentCodexAccess(t, now, 1)
	raw, err := codexauth.Encode(one, now)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, s.CodexAccessFile, string(raw))
	if err := SyncCodexAccess(s, now); err != nil {
		t.Fatal(err)
	}
	authPath := filepath.Join(s.CodexHome, "auth.json")
	oldFile, err := os.Open(authPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = oldFile.Close() }()
	old, _ := os.ReadFile(authPath)
	if err := os.Chmod(authPath, 0o644); err != nil {
		t.Fatal(err)
	}
	two := syntheticAgentCodexAccess(t, now.Add(time.Hour), 2)
	raw, err = codexauth.Encode(two, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, s.CodexAccessFile, string(raw))
	if err := SyncCodexAccess(s, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	stillOld, err := io.ReadAll(oldFile)
	if err != nil || string(stillOld) != string(old) {
		t.Fatal("reader saw an in-place credential rewrite")
	}
	current, err := os.ReadFile(authPath)
	if err != nil || string(current) == string(old) {
		t.Fatal("new generation did not replace auth")
	}
	fi, err := os.Stat(authPath)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatal("native auth is not private")
	}
	var native struct {
		Mode   string `json:"auth_mode"`
		Tokens struct {
			ID      string `json:"id_token"`
			Access  string `json:"access_token"`
			Refresh string `json:"refresh_token"`
			Account string `json:"account_id"`
		} `json:"tokens"`
	}
	if json.Unmarshal(current, &native) != nil || native.Mode != "chatgpt" || native.Tokens.Refresh != "" || native.Tokens.Access != two.AccessToken || native.Tokens.ID != two.IDToken || native.Tokens.Account != two.AccountID || !strings.Contains(string(current), `"refresh_token":""`) {
		t.Fatal("native access-only contract drift")
	}
	for _, name := range []string{"state.sqlite", "config.toml", "models_cache.json", "remote-enrollment"} {
		data, _ := os.ReadFile(filepath.Join(s.CodexHome, name))
		if string(data) != "provider-state-synthetic-canary" {
			t.Fatal("hot swap touched provider state")
		}
	}
	// No provider CLI or daemon restart is part of SyncCodexAccess. Same
	// generation is idempotent, and the private receipt survives process restart.
	if err := SyncCodexAccess(s, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	raw, _ = codexauth.Encode(one, now.Add(time.Hour))
	writeFile(t, s.CodexAccessFile, string(raw))
	if SyncCodexAccess(s, now.Add(time.Hour)) == nil {
		t.Fatal("older generation overwrote a running host")
	}
	after, _ := os.ReadFile(authPath)
	if string(after) != string(current) {
		t.Fatal("rollback changed auth")
	}
}

func TestCodexAccessRejectsInvalidProjectionWithoutOverwriting(t *testing.T) {
	for _, kind := range []string{"expired", "near-expiry", "refresh-field", "changed-generation", "account-mismatch", "malformed", "oversized", "fifo", "provider-home-symlink"} {
		t.Run(kind, func(t *testing.T) {
			home := t.TempDir()
			s := Settings{Home: home, StateDir: filepath.Join(home, ".agentd"), CodexHome: filepath.Join(home, ".codex"), CodexAccessFile: filepath.Join(home, "access.json")}
			now := time.Date(2026, 10, 9, 20, 0, 0, 0, time.UTC)
			a := syntheticAgentCodexAccess(t, now, 1)
			raw, _ := codexauth.Encode(a, now)
			writeFile(t, s.CodexAccessFile, string(raw))
			if err := SyncCodexAccess(s, now); err != nil {
				t.Fatal(err)
			}
			authPath := filepath.Join(s.CodexHome, "auth.json")
			before, _ := os.ReadFile(authPath)
			var doc map[string]any
			_ = json.Unmarshal(raw, &doc)
			at := now
			switch kind {
			case "expired":
				at = a.ExpiresAt.Add(time.Second)
			case "near-expiry":
				at = a.ExpiresAt.Add(-4 * time.Minute)
			case "refresh-field":
				doc["refresh_token"] = "synthetic-refresh-canary"
			case "changed-generation":
				doc["last_refresh"] = now.Add(time.Minute).Format(time.RFC3339)
			case "account-mismatch":
				doc["account_id"] = "other-synthetic-account"
			case "malformed":
				raw = []byte("malformed-synthetic-canary")
			case "oversized":
				raw = []byte(strings.Repeat("a", codexauth.MaxBytes+1))
			case "fifo":
				if err := os.Remove(s.CodexAccessFile); err != nil {
					t.Fatal(err)
				}
				if err := syscall.Mkfifo(s.CodexAccessFile, 0o600); err != nil {
					t.Fatal(err)
				}
			case "provider-home-symlink":
				private := s.CodexHome + "-private"
				if err := os.Rename(s.CodexHome, private); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(private, s.CodexHome); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "refresh-field" || kind == "changed-generation" || kind == "account-mismatch" {
				raw, _ = json.Marshal(doc)
			}
			if kind != "fifo" {
				writeFile(t, s.CodexAccessFile, string(raw))
			}
			start := time.Now()
			err := SyncCodexAccess(s, at)
			if err == nil || strings.Contains(err.Error(), "canary") || time.Since(start) > time.Second {
				t.Fatal("unsafe projection accepted, leaked or blocked")
			}
			after, _ := os.ReadFile(authPath)
			if string(after) != string(before) {
				t.Fatal("failed update overwrote valid auth")
			}
		})
	}
}

func TestCodexAccessDisabledLeavesAuthUntouched(t *testing.T) {
	home := t.TempDir()
	s := Settings{Home: home, CodexHome: filepath.Join(home, ".codex"), StateDir: filepath.Join(home, ".agentd")}
	path := filepath.Join(s.CodexHome, "auth.json")
	writeFile(t, path, "synthetic-existing-provider-state")
	if SyncCodexAccess(s, time.Now()) != nil || renderCodexAccess(s, time.Now()).State != StepSkip {
		t.Fatal("disabled mode read a projection")
	}
	after, _ := os.ReadFile(path)
	if string(after) != "synthetic-existing-provider-state" {
		t.Fatal("disabled mode modified auth")
	}
}
