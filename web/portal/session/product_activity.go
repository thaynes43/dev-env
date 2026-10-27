package session

import (
	"bytes"
	"encoding/json"
	"io"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/web/portal"
)

// decodeSessionActivity accepts exactly the operator's view=activity object.
// lastActivity, provider, launchedBy and taskResult are optional (an operator
// before 2026-10-27 sends none of the last three); a null, an unknown key or a
// word outside its fixed set makes the whole read unavailable. Usage covers
// task sessions only, so any other mode must carry an Unknown usage scope,
// which in turn carries no reported value, and no task result.
func decodeSessionActivity(raw json.RawMessage, now time.Time) (apiv1.PortalSessionActivity, error) {
	var a apiv1.PortalSessionActivity
	obj, err := jsonObject(raw)
	if err != nil {
		return a, err
	}
	if !exactKeys(obj, []string{"mode", "agentState", "requestedModel", "usage"}, "lastActivity", "provider", "launchedBy", "taskResult") {
		return a, portal.ErrUnavailable
	}
	a.Provider = protocol.UsageUnknown
	if raw, ok := obj["provider"]; ok {
		if a.Provider, ok = jsonString(raw); !ok || !oneOf(a.Provider, "claude", "codex", "opencode", protocol.UsageUnknown) {
			return a, portal.ErrUnavailable
		}
	}
	if raw, ok := obj["launchedBy"]; ok {
		if a.LaunchedBy, ok = jsonString(raw); !ok || !launcherName.MatchString(a.LaunchedBy) {
			return a, portal.ErrUnavailable
		}
	}
	if raw, ok := obj["taskResult"]; ok {
		if a.TaskResult, ok = jsonString(raw); !ok || !oneOf(a.TaskResult, "Succeeded", "Failed", "TimedOut", protocol.UsageUnknown) {
			return a, portal.ErrUnavailable
		}
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
	if mode != "task" && (usage.Scope != protocol.UsageUnknown || a.TaskResult != "") {
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

// attentionWindow is how long a failure stays in Needs attention. An older one
// is history: it is listed under Stopped and still shows its failed badge.
const attentionWindow = 24 * time.Hour

// maxListedSessions bounds the rows one Sessions page renders, so the page
// stays far under the 1 MiB response bound at the 500-session read cap. Rail
// counts, Usage stats and breakdowns still cover every filtered session.
const maxListedSessions = 150

var (
	// launcherName is spec.parent's shape: a session name, a namespace/name or
	// a ServiceAccount reference, at most 253 characters.
	launcherName = regexp.MustCompile(apiv1.PortalLauncherPattern)
	// projectName is spec.repo's shape.
	projectName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)
)

// validProject reports whether name is a repository name the portal shows.
func validProject(name string) bool {
	return projectName.MatchString(name) && name != "." && name != ".."
}

// sessionDriver is how a session runs. Interactive: remote or local, a person
// drives it. Headless: a task, run unattended, often launched by a
// coordinator session. Anything else is Unknown.
func sessionDriver(mode string) string {
	switch mode {
	case "remote", "local":
		return "Interactive"
	case "task":
		return "Headless"
	}
	return protocol.UsageUnknown
}

// sessionGroup files a session by what it needs now. A task's own result comes
// first: a Failed phase only says the pod ended, and agentd's last state
// outlives the pod. A failure is Needs attention for attentionWindow, then
// Stopped. A suspended or archived session is Stopped.
//
// When a failure happened: a task ends at its last activity. A pod that ended
// stops heartbeating, so its last heartbeat can be later than its last
// activity (an idle interactive session whose pod dies). A suspended session
// can keep heartbeating, so a heartbeat never dates a task's end. A failure
// with neither time is not recent: it is listed under Stopped, still red.
func sessionGroup(s apiv1.PortalSessionSummary, now time.Time) int {
	a := activityOrUnknown(s.Activity)
	var taskAt, podAt time.Time
	if a.LastActivity != nil {
		taskAt, podAt = *a.LastActivity, *a.LastActivity
	}
	if s.Freshness != "unknown" && s.ObservedAt.After(podAt) {
		podAt = s.ObservedAt
	}
	failed := func(at time.Time) int {
		if !at.IsZero() && now.Sub(at) <= attentionWindow {
			return 0
		}
		return 2
	}
	switch a.TaskResult {
	case "Failed", "TimedOut":
		return failed(taskAt)
	case "Succeeded":
		return 2
	}
	switch s.Phase {
	case "Failed":
		return failed(podAt)
	case "Suspended", "Archived":
		return 2
	}
	switch a.AgentState {
	case protocol.AgentWaiting:
		return 0
	case protocol.AgentFailed, protocol.AgentInterrupted:
		return failed(podAt)
	case protocol.AgentBusy, protocol.AgentPending, protocol.AgentIdle:
		return 1
	case protocol.AgentExited:
		return 2
	}
	switch s.Phase {
	case "Running", "Pending", "Idle":
		return 1
	case "Draining":
		return 2
	}
	return 3
}

// sessionsPage builds the Sessions page for one filter. The rail counts cover
// the whole read; the list and the Usage view cover the filtered sessions.
// Usage sums reported values per group only: Unknown is never counted as zero
// and cache reads stay a separate subset of input.
func sessionsPage(sessions []apiv1.PortalSessionSummary, complete bool, freshness string, now time.Time, filter portal.ProductFilter) portal.ProductPage {
	page := portal.ProductPage{Title: "Sessions", ListTitle: "All sessions", Connected: true, Complete: complete, Filter: filter}
	group := make([]int, len(sessions))
	projects := map[string]int{}
	attention := 0
	for i, s := range sessions {
		group[i] = sessionGroup(s, now)
		if group[i] == 0 {
			attention++
		}
		projects[s.Repo]++
	}
	page.Rail.All, page.Rail.Attention = strconv.Itoa(len(sessions)), strconv.Itoa(attention)
	for name, n := range projects {
		page.Rail.Projects = append(page.Rail.Projects, portal.ProductCount{Name: name, Count: strconv.Itoa(n)})
	}
	sort.Slice(page.Rail.Projects, func(i, j int) bool {
		ni, _ := strconv.Atoi(page.Rail.Projects[i].Count)
		nj, _ := strconv.Atoi(page.Rail.Projects[j].Count)
		if ni != nj {
			return ni > nj
		}
		return page.Rail.Projects[i].Name < page.Rail.Projects[j].Name
	})
	type entry struct {
		s     apiv1.PortalSessionSummary
		group int
	}
	var kept []entry
	for i, s := range sessions {
		if (filter.Attention && group[i] != 0) || (filter.Project != "" && s.Repo != filter.Project) {
			continue
		}
		kept = append(kept, entry{s, group[i]})
	}
	scope := "all projects"
	switch {
	case filter.Attention:
		page.ListTitle, scope = "Needs attention", "needs attention"
	case filter.Project != "":
		page.ListTitle, scope = filter.Project, filter.Project
	}
	switch {
	case !complete:
		page.Notice, page.ListCount = "Sessions read is incomplete. This is not the full inventory.", "Partial list"
	case len(kept) == 0 && filter.Attention:
		page.Notice = "Nothing needs attention."
	case len(kept) == 0 && filter.Project != "":
		page.Notice = "No sessions in " + filter.Project + "."
	case freshness != "observed":
		// Stopped sessions never heartbeat, so a stale page is normal. It is
		// not a banner; each session's detail shows its own freshness.
		page.Notice, page.Quiet = "Session freshness is "+freshness+".", true
	case len(sessions) == 0:
		page.Notice = "The completed session read returned no sessions."
	default:
		page.Notice, page.Quiet = "Connected session summaries.", true
	}
	if complete && len(kept) > 0 {
		page.ListCount = strconv.Itoa(len(kept)) + " sessions · " + scope
	}
	if len(kept) > maxListedSessions {
		page.ListCount = "Showing " + strconv.Itoa(maxListedSessions) + " of " + strconv.Itoa(len(kept)) + " sessions · " + scope
		if !complete {
			page.ListCount = "Partial list"
		}
	}
	sort.SliceStable(kept, func(i, j int) bool {
		if kept[i].group != kept[j].group {
			return kept[i].group < kept[j].group
		}
		ti, tj := lastActivity(kept[i].s), lastActivity(kept[j].s)
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return kept[i].s.Name < kept[j].s.Name
	})
	page.Groups = make([]portal.ProductGroup, len(sessionGroups))
	copy(page.Groups, sessionGroups)
	for _, e := range kept {
		page.Groups[e.group].Total++
	}
	usage := &portal.ProductUsage{
		Scope: "Initial invocations only", ScopeNote: "Headless task sessions' first invocation is measured. Interactive sessions and later turns are not.",
		Allowance: []portal.ProductField{
			{Label: "Claude", Value: "Not connected"},
			{Label: "Codex", Value: "Not connected"},
		},
		Columns: []string{"Session", "Input", "Output", "Cache read", "Cache create", "Est. cost"},
		Coverage: []portal.ProductField{
			{Label: "Measured", Value: "First managed invocation of headless task sessions"},
			{Label: "Not measured", Value: "Interactive sessions, later turns"},
			{Label: "Account allowance", Value: "Separate; no allowance collector is connected"},
			{Label: "Served model", Value: "Provider reported, where measured"},
			{Label: "Cache read tokens", Value: "A subset that can overlap input; never added to it"},
			{Label: "Cost", Value: "CLI-equivalent list-price estimate, not a charge; Unavailable where the provider reports none"},
			{Label: "Totals", Value: "Sums of reported values per group; Unknown is never counted as zero"},
		},
	}
	// The session the request selects always gets a row. Past the cap it takes
	// the last listed row's place, so the listing stays maxListedSessions long.
	selected, drop := -1, -1
	for i, e := range kept {
		if filter.Session != "" && e.s.UID == filter.Session {
			selected = i
			break
		}
	}
	if selected >= maxListedSessions {
		drop = maxListedSessions - 1
	}
	byDriver := map[string]*usageTotal{"Interactive": {}, "Headless": {}, protocol.UsageUnknown: {}}
	byModel, byProject := map[string]*usageTotal{}, map[string]*usageTotal{}
	measured, interactive, headless := 0, 0, 0
	for i, e := range kept {
		id := "s" + strconv.Itoa(i+1)
		usageRow, ok := sessionUsageRow(id, e.s)
		if ok {
			measured++
		}
		// Only the first maxListedSessions, in group then recency order, get a
		// row; every session counts in the stats and breakdowns below.
		if (i < maxListedSessions && i != drop) || i == selected {
			row := sessionRow(e.s, now)
			row.ID = id
			page.Groups[e.group].Rows = append(page.Groups[e.group].Rows, row)
			usage.Rows = append(usage.Rows, usageRow)
		}
		a := activityOrUnknown(e.s.Activity)
		driver := sessionDriver(a.Mode)
		switch driver {
		case "Interactive":
			interactive++
		case "Headless":
			headless++
		}
		modelKey := shown(a.Provider) + " · " + shown(a.RequestedModel)
		for _, t := range []*usageTotal{byDriver[driver], totalFor(byModel, modelKey), totalFor(byProject, e.s.Repo)} {
			t.add(a.Usage)
		}
	}
	countText := strconv.Itoa(measured) + " of " + strconv.Itoa(len(kept))
	interactiveText, headlessText := strconv.Itoa(interactive), strconv.Itoa(headless)
	if !complete {
		// A partial read is not a count of the inventory.
		countText = strconv.Itoa(measured) + " listed"
		interactiveText, headlessText = interactiveText+" listed", headlessText+" listed"
	}
	usage.Stats = []portal.ProductStat{
		{Label: "Measured sessions", Value: countText, Note: "Headless tasks' first invocation"},
		{Label: "Interactive", Value: interactiveText, Note: "Remote or local; a person drives them. Not measured"},
		{Label: "Headless", Value: headlessText, Note: "Tasks, often launched by a coordinator"},
	}
	columns := func(first string) []string {
		return []string{first, "Sessions", "Measured", "Input", "Output", "Cache read", "Cache create", "Est. cost"}
	}
	driverTable := portal.ProductUsageTable{Title: "By how sessions ran", Note: "Interactive sessions are listed but not measured.", Columns: columns("How it ran")}
	for _, name := range []string{"Interactive", "Headless", protocol.UsageUnknown} {
		if t := byDriver[name]; t.sessions > 0 {
			driverTable.Rows = append(driverTable.Rows, t.row(name))
		}
	}
	usage.Breakdowns = append(usage.Breakdowns, driverTable,
		portal.ProductUsageTable{Title: "By provider and requested model", Columns: columns("Provider · model"), Rows: totalRows(byModel)})
	if filter.Project == "" {
		usage.Breakdowns = append(usage.Breakdowns, portal.ProductUsageTable{Title: "By project", Columns: columns("Project"), Rows: totalRows(byProject)})
	}
	if len(kept) > maxListedSessions {
		usage.RowsNote = "The " + strconv.Itoa(maxListedSessions) + " sessions listed on the left. The breakdowns above cover all " + strconv.Itoa(len(kept)) + "."
	}
	page.Usage = usage
	return page
}

// usageTotal is one breakdown group: how many sessions, how many measured,
// and the sums of the values their providers reported.
type usageTotal struct {
	sessions, measured, costMissing   int
	input, output, cacheRead, created tokenSum
	cost                              *big.Rat
}

// tokenSum is a sum of reported counters and how many were Unknown. It is a
// big.Int: one counter may be as large as protocol.MaxUsageTokens.
type tokenSum struct {
	sum     big.Int
	unknown int
}

func (t *tokenSum) add(m protocol.TokenObservation) {
	if m.State == protocol.UsageReported && m.Amount != nil && *m.Amount >= 0 {
		t.sum.Add(&t.sum, big.NewInt(*m.Amount))
		return
	}
	t.unknown++
}

func totalFor(m map[string]*usageTotal, key string) *usageTotal {
	if m[key] == nil {
		m[key] = &usageTotal{}
	}
	return m[key]
}

// add counts one session. Only a measured session adds counters or cost; a
// cost that is not a reported USD estimate is counted as unavailable.
func (t *usageTotal) add(u apiv1.PortalSessionUsage) {
	t.sessions++
	if u.Scope != protocol.UsageInitial {
		return
	}
	t.measured++
	t.input.add(u.InputTokens)
	t.output.add(u.OutputTokens)
	t.cacheRead.add(u.CacheReadInputTokens)
	t.created.add(u.CacheCreationInputTokens)
	if amount, ok := new(big.Rat).SetString(u.Cost.Amount); ok && u.Cost.State == protocol.UsageReported && u.Cost.Unit == "USD" && amount.Sign() >= 0 {
		if t.cost == nil {
			t.cost = new(big.Rat)
		}
		t.cost.Add(t.cost, amount)
		return
	}
	t.costMissing++
}

// row is the group's table row. A group with nothing measured shows Not
// measured, not zero.
func (t *usageTotal) row(name string) portal.ProductUsageRow {
	cell := func(label, value string, unknown bool) portal.ProductUsageCell {
		return portal.ProductUsageCell{Label: label, Value: value, Unknown: unknown}
	}
	tokens := func(label string, s *tokenSum) portal.ProductUsageCell {
		switch {
		case t.measured == 0:
			return cell(label, "Not measured", true)
		case s.unknown == t.measured:
			return cell(label, "Unknown", true)
		}
		v := groupDigits(s.sum.String())
		if s.unknown > 0 {
			v += " (" + strconv.Itoa(s.unknown) + " Unknown)"
		}
		return cell(label, v, false)
	}
	cost := cell("Est. cost", "Unavailable", true)
	if t.cost != nil {
		cost = cell("Est. cost", usdText(t.cost), false)
		if t.costMissing > 0 {
			cost.Value += " (" + strconv.Itoa(t.costMissing) + " unavailable)"
		}
	} else if t.measured == 0 {
		cost.Value = "Not measured"
	}
	sub := strconv.Itoa(t.measured) + " of " + strconv.Itoa(t.sessions) + " measured"
	return portal.ProductUsageRow{Name: shown(name), Sub: sub, Cells: []portal.ProductUsageCell{
		cell("Sessions", strconv.Itoa(t.sessions), false), cell("Measured", strconv.Itoa(t.measured), t.measured == 0),
		tokens("Input", &t.input), tokens("Output", &t.output), tokens("Cache read", &t.cacheRead), tokens("Cache create", &t.created), cost,
	}}
}

// usdText is a cost sum to the cent; a positive sum under a cent is "<0.01".
func usdText(r *big.Rat) string {
	if r.Sign() > 0 && r.Cmp(big.NewRat(1, 100)) < 0 {
		return "<0.01 USD"
	}
	return r.FloatString(2) + " USD"
}

// maxBreakdownRows bounds a breakdown table; smaller groups are merged into
// one "Other" row.
const maxBreakdownRows = 20

// merge adds o's sessions, counters and costs to t.
func (t *usageTotal) merge(o *usageTotal) {
	t.sessions += o.sessions
	t.measured += o.measured
	t.costMissing += o.costMissing
	for _, p := range [][2]*tokenSum{{&t.input, &o.input}, {&t.output, &o.output}, {&t.cacheRead, &o.cacheRead}, {&t.created, &o.created}} {
		p[0].sum.Add(&p[0].sum, &p[1].sum)
		p[0].unknown += p[1].unknown
	}
	if o.cost != nil {
		if t.cost == nil {
			t.cost = new(big.Rat)
		}
		t.cost.Add(t.cost, o.cost)
	}
}

// totalRows orders groups by session count, then name, and merges any beyond
// maxBreakdownRows-1 into one "Other" row.
func totalRows(m map[string]*usageTotal) []portal.ProductUsageRow {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if m[names[i]].sessions != m[names[j]].sessions {
			return m[names[i]].sessions > m[names[j]].sessions
		}
		return names[i] < names[j]
	})
	rows := make([]portal.ProductUsageRow, 0, min(len(names), maxBreakdownRows))
	other := &usageTotal{}
	for i, name := range names {
		if len(names) > maxBreakdownRows && i >= maxBreakdownRows-1 {
			other.merge(m[name])
			continue
		}
		rows = append(rows, m[name].row(name))
	}
	if other.sessions > 0 {
		rows = append(rows, other.row("Other ("+strconv.Itoa(len(names)-maxBreakdownRows+1)+" groups)"))
	}
	return rows
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

// sessionRow renders one summary as a list item and its detail: its project,
// how it runs (interactive or headless), who launched it and a task's own
// result. Requested and served models stay separate, each counter keeps
// Unknown apart from zero, and nothing is summed. A row without activity
// renders every activity field Unknown.
func sessionRow(row apiv1.PortalSessionSummary, now time.Time) portal.ProductRow {
	a := activityOrUnknown(row.Activity)
	u := a.Usage
	driver, model := sessionDriver(a.Mode), a.RequestedModel
	if driver == protocol.UsageUnknown {
		driver = "mode Unknown"
	}
	if model == protocol.UsageUnknown {
		model = "model Unknown"
	}
	active := "activity Unknown"
	if a.LastActivity != nil {
		active = "active " + portal.Ago(*a.LastActivity, now)
	}
	out := portal.ProductRow{
		Title: shown(row.Name), MonoTitle: true, Kicker: shown(row.Repo), Crumb: shown(row.Name),
		Status: sessionStatus(row.Phase, a), Description: model + " · " + driver,
		Age: compactAgo(a.LastActivity, now), Badges: []string{driver, model, active}, ContextTitle: "Session context",
	}
	if a.LaunchedBy != "" {
		out.Badges = append(out.Badges, "launched by "+shown(a.LaunchedBy))
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
	activity := []portal.ProductField{
		{Label: "Last reported agent state", Value: shown(a.AgentState)},
		{Label: "Last activity", Value: lastSeen},
		{Label: "Phase", Value: shown(row.Phase)},
		{Label: "Freshness", Value: shown(row.Freshness)},
	}
	if a.TaskResult != "" {
		activity = append([]portal.ProductField{{Label: "Task result", Value: shown(a.TaskResult)}}, activity...)
	}
	out.Sections = []portal.ProductSection{{Title: "Activity", Fields: activity}}
	// Only a task session's first invocation is measured. Others get no
	// Usage panel rather than a panel of Unknowns.
	if u.Scope == protocol.UsageInitial {
		out.Sections = append([]portal.ProductSection{usage}, out.Sections...)
	}
	runs := sessionDriver(a.Mode)
	if runs != protocol.UsageUnknown {
		runs += " (" + a.Mode + ")"
	}
	out.Context = []portal.ProductField{
		{Label: "Project", Value: shown(row.Repo)},
		{Label: "How it runs", Value: runs},
		{Label: "Launched by", Value: shown(a.LaunchedBy), Mono: a.LaunchedBy != ""},
		{Label: "Provider", Value: shown(a.Provider)},
		{Label: "Requested model", Value: shown(a.RequestedModel), Mono: a.RequestedModel != protocol.UsageUnknown},
		{Label: "UID", Value: shown(row.UID), Mono: true},
	}
	out.Messages = sessionMessages(row, a)
	return out
}

// sessionStatus shows a task's own result first, then a stopped or failed
// phase, then agentd's state word, then the phase.
func sessionStatus(phase string, a *apiv1.PortalSessionActivity) portal.ProductStatus {
	switch a.TaskResult {
	case "Succeeded":
		return portal.ProductStatus{Text: "Succeeded", Tone: "muted"}
	case "Failed":
		return portal.ProductStatus{Text: "Task failed", Tone: "bad"}
	case "TimedOut":
		return portal.ProductStatus{Text: "Timed out", Tone: "bad"}
	}
	switch phase {
	case "Failed":
		return portal.ProductStatus{Text: "Failed", Tone: "bad"}
	case "Suspended", "Archived":
		return portal.ProductStatus{Text: phase, Tone: "muted"}
	}
	switch a.AgentState {
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
