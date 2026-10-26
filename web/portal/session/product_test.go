package session

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
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
		_, _ = w.Write([]byte(`{"sessions":[{"name":"worker","uid":"uid-1","repo":"dev-env","phase":"Running","observedAt":"2026-10-23T16:00:00Z","freshness":"observed","prompt":"SECRET-PROMPT"}],"limit":50,"complete":true,"freshness":"observed","observedAt":"2026-10-23T16:00:00Z"}`))
	}))
	opSrv := serveTLS(t, opCert, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != openProjectProjectPath || r.Header.Get("Accept") != mediaHAL || r.URL.RawQuery != "offset=1&pageSize=50&filters=%5B%5D" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/hal+json")
		_, _ = w.Write([]byte(`{"total":1,"count":1,"pageSize":50,"offset":1,"_embedded":{"elements":[{"id":42,"subject":"Canonical subject","createdAt":"2026-10-24T20:00:00Z","updatedAt":"2026-10-24T20:01:00Z","customField1":"Accountable owner","customField3":"dev-env; haynes-ops","description":{"raw":"SECRET-DESCRIPTION"},"_links":{"project":{"href":"/api/v3/projects/3"},"status":{"title":"Closed"},"assignee":{"title":"Assignee"},"responsible":{"title":"Responsible"}},"_embedded":{"project":{"id":3,"identifier":"haynes-engineering"}}}]},"_links":{}}`))
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
	var tasks *TaskSource
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
		if r.URL.Query().Get("view") != "activity" {
			http.Error(w, "view", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"sessions":[{"name":"worker","uid":"uid-1","repo":"dev-env","phase":"Running","observedAt":"2026-10-23T16:00:00Z","freshness":"observed",` + unknownActivity + `}],"limit":50,"complete":true,"freshness":"observed","observedAt":"2026-10-23T16:00:00Z"}`))
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
	replacement := filepath.Join(dir, "operator.next")
	if err := os.WriteFile(replacement, []byte(rotated+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, operatorToken); err != nil {
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
	if w.Code != http.StatusOK || !strings.Contains(body, "Canonical subject") || !strings.Contains(body, "Closed") || !strings.Contains(body, "Accountable owner") || !strings.Contains(body, "dev-env; haynes-ops") || !strings.Contains(body, "https://work.haynesnetwork.com/work_packages/42") || strings.Contains(body, "SECRET-DESCRIPTION") {
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
		_, _ = w.Write([]byte(`{"sessions":[{"name":"older","uid":"uid-2","repo":"dev-env","phase":"Running","observedAt":"2026-10-23T14:00:00Z","freshness":"stale",` + unknownActivity + `}],"limit":50,"complete":true,"freshness":"stale","observedAt":"2026-10-23T16:00:00Z"}`))
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

func TestProjectCollectionPagesAndSessionObjects(t *testing.T) {
	f := setupAuth(t)
	f.login(t)
	now := f.now
	opCert, opCA := mintNamed(t, openProjectPortalHost)
	element := func(id int, raw string) string {
		return fmt.Sprintf(`{"id":%d,"subject":"Package %d","description":{"raw":%q,"html":%q},"customField3":"","_links":{"project":{"href":"/api/v3/projects/3"},"status":{"title":"Closed"},"assignee":{"title":"Assignee"},"responsible":{"title":"Responsible"}},"_embedded":{"project":{"id":3,"identifier":"haynes-engineering"}}}`, id, id, raw, raw)
	}
	page := func(total, offset, count int, next string) string {
		parts := make([]string, count)
		for i := range parts {
			parts[i] = element((offset-1)*50+i+1, "")
		}
		links := "{}"
		if next != "" {
			links = fmt.Sprintf(`{"nextByOffset":{"href":%q}}`, next)
		}
		return fmt.Sprintf(`{"total":%d,"count":%d,"pageSize":50,"offset":%d,"_embedded":{"elements":[%s]},"_links":%s}`, total, count, offset, strings.Join(parts, ","), links)
	}
	var queries []string
	opSrv := serveTLS(t, opCert, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.Host+r.URL.RequestURI())
		if strings.Contains(r.URL.RawQuery, "filters=%5B%7B") || r.URL.RawQuery == "pageSize=50&offset=1" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/hal+json")
		switch r.URL.RawQuery {
		case "offset=1&pageSize=50&filters=%5B%5D":
			_, _ = w.Write([]byte(page(82, 1, 50, "https://work.haynesnetwork.com/api/v3/projects/3/work_packages?offset=2&pageSize=50&filters=%5B%5D")))
		case "offset=2&pageSize=50&filters=%5B%5D":
			_, _ = w.Write([]byte(page(82, 2, 32, "")))
		default:
			http.NotFound(w, r)
		}
	}))
	dir := t.TempDir()
	opToken := filepath.Join(dir, "openproject")
	if err := os.WriteFile(opToken, []byte("op-token-canary\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	authorizer := currentFixture(f, func(*http.Request) (*http.Response, error) { return userInfoResponse(currentAllow), nil })
	tasks, err := newTaskSource(opToken, opCA, func() time.Time { return now }, routeDial(map[string]string{openProjectPortalHost: opSrv}))
	if err != nil {
		t.Fatal(err)
	}
	handler := NewAssembly(AssemblyConfig{Session: f.e, Authorizer: authorizer, Reader: portal.UnavailableReader{}, Sessions: nil, Tasks: tasks, FreshFor: time.Minute, Now: func() time.Time { return now }})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, f.request(http.MethodGet, TasksPath, ""))
	body := w.Body.String()
	if !strings.Contains(body, "Package 1") || !strings.Contains(body, "Package 82") || !strings.Contains(body, "Closed") || strings.Contains(body, "Tasks unavailable") || len(queries) != 2 || queries[1] != openProjectPortalHost+":8443/api/v3/projects/3/work_packages?offset=2&pageSize=50&filters=%5B%5D" {
		t.Fatalf("82-item collection: %s queries=%v", body, queries)
	}
	marker := strings.Repeat("SECRET-DESCRIPTION-MARKER", 40)
	pad := strings.Repeat("x", 4096)
	fat := make([]string, 50)
	for i := range fat {
		fat[i] = element(i+1, marker+pad)
	}
	fatBody := fmt.Sprintf(`{"total":50,"count":50,"pageSize":50,"offset":1,"_embedded":{"elements":[%s]},"_links":{}}`, strings.Join(fat, ","))
	if len(fatBody) < 399213 {
		t.Fatalf("fixture is smaller than the reviewed payload: %d", len(fatBody))
	}
	fatSrv := serveTLS(t, opCert, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/hal+json")
		_, _ = w.Write([]byte(fatBody))
	}))
	tasks, err = newTaskSource(opToken, opCA, func() time.Time { return now }, routeDial(map[string]string{openProjectPortalHost: fatSrv}))
	if err != nil {
		t.Fatal(err)
	}
	handler = NewAssembly(AssemblyConfig{Session: f.e, Authorizer: authorizer, Reader: portal.UnavailableReader{}, Sessions: nil, Tasks: tasks, FreshFor: time.Minute, Now: func() time.Time { return now }})
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, f.request(http.MethodGet, TasksPath, ""))
	if !strings.Contains(w.Body.String(), "Package 50") || strings.Contains(w.Body.String(), "SECRET-DESCRIPTION-MARKER") {
		t.Fatal("full HAL description was rejected or rendered")
	}
	bad := []string{
		`{}`,
		`{"total":82,"count":0,"pageSize":50,"offset":1,"_embedded":{"elements":[]},"_links":{}}`,
		`{"total":1,"count":1,"pageSize":50,"offset":1,"_embedded":{"elements":[{"id":1,"subject":"DUP","_links":{"project":{"href":"/api/v3/projects/3"}}},{"id":1,"subject":"DUP","_links":{"project":{"href":"/api/v3/projects/3"}}}]},"_links":{}}`,
		func() string {
			parts := make([]string, 50)
			for i := range parts {
				parts[i] = element(i+1, "")
			}
			return `{"total":100,"count":50,"pageSize":50,"offset":1,"_embedded":{"elements":[` + strings.Join(parts, ",") + `]},"_links":{"nextByOffset":{"href":"https://user:pass@openproject-web.frontend.svc.cluster.local:8443/api/v3/projects/3/work_packages?offset=2&pageSize=50&filters=%5B%5D"}}}`
		}(),
	}
	for _, payload := range bad {
		srv := serveTLS(t, opCert, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/hal+json")
			_, _ = w.Write([]byte(payload))
		}))
		tasks, err = newTaskSource(opToken, opCA, func() time.Time { return now }, routeDial(map[string]string{openProjectPortalHost: srv}))
		if err != nil {
			t.Fatal(err)
		}
		handler = NewAssembly(AssemblyConfig{Session: f.e, Authorizer: authorizer, Reader: portal.UnavailableReader{}, Sessions: nil, Tasks: tasks, FreshFor: time.Minute, Now: func() time.Time { return now }})
		w = httptest.NewRecorder()
		handler.ServeHTTP(w, f.request(http.MethodGet, TasksPath, ""))
		if !strings.Contains(w.Body.String(), "Tasks unavailable") || strings.Contains(w.Body.String(), "The completed task read returned no work packages") || strings.Contains(w.Body.String(), "DUP") {
			t.Fatalf("malformed collection looked complete: %s", w.Body.String())
		}
	}
}

func TestPortalProductDecodersRejectNullsAndKeepExplicitZeroValues(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	session := `{"sessions":[],"limit":50,"complete":false,"freshness":"observed","observedAt":"2026-10-25T11:59:00Z","nextCursor":"next"}`
	for name, body := range map[string]string{
		"complete": strings.Replace(session, `"complete":false`, `"complete":null`, 1),
		"sessions": strings.Replace(session, `"sessions":[]`, `"sessions":null`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeSessionPage([]byte(body), now); err == nil {
				t.Fatal("null session field was accepted")
			}
		})
	}
	page, err := decodeSessionPage([]byte(session), now)
	if err != nil || page.Complete || len(page.Sessions) != 0 || page.NextCursor != "next" {
		t.Fatalf("explicit false and empty array changed: page=%+v err=%v", page, err)
	}

	task := `{"total":0,"count":0,"pageSize":50,"offset":1,"_embedded":{"elements":[]},"_links":{}}`
	for name, body := range map[string]string{
		"total":    strings.Replace(task, `"total":0`, `"total":null`, 1),
		"count":    strings.Replace(task, `"count":0`, `"count":null`, 1),
		"pageSize": strings.Replace(task, `"pageSize":50`, `"pageSize":null`, 1),
		"offset":   strings.Replace(task, `"offset":1`, `"offset":null`, 1),
		"elements": strings.Replace(task, `"elements":[]`, `"elements":null`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, _, err := decodeTaskPage([]byte(body), 1, map[int]struct{}{}); err == nil {
				t.Fatal("null task field was accepted")
			}
		})
	}
	rows, total, complete, err := decodeTaskPage([]byte(task), 1, map[int]struct{}{})
	if err != nil || total != 0 || !complete || len(rows) != 0 {
		t.Fatalf("explicit zero values changed: rows=%v total=%d complete=%t err=%v", rows, total, complete, err)
	}
}

func TestSessionSourceReadKeepsTheWorstPage(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name   string
		pages  []string
		notice string
	}{
		{name: "unknown then stale", pages: []string{"unknown", "stale"}, notice: "Session freshness is unknown."},
		{name: "stale then unknown", pages: []string{"stale", "unknown"}, notice: "Session freshness is unknown."},
		{name: "observed", pages: []string{"observed", "observed"}, notice: "The completed session read returned no dev-env sessions."},
	} {
		t.Run(test.name, func(t *testing.T) {
			cert, ca := mintNamed(t, operatorPortalHost)
			requests := 0
			server := serveTLS(t, cert, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				freshness := test.pages[requests-1]
				complete := requests == len(test.pages)
				next := ""
				if !complete {
					next = `,"nextCursor":"next"`
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"sessions":[],"limit":50,"complete":` + strconv.FormatBool(complete) + `,"freshness":"` + freshness + `","observedAt":"2026-10-25T11:59:00Z"` + next + `}`))
			}))
			tokenPath := filepath.Join(t.TempDir(), "operator")
			if err := os.WriteFile(tokenPath, []byte(fixtureOperatorToken("dev-env-operator", now.Add(time.Hour))+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			source, err := newSessionSource(tokenPath, ca, func() time.Time { return now }, routeDial(map[string]string{operatorPortalHost: server}))
			if err != nil {
				t.Fatal(err)
			}
			page, err := source.Read(context.Background())
			if err != nil || requests != len(test.pages) || page.Notice != test.notice {
				t.Fatalf("page=%+v requests=%d err=%v", page, requests, err)
			}
		})
	}
}
