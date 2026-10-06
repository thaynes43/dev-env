package agentd

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func launchFixture(t *testing.T) (Settings, protocol.Session, protocol.Workspace) {
	t.Helper()
	s := testSettings(t, t.TempDir())
	s.Getenv = envOf(map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "static-token"})
	sess := protocol.Session{
		Name: "haynes-ops-1006-120000", Repo: "haynes-ops", Agent: protocol.AgentClaude, Mode: protocol.ModeTask,
		Model: "claude-opus-5-5", Effort: "xhigh", Prompt: "--fix the docs", Limits: &protocol.Limits{Timeout: "40m0s", MaxTurns: 120},
	}
	ws := protocol.Workspace{Clone: s.ClonePath(sess.Repo), Worktree: s.WorktreePath(sess.Name), Branch: "agent/" + sess.Name}
	return s, sess, ws
}

func TestBuildLaunchTask(t *testing.T) {
	s, sess, ws := launchFixture(t)
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	l, err := BuildLaunch(s, sess, ws, "boot1", now)
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Join(l.Argv, " ")
	for _, want := range []string{
		"claude --model claude-opus-5-5 --effort xhigh --dangerously-skip-permissions --session-id " + l.ConversationID,
		"--max-turns 120",
		"--append-system-prompt You are working in an isolated git worktree (" + ws.Worktree + ") on branch " + ws.Branch,
		"--output-format stream-json --verbose -p",
	} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv lacks %q:\n%s", want, argv)
		}
	}
	if l.Argv[len(l.Argv)-1] != "-p" || strings.Contains(argv, sess.Prompt) {
		t.Errorf("the prompt must go to stdin, not argv: %q", l.Argv)
	}
	if l.Prompt != sess.Prompt || l.Dir != ws.Worktree || l.Timeout != 40*time.Minute || l.BootID != "boot1" || !l.CreatedAt.Equal(now) {
		t.Errorf("launch = %+v", l)
	}
	if len(l.ConversationID) != 36 || l.ConversationID[14] != '4' {
		t.Errorf("conversation id %q is not a v4 UUID", l.ConversationID)
	}
	for _, k := range []string{"ANTHROPIC_API_KEY", "DISABLE_GROWTHBOOK", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC", "DO_NOT_TRACK", "DISABLE_TELEMETRY", protocol.SessionEnv} {
		if !slices.Contains(l.Unset, k) {
			t.Errorf("%s is not unset for the agent", k)
		}
	}
	if l.LogPath != s.LogPath(sess.Name) || !slices.Contains(l.Unset, "GH_TOKEN") {
		t.Errorf("log path %q, unset %q", l.LogPath, l.Unset)
	}

	sess.Effort, sess.Limits = "", nil
	l, err = BuildLaunch(s, sess, ws, "boot1", now)
	if err != nil {
		t.Fatal(err)
	}
	if argv := strings.Join(l.Argv, " "); strings.Contains(argv, "--effort") || strings.Contains(argv, "--max-turns") || l.Timeout != 0 {
		t.Errorf("unset limits leaked: %s %v", argv, l.Timeout)
	}
}

func TestBuildLaunchRefuses(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Settings, *protocol.Session)
		want   string
	}{
		{"local", func(_ *Settings, s *protocol.Session) { s.Mode = protocol.ModeLocal }, "plan 02"},
		{"remote", func(_ *Settings, s *protocol.Session) { s.Mode = protocol.ModeRemote }, "plan 03"},
		{"codex", func(_ *Settings, s *protocol.Session) { s.Agent = protocol.AgentCodex }, "plan 04"},
		{"alias", func(_ *Settings, s *protocol.Session) { s.Model = "opus" }, "aliases are refused"},
		{"no static token", func(st *Settings, _ *protocol.Session) { st.Getenv = envOf(nil) }, "plan credential unavailable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, sess, ws := launchFixture(t)
			tc.mutate(&s, &sess)
			_, err := BuildLaunch(s, sess, ws, "b", time.Now())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
	s, sess, ws := launchFixture(t)
	sess.Mode = protocol.ModeLocal
	if _, err := BuildLaunch(s, sess, ws, "b", time.Now()); !errors.Is(err, ErrNotInPlan01) {
		t.Errorf("err = %v, want ErrNotInPlan01", err)
	}
}

func TestStartAgent(t *testing.T) {
	s, sess, ws := launchFixture(t)
	l, err := BuildLaunch(s, sess, ws, "b", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeRunner{handle: func(c Cmd) (Result, error) {
		if c.Args[0] == "has-session" {
			return Result{}, &CmdError{Name: "tmux", Sub: "has-session", ExitCode: 1}
		}
		return Result{}, nil
	}}
	if err := StartAgent(context.Background(), f, s, l, "/usr/local/bin/agentd"); err != nil {
		t.Fatal(err)
	}
	want := "tmux new-session -d -s agent -x 200 -y 50 -c " + ws.Worktree + " /usr/local/bin/agentd run-agent --launch " + s.statePath(launchFile)
	if got := f.lines(); len(got) != 2 || got[1] != want {
		t.Errorf("tmux calls = %q\nwant %q", got, want)
	}
	fi, err := os.Stat(s.statePath(launchFile))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("launch file: %v %v", fi, err)
	}
	var back Launch
	if err := readJSONFile(s.statePath(launchFile), &back); err != nil || back.Prompt != sess.Prompt {
		t.Errorf("launch file content: %+v %v", back, err)
	}
}

func TestStartAgentFailures(t *testing.T) {
	s, sess, ws := launchFixture(t)
	l, _ := BuildLaunch(s, sess, ws, "b", time.Now())

	// The session already exists: refuse, keep nothing running twice.
	exists := &fakeRunner{}
	if err := StartAgent(context.Background(), exists, s, l, "agentd"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("err = %v", err)
	}

	// tmux fails: the launch file goes, so a later boot may start the task.
	broken := &fakeRunner{handle: func(c Cmd) (Result, error) {
		return Result{}, &CmdError{Name: "tmux", Sub: c.Args[0], ExitCode: 1, Stderr: "no server"}
	}}
	if err := StartAgent(context.Background(), broken, s, l, "agentd"); err == nil || !strings.Contains(err.Error(), "no server") {
		t.Errorf("err = %v", err)
	}
	if exists := fileExists(s.statePath(launchFile)); exists {
		t.Error("the launch file survived a failed start")
	}
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }
