package agentd

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
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

// homePaths lists every path under the session's HOME except agentd's own
// state directory, which the refusal writes its boot record into.
func homePaths(t *testing.T, s Settings) []string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(s.Home, func(path string, e fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) && path == s.Home {
			return fs.SkipAll
		}
		if err != nil {
			return err
		}
		if path == s.StateDir {
			return fs.SkipDir
		}
		if path != s.Home {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

// assertRefusedBeforeBoot checks a refused first launch: the daemon keeps
// supervising and reporting, reports boot complete and the agent failed with
// why, and did nothing of the boot: no config rendered (nothing new in HOME
// outside agentd's state directory), no clone, no launch record, no tmux.
func assertRefusedBeforeBoot(t *testing.T, r daemonRig, why string) {
	t.Helper()
	before := homePaths(t, r.d.S)
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
	if after := homePaths(t, r.d.S); !slices.Equal(after, before) {
		t.Fatalf("a refused first launch rendered configuration: HOME was %q, is %q", before, after)
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
	if os.Geteuid() == 0 {
		// Root reads a directory whatever its mode, so the case proves nothing.
		t.Skip("running as root: a mode-0 directory stays readable")
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

// heldAdmission holds the launch admission until released, so a test can move
// the clock while a boot waits for the workspace's administrative lock.
type heldAdmission struct {
	entered, release chan struct{}
	once             sync.Once
}

func newHeldAdmission() *heldAdmission {
	return &heldAdmission{entered: make(chan struct{}), release: make(chan struct{})}
}

func (h *heldAdmission) admit(ctx context.Context, s Settings, sess protocol.Session, l *Launch) error {
	h.once.Do(func() { close(h.entered) })
	select {
	case <-h.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return admitWorkspaceLaunch(ctx, s, sess, l)
}

func (h *heldAdmission) passDeadlineDuringAdmission(t *testing.T, clock *testClock, to time.Time) {
	t.Helper()
	select {
	case <-h.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the boot never reached the launch admission")
	}
	clock.Set(to)
	close(h.release)
}

// Admission can wait up to sharedGitAdminBudget for the workspace's lock. A
// first launch admitted after the deadline still starts nothing: no launch
// record, no tmux.
func TestDaemonRefusesAFirstLaunchAdmittedPastItsDeadline(t *testing.T) {
	deadline := deadlineNow.Add(time.Minute)
	r, clock := deadlineRig(t, protocol.ModeTask, deadline.Format(time.RFC3339))
	held := newHeldAdmission()
	r.d.admitLaunch = held.admit
	stop := r.start(t)
	held.passDeadlineDuringAdmission(t, clock, deadline.Add(time.Second))
	waitFor(t, func() bool {
		st, _ := r.beats.last()
		return st.Boot == protocol.BootFailed || st.Boot == protocol.BootReady
	})
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
}

// The control: a session that launched before keeps its resume, however long
// admission waits and wherever the clock is.
func TestDaemonResumesAfterAnAdmissionPastTheDeadline(t *testing.T) {
	deadline := deadlineNow.Add(time.Minute)
	r, clock := deadlineRig(t, protocol.ModeTask, deadline.Format(time.RFC3339))
	if _, step := PrepareRepo(context.Background(), r.d.R, r.d.S, r.d.Session); step.State != StepOK {
		t.Fatal(step.Notes)
	}
	first := Launch{Session: r.d.Session.Name, Prompt: "p", ConversationID: "11111111-2222-4333-8444-555555555555", BootID: "earlier", CreatedAt: deadlineNow.Add(-time.Hour)}
	if err := writeJSONFile(r.d.S.statePath(launchFile), first); err != nil {
		t.Fatal(err)
	}
	held := newHeldAdmission()
	r.d.admitLaunch = held.admit
	stop := r.start(t)
	held.passDeadlineDuringAdmission(t, clock, deadline.Add(time.Second))
	waitFor(t, r.tmuxStarted)
	waitFor(t, func() bool { st, _ := r.beats.last(); return st.Boot == protocol.BootReady })
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	var resumed Launch
	if err := readJSONFile(r.d.S.statePath(resumeFile), &resumed); err != nil || !resumed.Resume || resumed.ConversationID != first.ConversationID {
		t.Fatalf("resume %+v %v", resumed, err)
	}
}

// A remote session whose boot holds its agent (no usable login, or a login
// ceremony opening) has not launched. Its deadline still ends it: the poll
// that would start the agent reports the refusal once the deadline passes,
// and nothing starts it afterwards.
func TestDaemonEndsAHeldRemoteFirstLaunchAtItsDeadline(t *testing.T) {
	for name, ceremony := range map[string]bool{"login-required": false, "ceremony-opening": true} {
		t.Run(name, func(t *testing.T) {
			deadline := deadlineNow.Add(time.Minute)
			r, clock := deadlineRig(t, protocol.ModeRemote, deadline.Format(time.RFC3339))
			// lateLogin lands the login the held agent waits for.
			lateLogin := func() { writeCredentials(t, r.d.S, 0) }
			if ceremony {
				writeCredentials(t, r.d.S, 0)
				release, err := acquireLoginLock(r.d.S)
				if err != nil {
					t.Fatal(err)
				}
				var once sync.Once
				lateLogin = func() { once.Do(release) }
				t.Cleanup(lateLogin)
			}
			stop := r.start(t)
			waitFor(t, func() bool {
				st, _ := r.beats.last()
				return st.Boot == protocol.BootReady && (ceremony || st.Agent.State == protocol.AgentLoginRequired)
			})
			clock.Set(deadline.Add(time.Second))
			_, seen := r.beats.last()
			waitFor(t, func() bool { _, count := r.beats.last(); return count >= seen+3 })
			// The login lands after the refusal: the agent still never starts.
			lateLogin()
			_, seen = r.beats.last()
			waitFor(t, func() bool { _, count := r.beats.last(); return count >= seen+3 })
			if err := stop(); err != nil {
				t.Fatal(err)
			}
			st, _ := r.beats.last()
			if st.Boot != protocol.BootFailed || st.Agent.State != protocol.AgentFailed || st.Agent.Error != protocol.StartDeadlinePassed || !agentEnded(st) {
				t.Fatalf("status boot %q agent %+v, want boot failed and agent failed with %q", st.Boot, st.Agent, protocol.StartDeadlinePassed)
			}
			var rec bootRecord
			if err := readJSONFile(r.d.S.statePath(bootFile), &rec); err != nil || rec.Boot != protocol.BootFailed || rec.AgentError != protocol.StartDeadlinePassed {
				t.Fatalf("boot record %+v %v", rec, err)
			}
			if r.tmuxStarted() || exists(r.d.S.statePath(launchFile)) {
				t.Fatal("a held remote agent started after its start deadline")
			}
		})
	}
}

// A Codex remote session's agent is the phone daemon, and agentd keeps no
// launch record for it, so the controller refuses the annotation on such a
// session. agentd skips the check too, so a value that reached the pod some
// other way (a profile's envFrom) cannot refuse every boot of the session.
func TestDaemonIgnoresAStartDeadlineOnACodexRemoteSession(t *testing.T) {
	old := codexRemoteDepsFor
	codexRemoteDepsFor = func() codexRemoteDeps {
		// An unknown daemon state: the supervisor touches nothing.
		return codexRemoteDeps{process: func(Settings, bool) (codexHostProcess, error) { return codexHostProcess{}, errCodexHostUnknown }}
	}
	t.Cleanup(func() { codexRemoteDepsFor = old })
	r, _ := deadlineRig(t, protocol.ModeRemote, deadlineNow.Add(-time.Hour).Format(time.RFC3339))
	r.d.Session.Agent, r.d.Session.Model = protocol.AgentCodex, "gpt-6-luna"
	stop := r.start(t)
	waitFor(t, func() bool {
		st, _ := r.beats.last()
		return st.Boot == protocol.BootFailed || st.Boot == protocol.BootReady
	})
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	var rec bootRecord
	if err := readJSONFile(r.d.S.statePath(bootFile), &rec); err != nil || rec.Boot != protocol.BootReady || rec.AgentError != "" || rec.Workspace == nil {
		t.Fatalf("boot record %+v %v, want a full boot with no agent error", rec, err)
	}
	if st, _ := r.beats.last(); st.Agent.Error == protocol.StartDeadlinePassed {
		t.Fatalf("agent %+v", st.Agent)
	}
}
