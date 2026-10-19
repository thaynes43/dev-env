//go:build linux

package hostexecutor

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

const NativeControlLimit = 256 << 10

var ErrNativeControl = errors.New("native owner request unconfirmed; do not replay mutations")

// ControlNative connects to the original native process, not an on-disk PID or
// history store. The caller must first validate original guard evidence and
// off-pod authority. It never launches/resumes a guard, pairs, or adds budget.
// Results are user content, not evidence that can admit/continue a campaign.
func ControlNative(ctx context.Context, socket string, p Observation, req apiv1.NativeControlRequest, consume func(context.Context) error) (json.RawMessage, error) {
	if !req.Valid() || req.Operation == "host/stop" || p.Stage != "Ready" || p.NativeVersion != PinnedNativeVersion || !sameBinding(p.Binding, Binding{TaskUID: req.Binding.TaskUID, HostID: req.Binding.HostID, PodUID: req.Binding.PodUID, Epoch: req.Binding.Epoch, Deadline: req.Binding.Deadline}) || !rootLive(p.Root) {
		return nil, ErrNativeControl
	}
	deadline, ok := ctx.Deadline()
	if !ok || !req.Binding.Deadline.After(time.Now()) {
		return nil, ErrNativeControl
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, ErrNativeControl
	}
	defer func() { _ = conn.Close() }()
	if !ownedSocketPeer(conn, p.Root) {
		return nil, ErrNativeControl
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	dialer := websocket.Dialer{HandshakeTimeout: time.Until(deadline), NetDialContext: func(context.Context, string, string) (net.Conn, error) { return conn, nil }}
	ws, response, err := dialer.DialContext(ctx, "ws://localhost/", nil)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		return nil, ErrNativeControl
	}
	defer func() { _ = ws.Close() }()
	ws.SetReadLimit(NativeControlLimit)
	if ws.SetReadDeadline(deadline) != nil || ws.SetWriteDeadline(deadline) != nil {
		return nil, ErrNativeControl
	}
	call := func(id int, method string, params any) (json.RawMessage, error) {
		if !rootLive(p.Root) || ws.WriteJSON(map[string]any{"id": id, "method": method, "params": params}) != nil {
			return nil, ErrNativeControl
		}
		for i := 0; i < 64; i++ {
			var envelope struct {
				ID     *int            `json:"id"`
				Method string          `json:"method"`
				Result json.RawMessage `json:"result"`
				Error  json.RawMessage `json:"error"`
			}
			if ws.ReadJSON(&envelope) != nil {
				return nil, ErrNativeControl
			}
			// Notifications may interleave. Server requests need an explicit
			// interactive owner and are not silently approved by this adapter.
			if envelope.Method != "" {
				if envelope.ID != nil {
					return nil, ErrNativeControl
				}
				continue
			}
			if envelope.ID == nil || *envelope.ID != id || len(envelope.Error) > 0 && string(envelope.Error) != "null" || len(envelope.Result) == 0 || string(envelope.Result) == "null" || !rootLive(p.Root) {
				return nil, ErrNativeControl
			}
			return envelope.Result, nil
		}
		return nil, ErrNativeControl
	}
	init, err := call(1, "initialize", map[string]any{"clientInfo": map[string]string{"name": "dev_env_owned_owner", "version": PinnedNativeVersion}, "capabilities": map[string]bool{"experimentalApi": true}})
	var initialized struct {
		UserAgent string `json:"userAgent"`
	}
	if err != nil || json.Unmarshal(init, &initialized) != nil {
		return nil, ErrNativeControl
	}
	_, tail, ok := strings.Cut(initialized.UserAgent, "/")
	fields := strings.Fields(tail)
	if !ok || len(fields) == 0 || fields[0] != PinnedNativeVersion || ws.WriteJSON(map[string]string{"method": "initialized"}) != nil {
		return nil, ErrNativeControl
	}
	params := map[string]any{}
	if req.ThreadID != "" {
		params["threadId"] = req.ThreadID
	}
	switch req.Operation {
	case "thread/start", "thread/resume":
		params["model"] = "gpt-6-luna"
	case "thread/read":
		params["includeTurns"] = true
	case "turn/start":
		params["model"] = "gpt-6-luna"
		params["input"] = []any{map[string]string{"type": "text", "text": req.Text}}
	case "turn/interrupt":
		params["turnId"] = req.TurnID
	}
	// Final fresh off-pod CAS consumption follows passive initialize and
	// immediately precedes the mutation. No nil callback grants authority.
	if req.Operation != "thread/read" && (consume == nil || consume(ctx) != nil) {
		return nil, ErrNativeControl
	}
	if ctx.Err() != nil || !req.Binding.Deadline.After(time.Now()) {
		return nil, ErrNativeControl
	}
	result, err := call(2, req.Operation, params)
	if err != nil {
		return nil, err
	}
	// Validate native identity in acknowledgements; turn acceptance is NOT
	// completed inference, and interrupt acceptance is NOT whole-tree stop.
	var shape struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if json.Unmarshal(result, &shape) != nil {
		return nil, ErrNativeControl
	}
	if strings.HasPrefix(req.Operation, "thread/") && (shape.Thread.ID == "" || req.ThreadID != "" && shape.Thread.ID != req.ThreadID) {
		return nil, ErrNativeControl
	}
	if req.Operation == "turn/start" && shape.Turn.ID == "" {
		return nil, ErrNativeControl
	}
	return result, nil
}
