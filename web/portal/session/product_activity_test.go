package session

import (
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/web/portal"
)

// unknownActivity is a view=activity object with nothing measured.
const unknownActivity = `"activity":{"mode":"remote","agentState":"idle","requestedModel":"claude-opus-5-5","usage":{"scope":"Unknown","resultState":"Unknown","servedModels":[],"inputTokens":{"state":"Unknown"},"outputTokens":{"state":"Unknown"},"cacheReadInputTokens":{"state":"Unknown"},"cacheCreationInputTokens":{"state":"Unknown"},"cost":{"state":"Unknown","kind":"Unknown"}}}`

const reportedActivity = `"activity":{"mode":"task","agentState":"busy","lastActivity":"2026-10-25T11:58:00Z","requestedModel":"claude-opus-5-5","usage":{"scope":"InitialManagedInvocation","resultState":"Partial","servedModels":["claude-haiku-4-5","claude-opus-5-5"],"inputTokens":{"state":"Reported","amount":1234567},"outputTokens":{"state":"Reported","amount":0},"cacheReadInputTokens":{"state":"Reported","amount":5000},"cacheCreationInputTokens":{"state":"Unknown"},"cost":{"state":"Reported","amount":"0.75","unit":"USD","kind":"ProviderCLIEquivalentEstimate"}}}`

func activityPage(rows ...string) string {
	return `{"sessions":[` + strings.Join(rows, ",") + `],"limit":50,"complete":true,"freshness":"observed","observedAt":"2026-10-25T11:59:00Z"}`
}

func activityRowPhase(name, phase, activity string) string {
	return strings.Replace(activityRow(name, activity), `"phase":"Running"`, `"phase":"`+phase+`"`, 1)
}

func activityRow(name, activity string) string {
	return `{"name":"` + name + `","uid":"uid-` + name + `","repo":"dev-env","phase":"Running","observedAt":"2026-10-25T11:59:00Z","freshness":"observed",` + activity + `}`
}

// rowFields collects a row's detail values by label: tiles, panels and context.
func rowFields(row portal.ProductRow) map[string]string {
	out := map[string]string{}
	for _, section := range row.Sections {
		for _, f := range append(append([]portal.ProductField{}, section.Metrics...), section.Fields...) {
			out[f.Label] = f.Value
		}
	}
	for _, f := range row.Context {
		out[f.Label] = f.Value
	}
	return out
}

func findRow(t *testing.T, page portal.ProductPage, title string) portal.ProductRow {
	t.Helper()
	for _, g := range page.Groups {
		for _, row := range g.Rows {
			if row.Title == title {
				return row
			}
		}
	}
	t.Fatalf("no row %q", title)
	return portal.ProductRow{}
}

func usageRow(t *testing.T, page portal.ProductPage, name string) map[string]portal.ProductUsageCell {
	t.Helper()
	for _, row := range page.Usage.Rows {
		if row.Name == name {
			out := map[string]portal.ProductUsageCell{}
			for _, c := range row.Cells {
				out[c.Label] = c
			}
			return out
		}
	}
	t.Fatalf("no usage row %q", name)
	return nil
}

func TestSessionActivityRendersSeparateUsageWithoutTotals(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	decoded, err := decodeSessionPage([]byte(activityPage(activityRow("worker", reportedActivity), activityRow("remote", unknownActivity))), now)
	if err != nil || len(decoded.Sessions) != 2 {
		t.Fatalf("page=%+v err=%v", decoded, err)
	}
	page := sessionsPage(decoded.Sessions, true, "observed", now, portal.ProductFilter{})
	worker := findRow(t, page, "worker")
	for label, want := range map[string]string{
		"Last reported agent state":        "busy",
		"Last activity":                    "2m ago · 25 Oct 2026, 11:58 UTC",
		"How it runs":                      "Headless (task)",
		"Requested model":                  "claude-opus-5-5",
		"Served model (provider reported)": "claude-haiku-4-5, claude-opus-5-5",
		"Scope":                            "First managed invocation (partial result)",
		"Input":                            "1,234,567",
		"Output":                           "0",
		"Cache read":                       "5,000",
		"Cache create":                     "Unknown",
		"Cost estimate":                    "0.75 USD (CLI-equivalent list-price estimate, not a charge)",
		"UID":                              "uid-worker",
	} {
		if got := rowFields(worker)[label]; got != want {
			t.Errorf("worker %s = %q, want %q", label, got, want)
		}
	}
	if worker.Meta != "In 1.2M · Out 0" || worker.Status != (portal.ProductStatus{Text: "Busy", Tone: "busy"}) || !worker.MonoTitle {
		t.Fatalf("worker list item %+v", worker)
	}
	remote := findRow(t, page, "remote")
	fields := rowFields(remote)
	if fields["Last activity"] != "Unknown" {
		t.Errorf("remote Last activity = %q, want Unknown", fields["Last activity"])
	}
	for _, label := range []string{"Scope", "Input", "Output", "Cache read", "Cache create", "Cost estimate"} {
		if _, ok := fields[label]; ok {
			t.Errorf("remote shows %s although nothing was measured", label)
		}
	}
	for _, section := range remote.Sections {
		if section.Title == "Usage" {
			t.Errorf("remote has a Usage panel although nothing was measured: %+v", section)
		}
	}
	if fields["Requested model"] != "claude-opus-5-5" || fields["How it runs"] != "Interactive (remote)" || remote.Meta != "" {
		t.Fatalf("remote %+v", fields)
	}
	w := usageRow(t, page, "worker")
	if w["Input"].Value != "1,234,567" || w["Output"].Value != "0" || w["Output"].Unknown || w["Cache read"].Value != "5,000" || !w["Cache create"].Unknown || w["Est. cost"].Value != "0.75 USD" {
		t.Fatalf("worker usage cells %+v", w)
	}
	for label, cell := range usageRow(t, page, "remote") {
		if cell.Value != "Unknown" || !cell.Unknown {
			t.Errorf("remote usage %s = %+v, want Unknown", label, cell)
		}
	}
	if page.Usage.Stats[0].Value != "1 of 2" || page.Usage.Stats[1].Label != "Interactive" || page.Usage.Stats[1].Value != "1" || page.Usage.Stats[2].Label != "Headless" || page.Usage.Stats[2].Value != "1" {
		t.Fatalf("usage stats %+v", page.Usage.Stats)
	}
	// Usage is its own view (?view=usage): the Sessions view carries no embedded
	// Usage section, so the Usage facts are read from the standalone view.
	page.ObservedAt = "2026-10-25T12:00:00Z"
	sessionsBody, err := portal.RenderProduct(page)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sessionsBody), `id="usage"`) || strings.Contains(string(sessionsBody), "Initial invocations only") {
		t.Error("the Sessions view embeds the Usage section")
	}
	usagePage := sessionsPage(decoded.Sessions, true, "observed", now, portal.ProductFilter{View: "usage"})
	usagePage.ObservedAt = "2026-10-25T12:00:00Z"
	if usagePage.Usage == nil || usagePage.Usage.Stats[0].Value != "1 of 2" {
		t.Fatalf("usage view model %+v", usagePage.Usage)
	}
	body, err := portal.RenderProduct(usagePage)
	if err != nil {
		t.Fatal(err)
	}
	html := string(body)
	for _, want := range []string{"Initial invocations only", "Claude reports them apart from input; Codex input already includes them. Never added to input", "not a charge", "Unknown is never counted as zero", `href="` + SessionsPath + `?session=uid-worker"`, `id="usage"`, "Account allowance", "Not connected", "By how sessions ran", "By provider and requested model", "By project"} {
		if !strings.Contains(html, want) {
			t.Errorf("rendered Usage view lacks %q", want)
		}
	}
	// Cache reads are a subset of input and are never added to it.
	for _, total := range []string{"1,239,567", "1239567", "1,234,567 total", "Total tokens", "Fixture preview"} {
		if strings.Contains(html, total) || strings.Contains(string(sessionsBody), total) {
			t.Fatalf("rendered %q", total)
		}
	}
	partial := sessionsPage(decoded.Sessions, false, "observed", now, portal.ProductFilter{})
	if partial.Complete || partial.ListCount != "Partial list" || partial.Usage.Stats[0].Value != "1 listed" || !strings.HasSuffix(partial.Usage.Stats[1].Value, " listed") || !strings.HasSuffix(partial.Usage.Stats[2].Value, " listed") {
		t.Fatalf("incomplete read was counted: %+v", partial)
	}
	disconnected, err := portal.RenderProduct(portal.ProductPage{Title: "Sessions", Notice: "Sessions unavailable", Usage: page.Usage, Groups: page.Groups})
	if err != nil || strings.Contains(string(disconnected), "Initial invocations only") || strings.Contains(string(disconnected), "worker") || !strings.Contains(string(disconnected), "Sessions unavailable") {
		t.Fatalf("disconnected page showed rows or usage: %s", disconnected)
	}
}

// A stopped or failed phase outranks agentd's last reported state, which
// outlives the pod; that state stays visible in Activity as history.
func TestSessionPhaseOutranksLastAgentState(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	state := func(word string) string {
		return strings.Replace(unknownActivity, `"agentState":"idle"`, `"agentState":"`+word+`"`, 1)
	}
	for _, test := range []struct {
		name, phase, agent, group, badge, tone string
	}{
		{"suspended-idle", "Suspended", "idle", "Stopped", "Suspended", "muted"},
		{"archived-busy", "Archived", "busy", "Stopped", "Archived", "muted"},
		{"failed-exited", "Failed", "exited", "Needs attention", "Failed", "bad"},
		{"failed-busy", "Failed", "busy", "Needs attention", "Failed", "bad"},
		{"running-busy", "Running", "busy", "In progress", "Busy", "busy"},
		{"running-waiting", "Running", "waiting", "Needs attention", "Waiting", "wait"},
		{"draining-busy", "Draining", "busy", "In progress", "Busy", "busy"},
	} {
		t.Run(test.name, func(t *testing.T) {
			decoded, err := decodeSessionPage([]byte(activityPage(activityRowPhase(test.name, test.phase, state(test.agent)))), now)
			if err != nil {
				t.Fatal(err)
			}
			page := sessionsPage(decoded.Sessions, true, "observed", now, portal.ProductFilter{})
			var group string
			for _, g := range page.Groups {
				if len(g.Rows) == 1 {
					group = g.Title
				}
			}
			row := findRow(t, page, test.name)
			fields := rowFields(row)
			if group != test.group || row.Status != (portal.ProductStatus{Text: test.badge, Tone: test.tone}) || fields["Last reported agent state"] != test.agent || fields["Phase"] != test.phase {
				t.Fatalf("group %q status %+v fields %v", group, row.Status, fields)
			}
		})
	}
}

// Sessions are grouped by what they need, then by most recent activity.
func TestSessionsPageGroupsByAttention(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	waiting := strings.Replace(unknownActivity, `"agentState":"idle"`, `"agentState":"waiting"`, 1)
	exited := strings.Replace(reportedActivity, `"agentState":"busy"`, `"agentState":"exited"`, 1)
	decoded, err := decodeSessionPage([]byte(activityPage(activityRow("a-busy", reportedActivity), activityRow("b-exited", exited), activityRow("c-waiting", waiting), legacyRow("d-legacy"))), now)
	if err != nil {
		t.Fatal(err)
	}
	page := sessionsPage(decoded.Sessions, true, "observed", now, portal.ProductFilter{})
	var got []string
	for _, g := range page.Groups {
		for _, row := range g.Rows {
			got = append(got, g.Title+":"+row.Title+":"+row.ID)
		}
	}
	want := []string{"Needs attention:c-waiting:s1", "In progress:a-busy:s2", "In progress:d-legacy:s3", "Stopped:b-exited:s4"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("groups %v, want %v", got, want)
	}
	if !page.Groups[0].Attention || page.Notice != "Connected session summaries." || !page.Quiet {
		t.Fatalf("page %+v", page)
	}
}

func TestSessionActivityDecoderFailsClosed(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	good := activityRow("worker", reportedActivity)
	if _, err := decodeSessionPage([]byte(activityPage(good)), now); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"activity null":            strings.Replace(good, reportedActivity, `"activity":null`, 1),
		"activity not object":      strings.Replace(good, reportedActivity, `"activity":"busy"`, 1),
		"extra row key":            strings.Replace(good, `"phase":"Running"`, `"phase":"Running","prompt":"SECRET"`, 1),
		"legacy row extra key":     strings.Replace(legacyRow("worker"), `"phase":"Running"`, `"phase":"Running","prompt":"SECRET"`, 1),
		"legacy row missing a key": strings.Replace(legacyRow("worker"), `"freshness":"observed"`, `"freshnessX":"observed"`, 1),
		// Usage covers task sessions only: another mode cannot carry a measured scope.
		"remote mode with reported usage":  strings.Replace(good, `"mode":"task"`, `"mode":"remote"`, 1),
		"local mode with reported usage":   strings.Replace(good, `"mode":"task"`, `"mode":"local"`, 1),
		"Unknown mode with reported usage": strings.Replace(good, `"mode":"task"`, `"mode":"Unknown"`, 1),
	}
	mutate := map[string][2]string{
		"activity extra key":         {`"mode":"task"`, `"mode":"task","prompt":"SECRET"`},
		"mode outside set":           {`"mode":"task"`, `"mode":"admin"`},
		"mode null":                  {`"mode":"task"`, `"mode":null`},
		"agent state case":           {`"agentState":"busy"`, `"agentState":"Busy"`},
		"model empty":                {`"requestedModel":"claude-opus-5-5"`, `"requestedModel":""`},
		"model alias with space":     {`"requestedModel":"claude-opus-5-5"`, `"requestedModel":"opus latest"`},
		"model missing":              {`"requestedModel":"claude-opus-5-5",`, ``},
		"last activity null":         {`"lastActivity":"2026-10-25T11:58:00Z"`, `"lastActivity":null`},
		"last activity future":       {`"lastActivity":"2026-10-25T11:58:00Z"`, `"lastActivity":"2026-10-25T13:00:00Z"`},
		"usage extra key":            {`"scope":"InitialManagedInvocation"`, `"scope":"InitialManagedInvocation","total":1239567`},
		"usage null":                 {`"usage":{"scope"`, `"usage":null,"x":{"scope"`},
		"scope outside set":          {`"scope":"InitialManagedInvocation"`, `"scope":"AllTurns"`},
		"result outside set":         {`"resultState":"Partial"`, `"resultState":"Done"`},
		"measured result Unknown":    {`"resultState":"Partial"`, `"resultState":"Unknown"`},
		"served models null":         {`"servedModels":["claude-haiku-4-5","claude-opus-5-5"]`, `"servedModels":null`},
		"served models duplicate":    {`"servedModels":["claude-haiku-4-5","claude-opus-5-5"]`, `"servedModels":["claude-opus-5-5","claude-opus-5-5"]`},
		"served model not string":    {`"servedModels":["claude-haiku-4-5","claude-opus-5-5"]`, `"servedModels":[5]`},
		"served models over bound":   {`"servedModels":["claude-haiku-4-5","claude-opus-5-5"]`, `"servedModels":["a","b","c","d","e","f","g","h","i"]`},
		"reported without amount":    {`"inputTokens":{"state":"Reported","amount":1234567}`, `"inputTokens":{"state":"Reported"}`},
		"reported amount null":       {`"inputTokens":{"state":"Reported","amount":1234567}`, `"inputTokens":{"state":"Reported","amount":null}`},
		"negative amount":            {`"inputTokens":{"state":"Reported","amount":1234567}`, `"inputTokens":{"state":"Reported","amount":-1}`},
		"amount over bound":          {`"inputTokens":{"state":"Reported","amount":1234567}`, `"inputTokens":{"state":"Reported","amount":1000000000000000001}`},
		"fractional amount":          {`"inputTokens":{"state":"Reported","amount":1234567}`, `"inputTokens":{"state":"Reported","amount":1.5}`},
		"unknown with amount":        {`"cacheCreationInputTokens":{"state":"Unknown"}`, `"cacheCreationInputTokens":{"state":"Unknown","amount":0}`},
		"token extra key":            {`"cacheCreationInputTokens":{"state":"Unknown"}`, `"cacheCreationInputTokens":{"state":"Unknown","note":"x"}`},
		"token state outside set":    {`"cacheCreationInputTokens":{"state":"Unknown"}`, `"cacheCreationInputTokens":{"state":"Zero"}`},
		"cost billed kind":           {`"kind":"ProviderCLIEquivalentEstimate"`, `"kind":"Billed"`},
		"cost other unit":            {`"unit":"USD"`, `"unit":"EUR"`},
		"cost amount null":           {`"amount":"0.75"`, `"amount":null`},
		"cost amount not decimal":    {`"amount":"0.75"`, `"amount":"$0.75"`},
		"cost unknown with amount":   {`"cost":{"state":"Reported"`, `"cost":{"state":"Unknown"`},
		"unknown scope with reports": {`"scope":"InitialManagedInvocation"`, `"scope":"Unknown"`},
	}
	for name, m := range mutate {
		if !strings.Contains(good, m[0]) {
			t.Fatalf("%s: fixture lacks %s", name, m[0])
		}
		cases[name] = strings.Replace(good, m[0], m[1], 1)
	}
	unknownRow := activityRow("remote", unknownActivity)
	for name, m := range map[string][2]string{
		"unknown scope with served model": {`"servedModels":[]`, `"servedModels":["claude-opus-5-5"]`},
		"unknown scope with zero":         {`"inputTokens":{"state":"Unknown"}`, `"inputTokens":{"state":"Reported","amount":0}`},
		"unknown scope with result":       {`"resultState":"Unknown"`, `"resultState":"Final"`},
		"unknown cost without kind":       {`"cost":{"state":"Unknown","kind":"Unknown"}`, `"cost":{"state":"Unknown"}`},
	} {
		cases[name] = strings.Replace(unknownRow, m[0], m[1], 1)
	}
	for name, row := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeSessionPage([]byte(activityPage(row)), now); err == nil {
				t.Fatalf("accepted %s", row)
			}
		})
	}
}

func legacyRow(name string) string {
	return `{"name":"` + name + `","uid":"uid-` + name + `","repo":"dev-env","phase":"Running","observedAt":"2026-10-25T11:59:00Z","freshness":"observed"}`
}

// A row with only the six base keys is accepted and renders every activity
// field Unknown, never zero; it can sit beside activity rows on one page.
func TestSessionLegacyRowRendersUnknownActivity(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	page, err := decodeSessionPage([]byte(activityPage(legacyRow("legacy"), activityRow("worker", reportedActivity))), now)
	if err != nil || len(page.Sessions) != 2 || page.Sessions[0].Activity != nil || page.Sessions[1].Activity == nil {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	row := sessionRow(page.Sessions[0], now)
	got := rowFields(row)
	for _, label := range []string{"Last reported agent state", "Last activity", "Requested model", "Provider", "Launched by"} {
		if got[label] != "Unknown" {
			t.Errorf("legacy %s = %q, want Unknown", label, got[label])
		}
	}
	if got["How it runs"] != "Unknown" {
		t.Errorf("legacy How it runs = %q", got["How it runs"])
	}
	for _, label := range []string{"Scope", "Input", "Output", "Cache read", "Cache create", "Cost estimate"} {
		if _, ok := got[label]; ok {
			t.Errorf("legacy row shows %s", label)
		}
	}
	if got["Phase"] != "Running" || got["Freshness"] != "observed" || got["UID"] != "uid-legacy" || row.Title != "legacy" || row.Meta != "" || row.Description != "model Unknown · mode Unknown" || row.Age != "Unknown" {
		t.Fatalf("legacy row %+v", row)
	}
}

// Non-task modes are accepted when their usage is entirely Unknown.
func TestSessionNonTaskModesAcceptUnknownUsage(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	for _, mode := range []string{"remote", "local", "Unknown"} {
		activity := strings.Replace(unknownActivity, `"mode":"remote"`, `"mode":"`+mode+`"`, 1)
		page, err := decodeSessionPage([]byte(activityPage(activityRow("s", activity))), now)
		if err != nil || page.Sessions[0].Activity.Mode != mode || page.Sessions[0].Activity.Usage.Scope != "Unknown" {
			t.Fatalf("%s: page=%+v err=%v", mode, page, err)
		}
	}
}

func TestGroupDigits(t *testing.T) {
	for in, want := range map[string]string{"0": "0", "999": "999", "1000": "1,000", "1234567": "1,234,567", "1000000000000000000": "1,000,000,000,000,000,000"} {
		if got := groupDigits(in); got != want {
			t.Errorf("groupDigits(%s) = %s, want %s", in, got, want)
		}
	}
}

// A registered Remote Control entry with the exact claude.ai link puts an
// "Open in Claude" link in the agent's header; every other state shows only
// its honest context line, and a headless or local session says it has none.
func TestSessionRemoteControlLinkAndState(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	rc := func(entry string) string {
		return strings.Replace(unknownActivity, `"mode":"remote"`, `"mode":"remote","remoteControl":`+entry, 1)
	}
	link := "https://claude.ai/code/session_ABCdef0123456789"
	local := strings.Replace(unknownActivity, `"mode":"remote"`, `"mode":"local"`, 1)
	decoded, err := decodeSessionPage([]byte(activityPage(
		activityRow("rc-registered", rc(`{"state":"registered","url":"`+link+`"}`)),
		activityRow("rc-nolink", rc(`{"state":"registered"}`)),
		activityRow("rc-pairing", rc(`{"state":"pair-code-issued"}`)),
		activityRow("rc-starting", rc(`{"state":"starting"}`)),
		activityRow("rc-other", rc(`{"state":"unregistered"}`)),
		activityRow("rc-offline", rc(`{"state":"offline"}`)),
		activityRow("rc-missing", unknownActivity),
		activityRow("task-busy", reportedActivity),
		activityRow("local-waiting", local),
	)), now)
	if err != nil {
		t.Fatal(err)
	}
	page := sessionsPage(decoded.Sessions, true, "observed", now, portal.ProductFilter{})
	for name, want := range map[string]string{
		"rc-registered": "Registered", "rc-nolink": "Registered, no link reported", "rc-pairing": "Pairing code issued (pairing not confirmed)",
		"rc-starting": "Starting", "rc-other": "unregistered", "rc-offline": "Offline", "rc-missing": "Not reported",
		"task-busy": "None (headless task)", "local-waiting": "None (local terminal)",
	} {
		row := findRow(t, page, name)
		if got := rowFields(row)["Remote Control"]; got != want {
			t.Errorf("%s: Remote Control %q, want %q", name, got, want)
		}
		wantLink := ""
		if name == "rc-registered" {
			wantLink = link
		}
		if row.Link != wantLink || (wantLink != "") != (row.LinkLabel == "Open in Claude") {
			t.Errorf("%s: link %q label %q", name, row.Link, row.LinkLabel)
		}
	}
	body, err := portal.RenderProduct(page)
	if err != nil {
		t.Fatal(err)
	}
	html := string(body)
	if strings.Count(html, `>Open in Claude<svg`) != 1 || !strings.Contains(html, `<a class="btn" href="`+link+`" rel="noreferrer">Open in Claude`) {
		t.Fatal("the registered agent's header lacks its one Open in Claude link")
	}
	// The renderer drops any header link that is not https.
	row := findRow(t, page, "rc-registered")
	row.Link = "javascript:alert(1)"
	bad, err := portal.RenderProduct(portal.ProductPage{Title: "Sessions", Connected: true, Complete: true, Groups: []portal.ProductGroup{{Title: "x", Rows: []portal.ProductRow{row}}}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(bad), "Open in Claude") || strings.Contains(string(bad), "javascript") {
		t.Fatal("a non-https header link rendered")
	}
}

// The task Usage panel says how the session's own provider counts cache reads:
// Claude apart from input, Codex inside it, and nothing when the provider is
// not reported.
func TestTaskUsageNoteNamesHowTheProviderCountsCacheReads(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	withProvider := func(p string) string {
		return strings.Replace(reportedActivity, `"mode":"task",`, `"mode":"task","provider":"`+p+`",`, 1)
	}
	decoded, err := decodeSessionPage([]byte(activityPage(activityRow("claude", withProvider("claude")), activityRow("codex", withProvider("codex")), activityRow("none", reportedActivity))), now)
	if err != nil {
		t.Fatal(err)
	}
	page := sessionsPage(decoded.Sessions, true, "observed", now, portal.ProductFilter{})
	for name, want := range map[string]string{
		"claude": "Counters are reported separately; Claude reports cache reads apart from input. Nothing is added.",
		"codex":  "Counters are reported separately; Codex input already includes cache reads. Nothing is added.",
		"none":   "Counters are reported separately. Nothing is added.",
	} {
		var note string
		for _, s := range findRow(t, page, name).Sections {
			if s.Title == "Usage" {
				note = s.Note
			}
		}
		if note != want {
			t.Errorf("%s: %q", name, note)
		}
	}
}

func TestSessionAttentionReasonsAndActions(t *testing.T) {
	now := time.Date(2026, 10, 29, 14, 0, 0, 0, time.UTC)
	recent, older := now.Add(-time.Hour), now.Add(-2*time.Hour)
	link := "https://claude.ai/code/session_fixture-247"
	for _, tc := range []struct {
		name, mode, phase, state, result, rcState, url, reason, next, when string
		commands                                                           bool
	}{
		{"waiting linked", "remote", "Running", protocol.AgentWaiting, "", "registered", link, "Waiting for your input", "", "", false},
		{"waiting no URL", "remote", "Running", protocol.AgentWaiting, "", "registered", "", "Waiting for your input", "Remote Control link not reported: answer from the session's Claude app or the workbench.", "", false},
		{"waiting no registration", "remote", "Running", protocol.AgentWaiting, "", "", "", "Waiting for your input", "Remote Control state was not reported: answer from the session's terminal on the workbench.", "", false},
		{"waiting pairing", "remote", "Running", protocol.AgentWaiting, "", "pair-code-issued", link, "Waiting for your input", "A pairing code was issued but pairing is not confirmed: answer from the session's terminal on the workbench.", "", false},
		{"waiting invalid URL", "remote", "Running", protocol.AgentWaiting, "", "registered", "https://evil.example/", "Waiting for your input", "Remote Control link not reported: answer from the session's Claude app or the workbench.", "", false},
		{"waiting local", "local", "Running", protocol.AgentWaiting, "", "registered", link, "Waiting for your input", "Answer in the session's terminal on the workbench.", "", false},
		{"waiting unknown mode", "Unknown", "Running", protocol.AgentWaiting, "", "", "", "Waiting for your input", "Check the session from the workbench.", "", false},
		{"waiting unknown mode registered", "Unknown", "Running", protocol.AgentWaiting, "", "registered", link, "Waiting for your input", "", "", false},
		{"waiting headless", "task", "Running", protocol.AgentWaiting, "Unknown", "", "", "Waiting for your input", "Check the session from the workbench.", "", false},
		{"task failed", "task", "Failed", protocol.AgentWaiting, "Failed", "", "", "Task failed", "Inspect the session from a session or the workbench; its log is kept on the shared volume at logs/fixture-agent.log.", "2h ago · 29 Oct 2026, 12:00 UTC", true},
		{"task timed out", "task", "Suspended", protocol.AgentExited, "TimedOut", "", "", "Task timed out", "Inspect the session from a session or the workbench; its log is kept on the shared volume at logs/fixture-agent.log.", "2h ago · 29 Oct 2026, 12:00 UTC", true},
		{"pod failed", "remote", "Failed", protocol.AgentWaiting, "", "registered", link, "Session pod failed", "Inspect the session from a session or the workbench; its log is kept on the shared volume at logs/fixture-agent.log.", "Last reported 1h ago · 29 Oct 2026, 13:00 UTC", true},
		{"agent failed", "remote", "Running", protocol.AgentFailed, "", "", "", "Agent stopped unexpectedly", "Inspect the session and its log from a session or the workbench.", "Last reported 1h ago · 29 Oct 2026, 13:00 UTC", true},
		{"agent interrupted", "remote", "Running", protocol.AgentInterrupted, "", "", "", "Agent stopped unexpectedly", "Inspect the session and its log from a session or the workbench.", "Last reported 1h ago · 29 Oct 2026, 13:00 UTC", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &apiv1.PortalSessionActivity{Mode: tc.mode, AgentState: tc.state, TaskResult: tc.result, LastActivity: &older, RequestedModel: "claude-opus-5-5", Usage: unknownSessionUsage()}
			if tc.rcState != "" {
				a.RemoteControl = &apiv1.PortalRemoteControl{State: tc.rcState, URL: tc.url}
			}
			s := apiv1.PortalSessionSummary{Name: "fixture-agent", UID: "uid-fixture", Repo: "dev-env", Phase: tc.phase, Freshness: "stale", ObservedAt: recent, Activity: a}
			row := sessionRow(s, now)
			panel := row.Attention
			if panel == nil {
				t.Fatal("missing attention panel")
			}
			if panel.Reason != tc.reason || panel.NextAction != tc.next || panel.When != tc.when {
				t.Fatalf("panel = %+v", panel)
			}
			if tc.name == "waiting linked" || tc.name == "waiting unknown mode registered" {
				if panel.Link != link || panel.Link != row.Link || row.LinkLabel != "Open in Claude" {
					t.Fatal("Answer link differs from existing registered URL")
				}
			} else if panel.Link != "" {
				t.Fatal("invented Answer link")
			}
			if tc.commands {
				want := "agent-run show fixture-agent"
				if tc.phase == "Running" {
					want += "|agent-run log fixture-agent"
				}
				if strings.Join(panel.Commands, "|") != want {
					t.Fatalf("commands = %v, want %s", panel.Commands, want)
				}
			}
			if strings.HasSuffix(panel.Reason, ".") {
				t.Fatal("attention reason has a trailing period")
			}
			if row.Status.Text == "Waiting" && row.Description != "Waiting for you" {
				t.Fatal("waiting row lacks subtitle")
			}
			row.ID = "s1"
			body, err := portal.RenderProduct(portal.ProductPage{Title: "Sessions", Connected: true, Groups: []portal.ProductGroup{{Title: "Needs attention", Rows: []portal.ProductRow{row}}}})
			if err != nil {
				t.Fatal(err)
			}
			text := string(body)
			if strings.Count(text, "The portal cannot show this session&#39;s conversation.") != 1 || strings.Contains(text, "Message history is unavailable") || strings.Contains(text, "<form") {
				t.Fatal("conversation gap is repeated, missing or adds authority")
			}
			if strings.Index(text, `class="attention-panel"`) > strings.Index(text, `class="work-area"`) || strings.Count(text, "<h1") != 1 {
				t.Fatal("attention is not at top, or extra h1")
			}
			if (tc.name == "waiting linked" || tc.name == "waiting unknown mode registered") && !strings.Contains(text, `class="btn primary" href="`+link+`" rel="noreferrer">Answer in Claude`) {
				t.Fatal("missing primary Answer link")
			}
		})
	}
}

func TestSessionAttentionHonorsGroupAndTimePrecedence(t *testing.T) {
	now := time.Date(2026, 10, 29, 14, 0, 0, 0, time.UTC)
	recent, old := now.Add(-time.Hour), now.Add(-25*time.Hour)
	for _, tc := range []struct {
		name, phase, state, result, freshness string
		last                                  *time.Time
		beat                                  time.Time
		attention                             bool
		when                                  string
	}{
		{"busy", "Running", protocol.AgentBusy, "", "observed", &recent, recent, false, ""},
		{"success despite failed pod", "Failed", protocol.AgentFailed, "Succeeded", "observed", &recent, recent, false, ""},
		{"suspended waiting", "Suspended", protocol.AgentWaiting, "", "observed", &recent, recent, false, ""},
		{"archived waiting", "Archived", protocol.AgentWaiting, "", "observed", &recent, recent, false, ""},
		{"old task recent heartbeat", "Failed", protocol.AgentFailed, "Failed", "observed", &old, recent, false, ""},
		{"old pod", "Failed", protocol.AgentFailed, "", "stale", &old, old, false, ""},
		{"undated failure", "Failed", protocol.AgentFailed, "", "unknown", nil, recent, false, ""},
		{"unknown heartbeat uses activity", "Failed", protocol.AgentFailed, "", "unknown", &recent, now, true, "Last reported 1h ago · 29 Oct 2026, 13:00 UTC"},
		{"later activity than heartbeat", "Failed", protocol.AgentFailed, "", "stale", &recent, old, true, "Last reported 1h ago · 29 Oct 2026, 13:00 UTC"},
		{"waiting without time", "Running", protocol.AgentWaiting, "", "unknown", nil, time.Time{}, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := apiv1.PortalSessionSummary{Name: "fixture", Repo: "dev-env", Phase: tc.phase, Freshness: tc.freshness, ObservedAt: tc.beat, Activity: &apiv1.PortalSessionActivity{Mode: "remote", AgentState: tc.state, TaskResult: tc.result, LastActivity: tc.last, Usage: unknownSessionUsage()}}
			row := sessionRow(s, now)
			if (row.Attention != nil) != tc.attention || (row.Attention != nil && row.Attention.When != tc.when) {
				t.Fatalf("panel = %+v", row.Attention)
			}
		})
	}
	if got := attentionTime(time.Time{}, now); got != "Unknown" {
		t.Fatalf("absent time = %s", got)
	}
}

func TestWaitingAttentionNamesUnregisteredRemoteState(t *testing.T) {
	now := time.Date(2026, 10, 29, 14, 0, 0, 0, time.UTC)
	for _, tc := range []struct{ state, words string }{
		{"login-required", "Remote Control needs a login"}, {"unpaired", "Remote Control is not paired"},
		{"offline", "Remote Control is offline"}, {"pair-code-issued", "A pairing code was issued but pairing is not confirmed"},
		{"errored", "Remote Control reported an error"}, {"starting", "Remote Control is starting"},
		{"not-reported", "Remote Control state was not reported"}, {"future-state", "Remote Control state future-state"},
	} {
		t.Run(tc.state, func(t *testing.T) {
			a := &apiv1.PortalSessionActivity{Mode: "remote", AgentState: protocol.AgentWaiting,
				RemoteControl: &apiv1.PortalRemoteControl{State: tc.state, URL: "https://claude.ai/code/session_fixture-247"}}
			row := sessionRow(apiv1.PortalSessionSummary{Name: "fixture", Phase: "Running", Activity: a}, now)
			want := tc.words + ": answer from the session's terminal on the workbench."
			if row.Attention == nil || row.Attention.NextAction != want || row.Attention.Link != "" || row.Link != "" {
				t.Fatalf("unregistered state offered the wrong action: %+v", row.Attention)
			}
		})
	}
}

func TestAttentionLabelsHeartbeatTimeAsLastReported(t *testing.T) {
	now := time.Date(2026, 10, 29, 14, 0, 0, 0, time.UTC)
	for _, tc := range []struct{ phase, state, reason string }{
		{"Failed", protocol.AgentIdle, "Session pod failed"},
		{"Running", protocol.AgentFailed, "Agent stopped unexpectedly"},
		{"Running", protocol.AgentInterrupted, "Agent stopped unexpectedly"},
	} {
		t.Run(tc.phase+"/"+tc.state, func(t *testing.T) {
			s := apiv1.PortalSessionSummary{Name: "fixture", Phase: tc.phase, Freshness: "observed", ObservedAt: now,
				Activity: &apiv1.PortalSessionActivity{Mode: "remote", AgentState: tc.state}}
			panel := sessionAttention(s, now, "")
			if panel == nil || panel.Reason != tc.reason || panel.When != "Last reported just now · 29 Oct 2026, 14:00 UTC" {
				t.Fatalf("heartbeat was presented as a failure time: %+v", panel)
			}
		})
	}
}

func TestAttentionLogGuidanceFollowsSessionPhase(t *testing.T) {
	now := time.Date(2026, 10, 29, 14, 0, 0, 0, time.UTC)
	ended := now.Add(-5 * time.Minute)
	for _, phase := range []string{"Running", "Failed", "Suspended", "Archived", "Pending", "Draining", "Idle", ""} {
		t.Run(phase, func(t *testing.T) {
			s := apiv1.PortalSessionSummary{Name: "fixture-task", Phase: phase, Freshness: "observed", ObservedAt: now,
				Activity: &apiv1.PortalSessionActivity{Mode: "task", AgentState: protocol.AgentExited, TaskResult: "Failed", LastActivity: &ended}}
			panel := sessionAttention(s, now, "")
			if panel == nil || panel.When != "5m ago · 29 Oct 2026, 13:55 UTC" {
				t.Fatalf("task end time changed: %+v", panel)
			}
			want := "agent-run show fixture-task"
			if phase == "Running" {
				want += "|agent-run log fixture-task"
				if strings.Contains(panel.NextAction, "shared volume") {
					t.Fatal("running session lost its live-log guidance")
				}
			} else if !strings.Contains(panel.NextAction, "its log is kept on the shared volume at logs/fixture-task.log") {
				t.Fatal("non-running session lacks the API's saved-log guidance")
			}
			if strings.Join(panel.Commands, "|") != want {
				t.Fatalf("commands %v, want %s", panel.Commands, want)
			}
		})
	}
}
