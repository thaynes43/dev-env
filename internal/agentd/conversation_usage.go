package agentd

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"slices"
	"syscall"
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

// conversationUsageScanLimit bounds how much of a provider record one usage
// measurement scans. It is far above the history's 1 MiB bound because usage
// streams the file line by line and keeps only numbers; a larger record is
// Unknown (source_limit). The history path keeps its own bound.
const conversationUsageScanLimit = 64 << 20

// MeasureConversationUsage measures the registered conversation of the current
// Session and pod. Anything the fence does not bind is Unknown with a reason,
// with no fallback to another file. Only interactive (local or remote) sessions
// are measured. An unsupported agent returns an Unknown that is not Valid; the
// caller must check Valid before it publishes.
//
// The provider record is opened by the history's fixed, no-link locator and
// scanned once, line by line, as of its size when it was opened: the live file
// keeps growing, so appended lines wait for the next measurement, and a final
// line without a newline (still being written) is left out. The file's identity
// is checked after the scan. Only numbers and model ids are kept.
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
	var (
		readErr     error
		unqualified bool
		measured    protocol.ConversationUsage
	)
	_, _, ok := readRegisteredPortalTranscript(settings, session, expectedSession, expectedPod, now, func(current, first Launch) ([]byte, error) {
		if current.Dir != settings.WorktreePath(session.Name) {
			return nil, os.ErrPermission
		}
		f, before, err := portalOpenOwnTranscript(settings, first)
		if err != nil {
			readErr = err
			return nil, os.ErrPermission
		}
		defer func() { _ = f.Close() }()
		if before.Size > conversationUsageScanLimit {
			readErr = errPortalSourceLimit
			return nil, os.ErrPermission
		}
		var claude *claudeUsageScan
		var codex *codexUsageScan
		var scanErr error
		if session.Agent == protocol.AgentClaude {
			claude = &claudeUsageScan{conversation: first.ConversationID}
			scanErr = scanTranscriptLines(io.LimitReader(f, before.Size), claudeAssistantMarker, claude.line)
		} else {
			codex = &codexUsageScan{}
			scanErr = scanTranscriptLines(io.LimitReader(f, before.Size), nil, func(line []byte, oversize, _ bool) { codex.line(line, oversize) })
		}
		// The file may have been appended to, but not replaced.
		var after syscall.Stat_t
		if scanErr != nil || syscall.Fstat(int(f.Fd()), &after) != nil || before.Dev != after.Dev || before.Ino != after.Ino || before.Nlink != after.Nlink || after.Size < before.Size {
			readErr = scanErr
			return nil, os.ErrPermission
		}
		if claude != nil {
			measured = claude.result(now)
			return nil, nil
		}
		// A Codex rollout is qualified by its first record, as for history. A
		// Claude transcript is qualified record by record, because one oversize
		// tool result must not hide every figure.
		if codex.first == nil || !portalSourceVersion(session.Agent, first.ConversationID, first.Dir, codex.first) {
			unqualified = true
			return nil, os.ErrPermission
		}
		measured = codex.result(now)
		return nil, nil
	})
	switch {
	case ok:
		return measured
	case errors.Is(readErr, errPortalSourceLimit):
		return unknown(protocol.ReasonSourceLimit)
	case unqualified:
		return unknown(protocol.ReasonUnqualified)
	default:
		return unknown(protocol.ReasonUnbound)
	}
}

// scanTranscriptLines reads r one line at a time and hands each to on, with its
// size classification: a line over the history's line bound is passed as its
// first chunk with oversize true. marked reports whether marker occurs anywhere
// in the line, including past the first chunk. A final line without a newline is
// still being written and is not passed. It copies nothing: on must not keep line.
func scanTranscriptLines(r io.Reader, marker []byte, on func(line []byte, oversize, marked bool)) error {
	reader := bufio.NewReaderSize(r, portalProjectionLineLimit+2)
	for {
		chunk, err := reader.ReadSlice('\n')
		switch {
		case err == nil:
			line := chunk[:len(chunk)-1]
			on(line, len(line) > portalProjectionLineLimit, len(marker) > 0 && bytes.Contains(line, marker))
		case errors.Is(err, bufio.ErrBufferFull):
			// Longer than the line bound: pass its first chunk, and keep looking
			// for the marker in the rest without keeping the rest.
			first := chunk[:portalProjectionLineLimit]
			marked := len(marker) > 0 && bytes.Contains(chunk, marker)
			head := make([]byte, 0, len(marker))
			if len(marker) > 1 {
				head = append(head, chunk[max(0, len(chunk)-len(marker)+1):]...)
			}
			firstCopy := append([]byte(nil), first...)
			for errors.Is(err, bufio.ErrBufferFull) {
				chunk, err = reader.ReadSlice('\n')
				if !marked && len(marker) > 0 && bytes.Contains(append(append([]byte(nil), head...), chunk...), marker) {
					marked = true
				}
				if len(marker) > 1 {
					head = append(head[:0], chunk[max(0, len(chunk)-len(marker)+1):]...)
				}
			}
			if err == nil {
				on(firstCopy, true, marked)
			}
		}
		if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
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

// claudeUsageScan sums the assistant messages of one Claude Code transcript per
// served model, a line at a time. It keeps ids, models and numbers only.
//
// Dedupe rule. Claude Code writes one JSONL record per content block of a
// streamed assistant message (thinking, text, each tool_use), and each of those
// records repeats the same message.id and message.usage (observed on 2.1.296; the
// published Claude Code transcript format does not document it, so this is an
// assumption the fixtures encode). Streaming updates can leave early records with
// a smaller output_tokens than the last. So a message counts once, and each of its
// counters is the MAXIMUM seen across its records: the counters only grow while a
// message streams, so the maximum is the final value whatever the record order.
// Messages are then summed per served model. Cache read and cache creation tokens
// are their own counters; they are never added to input.
//
// Only compact `"type":"assistant"` lines are decoded; every other line is left
// unread. Records of the API-error kind and Claude's own synthetic messages are
// ignored. A record that cannot be counted (not strict JSON, oversize, another
// Claude Code version or conversation, no id, bad model, no or bad usage, a
// message id seen with two models) adds to Skipped and makes the result Partial.
// No counted message at all is Unknown, never zero.
type claudeUsageScan struct {
	conversation string
	messages     map[string]*claudeMessage
	skipped      int64
}

func (a *claudeUsageScan) line(line []byte, oversize, marked bool) {
	if !marked {
		return
	}
	if oversize || !utf8.Valid(line) || !protocol.StrictPortalJSON(line) || !portalCanonicalFields(line, "type", "sessionId", "version", "isApiErrorMessage", "message") {
		a.skipped++
		return
	}
	var record struct {
		Type      string          `json:"type"`
		SessionID string          `json:"sessionId"`
		Version   string          `json:"version"`
		APIError  bool            `json:"isApiErrorMessage"`
		Message   json.RawMessage `json:"message"`
	}
	if json.Unmarshal(line, &record) != nil {
		a.skipped++
		return
	}
	if record.Type != "assistant" || record.APIError {
		return
	}
	var message struct {
		ID    string          `json:"id"`
		Model string          `json:"model"`
		Usage json.RawMessage `json:"usage"`
	}
	if !portalCanonicalFields(record.Message, "id", "model", "usage") || json.Unmarshal(record.Message, &message) != nil {
		a.skipped++
		return
	}
	if message.Model == claudeSyntheticModel {
		return
	}
	if record.Version != portalClaudeVersion || record.SessionID != a.conversation || message.ID == "" || len(message.ID) > 256 || !protocol.ValidUsageModelID(message.Model) {
		a.skipped++
		return
	}
	var usage struct {
		Input         json.RawMessage `json:"input_tokens"`
		Output        json.RawMessage `json:"output_tokens"`
		CacheRead     json.RawMessage `json:"cache_read_input_tokens"`
		CacheCreation json.RawMessage `json:"cache_creation_input_tokens"`
	}
	if !portalCanonicalFields(message.Usage, "input_tokens", "output_tokens", "cache_read_input_tokens", "cache_creation_input_tokens") || json.Unmarshal(message.Usage, &usage) != nil {
		a.skipped++
		return
	}
	in, inOK := countOf(usage.Input)
	out, outOK := countOf(usage.Output)
	read, hasRead, readOK := optionalCount(usage.CacheRead)
	write, hasWrite, writeOK := optionalCount(usage.CacheCreation)
	if !inOK || !outOK || !readOK || !writeOK {
		a.skipped++
		return
	}
	if a.messages == nil {
		a.messages = map[string]*claudeMessage{}
	}
	m := a.messages[message.ID]
	if m == nil {
		m = &claudeMessage{model: message.Model}
		a.messages[message.ID] = m
	}
	if m.model != message.Model {
		m.conflict = true
		return
	}
	m.input, m.output = max(m.input, in), max(m.output, out)
	if hasRead {
		m.cacheRead, m.hasRead = max(m.cacheRead, read), true
	}
	if hasWrite {
		m.cacheCreation, m.hasWrite = max(m.cacheCreation, write), true
	}
}

func (a *claudeUsageScan) result(observed time.Time) protocol.ConversationUsage {
	unknown := func(reason string) protocol.ConversationUsage {
		return protocol.UnknownConversationUsage(protocol.AgentClaude, reason, observed)
	}
	if !nativeThreadID.MatchString(a.conversation) || observed.IsZero() {
		return unknown(protocol.ReasonUnbound)
	}
	type modelSum struct {
		input, output, read, write    int64
		messages, withRead, withWrite int64
	}
	sums := map[string]*modelSum{}
	skipped := a.skipped
	var turns int64
	for _, m := range a.messages {
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

// claudeConversationUsage runs the scan over lines already in memory. It is the
// pure form the fixture tests use.
func claudeConversationUsage(raw []byte, conversation string, observed time.Time) protocol.ConversationUsage {
	scan := &claudeUsageScan{conversation: conversation}
	if err := scanTranscriptLines(bytes.NewReader(raw), claudeAssistantMarker, scan.line); err != nil || len(raw) > conversationUsageScanLimit {
		return protocol.UnknownConversationUsage(protocol.AgentClaude, protocol.ReasonUnbound, observed)
	}
	return scan.result(observed)
}

// codexSnapshot is one token_count event's cumulative total, and the provider's
// per-turn figure for that event when it gave one.
type codexSnapshot struct {
	input, output, cached int64
	hasCached             bool
	// last* are last_token_usage: the last call alone.
	lastInput, lastOutput, lastCached int64
	hasLast, hasLastCached            bool
}

// total is the cumulative part alone, for comparing snapshots.
func (s codexSnapshot) total() [3]int64 {
	c := int64(-1)
	if s.hasCached {
		c = s.cached
	}
	return [3]int64{s.input, s.output, c}
}

func (s codexSnapshot) backwardsFrom(p codexSnapshot) bool {
	return s.input < p.input || s.output < p.output || (s.hasCached && p.hasCached && s.cached < p.cached)
}

// deltaMatches is the consistency guard: when the provider gave a per-turn
// figure, the rise from the previous cumulative total must equal it. A rise that
// does not (a lost snapshot, or a total that is not what it seems) is not
// trusted.
func (s codexSnapshot) deltaMatches(p codexSnapshot) bool {
	if !s.hasLast {
		return true
	}
	if s.input-p.input != s.lastInput || s.output-p.output != s.lastOutput {
		return false
	}
	if s.hasCached && p.hasCached && s.hasLastCached && s.cached-p.cached != s.lastCached {
		return false
	}
	return true
}

// codexUsageScan reads the cumulative token totals of a Codex rollout, a line at
// a time.
//
// A rollout records `event_msg` records whose payload is `token_count`, with
// `info.total_token_usage` (the running totals for the thread) and
// `info.last_token_usage` (the last call alone). The figure is the latest valid
// total; repeated snapshots are never summed. A snapshot equal to the previous
// one is a repeat and counts nothing. last_token_usage is read only as a
// consistency guard: each rise in the total over the previous snapshot must equal
// it, and a rise that does not makes the result Partial (delta_mismatch). A
// counter that goes backwards means the totals restarted (a reset or a new run in
// the same file); nothing is added across it, the figure and Turns are the latest
// run's, and the result is Partial (counter_reset). A token_count whose info is
// null (the first event of a thread) is not a snapshot and is ignored. Codex's
// input_tokens already includes cached_input_tokens; both are reported as their
// own counters, as the exec stream path does, and are not to be added together.
//
// Totals are per thread, not per model. With one served model (the rollout's
// turn_context records) the totals belong to it. With several there is no honest
// split, so each model is listed with Unknown counters and the result is
// Partial (model_split_unknown), as for the exec path. The format is read as the
// pinned 0.160 CLI is documented to write it; no sample of a real rollout is
// checked in or read, so the fixtures are synthetic.
type codexUsageScan struct {
	// first is the first non-empty line, kept to qualify the rollout's version.
	first                  []byte
	started                bool
	skipped, turns, resets int64
	mismatches             int64
	latest                 codexSnapshot
	have                   bool
	models                 []string
}

func (a *codexUsageScan) line(line []byte, oversize bool) {
	if !a.started && len(bytes.TrimSpace(line)) > 0 {
		a.started = true
		if !oversize {
			a.first = append([]byte(nil), line...)
		}
	}
	head := line[:min(len(line), 512)]
	isCount := bytes.Contains(head, []byte(`"token_count"`))
	isContext := bytes.Contains(head[:min(len(head), 256)], rolloutTurnContext)
	if !isCount && !isContext {
		return
	}
	if oversize || !utf8.Valid(line) || !protocol.StrictPortalJSON(line) || !portalCanonicalFields(line, "type", "payload") {
		a.skipped++
		return
	}
	var record struct {
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	if json.Unmarshal(line, &record) != nil || !portalCanonicalFields(record.Payload, "type", "model", "info") {
		a.skipped++
		return
	}
	var payload struct {
		Type  string          `json:"type"`
		Model json.RawMessage `json:"model"`
		Info  json.RawMessage `json:"info"`
	}
	if json.Unmarshal(record.Payload, &payload) != nil {
		a.skipped++
		return
	}
	switch {
	case record.Type == "turn_context" && isContext:
		if len(payload.Model) == 0 || string(payload.Model) == "null" {
			return
		}
		var model string
		if json.Unmarshal(payload.Model, &model) != nil || !protocol.ValidUsageModelID(model) {
			a.skipped++
			return
		}
		if !slices.Contains(a.models, model) {
			a.models = append(a.models, model)
		}
	case record.Type == "event_msg" && payload.Type == "token_count":
		if len(payload.Info) == 0 || string(payload.Info) == "null" {
			return
		}
		snapshot, ok := codexTotals(payload.Info)
		if !ok {
			a.skipped++
			return
		}
		if a.have && snapshot.total() == a.latest.total() {
			return
		}
		switch {
		case a.have && snapshot.backwardsFrom(a.latest):
			a.resets++
			a.turns = 0
		case a.have && !snapshot.deltaMatches(a.latest):
			a.mismatches++
		}
		a.latest, a.have = snapshot, true
		a.turns++
	}
}

func (a *codexUsageScan) result(observed time.Time) protocol.ConversationUsage {
	unknown := func(reason string) protocol.ConversationUsage {
		return protocol.UnknownConversationUsage(protocol.AgentCodex, reason, observed)
	}
	if observed.IsZero() {
		return unknown(protocol.ReasonUnbound)
	}
	if !a.have {
		if a.skipped > 0 {
			result := unknown(protocol.ReasonSkippedRecords)
			result.Skipped = min(a.skipped, protocol.MaxConversationCount)
			return result
		}
		return unknown(protocol.ReasonNoUsage)
	}
	models := slices.Clone(a.models)
	slices.Sort(models)
	switch {
	case len(models) == 0:
		return unknown(protocol.ReasonModelUnknown)
	case len(models) > protocol.MaxConversationModels:
		return unknown(protocol.ReasonTooManyModels)
	}
	result := protocol.ConversationUsage{Version: protocol.ConversationUsageVersion, Provider: protocol.AgentCodex, ObservedAt: observed.UTC(),
		Coverage: protocol.ConversationCoverage, CounterSemantics: protocol.ConversationCumulative, ResultState: protocol.ConversationFinal,
		Turns: min(a.turns, protocol.MaxConversationCount), Skipped: min(a.skipped, protocol.MaxConversationCount)}
	for _, model := range models {
		row := protocol.ConversationModelUsage{Model: model, InputTokens: unknownAmount, OutputTokens: unknownAmount, CacheReadInputTokens: unknownAmount, CacheCreationInputTokens: unknownAmount}
		if len(models) == 1 {
			row.InputTokens, row.OutputTokens = reportedAmount(a.latest.input), reportedAmount(a.latest.output)
			if a.latest.hasCached {
				row.CacheReadInputTokens = reportedAmount(a.latest.cached)
			}
		}
		result.Models = append(result.Models, row)
	}
	switch {
	case len(models) > 1:
		result.ResultState, result.ReasonCode = protocol.ConversationPartial, protocol.ReasonModelSplit
	case a.resets > 0:
		result.ResultState, result.ReasonCode = protocol.ConversationPartial, protocol.ReasonCounterReset
	case a.mismatches > 0:
		result.ResultState, result.ReasonCode = protocol.ConversationPartial, protocol.ReasonDeltaMismatch
	case a.skipped > 0:
		result.ResultState, result.ReasonCode = protocol.ConversationPartial, protocol.ReasonSkippedRecords
	}
	return result
}

// codexConversationUsage runs the scan over lines already in memory. It is the
// pure form the fixture tests use.
func codexConversationUsage(raw []byte, observed time.Time) protocol.ConversationUsage {
	scan := &codexUsageScan{}
	if err := scanTranscriptLines(bytes.NewReader(raw), nil, func(line []byte, oversize, _ bool) { scan.line(line, oversize) }); err != nil || len(raw) > conversationUsageScanLimit {
		return protocol.UnknownConversationUsage(protocol.AgentCodex, protocol.ReasonUnbound, observed)
	}
	return scan.result(observed)
}

// codexTotals reads info.total_token_usage, and info.last_token_usage when it is
// there, and nothing else of the event.
func codexTotals(info json.RawMessage) (codexSnapshot, bool) {
	var s codexSnapshot
	if !portalCanonicalFields(info, "total_token_usage", "last_token_usage") {
		return s, false
	}
	var wrapper struct {
		Total json.RawMessage `json:"total_token_usage"`
		Last  json.RawMessage `json:"last_token_usage"`
	}
	if json.Unmarshal(info, &wrapper) != nil {
		return s, false
	}
	in, out, cached, hasCached, ok := codexCounters(wrapper.Total)
	if !ok {
		return s, false
	}
	s = codexSnapshot{input: in, output: out, cached: cached, hasCached: hasCached}
	if len(wrapper.Last) > 0 && string(wrapper.Last) != "null" {
		li, lo, lc, hasLC, ok := codexCounters(wrapper.Last)
		if !ok {
			return codexSnapshot{}, false
		}
		s.lastInput, s.lastOutput, s.lastCached, s.hasLast, s.hasLastCached = li, lo, lc, true, hasLC
	}
	return s, true
}

func codexCounters(raw json.RawMessage) (in, out, cached int64, hasCached, ok bool) {
	if !portalCanonicalFields(raw, "input_tokens", "output_tokens", "cached_input_tokens") {
		return 0, 0, 0, false, false
	}
	var c struct {
		Input  json.RawMessage `json:"input_tokens"`
		Output json.RawMessage `json:"output_tokens"`
		Cached json.RawMessage `json:"cached_input_tokens"`
	}
	if json.Unmarshal(raw, &c) != nil {
		return 0, 0, 0, false, false
	}
	var inOK, outOK, cachedOK bool
	in, inOK = countOf(c.Input)
	out, outOK = countOf(c.Output)
	cached, hasCached, cachedOK = optionalCount(c.Cached)
	return in, out, cached, hasCached, inOK && outOK && cachedOK
}
