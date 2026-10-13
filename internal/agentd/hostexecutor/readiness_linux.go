//go:build linux

package hostexecutor

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/sys/unix"
)

const PinnedNativeVersion = "0.160.1"

var errNativeReady = errors.New("owned native readiness unknown")

type nativeReadinessStage string
type nativeReadinessObserverKey struct{}

// A same-package synthetic test may install a bounded stage channel. Production
// callers install no observer. Delivery is nonblocking and never carries runtime
// data or changes the exact sentinel returned by a refusal.
func nativeReadinessRefused(ctx context.Context, stage nativeReadinessStage) error {
	if stages, ok := ctx.Value(nativeReadinessObserverKey{}).(chan nativeReadinessStage); ok {
		select {
		case stages <- stage:
		default:
		}
	}
	return errNativeReady
}

// probeNative uses the pinned daemon's Unix WebSocket initialize handshake.
// Socket existence is not readiness. Only absent/refused startup connects are
// retried within the caller's two-second deadline; RPCs are never replayed.
func probeNative(ctx context.Context, path string, root Identity, version string) error {
	if version != PinnedNativeVersion {
		return nativeReadinessRefused(ctx, "version-not-pinned")
	}
	if !rootLive(root) {
		return nativeReadinessRefused(ctx, "root-not-live-before-connect")
	}
	var conn net.Conn
	for attempts := 0; attempts < 40; attempts++ {
		var err error
		conn, err = (&net.Dialer{}).DialContext(ctx, "unix", path)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, unix.ECONNREFUSED) {
			return nativeReadinessRefused(ctx, "socket-connect-refused")
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nativeReadinessRefused(ctx, "connect-context-ended")
		case <-timer.C:
		}
	}
	if conn == nil {
		return nativeReadinessRefused(ctx, "connect-attempts-exhausted")
	}
	defer func() { _ = conn.Close() }()
	stopCancel := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopCancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		return nativeReadinessRefused(ctx, "missing-deadline")
	}
	if conn.SetDeadline(deadline) != nil {
		return nativeReadinessRefused(ctx, "socket-deadline-refused")
	}
	if !ownedSocketPeer(conn, root) {
		return nativeReadinessRefused(ctx, "peer-identity-refused")
	}
	dialer := websocket.Dialer{HandshakeTimeout: time.Until(deadline), NetDialContext: func(context.Context, string, string) (net.Conn, error) { return conn, nil }}
	ws, response, err := dialer.DialContext(ctx, "ws://localhost/", nil)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		return nativeReadinessRefused(ctx, "websocket-handshake-refused")
	}
	defer func() { _ = ws.Close() }()
	ws.SetReadLimit(16 << 10)
	if ws.SetReadDeadline(deadline) != nil || ws.SetWriteDeadline(deadline) != nil {
		return nativeReadinessRefused(ctx, "websocket-deadline-refused")
	}
	request := map[string]any{"id": 1, "method": "initialize", "params": map[string]any{"clientInfo": map[string]string{"name": "dev_env_owned_host", "version": version}}}
	if ws.WriteJSON(request) != nil {
		return nativeReadinessRefused(ctx, "initialize-write-refused")
	}
	var reply struct {
		ID     int `json:"id"`
		Result *struct {
			UserAgent string `json:"userAgent"`
		} `json:"result"`
		Error json.RawMessage `json:"error"`
	}
	if ws.ReadJSON(&reply) != nil {
		return nativeReadinessRefused(ctx, "initialize-read-refused")
	}
	if reply.ID != 1 || reply.Result == nil || len(reply.Error) != 0 && string(reply.Error) != "null" {
		return nativeReadinessRefused(ctx, "initialize-shape-refused")
	}
	_, tail, found := strings.Cut(reply.Result.UserAgent, "/")
	fields := strings.Fields(tail)
	if !found || len(fields) == 0 || fields[0] != version {
		return nativeReadinessRefused(ctx, "version-reply-refused")
	}
	if !rootLive(root) {
		return nativeReadinessRefused(ctx, "root-not-live-after-reply")
	}
	if ws.WriteJSON(map[string]any{"method": "initialized"}) != nil {
		return nativeReadinessRefused(ctx, "initialized-write-refused")
	}
	if !rootLive(root) {
		return nativeReadinessRefused(ctx, "root-not-live-after-notification")
	}
	return nil
}

func rootLive(root Identity) bool {
	current, err := identity(root.PID)
	if err != nil || !same(current, root) {
		return false
	}
	fd, err := unix.PidfdOpen(root.PID, 0)
	if err != nil {
		return false
	}
	defer func() { _ = unix.Close(fd) }()
	current, err = identity(root.PID)
	if err != nil || !same(current, root) {
		return false
	}
	status := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	n, err := unix.Poll(status, 0)
	return err == nil && n == 0
}

func ownedSocketPeer(conn net.Conn, root Identity) bool {
	u, ok := conn.(*net.UnixConn)
	if !ok || !rootLive(root) {
		return false
	}
	raw, err := u.SyscallConn()
	if err != nil {
		return false
	}
	var cred *unix.Ucred
	var socketErr error
	if raw.Control(func(fd uintptr) { cred, socketErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) }) != nil {
		return false
	}
	return socketErr == nil && cred != nil && int(cred.Pid) == root.PID && cred.Uid == uint32(os.Getuid()) && rootLive(root)
}
