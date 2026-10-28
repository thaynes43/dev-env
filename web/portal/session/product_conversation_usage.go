package session

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/web/portal"
)

// conversationUsageStaleAfter is how old a measured conversation figure may be
// before the portal marks it stale: three of agentd's five-minute measurements.
// An interactive session keeps running and its figure is cumulative, so an old
// one is a lower bound. A session that stopped, or whose agent reports nothing
// new, keeps the last figure the operator stored (DESIGN-029).
const conversationUsageStaleAfter = 15 * time.Minute

// decodeConversationUsage accepts exactly the operator's allowlist:
// resultState (Final or Partial), models, turns, skipped, observedAt and, for a
// Partial result, reasonCode. The decoded value must pass the same Valid() the
// agentd measurement and the operator's heartbeat use, for the session's own
// provider, so a figure that agentd could not have produced is refused. A time a
// little ahead of the portal's clock (skew between the operator and the portal)
// is clamped to now rather than failing the whole read, because a figure is
// otherwise exact. Like the other optional activity keys, a malformed value makes
// the whole read unavailable.
func decodeConversationUsage(raw json.RawMessage, provider, mode string, now time.Time) (*apiv1.PortalConversationUsage, error) {
	obj, err := jsonObject(raw)
	if err != nil {
		return nil, err
	}
	if !exactKeys(obj, []string{"resultState", "models", "turns", "skipped", "observedAt"}, "reasonCode") ||
		(mode != "local" && mode != "remote") || (provider != protocol.AgentClaude && provider != protocol.AgentCodex) {
		return nil, portal.ErrUnavailable
	}
	out := &apiv1.PortalConversationUsage{}
	var ok bool
	if out.ResultState, ok = jsonString(obj["resultState"]); !ok || !oneOf(out.ResultState, protocol.ConversationFinal, protocol.ConversationPartial) {
		return nil, portal.ErrUnavailable
	}
	if rawReason, present := obj["reasonCode"]; present {
		if out.ReasonCode, ok = jsonString(rawReason); !ok {
			return nil, portal.ErrUnavailable
		}
	}
	turns, okTurns := jsonInt(obj["turns"])
	skipped, okSkipped := jsonInt(obj["skipped"])
	if !okTurns || !okSkipped {
		return nil, portal.ErrUnavailable
	}
	out.Turns, out.Skipped = int64(turns), int64(skipped)
	var observed string
	if observed, ok = jsonString(obj["observedAt"]); !ok {
		return nil, portal.ErrUnavailable
	}
	at, err := time.Parse(time.RFC3339, observed)
	if err != nil {
		return nil, portal.ErrUnavailable
	}
	if at = at.UTC(); at.After(now) {
		at = now.UTC()
	}
	out.ObservedAt = at
	rows, ok := jsonArray(obj["models"])
	if !ok || len(rows) == 0 || len(rows) > protocol.MaxConversationModels {
		return nil, portal.ErrUnavailable
	}
	out.Models = make([]apiv1.PortalConversationModelUsage, 0, len(rows))
	for _, rawRow := range rows {
		row, err := jsonObject(rawRow)
		if err != nil || !exactKeys(row, []string{"model", "inputTokens", "outputTokens", "cacheReadInputTokens", "cacheCreationInputTokens"}) {
			return nil, portal.ErrUnavailable
		}
		m := apiv1.PortalConversationModelUsage{}
		var okModel, okIn, okOut, okRead, okWrite bool
		m.Model, okModel = jsonString(row["model"])
		m.InputTokens, okIn = decodeTokens(row["inputTokens"])
		m.OutputTokens, okOut = decodeTokens(row["outputTokens"])
		m.CacheReadInputTokens, okRead = decodeTokens(row["cacheReadInputTokens"])
		m.CacheCreationInputTokens, okWrite = decodeTokens(row["cacheCreationInputTokens"])
		if !okModel || !okIn || !okOut || !okRead || !okWrite {
			return nil, portal.ErrUnavailable
		}
		out.Models = append(out.Models, m)
	}
	check := protocol.ConversationUsage{Version: protocol.ConversationUsageVersion, Provider: provider, ObservedAt: out.ObservedAt,
		Coverage: protocol.ConversationCoverage, CounterSemantics: protocol.ConversationCumulative, ResultState: out.ResultState,
		ReasonCode: out.ReasonCode, Turns: out.Turns, Skipped: out.Skipped}
	for _, m := range out.Models {
		check.Models = append(check.Models, protocol.ConversationModelUsage{Model: m.Model, InputTokens: m.InputTokens, OutputTokens: m.OutputTokens,
			CacheReadInputTokens: m.CacheReadInputTokens, CacheCreationInputTokens: m.CacheCreationInputTokens})
	}
	if !check.Valid() {
		return nil, portal.ErrUnavailable
	}
	return out, nil
}

// conversationStale reports whether a figure is older than the threshold at now.
func conversationStale(c *apiv1.PortalConversationUsage, now time.Time) bool {
	return now.Sub(c.ObservedAt) > conversationUsageStaleAfter
}

// conversationReasons words the reason codes a Partial result can carry. Any
// other word shows as itself, escaped by the template.
var conversationReasons = map[string]string{
	protocol.ReasonSkippedRecords:  "some records could not be read",
	protocol.ReasonCounterReset:    "the provider's counters restarted; only the latest run is shown",
	protocol.ReasonModelSplit:      "several models share one total; no per-model split",
	protocol.ReasonMissingCounters: "some records lacked a counter",
	protocol.ReasonDeltaMismatch:   "a rise in the total did not match the provider's per-turn figure",
}

// conversationUsageSection is a session's measured conversation usage as a
// detail panel: the state, how old the figure is (and whether that is stale),
// then each served model's counters on a line of its own. Every counter keeps
// Reported apart from Unknown, and cache counters are never added to input.
// Nothing here is a cost, an allowance or a charge. The text is a placeholder
// for the interface review.
func conversationUsageSection(a *apiv1.PortalSessionActivity, now time.Time) (portal.ProductSection, bool) {
	c := a.ConversationUsage
	if c == nil {
		return portal.ProductSection{}, false
	}
	state := c.ResultState
	if c.ResultState == protocol.ConversationPartial {
		state = "Partial: " + shown(conversationReasons[c.ReasonCode])
		if conversationReasons[c.ReasonCode] == "" {
			state = "Partial: " + shown(c.ReasonCode)
		}
	}
	age := portal.Ago(c.ObservedAt, now) + " · " + c.ObservedAt.UTC().Format("2 Jan 2006, 15:04 UTC")
	freshness := "Current"
	if conversationStale(c, now) {
		freshness = "Stale (older than " + strconv.Itoa(int(conversationUsageStaleAfter/time.Minute)) + " minutes)"
	}
	section := portal.ProductSection{Title: "Conversation usage", Fields: []portal.ProductField{
		{Label: "Result", Value: state},
		{Label: "Measured", Value: age},
		{Label: "Freshness", Value: freshness},
		{Label: "Provider calls counted", Value: strconv.FormatInt(c.Turns, 10)},
		{Label: "Records skipped", Value: strconv.FormatInt(c.Skipped, 10)},
	}}
	for _, m := range c.Models {
		section.Fields = append(section.Fields, portal.ProductField{Label: shown(m.Model), Mono: true, Value: strings.Join([]string{
			"Input " + tokenText(m.InputTokens), "Output " + tokenText(m.OutputTokens),
			"Cache read " + tokenText(m.CacheReadInputTokens), "Cache create " + tokenText(m.CacheCreationInputTokens)}, " · ")})
	}
	section.Note = "Cumulative for the registered conversation, as of the time measured. Counters are reported separately; cache reads can overlap input. Not a cost, a charge or an allowance, and separate from a headless task's first invocation."
	return section, true
}

// conversationTotal is one provider-and-model group of the Usage view: how many
// sessions reported, how many of those are stale, and the sums of the values
// they reported. Providers never share a group, so Codex's input (which already
// includes cached tokens) is never added to Claude's.
type conversationTotal struct {
	sessions, stale, partial          int
	input, output, cacheRead, created tokenSum
}

func (t *conversationTotal) add(m apiv1.PortalConversationModelUsage, partial, stale bool) {
	t.sessions++
	if stale {
		t.stale++
	}
	if partial {
		t.partial++
	}
	t.input.add(m.InputTokens)
	t.output.add(m.OutputTokens)
	t.cacheRead.add(m.CacheReadInputTokens)
	t.created.add(m.CacheCreationInputTokens)
}

func (t *conversationTotal) row(name string) portal.ProductUsageRow {
	cell := func(label, value string, unknown bool) portal.ProductUsageCell {
		return portal.ProductUsageCell{Label: label, Value: value, Unknown: unknown}
	}
	tokens := func(label string, s *tokenSum) portal.ProductUsageCell {
		if s.unknown == t.sessions {
			return cell(label, "Unknown", true)
		}
		v := groupDigits(s.sum.String())
		if s.unknown > 0 {
			v += " (" + strconv.Itoa(s.unknown) + " Unknown)"
		}
		return cell(label, v, false)
	}
	return portal.ProductUsageRow{Name: shown(name), Sub: strconv.Itoa(t.sessions) + " sessions reported", Cells: []portal.ProductUsageCell{
		cell("Sessions", strconv.Itoa(t.sessions), false),
		tokens("Input", &t.input), tokens("Output", &t.output), tokens("Cache read", &t.cacheRead), tokens("Cache create", &t.created),
		cell("Stale", strconv.Itoa(t.stale), false), cell("Partial", strconv.Itoa(t.partial), false),
	}}
}
