package session

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/web/portal"
)

var claimsTestNow = time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC)

func claimsTestPage() apiv1.PortalTaskClaims {
	beat := claimsTestNow.Add(-time.Minute)
	return apiv1.PortalTaskClaims{Version: apiv1.PortalTaskClaimsVersion, Project: "dev-env", State: apiv1.PortalTaskClaimsStateAvailable, Reason: apiv1.PortalTaskClaimsReasonComplete,
		Items: []apiv1.PortalTaskClaimItem{{Name: "task-claim." + strings.Repeat("a", 32) + "." + strings.Repeat("b", 32), UID: "00000000-0000-4000-8000-000000000001",
			Repository: "dev-env", TaskKey: "issue:7", GitHub: "thaynes43/dev-env", CreatedAt: claimsTestNow.Add(-time.Hour),
			Owner: apiv1.PortalTaskClaimOwner{Name: "synthetic-owner", UID: "11111111-0000-4000-8000-000000000001", State: "Running", Freshness: "observed", HeartbeatAt: &beat}}},
		Limit: 100, Complete: true, ObservedAt: claimsTestNow}
}

func claimsJSON(t *testing.T, page apiv1.PortalTaskClaims) string {
	t.Helper()
	raw, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// claimsOperator is an httptest TLS operator whose certificate the source pins.
type claimsOperator struct {
	mu       sync.Mutex
	status   int
	media    string
	body     string
	tokens   []string
	queries  []string
	requests int
	block    bool
}

func (o *claimsOperator) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	o.mu.Lock()
	o.requests++
	o.tokens = append(o.tokens, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	o.queries = append(o.queries, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery+" "+r.Header.Get("Accept"))
	status, media, body, block := o.status, o.media, o.body, o.block
	o.mu.Unlock()
	if block {
		<-r.Context().Done()
		return
	}
	w.Header().Set("Content-Type", media)
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func claimsTLSSource(t *testing.T, o *claimsOperator) (*ClaimsSource, string) {
	t.Helper()
	cert, caPEM := mintNamed(t, operatorPortalHost)
	addr := serveTLS(t, cert, o)
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte(fixtureOperatorToken(apiv1.TokenAudience, claimsTestNow.Add(time.Hour))), 0600); err != nil {
		t.Fatal(err)
	}
	base, err := newSessionSource(token, caPEM, func() time.Time { return claimsTestNow }, routeDial(map[string]string{operatorPortalHost: addr}))
	if err != nil {
		t.Fatal(err)
	}
	return &ClaimsSource{path: base.path, client: base.client, now: base.now, project: "dev-env"}, token
}

func TestClaimsSourceReadsOnePinnedPageWithAFreshToken(t *testing.T) {
	o := &claimsOperator{status: 200, media: "application/json; charset=utf-8", body: claimsJSON(t, claimsTestPage())}
	source, token := claimsTLSSource(t, o)
	page := source.read(context.Background(), "")
	if page.State != portal.ClaimsAvailable || len(page.Read.Items) != 1 || page.Cursor {
		t.Fatalf("state %s", page.State)
	}
	second := fixtureOperatorToken(apiv1.TokenAudience, claimsTestNow.Add(2*time.Hour))
	if err := os.WriteFile(token, []byte(second), 0600); err != nil {
		t.Fatal(err)
	}
	if page := source.read(context.Background(), "Y3Vyc29y"); page.State != portal.ClaimsAvailable || !page.Cursor {
		t.Fatalf("cursor read %s", page.State)
	}
	if o.requests != 2 || o.tokens[1] != second || o.tokens[0] == second {
		t.Fatal("the token is not read on each request")
	}
	if o.queries[0] != "GET "+apiv1.PortalTaskClaimsPath+"?limit=100&project=dev-env application/json" || o.queries[1] != "GET "+apiv1.PortalTaskClaimsPath+"?cursor=Y3Vyc29y&limit=100&project=dev-env application/json" {
		t.Fatalf("operator request %v", o.queries)
	}
	if err := os.WriteFile(token, []byte(fixtureOperatorToken(apiv1.TokenAudience, claimsTestNow.Add(-time.Minute))), 0600); err != nil {
		t.Fatal(err)
	}
	if page := source.read(context.Background(), ""); page.State != portal.ClaimsUnavailable || o.requests != 2 {
		t.Fatal("an expired credential must fail closed before any request")
	}
}

func TestClaimsSourceMapsEveryOutcome(t *testing.T) {
	empty := claimsTestPage()
	empty.State, empty.Items = apiv1.PortalTaskClaimsStateEmpty, []apiv1.PortalTaskClaimItem{}
	unknown := func(reason string) string {
		p := claimsTestPage()
		p.State, p.Reason, p.Complete, p.Items = apiv1.PortalTaskClaimsStateUnknown, reason, false, []apiv1.PortalTaskClaimItem{}
		return claimsJSON(t, p)
	}
	other := claimsTestPage()
	other.Project = "other"
	limit50 := claimsTestPage()
	limit50.Limit = 50
	big := claimsTestPage()
	big.Items[0].Repository = strings.Repeat("x", 64)
	oversize := `{"pad":"` + strings.Repeat("x", apiv1.PortalTaskClaimsMaxBytes) + `"}`
	valid := claimsJSON(t, claimsTestPage())
	for _, test := range []struct {
		name, body string
		status     int
		media      string
		cursor     string
		want       portal.ClaimsState
	}{
		{"available", valid, 200, "", "", portal.ClaimsAvailable},
		{"empty", claimsJSON(t, empty), 200, "", "", portal.ClaimsEmpty},
		{"empty after cursor", claimsJSON(t, empty), 200, "", "abc", portal.ClaimsUnavailable},
		{"unconfigured", unknown(apiv1.PortalTaskClaimsReasonUnconfigured), 200, "", "", portal.ClaimsUnconfigured},
		{"scope", unknown(apiv1.PortalTaskClaimsReasonScopeUnconfirmed), 200, "", "", portal.ClaimsOutOfScope},
		{"unknown unavailable", unknown(apiv1.PortalTaskClaimsReasonUnavailable), 200, "", "", portal.ClaimsUnavailable},
		{"401", `{}`, 401, "", "", portal.ClaimsRefused},
		{"403", `{}`, 403, "", "", portal.ClaimsRefused},
		{"404", `{}`, 404, "", "", portal.ClaimsNotServed},
		{"400", valid, 400, "", "", portal.ClaimsUnavailable},
		{"500", valid, 500, "", "", portal.ClaimsUnavailable},
		{"503", valid, 503, "", "", portal.ClaimsUnavailable},
		{"302", valid, 302, "", "", portal.ClaimsUnavailable},
		{"media", valid, 200, "text/plain", "", portal.ClaimsUnavailable},
		{"oversize", oversize, 200, "", "", portal.ClaimsUnavailable},
		{"other project", claimsJSON(t, other), 200, "", "", portal.ClaimsUnavailable},
		{"limit differs", claimsJSON(t, limit50), 200, "", "", portal.ClaimsUnavailable},
		{"repository grammar", claimsJSON(t, big), 200, "", "", portal.ClaimsUnavailable},
		{"unknown top key", strings.Replace(valid, `{"version"`, `{"extra":1,"version"`, 1), 200, "", "", portal.ClaimsUnavailable},
		{"unknown item key", strings.Replace(valid, `"repository"`, `"actor":{},"repository"`, 1), 200, "", "", portal.ClaimsUnavailable},
		{"unknown owner key", strings.Replace(valid, `"freshness"`, `"namespace":"dev-agents","freshness"`, 1), 200, "", "", portal.ClaimsUnavailable},
		{"missing key", strings.Replace(valid, `"complete":true,`, ``, 1), 200, "", "", portal.ClaimsUnavailable},
		{"duplicate key", strings.Replace(valid, `{"version"`, `{"limit":100,"version"`, 1), 200, "", "", portal.ClaimsUnavailable},
		{"null heartbeat", strings.Replace(valid, `"heartbeatAt":"2026-11-02T14:59:00Z"`, `"heartbeatAt":null`, 1), 200, "", "", portal.ClaimsUnavailable},
		{"empty cursor key", strings.Replace(valid, `"complete":true`, `"nextCursor":"","complete":true`, 1), 200, "", "", portal.ClaimsUnavailable},
		{"state word", strings.Replace(valid, `"state":"Available"`, `"state":"Partial"`, 1), 200, "", "", portal.ClaimsUnavailable},
		{"owner word", strings.Replace(valid, `"state":"Running"`, `"state":"Active"`, 1), 200, "", "", portal.ClaimsUnavailable},
		{"trailing data", valid + `{}`, 200, "", "", portal.ClaimsUnavailable},
		{"not json", `<html>`, 200, "", "", portal.ClaimsUnavailable},
	} {
		media := test.media
		if media == "" {
			media = "application/json"
		}
		o := &claimsOperator{status: test.status, media: media, body: test.body}
		source, _ := claimsTLSSource(t, o)
		page := source.read(context.Background(), test.cursor)
		if page.State != test.want {
			t.Errorf("%s: %s, want %s", test.name, page.State, test.want)
		}
		if test.want != portal.ClaimsAvailable && test.want != portal.ClaimsEmpty && (len(page.Read.Items) != 0 || page.Read.Version != "") {
			t.Errorf("%s carried a read", test.name)
		}
		if page.Project != "dev-env" {
			t.Errorf("%s lost its project", test.name)
		}
		if _, _, err := portal.RenderClaims(page, 1<<20); err != nil {
			t.Errorf("%s does not render: %v", test.name, err)
		}
	}
}

func TestClaimsSourceTimeoutAndUnpinnedServerAreUnavailable(t *testing.T) {
	o := &claimsOperator{block: true}
	source, _ := claimsTLSSource(t, o)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if page := source.read(ctx, ""); page.State != portal.ClaimsUnavailable {
		t.Fatalf("timeout: %s", page.State)
	}
	// A server whose certificate the portal does not pin is never read.
	o2 := &claimsOperator{status: 200, media: "application/json", body: claimsJSON(t, claimsTestPage())}
	cert, _ := mintNamed(t, operatorPortalHost)
	_, otherCA := mintNamed(t, operatorPortalHost)
	addr := serveTLS(t, cert, o2)
	base, err := newSessionSource(filepath.Join(t.TempDir(), "token"), otherCA, func() time.Time { return claimsTestNow }, routeDial(map[string]string{operatorPortalHost: addr}))
	if err != nil {
		t.Fatal(err)
	}
	token := fixtureOperatorToken(apiv1.TokenAudience, claimsTestNow.Add(time.Hour))
	if err := os.WriteFile(base.path, []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	unpinned := &ClaimsSource{path: base.path, client: base.client, now: base.now, project: "dev-env"}
	if page := unpinned.read(context.Background(), ""); page.State != portal.ClaimsUnavailable || o2.requests != 0 {
		t.Fatal("an unpinned operator was read")
	}
	var none *ClaimsSource
	if page := none.read(context.Background(), ""); page.State != portal.ClaimsNotConnected {
		t.Fatal("a nil source is not connected")
	}
}

func TestOptionalClaimsSourceConfig(t *testing.T) {
	certPEM, _ := selfSigned(t, operatorPortalHost)
	write := func(t *testing.T, config string, token bool) string {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "config"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(root, "secrets"), 0700); err != nil {
			t.Fatal(err)
		}
		if config != "" {
			if err := os.WriteFile(filepath.Join(root, "config/claims.json"), []byte(config), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if token {
			if err := os.WriteFile(filepath.Join(root, "secrets/OPERATOR_TOKEN"), []byte(fixtureOperatorToken(apiv1.TokenAudience, time.Now().Add(time.Hour))), 0600); err != nil {
				t.Fatal(err)
			}
		}
		return root
	}
	if source, err := optionalClaimsSource(write(t, "", true), certPEM); source != nil || err != nil {
		t.Fatal("a missing file must leave Claims off")
	}
	for _, off := range []string{`{"enabled":false}`, `{"enabled":false,"project":"dev-env"}`} {
		if source, err := optionalClaimsSource(write(t, off, false), certPEM); source != nil || err != nil {
			t.Fatalf("%s must leave Claims off without credentials", off)
		}
	}
	source, err := optionalClaimsSource(write(t, `{"enabled":true,"project":"dev-env"}`, true), certPEM)
	if err != nil || source == nil || source.project != "dev-env" || source.client == nil {
		t.Fatalf("enabled config refused: %v", err)
	}
	for _, bad := range []string{`{"enabled":true}`, `{"enabled":true,"project":"other"}`, `{"enabled":true,"project":"dev-env","extra":1}`, `{"enabled":"true","project":"dev-env"}`,
		`{"enabled":true,"project":null}`, `{"enabled":true,"enabled":true,"project":"dev-env"}`, `{"enabled":true,"project":"dev-env"} {}`, `[]`, `{"enabled":false,"project":"other"}`} {
		if source, err := optionalClaimsSource(write(t, bad, true), certPEM); source != nil || err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	if source, err := optionalClaimsSource(write(t, `{"enabled":true,"project":"dev-env"}`, false), certPEM); source != nil || err == nil {
		t.Fatal("enabled without an operator credential accepted")
	}
	if _, err := NewClaimsSource("token", certPEM, "other", time.Now); err == nil {
		t.Fatal("a second project accepted")
	}
}

func claimsAssembly(t *testing.T) (*authFixture, AssemblyConfig, *claimsOperator) {
	t.Helper()
	f := setupAuth(t)
	f.now = claimsTestNow
	f.login(t)
	o := &claimsOperator{status: 200, media: "application/json", body: claimsJSON(t, claimsTestPage())}
	source, _ := claimsTLSSource(t, o)
	authorizer := currentFixture(f, func(*http.Request) (*http.Response, error) { return userInfoResponse(currentAllow), nil })
	return f, AssemblyConfig{Session: f.e, Authorizer: authorizer, Reader: portal.UnavailableReader{}, Claims: source, FreshFor: time.Minute, Now: func() time.Time { return f.now }}, o
}

func TestClaimsRouteServesOnlyAConfiguredReadOnlyPage(t *testing.T) {
	f, c, o := claimsAssembly(t)
	if ClaimsPath != "/portal/projects/dev-env/environments/internal/claims" {
		t.Fatal("ClaimsPath moved without the portal's claimsHref")
	}
	w := httptest.NewRecorder()
	NewAssembly(c).ServeHTTP(w, f.request(http.MethodGet, ClaimsPath, ""))
	if w.Code != http.StatusOK || o.requests != 1 {
		t.Fatalf("status %d, reads %d", w.Code, o.requests)
	}
	body := w.Body.String()
	for _, want := range []string{"Claims held in dev-env", "synthetic-owner", "Active", "Claim held", `href="https://github.com/thaynes43/dev-env/issues/7"`, `sessions?session=11111111-0000-4000-8000-000000000001`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Contains(body, "<form") || strings.Contains(body, `name="csrf"`) || strings.Contains(body, "<button") {
		t.Fatal("the Claims page has a control")
	}
	policy := w.Header().Get("Content-Security-Policy")
	style := strings.Split(strings.Split(body, "<style>")[1], "</style>")[0]
	digest := sha256.Sum256([]byte(style))
	if policy != portal.ClaimsPolicy || !strings.Contains(policy, "'sha256-"+base64.StdEncoding.EncodeToString(digest[:])+"'") || !strings.Contains(policy, "form-action 'none'") {
		t.Fatal("stylesheet does not match CSP")
	}
	for _, refused := range []struct{ method, path string }{
		{http.MethodPost, ClaimsPath},
		{http.MethodPut, ClaimsPath},
		{http.MethodHead, ClaimsPath},
		{http.MethodGet, ClaimsPath + "/release"},
		{http.MethodGet, ClaimsPath + "?cursor=a/b"},
		{http.MethodGet, ClaimsPath + "?cursor=a&cursor=b"},
		{http.MethodGet, ClaimsPath + "?project=other"},
		{http.MethodGet, ClaimsPath + "?cursor="},
		{http.MethodGet, ClaimsPath + "?"},
		{http.MethodGet, ClaimsPath + "?cursor=" + strings.Repeat("a", apiv1.PortalTaskClaimsCursorMax+1)},
	} {
		w := httptest.NewRecorder()
		NewAssembly(c).ServeHTTP(w, f.request(refused.method, refused.path, ""))
		if w.Code == http.StatusOK || o.requests != 1 {
			t.Errorf("%s %s: status %d, reads %d", refused.method, refused.path, w.Code, o.requests)
		}
	}
	w = httptest.NewRecorder()
	NewAssembly(c).ServeHTTP(w, f.request(http.MethodGet, ClaimsPath+"?cursor=Y3Vyc29y", ""))
	if w.Code != http.StatusOK || o.requests != 2 || !strings.Contains(o.queries[1], "cursor=Y3Vyc29y") {
		t.Fatalf("cursor page: status %d", w.Code)
	}
	// Signed-out readers go to sign-in and the operator is not read.
	anonymous := httptest.NewRequest(http.MethodGet, "https://portal.example"+ClaimsPath, nil)
	w = httptest.NewRecorder()
	NewAssembly(c).ServeHTTP(w, anonymous)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/auth/login" || o.requests != 2 {
		t.Fatalf("anonymous: status %d", w.Code)
	}
}

func TestClaimsRouteAndNavAbsentWithoutASource(t *testing.T) {
	f, c, o := claimsAssembly(t)
	c.Claims = nil
	w := httptest.NewRecorder()
	NewAssembly(c).ServeHTTP(w, f.request(http.MethodGet, ClaimsPath, ""))
	if w.Code == http.StatusOK || strings.Contains(w.Body.String(), "Claims · dev-env") || o.requests != 0 {
		t.Fatalf("an unconfigured portal served Claims: %d", w.Code)
	}
	for _, configured := range []bool{false, true} {
		cfg := c
		if configured {
			_, cfg, _ = claimsAssembly(t)
			cfg.Session, cfg.Authorizer = c.Session, c.Authorizer
		}
		for _, path := range []string{AsksPath, pagesPath} {
			w := httptest.NewRecorder()
			NewAssembly(cfg).ServeHTTP(w, f.request(http.MethodGet, path, ""))
			links := strings.Count(w.Body.String(), `href="`+ClaimsPath+`"`)
			if w.Code != http.StatusOK || (configured && links != 2) || (!configured && links != 0) {
				t.Errorf("%s configured=%v: status %d, %d Claims links", path, configured, w.Code, links)
			}
		}
	}
}

func TestClaimsReadFailuresNeverShowEmpty(t *testing.T) {
	for _, status := range []int{401, 403, 404, 400, 500} {
		f, c, o := claimsAssembly(t)
		o.status = status
		w := httptest.NewRecorder()
		NewAssembly(c).ServeHTTP(w, f.request(http.MethodGet, ClaimsPath, ""))
		body := w.Body.String()
		if w.Code != http.StatusOK || strings.Contains(body, "No TaskClaims recorded") || strings.Contains(body, "<table") || !strings.Contains(body, "No claims are shown") {
			t.Errorf("status %d: %d %s", status, w.Code, fmt.Sprint(strings.Contains(body, "<table")))
		}
	}
}
