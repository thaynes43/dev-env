package agentd

import (
	"context"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// The default-off existing-host operation supplies accepted catalog/root and
// current pairing evidence, fences host/pod/process and durably consumes one
// exact intent. The task-budget native executor remains a separate computer.
// Connected status below proves transport identity, not phone pairing.
var codexRCTurnID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

type codexRCThreadSelection struct {
	InstallationID string
	EnvironmentID  string
	Cwd            string // exact accepted repository root on the selected host
	Model          string
	Effort         string
	Handoff        string
	BeforeTurn     func(context.Context) error
}

type codexRCThreadReceipt struct {
	ThreadID string
	TurnID   string
	Cwd      string
	Model    string // configured model, never evidence of served inference
}

func (s codexRCThreadSelection) valid() bool {
	if s.InstallationID == "" || len(s.InstallationID) > 256 || s.EnvironmentID == "" || len(s.EnvironmentID) > 256 || len(s.Cwd) > 4096 || !utf8.ValidString(s.Cwd) || !filepath.IsAbs(s.Cwd) || filepath.Clean(s.Cwd) != s.Cwd || s.Cwd == "/" || protocol.ValidateCodexModel(s.Model) != nil {
		return false
	}
	return !strings.ContainsFunc(s.InstallationID+s.EnvironmentID+s.Cwd, unicode.IsControl)
}

// startCodexRCThread talks only to the existing peer-verified daemon. It never
// starts a process, pairs a device, resumes a thread or changes
// permissions. An optional single initial handoff uses only the newly ACKed
// thread on this same connection. Errors after authorization remain unconfirmed
// and MUST NOT be
// retried: thread/start has no idempotency key in the pinned protocol.
func startCodexRCThread(ctx context.Context, settings Settings, expectedPID int, selection codexRCThreadSelection, authorizeOnce func(context.Context) error) (codexRCThreadReceipt, error) {
	if !selection.valid() || authorizeOnce == nil || ctx.Err() != nil || (selection.Handoff != "" && (selection.BeforeTurn == nil || protocol.ValidateCodexEffort(selection.Model, selection.Effort) != nil)) {
		return codexRCThreadReceipt{}, errCodexHostUnknown
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	r, err := openCodexHostRPC(ctx, settings, expectedPID)
	if err != nil {
		return codexRCThreadReceipt{}, errCodexHostUnknown
	}
	defer func() { _ = r.conn.Close() }()
	var initialized struct {
		UserAgent string `json:"userAgent"`
	}
	if r.call("initialize", map[string]any{"clientInfo": map[string]string{"name": "dev_env_rc_create", "version": managedCodexVersion}, "capabilities": map[string]bool{"experimentalApi": true}}, &initialized) != nil {
		return codexRCThreadReceipt{}, errCodexHostUnknown
	}
	_, tail, ok := strings.Cut(initialized.UserAgent, "/")
	fields := strings.Fields(tail)
	if !ok || len(fields) == 0 || fields[0] != managedCodexVersion || r.conn.WriteJSON(map[string]string{"method": "initialized"}) != nil {
		return codexRCThreadReceipt{}, errCodexHostUnknown
	}
	var remote struct {
		Status         string `json:"status"`
		InstallationID string `json:"installationId"`
		EnvironmentID  string `json:"environmentId"`
	}
	if r.call("remoteControl/status/read", struct{}{}, &remote) != nil || remote.Status != "connected" || remote.InstallationID != selection.InstallationID || remote.EnvironmentID != selection.EnvironmentID {
		return codexRCThreadReceipt{}, errCodexHostUnknown
	}
	if authorizeOnce(ctx) != nil || ctx.Err() != nil {
		return codexRCThreadReceipt{}, errCodexHostUnknown
	}
	var response struct {
		Thread struct {
			ID           string            `json:"id"`
			Environments []json.RawMessage `json:"environments"`
		} `json:"thread"`
		Cwd   string `json:"cwd"`
		Model string `json:"model"`
	}
	// cwd on this daemon is independent of the Mac connector project catalog.
	// Explicitly disable execution-environment selection: omission could choose
	// an unrelated default remote environment. The RC registration environment
	// checked above is not an execution-environment registry identity. Omit
	// projectId, provider fallback, config, sandbox and approval overrides.
	if r.call("thread/start", map[string]any{"cwd": selection.Cwd, "model": selection.Model, "environments": []any{}}, &response) != nil || !nativeThreadID.MatchString(response.Thread.ID) || response.Thread.Environments == nil || len(response.Thread.Environments) != 0 || response.Cwd != selection.Cwd || response.Model != selection.Model {
		return codexRCThreadReceipt{}, errCodexHostUnknown
	}
	receipt := codexRCThreadReceipt{ThreadID: response.Thread.ID, Cwd: response.Cwd, Model: response.Model}
	if selection.Handoff != "" {
		if selection.BeforeTurn(ctx) != nil || ctx.Err() != nil {
			return receipt, errCodexHostUnknown
		}
		var turn struct {
			Turn struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		if r.call("turn/start", map[string]any{"threadId": receipt.ThreadID, "model": selection.Model, "effort": selection.Effort, "input": []any{map[string]string{"type": "text", "text": selection.Handoff}}}, &turn) != nil || !codexRCTurnID.MatchString(turn.Turn.ID) {
			return receipt, errCodexHostUnknown
		}
		receipt.TurnID = turn.Turn.ID
	}
	return receipt, nil
}
