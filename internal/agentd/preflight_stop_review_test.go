package agentd

// OP791 round 3: live launchers, injected I/O failures and a fake stop clock.
// No provider or cluster is reached, and no scheduling delay is a proof.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	clocktesting "k8s.io/utils/clock/testing"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func TestPreflightStopWithGraceFencesLiveLauncher(t *testing.T) {
	for _, point := range []string{"before-task", "grace-primary", "grace-fallback"} {
		t.Run(point, func(t *testing.T) {
			r := newLifecycleRig(t, point)
			first := r.start("first")
			if point == "before-task" {
				r.waitEvent("hook")
			} else {
				for {
					e := r.waitEvent("probe")
					if point == "grace-primary" || e.Model == preflightFallback {
						break
					}
				}
			}
			oldSignal := preflightSignal
			t.Cleanup(func() { preflightSignal = oldSignal })
			terms, kills := 0, 0
			preflightSignal = func(pid int, sig syscall.Signal) error {
				if sig == syscall.SIGKILL {
					kills++
				}
				err := oldSignal(pid, sig)
				if pid == first.cmd.Process.Pid && sig == syscall.SIGTERM {
					terms++
					if point == "before-task" {
						// Stop has signalled the still-live launcher. Release it
						// inside the grace, so only the marker can refuse Start.
						r.release("hook")
					}
				}
				return err
			}
			st := stopAgent(r.s, lifecycleWait)
			if st.Killed || st.Running || terms != 1 || kills != 0 {
				t.Errorf("graceful stop %+v, TERM=%d KILL=%d; want launcher TERM only and confirmed absence", st, terms, kills)
			}
			r.wait(first)
			r.assertNoTask()
			r.assertLauncherGroupAbsent(first)
		})
	}
}

func TestPreflightStopBeforeLauncherFencesEveryStart(t *testing.T) {
	r := newLifecycleRig(t, "success")
	st := stopAgent(r.s, lifecycleWait)
	if st.WasRunning || st.Killed || st.Running {
		t.Fatalf("stop before launcher: %+v", st)
	}
	r.wait(r.start("first"))
	if got := r.invocations(); len(got) != 0 {
		t.Errorf("a launcher started after stop ran a probe or task: %s", got)
	}
}

func TestPreflightStopUnconfirmedFenceSignalsNothing(t *testing.T) {
	for _, fault := range []string{"lock-held", "marker-write"} {
		t.Run(fault, func(t *testing.T) {
			r := newLifecycleRig(t, "before-primary")
			first := r.start("first")
			r.waitEvent("hook")
			oldSignal, oldWait := preflightSignal, preflightLockWait
			t.Cleanup(func() { preflightSignal, preflightLockWait = oldSignal, oldWait })
			signals := 0
			preflightSignal = func(int, syscall.Signal) error { signals++; return nil }
			if fault == "lock-held" {
				unlock, err := lockFileWait(r.s.statePath(startSpawnLockFile), 0)
				if err != nil {
					t.Fatal(err)
				}
				defer unlock()
				preflightLockWait = 0 // a real held flock; no timer race or sleep
			} else if err := os.Mkdir(r.s.statePath(startStoppedFile), 0o700); err != nil {
				t.Fatal(err) // rename of the durable marker must fail
			}
			st := stopAgent(r.s, 0)
			if !st.Running || st.Killed || signals != 0 || !lifecycleLive(first.cmd.Process.Pid, first.start) {
				t.Errorf("unconfirmed %s: stop %+v signals=%d; want running and nothing signalled", fault, st, signals)
			}
			if fault == "lock-held" {
				var marker startRefused
				if err := readJSONFile(r.s.statePath(startStoppedFile), &marker); err != nil || marker.BootID != r.l.BootID {
					t.Errorf("stop did not persist its marker before waiting for the lock: %+v, %v", marker, err)
				}
			}
		})
	}
}

func lifecycleStatus(r *lifecycleRig) protocol.Status {
	sess := protocol.Session{Name: r.l.Session, Agent: protocol.AgentClaude, Mode: protocol.ModeTask, Model: r.l.Argv[2], Caller: summonedCaller}
	return CollectStatus(context.Background(), &fakeRunner{}, r.s, sess, loginNow.Add(3*time.Minute))
}

func TestPreflightStopIgnoresAnotherBoot(t *testing.T) {
	for _, state := range []string{preflightChecking, preflightStarted} {
		t.Run(state, func(t *testing.T) {
			r := newLifecycleRig(t, "before-primary")
			first := r.start("first")
			r.waitEvent("hook")
			rec, ok := readPreflight(r.s, r.l)
			if !ok {
				t.Fatal("no live launch claim")
			}
			rec.State, rec.TaskPending = state, state == preflightChecking
			for path, value := range map[string]any{
				startPreflightFile: rec,
				pidFile:            agentPid{Pid: first.cmd.Process.Pid, Start: first.start},
				bootFile:           bootRecord{BootID: "another-boot", Boot: protocol.BootReady},
			} {
				if err := writeJSONFile(r.s.statePath(path), value); err != nil {
					t.Fatal(err)
				}
			}
			oldSignal, oldWait := preflightSignal, preflightStopWait
			t.Cleanup(func() { preflightSignal, preflightStopWait = oldSignal, oldWait })
			signals := 0
			preflightSignal = func(int, syscall.Signal) error { signals++; return nil }
			preflightStopWait = func(int, time.Duration, ...agentPid) bool { return false }
			st := stopAgent(r.s, 0)
			if st.WasRunning || st.Running || st.Killed || signals != 0 {
				t.Errorf("another boot's PID was treated as owned: stop %+v signals=%d", st, signals)
			}
			if state := lifecycleStatus(r).Agent.State; state == protocol.AgentBusy {
				t.Error("another boot's pre-flight or task PID made status busy")
			}
		})
	}
}

func TestPreflightFailedSettleWriteDoesNotStrandFinishedTask(t *testing.T) {
	for _, point := range []string{"settle-once", "settle-persistent"} {
		t.Run(point, func(t *testing.T) {
			r := newLifecycleRig(t, point)
			r.wait(r.start("first"))
			if !strings.Contains(string(r.invocations()), "settle-write-failed") || !strings.Contains(string(r.invocations()), "task-spawn") {
				t.Fatal("the proof did not run a task with the second ownership write failing")
			}
			var result taskResult
			if err := readJSONFile(r.s.statePath(resultFile), &result); err != nil || result.ConversationID != r.l.ConversationID {
				t.Fatalf("task did not finish with its result: %+v %v", result, err)
			}
			if state := lifecycleStatus(r).Agent.State; state != protocol.AgentExited {
				t.Errorf("finished task with failed settle write has status %q, want exited", state)
			}
			if st := stopAgent(r.s, 0); st.Running {
				t.Errorf("finished task with failed settle write still reports running: %+v", st)
			}
			log, err := os.ReadFile(filepath.Join(r.root, "first.out"))
			if err != nil || !strings.Contains(string(log), "pre-flight's started record could not be written") {
				t.Errorf("failed started write was not reported: %v, %s", err, log)
			}
		})
	}
}

func TestPreflightStopLeavesStartedTaskResult(t *testing.T) {
	for _, mode := range []string{"summoned", "ordinary"} {
		t.Run(mode, func(t *testing.T) {
			r := newLifecycleRig(t, "slow-result")
			if mode == "ordinary" {
				r.l.Preflight, r.l.StartDeadline = nil, ""
				writeLaunch(t, r.s, r.l)
			}
			first := r.start("first")
			task := r.waitEvent("task")
			r.waitOwnedEvent("task-published")
			fc := clocktesting.NewFakeClock(loginNow)
			oldNow, oldWait := preflightStopNow, preflightStopWait
			t.Cleanup(func() { preflightStopNow, preflightStopWait = oldNow, oldWait })
			preflightStopNow = fc.Now
			grace := time.Second
			preflightStopWait = func(pgid int, bound time.Duration, except ...agentPid) bool {
				if pgid == task.PID {
					gone := oldWait(pgid, lifecycleWait, except...)
					r.waitEvent("before-result")
					fc.Step(grace + time.Second)
					return gone
				}
				fc.Step(bound)
				return false
			}
			// The ordinary path uses its existing kernel wait. In either
			// path, result work stays held until after stop returns.
			st := stopAgent(r.s, grace)
			if mode == "ordinary" {
				r.waitEvent("before-result")
				fc.Step(grace + time.Second)
				if exists(r.s.statePath(startStoppedFile)) || exists(r.s.statePath(startPreflightFile)) {
					t.Error("ordinary task stop acquired summoned pre-flight state")
				}
			}
			if st.Running || !lifecycleLive(first.cmd.Process.Pid, first.start) {
				t.Errorf("stop interrupted the started task's result writer: stop %+v, launcher alive=%t", st, lifecycleLive(first.cmd.Process.Pid, first.start))
			}
			if fc.Now().Sub(loginNow) <= grace {
				t.Fatal("the proof did not hold result work beyond the grace")
			}
			r.release("result")
			r.wait(first)
			var result taskResult
			if err := readJSONFile(r.s.statePath(resultFile), &result); err != nil || result.ConversationID != r.l.ConversationID || result.FinishedAt.IsZero() {
				t.Errorf("stop lost the started task's result: %+v %v", result, err)
			}
		})
	}
}

// The synthetic task below owns a separate group but never runs a provider.
func reviewStopTask(t *testing.T, r *lifecycleRig) agentPid {
	t.Helper()
	cmd := exec.Command("sh", "-c", `read value < "$1"`, "stop-proof", filepath.Join(r.root, "task.fifo"))
	cmd.Env = lifecycleEnv(os.Environ())
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := agentPid{Pid: cmd.Process.Pid, Start: procStartTime(cmd.Process.Pid)}
	t.Cleanup(func() { _ = syscall.Kill(-p.Pid, syscall.SIGKILL); _ = cmd.Wait() })
	if err := writeJSONFile(r.s.statePath(pidFile), p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPreflightStopUsesOneDeadline(t *testing.T) {
	for _, phase := range []string{"preflight", "published-task"} {
		for _, grace := range []time.Duration{30 * time.Second, 24 * time.Hour} {
			t.Run(phase+"/"+grace.String(), func(t *testing.T) {
				r := newLifecycleRig(t, "before-primary")
				first := r.start("first")
				r.waitEvent("hook")
				if phase == "published-task" {
					reviewStopTask(t, r)
				}
				fc := clocktesting.NewFakeClock(loginNow)
				oldNow, oldLock, oldWait, oldSignal := preflightStopNow, preflightStopLock, preflightStopWait, preflightSignal
				t.Cleanup(func() {
					preflightStopNow, preflightStopLock, preflightStopWait, preflightSignal = oldNow, oldLock, oldWait, oldSignal
				})
				preflightStopNow = fc.Now
				preflightStopLock = func(_ string, bound time.Duration) (func(), error) { fc.Step(bound); return func() {}, nil }
				preflightStopWait = func(_ int, bound time.Duration, _ ...agentPid) bool { fc.Step(bound); return false }
				launcherSignals := 0
				preflightSignal = func(pid int, _ syscall.Signal) error {
					if pid == first.cmd.Process.Pid || pid == -first.cmd.Process.Pid {
						launcherSignals++
					}
					return nil
				}
				st := stopAgent(r.s, grace)
				if elapsed := fc.Now().Sub(loginNow); elapsed > 40*time.Second {
					t.Errorf("stop waited %s, want at most 40s including lock, grace and kill observation", elapsed)
				}
				if phase == "published-task" && launcherSignals != 0 {
					t.Errorf("published task stop signalled its launcher %d times", launcherSignals)
				}
				if !st.Running {
					t.Error("unknown group absence was reported stopped at the deadline")
				}
			})
		}
	}
}
