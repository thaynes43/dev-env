package agentd

import (
	"context"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// This metadata checks the supported exec route only. A workbench process can
// forge its environment; construction and API authorization remain the trust root.
func codexCoordinatorEntry(s Settings, sess protocol.Session, req CodexThreadRequest) bool {
	if req.ExpectedSessionUID != "" || req.ExpectedPodUID != "" {
		if req.ExpectedSessionUID == "" || req.ExpectedPodUID == "" || req.ExpectedSessionUID != sess.SessionUID || req.ExpectedPodUID != s.PodUID {
			return false
		}
	}
	privileged := s.WorkbenchPod || s.CodexCoordinatorHost != "" || s.CodexCoordinatorUID != ""
	if !privileged {
		return !req.Coordinator
	}
	return req.Coordinator && s.WorkbenchPod &&
		s.CodexCoordinatorHost == sess.Name && s.CodexCoordinatorUID != "" && s.CodexCoordinatorUID == sess.SessionUID &&
		req.Host == sess.Name && req.ExpectedSessionUID == sess.SessionUID &&
		req.ExpectedPodUID != "" && req.ExpectedPodUID == s.PodUID &&
		req.Model == "gpt-6.1-sol" && req.Effort == "medium"
}

const codexCoordinatorTitle = "GPT | Dev-env coordinator 2"

// setCodexCoordinatorTitle uses the peer-verified, version-pinned daemon client.
// Reads and writes metadata only: no resume, turn or shell/TUI rename.
func setCodexCoordinatorTitle(ctx context.Context, s Settings, pid int, id, cwd string) error {
	if !nativeThreadID.MatchString(id) || s.CodexCoordinatorHost == "" || s.CodexCoordinatorUID == "" || s.PodUID == "" {
		return errCodexHostUnknown
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	r, err := openCodexThreadObserver(ctx, s, pid)
	if err != nil {
		return err
	}
	defer func() { _ = r.conn.Close() }()
	r.widen(codexHostRPCBound)
	read := func() (string, error) {
		var result struct {
			Thread struct {
				ID   string  `json:"id"`
				Cwd  string  `json:"cwd"`
				Name *string `json:"name"`
			} `json:"thread"`
		}
		if r.call("thread/read", map[string]any{"threadId": id, "includeTurns": false}, &result) != nil || result.Thread.ID != id || result.Thread.Cwd != cwd {
			return "", errCodexHostUnknown
		}
		if result.Thread.Name == nil {
			return "", nil
		}
		return *result.Thread.Name, nil
	}
	if _, err := read(); err != nil {
		return err
	}
	var ack struct{}
	if r.call("thread/name/set", map[string]string{"threadId": id, "name": codexCoordinatorTitle}, &ack) != nil {
		return errCodexHostUnknown
	}
	name, err := read()
	if err != nil || name != codexCoordinatorTitle {
		return errCodexHostUnknown
	}
	return nil
}
