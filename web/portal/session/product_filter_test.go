package session

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/web/portal"
)

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
	for _, bad := range []string{
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
	if headless["Sessions"] != "3" || headless["Measured"] != "3" || headless["Input"] != "3,500" || headless["Cache read"] != "500" || headless["Est. cost"] != "1.00 USD (1 unavailable)" || headless["Cache create"] != "Not measured" {
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
