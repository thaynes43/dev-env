package agentd

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"k8s.io/utils/clock"
	clocktesting "k8s.io/utils/clock/testing"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// These proofs exercise the actual command runner; only its clock and its
// observation boundary are replaced. No provider binary is used.
func reviewCLI(t *testing.T, l *Launch, body string) {
	t.Helper()
	oldTurn := preflightTurn
	t.Cleanup(func() { preflightTurn = oldTurn })
	preflightTurn = runPreflightTurn
	scrubClusterEnv(t)
	if err := os.Unsetenv("KUBECONFIG"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(l.Dir, "review-provider")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	l.Argv[0] = path
}

// R3: preparation may consume the remaining absolute start window, for
// either model. The pause is after argv, environment and stdio preparation.
func TestPreflightPreparedCommandRechecksDeadline(t *testing.T) {
	if isolatePreflightTest(t) {
		return
	}
	for _, phase := range []string{"primary", "fallback"} {
		t.Run(phase, func(t *testing.T) {
			s, sess, l, task := summonedTaskLaunch(t)
			probes := filepath.Join(l.Dir, "probes")
			reviewCLI(t, &l, `case " $* " in
*" --no-session-persistence "*)
  echo "$2" >> '`+probes+`'
  echo '{"type":"assistant","error":"model_not_found","is_api_error_message":true}'
  exit 0;;
esac
echo task >> '`+task+`'
`)
			writeLaunch(t, s, l)
			now := loginNow
			oldNow, oldHook := runAgentNow, beforePreflightStart
			t.Cleanup(func() { runAgentNow, beforePreflightStart = oldNow, oldHook })
			runAgentNow = func() time.Time { return now }
			beforePreflightStart = func(model string) {
				if (phase == "primary" && model == sess.Model) || (phase == "fallback" && model == preflightFallback) {
					now = loginNow.Add(16 * time.Minute)
				}
			}
			if code := RunAgent(s.statePath(launchFile), &syncBuffer{}, nil, 0); code == 0 {
				t.Error("a prepared command crossed its start deadline")
			}
			got, _ := os.ReadFile(probes)
			want := ""
			if phase == "fallback" {
				want = sess.Model + "\n"
			}
			if string(got) != want {
				t.Errorf("probe starts %q, want %q: a prepared probe started past the deadline", got, want)
			}
			assertReviewStartRefused(t, s, l)
			if exists(task) {
				t.Error("task started after the prepared probe crossed the deadline")
			}
		})
	}
}

func assertReviewStartRefused(t *testing.T, s Settings, l Launch) {
	t.Helper()
	var refused startRefused
	if err := readJSONFile(s.statePath(startRefusedFile), &refused); err != nil || refused.Reason != protocol.StartDeadlinePassed {
		t.Errorf("start refusal %+v, err %v; want start deadline passed", refused, err)
	}
	if rec := preflightStartRecord(s, l); rec != nil {
		t.Errorf("deadline produced typed model failure %+v", rec)
	}
}

// The earlier limit was 90s, but preparation leaves only one second. The
// fake timer expires without a sleep and must retain the fence's diagnosis.
func TestPreflightPreparedCommandBoundsTimer(t *testing.T) {
	if isolatePreflightTest(t) {
		return
	}
	s, _, l, task := summonedTaskLaunch(t)
	fifo := filepath.Join(l.Dir, "wait")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	reviewCLI(t, &l, "read value < '"+fifo+"'\n")
	writeLaunch(t, s, l)
	fc := clocktesting.NewFakeClock(loginNow)
	deadline := loginNow.Add(15 * time.Minute)
	oldNow, oldHook, oldTimer := runAgentNow, beforePreflightStart, preflightNewTimer
	t.Cleanup(func() { runAgentNow, beforePreflightStart, preflightNewTimer = oldNow, oldHook, oldTimer })
	runAgentNow = fc.Now
	beforePreflightStart = func(string) { fc.SetTime(deadline.Add(-time.Second)) }
	timers := 0
	preflightNewTimer = func(d time.Duration) clock.Timer {
		timers++
		if d != time.Second {
			t.Errorf("prepared probe timer = %s, want 1s remaining to absolute deadline", d)
		}
		timer := fc.NewTimer(d)
		fc.Step(d)
		return timer
	}
	if code := RunAgent(s.statePath(launchFile), &syncBuffer{}, nil, 0); code == 0 {
		t.Error("deadline-cut probe authorized the task")
	}
	if timers != 1 {
		t.Errorf("created %d timers, want exactly the first turn", timers)
	}
	assertReviewStartRefused(t, s, l)
	if exists(task) {
		t.Error("task started after the deadline")
	}
}

// R2: a leader can exit while a descendant has closed all output pipes.
// Waiting for the leader is not the observation that permits another start.
func TestPreflightWaitsForDescendantAbsence(t *testing.T) {
	if isolatePreflightTest(t) {
		return
	}
	s, sess, l, task := summonedTaskLaunch(t)
	fifo := filepath.Join(l.Dir, "descendant-wait")
	childFile := filepath.Join(l.Dir, "descendant-pid")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	reviewCLI(t, &l, `case " $* " in
*" --no-session-persistence "*)
  sh -c 'exec 0</dev/null 1>/dev/null 2>/dev/null; read value < "`+fifo+`"' &
  echo "$!" > '`+childFile+`'
  cat <<'ANSWER'
`+preflightAccepted+`
ANSWER
  exit 0;;
esac
echo task >> '`+task+`'
`)
	writeLaunch(t, s, l)
	oldObserve, oldStart, oldSignal := preflightGroupAbsent, beforeTaskStart, preflightSignal
	t.Cleanup(func() { preflightGroupAbsent, beforeTaskStart, preflightSignal = oldObserve, oldStart, oldSignal })
	killed := map[int]bool{}
	preflightSignal = func(pid int, sig syscall.Signal) error {
		err := oldSignal(pid, sig)
		if sig == syscall.SIGKILL && err == nil {
			killed[pid] = true
		}
		return err
	}
	t.Cleanup(func() {
		raw, _ := os.ReadFile(childFile)
		pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
		if group, err := syscall.Getpgid(pid); pid > 0 && err == nil && group == os.Getpid() {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	observed := false
	preflightGroupAbsent = func(pgid int, bound time.Duration, except ...agentPid) bool {
		raw, err := os.ReadFile(childFile)
		pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
		if err != nil || pid <= 0 || !killed[pid] {
			t.Errorf("descendant PID %d was not sent SIGKILL before the absence observation: %v", pid, err)
		}
		if pgid != os.Getpid() || bound != time.Second || len(except) != 1 {
			t.Errorf("wrong bounded launcher observation: pgid=%d bound=%s except=%+v", pgid, bound, except)
		}
		// The signal is real; absence is injected. PID 1's reap latency is
		// not a property of this test, nor required to fit in one second.
		observed = true
		return true
	}
	beforeTaskStart = func() {
		if !observed {
			t.Error("task start reached before bounded descendant absence was proved")
		}
	}
	r := runSummonedTask(t, s, sess, l, task, loginNow, nil)
	if !observed {
		t.Errorf("leader exit did not establish group absence (exit %d)", r.code)
	}
	if !exists(task) {
		t.Errorf("task did not start after descendant absence: %s", r.pane)
	}
}

// Uncertainty in the independent group observation must gate both a
// fallback after refusal and a task after an otherwise accepted answer.
func TestPreflightUncertainGroupStartsNothing(t *testing.T) {
	if isolatePreflightTest(t) {
		return
	}
	for _, response := range []string{"answer", "refusal"} {
		t.Run(response, func(t *testing.T) {
			s, sess, l, task := summonedTaskLaunch(t)
			probes := filepath.Join(l.Dir, "probes")
			output := preflightAccepted
			if response == "refusal" {
				output = `{"type":"assistant","error":"model_not_found","is_api_error_message":true}`
			}
			reviewCLI(t, &l, `case " $* " in
*" --no-session-persistence "*)
  echo "$2" >> '`+probes+`'
  cat <<'ANSWER'
`+output+`
ANSWER
  exit 0;;
esac
echo task >> '`+task+`'
`)
			writeLaunch(t, s, l)
			oldObserve := preflightGroupAbsent
			t.Cleanup(func() { preflightGroupAbsent = oldObserve })
			calls := 0
			preflightGroupAbsent = func(int, time.Duration, ...agentPid) bool { calls++; return false }
			r := runSummonedTask(t, s, sess, l, task, loginNow, nil)
			if r.code == 0 || exists(task) {
				t.Error("task started despite an uncertain group observation")
			}
			got, _ := os.ReadFile(probes)
			if string(got) != sess.Model+"\n" {
				t.Errorf("probe starts %q, want only primary %q; fallback started without absence", strings.TrimSpace(string(got)), sess.Model)
			}
			if calls != 1 {
				t.Errorf("group observed %d times, want 1", calls)
			}
			var refused startRefused
			if err := readJSONFile(s.statePath(startRefusedFile), &refused); err != nil || !strings.Contains(refused.Reason, "group") {
				t.Errorf("uncertain group produced no start refusal: %+v, %v", refused, err)
			}
			if rec := preflightStartRecord(s, l); rec != nil {
				t.Errorf("uncertain group was misreported as a typed model failure: %+v", rec)
			}
		})
	}
}

func TestPreflightTurnLockRefusalIsNotSignal(t *testing.T) {
	if isolatePreflightTest(t) {
		return
	}
	s, sess, l, task := summonedTaskLaunch(t)
	reviewCLI(t, &l, "exit 0\n")
	writeLaunch(t, s, l)
	oldHook, oldWait := beforePreflightStart, preflightLockWait
	t.Cleanup(func() { beforePreflightStart, preflightLockWait = oldHook, oldWait })
	beforePreflightStart = func(string) {
		unlock, err := lockFileWait(s.statePath(startSpawnLockFile), 0)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(unlock)
		preflightLockWait = 0
	}
	r := runSummonedTask(t, s, sess, l, task, loginNow, nil)
	r.mustStartNothing(t)
	if strings.Contains(r.pane, "stopped by a signal") || !strings.Contains(r.pane, "start lock") {
		t.Errorf("lock refusal was diagnosed as a signal: %s", r.pane)
	}
	var refused startRefused
	if err := readJSONFile(s.statePath(startRefusedFile), &refused); err != nil || !strings.Contains(refused.Reason, "start lock") {
		t.Errorf("lock failure produced no start refusal: %+v, %v", refused, err)
	}
}
