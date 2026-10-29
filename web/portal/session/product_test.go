package session

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
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
	if w.Code != http.StatusOK || !strings.Contains(body, "Canonical subject") || !strings.Contains(body, "Closed") || !strings.Contains(body, "Accountable owner") || !strings.Contains(body, "dev-env; haynes-ops") || !strings.Contains(body, "https://work.haynesnetwork.com/work_packages/42") || !strings.Contains(body, `<p class="note">SECRET-DESCRIPTION</p>`) {
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
	// Stopped sessions never heartbeat: stale freshness is a quiet status
	// line, not a banner.
	if !strings.Contains(w.Body.String(), ">older<") || !strings.Contains(w.Body.String(), `<span role="status">Session freshness is stale.</span>`) || strings.Contains(w.Body.String(), `class="banner"`) {
		t.Fatalf("stale session was hidden or bannered: %s", w.Body.String())
	}
	if err := os.WriteFile(operatorToken, []byte(fixtureOperatorToken("dev-env-operator", now.Add(-time.Minute))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, f.request(http.MethodGet, SessionsPath, ""))
	if !strings.Contains(w.Body.String(), "Sessions unavailable") || strings.Contains(w.Body.String(), ">older<") {
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
	if !strings.Contains(w.Body.String(), "Sessions unavailable") || strings.Contains(w.Body.String(), ">older<") {
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
			// OpenProject 17's own next link: another path and extra keys. The
			// reader builds page 2's URL itself and ignores this.
			_, _ = w.Write([]byte(page(82, 1, 50, "/api/v3/workspaces/3/work_packages?filters=%5B%5D&includeSubprojects=false&offset=2&pageSize=50&showHierarchies=true&showSums=false&sortBy=%5B%5B%22id%22%2C%22asc%22%5D%5D")))
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
	// Only a bounded excerpt is rendered: the 1,000-character marker never
	// appears whole, and none of the padding after it does.
	if !strings.Contains(w.Body.String(), "Package 50") || !strings.Contains(w.Body.String(), "SECRET-DESCRIPTION-MARKER") || strings.Contains(w.Body.String(), marker) || strings.Contains(w.Body.String(), "xxxx") {
		t.Fatal("full HAL description was rejected or rendered whole")
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
			if _, _, _, err := decodeTaskPage([]byte(body), 1, map[int]struct{}{}, time.Time{}); err == nil {
				t.Fatal("null task field was accepted")
			}
		})
	}
	rows, total, complete, err := decodeTaskPage([]byte(task), 1, map[int]struct{}{}, time.Time{})
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
		{name: "observed", pages: []string{"observed", "observed"}, notice: "The completed session read returned no sessions."},
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
			page, err := source.Read(context.Background(), portal.ProductFilter{})
			if err != nil || requests != len(test.pages) || page.Notice != test.notice {
				t.Fatalf("page=%+v requests=%d err=%v", page, requests, err)
			}
		})
	}
}

// A rendered product page allows exactly its embedded stylesheet; a refusal
// keeps the strict policy with no style source.
func TestProductPagesSendTheirStylesheetPolicyOnly(t *testing.T) {
	f := setupAuth(t)
	f.login(t)
	authorizer := currentFixture(f, func(*http.Request) (*http.Response, error) { return userInfoResponse(currentAllow), nil })
	handler := NewAssembly(AssemblyConfig{Session: f.e, Authorizer: authorizer, Reader: portal.UnavailableReader{}, FreshFor: time.Minute, Now: func() time.Time { return f.now }})
	for _, path := range []string{SessionsPath, TasksPath} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, f.request(http.MethodGet, path, ""))
		if w.Code != http.StatusOK || w.Header().Get("Content-Security-Policy") != portal.ProductPolicy {
			t.Fatalf("%s: %d %q", path, w.Code, w.Header().Get("Content-Security-Policy"))
		}
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, f.request(http.MethodPost, SessionsPath, ""))
	if w.Code == http.StatusOK || strings.Contains(w.Header().Get("Content-Security-Policy"), "style-src") {
		t.Fatalf("refusal carried the product stylesheet policy: %d %q", w.Code, w.Header().Get("Content-Security-Policy"))
	}
}

// The bare origin lands on Sessions, and a missing session starts sign-in. A
// signed-in user whom current authority refuses gets the refusal, not a loop.
func TestEntryRedirects(t *testing.T) {
	f := setupAuth(t)
	allow := currentFixture(f, func(*http.Request) (*http.Response, error) { return userInfoResponse(currentAllow), nil })
	handler := NewAssembly(AssemblyConfig{Session: f.e, Authorizer: allow, Reader: portal.UnavailableReader{}, FreshFor: time.Minute, Now: func() time.Time { return f.now }})
	for _, path := range []string{"/", SessionsPath, TasksPath} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, f.request(http.MethodGet, path, ""))
		want := "/auth/login"
		if path == "/" {
			want = SessionsPath
		}
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != want || w.Body.Len() != 0 {
			t.Fatalf("%s without a session: %d %q %q", path, w.Code, w.Header().Get("Location"), w.Body.String())
		}
	}
	for _, r := range []*http.Request{f.request(http.MethodPost, "/", ""), f.request(http.MethodGet, "/?x=1", "")} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code == http.StatusSeeOther || w.Header().Get("Location") != "" {
			t.Fatalf("%s %s redirected", r.Method, r.URL)
		}
	}
	f.login(t)
	deny := currentFixture(f, func(*http.Request) (*http.Response, error) {
		return userInfoResponse(`{"sub":"synthetic-subject","portal_is_active":true,"portal_is_admin":false}`), nil
	})
	handler = NewAssembly(AssemblyConfig{Session: f.e, Authorizer: deny, Reader: portal.UnavailableReader{}, FreshFor: time.Minute, Now: func() time.Time { return f.now }})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, f.request(http.MethodGet, SessionsPath, ""))
	if w.Code != http.StatusForbidden || w.Header().Get("Location") != "" {
		t.Fatalf("refused user was redirected: %d %q", w.Code, w.Header().Get("Location"))
	}
}

// A work package shaped like OpenProject 17's renders its type, Execution
// owner worktree, parent and a plain-text summary; empty people are omitted.
func TestTaskRowShowsTypeOwnerParentAndSummary(t *testing.T) {
	now := time.Date(2026, 10, 27, 1, 0, 0, 0, time.UTC)
	var element halWorkPackage
	raw := `{"id":178,"subject":"Portal","createdAt":"2026-10-20T00:00:00Z","updatedAt":"2026-10-26T23:00:00Z","customField1":null,
	"customField2":"agent | worktree dev-env-1026-134753 | branch agent/dev-env-1026-134753","customField3":"dev-env; haynes-ops",
	"description":{"format":"markdown","raw":"## Scope\n- Ship the [portal](https://example.invalid/x) ![diagram](https://example.invalid/d.png) **today** as dev_env_v2_portal_runtime\n\nThen stop."},
	"_links":{"project":{"href":"/api/v3/projects/3"},"status":{"title":"In progress"},"type":{"title":"Story"},"parent":{"href":"/api/v3/work_packages/85","title":"Distributed dev-env"},"assignee":{"href":null},"responsible":{"href":null}}}`
	if err := json.Unmarshal([]byte(raw), &element); err != nil {
		t.Fatal(err)
	}
	row, err := taskRow(element, now)
	if err != nil {
		t.Fatal(err)
	}
	if row.Kicker != "#178 · Story" || row.Meta != "dev-env-1026-134753" || row.Description != "in Distributed dev-env" || row.Status.Tone != "busy" {
		t.Fatalf("row %+v", row)
	}
	if row.Sections[0].Title != "Summary" || row.Sections[0].Note != "Scope Ship the portal diagram today as dev_env_v2_portal_runtime Then stop." {
		t.Fatalf("summary %+v", row.Sections[0])
	}
	if fields := rowFields(row); fields["Execution owner"] == "" || fields["Parent"] != "Distributed dev-env" || fields["Type"] != "Story" {
		t.Fatalf("fields %v", fields)
	} else {
		for _, absent := range []string{"Accountable owner", "Assignee", "Responsible"} {
			if _, ok := fields[absent]; ok {
				t.Errorf("empty %s is shown", absent)
			}
		}
	}
	if long := taskSummary(strings.Repeat("é", taskSummaryRunes+10)); len([]rune(long)) != taskSummaryRunes+1 || !strings.HasSuffix(long, "…") {
		t.Fatalf("summary is not bounded by runes: %d", len([]rune(long)))
	}
	for raw, want := range map[string]string{
		"gpt-6.1-sol | dev-env-1019-205125 | haynes-ops-unraid-plan-20261022": "gpt-6.1-sol",
		"Sol coordinator / dev-env-1019-205125 / thread01a1 / story190":       "Sol coordinator",
		"Cursor Grok | session 1772 | worktree dev-env-grok-101 | PR4264":     "dev-env-grok-101",
		strings.Repeat("long ", 20):                                           strings.TrimSpace(strings.Repeat("long ", 8)) + "…",
		" | / | ":                                                             "",
		"agent | worktree " + strings.Repeat("w", 60) + " | branch b":         strings.Repeat("w", 40) + "…",
	} {
		if got := taskOwner(raw); got != want {
			t.Errorf("taskOwner(%q) = %q, want %q", raw, got, want)
		}
	}
	for status, group := range map[string]int{"Draft": 2, "Ungroomed": 2, "Paused": 0, "Needs approval": 0, "In review": 1, "Accepted": 3} {
		if got := taskGroup(status); got != group {
			t.Errorf("taskGroup(%q) = %d, want %d", status, got, group)
		}
	}
}

func TestSessionReadFailureReasons(t *testing.T) {
	now := time.Date(2026, 10, 29, 14, 0, 0, 0, time.UTC)
	token := filepath.Join(t.TempDir(), "operator")
	if err := os.WriteFile(token, []byte(fixtureOperatorToken("dev-env-operator", now.Add(time.Hour))), 0600); err != nil {
		t.Fatal(err)
	}
	good := activityPage()
	badRow := activityRow("canary", unknownActivity)
	badRow = strings.Replace(badRow, `"name":"canary"`, `"name":"canary","prompt":"SECRET-RESPONSE"`, 1)
	for _, tc := range []struct {
		name, reason, body, media string
		status                    int
		transportErr              error
		missing, disconnected     bool
	}{
		{name: "not connected", reason: "not_connected", disconnected: true},
		{name: "credential", reason: "credential_unavailable", missing: true},
		{name: "unreachable", reason: "operator_unreachable", transportErr: fmt.Errorf("SECRET-TRANSPORT")},
		{name: "timeout", reason: "operator_unreachable", transportErr: context.DeadlineExceeded},
		{name: "cancelled", reason: "cancelled", transportErr: context.Canceled},
		{name: "refused", reason: "operator_refused", status: 403, body: "SECRET-ERROR"},
		{name: "redirect", reason: "operator_refused", status: 302, body: "SECRET-REDIRECT"},
		{name: "media", reason: "response_contract", status: 200, media: "text/plain", body: good},
		{name: "oversize", reason: "response_contract", status: 200, body: strings.Repeat("x", 256<<10+1)},
		{name: "envelope", reason: "response_contract", status: 200, body: `{"secret":"SECRET-ENVELOPE"}`},
		{name: "row", reason: "response_contract", status: 200, body: activityPage(badRow)},
		{name: "clock ahead", reason: "clock_ahead", status: 200, body: strings.ReplaceAll(good, "2026-10-25T11:59:00Z", now.Add(3*time.Second).Format(time.RFC3339Nano))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			s := &SessionSource{path: token, now: func() time.Time { return now }, client: &http.Client{Transport: tripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if deadline, ok := r.Context().Deadline(); !ok || time.Until(deadline) > sessionReadTimeout {
					t.Error("whole-read deadline missing")
				}
				if tc.transportErr != nil {
					return nil, tc.transportErr
				}
				media := tc.media
				if media == "" {
					media = "application/json"
				}
				return &http.Response{StatusCode: tc.status, Header: http.Header{"Content-Type": {media}}, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})}}
			if tc.missing {
				s.path += "-missing"
			}
			if tc.disconnected {
				s = nil
			}
			var logs bytes.Buffer
			previousLogger := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previousLogger) })
			page, err := s.Read(t.Context(), portal.ProductFilter{Attention: true})
			if err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("reason = %v, want %s", err, tc.reason)
			}
			var failure *sessionReadError
			if !errors.As(err, &failure) || !errors.Is(err, portal.ErrUnavailable) || failure.ReasonCode != tc.reason {
				t.Fatalf("untyped failure: %v", err)
			}
			wantPage, wantRow := 1, -1
			if tc.missing || tc.disconnected {
				wantPage = 0
			}
			if tc.name == "row" {
				wantRow = 0
			}
			if failure.Page != wantPage || failure.RowIndex != wantRow || failure.TimedOut != (tc.name == "timeout") {
				t.Fatalf("failure metadata: %+v", failure)
			}
			var line map[string]any
			if strings.Count(strings.TrimSpace(logs.String()), "\n") != 0 || json.Unmarshal(logs.Bytes(), &line) != nil {
				t.Fatalf("expected one structured failure log: %s", logs.String())
			}
			if line["reason_code"] != tc.reason || line["page"] != float64(wantPage) || line["row_index"] != float64(wantRow) {
				t.Fatalf("failure log metadata: %v", line)
			}
			level := "WARN"
			if tc.reason == "not_connected" || tc.reason == "cancelled" {
				level = "INFO"
			}
			if line["level"] != level {
				t.Errorf("log level = %v, want %s", line["level"], level)
			}
			for _, key := range []string{"elapsed_ms", "credential_ms", "fetch_ms", "decode_ms", "projection_ms", "history_ms"} {
				if n, ok := line[key].(float64); !ok || n < 0 {
					t.Errorf("missing or invalid log duration %s", key)
				}
			}
			page.ObservedAt = now.Format(time.RFC3339)
			body, renderErr := portal.RenderProduct(page)
			if renderErr != nil {
				t.Fatal(renderErr)
			}
			text := string(body)
			for _, want := range []string{`href="` + SessionsPath + `?show=attention">Retry</a>`, "Observed:", "Last successful read in this process:", "Unknown"} {
				if !strings.Contains(text, want) {
					t.Errorf("missing failure UI %s", want)
				}
			}
			words := map[string]string{"not_connected": "not connected", "credential_unavailable": "credential is unavailable", "operator_unreachable": "could not be reached", "operator_refused": "HTTP " + strconv.Itoa(tc.status), "response_contract": "refused by the Sessions contract", "clock_ahead": "ahead of the portal clock", "cancelled": "was cancelled"}
			want := words[tc.reason]
			if tc.name == "timeout" {
				want = "timed out"
			}
			if !strings.Contains(text, want) {
				t.Errorf("reason not rendered: %s", want)
			}
			if strings.Contains(text, "SECRET-") || strings.Contains(logs.String(), "SECRET-") || strings.Contains(text, fixtureOperatorToken("dev-env-operator", now.Add(time.Hour))) || strings.Contains(logs.String(), fixtureOperatorToken("dev-env-operator", now.Add(time.Hour))) {
				t.Fatal("source value or credential escaped into diagnostics")
			}
			if len(page.Groups) != 0 || page.Usage != nil || page.Complete {
				t.Fatal("failed read returned session data")
			}
			if (tc.missing || tc.disconnected) && calls != 0 {
				t.Fatal("unavailable credential/source made a request")
			}
		})
	}
}

func TestSessionObservationClockTolerance(t *testing.T) {
	now := time.Date(2026, 10, 29, 14, 0, 0, 0, time.UTC)
	for _, delta := range []time.Duration{time.Millisecond, 2 * time.Second, 2*time.Second + time.Nanosecond} {
		raw, _ := json.Marshal(now.Add(delta).Format(time.RFC3339Nano))
		got, err := observationTime(raw, now)
		if delta <= 2*time.Second {
			if err != nil || !got.Equal(now) {
				t.Errorf("skew %s not clamped: %v %v", delta, got, err)
			}
		} else if err == nil || !strings.Contains(err.Error(), "clock_ahead") {
			t.Errorf("large skew accepted: %v", err)
		}
	}
}

func sessionReadFixture(t *testing.T, now func() time.Time, transport tripFunc) *SessionSource {
	t.Helper()
	path := filepath.Join(t.TempDir(), "operator")
	if err := os.WriteFile(path, []byte(fixtureOperatorToken("dev-env-operator", now().Add(time.Hour))), 0600); err != nil {
		t.Fatal(err)
	}
	return &SessionSource{path: path, now: now, client: &http.Client{Transport: transport}}
}

func TestSessionReadLatePageFailureHasPositionAndLastSuccess(t *testing.T) {
	now := time.Date(2026, 10, 29, 14, 0, 0, 0, time.UTC)
	reading := 0
	var logs bytes.Buffer
	good := activityRow("SECRET-CACHED-SESSION", unknownActivity)
	bad := strings.Replace(activityRow("bad", unknownActivity), `"phase":"Running"`, `"phase":"SECRET-BAD-PHASE"`, 1)
	s := sessionReadFixture(t, func() time.Time { return now }, func(r *http.Request) (*http.Response, error) {
		body := activityPage(good)
		if reading > 0 {
			if r.URL.Query().Get("cursor") == "" {
				body = strings.Replace(body, `"complete":true`, `"complete":false,"nextCursor":"next"`, 1)
			} else {
				body = activityPage(activityRow("second-page", unknownActivity), bad)
			}
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	s.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	if _, err := s.Read(t.Context(), portal.ProductFilter{}); err != nil {
		t.Fatal(err)
	}
	if logs.Len() != 0 {
		t.Fatal("successful read logged a failure")
	}
	reading++
	now = now.Add(time.Minute)
	page, err := s.Read(t.Context(), portal.ProductFilter{Project: "dev-env"})
	var failure *sessionReadError
	if !errors.As(err, &failure) || failure.Page != 2 || failure.RowIndex != 1 {
		t.Fatalf("position not preserved: %v", err)
	}
	if page.ReadFailure.LastSuccessAt != "2026-10-29T14:00:00Z" || page.ReadFailure.ObservedAt != "2026-10-29T14:01:00Z" {
		t.Fatalf("read times missing: %+v", page.ReadFailure)
	}
	body, err := portal.RenderProduct(page)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Page 2, row index 1", `datetime="2026-10-29T14:00:00Z"`, `datetime="2026-10-29T14:01:00Z"`, `href="` + SessionsPath + `?project=dev-env">Retry</a>`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("failure lacks %s", want)
		}
	}
	if strings.Contains(string(body), "SECRET-") || strings.Contains(logs.String(), "SECRET-") || len(page.Groups) > 0 || page.Usage != nil {
		t.Fatal("failed read exposed cached or partial session data")
	}
	t.Logf("late contract failure: %s", strings.TrimSpace(logs.String()))
}

func TestSessionReadUsesOneTotalDeadlineAcrossPages(t *testing.T) {
	now := time.Date(2026, 10, 29, 14, 0, 0, 0, time.UTC)
	var firstDeadline time.Time
	calls := 0
	s := sessionReadFixture(t, func() time.Time { return now }, func(r *http.Request) (*http.Response, error) {
		calls++
		deadline, ok := r.Context().Deadline()
		if !ok || time.Until(deadline) > sessionReadTimeout {
			t.Fatal("deadline missing or too long")
		}
		if calls == 1 {
			firstDeadline = deadline
		} else if !deadline.Equal(firstDeadline) {
			t.Error("deadline renewed for a later page")
		}
		if calls == 3 {
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		body := strings.Replace(activityPage(), `"complete":true`, `"complete":false,"nextCursor":"next"`, 1)
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	var logs bytes.Buffer
	s.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	page, err := s.Read(ctx, portal.ProductFilter{})
	var failure *sessionReadError
	if !errors.As(err, &failure) || failure.ReasonCode != "operator_unreachable" || !failure.TimedOut || failure.Page != 3 || len(page.Groups) != 0 {
		t.Fatalf("total timeout not classified: %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("read failed to honor the total deadline")
	}
	t.Logf("total timeout: %s", strings.TrimSpace(logs.String()))
}

func TestSessionReadClockAheadRetainsRowIndex(t *testing.T) {
	now := time.Date(2026, 10, 29, 14, 0, 0, 0, time.UTC)
	for _, location := range []string{"row", "last activity"} {
		row := activityRow("clock-test", unknownActivity)
		if location == "row" {
			row = strings.Replace(row, "2026-10-25T11:59:00Z", now.Add(3*time.Second).Format(time.RFC3339), 1)
		} else {
			row = strings.Replace(row, `"agentState":"idle"`, `"agentState":"idle","lastActivity":"`+now.Add(3*time.Second).Format(time.RFC3339)+`"`, 1)
		}
		_, err := decodeSessionPage([]byte(activityPage(row)), now)
		var failure *sessionReadError
		if !errors.As(err, &failure) || failure.ReasonCode != "clock_ahead" || failure.RowIndex != 0 {
			t.Errorf("%s: %v", location, err)
		}
	}
}

func TestServeSessionFailureDetailsAndRetry(t *testing.T) {
	f := setupAuth(t)
	f.login(t)
	s := sessionReadFixture(t, func() time.Time { return f.now }, func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 403, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader("SECRET-REFUSAL"))}, nil
	})
	var logs bytes.Buffer
	s.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	authorizer := currentFixture(f, func(*http.Request) (*http.Response, error) { return userInfoResponse(currentAllow), nil })
	h := NewAssembly(AssemblyConfig{Session: f.e, Authorizer: authorizer, Reader: portal.UnavailableReader{}, Sessions: s, FreshFor: time.Minute, Now: func() time.Time { return f.now }})
	for _, query := range []string{"?show=attention", "?project=haynes-ops", "?session=uid-a", "?view=usage"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, f.request(http.MethodGet, SessionsPath+query, ""))
		body := w.Body.String()
		for _, want := range []string{"Sessions unavailable", "The operator refused the read (HTTP 403).", "Page 1.", `href="` + SessionsPath + query + `">Retry</a>`, `datetime="` + f.now.Format(time.RFC3339) + `"`} {
			if w.Code != 200 || !strings.Contains(body, want) {
				t.Errorf("%s lost %s (HTTP %d)", query, want, w.Code)
			}
		}
		if query == "?view=usage" && (strings.Contains(body, `class="rail"`) || strings.Contains(body, `class="list"`) || strings.Count(body, `?view=usage" aria-current="page"`) != 2) {
			t.Error("failed Usage read lost view state")
		}
		if strings.Contains(body, "SECRET-") {
			t.Fatal("upstream error leaked")
		}
	}
}

type failedSessionBody struct{ err error }

func (b failedSessionBody) Read([]byte) (int, error) { return 0, b.err }
func (failedSessionBody) Close() error               { return nil }

func TestSessionReadBodyFailuresAndParentCancellation(t *testing.T) {
	now := time.Date(2026, 10, 29, 14, 0, 0, 0, time.UTC)
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded, io.ErrUnexpectedEOF} {
		s := sessionReadFixture(t, func() time.Time { return now }, func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: failedSessionBody{cause}}, nil
		})
		var logs bytes.Buffer
		s.logger = slog.New(slog.NewJSONHandler(&logs, nil))
		_, err := s.Read(t.Context(), portal.ProductFilter{})
		var failure *sessionReadError
		want := "operator_unreachable"
		if errors.Is(cause, context.Canceled) {
			want = "cancelled"
		}
		if !errors.As(err, &failure) || failure.ReasonCode != want || failure.TimedOut != errors.Is(cause, context.DeadlineExceeded) {
			t.Errorf("body failure %v: %v", cause, err)
		}
	}
	calls := 0
	s := sessionReadFixture(t, func() time.Time { return now }, func(*http.Request) (*http.Response, error) { calls++; return nil, fmt.Errorf("unexpected call") })
	var logs bytes.Buffer
	s.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := s.Read(ctx, portal.ProductFilter{})
	var failure *sessionReadError
	if !errors.As(err, &failure) || failure.ReasonCode != "cancelled" || failure.Page != 0 || calls != 0 {
		t.Fatalf("cancelled parent was read: %v, calls %d", err, calls)
	}
}

type deadlineConversations struct{}

func (deadlineConversations) ReadConversation(ctx context.Context, _, _ string) (Conversation, error) {
	<-ctx.Done()
	return Conversation{}, ctx.Err()
}

func TestSessionReadHistoryDeadlineDoesNotBlameAnInventoryPage(t *testing.T) {
	now := time.Date(2026, 10, 29, 14, 0, 0, 0, time.UTC)
	s := sessionReadFixture(t, func() time.Time { return now }, func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(activityPage(activityRow("selected", unknownActivity))))}, nil
	})
	s.UseConversations(deadlineConversations{})
	var logs bytes.Buffer
	s.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	page, err := s.Read(ctx, portal.ProductFilter{Session: "uid-selected"})
	var failure *sessionReadError
	if !errors.As(err, &failure) || failure.Page != 0 || !failure.TimedOut || failure.ReasonCode != "operator_unreachable" || len(page.Groups) != 0 {
		t.Fatalf("history timeout was attributed to a successful inventory page: %+v", failure)
	}
	t.Logf("history timeout: %s", strings.TrimSpace(logs.String()))
}

func TestSessionTransportFailurePrefersContextCause(t *testing.T) {
	expired, expire := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer expire()
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		name   string
		ctx    context.Context
		err    error
		reason string
		timed  bool
	}{
		{"our deadline, transport cancelled", expired, context.Canceled, "operator_unreachable", true},
		{"our cancellation, transport timed out", cancelled, context.DeadlineExceeded, "cancelled", false},
		{"transport cancellation", t.Context(), context.Canceled, "cancelled", false},
		{"transport deadline", t.Context(), context.DeadlineExceeded, "operator_unreachable", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			failure := sessionTransportFailure(tc.ctx, tc.err)
			if failure.ReasonCode != tc.reason || failure.TimedOut != tc.timed {
				t.Fatalf("failure = %+v, want %s timed_out=%v", failure, tc.reason, tc.timed)
			}
		})
	}
}

func TestServeSessionsReadDeadlineStartsBeforeInitialAuthorization(t *testing.T) {
	f := setupAuth(t)
	f.login(t)
	synctest.Test(t, func(t *testing.T) {
		started := time.Now()
		calls, reads := 0, 0
		authorizer := authorizeFunc(func(ctx context.Context, _ portal.Principal, _ portal.Resource) error {
			calls++
			if ctx.Err() != nil {
				t.Fatal("read deadline cancelled the authorization context")
			}
			if calls == 1 {
				time.Sleep(4 * time.Second)
			} else {
				time.Sleep(5 * time.Second)
			}
			return nil
		})
		s := sessionReadFixture(t, func() time.Time { return f.now }, func(r *http.Request) (*http.Response, error) {
			reads++
			deadline, ok := r.Context().Deadline()
			if !ok || !deadline.Equal(started.Add(12*time.Second)) {
				t.Errorf("deadline = %s, want request start + 12s", deadline)
			}
			<-r.Context().Done()
			return nil, context.Canceled
		})
		var logs bytes.Buffer
		s.logger = slog.New(slog.NewJSONHandler(&logs, nil))
		h := NewAssembly(AssemblyConfig{Session: f.e, Authorizer: authorizer, Reader: portal.UnavailableReader{}, Sessions: s, FreshFor: time.Minute, Now: func() time.Time { return f.now }})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, f.request(http.MethodGet, SessionsPath, ""))
		if calls != 2 || reads != 1 || w.Code != 200 || !strings.Contains(w.Body.String(), "The operator read timed out.") || time.Since(started) != 17*time.Second {
			t.Fatalf("auth=%d reads=%d status=%d elapsed=%s", calls, reads, w.Code, time.Since(started))
		}
	})
}

func TestSessionReadDeadlineBetweenPagesReportsNoRequestedPage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		now := time.Date(2026, 10, 29, 14, 0, 0, 0, time.UTC)
		clocks, reads := 0, 0
		s := sessionReadFixture(t, func() time.Time { return now }, func(*http.Request) (*http.Response, error) {
			reads++
			body := strings.Replace(activityPage(), `"complete":true`, `"complete":false,"nextCursor":"next"`, 1)
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
		})
		s.now = func() time.Time {
			clocks++
			if clocks == 2 {
				// Expire the read after transport completes, before the next page.
				time.Sleep(13 * time.Second)
			}
			return now
		}
		var logs bytes.Buffer
		s.logger = slog.New(slog.NewJSONHandler(&logs, nil))
		page, err := s.Read(t.Context(), portal.ProductFilter{})
		var failure *sessionReadError
		if !errors.As(err, &failure) || !failure.TimedOut || failure.Page != 0 || reads != 1 || len(page.Groups) != 0 {
			t.Fatalf("between-page deadline: failure=%+v reads=%d", failure, reads)
		}
		var line map[string]any
		if json.Unmarshal(logs.Bytes(), &line) != nil || line["page"] != float64(0) {
			t.Fatalf("log blamed an unrequested page: %s", logs.String())
		}
	})
}

func TestSessionReadPageCapIsSuccessfulAndIncomplete(t *testing.T) {
	now := time.Date(2026, 10, 29, 14, 0, 0, 0, time.UTC)
	reads := 0
	s := sessionReadFixture(t, func() time.Time { return now }, func(*http.Request) (*http.Response, error) {
		reads++
		body := strings.Replace(activityPage(activityRow("cap-"+strconv.Itoa(reads), unknownActivity)), `"complete":true`, `"complete":false,"nextCursor":"next"`, 1)
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	var logs bytes.Buffer
	s.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	page, err := s.Read(t.Context(), portal.ProductFilter{})
	if err != nil || !page.Connected || page.Complete || page.ReadFailure != nil || reads != 10 || !s.lastSuccess.Equal(now) || logs.Len() != 0 {
		t.Fatalf("cap became a failure: err=%v complete=%v reads=%d last=%s logs=%s", err, page.Complete, reads, s.lastSuccess, logs.String())
	}
}

func TestServeSessionReadFailureKeepsSeparateLaunchRoute(t *testing.T) {
	f, launch, transport, _ := launchFixture(t)
	s := sessionReadFixture(t, func() time.Time { return f.now }, func(*http.Request) (*http.Response, error) {
		return nil, io.ErrUnexpectedEOF
	})
	h := NewAssembly(AssemblyConfig{Session: f.e, Authorizer: launch.authorizer, Reader: portal.UnavailableReader{}, Sessions: s, Launch: launch, FreshFor: time.Minute, Now: func() time.Time { return f.now }})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, f.request(http.MethodGet, SessionsPath, ""))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "could not be reached") || !strings.Contains(w.Body.String(), `<form class="launch"`) || w.Header().Get("Content-Security-Policy") != portal.ProductSendPolicy || transport.count() != 0 {
		t.Fatalf("read failure lost the separate launch form: HTTP %d", w.Code)
	}
}
