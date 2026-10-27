package session

import (
	"bytes"
	"encoding/json"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/web/portal"
)

// decodeSessionActivity accepts exactly the operator's view=activity object.
// lastActivity is the one optional key; a null, an unknown key or a word outside
// its fixed set makes the whole read unavailable. Usage covers task sessions
// only, so any other mode must carry an Unknown usage scope, which in turn
// carries no reported value.
func decodeSessionActivity(raw json.RawMessage, now time.Time) (apiv1.PortalSessionActivity, error) {
	var a apiv1.PortalSessionActivity
	obj, err := jsonObject(raw)
	if err != nil {
		return a, err
	}
	if !exactKeys(obj, []string{"mode", "agentState", "requestedModel", "usage"}, "lastActivity") {
		return a, portal.ErrUnavailable
	}
	mode, ok1 := jsonString(obj["mode"])
	state, ok2 := jsonString(obj["agentState"])
	model, ok3 := jsonString(obj["requestedModel"])
	if !ok1 || !ok2 || !ok3 || !oneOf(mode, "task", "local", "remote", protocol.UsageUnknown) ||
		!oneOf(state, protocol.AgentPending, protocol.AgentBusy, protocol.AgentIdle, protocol.AgentWaiting, protocol.AgentExited, protocol.AgentFailed, protocol.AgentInterrupted, protocol.UsageUnknown) ||
		(model != protocol.UsageUnknown && !protocol.ValidUsageModelID(model)) {
		return a, portal.ErrUnavailable
	}
	if rawTime, ok := obj["lastActivity"]; ok {
		at, err := observationTime(rawTime, now)
		if err != nil {
			return a, err
		}
		a.LastActivity = &at
	}
	usage, err := decodeSessionUsage(obj["usage"])
	if err != nil {
		return a, err
	}
	if mode != "task" && usage.Scope != protocol.UsageUnknown {
		return a, portal.ErrUnavailable
	}
	a.Mode, a.AgentState, a.RequestedModel, a.Usage = mode, state, model, usage
	return a, nil
}

// decodeSessionUsage keeps Unknown distinct from an explicit zero. An Unknown
// scope must carry no reported value.
func decodeSessionUsage(raw json.RawMessage) (apiv1.PortalSessionUsage, error) {
	var u apiv1.PortalSessionUsage
	obj, err := jsonObject(raw)
	if err != nil {
		return u, err
	}
	if !exactKeys(obj, []string{"scope", "resultState", "servedModels", "inputTokens", "outputTokens", "cacheReadInputTokens", "cacheCreationInputTokens", "cost"}) {
		return u, portal.ErrUnavailable
	}
	scope, ok1 := jsonString(obj["scope"])
	result, ok2 := jsonString(obj["resultState"])
	if !ok1 || !ok2 || !oneOf(scope, protocol.UsageInitial, protocol.UsageUnknown) || !oneOf(result, "Final", "Partial", protocol.UsageUnknown) {
		return u, portal.ErrUnavailable
	}
	models, ok := jsonArray(obj["servedModels"])
	if !ok || len(models) > protocol.MaxServedModels {
		return u, portal.ErrUnavailable
	}
	u.ServedModels = make([]string, 0, len(models))
	seen := map[string]struct{}{}
	for _, raw := range models {
		model, ok := jsonString(raw)
		if _, dup := seen[model]; !ok || dup || !protocol.ValidUsageModelID(model) {
			return u, portal.ErrUnavailable
		}
		seen[model] = struct{}{}
		u.ServedModels = append(u.ServedModels, model)
	}
	var okTokens [4]bool
	u.InputTokens, okTokens[0] = decodeTokens(obj["inputTokens"])
	u.OutputTokens, okTokens[1] = decodeTokens(obj["outputTokens"])
	u.CacheReadInputTokens, okTokens[2] = decodeTokens(obj["cacheReadInputTokens"])
	u.CacheCreationInputTokens, okTokens[3] = decodeTokens(obj["cacheCreationInputTokens"])
	cost, okCost := decodeCost(obj["cost"])
	if okTokens != [4]bool{true, true, true, true} || !okCost {
		return u, portal.ErrUnavailable
	}
	u.Scope, u.ResultState, u.Cost = scope, result, cost
	if scope == protocol.UsageUnknown {
		for _, m := range []protocol.TokenObservation{u.InputTokens, u.OutputTokens, u.CacheReadInputTokens, u.CacheCreationInputTokens} {
			if m.State != protocol.UsageUnknown {
				return u, portal.ErrUnavailable
			}
		}
		if result != protocol.UsageUnknown || cost.State != protocol.UsageUnknown || len(u.ServedModels) != 0 {
			return u, portal.ErrUnavailable
		}
	} else if result == protocol.UsageUnknown {
		return u, portal.ErrUnavailable
	}
	return u, nil
}

func decodeTokens(raw json.RawMessage) (protocol.TokenObservation, bool) {
	var m protocol.TokenObservation
	obj, err := jsonObject(raw)
	if err != nil || !exactKeys(obj, []string{"state"}, "amount") {
		return m, false
	}
	state, ok := jsonString(obj["state"])
	if !ok {
		return m, false
	}
	m.State = state
	if rawAmount, present := obj["amount"]; present {
		n, ok := jsonInt(rawAmount)
		if !ok {
			return m, false
		}
		amount := int64(n)
		m.Amount = &amount
	}
	return m, m.Valid()
}

func decodeCost(raw json.RawMessage) (protocol.CostObservation, bool) {
	var c protocol.CostObservation
	obj, err := jsonObject(raw)
	if err != nil || !exactKeys(obj, []string{"state", "kind"}, "amount", "unit") {
		return c, false
	}
	var ok [4]bool
	c.State, ok[0] = jsonString(obj["state"])
	c.Kind, ok[1] = jsonString(obj["kind"])
	ok[2], ok[3] = true, true
	if rawAmount, present := obj["amount"]; present {
		c.Amount, ok[2] = jsonString(rawAmount)
	}
	if rawUnit, present := obj["unit"]; present {
		c.Unit, ok[3] = jsonString(rawUnit)
	}
	return c, ok == [4]bool{true, true, true, true} && c.Valid()
}

// exactKeys reports whether obj has every required key and nothing beyond the optional ones.
func exactKeys(obj map[string]json.RawMessage, required []string, optional ...string) bool {
	for _, key := range required {
		if _, ok := obj[key]; !ok {
			return false
		}
	}
	for key := range obj {
		if !oneOf(key, required...) && !oneOf(key, optional...) {
			return false
		}
	}
	return true
}

// jsonString accepts one JSON string; null and every other type are refused.
func jsonString(raw json.RawMessage) (string, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	var value any
	if dec.Decode(&value) != nil {
		return "", false
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return "", false
	}
	text, ok := value.(string)
	return text, ok
}

func oneOf(value string, set ...string) bool {
	for _, item := range set {
		if value == item {
			return true
		}
	}
	return false
}

// sessionGroups are the Sessions list groups, in display order.
var sessionGroups = []portal.ProductGroup{{Title: "Needs attention", Attention: true}, {Title: "In progress"}, {Title: "Stopped"}, {Title: "State Unknown"}}

// sessionGroup puts a stopped or failed phase first. agentd's last reported
// state outlives the pod, so after a suspend, an archive or a failure it is
// history, not what the session is doing now.
func sessionGroup(phase string, a *apiv1.PortalSessionActivity) int {
	switch phase {
	case "Failed":
		return 0
	case "Suspended", "Archived":
		return 2
	}
	state := protocol.UsageUnknown
	if a != nil {
		state = a.AgentState
	}
	switch state {
	case protocol.AgentWaiting, protocol.AgentFailed, protocol.AgentInterrupted:
		return 0
	case protocol.AgentBusy, protocol.AgentPending, protocol.AgentIdle:
		return 1
	case protocol.AgentExited:
		return 2
	}
	switch phase {
	case "Running", "Pending", "Idle":
		return 1
	case "Draining":
		return 2
	}
	return 3
}

// sessionsPage builds the Sessions page: groups ordered by attention, then
// most recent activity, and the Usage view. It counts sessions; it never adds
// token or cost values.
func sessionsPage(sessions []apiv1.PortalSessionSummary, complete bool, freshness string, now time.Time) portal.ProductPage {
	page := portal.ProductPage{Title: "Sessions", ListTitle: "All sessions", Connected: true, Complete: complete}
	switch {
	case !complete:
		page.Notice, page.ListCount = "Sessions read is incomplete. This is not the full inventory.", "Partial list"
	case freshness != "observed":
		// Stopped sessions never heartbeat, so a stale page is normal. It is
		// not a banner; each session's detail shows its own freshness.
		page.Notice, page.Quiet = "Session freshness is "+freshness+".", true
	case len(sessions) == 0:
		page.Notice = "The completed session read returned no dev-env sessions."
	default:
		page.Notice, page.Quiet = "Connected session summaries.", true
	}
	ordered := append([]apiv1.PortalSessionSummary(nil), sessions...)
	sort.SliceStable(ordered, func(i, j int) bool {
		gi, gj := sessionGroup(ordered[i].Phase, ordered[i].Activity), sessionGroup(ordered[j].Phase, ordered[j].Activity)
		if gi != gj {
			return gi < gj
		}
		ti, tj := lastActivity(ordered[i]), lastActivity(ordered[j])
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return ordered[i].Name < ordered[j].Name
	})
	page.Groups = make([]portal.ProductGroup, len(sessionGroups))
	copy(page.Groups, sessionGroups)
	usage := &portal.ProductUsage{
		Scope: "Initial invocations only", ScopeNote: "Later turns, interactive and remote sessions, and account usage are not captured.",
		Columns: []string{"Session", "Input", "Output", "Cache read", "Cache create", "Est. cost"},
		Coverage: []portal.ProductField{
			{Label: "Measured", Value: "First managed invocation of task sessions"},
			{Label: "Not measured", Value: "Interactive and remote sessions, later turns, account allowance"},
			{Label: "Served model", Value: "Provider reported, where measured"},
			{Label: "Cache read tokens", Value: "Separate counter; can overlap input; never added"},
			{Label: "Cost", Value: "CLI-equivalent list-price estimate; not a charge"},
			{Label: "Totals", Value: "None computed"},
		},
	}
	measured, running, attention := 0, 0, 0
	for i, s := range ordered {
		id := "s" + strconv.Itoa(i+1)
		row := sessionRow(s, now)
		row.ID = id
		g := sessionGroup(s.Phase, s.Activity)
		page.Groups[g].Rows = append(page.Groups[g].Rows, row)
		switch g {
		case 0:
			attention++
		case 1:
			running++
		}
		usageRow, ok := sessionUsageRow(id, s)
		if ok {
			measured++
		}
		usage.Rows = append(usage.Rows, usageRow)
	}
	if complete && len(sessions) > 0 {
		page.ListCount = strconv.Itoa(len(sessions)) + " sessions · dev-env"
	}
	countText := strconv.Itoa(measured) + " of " + strconv.Itoa(len(sessions))
	runningText, attentionText := strconv.Itoa(running), strconv.Itoa(attention)
	if !complete {
		// A partial read is not a count of the inventory.
		countText = strconv.Itoa(measured) + " listed"
		runningText, attentionText = runningText+" listed", attentionText+" listed"
	}
	usage.Stats = []portal.ProductStat{
		{Label: "Measured sessions", Value: countText, Note: "Task sessions' first invocation"},
		{Label: "In progress", Value: runningText, Note: "Sessions listed in progress"},
		{Label: "Needs attention", Value: attentionText, Note: "Failed, waiting or interrupted"},
	}
	page.Usage = usage
	return page
}

func lastActivity(s apiv1.PortalSessionSummary) time.Time {
	if s.Activity == nil || s.Activity.LastActivity == nil {
		return time.Time{}
	}
	return *s.Activity.LastActivity
}

// sessionUsageRow is one Usage table row. A session without measured usage
// keeps every cell Unknown; it is never listed as zero.
func sessionUsageRow(id string, s apiv1.PortalSessionSummary) (portal.ProductUsageRow, bool) {
	a := activityOrUnknown(s.Activity)
	u := a.Usage
	row := portal.ProductUsageRow{ID: id, Name: shown(s.Name), Sub: "requested model Unknown"}
	if a.RequestedModel != protocol.UsageUnknown {
		row.Sub = a.RequestedModel + " requested"
	}
	cell := func(label, value string) portal.ProductUsageCell {
		return portal.ProductUsageCell{Label: label, Value: value, Unknown: value == "Unknown"}
	}
	measured := u.Scope == protocol.UsageInitial
	if measured && len(u.ServedModels) > 0 {
		row.Sub += " · served " + servedModelsText(u.ServedModels)
	}
	row.Cells = []portal.ProductUsageCell{
		cell("Input", tokenText(u.InputTokens)), cell("Output", tokenText(u.OutputTokens)),
		cell("Cache read", tokenText(u.CacheReadInputTokens)), cell("Cache create", tokenText(u.CacheCreationInputTokens)),
		cell("Est. cost", shortCost(u.Cost)),
	}
	return row, measured
}

func activityOrUnknown(a *apiv1.PortalSessionActivity) *apiv1.PortalSessionActivity {
	if a != nil {
		return a
	}
	return &apiv1.PortalSessionActivity{Mode: protocol.UsageUnknown, AgentState: protocol.UsageUnknown, RequestedModel: protocol.UsageUnknown, Usage: unknownSessionUsage()}
}

// sessionRow renders one summary as a list item and its detail. Requested and
// served models stay separate, each counter keeps Unknown apart from zero, and
// nothing is summed. A row without activity renders every activity field Unknown.
func sessionRow(row apiv1.PortalSessionSummary, now time.Time) portal.ProductRow {
	a := activityOrUnknown(row.Activity)
	u := a.Usage
	mode, model := a.Mode, a.RequestedModel
	if mode == protocol.UsageUnknown {
		mode = "mode Unknown"
	}
	if model == protocol.UsageUnknown {
		model = "model Unknown"
	}
	active := "activity Unknown"
	if a.LastActivity != nil {
		active = "active " + portal.Ago(*a.LastActivity, now)
	}
	out := portal.ProductRow{
		Title: shown(row.Name), MonoTitle: true, Kicker: "dev-env", Crumb: shown(row.Name),
		Status: sessionStatus(row.Phase, a.AgentState), Description: model + " · " + mode,
		Age: compactAgo(a.LastActivity, now), Badges: []string{mode, model, active}, ContextTitle: "Session context",
	}
	if row.Freshness != "observed" {
		out.Badges = append(out.Badges, "freshness "+shown(row.Freshness))
	}
	usage := portal.ProductSection{Title: "Usage"}
	if u.Scope == protocol.UsageInitial {
		out.Meta = "In " + compactTokens(u.InputTokens) + " · Out " + compactTokens(u.OutputTokens)
		usage.Metrics = []portal.ProductField{
			{Label: "Input", Value: tokenText(u.InputTokens)}, {Label: "Output", Value: tokenText(u.OutputTokens)},
			{Label: "Cache read", Value: tokenText(u.CacheReadInputTokens)}, {Label: "Cache create", Value: tokenText(u.CacheCreationInputTokens)},
		}
		usage.Fields = []portal.ProductField{
			{Label: "Served model (provider reported)", Value: servedModelsText(u.ServedModels), Mono: len(u.ServedModels) > 0},
			{Label: "Requested model", Value: shown(a.RequestedModel), Mono: a.RequestedModel != protocol.UsageUnknown},
			{Label: "Cost estimate", Value: costText(u.Cost)},
			{Label: "Scope", Value: usageScopeText(u)},
		}
		usage.Note = "Counters are reported separately; cache reads can overlap input. Nothing is added."
	}
	lastSeen := activityTime(a.LastActivity)
	if a.LastActivity != nil {
		lastSeen = portal.Ago(*a.LastActivity, now) + " · " + a.LastActivity.UTC().Format("2 Jan 2006, 15:04 UTC")
	}
	out.Sections = []portal.ProductSection{{Title: "Activity", Fields: []portal.ProductField{
		{Label: "Last reported agent state", Value: shown(a.AgentState)},
		{Label: "Last activity", Value: lastSeen},
		{Label: "Phase", Value: shown(row.Phase)},
		{Label: "Freshness", Value: shown(row.Freshness)},
	}}}
	// Only a task session's first invocation is measured. Others get no
	// Usage panel rather than a panel of Unknowns.
	if u.Scope == protocol.UsageInitial {
		out.Sections = append([]portal.ProductSection{usage}, out.Sections...)
	}
	out.Context = []portal.ProductField{
		{Label: "Project", Value: "dev-env"},
		{Label: "Mode", Value: shown(a.Mode)},
		{Label: "Requested model", Value: shown(a.RequestedModel), Mono: a.RequestedModel != protocol.UsageUnknown},
		{Label: "UID", Value: shown(row.UID), Mono: true},
	}
	return out
}

// sessionStatus shows a stopped or failed phase first, then agentd's state
// word, then the phase.
func sessionStatus(phase, agent string) portal.ProductStatus {
	switch phase {
	case "Failed":
		return portal.ProductStatus{Text: "Failed", Tone: "bad"}
	case "Suspended", "Archived":
		return portal.ProductStatus{Text: phase, Tone: "muted"}
	}
	switch agent {
	case protocol.AgentBusy:
		return portal.ProductStatus{Text: "Busy", Tone: "busy"}
	case protocol.AgentWaiting:
		return portal.ProductStatus{Text: "Waiting", Tone: "wait"}
	case protocol.AgentIdle:
		return portal.ProductStatus{Text: "Idle", Tone: "muted"}
	case protocol.AgentPending:
		return portal.ProductStatus{Text: "Starting", Tone: "muted"}
	case protocol.AgentExited:
		return portal.ProductStatus{Text: "Exited", Tone: "muted"}
	case protocol.AgentFailed:
		return portal.ProductStatus{Text: "Failed", Tone: "bad"}
	case protocol.AgentInterrupted:
		return portal.ProductStatus{Text: "Interrupted", Tone: "bad"}
	}
	tone := map[string]string{"Running": "ok", "Pending": "muted", "Idle": "muted", "Draining": "wait", "Failed": "bad"}[phase]
	return portal.ProductStatus{Text: shown(phase), Tone: tone}
}

// compactAgo is the list's short age: 2m, 3h, 4d, now or Unknown.
func compactAgo(at *time.Time, now time.Time) string {
	if at == nil {
		return "Unknown"
	}
	text := portal.Ago(*at, now)
	if text == "just now" {
		return "now"
	}
	return strings.TrimSuffix(text, " ago")
}

// compactTokens shortens one reported counter for the list; Unknown stays Unknown.
func compactTokens(m protocol.TokenObservation) string {
	if m.State != protocol.UsageReported || !m.Valid() {
		return "Unknown"
	}
	n := *m.Amount
	switch {
	case n < 1000:
		return strconv.FormatInt(n, 10)
	case n < 1_000_000:
		return strconv.FormatFloat(float64(n)/1000, 'f', 1, 64) + "k"
	default:
		return strconv.FormatFloat(float64(n)/1_000_000, 'f', 1, 64) + "M"
	}
}

func shortCost(c protocol.CostObservation) string {
	if c.State != protocol.UsageReported || !c.Valid() {
		return "Unknown"
	}
	return c.Amount + " USD"
}

func activityTime(at *time.Time) string {
	if at == nil || at.IsZero() {
		return "Unknown"
	}
	return at.UTC().Format(time.RFC3339)
}

func servedModelsText(models []string) string {
	if len(models) == 0 {
		return "Unknown"
	}
	return strings.Join(models, ", ")
}

func usageScopeText(u apiv1.PortalSessionUsage) string {
	if u.Scope != protocol.UsageInitial {
		return "Unknown"
	}
	if u.ResultState == "Partial" {
		return "First managed invocation (partial result)"
	}
	return "First managed invocation"
}

func tokenText(m protocol.TokenObservation) string {
	if m.State != protocol.UsageReported || !m.Valid() {
		return "Unknown"
	}
	return groupDigits(strconv.FormatInt(*m.Amount, 10))
}

func costText(c protocol.CostObservation) string {
	if c.State != protocol.UsageReported || !c.Valid() {
		return "Unknown"
	}
	return c.Amount + " USD (CLI-equivalent estimate, not a charge)"
}

// groupDigits writes a non-negative decimal integer with thousands separators.
func groupDigits(digits string) string {
	var b strings.Builder
	for i, r := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// unknownSessionUsage is usage with nothing measured.
func unknownSessionUsage() apiv1.PortalSessionUsage {
	unknown := protocol.TokenObservation{State: protocol.UsageUnknown}
	return apiv1.PortalSessionUsage{Scope: protocol.UsageUnknown, ResultState: protocol.UsageUnknown, ServedModels: []string{},
		InputTokens: unknown, OutputTokens: unknown, CacheReadInputTokens: unknown, CacheCreationInputTokens: unknown,
		Cost: protocol.CostObservation{State: protocol.UsageUnknown, Kind: protocol.UsageUnknown}}
}
