package agentd

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// muxRunner runs git for real (against the fixture) and fakes the rest, so no
// test starts tmux or claude.
type muxRunner struct {
	git  ExecRunner
	fake *fakeRunner
}

func (m muxRunner) Run(ctx context.Context, c Cmd) (Result, error) {
	if c.Name == "git" {
		return m.git.Run(ctx, c)
	}
	return m.fake.Run(ctx, c)
}
func (m muxRunner) LookPath(name string) (string, error) { return m.fake.LookPath(name) }

// beats records heartbeats.
type beats struct {
	mu  sync.Mutex
	all []protocol.Status
	err error
}

func (b *beats) send(_ context.Context, st protocol.Status) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.all = append(b.all, st)
	return b.err
}

func (b *beats) last() (protocol.Status, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.all) == 0 {
		return protocol.Status{}, 0
	}
	return b.all[len(b.all)-1], len(b.all)
}

type daemonRig struct {
	d     *Daemon
	tmux  *fakeRunner
	beats *beats
	logs  *syncBuffer
	g     gitFixture
}

func newDaemonRig(t *testing.T, env map[string]string) daemonRig {
	t.Helper()
	g := newGitFixture(t, "demo")
	s, git := g.settings(t)
	s.Getenv = envOf(env)
	tmux := &fakeRunner{handle: func(c Cmd) (Result, error) {
		if c.Name == "tmux" && c.Args[0] == "has-session" {
			return Result{}, &CmdError{Name: "tmux", Sub: "has-session", ExitCode: 1}
		}
		return Result{}, nil
	}, missing: map[string]bool{"claude": true}}
	b := &beats{}
	logs := &syncBuffer{}
	d := &Daemon{
		S: s, R: muxRunner{git: git, fake: tmux}, Log: slog.New(slog.NewTextHandler(logs, nil)),
		Session: cloneSession("demo-1006-160000"), Self: "/usr/local/bin/agentd", Send: b.send,
		Interval: 40 * time.Millisecond, Poll: 10 * time.Millisecond, StopGrace: 2 * time.Second,
	}
	return daemonRig{d: d, tmux: tmux, beats: b, logs: logs, g: g}
}

func (r daemonRig) start(t *testing.T) (cancel func() error) {
	t.Helper()
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.d.Run(ctx) }()
	return func() error {
		stop()
		select {
		case err := <-done:
			return err
		case <-time.After(10 * time.Second):
			return errors.New("the daemon did not stop")
		}
	}
}

func (r daemonRig) tmuxStarted() bool {
	for _, l := range r.tmux.lines() {
		if strings.HasPrefix(l, "tmux new-session") {
			return true
		}
	}
	return false
}

func TestDaemonBootsStartsAndReports(t *testing.T) {
	r := newDaemonRig(t, map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "static"})
	stop := r.start(t)
	waitFor(t, r.tmuxStarted)
	waitFor(t, func() bool { st, _ := r.beats.last(); return st.Boot == protocol.BootReady })

	st, _ := r.beats.last()
	if st.Agent.State != protocol.AgentBusy || st.Workspace == nil || st.Workspace.Branch != "agent/demo-1006-160000" || st.Workspace.Head == "" {
		t.Errorf("status after boot: %+v %+v", st.Agent, st.Workspace)
	}
	var l Launch
	if err := readJSONFile(r.d.S.statePath(launchFile), &l); err != nil || l.Prompt != "p" {
		t.Fatalf("launch file: %+v %v", l, err)
	}

	// The task finishes: agentd reports it before the next minute.
	_, n := r.beats.last()
	if err := writeJSONFile(r.d.S.statePath(resultFile), taskResult{TaskResult: protocol.TaskResult{ExitCode: 0, Subtype: "success"}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		st, m := r.beats.last()
		return m > n && st.Agent.State == protocol.AgentExited
	})
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	var boot bootRecord
	if err := readJSONFile(r.d.S.statePath(bootFile), &boot); err != nil || boot.Boot != protocol.BootReady || boot.Workspace == nil {
		t.Errorf("boot record %+v %v", boot, err)
	}
	if !strings.Contains(r.logs.String(), "promptBytes=1") || strings.Contains(r.logs.String(), "static") {
		t.Errorf("logs:\n%s", r.logs.String())
	}
}

func TestDaemonNeverStartsATaskTwice(t *testing.T) {
	r := newDaemonRig(t, map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "static"})
	if err := writeJSONFile(r.d.S.statePath(launchFile), Launch{BootID: "earlier", CreatedAt: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	stop := r.start(t)
	waitFor(t, func() bool { st, _ := r.beats.last(); return st.Boot == protocol.BootReady })
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if r.tmuxStarted() {
		t.Error("the task was started a second time")
	}
	st, _ := r.beats.last()
	if st.Agent.State != protocol.AgentInterrupted {
		t.Errorf("agent = %+v", st.Agent)
	}
	if len(st.Problems) == 0 || st.Problems[len(st.Problems)-1].Name != "agent" {
		t.Errorf("problems = %+v", st.Problems)
	}
}

func TestDaemonReportsAFailedStart(t *testing.T) {
	r := newDaemonRig(t, nil) // no static token
	stop := r.start(t)
	waitFor(t, func() bool { st, _ := r.beats.last(); return st.Boot == protocol.BootFailed })
	_, n := r.beats.last()
	waitFor(t, func() bool { _, m := r.beats.last(); return m > n }) // it keeps heartbeating
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	st, _ := r.beats.last()
	if st.Agent.State != protocol.AgentFailed || !strings.Contains(st.Agent.Error, "plan credential unavailable") || r.tmuxStarted() {
		t.Errorf("agent = %+v", st.Agent)
	}
}

func TestDaemonShutdownForwardsSIGTERM(t *testing.T) {
	r := newDaemonRig(t, map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "static"})
	stop := r.start(t)
	waitFor(t, r.tmuxStarted)

	// Stand in for the CLI that run-agent started in the pane.
	cli := exec.Command("sleep", "30")
	if err := cli.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { _ = cli.Wait(); close(exited) }()
	p := agentPid{Pid: cli.Process.Pid, Start: procStartTime(cli.Process.Pid)}
	if err := writeJSONFile(r.d.S.statePath(pidFile), p); err != nil {
		t.Fatal(err)
	}

	if err := stop(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		_ = cli.Process.Kill()
		t.Fatal("the CLI did not get SIGTERM")
	}
	if !strings.Contains(r.logs.String(), "SIGTERM: forwarding to the agent") {
		t.Errorf("logs:\n%s", r.logs.String())
	}
}

func TestDaemonHeartbeatFailuresAreQuiet(t *testing.T) {
	var logs bytes.Buffer
	d := &Daemon{S: testSettings(t, t.TempDir()), R: &fakeRunner{}, Log: slog.New(slog.NewTextHandler(&logs, nil)), Session: cloneSession("q-1")}
	fail := errors.New("connection refused")
	var calls int
	d.Send = func(context.Context, protocol.Status) error {
		calls++
		if calls <= 12 {
			return fail
		}
		return nil
	}
	for i := 0; i < 13; i++ {
		d.beat(context.Background())
	}
	out := logs.String()
	if n := strings.Count(out, "heartbeat failed"); n != 2 { // the 1st and the 10th
		t.Errorf("%d failure lines:\n%s", n, out)
	}
	if !strings.Contains(out, "heartbeat recovered") {
		t.Errorf("no recovery line:\n%s", out)
	}
}
