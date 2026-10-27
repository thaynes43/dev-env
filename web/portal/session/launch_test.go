package session

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/web/portal"
)

type launchTransportFake struct {
	mu       sync.Mutex
	requests []apiv1.PortalLaunchRequest
	result   launchResult
}

func (f *launchTransportFake) launch(_ context.Context, r apiv1.PortalLaunchRequest) launchResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r)
	if f.result.Outcome == "" {
		return launchResult{Outcome: launchCreated, Receipt: apiv1.PortalLaunchReceipt{RequestID: r.RequestID, SessionName: "dev-env-1027-120000", SessionUID: "uid-new-session", Phase: "Pending", ObservedAt: time.Date(2026, 10, 23, 16, 0, 0, 0, time.UTC)}}
	}
	return f.result
}
func (f *launchTransportFake) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.requests) }

const launchTestID = "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789-_AbCde" // 43 base64url characters

var launchRepos = []string{"dev-env", "haynes-ops"}

func launchFixture(t *testing.T) (*authFixture, *launchHandler, *launchTransportFake, url.Values) {
	t.Helper()
	f := setupAuth(t)
	f.login(t)
	transport := &launchTransportFake{}
	authorizer := currentFixture(f, func(*http.Request) (*http.Response, error) { return userInfoResponse(currentAllow), nil })
	key, _ := requestHandle(f.request("GET", portalLaunchesPath, ""), sessionCookie)
	record := f.store.sessions[key]
	handler := &launchHandler{scope: &launchScope{Principal: record.Principal, Repos: launchRepos}, engine: f.e, authorizer: authorizer, transport: transport, now: func() time.Time { return f.now }}
	values := url.Values{"csrf": {record.CSRF}, "requestID": {launchTestID}, "repo": {"dev-env"}, "mode": {"task"}, "model": {"claude-opus-5-5"}, "effort": {"high"}, "size": {"S"}, "prompt": {"SYNTHETIC PROMPT"}}
	return f, handler, transport, values
}

func launchPost(f *authFixture, h http.Handler, v url.Values, origin string) *httptest.ResponseRecorder {
	r := f.request("POST", portalLaunchesPath, v.Encode())
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestLaunchSuccessRedirectsToTheSelectedAgentAndKeepsSendsActorRef(t *testing.T) {
	f, h, transport, v := launchFixture(t)
	v.Set("prompt", "line one\r\nline two")
	w := launchPost(f, h, v, "https://portal.example")
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != SessionsPath+"?session=uid-new-session" {
		t.Fatalf("status %d location %q body %q", w.Code, w.Header().Get("Location"), w.Body.String())
	}
	if transport.count() != 1 {
		t.Fatalf("%d operator calls", transport.count())
	}
	key, _ := requestHandle(f.request("GET", portalLaunchesPath, ""), sessionCookie)
	want := apiv1.PortalLaunchRequest{RequestID: launchTestID, ActorRef: actorRef(key, f.store.sessions[key].Principal), Repo: "dev-env", Mode: "task", Model: "claude-opus-5-5", Effort: "high", Size: "S", Prompt: "line one\nline two"}
	if transport.requests[0] != want {
		t.Fatalf("request %+v, want %+v", transport.requests[0], want)
	}
	// ActorRef is the derivation Send uses (the same actorRef function over the
	// session handle and principal), so one person is one actor in both.
	// Optional fields may be empty; local and remote carry no prompt.
	for _, mode := range []string{"local", "remote"} {
		v := url.Values{"csrf": v["csrf"], "requestID": {launchTestID}, "repo": {"haynes-ops"}, "mode": {mode}, "model": {"claude-sonnet-5-5"}, "effort": {""}, "size": {""}, "prompt": {""}}
		if w := launchPost(f, h, v, "https://portal.example"); w.Code != http.StatusSeeOther {
			t.Fatalf("%s: %d", mode, w.Code)
		}
	}
	last := transport.requests[len(transport.requests)-1]
	if last.Mode != "remote" || last.Prompt != "" || last.Effort != "" || last.Size != "" || last.Repo != "haynes-ops" {
		t.Fatalf("last request %+v", last)
	}
}

func TestLaunchRefusesTamperedFormsBeforeTheOperator(t *testing.T) {
	good := "https://portal.example"
	for name, tc := range map[string]struct {
		edit   func(url.Values)
		origin string
	}{
		"no origin":          {func(url.Values) {}, ""},
		"other origin":       {func(url.Values) {}, "https://evil.example"},
		"wrong csrf":         {func(v url.Values) { v.Set("csrf", strings.Repeat("x", 43)) }, good},
		"empty csrf":         {func(v url.Values) { v.Set("csrf", "") }, good},
		"short request id":   {func(v url.Values) { v.Set("requestID", "request-0000000001") }, good},
		"long request id":    {func(v url.Values) { v.Set("requestID", launchTestID+"x") }, good},
		"bad request id":     {func(v url.Values) { v.Set("requestID", strings.Repeat("!", 43)) }, good},
		"repo outside scope": {func(v url.Values) { v.Set("repo", "sigoalumni") }, good},
		"repo empty":         {func(v url.Values) { v.Set("repo", "") }, good},
		"mode other":         {func(v url.Values) { v.Set("mode", "batch") }, good},
		"model alias":        {func(v url.Values) { v.Set("model", "opus") }, good},
		"model not offered":  {func(v url.Values) { v.Set("model", "claude-fable-5-1") }, good},
		"effort other":       {func(v url.Values) { v.Set("effort", "turbo") }, good},
		"size L":             {func(v url.Values) { v.Set("size", "L") }, good},
		"extra key":          {func(v url.Values) { v.Set("profile", "full") }, good},
		"agent key":          {func(v url.Values) { v.Set("agent", "codex") }, good},
		"actor key":          {func(v url.Values) { v.Set("actorRef", "someone-else-0000000") }, good},
		"missing model":      {func(v url.Values) { v.Del("model") }, good},
		"missing repo":       {func(v url.Values) { v.Del("repo") }, good},
		"duplicate repo":     {func(v url.Values) { v.Add("repo", "haynes-ops") }, good},
		"prompt control":     {func(v url.Values) { v.Set("prompt", "a\x1b[2Jb") }, good},
		"prompt too large":   {func(v url.Values) { v.Set("prompt", strings.Repeat("p", 64<<10+1)) }, good},
	} {
		t.Run(name, func(t *testing.T) {
			f, h, transport, v := launchFixture(t)
			tc.edit(v)
			if w := launchPost(f, h, v, tc.origin); w.Code == http.StatusSeeOther || w.Code == http.StatusOK || transport.count() != 0 {
				t.Fatalf("status %d, %d operator calls", w.Code, transport.count())
			}
		})
	}
}

func TestLaunchRefusesWrongMethodPathPrincipalAndSession(t *testing.T) {
	f, h, transport, v := launchFixture(t)
	for _, method := range []string{"GET", "PUT", "DELETE", "HEAD"} {
		r := f.request(method, portalLaunchesPath, "")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code == http.StatusOK || w.Code == http.StatusSeeOther {
			t.Fatalf("%s answered %d", method, w.Code)
		}
	}
	for _, target := range []string{portalLaunchesPath + "/", portalLaunchesPath + "/x", portalLaunchesPath + "?repo=x", "/portal/other"} {
		r := f.request("POST", target, v.Encode())
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", "https://portal.example")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code == http.StatusSeeOther || w.Code == http.StatusOK {
			t.Fatalf("%s answered %d", target, w.Code)
		}
	}
	// A principal other than the scope's is refused.
	other := *h
	other.scope = &launchScope{Principal: portal.Principal{Issuer: h.scope.Principal.Issuer, Subject: "someone-else"}, Repos: launchRepos}
	if w := launchPost(f, &other, v, "https://portal.example"); w.Code != http.StatusForbidden {
		t.Fatalf("other principal answered %d", w.Code)
	}
	// No portal session at all.
	r := httptest.NewRequest("POST", "https://portal.example"+portalLaunchesPath, strings.NewReader(v.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "https://portal.example")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code == http.StatusSeeOther || w.Code == http.StatusOK {
		t.Fatalf("anonymous answered %d", w.Code)
	}
	// An incomplete handler is unavailable.
	var empty *launchHandler
	w = httptest.NewRecorder()
	empty.ServeHTTP(w, f.request("POST", portalLaunchesPath, ""))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil handler answered %d", w.Code)
	}
	if transport.count() != 0 {
		t.Fatalf("%d operator calls", transport.count())
	}
}

func TestLaunchPromptRulesAnswerWithAFieldNotADenial(t *testing.T) {
	for name, edit := range map[string]func(url.Values){
		"task without a prompt": func(v url.Values) { v.Set("prompt", "") },
		"task blank prompt":     func(v url.Values) { v.Set("prompt", " \r\n\t") },
		"local with a prompt":   func(v url.Values) { v.Set("mode", "local") },
		"remote with a prompt":  func(v url.Values) { v.Set("mode", "remote") },
	} {
		t.Run(name, func(t *testing.T) {
			f, h, transport, v := launchFixture(t)
			edit(v)
			w := launchPost(f, h, v, "https://portal.example")
			body := w.Body.String()
			if w.Code != http.StatusOK || transport.count() != 0 || !strings.Contains(body, "The launch was not valid") || !strings.Contains(body, "prompt") || strings.Contains(body, "SYNTHETIC PROMPT") {
				t.Fatalf("status %d calls %d: %s", w.Code, transport.count(), body)
			}
		})
	}
}

func TestLaunchOutcomesRenderFixedWordingAndNeverThePromptOrOperatorText(t *testing.T) {
	for name, tc := range map[string]struct {
		result launchResult
		want   string
	}{
		"limit":       {launchResult{Outcome: launchLimit}, "Too many launches are running"},
		"unavailable": {launchResult{Outcome: launchUnavailable}, "Launching is unavailable"},
		"unknown":     {launchResult{Outcome: launchUnknown}, "Outcome Unknown"},
		"invalid": {launchResult{Outcome: launchInvalid, Fields: []apiv1.FieldError{
			{Field: "effort", Message: "model claude-sonnet-5-5 takes low, medium, high, xhigh, max"},
			{Field: "prompt", Message: "echo SYNTHETIC PROMPT back"},
			{Field: "long", Message: strings.Repeat("m", 201)},
		}}, "The launch was not valid"},
		"created with an unusable uid": {launchResult{Outcome: launchCreated, Receipt: apiv1.PortalLaunchReceipt{SessionUID: "bad uid/../"}}, "Outcome Unknown"},
	} {
		t.Run(name, func(t *testing.T) {
			f, h, transport, v := launchFixture(t)
			transport.result = tc.result
			w := launchPost(f, h, v, "https://portal.example")
			body := w.Body.String()
			if w.Code != http.StatusOK || !strings.Contains(body, tc.want) || strings.Contains(body, "SYNTHETIC PROMPT") || strings.Contains(body, "<form") || w.Header().Get("Content-Security-Policy") != portal.ProductPolicy {
				t.Fatalf("status %d policy %q: %s", w.Code, w.Header().Get("Content-Security-Policy"), body)
			}
			if name == "invalid" && (!strings.Contains(body, "takes low, medium") || strings.Contains(body, "echo ") || strings.Contains(body, "mmmmmmmmmm")) {
				t.Fatalf("field errors wrong: %s", body)
			}
			if name == "unknown" && !strings.Contains(body, "will not start a second session") {
				t.Fatalf("unknown wording lost its retry warning: %s", body)
			}
			if transport.count() != 1 {
				t.Fatalf("%d operator calls", transport.count())
			}
		})
	}
}

func TestLaunchRoutedByTheAssemblyOnlyWhenConfigured(t *testing.T) {
	f, h, transport, v := launchFixture(t)
	config := AssemblyConfig{Session: f.e, Authorizer: h.authorizer, Reader: portal.UnavailableReader{}, FreshFor: time.Minute, Now: func() time.Time { return f.now }}
	w := launchPost(f, NewAssembly(config), v, "https://portal.example")
	if w.Code != http.StatusServiceUnavailable || transport.count() != 0 {
		t.Fatalf("default-off assembly answered %d with %d calls", w.Code, transport.count())
	}
	config.Launch = h
	w = launchPost(f, NewAssembly(config), v, "https://portal.example")
	if w.Code != http.StatusSeeOther || transport.count() != 1 {
		t.Fatalf("wired assembly answered %d with %d calls", w.Code, transport.count())
	}
	// A launch handler is not a send route.
	r := f.request("POST", portalMessagesPath, v.Encode())
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "https://portal.example")
	w = httptest.NewRecorder()
	NewAssembly(config).ServeHTTP(w, r)
	if w.Code == http.StatusSeeOther || transport.count() != 1 {
		t.Fatalf("launch handler served the send route: %d", w.Code)
	}
}

func TestNewLaunchFormsRequiresBoundDependencies(t *testing.T) {
	if forms, err := NewLaunchForms(nil, nil, "", nil, nil, nil); err == nil || forms != nil {
		t.Fatal("incomplete launch dependencies accepted")
	}
	f := setupAuth(t)
	authorizer := currentFixture(f, func(*http.Request) (*http.Response, error) { return userInfoResponse(currentAllow), nil })
	scope := &launchScope{Principal: portal.Principal{Issuer: "https://issuer.example/portal/", Subject: "s"}, Repos: launchRepos}
	if forms, err := NewLaunchForms(f.e, authorizer, filepath.Join(t.TempDir(), "token"), nil, time.Now, scope); err == nil || forms != nil {
		t.Fatal("a launch route without an operator CA was accepted")
	}
	if forms, err := NewLaunchForms(f.e, authorizer, "x", nil, time.Now, &launchScope{Principal: scope.Principal}); err == nil || forms != nil {
		t.Fatal("a scope without repositories was accepted")
	}
}

func TestLaunchRepoListIsPrincipalBound(t *testing.T) {
	_, h, _, _ := launchFixture(t)
	if got := h.LaunchRepos(h.scope.Principal); strings.Join(got, ",") != "dev-env,haynes-ops" {
		t.Fatalf("repos %v", got)
	}
	if h.LaunchRepos(portal.Principal{Issuer: h.scope.Principal.Issuer, Subject: "other"}) != nil || (*launchHandler)(nil).LaunchRepos(h.scope.Principal) != nil {
		t.Fatal("a repository list was given to another principal")
	}
	got := h.LaunchRepos(h.scope.Principal)
	got[0] = "changed"
	if h.scope.Repos[0] != "dev-env" {
		t.Fatal("the scope's list is aliased")
	}
}

func TestServeProductAddsTheLaunchFormOnlyForTheScopePrincipalOnSessions(t *testing.T) {
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
	own := &launchHandler{scope: &launchScope{Principal: record.Principal, Repos: launchRepos}}
	elsewhere := &launchHandler{scope: &launchScope{Principal: portal.Principal{Issuer: record.Principal.Issuer, Subject: "someone-else"}, Repos: launchRepos}}
	get := func(launch LaunchForms, send SendForms, path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		NewAssembly(AssemblyConfig{Session: f.e, Authorizer: authorizer, Reader: portal.UnavailableReader{}, Sessions: sessions, Send: send, Launch: launch, FreshFor: time.Minute, Now: func() time.Time { return now }}).ServeHTTP(w, f.request(http.MethodGet, path, ""))
		return w
	}
	// Not configured, or configured for another principal: no form, read-only policy.
	for name, launch := range map[string]LaunchForms{"off": nil, "other principal": elsewhere} {
		w := get(launch, nil, SessionsPath)
		if w.Code != http.StatusOK || strings.Contains(w.Body.String(), `id="launch"`) || strings.Contains(w.Body.String(), "<form") || w.Header().Get("Content-Security-Policy") != portal.ProductPolicy {
			t.Fatalf("%s: status %d policy %q", name, w.Code, w.Header().Get("Content-Security-Policy"))
		}
	}
	// Configured for this principal: the form, bound to this session's CSRF, and the send policy.
	w := get(own, nil, SessionsPath)
	body := w.Body.String()
	if w.Code != http.StatusOK || w.Header().Get("Content-Security-Policy") != portal.ProductSendPolicy || !strings.Contains(body, `id="launch"`) ||
		!strings.Contains(body, `name="csrf" value="`+record.CSRF+`"`) || !strings.Contains(body, `action="`+portalLaunchesPath+`"`) ||
		!strings.Contains(body, `<option value="dev-env">`) || !strings.Contains(body, `<option value="haynes-ops">`) || !strings.Contains(body, `<option value="claude-opus-5-5">`) ||
		!strings.Contains(body, `name="prompt"`) || strings.Contains(body, "claude-fable") || !strings.Contains(body, "form-action &#39;self&#39;") {
		t.Fatalf("launch form wrong: status %d", w.Code)
	}
	if !strings.Contains(body, "Read and launch") {
		t.Fatalf("page does not say it can launch")
	}
	// With send also configured, both are said.
	if w := get(own, &countingForms{}, SessionsPath); !strings.Contains(w.Body.String(), "Read, message and launch") || w.Header().Get("Content-Security-Policy") != portal.ProductSendPolicy {
		t.Fatal("combined access wording or policy wrong")
	}
	// Never on Tasks.
	w = get(own, nil, TasksPath)
	if strings.Contains(w.Body.String(), `id="launch"`) || strings.Contains(w.Body.String(), "<form") || w.Header().Get("Content-Security-Policy") != portal.ProductPolicy {
		t.Fatal("Tasks page carried a launch form")
	}
	// The request ID is fresh and well formed for each render.
	a, b := get(own, nil, SessionsPath).Body.String(), get(own, nil, SessionsPath).Body.String()
	idOf := func(s string) string {
		i := strings.Index(s, `name="requestID" value="`)
		return s[i+len(`name="requestID" value="`) : i+len(`name="requestID" value="`)+43]
	}
	if idOf(a) == idOf(b) || !launchRequestID.MatchString(idOf(a)) {
		t.Fatalf("request IDs %q %q", idOf(a), idOf(b))
	}
}
