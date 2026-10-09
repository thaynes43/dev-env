package agentd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

type codexStream struct {
	thread   bool
	turn     bool
	terminal bool
	failed   bool
}

func codexJSONLine(data []byte, atEOF bool) (int, []byte, error) {
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF && len(data) > 0 {
		return 0, nil, io.ErrUnexpectedEOF
	}
	return 0, nil, nil
}

// The pinned exec protocol emits thread.started first. Only native stdout is
// parsed, so stderr cannot manufacture the durable resume identity.
func (s *codexStream) line(raw []byte, path string, l *Launch, out io.Writer, result *streamResult) error {
	var e struct {
		Type     string `json:"type"`
		ThreadID string `json:"thread_id"`
		Item     struct {
			Type             string `json:"type"`
			Text             string `json:"text"`
			AggregatedOutput string `json:"aggregated_output"`
		} `json:"item"`
		Usage *struct {
			Input      int64 `json:"input_tokens"`
			Output     int64 `json:"output_tokens"`
			Cached     int64 `json:"cached_input_tokens"`
			CacheWrite int64 `json:"cache_write_input_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(raw, &e) != nil || e.Type == "" {
		return errors.New("malformed native Codex JSONL")
	}
	if !s.thread && e.Type != "thread.started" {
		return errors.New("native Codex did not confirm a thread before turn output")
	}
	switch e.Type {
	case "thread.started":
		if s.thread {
			l.NativeThreadConfirmed = false
			_ = writeWorkspaceJSON(path, *l)
			return errors.New("native Codex emitted a second thread identity")
		}
		if err := persistCodexThread(path, l, e.ThreadID); err != nil {
			return err
		}
		s.thread = true
		_, _ = fmt.Fprintln(out, "[agentd] native Codex thread confirmed")
	case "turn.started":
		if s.turn || s.terminal {
			return errors.New("unexpected native Codex turn")
		}
		s.turn = true
	case "item.started", "item.updated", "item.completed":
		if !s.turn || s.terminal {
			return errors.New("native Codex item outside a turn")
		}
		if e.Type == "item.completed" {
			switch e.Item.Type {
			case "agent_message":
				_, _ = fmt.Fprintln(out, e.Item.Text)
			case "command_execution":
				_, _ = fmt.Fprintln(out, e.Item.AggregatedOutput)
			}
		}
	case "turn.completed":
		if !s.turn || s.terminal || e.Usage == nil || e.Usage.Input < 0 || e.Usage.Output < 0 || e.Usage.Cached < 0 || e.Usage.CacheWrite < 0 {
			return errors.New("invalid native Codex completion")
		}
		s.terminal = true
		*result = streamResult{Seen: true, Subtype: "success", NumTurns: 1, Usage: protocol.Usage{InputTokens: e.Usage.Input, OutputTokens: e.Usage.Output, CacheReadInputTokens: e.Usage.Cached, CacheCreationInputTokens: e.Usage.CacheWrite}}
	case "turn.failed", "error":
		s.terminal, s.failed = true, true
		*result = streamResult{Seen: true, Subtype: "provider_error", IsError: true}
		_, _ = fmt.Fprintln(out, "[agentd] native Codex turn failed")
	default:
		return errors.New("unknown pinned native Codex event")
	}
	return nil
}
