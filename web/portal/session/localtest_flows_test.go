//go:build portallocaltest

package session

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/web/portal"
)

var localTestSendForm = regexp.MustCompile(`<form class="composer" method="post" action="([^"]+)"><input type="hidden" name="csrf" value="([^"]+)"><input type="hidden" name="requestID" value="([^"]+)"><input type="hidden" name="session" value="([^"]+)"><input type="hidden" name="sessionUID" value="([^"]+)">`)

func localTestPost(instance *LocalTest, path, origin string, form url.Values) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	r.Host, r.RemoteAddr = localTestListen.String(), "127.0.0.1:50000"
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", origin)
	return serveLocalTest(instance, r)
}

func localTestCallsTo(instance *LocalTest, path string) []localTestCall {
	var out []localTestCall
	for _, call := range instance.calls.snapshot() {
		if call.Path == path {
			out = append(out, call)
		}
	}
	return out
}

// TestLocalTestSendFlow drives the production send handler: the Sessions page
// renders a working composer, one POST reaches the fixture operator exactly
// once, and a repeated or reloaded submission answers from the receipt store
// without a second send.
func TestLocalTestSendFlow(t *testing.T) {
	instance := newLocalTestForTest(t)
	page := serveLocalTest(instance, localTestRequest(http.MethodGet, SessionsPath))
	if page.Code != http.StatusOK || page.Header().Get("Content-Security-Policy") != portal.ProductSendPolicy {
		t.Fatalf("sessions page %d policy %q", page.Code, page.Header().Get("Content-Security-Policy"))
	}
	var form url.Values
	for _, m := range localTestSendForm.FindAllStringSubmatch(page.Body.String(), -1) {
		if m[4] == "fixture-coordinator" {
			if m[1] != SessionMessagesPath {
				t.Fatalf("form posts to %q", m[1])
			}
			form = url.Values{"csrf": {m[2]}, "requestID": {m[3]}, "session": {m[4]}, "sessionUID": {m[5]}, "text": {"Fixture message from the local test."}}
		}
	}
	if form == nil {
		t.Fatal("no send form for fixture-coordinator")
	}
	origin := "http://" + localTestListen.String()
	first := localTestPost(instance, SessionMessagesPath, origin, form)
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), "Delivered") || !strings.Contains(first.Body.String(), `id="`+LocalTestBannerID+`"`) {
		t.Fatalf("send receipt %d: %.400s", first.Code, first.Body.String())
	}
	calls := localTestCallsTo(instance, apiv1.PortalSessionMessagesPath)
	if len(calls) != 1 || calls[0].Method != http.MethodPost {
		t.Fatalf("fixture operator saw %d sends", len(calls))
	}
	var sent apiv1.PortalMessageRequest
	if err := json.Unmarshal(calls[0].Body, &sent); err != nil {
		t.Fatal(err)
	}
	if sent.RequestID != form.Get("requestID") || sent.Project != "dev-env" || sent.SessionName != "fixture-coordinator" || sent.SessionUID != form.Get("sessionUID") || sent.Text != form.Get("text") || sent.ActorRef == "" || strings.Contains(sent.ActorRef, localTestSubject) {
		t.Fatalf("fixture operator received %+v", sent)
	}
	// Double submit and reload: the receipt store answers; no second send.
	if again := localTestPost(instance, SessionMessagesPath, origin, form); again.Code != http.StatusOK || !strings.Contains(again.Body.String(), "Delivered") {
		t.Fatalf("repeated submit %d", again.Code)
	}
	if reload := serveLocalTest(instance, localTestRequest(http.MethodGet, SessionMessagesPath+"/"+form.Get("requestID"))); reload.Code != http.StatusOK || !strings.Contains(reload.Body.String(), "Delivered") {
		t.Fatalf("receipt reload %d", reload.Code)
	}
	// The same request ID with other text is refused, as is a cross-origin post.
	changed := url.Values{}
	for k, v := range form {
		changed[k] = v
	}
	changed.Set("text", "Different text under the same request ID.")
	if w := localTestPost(instance, SessionMessagesPath, origin, changed); w.Code != http.StatusForbidden {
		t.Fatalf("changed text under one request ID: %d", w.Code)
	}
	changed.Set("requestID", strings.Repeat("Z", 43))
	if w := localTestPost(instance, SessionMessagesPath, "https://evil.example", changed); w.Code != http.StatusForbidden {
		t.Fatalf("cross-origin send: %d", w.Code)
	}
	if calls := localTestCallsTo(instance, apiv1.PortalSessionMessagesPath); len(calls) != 1 {
		t.Fatalf("fixture operator saw %d sends, want 1", len(calls))
	}
}

var localTestLaunchForm = regexp.MustCompile(`<form class="launch" method="post" action="([^"]+)"><input type="hidden" name="csrf" value="([^"]+)"><input type="hidden" name="requestID" value="([^"]+)">`)

// TestLocalTestLaunchFlow drives the production launch handler: the Sessions
// page renders the New agent form, a POST reaches the fixture operator, and
// the redirect selects the new fixture session. A repeated submission reaches
// the operator again with the same request and gets the same session, as the
// operator's per-request idempotency answers; other content under the same
// request ID is a conflict.
func TestLocalTestLaunchFlow(t *testing.T) {
	instance := newLocalTestForTest(t)
	page := serveLocalTest(instance, localTestRequest(http.MethodGet, SessionsPath))
	m := localTestLaunchForm.FindStringSubmatch(page.Body.String())
	if page.Code != http.StatusOK || m == nil || m[1] != portalLaunchesPath {
		t.Fatalf("no launch form on the Sessions page (status %d)", page.Code)
	}
	form := url.Values{"csrf": {m[2]}, "requestID": {m[3]}, "repo": {"dev-env"}, "mode": {"task"}, "model": {"claude-opus-5-5"}, "effort": {""}, "size": {"M"}, "prompt": {"Fixture task from the local test."}}
	origin := "http://" + localTestListen.String()
	first := localTestPost(instance, portalLaunchesPath, origin, form)
	location := first.Header().Get("Location")
	if first.Code != http.StatusSeeOther || !strings.HasPrefix(location, SessionsPath+"?session=fixture-uid-launch-") {
		t.Fatalf("launch %d location %q: %.300s", first.Code, location, first.Body.String())
	}
	calls := localTestCallsTo(instance, apiv1.PortalLaunchPath)
	if len(calls) != 1 {
		t.Fatalf("fixture operator saw %d launches", len(calls))
	}
	var sent apiv1.PortalLaunchRequest
	if err := json.Unmarshal(calls[0].Body, &sent); err != nil {
		t.Fatal(err)
	}
	if sent.RequestID != form.Get("requestID") || sent.Repo != "dev-env" || sent.Mode != "task" || sent.Model != "claude-opus-5-5" || sent.Size != "M" || sent.Prompt != form.Get("prompt") || sent.ActorRef == "" {
		t.Fatalf("fixture operator received %+v", sent)
	}
	// The redirect target shows the new session selected.
	selected := serveLocalTest(instance, localTestRequest(http.MethodGet, location))
	if selected.Code != http.StatusOK || !strings.Contains(selected.Body.String(), "fixture-launch-1") {
		t.Fatalf("launched session page %d", selected.Code)
	}
	// Double submit: same request again, same session, one launched session.
	again := localTestPost(instance, portalLaunchesPath, origin, form)
	if again.Code != http.StatusSeeOther || again.Header().Get("Location") != location {
		t.Fatalf("repeated launch %d location %q", again.Code, again.Header().Get("Location"))
	}
	if calls := localTestCallsTo(instance, apiv1.PortalLaunchPath); len(calls) != 2 || string(calls[1].Body) != string(calls[0].Body) {
		t.Fatalf("repeated launch reached the operator %d times or changed", len(calls))
	}
	// Other content under the same request ID is a conflict page.
	changed := url.Values{}
	for k, v := range form {
		changed[k] = v
	}
	changed.Set("prompt", "A different fixture task.")
	if w := localTestPost(instance, portalLaunchesPath, origin, changed); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `id="`+LocalTestBannerID+`"`) || strings.Contains(w.Body.String(), "A different fixture task.") {
		t.Fatalf("conflicting launch %d", w.Code)
	}
	if !strings.Contains(serveLocalTest(instance, localTestRequest(http.MethodGet, SessionsPath)).Body.String(), "fixture-launch-1") {
		t.Fatal("launched session missing from Sessions")
	}
	// Cross-origin and tampered posts are refused before the operator.
	before := len(localTestCallsTo(instance, apiv1.PortalLaunchPath))
	if w := localTestPost(instance, portalLaunchesPath, "https://evil.example", form); w.Code != http.StatusForbidden {
		t.Fatalf("cross-origin launch %d", w.Code)
	}
	tampered := url.Values{}
	for k, v := range form {
		tampered[k] = v
	}
	tampered.Set("repo", "not-in-scope")
	if w := localTestPost(instance, portalLaunchesPath, origin, tampered); w.Code != http.StatusForbidden {
		t.Fatalf("out-of-scope repo %d", w.Code)
	}
	if after := len(localTestCallsTo(instance, apiv1.PortalLaunchPath)); after != before {
		t.Fatalf("refused launches reached the operator: %d -> %d", before, after)
	}
}

// TestLocalTestHistoryFlow drives the production conversation source: the
// Sessions page links a history target's conversation, selecting it reads the
// fixture operator's conversation route once per view, and a session outside
// the history scope is never read.
func TestLocalTestHistoryFlow(t *testing.T) {
	instance := newLocalTestForTest(t)
	const uid = "fixture-uid-coordinator"
	conversation := apiv1.PortalConversationPrefix + uid + "/conversation"
	page := serveLocalTest(instance, localTestRequest(http.MethodGet, SessionsPath))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `<a href="`+SessionsPath+`?session=`+uid+`">Load this session's conversation</a>`) {
		t.Fatalf("no conversation link for the coordinator (status %d)", page.Code)
	}
	if calls := localTestCallsTo(instance, conversation); len(calls) != 0 {
		t.Fatalf("the list view read %d conversations", len(calls))
	}
	for view := 1; view <= 2; view++ {
		selected := serveLocalTest(instance, localTestRequest(http.MethodGet, SessionsPath+"?session="+uid))
		body := selected.Body.String()
		if selected.Code != http.StatusOK || !strings.Contains(body, "Fixture conversation: review the portal composer.") || !strings.Contains(body, "Fixture reply: the composer renders.") {
			t.Fatalf("view %d: conversation missing (status %d)", view, selected.Code)
		}
		calls := localTestCallsTo(instance, conversation)
		if len(calls) != view || calls[view-1].Method != http.MethodGet {
			t.Fatalf("view %d: fixture operator saw %d conversation reads", view, len(calls))
		}
	}
	// A session outside the history scope shows no conversation and is not read.
	outside := serveLocalTest(instance, localTestRequest(http.MethodGet, SessionsPath+"?session=fixture-uid-portal-polish"))
	if outside.Code != http.StatusOK || strings.Contains(outside.Body.String(), "Fixture conversation:") {
		t.Fatalf("out-of-scope session %d", outside.Code)
	}
	if calls := localTestCallsTo(instance, apiv1.PortalConversationPrefix+"fixture-uid-portal-polish/conversation"); len(calls) != 0 {
		t.Fatalf("out-of-scope conversation read %d times", len(calls))
	}
}

var localTestAnswerForm = regexp.MustCompile(`<form class="ask-answer" method="post" action="([^"]+)"[^>]*><input type="hidden" name="csrf" value="([^"]+)"><input type="hidden" name="requestID" value="([^"]+)"><input type="hidden" name="askID" value="([^"]+)">`)

func localTestAnswerFormFor(t *testing.T, instance *LocalTest, id string) url.Values {
	t.Helper()
	page := serveLocalTest(instance, localTestRequest(http.MethodGet, AsksPath+"?ask="+id))
	m := localTestAnswerForm.FindStringSubmatch(page.Body.String())
	if page.Code != http.StatusOK || m == nil || m[1] != AsksPath+"/answer" || m[4] != id {
		t.Fatalf("no answer form for %s (status %d)", id, page.Code)
	}
	return url.Values{"csrf": {m[2]}, "requestID": {m[3]}, "askID": {m[4]}, "optionId": {""}, "text": {""}}
}

// TestLocalTestAsksAnswerFlow drives the production Asks answer route: one
// answer reaches the fixture operator once and the page then shows it
// recorded; a double submit, a retry under a new request ID and a reload are
// answered from the recorded state without a second operator call; a
// different answer after the ask is closed and a cross-origin post are
// refused before the operator.
func TestLocalTestAsksAnswerFlow(t *testing.T) {
	instance := newLocalTestForTest(t)
	const info, approval = "ask-0000000000000001", "ask-0000000000000002"
	origin := "http://" + localTestListen.String()
	form := localTestAnswerFormFor(t, instance, info)
	form.Set("optionId", "small")
	form.Set("text", "Fixture answer from the local test.")
	first := localTestPost(instance, AsksPath+"/answer", origin, form)
	if first.Code != http.StatusSeeOther || first.Header().Get("Location") != AsksPath+"?ask="+info {
		t.Fatalf("answer %d location %q: %.300s", first.Code, first.Header().Get("Location"), first.Body.String())
	}
	answers := localTestCallsTo(instance, apiv1.PortalAskAnswerPath(info))
	if len(answers) != 1 || answers[0].Method != http.MethodPost {
		t.Fatalf("fixture operator saw %d answers", len(answers))
	}
	var sent apiv1.AskAnswerRequest
	if err := json.Unmarshal(answers[0].Body, &sent); err != nil {
		t.Fatal(err)
	}
	if sent.OptionID != "small" || sent.Text != "Fixture answer from the local test." || sent.Approved != nil || sent.Actor.Issuer != localTestIssuer || sent.Actor.Subject != localTestSubject {
		t.Fatalf("fixture operator received %+v", sent)
	}
	recorded := serveLocalTest(instance, localTestRequest(http.MethodGet, AsksPath+"?ask="+info))
	if recorded.Code != http.StatusOK || !strings.Contains(recorded.Body.String(), "Recorded answer") {
		t.Fatalf("answered ask page %d", recorded.Code)
	}
	for _, m := range localTestAnswerForm.FindAllStringSubmatch(recorded.Body.String(), -1) {
		if m[4] == info {
			t.Fatal("the answered ask still offers an answer form")
		}
	}
	// Double submit, then the same answer under a new request ID: no new call.
	if again := localTestPost(instance, AsksPath+"/answer", origin, form); again.Code != http.StatusSeeOther {
		t.Fatalf("repeated answer %d", again.Code)
	}
	retry := url.Values{}
	for k, v := range form {
		retry[k] = v
	}
	retry.Set("requestID", strings.Repeat("R", 43))
	if w := localTestPost(instance, AsksPath+"/answer", origin, retry); w.Code != http.StatusSeeOther {
		t.Fatalf("same answer under a new request ID %d", w.Code)
	}
	// A different answer to the closed ask, and a cross-origin post, are refused.
	retry.Set("requestID", strings.Repeat("S", 43))
	retry.Set("text", "A different fixture answer.")
	if w := localTestPost(instance, AsksPath+"/answer", origin, retry); w.Code == http.StatusSeeOther {
		t.Fatalf("different answer to a closed ask %d", w.Code)
	}
	if w := localTestPost(instance, AsksPath+"/answer", "https://evil.example", form); w.Code != http.StatusForbidden {
		t.Fatalf("cross-origin answer %d", w.Code)
	}
	if answers := localTestCallsTo(instance, apiv1.PortalAskAnswerPath(info)); len(answers) != 1 {
		t.Fatalf("fixture operator saw %d answers, want 1", len(answers))
	}
	// An approval needs the decision and the confirmation.
	decision := localTestAnswerFormFor(t, instance, approval)
	decision.Set("decision", "approve")
	if w := localTestPost(instance, AsksPath+"/answer", origin, decision); w.Code == http.StatusSeeOther {
		t.Fatal("approval without confirmation was sent")
	}
	decision = localTestAnswerFormFor(t, instance, approval)
	decision.Set("decision", "approve")
	decision.Set("confirm", "yes")
	if w := localTestPost(instance, AsksPath+"/answer", origin, decision); w.Code != http.StatusSeeOther {
		t.Fatalf("approval %d: %.300s", w.Code, w.Body.String())
	}
	approvals := localTestCallsTo(instance, apiv1.PortalAskAnswerPath(approval))
	if len(approvals) != 1 || !strings.Contains(string(approvals[0].Body), `"approved":true`) {
		t.Fatalf("fixture operator saw %d approvals", len(approvals))
	}
}
