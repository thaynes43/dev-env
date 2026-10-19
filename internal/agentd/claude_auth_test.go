package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/claudeauth"
)

// Story110 T09 increment 1: the projection consumer. Every value is an
// obvious fake; the canaries must never reach a status, log line or error.

const (
	projAccount = "11111111-1111-4111-8111-111111111111"
	projOrg     = "22222222-2222-4222-8222-222222222222"
	projMCP     = "canary-mcp-token"
)

func projToken(gen uint64) string { return fmt.Sprintf("sk-canary-access-gen%d-0123456789abcdef", gen) }

func projAccess(gen uint64, life time.Duration) claudeauth.Access {
	return claudeauth.Access{Generation: gen, AccessToken: projToken(gen), ExpiresAt: loginNow.Add(life),
		Scopes: []string{"user:inference", "user:profile"}, SubscriptionType: "max", RateLimitTier: "default_claude_max_5x",
		AccountUUID: projAccount, OrganizationUUID: projOrg}
}

// publish writes the keeper's projection the way a Secret volume updates it.
func publish(t *testing.T, path string, a claudeauth.Access) {
	t.Helper()
	raw, err := claudeauth.Encode(a, loginNow)
	if err != nil {
		t.Fatal(err)
	}
	writeRaw(t, path, raw)
}

func writeRaw(t *testing.T, path string, raw []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

// projHome is a pod HOME in projection mode with CLI-owned state in place.
func projHome(t *testing.T, live string) Settings {
	t.Helper()
	s := testSettings(t, t.TempDir())
	s.ClaudeLiveFile = live
	seedCLIState(t, s)
	return s
}

// seedCLIState is what the CLI wrote before agentd's first sync.
func seedCLIState(t *testing.T, s Settings) {
	t.Helper()
	writeJSON(t, filepath.Join(s.ClaudeConfigDir, credentialsFileName), map[string]any{
		"claudeAiOauth": map[string]any{"accessToken": "old", "expiresAt": 1, "scopes": []string{"old"}, "cliOwned": true},
		"mcpOAuth":      map[string]any{"srv": map[string]any{"accessToken": projMCP}},
		"unknownTop":    []int{1},
	})
	if err := os.Chmod(filepath.Join(s.ClaudeConfigDir, credentialsFileName), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readCreds(t *testing.T, s Settings) (map[string]json.RawMessage, string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(s.ClaudeConfigDir, credentialsFileName))
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatal(err)
	}
	return top, string(raw)
}

func assertConverged(t *testing.T, s Settings, gen uint64, life time.Duration) {
	t.Helper()
	top, raw := readCreds(t, s)
	var oauth map[string]json.RawMessage
	_ = json.Unmarshal(top["claudeAiOauth"], &oauth)
	if string(oauth["accessToken"]) != `"`+projToken(gen)+`"` || string(oauth["expiresAt"]) != fmt.Sprint(loginNow.Add(life).UnixMilli()) ||
		string(oauth["scopes"]) != `["user:inference","user:profile"]` || string(oauth["subscriptionType"]) != `"max"` ||
		string(oauth["rateLimitTier"]) != `"default_claude_max_5x"` || string(oauth["cliOwned"]) != "true" {
		t.Errorf("gen %d: access fields %v", gen, oauth)
	}
	if !strings.Contains(string(top["mcpOAuth"]), projMCP) || string(top["unknownTop"]) != "[1]" {
		t.Errorf("gen %d: CLI-owned members lost", gen)
	}
	if strings.Contains(strings.ToLower(raw), "refresh") {
		t.Errorf("gen %d: a refresh member is in the file", gen)
	}
	for g := uint64(1); g < gen; g++ {
		if strings.Contains(raw, projToken(g)) {
			t.Errorf("gen %d: older token %d remains", gen, g)
		}
	}
	if fi, err := os.Stat(filepath.Join(s.ClaudeConfigDir, credentialsFileName)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("gen %d: mode %v err %v", gen, fi.Mode().Perm(), err)
	}
	var st map[string]json.RawMessage
	data, err := os.ReadFile(s.ClaudeState)
	if err != nil || json.Unmarshal(data, &st) != nil {
		t.Fatalf("gen %d: .claude.json: %v", gen, err)
	}
	var acct oauthAccount
	if json.Unmarshal(st[keyOAuthAccount], &acct) != nil || acct.AccountUUID != projAccount || acct.OrganizationUUID != projOrg {
		t.Errorf("gen %d: oauthAccount %s", gen, st[keyOAuthAccount])
	}
	if matches, _ := filepath.Glob(filepath.Join(s.ClaudeConfigDir, ".claude-access-*")); len(matches) != 0 {
		t.Errorf("gen %d: temp files left: %v", gen, matches)
	}
}

func TestClaudeProjectionFollowsThreeGenerationsInThreeHomes(t *testing.T) {
	live := filepath.Join(t.TempDir(), "claude-live", "access.json")
	homes := []Settings{projHome(t, live), projHome(t, live), projHome(t, live)}
	for gen := uint64(1); gen <= 3; gen++ {
		life := time.Duration(gen) * time.Hour
		publish(t, live, projAccess(gen, life))
		for i, s := range homes {
			if err := SyncClaudeAccess(s, loginNow); err != nil {
				t.Fatalf("home %d gen %d: %v", i, gen, err)
			}
			assertConverged(t, s, gen, life)
		}
	}
	// Applying the same generation again changes nothing.
	before, _ := readCreds(t, homes[0])
	if err := SyncClaudeAccess(homes[0], loginNow); err != nil {
		t.Fatal(err)
	}
	after, _ := readCreds(t, homes[0])
	if string(before["claudeAiOauth"]) != string(after["claudeAiOauth"]) {
		t.Error("a repeat sync rewrote the access fields")
	}
}

func TestClaudeProjectionRefusesRollbackAndAlteredGeneration(t *testing.T) {
	live := filepath.Join(t.TempDir(), "access.json")
	s := projHome(t, live)
	publish(t, live, projAccess(1, time.Hour))
	if err := SyncClaudeAccess(s, loginNow); err != nil {
		t.Fatal(err)
	}
	publish(t, live, projAccess(2, 2*time.Hour))
	if err := SyncClaudeAccess(s, loginNow); err != nil {
		t.Fatal(err)
	}
	// An older generation, even with a later expiry, is refused.
	publish(t, live, projAccess(1, 9*time.Hour))
	if err := SyncClaudeAccess(s, loginNow); !errors.Is(err, ErrClaudeAccess) {
		t.Fatalf("rollback: %v", err)
	}
	assertConverged(t, s, 2, 2*time.Hour)
	// The same generation with different content is refused too.
	altered := projAccess(2, 2*time.Hour)
	altered.AccessToken = "sk-canary-other-0123456789abcdef"
	publish(t, live, altered)
	if err := SyncClaudeAccess(s, loginNow); !errors.Is(err, ErrClaudeAccess) {
		t.Fatalf("altered generation: %v", err)
	}
	assertConverged(t, s, 2, 2*time.Hour)
	// The fence survives an agentd restart (a fresh call reads the receipt only).
	publish(t, live, projAccess(1, time.Hour))
	if err := SyncClaudeAccess(s, loginNow); !errors.Is(err, ErrClaudeAccess) {
		t.Fatalf("rollback after restart: %v", err)
	}
	// A newer generation is accepted again.
	publish(t, live, projAccess(3, 3*time.Hour))
	if err := SyncClaudeAccess(s, loginNow); err != nil {
		t.Fatal(err)
	}
	assertConverged(t, s, 3, 3*time.Hour)
}

func TestClaudeProjectionRejectsARefreshTokenAndBadDocuments(t *testing.T) {
	live := filepath.Join(t.TempDir(), "access.json")
	s := projHome(t, live)
	_, original := readCreds(t, s)
	good, _ := claudeauth.Encode(projAccess(1, time.Hour), loginNow)
	withRefresh := strings.Replace(string(good), `"version":1`, `"version":1,"refreshToken":"sk-canary-refresh-0123456789abcdef"`, 1)
	expired, _ := claudeauth.Encode(projAccess(2, time.Hour), loginNow)
	cases := map[string][]byte{
		"refresh token": []byte(withRefresh),
		"unknown field": []byte(strings.Replace(string(good), `"version":1`, `"version":1,"x":1`, 1)),
		"garbage":       []byte("not json"),
		"empty":         nil,
		"oversize":      []byte(strings.Repeat("x", claudeauth.MaxBytes+1)),
		"expired":       expired,
	}
	for name, doc := range cases {
		writeRaw(t, live, doc)
		now := loginNow
		if name == "expired" {
			now = loginNow.Add(2 * time.Hour)
		}
		err := SyncClaudeAccess(s, now)
		if !errors.Is(err, ErrClaudeAccess) {
			t.Errorf("%s: %v", name, err)
		}
		if err != nil && (strings.Contains(err.Error(), "canary") || strings.Contains(err.Error(), "refresh-0123")) {
			t.Errorf("%s: the error carries a token", name)
		}
		if _, raw := readCreds(t, s); raw != original {
			t.Errorf("%s: the credentials changed", name)
		}
		if _, err := os.Stat(s.statePath("claude-access-generation.json")); err == nil {
			t.Errorf("%s: a receipt was written", name)
		}
	}
	// A missing projection is the same fixed error.
	if err := os.Remove(live); err != nil {
		t.Fatal(err)
	}
	if err := SyncClaudeAccess(s, loginNow); !errors.Is(err, ErrClaudeAccess) {
		t.Errorf("missing: %v", err)
	}
}

func TestClaudeProjectionFailsClosedOnAPodOwnedLogin(t *testing.T) {
	live := filepath.Join(t.TempDir(), "access.json")
	s := testSettings(t, t.TempDir())
	s.ClaudeLiveFile = live
	writeCredentials(t, s, loginNow.Add(30*24*time.Hour).UnixMilli()) // holds canaryRefresh
	_, original := readCreds(t, s)
	publish(t, live, projAccess(1, time.Hour))
	if err := SyncClaudeAccess(s, loginNow); !errors.Is(err, ErrClaudeLoginConflict) {
		t.Fatalf("conflict: %v", err)
	}
	if _, raw := readCreds(t, s); raw != original {
		t.Error("the pod-owned credentials were merged into")
	}
	if _, err := os.Stat(s.statePath("claude-access-generation.json")); err == nil {
		t.Error("a receipt was written")
	}
	// A missing projection cannot hide the conflict.
	_ = os.Remove(live)
	if err := SyncClaudeAccess(s, loginNow); !errors.Is(err, ErrClaudeLoginConflict) {
		t.Errorf("conflict without a projection: %v", err)
	}
	// Status and login are projection-aware: not usable, no ceremony.
	if checkLogin(s).Usable(loginNow) {
		t.Error("a pod-owned login counts as usable in projection mode")
	}
	line := LoginStatus(context.Background(), &fakeRunner{}, s, remoteSession(), loginNow)
	if !strings.HasPrefix(line, "login-conflict") || strings.Contains(line, canaryRefresh) || strings.Contains(line, canaryAccess) {
		t.Errorf("status %q", line)
	}

	// The daemon does not start the agent, and starts no login.
	tm := &loginTmux{}
	r := &fakeRunner{handle: tm.handle}
	var logs strings.Builder
	d := &Daemon{S: s, R: r, Log: slog.New(slog.NewTextHandler(&logs, nil)), Session: remoteSession(), Self: "/agentd", Now: func() time.Time { return loginNow }}
	ws := protocol.Workspace{Clone: s.ClonePath("dev-env"), Worktree: s.WorktreePath(d.Session.Name), Branch: "agent/x"}
	step, agentErr := d.startAgent(context.Background(), ws, Step{Name: "repo", State: StepOK}, "b1")
	if !strings.HasPrefix(agentErr, "login-conflict") || step.State != StepFail || d.remote != nil {
		t.Fatalf("step %+v err %q remote %v", step, agentErr, d.remote)
	}
	if _, err := os.Stat(s.statePath(launchFile)); err == nil {
		t.Error("the agent was launched")
	}
	d.syncClaudeAccess()
	d.pollRemoteLogin(context.Background())
	if tm.started != 0 || tm.window {
		t.Error("a login window started")
	}
	for _, l := range r.lines() {
		if strings.Contains(l, "new-session") || strings.Contains(l, "auth login") {
			t.Errorf("the agent or a login started: %s", l)
		}
	}
	if strings.Contains(logs.String(), canaryRefresh) || strings.Contains(logs.String(), canaryAccess) || !strings.Contains(logs.String(), "holds its own login") {
		t.Errorf("logs: %s", logs.String())
	}
}

func TestClaudeProjectionDaemonWaitsThenStartsAndFollows(t *testing.T) {
	live := filepath.Join(t.TempDir(), "claude-live", "access.json")
	s := testSettings(t, t.TempDir())
	s.TmuxBin, s.ClaudeBin, s.Getenv = "tmux", "claude", envOf(nil)
	s.ClaudeLiveFile = live
	seedCLIState(t, s)
	if err := os.MkdirAll(s.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sess := remoteSession()
	tm := &loginTmux{}
	r := &fakeRunner{handle: func(c Cmd) (Result, error) {
		if c.Args[0] == "has-session" && len(c.Args) > 2 && c.Args[2] == "="+TmuxSession {
			return Result{}, &CmdError{Name: "tmux", ExitCode: 1}
		}
		return tm.handle(c)
	}}
	var logs strings.Builder
	d := &Daemon{S: s, R: r, Log: slog.New(slog.NewTextHandler(&logs, nil)), Session: sess, Self: "/agentd", Now: func() time.Time { return loginNow }}
	ws := protocol.Workspace{Clone: s.ClonePath(sess.Repo), Worktree: s.WorktreePath(sess.Name), Branch: "agent/" + sess.Name}
	_ = writeJSONFile(s.statePath(bootFile), bootRecord{BootID: "b1", Boot: protocol.BootReady, Steps: []Step{{Name: "agent", State: StepOK}}})

	// No projection yet: the agent is held, no login is offered.
	step, agentErr := d.startAgent(context.Background(), ws, Step{Name: "repo", State: StepOK}, "b1")
	if agentErr != "" || d.remote == nil || !d.remote.waiting || !strings.Contains(strings.Join(step.Notes, " "), "waiting for the keeper's Claude projection") {
		t.Fatalf("held: %+v %q", step, agentErr)
	}
	st := CollectStatus(context.Background(), r, s, sess, loginNow)
	if st.Agent.State != protocol.AgentLoginRequired || strings.Contains(st.Agent.Error, "agent-run login") {
		t.Errorf("status %+v", st.Agent)
	}
	if _, err := LoginURL(context.Background(), r, s, sess, func() time.Time { return loginNow }); !errors.Is(err, ErrClaudeProjectionLogin) {
		t.Errorf("login url: %v", err)
	}
	if err := LoginCode(context.Background(), r, s, sess, strings.NewReader(canaryCode)); !errors.Is(err, ErrClaudeProjectionLogin) {
		t.Errorf("login code: %v", err)
	}
	d.syncClaudeAccess()
	d.pollRemoteLogin(context.Background())
	if !d.remote.waiting {
		t.Fatal("started without a projection")
	}

	// The keeper publishes generation 1: the next poll merges and starts.
	publish(t, live, projAccess(1, 8*time.Hour))
	d.syncClaudeAccess()
	d.pollRemoteLogin(context.Background())
	if d.remote.waiting {
		t.Fatal("did not start after the projection landed")
	}
	var l Launch
	if err := readJSONFile(s.statePath(launchFile), &l); err != nil || !l.TUI || !strings.Contains(strings.Join(l.Argv, " "), "--remote-control "+sess.Name) {
		t.Fatalf("launch %+v err %v", l, err)
	}
	if !contains(l.Unset, "CLAUDE_CODE_OAUTH_REFRESH_TOKEN") || !contains(l.Unset, "CLAUDE_CODE_OAUTH_TOKEN") {
		t.Errorf("unset %v", l.Unset)
	}
	assertConverged(t, s, 1, 8*time.Hour)
	if line := LoginStatus(context.Background(), r, s, sess, loginNow); !strings.HasPrefix(line, "logged in on the keeper's projection") {
		t.Errorf("status %q", line)
	}

	// New generations are followed while the agent runs.
	publish(t, live, projAccess(2, 9*time.Hour))
	d.syncClaudeAccess()
	assertConverged(t, s, 2, 9*time.Hour)
	// The login lapsing without a successor reports login-required, not a crash.
	late := loginNow.Add(10 * time.Hour)
	if checkLogin(s).Usable(late) {
		t.Error("an expired projected token counts as usable")
	}

	for _, secret := range []string{projToken(1), projToken(2), projMCP, canaryRefresh} {
		if strings.Contains(logs.String(), secret) || strings.Contains(strings.Join(r.lines(), "\n"), secret) {
			t.Errorf("a token reached the logs or a command line")
		}
	}
	out, _ := json.Marshal(CollectStatus(context.Background(), r, s, sess, loginNow))
	if strings.Contains(string(out), "sk-canary") {
		t.Error("a token reached the status")
	}
}

func TestClaudeProjectionOffLeavesEverythingUnchanged(t *testing.T) {
	s := testSettings(t, t.TempDir())
	if s.ClaudeLiveFile != "" {
		t.Fatal("projection mode is on by default")
	}
	if err := SyncClaudeAccess(s, loginNow); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.ClaudeConfigDir); err == nil {
		t.Error("a sync without the variable touched the HOME")
	}
	// The pod-owned login still reads as before.
	writeCredentials(t, s, loginNow.Add(24*time.Hour).UnixMilli())
	if !checkLogin(s).Usable(loginNow) {
		t.Error("the pod-owned login is no longer usable")
	}
	d := &Daemon{S: s, Session: remoteSession()}
	if d.projectsClaude() {
		t.Error("the daemon projects without the variable")
	}
}

func TestUnsetForRemoteDropsBothTokenVariables(t *testing.T) {
	for _, k := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_OAUTH_REFRESH_TOKEN"} {
		if !contains(unsetForRemote, k) || !contains(unsetFor(remoteSession()), k) {
			t.Errorf("%s is not unset for a remote session", k)
		}
		if contains(unsetFor(protocol.Session{Mode: protocol.ModeTask}), k) && k == "CLAUDE_CODE_OAUTH_REFRESH_TOKEN" {
			t.Errorf("a task unsets %s", k)
		}
	}
	s, _, ws := launchFixture(t)
	s.Getenv = envOf(nil)
	l, err := BuildLaunch(s, remoteSession(), ws, "b", rescueNow)
	if err != nil {
		t.Fatal(err)
	}
	base := []string{"PATH=/bin", "CLAUDE_CODE_OAUTH_TOKEN=" + canaryAccess, "CLAUDE_CODE_OAUTH_REFRESH_TOKEN=" + canaryRefresh}
	for _, kv := range agentEnv(base, l) {
		if strings.Contains(kv, canaryAccess) || strings.Contains(kv, canaryRefresh) {
			t.Errorf("the agent's environment keeps %q", strings.SplitN(kv, "=", 2)[0])
		}
	}
}

func TestEnsureOAuthAccountKeepsKeysAndSkipsNoOpWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude.json")
	writeFile(t, path, `{"keep":{"a":1},"oauthAccount":{"emailAddress":"x@example.invalid"}}`)
	a := oauthAccount{AccountUUID: projAccount, OrganizationUUID: projOrg}
	if err := ensureOAuthAccount(path, a); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	for _, want := range []string{`"keep"`, "x@example.invalid", projAccount, projOrg} {
		if !strings.Contains(string(data), want) {
			t.Errorf("missing %s: %s", want, data)
		}
	}
	// A matching file is not rewritten (the CLI owns it while it runs).
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if err := ensureOAuthAccount(path, a); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); !fi.ModTime().Equal(old) {
		t.Error("a matching state file was rewritten")
	}
	// A file that is not an object is left as it is.
	bad := filepath.Join(t.TempDir(), ".claude.json")
	writeFile(t, bad, "[1]")
	if err := ensureOAuthAccount(bad, a); err == nil {
		t.Error("a non-object state file was overwritten")
	}
	if got, _ := os.ReadFile(bad); string(got) != "[1]" {
		t.Error("the non-object file changed")
	}
}
