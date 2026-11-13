package agentd

// The agentd half of story 780 (slice d of item 741; DESIGN-064 5.9, N2): a
// summoned session's provider starts once and never past its start deadline.
// A boot that finds a summoned session's launch record starts nothing, in
// every mode; run-agent checks the launch's start deadline immediately before
// it spawns the provider. Ordinary sessions boot and run exactly as before.
// Every test runs on a fake clock; the pause between the launch record and
// the spawn is a hook, not goroutine timing.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

const summonedCaller = "alert-ns.alert-responder"

// errNoSession is tmux has-session's answer when the agent's session is absent.
var errNoSession = errors.New("can't find session")

// summonedFixture is launchFixture's session made summoned in mode, with a
// usable login so a remote boot is not held for one.
func summonedFixture(t *testing.T, mode string) (Settings, protocol.Session, protocol.Workspace) {
	t.Helper()
	s, sess, ws := launchFixture(t)
	sess.Mode, sess.Caller = mode, summonedCaller
	if mode != protocol.ModeTask {
		sess.Limits = nil
		if mode == protocol.ModeLocal {
			sess.Prompt = ""
		}
	}
	if err := os.MkdirAll(s.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeCredentials(t, s, loginNow.Add(30*24*time.Hour).UnixMilli())
	return s, sess, ws
}

func quietDaemon(s Settings, sess protocol.Session, r Runner, now time.Time) *Daemon {
	return &Daemon{S: s, R: r, Session: sess, Self: "/agentd", Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return now }}
}

// 7265 point 2 and DESIGN-064 5.9: a boot of a summoned session that finds its
// launch record (a same-pod agentd restart, or a recreated pod on the same
// volume) starts nothing in any mode: no tmux, no TUI resume, no provider,
// whatever the repo step did. Its status says how the first run ended.
func TestSummonedLaunchRecordStartsNothingOnBoot(t *testing.T) {
	for _, mode := range []string{protocol.ModeTask, protocol.ModeLocal, protocol.ModeRemote} {
		for _, ended := range []string{"interrupted", "exited", "refused"} {
			for _, repo := range []string{StepOK, StepFail} {
				t.Run(mode+"/"+ended+"/repo-"+repo, func(t *testing.T) {
					s, sess, ws := summonedFixture(t, mode)
					first, err := BuildLaunch(s, sess, ws, "first-boot", loginNow.Add(-time.Hour))
					if err != nil {
						t.Fatal(err)
					}
					if err := writeJSONFile(s.statePath(launchFile), first); err != nil {
						t.Fatal(err)
					}
					// The handoff record a tmux start that succeeded leaves
					// (fix round 3). Without one the launch reads "asked",
					// which is never exited (TestSummonedStartHandoffStatusTable).
					if err := writeJSONFile(s.statePath(handoffRecordName), handoffOnDisk{State: "started", ConversationID: first.ConversationID, BootID: first.BootID}); err != nil {
						t.Fatal(err)
					}
					switch {
					case ended == "exited" && mode == protocol.ModeTask:
						if err := writeJSONFile(s.statePath(resultFile), taskResult{TaskResult: protocol.TaskResult{ExitCode: 0, FinishedAt: loginNow.Add(-30 * time.Minute)}, BootID: "first-boot"}); err != nil {
							t.Fatal(err)
						}
					case ended == "exited":
						if err := writeJSONFile(s.statePath(tuiExitFile), tuiExit{ExitCode: 0, StartedAt: first.CreatedAt, FinishedAt: loginNow.Add(-30 * time.Minute), ConversationID: first.ConversationID, BootID: "first-boot"}); err != nil {
							t.Fatal(err)
						}
					case ended == "refused":
						if err := writeJSONFile(s.statePath(startRefusedFile), startRefused{Reason: protocol.StartDeadlinePassed, At: first.CreatedAt, ConversationID: first.ConversationID, BootID: "first-boot"}); err != nil {
							t.Fatal(err)
						}
					}
					runner := &fakeRunner{}
					d := quietDaemon(s, sess, runner, loginNow)
					step, why := d.startAgent(context.Background(), ws, Step{Name: "repo", State: repo}, "rebooted")
					if why != "" || step.State == StepFail || len(runner.calls) != 0 {
						t.Fatalf("a summoned session started again: step %+v why %q calls %+v", step, why, runner.calls)
					}
					if d.remote != nil {
						t.Fatal("a summoned remote boot kept a held launch the poll could start")
					}
					if _, err := os.Lstat(s.statePath(resumeFile)); !os.IsNotExist(err) {
						t.Fatalf("a resume launch was written: %v", err)
					}
					if _, err := BuildResume(s, sess, ws, first, "rebooted", loginNow); err == nil {
						t.Fatal("BuildResume accepted a summoned session")
					}
					// The heartbeat of the boot that started nothing.
					if err := writeJSONFile(s.statePath(bootFile), bootRecord{BootID: "rebooted", Boot: protocol.BootReady}); err != nil {
						t.Fatal(err)
					}
					if mode == protocol.ModeRemote {
						// No login: still not login-required, since nothing will start.
						if err := os.Remove(filepath.Join(s.ClaudeConfigDir, credentialsFileName)); err != nil {
							t.Fatal(err)
						}
					}
					st := CollectStatus(context.Background(), &fakeRunner{}, s, sess, loginNow)
					want := map[string]string{"interrupted": protocol.AgentInterrupted, "exited": protocol.AgentExited, "refused": protocol.AgentFailed}[ended]
					if st.Agent.State != want {
						t.Fatalf("status %+v, want %s", st.Agent, want)
					}
					if ended == "interrupted" && strings.Contains(st.Agent.Error, "D-58") {
						t.Fatalf("a summoned session's status promises a resume: %q", st.Agent.Error)
					}
				})
			}
		}
	}
}

// A summoned task's explicit continuation is refused too (N3): it would be a
// second provider start.
func TestSummonedTaskRefusesExplicitContinuation(t *testing.T) {
	s, sess, _ := summonedFixture(t, protocol.ModeTask)
	sess.PrivateHistoryRetained = true
	if err := writeJSONFile(s.statePath(launchFile), Launch{Session: sess.Name, ConversationID: "c"}); err != nil {
		t.Fatal(err)
	}
	err := explicitSessionAllowed(s, sess, ExplicitClaudeRCRequest{SessionUID: sess.SessionUID})
	if err == nil || !strings.Contains(err.Error(), "summoned") {
		t.Fatalf("explicit continuation of a summoned task: %v", err)
	}
}

// Today's sessions (proof 7): an ordinary session's boot with an existing
// launch record resumes exactly as before, in task, local and remote mode, and
// BuildResume carries no start deadline.
func TestOrdinaryBootWithLaunchRecordResumesAsToday(t *testing.T) {
	for _, mode := range []string{protocol.ModeTask, protocol.ModeLocal, protocol.ModeRemote} {
		t.Run(mode, func(t *testing.T) {
			s, sess, ws := summonedFixture(t, mode)
			sess.Caller = ""
			if mode == protocol.ModeRemote {
				sess.Prompt = ""
			}
			s.StartDeadline = loginNow.Add(-time.Hour).Format(time.RFC3339) // a scheduled session's, long passed
			first, err := BuildLaunch(s, sess, ws, "first-boot", loginNow.Add(-2*time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			if first.StartDeadline != "" {
				t.Fatalf("an ordinary first launch carries a start deadline: %q", first.StartDeadline)
			}
			if err := writeJSONFile(s.statePath(launchFile), first); err != nil {
				t.Fatal(err)
			}
			runner := &fakeRunner{handle: func(c Cmd) (Result, error) {
				if len(c.Args) > 0 && c.Args[0] == "has-session" {
					return Result{}, errNoSession
				}
				return Result{}, nil
			}}
			d := quietDaemon(s, sess, runner, loginNow)
			step, why := d.startAgent(context.Background(), ws, Step{Name: "repo", State: StepOK}, "rebooted")
			if why != "" || step.State == StepFail {
				t.Fatalf("ordinary resume failed: %+v %q", step, why)
			}
			var resumed Launch
			if err := readJSONFile(s.statePath(resumeFile), &resumed); err != nil {
				t.Fatalf("no resume launch: %v", err)
			}
			if !resumed.Resume || !resumed.TUI || resumed.ConversationID != first.ConversationID || resumed.StartDeadline != "" {
				t.Fatalf("resume launch %+v", resumed)
			}
			started := false
			for _, c := range runner.calls {
				if len(c.Args) > 0 && c.Args[0] == "new-session" && strings.Contains(strings.Join(c.Args, " "), "run-agent --launch "+s.statePath(resumeFile)) {
					started = true
				}
			}
			if !started {
				t.Fatalf("the resume was not started in tmux: %+v", runner.calls)
			}
		})
	}
}

// BuildLaunch writes the start deadline only into a summoned session's first
// launch; a launch record without it, every older record, reads as before.
func TestBuildLaunchWritesStartDeadlineOnlyForSummoned(t *testing.T) {
	deadline := loginNow.Add(15 * time.Minute).Format(time.RFC3339)
	for _, tc := range []struct {
		name, caller, deadline, want string
	}{
		{"summoned", summonedCaller, deadline, deadline},
		{"summoned-no-deadline", summonedCaller, "", ""},
		{"scheduled-ordinary", "", deadline, ""},
		{"ordinary", "", "", ""},
	} {
		for _, mode := range []string{protocol.ModeTask, protocol.ModeRemote} {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				s, sess, ws := summonedFixture(t, mode)
				sess.Caller = tc.caller
				if mode == protocol.ModeRemote && tc.caller == "" {
					sess.Prompt = ""
				}
				s.StartDeadline = tc.deadline
				l, err := BuildLaunch(s, sess, ws, "b1", loginNow)
				if err != nil {
					t.Fatal(err)
				}
				if l.StartDeadline != tc.want {
					t.Fatalf("startDeadline %q, want %q", l.StartDeadline, tc.want)
				}
				raw, err := json.Marshal(l)
				if err != nil {
					t.Fatal(err)
				}
				if has := strings.Contains(string(raw), `"startDeadline"`); has != (tc.want != "") {
					t.Fatalf("the record's bytes carry startDeadline=%v: %s", has, raw)
				}
			})
		}
	}
	// An older record has no such field and decodes to none.
	var old Launch
	if err := json.Unmarshal([]byte(`{"session":"s-1","argv":["claude"],"dir":"/w","logPath":"/l","eventsPath":"/e","conversationId":"c","bootId":"b","createdAt":"2026-11-01T00:00:00Z"}`), &old); err != nil || old.StartDeadline != "" {
		t.Fatalf("an older launch record: %+v %v", old, err)
	}
}

// runSpawnFence runs run-agent on l with the fake CLI, which leaves a marker
// if it runs, and its clock at now. pause, when set, runs between the launch
// record and the spawn.
func runSpawnFence(t *testing.T, s Settings, l Launch, now time.Time, pause func()) (code int, ran bool, pane string) {
	t.Helper()
	dir := t.TempDir()
	marker := filepath.Join(dir, "provider-ran")
	cli := fakeCLI(t, dir, `: > "`+marker+`"
exit 0`)
	l.Argv[0] = cli
	l.Dir, l.LogPath, l.EventsPath = dir, filepath.Join(dir, "task.log"), s.statePath(eventsFile)
	path := writeLaunch(t, s, l)
	restoreNow, restoreHook := runAgentNow, beforeProviderSpawn
	t.Cleanup(func() { runAgentNow, beforeProviderSpawn = restoreNow, restoreHook })
	clock := now
	runAgentNow = func() time.Time { return clock }
	beforeProviderSpawn = func(Launch) {
		if pause != nil {
			pause()
			clock = clock.Add(time.Nanosecond) // the pause ends after it began
		}
	}
	buf := &syncBuffer{}
	code = RunAgent(path, buf, make(chan os.Signal), time.Second)
	_, err := os.Stat(marker)
	return code, err == nil, buf.String()
}

// run-agent's start fence (N2): a launch that carries a start deadline spawns
// its provider before it and spawns nothing at or past it, for a task and a
// TUI; a launch without one spawns whatever the time. A summoned task's model
// pre-flight runs in the in-process stand-in fakeCLI installs, so a spawn one
// nanosecond before the deadline does not depend on how fast a process
// starts (ruling 3 of story 791 fix round 1).
func TestRunAgentStartFence(t *testing.T) {
	deadline := loginNow.Add(15 * time.Minute)
	for _, mode := range []string{protocol.ModeTask, protocol.ModeRemote} {
		for _, tc := range []struct {
			name     string
			deadline string
			now      time.Time
			spawns   bool
		}{
			{"before", deadline.Format(time.RFC3339), deadline.Add(-time.Second), true},
			{"just-before", deadline.Format(time.RFC3339), deadline.Add(-time.Nanosecond), true},
			{"at", deadline.Format(time.RFC3339), deadline, false},
			{"past", deadline.Format(time.RFC3339), deadline.Add(time.Hour), false},
			{"invalid", "tomorrow", loginNow, false},
			{"none-past", "", deadline.Add(time.Hour), true},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				s, sess, ws := summonedFixture(t, mode)
				l, err := BuildLaunch(s, sess, ws, "b1", loginNow)
				if err != nil {
					t.Fatal(err)
				}
				l.StartDeadline = tc.deadline
				code, ran, pane := runSpawnFence(t, s, l, tc.now, nil)
				if ran != tc.spawns {
					t.Fatalf("provider ran=%v, want %v (exit %d)\n%s", ran, tc.spawns, code, pane)
				}
				_, refusedErr := os.Stat(s.statePath(startRefusedFile))
				if tc.spawns {
					if code != 0 || refusedErr == nil {
						t.Fatalf("exit %d, refusal record present=%v\n%s", code, refusedErr == nil, pane)
					}
					return
				}
				if code == 0 || refusedErr != nil || !strings.Contains(pane, "the provider was not started") {
					t.Fatalf("refusal: exit %d, record err %v\n%s", code, refusedErr, pane)
				}
				for _, f := range []string{resultFile, tuiExitFile, pidFile} {
					if _, err := os.Stat(s.statePath(f)); !os.IsNotExist(err) {
						t.Fatalf("a refused spawn left %s", f)
					}
				}
				if err := writeJSONFile(s.statePath(bootFile), bootRecord{BootID: "b1", Boot: protocol.BootReady}); err != nil {
					t.Fatal(err)
				}
				if st := CollectStatus(context.Background(), &fakeRunner{}, s, sess, tc.now); st.Agent.State != protocol.AgentFailed || !strings.Contains(st.Agent.Error, "refused to start the provider") {
					t.Fatalf("status %+v, want failed by the fence", st.Agent)
				}
			})
		}
	}
}

// N2, agentd's half (DESIGN-064 5.9, fix round 2): the daemon writes a
// summoned session's first launch record before its start deadline; the path
// pauses between that record and the provider spawn while the clock passes
// the deadline (the operator closes and releases the session and admits the
// next in its lane meanwhile: TestSummonPausedLaunchSpawnsNoProviderAfterRelease
// in internal/controller runs both halves together); resumed, run-agent
// refuses and no provider starts. For a task and for a remote session.
func TestSummonedPausedLaunchRefusedPastStartDeadline(t *testing.T) {
	for _, mode := range []string{protocol.ModeTask, protocol.ModeRemote} {
		t.Run(mode, func(t *testing.T) {
			s, sess, ws := summonedFixture(t, mode)
			deadline := loginNow.Add(15 * time.Minute)
			s.StartDeadline = deadline.Format(time.RFC3339)
			var launchPath string
			runner := &fakeRunner{handle: func(c Cmd) (Result, error) {
				if len(c.Args) > 0 && c.Args[0] == "has-session" {
					return Result{}, errNoSession
				}
				if len(c.Args) > 0 && c.Args[0] == "new-session" {
					// tmux would start run-agent now; the test holds it.
					launchPath = c.Args[len(c.Args)-1]
				}
				return Result{}, nil
			}}
			d := quietDaemon(s, sess, runner, deadline.Add(-10*time.Minute))
			step, why := d.startAgent(context.Background(), ws, Step{Name: "repo", State: StepOK}, "first-boot")
			if why != "" || step.State == StepFail || launchPath != s.statePath(launchFile) {
				t.Fatalf("first launch: %+v %q path %q", step, why, launchPath)
			}
			var l Launch
			if err := readJSONFile(launchPath, &l); err != nil || l.StartDeadline != s.StartDeadline {
				t.Fatalf("launch record %+v %v: the start deadline is not in it", l, err)
			}
			paused := false
			code, ran, pane := runSpawnFence(t, s, l, deadline.Add(-10*time.Minute), func() {
				paused = true
				runAgentNow = func() time.Time { return deadline.Add(time.Minute) }
			})
			if !paused || ran || code == 0 {
				t.Fatalf("paused=%v provider ran=%v exit %d\n%s", paused, ran, code, pane)
			}
			// A later boot of the same pod or a recreated one starts nothing
			// either: the launch record is there.
			runner.calls = nil
			d2 := quietDaemon(s, sess, runner, deadline.Add(2*time.Minute))
			if step, why := d2.startAgent(context.Background(), ws, Step{Name: "repo", State: StepOK}, "second-boot"); why != "" || step.State == StepFail || len(runner.calls) != 0 {
				t.Fatalf("a later boot started again: %+v %q %+v", step, why, runner.calls)
			}
		})
	}
}

// F1 of story 780 fix round 2 (coordinator rulings in PR 1100 comment
// 6839380816): when the tmux start of a summoned session's first launch
// returns an error, its launch record stays, whether or not the provider may
// have started, so no later boot starts it again: no tmux, no provider. The
// status says how that launch ended, in the boot that failed and after it.
// Since fix round 3 a launch that may have started reads busy inside the start
// window of the boot that failed (TestSummonedStartHandoffRecord); after it,
// and on every later boot, it is never busy, idle or waiting:
//
//   - interrupted when tmux may have started run-agent: the fake tmux loads
//     the launch and runs it, the provider starts once, and then tmux
//     reports a failure or gives no answer; and a lost answer where in fact
//     nothing ran, which agentd cannot tell apart;
//   - failed when nothing can have started: tmux was never asked (its
//     session already exists), or the tmux client could not be run; and when
//     tmux ran run-agent but its start fence refused the spawn (the
//     record-written, nothing-spawned case).
//
// An ordinary session keeps today's behaviour: the record goes and a later
// boot starts the launch again.
func TestSummonedTmuxStartFailureKeepsTheLaunch(t *testing.T) {
	deadline := loginNow.Add(15 * time.Minute)
	lost := errors.New("tmux: signal: killed")
	for _, mode := range []string{protocol.ModeTask, protocol.ModeRemote} {
		for _, tc := range []struct {
			name string
			// ordinary: no caller.
			ordinary bool
			// exists: tmux has-session finds a session, so new-session is
			// never sent.
			exists bool
			// load: the fake tmux loads the launch and runs run-agent on it
			// before it answers newSessionErr; spawnAt is run-agent's clock.
			load    bool
			spawnAt time.Time
			err     error
			// want is the agent state; started is how often the provider ran.
			want    string
			started int
		}{
			{name: "tmux loads and runs the launch, then reports a failure", load: true, spawnAt: loginNow, err: &CmdError{Name: "tmux", Sub: "new-session", ExitCode: 1, Stderr: "server exited unexpectedly"}, want: protocol.AgentInterrupted, started: 1},
			{name: "tmux loads and runs the launch, then gives no answer", load: true, spawnAt: loginNow, err: lost, want: protocol.AgentInterrupted, started: 1},
			{name: "tmux gives no answer and nothing ran", err: lost, want: protocol.AgentInterrupted},
			{name: "tmux runs run-agent, whose fence refuses the spawn", load: true, spawnAt: deadline.Add(time.Minute), err: lost, want: protocol.AgentFailed},
			{name: "the tmux session already exists", exists: true, want: protocol.AgentFailed},
			{name: "the tmux client cannot be run", err: fmt.Errorf("tmux: %w", &exec.Error{Name: "tmux", Err: exec.ErrNotFound}), want: protocol.AgentFailed},
			{name: "ordinary: the record goes and a later boot starts again", ordinary: true, err: &CmdError{Name: "tmux", Sub: "new-session", ExitCode: 1, Stderr: "no server"}},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				ctx := context.Background()
				s, sess, ws := summonedFixture(t, mode)
				if tc.ordinary {
					sess.Caller = ""
					if mode == protocol.ModeRemote {
						sess.Prompt = ""
					}
				}
				s.StartDeadline = deadline.Format(time.RFC3339)
				dir := t.TempDir()
				ran := filepath.Join(dir, "provider-ran")
				s.ClaudeBin = fakeCLI(t, dir, `echo ran >> "`+ran+`"
exit 0`)
				restoreNow := runAgentNow
				t.Cleanup(func() { runAgentNow = restoreNow })
				runAgentNow = func() time.Time { return tc.spawnAt }
				runner := &fakeRunner{handle: func(c Cmd) (Result, error) {
					switch {
					case len(c.Args) > 0 && c.Args[0] == "has-session" && !tc.exists:
						return Result{}, errNoSession
					case len(c.Args) > 0 && c.Args[0] == "new-session":
						if tc.load {
							path := c.Args[len(c.Args)-1]
							var l Launch
							if err := readJSONFile(path, &l); err != nil {
								t.Fatalf("tmux could not load the launch: %v", err)
							}
							for _, d := range []string{l.Dir, filepath.Dir(l.LogPath)} {
								if err := os.MkdirAll(d, 0o700); err != nil {
									t.Fatal(err)
								}
							}
							RunAgent(path, &syncBuffer{}, make(chan os.Signal), time.Second)
						}
						return Result{}, tc.err
					}
					return Result{}, nil
				}}
				starts := func() int {
					n := 0
					for _, c := range runner.calls {
						if len(c.Args) > 0 && c.Args[0] == "new-session" {
							n++
						}
					}
					return n
				}
				providerRuns := func() int {
					data, err := os.ReadFile(ran)
					if errors.Is(err, os.ErrNotExist) {
						return 0
					} else if err != nil {
						t.Fatal(err)
					}
					return strings.Count(string(data), "ran\n")
				}
				d := quietDaemon(s, sess, runner, loginNow)
				step, why := d.startAgent(ctx, ws, Step{Name: "repo", State: StepOK}, "first-boot")
				if why == "" || step.State != StepFail {
					t.Fatalf("the failed tmux start was not reported: %+v %q", step, why)
				}
				_, recordErr := os.Lstat(s.statePath(launchFile))
				if tc.ordinary {
					if recordErr == nil {
						t.Fatal("an ordinary launch record survived a failed tmux start")
					}
					if _, err := os.Lstat(s.statePath(handoffRecordName)); !os.IsNotExist(err) {
						t.Fatalf("an ordinary failed start wrote a handoff record: %v", err)
					}
					runner.calls = nil
					d2 := quietDaemon(s, sess, runner, loginNow.Add(time.Minute))
					d2.startAgent(ctx, ws, Step{Name: "repo", State: StepOK}, "second-boot")
					if starts() != 1 {
						t.Fatalf("an ordinary later boot did not start the launch again: %+v", runner.calls)
					}
					return
				}
				if recordErr != nil {
					t.Fatalf("the summoned launch record went with the failed tmux start: %v", recordErr)
				}
				var first Launch
				if err := readJSONFile(s.statePath(launchFile), &first); err != nil {
					t.Fatal(err)
				}
				wantStatus := func(when string, at time.Time) {
					t.Helper()
					st := CollectStatus(ctx, &fakeRunner{}, s, sess, at)
					want := tc.want
					if want == protocol.AgentInterrupted && when == "the boot that failed, inside the start window" {
						// Fix round 3: tmux may still be starting it.
						want = protocol.AgentBusy
					}
					if st.Agent.State != want {
						t.Fatalf("%s: status %+v, want %s", when, st.Agent, want)
					}
					if st.Agent.ConversationID != first.ConversationID || strings.Contains(st.Agent.Error, "D-58") {
						t.Fatalf("%s: status %+v", when, st.Agent)
					}
					if mode == protocol.ModeTask && (st.Agent.Task != nil) != (tc.started == 1) {
						t.Fatalf("%s: the task's own result is not in the status: %+v", when, st.Agent.Task)
					}
				}
				// The heartbeat of the boot that failed. Inside the start
				// window a launch that may have started reads busy (fix round
				// 3); after it, how the launch ended.
				if err := writeJSONFile(s.statePath(bootFile), bootRecord{BootID: "first-boot", Boot: protocol.BootFailed, AgentError: why}); err != nil {
					t.Fatal(err)
				}
				wantStatus("the boot that failed, inside the start window", loginNow)
				wantStatus("the boot that failed, after the start window", loginNow.Add(startWindow))

				// Later boots, of the same pod or a recreated one, start nothing.
				for i, at := range []time.Duration{30 * time.Second, 2 * time.Minute, time.Hour} {
					boot := fmt.Sprintf("boot-%d", i+2)
					runner.calls = nil
					runAgentNow = func() time.Time { return loginNow.Add(at) }
					d2 := quietDaemon(s, sess, runner, loginNow.Add(at))
					if step, why := d2.startAgent(ctx, ws, Step{Name: "repo", State: StepOK}, boot); why != "" || step.State == StepFail || len(runner.calls) != 0 {
						t.Fatalf("%s: a later boot started again: %+v %q %+v", boot, step, why, runner.calls)
					}
					if err := writeJSONFile(s.statePath(bootFile), bootRecord{BootID: boot, Boot: protocol.BootReady}); err != nil {
						t.Fatal(err)
					}
					wantStatus(boot, loginNow.Add(at))
				}
				if got := providerRuns(); got != tc.started {
					t.Fatalf("the provider ran %d times, want %d", got, tc.started)
				}
			})
		}
	}
}

// handoffOnDisk is the part of ~/.agentd/start-handoff.json these tests read.
type handoffOnDisk struct {
	State          string `json:"state"`
	ConversationID string `json:"conversationId"`
	BootID         string `json:"bootId"`
}

const handoffRecordName = "start-handoff.json"

// R2-F1a of story 780 fix round 3 (coordinator rulings in PR 1100 comment
// 6840263445): before agentd asks tmux to start a summoned session's first
// launch, it durably writes a handoff record, state "asked", naming that
// launch. If that write fails, tmux is not called at all. After tmux returns,
// agentd updates the record; if the update fails, the record still says
// "asked" (here: a directory stands in its place, so it cannot be read, which
// counts as "asked" too). The status follows the record, never the boot's
// agent error or the start window of a later boot:
//
//   - "asked" or "ambiguous": a live process is busy; otherwise busy inside
//     the start window of the launch's own boot, and interrupted after it
//     and at once on any later boot. Never failed.
//   - "started": as today; here the provider ran and left its result or
//     exit record, so exited, and busy while a process is live (remote).
//
// For a task and for a remote session, no later boot starts tmux or the
// provider again.
func TestSummonedStartHandoffRecord(t *testing.T) {
	ambiguous := &CmdError{Name: "tmux", Sub: "new-session", ExitCode: 1, Stderr: "server exited unexpectedly"}
	readErr := fmt.Errorf("tmux: %w", &fs.PathError{Op: "read", Path: "|0", Err: os.ErrClosed})
	for _, mode := range []string{protocol.ModeTask, protocol.ModeRemote} {
		for _, tc := range []struct {
			name string
			// blockFirst: a directory stands where the record goes, so its
			// first write fails. blockUpdate: the fake tmux puts one there
			// after the first write, so the update fails.
			blockFirst, blockUpdate bool
			// load: the fake tmux loads the launch and runs run-agent on it
			// before it answers err.
			load bool
			err  error
			// record is the record's state after the start; "" when it
			// cannot be read.
			record    string
			stepFails bool
			// tmux is how often new-session was sent; started how often the
			// provider ran.
			tmux, started int
		}{
			{name: "the first write fails", blockFirst: true, stepFails: true},
			{name: "the update fails after an ambiguous error", blockUpdate: true, load: true, err: ambiguous, stepFails: true, tmux: 1, started: 1},
			{name: "the update fails after an ambiguous error and nothing ran", blockUpdate: true, err: ambiguous, stepFails: true, tmux: 1},
			{name: "the update fails after a success", blockUpdate: true, load: true, tmux: 1, started: 1},
			{name: "tmux loads the launch and returns a wrapped read path error", load: true, err: readErr, record: "ambiguous", stepFails: true, tmux: 1, started: 1},
			{name: "an ambiguous error", err: ambiguous, record: "ambiguous", stepFails: true, tmux: 1},
			{name: "a success", load: true, record: "started", tmux: 1, started: 1},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				ctx := context.Background()
				s, sess, ws := summonedFixture(t, mode)
				s.StartDeadline = loginNow.Add(15 * time.Minute).Format(time.RFC3339)
				dir := t.TempDir()
				ran := filepath.Join(dir, "provider-ran")
				s.ClaudeBin = fakeCLI(t, dir, `echo ran >> "`+ran+`"
exit 0`)
				restoreNow := runAgentNow
				t.Cleanup(func() { runAgentNow = restoreNow })
				runAgentNow = func() time.Time { return loginNow }
				record := s.statePath(handoffRecordName)
				block := func() {
					t.Helper()
					_ = os.Remove(record)
					if err := os.Mkdir(record, 0o700); err != nil {
						t.Fatal(err)
					}
				}
				if tc.blockFirst {
					block()
				}
				var atTmux handoffOnDisk
				runner := &fakeRunner{handle: func(c Cmd) (Result, error) {
					switch {
					case len(c.Args) > 0 && c.Args[0] == "has-session":
						return Result{}, errNoSession
					case len(c.Args) > 0 && c.Args[0] == "new-session":
						// The record is on disk, naming this launch, before
						// tmux is asked.
						if err := readJSONFile(record, &atTmux); err != nil {
							t.Errorf("tmux was asked before the handoff record was written: %v", err)
						}
						if tc.load {
							path := c.Args[len(c.Args)-1]
							var l Launch
							if err := readJSONFile(path, &l); err != nil {
								t.Fatalf("tmux could not load the launch: %v", err)
							}
							for _, d := range []string{l.Dir, filepath.Dir(l.LogPath)} {
								if err := os.MkdirAll(d, 0o700); err != nil {
									t.Fatal(err)
								}
							}
							RunAgent(path, &syncBuffer{}, make(chan os.Signal), time.Second)
						}
						if tc.blockUpdate {
							block()
						}
						return Result{}, tc.err
					}
					return Result{}, nil
				}}
				count := func(sub string) int {
					n := 0
					for _, c := range runner.calls {
						if len(c.Args) > 0 && c.Args[0] == sub {
							n++
						}
					}
					return n
				}
				d := quietDaemon(s, sess, runner, loginNow)
				step, why := d.startAgent(ctx, ws, Step{Name: "repo", State: StepOK}, "first-boot")
				if tc.stepFails != (why != "" && step.State == StepFail) {
					t.Fatalf("start step %+v %q, want failed=%v", step, why, tc.stepFails)
				}
				if tc.blockFirst && len(runner.calls) != 0 {
					t.Fatalf("tmux was called although the handoff record was not written: %+v", runner.calls)
				}
				if got := count("new-session"); got != tc.tmux {
					t.Fatalf("new-session sent %d times, want %d", got, tc.tmux)
				}
				var first Launch
				if err := readJSONFile(s.statePath(launchFile), &first); err != nil {
					t.Fatalf("the summoned launch record went: %v", err)
				}
				if tc.tmux > 0 && (atTmux.State != "asked" || atTmux.ConversationID != first.ConversationID || atTmux.BootID != first.BootID) {
					t.Fatalf("the record when tmux was asked: %+v, want asked for %s/%s", atTmux, first.ConversationID, first.BootID)
				}
				var after handoffOnDisk
				if err := readJSONFile(record, &after); (err == nil) != (tc.record != "") || after.State != tc.record {
					t.Fatalf("the record after the start: %+v %v, want %q", after, err, tc.record)
				}

				me := agentPid{Pid: os.Getpid(), Start: procStartTime(os.Getpid())}
				want := func(live bool, boot string, at time.Duration) string {
					t.Helper()
					switch {
					case tc.record == "started" && mode == protocol.ModeRemote && live:
						return protocol.AgentBusy
					case tc.record == "started":
						return protocol.AgentExited
					case live, boot == "first-boot" && at < startWindow:
						return protocol.AgentBusy
					}
					return protocol.AgentInterrupted
				}
				check := func(point, boot string, at time.Duration, live bool) {
					t.Helper()
					if live {
						if err := writeJSONFile(s.statePath(pidFile), me); err != nil {
							t.Fatal(err)
						}
						defer func() { _ = os.Remove(s.statePath(pidFile)) }()
					}
					st := CollectStatus(ctx, &fakeRunner{}, s, sess, loginNow.Add(at))
					if w := want(live, boot, at); st.Agent.State != w {
						t.Fatalf("%s: status %+v, want %s", point, st.Agent, w)
					}
					if st.Agent.ConversationID != first.ConversationID || strings.Contains(st.Agent.Error, "D-58") {
						t.Fatalf("%s: status %+v", point, st.Agent)
					}
					if mode == protocol.ModeTask && (st.Agent.Task != nil) != (tc.started == 1) {
						t.Fatalf("%s: the task's own result is not in the status: %+v", point, st.Agent.Task)
					}
				}
				// The boot that started the launch, as its heartbeat reads it.
				failed := bootRecord{BootID: "first-boot", Boot: protocol.BootReady}
				if why != "" {
					failed.Boot, failed.AgentError = protocol.BootFailed, why
				}
				if err := writeJSONFile(s.statePath(bootFile), failed); err != nil {
					t.Fatal(err)
				}
				check("the launch's boot, inside the start window", "first-boot", 0, false)
				check("the launch's boot, a live process", "first-boot", 2*time.Minute, true)
				check("the launch's boot, after the start window", "first-boot", startWindow, false)

				// Later boots, of the same pod or a recreated one, start nothing.
				for i, p := range []struct {
					at   time.Duration
					live bool
				}{{30 * time.Second, false}, {40 * time.Second, true}, {2 * time.Minute, true}, {time.Hour, false}} {
					boot := fmt.Sprintf("boot-%d", i+2)
					runner.calls = nil
					runAgentNow = func() time.Time { return loginNow.Add(p.at) }
					d2 := quietDaemon(s, sess, runner, loginNow.Add(p.at))
					if step, why := d2.startAgent(ctx, ws, Step{Name: "repo", State: StepOK}, boot); why != "" || step.State == StepFail || len(runner.calls) != 0 {
						t.Fatalf("%s: a later boot started again: %+v %q %+v", boot, step, why, runner.calls)
					}
					if err := writeJSONFile(s.statePath(bootFile), bootRecord{BootID: boot, Boot: protocol.BootReady}); err != nil {
						t.Fatal(err)
					}
					check(fmt.Sprintf("%s at %s, live=%v", boot, p.at, p.live), boot, p.at, p.live)
				}
				data, err := os.ReadFile(ran)
				if err != nil && !errors.Is(err, os.ErrNotExist) {
					t.Fatal(err)
				}
				if got := strings.Count(string(data), "ran\n"); got != tc.started {
					t.Fatalf("the provider ran %d times, want %d", got, tc.started)
				}
			})
		}
	}
}

// The status table of R2-F1a for each record state, read straight from the
// files agentd leaves: a record of another launch, a truncated one, a
// directory in its place and no record at all (a launch record written by
// round 2's code, or a boot that died between the two writes) all count as
// "asked". "nothing-started" and a matching fence refusal are failed,
// whatever the pid file says.
func TestSummonedStartHandoffStatusTable(t *testing.T) {
	for _, mode := range []string{protocol.ModeTask, protocol.ModeRemote} {
		for _, rec := range []string{"asked", "ambiguous", "nothing-started", "started", "fence-refused", "other-launch", "truncated", "directory", "none"} {
			t.Run(mode+"/"+rec, func(t *testing.T) {
				ctx := context.Background()
				s, sess, ws := summonedFixture(t, mode)
				first, err := BuildLaunch(s, sess, ws, "first-boot", loginNow)
				if err != nil {
					t.Fatal(err)
				}
				if err := writeJSONFile(s.statePath(launchFile), first); err != nil {
					t.Fatal(err)
				}
				path := s.statePath(handoffRecordName)
				put := func(state, conversation string) {
					t.Helper()
					if err := writeJSONFile(path, map[string]any{"state": state, "at": loginNow, "conversationId": conversation, "bootId": first.BootID}); err != nil {
						t.Fatal(err)
					}
				}
				switch rec {
				case "asked", "ambiguous", "nothing-started", "started":
					put(rec, first.ConversationID)
				case "fence-refused":
					put("ambiguous", first.ConversationID)
					if err := writeJSONFile(s.statePath(startRefusedFile), startRefused{Reason: protocol.StartDeadlinePassed, At: loginNow, ConversationID: first.ConversationID, BootID: first.BootID}); err != nil {
						t.Fatal(err)
					}
				case "other-launch":
					put("nothing-started", "another-conversation")
				case "truncated":
					if err := os.WriteFile(path, []byte(`{"state":"nothing-st`), 0o600); err != nil {
						t.Fatal(err)
					}
				case "directory":
					if err := os.Mkdir(path, 0o700); err != nil {
						t.Fatal(err)
					}
				}
				me := agentPid{Pid: os.Getpid(), Start: procStartTime(os.Getpid())}
				for _, p := range []struct {
					boot string
					at   time.Duration
					live bool
				}{
					{"first-boot", 0, false}, {"first-boot", 0, true}, {"first-boot", startWindow, false}, {"first-boot", time.Hour, true},
					{"later-boot", 0, false}, {"later-boot", 30 * time.Second, true}, {"later-boot", time.Hour, false},
				} {
					_ = os.Remove(s.statePath(pidFile))
					if p.live {
						if err := writeJSONFile(s.statePath(pidFile), me); err != nil {
							t.Fatal(err)
						}
					}
					// The boot's own error never decides a summoned launch's state.
					if err := writeJSONFile(s.statePath(bootFile), bootRecord{BootID: p.boot, Boot: protocol.BootFailed, AgentError: "tmux: exit status 1"}); err != nil {
						t.Fatal(err)
					}
					var want string
					switch {
					case rec == "nothing-started" || rec == "fence-refused":
						want = protocol.AgentFailed
					case p.live:
						want = protocol.AgentBusy
					case p.boot == "first-boot" && p.at < startWindow:
						// "started" as today; "asked" and "ambiguous" alike.
						// A later boot never counts the start window.
						want = protocol.AgentBusy
					default:
						want = protocol.AgentInterrupted
					}
					st := CollectStatus(ctx, &fakeRunner{}, s, sess, loginNow.Add(p.at))
					if st.Agent.State != want {
						t.Fatalf("%s at %s live=%v: status %+v, want %s", p.boot, p.at, p.live, st.Agent, want)
					}
					if strings.Contains(st.Agent.Error, "D-58") {
						t.Fatalf("a summoned status promises a resume: %q", st.Agent.Error)
					}
				}
			})
		}
	}
}

// R2-F1b: only a lookup failure and a path error of fork/exec prove the tmux
// client never ran. The real runner's own errors for a missing binary and for
// files it cannot execute are those; any other error, another path error
// included, may have started. No real program runs here: each command fails
// before it executes, and its environment carries no cluster variable.
func TestTmuxNeverRanOnlyForLookupAndExecFailures(t *testing.T) {
	dir := t.TempDir()
	notExec := filepath.Join(dir, "tmux-not-executable")
	if err := os.WriteFile(notExec, []byte("#!/bin/sh\nexit 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	noFormat := filepath.Join(dir, "tmux-no-format")
	if err := os.WriteFile(noFormat, []byte("not a program\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	run := ExecRunner{BaseEnv: []string{"PATH=/nonexistent"}}
	runErr := func(name, cwd string) error {
		t.Helper()
		_, err := run.Run(context.Background(), Cmd{Name: name, Dir: cwd, Args: []string{"new-session", "-d"}})
		if err == nil {
			t.Fatalf("%s ran", name)
		}
		return err
	}
	for _, tc := range []struct {
		name     string
		err      error
		neverRan bool
	}{
		{"lookup failure", runErr("agentd-test-no-such-tmux-780", ""), true},
		{"not executable", runErr(notExec, ""), true},
		{"no executable format", runErr(noFormat, ""), true},
		// The runner sets SysProcAttr, so a missing directory fails in the
		// child before its exec, and Go reports it as fork/exec too.
		{"a missing directory", runErr(noFormat, filepath.Join(dir, "missing")), true},
		{"a wrapped read path error", fmt.Errorf("tmux: %w", &fs.PathError{Op: "read", Path: "|0", Err: os.ErrClosed}), false},
		{"a path error of open", &fs.PathError{Op: "open", Path: "/dev/null", Err: os.ErrPermission}, false},
		{"a non-zero exit", &CmdError{Name: "tmux", Sub: "new-session", ExitCode: 1}, false},
		{"a time limit", fmt.Errorf("tmux: %w", context.DeadlineExceeded), false},
		{"a lost answer", errors.New("tmux: signal: killed"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tmuxNeverRan(tc.err); got != tc.neverRan {
				t.Fatalf("tmuxNeverRan(%v) = %v, want %v", tc.err, got, tc.neverRan)
			}
		})
	}
}

// R2-F1b end to end: the daemon's first launch of a summoned session through
// the real runner, with a tmux it cannot find or cannot execute, records
// "nothing-started", and the status is failed on that boot and every later
// one, which starts nothing.
func TestSummonedRealTmuxLookupAndExecFailuresFail(t *testing.T) {
	for _, mode := range []string{protocol.ModeTask, protocol.ModeRemote} {
		for _, bin := range []string{"missing", "not-executable"} {
			t.Run(mode+"/"+bin, func(t *testing.T) {
				ctx := context.Background()
				s, sess, ws := summonedFixture(t, mode)
				s.TmuxBin = "agentd-test-no-such-tmux-780"
				if bin == "not-executable" {
					s.TmuxBin = filepath.Join(t.TempDir(), "tmux")
					if err := os.WriteFile(s.TmuxBin, []byte("#!/bin/sh\nexit 0\n"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				run := ExecRunner{BaseEnv: []string{"PATH=/nonexistent"}}
				runner := &fakeRunner{handle: func(c Cmd) (Result, error) {
					if c.Name == s.TmuxBin {
						return run.Run(ctx, c)
					}
					return Result{}, nil
				}}
				d := quietDaemon(s, sess, runner, loginNow)
				step, why := d.startAgent(ctx, ws, Step{Name: "repo", State: StepOK}, "first-boot")
				if why == "" || step.State != StepFail {
					t.Fatalf("the failed tmux start was not reported: %+v %q", step, why)
				}
				var rec handoffOnDisk
				if err := readJSONFile(s.statePath(handoffRecordName), &rec); err != nil || rec.State != "nothing-started" {
					t.Fatalf("handoff record %+v %v, want nothing-started", rec, err)
				}
				for i, at := range []time.Duration{0, 30 * time.Second, time.Hour} {
					boot := "first-boot"
					if i > 0 {
						boot = fmt.Sprintf("boot-%d", i+1)
						runner.calls = nil
						d2 := quietDaemon(s, sess, runner, loginNow.Add(at))
						if step, why := d2.startAgent(ctx, ws, Step{Name: "repo", State: StepOK}, boot); why != "" || step.State == StepFail || len(runner.calls) != 0 {
							t.Fatalf("%s: a later boot started again: %+v %q %+v", boot, step, why, runner.calls)
						}
					}
					if err := writeJSONFile(s.statePath(bootFile), bootRecord{BootID: boot, Boot: protocol.BootReady}); err != nil {
						t.Fatal(err)
					}
					if st := CollectStatus(ctx, &fakeRunner{}, s, sess, loginNow.Add(at)); st.Agent.State != protocol.AgentFailed {
						t.Fatalf("%s: status %+v, want failed", boot, st.Agent)
					}
				}
			})
		}
	}
}
