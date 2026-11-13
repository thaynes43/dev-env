package agentd

// Story 791 (slice c of item 743; DESIGN-064 6.9): a summoned task checks its
// model before it starts and falls back once. Every test runs on run-agent's
// fake clock (runAgentNow). The pre-flight turns are an in-process provider
// stand-in (preflightTurn), so no turn depends on goroutine timing; the real
// start is the fake CLI, which records the argv it was started with. Three
// tests run the real turn runner: two on a fake CLI, to show its argument
// list and its stop at the limit, and one on TestMain's claude guard, to show
// no test can reach the real CLI. The classifier's cases are the streams the
// real CLI wrote (testdata/preflight) and fields taken from them. No test
// calls a real provider, and every test that starts a child process scrubs
// the cluster environment first.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

const preflightFallback = "claude-opus-5"

// preflightAccepted is the stream of a turn the model answered: a real
// response (a model name and non-zero usage) and a clean result.
const preflightAccepted = `{"type":"system","subtype":"init","model":"claude-opus-5-5","session_id":"probe"}
{"type":"assistant","message":{"model":"claude-opus-5-5","usage":{"input_tokens":3,"output_tokens":1},"content":[{"type":"text","text":"ok"}]}}
{"type":"result","subtype":"success","is_error":false,"num_turns":1}`

// scrubClusterEnv removes every KUBERNETES_* variable from this test
// process's environment for the test and points KUBECONFIG at a missing
// file, so no child process the test starts can reach a cluster (item 782
// comment 7521). It skips the test when it cannot.
func scrubClusterEnv(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "KUBERNETES_") {
			t.Setenv(k, "")
			if err := os.Unsetenv(k); err != nil {
				t.Skipf("cannot remove %s from the environment: %v", k, err)
			}
		}
	}
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing", "kubeconfig"))
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "KUBERNETES_") {
			t.Skip("the cluster environment could not be scrubbed")
		}
	}
}

// turn is one pre-flight turn the stand-in saw.
type turn struct {
	model string
	limit time.Duration
}

// standIn is the provider stand-in for pre-flight turns: outcome per model,
// and an optional hook that runs inside the turn (to move the fake clock or
// capture the state).
type standIn struct {
	t        *testing.T
	outcomes map[string]preflightOutcome
	during   func(model string, limit time.Duration)
	turns    []turn
}

func installStandIn(t *testing.T, outcomes map[string]preflightOutcome) *standIn {
	t.Helper()
	si := &standIn{t: t, outcomes: outcomes}
	restore := preflightTurn
	t.Cleanup(func() { preflightTurn = restore })
	preflightTurn = func(_ Launch, model string, limit time.Duration, _ <-chan os.Signal) preflightOutcome {
		si.turns = append(si.turns, turn{model, limit})
		if si.during != nil {
			si.during(model, limit)
		}
		out, ok := si.outcomes[model]
		if !ok {
			si.t.Errorf("a pre-flight turn ran on %s, which this test does not expect", model)
			return preflightOutcome{Failure: protocol.StartFailureNoAnswer, Gone: true}
		}
		return out
	}
	return si
}

var (
	answered      = preflightOutcome{Answered: true, Gone: true}
	modelRefused  = preflightOutcome{Failure: protocol.StartFailureModelRefused, Gone: true}
	quotaRefused  = preflightOutcome{Failure: protocol.StartFailureQuotaRefused, Gone: true}
	loginRejected = preflightOutcome{Failure: protocol.StartFailureLoginRejected, Gone: true}
	noAnswer      = preflightOutcome{Failure: protocol.StartFailureNoAnswer, Gone: true}
	ownLimit      = preflightOutcome{Failure: protocol.StartFailureNoAnswer, TimedOut: true, Gone: true}
)

// preflightRun is one run-agent of a summoned task's first launch.
type preflightRun struct {
	s       Settings
	sess    protocol.Session
	l       Launch
	clock   *time.Time
	code    int
	started []string // the real start's argv, nil when no provider started
	pane    string
}

// summonedTaskLaunch builds a summoned task's first launch with its fallback
// and a start deadline 15 minutes after loginNow, its real start the fake
// CLI, and tmux's handoff recorded as started.
func summonedTaskLaunch(t *testing.T) (Settings, protocol.Session, Launch, string) {
	t.Helper()
	s, sess, ws := summonedFixture(t, protocol.ModeTask)
	sess.FallbackModel = preflightFallback
	s.StartDeadline = loginNow.Add(15 * time.Minute).Format(time.RFC3339)
	l, err := BuildLaunch(s, sess, ws, "b1", loginNow)
	if err != nil {
		t.Fatal(err)
	}
	if l.Preflight == nil || l.Preflight.Model != sess.Model || l.Preflight.Fallback != preflightFallback {
		t.Fatalf("a summoned task's first launch has no pre-flight of its model and fallback: %+v", l.Preflight)
	}
	dir := t.TempDir()
	argvFile := filepath.Join(dir, "provider-argv")
	l.Argv[0] = fakeCLI(t, dir, `printf '%s\n' "$@" > "`+argvFile+`"
exit 0`)
	l.Dir, l.LogPath, l.EventsPath = dir, filepath.Join(dir, "task.log"), s.statePath(eventsFile)
	writeLaunch(t, s, l)
	if err := writeStartHandoff(s, l, handoffStarted, ""); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(s.statePath(bootFile), bootRecord{BootID: "b1", Boot: protocol.BootReady}); err != nil {
		t.Fatal(err)
	}
	return s, sess, l, argvFile
}

// runSummonedTask runs run-agent on l at now. atSpawn, when set, runs at
// each start-fence check (before each pre-flight turn and the real start),
// with its count from 1.
func runSummonedTask(t *testing.T, s Settings, sess protocol.Session, l Launch, argvFile string, now time.Time, atSpawn func(n int, clock *time.Time)) *preflightRun {
	t.Helper()
	r := &preflightRun{s: s, sess: sess, l: l, clock: &now}
	restoreNow, restoreHook := runAgentNow, beforeProviderSpawn
	t.Cleanup(func() { runAgentNow, beforeProviderSpawn = restoreNow, restoreHook })
	runAgentNow = func() time.Time { return *r.clock }
	n := 0
	beforeProviderSpawn = func(Launch) {
		n++
		if atSpawn != nil {
			atSpawn(n, r.clock)
		}
	}
	buf := &syncBuffer{}
	r.code = RunAgent(s.statePath(launchFile), buf, make(chan os.Signal), time.Second)
	r.pane = buf.String()
	if raw, err := os.ReadFile(argvFile); err == nil {
		r.started = strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	}
	return r
}

// status is the session's status at the run's clock plus d.
func (r *preflightRun) status(d time.Duration) protocol.Status {
	return CollectStatus(context.Background(), &fakeRunner{}, r.s, r.sess, r.clock.Add(d))
}

// mustStartOn fails unless the provider started once, on model.
func (r *preflightRun) mustStartOn(t *testing.T, model string) {
	t.Helper()
	if r.code != 0 || len(r.started) < 2 || r.started[0] != "--model" || r.started[1] != model {
		t.Fatalf("exit %d, provider argv %q, want a start on %s\n%s", r.code, r.started, model, r.pane)
	}
}

// mustStartNothing fails unless no provider started and run-agent left no
// task result, TUI exit or pid file.
func (r *preflightRun) mustStartNothing(t *testing.T) {
	t.Helper()
	if r.code == 0 || r.started != nil {
		t.Fatalf("exit %d, provider argv %q, want nothing started\n%s", r.code, r.started, r.pane)
	}
	for _, f := range []string{resultFile, tuiExitFile, pidFile} {
		if _, err := os.Stat(r.s.statePath(f)); !os.IsNotExist(err) {
			t.Fatalf("nothing started, yet %s exists", f)
		}
	}
}

func mustTurns(t *testing.T, si *standIn, want ...turn) {
	t.Helper()
	if len(si.turns) != len(want) {
		t.Fatalf("pre-flight turns %+v, want %+v", si.turns, want)
	}
	for i := range want {
		if si.turns[i] != want[i] {
			t.Fatalf("pre-flight turn %d is %+v, want %+v", i, si.turns[i], want[i])
		}
	}
}

// Proof 1: the first model answers. One pre-flight turn ran, with the full
// 90-second limit; the task started on the session's model; the status
// records it.
func TestPreflightFirstModelAnswers(t *testing.T) {
	s, sess, l, argvFile := summonedTaskLaunch(t)
	si := installStandIn(t, map[string]preflightOutcome{sess.Model: answered})
	r := runSummonedTask(t, s, sess, l, argvFile, loginNow.Add(time.Minute), nil)
	r.mustStartOn(t, sess.Model)
	mustTurns(t, si, turn{sess.Model, 90 * time.Second})
	st := r.status(time.Minute)
	if st.Start == nil || *st.Start != (protocol.StartRecord{Model: sess.Model}) {
		t.Fatalf("start record %+v, want the model %s", st.Start, sess.Model)
	}
	if st.Agent.State != protocol.AgentExited || st.Agent.Task == nil {
		t.Fatalf("agent %+v, want exited with its task result", st.Agent)
	}
}

// Proof 2: the first model is refused for a model error, for a quota error,
// or fails in a way agentd does not recognise (an unmatched API error, no
// entry, or its own 90-second limit): the fallback is tried once, the task
// starts on it, and the record names the first model's cause (op791-a-1).
func TestPreflightFallsBackOnce(t *testing.T) {
	for _, tc := range []struct {
		name  string
		first preflightOutcome
		cause string
	}{
		{"a refused model", modelRefused, protocol.StartFailureModelRefused},
		{"a quota refusal", quotaRefused, protocol.StartFailureQuotaRefused},
		{"an unrecognised failure", noAnswer, protocol.StartFailureNoAnswer},
		{"no answer inside its own limit", ownLimit, protocol.StartFailureNoAnswer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, sess, l, argvFile := summonedTaskLaunch(t)
			si := installStandIn(t, map[string]preflightOutcome{sess.Model: tc.first, preflightFallback: answered})
			r := runSummonedTask(t, s, sess, l, argvFile, loginNow.Add(time.Minute), nil)
			r.mustStartOn(t, preflightFallback)
			mustTurns(t, si, turn{sess.Model, 90 * time.Second}, turn{preflightFallback, 90 * time.Second})
			st := r.status(time.Minute)
			if st.Start == nil || *st.Start != (protocol.StartRecord{Model: preflightFallback, FallbackCause: tc.cause}) {
				t.Fatalf("start record %+v, want the fallback with cause %s", st.Start, tc.cause)
			}
			// The launch record agentd wrote is unchanged: only the spawn
			// named the fallback, so the usage receipt still binds it.
			var saved Launch
			if err := readJSONFile(s.statePath(launchFile), &saved); err != nil || saved.Argv[2] != sess.Model {
				t.Fatalf("launch record %v %v", saved.Argv, err)
			}
		})
	}
}

// Proof 3, agentd's half: neither model answers. No provider starts, no task
// result is written, the status reports failed, and the record names the
// last model's cause.
func TestPreflightNeitherAnswersStartsNothing(t *testing.T) {
	for _, tc := range []struct {
		name            string
		first, fallback preflightOutcome
		want            string
	}{
		{"model then quota", modelRefused, quotaRefused, protocol.StartFailureQuotaRefused},
		{"quota then no answer", quotaRefused, ownLimit, protocol.StartFailureNoAnswer},
		{"no answer then a refused model", noAnswer, modelRefused, protocol.StartFailureModelRefused},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, sess, l, argvFile := summonedTaskLaunch(t)
			si := installStandIn(t, map[string]preflightOutcome{sess.Model: tc.first, preflightFallback: tc.fallback})
			r := runSummonedTask(t, s, sess, l, argvFile, loginNow.Add(time.Minute), nil)
			r.mustStartNothing(t)
			mustTurns(t, si, turn{sess.Model, 90 * time.Second}, turn{preflightFallback, 90 * time.Second})
			for _, later := range []time.Duration{0, 10 * time.Minute} {
				st := r.status(later)
				if st.Start == nil || *st.Start != (protocol.StartRecord{Failure: tc.want}) {
					t.Fatalf("start record %+v, want failure %s", st.Start, tc.want)
				}
				if st.Agent.State != protocol.AgentFailed || st.Agent.Task != nil || !strings.Contains(st.Agent.Error, "no usable model") {
					t.Fatalf("agent %+v, want failed with no task result", st.Agent)
				}
			}
			if _, err := os.Stat(s.statePath(startRefusedFile)); !os.IsNotExist(err) {
				t.Fatal("no usable model is not the start fence's refusal")
			}
		})
	}
}

// Proof 4, agentd's half: a rejected login, on either turn, tries nothing
// further and starts nothing; the cause is recorded (ruling (d)). A turn
// whose process cannot be shown stopped starts nothing further either
// (op791-a-1, point 6).
func TestPreflightRejectedLoginTriesNoFallback(t *testing.T) {
	stuck := preflightOutcome{Failure: protocol.StartFailureNoAnswer, TimedOut: true, Gone: false}
	for _, tc := range []struct {
		name     string
		outcomes map[string]preflightOutcome
		turns    int
		want     string
	}{
		{"on the first model", map[string]preflightOutcome{"claude-opus-5-5": loginRejected}, 1, protocol.StartFailureLoginRejected},
		{"on the fallback", map[string]preflightOutcome{"claude-opus-5-5": modelRefused, preflightFallback: loginRejected}, 2, protocol.StartFailureLoginRejected},
		{"a turn that cannot be shown stopped", map[string]preflightOutcome{"claude-opus-5-5": stuck}, 1, protocol.StartFailureNoAnswer},
		{"an answer whose process cannot be shown stopped", map[string]preflightOutcome{"claude-opus-5-5": {Answered: true, Gone: false}}, 1, protocol.StartFailureNoAnswer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, sess, l, argvFile := summonedTaskLaunch(t)
			si := installStandIn(t, tc.outcomes)
			r := runSummonedTask(t, s, sess, l, argvFile, loginNow.Add(time.Minute), nil)
			r.mustStartNothing(t)
			if len(si.turns) != tc.turns {
				t.Fatalf("pre-flight turns %+v, want %d", si.turns, tc.turns)
			}
			if st := r.status(0); st.Start == nil || *st.Start != (protocol.StartRecord{Failure: tc.want}) || st.Agent.State != protocol.AgentFailed {
				t.Fatalf("status start %+v agent %+v, want failed %s", st.Start, st.Agent, tc.want)
			}
		})
	}
}

// Proof 5: the start fence (ruling (c)). No pre-flight turn starts at or past
// the start deadline; a turn's limit is cut to the time left; a turn the
// deadline cut short, and a pre-flight that ended past the deadline, are the
// fence's refusal as story 780 records it, never no-usable-model.
func TestPreflightInsideTheStartFence(t *testing.T) {
	deadline := loginNow.Add(15 * time.Minute)
	for _, tc := range []struct {
		name     string
		now      time.Time
		outcomes map[string]preflightOutcome
		during   func(clock *time.Time) func(string, time.Duration)
		atSpawn  func(n int, clock *time.Time)
		turns    []turn
	}{
		{name: "at the deadline: no turn starts", now: deadline},
		{name: "past the deadline: no turn starts", now: deadline.Add(time.Hour)},
		{name: "paused before the first turn past the deadline: no turn starts", now: deadline.Add(-10 * time.Minute),
			atSpawn: func(n int, clock *time.Time) { *clock = deadline.Add(time.Minute) }},
		{name: "the first turn is cut to the time left and the deadline cuts it short", now: deadline.Add(-30 * time.Second),
			outcomes: map[string]preflightOutcome{"claude-opus-5-5": ownLimit},
			during: func(clock *time.Time) func(string, time.Duration) {
				return func(string, time.Duration) { *clock = deadline }
			},
			turns: []turn{{"claude-opus-5-5", 30 * time.Second}}},
		{name: "the fallback's turn is cut to the time left and the deadline cuts it short", now: deadline.Add(-2 * time.Minute),
			outcomes: map[string]preflightOutcome{"claude-opus-5-5": modelRefused, preflightFallback: ownLimit},
			during: func(clock *time.Time) func(string, time.Duration) {
				return func(m string, limit time.Duration) { *clock = clock.Add(limit) }
			},
			turns: []turn{{"claude-opus-5-5", 90 * time.Second}, {preflightFallback, 30 * time.Second}}},
		{name: "the first turn ends past the deadline: the fallback does not start", now: deadline.Add(-time.Minute),
			outcomes: map[string]preflightOutcome{"claude-opus-5-5": modelRefused},
			during: func(clock *time.Time) func(string, time.Duration) {
				return func(string, time.Duration) { *clock = deadline }
			},
			turns: []turn{{"claude-opus-5-5", time.Minute}}},
		{name: "the pre-flight answers past the deadline: the real start is refused", now: deadline.Add(-time.Minute),
			outcomes: map[string]preflightOutcome{"claude-opus-5-5": answered},
			during: func(clock *time.Time) func(string, time.Duration) {
				return func(string, time.Duration) { *clock = deadline.Add(time.Second) }
			},
			turns: []turn{{"claude-opus-5-5", time.Minute}}},
		{name: "paused between the pre-flight and the real start past the deadline", now: deadline.Add(-10 * time.Minute),
			outcomes: map[string]preflightOutcome{"claude-opus-5-5": answered},
			atSpawn: func(n int, clock *time.Time) {
				if n == 2 {
					*clock = deadline.Add(time.Minute)
				}
			},
			turns: []turn{{"claude-opus-5-5", 90 * time.Second}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, sess, l, argvFile := summonedTaskLaunch(t)
			si := installStandIn(t, tc.outcomes)
			var clockRef *time.Time
			if tc.during != nil {
				si.during = func(m string, limit time.Duration) { tc.during(clockRef)(m, limit) }
			}
			hook := func(n int, clock *time.Time) {
				clockRef = clock
				if tc.atSpawn != nil {
					tc.atSpawn(n, clock)
				}
			}
			r := runSummonedTask(t, s, sess, l, argvFile, tc.now, hook)
			r.mustStartNothing(t)
			mustTurns(t, si, tc.turns...)
			var refused startRefused
			if err := readJSONFile(s.statePath(startRefusedFile), &refused); err != nil || refused.Reason != protocol.StartDeadlinePassed || refused.ConversationID != l.ConversationID {
				t.Fatalf("the fence's refusal record %+v %v\n%s", refused, err, r.pane)
			}
			st := r.status(0)
			if st.Agent.State != protocol.AgentFailed || !strings.Contains(st.Agent.Error, "run-agent refused to start the provider") || st.Start != nil {
				t.Fatalf("status agent %+v start %+v, want the fence's failed and no start record", st.Agent, st.Start)
			}
		})
	}
}

// Proof 6: story 780's latch with the pre-flight in the path. An agentd
// restart before the pre-flight, during it, and between it and the real
// start: the later boot starts nothing (no tmux, so no second pre-flight and
// no provider), and the status reads interrupted, never busy or failed.
// TestSummonedPausedLaunchRefusedPastStartDeadline, TestRunAgentStartFence,
// TestSummonedTmuxStartFailureKeepsTheLaunch and TestSummonedStartHandoffRecord
// now run their task cases with the pre-flight in the path too (fakeCLI
// answers it).
func TestPreflightRestartStartsNothingAgain(t *testing.T) {
	for _, point := range []string{"before the pre-flight", "during it", "between it and the real start"} {
		t.Run(point, func(t *testing.T) {
			s, sess, ws := summonedFixture(t, protocol.ModeTask)
			sess.FallbackModel = preflightFallback
			deadline := loginNow.Add(15 * time.Minute)
			s.StartDeadline = deadline.Format(time.RFC3339)
			dir := t.TempDir()
			ranFile := filepath.Join(dir, "provider-ran")
			s.ClaudeBin = fakeCLI(t, dir, `echo ran >> "`+ranFile+`"
exit 0`)
			snapshot := ""
			take := func() {
				if snapshot == "" {
					snapshot = copyStateDir(t, s.StateDir)
				}
			}
			si := installStandIn(t, map[string]preflightOutcome{sess.Model: answered})
			if point == "during it" {
				si.during = func(string, time.Duration) { take() }
			}
			restoreNow, restoreHook := runAgentNow, beforeProviderSpawn
			t.Cleanup(func() { runAgentNow, beforeProviderSpawn = restoreNow, restoreHook })
			runAgentNow = func() time.Time { return deadline.Add(-10 * time.Minute) }
			spawns := 0
			beforeProviderSpawn = func(Launch) {
				if spawns++; spawns == 2 && point == "between it and the real start" {
					take()
				}
			}
			runner := &fakeRunner{handle: func(c Cmd) (Result, error) {
				switch {
				case len(c.Args) > 0 && c.Args[0] == "has-session":
					return Result{}, errNoSession
				case len(c.Args) > 0 && c.Args[0] == "new-session":
					if point == "before the pre-flight" {
						take()
					}
					path := c.Args[len(c.Args)-1]
					var l Launch
					if err := readJSONFile(path, &l); err != nil {
						t.Fatal(err)
					}
					for _, d := range []string{l.Dir, filepath.Dir(l.LogPath)} {
						if err := os.MkdirAll(d, 0o700); err != nil {
							t.Fatal(err)
						}
					}
					RunAgent(path, &syncBuffer{}, make(chan os.Signal), time.Second)
				}
				return Result{}, nil
			}}
			d := quietDaemon(s, sess, runner, deadline.Add(-10*time.Minute))
			if step, why := d.startAgent(context.Background(), ws, Step{Name: "repo", State: StepOK}, "b1"); why != "" || step.State == StepFail {
				t.Fatalf("first launch: %+v %q", step, why)
			}
			if snapshot == "" {
				t.Fatal("the run never reached the restart point")
			}
			// The pod's container restarted at that point: the run-agent that
			// ran the pre-flight is gone with it.
			s2 := s
			s2.StateDir = snapshot
			var rec startPreflight
			if readJSONFile(s2.statePath(startPreflightFile), &rec) == nil {
				rec.Pid, rec.Start = 0, ""
				if err := writeWorkspaceJSON(s2.statePath(startPreflightFile), rec); err != nil {
					t.Fatal(err)
				}
			}
			if err := writeJSONFile(s2.statePath(bootFile), bootRecord{BootID: "b2", Boot: protocol.BootReady}); err != nil {
				t.Fatal(err)
			}
			turns := len(si.turns)
			runner2 := &fakeRunner{}
			d2 := quietDaemon(s2, sess, runner2, deadline.Add(-5*time.Minute))
			if step, why := d2.startAgent(context.Background(), ws, Step{Name: "repo", State: StepOK}, "b2"); why != "" || step.State == StepFail || len(runner2.calls) != 0 {
				t.Fatalf("the later boot started again: %+v %q %+v", step, why, runner2.calls)
			}
			if len(si.turns) != turns {
				t.Fatalf("a second pre-flight ran: %+v", si.turns)
			}
			st := CollectStatus(context.Background(), &fakeRunner{}, s2, sess, deadline.Add(-5*time.Minute))
			if st.Agent.State != protocol.AgentInterrupted || st.Start != nil {
				t.Fatalf("after the restart: agent %+v start %+v, want interrupted with no start record", st.Agent, st.Start)
			}
		})
	}
}

// copyStateDir copies agentd's state directory, as it stands, to a new one.
func copyStateDir(t *testing.T, from string) string {
	t.Helper()
	to := filepath.Join(t.TempDir(), ".agentd")
	if out, err := exec.Command("cp", "-a", from, to).CombinedOutput(); err != nil {
		t.Fatalf("copy the state: %v %s", err, out)
	}
	return to
}

// While run-agent runs the pre-flight, past the start window and with no
// provider pid yet, the status reads busy, whatever tmux's handoff record
// says; once that run-agent is gone, interrupted. A signal stops the
// pre-flight and starts nothing.
func TestPreflightStatusWhileItRuns(t *testing.T) {
	for _, handoff := range []string{handoffStarted, handoffAsked} {
		t.Run(handoff, func(t *testing.T) {
			s, sess, l, argvFile := summonedTaskLaunch(t)
			if handoff == handoffAsked {
				if err := writeStartHandoff(s, l, handoffAsked, ""); err != nil {
					t.Fatal(err)
				}
			}
			var during protocol.Status
			si := installStandIn(t, map[string]preflightOutcome{sess.Model: {Stopped: true, Failure: protocol.StartFailureNoAnswer, Gone: true}})
			si.during = func(string, time.Duration) {
				during = CollectStatus(context.Background(), &fakeRunner{}, s, sess, loginNow.Add(5*time.Minute))
			}
			r := runSummonedTask(t, s, sess, l, argvFile, loginNow.Add(4*time.Minute), nil)
			r.mustStartNothing(t)
			if during.Agent.State != protocol.AgentBusy || during.Start != nil {
				t.Fatalf("during the pre-flight: agent %+v start %+v, want busy", during.Agent, during.Start)
			}
			var rec startPreflight
			if err := readJSONFile(s.statePath(startPreflightFile), &rec); err != nil || rec.State != preflightChecking {
				t.Fatalf("pre-flight record %+v %v", rec, err)
			}
			rec.Pid, rec.Start = 0, ""
			if err := writeWorkspaceJSON(s.statePath(startPreflightFile), rec); err != nil {
				t.Fatal(err)
			}
			if st := r.status(time.Minute); st.Agent.State != protocol.AgentInterrupted {
				t.Fatalf("after run-agent is gone: agent %+v, want interrupted", st.Agent)
			}
		})
	}
}

// Proof 7: an ordinary session (no spec.caller), task, local and remote, gets
// no pre-flight, and neither does a summoned remote session (743d); a launch
// record and a session document without the new fields read as before; and
// an ordinary task runs exactly as before, its status with no start record.
func TestPreflightOnlyForASummonedTask(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mode   string
		caller string
		want   bool
	}{
		{"ordinary task", protocol.ModeTask, "", false},
		{"ordinary local", protocol.ModeLocal, "", false},
		{"ordinary remote", protocol.ModeRemote, "", false},
		{"summoned remote", protocol.ModeRemote, summonedCaller, false},
		{"summoned task", protocol.ModeTask, summonedCaller, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, sess, ws := summonedFixture(t, tc.mode)
			sess.Caller = tc.caller
			if tc.caller == "" && tc.mode == protocol.ModeRemote {
				sess.Prompt = ""
			}
			if tc.caller != "" {
				sess.FallbackModel = preflightFallback
			}
			s.StartDeadline = loginNow.Add(15 * time.Minute).Format(time.RFC3339)
			l, err := BuildLaunch(s, sess, ws, "b1", loginNow)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(l)
			if has := l.Preflight != nil; has != tc.want || strings.Contains(string(raw), `"preflight"`) != tc.want {
				t.Fatalf("pre-flight %+v, want present=%v: %s", l.Preflight, tc.want, raw)
			}
			if tc.caller != "" && tc.mode == protocol.ModeRemote {
				// A summoned remote session's start is unchanged by its
				// fallback: the same launch but its fresh conversation id.
				sess.FallbackModel = ""
				before, err := BuildLaunch(s, sess, ws, "b1", loginNow)
				if err != nil {
					t.Fatal(err)
				}
				before.ConversationID = l.ConversationID
				for i := 1; i < len(before.Argv) && i < len(l.Argv); i++ {
					if before.Argv[i-1] == "--session-id" {
						before.Argv[i] = l.Argv[i]
					}
				}
				a, _ := json.Marshal(before)
				if string(a) != string(raw) {
					t.Fatalf("a summoned remote launch changed with its fallback:\n%s\n%s", a, raw)
				}
			}
		})
	}
	// An older launch record has no pre-flight.
	var old Launch
	if err := json.Unmarshal([]byte(`{"session":"s-1","argv":["claude","--model","claude-opus-5-5"],"dir":"/w","logPath":"/l","eventsPath":"/e","conversationId":"c","bootId":"b","createdAt":"2026-11-01T00:00:00Z","startDeadline":"2026-11-01T00:15:00Z"}`), &old); err != nil || old.Preflight != nil {
		t.Fatalf("an older launch record: %+v %v", old, err)
	}
	// A session document without fallbackModel reads as before; the field
	// is for a summoned session only, and must be a full Claude id.
	doc := `{"name":"s-1","repo":"dev-env","agent":"claude","mode":"task","model":"claude-opus-5-5","prompt":"p"%s}`
	if sess, err := parseSessionDoc(strings.Replace(doc, "%s", "", 1)); err != nil || sess.FallbackModel != "" {
		t.Fatalf("a document without the field: %+v %v", sess, err)
	}
	if _, err := parseSessionDoc(strings.Replace(doc, "%s", `,"fallbackModel":"claude-opus-5"`, 1)); err == nil {
		t.Fatal("an ordinary session's document with a fallback was accepted")
	}
	if _, err := parseSessionDoc(strings.Replace(doc, "%s", `,"caller":"c","fallbackModel":"opus"`, 1)); err == nil {
		t.Fatal("an alias as the fallback was accepted")
	}
	if sess, err := parseSessionDoc(strings.Replace(doc, "%s", `,"caller":"c","fallbackModel":"claude-opus-5"`, 1)); err != nil || sess.FallbackModel != "claude-opus-5" {
		t.Fatalf("a summoned session's fallback: %+v %v", sess, err)
	}
}

// parseSessionDoc is protocol.ParseSession on a string.
func parseSessionDoc(doc string) (protocol.Session, error) { return protocol.ParseSession([]byte(doc)) }

// An ordinary task and a launch record written before this change run no
// pre-flight turn and start as before; no pre-flight record is written and
// the status has no start record.
func TestOrdinaryTaskRunsNoPreflight(t *testing.T) {
	for _, tc := range []string{"ordinary task", "summoned task, older launch record"} {
		t.Run(tc, func(t *testing.T) {
			s, sess, l, argvFile := summonedTaskLaunch(t)
			if tc == "ordinary task" {
				sess.Caller, sess.FallbackModel = "", ""
			}
			l.Preflight, l.StartDeadline = nil, ""
			writeLaunch(t, s, l)
			si := installStandIn(t, nil)
			r := runSummonedTask(t, s, sess, l, argvFile, loginNow.Add(time.Hour), nil)
			r.mustStartOn(t, sess.Model)
			if len(si.turns) != 0 {
				t.Fatalf("pre-flight turns %+v", si.turns)
			}
			if _, err := os.Stat(s.statePath(startPreflightFile)); !os.IsNotExist(err) {
				t.Fatal("a pre-flight record was written")
			}
			st := r.status(time.Minute)
			raw, _ := json.Marshal(st)
			if st.Start != nil || strings.Contains(string(raw), `"start"`) || st.Agent.State != protocol.AgentExited {
				t.Fatalf("status %s", raw)
			}
		})
	}
}

// preflightStream is one stream Claude Code 2.1.321 really wrote for a
// pre-flight turn (testdata/preflight/README.md says whose capture each is).
func preflightStream(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "preflight", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// Ruling 1 of story 791 fix round 1 (PR 1120 comment 6843361182): the
// pre-flight reads the fields the stream really carries. Each case is a real
// stream: an answer, a refused model, a missing login and a rejected token.
// Ruling 2: the answer counts only for the model that was asked.
func TestClassifyPreflightRealStreams(t *testing.T) {
	for _, tc := range []struct {
		name, file, asked string
		exitOK            bool
		answered          bool
		failure           string
	}{
		{"an answer", "answer.jsonl", "claude-opus-5-5", true, true, ""},
		{"an answer from a CLI that exited non-zero", "answer.jsonl", "claude-opus-5-5", false, false, protocol.StartFailureNoAnswer},
		{"an answer from a model that was not asked", "answer.jsonl", "claude-opus-5", true, false, protocol.StartFailureModelRefused},
		{"a refused model", "refused-model.jsonl", "claude-nonexistent-0-0", false, false, protocol.StartFailureModelRefused},
		{"a missing login", "login-missing.jsonl", "claude-opus-5-5", false, false, protocol.StartFailureLoginRejected},
		{"a rejected token", "login-rejected.jsonl", "claude-opus-5-5", false, false, protocol.StartFailureLoginRejected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			answered, failure := classifyPreflight([]byte(preflightStream(t, tc.file)), tc.exitOK, tc.asked)
			if answered != tc.answered || failure != tc.failure {
				t.Fatalf("answered=%v failure=%q, want %v %q", answered, failure, tc.answered, tc.failure)
			}
		})
	}
}

// The classifier's other cases, each built from a real stream's fields with
// one thing changed. The error code and status decide first, in either
// spelling of the flag and the status; the CLI's wordings are a second line,
// read only on an API-error entry, so a reply that quotes them is an answer.
// The quota cases are NOT VERIFIED AGAINST A REAL STREAM: no quota refusal
// could be captured, so they rest on the CLI's own error codes and strings
// (rate_limit, billing_error, 429) and v1's fragments.
func TestClassifyPreflight(t *testing.T) {
	const asked = "claude-opus-5-5"
	// apiError is the refused-model stream's assistant entry with its text and
	// its top-level fields replaced.
	apiError := func(text, fields string) string {
		return `{"type":"system","subtype":"init","model":"claude-opus-5-5"}
{"type":"assistant","message":{"model":"<synthetic>","content":[{"type":"text","text":"` + text + `"}],"usage":{"input_tokens":0,"output_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}` + fields + `}
{"type":"result","subtype":"success","is_error":true}`
	}
	// answer is the answer stream with its model and text replaced.
	answer := func(model, text string) string {
		return `{"type":"system","subtype":"init","model":"` + asked + `"}
{"type":"assistant","message":{"model":"` + model + `","content":[{"type":"text","text":"` + text + `"}],"usage":{"input_tokens":2,"output_tokens":4}}}
{"type":"result","subtype":"success","is_error":false}`
	}
	for _, tc := range []struct {
		name     string
		stream   string
		asked    string
		exitOK   bool
		answered bool
		failure  string
	}{
		// Ruling 2: which model answered.
		{"the asked model with a date suffix", answer("claude-opus-5-5-20261101", "ok"), asked, true, true, ""},
		{"the asked model with [1m]", answer("claude-opus-5-5[1m]", "ok"), asked, true, true, ""},
		{"asked with [1m], answered without", answer("claude-opus-5-5", "ok"), "claude-opus-5-5[1m]", true, true, ""},
		{"another model answered", answer("claude-sonnet-5-5", "ok"), asked, true, false, protocol.StartFailureModelRefused},
		{"a longer id is another model", answer("claude-opus-5-5", "ok"), "claude-opus-5", true, false, protocol.StartFailureModelRefused},
		{"another dated snapshot is another model", answer("claude-opus-5-5-20250101", "ok"), "claude-opus-5-5-20261101", true, false, protocol.StartFailureModelRefused},
		{"a suffix that is not a date", answer("claude-opus-5-5-beta", "ok"), asked, true, false, protocol.StartFailureModelRefused},
		// The model_fallback entry is the CLI's own schema (2.1.321), not a
		// captured stream.
		{"the CLI fell back to another model by itself",
			apiError("There's an issue with the selected model (claude-opus-5-5).", `,"error":"model_not_found","is_api_error_message":true,"api_error_status":404`) + "\n" +
				`{"type":"system","subtype":"model_fallback","trigger":"model_not_found","original_model":"claude-opus-5-5","fallback_model":"claude-sonnet-5-5"}` + "\n" +
				answer("claude-sonnet-5-5", "ok"), asked, true, false, protocol.StartFailureModelRefused},
		{"a reply that quotes every wording", answer(asked, "ok: not logged in, please run /login, failed to authenticate, model_not_found, usage limit, rate limit, unknown model, may not exist or you may not have access"), asked, true, true, ""},
		// Both spellings of the flag and the status.
		{"the transcript's flag spelling", apiError("There's an issue with the selected model (x). It may not exist or you may not have access to it.", `,"isApiErrorMessage":true`), asked, false, false, protocol.StartFailureModelRefused},
		{"the transcript's status spelling", apiError("Failed to authenticate.", `,"isApiErrorMessage":true,"apiErrorStatus":401`), asked, false, false, protocol.StartFailureLoginRejected},
		{"a status as a string", apiError("Failed.", `,"is_api_error_message":true,"api_error_status":"401"`), asked, false, false, protocol.StartFailureLoginRejected},
		// The code and the status decide before the text.
		{"a 401 by its status alone", apiError("API Error: request failed", `,"is_api_error_message":true,"api_error_status":401`), asked, false, false, protocol.StartFailureLoginRejected},
		{"a 404 by its status alone", apiError("API Error: request failed", `,"is_api_error_message":true,"api_error_status":404`), asked, false, false, protocol.StartFailureModelRefused},
		{"the error code before the text", apiError("Please run /login", `,"error":"model_not_found","is_api_error_message":true`), asked, false, false, protocol.StartFailureModelRefused},
		{"an error code without the flag", apiError("Not logged in", `,"error":"authentication_failed"`), asked, false, false, protocol.StartFailureLoginRejected},
		// The wordings, as a second line.
		{"a missing login by its text", apiError("Not logged in · Please run /login", `,"is_api_error_message":true`), asked, false, false, protocol.StartFailureLoginRejected},
		{"a rejected token by its text", apiError("Failed to authenticate. API Error: 401 Invalid bearer token", `,"is_api_error_message":true`), asked, false, false, protocol.StartFailureLoginRejected},
		{"a revoked token by its text", apiError("API Error: 401 OAuth token has been revoked", ""), asked, false, false, protocol.StartFailureLoginRejected},
		{"a refused model by its text", apiError("There's an issue with the selected model (x). It may not exist or you may not have access to it.", `,"is_api_error_message":true`), asked, false, false, protocol.StartFailureModelRefused},
		{"a refused model by v1's fragment", apiError("API Error: 404 unknown model claude-fable-9", ""), asked, false, false, protocol.StartFailureModelRefused},
		// Quota: NOT VERIFIED AGAINST A REAL STREAM.
		{"quota, not verified: rate_limit", apiError("API Error: Request rejected (429) · this may be a temporary capacity issue.", `,"error":"rate_limit","is_api_error_message":true,"api_error_status":429`), asked, false, false, protocol.StartFailureQuotaRefused},
		{"quota, not verified: billing_error", apiError("API Error: credit balance too low", `,"error":"billing_error","is_api_error_message":true,"api_error_status":400`), asked, false, false, protocol.StartFailureQuotaRefused},
		{"quota, not verified: a 429 alone", apiError("API Error: request failed", `,"is_api_error_message":true,"api_error_status":429`), asked, false, false, protocol.StartFailureQuotaRefused},
		{"quota, not verified: v1's fragment", apiError("You're out of usage credits for this model", `,"is_api_error_message":true`), asked, false, false, protocol.StartFailureQuotaRefused},
		{"quota, not verified: a usage limit", apiError("API Error: Claude usage limit reached", ""), asked, false, false, protocol.StartFailureQuotaRefused},
		// No answer.
		{"an unmatched API error", apiError("API Error: 500 internal server error", `,"error":"server_error","is_api_error_message":true,"api_error_status":500`), asked, false, false, protocol.StartFailureNoAnswer},
		{"an overloaded model", apiError("API Error: the API is at capacity", `,"error":"overloaded","is_api_error_message":true,"api_error_status":529`), asked, false, false, protocol.StartFailureNoAnswer},
		{"refusal words outside an API error", `{"type":"assistant","message":{"model":"<synthetic>","content":[{"type":"text","text":"usage limit, not logged in"}]}}`, asked, false, false, protocol.StartFailureNoAnswer},
		{"a synthetic entry is never an answer", answer("<synthetic>", "ok"), asked, true, false, protocol.StartFailureNoAnswer},
		{"no assistant entry", `{"type":"system","subtype":"init","model":"claude-opus-5-5"}`, asked, false, false, protocol.StartFailureNoAnswer},
		{"no output at all", "", asked, false, false, protocol.StartFailureNoAnswer},
		{"output that is not JSON", "Error: something went wrong\n", asked, false, false, protocol.StartFailureNoAnswer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			answered, failure := classifyPreflight([]byte(tc.stream), tc.exitOK, tc.asked)
			if answered != tc.answered || failure != tc.failure {
				t.Fatalf("answered=%v failure=%q, want %v %q", answered, failure, tc.answered, tc.failure)
			}
		})
	}
}

// The real turn runner: the CLI it starts gets exactly the pre-flight's
// argument list, with no tools (--tools ""), no user, project or local
// settings and so none of their hooks (--setting-sources "", ruling 4 of
// story 791 fix round 1), no MCP servers, no saved session, one turn and no
// --dangerously-skip-permissions, and "reply with ok" on stdin; its answer is
// read from its stream (op791-a-1, point 7). The limit is the real one, so
// only a CLI that does not exit could reach it.
func TestPreflightTurnArgumentsAndPrompt(t *testing.T) {
	scrubClusterEnv(t)
	dir := t.TempDir()
	args, stdin := filepath.Join(dir, "args"), filepath.Join(dir, "stdin")
	cli := filepath.Join(dir, "fake-claude")
	answer := strings.ReplaceAll(preflightAccepted, `"claude-opus-5-5"`, `"claude-opus-5"`)
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '[%s]\\n' \"$a\"; done > '" + args + "'\ncat > '" + stdin + "'\ncat <<'EOF'\n" + answer + "\nEOF\n"
	if err := os.WriteFile(cli, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	l := Launch{Argv: []string{cli, "--model", "claude-opus-5-5"}, Dir: dir}
	out := runPreflightTurn(l, "claude-opus-5", preflightTurnLimit, make(chan os.Signal))
	if !out.Answered || !out.Gone || out.Failure != "" {
		t.Fatalf("outcome %+v, want answered", out)
	}
	gotArgs, _ := os.ReadFile(args)
	want := "[--model]\n[claude-opus-5]\n[--tools]\n[]\n[--setting-sources]\n[]\n[--strict-mcp-config]\n[--no-session-persistence]\n[--max-turns]\n[1]\n[--output-format]\n[stream-json]\n[--verbose]\n[-p]\n"
	if string(gotArgs) != want {
		t.Fatalf("argument list\n%s\nwant\n%s", gotArgs, want)
	}
	if strings.Contains(string(gotArgs), "dangerously") {
		t.Fatal("a pre-flight turn may not skip permissions")
	}
	if got, _ := os.ReadFile(stdin); string(got) != preflightPrompt {
		t.Fatalf("stdin %q, want %q", got, preflightPrompt)
	}
}

// Ruling 6 of story 791 fix round 1: no test in this package can reach the
// real Claude CLI by accident. Settings.ClaudeBin defaults to "claude", so a
// test that builds a launch and forgets its fake CLI would start whatever
// "claude" PATH names, the real one in a session pod. TestMain puts
// realClaudeGuard first on PATH; its claude answers nothing and exits 97.
// The bare name, the default settings' launch and a pre-flight turn on that
// launch each reach the guard. The first check fails before anything starts.
func TestNoTestReachesTheRealClaude(t *testing.T) {
	scrubClusterEnv(t)
	if realClaudeGuard == "" {
		t.Fatal("TestMain put no claude guard on PATH")
	}
	want := filepath.Join(realClaudeGuard, "claude")
	if got, err := exec.LookPath("claude"); err != nil || got != want {
		t.Fatalf("claude resolves to %q (%v), want the guard %s", got, err, want)
	}
	s, sess, ws := summonedFixture(t, protocol.ModeTask)
	l, err := BuildLaunch(s, sess, ws, "b1", loginNow)
	if err != nil {
		t.Fatal(err)
	}
	if l.Argv[0] != "claude" || l.Preflight == nil {
		t.Fatalf("the default settings' launch: argv %q pre-flight %+v", l.Argv, l.Preflight)
	}
	out, err := exec.Command(l.Argv[0], "--version").CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 97 || !strings.Contains(string(out), "not the real claude") {
		t.Fatalf("the default CLI ran: %v %q", err, out)
	}
	l.Dir = t.TempDir()
	if res := runPreflightTurn(l, sess.Model, preflightTurnLimit, make(chan os.Signal)); res.Answered || !res.Gone || res.Failure != protocol.StartFailureNoAnswer {
		t.Fatalf("a pre-flight turn on the default CLI: %+v", res)
	}
}

// The real turn runner stops a turn at its limit and waits for its process
// before it returns, so the next turn or the real start never runs beside it
// (op791-a-1, point 6). The CLI sleeps far longer than the limit. Under load
// the turn may be stopped before its script wrote its pid: then Gone alone
// shows it ended (ruling 3 of story 791 fix round 1).
func TestPreflightTurnStoppedAtItsLimit(t *testing.T) {
	scrubClusterEnv(t)
	dir := t.TempDir()
	cli := filepath.Join(dir, "fake-claude")
	pidFile := filepath.Join(dir, "pid")
	if err := os.WriteFile(cli, []byte("#!/bin/sh\necho $$ > '"+pidFile+"'\nexec sleep 300\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	l := Launch{Argv: []string{cli, "--model", "claude-opus-5-5"}, Dir: dir}
	out := runPreflightTurn(l, "claude-opus-5-5", 200*time.Millisecond, make(chan os.Signal))
	if !out.TimedOut || !out.Gone || out.Answered || out.Failure != protocol.StartFailureNoAnswer {
		t.Fatalf("outcome %+v, want stopped at its limit, gone, no answer", out)
	}
	if raw, err := os.ReadFile(pidFile); err == nil {
		// Empty when the stop came between the shell's open and its write.
		pid := strings.TrimSpace(string(raw))
		if _, err := os.Stat("/proc/" + pid); pid != "" && err == nil {
			if stat, _ := os.ReadFile("/proc/" + pid + "/stat"); !strings.Contains(string(stat), ") Z ") {
				t.Fatalf("the turn's process %s still runs", pid)
			}
		}
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if out := runPreflightTurn(l, "claude-opus-5-5", 0, make(chan os.Signal)); out.Answered || !out.TimedOut || !out.Gone {
		t.Fatalf("a turn with no time left: %+v", out)
	}
}
