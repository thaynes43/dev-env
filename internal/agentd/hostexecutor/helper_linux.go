//go:build linux

package hostexecutor

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Helper runs only in a dedicated process reached through its parent's
// inherited private socket. Its child subreaper scope excludes other hosts.
func Helper() error {
	cred, err := unix.GetsockoptUcred(3, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil || int(cred.Pid) != os.Getppid() || cred.Uid != uint32(os.Getuid()) {
		return errors.New("missing private parent control socket")
	}
	self, err := os.Stat("/proc/self/exe")
	if err != nil {
		return err
	}
	parent, err := os.Stat(fmt.Sprintf("/proc/%d/exe", os.Getppid()))
	if err != nil || !os.SameFile(self, parent) {
		return errors.New("helper parent executable is not the owned supervisor")
	}
	f := os.NewFile(3, "owned-host-control")
	conn, err := net.FileConn(f)
	_ = f.Close()
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if err = conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	dec := json.NewDecoder(conn)
	var launch message
	if err = dec.Decode(&launch); err != nil {
		return err
	}
	if launch.Kind != "launch" || launch.Config == nil {
		return errors.New("missing immutable launch request")
	}
	c := *launch.Config
	if err = c.validate(); err != nil {
		return err
	}
	if err = conn.SetReadDeadline(time.Time{}); err != nil {
		return err
	}
	if err = unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		return err
	}
	var mode int32
	err = unix.Prctl(unix.PR_GET_CHILD_SUBREAPER, uintptr(unsafe.Pointer(&mode)), 0, 0, 0)
	if err != nil || mode != 1 {
		return errors.New("subreaper mode not confirmed")
	}
	deadline := time.NewTimer(time.Until(c.Binding.Deadline))
	defer deadline.Stop()
	cmd := exec.Command(c.NativeBinary, "app-server", "--remote-control", "--managed-daemon", "--listen", "unix://"+c.SocketPath)
	cmd.Env = append(os.Environ(), "HOME="+c.Home, "CODEX_HOME="+c.Home+"/.codex")
	cmd.Dir = c.Home
	if err = cmd.Start(); err != nil {
		return err
	}
	root, err := identity(cmd.Process.Pid)
	rootDone := make(chan error, 1)
	go func() { rootDone <- cmd.Wait() }()
	if err != nil {
		_ = cmd.Process.Kill()
		<-rootDone
		return err
	}
	send := func(m message) error {
		if e := conn.SetWriteDeadline(time.Now().Add(time.Second)); e != nil {
			return e
		}
		return json.NewEncoder(conn).Encode(m)
	}
	incoming := make(chan message, 2)
	go func() {
		defer close(incoming)
		for {
			var m message
			if dec.Decode(&m) != nil {
				return
			}
			incoming <- m
		}
	}()
	if err = send(message{Kind: "started", Root: root}); err != nil {
		return stopAndReport(c, root, rootDone, send)
	}
	confirm := time.NewTimer(5 * time.Second)
	defer confirm.Stop()
	select {
	case m, ok := <-incoming:
		if !ok || m.Kind != "confirm" {
			return stopAndReport(c, root, rootDone, send)
		}
	case <-confirm.C:
		return stopAndReport(c, root, rootDone, send)
	case <-deadline.C:
		return stopAndReport(c, root, rootDone, send)
	case <-rootDone:
		rootDone <- nil
		return stopAndReport(c, root, rootDone, send)
	}
	select {
	case <-incoming:
	case <-deadline.C:
	case e := <-rootDone:
		rootDone <- e
	}
	return stopAndReport(c, root, rootDone, send)
}

func stopAndReport(c Config, root Identity, done <-chan error, send func(message) error) error {
	err := stopTree(root, done, c.StopTimeout)
	m := message{Kind: "stopped", Root: root, RootWaitObserved: true, TreeReaped: true}
	if err != nil {
		m = message{Kind: "unknown", Root: root}
		_ = send(m)
		return err
	}
	return send(m)
}

func identity(pid int) (Identity, error) {
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return Identity{}, err
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return Identity{}, err
	}
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return Identity{}, errors.New("malformed process stat")
	}
	fields := strings.Fields(string(data)[end+1:])
	if len(fields) < 20 {
		return Identity{}, errors.New("short process stat")
	}
	start, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return Identity{}, err
	}
	return Identity{PID: pid, BootID: strings.TrimSpace(string(boot)), StartTicks: start}, nil
}

func same(a, b Identity) bool { return a == b }

type tracked struct {
	id Identity
	fd int
}

// stopTree freezes each process before enumerating all thread children, closes
// the fork race, then kills through pidfds. The final ECHILD proof is specific to
// this dedicated subreaper; a root PID or process group disappearing is not proof.
func stopTree(root Identity, rootDone <-chan error, budget time.Duration) error {
	until := time.Now().Add(budget)
	seen := map[int]tracked{}
	defer func() {
		for _, p := range seen {
			_ = unix.Close(p.fd)
		}
	}()
	var freeze func(int, int) error
	freeze = func(pid, parent int) error {
		if time.Now().After(until) {
			return errors.New("tree freeze deadline")
		}
		if _, ok := seen[pid]; ok {
			return nil
		}
		if len(seen) >= 128 {
			return errors.New("owned tree exceeds process cap")
		}
		before, err := identity(pid)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		ppid, err := parentPID(pid)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if ppid != parent && ppid != os.Getpid() {
			return errors.New("child ancestry changed")
		}
		if pid == root.PID && !same(before, root) {
			return errors.New("root process identity changed")
		}
		fd, err := unix.PidfdOpen(pid, 0)
		if errors.Is(err, unix.ESRCH) {
			return nil
		}
		if err != nil {
			return err
		}
		after, err := identity(pid)
		if err != nil {
			_ = unix.Close(fd)
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if !same(before, after) {
			_ = unix.Close(fd)
			return errors.New("process identity changed while opening pidfd")
		}
		ppid, err = parentPID(pid)
		if err != nil || ppid != parent && ppid != os.Getpid() {
			_ = unix.Close(fd)
			return errors.New("child ancestry changed while opening pidfd")
		}
		seen[pid] = tracked{before, fd}
		if err = unix.PidfdSendSignal(fd, unix.SIGSTOP, nil, 0); err != nil && !errors.Is(err, unix.ESRCH) {
			return err
		}
		for {
			stopped, e := threadsStopped(pid, until)
			if errors.Is(e, os.ErrNotExist) {
				break
			}
			if e != nil {
				return e
			}
			if stopped {
				break
			}
			if time.Now().After(until) {
				return errors.New("process did not freeze")
			}
			time.Sleep(5 * time.Millisecond)
		}
		children, err := childPIDs(pid, until)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		for _, child := range children {
			if err = freeze(child, pid); err != nil {
				return err
			}
		}
		return nil
	}
	// Root can have exited already; adopted descendants are enumerated from the
	// helper too, including children created by any native worker thread.
	err := freeze(root.PID, os.Getpid())
	if err == nil {
		var children []int
		children, err = childPIDs(os.Getpid(), until)
		if err == nil {
			for _, p := range children {
				if err = freeze(p, os.Getpid()); err != nil {
					break
				}
			}
		}
	}
	// Even on uncertain freeze, terminate each identity already owned. Never
	// turn that best effort into a Stopped receipt.
	for _, p := range seen {
		e := unix.PidfdSendSignal(p.fd, unix.SIGKILL, nil, 0)
		if e != nil && !errors.Is(e, unix.ESRCH) && err == nil {
			err = e
		}
	}
	if err != nil {
		return err
	}
	select {
	case <-rootDone:
	case <-time.After(time.Until(until)):
		return errors.New("native Wait not observed")
	}
	for {
		var status unix.WaitStatus
		pid, e := unix.Wait4(-1, &status, unix.WNOHANG, nil)
		if errors.Is(e, unix.ECHILD) {
			return nil
		}
		if e != nil {
			return e
		}
		if time.Now().After(until) {
			return errors.New("adopted descendants not reaped")
		}
		if pid == 0 {
			time.Sleep(5 * time.Millisecond)
		}
	}
}

func childPIDs(pid int, until time.Time) ([]int, error) {
	tasks, err := os.ReadDir(fmt.Sprintf("/proc/%d/task", pid))
	if err != nil {
		return nil, err
	}
	if len(tasks) > 1024 {
		return nil, errors.New("owned thread cap exceeded")
	}
	seen := map[int]bool{}
	out := []int{}
	for _, task := range tasks {
		if time.Now().After(until) {
			return nil, errors.New("child enumeration deadline")
		}
		data, e := os.ReadFile(fmt.Sprintf("/proc/%d/task/%s/children", pid, task.Name()))
		if errors.Is(e, os.ErrNotExist) {
			continue
		}
		if e != nil {
			return nil, e
		}
		if len(data) > 16384 {
			return nil, errors.New("child list exceeds cap")
		}
		for _, s := range strings.Fields(string(data)) {
			n, e := strconv.Atoi(s)
			if e != nil {
				return nil, e
			}
			if !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	return out, nil
}

func parentPID(pid int) (int, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return 0, errors.New("malformed process stat")
	}
	fields := strings.Fields(string(data)[end+1:])
	if len(fields) < 2 {
		return 0, errors.New("missing parent identity")
	}
	return strconv.Atoi(fields[1])
}

func threadsStopped(pid int, until time.Time) (bool, error) {
	tasks, err := os.ReadDir(fmt.Sprintf("/proc/%d/task", pid))
	if err != nil {
		return false, err
	}
	if len(tasks) > 1024 {
		return false, errors.New("owned thread cap exceeded")
	}
	for _, task := range tasks {
		if time.Now().After(until) {
			return false, errors.New("thread freeze deadline")
		}
		data, e := os.ReadFile(fmt.Sprintf("/proc/%d/task/%s/stat", pid, task.Name()))
		if errors.Is(e, os.ErrNotExist) {
			continue
		}
		if e != nil {
			return false, e
		}
		end := strings.LastIndexByte(string(data), ')')
		if end < 0 {
			return false, errors.New("malformed thread stat")
		}
		fields := strings.Fields(string(data)[end+1:])
		if len(fields) < 1 {
			return false, errors.New("missing thread state")
		}
		if fields[0] != "T" && fields[0] != "t" && fields[0] != "Z" && fields[0] != "X" {
			return false, nil
		}
	}
	return true, nil
}
