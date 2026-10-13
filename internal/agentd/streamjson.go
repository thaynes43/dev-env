package agentd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strconv"
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
	IsError      bool            `json:"is_error"`
	NumTurns     int             `json:"num_turns"`
	TotalCostUSD json.RawMessage `json:"total_cost_usd"`
	Usage        json.RawMessage `json:"usage"`
	// ModelUsage maps each served model id to its own counters and cost.
	ModelUsage json.RawMessage `json:"modelUsage"`
}

type streamUsage struct {
	InputTokens              json.RawMessage `json:"input_tokens"`
	OutputTokens             json.RawMessage `json:"output_tokens"`
	CacheReadInputTokens     json.RawMessage `json:"cache_read_input_tokens"`
	CacheCreationInputTokens json.RawMessage `json:"cache_creation_input_tokens"`
}

// streamModelUsage is one modelUsage entry of Claude's result event.
type streamModelUsage struct {
	InputTokens              json.RawMessage `json:"inputTokens"`
	OutputTokens             json.RawMessage `json:"outputTokens"`
	CacheReadInputTokens     json.RawMessage `json:"cacheReadInputTokens"`
	CacheCreationInputTokens json.RawMessage `json:"cacheCreationInputTokens"`
	CostUSD                  json.RawMessage `json:"costUSD"`
}

type contentBlock struct {
	Type  string         `json:"type"`
	Text  string         `json:"text"`
	Name  string         `json:"name"`
	Input map[string]any `json:"input"`
}

// streamResult is what the result event says about the task.
type streamResult struct {
	Seen          bool
	Subtype       string
	IsError       bool
	NumTurns      int
	Usage         protocol.Usage
	Observation   protocol.UsageObservation
	UsageConflict bool
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
			_, _ = fmt.Fprintf(w, "[agentd] claude session %s, CLI-reported model %s; actual model Unknown\n", ev.SessionID, ev.Model)
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
		observation := protocol.UnknownUsage()
		observation.ResultState = "Final"
		if ev.IsError {
			observation.ResultState = "Partial"
		}
		observation.Cost = reportedCost(ev.TotalCostUSD)
		var usage streamUsage
		if json.Unmarshal(ev.Usage, &usage) == nil {
			observation.InputTokens = reportedTokens(usage.InputTokens)
			observation.OutputTokens = reportedTokens(usage.OutputTokens)
			observation.CacheReadInputTokens = reportedTokens(usage.CacheReadInputTokens)
			observation.CacheCreationInputTokens = reportedTokens(usage.CacheCreationInputTokens)
		}
		observation.ServedModels = claudeServedModels(ev.ModelUsage)
		if len(observation.ServedModels) > 0 {
			observation.ActualModelState = protocol.UsageProviderReported
		}
		if res.Seen {
			first, _ := json.Marshal(res.Observation)
			next, _ := json.Marshal(observation)
			if string(first) != string(next) || res.Subtype != ev.Subtype || res.IsError != ev.IsError || res.NumTurns != ev.NumTurns {
				res.UsageConflict = true
			}
			return // cumulative or replayed results never replace the original.
		}
		res.Seen = true
		res.Subtype = ev.Subtype
		res.IsError = ev.IsError
		res.NumTurns = ev.NumTurns
		res.Observation = observation
		res.Usage = legacyUsage(observation)
		cost := "Unknown"
		if observation.Cost.State == protocol.UsageReported {
			cost = observation.Cost.Amount + " USD (CLI equivalent estimate)"
		}
		_, _ = fmt.Fprintf(w, "[agentd] result %s after %d turns, initial invocation cost %s; %s\n", ev.Subtype, ev.NumTurns, cost, servedModelsText(observation.ServedModels))
	}
}

// servedModelsText names the provider-reported served models for the log.
func servedModelsText(models []protocol.ModelUsage) string {
	if len(models) == 0 {
		return "actual model Unknown"
	}
	names := make([]string, 0, len(models))
	for _, m := range models {
		names = append(names, m.Model)
	}
	what := "served model "
	if len(names) > 1 {
		what = "served models "
	}
	return what + strings.Join(names, ", ") + " (provider reported)"
}

// claudeServedModels reads the result event's modelUsage object. Anything but
// an object of at most MaxServedModels valid ids, each an object, with no key
// repeated, is no list at all: a partial list would misstate what served the
// invocation. Within a valid entry each counter and the cost keep their own
// presence, as the totals do.
func claudeServedModels(raw json.RawMessage) []protocol.ModelUsage {
	d := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := d.Token(); err != nil || tok != json.Delim('{') {
		return nil
	}
	var models []protocol.ModelUsage
	seen := map[string]bool{}
	for d.More() {
		tok, err := d.Token()
		id, ok := tok.(string)
		if err != nil || !ok || seen[id] || !protocol.ValidUsageModelID(id) || len(models) == protocol.MaxServedModels {
			return nil
		}
		seen[id] = true
		var entry json.RawMessage
		if d.Decode(&entry) != nil || !bytes.HasPrefix(bytes.TrimSpace(entry), []byte("{")) {
			return nil
		}
		var m streamModelUsage
		if json.Unmarshal(entry, &m) != nil {
			return nil
		}
		models = append(models, protocol.ModelUsage{Model: id, InputTokens: reportedTokens(m.InputTokens), OutputTokens: reportedTokens(m.OutputTokens),
			CacheReadInputTokens: reportedTokens(m.CacheReadInputTokens), CacheCreationInputTokens: reportedTokens(m.CacheCreationInputTokens), Cost: reportedCost(m.CostUSD)})
	}
	if tok, err := d.Token(); err != nil || tok != json.Delim('}') {
		return nil
	}
	slices.SortFunc(models, func(a, b protocol.ModelUsage) int { return strings.Compare(a.Model, b.Model) })
	return models
}

func reportedTokens(raw json.RawMessage) protocol.TokenObservation {
	m := protocol.TokenObservation{State: protocol.UsageUnknown}
	var amount int64
	if len(raw) == 0 || string(raw) == "null" || json.Unmarshal(raw, &amount) != nil || amount < 0 || amount > protocol.MaxUsageTokens {
		return m
	}
	m.State, m.Amount = protocol.UsageReported, &amount
	return m
}

func reportedCost(raw json.RawMessage) protocol.CostObservation {
	m := protocol.CostObservation{State: protocol.UsageUnknown, Kind: protocol.UsageUnknown}
	amount, ok := canonicalCostNumber(raw)
	if !ok {
		return m
	}
	m = protocol.CostObservation{State: protocol.UsageReported, Amount: amount, Unit: "USD", Kind: protocol.UsageCLIEstimate}
	if !m.Valid() {
		return protocol.CostObservation{State: protocol.UsageUnknown, Kind: protocol.UsageUnknown}
	}
	return m
}

// Preserve the CLI's decimal exactly. A float conversion can round a positive
// report down to zero or change its digits. Expansion is bounded before allocation.
func canonicalCostNumber(raw json.RawMessage) (string, bool) {
	s := strings.TrimSpace(string(raw))
	if len(s) == 0 || len(s) > 128 || !json.Valid(raw) || s[0] != '-' && (s[0] < '0' || s[0] > '9') {
		return "", false
	}
	negative := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	exponent := 0
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		var err error
		exponent, err = strconv.Atoi(s[i+1:])
		if err != nil || exponent < -256 || exponent > 256 {
			return "", false
		}
		s = s[:i]
	}
	point := strings.IndexByte(s, '.')
	if point < 0 {
		point = len(s)
	} else {
		s = s[:point] + s[point+1:]
	}
	point += exponent
	for len(s) > 0 && s[0] == '0' {
		s, point = s[1:], point-1
	}
	if s == "" {
		return "0", true
	}
	if negative {
		return "", false
	}
	s = strings.TrimRight(s, "0")
	var amount string
	switch {
	case point <= 0:
		if 2-point+len(s) > 64 {
			return "", false
		}
		amount = "0." + strings.Repeat("0", -point) + s
	case point >= len(s):
		if point > 64 {
			return "", false
		}
		amount = s + strings.Repeat("0", point-len(s))
	default:
		if len(s)+1 > 64 {
			return "", false
		}
		amount = s[:point] + "." + s[point:]
	}
	return amount, true
}

func legacyUsage(u protocol.UsageObservation) protocol.Usage {
	value := func(m protocol.TokenObservation) int64 {
		if m.Amount != nil {
			return *m.Amount
		}
		return 0
	}
	cost, _ := strconv.ParseFloat(u.Cost.Amount, 64)
	return protocol.Usage{CostUSD: cost, InputTokens: value(u.InputTokens), OutputTokens: value(u.OutputTokens),
		CacheReadInputTokens: value(u.CacheReadInputTokens), CacheCreationInputTokens: value(u.CacheCreationInputTokens)}
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
