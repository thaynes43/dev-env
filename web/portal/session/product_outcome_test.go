package session

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/web/portal"
)

var outcomeNow = time.Date(2026, 10, 23, 16, 0, 0, 0, time.UTC)

const (
	outcomePR    = "https://github.com/thaynes43/dev-env/pull/759"
	outcomeOpsPR = "https://github.com/thaynes43/haynes-ops/pull/4100"
	outcomePod   = "0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0"
)

var outcomeHead = strings.Repeat("Ab", 20)

// outcomeRecord is a valid retained outcome for the session named name, as
// the operator sends it to a portal that asks for it.
func outcomeRecord(name, summary string) string {
	return `{"sessionUID":"uid-` + name + `","podUID":"` + outcomePod + `","record":{"prURLs":["` + outcomePR + `","` + outcomeOpsPR + `"],"headSHAs":["` + outcomeHead + `"],"summary":` + summary + `}}`
}

// outcomeRow is a finished task in phase with its process exit result and,
// unless outcome is empty, its retained outcome.
func outcomeRow(name, phase, result, outcome string) string {
	activity := filterActivity("task", "claude", result, "2026-10-23T15:00:00Z")
	if outcome != "" {
		activity = strings.Replace(activity, `"activity":{`, `"activity":{"taskOutcome":`+outcome+`,`, 1)
	}
	return filterRow(name, "dev-env", phase, activity)
}

func outcomeSection(t *testing.T, row portal.ProductRow) portal.ProductSection {
	t.Helper()
	for _, s := range row.Sections {
		if s.Title == "Task outcome" {
			return s
		}
	}
	t.Fatalf("%s has no Task outcome panel: %+v", row.Title, row.Sections)
	return portal.ProductSection{}
}

// fieldValues lists a panel's values for one label, in order.
func fieldValues(s portal.ProductSection, label string) []string {
	var out []string
	for _, f := range s.Fields {
		if f.Label == label {
			out = append(out, f.Value)
		}
	}
	return out
}

func renderOutcomePage(t *testing.T, rows ...string) string {
	t.Helper()
	decoded, err := decodeSessionPage([]byte(`{"sessions":[`+strings.Join(rows, ",")+`],"limit":50,"complete":true,"freshness":"observed","observedAt":"2026-10-23T15:59:00Z"}`), outcomeNow)
	if err != nil {
		t.Fatal(err)
	}
	body, err := portal.RenderProduct(sessionsPage(decoded.Sessions, true, "observed", outcomeNow, portal.ProductFilter{}))
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// The portal asks for the retained outcome and reads it when the operator
// sends it. An operator that predates it sends rows without the key, and those
// still read, as No outcome recorded.
func TestTaskOutcomeReaderRequestsAndAcceptsTheRecord(t *testing.T) {
	body := `{"sessions":[` + outcomeRow("done", "Suspended", "Succeeded", outcomeRecord("done", `"Opened PR 759; lead review next."`)) + "," +
		outcomeRow("older", "Suspended", "Succeeded", "") + `],"limit":50,"complete":true,"freshness":"observed","observedAt":"2026-10-23T15:59:00Z"}`
	asked := ""
	s := sessionReadFixture(t, func() time.Time { return outcomeNow }, func(r *http.Request) (*http.Response, error) {
		asked = r.Header.Get(apiv1.TaskOutcomeHeader)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	page, err := s.Read(t.Context(), portal.ProductFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if asked != apiv1.TaskOutcomeV1 {
		t.Fatalf("the read did not ask for the outcome: %q", asked)
	}
	done := outcomeSection(t, findRow(t, page, "done"))
	if len(done.States) != 1 || done.States[0].Status.Text != "Recorded" ||
		strings.Join(fieldValues(done, "Summary"), "|") != "Opened PR 759; lead review next." ||
		strings.Join(fieldValues(done, "Pull request"), "|") != "thaynes43/dev-env#759|thaynes43/haynes-ops#4100" ||
		strings.Join(fieldValues(done, "Head commit"), "|") != "abababa" ||
		strings.Join(fieldValues(done, "Original session"), "|") != "uid-done" ||
		strings.Join(fieldValues(done, "Original pod"), "|") != "0f1e2d3c…" {
		t.Fatalf("recorded outcome panel %+v", done)
	}
	hrefs := 0
	for _, f := range done.Fields {
		if f.Href != "" {
			hrefs++
			if f.Href != outcomePR && f.Href != outcomeOpsPR {
				t.Fatalf("unexpected link %q", f.Href)
			}
		}
	}
	if hrefs != 2 {
		t.Fatalf("want two pull request links, got %d", hrefs)
	}
	older := outcomeSection(t, findRow(t, page, "older"))
	if len(older.States) != 1 || older.States[0].Status.Text != "Unknown" || older.States[0].Text != "No outcome recorded." || len(older.Fields) != 0 {
		t.Fatalf("row without the key %+v", older)
	}

	// The decoded record is exactly what the operator sent.
	decoded, err := decodeSessionPage([]byte(body), outcomeNow)
	if err != nil {
		t.Fatal(err)
	}
	o := decoded.Sessions[0].Activity.TaskOutcome
	if o == nil || o.SessionUID != "uid-done" || o.PodUID != outcomePod || o.Record.Summary != "Opened PR 759; lead review next." ||
		strings.Join(o.Record.PRURLs, " ") != outcomePR+" "+outcomeOpsPR || strings.Join(o.Record.HeadSHAs, " ") != outcomeHead {
		t.Fatalf("decoded outcome %+v", o)
	}
	if decoded.Sessions[1].Activity.TaskOutcome != nil {
		t.Fatal("a row without the key gained an outcome")
	}
}

// Like every other optional activity key, an outcome the operator could not
// have sent fails the whole read rather than being shown in part.
func TestTaskOutcomeInvalidIsRefused(t *testing.T) {
	valid := outcomeRecord("done", `"Ready for review"`)
	swap := func(old, new string) string {
		if !strings.Contains(valid, old) {
			t.Fatalf("fixture lacks %s", old)
		}
		return strings.Replace(valid, old, new, 1)
	}
	for _, tc := range []struct{ name, row string }{
		{"null", outcomeRow("done", "Suspended", "Succeeded", "null")},
		{"not an object", outcomeRow("done", "Suspended", "Succeeded", `"Ready"`)},
		{"unknown key", outcomeRow("done", "Suspended", "Succeeded", swap(`{"sessionUID"`, `{"extra":1,"sessionUID"`))},
		{"unknown record key", outcomeRow("done", "Suspended", "Succeeded", swap(`"record":{`, `"record":{"note":"x",`))},
		{"duplicate record key", outcomeRow("done", "Suspended", "Succeeded", swap(`"summary":`, `"summary":"a","summary":`))},
		{"missing record", outcomeRow("done", "Suspended", "Succeeded", `{"sessionUID":"uid-done","podUID":"`+outcomePod+`"}`)},
		{"missing summary", outcomeRow("done", "Suspended", "Succeeded", `{"sessionUID":"uid-done","podUID":"`+outcomePod+`","record":{"prURLs":["`+outcomePR+`"]}}`)},
		{"missing pod", outcomeRow("done", "Suspended", "Succeeded", swap(`"podUID":"`+outcomePod+`",`, ""))},
		{"empty summary", outcomeRow("done", "Suspended", "Succeeded", outcomeRecord("done", `""`))},
		{"summary not a string", outcomeRow("done", "Suspended", "Succeeded", outcomeRecord("done", `7`))},
		{"newline", outcomeRow("done", "Suspended", "Succeeded", outcomeRecord("done", `"line one\nline two"`))},
		{"control", outcomeRow("done", "Suspended", "Succeeded", outcomeRecord("done", `"bell\u0007"`))},
		{"bidi override", outcomeRow("done", "Suspended", "Succeeded", outcomeRecord("done", "\"\u202egnp.exe\""))},
		{"emoji", outcomeRow("done", "Suspended", "Succeeded", outcomeRecord("done", `"done 😀"`))},
		{"201 characters", outcomeRow("done", "Suspended", "Succeeded", outcomeRecord("done", `"`+strings.Repeat("x", 201)+`"`))},
		{"off-org link", outcomeRow("done", "Suspended", "Succeeded", swap(outcomePR, "https://github.com/someone/dev-env/pull/759"))},
		{"lookalike host", outcomeRow("done", "Suspended", "Succeeded", swap(outcomePR, "https://github.com.example/thaynes43/dev-env/pull/759"))},
		{"http link", outcomeRow("done", "Suspended", "Succeeded", swap(outcomePR, "http://github.com/thaynes43/dev-env/pull/759"))},
		{"script link", outcomeRow("done", "Suspended", "Succeeded", swap(outcomePR, "javascript:alert(1)"))},
		{"query", outcomeRow("done", "Suspended", "Succeeded", swap(outcomePR, outcomePR+"?x=1"))},
		{"dot-dot repository", outcomeRow("done", "Suspended", "Succeeded", swap(outcomePR, "https://github.com/thaynes43/../pull/759"))},
		{"four links", outcomeRow("done", "Suspended", "Succeeded", swap(`"prURLs":["`, `"prURLs":["`+outcomePR+`","`+outcomePR+`","`))},
		{"links empty", outcomeRow("done", "Suspended", "Succeeded", swap(`"prURLs":["`+outcomePR+`","`+outcomeOpsPR+`"]`, `"prURLs":[]`))},
		{"heads empty", outcomeRow("done", "Suspended", "Succeeded", swap(`"headSHAs":["`+outcomeHead+`"]`, `"headSHAs":[]`))},
		{"links null", outcomeRow("done", "Suspended", "Succeeded", swap(`"prURLs":["`+outcomePR+`","`+outcomeOpsPR+`"]`, `"prURLs":null`))},
		{"links not a list", outcomeRow("done", "Suspended", "Succeeded", swap(`"prURLs":["`+outcomePR+`","`+outcomeOpsPR+`"]`, `"prURLs":"`+outcomePR+`"`))},
		{"link not a string", outcomeRow("done", "Suspended", "Succeeded", swap(`"prURLs":["`, `"prURLs":[759,"`))},
		{"short head", outcomeRow("done", "Suspended", "Succeeded", swap(outcomeHead, "abc1234"))},
		{"head not hex", outcomeRow("done", "Suspended", "Succeeded", swap(outcomeHead, strings.Repeat("g", 40)))},
		{"four heads", outcomeRow("done", "Suspended", "Succeeded", swap(`"headSHAs":["`, `"headSHAs":["`+outcomeHead+`","`+outcomeHead+`","`+outcomeHead+`","`))},
		{"pod with a space", outcomeRow("done", "Suspended", "Succeeded", swap(outcomePod, "pod uid"))},
		{"another session's record", outcomeRow("done", "Suspended", "Succeeded", outcomeRecord("other", `"Ready for review"`))},
		{"interactive session", filterRow("done", "dev-env", "Running", strings.Replace(filterActivity("remote", "claude", "", "2026-10-23T15:00:00Z"), `"activity":{`, `"activity":{"taskOutcome":`+valid+`,`, 1))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page := `{"sessions":[` + tc.row + `],"limit":50,"complete":true,"freshness":"observed","observedAt":"2026-10-23T15:59:00Z"}`
			if decoded, err := decodeSessionPage([]byte(page), outcomeNow); err == nil {
				t.Fatalf("accepted %+v", decoded.Sessions[0].Activity.TaskOutcome)
			}
		})
	}
	// The valid record itself reads.
	if _, err := decodeSessionPage([]byte(`{"sessions":[`+outcomeRow("done", "Suspended", "Succeeded", valid)+`],"limit":50,"complete":true,"freshness":"observed","observedAt":"2026-10-23T15:59:00Z"}`), outcomeNow); err != nil {
		t.Fatal(err)
	}
}

// A hostile summary renders as escaped text. Only an allowlisted pull request
// URL becomes a link, and it opens outside the portal without a referrer.
func TestTaskOutcomeRendersEscapedTextAndAllowlistedLinks(t *testing.T) {
	hostile := `"<script>alert(1)</script> & <img src=x onerror=alert(2)> \"quoted\" 'single'"`
	html := renderOutcomePage(t, outcomeRow("done", "Suspended", "Succeeded", outcomeRecord("done", hostile)))
	for _, bad := range []string{"<script>alert", "<img src=x", `onerror=alert(2)>`} {
		if strings.Contains(html, bad) {
			t.Fatalf("summary rendered as markup: %s", bad)
		}
	}
	for _, want := range []string{
		"&lt;script&gt;alert(1)&lt;/script&gt; &amp; &lt;img src=x onerror=alert(2)&gt; &#34;quoted&#34; &#39;single&#39;",
		`<a class="kv-link" href="` + outcomePR + `" target="_blank" rel="noopener noreferrer">thaynes43/dev-env#759`,
		`<a class="kv-link" href="` + outcomeOpsPR + `" target="_blank" rel="noopener noreferrer">thaynes43/haynes-ops#4100`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("page lacks %s", want)
		}
	}
	if strings.Count(html, `class="kv-link"`) != 2 {
		t.Fatalf("want two outcome links, got %d", strings.Count(html, `class="kv-link"`))
	}

	// The renderer checks field links again, whatever a builder hands it.
	var fields []portal.ProductField
	for _, href := range []string{
		"javascript:alert(1)", "data:text/html,x", "http://github.com/thaynes43/dev-env/pull/1",
		"https://github.com.example/thaynes43/dev-env/pull/1", "https://github.com/other/dev-env/pull/1",
		"https://github.com/thaynes43/../pull/1", "https://github.com/thaynes43/dev-env/pull/1?x=1",
		"https://github.com/thaynes43/dev-env/pull/1#top", "//github.com/thaynes43/dev-env/pull/1",
		"https://github.com/thaynes43/dev-env/issues/1", outcomePR,
	} {
		fields = append(fields, portal.ProductField{Label: "Pull request", Value: "shown " + href, Href: href})
	}
	page := portal.ProductPage{Title: "Sessions", Connected: true, Complete: true, Groups: []portal.ProductGroup{{Title: "Stopped", Rows: []portal.ProductRow{{
		ID: "s1", Title: "done", Status: portal.ProductStatus{Text: "Exited 0", Tone: "muted"}, Sections: []portal.ProductSection{{Title: "Task outcome", Fields: fields}},
	}}}}}
	body, err := portal.RenderProduct(page)
	if err != nil {
		t.Fatal(err)
	}
	rendered := string(body)
	if strings.Count(rendered, `class="kv-link"`) != 1 || !strings.Contains(rendered, `href="`+outcomePR+`"`) {
		t.Fatalf("renderer linked a URL outside the allowlist:\n%s", rendered)
	}
	for _, f := range fields[:len(fields)-1] {
		if strings.Contains(rendered, `href="`+f.Href+`"`) {
			t.Fatalf("renderer linked %s", f.Href)
		}
	}
	if strings.Contains(rendered, "ZgotmplZ") {
		t.Fatal("a refused link reached the template")
	}
	if strings.Count(rendered, "shown ") != 11 {
		t.Fatalf("every value stays visible as text: %d", strings.Count(rendered, "shown "))
	}
	if page.Groups[0].Rows[0].Sections[0].Fields[0].Href != "javascript:alert(1)" {
		t.Fatal("the renderer changed the caller's page")
	}
}

// A reclaimed task is Suspended with its pod gone; the HOME expiry may later
// archive it. Its outcome reads from Session status alone, so the panel is the
// same in every phase, and Process exit stays its own row.
func TestTaskOutcomeShownAfterReclaim(t *testing.T) {
	for _, phase := range []string{"Suspended", "Archived", "Failed", "Running"} {
		t.Run(phase, func(t *testing.T) {
			decoded, err := decodeSessionPage([]byte(`{"sessions":[`+outcomeRow("done", phase, "Succeeded", outcomeRecord("done", `"Opened PR 759"`))+`],"limit":50,"complete":true,"freshness":"observed","observedAt":"2026-10-23T15:59:00Z"}`), outcomeNow)
			if err != nil {
				t.Fatal(err)
			}
			page := sessionsPage(decoded.Sessions, true, "observed", outcomeNow, portal.ProductFilter{})
			row := findRow(t, page, "done")
			if row.Sections[0].Title != "Task outcome" {
				t.Fatalf("the outcome does not lead the detail: %+v", row.Sections)
			}
			o := outcomeSection(t, row)
			if o.States[0].Status.Text != "Recorded" || strings.Join(fieldValues(o, "Summary"), "") != "Opened PR 759" || len(fieldValues(o, "Process exit")) != 0 {
				t.Fatalf("outcome panel %+v", o)
			}
			fields := rowFields(row)
			if fields["Process exit"] != "Exited 0" || fields["Phase"] != phase {
				t.Fatalf("process exit %q phase %q", fields["Process exit"], fields["Phase"])
			}
			if _, ok := fields["Task result"]; ok {
				t.Fatal("the process exit is still labelled as the task's result")
			}
			html := renderOutcomePage(t, outcomeRow("done", phase, "Succeeded", outcomeRecord("done", `"Opened PR 759"`)))
			for _, want := range []string{"<h3>Task outcome</h3>", "Opened PR 759", "<dt>Process exit</dt><dd>Exited 0</dd>", "<dt>Phase</dt><dd>" + phase + "</dd>"} {
				if !strings.Contains(html, want) {
					t.Fatalf("%s page lacks %s", phase, want)
				}
			}
		})
	}
}

// A clean process exit with no outcome is Unknown. Nothing on the page reads
// as success.
func TestTaskExitZeroWithoutOutcomeIsUnknown(t *testing.T) {
	for _, result := range []string{"Succeeded", "Unknown", "Failed", "TimedOut"} {
		t.Run(result, func(t *testing.T) {
			row := outcomeRow("done", "Suspended", result, "")
			decoded, err := decodeSessionPage([]byte(`{"sessions":[`+row+`],"limit":50,"complete":true,"freshness":"observed","observedAt":"2026-10-23T15:59:00Z"}`), outcomeNow)
			if err != nil {
				t.Fatal(err)
			}
			got := findRow(t, sessionsPage(decoded.Sessions, true, "observed", outcomeNow, portal.ProductFilter{}), "done")
			o := outcomeSection(t, got)
			if len(o.States) != 1 || o.States[0].Status != (portal.ProductStatus{Text: "Unknown", Tone: "muted"}) || o.States[0].Text != "No outcome recorded." || len(o.Fields) != 0 {
				t.Fatalf("missing outcome panel %+v", o)
			}
			want := map[string]string{"Succeeded": "Exited 0", "Unknown": "Unknown", "Failed": "Failed", "TimedOut": "Timed out"}[result]
			if rowFields(got)["Process exit"] != want {
				t.Fatalf("process exit %q, want %q", rowFields(got)["Process exit"], want)
			}
			if result == "Succeeded" && got.Status != (portal.ProductStatus{Text: "Exited 0", Tone: "muted"}) {
				t.Fatalf("a clean exit's badge %+v", got.Status)
			}
			html := renderOutcomePage(t, row)
			if strings.Contains(html, "Succeeded") || strings.Contains(html, "Success") || strings.Contains(html, "Recorded") {
				t.Fatal("a missing outcome reads as success or as recorded")
			}
			if !strings.Contains(html, `<span class="badge tone-muted">Unknown</span><span>No outcome recorded.</span>`) {
				t.Fatal("the Unknown outcome state is not rendered")
			}
		})
	}
}

// Only a task has an outcome panel.
func TestInteractiveSessionHasNoOutcomePanel(t *testing.T) {
	decoded, err := decodeSessionPage([]byte(`{"sessions":[`+filterRow("remote", "dev-env", "Running", filterActivity("remote", "claude", "", "2026-10-23T15:00:00Z"))+`],"limit":50,"complete":true,"freshness":"observed","observedAt":"2026-10-23T15:59:00Z"}`), outcomeNow)
	if err != nil {
		t.Fatal(err)
	}
	row := findRow(t, sessionsPage(decoded.Sessions, true, "observed", outcomeNow, portal.ProductFilter{}), "remote")
	for _, s := range row.Sections {
		if s.Title == "Task outcome" {
			t.Fatal("an interactive session shows a task outcome panel")
		}
	}
	if _, ok := rowFields(row)["Process exit"]; ok {
		t.Fatal("an interactive session shows a process exit")
	}
}
