package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// The complete process platform is synthetic. No signal, process launch, tmux,
// Codex binary, Kubernetes client, credential or native transcript is used.
type remoteProcEntry string

func (e remoteProcEntry) Name() string               { return string(e) }
func (e remoteProcEntry) IsDir() bool                { return true }
func (e remoteProcEntry) Type() fs.FileMode          { return fs.ModeDir }
func (e remoteProcEntry) Info() (fs.FileInfo, error) { return nil, errors.New("unused") }

type remoteStopRig struct {
	initOK   bool
	threads  map[int]map[int]string
	cmdlines map[int]string

	s                  Settings
	sess               protocol.Session
	p                  codexRemoteProcesses
	procs              map[int]codexRemoteProcess
	fds                map[int]codexRemoteProcess
	term               map[int]bool
	stubborn           map[int]bool
	init               string
	exe                bool
	clock              time.Time
	next               int
	signals            []string
	attempts           []string
	readErr, listErr   string
	openErr, signalErr error
	afterOpen          func(int)
	afterSignal        func(int, unix.Signal)
	threadRunning      bool
	keepKilled         bool
}

func remoteStat(proc codexRemoteProcess) []byte {
	f := make([]string, 20)
	for i := range f {
		f[i] = "0"
	}
	f[0], f[1], f[19] = proc.state, strconv.Itoa(proc.parent), proc.start
	return []byte(fmt.Sprintf("%d (a name ) with spaces) %s", proc.pid, strings.Join(f, " ")))
}
func newRemoteStopRig(t *testing.T) *remoteStopRig {
	t.Helper()
	r := &remoteStopRig{s: testSettings(t, t.TempDir()), procs: map[int]codexRemoteProcess{}, fds: map[int]codexRemoteProcess{}, term: map[int]bool{}, stubborn: map[int]bool{}, exe: true, init: "/usr/local/bin/tini\x00--\x00/usr/local/bin/agentd\x00run\x00", clock: rescueNow, next: 1000}
	r.s.RuntimeDir = filepath.Join(t.TempDir(), "runtime")
	if err := os.Mkdir(r.s.RuntimeDir, 0700); err != nil {
		t.Fatal(err)
	}
	r.initOK = true
	r.threads = map[int]map[int]string{}
	r.cmdlines = map[int]string{}
	r.sess = codexRemoteSess
	r.sess.SessionUID = "session-c1"
	r.s.PodUID = "pod-c1"
	raw, _ := json.Marshal(r.sess)
	r.s.Getenv = envOf(map[string]string{protocol.SessionEnv: string(raw)})
	for _, pid := range []int{10, 30, 40, 41, 42, 43} {
		// 10 agentd; 30 daemon; 40 tmux server; 41 pane; 42 client;
		// 43 already detached/orphaned child. Names never drive selection.
		r.procs[pid] = codexRemoteProcess{pid: pid, parent: 1, start: strconv.Itoa(pid * 100), state: "S"}
	}
	writeJSON(t, r.s.statePath(bootFile), bootRecord{BootID: "boot-c1", Daemon: &daemonProcess{SessionUID: r.sess.SessionUID, PodUID: r.s.PodUID, Pid: 10, Start: "1000"}})
	if err := os.Chmod(r.s.StateDir, 0700); err != nil {
		t.Fatal(err)
	}
	r.p = codexRemoteProcesses{self: 20, initExe: func() bool { return r.initOK }, sameExe: func(int) bool { return r.exe }, now: func() time.Time { return r.clock }, close: func(fd int) error {
		if _, ok := r.fds[fd]; !ok {
			t.Error("pidfd closed twice")
		}
		delete(r.fds, fd)
		return nil
	},
		pause: func(ctx context.Context) error {
			r.clock = r.clock.Add(50 * time.Millisecond)
			if r.clock.Sub(rescueNow) > 45*time.Second {
				return errors.New("fake stop exceeded finite clock bound")
			}
			return ctx.Err()
		},
		read: func(path string) ([]byte, error) {
			if path == r.readErr {
				return nil, errors.New("synthetic read failure")
			}
			if value, ok := r.cmdlinesPath(path); ok {
				return []byte(value), nil
			}
			if path == "/proc/1/cmdline" {
				return []byte(r.init), nil
			}
			var pid int
			if _, err := fmt.Sscanf(path, "/proc/%d/", &pid); err != nil {
				return nil, os.ErrNotExist
			}
			proc, ok := r.procs[pid]
			if !ok {
				return nil, os.ErrNotExist
			}
			if strings.HasSuffix(path, "/cmdline") {
				return []byte(r.cmdlines[pid]), nil
			}
			if strings.Contains(path, "/task/") {
				parts := strings.Split(path, "/")
				tid, _ := strconv.Atoi(parts[4])
				proc.pid = tid
				if state, ok := r.threads[pid][tid]; ok {
					proc.state = state
				}
			}
			if strings.Contains(path, "/task/") && r.threadRunning && pid == 10 {
				proc.state = "R"
			}
			return remoteStat(proc), nil
		},
		list: func(path string) ([]os.DirEntry, error) {
			if path == r.listErr {
				return nil, errors.New("synthetic list failure")
			}
			if path == "/proc" {
				out := []os.DirEntry{remoteProcEntry("1"), remoteProcEntry("20"), remoteProcEntry("self")}
				ids := []int{}
				for pid := range r.procs {
					ids = append(ids, pid)
				}
				slices.Sort(ids)
				for _, pid := range ids {
					out = append(out, remoteProcEntry(strconv.Itoa(pid)))
				}
				return out, nil
			}
			var pid int
			_, err := fmt.Sscanf(path, "/proc/%d/task", &pid)
			if err != nil {
				return nil, err
			}
			out := []os.DirEntry{remoteProcEntry(strconv.Itoa(pid))}
			for tid := range r.threads[pid] {
				if tid != pid {
					out = append(out, remoteProcEntry(strconv.Itoa(tid)))
				}
			}
			return out, nil
		},
		open: func(pid int) (int, error) {
			if r.openErr != nil {
				return -1, r.openErr
			}
			proc, ok := r.procs[pid]
			if !ok {
				return -1, unix.ESRCH
			}
			fd := r.next
			r.next++
			r.fds[fd] = proc
			if r.afterOpen != nil {
				r.afterOpen(pid)
			}
			return fd, nil
		},
		signal: func(fd int, sig unix.Signal) error {
			if r.signalErr != nil {
				return r.signalErr
			}
			held, ok := r.fds[fd]
			r.attempts = append(r.attempts, fmt.Sprintf("%d:%d", held.pid, sig))
			if !ok {
				t.Fatal("unowned pidfd")
			}
			cur, ok := r.procs[held.pid]
			if !ok || cur.start != held.start {
				return unix.ESRCH
			}
			r.signals = append(r.signals, fmt.Sprintf("%d:%d", cur.pid, sig))
			switch sig {
			case unix.SIGSTOP:
				for tid := range r.threads[cur.pid] {
					r.threads[cur.pid][tid] = "T"
				}
				cur.state = "T"
				r.procs[cur.pid] = cur
			case unix.SIGTERM:
				r.term[cur.pid] = true
			case unix.SIGCONT:
				if cur.pid == 10 {
					t.Fatal("supervisor must never thaw")
				}
				cur.state = "S"
				r.procs[cur.pid] = cur
				if r.term[cur.pid] && !r.stubborn[cur.pid] {
					delete(r.procs, cur.pid)
				}
			case unix.SIGKILL:
				if cur.pid == 10 {
					t.Fatal("supervisor must stay frozen through snapshot")
				}
				if !r.keepKilled {
					delete(r.procs, cur.pid)
				}
			}
			if r.afterSignal != nil {
				r.afterSignal(cur.pid, sig)
			}
			return nil
		},
	}
	return r
}
func (r *remoteStopRig) install(t *testing.T) {
	t.Helper()
	old := codexRemoteProcessesFor
	codexRemoteProcessesFor = func() codexRemoteProcesses { return r.p }
	t.Cleanup(func() { codexRemoteProcessesFor = old })
}
func (r *remoteStopRig) quiesced() bool {
	if r.procs[10].state != "T" {
		return false
	}
	for pid, p := range r.procs {
		if pid != 10 && !remoteProcessGone(p) {
			return false
		}
	}
	return true
}
func (r *remoteStopRig) stop(ctx context.Context, grace time.Duration) (*protocol.AgentStop, error) {
	return quiesceCodexRemote(ctx, r.s, r.sess, grace, r.p)
}
func TestCodexRemoteStopQuiescesEveryWriter(t *testing.T) {
	r := newRemoteStopRig(t)
	st, err := r.stop(context.Background(), time.Second)
	if err != nil || st.Running || !st.WasRunning || st.Killed || !r.quiesced() {
		t.Fatalf("stop=%+v err=%v procs=%v", st, err, r.procs)
	}
	if len(r.fds) != 0 {
		t.Fatal("pidfds leaked")
	}
	for _, pid := range []int{30, 40, 41, 42, 43} {
		for _, sig := range []unix.Signal{unix.SIGSTOP, unix.SIGTERM, unix.SIGCONT} {
			if !slices.Contains(r.signals, fmt.Sprintf("%d:%d", pid, sig)) {
				t.Fatalf("writer %d missed %v", pid, sig)
			}
		}
	}
}
func TestCodexRemoteStopCatchesChildrenBornDuringGrace(t *testing.T) {
	r := newRemoteStopRig(t)
	r.afterSignal = func(pid int, sig unix.Signal) {
		if pid == 30 && sig == unix.SIGCONT {
			r.procs[44] = codexRemoteProcess{pid: 44, parent: 1, start: "4400", state: "S"}
			r.stubborn[44] = true
		}
	}
	st, err := r.stop(context.Background(), 0)
	if err != nil || st.Running || !st.Killed || !r.quiesced() {
		t.Fatalf("stop=%+v err=%v procs=%v", st, err, r.procs)
	}
	if !slices.Contains(r.signals, "44:19") || !slices.Contains(r.signals, "44:9") {
		t.Fatalf("new detached writer missed: %v", r.signals)
	}
}
func TestCodexRemoteStopRefusesUnprovenNamespace(t *testing.T) {
	cases := map[string]func(*remoteStopRig){
		"missing boot":       func(r *remoteStopRig) { _ = os.Remove(r.s.statePath(bootFile)) },
		"missing supervisor": func(r *remoteStopRig) { writeJSON(t, r.s.statePath(bootFile), bootRecord{}) },
		"session UID":        func(r *remoteStopRig) { r.sess.SessionUID = "other" },
		"pod UID":            func(r *remoteStopRig) { r.s.PodUID = "other" },
		"supervisor PID1": func(r *remoteStopRig) {
			var b bootRecord
			_ = readJSONFile(r.s.statePath(bootFile), &b)
			b.Daemon.Pid = 1
			writeJSON(t, r.s.statePath(bootFile), b)
		},
		"supervisor is self":        func(r *remoteStopRig) { r.p.self = 10 },
		"supervisor start":          func(r *remoteStopRig) { p := r.procs[10]; p.start = "999"; r.procs[10] = p },
		"supervisor parent":         func(r *remoteStopRig) { p := r.procs[10]; p.parent = 2; r.procs[10] = p },
		"supervisor dead":           func(r *remoteStopRig) { p := r.procs[10]; p.state = "Z"; r.procs[10] = p },
		"immutable PID1 executable": func(r *remoteStopRig) { r.initOK = false },
		"supervisor executable":     func(r *remoteStopRig) { r.exe = false },
		"self PID1":                 func(r *remoteStopRig) { r.p.self = 1 },
		"host namespace":            func(r *remoteStopRig) { r.init = "/sbin/init\x00" },
		"shared namespace":          func(r *remoteStopRig) { r.init = "/pause\x00" },
		"wrong tini child":          func(r *remoteStopRig) { r.init = "/usr/local/bin/tini\x00--\x00/bin/sh\x00" },
		"wrong tini separator":      func(r *remoteStopRig) { r.init = "/usr/local/bin/tini\x00-g\x00/usr/local/bin/agentd\x00" },
		"wrong agentd mode":         func(r *remoteStopRig) { r.init = "/usr/local/bin/tini\x00--\x00/usr/local/bin/agentd\x00other\x00" },
		"unreadable PID1":           func(r *remoteStopRig) { r.readErr = "/proc/1/cmdline" },
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			r := newRemoteStopRig(t)
			edit(r)
			st, err := r.stop(context.Background(), 0)
			if err == nil || !st.Running || len(r.signals) != 0 {
				t.Fatalf("unsafe stop=%+v err=%v signals=%v", st, err, r.signals)
			}
		})
	}
}
func TestCodexRemoteStopBlocksOnUncertainty(t *testing.T) {
	cases := map[string]func(*remoteStopRig){
		"namespace enumeration": func(r *remoteStopRig) { r.listErr = "/proc" },
		"process inspection":    func(r *remoteStopRig) { r.readErr = "/proc/30/stat" },
		"thread enumeration":    func(r *remoteStopRig) { r.listErr = "/proc/10/task" },
		"thread inspection":     func(r *remoteStopRig) { r.readErr = "/proc/10/task/10/stat" },
		"thread still running":  func(r *remoteStopRig) { r.threadRunning = true },
		"pidfd unsupported":     func(r *remoteStopRig) { r.openErr = unix.ENOSYS },
		"signal refused":        func(r *remoteStopRig) { r.signalErr = unix.EPERM },
		"writers survive KILL":  func(r *remoteStopRig) { r.stubborn[30] = true; r.keepKilled = true },
		"PID replaced during open": func(r *remoteStopRig) {
			r.afterOpen = func(pid int) {
				if pid == 30 {
					p := r.procs[pid]
					p.start = "9999"
					r.procs[pid] = p
				}
			}
		},
		"supervisor thawed": func(r *remoteStopRig) {
			r.afterSignal = func(pid int, sig unix.Signal) {
				if pid == 30 && sig == unix.SIGCONT {
					p := r.procs[10]
					p.state = "S"
					r.procs[10] = p
				}
			}
		},
		"process bound": func(r *remoteStopRig) {
			for pid := 100; pid < 700; pid++ {
				r.procs[pid] = codexRemoteProcess{pid: pid, parent: 1, start: strconv.Itoa(pid), state: "S"}
			}
		},
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			r := newRemoteStopRig(t)
			edit(r)
			st, err := r.stop(context.Background(), 0)
			if err == nil || !st.Running {
				t.Fatalf("uncertain stop=%+v err=%v", st, err)
			}
			if len(r.fds) != 0 {
				t.Fatal("pidfds leaked")
			}
		})
	}
	t.Run("cancelled", func(t *testing.T) {
		r := newRemoteStopRig(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		st, err := r.stop(ctx, 0)
		if err == nil || !st.Running {
			t.Fatalf("stop=%+v %v", st, err)
		}
	})
}
func TestCodexRemoteStopAllowsZombiesButNoLiveWriter(t *testing.T) {
	r := newRemoteStopRig(t)
	r.afterSignal = func(pid int, sig unix.Signal) {
		if sig == unix.SIGCONT {
			r.procs[pid] = codexRemoteProcess{pid: pid, parent: 10, start: strconv.Itoa(pid * 100), state: "Z"}
		}
	}
	st, err := r.stop(context.Background(), time.Second)
	if err != nil || st.Running || !r.quiesced() {
		t.Fatalf("stop=%+v %v", st, err)
	}
}
func TestCodexRemoteRescueStopsBeforeSnapshot(t *testing.T) {
	r := newRemoteStopRig(t)
	r.install(t)
	snapshots := 0
	runner := &fakeRunner{handle: func(c Cmd) (Result, error) {
		if !r.quiesced() {
			t.Fatal("snapshot command before writers quiesced")
		}
		if release, err := codexRemoteLifecycle(r.s, false); err == nil {
			release()
			t.Fatal("snapshot lost lifecycle exclusion")
		} else if !strings.Contains(err.Error(), "busy") {
			t.Fatalf("snapshot lock released before Git: %v", err)
		}
		snapshots++
		return Result{}, nil
	}}
	// A synthetic clone reaches Git inspection without running any child process.
	writeFile(t, filepath.Join(r.s.ReposDir(), "haynes-ops", ".git", "config"), "fake")
	_, err := Rescue(context.Background(), runner, r.s, r.sess.Name, rescueNow, RescueOptions{StopAgent: true, StopGrace: time.Second})
	if err != nil || snapshots == 0 || !r.quiesced() {
		t.Fatalf("snapshots=%d err=%v", snapshots, err)
	}
	if err := codexRemoteStopped(r.s); !errors.Is(err, errCodexRemoteStopping) {
		t.Fatalf("stop fence not retained: %v", err)
	}
}
func TestCodexRemoteRescueRefusesBeforeGitOnStopFailure(t *testing.T) {
	r := newRemoteStopRig(t)
	r.install(t)
	r.openErr = unix.ENOSYS
	runner := &fakeRunner{handle: func(c Cmd) (Result, error) {
		t.Fatalf("snapshot after failed quiesce: %s", c.Name)
		return Result{}, nil
	}}
	rep, err := Rescue(context.Background(), runner, r.s, r.sess.Name, rescueNow, RescueOptions{StopAgent: true})
	if err == nil || rep.OK || rep.Bundle != nil || len(rep.Repos) != 0 {
		t.Fatalf("rep=%+v err=%v", rep, err)
	}
	if err := codexRemoteStopped(r.s); !errors.Is(err, errCodexRemoteStopping) {
		t.Fatalf("failure lost fence: %v", err)
	}
}
func TestCodexRemoteLifecycleExcludesSnapshotRaces(t *testing.T) {
	r := newRemoteStopRig(t)
	r.install(t)
	launch, err := codexRemoteLifecycle(r.s, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := stopCodexRemoteForRescue(context.Background(), r.s, r.sess, 0); err == nil {
		t.Fatal("stop overlapped admitted launcher")
	}
	launch()
	_, snapshot, err := stopCodexRemoteForRescue(context.Background(), r.s, r.sess, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot()
	if release, err := codexRemoteLifecycle(r.s, false); err == nil {
		release()
		t.Fatal("launcher overlapped snapshot")
	}
	if _, err := os.Stat(r.s.statePath(codexRemoteStopFile)); err != nil {
		t.Fatal(err)
	}
}
func TestCodexRemoteStopFenceSurvivesSupervisorRestart(t *testing.T) {
	r := newRemoteStopRig(t)
	r.install(t)
	_, unlock, err := stopCodexRemoteForRescue(context.Background(), r.s, r.sess, 0)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	w := newFakeCodexWorld(t)
	w.s = r.s
	c := &codexRemote{deps: w.deps(), boot: "replacement"}
	if err := c.ensure(context.Background(), r.s, rescueNow); !errors.Is(err, errCodexRemoteStopping) || w.starts() != 0 {
		t.Fatalf("restart escaped: %v", err)
	}
	r.s.RuntimeDir = filepath.Join(t.TempDir(), "runtime")
	if err := os.Mkdir(r.s.RuntimeDir, 0700); err != nil {
		t.Fatal(err)
	} // New container, SAME PodUID: the HOME fence must survive.
	d := &Daemon{S: r.s, Session: r.sess}
	if err := d.Run(context.Background()); !errors.Is(err, errCodexRemoteStopping) {
		t.Fatalf("boot escaped stop before writers: %v", err)
	}
	var b bootRecord
	_ = readJSONFile(r.s.statePath(bootFile), &b)
	if b.BootID != "boot-c1" {
		t.Fatal("restart rewrote boot before stop check")
	}
}
func TestCodexRemoteStopFencesAllSupportedLaunches(t *testing.T) {
	r := newRemoteStopRig(t)
	writeRemoteStop(t, r.s, codexRemoteStop{1, r.sess.SessionUID, r.s.PodUID})
	w := newFakeCodexWorld(t)
	if err := codexPair(context.Background(), r.s, r.sess, &bytes.Buffer{}, time.Now, w.deps()); !errors.Is(err, errCodexRemoteStopping) {
		t.Fatalf("pair escaped stop: %v", err)
	}
	f := &threadFake{now: threadNow}
	deps := f.deps()
	deps.trust = func(context.Context, Settings, int, string) error { t.Fatal("trust changed after stop"); return nil }
	got := startCodexThread(context.Background(), r.s, r.sess, CodexThreadRequest{Wait: time.Second}, deps)
	if got.Error == nil || got.Error.Code != "host_stopping" || len(f.launches) != 0 {
		t.Fatalf("thread escaped: %+v", got)
	}
	rec, err := codexRCControl(context.Background(), r.s, r.sess, "start", apiv1.CodexRCDispatch{}, realCodexRCDeps())
	if err != nil || rec.Reason != "host_stopping" {
		t.Fatalf("RC escaped: %+v %v", rec, err)
	}
}
func TestCodexRemoteStopRetainsPrivateHistoryAndResumeRequiresExactThread(t *testing.T) {
	r := newRemoteStopRig(t)
	r.install(t)
	history := map[string]string{"sessions/2026/rollout.jsonl": "synthetic history\n", "archived_sessions/old.jsonl": "old\n", "thread_history_1.sqlite": "synthetic db", "thread_history_1.sqlite-wal": "synthetic wal", "state_1.sqlite": "synthetic enrollment db", "installation_id": "synthetic installation", "session_index.jsonl": "synthetic index\n"}
	for path, data := range history {
		writeFile(t, filepath.Join(r.s.CodexHome, path), data)
	}
	_, unlock, err := stopCodexRemoteForRescue(context.Background(), r.s, r.sess, 0)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	r.s.PodUID = "resumed-pod"
	if err := codexRemoteStopped(r.s); err != nil {
		t.Fatalf("same Session new pod refused: %v", err)
	}
	// Resume only starts supervision. Existing launch/result files are decoys;
	// neither may select a latest conversation or run a client.
	writeJSON(t, r.s.statePath(launchFile), Launch{ConversationID: oldThread})
	w := newFakeCodexWorld(t)
	w.setLive(true)
	old := codexRemoteDepsFor
	codexRemoteDepsFor = func() codexRemoteDeps { return w.deps() }
	t.Cleanup(func() { codexRemoteDepsFor = old })
	d := &Daemon{S: r.s, Session: r.sess, Now: func() time.Time { return rescueNow }}
	step, _ := d.startAgent(context.Background(), protocol.Workspace{}, Step{State: StepOK}, "resumed-boot")
	if step.State != StepOK || len(w.calls) != 0 {
		t.Fatalf("resume started a conversation: %+v calls=%v", step, w.calls)
	}
	for path, data := range history {
		got, err := os.ReadFile(filepath.Join(r.s.CodexHome, path))
		if err != nil || string(got) != data {
			t.Fatalf("private history %s changed: %v", path, err)
		}
	}
	f := &threadFake{now: threadNow}
	deps := f.deps()
	listed, turn := 0, ""
	deps.list = func(_ context.Context, _ Settings, _ int, cwd string, _ bool) ([]codexThreadSummary, error) {
		listed++
		return []codexThreadSummary{{ID: oldThread, Cwd: cwd, Source: "vscode"}, {ID: newThread, Cwd: cwd, Source: "vscode"}}, nil
	}
	deps.turn = func(_ context.Context, _ Settings, _ int, id string) (codexTurnObservation, error) {
		turn = id
		return codexTurnObservation{Status: "completed", AgentMessage: true}, nil
	}
	writeFile(t, filepath.Join(r.s.ReposDir(), r.sess.Repo, ".git", "config"), "fake")
	result := observeCodexThread(context.Background(), r.s, r.sess, CodexThreadStatusRequest{}, deps)
	if result.Error == nil || result.Error.Code != "invalid_input" || listed != 0 || turn != "" {
		t.Fatalf("implicit latest binding: %+v list=%d turn=%s", result, listed, turn)
	}
	result = observeCodexThread(context.Background(), r.s, r.sess, CodexThreadStatusRequest{ThreadID: oldThread}, deps)
	if result.ThreadID != oldThread || turn != oldThread {
		t.Fatalf("explicit exact thread lost: %+v turn=%s", result, turn)
	}
}

func (r *remoteStopRig) cmdlinesPath(path string) (string, bool) {
	for pid, raw := range r.cmdlines {
		if path == fmt.Sprintf("/proc/%d/cmdline", pid) {
			return raw, true
		}
	}
	return "", false
}

func TestCodexRemoteStopHoldUsesCurrentProcessInsteadOfRetainedBoot(t *testing.T) {
	r := newRemoteStopRig(t)
	r.init = "/usr/local/bin/tini\x00--\x00/usr/local/bin/agentd\x00hold\x00"
	r.cmdlines[10] = "/usr/local/bin/agentd\x00hold\x00"
	r.s.PodUID = "distinct-hold-pod"
	// The hold did not write a new boot record. The retained executor is gone.
	st, err := r.stop(context.Background(), 0)
	if err != nil || st.Running || !r.quiesced() {
		t.Fatalf("hold stop=%+v err=%v", st, err)
	}
	// A failed/previous rescue may already have frozen this same hold process.
	st, err = r.stop(context.Background(), 0)
	if err != nil || st.Running {
		t.Fatalf("repeat hold stop=%+v err=%v", st, err)
	}
}
func TestCodexRemoteStopHoldRefusesUnprovenSupervisor(t *testing.T) {
	for _, name := range []string{"wrong command", "wrong executable", "ambiguous", "no supervisor", "enumeration failure"} {
		t.Run(name, func(t *testing.T) {
			r := newRemoteStopRig(t)
			r.init = "/usr/local/bin/tini\x00--\x00/usr/local/bin/agentd\x00hold\x00"
			r.cmdlines[10] = "/usr/local/bin/agentd\x00hold\x00"
			switch name {
			case "wrong command":
				r.cmdlines[10] = "/usr/local/bin/agentd\x00run\x00"
			case "wrong executable":
				r.exe = false
			case "ambiguous":
				r.cmdlines[30] = r.cmdlines[10]
			case "no supervisor":
				delete(r.procs, 10)
			case "enumeration failure":
				r.listErr = "/proc"
			}
			st, err := r.stop(context.Background(), 0)
			if err == nil || !st.Running || len(r.signals) != 0 {
				t.Fatalf("unsafe hold=%+v err=%v signals=%v", st, err, r.signals)
			}
		})
	}
}
func TestCodexRemoteStopZombieLeaderWithLiveWorkerIsNotAbsent(t *testing.T) {
	r := newRemoteStopRig(t)
	proc := r.procs[30]
	proc.state = "Z"
	r.procs[30] = proc
	r.threads[30] = map[int]string{31: "R"}
	st, err := r.stop(context.Background(), 0)
	if err != nil || st.Running || !slices.Contains(r.signals, "30:19") {
		t.Fatalf("live worker missed: stop=%+v err=%v signals=%v", st, err, r.signals)
	}
}
func TestCodexRemoteStopWaitsForEverySupervisorThread(t *testing.T) {
	r := newRemoteStopRig(t)
	r.threads[10] = map[int]string{11: "R"}
	// The signal is accepted but this synthetic worker never reaches stop state.
	r.afterSignal = func(pid int, sig unix.Signal) {
		if pid == 10 && sig == unix.SIGSTOP {
			r.threads[10][11] = "R"
		}
	}
	st, err := r.stop(context.Background(), 0)
	if err == nil || !st.Running || slices.Contains(r.signals, "30:19") {
		t.Fatalf("unfrozen supervisor worker accepted: %+v %v", st, err)
	}
}
func TestCodexRemoteStopRejectsReusedPIDBeforeSignal(t *testing.T) {
	r := newRemoteStopRig(t)
	r.afterOpen = func(pid int) {
		if pid == 30 {
			proc := r.procs[pid]
			proc.start = "replacement"
			r.procs[pid] = proc
		}
	}
	st, err := r.stop(context.Background(), 0)
	if err == nil || !st.Running || slices.Contains(r.attempts, "30:19") || len(r.fds) != 0 {
		t.Fatalf("replacement signalled: %+v %v %v fds=%v", st, err, r.signals, r.fds)
	}
}
func TestCodexRemoteStopBoundsAndCancellation(t *testing.T) {
	for _, name := range []string{"process cap", "thread cap", "kill not complete", "cancelled"} {
		t.Run(name, func(t *testing.T) {
			r := newRemoteStopRig(t)
			ctx := context.Background()
			switch name {
			case "process cap":
				for pid := 100; pid < 100+codexRemoteProcessCap; pid++ {
					r.procs[pid] = codexRemoteProcess{pid: pid, parent: 1, start: "1", state: "S"}
				}
			case "thread cap":
				for tid := 1000; tid < 2025; tid++ {
					if r.threads[10] == nil {
						r.threads[10] = map[int]string{}
					}
					r.threads[10][tid] = "S"
				}
			case "kill not complete":
				r.stubborn[30] = true
				r.keepKilled = true
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			st, err := r.stop(ctx, 0)
			if err == nil || !st.Running {
				t.Fatalf("uncertain stop accepted: %+v %v", st, err)
			}
			if name == "cancelled" && len(r.attempts) != 0 {
				t.Fatal("signaled a process after cancellation")
			}
			if r.clock.Sub(rescueNow) > 11*time.Second {
				t.Fatal("unbounded drain")
			}
		})
	}
}
func TestCodexRemoteStopFenceBindingAndPrivateFiles(t *testing.T) {
	for _, name := range []string{"bad json", "version", "missing session", "missing pod", "new Session", "missing current pod", "missing environment", "symlink", "public file", "oversized"} {
		t.Run(name, func(t *testing.T) {
			r := newRemoteStopRig(t)
			rec := codexRemoteStop{1, r.sess.SessionUID, r.s.PodUID}
			switch name {
			case "version":
				rec.Version = 2
			case "missing session":
				rec.SessionUID = ""
			case "missing pod":
				rec.PodUID = ""
			case "new Session":
				rec.SessionUID = "other-session"
				r.s.PodUID = "new-resume-pod"
			case "missing current pod":
				r.s.PodUID = ""
			case "missing environment":
				r.s.Getenv = nil
			}
			path := r.s.statePath(codexRemoteStopFile)
			writeRemoteStop(t, r.s, rec)
			switch name {
			case "bad json":
				writeFile(t, path, "invalid")
			case "oversized":
				raw, _ := os.ReadFile(path)
				if err := os.WriteFile(path, append(raw, []byte(strings.Repeat(" ", 4096))...), 0600); err != nil {
					t.Fatal(err)
				}
			case "public file":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target := filepath.Join(r.s.RuntimeDir, "other.json")
				if err := os.Rename(path, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			if err := codexRemoteStopped(r.s); err == nil || errors.Is(err, errCodexRemoteStopping) {
				t.Fatalf("unproven stop binding did not report its uncertainty: %v", err)
			}
		})
	}
}
func TestCodexRemoteLifecycleRejectsUnsafeLock(t *testing.T) {
	for _, name := range []string{"symlink", "public", "directory"} {
		t.Run(name, func(t *testing.T) {
			r := newRemoteStopRig(t)
			path := codexRemoteGatePath(r.s, codexRemoteLifecycleLock)
			switch name {
			case "symlink":
				target := filepath.Join(r.s.RuntimeDir, "target")
				writeFile(t, target, "unchanged")
				if err := os.Chmod(target, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			case "public":
				writeFile(t, path, "")
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if release, err := codexRemoteLifecycle(r.s, false); err == nil {
				release()
				t.Fatal("unsafe gate accepted")
			}
		})
	}
}
func TestCodexRemoteStopDoesNotChangeClaudeOrCodexTaskStop(t *testing.T) {
	for _, sess := range []protocol.Session{
		{Name: "ordinary-claude", Repo: "demo", Agent: protocol.AgentClaude, Mode: protocol.ModeRemote, Model: "claude-opus-5-5"},
		{Name: "ordinary-task", Repo: "demo", Agent: protocol.AgentCodex, Mode: protocol.ModeTask, Model: "gpt-6.1-sol", Prompt: "synthetic"},
	} {
		t.Run(sess.Name, func(t *testing.T) {
			r := newRemoteStopRig(t)
			old := codexRemoteProcessesFor
			codexRemoteProcessesFor = func() codexRemoteProcesses { t.Fatal("non-remote stop used remote drain"); return r.p }
			t.Cleanup(func() { codexRemoteProcessesFor = old })
			raw, _ := json.Marshal(sess)
			r.s.Getenv = envOf(map[string]string{protocol.SessionEnv: string(raw)})
			rep, err := Rescue(context.Background(), &fakeRunner{}, r.s, sess.Name, rescueNow, RescueOptions{StopAgent: true})
			if err != nil || rep.Agent == nil || rep.Agent.Running {
				t.Fatalf("ordinary stop changed: %+v %v", rep, err)
			}
			if _, err := os.Stat(r.s.statePath(codexRemoteStopFile)); !os.IsNotExist(err) {
				t.Fatal("ordinary stop got a remote fence")
			}
		})
	}
}

func writeRemoteStop(t *testing.T, s Settings, rec codexRemoteStop) {
	t.Helper()
	if err := os.MkdirAll(s.StateDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(s.StateDir, 0700); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, s.statePath(codexRemoteStopFile), rec)
	if err := os.Chmod(s.statePath(codexRemoteStopFile), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestCodexRemoteRescueEmptyHoldLeavesHomeEmpty(t *testing.T) {
	r := newRemoteStopRig(t)
	r.install(t)
	if err := os.RemoveAll(r.s.StateDir); err != nil {
		t.Fatal(err)
	}
	for pid := range r.procs {
		if pid != 10 {
			delete(r.procs, pid)
		}
	}
	r.init = "/usr/local/bin/tini\x00--\x00/usr/local/bin/agentd\x00hold\x00"
	r.cmdlines[10] = "/usr/local/bin/agentd\x00hold\x00"
	for attempt := 0; attempt < 2; attempt++ {
		rep, err := Rescue(context.Background(), &fakeRunner{}, r.s, r.sess.Name, rescueNow, RescueOptions{StopAgent: true})
		if err != nil || !rep.OK || !rep.VolumeEmpty || rep.Agent == nil || rep.Agent.Running {
			t.Fatalf("empty hold attempt %d: %+v %v", attempt, rep, err)
		}
		entries, err := os.ReadDir(r.s.Home)
		if err != nil || len(entries) != 0 {
			t.Fatalf("empty HOME acquired state: %v %v", entries, err)
		}
	}
	if _, err := os.Stat(codexRemoteGatePath(r.s, codexRemoteStopFile)); err != nil {
		t.Fatal("empty hold runtime stop was not retained")
	}
}
func TestCodexRemoteStopRequiresUIDsAndSeparateRuntime(t *testing.T) {
	for _, name := range []string{"Session UID", "pod UID", "runtime missing", "runtime in HOME", "runtime is HOME", "wrong provider", "task"} {
		t.Run(name, func(t *testing.T) {
			r := newRemoteStopRig(t)
			r.install(t)
			switch name {
			case "Session UID":
				r.sess.SessionUID = ""
			case "pod UID":
				r.s.PodUID = ""
			case "runtime missing":
				r.s.RuntimeDir = ""
			case "runtime in HOME":
				r.s.RuntimeDir = filepath.Join(r.s.Home, "runtime")
			case "runtime is HOME":
				r.s.RuntimeDir = r.s.Home
			case "wrong provider":
				r.sess.Agent = protocol.AgentClaude
			case "task":
				r.sess.Mode = protocol.ModeTask
			}
			st, unlock, err := stopCodexRemoteForRescue(context.Background(), r.s, r.sess, 0)
			if unlock != nil {
				unlock()
			}
			want := "requires the Session and pod UID"
			if strings.HasPrefix(name, "runtime") {
				want = "runtime directory outside HOME"
			}
			if err == nil || !strings.Contains(err.Error(), want) || st != nil || len(r.attempts) != 0 {
				t.Fatalf("unsafe stop admitted: %+v %v", st, err)
			}
		})
	}
}

func TestCodexRemoteStopRejectsMalformedProcIdentity(t *testing.T) {
	good := remoteStat(codexRemoteProcess{pid: 30, parent: 1, start: "3000", state: "S"})
	if proc, err := parseCodexRemoteProcess(good); err != nil || proc.pid != 30 || proc.start != "3000" {
		t.Fatalf("valid parenthesized command: %+v %v", proc, err)
	}
	for _, name := range []string{"no command", "short stat", "bad PID", "zero PID", "bad parent", "negative parent", "bad start", "zero start", "bad state"} {
		t.Run(name, func(t *testing.T) {
			raw := string(good)
			switch name {
			case "no command":
				raw = "30 S 1"
			case "short stat":
				raw = "30 (cmd) S 1"
			case "bad PID":
				raw = "x" + raw[2:]
			case "zero PID":
				raw = "0" + raw[2:]
			default:
				i := strings.LastIndexByte(raw, ')')
				fields := strings.Fields(raw[i+1:])
				switch name {
				case "bad parent":
					fields[1] = "x"
				case "negative parent":
					fields[1] = "-1"
				case "bad start":
					fields[19] = "x"
				case "zero start":
					fields[19] = "0"
				case "bad state":
					fields[0] = "SS"
				}
				raw = raw[:i+1] + " " + strings.Join(fields, " ")
			}
			if _, err := parseCodexRemoteProcess([]byte(raw)); err == nil {
				t.Fatal("malformed process identity accepted")
			}
		})
	}
	r := newRemoteStopRig(t)
	read := r.p.read
	r.p.read = func(path string) ([]byte, error) {
		if path == "/proc/30/stat" {
			return remoteStat(codexRemoteProcess{pid: 99, parent: 1, start: "3000", state: "S"}), nil
		}
		return read(path)
	}
	if _, err := r.p.process(30); err == nil {
		t.Fatal("another PID's stat accepted")
	}
}
func TestCodexRemoteResumeStartsOnlyDaemon(t *testing.T) {
	w := newFakeCodexWorld(t)
	s := w.s
	s.PodUID = "new-resume-pod"
	s.RuntimeDir = filepath.Join(t.TempDir(), "runtime")
	sess := codexRemoteSess
	sess.SessionUID = "retained-session"
	raw, _ := json.Marshal(sess)
	s.Getenv = envOf(map[string]string{protocol.SessionEnv: string(raw)})
	writeRemoteStop(t, s, codexRemoteStop{1, sess.SessionUID, "old-pod"})
	writeHostFixture(t, filepath.Join(s.CodexHome, "sessions", "rollout.jsonl"), "synthetic retained thread")
	writeJSON(t, s.statePath(launchFile), Launch{ConversationID: oldThread})
	deps := w.deps()
	deps.home = func(context.Context) error { return nil }
	old := codexRemoteDepsFor
	codexRemoteDepsFor = func() codexRemoteDeps { return deps }
	t.Cleanup(func() { codexRemoteDepsFor = old })
	d := &Daemon{S: s, Session: sess, Now: time.Now}
	step, why := d.startAgent(context.Background(), protocol.Workspace{}, Step{State: StepOK}, "resumed-boot")
	if step.State != StepOK || why != "" || w.starts() != 1 || len(w.calls) != 1 || w.calls[0] != "fake-codex remote-control start --json" {
		t.Fatalf("resume did more than daemon supervision: %+v %s calls=%v", step, why, w.calls)
	}
	if got, err := os.ReadFile(filepath.Join(s.CodexHome, "sessions", "rollout.jsonl")); err != nil || string(got) != "synthetic retained thread" {
		t.Fatal("resume changed native thread history")
	}
}

func TestCodexRemoteStopRejectsEphemeralStateDirRedirect(t *testing.T) {
	for _, name := range []string{"symlink", "alternate path", "public directory"} {
		t.Run(name, func(t *testing.T) {
			r := newRemoteStopRig(t)
			r.install(t)
			switch name {
			case "symlink":
				if err := os.RemoveAll(r.s.StateDir); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(r.s.RuntimeDir, r.s.StateDir); err != nil {
					t.Fatal(err)
				}
			case "alternate path":
				r.s.StateDir = r.s.RuntimeDir
			case "public directory":
				if err := os.Chmod(r.s.StateDir, 0755); err != nil {
					t.Fatal(err)
				}
			}
			if err := codexRemoteStopped(r.s); err == nil {
				t.Fatal("ephemeral or public HOME stop location accepted")
			}
			_, unlock, err := stopCodexRemoteForRescue(context.Background(), r.s, r.sess, 0)
			if unlock != nil {
				unlock()
			}
			if err == nil || len(r.signals) != 0 {
				t.Fatalf("unsafe durable marker admitted stop: %v %v", err, r.signals)
			}
		})
	}
}
func TestCodexRemoteStopCreatesPrivateStateForNonemptyHold(t *testing.T) {
	r := newRemoteStopRig(t)
	r.install(t)
	if err := os.RemoveAll(r.s.StateDir); err != nil {
		t.Fatal(err)
	}
	writeHostFixture(t, filepath.Join(r.s.Home, "synthetic-work"), "retained")
	r.init = "/usr/local/bin/tini\x00--\x00/usr/local/bin/agentd\x00hold\x00"
	r.cmdlines[10] = "/usr/local/bin/agentd\x00hold\x00"
	_, unlock, err := stopCodexRemoteForRescue(context.Background(), r.s, r.sess, 0)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	st, err := os.Stat(r.s.StateDir)
	if err != nil || st.Mode().Perm() != 0700 {
		t.Fatalf("nonprivate stop parent: %v %v", st, err)
	}
	if err := codexRemoteStopped(r.s); !errors.Is(err, errCodexRemoteStopping) {
		t.Fatalf("hold lost durable fence: %v", err)
	}
}
func TestCodexRemoteStopRefusesParentGoneDuringInventory(t *testing.T) {
	r := newRemoteStopRig(t)
	read := r.p.read
	once := true
	r.p.read = func(path string) ([]byte, error) {
		if path == "/proc/30/stat" && once {
			once = false
			delete(r.procs, 30)
			r.procs[44] = codexRemoteProcess{pid: 44, parent: 1, start: "4400", state: "S"}
			return nil, os.ErrNotExist
		}
		return read(path)
	}
	st, err := r.stop(context.Background(), 0)
	if err == nil || !st.Running {
		t.Fatalf("fork/exit census accepted: %+v %v procs=%v", st, err, r.procs)
	}
}
func TestCodexRemoteStopConfirmsDeadParentsBeforeGrace(t *testing.T) {
	r := newRemoteStopRig(t)
	for pid := range r.procs {
		if pid != 10 && pid != 30 {
			delete(r.procs, pid)
		}
	}
	read := r.p.read
	once := true
	r.p.read = func(path string) ([]byte, error) {
		if path == "/proc/30/stat" && once {
			once = false
			p := r.procs[30]
			p.state = "Z"
			r.procs[30] = p
			r.procs[44] = codexRemoteProcess{pid: 44, parent: 1, start: "4400", state: "S"}
		}
		return read(path)
	}
	st, err := r.stop(context.Background(), time.Second)
	if err != nil || st.Running || st.Killed || !r.quiesced() || !slices.Contains(r.signals, "44:15") {
		t.Fatalf("dead-parent child missed graceful freeze: %+v %v signals=%v", st, err, r.signals)
	}
}
func TestCodexRemoteStopRefusesLostFenceBeforeSnapshot(t *testing.T) {
	r := newRemoteStopRig(t)
	r.install(t)
	r.afterSignal = func(pid int, sig unix.Signal) {
		if pid == 43 && sig == unix.SIGCONT {
			_ = os.Remove(r.s.statePath(codexRemoteStopFile))
		}
	}
	rep, err := Rescue(context.Background(), &fakeRunner{handle: func(Cmd) (Result, error) { t.Fatal("snapshot after fence loss"); return Result{}, nil }}, r.s, r.sess.Name, rescueNow, RescueOptions{StopAgent: true})
	if err == nil || rep.OK {
		t.Fatalf("lost fence accepted: %+v %v", rep, err)
	}
}

func TestCodexRemoteStopDrainsOrphanGroupForkDuringKill(t *testing.T) {
	r := newRemoteStopRig(t)
	r.stubborn[30], r.stubborn[40] = true, true
	r.afterSignal = func(pid int, sig unix.Signal) {
		if pid == 30 && sig == unix.SIGKILL {
			// Killing a parent resumes a stopped orphaned job-control group.
			// Its child forks before the next queued pidfd KILL arrives.
			r.procs[44] = codexRemoteProcess{pid: 44, parent: 1, start: "4400", state: "S"}
		}
	}
	read := r.p.read
	once := true
	r.p.read = func(path string) ([]byte, error) {
		if path == "/proc/44/stat" && once {
			once = false
			proc := r.procs[44]
			proc.state = "Z"
			r.procs[44] = proc
			r.procs[45] = codexRemoteProcess{pid: 45, parent: 1, start: "4500", state: "S"}
		}
		return read(path)
	}
	st, err := r.stop(context.Background(), 0)
	if err != nil || st.Running || !st.Killed || !r.quiesced() || !slices.Contains(r.signals, "45:9") {
		t.Fatalf("orphan-group descendant survived forced drain: %+v %v signals=%v procs=%v", st, err, r.signals, r.procs)
	}
}

func TestCodexRemoteLifecycleRejectsUnsafeDirectory(t *testing.T) {
	for _, name := range []string{"relative", "public", "symlink"} {
		t.Run(name, func(t *testing.T) {
			r := newRemoteStopRig(t)
			switch name {
			case "relative":
				r.s.RuntimeDir = "."
			case "public":
				if err := os.Chmod(r.s.RuntimeDir, 0755); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				link := filepath.Join(t.TempDir(), "link")
				if err := os.Symlink(r.s.RuntimeDir, link); err != nil {
					t.Fatal(err)
				}
				r.s.RuntimeDir = link
			}
			if release, err := codexRemoteLifecycle(r.s, false); err == nil {
				release()
				t.Fatal("unsafe runtime admitted lifecycle mutation")
			}
		})
	}
}
func TestCodexRemoteStopInventoryCapsBeforeOpeningAnyWriter(t *testing.T) {
	r := newRemoteStopRig(t)
	for pid := 100; pid < 100+codexRemoteProcessCap; pid++ {
		r.procs[pid] = codexRemoteProcess{pid: pid, parent: 1, start: "1", state: "S"}
	}
	if _, err := r.p.inventory(r.procs[10]); err == nil {
		t.Fatal("unbounded process inventory")
	}
	r.init = "/usr/local/bin/tini\x00--\x00/usr/local/bin/agentd\x00hold\x00"
	if _, err := r.p.supervisor(r.s, r.sess); err == nil || !strings.Contains(err.Error(), "bound") {
		t.Fatalf("unbounded hold inventory: %v", err)
	}
}
func TestCodexRemoteStopSignalRefusalStopsImmediately(t *testing.T) {
	r := newRemoteStopRig(t)
	r.signalErr = unix.EPERM
	st, err := r.stop(context.Background(), 0)
	if err == nil || !strings.Contains(err.Error(), "could not signal") || !st.Running || !r.clock.Equal(rescueNow) {
		t.Fatalf("signal refusal continued drain: %+v %v clock=%s", st, err, r.clock)
	}
}

func TestCodexRemoteRescueRejectsForeignRetainedFenceBeforeMutation(t *testing.T) {
	r := newRemoteStopRig(t)
	r.install(t)
	writeRemoteStop(t, r.s, codexRemoteStop{1, "foreign-retained-session", "old-pod"})
	before, err := os.ReadFile(r.s.statePath(codexRemoteStopFile))
	if err != nil {
		t.Fatal(err)
	}
	rep, err := Rescue(context.Background(), &fakeRunner{}, r.s, r.sess.Name, rescueNow, RescueOptions{StopAgent: true})
	after, readErr := os.ReadFile(r.s.statePath(codexRemoteStopFile))
	if err == nil || !strings.Contains(err.Error(), "retained Session") || rep.OK || len(r.attempts) != 0 || readErr != nil || !bytes.Equal(before, after) {
		t.Fatalf("foreign retained HOME admitted a stop mutation: %+v %v attempts=%v", rep, err, r.attempts)
	}
}

func TestCodexRemoteRescueRefusesChangedEmptyHoldHome(t *testing.T) {
	r := newRemoteStopRig(t)
	r.install(t)
	if err := os.RemoveAll(r.s.StateDir); err != nil {
		t.Fatal(err)
	}
	r.init = "/usr/local/bin/tini\x00--\x00/usr/local/bin/agentd\x00hold\x00"
	r.cmdlines[10] = "/usr/local/bin/agentd\x00hold\x00"
	r.afterSignal = func(pid int, sig unix.Signal) {
		if pid == 10 && sig == unix.SIGSTOP {
			writeFile(t, filepath.Join(r.s.Home, "synthetic-unexpected-write"), "changed")
		}
	}
	rep, err := Rescue(context.Background(), &fakeRunner{}, r.s, r.sess.Name, rescueNow, RescueOptions{StopAgent: true})
	if err == nil || !strings.Contains(err.Error(), "HOME changed") || rep.OK {
		t.Fatalf("runtime-only stop accepted a changed HOME: %+v %v", rep, err)
	}
}

func TestCodexRemoteStopBoundsAccumulatedProcessIdentities(t *testing.T) {
	r := newRemoteStopRig(t)
	r.procs = map[int]codexRemoteProcess{10: r.procs[10]}
	for pid := 100; pid < 600; pid++ {
		r.procs[pid] = codexRemoteProcess{pid: pid, parent: 1, start: strconv.Itoa(pid * 100), state: "S"}
		r.stubborn[pid] = true
	}
	r.afterSignal = func(pid int, sig unix.Signal) {
		if pid == 100 && sig == unix.SIGKILL {
			// Neither inventory exceeds its cap; retained exact identities do.
			for child := 1000; child < 1500; child++ {
				r.procs[child] = codexRemoteProcess{pid: child, parent: 1, start: strconv.Itoa(child * 100), state: "S"}
			}
		}
	}
	st, err := r.stop(context.Background(), 0)
	if err == nil || !strings.Contains(err.Error(), "identity enumeration exceeds") || !st.Running || len(r.fds) != 0 {
		t.Fatalf("accumulated identities escaped their bound: %+v %v fds=%d", st, err, len(r.fds))
	}
}
