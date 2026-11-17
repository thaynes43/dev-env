package agentd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// A remote host is a dedicated container, not an owned subreaper tree. Native
// daemon children and tmux panes can detach and be adopted by tini. Enumerating
// that verified PID namespace avoids guessing executable names, environments,
// process groups, pane names, or a daemon's current list of conversations.
// Never use this drain on a task, a shared PID namespace, or a host namespace.
const codexRemoteProcessCap = 512

type codexRemoteProcess struct {
	pid, parent int
	hold        bool
	start       string
	state       string
}

type codexRemoteProcesses struct {
	self    int
	read    func(string) ([]byte, error)
	list    func(string) ([]os.DirEntry, error)
	sameExe func(int) bool
	initExe func() bool
	open    func(int) (int, error)
	exited  func(int) (bool, error)
	signal  func(int, unix.Signal) error
	close   func(int) error
	now     func() time.Time
	pause   func(context.Context) error
}

var codexRemoteProcessesFor = realCodexRemoteProcesses

func realCodexRemoteProcesses() codexRemoteProcesses {
	return codexRemoteProcesses{
		self: os.Getpid(),
		initExe: func() bool {
			a, e1 := os.Stat("/proc/1/exe")
			b, e2 := os.Stat("/usr/local/bin/tini")
			return e1 == nil && e2 == nil && os.SameFile(a, b)
		},
		read: func(path string) ([]byte, error) { return boundedHostFile(path, 16<<10, false) },
		list: os.ReadDir,
		sameExe: func(pid int) bool {
			a, aerr := os.Stat("/proc/self/exe")
			b, berr := os.Stat(fmt.Sprintf("/proc/%d/exe", pid))
			return aerr == nil && berr == nil && os.SameFile(a, b)
		},
		open:   func(pid int) (int, error) { return unix.PidfdOpen(pid, 0) },
		exited: func(fd int) (bool, error) { return codexRemotePidfdExited(fd, unix.Poll) },
		signal: func(fd int, sig unix.Signal) error {
			return unix.PidfdSendSignal(fd, sig, nil, 0)
		},
		close: unix.Close,
		now:   time.Now,
		pause: func(ctx context.Context) error {
			t := time.NewTimer(50 * time.Millisecond)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		},
	}
}

// A flags=0 pidfd reports whole-thread-group exit, not merely leader exit:
// https://github.com/torvalds/linux/blob/v6.12/fs/pidfs.c#L86-L107
// /proc task samples cannot prove absence: an exiting worker may create an
// unlisted replacement before it is sampled as X.
func codexRemotePidfdExited(fd int, poll func([]unix.PollFd, int) (int, error)) (bool, error) {
	fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	_, err := poll(fds, 0)
	if err != nil || fds[0].Revents&(unix.POLLERR|unix.POLLNVAL) != 0 {
		return false, errors.New("codex stop cannot prove whole-group exit; rescue blocked")
	}
	return fds[0].Revents&(unix.POLLIN|unix.POLLHUP) != 0, nil
}

func parseCodexRemoteProcess(raw []byte) (codexRemoteProcess, error) {
	i, j := strings.IndexByte(string(raw), '('), strings.LastIndexByte(string(raw), ')')
	if i < 1 || j <= i {
		return codexRemoteProcess{}, errors.New("unreadable process identity")
	}
	f := strings.Fields(string(raw[j+1:]))
	if len(f) < 20 {
		return codexRemoteProcess{}, errors.New("incomplete process identity")
	}
	pid, e1 := strconv.Atoi(strings.TrimSpace(string(raw[:i])))
	parent, e2 := strconv.Atoi(f[1])
	start, e3 := strconv.ParseUint(f[19], 10, 64)
	if e1 != nil || e2 != nil || e3 != nil || pid < 1 || parent < 0 || start == 0 || len(f[0]) != 1 {
		return codexRemoteProcess{}, errors.New("invalid process identity")
	}
	return codexRemoteProcess{pid: pid, parent: parent, start: f[19], state: f[0]}, nil
}

func (p codexRemoteProcesses) process(pid int) (codexRemoteProcess, error) {
	raw, err := p.read(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return codexRemoteProcess{}, err
	}
	proc, err := parseCodexRemoteProcess(raw)
	if err != nil || proc.pid != pid {
		return codexRemoteProcess{}, errors.New("process identity did not match")
	}
	return proc, nil
}

func sameCodexRemoteProcess(a, b codexRemoteProcess) bool {
	return a.pid == b.pid && a.start == b.start
}

func remoteProcessGone(p codexRemoteProcess) bool { return p.state == "Z" || p.state == "X" }

// Check every native thread, not just the group leader: SIGSTOP delivery and
// actual scheduling into the stopped state are separate events.
func (p codexRemoteProcesses) frozen(proc codexRemoteProcess) (bool, error) {
	return p.threadStates(proc, true)
}

func (p codexRemoteProcesses) threadStates(proc codexRemoteProcess, stopped bool) (bool, error) {
	cur, err := p.process(proc.pid)
	if err != nil {
		return false, err
	}
	if !sameCodexRemoteProcess(proc, cur) {
		return false, errors.New("process changed during stop")
	}
	tasks, err := p.list(fmt.Sprintf("/proc/%d/task", proc.pid))
	if err != nil || len(tasks) == 0 || len(tasks) > 1024 {
		return false, errors.New("cannot enumerate every process thread")
	}
	for _, task := range tasks {
		raw, err := p.read(fmt.Sprintf("/proc/%d/task/%s/stat", proc.pid, task.Name()))
		if err != nil {
			return false, errors.New("cannot read every process thread")
		}
		t, err := parseCodexRemoteProcess(raw)
		if err != nil {
			return false, err
		}
		tid, parseErr := strconv.Atoi(task.Name())
		if parseErr != nil || t.pid != tid {
			return false, errors.New("thread identity did not match")
		}
		if !remoteProcessGone(t) && (!stopped || t.state != "T" && t.state != "t") {
			return false, nil
		}
	}
	cur, err = p.process(proc.pid)
	if err != nil || !sameCodexRemoteProcess(proc, cur) {
		return false, errors.New("process changed during thread inspection")
	}
	return true, nil
}

func (p codexRemoteProcesses) supervisor(s Settings, sess protocol.Session) (codexRemoteProcess, error) {
	// The image starts exactly tini -- agentd [run|hold]. An immutable image
	// executable plus its argv proves the dedicated namespace shape. A shared
	// namespace's pause or a host's init refuses before any signal.
	init, err := p.read("/proc/1/cmdline")
	argv := strings.Split(strings.TrimSuffix(string(init), "\x00"), "\x00")
	if err != nil || len(argv) < 3 || len(argv) > 4 || argv[0] != "/usr/local/bin/tini" || argv[1] != "--" || argv[2] != "/usr/local/bin/agentd" || len(argv) == 4 && argv[3] != "run" && argv[3] != "hold" || !p.initExe() || p.self <= 1 {
		return codexRemoteProcess{}, errors.New("codex stop requires the dedicated tini/agentd PID namespace")
	}
	if len(argv) == 4 && argv[3] == "hold" {
		// A D-55 hold has no boot writer and retains the old executor's boot.json.
		// Discover only the immutable agentd hold which tini actually supervises;
		// never mistake a stale executor PID for the current hold process.
		entries, err := p.list("/proc")
		if err != nil {
			return codexRemoteProcess{}, errors.New("cannot inspect the Codex rescue hold")
		}
		var hold codexRemoteProcess
		count := 0
		for _, entry := range entries {
			pid, err := strconv.Atoi(entry.Name())
			if err != nil {
				continue
			}
			count++
			if count > codexRemoteProcessCap {
				return hold, errors.New("codex hold process enumeration exceeds its bound")
			}
			if pid <= 1 || pid == p.self {
				continue
			}
			proc, err := p.process(pid)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return hold, errors.New("cannot inspect every Codex hold process")
			}
			if proc.parent != 1 || remoteProcessGone(proc) || !p.sameExe(pid) {
				continue
			}
			raw, err := p.read(fmt.Sprintf("/proc/%d/cmdline", pid))
			if err != nil || string(raw) != "/usr/local/bin/agentd\x00hold\x00" {
				continue
			}
			if hold.pid != 0 {
				return hold, errors.New("codex hold supervisor is ambiguous")
			}
			hold = proc
		}
		if hold.pid == 0 {
			return hold, errors.New("codex hold supervisor is unproven")
		}
		hold.hold = true
		return hold, nil
	}
	var boot bootRecord
	if readJSONFile(s.statePath(bootFile), &boot) != nil || boot.Daemon == nil {
		return codexRemoteProcess{}, errors.New("codex stop cannot identify this pod's supervisor")
	}
	d := boot.Daemon
	if d.SessionUID != sess.SessionUID || d.PodUID != s.PodUID || d.Pid <= 1 || d.Pid == p.self || d.Start == "" {
		return codexRemoteProcess{}, errors.New("codex stop supervisor binding does not match this Session and pod")
	}
	proc, err := p.process(d.Pid)
	if err != nil || proc.start != d.Start || proc.parent != 1 || remoteProcessGone(proc) || !p.sameExe(d.Pid) {
		return codexRemoteProcess{}, errors.New("codex stop supervisor process identity is unproven")
	}
	return proc, nil
}

// Carry the vanished PID privately so only this stop's already-signalled
// processes can authorize a bounded re-list. The diagnostic exposes no PID.
type codexRemoteVanished struct {
	pid   int
	start string // When known, require this exact start; inventory ENOENT has only PID.
}

func (*codexRemoteVanished) Error() string {
	return "codex process disappeared during stop; rescue blocked"
}

func (p codexRemoteProcesses) inventory(supervisor codexRemoteProcess, allowVanished bool) ([]codexRemoteProcess, error) {
	entries, err := p.list("/proc")
	if err != nil {
		return nil, errors.New("codex stop cannot enumerate the PID namespace")
	}
	var out []codexRemoteProcess
	count := 0
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		count++
		if count > codexRemoteProcessCap {
			return nil, errors.New("codex stop process enumeration exceeds its bound")
		}
		if pid == 1 || pid == p.self || pid == supervisor.pid {
			continue
		}
		proc, err := p.process(pid)
		if errors.Is(err, os.ErrNotExist) {
			if allowVanished {
				continue // Grace only: this is a wait, not the final absence proof.
			}
			// Stat has vanished, so its start time is unavailable here and
			// re-list matches a retained identity by PID alone. This cannot hide
			// a writer: the final inventory must still see every live process.
			return nil, &codexRemoteVanished{pid: pid}
		}
		if err != nil {
			return nil, errors.New("codex stop cannot inspect every process")
		}
		out = append(out, proc)
	}
	return out, nil
}

func quiesceCodexRemote(ctx context.Context, s Settings, sess protocol.Session, grace time.Duration, p codexRemoteProcesses) (*protocol.AgentStop, error) {
	st := &protocol.AgentStop{Running: true}
	supervisor, err := p.supervisor(s, sess)
	if err != nil {
		return st, err
	}
	// One total bound, including freeze, graceful drain, forced drain and reads.
	grace = max(0, min(grace, 30*time.Second))
	until := p.now().Add(grace + 10*time.Second)
	check := func() error {
		if ctx.Err() != nil || !p.now().Before(until) {
			return errors.New("codex writers did not quiesce within the stop budget; rescue blocked")
		}
		return nil
	}
	type processKey struct {
		pid   int
		start string
	}
	key := func(proc codexRemoteProcess) processKey { return processKey{proc.pid, proc.start} }
	fds := map[processKey]int{}
	signalled := map[int]bool{}
	defer func() {
		for _, fd := range fds {
			_ = p.close(fd)
		}
	}()
	signal := func(fd int, sig unix.Signal) error {
		if err := p.signal(fd, sig); err != nil && !errors.Is(err, unix.ESRCH) {
			return errors.New("codex stop could not signal an exact process; rescue blocked")
		}
		signalled[fd] = true
		return nil
	}
	// Re-list at most three times in this call, and only after signalling that
	// PID through a retained exact pidfd whose whole-group exit is now proven.
	// An unsignalled exit, a mismatching known start, or uncertain readiness refuses.
	relists := 0
	relist := func(err error) (bool, error) {
		var vanished *codexRemoteVanished
		if !errors.As(err, &vanished) || relists >= 3 {
			return false, err
		}
		for identity, fd := range fds {
			if identity.pid != vanished.pid || vanished.start != "" && identity.start != vanished.start || !signalled[fd] {
				continue
			}
			dead, exitErr := p.exited(fd)
			if exitErr != nil {
				return false, exitErr
			}
			if dead {
				relists++
				if err := check(); err != nil {
					return false, err
				}
				if err := p.pause(ctx); err != nil {
					return false, err
				}
				return true, nil
			}
		}
		return false, err
	}
	acquire := func(proc codexRemoteProcess) (int, error) {
		if err := check(); err != nil {
			return -1, err
		}
		fd, held := fds[key(proc)]
		var err error
		if !held {
			if len(fds) >= codexRemoteProcessCap {
				return -1, errors.New("codex stop identity enumeration exceeds its bound")
			}
			fd, err = p.open(proc.pid)
		}
		if errors.Is(err, unix.ESRCH) {
			return -1, &codexRemoteVanished{pid: proc.pid, start: proc.start}
		}
		if err != nil {
			return -1, errors.New("codex stop requires exact pidfd process control")
		}
		cur, err := p.process(proc.pid)
		if err != nil || !sameCodexRemoteProcess(proc, cur) {
			if !held {
				_ = p.close(fd)
			}
			if errors.Is(err, os.ErrNotExist) {
				return -1, &codexRemoteVanished{pid: proc.pid, start: proc.start}
			}
			return -1, errors.New("codex stop process identity changed before signalling")
		}
		fds[key(proc)] = fd
		return fd, nil
	}
	freeze := func(proc codexRemoteProcess) error {
		fd, err := acquire(proc)
		if err != nil {
			return err
		}
		if err := signal(fd, unix.SIGSTOP); err != nil {
			return err
		}
		for {
			if err := check(); err != nil {
				return err
			}
			ok, err := p.frozen(proc)
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			if ok {
				return nil
			}
			if err := p.pause(ctx); err != nil {
				return err
			}
		}
	}
	if err := freeze(supervisor); err != nil {
		return st, err
	}
	// Never thaw the supervisor, on success or failure. A stopped process is
	// kept by the pod until its authorized delete; closure of a pidfd is no thaw.
	frozenSupervisor := func() error {
		ok, err := p.frozen(supervisor)
		if err != nil || !ok {
			return errors.New("codex supervisor is not quiescent; rescue blocked")
		}
		return check()
	}
	freezeAll := func() ([]codexRemoteProcess, error) {
		settled := map[processKey]bool{}
	scan:
		for {
			if err := frozenSupervisor(); err != nil {
				return nil, err
			}
			inventory, err := p.inventory(supervisor, false)
			if err != nil {
				if retry, err := relist(err); retry {
					continue scan
				} else {
					return nil, err
				}
			}
			all := true
			var writers []codexRemoteProcess
			for _, proc := range inventory {
				if err := check(); err != nil {
					return nil, err
				}
				// Even a dead group may have forked just before this listing. A
				// confirmatory pass is required after every new identity is quiescent.
				if !settled[key(proc)] {
					all = false
				}
				fd, err := acquire(proc)
				if err != nil {
					if retry, err := relist(err); retry {
						continue scan
					} else {
						return nil, err
					}
				}
				dead, err := p.exited(fd)
				if err != nil {
					return nil, err
				}
				if dead {
					settled[key(proc)] = true
					continue
				}
				ok, err := p.frozen(proc)
				if err != nil {
					if errors.Is(err, os.ErrNotExist) {
						err = &codexRemoteVanished{pid: proc.pid, start: proc.start}
					}
					if retry, err := relist(err); retry {
						continue scan
					} else {
						return nil, err
					}
				}
				if !ok || !settled[key(proc)] {
					all = false
					if err := freeze(proc); err != nil {
						if retry, err := relist(err); retry {
							continue scan
						} else {
							return nil, err
						}
					}
				}
				settled[key(proc)] = true
				writers = append(writers, proc)
			}
			if all {
				return writers, nil
			}
			if err := p.pause(ctx); err != nil {
				return nil, err
			}
		}
	}

	writers, err := freezeAll()
	if err != nil {
		return st, err
	}
	st.WasRunning = len(writers) > 0
	for _, proc := range writers {
		if err := signal(fds[key(proc)], unix.SIGTERM); err != nil {
			return st, err
		}
		if err := signal(fds[key(proc)], unix.SIGCONT); err != nil {
			return st, err
		}
	}
	graceUntil := p.now().Add(grace)
	for p.now().Before(graceUntil) {
		if err := check(); err != nil {
			return st, err
		}
		allDead := true
		for _, proc := range writers {
			if err := check(); err != nil {
				return st, err
			}
			dead, err := p.exited(fds[key(proc)])
			if err != nil {
				return st, err
			}
			allDead = allDead && dead
		}
		if allDead {
			// A terminating writer may have forked a flush helper during grace.
			// Do not freeze it or end its remaining grace just because the old
			// list has exited: check a fresh inventory for another live writer.
			inventory, err := p.inventory(supervisor, true)
			if err != nil {
				if retry, err := relist(err); retry {
					continue
				} else {
					return st, err
				}
			}
			writers = nil
			for _, proc := range inventory {
				fd, err := acquire(proc)
				if err != nil {
					var vanished *codexRemoteVanished
					if errors.As(err, &vanished) {
						continue // Grace only; freezeAll still requires strict evidence.
					}
					return st, err
				}
				dead, err := p.exited(fd)
				if err != nil {
					return st, err
				}
				if !dead {
					writers = append(writers, proc)
				}
			}
			if len(writers) == 0 {
				break
			}
		}
		if err := p.pause(ctx); err != nil {
			return st, err
		}
	}
	// Leaving grace early is only a wait optimization. The fresh confirmed
	// inventory below remains mandatory after the last signal and before rescue.
	// Grace can fork detached children. Killing a parent can also cause the
	// kernel to continue an orphaned job-control group, which may fork before
	// its own KILL arrives. Confirm a frozen inventory before every kill round
	// and again before success, including newly dead parents in that proof.
	for {
		writers, err = freezeAll()
		if err != nil {
			return st, err
		}
		if len(writers) == 0 {
			st.Running = false
			return st, nil
		}
		st.Killed = true
		for _, proc := range writers {
			if err := signal(fds[key(proc)], unix.SIGKILL); err != nil {
				return st, err
			}
		}
		if err := p.pause(ctx); err != nil {
			return st, err
		}
	}
}
