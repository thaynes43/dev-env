//go:build linux

package hostexecutor

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// AllowanceOwner holds a direct helper/child pair, never an adopted daemon.
type AllowanceOwner struct {
	conn net.Conn
	cmd  *exec.Cmd
	done chan error
	root Identity
}
type allowanceOwnerMessage struct {
	Kind string   `json:"kind"`
	Home string   `json:"home,omitempty"`
	Root Identity `json:"root,omitempty"`
}

func StartAllowanceOwner(ctx context.Context, home string) (*AllowanceOwner, error) {
	if !filepath.IsAbs(home) {
		return nil, errNativeReady
	}
	helper, err := os.Executable()
	if err != nil {
		return nil, errNativeReady
	}
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, errNativeReady
	}
	parent := os.NewFile(uintptr(pair[0]), "allowance-parent")
	child := os.NewFile(uintptr(pair[1]), "allowance-helper")
	conn, err := net.FileConn(parent)
	_ = parent.Close()
	if err != nil {
		_ = child.Close()
		return nil, errNativeReady
	}
	cmd := exec.Command(helper, "owned-allowance-helper")
	image, err := os.Open("/proc/self/exe")
	if err != nil {
		_ = child.Close()
		_ = conn.Close()
		return nil, errNativeReady
	}
	defer func() { _ = image.Close() }()
	cmd.ExtraFiles = []*os.File{child, image}
	if err = cmd.Start(); err != nil {
		_ = child.Close()
		_ = conn.Close()
		return nil, errNativeReady
	}
	_ = child.Close()
	o := &AllowanceOwner{conn: conn, cmd: cmd, done: make(chan error, 1)}
	go func() { o.done <- cmd.Wait() }()
	if err = conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		_ = conn.Close()
		return nil, errNativeReady
	}
	if err = json.NewEncoder(conn).Encode(allowanceOwnerMessage{Kind: "launch", Home: home}); err != nil {
		_ = conn.Close()
		return nil, errNativeReady
	}
	var msg allowanceOwnerMessage
	if err = json.NewDecoder(conn).Decode(&msg); err != nil || msg.Kind != "started" || msg.Root.PID <= 0 || !rootLive(msg.Root) {
		_ = conn.Close()
		return nil, errNativeReady
	}
	o.root = msg.Root
	if err = conn.SetDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		return nil, errNativeReady
	}
	select {
	case <-ctx.Done():
		_ = o.Stop()
		return nil, errNativeReady
	default:
	}
	return o, nil
}
func (o *AllowanceOwner) PID() int { return o.root.PID }
func allowanceMatchesExecutable(pid int, path string) bool {
	process, err := os.Stat(fmt.Sprintf("/proc/%d", pid))
	if err != nil {
		return false
	}
	owner, ok := process.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Geteuid()) {
		return false
	}
	expected, err := os.Stat(path)
	if err != nil || !expected.Mode().IsRegular() {
		return false
	}
	actual, err := os.Stat(fmt.Sprintf("/proc/%d/exe", pid))
	return err == nil && os.SameFile(expected, actual)
}
func (o *AllowanceOwner) Alive() bool {
	if o == nil || !rootLive(o.root) || !allowanceMatchesExecutable(o.root.PID, "/usr/local/bin/codex") {
		return false
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", o.root.PID))
	if err != nil {
		return false
	}
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return false
	}
	fields := strings.Fields(string(data)[end+1:])
	if len(fields) < 2 {
		return false
	}
	parent, err := strconv.Atoi(fields[1])
	return err == nil && parent == o.cmd.Process.Pid && rootLive(o.root)
}
func (o *AllowanceOwner) Stop() error {
	if o == nil {
		return errNativeReady
	}
	defer func() { _ = o.conn.Close() }()
	if o.conn.SetDeadline(time.Now().Add(5*time.Second)) != nil {
		return errNativeReady
	}
	if json.NewEncoder(o.conn).Encode(allowanceOwnerMessage{Kind: "stop"}) != nil {
		return errNativeReady
	}
	var result allowanceOwnerMessage
	if json.NewDecoder(o.conn).Decode(&result) != nil || result.Kind != "stopped" {
		return errNativeReady
	}
	select {
	case err := <-o.done:
		if err != nil {
			return errNativeReady
		}
		return nil
	case <-time.After(5 * time.Second):
		return errNativeReady
	}
}

// AllowanceHelper is a dedicated child subreaper with authenticated inherited control.
func AllowanceHelper() error {
	cred, err := unix.GetsockoptUcred(3, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil || int(cred.Pid) != os.Getppid() || cred.Uid != uint32(os.Getuid()) {
		return errNativeReady
	}
	self, err := os.Stat("/proc/self/exe")
	if err != nil {
		return errNativeReady
	}
	parent, err := os.Stat(fmt.Sprintf("/proc/%d/exe", os.Getppid()))
	if err != nil && os.Getppid() == 1 {
		image := os.NewFile(4, "allowance-parent-image")
		parent, err = image.Stat()
		_ = image.Close()
	}
	if err != nil || !os.SameFile(self, parent) {
		return errNativeReady
	}
	f := os.NewFile(3, "allowance-control")
	conn, err := net.FileConn(f)
	_ = f.Close()
	if err != nil {
		return errNativeReady
	}
	defer func() { _ = conn.Close() }()
	if conn.SetDeadline(time.Now().Add(5*time.Second)) != nil {
		return errNativeReady
	}
	var req allowanceOwnerMessage
	if json.NewDecoder(conn).Decode(&req) != nil || req.Kind != "launch" || !filepath.IsAbs(req.Home) {
		return errNativeReady
	}
	home, err := os.Lstat(req.Home)
	if err != nil || !home.IsDir() || home.Mode().Perm() != 0700 {
		return errNativeReady
	}
	if unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0) != nil {
		return errNativeReady
	}
	var subreaper int32
	if unix.Prctl(unix.PR_GET_CHILD_SUBREAPER, uintptr(unsafe.Pointer(&subreaper)), 0, 0, 0) != nil || subreaper != 1 {
		return errNativeReady
	}
	if ProtectEvidenceProcess() != nil {
		return errNativeReady
	}
	socket := filepath.Join(req.Home, ".codex", "app-server-control", "app-server-control.sock")
	cmd := nativeCommand(Config{NativeBinary: "/usr/local/bin/codex", Home: req.Home, SocketPath: socket})
	return serveAllowanceChild(conn, cmd, "/usr/local/bin/codex")
}

func serveAllowanceChild(conn net.Conn, cmd *exec.Cmd, expectedBinary string) error {
	if cmd.Start() != nil {
		return errNativeReady
	}
	root, err := identity(cmd.Process.Pid)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return errNativeReady
	}
	// No Wait runs before the parent's stop; the child PID remains reserved.
	cleanup := func() error {
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		return stopTree(root, done, 3*time.Second)
	}
	if !rootLive(root) || !allowanceMatchesExecutable(root.PID, expectedBinary) {
		_ = cleanup()
		return errNativeReady
	}
	if json.NewEncoder(conn).Encode(allowanceOwnerMessage{Kind: "started", Root: root}) != nil {
		_ = cleanup()
		return errNativeReady
	}
	// Give the owned operation its own bounded lifetime after the handshake.
	// These are safety bounds, not measured production service objectives.
	if conn.SetDeadline(time.Now().Add(20*time.Second)) != nil {
		_ = cleanup()
		return errNativeReady
	}
	var stop allowanceOwnerMessage
	err = json.NewDecoder(conn).Decode(&stop)
	cleanupErr := cleanup()
	if err != nil || stop.Kind != "stop" || cleanupErr != nil {
		return errNativeReady
	}
	// Cleanup must not spend the acknowledgement's write budget.
	if conn.SetWriteDeadline(time.Now().Add(5*time.Second)) != nil {
		return errNativeReady
	}
	return json.NewEncoder(conn).Encode(allowanceOwnerMessage{Kind: "stopped"})
}
