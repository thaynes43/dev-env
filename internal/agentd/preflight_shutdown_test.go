package agentd

import (
	"context"
	"testing"
	"time"

	clocktesting "k8s.io/utils/clock/testing"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func TestPreflightShutdownWaitsForTaskResult(t *testing.T) {
	for _, result := range []string{"during-wait", "never-written", "task-already-exited"} {
		t.Run(result, func(t *testing.T) {
			r := newLifecycleRig(t, "slow-result")
			first := r.start("first")
			task := r.waitEvent("task")
			r.waitOwnedEvent("task-published")
			if result == "task-already-exited" {
				r.release("task")
				r.waitEvent("before-result")
			}
			fc := clocktesting.NewFakeClock(loginNow.Add(3 * time.Minute))
			start := fc.Now()
			oldNow, oldLock, oldWait, oldSleep := preflightStopNow, preflightStopLock, preflightStopWait, preflightResultSleep
			t.Cleanup(func() {
				preflightStopNow, preflightStopLock, preflightStopWait, preflightResultSleep = oldNow, oldLock, oldWait, oldSleep
			})
			preflightStopNow = fc.Now
			preflightStopLock = func(path string, bound time.Duration) (func(), error) {
				unlock, err := oldLock(path, bound)
				fc.Step(bound) // consume the full lock budget without a sleep
				return unlock, err
			}
			waits := 0
			preflightStopWait = func(pgid int, bound time.Duration, except ...agentPid) bool {
				if pgid != task.PID {
					t.Fatalf("stop waited for group %d, want only task %d", pgid, task.PID)
				}
				if !oldWait(pgid, lifecycleWait, except...) {
					t.Fatal("stand-in task did not leave its group")
				}
				waits++
				fc.Step(bound)
				if result == "task-already-exited" {
					return true // absence before any signal, WasRunning is false
				}
				if waits == 1 {
					r.waitEvent("before-result")
					// Model an uncertain first observation, consuming the full
					// grace and kill budgets on the same fake stop clock.
					return false
				}
				return true
			}
			var slept time.Duration
			preflightResultSleep = func(bound time.Duration) {
				slept += bound
				fc.Step(bound)
				if result != "never-written" {
					r.release("result")
					r.wait(first) // the result becomes durable inside the wait
				}
			}
			sess := protocol.Session{Name: r.l.Session, Agent: protocol.AgentClaude, Mode: protocol.ModeTask, Caller: summonedCaller}
			d := quietDaemon(r.s, sess, &fakeRunner{}, fc.Now())
			d.Now, d.StopGrace = fc.Now, 30*time.Second
			var last protocol.Status
			beats := 0
			d.Send = func(_ context.Context, st protocol.Status) error { last = st; beats++; return nil }
			d.shutdown()
			if beats != 1 {
				t.Fatalf("shutdown sent %d final heartbeats, want one", beats)
			}
			if result == "never-written" {
				if slept != 2*time.Second || fc.Since(start) != 42*time.Second {
					t.Errorf("result wait=%s, stop plus wait=%s; want 2s and 42s", slept, fc.Since(start))
				}
				if last.Agent.Task != nil || last.Agent.State != protocol.AgentInterrupted {
					t.Errorf("bounded wait invented a result: agent=%+v", last.Agent)
				}
			} else {
				if slept != 100*time.Millisecond {
					t.Errorf("result wait=%s, want one 100ms step before the gate releases", slept)
				}
				if last.Agent.Task == nil || last.Agent.Task.FinishedAt.IsZero() || last.Agent.ConversationID != r.l.ConversationID || last.Agent.State != protocol.AgentExited {
					t.Errorf("final heartbeat lost the result released inside the wait: agent=%+v", last.Agent)
				}
			}
		})
	}
}

func TestPreflightShutdownSkipsResultWaitWithoutTask(t *testing.T) {
	for _, phase := range []string{"unclaimed", "checking", "another-boot"} {
		t.Run(phase, func(t *testing.T) {
			r := newLifecycleRig(t, "before-primary")
			if phase != "unclaimed" {
				r.start("first")
				r.waitEvent("hook")
			}
			if phase == "another-boot" {
				if err := writeJSONFile(r.s.statePath(bootFile), bootRecord{BootID: "later-boot", Boot: protocol.BootReady}); err != nil {
					t.Fatal(err)
				}
			}
			oldSleep := preflightResultSleep
			t.Cleanup(func() { preflightResultSleep = oldSleep })
			preflightResultSleep = func(time.Duration) { t.Error("shutdown waited for a task result without finding a started task") }
			sess := protocol.Session{Name: r.l.Session, Agent: protocol.AgentClaude, Mode: protocol.ModeTask, Caller: summonedCaller}
			d := quietDaemon(r.s, sess, &fakeRunner{}, loginNow.Add(3*time.Minute))
			beats := 0
			d.Send = func(context.Context, protocol.Status) error { beats++; return nil }
			d.shutdown()
			if beats != 1 {
				t.Errorf("shutdown sent %d final heartbeats, want one", beats)
			}
		})
	}
}
