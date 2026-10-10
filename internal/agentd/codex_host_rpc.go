package agentd

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/gorilla/websocket"
)

const codexHostRPCBound = 64 << 10

type codexHostRPC struct {
	conn  *websocket.Conn
	next  int
	churn bool
}
type codexHostRemote struct {
	Version    string
	Connection string
	Idle       bool
	Loaded     int
}

func openCodexHostRPC(ctx context.Context, s Settings, expectedPID int) (*codexHostRPC, error) {
	bound := ctx
	dialer := websocket.Dialer{HandshakeTimeout: 2 * time.Second, NetDialContext: func(c context.Context, _, _ string) (net.Conn, error) {
		conn, err := (&net.Dialer{}).DialContext(c, "unix", filepath.Join(s.CodexHome, "app-server-control", "app-server-control.sock"))
		if err != nil {
			return nil, err
		}
		uds, ok := conn.(*net.UnixConn)
		if !ok || expectedPID <= 0 {
			_ = conn.Close()
			return nil, errCodexHostUnknown
		}
		raw, err := uds.SyscallConn()
		if err != nil {
			_ = conn.Close()
			return nil, errCodexHostUnknown
		}
		valid := false
		controlErr := raw.Control(func(fd uintptr) {
			cred, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
			valid = err == nil && int(cred.Pid) == expectedPID && int(cred.Uid) == os.Geteuid()
		})
		if controlErr != nil || !valid {
			_ = conn.Close()
			return nil, errCodexHostUnknown
		}
		return conn, nil
	}}
	conn, response, err := dialer.DialContext(bound, (&url.URL{Scheme: "ws", Host: "localhost", Path: "/"}).String(), http.Header{})
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		return nil, errCodexHostUnknown
	}
	conn.SetReadLimit(codexHostRPCBound)
	deadline := time.Now().Add(5 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetReadDeadline(deadline)
	_ = conn.SetWriteDeadline(deadline)
	rpc := &codexHostRPC{conn: conn}
	// Cancellation closes only our passive connection; it does not stop the host.
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()
	return rpc, nil
}
func (r *codexHostRPC) call(method string, params any, result any) error {
	r.next++
	if r.conn.WriteJSON(map[string]any{"id": r.next, "method": method, "params": params}) != nil {
		return errCodexHostUnknown
	}
	for message := 0; message < 32; message++ {
		kind, data, err := r.conn.ReadMessage()
		if err != nil || kind != websocket.TextMessage || len(data) > codexHostRPCBound {
			return errCodexHostUnknown
		}
		var envelope struct {
			ID     *int            `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if json.Unmarshal(data, &envelope) != nil {
			return errCodexHostUnknown
		}
		if envelope.Method != "" {
			if strings.HasPrefix(envelope.Method, "thread/") || strings.HasPrefix(envelope.Method, "turn/") {
				r.churn = true
			}
			continue
		}
		if envelope.ID == nil || *envelope.ID != r.next || len(envelope.Error) > 0 || len(envelope.Result) == 0 {
			return errCodexHostUnknown
		}
		if json.Unmarshal(envelope.Result, result) != nil {
			return errCodexHostUnknown
		}
		return nil
	}
	return errCodexHostUnknown
}
func (r *codexHostRPC) loaded() ([]string, error) {
	cursor := ""
	seen := map[string]bool{}
	ids := []string{}
	for page := 0; page < 4; page++ {
		var response struct {
			Data *[]string `json:"data"`
			Next *string   `json:"nextCursor"`
		}
		params := map[string]any{"limit": 32}
		if cursor != "" {
			params["cursor"] = cursor
		}
		if r.call("thread/loaded/list", params, &response) != nil || response.Data == nil || len(*response.Data) > 32 {
			return nil, errCodexHostUnknown
		}
		for _, id := range *response.Data {
			if !nativeThreadID.MatchString(id) || seen[id] {
				return nil, errCodexHostUnknown
			}
			seen[id] = true
			ids = append(ids, id)
		}
		if response.Next == nil {
			slices.Sort(ids)
			return ids, nil
		}
		if *response.Next == "" || *response.Next == cursor {
			return nil, errCodexHostUnknown
		}
		cursor = *response.Next
	}
	return nil, errCodexHostUnknown
}
func probeCodexHostRemote(ctx context.Context, s Settings, idle bool, expectedPID int) (codexHostRemote, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	r, err := openCodexHostRPC(ctx, s, expectedPID)
	if err != nil {
		return codexHostRemote{}, err
	}
	defer func() { _ = r.conn.Close() }()
	var initialize struct {
		UserAgent string `json:"userAgent"`
	}
	if r.call("initialize", map[string]any{"clientInfo": map[string]string{"name": "dev_env_host_observer", "version": managedCodexVersion}, "capabilities": map[string]bool{"experimentalApi": true}}, &initialize) != nil {
		return codexHostRemote{}, errCodexHostUnknown
	}
	_, tail, ok := strings.Cut(initialize.UserAgent, "/")
	fields := strings.Fields(tail)
	if !ok || len(fields) == 0 || fields[0] != managedCodexVersion {
		return codexHostRemote{}, errCodexHostUnknown
	}
	if r.conn.WriteJSON(map[string]string{"method": "initialized"}) != nil {
		return codexHostRemote{}, errCodexHostUnknown
	}
	var status struct {
		Status string `json:"status"`
	}
	if r.call("remoteControl/status/read", map[string]any{}, &status) != nil {
		return codexHostRemote{}, errCodexHostUnknown
	}
	switch status.Status {
	case "connected", "connecting", "disabled", "errored":
	default:
		return codexHostRemote{}, errCodexHostUnknown
	}
	result := codexHostRemote{Version: fields[0], Connection: status.Status}
	if !idle {
		return result, nil
	}
	first, err := r.loaded()
	if err != nil {
		return codexHostRemote{}, err
	}
	result.Loaded = len(first)
	for _, id := range first {
		var response struct {
			Thread struct {
				ID     string `json:"id"`
				Status struct {
					Type string `json:"type"`
				} `json:"status"`
			} `json:"thread"`
		}
		if r.call("thread/read", map[string]any{"threadId": id, "includeTurns": false}, &response) != nil || response.Thread.ID != id {
			return codexHostRemote{}, errCodexHostUnknown
		}
		if response.Thread.Status.Type != "idle" {
			return result, nil
		}
	}
	second, err := r.loaded()
	if err != nil || r.churn || !slices.Equal(first, second) {
		return codexHostRemote{}, errCodexHostUnknown
	}
	result.Idle = true
	return result, nil
}
