package session

import (
	stdhtml "html"
	"math/big"
	"regexp"
	"strconv"
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
	if strings.Count(html, `>Open in Claude<svg`) != 1 || !strings.Contains(html, `<a class="btn" href="`+link+`" target="_blank" rel="noopener noreferrer">Open in Claude`) {
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
			if (tc.name == "waiting linked" || tc.name == "waiting unknown mode registered") && !strings.Contains(text, `class="btn primary" href="`+link+`" target="_blank" rel="noopener noreferrer">Answer in Claude`) {
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

func TestAccountAllowanceNeverComesFromSessionTokensOrCost(t *testing.T) {
	now := time.Date(2026, 10, 29, 14, 0, 0, 0, time.UTC)
	count := int64(62000)
	row := apiv1.PortalSessionSummary{Name: "synthetic-worker", UID: "synthetic-worker", Repo: "fixture", Phase: "Running", Activity: &apiv1.PortalSessionActivity{
		Mode: "task", Provider: "claude", Usage: apiv1.PortalSessionUsage{Scope: protocol.UsageInitial,
			InputTokens:  protocol.TokenObservation{State: protocol.UsageReported, Amount: &count},
			OutputTokens: protocol.TokenObservation{State: protocol.UsageReported, Amount: &count},
			Cost:         protocol.CostObservation{State: protocol.UsageReported, Amount: "38.00", Unit: "USD"}},
	}}
	for _, view := range []string{"", "usage"} {
		page := sessionsPage([]apiv1.PortalSessionSummary{row}, true, "observed", now, portal.ProductFilter{View: view})
		for _, a := range append(append([]portal.ProductAllowance{}, page.Allowance...), page.Usage.Allowance...) {
			if a.Value != portal.AllowanceNotConnected || len(a.Windows) != 0 {
				t.Fatalf("usage became allowance: %+v", a)
			}
		}
		body, err := portal.RenderProduct(page)
		if err != nil {
			t.Fatal(err)
		}
		pattern := `(?s)<div class="allowance-strip">.*?</ul>(?:</details>)?</div>`
		if view == "usage" {
			pattern = `(?s)<section class="allowance".*?Plan limits.*?</section>`
		}
		block := regexp.MustCompile(pattern).FindString(string(body))
		for _, name := range []string{"Claude", "Codex", "Cursor"} {
			if !strings.Contains(block, name) {
				t.Fatalf("allowance missing %s: %s", name, block)
			}
		}
		if block == "" || strings.Count(block, "Not connected") != 3 || strings.Contains(block, "%") || strings.Contains(block, "38.00") || strings.Contains(block, "62,000") || strings.Contains(block, "62000") {
			t.Fatalf("usage leaked into allowance: %s", block)
		}
		if view == "usage" {
			html := string(body)
			chart, fold := strings.Index(html, `class="usage-chart"`), strings.Index(html, `<details class="usage-fold"`)
			if chart < 0 || fold < 0 || chart > fold || !strings.Contains(html, "synthetic-worker: 62000 tokens") {
				t.Fatal("measured tokens were not charted ahead of the tables")
			}
		}
	}
}

func TestUsageChartsUseExactSourceAmounts(t *testing.T) {
	now := time.Date(2026, 10, 29, 14, 0, 0, 0, time.UTC)
	unk := protocol.TokenObservation{State: protocol.UsageUnknown}
	reported := func(n int64) protocol.TokenObservation {
		v := n
		return protocol.TokenObservation{State: protocol.UsageReported, Amount: &v}
	}
	usd := func(amount string) protocol.CostObservation {
		return protocol.CostObservation{State: protocol.UsageReported, Amount: amount, Unit: "USD", Kind: protocol.UsageCLIEstimate}
	}
	unknownCost := protocol.CostObservation{State: protocol.UsageUnknown, Kind: protocol.UsageUnknown}
	usage := func(in, out, cache protocol.TokenObservation, cost protocol.CostObservation) apiv1.PortalSessionUsage {
		return apiv1.PortalSessionUsage{Scope: protocol.UsageInitial, ResultState: "Final", ServedModels: []string{"claude-opus-5-5"},
			InputTokens: in, OutputTokens: out, CacheReadInputTokens: cache, CacheCreationInputTokens: unk, Cost: cost}
	}
	task := func(name, repo string, u apiv1.PortalSessionUsage) apiv1.PortalSessionSummary {
		return apiv1.PortalSessionSummary{Name: name, UID: "uid-" + name, Repo: repo, Phase: "Running", Freshness: "observed", ObservedAt: now,
			Activity: &apiv1.PortalSessionActivity{Mode: "task", Provider: "claude", AgentState: protocol.AgentBusy, RequestedModel: "claude-opus-5-5", Usage: u}}
	}
	half := protocol.MaxUsageTokens / 2
	page := sessionsPage([]apiv1.PortalSessionSummary{
		task("zero", "zero-repo", usage(reported(0), reported(0), reported(7), usd("0"))),
		task("part-known", "partial-repo", usage(reported(100), unk, reported(40), unknownCost)),
		task("part-unknown", "partial-repo", usage(unk, reported(5), unk, unknownCost)),
		task("tiny", "tiny-repo", usage(reported(4), reported(1), reported(9), usd("0.0000001"))),
		task("huge-a", "sum-huge", usage(reported(protocol.MaxUsageTokens), reported(1), reported(protocol.MaxUsageTokens), usd("1"))),
		task("huge-b", "sum-huge", usage(reported(protocol.MaxUsageTokens), reported(1), reported(protocol.MaxUsageTokens), usd("1"))),
		task("half", "half-huge", usage(reported(half), reported(1), reported(0), usd("0"))),
		{Name: "person", UID: "uid-person", Repo: "interactive-repo", Phase: "Running", Freshness: "observed", ObservedAt: now,
			Activity: &apiv1.PortalSessionActivity{Mode: "remote", Provider: "claude", AgentState: protocol.AgentIdle, RequestedModel: "claude-opus-5-5", Usage: unknownSessionUsage()}},
	}, true, "observed", now, portal.ProductFilter{View: "usage"})
	if !page.AsOf.Equal(now) {
		t.Fatalf("page clock = %s", page.AsOf)
	}
	project := breakdownTable(t, page, "By project")
	sumInput := new(big.Int).Add(big.NewInt(protocol.MaxUsageTokens), big.NewInt(protocol.MaxUsageTokens)).String()
	input := chartCell(t, project.Rows, "sum-huge", "Input")
	if input.ChartAmount != sumInput || input.ChartMetric != "input" || input.ChartUnit != "tokens" || input.ChartPartial || input.Unknown || input.ChartAmount == "4000000000000000" {
		t.Fatalf("huge input %+v", input)
	}
	if cache := chartCell(t, project.Rows, "sum-huge", "Cache read"); cache.ChartMetric != "" || cache.ChartAmount != "" {
		t.Fatalf("cache read joined the chart: %+v", cache)
	}
	halfBar := barByLabel(t, project.Charts(), "input", "half-huge")
	sumBar := barByLabel(t, project.Charts(), "input", "sum-huge")
	if sumBar.Width != "100" || halfBar.Width != "25" || halfBar.Amount != strconv.FormatInt(half, 10) || sumBar.Amount != sumInput {
		t.Fatalf("ratio half %+v sum %+v", halfBar, sumBar)
	}
	partial := chartCell(t, project.Rows, "partial-repo", "Input")
	if partial.ChartAmount != "100" || !partial.ChartPartial || partial.Unknown || strings.Contains(partial.ChartAmount, "40") {
		t.Fatalf("partial input %+v", partial)
	}
	if out := chartCell(t, project.Rows, "partial-repo", "Output"); out.ChartAmount != "5" || !out.ChartPartial {
		t.Fatalf("partial output %+v", out)
	}
	zero := chartCell(t, project.Rows, "zero-repo", "Output")
	if zero.ChartAmount != "0" || zero.Unknown || zero.ChartPartial || zero.ChartMetric != "output" {
		t.Fatalf("known zero %+v", zero)
	}
	idle := chartCell(t, project.Rows, "interactive-repo", "Input")
	if idle.ChartAmount != "" || !idle.Unknown || idle.ChartMetric != "input" || idle.ChartUnit != "tokens" || idle.Value != "Not measured" {
		t.Fatalf("unmeasured %+v", idle)
	}
	tinyCost := chartCell(t, project.Rows, "tiny-repo", "Est. cost")
	if tinyCost.ChartAmount != "1/10000000" || tinyCost.Value != "<0.01 USD" || tinyCost.ChartUnit != "USD" || tinyCost.Unknown {
		t.Fatalf("tiny sum %+v", tinyCost)
	}
	sessionCost := chartCell(t, page.Usage.Rows, "tiny", "Est. cost")
	if sessionCost.ChartAmount != "0.0000001" || sessionCost.Value != "0.0000001 USD" {
		t.Fatalf("source cost %+v", sessionCost)
	}
	sessionInput := chartCell(t, page.Usage.Rows, "tiny", "Input")
	if sessionInput.ChartAmount != "4" || chartCell(t, page.Usage.Rows, "tiny", "Cache read").ChartMetric != "" {
		t.Fatalf("session cache joined input: %+v", sessionInput)
	}
	unknownOut := chartCell(t, page.Usage.Rows, "part-known", "Output")
	if unknownOut.ChartAmount != "" || !unknownOut.Unknown || unknownOut.ChartMetric != "output" || unknownOut.ChartUnit != "tokens" {
		t.Fatalf("unknown output %+v", unknownOut)
	}
	knownZero := chartCell(t, page.Usage.Rows, "zero", "Output")
	if knownZero.ChartAmount != "0" || knownZero.Unknown {
		t.Fatalf("session zero %+v", knownZero)
	}
	tinyBar := barByLabel(t, page.Usage.SessionCharts(), "cost", "tiny")
	zeroBar := barByLabel(t, page.Usage.SessionCharts(), "cost", "zero")
	if tinyBar.Unknown || tinyBar.Zero || tinyBar.Width == "0" || tinyBar.Amount != "0.0000001" || !zeroBar.Zero || zeroBar.Width != "0" || zeroBar.Unknown {
		t.Fatalf("session cost bars tiny %+v zero %+v", tinyBar, zeroBar)
	}
	body, err := portal.RenderProduct(page)
	if err != nil {
		t.Fatal(err)
	}
	html := string(body)
	chart, fold := strings.Index(html, `class="usage-chart"`), strings.Index(html, `<details class="usage-fold"`)
	for _, want := range []string{"tiny: 0.0000001 USD", sumInput + " tokens", "Input follows each provider's own convention", "Cache reads are never added again"} {
		if !strings.Contains(html, stdhtml.EscapeString(want)) {
			t.Errorf("rendered chart lacks %s", want)
		}
	}
	if chart < 0 || fold < 0 || chart > fold || strings.Contains(html, "4000000000000000") || strings.Contains(html, "13 tokens") || strings.Contains(html, "<script") {
		t.Fatal("charts were missing, parsed display text, or added cache reads to input")
	}
}

func TestConversationChartsDoNotCrossInitialInvocations(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	unk := protocol.TokenObservation{State: protocol.UsageUnknown}
	reported := func(n int64) protocol.TokenObservation {
		v := n
		return protocol.TokenObservation{State: protocol.UsageReported, Amount: &v}
	}
	conv := func(model string, in, cache int64, partial bool) *apiv1.PortalConversationUsage {
		state, reason := protocol.ConversationFinal, ""
		if partial {
			state, reason = protocol.ConversationPartial, "skipped_records"
		}
		return &apiv1.PortalConversationUsage{ResultState: state, ReasonCode: reason, Turns: 1, ObservedAt: now.Add(-time.Minute),
			Models: []apiv1.PortalConversationModelUsage{{Model: model, InputTokens: reported(in), OutputTokens: reported(0), CacheReadInputTokens: reported(cache), CacheCreationInputTokens: unk}}}
	}
	sess := func(name, mode, provider, model string, c *apiv1.PortalConversationUsage) apiv1.PortalSessionSummary {
		return apiv1.PortalSessionSummary{Name: name, UID: "uid-" + name, Repo: "dev-env", Phase: "Running", Freshness: "observed", ObservedAt: now,
			Activity: &apiv1.PortalSessionActivity{Mode: mode, Provider: provider, AgentState: protocol.AgentIdle, RequestedModel: model, Usage: unknownSessionUsage(), ConversationUsage: c}}
	}
	page := sessionsPage([]apiv1.PortalSessionSummary{
		sess("claude-one", "remote", "claude", "claude-opus-5-5", conv("claude-opus-5-5", 10, 4, true)),
		sess("codex-one", "local", "codex", "gpt-6-astra", conv("gpt-6-astra", 7, 3, false)),
	}, true, "observed", now, portal.ProductFilter{View: "usage"})
	table := page.Usage.Breakdowns[0]
	if table.Title != "Conversation usage by provider and served model" {
		t.Fatal(table.Title)
	}
	claude := chartCell(t, table.Rows, "claude · claude-opus-5-5", "Input")
	codex := chartCell(t, table.Rows, "codex · gpt-6-astra", "Input")
	if claude.ChartAmount != "10" || !claude.ChartPartial || codex.ChartAmount != "7" || codex.ChartPartial || chartCell(t, table.Rows, "claude · claude-opus-5-5", "Cache read").ChartMetric != "" {
		t.Fatalf("conversation input claude %+v codex %+v", claude, codex)
	}
	charts := table.Charts()
	if len(charts) != 2 || charts[0].Metric != "input" || charts[1].Metric != "output" {
		t.Fatalf("conversation charts %+v", charts)
	}
	for _, chart := range charts {
		for _, bar := range chart.Bars {
			if bar.Amount == "14" || bar.Amount == "17" || bar.Amount == "13" {
				t.Fatalf("cross-scope or cache total %s", bar.Amount)
			}
		}
	}
	initial := breakdownTable(t, page, "Initial invocations by how sessions ran")
	if got := chartCell(t, initial.Rows, "Interactive", "Input"); got.ChartAmount != "" || got.Value != "Not measured" {
		t.Fatalf("conversation tokens entered the invocation chart: %+v", got)
	}
}

func breakdownTable(t *testing.T, page portal.ProductPage, title string) portal.ProductUsageTable {
	t.Helper()
	for _, table := range page.Usage.Breakdowns {
		if table.Title == title {
			return table
		}
	}
	t.Fatalf("no table %s", title)
	return portal.ProductUsageTable{}
}

func chartCell(t *testing.T, rows []portal.ProductUsageRow, name, label string) portal.ProductUsageCell {
	t.Helper()
	for _, row := range rows {
		if row.Name != name {
			continue
		}
		for _, cell := range row.Cells {
			if cell.Label == label {
				return cell
			}
		}
	}
	t.Fatalf("no %s/%s", name, label)
	return portal.ProductUsageCell{}
}

func barByLabel(t *testing.T, charts []portal.ProductUsageChart, metric, label string) portal.ProductUsageBar {
	t.Helper()
	for _, chart := range charts {
		if chart.Metric != metric {
			continue
		}
		for _, bar := range chart.Bars {
			if bar.Label == label {
				return bar
			}
		}
	}
	t.Fatalf("no %s bar %s", metric, label)
	return portal.ProductUsageBar{}
}
