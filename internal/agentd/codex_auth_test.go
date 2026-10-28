package agentd

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
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

// The child process uses synthetic fixtures and the same fence as its parent.
func TestCodexAccessProcessFixture(t *testing.T) {
	home := os.Getenv("CODEX_TEST_FENCE_HOME")
	if home == "" {
		return
	}
	s := Settings{Home: home, StateDir: filepath.Join(home, ".agentd"), CodexHome: filepath.Join(home, "observer"), CodexAccessFile: filepath.Join(home, "projection.json")}
	if os.Getenv("CODEX_TEST_LOCK_EXIT") == "1" {
		f, err := lockCodexAccess(s)
		if err != nil {
			t.Fatal(err)
		}
		_ = f // deliberately left open until this subprocess exits
		return
	}
	if os.Getenv("CODEX_TEST_CONTENDED") == "1" {
		fd, err := syscall.Open(s.statePath("codex-access-generation.lock"), syscall.O_RDWR|syscall.O_NOFOLLOW, 0)
		if err != nil {
			t.Fatal(err)
		}
		err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		_ = syscall.Close(fd)
		if err != syscall.EWOULDBLOCK {
			t.Fatal("expected actual contention", err)
		}
		fmt.Println("contended")
	}
	err := SyncCodexAccess(s, time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC))
	if (err != nil) != (os.Getenv("CODEX_TEST_EXPECT_REFUSAL") == "1") {
		t.Fatal("unexpected synthetic child outcome")
	}
}

func TestCodexAccessCrossProcessFence(t *testing.T) {
	home := t.TempDir()
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	s := Settings{Home: home, StateDir: filepath.Join(home, ".agentd"), CodexHome: filepath.Join(home, "native"), CodexAccessFile: filepath.Join(home, "projection.json")}
	writeProjection := func(g uint64) {
		t.Helper()
		raw, err := codexauth.Encode(syntheticAgentCodexAccess(t, now, g), now)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, s.CodexAccessFile, string(raw))
	}
	runChild := func(refuse bool) {
		t.Helper()
		cmd := exec.Command(os.Args[0], "-test.run=^TestCodexAccessProcessFixture$")
		want := "0"
		if refuse {
			want = "1"
		}
		cmd.Env = append(os.Environ(), "CODEX_TEST_FENCE_HOME="+home, "CODEX_TEST_EXPECT_REFUSAL="+want)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("synthetic child: %v %s", err, out)
		}
	}
	writeProjection(1)
	if err := SyncCodexAccess(s, now); err != nil {
		t.Fatal(err)
	}
	lock, err := lockCodexAccess(s)
	if err != nil {
		t.Fatal(err)
	}
	writeProjection(2)
	priorFence, err := os.ReadFile(s.statePath("codex-access-generation.json"))
	if err != nil {
		t.Fatal(err)
	}
	runChild(true) // bounded timeout: writer must not overwrite while held
	afterTimeout, err := os.ReadFile(s.statePath("codex-access-generation.json"))
	if err != nil || string(afterTimeout) != string(priorFence) {
		t.Fatal("timeout rewrote fence")
	}
	if _, err := os.Stat(filepath.Join(home, "observer", "auth.json")); !os.IsNotExist(err) {
		t.Fatal("timeout wrote observer auth")
	}
	waiting := exec.Command(os.Args[0], "-test.run=^TestCodexAccessProcessFixture$")
	waiting.Env = append(os.Environ(), "CODEX_TEST_FENCE_HOME="+home, "CODEX_TEST_CONTENDED=1")
	stdout, err := waiting.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := waiting.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "contended" {
		t.Fatal("child did not observe contention", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if err := waiting.Wait(); err != nil {
		t.Fatal("waiting writer failed after release", err)
	}
	runChild(false)
	cmd := exec.Command(os.Args[0], "-test.run=^TestCodexAccessProcessFixture$")
	cmd.Env = append(os.Environ(), "CODEX_TEST_FENCE_HOME="+home, "CODEX_TEST_LOCK_EXIT=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("lock child: %v %s", err, out)
	}
	lock, err = lockCodexAccess(s)
	if err != nil {
		t.Fatal("lock not released after process exit")
	}
	_ = lock.Close()
	before, err := os.ReadFile(s.statePath("codex-access-generation.json"))
	if err != nil {
		t.Fatal(err)
	}
	writeProjection(1)
	if err := SyncCodexAccess(s, now); err == nil {
		t.Fatal("older generation accepted")
	}
	after, err := os.ReadFile(s.statePath("codex-access-generation.json"))
	if err != nil || string(before) != string(after) {
		t.Fatal("fence rolled back")
	}
}

func TestCodexAccessLockRefusesUnsafeFile(t *testing.T) {
	for _, kind := range []string{"symlink", "directory", "mode", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			home := t.TempDir()
			s := Settings{Home: home, StateDir: filepath.Join(home, ".agentd")}
			if err := os.Mkdir(s.StateDir, 0700); err != nil {
				t.Fatal(err)
			}
			path := s.statePath("codex-access-generation.lock")
			switch kind {
			case "symlink":
				if err := os.Symlink(filepath.Join(home, "target"), path); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "mode":
				if err := os.WriteFile(path, nil, 0644); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				other := filepath.Join(home, "other")
				if err := os.WriteFile(other, nil, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(other, path); err != nil {
					t.Fatal(err)
				}
			}
			if f, err := lockCodexAccess(s); err == nil {
				_ = f.Close()
				t.Fatal("unsafe lock accepted")
			}
		})
	}
}
