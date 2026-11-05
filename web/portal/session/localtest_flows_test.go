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
	if strings.Count(string(serveLocalTest(instance, localTestRequest(http.MethodGet, SessionsPath)).Body.String()), "fixture-launch-") == 0 {
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
