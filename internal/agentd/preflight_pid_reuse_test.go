package agentd

// OP791 round 5: a live unrelated group leader occupies a recorded PID.
// Wrong recorded start times and injected /proc reads prove reuse without
// forcing PID wraparound. The stand-ins block on stdin; no sleeps or provider.

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func pidReuseStandIn(t *testing.T, ownGroup bool) agentPid {
	t.Helper()
	cmd := exec.Command("sh", "-c", "read value")
	// A deliberately minimal environment, also passed through the common
	// scrubber: neither KUBERNETES_* nor KUBECONFIG reaches this binary.
	cmd.Env = lifecycleEnv([]string{"PATH=/usr/bin:/bin"})
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: ownGroup}
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = in.Close() })
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Wait only at cleanup, so this child cannot be reaped/reused before Kill.
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	p, group, err := readPreflightProcess(cmd.Process.Pid)
	if err != nil || p.Start == "" || (group == p.Pid) != ownGroup || !pidAlive(p) {
		t.Fatalf("stand-in identity %+v, group %d, own group %t: %v", p, group, ownGroup, err)
	}
	return p
}

func pidReuseDifferentStart(t *testing.T, p agentPid) agentPid {
	t.Helper()
	start, err := strconv.ParseUint(p.Start, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	p.Start = strconv.FormatUint(start+1, 10)
	return p
}

func pidReuseRecord(t *testing.T, r *lifecycleRig, p agentPid, state string, pending bool) startPreflight {
	t.Helper()
	rec := startPreflight{State: state, Pid: p.Pid, Start: p.Start, GroupOwned: true,
		TaskPending: pending, ConversationID: r.l.ConversationID, BootID: r.l.BootID,
		Failure: protocol.StartFailureNoAnswer}
	if err := writeJSONFile(r.s.statePath(startPreflightFile), rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestPreflightPIDReuseStop(t *testing.T) {
	for _, owner := range []string{"launcher", "started-task"} {
		t.Run(owner, func(t *testing.T) {
			r := newLifecycleRig(t, "pid-reuse")
			live := pidReuseStandIn(t, true)
			stale := pidReuseDifferentStart(t, live)
			state := preflightChecking
			if owner == "started-task" {
				state = preflightStarted
				if err := writeJSONFile(r.s.statePath(pidFile), stale); err != nil {
					t.Fatal(err)
				}
			}
			pidReuseRecord(t, r, stale, state, false)
			oldSignal := preflightSignal
			t.Cleanup(func() { preflightSignal = oldSignal })
			signals := 0
			preflightSignal = func(int, syscall.Signal) error { signals++; return nil }
			st := stopAgent(r.s, 0)
			if st.Running || st.WasRunning || st.Killed {
				t.Errorf("reused %s PID still owns a stop: %+v; want all false", owner, st)
			}
			if signals != 0 || !pidAlive(live) {
				t.Errorf("unrelated stand-in was signalled or died: signals=%d alive=%t", signals, pidAlive(live))
			}
		})
	}
}

func TestPreflightPIDReuseStatus(t *testing.T) {
	for _, state := range []string{preflightChecking, preflightNoUsableModel} {
		t.Run(state, func(t *testing.T) {
			r := newLifecycleRig(t, "pid-reuse")
			live := pidReuseStandIn(t, true)
			pidReuseRecord(t, r, pidReuseDifferentStart(t, live), state, false)
			if preflightLive(r.s, r.l) {
				t.Error("reused launcher PID still makes preflightLive true")
			}
			st := lifecycleStatus(r)
			if st.Agent.State == protocol.AgentBusy {
				t.Error("unrelated group leader still makes status busy")
			}
			if state == preflightNoUsableModel && (st.Agent.State != protocol.AgentFailed || st.Start == nil || st.Start.Failure != protocol.StartFailureNoAnswer) {
				t.Errorf("same-boot no-usable-model outcome hidden: agent=%+v start=%+v", st.Agent, st.Start)
			}
			if !pidAlive(live) {
				t.Error("status disturbed the unrelated stand-in")
			}
		})
	}
}

func TestPreflightPIDReusePendingTask(t *testing.T) {
	r := newLifecycleRig(t, "pid-reuse")
	live := pidReuseStandIn(t, true)
	stale := pidReuseDifferentStart(t, live)
	rec := pidReuseRecord(t, r, stale, preflightChecking, true)
	if err := writeJSONFile(r.s.statePath(pidFile), stale); err != nil {
		t.Fatal(err)
	}
	if pendingPreflightTask(r.s, rec) {
		t.Error("reused published task PID still makes pendingPreflightTask true")
	}
	if preflightLive(r.s, r.l) || lifecycleStatus(r).Agent.State == protocol.AgentBusy {
		t.Error("reused pending task PID still makes status busy")
	}
	if !pidAlive(live) {
		t.Error("pending-task status disturbed the unrelated stand-in")
	}
}

func TestPreflightPIDReuseKeepsPendingTaskOwnership(t *testing.T) {
	for _, published := range []bool{false, true} {
		t.Run(strconv.FormatBool(published), func(t *testing.T) {
			r := newLifecycleRig(t, "pid-reuse")
			launcher := pidReuseStandIn(t, true)
			rec := pidReuseRecord(t, r, pidReuseDifferentStart(t, launcher), preflightChecking, true)
			if published {
				if err := writeJSONFile(r.s.statePath(pidFile), pidReuseStandIn(t, true)); err != nil {
					t.Fatal(err)
				}
			}
			if !pendingPreflightTask(r.s, rec) || !preflightLive(r.s, r.l) || lifecycleStatus(r).Agent.State != protocol.AgentBusy {
				t.Error("launcher reuse hid an independently live or unpublished task")
			}
		})
	}
}

// A stat snapshot with just field 22 changed, used for malformed reads and
// the injected identity change between TERM and KILL. It never alters /proc.
func pidReuseStat(t *testing.T, p agentPid, start string) []byte {
	t.Helper()
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(p.Pid) + "/stat")
	if err != nil {
		t.Fatal(err)
	}
	i := strings.LastIndexByte(string(raw), ')')
	fields := strings.Fields(string(raw)[i+1:])
	if i < 0 || len(fields) < 20 {
		t.Fatal("stand-in has malformed stat")
	}
	fields[19] = start
	return []byte(string(raw[:i+1]) + " " + strings.Join(fields, " ") + "\n")
}

func TestPreflightPIDReuseUncertainIdentity(t *testing.T) {
	for _, fault := range []string{"same-start-other-group", "unreadable", "malformed", "malformed-start", "no-recorded-start", "malformed-recorded-start"} {
		t.Run(fault, func(t *testing.T) {
			live := pidReuseStandIn(t, fault != "same-start-other-group")
			p := live
			oldRead, oldSignal, oldWait := readPreflightStat, preflightSignal, preflightStopWait
			t.Cleanup(func() { readPreflightStat, preflightSignal, preflightStopWait = oldRead, oldSignal, oldWait })
			path := "/proc/" + strconv.Itoa(p.Pid) + "/stat"
			badStart := pidReuseStat(t, p, "not-a-start-time")
			readPreflightStat = func(name string) ([]byte, error) {
				if name == path {
					switch fault {
					case "unreadable":
						return nil, os.ErrPermission
					case "malformed":
						return []byte("malformed stat"), nil
					case "malformed-start":
						return badStart, nil
					}
				}
				return oldRead(name)
			}
			switch fault {
			case "no-recorded-start":
				p.Start = ""
			case "malformed-recorded-start":
				p.Start = "not-a-start-time"
			}
			signals := 0
			preflightSignal = func(int, syscall.Signal) error { signals++; return nil }
			preflightStopWait = func(int, time.Duration, ...agentPid) bool { return false }
			st := stopPreflightGroup(p, 0, loginNow)
			if !st.Running || st.WasRunning || st.Killed || signals != 0 {
				t.Errorf("uncertain %s identity was treated as stopped or signalled: %+v signals=%d", fault, st, signals)
			}
			if !pidAlive(live) {
				t.Error("uncertain identity disturbed the stand-in")
			}
		})
	}
}

func TestPreflightPIDReuseMissingLeaderUsesGroupCheck(t *testing.T) {
	for _, absent := range []bool{false, true} {
		t.Run(strconv.FormatBool(absent), func(t *testing.T) {
			// An absent leader can still have descendants. The wait seam
			// supplies each whole-group observation, without a reap race.
			p := agentPid{Pid: 1 << 30, Start: "1"}
			oldRead, oldSignal, oldWait := readPreflightStat, preflightSignal, preflightStopWait
			t.Cleanup(func() { readPreflightStat, preflightSignal, preflightStopWait = oldRead, oldSignal, oldWait })
			readPreflightStat = func(string) ([]byte, error) { return nil, os.ErrNotExist }
			waits, kills := 0, 0
			preflightStopWait = func(int, time.Duration, ...agentPid) bool { waits++; return absent }
			preflightSignal = func(pid int, sig syscall.Signal) error {
				if pid != -p.Pid || sig != syscall.SIGKILL {
					t.Errorf("unexpected missing-leader signal: %d %v", pid, sig)
				}
				kills++
				return nil
			}
			st := stopPreflightGroup(p, 0, loginNow)
			if waits == 0 || st.Running != !absent || st.WasRunning != !absent || st.Killed != !absent || (kills == 0) != absent {
				t.Errorf("missing leader bypassed whole-group proof: absent=%t stop=%+v waits=%d kills=%d", absent, st, waits, kills)
			}
		})
	}
}

func TestPreflightPIDReuseBeforeKill(t *testing.T) {
	live := pidReuseStandIn(t, true)
	reused := pidReuseStat(t, live, pidReuseDifferentStart(t, live).Start)
	path := "/proc/" + strconv.Itoa(live.Pid) + "/stat"
	oldRead, oldSignal, oldWait, oldNow := readPreflightStat, preflightSignal, preflightStopWait, preflightStopNow
	t.Cleanup(func() {
		readPreflightStat, preflightSignal, preflightStopWait, preflightStopNow = oldRead, oldSignal, oldWait, oldNow
	})
	changed, reread := false, false
	readPreflightStat = func(name string) ([]byte, error) {
		if name == path && changed {
			reread = true
			return reused, nil
		}
		return oldRead(name)
	}
	terms, kills := 0, 0
	preflightSignal = func(pid int, sig syscall.Signal) error {
		switch {
		case pid == live.Pid && sig == syscall.SIGTERM:
			terms++
		case pid == -live.Pid && sig == syscall.SIGKILL:
			kills++
		default:
			t.Errorf("unexpected signal %v to %d", sig, pid)
		}
		return nil // observe only; the real stand-in remains available
	}
	preflightStopNow = func() time.Time { return loginNow }
	preflightStopWait = func(int, time.Duration, ...agentPid) bool {
		if terms != 1 {
			t.Fatal("identity change was not placed after SIGTERM")
		}
		changed = true
		return false // the unrelated replacement group still exists
	}
	st := stopPreflightGroup(live, time.Second, loginNow.Add(6*time.Second))
	if st.Running || st.WasRunning || st.Killed {
		t.Errorf("PID reuse during the grace still owns a stop: %+v; want all false", st)
	}
	if !reread || terms != 1 || kills != 0 || !pidAlive(live) {
		t.Errorf("reuse before KILL: reread=%t TERM=%d KILL=%d alive=%t", reread, terms, kills, pidAlive(live))
	}
}

func TestPreflightPIDReuseAfterMatchingObservation(t *testing.T) {
	live := pidReuseStandIn(t, true)
	reused := pidReuseStat(t, live, pidReuseDifferentStart(t, live).Start)
	path := "/proc/" + strconv.Itoa(live.Pid) + "/stat"
	oldRead, oldSignal, oldWait := readPreflightStat, preflightSignal, preflightStopWait
	t.Cleanup(func() { readPreflightStat, preflightSignal, preflightStopWait = oldRead, oldSignal, oldWait })
	reads, kills := 0, 0
	readPreflightStat = func(name string) ([]byte, error) {
		if name == path {
			reads++
			if reads > 1 {
				return reused, nil
			}
		}
		return oldRead(name)
	}
	preflightSignal = func(_ int, sig syscall.Signal) error {
		if sig == syscall.SIGKILL {
			kills++
		}
		return nil
	}
	preflightStopWait = func(int, time.Duration, ...agentPid) bool { return false }
	st := stopPreflightGroup(live, 0, loginNow)
	if st.Running || st.WasRunning || st.Killed {
		t.Errorf("observed PID reuse after a matching read was discarded: %+v; want all false", st)
	}
	if reads < 2 || kills != 0 || !pidAlive(live) {
		t.Errorf("reuse observation missing or unsafe: reads=%d KILL=%d alive=%t", reads, kills, pidAlive(live))
	}
}
