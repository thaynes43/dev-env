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
	// bound is the largest accepted frame; zero is codexHostRPCBound.
	bound int
}

// widen raises the frame bound for a read-only observer whose answers carry
// thread lists (DESIGN-057). Mutating callers keep the default.
func (r *codexHostRPC) widen(n int) {
	r.bound = n
	r.conn.SetReadLimit(int64(n))
}

func (r *codexHostRPC) frameBound() int {
	if r.bound > 0 {
		return r.bound
	}
	return codexHostRPCBound
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

// codexRPCFault is one JSON-RPC error object from the host daemon.
type codexRPCFault struct {
	Code    int64           `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// configWriteCode is data.config_write_error_code, or "" when the fault is
// not a config write rejection.
func (f *codexRPCFault) configWriteCode() string {
	if f == nil || len(f.Data) == 0 {
		return ""
	}
	var body struct {
		Code string `json:"config_write_error_code"`
	}
	if json.Unmarshal(f.Data, &body) != nil {
		return ""
	}
	return body.Code
}

func (r *codexHostRPC) call(method string, params any, result any) error {
	raw, fault, _, err := r.callOutcome(method, params)
	if err != nil || fault != nil || len(raw) == 0 || string(raw) == "null" || json.Unmarshal(raw, result) != nil {
		return errCodexHostUnknown
	}
	return nil
}

// callOutcome writes one request and returns its result, or the server's
// error object. sent is true once the request frame was written. err is a
// transport or protocol failure, including a failure while reading the answer
// after a successful write.
func (r *codexHostRPC) callOutcome(method string, params any) (json.RawMessage, *codexRPCFault, bool, error) {
	r.next++
	if r.conn.WriteJSON(map[string]any{"id": r.next, "method": method, "params": params}) != nil {
		return nil, nil, false, errCodexHostUnknown
	}
	for message := 0; message < 32; message++ {
		kind, data, err := r.conn.ReadMessage()
		if err != nil || kind != websocket.TextMessage || len(data) > r.frameBound() {
			return nil, nil, true, errCodexHostUnknown
		}
		var envelope struct {
			ID     *int            `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if json.Unmarshal(data, &envelope) != nil {
			return nil, nil, true, errCodexHostUnknown
		}
		if envelope.Method != "" {
			// A server request needs an interactive owner. Never discard it as
			// though it were a notification while waiting for a mutation ACK.
			if envelope.ID != nil {
				return nil, nil, true, errCodexHostUnknown
			}
			if strings.HasPrefix(envelope.Method, "thread/") || strings.HasPrefix(envelope.Method, "turn/") {
				r.churn = true
			}
			continue
		}
		if envelope.ID == nil || *envelope.ID != r.next {
			return nil, nil, true, errCodexHostUnknown
		}
		if len(envelope.Error) > 0 && string(envelope.Error) != "null" {
			var fault codexRPCFault
			if json.Unmarshal(envelope.Error, &fault) != nil || fault.Message == "" {
				return nil, nil, true, errCodexHostUnknown
			}
			return nil, &fault, true, nil
		}
		if len(envelope.Result) == 0 || string(envelope.Result) == "null" {
			return nil, nil, true, errCodexHostUnknown
		}
		return envelope.Result, nil, true, nil
	}
	return nil, nil, true, errCodexHostUnknown
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
