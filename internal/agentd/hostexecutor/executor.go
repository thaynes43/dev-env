// Package hostexecutor owns one foreground native computer for one immutable
// budget binding. It does not implement budget authority or remote pairing.
package hostexecutor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

// Binding is deliberately independent of API packages. Adapters must validate
// the exact immutable binding against off-pod authority, including its epoch.
type Binding struct {
	TaskUID  string    `json:"taskUID"`
	Epoch    uint64    `json:"epoch"`
	Deadline time.Time `json:"deadline"`
	HostID   string    `json:"hostID"`
	PodUID   string    `json:"podUID"`
}

type Gate interface {
	Admit(context.Context, Binding) error
	Observe(context.Context, Binding) (latched bool, err error)
}

type Config struct {
	Binding      Binding
	HelperBinary string
	NativeBinary string
	Home         string
	SocketPath   string
	ReceiptPath  string
	StopTimeout  time.Duration
}

type Identity struct {
	PID        int    `json:"pid"`
	BootID     string `json:"bootID"`
	StartTicks uint64 `json:"startTicks"`
}

// Confirmed means the helper owns the foreground root and received a durable
// acknowledgement. It does not assert RPC readiness, pairing, enrollment or
// conversation recovery; those require separate native acceptance.
type Receipt struct {
	Binding          Binding   `json:"binding"`
	State            string    `json:"state"`
	Updated          time.Time `json:"updated"`
	Root             Identity  `json:"root"`
	RootWaitObserved bool      `json:"rootWaitObserved"`
	TreeReaped       bool      `json:"treeReaped"`
	Reason           string    `json:"reason,omitempty"`
}

type message struct {
	Kind             string   `json:"kind"`
	Config           *Config  `json:"config,omitempty"`
	Root             Identity `json:"root,omitempty"`
	RootWaitObserved bool     `json:"rootWaitObserved,omitempty"`
	TreeReaped       bool     `json:"treeReaped,omitempty"`
	Reason           string   `json:"reason,omitempty"`
}

var ErrDisabled = errors.New("owned executor disabled: no budget authority adapter")
var ErrNeedsReview = errors.New("owned executor receipt already exists: explicit review required")

func (c Config) validate() error {
	if c.Binding.TaskUID == "" || c.Binding.Epoch == 0 || c.Binding.HostID == "" || c.Binding.PodUID == "" || !c.Binding.Deadline.After(time.Now()) {
		return errors.New("invalid immutable budget binding")
	}
	for _, p := range []string{c.HelperBinary, c.NativeBinary, c.Home, c.SocketPath, c.ReceiptPath} {
		if !filepath.IsAbs(p) {
			return errors.New("executor paths must be absolute")
		}
	}
	home, err := os.Lstat(c.Home)
	if err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(c.Home)
	if err != nil {
		return err
	}
	if resolved != filepath.Clean(c.Home) {
		return errors.New("private home must not contain symlink aliases")
	}
	if !home.IsDir() || home.Mode().Perm()&0o077 != 0 {
		return errors.New("native home must be a private directory")
	}
	if c.StopTimeout < time.Millisecond || c.StopTimeout > 5*time.Second {
		return errors.New("stop timeout must be positive and at most five seconds")
	}
	// The socket is confined to the private home. No listener is opened to the network.
	rel, err := filepath.Rel(c.Home, c.SocketPath)
	if err != nil || rel == ".." || len(rel) > 3 && rel[:3] == "../" {
		return errors.New("native socket must be inside private home")
	}
	return nil
}

// Run admits once, then refuses continuation on deadline, latch or authority
// error. Each receipt path is one-use, including after a successful stop. There
// is no automatic rearm, enrollment reset or workspace mutation.
func Run(ctx context.Context, c Config, gate Gate) (Receipt, error) {
	receipt := Receipt{Binding: c.Binding, State: "Attempting", Updated: time.Now().UTC()}
	if gate == nil {
		return Receipt{}, ErrDisabled
	}
	if err := c.validate(); err != nil {
		return Receipt{}, err
	}
	fd, err := unix.Open(c.ReceiptPath+".lock", unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return Receipt{}, err
	}
	lock := os.NewFile(uintptr(fd), "owned-host-lock")
	defer func() { _ = lock.Close() }()
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return Receipt{}, ErrNeedsReview
	}
	if _, err = os.Lstat(c.ReceiptPath); err == nil {
		return Receipt{}, ErrNeedsReview
	} else if !errors.Is(err, os.ErrNotExist) {
		return Receipt{}, err
	}
	admitCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	err = admit(admitCtx, gate, c.Binding)
	cancel()
	if err != nil {
		return Receipt{}, fmt.Errorf("budget admission: %w", err)
	}
	if err = save(c.ReceiptPath, receipt); err != nil {
		return receipt, err
	}
	sockets, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return receipt, err
	}
	parentFile := os.NewFile(uintptr(sockets[0]), "owned-host-parent")
	childFile := os.NewFile(uintptr(sockets[1]), "owned-host-helper")
	conn, err := net.FileConn(parentFile)
	_ = parentFile.Close()
	if err != nil {
		_ = childFile.Close()
		return receipt, err
	}
	defer func() { _ = conn.Close() }()
	cmd := exec.Command(c.HelperBinary, "owned-codex-host-helper")
	cmd.ExtraFiles = []*os.File{childFile}
	// Native output may contain credentials or pairing material. It is never
	// copied to this receipt, the API, stderr or a public log.
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err = cmd.Start(); err != nil {
		_ = childFile.Close()
		return receipt, err
	}
	_ = childFile.Close()
	helperDone := make(chan error, 1)
	go func() { helperDone <- cmd.Wait() }()
	events := make(chan message, 4)
	go func() {
		defer close(events)
		dec := json.NewDecoder(conn)
		for {
			var m message
			if dec.Decode(&m) != nil {
				return
			}
			events <- m
		}
	}()
	send := func(m message) error {
		if e := conn.SetWriteDeadline(time.Now().Add(time.Second)); e != nil {
			return e
		}
		return json.NewEncoder(conn).Encode(m)
	}
	err = send(message{Kind: "launch", Config: &c})
	reason := "parent control failure"
	if err == nil {
		select {
		case m, ok := <-events:
			if ok && m.Kind == "started" && m.Root.PID > 0 && m.Root.StartTicks > 0 && m.Root.BootID != "" {
				receipt.State = "Confirmed"
				receipt.Root = m.Root
				receipt.Updated = time.Now().UTC()
				err = save(c.ReceiptPath, receipt)
				if err == nil {
					err = send(message{Kind: "confirm"})
				}
			} else {
				err = errors.New("helper did not confirm owned launch")
			}
		case <-ctx.Done():
			err = ctx.Err()
		case <-time.After(5 * time.Second):
			err = errors.New("owned launch confirmation timed out")
		}
	}
	if err == nil {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		deadline := time.NewTimer(time.Until(c.Binding.Deadline))
		defer deadline.Stop()
	monitor:
		for {
			select {
			case <-ctx.Done():
				reason = "supervisor canceled"
				break monitor
			case <-deadline.C:
				reason = "budget deadline"
				break monitor
			case m, ok := <-events:
				if ok && (m.Kind == "stopped" || m.Kind == "unknown") {
					receipt.State = "Stopping"
					receipt.Reason = "native exit or helper backstop"
					receipt.Updated = time.Now().UTC()
					if e := save(c.ReceiptPath, receipt); e != nil {
						return receipt, e
					}
					return finish(c, receipt, m, helperDone)
				}
				reason = "helper control lost"
				break monitor
			case <-ticker.C:
				observeCtx, stop := context.WithTimeout(ctx, time.Second)
				latched, e := observe(observeCtx, gate, c.Binding)
				stop()
				if e != nil {
					reason = "budget authority unavailable"
					break monitor
				}
				if latched {
					reason = "budget latched"
					break monitor
				}
			}
		}
	}
	receipt.State = "Stopping"
	receipt.Reason = reason
	receipt.Updated = time.Now().UTC()
	if e := save(c.ReceiptPath, receipt); e != nil {
		_ = conn.Close()
		return receipt, e
	}
	_ = send(message{Kind: "stop"})
	timeout := time.NewTimer(c.StopTimeout + 2*time.Second)
	defer timeout.Stop()
	for {
		select {
		case m, ok := <-events:
			if !ok {
				return receipt, errors.New("owned process tree stop unknown: control closed")
			}
			if m.Kind == "stopped" || m.Kind == "unknown" {
				return finish(c, receipt, m, helperDone)
			}
		case <-timeout.C:
			return receipt, errors.New("owned process tree stop unknown: helper deadline")
		}
	}
}

func finish(c Config, r Receipt, m message, done <-chan error) (Receipt, error) {
	if m.Kind != "stopped" || !m.RootWaitObserved || !m.TreeReaped || !same(m.Root, r.Root) {
		return r, errors.New("owned process tree stop unknown")
	}
	select {
	case e := <-done:
		if e != nil {
			return r, fmt.Errorf("helper exit unknown: %w", e)
		}
	case <-time.After(time.Second):
		return r, errors.New("helper exit not observed")
	}
	r.State = "Stopped"
	r.RootWaitObserved = true
	r.TreeReaped = true
	r.Updated = time.Now().UTC()
	if err := save(c.ReceiptPath, r); err != nil {
		return r, err
	}
	return r, nil
}

func save(path string, r Receipt) error {
	// Write/fsync/rename/fsync protects the interruption checkpoint. Receipt
	// content is metadata only; enrollment, home and shared WIP are untouched.
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".owned-receipt-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer func() { _ = os.Remove(name) }()
	if err = f.Chmod(0o600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}

// An adapter that fails to honor cancellation cannot delay the independent
// helper deadline or obtain admission after this caller's timeout.
func admit(ctx context.Context, g Gate, b Binding) error {
	ch := make(chan error, 1)
	go func() { ch <- g.Admit(ctx, b) }()
	select {
	case err := <-ch:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
func observe(ctx context.Context, g Gate, b Binding) (bool, error) {
	type result struct {
		latched bool
		err     error
	}
	ch := make(chan result, 1)
	go func() { v, e := g.Observe(ctx, b); ch <- result{v, e} }()
	select {
	case r := <-ch:
		return r.latched, r.err
	case <-ctx.Done():
		return false, ctx.Err()
	}
}
