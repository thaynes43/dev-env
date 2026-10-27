package agentd

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// Conversation usage (Unit A, inert): the cumulative token counters of one
// registered interactive conversation, read from the provider's own record of it
// by the same registration and current-pod fence the portal's history uses. This
// file wires into no heartbeat, status or view. It holds no text: a record is
// decoded into a struct of counters and ids only, and the result carries models
// and numbers.
//
// A headless task's figure stays the InitialManagedInvocation observation. This
// measurement refuses a task session by mode and is never added to it.

var claudeAssistantMarker = []byte(`"type":"assistant"`)

// claudeSyntheticModel is the model Claude Code writes on messages it made up
// itself (interrupts, errors). They carry no usage worth counting.
const claudeSyntheticModel = "<synthetic>"

// MeasureConversationUsage measures the registered conversation of the current
// Session and pod. Anything the fence does not bind is Unknown with a reason,
// with no fallback to another file. Only interactive (local or remote) sessions
// are measured. An unsupported agent returns an Unknown that is not Valid; the
// caller must check Valid before it publishes.
func MeasureConversationUsage(settings Settings, session protocol.Session, expectedSession, expectedPod string, now time.Time) protocol.ConversationUsage {
	unknown := func(reason string) protocol.ConversationUsage {
		return protocol.UnknownConversationUsage(session.Agent, reason, now)
	}
	if session.Agent != protocol.AgentClaude && session.Agent != protocol.AgentCodex {
		return unknown(protocol.ReasonUnsupportedAgent)
	}
	if session.Mode != protocol.ModeLocal && session.Mode != protocol.ModeRemote {
		return unknown(protocol.ReasonNotInteractive)
	}
	var readErr error
	unqualified := false
	raw, current, ok := readRegisteredPortalTranscript(settings, session, expectedSession, expectedPod, now, func(current, first Launch) ([]byte, error) {
		if current.Dir != settings.WorktreePath(session.Name) {
			return nil, os.ErrPermission
		}
		raw, err := portalReadOwnTranscript(settings, first)
		if err != nil {
			readErr = err
			return nil, os.ErrPermission
		}
		// A Codex rollout is qualified by its first record, as for history. A
		// Claude transcript is qualified record by record below, because one
		// oversize tool result must not hide every figure.
		if session.Agent == protocol.AgentCodex && !portalSourceVersion(session.Agent, first.ConversationID, first.Dir, raw) {
			unqualified = true
			return nil, os.ErrPermission
		}
		return raw, nil
	})
	switch {
	case ok:
	case errors.Is(readErr, errPortalSourceLimit):
		return unknown(protocol.ReasonSourceLimit)
	case unqualified:
		return unknown(protocol.ReasonUnqualified)
	default:
		return unknown(protocol.ReasonUnbound)
	}
	if session.Agent == protocol.AgentClaude {
		return claudeConversationUsage(raw, current.ConversationID, now)
	}
	return codexConversationUsage(raw, now)
}

// countOf reads one JSON counter: an integer from 0 to the counter bound.
func countOf(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, false
	}
	var n int64
	if json.Unmarshal(raw, &n) != nil || n < 0 || n > protocol.MaxUsageTokens {
		return 0, false
	}
	return n, true
}

// optionalCount reads a counter that may be absent. present is false for an
// absent or null field; ok is false for a present value that is not a counter.
func optionalCount(raw json.RawMessage) (n int64, present, ok bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, false, true
	}
	n, ok = countOf(raw)
	return n, ok, ok
}

func reportedAmount(n int64) protocol.TokenObservation {
	return protocol.TokenObservation{State: protocol.UsageReported, Amount: &n}
}

var unknownAmount = protocol.TokenObservation{State: protocol.UsageUnknown}

// claudeMessage accumulates one assistant message across the records it was
// streamed in.
type claudeMessage struct {
	model             string
	input, output     int64
	cacheRead         int64
	cacheCreation     int64
	hasRead, hasWrite bool
	conflict          bool
}

// claudeConversationUsage sums the assistant messages of one Claude Code
// transcript per served model. It is pure: it reads lines and returns numbers.
//
// Dedupe rule. Claude Code writes one JSONL record per content block of a
// streamed assistant message (thinking, text, each tool_use), and every one of
// those records repeats the same message.id and message.usage. Streaming
// updates can leave the first records holding a smaller output_tokens than the
// last. So a message counts once, and each of its counters is the MAXIMUM seen
// across its records: the counters only grow while a message streams, so the
// maximum is the final value whatever the record order. Messages are then summed
// per served model. Cache read and cache creation tokens are their own
// counters; they are never added to input.
//
// Only compact `"type":"assistant"` lines are decoded; every other line is left
// unread. Records of the API-error kind and Claude's own synthetic messages are
// ignored. A record that cannot be counted (not strict JSON, oversize, another
// Claude Code version or conversation, no id, bad model, no or bad usage, a
// message id seen with two models) adds to Skipped and makes the result Partial.
// No counted message at all is Unknown, never zero.
func claudeConversationUsage(raw []byte, conversation string, observed time.Time) protocol.ConversationUsage {
	unknown := func(reason string) protocol.ConversationUsage {
		return protocol.UnknownConversationUsage(protocol.AgentClaude, reason, observed)
	}
	if !nativeThreadID.MatchString(conversation) || len(raw) > portalProjectionInputLimit || observed.IsZero() {
		return unknown(protocol.ReasonUnbound)
	}
	messages := map[string]*claudeMessage{}
	var skipped int64
	for _, line := range bytes.Split(raw, []byte("\n")) {
		if !bytes.Contains(line, claudeAssistantMarker) {
			continue
		}
		if len(line) > portalProjectionLineLimit || !utf8.Valid(line) || !protocol.StrictPortalJSON(line) || !portalCanonicalFields(line, "type", "sessionId", "version", "isApiErrorMessage", "message") {
			skipped++
			continue
		}
		var record struct {
			Type      string          `json:"type"`
			SessionID string          `json:"sessionId"`
			Version   string          `json:"version"`
			APIError  bool            `json:"isApiErrorMessage"`
			Message   json.RawMessage `json:"message"`
		}
		if json.Unmarshal(line, &record) != nil {
			skipped++
			continue
		}
		if record.Type != "assistant" || record.APIError {
			continue
		}
		var message struct {
			ID    string          `json:"id"`
			Model string          `json:"model"`
			Usage json.RawMessage `json:"usage"`
		}
		if !portalCanonicalFields(record.Message, "id", "model", "usage") || json.Unmarshal(record.Message, &message) != nil {
			skipped++
			continue
		}
		if message.Model == claudeSyntheticModel {
			continue
		}
		if record.Version != portalClaudeVersion || record.SessionID != conversation || message.ID == "" || len(message.ID) > 256 || !protocol.ValidUsageModelID(message.Model) {
			skipped++
			continue
		}
		var usage struct {
			Input         json.RawMessage `json:"input_tokens"`
			Output        json.RawMessage `json:"output_tokens"`
			CacheRead     json.RawMessage `json:"cache_read_input_tokens"`
			CacheCreation json.RawMessage `json:"cache_creation_input_tokens"`
		}
		if !portalCanonicalFields(message.Usage, "input_tokens", "output_tokens", "cache_read_input_tokens", "cache_creation_input_tokens") || json.Unmarshal(message.Usage, &usage) != nil {
			skipped++
			continue
		}
		in, inOK := countOf(usage.Input)
		out, outOK := countOf(usage.Output)
		read, hasRead, readOK := optionalCount(usage.CacheRead)
		write, hasWrite, writeOK := optionalCount(usage.CacheCreation)
		if !inOK || !outOK || !readOK || !writeOK {
			skipped++
			continue
		}
		m := messages[message.ID]
		if m == nil {
			m = &claudeMessage{model: message.Model}
			messages[message.ID] = m
		}
		if m.model != message.Model {
			m.conflict = true
			continue
		}
		m.input, m.output = max(m.input, in), max(m.output, out)
		if hasRead {
			m.cacheRead, m.hasRead = max(m.cacheRead, read), true
		}
		if hasWrite {
			m.cacheCreation, m.hasWrite = max(m.cacheCreation, write), true
		}
	}
	type modelSum struct {
		input, output, read, write    int64
		messages, withRead, withWrite int64
	}
	sums := map[string]*modelSum{}
	var turns int64
	for _, m := range messages {
		if m.conflict {
			skipped++
			continue
		}
		s := sums[m.model]
		if s == nil {
			s = &modelSum{}
			sums[m.model] = s
		}
		s.input += m.input
		s.output += m.output
		s.messages++
		if m.hasRead {
			s.read += m.cacheRead
			s.withRead++
		}
		if m.hasWrite {
			s.write += m.cacheCreation
			s.withWrite++
		}
		turns++
	}
	if len(sums) == 0 {
		if skipped > 0 {
			result := unknown(protocol.ReasonSkippedRecords)
			result.Skipped = min(skipped, protocol.MaxConversationCount)
			return result
		}
		return unknown(protocol.ReasonNoUsage)
	}
	if len(sums) > protocol.MaxConversationModels {
		return unknown(protocol.ReasonTooManyModels)
	}
	result := protocol.ConversationUsage{Version: protocol.ConversationUsageVersion, Provider: protocol.AgentClaude, ObservedAt: observed.UTC(),
		Coverage: protocol.ConversationCoverage, CounterSemantics: protocol.ConversationCumulative, ResultState: protocol.ConversationFinal,
		Turns: turns, Skipped: min(skipped, protocol.MaxConversationCount)}
	missing := false
	for model, s := range sums {
		if s.input > protocol.MaxUsageTokens || s.output > protocol.MaxUsageTokens || s.read > protocol.MaxUsageTokens || s.write > protocol.MaxUsageTokens {
			return unknown(protocol.ReasonCountOverflow)
		}
		row := protocol.ConversationModelUsage{Model: model, InputTokens: reportedAmount(s.input), OutputTokens: reportedAmount(s.output), CacheReadInputTokens: unknownAmount, CacheCreationInputTokens: unknownAmount}
		// A cache counter some messages carried and others did not is a lower bound.
		if s.withRead > 0 {
			row.CacheReadInputTokens = reportedAmount(s.read)
			missing = missing || s.withRead < s.messages
		}
		if s.withWrite > 0 {
			row.CacheCreationInputTokens = reportedAmount(s.write)
			missing = missing || s.withWrite < s.messages
		}
		result.Models = append(result.Models, row)
	}
	slices.SortFunc(result.Models, func(a, b protocol.ConversationModelUsage) int {
		switch {
		case a.Model < b.Model:
			return -1
		case a.Model > b.Model:
			return 1
		}
		return 0
	})
	switch {
	case skipped > 0:
		result.ResultState, result.ReasonCode = protocol.ConversationPartial, protocol.ReasonSkippedRecords
	case missing:
		result.ResultState, result.ReasonCode = protocol.ConversationPartial, protocol.ReasonMissingCounters
	}
	return result
}

// codexSnapshot is one token_count event's cumulative total.
type codexSnapshot struct {
	input, output, cached int64
	hasCached             bool
}

func (s codexSnapshot) backwardsFrom(p codexSnapshot) bool {
	return s.input < p.input || s.output < p.output || (s.hasCached && p.hasCached && s.cached < p.cached)
}

// codexConversationUsage reads the cumulative token totals of a Codex rollout.
//
// A rollout records `event_msg` records whose payload is `token_count`, with
// `info.total_token_usage` (the running totals for the thread) and
// `info.last_token_usage` (the last call alone). Only the totals are read;
// last_token_usage is never read or added, and repeated snapshots are never
// summed. The result is the latest valid snapshot. A snapshot equal to the
// previous one is a repeat and counts nothing. A counter that goes backwards
// means the totals restarted (a reset or a new run in the same file); nothing is
// added across it, the figure is the latest run's totals, and the result is
// Partial with counter_reset. A token_count whose info is null (the first event
// of a thread) is not a snapshot and is ignored. input_tokens includes
// cached_input_tokens in Codex's accounting; both are reported as their own
// counters, as the exec stream path does, and are not to be added together.
//
// Totals are per thread, not per model. With one served model (the rollout's
// turn_context records) the totals belong to it. With several there is no honest
// split, so each model is listed with Unknown counters and the result is
// Partial (model_split_unknown), as for the exec path. The format is read as the
// pinned 0.160 CLI is understood to write it; the repository holds no sample of
// a real rollout, so the fixtures are synthetic and a real one must be checked
// before this measurement is published.
func codexConversationUsage(raw []byte, observed time.Time) protocol.ConversationUsage {
	unknown := func(reason string) protocol.ConversationUsage {
		return protocol.UnknownConversationUsage(protocol.AgentCodex, reason, observed)
	}
	if len(raw) > portalProjectionInputLimit || observed.IsZero() {
		return unknown(protocol.ReasonUnbound)
	}
	var (
		skipped, turns, resets int64
		latest                 codexSnapshot
		have                   bool
		models                 []string
	)
	for _, line := range bytes.Split(raw, []byte("\n")) {
		head := line[:min(len(line), 512)]
		isCount := bytes.Contains(head, []byte(`"token_count"`))
		isContext := bytes.Contains(head[:min(len(head), 256)], rolloutTurnContext)
		if !isCount && !isContext {
			continue
		}
		if len(line) > portalProjectionLineLimit || !utf8.Valid(line) || !protocol.StrictPortalJSON(line) || !portalCanonicalFields(line, "type", "payload") {
			skipped++
			continue
		}
		var record struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(line, &record) != nil || !portalCanonicalFields(record.Payload, "type", "model", "info") {
			skipped++
			continue
		}
		var payload struct {
			Type  string          `json:"type"`
			Model json.RawMessage `json:"model"`
			Info  json.RawMessage `json:"info"`
		}
		if json.Unmarshal(record.Payload, &payload) != nil {
			skipped++
			continue
		}
		switch {
		case record.Type == "turn_context" && isContext:
			if len(payload.Model) == 0 || string(payload.Model) == "null" {
				continue
			}
			var model string
			if json.Unmarshal(payload.Model, &model) != nil || !protocol.ValidUsageModelID(model) {
				skipped++
				continue
			}
			if !slices.Contains(models, model) {
				models = append(models, model)
			}
		case record.Type == "event_msg" && payload.Type == "token_count":
			if len(payload.Info) == 0 || string(payload.Info) == "null" {
				continue
			}
			snapshot, ok := codexTotals(payload.Info)
			if !ok {
				skipped++
				continue
			}
			if have && snapshot == latest {
				continue
			}
			if have && snapshot.backwardsFrom(latest) {
				resets++
			}
			latest, have = snapshot, true
			turns++
		}
	}
	if !have {
		if skipped > 0 {
			result := unknown(protocol.ReasonSkippedRecords)
			result.Skipped = min(skipped, protocol.MaxConversationCount)
			return result
		}
		return unknown(protocol.ReasonNoUsage)
	}
	slices.Sort(models)
	switch {
	case len(models) == 0:
		return unknown(protocol.ReasonModelUnknown)
	case len(models) > protocol.MaxConversationModels:
		return unknown(protocol.ReasonTooManyModels)
	}
	result := protocol.ConversationUsage{Version: protocol.ConversationUsageVersion, Provider: protocol.AgentCodex, ObservedAt: observed.UTC(),
		Coverage: protocol.ConversationCoverage, CounterSemantics: protocol.ConversationCumulative, ResultState: protocol.ConversationFinal,
		Turns: min(turns, protocol.MaxConversationCount), Skipped: min(skipped, protocol.MaxConversationCount)}
	for _, model := range models {
		row := protocol.ConversationModelUsage{Model: model, InputTokens: unknownAmount, OutputTokens: unknownAmount, CacheReadInputTokens: unknownAmount, CacheCreationInputTokens: unknownAmount}
		if len(models) == 1 {
			row.InputTokens, row.OutputTokens = reportedAmount(latest.input), reportedAmount(latest.output)
			if latest.hasCached {
				row.CacheReadInputTokens = reportedAmount(latest.cached)
			}
		}
		result.Models = append(result.Models, row)
	}
	switch {
	case len(models) > 1:
		result.ResultState, result.ReasonCode = protocol.ConversationPartial, protocol.ReasonModelSplit
	case resets > 0:
		result.ResultState, result.ReasonCode = protocol.ConversationPartial, protocol.ReasonCounterReset
	case skipped > 0:
		result.ResultState, result.ReasonCode = protocol.ConversationPartial, protocol.ReasonSkippedRecords
	}
	return result
}

// codexTotals reads info.total_token_usage and nothing else of the event.
func codexTotals(info json.RawMessage) (codexSnapshot, bool) {
	var s codexSnapshot
	if !portalCanonicalFields(info, "total_token_usage") {
		return s, false
	}
	var wrapper struct {
		Total json.RawMessage `json:"total_token_usage"`
	}
	if json.Unmarshal(info, &wrapper) != nil || !portalCanonicalFields(wrapper.Total, "input_tokens", "output_tokens", "cached_input_tokens") {
		return s, false
	}
	var total struct {
		Input  json.RawMessage `json:"input_tokens"`
		Output json.RawMessage `json:"output_tokens"`
		Cached json.RawMessage `json:"cached_input_tokens"`
	}
	if json.Unmarshal(wrapper.Total, &total) != nil {
		return s, false
	}
	in, inOK := countOf(total.Input)
	out, outOK := countOf(total.Output)
	cached, hasCached, cachedOK := optionalCount(total.Cached)
	if !inOK || !outOK || !cachedOK {
		return s, false
	}
	return codexSnapshot{input: in, output: out, cached: cached, hasCached: hasCached}, true
}
