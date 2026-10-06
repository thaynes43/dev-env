package agentd

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// streamEvent is the part of a Claude CLI stream-json event agentd reads
// (`claude -p --output-format stream-json --verbose`). Unknown events and
// fields are ignored, so a CLI release that adds some breaks nothing.
type streamEvent struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	Model     string `json:"model"`
	SessionID string `json:"session_id"`
	Message   *struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
	// The result event.
	IsError      bool    `json:"is_error"`
	NumTurns     int     `json:"num_turns"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	Usage        *struct {
		InputTokens              int64 `json:"input_tokens"`
		OutputTokens             int64 `json:"output_tokens"`
		CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

type contentBlock struct {
	Type  string         `json:"type"`
	Text  string         `json:"text"`
	Name  string         `json:"name"`
	Input map[string]any `json:"input"`
}

// streamResult is what the result event says about the task.
type streamResult struct {
	Seen     bool
	Subtype  string
	IsError  bool
	NumTurns int
	Usage    protocol.Usage
}

// renderStreamLine writes a readable form of one stream-json line to w (the
// pane and the task log) and records the result event in res. A line that is
// not JSON is written as it is.
func renderStreamLine(line []byte, w io.Writer, res *streamResult) {
	var ev streamEvent
	if len(line) == 0 {
		return
	}
	if err := json.Unmarshal(line, &ev); err != nil || ev.Type == "" {
		_, _ = fmt.Fprintf(w, "%s\n", line)
		return
	}
	switch ev.Type {
	case "system":
		if ev.Subtype == "init" {
			_, _ = fmt.Fprintf(w, "[agentd] claude session %s, model %s\n", ev.SessionID, ev.Model)
		}
	case "assistant":
		if ev.Message == nil {
			return
		}
		var blocks []contentBlock
		if json.Unmarshal(ev.Message.Content, &blocks) != nil {
			return
		}
		for _, b := range blocks {
			switch b.Type {
			case "text":
				if t := strings.TrimSpace(b.Text); t != "" {
					_, _ = fmt.Fprintf(w, "%s\n", t)
				}
			case "tool_use":
				_, _ = fmt.Fprintf(w, "-> %s%s\n", b.Name, toolSummary(b.Input))
			}
		}
	case "result":
		res.Seen = true
		res.Subtype = ev.Subtype
		res.IsError = ev.IsError
		res.NumTurns = ev.NumTurns
		res.Usage.CostUSD = ev.TotalCostUSD
		if ev.Usage != nil {
			res.Usage.InputTokens = ev.Usage.InputTokens
			res.Usage.OutputTokens = ev.Usage.OutputTokens
			res.Usage.CacheReadInputTokens = ev.Usage.CacheReadInputTokens
			res.Usage.CacheCreationInputTokens = ev.Usage.CacheCreationInputTokens
		}
		_, _ = fmt.Fprintf(w, "[agentd] result %s after %d turns, cost $%.4f\n", ev.Subtype, ev.NumTurns, ev.TotalCostUSD)
	}
}

// toolSummary is one short line about a tool call: its command, path or
// pattern.
func toolSummary(in map[string]any) string {
	for _, k := range []string{"command", "file_path", "pattern", "url", "description"} {
		if v, ok := in[k].(string); ok && v != "" {
			v = strings.Join(strings.Fields(v), " ")
			if len(v) > 160 {
				v = v[:160] + "…"
			}
			return ": " + v
		}
	}
	return ""
}
