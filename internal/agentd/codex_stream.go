package agentd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

type codexStream struct {
	capture  *managedCapture
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
	if !utf8.Valid(raw) {
		if s.capture != nil {
			s.capture.unknown = true
		}
		return errors.New("native Codex JSONL is not UTF-8")
	}
	var e struct {
		Type     string `json:"type"`
		ThreadID string `json:"thread_id"`
		Item     struct {
			Type             string `json:"type"`
			Text             string `json:"text"`
			AggregatedOutput string `json:"aggregated_output"`
		} `json:"item"`
		Usage *struct {
			Input      json.RawMessage `json:"input_tokens"`
			Output     json.RawMessage `json:"output_tokens"`
			Cached     json.RawMessage `json:"cached_input_tokens"`
			CacheWrite json.RawMessage `json:"cache_write_input_tokens"`
		} `json:"usage"`
		// Model is read only as an explicit served-model string on a
		// terminal event. The pinned 0.160 exec schema sends none.
		Model json.RawMessage `json:"model"`
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
		if s.capture != nil {
			s.capture.confirmed = true
		}
		_, _ = fmt.Fprintln(out, "[agentd] native Codex thread confirmed")
	case "turn.started":
		if s.turn || s.terminal {
			return errors.New("unexpected native Codex turn")
		}
		s.turn = true
	case "item.started", "item.updated", "item.completed":
		// Pinned startup/configuration warnings and deprecation notices are
		// completed error items, and can arrive before turn.started. They do
		// not create a turn, terminal result or native identity.
		if e.Type == "item.completed" && e.Item.Type == "error" {
			return nil
		}
		if !s.turn || s.terminal {
			return errors.New("native Codex item outside a turn")
		}
		if e.Type == "item.completed" {
			switch e.Item.Type {
			case "agent_message":
				_, _ = fmt.Fprintln(out, e.Item.Text)
				s.capture.text(e.Item.Text)
			case "command_execution":
				_, _ = fmt.Fprintln(out, e.Item.AggregatedOutput)
			}
		}
	case "turn.completed":
		if !s.turn || s.terminal || e.Usage == nil {
			return errors.New("invalid native Codex completion")
		}
		// Preserve the pinned protocol's refusal of explicitly negative counts.
		for _, raw := range []json.RawMessage{e.Usage.Input, e.Usage.Output, e.Usage.Cached, e.Usage.CacheWrite} {
			var n int64
			if json.Unmarshal(raw, &n) == nil && n < 0 {
				return errors.New("invalid native Codex completion")
			}
		}
		s.terminal = true
		u := protocol.UnknownUsage()
		u.ResultState = "Final"
		u.InputTokens, u.OutputTokens = reportedTokens(e.Usage.Input), reportedTokens(e.Usage.Output)
		u.CacheReadInputTokens, u.CacheCreationInputTokens = reportedTokens(e.Usage.Cached), reportedTokens(e.Usage.CacheWrite)
		codexServedModel(&u, e.Model)
		*result = streamResult{Seen: true, Subtype: "success", NumTurns: 1, Usage: legacyUsage(u), Observation: u}
	case "turn.failed", "error":
		if s.terminal {
			return errors.New("native Codex emitted a second terminal event")
		}
		s.terminal, s.failed = true, true
		u := protocol.UnknownUsage()
		if e.Usage != nil {
			u.InputTokens, u.OutputTokens = reportedTokens(e.Usage.Input), reportedTokens(e.Usage.Output)
			u.CacheReadInputTokens, u.CacheCreationInputTokens = reportedTokens(e.Usage.Cached), reportedTokens(e.Usage.CacheWrite)
		}
		codexServedModel(&u, e.Model)
		*result = streamResult{Seen: true, Subtype: "provider_error", IsError: true, Observation: u, Usage: legacyUsage(u)}
		_, _ = fmt.Fprintln(out, "[agentd] native Codex turn failed")
	default:
		return errors.New("unknown pinned native Codex event")
	}
	return nil
}

// codexServedModel records the terminal event's explicit served model, with
// the turn's own counters and an Unknown cost. A missing or malformed model is
// Unknown; it never refuses an otherwise valid turn.
func codexServedModel(u *protocol.UsageObservation, raw json.RawMessage) {
	var model string
	if len(raw) == 0 || json.Unmarshal(raw, &model) != nil || !protocol.ValidUsageModelID(model) {
		return
	}
	u.ServedModels = []protocol.ModelUsage{{Model: model, InputTokens: u.InputTokens, OutputTokens: u.OutputTokens,
		CacheReadInputTokens: u.CacheReadInputTokens, CacheCreationInputTokens: u.CacheCreationInputTokens,
		Cost: protocol.CostObservation{State: protocol.UsageUnknown, Kind: protocol.UsageUnknown}}}
	u.ActualModelState = protocol.UsageProviderReported
}
