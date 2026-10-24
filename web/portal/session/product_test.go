package session

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/web/portal"
)

func fixtureOperatorToken(aud string, exp time.Time) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payload, _ := json.Marshal(map[string]any{"aud": aud, "exp": exp.Unix()})
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func TestSessionsAndTasksAreIndependent(t *testing.T) {
	f := setupAuth(t)
	f.login(t)
	now := f.now
	var seen []string
	operatorCert, operatorCA := mintNamed(t, operatorPortalHost)
	opCert, opCA := mintNamed(t, openProjectPortalHost)
	operatorSrv := serveTLS(t, operatorCert, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		if r.URL.Path != "/v1/sessions" || r.URL.Query().Get("limit") != "50" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		if strings.Contains(r.URL.RawQuery, "repo=") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.Header.Get("Authorization"), "expired") {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"sessions":[{"name":"worker","uid":"uid-1","repo":"dev-env","phase":"Running","observedAt":"2026-10-24T20:00:00Z","freshness":"observed","prompt":"SECRET-PROMPT"}],"limit":50,"complete":true,"freshness":"observed","observedAt":"2026-10-24T20:00:00Z"}`))
	}))
	opSrv := serveTLS(t, opCert, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != openProjectProjectPath || r.Header.Get("Accept") != mediaHAL {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("pageSize") != "50" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/hal+json")
		_, _ = w.Write([]byte(`{"total":1,"count":1,"_embedded":{"elements":[{"id":42,"subject":"Canonical subject","createdAt":"2026-10-24T20:00:00Z","updatedAt":"2026-10-24T20:01:00Z","customField1":"Accountable owner","customField3":"dev-env; haynes-ops","description":{"raw":"SECRET-DESCRIPTION"},"_links":{"project":{"href":"/api/v3/projects/3"},"status":{"title":"In progress"},"assignee":{"title":"Assignee"},"responsible":{"title":"Responsible"}},"_embedded":{"project":{"id":3,"identifier":"haynes-engineering"}}}]}}`))
	}))
	dial := routeDial(map[string]string{operatorPortalHost: operatorSrv, openProjectPortalHost: opSrv})
	dir := t.TempDir()
	operatorToken := filepath.Join(dir, "operator")
	opToken := filepath.Join(dir, "openproject")
	if err := os.WriteFile(operatorToken, []byte(fixtureOperatorToken("dev-env-operator", now.Add(time.Hour))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(opToken, []byte("op-token-canary\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sessions, err := newSessionSource(operatorToken, operatorCA, func() time.Time { return now }, dial)
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := newTaskSource(opToken, opCA, func() time.Time { return now }, dial)
	if err != nil {
		t.Fatal(err)
	}
	authorizer := currentFixture(f, func(*http.Request) (*http.Response, error) { return userInfoResponse(currentAllow), nil })
	handler := NewAssembly(AssemblyConfig{Session: f.e, Authorizer: authorizer, Reader: portal.UnavailableReader{}, Sessions: sessions, Tasks: nil, FreshFor: time.Minute, Now: func() time.Time { return now }})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, f.request(http.MethodGet, TasksPath, ""))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Tasks not connected") || strings.Contains(w.Body.String(), "No work packages") || strings.Contains(w.Body.String(), "Canonical subject") {
		t.Fatalf("tasks looked connected or empty: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, f.request(http.MethodGet, SessionsPath, ""))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Sessions unavailable") || strings.Contains(w.Body.String(), "SECRET-PROMPT") || strings.Contains(w.Body.String(), "worker") {
		t.Fatalf("prompt-bearing session was rendered: %d %s", w.Code, w.Body.String())
	}
	operatorCert, operatorCA = mintNamed(t, operatorPortalHost)
	cleanSrv := serveTLS(t, operatorCert, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sessions":[{"name":"worker","uid":"uid-1","repo":"dev-env","phase":"Running","observedAt":"2026-10-24T20:00:00Z","freshness":"observed"}],"limit":50,"complete":true,"freshness":"observed","observedAt":"2026-10-24T20:00:00Z"}`))
	}))
	sessions, err = newSessionSource(operatorToken, operatorCA, func() time.Time { return now }, routeDial(map[string]string{operatorPortalHost: cleanSrv, openProjectPortalHost: opSrv}))
	if err != nil {
		t.Fatal(err)
	}
	tasks, err = newTaskSource(opToken, opCA, func() time.Time { return now }, routeDial(map[string]string{operatorPortalHost: cleanSrv, openProjectPortalHost: opSrv}))
	if err != nil {
		t.Fatal(err)
	}
	handler = NewAssembly(AssemblyConfig{Session: f.e, Authorizer: authorizer, Reader: portal.UnavailableReader{}, Sessions: sessions, Tasks: tasks, FreshFor: time.Minute, Now: func() time.Time { return now }})
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, f.request(http.MethodGet, SessionsPath, ""))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "worker") || !strings.Contains(w.Body.String(), "Running") || strings.Contains(w.Body.String(), "SECRET-PROMPT") {
		t.Fatalf("session projection missing: %s", w.Body.String())
	}
	rotated := fixtureOperatorToken("dev-env-operator", now.Add(2*time.Hour))
	if err := os.WriteFile(operatorToken, []byte(rotated+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, f.request(http.MethodGet, SessionsPath, ""))
	if w.Code != http.StatusOK || len(seen) < 2 || seen[len(seen)-1] == seen[len(seen)-2] || !strings.Contains(seen[len(seen)-1], rotated) {
		t.Fatalf("rotated token was not read: %v", seen)
	}
	if err := os.WriteFile(operatorToken, []byte(fixtureOperatorToken("https://kubernetes.default.svc", now.Add(time.Hour))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, f.request(http.MethodGet, SessionsPath, ""))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Sessions unavailable") || strings.Contains(w.Body.String(), "worker") {
		t.Fatalf("wrong audience rendered sessions: %s", w.Body.String())
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, f.request(http.MethodGet, TasksPath, ""))
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, "Canonical subject") || !strings.Contains(body, "In progress") || !strings.Contains(body, "Accountable owner") || !strings.Contains(body, "dev-env; haynes-ops") || !strings.Contains(body, "https://work.haynesnetwork.com/work_packages/42") || strings.Contains(body, "SECRET-DESCRIPTION") {
		t.Fatalf("task projection: %s", body)
	}
	foreignSrv := serveTLS(t, opCert, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/hal+json")
		_, _ = w.Write([]byte(`{"total":1,"_embedded":{"elements":[{"id":9,"subject":"FOREIGN-SUBJECT","_links":{"project":{"href":"/api/v3/projects/8"}}}]}}`))
	}))
	tasks, err = newTaskSource(opToken, opCA, func() time.Time { return now }, routeDial(map[string]string{openProjectPortalHost: foreignSrv}))
	if err != nil {
		t.Fatal(err)
	}
	handler = NewAssembly(AssemblyConfig{Session: f.e, Authorizer: authorizer, Reader: portal.UnavailableReader{}, Sessions: nil, Tasks: tasks, FreshFor: time.Minute, Now: func() time.Time { return now }})
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, f.request(http.MethodGet, TasksPath, ""))
	if !strings.Contains(w.Body.String(), "Tasks unavailable") || strings.Contains(w.Body.String(), "FOREIGN-SUBJECT") || strings.Contains(w.Body.String(), "The completed task read returned no work packages") {
		t.Fatalf("foreign project rendered: %s", w.Body.String())
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, f.request(http.MethodGet, SessionsPath, ""))
	if !strings.Contains(w.Body.String(), "Sessions not connected") {
		t.Fatalf("missing sessions looked empty: %s", w.Body.String())
	}
}

func TestProductFailsClosedOnRedirectExpiryAndUntrustedCA(t *testing.T) {
	f := setupAuth(t)
	f.login(t)
	now := f.now
	operatorCert, operatorCA := mintNamed(t, operatorPortalHost)
	opCert, opCA := mintNamed(t, openProjectPortalHost)
	redirectSrv := serveTLS(t, operatorCert, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://evil.example/v1/sessions", http.StatusFound)
	}))
	staleSrv := serveTLS(t, operatorCert, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sessions":[{"name":"older","uid":"uid-2","repo":"dev-env","phase":"Running","observedAt":"2026-10-24T18:00:00Z","freshness":"stale"}],"limit":50,"complete":true,"freshness":"stale","observedAt":"2026-10-24T20:00:00Z"}`))
	}))
	foreignNext := serveTLS(t, opCert, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/hal+json")
		_, _ = w.Write([]byte(`{"total":100,"_embedded":{"elements":[{"id":7,"subject":"FOLLOWED-SUBJECT","customField3":"","_links":{"project":{"href":"/api/v3/projects/3"}},"_embedded":{"project":{"id":3,"identifier":"haynes-engineering"}}}]},"_links":{"nextByOffset":{"href":"https://evil.example/api/v3/projects/3/work_packages?pageSize=50&offset=2"}}}`))
	}))
	dir := t.TempDir()
	operatorToken := filepath.Join(dir, "operator")
	opToken := filepath.Join(dir, "openproject")
	if err := os.WriteFile(operatorToken, []byte(fixtureOperatorToken("dev-env-operator", now.Add(time.Hour))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(opToken, []byte("op-token-canary\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	authorizer := currentFixture(f, func(*http.Request) (*http.Response, error) { return userInfoResponse(currentAllow), nil })
	sessions, err := newSessionSource(operatorToken, operatorCA, func() time.Time { return now }, routeDial(map[string]string{operatorPortalHost: redirectSrv}))
	if err != nil {
		t.Fatal(err)
	}
	handler := NewAssembly(AssemblyConfig{Session: f.e, Authorizer: authorizer, Reader: portal.UnavailableReader{}, Sessions: sessions, Tasks: nil, FreshFor: time.Minute, Now: func() time.Time { return now }})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, f.request(http.MethodGet, SessionsPath, ""))
	if !strings.Contains(w.Body.String(), "Sessions unavailable") || strings.Contains(w.Body.String(), "evil.example") {
		t.Fatalf("redirect was followed: %s", w.Body.String())
	}
	sessions, err = newSessionSource(operatorToken, operatorCA, func() time.Time { return now }, routeDial(map[string]string{operatorPortalHost: staleSrv}))
	if err != nil {
		t.Fatal(err)
	}
	handler = NewAssembly(AssemblyConfig{Session: f.e, Authorizer: authorizer, Reader: portal.UnavailableReader{}, Sessions: sessions, Tasks: nil, FreshFor: time.Minute, Now: func() time.Time { return now }})
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, f.request(http.MethodGet, SessionsPath, ""))
	if !strings.Contains(w.Body.String(), "older") || !strings.Contains(w.Body.String(), "Session freshness is stale.") {
		t.Fatalf("stale session was hidden: %s", w.Body.String())
	}
	if err := os.WriteFile(operatorToken, []byte(fixtureOperatorToken("dev-env-operator", now.Add(-time.Minute))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, f.request(http.MethodGet, SessionsPath, ""))
	if !strings.Contains(w.Body.String(), "Sessions unavailable") || strings.Contains(w.Body.String(), "older") {
		t.Fatalf("expired token rendered sessions: %s", w.Body.String())
	}
	_, otherCA := mintNamed(t, operatorPortalHost)
	if err := os.WriteFile(operatorToken, []byte(fixtureOperatorToken("dev-env-operator", now.Add(time.Hour))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	untrusted, err := newSessionSource(operatorToken, otherCA, func() time.Time { return now }, routeDial(map[string]string{operatorPortalHost: staleSrv}))
	if err != nil {
		t.Fatal(err)
	}
	handler = NewAssembly(AssemblyConfig{Session: f.e, Authorizer: authorizer, Reader: portal.UnavailableReader{}, Sessions: untrusted, Tasks: nil, FreshFor: time.Minute, Now: func() time.Time { return now }})
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, f.request(http.MethodGet, SessionsPath, ""))
	if !strings.Contains(w.Body.String(), "Sessions unavailable") || strings.Contains(w.Body.String(), "older") {
		t.Fatalf("untrusted CA rendered sessions: %s", w.Body.String())
	}
	tasks, err := newTaskSource(opToken, opCA, func() time.Time { return now }, routeDial(map[string]string{openProjectPortalHost: foreignNext}))
	if err != nil {
		t.Fatal(err)
	}
	handler = NewAssembly(AssemblyConfig{Session: f.e, Authorizer: authorizer, Reader: portal.UnavailableReader{}, Sessions: nil, Tasks: tasks, FreshFor: time.Minute, Now: func() time.Time { return now }})
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, f.request(http.MethodGet, TasksPath, ""))
	if !strings.Contains(w.Body.String(), "Tasks unavailable") || strings.Contains(w.Body.String(), "FOLLOWED-SUBJECT") {
		t.Fatalf("foreign next link was followed: %s", w.Body.String())
	}
}
