package agentd

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// The canary stands in for a token. No status, log line, error or argument
// below may carry it (Story110 T03).
const (
	canaryAccess  = "sk-canary-access-0123456789abcdef"
	canaryRefresh = "sk-canary-refresh-0123456789abcdef"
	canaryCode    = "canarycode0123456789abcdef#canarystate0123456789"
)

var loginNow = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

func writeCredentials(t *testing.T, s Settings, refreshExpiresMs int64) {
	t.Helper()
	oauth := map[string]any{"accessToken": canaryAccess, "refreshToken": canaryRefresh, "expiresAt": loginNow.UnixMilli() + 3600_000}
	if refreshExpiresMs != 0 {
		oauth["refreshTokenExpiresAt"] = refreshExpiresMs
	}
	if err := os.MkdirAll(s.ClaudeConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"claudeAiOauth": oauth, "mcpOAuth": map[string]any{"x": "canary-mcp"}})
	if err := os.WriteFile(filepath.Join(s.ClaudeConfigDir, credentialsFileName), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func remoteSession() protocol.Session {
	return protocol.Session{Name: "dev-env-1019-120000", Repo: "dev-env", Agent: protocol.AgentClaude, Mode: protocol.ModeRemote, Model: "claude-haiku-4-5"}
}

func TestCheckLoginPresenceAndExpiry(t *testing.T) {
	s := testSettings(t, t.TempDir())
	if c := checkLogin(s); c.Present || c.Usable(loginNow) {
		t.Fatalf("no file: %+v", c)
	}
	writeCredentials(t, s, 0)
	if c := checkLogin(s); !c.Present || !c.Usable(loginNow) || !c.Expires.IsZero() {
		t.Errorf("a login with no recorded lapse: %+v", c)
	}
	writeCredentials(t, s, loginNow.Add(48*time.Hour).UnixMilli())
	if c := checkLogin(s); !c.Usable(loginNow) || !c.Expires.Equal(loginNow.Add(48*time.Hour)) {
		t.Errorf("a login that lapses in two days: %+v", c)
	}
	if c := checkLogin(s); c.Usable(loginNow.Add(49 * time.Hour)) {
		t.Errorf("a lapsed login is usable: %+v", c)
	}
	writeCredentials(t, s, loginNow.Add(-time.Hour).UnixMilli())
	if c := checkLogin(s); !c.Present || c.Usable(loginNow) {
		t.Errorf("an expired login: %+v", c)
	}
	// Seconds, not milliseconds.
	writeCredentials(t, s, loginNow.Add(time.Hour).Unix())
	if c := checkLogin(s); !c.Usable(loginNow) || !c.Expires.Equal(loginNow.Add(time.Hour)) {
		t.Errorf("epoch seconds: %+v", c)
	}

	path := filepath.Join(s.ClaudeConfigDir, credentialsFileName)
	for name, content := range map[string]string{"garbage": "not json " + canaryAccess, "no oauth key": `{"mcpOAuth":{}}`, "empty tokens": `{"claudeAiOauth":{"accessToken":"","refreshToken":""}}`} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if c := checkLogin(s); c.Present {
			t.Errorf("%s: %+v", name, c)
		}
	}
	// A symlink is not followed.
	target := filepath.Join(t.TempDir(), "real.json")
	if err := os.WriteFile(target, []byte(`{"claudeAiOauth":{"accessToken":"x"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(path)
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if c := checkLogin(s); c.Present {
		t.Errorf("a symlinked credentials file: %+v", c)
	}
}

func TestRemoteLaunchRunsWithoutTheStaticToken(t *testing.T) {
	s, _, ws := launchFixture(t)
	s.Getenv = envOf(nil) // the operator leaves the token out of a remote pod
	sess := remoteSession()
	sess.Repo = "haynes-ops"
	l, err := BuildLaunch(s, sess, ws, "boot1", rescueNow)
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Join(l.Argv, " ")
	if !l.TUI || !strings.Contains(argv, "--model claude-haiku-4-5") || !strings.Contains(argv, "--remote-control "+sess.Name) || strings.Contains(argv, "--effort") {
		t.Errorf("remote launch %q", argv)
	}
	if !strings.Contains(argv, "--session-id "+l.ConversationID) {
		t.Errorf("no session id: %q", argv)
	}
	if !contains(l.Unset, "CLAUDE_CODE_OAUTH_TOKEN") || !contains(l.Unset, "ANTHROPIC_API_KEY") {
		t.Errorf("unset %v", l.Unset)
	}
	// A profile envFrom Secret that still carries the token does not reach the CLI.
	base := []string{"PATH=/bin", "CLAUDE_CODE_OAUTH_TOKEN=" + canaryAccess, "ANTHROPIC_API_KEY=k"}
	for _, kv := range agentEnv(base, l) {
		if strings.Contains(kv, canaryAccess) || strings.HasPrefix(kv, "CLAUDE_CODE_OAUTH_TOKEN=") || strings.HasPrefix(kv, "ANTHROPIC_API_KEY=") {
			t.Errorf("the agent's environment keeps %q", strings.SplitN(kv, "=", 2)[0])
		}
	}

	// The resume reattaches the same entry (S-6): --resume first, then the name.
	r, err := BuildResume(s, sess, ws, l, "boot2", rescueNow)
	if err != nil {
		t.Fatal(err)
	}
	ra := strings.Join(r.Argv, " ")
	if !strings.Contains(ra, "--resume "+l.ConversationID+" --remote-control "+sess.Name) || !r.Resume || !contains(r.Unset, "CLAUDE_CODE_OAUTH_TOKEN") {
		t.Errorf("remote resume %q unset %v", ra, r.Unset)
	}

	// Task and local sessions still run on the static token alone, and keep it.
	s2, task, ws2 := launchFixture(t)
	for _, mode := range []string{protocol.ModeTask, protocol.ModeLocal} {
		task.Mode = mode
		tl, err := BuildLaunch(s2, task, ws2, "b", rescueNow)
		if err != nil || contains(tl.Unset, "CLAUDE_CODE_OAUTH_TOKEN") {
			t.Errorf("%s: err %v unset %v", mode, err, tl.Unset)
		}
		s3 := s2
		s3.Getenv = envOf(nil)
		if _, err := BuildLaunch(s3, task, ws2, "b", rescueNow); err == nil || !strings.Contains(err.Error(), "plan credential unavailable") {
			t.Errorf("%s without the token: %v", mode, err)
		}
	}
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

const loginPaneWithURL = "Opening browser to sign in...\nIf the browser did not open, visit:\nhttps://claude.com/cai/oauth/authorize?code=true&client_id=abc&state=st-xyz&code_challenge=cc\nPaste code here if prompted > \n"

// loginTmux fakes the login window: has-session, new-session, capture-pane,
// and the three calls that deliver a code.
type loginTmux struct {
	window  bool
	pane    string
	loaded  string
	started int
	killed  int
}

func (f *loginTmux) handle(c Cmd) (Result, error) {
	switch c.Args[0] {
	case "has-session":
		if f.window {
			return Result{}, nil
		}
		return Result{}, &CmdError{Name: "tmux", Sub: "has-session", ExitCode: 1}
	case "new-session":
		f.window, f.pane, f.started = true, loginPaneWithURL, f.started+1
	case "kill-session":
		f.window, f.killed = false, f.killed+1
	case "capture-pane":
		return Result{Stdout: []byte(f.pane)}, nil
	case "load-buffer":
		b, _ := io.ReadAll(c.Stdin)
		f.loaded = string(b)
	}
	return Result{}, nil
}

func TestLoginURLStartsWindowAndPrintsOnlyTheLink(t *testing.T) {
	s := testSettings(t, t.TempDir())
	s.TmuxBin, s.ClaudeBin = "tmux", "claude"
	tm := &loginTmux{}
	r := &fakeRunner{handle: tm.handle}
	u, err := LoginURL(context.Background(), r, s, remoteSession(), func() time.Time { return loginNow })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(u, "https://claude.com/cai/oauth/authorize?") || strings.ContainsAny(u, " \n") || tm.started != 1 {
		t.Errorf("url %q started %d", u, tm.started)
	}
	var create string
	for _, l := range r.lines() {
		if strings.HasPrefix(l, "tmux new-session") {
			create = l
		}
	}
	for _, want := range []string{"-s login", "-x 300", "-u CLAUDE_CODE_OAUTH_TOKEN", `"$0" auth login`, "LOGIN_EXIT="} {
		if !strings.Contains(create, want) {
			t.Errorf("login window %q lacks %q", create, want)
		}
	}
	// A second call reuses a window that is still waiting.
	if _, err := LoginURL(context.Background(), r, s, remoteSession(), func() time.Time { return loginNow }); err != nil || tm.started != 1 {
		t.Errorf("reuse: %v started %d", err, tm.started)
	}
	// An ended window (a used flow) is replaced so the link is fresh.
	tm.pane += "Login successful.\nLOGIN_EXIT=0\n"
	if _, err := LoginURL(context.Background(), r, s, remoteSession(), func() time.Time { return loginNow }); err != nil || tm.started != 2 || tm.killed != 1 {
		t.Errorf("restart: %v started %d killed %d", err, tm.started, tm.killed)
	}
	// Not for other modes, and not once a login exists.
	task := remoteSession()
	task.Mode = protocol.ModeTask
	if _, err := LoginURL(context.Background(), r, s, task, func() time.Time { return loginNow }); err == nil {
		t.Error("login url on a task session")
	}
	writeCredentials(t, s, 0)
	if _, err := LoginURL(context.Background(), r, s, remoteSession(), func() time.Time { return loginNow }); err == nil || !strings.Contains(err.Error(), "already has a login") {
		t.Errorf("with a login: %v", err)
	}
}

func TestLoginCodeNeverReachesArguments(t *testing.T) {
	old := deliverSettle
	deliverSettle = time.Millisecond
	defer func() { deliverSettle = old }()
	s := testSettings(t, t.TempDir())
	s.TmuxBin = "tmux"
	tm := &loginTmux{window: true, pane: loginPaneWithURL}
	r := &fakeRunner{handle: tm.handle}
	if err := LoginCode(context.Background(), r, s, remoteSession(), strings.NewReader(canaryCode+"\n")); err != nil {
		t.Fatal(err)
	}
	if tm.loaded != canaryCode {
		t.Errorf("the buffer holds %q", tm.loaded)
	}
	got := strings.Join(r.lines(), "\n")
	if strings.Contains(got, canaryCode) || strings.Contains(got, "canarycode") {
		t.Errorf("the code reached a command line:\n%s", got)
	}
	for _, want := range []string{"tmux load-buffer", "tmux paste-buffer", "tmux send-keys -t =login: Enter"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}

	// Refusals send nothing and never echo the input.
	for name, tc := range map[string]struct {
		in     string
		window bool
		pane   string
	}{
		"bad shape":    {"has space and " + canaryCode, true, loginPaneWithURL},
		"empty":        {"", true, loginPaneWithURL},
		"no window":    {canaryCode, false, ""},
		"window ended": {canaryCode, true, loginPaneWithURL + "LOGIN_EXIT=1\n"},
	} {
		tm := &loginTmux{window: tc.window, pane: tc.pane}
		r := &fakeRunner{handle: tm.handle}
		err := LoginCode(context.Background(), r, s, remoteSession(), strings.NewReader(tc.in))
		if err == nil || strings.Contains(err.Error(), "canary") || tm.loaded != "" {
			t.Errorf("%s: err %v loaded %q", name, err, tm.loaded)
		}
	}
}

func TestLoginStatusLines(t *testing.T) {
	s := testSettings(t, t.TempDir())
	s.TmuxBin = "tmux"
	tm := &loginTmux{}
	r := &fakeRunner{handle: tm.handle}
	say := func() string { return LoginStatus(context.Background(), r, s, remoteSession(), loginNow) }
	if got := say(); !strings.Contains(got, "no login window") {
		t.Errorf("no window: %q", got)
	}
	tm.window, tm.pane = true, loginPaneWithURL
	if got := say(); !strings.Contains(got, "waiting for the code") {
		t.Errorf("waiting: %q", got)
	}
	tm.pane += "LOGIN_EXIT=1\n"
	if got := say(); !strings.Contains(got, "ended without a login") {
		t.Errorf("ended: %q", got)
	}
	writeCredentials(t, s, loginNow.Add(30*24*time.Hour).UnixMilli())
	got := say()
	if !strings.Contains(got, "logged in, lapses 2026-11-18 (30 days)") || strings.Contains(got, "canary") {
		t.Errorf("logged in: %q", got)
	}
}

func TestStatusRemoteLoginRequiredAndRegistered(t *testing.T) {
	s := testSettings(t, t.TempDir())
	s.TmuxBin = "tmux"
	if err := os.MkdirAll(s.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sess := remoteSession()
	r := &fakeRunner{handle: func(c Cmd) (Result, error) { return Result{}, &CmdError{Name: "tmux", ExitCode: 1} }}
	_ = writeJSONFile(s.statePath(bootFile), bootRecord{BootID: "b1", Boot: protocol.BootReady})
	collect := func() protocol.Status { return CollectStatus(context.Background(), r, s, sess, loginNow) }

	st := collect()
	if st.Agent.State != protocol.AgentLoginRequired || !strings.Contains(st.Agent.Error, "agent-run login "+sess.Name+" url") ||
		st.RemoteControl == nil || st.RemoteControl.State != protocol.RemoteLoginRequired || st.RemoteControl.URL != "" || st.RemoteControl.Name != sess.Name {
		t.Fatalf("no login: %+v rc %+v", st.Agent, st.RemoteControl)
	}

	// Logged in, agent running, CLI not yet registered: starting, no link.
	writeCredentials(t, s, 0)
	me := agentPid{Pid: os.Getpid(), Start: procStartTime(os.Getpid())}
	_ = writeJSONFile(s.statePath(launchFile), Launch{BootID: "b1", TUI: true, ConversationID: "conv", CreatedAt: loginNow.Add(-time.Minute), LogPath: s.LogPath("s")})
	_ = writeJSONFile(s.statePath(pidFile), me)
	st = collect()
	if st.Agent.State == protocol.AgentLoginRequired || st.RemoteControl == nil || st.RemoteControl.State != protocol.RemoteStarting || st.RemoteControl.URL != "" {
		t.Fatalf("starting: %+v rc %+v", st.Agent, st.RemoteControl)
	}

	// The CLI's record names the bridge session: registered, link set.
	rec := filepath.Join(s.ClaudeConfigDir, "sessions", strconv.Itoa(me.Pid)+".json")
	writeFile(t, rec, `{"status":"idle","updatedAt":1760875200000,"name":"wt-ab","bridgeSessionId":"session_01AbCdEf"}`)
	st = collect()
	rc := st.RemoteControl
	if rc == nil || rc.State != protocol.RemoteRegistered || rc.SessionID != "session_01AbCdEf" || rc.URL != "https://claude.ai/code/session_01AbCdEf" || rc.Name != sess.Name {
		t.Fatalf("registered: %+v", rc)
	}
	// A malformed id gives no link.
	writeFile(t, rec, `{"status":"idle","bridgeSessionId":"https://evil.example/x"}`)
	if rc := collect().RemoteControl; rc.State != protocol.RemoteStarting || rc.URL != "" {
		t.Errorf("malformed id: %+v", rc)
	}

	// The CLI is gone: offline, not login-required, as the login is there.
	_ = os.Remove(s.statePath(pidFile))
	if rc := collect().RemoteControl; rc.State != protocol.RemoteOffline {
		t.Errorf("offline: %+v", rc)
	}

	// A new boot whose resume has not started the CLI yet is starting, not offline.
	_ = writeJSONFile(s.statePath(bootFile), bootRecord{BootID: "b2", Boot: protocol.BootReady})
	if rc := collect().RemoteControl; rc.State != protocol.RemoteStarting {
		t.Errorf("after a roll, before the resume: %+v", rc)
	}
	_ = writeJSONFile(s.statePath(bootFile), bootRecord{BootID: "b1", Boot: protocol.BootReady})

	// Nothing in the status carries a token.
	data, _ := json.Marshal(collect())
	for _, secret := range []string{canaryAccess, canaryRefresh, "canary-mcp"} {
		if strings.Contains(string(data), secret) {
			t.Errorf("the status carries %q", secret)
		}
	}
	// Other modes report no entry.
	local := sess
	local.Mode = protocol.ModeLocal
	if CollectStatus(context.Background(), r, s, local, loginNow).RemoteControl != nil {
		t.Error("a local session reports a Remote Control entry")
	}
}

// A remote session without a login does not start its agent, opens the login
// window and logs nothing secret; once the login exists and the window ends,
// the agent starts with the same boot (Story110 T03).
func TestDaemonHoldsRemoteAgentUntilLogin(t *testing.T) {
	s := testSettings(t, t.TempDir())
	s.TmuxBin, s.ClaudeBin = "tmux", "claude"
	s.Getenv = envOf(nil)
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

	step, agentErr := d.startAgent(context.Background(), ws, Step{Name: "repo", State: StepOK}, "b1")
	if agentErr != "" || step.State == StepFail || d.remote == nil || tm.started != 1 || !strings.Contains(strings.Join(step.Notes, " "), "login-required") {
		t.Fatalf("held: step %+v err %q remote %v started %d", step, agentErr, d.remote, tm.started)
	}
	if _, err := os.Stat(s.statePath(launchFile)); err == nil {
		t.Fatal("the agent was launched without a login")
	}
	d.pollRemoteLogin(context.Background())
	if d.remote == nil {
		t.Fatal("started without a login")
	}

	// The login lands; the window's CLI is still finishing: wait for it.
	writeCredentials(t, s, 0)
	d.pollRemoteLogin(context.Background())
	if d.remote == nil {
		t.Fatal("started before the login window ended")
	}
	tm.pane += "Login successful.\nLOGIN_EXIT=0\n"
	d.pollRemoteLogin(context.Background())
	if d.remote != nil {
		t.Fatal("did not start after the login")
	}
	var l Launch
	if err := readJSONFile(s.statePath(launchFile), &l); err != nil || !l.TUI || !strings.Contains(strings.Join(l.Argv, " "), "--remote-control "+sess.Name) {
		t.Fatalf("launch %+v err %v", l, err)
	}
	var rec bootRecord
	if err := readJSONFile(s.statePath(bootFile), &rec); err != nil || rec.AgentError != "" || rec.Boot != protocol.BootReady || tm.killed != 1 {
		t.Errorf("boot %+v err %v killed %d", rec, err, tm.killed)
	}
	for _, secret := range []string{canaryAccess, canaryRefresh, "canary-mcp"} {
		if strings.Contains(logs.String(), secret) || strings.Contains(strings.Join(r.lines(), "\n"), secret) {
			t.Errorf("a token reached the logs or a command line")
		}
	}
}
