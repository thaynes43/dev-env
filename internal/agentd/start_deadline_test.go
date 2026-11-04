package agentd

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/standby"
)

// DESIGN-054 S3c: a scheduled session's start deadline bounds its first launch
// only. The tests run the daemon on a fake clock, with git real against a local
// fixture and tmux and claude faked.

var deadlineNow = time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)

// testClock is a fake clock a test can move while the daemon runs.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time  { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *testClock) Set(t time.Time) { c.mu.Lock(); defer c.mu.Unlock(); c.now = t }

func deadlineRig(t *testing.T, mode, deadline string) (daemonRig, *testClock) {
	t.Helper()
	r := newDaemonRig(t, map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "static"})
	r.d.Session.Mode = mode
	if mode != protocol.ModeTask {
		r.d.Session.Prompt = ""
	}
	r.d.S.StartDeadline = deadline
	clock := &testClock{now: deadlineNow}
	r.d.Now = clock.Now
	return r, clock
}

// agentEnded is the operator's view of the reported status: the heartbeat
// route copies the boot and agent state into status.agent (apiserver
// agentStatus), and AgentEnded is the session-end rule's test.
func agentEnded(st protocol.Status) bool {
	return standby.AgentEnded(&v1alpha1.AgentSession{
		Spec:   v1alpha1.AgentSessionSpec{OperatingMode: v1alpha1.OperatingModeRunning},
		Status: v1alpha1.AgentSessionStatus{Phase: v1alpha1.PhaseRunning, Agent: &v1alpha1.AgentStatus{Boot: st.Boot, Status: st.Agent.State}},
	})
}

// assertRefusedBeforeBoot checks a refused first launch: the daemon keeps
// supervising and reporting, reports boot complete and the agent failed with
// why, and did nothing of the boot: no clone, no launch record, no tmux.
func assertRefusedBeforeBoot(t *testing.T, r daemonRig, why string) {
	t.Helper()
	stop := r.start(t)
	waitFor(t, func() bool { st, count := r.beats.last(); return st.Boot == protocol.BootFailed && count >= 3 })
	if err := stop(); err != nil {
		t.Fatal("a refused first launch exited instead of supervising: ", err)
	}
	st, _ := r.beats.last()
	if st.Boot == protocol.BootBooting || st.Agent.State != protocol.AgentFailed || st.Agent.Error != why {
		t.Fatalf("status boot %q agent %+v, want boot complete and agent failed with %q", st.Boot, st.Agent, why)
	}
	if !agentEnded(st) {
		t.Fatal("the operator would not see the refused agent as ended")
	}
	var rec bootRecord
	if err := readJSONFile(r.d.S.statePath(bootFile), &rec); err != nil || rec.Boot != protocol.BootFailed || rec.AgentError != why || rec.Workspace != nil {
		t.Fatalf("boot record %+v %v", rec, err)
	}
	if r.tmuxStarted() || exists(r.d.S.statePath(launchFile)) || exists(r.d.S.statePath(resumeFile)) ||
		exists(r.d.S.ClonePath(r.d.Session.Repo)) || exists(r.d.S.WorktreePath(r.d.Session.Name)) {
		t.Fatal("a refused first launch started the agent or prepared its repo")
	}
	for _, line := range r.tmux.lines() {
		if !strings.HasPrefix(line, "tmux list-clients ") && !strings.HasPrefix(line, "tmux display-message ") {
			t.Fatal("a refused daemon ran a mutation: ", line)
		}
	}
}

func TestDaemonRefusesAFirstLaunchPastItsStartDeadline(t *testing.T) {
	for _, mode := range []string{protocol.ModeTask, protocol.ModeRemote} {
		for name, deadline := range map[string]string{
			"passed": deadlineNow.Add(-time.Second).Format(time.RFC3339),
			"at":     deadlineNow.Format(time.RFC3339),
		} {
			t.Run(mode+"/"+name, func(t *testing.T) {
				r, _ := deadlineRig(t, mode, deadline)
				assertRefusedBeforeBoot(t, r, protocol.StartDeadlinePassed)
			})
		}
	}
}

// An unparsable deadline fails closed: the first launch is refused.
func TestDaemonRefusesAFirstLaunchWithAnInvalidStartDeadline(t *testing.T) {
	for _, deadline := range []string{"tomorrow", "2026-11-04T11:00:00+01:00", "2026-11-04T11:00:00", " "} {
		t.Run(deadline, func(t *testing.T) {
			r, _ := deadlineRig(t, protocol.ModeTask, deadline)
			assertRefusedBeforeBoot(t, r, protocol.StartDeadlineInvalid)
		})
	}
}

func TestDaemonLaunchesBeforeItsStartDeadline(t *testing.T) {
	r, _ := deadlineRig(t, protocol.ModeTask, deadlineNow.Add(time.Minute).Format(time.RFC3339))
	stop := r.start(t)
	waitFor(t, r.tmuxStarted)
	waitFor(t, func() bool { st, _ := r.beats.last(); return st.Boot == protocol.BootReady })
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	var l Launch
	if err := readJSONFile(r.d.S.statePath(launchFile), &l); err != nil || l.Prompt != "p" || l.Resume {
		t.Fatalf("first launch %+v %v", l, err)
	}
	if st, _ := r.beats.last(); st.Agent.State != protocol.AgentBusy {
		t.Fatalf("agent %+v", st.Agent)
	}
}

// A session that launched in time and boots again later (a drain, a
// preemption, a resume) is never refused, whatever its deadline says.
func TestDaemonNeverRefusesABootAfterTheFirstLaunch(t *testing.T) {
	for name, deadline := range map[string]string{
		"passed":  deadlineNow.Add(-time.Hour).Format(time.RFC3339),
		"invalid": "tomorrow",
	} {
		t.Run(name, func(t *testing.T) {
			r, _ := deadlineRig(t, protocol.ModeTask, deadline)
			if _, step := PrepareRepo(context.Background(), r.d.R, r.d.S, r.d.Session); step.State != StepOK {
				t.Fatal(step.Notes)
			}
			first := Launch{Session: r.d.Session.Name, Prompt: "p", ConversationID: "11111111-2222-4333-8444-555555555555", BootID: "earlier", CreatedAt: deadlineNow.Add(-2 * time.Hour)}
			if err := writeJSONFile(r.d.S.statePath(launchFile), first); err != nil {
				t.Fatal(err)
			}
			stop := r.start(t)
			waitFor(t, r.tmuxStarted)
			waitFor(t, func() bool { st, _ := r.beats.last(); return st.Boot == protocol.BootReady })
			if err := stop(); err != nil {
				t.Fatal(err)
			}
			var resumed Launch
			if err := readJSONFile(r.d.S.statePath(resumeFile), &resumed); err != nil || !resumed.Resume || resumed.ConversationID != first.ConversationID {
				t.Fatalf("resume %+v %v", resumed, err)
			}
			if st, _ := r.beats.last(); st.Agent.State != protocol.AgentBusy || st.Agent.Error != "" {
				t.Fatalf("agent %+v", st.Agent)
			}
		})
	}
}

// clockRunner moves the fake clock when the boot first runs git, so the boot
// itself carries the session past its deadline.
type clockRunner struct {
	Runner
	once  sync.Once
	clock *testClock
	to    time.Time
}

func (c *clockRunner) Run(ctx context.Context, cmd Cmd) (Result, error) {
	if cmd.Name == "git" {
		c.once.Do(func() { c.clock.Set(c.to) })
	}
	return c.Runner.Run(ctx, cmd)
}

// The deadline bounds the first launch itself: a boot that began in time but
// reaches the launch after the deadline does not start the agent.
func TestDaemonRefusesAFirstLaunchTheBootDelayedPastItsDeadline(t *testing.T) {
	deadline := deadlineNow.Add(time.Minute)
	r, clock := deadlineRig(t, protocol.ModeTask, deadline.Format(time.RFC3339))
	r.d.R = &clockRunner{Runner: r.d.R, clock: clock, to: deadline.Add(time.Second)}
	stop := r.start(t)
	waitFor(t, func() bool { st, count := r.beats.last(); return st.Boot == protocol.BootFailed && count >= 3 })
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	st, _ := r.beats.last()
	if st.Agent.State != protocol.AgentFailed || st.Agent.Error != protocol.StartDeadlinePassed || !agentEnded(st) {
		t.Fatalf("status boot %q agent %+v", st.Boot, st.Agent)
	}
	if r.tmuxStarted() || exists(r.d.S.statePath(launchFile)) {
		t.Fatal("the agent started after its start deadline")
	}
	if !exists(r.d.S.WorktreePath(r.d.Session.Name)) {
		t.Fatal("the boot did not reach the launch, so this test proves nothing")
	}
}

func TestStartDeadlineRefusal(t *testing.T) {
	at := deadlineNow.Format(time.RFC3339)
	for _, c := range []struct {
		raw      string
		launched bool
		now      time.Time
		want     string
	}{
		{"", false, deadlineNow, ""},
		{at, false, deadlineNow.Add(-time.Nanosecond), ""},
		{at, false, deadlineNow, protocol.StartDeadlinePassed},
		{at, false, deadlineNow.Add(time.Hour), protocol.StartDeadlinePassed},
		{at, true, deadlineNow.Add(time.Hour), ""},
		{"tomorrow", false, deadlineNow, protocol.StartDeadlineInvalid},
		{"tomorrow", true, deadlineNow, ""},
	} {
		if got := startDeadlineRefusal(c.raw, c.launched, c.now); got != c.want {
			t.Errorf("%q launched=%v at %v: %q, want %q", c.raw, c.launched, c.now, got, c.want)
		}
	}
}

// Only a proven absence of launch.json is a session that never launched. A
// record agentd cannot read counts as launched: the boot then fails it closed
// as an unreadable launch, and no prompt replays.
func TestFirstLaunchRecorded(t *testing.T) {
	s := Settings{StateDir: t.TempDir() + "/.agentd"}
	if firstLaunchRecorded(s) {
		t.Fatal("no state directory read as a launch")
	}
	if err := os.MkdirAll(s.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if firstLaunchRecorded(s) {
		t.Fatal("no launch record read as a launch")
	}
	writeFile(t, s.statePath(launchFile), "not json")
	if !firstLaunchRecorded(s) {
		t.Fatal("an unreadable launch record read as no launch")
	}
	if err := os.Remove(s.statePath(launchFile)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(s.StateDir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(s.StateDir, 0o700) })
	if !firstLaunchRecorded(s) {
		t.Fatal("a state directory agentd cannot read read as no launch")
	}
}

func TestSettingsReadTheStartDeadline(t *testing.T) {
	s, err := LoadSettings(envOf(map[string]string{"HOME": "/home/dev", protocol.StartDeadlineEnv: "2026-11-04T10:05:00Z"}))
	if err != nil || s.StartDeadline != "2026-11-04T10:05:00Z" {
		t.Fatalf("%q %v", s.StartDeadline, err)
	}
}
