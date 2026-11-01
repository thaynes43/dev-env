package session

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	pa "github.com/thaynes43/dev-env/internal/providerallowance"
	"github.com/thaynes43/dev-env/web/portal"
)

const currentSourceSevenDayReset = "2026-01-22T12:00:00Z"

func TestCurrentSourceTransportProjectsUsageAndDetail(t *testing.T) {
	now := time.Date(2026, 1, 15, 12, 10, 0, 0, time.UTC)
	report := syntheticSourceReport("40", "60", 10080, currentSourceSevenDayReset, "")
	rig := newCurrentSourceRig(t, now, "", true, report)
	handler := rig.handler(nil)

	usage := rig.do(t, handler, SessionsPath+"?view=usage")
	if usage.code != http.StatusOK {
		t.Fatalf("usage status %d", usage.code)
	}
	for _, want := range []string{
		"Current source",
		"Selected source report. Stable account identity is unavailable.",
		"40% used",
		"60% remaining (derived)",
		"Window: 7 days.",
		`Retrieved at <time datetime="2026-01-15T12:00:00Z">15 Jan 2026, 12:00:00</time> (UTC). Retrieval does not establish provider update time.`,
		"Exact observation time not reported.",
		`Resets at <time datetime="2026-01-22T12:00:00Z">22 Jan 2026, 12:00:00</time> (UTC). Resets in 6 days 23 hours 50 minutes, as of 15 Jan 2026, 12:10:00 UTC.`,
		`width="40"`,
		"Not connected",
		"No Cursor account view is connected.",
		`data-label="Input" class="unknown">Unknown`,
		"worker",
	} {
		if !strings.Contains(usage.body, want) {
			t.Errorf("usage missing %q", want)
		}
	}
	for _, banned := range []string{
		"This is a measured provider report", "weekly", "calendar week", " week",
		currentSourceIncarnation, pa.CurrentSourcePublisher, "usedPercent", "providerUpdatedAt",
		rig.token, "SECRET",
	} {
		if strings.Contains(usage.body, banned) {
			t.Errorf("usage leaked %q", banned)
		}
	}
	if usage.slots != 1 || usage.sessions != 1 || usage.slotQuery != 0 {
		t.Fatalf("usage calls slots=%d sessions=%d query=%d", usage.slots, usage.sessions, usage.slotQuery)
	}
	if len(rig.operator.slotAuth()) != 1 || rig.operator.slotAuth()[0] != "Bearer "+rig.token || rig.operator.slotCookies()[0] != "" {
		t.Fatalf("slot authorization %#v cookies %#v", rig.operator.slotAuth(), rig.operator.slotCookies())
	}

	selected := rig.do(t, handler, SessionsPath+"?session=uid-worker")
	if selected.code != http.StatusOK || selected.slots != 1 || selected.sessions != 1 || rig.operator.exact(slotHit()) != 2 {
		t.Fatalf("selected status %d slots %d sessions %d total %d", selected.code, selected.slots, selected.sessions, rig.operator.exact(slotHit()))
	}
	for _, want := range []string{
		`<details class="allowance-detail">`,
		"By provider",
		"40% used · 60% remaining (derived) · 7 days · resets 22 Jan 12:00 UTC",
		"worker",
		"uid-worker",
		`class="detail`,
	} {
		if !strings.Contains(selected.body, want) {
			t.Errorf("selected detail missing %q", want)
		}
	}
	if strings.Contains(selected.body, "This is a measured provider report") || strings.Contains(selected.body, "weekly") || strings.Contains(selected.body, rig.token) {
		t.Fatal("selected detail relabeled the source or echoed the token")
	}
}

func TestCurrentSourceTransportReadsOnlyAfterAuthorization(t *testing.T) {
	now := time.Date(2026, 1, 15, 12, 10, 0, 0, time.UTC)
	report := syntheticSourceReport("40", "60", 10080, currentSourceSevenDayReset, "")

	t.Run("absent", func(t *testing.T) {
		rig := newCurrentSourceRig(t, now, "", false, report)
		got := rig.do(t, rig.handler(nil), SessionsPath+"?view=usage")
		if got.code != http.StatusOK || got.slots != 0 || got.sessions != 1 {
			t.Fatalf("absent status %d slots %d sessions %d", got.code, got.slots, got.sessions)
		}
		if strings.Contains(got.body, "Current source") || strings.Contains(got.body, "40% used") || !strings.Contains(got.body, "Not connected") {
			t.Fatal("absent config projected a current source")
		}
	})

	t.Run("anonymous", func(t *testing.T) {
		rig := newCurrentSourceRig(t, now, "", true, report)
		req := rig.f.request(http.MethodGet, SessionsPath+"?view=usage", "")
		req.Header.Del("Cookie")
		w := httptest.NewRecorder()
		rig.handler(nil).ServeHTTP(w, req)
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/auth/login" || rig.operator.exact(slotHit()) != 0 || rig.operator.sessions() != 0 {
			t.Fatalf("anonymous %d %q slots %d", w.Code, w.Header().Get("Location"), rig.operator.exact(slotHit()))
		}
		if strings.Contains(w.Body.String(), "40% used") || strings.Contains(w.Body.String(), "worker") || strings.Contains(w.Body.String(), rig.token) {
			t.Fatal("anonymous response echoed the report")
		}
	})

	t.Run("not admin", func(t *testing.T) {
		rig := newCurrentSourceRig(t, now, "", true, report)
		auth := currentFixture(rig.f, func(*http.Request) (*http.Response, error) {
			return userInfoResponse(strings.Replace(currentAllow, `"portal_is_admin":true`, `"portal_is_admin":false`, 1)), nil
		})
		w := httptest.NewRecorder()
		rig.handler(auth).ServeHTTP(w, rig.f.request(http.MethodGet, SessionsPath+"?view=usage", ""))
		if w.Code != http.StatusForbidden || rig.operator.exact(slotHit()) != 0 || rig.operator.sessions() != 0 {
			t.Fatalf("denied %d slots %d sessions %d", w.Code, rig.operator.exact(slotHit()), rig.operator.sessions())
		}
		if w.Body.String() != "Portal authentication unavailable or denied.\n" || strings.Contains(w.Body.String(), "40") || strings.Contains(w.Body.String(), "worker") {
			t.Fatalf("denied body %q", w.Body.String())
		}
	})

	t.Run("revoked before release", func(t *testing.T) {
		rig := newCurrentSourceRig(t, now, "", true, report)
		var n int
		auth := currentFixture(rig.f, func(*http.Request) (*http.Response, error) {
			n++
			if n >= 2 {
				return userInfoResponse(strings.Replace(currentAllow, `"portal_is_admin":true`, `"portal_is_admin":false`, 1)), nil
			}
			return userInfoResponse(currentAllow), nil
		})
		w := httptest.NewRecorder()
		rig.handler(auth).ServeHTTP(w, rig.f.request(http.MethodGet, SessionsPath+"?view=usage", ""))
		if n != 2 || w.Code != http.StatusForbidden || rig.operator.exact(slotHit()) != 1 || rig.operator.sessions() != 1 {
			t.Fatalf("revoke auth %d status %d slots %d sessions %d", n, w.Code, rig.operator.exact(slotHit()), rig.operator.sessions())
		}
		if w.Body.String() != "Portal authentication unavailable or denied.\n" || strings.Contains(w.Body.String(), "40% used") || strings.Contains(w.Body.String(), "worker") || strings.Contains(w.Body.String(), rig.token) {
			t.Fatalf("released %q", w.Body.String())
		}
	})

	t.Run("tasks and failed inventory", func(t *testing.T) {
		rig := newCurrentSourceRig(t, now, "", true, report)
		handler := rig.handler(nil)
		before := rig.operator.exact(slotHit())
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, rig.f.request(http.MethodGet, TasksPath, ""))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Tasks not connected") || rig.operator.exact(slotHit()) != before {
			t.Fatal("tasks read the current-source slot")
		}
		rig.operator.sessionsStatus = http.StatusInternalServerError
		rig.operator.sessionsBody = "SECRET-INVENTORY"
		failed := rig.do(t, handler, SessionsPath+"?view=usage")
		if failed.slots != 0 || before != 0 || failed.sessions != 1 || !strings.Contains(failed.body, "Sessions unavailable") || strings.Contains(failed.body, "SECRET-INVENTORY") || strings.Contains(failed.body, "40% used") {
			t.Fatalf("failed inventory slots %d body leaked", failed.slots-before)
		}
	})
}

func TestCurrentSourceTransportFailurePanelKeepsInventory(t *testing.T) {
	now := time.Date(2026, 1, 15, 12, 10, 0, 0, time.UTC)
	report := syntheticSourceReport("40", "60", 10080, currentSourceSevenDayReset, "")
	cases := []struct {
		name string
		slot func(http.ResponseWriter, *http.Request)
	}{
		{name: "forbidden", slot: func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "SECRET-DENIED", http.StatusForbidden)
		}},
		{name: "missing", slot: func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "SECRET-MISSING", http.StatusNotFound)
		}},
		{name: "redirect", slot: func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "https://"+r.Host+"/followed", http.StatusFound)
		}},
		{name: "oversize", slot: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", mediaJSON)
			_, _ = w.Write(bytesRepeat(int(currentSourceReportMaxBytes)+1, 'x'))
		}},
		{name: "malformed", slot: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", mediaJSON)
			_, _ = io.WriteString(w, `{"SECRET-MALFORMED":true}`)
		}},
		{name: "plain", slot: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write(report)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rig := newCurrentSourceRig(t, now, "", true, nil)
			rig.operator.slot = tc.slot
			got := rig.do(t, rig.handler(nil), SessionsPath+"?view=usage")
			assertUnknownInventory(t, got, rig.token)
			if tc.name == "redirect" && rig.operator.contains("GET /followed") {
				t.Fatal("redirect was followed")
			}
			if tc.name == "oversize" && strings.Contains(got.body, strings.Repeat("x", 64)) {
				t.Fatal("oversize body was rendered")
			}
		})
	}

	t.Run("stale", func(t *testing.T) {
		staleNow := time.Date(2026, 1, 15, 12, 31, 0, 0, time.UTC)
		rig := newCurrentSourceRig(t, staleNow, "", true, report)
		assertUnknownInventory(t, rig.do(t, rig.handler(nil), SessionsPath), rig.token)
	})

	t.Run("passed reset", func(t *testing.T) {
		resetNow := time.Date(2026, 1, 15, 17, 0, 0, 0, time.UTC)
		sel := januarySelection()
		sel.MaxAge = 6 * time.Hour
		rig := newCurrentSourceRig(t, resetNow, "", true, januaryCurrentSourceRaw("40", "60", ""))
		rig.sessions.currentSource.selection = sel
		assertUnknownInventory(t, rig.do(t, rig.handler(nil), SessionsPath), rig.token)
	})

	t.Run("invalid selection", func(t *testing.T) {
		rig := newCurrentSourceRig(t, now, "", true, report)
		rig.sessions.currentSource.selection.SourceGeneration = 2
		got := rig.do(t, rig.handler(nil), SessionsPath)
		assertUnknownInventory(t, got, rig.token)
		if strings.Contains(got.body, currentSourceIncarnation) || strings.Contains(got.body, "other-incarnation") {
			t.Fatal("selection id was rendered")
		}
	})

	t.Run("timeout", func(t *testing.T) {
		rig := newCurrentSourceRig(t, now, "", true, report)
		short, err := pinnedClient(rig.ca, operatorPortalHost, time.Second, rig.dial)
		if err != nil {
			t.Fatal(err)
		}
		rig.sessions.currentSource.client = short
		rig.operator.slot = func(w http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
		}
		got := rig.do(t, rig.handler(nil), SessionsPath)
		assertUnknownInventory(t, got, rig.token)
		if rig.operator.exact(slotHit()) != 1 {
			t.Fatal("timeout did not attempt the slot")
		}
	})
}

func TestCurrentSourceTransportRejectsProviderUpdatedAt(t *testing.T) {
	now := time.Date(2026, 1, 15, 12, 10, 0, 0, time.UTC)
	raw := januaryCurrentSourceRaw("40", "60", currentSourceUpdated)
	assertCodexCurrentSource(t, NewCurrentSourceReader(raw, januarySelection()).Allowances(now), "40% used, 60% remaining (derived)", 40, now, currentSourceUpdated)

	rig := newCurrentSourceRig(t, now, "", true, raw)
	got := rig.do(t, rig.handler(nil), SessionsPath+"?view=usage")
	assertUnknownInventory(t, got, rig.token)
	if strings.Contains(got.body, currentSourceUpdated) || strings.Contains(got.body, "11:45") {
		t.Fatal("non-null provider time was rendered")
	}
}

func TestCurrentSourceTransportDoesNotMergeBoundAllowance(t *testing.T) {
	now := time.Date(2026, 1, 15, 12, 10, 0, 0, time.UTC)
	report := syntheticSourceReport("40", "60", 10080, currentSourceSevenDayReset, "")
	rig := newCurrentSourceRig(t, now, "", true, report)
	stub := fixedAllowance{rows: []portal.ProductAllowance{{
		Provider: "Claude", Value: portal.AllowanceMeasured,
		Windows: []portal.ProductAllowanceWindow{{Percent: "BOUND-17% left", PercentKnown: true, PercentValue: 17, Direction: "remaining"}},
	}}}
	rig.sessions.SetAllowanceReader(stub)
	handler := rig.handler(nil)
	got := rig.do(t, handler, SessionsPath+"?view=usage")
	if got.code != http.StatusOK || got.slots != 1 || !strings.Contains(got.body, "40% used") || strings.Contains(got.body, "BOUND-17") || strings.Contains(got.body, "99% used") {
		t.Fatalf("bound report mixed into selected source: %d slots %d", got.code, got.slots)
	}
	if !strings.Contains(got.body, "Not connected") || strings.Contains(got.body, "This is a measured provider report") {
		t.Fatal("selected projection kept a bound measured card")
	}
	rig.operator.slot = func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "SECRET-LATER", http.StatusNotFound)
	}
	failed := rig.do(t, handler, SessionsPath)
	if strings.Contains(failed.body, "BOUND-17") || strings.Contains(failed.body, "40% used") || strings.Contains(failed.body, "SECRET-LATER") || !strings.Contains(failed.body, "Unknown · Current source unavailable") || !strings.Contains(failed.body, "worker") {
		t.Fatal("bound allowance became the fallback for a failed selected read")
	}
	if rig.sessions.allowance == nil || rig.sessions.currentSource == nil {
		t.Fatal("request changed the shared readers")
	}
}

func TestCurrentSourceTransportConcurrentRequestsDoNotMix(t *testing.T) {
	now := time.Date(2026, 1, 15, 12, 10, 0, 0, time.UTC)
	tokenA := distinctOperatorToken("synthetic-a", now.Add(time.Hour))
	tokenB := distinctOperatorToken("synthetic-b", now.Add(2*time.Hour))
	reportA := syntheticSourceReport("40", "60", 10080, currentSourceSevenDayReset, "")
	reportB := syntheticSourceReport("15", "85", 10080, currentSourceSevenDayReset, "")
	rig := newCurrentSourceRig(t, now, tokenA, true, nil)
	if rig.sessions.allowance != nil {
		t.Fatal("connection installed a shared allowance")
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var open sync.Once
	var closeRelease sync.Once
	defer closeRelease.Do(func() { close(release) })
	var mix sync.Mutex
	mixed := false
	rig.operator.slot = func(w http.ResponseWriter, r *http.Request) {
		if rig.sessions.allowance != nil {
			mix.Lock()
			mixed = true
			mix.Unlock()
		}
		switch r.Header.Get("Authorization") {
		case "Bearer " + tokenA:
			open.Do(func() { close(entered) })
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			writeSource(w, reportA)
		case "Bearer " + tokenB:
			writeSource(w, reportB)
		default:
			http.Error(w, "SECRET-TOKEN", http.StatusForbidden)
		}
	}
	handler := rig.handler(nil)
	type page struct {
		code int
		body string
	}
	gotA := make(chan page, 1)
	go func() {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, rig.f.request(http.MethodGet, SessionsPath+"?view=usage", ""))
		gotA <- page{w.Code, w.Body.String()}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first request did not reach the slot")
	}
	if err := os.WriteFile(rig.sessions.path, []byte(tokenB+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	wB := httptest.NewRecorder()
	handler.ServeHTTP(wB, rig.f.request(http.MethodGet, SessionsPath+"?view=usage", ""))
	closeRelease.Do(func() { close(release) })
	var pageA page
	select {
	case pageA = <-gotA:
	case <-time.After(5 * time.Second):
		t.Fatal("first request did not finish")
	}
	mix.Lock()
	defer mix.Unlock()
	if mixed || rig.sessions.allowance != nil {
		t.Fatal("concurrent requests wrote SessionSource.allowance")
	}
	if pageA.code != http.StatusOK || wB.Code != http.StatusOK {
		t.Fatalf("status %d %d", pageA.code, wB.Code)
	}
	if !strings.Contains(pageA.body, "40% used") || strings.Contains(pageA.body, "15% used") || !strings.Contains(wB.Body.String(), "15% used") || strings.Contains(wB.Body.String(), "40% used") {
		t.Fatal("concurrent reports were mixed")
	}
	for _, body := range []string{pageA.body, wB.Body.String()} {
		if strings.Contains(body, tokenA) || strings.Contains(body, tokenB) || strings.Contains(body, "SECRET-TOKEN") {
			t.Fatal("response echoed an operator token")
		}
	}
	auth := rig.operator.slotAuth()
	if len(auth) != 2 || auth[0] != "Bearer "+tokenA || auth[1] != "Bearer "+tokenB {
		t.Fatalf("slot tokens %#v", auth)
	}
	for _, cookie := range rig.operator.slotCookies() {
		if cookie != "" || strings.Contains(strings.Join(auth, " "), rig.f.session.Value) {
			t.Fatal("user session was sent to the operator")
		}
	}
}

func TestCurrentSourceConfigIsImmutableAndOptional(t *testing.T) {
	now := time.Date(2026, 1, 15, 12, 10, 0, 0, time.UTC)
	report := syntheticSourceReport("40", "60", 10080, currentSourceSevenDayReset, "")
	rig := newCurrentSourceRig(t, now, "", false, report)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "config"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := attachCurrentSourceConfig(root, rig.sessions); err != nil || rig.sessions.currentSource != nil || rig.sessions.allowance != nil {
		t.Fatal("absent file connected a reader")
	}
	original := currentSourceConfigDocument(currentSourceIncarnation, 1, 1800)
	path := filepath.Join(root, currentSourceConfigRel)
	if err := os.WriteFile(path, []byte(original+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := attachCurrentSourceConfig(root, rig.sessions); err != nil {
		t.Fatal(err)
	}
	if rig.sessions.allowance != nil || rig.sessions.currentSource == nil || rig.sessions.currentSource.client != rig.sessions.client || rig.sessions.currentSource.tokenPath != rig.sessions.path {
		t.Fatal("config did not reuse the operator client")
	}
	if rig.sessions.currentSource.selection.ProducerIncarnation != currentSourceIncarnation || rig.sessions.currentSource.selection.SourceGeneration != 1 || rig.sessions.currentSource.selection.MaxAge != 1800*time.Second || rig.sessions.currentSource.selection.PublisherRef != pa.CurrentSourcePublisher {
		t.Fatalf("selection %+v", rig.sessions.currentSource.selection)
	}
	if err := os.WriteFile(path, []byte(currentSourceConfigDocument("other-incarnation", 9, 60)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	got := rig.do(t, rig.handler(nil), SessionsPath+"?view=usage")
	if got.code != http.StatusOK || got.slots != 1 || !strings.Contains(got.body, "40% used") || strings.Contains(got.body, "other-incarnation") {
		t.Fatal("request reread a removed config or missed the copied selection")
	}
	if rig.sessions.currentSource.selection.ProducerIncarnation != currentSourceIncarnation || rig.sessions.allowance != nil {
		t.Fatal("request mutated the copied selection or the shared allowance")
	}
}

type currentSourceRig struct {
	f        *authFixture
	sessions *SessionSource
	operator *recordedOperator
	now      time.Time
	token    string
	ca       []byte
	dial     func(context.Context, string, string) (net.Conn, error)
}

type currentSourceResult struct {
	code, slots, sessions, slotQuery int
	body                             string
}

func newCurrentSourceRig(t *testing.T, now time.Time, token string, connect bool, report []byte) *currentSourceRig {
	t.Helper()
	f := setupAuth(t)
	f.now = now
	f.login(t)
	if token == "" {
		token = fixtureOperatorToken(apiv1.TokenAudience, now.Add(time.Hour))
	}
	operator := &recordedOperator{}
	if report != nil {
		body := append([]byte(nil), report...)
		operator.slot = func(w http.ResponseWriter, _ *http.Request) { writeSource(w, body) }
	}
	cert, ca := mintNamed(t, operatorPortalHost)
	address := serveTLS(t, cert, operator)
	dial := routeDial(map[string]string{operatorPortalHost: address})
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "operator")
	if err := os.WriteFile(tokenPath, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sessions, err := newSessionSource(tokenPath, ca, func() time.Time { return now }, dial)
	if err != nil {
		t.Fatal(err)
	}
	if connect {
		sessions.currentSource = &currentSourceConnection{client: sessions.client, tokenPath: tokenPath, selection: januarySelection()}
	}
	return &currentSourceRig{f: f, sessions: sessions, operator: operator, now: now, token: token, ca: ca, dial: dial}
}

func (r *currentSourceRig) handler(auth *CurrentAuthorizer) http.Handler {
	if auth == nil {
		auth = currentFixture(r.f, func(*http.Request) (*http.Response, error) {
			return userInfoResponse(currentAllow), nil
		})
	}
	return NewAssembly(AssemblyConfig{Session: r.f.e, Authorizer: auth, Reader: portal.UnavailableReader{}, Sessions: r.sessions, FreshFor: time.Minute, Now: func() time.Time { return r.now }})
}

func (r *currentSourceRig) do(t *testing.T, handler http.Handler, path string) currentSourceResult {
	t.Helper()
	beforeSlots := r.operator.exact(slotHit())
	beforeSessions := r.operator.sessions()
	beforeQuery := r.operator.slotQueries()
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r.f.request(http.MethodGet, path, ""))
	return currentSourceResult{
		code: w.Code, body: w.Body.String(),
		slots: r.operator.exact(slotHit()) - beforeSlots, sessions: r.operator.sessions() - beforeSessions,
		slotQuery: r.operator.slotQueries() - beforeQuery,
	}
}

func assertUnknownInventory(t *testing.T, got currentSourceResult, token string) {
	t.Helper()
	if got.code != http.StatusOK || got.slots != 1 || got.sessions != 1 || got.slotQuery != 0 {
		t.Fatalf("status %d slots %d sessions %d query %d", got.code, got.slots, got.sessions, got.slotQuery)
	}
	if !strings.Contains(got.body, "worker") || !strings.Contains(got.body, "Unknown · Current source unavailable") || strings.Contains(got.body, "40% used") || strings.Contains(got.body, "Sessions unavailable") {
		t.Fatal("source failure did not stay on the allowance panel")
	}
	for _, banned := range []string{"SECRET", token, currentSourceIncarnation, pa.CurrentSourcePublisher, "usedPercent", "This is a measured provider report"} {
		if strings.Contains(got.body, banned) {
			t.Errorf("panel leaked %q", banned)
		}
	}
}

type recordedOperator struct {
	mu             sync.Mutex
	hits           []string
	authorizations []string
	cookies        []string
	slot           func(http.ResponseWriter, *http.Request)
	sessionsStatus int
	sessionsBody   string
}

func (o *recordedOperator) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	hit := r.Method + " " + r.URL.Path
	if r.URL.RawQuery != "" {
		hit += "?" + r.URL.RawQuery
	}
	o.mu.Lock()
	o.hits = append(o.hits, hit)
	if r.URL.Path == currentSourceReportPath {
		o.authorizations = append(o.authorizations, r.Header.Get("Authorization"))
		o.cookies = append(o.cookies, r.Header.Get("Cookie"))
	}
	slot := o.slot
	status, secret := o.sessionsStatus, o.sessionsBody
	o.mu.Unlock()
	if r.URL.Path == currentSourceReportPath {
		if slot != nil {
			slot(w, r)
			return
		}
		http.NotFound(w, r)
		return
	}
	if r.URL.Path == "/v1/sessions" && r.Method == http.MethodGet && r.URL.Query().Get("view") == apiv1.PortalViewActivity {
		if status != 0 {
			http.Error(w, secret, status)
			return
		}
		w.Header().Set("Content-Type", mediaJSON)
		_, _ = io.WriteString(w, syntheticSessionInventory())
		return
	}
	http.NotFound(w, r)
}

func (o *recordedOperator) snapshot() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.hits...)
}

func (o *recordedOperator) exact(hit string) int {
	n := 0
	for _, got := range o.snapshot() {
		if got == hit {
			n++
		}
	}
	return n
}

func (o *recordedOperator) sessions() int {
	n := 0
	for _, got := range o.snapshot() {
		if strings.HasPrefix(got, "GET /v1/sessions?") {
			n++
		}
	}
	return n
}

func (o *recordedOperator) slotQueries() int {
	n := 0
	for _, got := range o.snapshot() {
		if strings.HasPrefix(got, "GET "+currentSourceReportPath+"?") {
			n++
		}
	}
	return n
}

func (o *recordedOperator) contains(hit string) bool {
	for _, got := range o.snapshot() {
		if got == hit || strings.HasPrefix(got, hit+"?") {
			return true
		}
	}
	return false
}

func (o *recordedOperator) slotAuth() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.authorizations...)
}

func (o *recordedOperator) slotCookies() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.cookies...)
}

func slotHit() string { return "GET " + currentSourceReportPath }

func writeSource(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func syntheticSessionInventory() string {
	return `{"sessions":[{"name":"worker","uid":"uid-worker","repo":"dev-env","phase":"Running","observedAt":"2026-01-15T12:00:00Z","freshness":"observed",` + unknownActivity + `}],"limit":50,"complete":true,"freshness":"observed","observedAt":"2026-01-15T12:00:00Z"}`
}

func syntheticSourceReport(used, remaining string, minutes int, resets, updated string) []byte {
	provider := "null"
	if updated != "" {
		provider = `"` + updated + `"`
	}
	return []byte(`{"schema":"` + pa.CurrentSourceSchema + `",` +
		`"publisherRef":"` + pa.CurrentSourcePublisher + `",` +
		`"producerIncarnation":"` + currentSourceIncarnation + `",` +
		`"sourceGeneration":1,` +
		`"retrievedAt":"` + currentSourceRetrieved + `",` +
		`"providerUpdatedAt":` + provider + `,` +
		`"usedPercent":"` + used + `",` +
		`"remainingPercent":"` + remaining + `",` +
		`"remainingPercentBasis":"derived_complement_of_used_percent",` +
		`"windowDurationMinutes":` + strconv.Itoa(minutes) + `,` +
		`"resetsAt":"` + resets + `"}`)
}

func currentSourceConfigDocument(incarnation string, generation, maxAge int64) string {
	return `{"slot":"` + currentSourceSlotName + `","publisherRef":"` + pa.CurrentSourcePublisher + `","producerIncarnation":"` + incarnation + `","sourceGeneration":` + strconv.FormatInt(generation, 10) + `,"maxAgeSeconds":` + strconv.FormatInt(maxAge, 10) + `}`
}

func distinctOperatorToken(jti string, exp time.Time) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payload, _ := json.Marshal(map[string]any{"aud": apiv1.TokenAudience, "exp": exp.Unix(), "jti": jti})
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func bytesRepeat(n int, b byte) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}

type fixedAllowance struct {
	rows []portal.ProductAllowance
}

func (f fixedAllowance) Allowances(time.Time) []portal.ProductAllowance { return f.rows }
