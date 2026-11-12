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
	"io"
	"log/slog"
	"os"
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
// TUI; a launch without one spawns whatever the time.
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
