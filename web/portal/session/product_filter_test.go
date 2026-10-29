package session

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/web/portal"
)

func TestUsageLinksKeepUIDWhenActivityReorders(t *testing.T) {
	now := time.Date(2026, 10, 29, 14, 0, 0, 0, time.UTC)
	reads := 0
	s := sessionReadFixture(t, func() time.Time { return now }, func(*http.Request) (*http.Response, error) {
		reads++
		first, second := "2026-10-29T13:59:00Z", "2026-10-29T13:58:00Z"
		if reads == 2 {
			first, second = second, first
		}
		activity := func(last string) string {
			return strings.Replace(unknownActivity, `"agentState":"idle"`, `"agentState":"idle","lastActivity":"`+last+`"`, 1)
		}
		body := activityPage(activityRow("a", activity(first)), activityRow("b", activity(second)))
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	usage, err := s.Read(t.Context(), portal.ProductFilter{View: "usage"})
	if err != nil {
		t.Fatal(err)
	}
	row := usage.Usage.Rows[0]
	if row.Name != "a" || row.ID != "s1" || row.SessionUID != "uid-a" {
		t.Fatalf("unexpected initial order: %+v", row)
	}
	body, err := portal.RenderProduct(usage)
	if err != nil || !strings.Contains(string(body), `href="`+SessionsPath+`?session=uid-a">a</a>`) {
		t.Fatalf("Usage did not link to its session UID: %v", err)
	}
	link, err := url.Parse(row.SessionHref())
	if err != nil {
		t.Fatal(err)
	}
	filter, ok := productFilter(link)
	if !ok || filter.Session != "uid-a" {
		t.Fatal("Usage href is not a canonical session selection")
	}
	selected, err := s.Read(t.Context(), filter)
	if err != nil {
		t.Fatal(err)
	}
	current := findRow(t, selected, "a")
	if current.ID != "s2" || !current.Selected || current.Messages.SessionUID != row.SessionUID || findRow(t, selected, "b").Selected {
		t.Fatalf("activity reordering changed the selected session: %+v", current)
	}
}

// filterActivity is a task activity with a provider, launcher and task result,
// as the 2026-10-27 operator sends it.
func filterActivity(mode, provider, result, last string) string {
	extra := `"provider":"` + provider + `","launchedBy":"dev-env-1019-205125",`
	if result != "" {
		extra += `"taskResult":"` + result + `",`
	}
	return `"activity":{"mode":"` + mode + `",` + extra + `"agentState":"exited","lastActivity":"` + last + `","requestedModel":"claude-opus-5-5","usage":{"scope":"Unknown","resultState":"Unknown","servedModels":[],"inputTokens":{"state":"Unknown"},"outputTokens":{"state":"Unknown"},"cacheReadInputTokens":{"state":"Unknown"},"cacheCreationInputTokens":{"state":"Unknown"},"cost":{"state":"Unknown","kind":"Unknown"}}}`
}

func filterRow(name, repo, phase, activity string) string {
	return `{"name":"` + name + `","uid":"uid-` + name + `","repo":"` + repo + `","phase":"` + phase + `","observedAt":"2026-10-23T15:59:00Z","freshness":"observed",` + activity + `}`
}

// The rail filters Sessions by Needs attention and by project, counted over
// the whole read. A task's own result decides its badge and group: a Failed
// phase whose task succeeded is Stopped, and a failure leaves Needs attention
// after a day.
func TestProductFiltersAndProjects(t *testing.T) {
	f := setupAuth(t)
	f.login(t)
	now := f.now
	recent, old := now.Add(-time.Hour).Format(time.RFC3339), now.Add(-72*time.Hour).Format(time.RFC3339)
	body := `{"sessions":[` + strings.Join([]string{
		filterRow("a-dev-running", "dev-env", "Running", strings.Replace(filterActivity("task", "claude", "Unknown", recent), `"agentState":"exited"`, `"agentState":"busy"`, 1)),
		filterRow("b-ops-failed", "haynes-ops", "Suspended", filterActivity("task", "codex", "Failed", recent)),
		filterRow("c-ops-old-failure", "haynes-ops", "Suspended", filterActivity("task", "codex", "TimedOut", old)),
		filterRow("d-dev-pod-failed", "dev-env", "Failed", filterActivity("task", "claude", "Succeeded", recent)),
		filterRow("e-sigo-remote", "sigo-alumni", "Running", strings.Replace(filterActivity("remote", "claude", "", recent), `"agentState":"exited"`, `"agentState":"idle"`, 1)),
	}, ",") + `],"limit":50,"complete":true,"freshness":"observed","observedAt":"2026-10-23T15:59:00Z"}`
	cert, ca := mintNamed(t, operatorPortalHost)
	srv := serveTLS(t, cert, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	token := filepath.Join(t.TempDir(), "operator")
	if err := os.WriteFile(token, []byte(fixtureOperatorToken("dev-env-operator", now.Add(time.Hour))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sessions, err := newSessionSource(token, ca, func() time.Time { return now }, routeDial(map[string]string{operatorPortalHost: srv}))
	if err != nil {
		t.Fatal(err)
	}
	authorizer := currentFixture(f, func(*http.Request) (*http.Response, error) { return userInfoResponse(currentAllow), nil })
	handler := NewAssembly(AssemblyConfig{Session: f.e, Authorizer: authorizer, Reader: portal.UnavailableReader{}, Sessions: sessions, FreshFor: time.Minute, Now: func() time.Time { return now }})
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, f.request(http.MethodGet, path, ""))
		return w
	}
	all := get(SessionsPath).Body.String()
	for _, want := range []string{
		`All sessions<span class="count">5</span>`, `Needs attention<span class="attention-count">1</span>`,
		`?project=dev-env">`, `dev-env<span class="count">2</span>`, `haynes-ops<span class="count">2</span>`, `sigo-alumni<span class="count">1</span>`,
		`class="badge tone-muted">Succeeded`, `class="badge tone-bad">Task failed`, `class="badge tone-bad">Timed out`,
		"Interactive (remote)", "Headless (task)", "launched by dev-env-1019-205125",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("all sessions lacks %s", want)
		}
	}
	groups := map[string]string{}
	page, err := sessions.Read(t.Context(), portal.ProductFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range page.Groups {
		for _, row := range g.Rows {
			groups[row.Title] = g.Title
		}
	}
	for name, want := range map[string]string{"a-dev-running": "In progress", "b-ops-failed": "Needs attention", "c-ops-old-failure": "Stopped", "d-dev-pod-failed": "Stopped", "e-sigo-remote": "In progress"} {
		if groups[name] != want {
			t.Errorf("%s is in %q, want %q", name, groups[name], want)
		}
	}
	attention := get(SessionsPath + "?show=attention").Body.String()
	if !strings.Contains(attention, `<h1 id="list-title">Needs attention</h1>`) || !strings.Contains(attention, "b-ops-failed") || strings.Contains(attention, "a-dev-running") || strings.Contains(attention, "c-ops-old-failure") ||
		!strings.Contains(attention, `href="/portal/projects/dev-env/environments/internal/sessions?show=attention" aria-current="page"`) || !strings.Contains(attention, `All sessions<span class="count">5</span>`) {
		t.Fatalf("attention filter: %s", attention)
	}
	ops := get(SessionsPath + "?project=haynes-ops").Body.String()
	if !strings.Contains(ops, `<h1 id="list-title">haynes-ops</h1>`) || !strings.Contains(ops, "b-ops-failed") || !strings.Contains(ops, "c-ops-old-failure") || strings.Contains(ops, "a-dev-running") || strings.Contains(ops, "e-sigo-remote") ||
		!strings.Contains(ops, `?project=haynes-ops" aria-current="page"`) || strings.Contains(ops, "By project") {
		t.Fatalf("project filter: %s", ops)
	}
	usage := get(SessionsPath + "?view=usage")
	if usage.Code != http.StatusOK || !strings.Contains(usage.Body.String(), `<title>Usage · dev-env</title>`) ||
		!strings.Contains(usage.Body.String(), `?view=usage" aria-current="page"`) || strings.Contains(usage.Body.String(), `class="rail"`) || strings.Contains(usage.Body.String(), `class="list"`) {
		t.Fatalf("Usage is not its own view: %d", usage.Code)
	}
	for _, bad := range []string{
		SessionsPath + "?view=other", SessionsPath + "?view=usage&view=usage", SessionsPath + "?view=usage&show=attention", TasksPath + "?view=usage",
		SessionsPath + "?show=all", SessionsPath + "?project=..", SessionsPath + "?project=a%20b", SessionsPath + "?project=",
		SessionsPath + "?show=attention&show=attention", SessionsPath + "?show=attention&project=dev-env", SessionsPath + "?Show=attention",
		SessionsPath + "?show=attention&", SessionsPath + "?project=dev%2Denv", TasksPath + "?project=dev-env",
	} {
		if w := get(bad); w.Code == http.StatusOK {
			t.Errorf("filter %s accepted", bad)
		}
	}
	if w := get(TasksPath + "?show=attention"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Tasks not connected") {
		t.Errorf("tasks attention filter refused: %d", w.Code)
	}
}

// The new activity keys are optional and checked; a task result belongs to a
// task session only.
func TestSessionActivityOptionalKeysFailClosed(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	good := filterRow("worker", "haynes-ops", "Running", filterActivity("task", "codex", "Failed", "2026-10-25T11:00:00Z"))
	decoded, err := decodeSessionPage([]byte(activityPage(good)), now)
	if err != nil {
		t.Fatal(err)
	}
	a := decoded.Sessions[0].Activity
	if decoded.Sessions[0].Repo != "haynes-ops" || a.Provider != "codex" || a.LaunchedBy != "dev-env-1019-205125" || a.TaskResult != "Failed" {
		t.Fatalf("decoded %+v %+v", decoded.Sessions[0], a)
	}
	if legacy, err := decodeSessionPage([]byte(activityPage(activityRow("old", reportedActivity))), now); err != nil || legacy.Sessions[0].Activity.Provider != "Unknown" || legacy.Sessions[0].Activity.TaskResult != "" {
		t.Fatalf("an activity without the new keys was refused or misread: %+v %v", legacy, err)
	}
	for name, body := range map[string]string{
		"provider outside set":   strings.Replace(good, `"provider":"codex"`, `"provider":"gpt"`, 1),
		"provider null":          strings.Replace(good, `"provider":"codex"`, `"provider":null`, 1),
		"launcher with a space":  strings.Replace(good, `"launchedBy":"dev-env-1019-205125"`, `"launchedBy":"a b"`, 1),
		"launcher too long":      strings.Replace(good, `"launchedBy":"dev-env-1019-205125"`, `"launchedBy":"`+strings.Repeat("x", 254)+`"`, 1),
		"launcher empty":         strings.Replace(good, `"launchedBy":"dev-env-1019-205125"`, `"launchedBy":""`, 1),
		"task result outside":    strings.Replace(good, `"taskResult":"Failed"`, `"taskResult":"Done"`, 1),
		"task result on remote":  strings.Replace(good, `"mode":"task"`, `"mode":"remote"`, 1),
		"repo with a slash":      strings.Replace(good, `"repo":"haynes-ops"`, `"repo":"a/b"`, 1),
		"repo dot dot":           strings.Replace(good, `"repo":"haynes-ops"`, `"repo":".."`, 1),
		"repo empty":             strings.Replace(good, `"repo":"haynes-ops"`, `"repo":""`, 1),
		"unknown activity key":   strings.Replace(good, `"provider":"codex"`, `"provider":"codex","prompt":"SECRET"`, 1),
		"repo over 100 runes":    strings.Replace(good, `"repo":"haynes-ops"`, `"repo":"`+strings.Repeat("r", 101)+`"`, 1),
		"task result is numeric": strings.Replace(good, `"taskResult":"Failed"`, `"taskResult":1`, 1),
	} {
		if _, err := decodeSessionPage([]byte(activityPage(body)), now); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// remoteControl is optional and exact: its state word and link are checked again
// here, and a bad value makes the whole read unavailable like the other optional keys.
func TestSessionActivityRemoteControl(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	const link = "https://claude.ai/code/session_FIXTURE0000000000"
	base := filterRow("rc", "dev-env", "Running", filterActivity("remote", "claude", "", "2026-10-25T11:00:00Z"))
	with := func(rc string) string {
		return strings.Replace(base, `"provider":"claude",`, `"provider":"claude","remoteControl":`+rc+`,`, 1)
	}
	decode := func(body string) (*apiv1.PortalRemoteControl, error) {
		page, err := decodeSessionPage([]byte(activityPage(body)), now)
		if err != nil {
			return nil, err
		}
		return page.Sessions[0].Activity.RemoteControl, nil
	}
	if rc, err := decode(base); err != nil || rc != nil {
		t.Fatalf("absent key: %+v %v", rc, err)
	}
	if rc, err := decode(with(`{"state":"registered","url":"` + link + `"}`)); err != nil || rc == nil || rc.State != "registered" || rc.URL != link {
		t.Fatalf("registered: %+v %v", rc, err)
	}
	if rc, err := decode(with(`{"state":"pair-code-issued"}`)); err != nil || rc == nil || rc.State != "pair-code-issued" || rc.URL != "" {
		t.Fatalf("no url: %+v %v", rc, err)
	}
	for name, rc := range map[string]string{
		"null":           `null`,
		"string":         `"registered"`,
		"empty object":   `{}`,
		"no state":       `{"url":"` + link + `"}`,
		"state null":     `{"state":null}`,
		"state number":   `{"state":1}`,
		"state upper":    `{"state":"Registered"}`,
		"state markup":   `{"state":"<b>"}`,
		"state empty":    `{"state":""}`,
		"state long":     `{"state":"` + strings.Repeat("a", 33) + `"}`,
		"url null":       `{"state":"registered","url":null}`,
		"url empty":      `{"state":"registered","url":""}`,
		"other host":     `{"state":"registered","url":"https://evil.example/code/session_FIXTURE0000000000"}`,
		"http":           `{"state":"registered","url":"http://claude.ai/code/session_FIXTURE0000000000"}`,
		"javascript":     `{"state":"registered","url":"javascript:alert(1)"}`,
		"query":          `{"state":"registered","url":"` + link + `?a=b"}`,
		"fragment":       `{"state":"registered","url":"` + link + `#x"}`,
		"whitespace":     `{"state":"registered","url":"` + link + ` "}`,
		"extra path":     `{"state":"registered","url":"` + link + `/x"}`,
		"name leaks":     `{"state":"registered","name":"x"}`,
		"session id key": `{"state":"registered","sessionId":"x"}`,
	} {
		if _, err := decode(with(rc)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// Usage groups sum reported values only: cache reads stay apart from input, a
// cost that is not reported is counted as unavailable, and a group with
// nothing measured says so instead of zero.
func TestSessionUsageBreakdowns(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	measured := func(input, cache, cost string) string {
		c := `{"state":"Unknown","kind":"Unknown"}`
		if cost != "" {
			c = `{"state":"Reported","amount":"` + cost + `","unit":"USD","kind":"ProviderCLIEquivalentEstimate"}`
		}
		return strings.NewReplacer(`"inputTokens":{"state":"Reported","amount":1234567}`, `"inputTokens":{"state":"Reported","amount":`+input+`}`,
			`"cacheReadInputTokens":{"state":"Reported","amount":5000}`, `"cacheReadInputTokens":{"state":"Reported","amount":`+cache+`}`,
			`"cost":{"state":"Reported","amount":"0.75","unit":"USD","kind":"ProviderCLIEquivalentEstimate"}`, `"cost":`+c).Replace(reportedActivity)
	}
	decoded, err := decodeSessionPage([]byte(activityPage(
		filterRow("one", "dev-env", "Running", measured("1000", "400", "0.75")),
		filterRow("two", "haynes-ops", "Running", measured("2000", "100", "0.25")),
		filterRow("three", "haynes-ops", "Running", measured("500", "0", "")),
		activityRow("remote", unknownActivity),
	)), now)
	if err != nil {
		t.Fatal(err)
	}
	page := sessionsPage(decoded.Sessions, true, "observed", now, portal.ProductFilter{})
	tables := map[string]map[string]map[string]string{}
	for _, table := range page.Usage.Breakdowns {
		rows := map[string]map[string]string{}
		for _, row := range table.Rows {
			cells := map[string]string{}
			for _, c := range row.Cells {
				cells[c.Label] = c.Value
			}
			rows[row.Name] = cells
		}
		tables[table.Title] = rows
	}
	headless := tables["By how sessions ran"]["Headless"]
	if headless["Sessions"] != "3" || headless["Measured"] != "3" || headless["Input"] != "3,500" || headless["Cache read"] != "500" || headless["Est. cost"] != "1.00 USD (1 unavailable)" || headless["Cache create"] != "Unknown" {
		t.Fatalf("headless %v", headless)
	}
	if interactive := tables["By how sessions ran"]["Interactive"]; interactive["Sessions"] != "1" || interactive["Measured"] != "0" || interactive["Input"] != "Not measured" || interactive["Est. cost"] != "Not measured" {
		t.Fatalf("interactive %v", interactive)
	}
	if ops := tables["By project"]["haynes-ops"]; ops["Input"] != "2,500" || ops["Est. cost"] != "0.25 USD (1 unavailable)" {
		t.Fatalf("haynes-ops %v", ops)
	}
	if model := tables["By provider and requested model"]["Unknown · claude-opus-5-5"]; model["Sessions"] != "4" || model["Input"] != "3,500" {
		t.Fatalf("model rows %v", tables["By provider and requested model"])
	}
	if len(page.Usage.Allowance) != 2 || page.Usage.Allowance[0].Value != "Not connected" {
		t.Fatalf("allowance %+v", page.Usage.Allowance)
	}
	filtered := sessionsPage(decoded.Sessions, true, "observed", now, portal.ProductFilter{Project: "haynes-ops"})
	if filtered.Rail.All != "4" || filtered.ListTitle != "haynes-ops" || len(filtered.Usage.Rows) != 2 || len(filtered.Usage.Breakdowns) != 2 {
		t.Fatalf("project filter %+v", filtered)
	}
}

func TestTasksAttentionFilter(t *testing.T) {
	row := func(status string) portal.ProductRow {
		return portal.ProductRow{Title: status, Status: portal.ProductStatus{Text: status}}
	}
	rows := []portal.ProductRow{row("Needs approval"), row("In progress"), row("Paused"), row("Accepted")}
	page := tasksPage(rows, true, portal.ProductFilter{Attention: true})
	var titles []string
	for _, g := range page.Groups {
		for _, r := range g.Rows {
			titles = append(titles, r.Title)
		}
	}
	if strings.Join(titles, ",") != "Needs approval,Paused" || page.Rail.All != "4" || page.Rail.Attention != "2" || page.ListTitle != "Needs attention" || page.ListCount != "2 work packages · haynes-engineering" {
		t.Fatalf("attention tasks %v %+v", titles, page)
	}
	if none := tasksPage([]portal.ProductRow{row("Accepted")}, true, portal.ProductFilter{Attention: true}); none.Notice != "Nothing needs attention." {
		t.Fatalf("empty attention notice %q", none.Notice)
	}
}

func TestSyntheticFixtureIsGroupedAndKeptOutOfAttention(t *testing.T) {
	now := time.Date(2026, 10, 29, 18, 0, 0, 0, time.UTC)
	real := mustTaskRow(t, sampleWorkPackage(41, "Needs approval", "Ada", "", "Real work that mentions Synthetic fixture later", "Needs a decision"), now)
	lookalike := mustTaskRow(t, sampleWorkPackage(42, "Paused", "", "", "Real work", "Synthetic fixture, never execution approval"), now)
	marked := mustTaskRow(t, sampleWorkPackage(40, "Paused", "", "", "Synthetic fixture, never execution approval.", "OP-ACCEPTANCE-20261015 Story A"), now)
	mid := mustTaskRow(t, sampleWorkPackage(43, "On hold", "Ada", "Bea", "Notes. Synthetic fixture inside.", "Held"), now)
	markdown := mustTaskRow(t, sampleWorkPackage(44, "Needs answer", "", "", "**Synthetic fixture** bold", "Bold"), now)
	lower := mustTaskRow(t, sampleWorkPackage(45, "Paused", "", "", "synthetic fixture lowercase", "Lower"), now)
	progress := mustTaskRow(t, sampleWorkPackage(46, "In progress", "Ada", "", "Ship it", "Building"), now)
	exact := mustTaskRow(t, sampleWorkPackage(47, "In progress", "Ada", "Bea", "Synthetic fixture", "Exact marker"), now)
	padded := mustTaskRow(t, sampleWorkPackage(48, "Needs approval", "Ada", "", " Synthetic fixture, never", "Padded"), now)
	named := mustTaskRow(t, sampleWorkPackage(49, "Paused", "Fixture", "", "Real work", "Owner named Fixture"), now)
	acceptance := mustTaskRow(t, sampleWorkPackage(50, "Needs approval", "", "", "Synthetic acceptance fixture only. No imported source work, human approval or execution authority.", "OP-ACCEPTANCE import"), now)
	probe := mustTaskRow(t, sampleWorkPackage(51, "Paused", "", "", "Negative authorization probe only. Never human approval or execution authority.", "Paused Saga"), now)
	for _, row := range []portal.ProductRow{real, lookalike, mid, markdown, lower, progress, padded, named} {
		if row.Fixture {
			t.Fatalf("%s was treated as a fixture", row.Title)
		}
	}
	if !marked.Fixture || !exact.Fixture || !acceptance.Fixture || !probe.Fixture {
		t.Fatal("explicit marker was not a fixture")
	}
	if marked.Meta != "Owner not recorded" {
		t.Fatalf("empty fixture owner %q", marked.Meta)
	}
	if nextActionNote(marked) != "Paused: resume needs its owner" || nextActionNote(exact) != "" {
		t.Fatalf("fixture actions %q %q", nextActionNote(marked), nextActionNote(exact))
	}
	for _, row := range []portal.ProductRow{marked, acceptance, probe} {
		if _, ok := sectionByTitle(row, "Needs attention"); ok || row.Attention != nil {
			t.Fatalf("fixture detail %s is filed under Needs attention", row.Title)
		}
	}
	if nextActionNote(acceptance) != "Waiting for approval" || nextActionNote(probe) != "Paused: resume needs its owner" {
		t.Fatalf("fixture actions %q %q", nextActionNote(acceptance), nextActionNote(probe))
	}
	summary, ok := sectionByTitle(marked, "Summary")
	if !ok || len(summary.States) != 1 || summary.States[0].Status.Text != "Fixture" || summary.States[0].Text != "Synthetic fixture" {
		t.Fatalf("fixture badge %+v", summary.States)
	}
	hasBadge := false
	for _, badge := range marked.Badges {
		if badge == "Fixture" {
			hasBadge = true
		}
	}
	if !hasBadge {
		t.Fatal("detail meta lost the Fixture badge")
	}
	rows := []portal.ProductRow{real, lookalike, marked, mid, markdown, lower, progress, exact, padded, named, acceptance, probe}
	page := tasksPage(rows, true, portal.ProductFilter{})
	if page.Rail.All != "12" || page.Rail.Attention != "7" {
		t.Fatalf("rail all %s attention %s", page.Rail.All, page.Rail.Attention)
	}
	var groups, attentionTitles, fixtureTitles []string
	for _, group := range page.Groups {
		if len(group.Rows) == 0 {
			continue
		}
		groups = append(groups, group.Title)
		for _, row := range group.Rows {
			switch group.Title {
			case "Needs attention":
				attentionTitles = append(attentionTitles, row.Title)
			case "Fixtures (synthetic)":
				fixtureTitles = append(fixtureTitles, row.Title)
			}
		}
	}
	if strings.Join(groups, ",") != "Needs attention,In progress,Fixtures (synthetic)" {
		t.Fatalf("groups %s", strings.Join(groups, ","))
	}
	if strings.Join(attentionTitles, ",") != "Needs a decision,Synthetic fixture, never execution approval,Held,Bold,Lower,Padded,Owner named Fixture" {
		t.Fatalf("attention %s", strings.Join(attentionTitles, ","))
	}
	if strings.Join(fixtureTitles, ",") != "OP-ACCEPTANCE-20261015 Story A,Exact marker,OP-ACCEPTANCE import,Paused Saga" {
		t.Fatalf("fixtures %s", strings.Join(fixtureTitles, ","))
	}
	page.ObservedAt = now.Format(time.RFC3339)
	body, err := portal.RenderProduct(page)
	if err != nil {
		t.Fatal(err)
	}
	html := string(body)
	attentionAt := strings.Index(html, `<span>Needs attention</span>`)
	progressAt := strings.Index(html, `<span>In progress</span>`)
	fixtureAt := strings.Index(html, `<span>Fixtures (synthetic)</span>`)
	if attentionAt < 0 || progressAt < attentionAt || fixtureAt < progressAt {
		t.Fatalf("group order attention %d progress %d fixtures %d", attentionAt, progressAt, fixtureAt)
	}
	for _, want := range []string{
		"OP-ACCEPTANCE-20261015 Story A", `class="badge tone-muted">Fixture</span>`,
		`<h3 id="t1-attention">Needs approval</h3><p>Waiting for approval</p>`,
		`<h3 id="t2-attention">Paused</h3><p>Paused: resume needs its owner</p>`,
		"Owner not recorded", `All tasks<span class="count">12</span>`, `attention-count">7`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("all-tasks page lacks %q", want)
		}
	}
	if strings.Count(html, `class="attention-panel"`) != 7 || strings.Contains(html, `</span><span>Paused</span>`) || strings.Contains(html, `</span><span>Needs approval</span>`) {
		t.Error("attention panels are not one per Needs attention task, or a status is repeated")
	}
	if strings.Contains(strings.ToLower(html), "stuck") {
		t.Fatal("fixture grouping inferred a stall")
	}
	filtered := tasksPage(rows, true, portal.ProductFilter{Attention: true})
	filtered.ObservedAt = now.Format(time.RFC3339)
	attentionBody, err := portal.RenderProduct(filtered)
	if err != nil {
		t.Fatal(err)
	}
	attentionHTML := string(attentionBody)
	for _, absent := range []string{"OP-ACCEPTANCE-20261015 Story A", "Exact marker", "OP-ACCEPTANCE import", "Paused Saga", "Building", `<span>Fixtures (synthetic)</span>`, `tone-muted">Fixture`} {
		if strings.Contains(attentionHTML, absent) {
			t.Errorf("attention list still shows %q", absent)
		}
	}
	if !strings.Contains(attentionHTML, "Needs a decision") || !strings.Contains(attentionHTML, "Owner named Fixture") || !strings.Contains(attentionHTML, `attention-count">7`) {
		t.Fatal("attention list dropped a real row or its count")
	}
	only := tasksPage([]portal.ProductRow{marked, exact}, true, portal.ProductFilter{Attention: true})
	if only.Notice != "Nothing needs attention." || only.Rail.Attention != "0" || only.Rail.All != "2" {
		t.Fatalf("fixtures-only attention %+v", only)
	}
	only.ObservedAt = now.Format(time.RFC3339)
	onlyBody, err := portal.RenderProduct(only)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(onlyBody), `class="attention-count"`) || strings.Contains(string(onlyBody), "OP-ACCEPTANCE-20261015 Story A") || strings.Contains(string(onlyBody), "Exact marker") {
		t.Fatal("a fixture-only attention view showed a count or a fixture")
	}
	visible := tasksPage([]portal.ProductRow{marked, exact}, true, portal.ProductFilter{})
	visible.ObservedAt = now.Format(time.RFC3339)
	visibleBody, err := portal.RenderProduct(visible)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(visibleBody), "OP-ACCEPTANCE-20261015 Story A") || !strings.Contains(string(visibleBody), "Exact marker") || !strings.Contains(string(visibleBody), `<span>Fixtures (synthetic)</span>`) {
		t.Fatal("fixtures were hidden from the full list")
	}
}

// Only the three exact, case-sensitive, untrimmed description prefixes mark a
// fixture. The subject is never read, and a near miss stays real work.
func TestTaskFixturePrefixesAreExact(t *testing.T) {
	now := time.Date(2026, 10, 29, 18, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		description, subject string
		fixture              bool
	}{
		{"Synthetic fixture", "Story", true},
		{"Synthetic fixture, never execution approval.", "Story", true},
		{"Synthetic acceptance fixture", "Story", true},
		{"Synthetic acceptance fixture only. No imported source work, human approval or execution authority.", "Story", true},
		{"Negative authorization probe", "Story", true},
		{"Negative authorization probe only. Never human approval or execution authority.", "Paused Saga", true},
		{"Real work. Negative authorization probe only.", "Story", false},
		{"Notes: Synthetic acceptance fixture only.", "Story", false},
		{"negative authorization probe only.", "Story", false},
		{"Negative Authorization Probe only.", "Story", false},
		{"synthetic acceptance fixture only.", "Story", false},
		{"Synthetic Acceptance Fixture only.", "Story", false},
		{" Negative authorization probe only.", "Story", false},
		{"\nNegative authorization probe only.", "Story", false},
		{" Synthetic acceptance fixture only.", "Story", false},
		{"**Negative authorization probe** only", "Story", false},
		{"Negative authorization", "Story", false},
		{"Negative-authorization probe", "Story", false},
		{"Synthetic acceptance", "Story", false},
		{"Real work", "Negative authorization probe only.", false},
		{"Real work", "Synthetic acceptance fixture only.", false},
		{"", "Synthetic fixture", false},
	} {
		row := mustTaskRow(t, sampleWorkPackage(40, "Paused", "", "", tc.description, tc.subject), now)
		if row.Fixture != tc.fixture {
			t.Errorf("description %q subject %q: fixture %v, want %v", tc.description, tc.subject, row.Fixture, tc.fixture)
		}
		if attention := row.Attention != nil; attention == tc.fixture {
			t.Errorf("description %q subject %q: attention %v", tc.description, tc.subject, attention)
		}
	}
}

// worstSession is a summary with every rendered string at its bound.
func worstSession(i int, now time.Time) apiv1.PortalSessionSummary {
	limit := protocol.MaxUsageTokens
	at := now.Add(-time.Duration(i) * time.Minute)
	model := "m" + strings.Repeat("x", protocol.MaxUsageModelID-1)
	reported := protocol.TokenObservation{State: protocol.UsageReported, Amount: &limit}
	return apiv1.PortalSessionSummary{
		Name: strconv.Itoa(i) + "-" + strings.Repeat("n", 240), UID: strings.Repeat("u", 36), Repo: strings.Repeat("r", 100),
		Phase: "Running", ObservedAt: at, Freshness: "observed",
		Activity: &apiv1.PortalSessionActivity{Mode: "task", Provider: "claude", AgentState: "busy", LastActivity: &at, RequestedModel: model,
			LaunchedBy: "l" + strings.Repeat("x", 252), TaskResult: "Unknown",
			Usage: apiv1.PortalSessionUsage{Scope: protocol.UsageInitial, ResultState: "Final", ServedModels: []string{model, model, model, model},
				InputTokens: reported, OutputTokens: reported, CacheReadInputTokens: reported, CacheCreationInputTokens: reported,
				Cost: protocol.CostObservation{State: protocol.UsageReported, Amount: "123456789.123456789", Unit: "USD", Kind: "ProviderCLIEquivalentEstimate"}}},
	}
}

// The Sessions read may hold 500 sessions. Even with every string at its
// limit, the rendered page fits the assembly's response bound: the list is
// first bounded at maxListedSessions, then trimmed further to the byte budget,
// while the rail, group counts and breakdowns still count all 500.
func TestSessionsPageStaysUnderTheResponseBound(t *testing.T) {
	now := time.Date(2026, 10, 27, 4, 0, 0, 0, time.UTC)
	var sessions []apiv1.PortalSessionSummary
	for i := 0; i < sessionPageCap*productPageSize; i++ {
		sessions = append(sessions, worstSession(i, now))
	}
	page := sessionsPage(sessions, true, "observed", now, portal.ProductFilter{})
	page.ObservedAt = now.Format(time.RFC3339)
	listed := 0
	for _, g := range page.Groups {
		listed += len(g.Rows)
	}
	if listed != maxListedSessions || len(page.Usage.Rows) != maxListedSessions || page.Rail.All != "500" || page.Groups[1].Total != 500 ||
		page.Usage.Stats[0].Value != "500 of 500" || page.ListCount != "Showing 150 of 500 sessions · all projects" {
		t.Fatalf("listed=%d usage=%d rail=%s list=%q stats=%+v", listed, len(page.Usage.Rows), page.Rail.All, page.ListCount, page.Usage.Stats)
	}
	if headless := page.Usage.Breakdowns[0].Rows[0]; headless.Name != "Headless" || headless.Cells[2].Value != "500,000,000,000,000,000" {
		t.Fatalf("token sums overflowed or were dropped: %+v", headless.Cells)
	}
	if rows := page.Usage.Breakdowns[1].Rows; len(rows) != 1 {
		t.Fatalf("one requested model is one row: %d", len(rows))
	}
	body, err := portal.RenderProductWithin(page, maximumResponse*7/8)
	if err != nil || len(body) > maximumResponse*7/8 {
		t.Fatalf("worst-case Sessions page: %d bytes, err %v", len(body), err)
	}
	html := string(body)
	if !strings.Contains(html, "of 500</span>") || !strings.Contains(html, "narrow with a filter") || strings.Count(html, `class="item"`) < 50 || strings.Contains(html, `id="usage"`) {
		t.Fatalf("the trimmed Sessions page lost its counts or most rows: %d items", strings.Count(html, `class="item"`))
	}
	page.Filter = portal.ProductFilter{View: "usage"}
	body, err = portal.RenderProductWithin(page, maximumResponse*7/8)
	if err != nil || len(body) > maximumResponse*7/8 || !strings.Contains(string(body), "500,000,000,000,000,000") {
		t.Fatalf("bounded Usage lost its totals: %d bytes, err %v", len(body), err)
	}
}

// Tasks hold at most productPageCap pages. With every rendered string at its
// bound and escaping at its worst, the page is trimmed to fit the bound.
func TestTasksPageStaysUnderTheResponseBound(t *testing.T) {
	now := time.Date(2026, 10, 27, 4, 0, 0, 0, time.UTC)
	worst := strings.Repeat("&", 300)
	var rows []portal.ProductRow
	for i := 1; i <= productPageCap*productPageSize; i++ {
		var e halWorkPackage
		e.ID, e.Subject, e.CreatedAt, e.UpdatedAt = 100000+i, worst, "2026-10-20T00:00:00Z", "2026-10-26T00:00:00Z"
		e.CustomField1, e.CustomField2, e.CustomField3 = worst, "agent | worktree "+worst+" | branch b", worst
		e.Description.Raw = strings.Repeat("&", 5000)
		e.Links.Project.Href = "/api/v3/projects/3"
		e.Links.Status.Title, e.Links.Type.Title, e.Links.Parent.Title = "Needs approval", worst, worst
		e.Links.Assignee.Title, e.Links.Responsible.Title = worst, worst
		row, err := taskRow(e, now)
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, row)
	}
	page := tasksPage(rows, true, portal.ProductFilter{})
	page.ObservedAt = now.Format(time.RFC3339)
	body, err := portal.RenderProductWithin(page, maximumResponse*7/8)
	if err != nil || len(body) > maximumResponse*7/8 || strings.Count(string(body), `class="item"`) < 10 || !strings.Contains(string(body), "of 200</span>") {
		t.Fatalf("worst-case Tasks page: %d bytes, %d items, err %v", len(body), strings.Count(string(body), `class="item"`), err)
	}
}

// Breakdowns keep the largest groups and merge the rest into one Other row.
func TestUsageBreakdownMergesSmallGroups(t *testing.T) {
	m := map[string]*usageTotal{}
	for i := 0; i < 30; i++ {
		t := totalFor(m, "p"+strconv.Itoa(100+i))
		for j := 0; j <= i; j++ {
			t.add(apiv1.PortalSessionUsage{Scope: protocol.UsageUnknown})
		}
	}
	rows := totalRows(m)
	if len(rows) != maxBreakdownRows || rows[0].Name != "p129" || rows[len(rows)-1].Name != "Other (11 groups)" || rows[len(rows)-1].Cells[0].Value != strconv.Itoa(1+2+3+4+5+6+7+8+9+10+11) {
		t.Fatalf("rows %d first %s last %+v", len(rows), rows[0].Name, rows[len(rows)-1])
	}
}

// A task's failure is dated by its last activity; a pod that ended by the
// later of its last activity and its last real heartbeat; with neither time it
// is not recent.
func TestFailureRecency(t *testing.T) {
	now := time.Date(2026, 10, 27, 4, 0, 0, 0, time.UTC)
	old, recent := now.Add(-72*time.Hour), now.Add(-time.Hour)
	session := func(phase, mode, result, freshness string, last *time.Time, beat time.Time) apiv1.PortalSessionSummary {
		return apiv1.PortalSessionSummary{Name: "s", Repo: "dev-env", Phase: phase, ObservedAt: beat, Freshness: freshness,
			Activity: &apiv1.PortalSessionActivity{Mode: mode, Provider: "claude", AgentState: "exited", LastActivity: last, RequestedModel: "claude-opus-5-5", TaskResult: result, Usage: unknownSessionUsage()}}
	}
	for name, test := range map[string]struct {
		s    apiv1.PortalSessionSummary
		want int
	}{
		"task failed an hour ago":                      {session("Suspended", "task", "Failed", "stale", &recent, recent), 0},
		"task failed days ago, suspended heartbeating": {session("Suspended", "task", "Failed", "observed", &old, now), 2},
		"task failure with no activity time":           {session("Failed", "task", "TimedOut", "observed", nil, now), 2},
		"idle interactive pod ended an hour ago":       {session("Failed", "remote", "", "stale", &old, recent), 0},
		"interactive pod ended days ago":               {session("Failed", "remote", "", "stale", &old, old), 2},
		"pod failed before any report":                 {session("Failed", "remote", "", "unknown", nil, now), 2},
	} {
		if got := sessionGroup(test.s, now); got != test.want {
			t.Errorf("%s: group %d, want %d", name, got, test.want)
		}
	}
}
