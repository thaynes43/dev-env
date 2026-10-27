package session

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/web/portal"
)

// countingForms hands out request IDs from a fixed sequence.
type countingForms struct {
	n    int
	ids  []string
	fail bool
}

func (c *countingForms) NewRequestID() (string, error) {
	if c.fail {
		return "", errors.New("no request ID")
	}
	c.n++
	if len(c.ids) > 0 {
		return c.ids[(c.n-1)%len(c.ids)], nil
	}
	return fmt.Sprintf("request-%012d", c.n), nil
}

func sendTestPage(t *testing.T) portal.ProductPage {
	t.Helper()
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	busy := strings.Replace(unknownActivity, `"agentState":"idle"`, `"agentState":"busy"`, 1)
	decoded, err := decodeSessionPage([]byte(activityPage(activityRow("remote-idle", unknownActivity), activityRow("task-busy", reportedActivity), activityRow("remote-busy", busy), activityRowPhase("remote-suspended", "Suspended", unknownActivity))), now)
	if err != nil {
		t.Fatal(err)
	}
	return sessionsPage(decoded.Sessions, true, "observed", now, portal.ProductFilter{})
}

func TestWithSendFormsBindsOnlyReachableSessions(t *testing.T) {
	csrf := strings.Repeat("k", 43)
	page := sendTestPage(t)
	forms := &countingForms{}
	got := withSendForms(page, csrf, forms)
	if forms.n != 2 {
		t.Fatalf("%d request IDs drawn, want one per reachable session (2)", forms.n)
	}
	for name, want := range map[string]bool{"remote-idle": true, "remote-busy": true, "task-busy": false, "remote-suspended": false} {
		m := findRow(t, got, name).Messages
		if (m.Send != nil) != want {
			t.Fatalf("%s: send form %v, want %v", name, m.Send != nil, want)
		}
		if want && (m.Send.Action != SessionMessagesPath || m.Send.CSRF != csrf || m.Gate != sendGate || m.Route.Text != "Portal and CLI" || !strings.HasPrefix(m.Command, "agent-run msg "+name) || m.Session != name || m.SessionUID != "uid-"+name) {
			t.Fatalf("%s: send %+v gate %q", name, m.Send, m.Gate)
		}
		if !want && (m.Gate != messagesGate || m.Route.Text == "Portal and CLI") {
			t.Fatalf("%s: gate %q", name, m.Gate)
		}
	}
	if findRow(t, page, "remote-idle").Messages.Send != nil {
		t.Fatal("decoration changed the input page")
	}
	for _, forms := range []*countingForms{{fail: true}, {ids: []string{"request-same-0001"}}} {
		unchanged := withSendForms(page, csrf, forms)
		for _, name := range []string{"remote-idle", "remote-busy"} {
			if findRow(t, unchanged, name).Messages.Send != nil {
				t.Fatalf("%s got a form after an ID failure or repeat", name)
			}
		}
	}
	if findRow(t, withSendForms(page, "", &countingForms{}), "remote-idle").Messages.Send != nil || findRow(t, withSendForms(page, csrf, nil), "remote-idle").Messages.Send != nil {
		t.Fatal("a form rendered without a CSRF value or a send route")
	}
}

func TestServeProductBindsSendFormsToTheCallersSession(t *testing.T) {
	f := setupAuth(t)
	f.login(t)
	now := f.now
	cert, ca := mintNamed(t, operatorPortalHost)
	srv := serveTLS(t, cert, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sessions":[{"name":"remote-idle","uid":"uid-remote-idle","repo":"dev-env","phase":"Running","observedAt":"2026-10-23T16:00:00Z","freshness":"observed",` + unknownActivity + `}],"limit":50,"complete":true,"freshness":"observed","observedAt":"2026-10-23T16:00:00Z"}`))
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
	_, record, err := f.e.current(f.request(http.MethodGet, SessionsPath, ""))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		send   SendForms
		policy string
		form   bool
	}{{nil, portal.ProductPolicy, false}, {&countingForms{}, portal.ProductSendPolicy, true}} {
		handler := NewAssembly(AssemblyConfig{Session: f.e, Authorizer: authorizer, Reader: portal.UnavailableReader{}, Sessions: sessions, Send: tc.send, FreshFor: time.Minute, Now: func() time.Time { return now }})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, f.request(http.MethodGet, SessionsPath, ""))
		body := w.Body.String()
		if w.Code != http.StatusOK || w.Header().Get("Content-Security-Policy") != tc.policy {
			t.Fatalf("send %v: status %d policy %q", tc.form, w.Code, w.Header().Get("Content-Security-Policy"))
		}
		if strings.Contains(body, "<form") != tc.form || strings.Contains(body, `name="csrf" value="`+record.CSRF+`"`) != tc.form {
			t.Fatalf("send %v: form or CSRF binding wrong: %s", tc.form, body)
		}
		if tc.form && (!strings.Contains(body, `name="requestID" value="request-000000000001"`) || !strings.Contains(body, `name="sessionUID" value="uid-remote-idle"`)) {
			t.Fatalf("form lacks its request ID or target: %s", body)
		}
	}
	w := httptest.NewRecorder()
	NewAssembly(AssemblyConfig{Session: f.e, Authorizer: authorizer, Reader: portal.UnavailableReader{}, Sessions: sessions, Send: &countingForms{}, FreshFor: time.Minute, Now: func() time.Time { return now }}).ServeHTTP(w, f.request(http.MethodGet, TasksPath, ""))
	if strings.Contains(w.Body.String(), "<form") || w.Header().Get("Content-Security-Policy") != portal.ProductPolicy {
		t.Fatal("Tasks page allowed form posts")
	}
}
