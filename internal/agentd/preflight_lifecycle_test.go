package agentd

// OP791 round 2: actual launcher, probe and task processes prove that stop
// fences every pre-flight boundary and that direct run-agent retries cannot
// replace the first launch. FIFO readiness, rather than elapsed time, places
// each stop or retry. Only the test binary is executed; no provider is used.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

const lifecycleChildEnv = "AGENTD_PREFLIGHT_LIFECYCLE_CHILD"
const lifecycleWait = 15 * time.Second

// lifecycleEvent includes start times, so even failure cleanup addresses only
// the processes this test created, never a PID that has since been reused.
type lifecycleEvent struct {
	Kind, ID, Model, State string
	PID, Descendant        int
	Start, DescendantStart string
}

type lifecycleChild struct {
	cmd   *exec.Cmd
	done  chan struct{}
	err   error
	start string
}

type lifecycleRig struct {
	t           *testing.T
	root, point string
	s           Settings
	l           Launch
	events      chan lifecycleEvent
	fifo        *os.File
	gates       map[string]*os.File
	children    []*lifecycleChild
	owned       []lifecycleEvent
}

// lifecycleEnv deliberately removes KUBECONFIG as well as KUBERNETES_*.
// The helper receives this same scrubbed environment at every subprocess hop.
func lifecycleEnv(base []string, values ...string) []string {
	out := make([]string, 0, len(base)+len(values))
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if k == "KUBECONFIG" || strings.HasPrefix(k, "KUBERNETES_") ||
			k == lifecycleChildEnv || strings.HasPrefix(k, "AGENTD_LIFECYCLE_") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, values...)
}

func newLifecycleRig(t *testing.T, point string) *lifecycleRig {
	t.Helper()
	scrubClusterEnv(t)
	r := &lifecycleRig{t: t, root: t.TempDir(), point: point, events: make(chan lifecycleEvent, 64), gates: map[string]*os.File{}}
	r.s = testSettings(t, filepath.Join(r.root, "home"))
	if err := os.MkdirAll(r.s.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"events", "hook", "primary", "fallback", "task", "descendant", "pid-record", "task-published", "result"} {
		path := filepath.Join(r.root, name+".fifo")
		if err := syscall.Mkfifo(path, 0o600); err != nil {
			t.Fatal(err)
		}
		f, err := os.OpenFile(path, os.O_RDWR|syscall.O_NONBLOCK, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if name == "events" {
			r.fifo = f
		} else {
			r.gates[name] = f
		}
	}
	go func() {
		sc := bufio.NewScanner(r.fifo)
		for sc.Scan() {
			var e lifecycleEvent
			if json.Unmarshal(sc.Bytes(), &e) == nil {
				r.events <- e
			}
		}
		close(r.events)
	}()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cli := filepath.Join(r.root, "fake-provider")
	// Shell quoting is only for generated, local executable paths. The role
	// is selected by the real pre-flight command's distinctive CLI flag.
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	script := "#!/bin/sh\nrole=task\nmodel=\nprevious=\nfor arg in \"$@\"; do\n  [ \"$previous\" = --model ] && model=$arg\n  [ \"$arg\" = --strict-mcp-config ] && role=probe\n  previous=$arg\ndone\nexport " + lifecycleChildEnv + "=$role\nexport AGENTD_LIFECYCLE_MODEL=\"$model\"\nexec " + quote(bin) + " -test.run=^TestPreflightLifecycleHelper$\n"
	if err := os.WriteFile(cli, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	r.l = Launch{Session: "lifecycle-proof", Provider: protocol.AgentClaude, Argv: []string{cli, "--model", "claude-opus-5-5"}, Prompt: "local proof", Dir: r.root, LogPath: filepath.Join(r.root, "task.log"), EventsPath: r.s.statePath(eventsFile), ConversationID: "lifecycle-proof", BootID: "proof-boot", CreatedAt: loginNow, StartDeadline: loginNow.Add(15 * time.Minute).Format(time.RFC3339), Preflight: &launchPreflight{Model: "claude-opus-5-5", Fallback: preflightFallback}}
	// Explicitly remove the unrelated admission guards this story must not
	// rely on: the pre-flight claim alone has to make a retry refuse.
	r.l.UsageSource, r.l.CaptureOutput = nil, false
	writeLaunch(t, r.s, r.l)
	if err := writeJSONFile(r.s.statePath(bootFile), bootRecord{BootID: r.l.BootID, Boot: protocol.BootReady}); err != nil {
		t.Fatal(err)
	}
	if err := writeStartHandoff(r.s, r.l, handoffStarted, ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.cleanup)
	return r
}

func (r *lifecycleRig) start(id string) *lifecycleChild {
	r.t.Helper()
	bin, err := os.Executable()
	if err != nil {
		r.t.Fatal(err)
	}
	cmd := exec.Command(bin, "-test.run=^TestPreflightLifecycleHelper$")
	cmd.Env = lifecycleEnv(os.Environ(), lifecycleChildEnv+"=launcher", "AGENTD_LIFECYCLE_ROOT="+r.root, "AGENTD_LIFECYCLE_POINT="+r.point, "AGENTD_LIFECYCLE_ID="+id, "AGENTD_LIFECYCLE_LAUNCH="+r.s.statePath(launchFile))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	logf, err := os.OpenFile(filepath.Join(r.root, id+".out"), os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		r.t.Fatal(err)
	}
	cmd.Stdout, cmd.Stderr = logf, logf
	if err := cmd.Start(); err != nil {
		_ = logf.Close()
		r.t.Fatal(err)
	}
	c := &lifecycleChild{cmd: cmd, done: make(chan struct{}), start: procStartTime(cmd.Process.Pid)}
	r.children = append(r.children, c)
	r.owned = append(r.owned, lifecycleEvent{PID: cmd.Process.Pid, Start: procStartTime(cmd.Process.Pid)})
	go func() { c.err = cmd.Wait(); _ = logf.Close(); close(c.done) }()
	return c
}

func (r *lifecycleRig) waitEvent(kind string) lifecycleEvent {
	r.t.Helper()
	timer := time.NewTimer(lifecycleWait)
	defer timer.Stop()
	for {
		select {
		case e, ok := <-r.events:
			if !ok {
				r.t.Fatalf("event FIFO closed before %s", kind)
			}
			r.owned = append(r.owned, e)
			if e.Kind == kind && e.ID == "first" {
				return e
			}
		case <-timer.C:
			r.t.Fatalf("launcher never reached %s", kind)
		}
	}
}

func (r *lifecycleRig) waitOwnedEvent(kind string) lifecycleEvent {
	r.t.Helper()
	for _, e := range r.owned {
		if e.Kind == kind && e.ID == "first" {
			return e
		}
	}
	return r.waitEvent(kind)
}

// The live-only production helper deliberately rejects zombies. These
// publication proofs need the kernel identity that a zombie still retains.
func lifecycleRawIdentity(pid int) (start, state string, err error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", "", err
	}
	i := strings.LastIndexByte(string(raw), ')')
	if i < 0 {
		return "", "", fmt.Errorf("PID %d has no process name terminator", pid)
	}
	fields := strings.Fields(string(raw)[i+1:])
	if len(fields) < 20 {
		return "", "", fmt.Errorf("PID %d has incomplete process identity", pid)
	}
	return fields[19], fields[0], nil
}

func (r *lifecycleRig) release(gate string) {
	r.t.Helper()
	if _, err := r.gates[gate].Write([]byte{'x'}); err != nil {
		r.t.Fatal(err)
	}
}

func (r *lifecycleRig) wait(c *lifecycleChild) {
	r.t.Helper()
	select {
	case <-c.done:
	case <-time.After(lifecycleWait):
		r.t.Fatal("launcher did not exit after release or stop")
	}
}

// A stopped pre-flight is proved absent as a whole group, including zombie
// members. Excluding zombies in /proc would not establish this stop proof.
func (r *lifecycleRig) assertLauncherGroupAbsent(c *lifecycleChild) {
	r.t.Helper()
	pid := c.cmd.Process.Pid
	if start := procStartTime(pid); start != "" && start == c.start {
		r.t.Errorf("stopped launcher's verified PID %d still exists", pid)
	}
	if err := syscall.Kill(-pid, 0); err != syscall.ESRCH {
		r.t.Errorf("stopped launcher group %d is not absent: kill(0) = %v, want ESRCH", pid, err)
	}
}

func lifecycleLive(pid int, start string) bool {
	if pid <= 0 || start == "" || procStartTime(pid) != start {
		return false
	}
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	i := strings.LastIndexByte(string(raw), ')')
	f := strings.Fields(string(raw)[i+1:])
	return len(f) > 0 && f[0] != "Z" && f[0] != "X"
}

func (r *lifecycleRig) cleanup() {
	// Drain readiness already written before cleaning groups. Old code puts
	// probes in a separate group, which is also owned and must be removed.
	for {
		select {
		case e, ok := <-r.events:
			if !ok {
				goto drained
			}
			r.owned = append(r.owned, e)
		default:
			goto drained
		}
	}
drained:
	for _, line := range bytes.Split(r.invocations(), []byte{'\n'}) {
		var e lifecycleEvent
		if json.Unmarshal(line, &e) == nil {
			r.owned = append(r.owned, e)
		}
	}
	for _, e := range r.owned {
		for _, p := range []agentPid{{Pid: e.PID, Start: e.Start}, {Pid: e.Descendant, Start: e.DescendantStart}} {
			if !lifecycleLive(p.Pid, p.Start) {
				continue
			}
			pgid, err := syscall.Getpgid(p.Pid)
			if err == nil && pgid != syscall.Getpgrp() {
				// All helpers inherit the owned launcher or owned probe group.
				_ = syscall.Kill(-pgid, syscall.SIGKILL)
			}
		}
	}
	for _, c := range r.children {
		select {
		case <-c.done:
		case <-time.After(lifecycleWait):
			r.t.Errorf("cleanup could not reap launcher %d", c.cmd.Process.Pid)
		}
	}
	_ = r.fifo.Close()
	for _, f := range r.gates {
		_ = f.Close()
	}
}

func lifecycleEmit(root string, e lifecycleEvent) {
	f, err := os.OpenFile(filepath.Join(root, "events.fifo"), os.O_WRONLY, 0o600)
	if err != nil {
		os.Exit(91)
	}
	b, _ := json.Marshal(e)
	_, err = f.Write(append(b, '\n'))
	_ = f.Close()
	if err != nil {
		os.Exit(92)
	}
}

func lifecycleGate(root, gate string) {
	f, err := os.Open(filepath.Join(root, gate+".fifo"))
	if err != nil {
		os.Exit(93)
	}
	var b [1]byte
	_, err = io.ReadFull(f, b[:])
	_ = f.Close()
	if err != nil {
		os.Exit(94)
	}
}

func lifecycleInvocation(root string, e lifecycleEvent) {
	f, err := os.OpenFile(filepath.Join(root, "invocations.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		os.Exit(95)
	}
	b, _ := json.Marshal(e)
	_, err = f.Write(append(b, '\n'))
	_ = f.Close()
	if err != nil {
		os.Exit(96)
	}
}

// TestPreflightLifecycleHelper is reexecuted for all stand-ins. No command
// here can resolve an actual provider executable or retain cluster access.
func TestPreflightLifecycleHelper(t *testing.T) {
	role := os.Getenv(lifecycleChildEnv)
	if role == "" {
		return
	}
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if k == "KUBECONFIG" || strings.HasPrefix(k, "KUBERNETES_") {
			t.Fatalf("cluster environment reached %s helper", role)
		}
	}
	root, point, id := os.Getenv("AGENTD_LIFECYCLE_ROOT"), os.Getenv("AGENTD_LIFECYCLE_POINT"), os.Getenv("AGENTD_LIFECYCLE_ID")
	model := os.Getenv("AGENTD_LIFECYCLE_MODEL")
	self := os.Getpid()
	e := lifecycleEvent{Kind: role, ID: id, Model: model, PID: self, Start: procStartTime(self)}
	switch role {
	case "launcher":
		runAgentNow = func() time.Time { return loginNow.Add(time.Minute) }
		if strings.HasPrefix(point, "settle-") {
			writes := 0
			writePreflightRecord = func(path string, value any) error {
				writes++
				if writes == 2 || (point == "settle-persistent" && writes > 2) {
					lifecycleInvocation(root, lifecycleEvent{Kind: "settle-write-failed", ID: id})
					return fmt.Errorf("injected ownership write failure")
				}
				return writeWorkspaceJSON(path, value)
			}
		}
		n := 0
		beforeProviderSpawn = func(Launch) {
			n++
			if id == "first" && ((point == "before-primary" && n == 1) || (point == "before-task" && n == 2)) {
				e.Kind = "hook"
				lifecycleEmit(root, e)
				lifecycleGate(root, "hook")
			}
		}
		beforeTaskStart = func() {
			if id == "first" && point == "spawn-race" {
				e.Kind = "hook"
				lifecycleEmit(root, e)
				lifecycleGate(root, "hook")
			}
		}
		var pidRecord lifecycleEvent
		beforeTaskPIDRecord = func(pid int) {
			// Count the actual successful Start, even if the stop reaches the
			// child before its first instruction or readiness write.
			lifecycleInvocation(root, lifecycleEvent{Kind: "task-spawn", ID: id, PID: pid, Start: procStartTime(pid)})
			if id != "first" || (point != "zombie-before-pid" && point != "live-before-pid") {
				return
			}
			if point == "zombie-before-pid" {
				// RunAgent is the direct parent and has not called Wait yet.
				// WNOWAIT observes exit without consuming the zombie identity.
				for {
					var info unix.Siginfo
					err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
					if err == unix.EINTR {
						continue
					}
					if err != nil {
						t.Fatal(err)
					}
					break
				}
			}
			start, state, err := lifecycleRawIdentity(pid)
			if err != nil {
				t.Fatal(err)
			}
			pidRecord = lifecycleEvent{Kind: "pid-record", ID: id, PID: pid, Start: start, State: state}
			lifecycleEmit(root, pidRecord)
			lifecycleGate(root, "pid-record")
		}
		beforeTaskInstructionPublish = func(Launch) {
			if point == "slow-result" {
				lifecycleEmit(root, lifecycleEvent{Kind: "task-published", ID: id})
			}
			if id == "first" && point == "zombie-before-pid" {
				// The spawn lock has been released; the task Wait goroutine
				// can reap its leader while this keeps agent.pid observable.
				pidRecord.Kind = "pid-published"
				lifecycleEmit(root, pidRecord)
				lifecycleGate(root, "task-published")
			}
		}
		beforeTaskResultWrite = func() {
			if point == "slow-result" {
				lifecycleEmit(root, lifecycleEvent{Kind: "before-result", ID: id})
				lifecycleGate(root, "result")
			}
		}
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
		code := RunAgent(os.Getenv("AGENTD_LIFECYCLE_LAUNCH"), os.Stdout, sigs, 0)
		signal.Stop(sigs)
		os.Exit(code)
	case "descendant":
		signal.Ignore(syscall.SIGTERM)
		lifecycleInvocation(root, e)
		ready := os.NewFile(3, "descendant-ready")
		_, _ = ready.Write([]byte{'r'})
		_ = ready.Close()
		lifecycleGate(root, "descendant")
		os.Exit(0)
	case "probe":
		// Record the owned probe before it creates or waits on a child, so
		// cleanup also finds it if readiness itself ever fails.
		lifecycleInvocation(root, e)
		phase := strings.TrimPrefix(point, "grace-")
		blocked := id == "first" && ((phase == "primary" && model == "claude-opus-5-5") || (phase == "fallback" && model == preflightFallback))
		if blocked && !strings.HasPrefix(point, "grace-") {
			bin, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			rd, wr, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			child := exec.Command(bin, "-test.run=^TestPreflightLifecycleHelper$")
			child.Env = lifecycleEnv(os.Environ(), lifecycleChildEnv+"=descendant", "AGENTD_LIFECYCLE_ROOT="+root, "AGENTD_LIFECYCLE_ID="+id)
			child.ExtraFiles = []*os.File{wr}
			child.Stdout, child.Stderr = io.Discard, io.Discard
			if err := child.Start(); err != nil {
				t.Fatal(err)
			}
			_ = wr.Close()
			var b [1]byte
			if _, err := io.ReadFull(rd, b[:]); err != nil {
				t.Fatal(err)
			}
			_ = rd.Close()
			e.Descendant, e.DescendantStart = child.Process.Pid, procStartTime(child.Process.Pid)
			go func() { _ = child.Wait() }()
		}
		lifecycleEmit(root, e)
		if blocked {
			if model == preflightFallback {
				lifecycleGate(root, "fallback")
			} else {
				lifecycleGate(root, "primary")
			}
		}
		if point == "failure" || (phase == "fallback" && model == "claude-opus-5-5") {
			fmt.Println(`{"type":"assistant","is_api_error_message":true,"error":"model_not_found","message":{"content":[{"type":"text","text":"unknown model"}]}}`)
			os.Exit(1)
		}
		fmt.Println(strings.ReplaceAll(preflightAccepted, "claude-opus-5-5", model))
		os.Exit(0)
	case "task":
		lifecycleInvocation(root, e)
		if point == "zombie-before-pid" {
			e.Descendant, e.DescendantStart = lifecycleTaskDescendant(t, root, id)
		}
		lifecycleEmit(root, e)
		if point == "spawn-race" || point == "live-before-pid" || point == "slow-result" {
			lifecycleGate(root, "task")
		}
		fmt.Print(streamLines)
		os.Exit(0)
	default:
		t.Fatalf("unknown lifecycle role %q", role)
	}
}

func lifecycleTaskDescendant(t *testing.T, root, id string) (int, string) {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	rd, wr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(bin, "-test.run=^TestPreflightLifecycleHelper$")
	child.Env = lifecycleEnv(os.Environ(), lifecycleChildEnv+"=descendant", "AGENTD_LIFECYCLE_ROOT="+root, "AGENTD_LIFECYCLE_ID="+id)
	child.ExtraFiles = []*os.File{wr}
	child.Stdout, child.Stderr = io.Discard, io.Discard
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	_ = wr.Close()
	var b [1]byte
	if _, err := io.ReadFull(rd, b[:]); err != nil {
		t.Fatal(err)
	}
	_ = rd.Close()
	start := procStartTime(child.Process.Pid)
	go func() { _ = child.Wait() }()
	return child.Process.Pid, start
}

func (r *lifecycleRig) invocations() []byte {
	r.t.Helper()
	b, err := os.ReadFile(filepath.Join(r.root, "invocations.jsonl"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		r.t.Fatal(err)
	}
	return b
}

func (r *lifecycleRig) assertNoTask() {
	r.t.Helper()
	for _, line := range bytes.Split(r.invocations(), []byte{'\n'}) {
		var e lifecycleEvent
		if json.Unmarshal(line, &e) == nil && (e.Kind == "task" || e.Kind == "task-spawn") {
			r.t.Errorf("task started after stop: %+v", e)
		}
	}
}

func TestPreflightLifecycleStopAndRescueFenceEveryBoundary(t *testing.T) {
	for _, action := range []string{"stop", "rescue"} {
		for _, point := range []string{"primary", "fallback", "before-task"} {
			t.Run(action+"/"+point, func(t *testing.T) {
				r := newLifecycleRig(t, point)
				first := r.start("first")
				var e lifecycleEvent
				if point == "before-task" {
					e = r.waitEvent("hook")
				} else {
					for {
						e = r.waitEvent("probe")
						if (point == "primary" && e.Model == r.l.Preflight.Model) || (point == "fallback" && e.Model == preflightFallback) {
							break
						}
					}
				}
				var stopped *protocol.AgentStop
				if action == "stop" {
					stopped = stopAgent(r.s, 0)
				} else {
					report, err := Rescue(context.Background(), &fakeRunner{}, r.s, "lifecycle-proof", loginNow, RescueOptions{StopAgent: true, StopGrace: 0})
					if err != nil {
						t.Fatal(err)
					}
					stopped = report.Agent
				}
				if stopped == nil || !stopped.WasRunning || stopped.Running {
					t.Errorf("%s during %s reported %+v; want observed and stopped launcher", action, point, stopped)
				}
				// An answer released after the stop must never become a task.
				gate := point
				if point == "before-task" {
					gate = "hook"
				}
				r.release(gate)
				r.wait(first)
				r.assertNoTask()
				r.assertLauncherGroupAbsent(first)
				if lifecycleLive(e.PID, e.Start) || lifecycleLive(e.Descendant, e.DescendantStart) {
					t.Errorf("stop left the paused launch or its descendant alive: %+v", e)
				}
			})
		}
	}
}

func TestPreflightLifecycleAbruptLauncherDeathStopsDescendant(t *testing.T) {
	r := newLifecycleRig(t, "primary")
	first := r.start("first")
	e := r.waitEvent("probe")
	if e.Descendant == 0 {
		t.Fatal("probe did not create its descendant")
	}
	if err := syscall.Kill(first.cmd.Process.Pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	r.wait(first)
	if !lifecycleLive(e.Descendant, e.DescendantStart) {
		t.Fatal("the abrupt-death proof lost its still-live orphan descendant")
	}
	if !preflightLive(r.s, r.l) {
		t.Error("pre-flight reports not live after launcher death while its orphan descendant is still alive")
	}
	stopped := stopAgent(r.s, 0)
	if stopped.Running {
		t.Errorf("stop could not finish the interrupted pre-flight: %+v", stopped)
	}
	if lifecycleLive(e.PID, e.Start) || lifecycleLive(e.Descendant, e.DescendantStart) {
		t.Errorf("launcher death and stop left its probe tree alive: %+v; stop %+v", e, stopped)
	}
	r.release("primary")
	r.assertNoTask()
	r.assertLauncherGroupAbsent(first)
}

func TestPreflightLifecycleDirectRunAgentCannotClaimTwice(t *testing.T) {
	for _, point := range []string{"before-primary", "primary", "fallback", "success", "failure"} {
		t.Run(point, func(t *testing.T) {
			r := newLifecycleRig(t, point)
			first := r.start("first")
			switch point {
			case "before-primary":
				r.waitEvent("hook")
			case "primary", "fallback":
				for {
					e := r.waitEvent("probe")
					if (point == "primary" && e.Model == r.l.Preflight.Model) || (point == "fallback" && e.Model == preflightFallback) {
						break
					}
				}
			default:
				r.wait(first)
			}
			original, err := os.ReadFile(r.s.statePath(startPreflightFile))
			if err != nil {
				t.Fatal(err)
			}
			invocations := r.invocations()
			duplicate := r.start("duplicate")
			r.wait(duplicate)
			if duplicate.err == nil {
				t.Error("a second direct RunAgent invocation was admitted")
			}
			after, err := os.ReadFile(r.s.statePath(startPreflightFile))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(original, after) {
				t.Errorf("retry replaced the original pre-flight record\nbefore %s\nafter %s", original, after)
			}
			if after := r.invocations(); !bytes.Equal(invocations, after) {
				t.Errorf("retry started another probe or task\nbefore %s\nafter %s", invocations, after)
			}
			switch point {
			case "before-primary":
				r.release("hook")
			case "primary", "fallback":
				r.release(point)
			}
			r.wait(first)
		})
	}
}

// lifecycleStopMarker watches the durable marker without polling or sleeping.
// The stop's return is an alternate readiness signal on old code, which never
// creates the marker. The watch is installed before that stop begins.
func lifecycleStopMarker(t *testing.T, dir string) (<-chan struct{}, func()) {
	t.Helper()
	fd, err := syscall.InotifyInit1(syscall.IN_CLOEXEC | syscall.IN_NONBLOCK)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := syscall.InotifyAddWatch(fd, dir, syscall.IN_CREATE|syscall.IN_MOVED_TO); err != nil {
		_ = syscall.Close(fd)
		t.Fatal(err)
	}
	ready := make(chan struct{})
	var once sync.Once
	watch := os.NewFile(uintptr(fd), "lifecycle-stop-marker")
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := watch.Read(buf)
			if err != nil {
				return
			}
			for at := 0; at+syscall.SizeofInotifyEvent <= n; {
				length := int(binary.NativeEndian.Uint32(buf[at+12 : at+16]))
				end := at + syscall.SizeofInotifyEvent + length
				if end > n {
					return
				}
				name := strings.TrimRight(string(buf[at+syscall.SizeofInotifyEvent:end]), "\x00")
				if name == "start-stopped.json" {
					once.Do(func() { close(ready) })
				}
				at = end
			}
		}
	}()
	return ready, func() { _ = watch.Close() }
}

func TestPreflightLifecycleStopSerializesFinalTaskSpawn(t *testing.T) {
	r := newLifecycleRig(t, "spawn-race")
	first := r.start("first")
	r.waitEvent("hook") // final marker check passed; task cmd.Start has not run
	marker, closeWatch := lifecycleStopMarker(t, r.s.StateDir)
	defer closeWatch()
	done := make(chan *protocol.AgentStop, 1)
	go func() { done <- stopAgent(r.s, 0) }()
	var stopped *protocol.AgentStop
	select {
	case <-marker:
	case stopped = <-done:
	case <-time.After(lifecycleWait):
		t.Fatal("stop neither wrote its marker nor returned")
	}
	r.release("hook")
	if stopped == nil {
		select {
		case stopped = <-done:
		case <-time.After(lifecycleWait):
			t.Fatal("stop did not release the spawn lock")
		}
	}
	// A real stop can kill the recorded task before its helper writes ready.
	// Old code instead returns stopped and leaves a live, unrecorded task.
	timer := time.NewTimer(lifecycleWait)
	defer timer.Stop()
	for {
		select {
		case <-first.done:
			if stopped.Running {
				t.Errorf("stop failed to finish the fenced task: %+v", stopped)
			}
			for _, line := range bytes.Split(r.invocations(), []byte{'\n'}) {
				var e lifecycleEvent
				if json.Unmarshal(line, &e) == nil && e.Kind == "task" && lifecycleLive(e.PID, e.Start) && !stopped.Running {
					t.Errorf("stop reported not running after launcher exited while its task survived: %+v", e)
				}
			}
			r.assertLauncherGroupAbsent(first)
			return
		case e := <-r.events:
			r.owned = append(r.owned, e)
			if e.Kind == "task" && lifecycleLive(e.PID, e.Start) && !stopped.Running {
				t.Errorf("stop reported not running while the task survived the final spawn window: task %+v; stop %+v", e, stopped)
				return
			}
		case <-timer.C:
			t.Fatal("launcher remained live after stop completed")
		}
	}
}

func TestPreflightLifecycleZombieTaskLeaderRetainsPublishedIdentity(t *testing.T) {
	r := newLifecycleRig(t, "zombie-before-pid")
	first := r.start("first")
	identity := r.waitEvent("pid-record")
	task := r.waitOwnedEvent("task")
	if identity.State != "Z" || identity.Start == "" || identity.PID != task.PID || identity.Start != task.Start {
		t.Fatalf("publication boundary has identity %+v, want task %+v as a verified zombie", identity, task)
	}
	if !lifecycleLive(task.Descendant, task.DescendantStart) {
		t.Fatal("the exited task leader did not leave its verified live descendant")
	}
	if _, err := os.Lstat(r.s.statePath(pidFile)); !os.IsNotExist(err) {
		t.Fatalf("task PID was already published before its publication hook: %v", err)
	}
	r.release("pid-record")
	r.waitEvent("pid-published")
	var published agentPid
	if err := readJSONFile(r.s.statePath(pidFile), &published); err != nil {
		t.Fatal(err)
	}
	if published.Pid != identity.PID || published.Start != identity.Start {
		t.Errorf("task PID publication lost zombie identity: got %+v, want PID %d start %q", published, identity.PID, identity.Start)
	}
	stopped := stopAgent(r.s, 0)
	if stopped.Running {
		t.Errorf("stop did not finish the task whose exited leader retained its identity: %+v", stopped)
	}
	if lifecycleLive(task.Descendant, task.DescendantStart) {
		t.Errorf("stop left the published task's descendant alive: %+v; stop %+v", task, stopped)
	}
	if err := syscall.Kill(-task.PID, 0); !stopped.Running && err != syscall.ESRCH {
		t.Errorf("stop reported not running while published task group %d remains: %v", task.PID, err)
	}
	r.release("task-published")
	r.wait(first)
	r.assertLauncherGroupAbsent(first)
}

func TestPreflightLifecycleLauncherDeathBeforePIDPublicationFailsClosed(t *testing.T) {
	r := newLifecycleRig(t, "live-before-pid")
	if err := writeStartHandoff(r.s, r.l, handoffStarted, ""); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(r.s.statePath(bootFile), bootRecord{BootID: r.l.BootID, Boot: protocol.BootReady}); err != nil {
		t.Fatal(err)
	}
	first := r.start("first")
	identity := r.waitEvent("pid-record")
	task := r.waitOwnedEvent("task")
	if identity.PID != task.PID || identity.Start != task.Start || !lifecycleLive(task.PID, task.Start) {
		t.Fatalf("PID publication hook did not hold the verified live task: hook %+v; task %+v", identity, task)
	}
	if _, err := os.Lstat(r.s.statePath(pidFile)); !os.IsNotExist(err) {
		t.Fatalf("task PID was already published before launcher death: %v", err)
	}
	if err := syscall.Kill(first.cmd.Process.Pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	r.wait(first)
	if !lifecycleLive(task.PID, task.Start) {
		t.Fatal("the pending publication proof lost its unrecorded live task")
	}
	stopped := stopAgent(r.s, 0)
	if !stopped.Running {
		t.Errorf("stop reported not running after launcher died before publishing its live task: task %+v; stop %+v", task, stopped)
	}
	if !preflightLive(r.s, r.l) {
		t.Error("unconfirmed task PID publication stopped counting as a live pre-flight")
	}
	sess := protocol.Session{Name: r.l.Session, Agent: protocol.AgentClaude, Mode: protocol.ModeTask, Model: r.l.Preflight.Model, Caller: summonedCaller}
	status := CollectStatus(context.Background(), &fakeRunner{}, r.s, sess, loginNow.Add(3*time.Minute))
	if status.Agent.State != protocol.AgentBusy {
		t.Errorf("unconfirmed task PID publication status %q, want busy beyond the start window", status.Agent.State)
	}
	// The pending receipt has no confirmed task identity and therefore must
	// remain conservative even after test-owned cleanup ends that task.
	if err := syscall.Kill(-task.PID, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		t.Fatal(err)
	}
	r.assertLauncherGroupAbsent(first)
}
