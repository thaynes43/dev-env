package agentd

// Story 791 (slice c of item 743; DESIGN-064 6.9): a summoned task checks its
// model before it starts and falls back once. Every test runs on run-agent's
// fake clock (runAgentNow). The pre-flight turns are an in-process provider
// stand-in (preflightTurn), so no turn depends on goroutine timing; the real
// start is the fake CLI, which records the argv it was started with. Two
// tests run the real turn runner on a fake CLI, to show its argument list
// and its stop at the limit. No test calls a real provider, and every test
// that starts a child process scrubs the cluster environment first.

import (
	"context"
	"encoding/json"
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

// preflightAnswerSh answers a pre-flight turn in a fake CLI (fakeCLI).
const preflightAnswerSh = `case " $* " in *" --no-session-persistence "*)
cat > /dev/null
printf '%s\n' '{"type":"assistant","message":{"model":"claude-opus-5-5","usage":{"input_tokens":3,"output_tokens":1},"content":[{"type":"text","text":"ok"}]}}' '{"type":"result","subtype":"success","is_error":false,"num_turns":1}'
exit 0;;
esac
`

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

// classifyPreflight reads a turn with agentd's transcript classifier: an
// answer is a real model response from a CLI that exited 0; a rejected login
// is its Revoked; an API error names a quota or model refusal by v1's
// fragments; anything else is no answer.
func TestClassifyPreflight(t *testing.T) {
	apiError := func(text, extra string) string {
		return `{"type":"assistant","message":{"model":"<synthetic>","usage":{"input_tokens":0,"output_tokens":0},"content":[{"type":"text","text":"` + text + `"}]}` + extra + `}
{"type":"result","subtype":"success","is_error":true,"num_turns":1}`
	}
	for _, tc := range []struct {
		name     string
		stream   string
		exitOK   bool
		answered bool
		failure  string
	}{
		{"an answer", preflightAccepted, true, true, ""},
		{"an answer from a CLI that exited non-zero", preflightAccepted, false, false, protocol.StartFailureNoAnswer},
		{"an answer that quotes a refusal", strings.Replace(preflightAccepted, `"text":"ok"`, `"text":"ok, no rate limit or unknown model here"`, 1), true, true, ""},
		{"a 401 by its error code", apiError("API Error: 401 OAuth token has been revoked", `,"error":"authentication_failed"`), false, false, protocol.StartFailureLoginRejected},
		{"a 401 by its text", apiError("API Error: 401 authentication_error: invalid bearer token", ""), false, false, protocol.StartFailureLoginRejected},
		{"a quota refusal", apiError("API Error: You're out of usage credits for this model", ""), false, false, protocol.StartFailureQuotaRefused},
		{"a usage limit", apiError("API Error: 429 Claude usage limit reached", ""), false, false, protocol.StartFailureQuotaRefused},
		{"a rate limit", apiError("API Error: Rate limit exceeded", ""), false, false, protocol.StartFailureQuotaRefused},
		{"an unknown model", apiError("API Error: 404 unknown model claude-fable-9", ""), false, false, protocol.StartFailureModelRefused},
		{"a model not available", apiError("API Error: claude-fable-5-2 is not available on this plan; use /model to switch", ""), false, false, protocol.StartFailureModelRefused},
		{"an unmatched API error", apiError("API Error: 500 internal server error", ""), false, false, protocol.StartFailureNoAnswer},
		{"refusal words outside an API error", `{"type":"assistant","message":{"model":"<synthetic>","content":[{"type":"text","text":"usage limit"}]}}`, false, false, protocol.StartFailureNoAnswer},
		{"no output at all", "", false, false, protocol.StartFailureNoAnswer},
		{"output that is not JSON", "Error: something went wrong\n", false, false, protocol.StartFailureNoAnswer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			answered, failure := classifyPreflight([]byte(tc.stream), tc.exitOK)
			if answered != tc.answered || failure != tc.failure {
				t.Fatalf("answered=%v failure=%q, want %v %q", answered, failure, tc.answered, tc.failure)
			}
		})
	}
}

// The real turn runner: the CLI it starts gets exactly the pre-flight's
// argument list, with no tools (--tools ""), no MCP servers, no saved
// session, one turn and no --dangerously-skip-permissions, and "reply with
// ok" on stdin; its answer is read from its stream (op791-a-1, point 7).
func TestPreflightTurnArgumentsAndPrompt(t *testing.T) {
	scrubClusterEnv(t)
	dir := t.TempDir()
	args, stdin := filepath.Join(dir, "args"), filepath.Join(dir, "stdin")
	cli := filepath.Join(dir, "fake-claude")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '[%s]\\n' \"$a\"; done > '" + args + "'\ncat > '" + stdin + "'\ncat <<'EOF'\n" + preflightAccepted + "\nEOF\n"
	if err := os.WriteFile(cli, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	l := Launch{Argv: []string{cli, "--model", "claude-opus-5-5"}, Dir: dir}
	out := runPreflightTurn(l, "claude-opus-5", time.Minute, make(chan os.Signal))
	if !out.Answered || !out.Gone || out.Failure != "" {
		t.Fatalf("outcome %+v, want answered", out)
	}
	gotArgs, _ := os.ReadFile(args)
	want := "[--model]\n[claude-opus-5]\n[--tools]\n[]\n[--strict-mcp-config]\n[--no-session-persistence]\n[--max-turns]\n[1]\n[--output-format]\n[stream-json]\n[--verbose]\n[-p]\n"
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

// The real turn runner stops a turn at its limit and waits for its process
// before it returns, so the next turn or the real start never runs beside it
// (op791-a-1, point 6). The CLI sleeps far longer than the limit.
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
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid := strings.TrimSpace(string(raw))
	if _, err := os.Stat("/proc/" + pid); err == nil {
		if stat, _ := os.ReadFile("/proc/" + pid + "/stat"); !strings.Contains(string(stat), ") Z ") {
			t.Fatalf("the turn's process %s still runs", pid)
		}
	}
	if out := runPreflightTurn(l, "claude-opus-5-5", 0, make(chan os.Signal)); out.Answered || !out.TimedOut || !out.Gone {
		t.Fatalf("a turn with no time left: %+v", out)
	}
}
