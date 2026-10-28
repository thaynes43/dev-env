package session

import (
	"encoding/json"
	"sort"
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

// conversationReasons words the reason codes a Partial result can carry, as
// one plain sentence each. Any other code shows as itself, escaped by the
// template.
var conversationReasons = map[string]string{
	protocol.ReasonSkippedRecords:  "Some records could not be read and are not counted.",
	protocol.ReasonCounterReset:    "The provider's counters restarted, so only the latest run is counted.",
	protocol.ReasonModelSplit:      "Several models share one provider total, so each model's counts are Unknown.",
	protocol.ReasonMissingCounters: "Some records lacked a counter that others carried.",
	protocol.ReasonDeltaMismatch:   "A rise in the provider's running total did not match its per-call figure.",
}

// staleMinutes is the stale threshold in whole minutes, for the page's text.
var staleMinutes = strconv.Itoa(int(conversationUsageStaleAfter / time.Minute))

// conversationStates are the panel's two state lines: how complete the count
// is, and how fresh. Partial and Stale are amber, the others muted. A Partial
// line names its reason and says the counts are a lower bound; a Stale line says
// the figure may be behind. Neither hides the figure.
func conversationStates(c *apiv1.PortalConversationUsage, now time.Time) []portal.ProductState {
	result := portal.ProductState{Status: portal.ProductStatus{Text: "Final", Tone: "muted"}, Text: "Every record was counted, up to the measured time."}
	if c.ResultState == protocol.ConversationPartial {
		reason := conversationReasons[c.ReasonCode]
		if reason == "" {
			reason = "Reason: " + shown(c.ReasonCode) + "."
		}
		result = portal.ProductState{Status: portal.ProductStatus{Text: "Partial", Tone: "wait"}, Text: reason + " The counts shown are a lower bound."}
	}
	fresh := portal.ProductState{Status: portal.ProductStatus{Text: "Current", Tone: "muted"}, Text: "Measured within the last " + staleMinutes + " minutes."}
	if conversationStale(c, now) {
		fresh = portal.ProductState{Status: portal.ProductStatus{Text: "Stale", Tone: "wait"}, Text: "Older than " + staleMinutes + " minutes, so it may be behind the conversation."}
	}
	return []portal.ProductState{result, fresh}
}

// conversationUsageSection is a session's measured conversation usage as a
// detail panel: the two state lines, a table with one row per served model,
// when it was measured, the provider calls counted and the records skipped.
// Every counter keeps Reported apart from Unknown, and nothing is added: not
// cache to input, not one model to another. It shows no price, and nothing here
// is an allowance or a charge.
func conversationUsageSection(a *apiv1.PortalSessionActivity, now time.Time) (portal.ProductSection, bool) {
	c := a.ConversationUsage
	if c == nil {
		return portal.ProductSection{}, false
	}
	table := &portal.ProductUsageTable{Title: "Token counts by served model", Columns: []string{"Served model", "Input", "Output", "Cache read", "Cache create"}}
	cell := func(label string, m protocol.TokenObservation) portal.ProductUsageCell {
		v := tokenText(m)
		return portal.ProductUsageCell{Label: label, Value: v, Unknown: v == "Unknown"}
	}
	for _, m := range c.Models {
		table.Rows = append(table.Rows, portal.ProductUsageRow{Name: shown(m.Model), Cells: []portal.ProductUsageCell{
			cell("Input", m.InputTokens), cell("Output", m.OutputTokens), cell("Cache read", m.CacheReadInputTokens), cell("Cache create", m.CacheCreationInputTokens)}})
	}
	adding := "Each counter stands alone; none is added to another."
	if a.Provider == protocol.AgentCodex {
		adding = "Codex input already includes cache reads, so the two are never added."
	}
	return portal.ProductSection{
		Title:  "Conversation usage",
		States: conversationStates(c, now),
		Table:  table,
		Fields: []portal.ProductField{
			{Label: "Measured", Value: portal.Ago(c.ObservedAt, now) + " · " + c.ObservedAt.UTC().Format("2 Jan 2006, 15:04 UTC")},
			{Label: "Provider calls counted", Value: groupDigits(strconv.FormatInt(c.Turns, 10))},
			{Label: "Records skipped", Value: groupDigits(strconv.FormatInt(c.Skipped, 10))},
		},
		Note: "Provider-reported token counts for this session's registered conversation, cumulative to the measured time. " + adding + " Not a cost or a charge.",
	}, true
}

// conversationMeta is the list item's short line for a measured conversation:
// one model's input and output, or how many models, then partial and stale.
func conversationMeta(c *apiv1.PortalConversationUsage, now time.Time) string {
	meta := "Conversation: " + strconv.Itoa(len(c.Models)) + " models"
	if len(c.Models) == 1 {
		meta = "Conversation: In " + compactTokens(c.Models[0].InputTokens) + " · Out " + compactTokens(c.Models[0].OutputTokens)
	}
	if c.ResultState == protocol.ConversationPartial {
		meta += " · partial"
	}
	if conversationStale(c, now) {
		meta += " · stale"
	}
	return meta
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
	// The row's own line says what its Partial and Stale counts mean.
	var flags []string
	if t.partial > 0 {
		flags = append(flags, "Lower bound")
	}
	if t.stale > 0 {
		flags = append(flags, "May be behind")
	}
	return portal.ProductUsageRow{Name: shown(name), Sub: strings.Join(flags, " · "), Cells: []portal.ProductUsageCell{
		cell("Sessions", strconv.Itoa(t.sessions), false),
		tokens("Input", &t.input), tokens("Output", &t.output), tokens("Cache read", &t.cacheRead), tokens("Cache create", &t.created),
		cell("Partial", strconv.Itoa(t.partial), false), cell("Stale", strconv.Itoa(t.stale), false),
	}}
}

// conversationView rewrites the Usage view's task-only wording for a page that
// holds at least one conversation figure, and puts the conversation table first,
// apart from the initial-invocation tables, which name their own scope.
// conversations is how many sessions in the view carry a figure; an incomplete
// read says "listed". Nothing here adds a conversation figure to an initial
// invocation, or one provider's counts to another's.
func conversationView(usage *portal.ProductUsage, totals map[string]*conversationTotal, conversations int, complete bool) {
	usage.Scope = "Initial invocations and conversations"
	usage.ScopeNote = "A headless task's first invocation and an interactive session's registered conversation are counted apart and never added. Later task turns are not measured."
	with := strconv.Itoa(conversations)
	if !complete {
		with += " listed"
	}
	usage.Stats[0].Note = "Headless tasks' first invocation only"
	usage.Stats[1].Note = "Remote or local; a person drives them. " + with + " with conversation usage"
	usage.Breakdowns[0].Note = "Interactive sessions show Not measured here. Their conversation usage is in the table above."
	usage.RowsTitle = "Initial invocations by session"
	usage.Coverage = []portal.ProductField{
		{Label: "Measured", Value: "First managed invocation of headless task sessions"},
		{Label: "Conversations", Value: "Provider-reported token counts of an interactive session's registered conversation, cumulative to its last measurement. Older than " + staleMinutes + " minutes is marked stale and may be behind"},
		{Label: "Not measured", Value: "Later turns of headless tasks, and interactive sessions that report no conversation figure, such as those on an older agent"},
		{Label: "Account allowance", Value: "Separate; no allowance collector is connected"},
		{Label: "Served model", Value: "Provider reported, where measured"},
		{Label: "Cache read tokens", Value: "A subset that can overlap input; never added to it. Codex input already includes them"},
		{Label: "Cost", Value: "Initial invocations: CLI-equivalent list-price estimate, not a charge; Unavailable where the provider reports none. Conversations: no price is shown"},
		{Label: "Totals", Value: "Sums of reported values per group; Unknown is never counted as zero. Claude and Codex are never added together"},
	}
	table := portal.ProductUsageTable{Title: "Conversation usage by provider and served model",
		Note:    "Provider-reported token counts from each interactive session's registered conversation, as last measured. A row sums its sessions' reported counts; Unknown is never counted as zero. Rows are never added together, and Codex input already includes cache reads. A row with a Partial session is a lower bound. Stale: measured more than " + staleMinutes + " minutes ago.",
		Columns: []string{"Provider · model", "Sessions", "Input", "Output", "Cache read", "Cache create", "Partial", "Stale"}}
	names := make([]string, 0, len(totals))
	for name := range totals {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		table.Rows = append(table.Rows, totals[name].row(name))
	}
	usage.Breakdowns = append([]portal.ProductUsageTable{table}, usage.Breakdowns...)
}
