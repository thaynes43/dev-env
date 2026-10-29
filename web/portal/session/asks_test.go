package session

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/web/portal"
)

type askSessionStore struct {
	Store
	commandReceipts
}

type asksOperator struct {
	t       *testing.T
	items   []apiv1.Ask
	reads   int
	answers []apiv1.AskAnswerRequest
	onRead  func()
	fail    bool
}

func syntheticAsk(now time.Time) apiv1.Ask {
	return apiv1.Ask{ID: "ask-0123456789abcdef", Type: apiv1.AskTypeInfo, Question: "Which fixture should run?", Options: []apiv1.AskOption{{ID: "small", Label: "Small fixture", Description: "Bounded synthetic check"}, {ID: "large", Label: "Large fixture"}}, Recommended: "small", AllowFreeText: true, Context: apiv1.AskContext{SessionName: "synthetic-agent", SessionUID: "00000000-1111-2222-3333-444444444444", WorkPackage: 258, Links: []string{"https://example.com/context"}}, CreatedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour), State: apiv1.AskStateOpen}
}

func (o *asksOperator) trip(r *http.Request) (*http.Response, error) {
	o.t.Helper()
	if r.URL.Scheme != "https" || r.URL.Host != "dev-env-operator.dev-env-system.svc.cluster.local:8443" || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		o.t.Fatal("operator binding lost")
	}
	if o.fail {
		return nil, errors.New("PRIVATE-OPERATOR-ERROR")
	}
	var result any
	if r.Method == http.MethodGet {
		o.reads++
		if r.URL.Path != apiv1.PortalAsksPath || (r.URL.Query().Get("state") != "all" && r.URL.Query().Get("state") != "open") || r.URL.Query().Get("limit") != "20" {
			o.t.Fatal("unexpected asks list destination")
		}
		if o.onRead != nil {
			o.onRead()
		}
		items := []apiv1.Ask{}
		for _, ask := range o.items {
			if r.URL.Query().Get("state") == "all" || ask.State == apiv1.AskStateOpen {
				items = append(items, ask)
			}
		}
		result = apiv1.AskList{Asks: items, Complete: true}
	} else {
		if r.Method != http.MethodPost || r.GetBody != nil || r.Header.Get("Content-Type") != mediaJSON {
			o.t.Fatal("answer retry or media contract lost")
		}
		var answer apiv1.AskAnswerRequest
		if json.NewDecoder(r.Body).Decode(&answer) != nil {
			o.t.Fatal("answer request invalid")
		}
		o.answers = append(o.answers, answer)
		found := false
		for i := range o.items {
			if r.URL.Path == apiv1.PortalAskAnswerPath(o.items[i].ID) {
				o.items[i].State = apiv1.AskStateAnswered
				o.items[i].Answer = &apiv1.AskAnswer{OptionID: answer.OptionID, Text: answer.Text, Approved: answer.Approved, AnsweredBy: answer.Actor, AnsweredAt: o.items[i].CreatedAt.Add(time.Hour)}
				result, found = o.items[i], true
			}
		}
		if !found {
			o.t.Fatal("answer target changed")
		}
	}
	raw, err := json.Marshal(result)
	if err != nil {
		o.t.Fatal(err)
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {mediaJSON}}, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
}

func asksFixture(t *testing.T) (*authFixture, AssemblyConfig, *asksOperator, url.Values) {
	t.Helper()
	f := setupAuth(t)
	f.now = time.Date(2026, 10, 29, 19, 5, 0, 0, time.UTC)
	f.login(t)
	reservations := &commandMemory{reservations: map[string]commandReservation{}, terminals: map[string]apiv1.PortalMessageReceipt{}}
	f.e.config.Store = &askSessionStore{f.store, reservations}
	operator := &asksOperator{t: t, items: []apiv1.Ask{syntheticAsk(f.now)}}
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte(fixtureOperatorToken(apiv1.TokenAudience, f.now.Add(time.Hour))), 0600); err != nil {
		t.Fatal(err)
	}
	source := &AsksSource{path: token, client: &http.Client{Transport: tripFunc(operator.trip)}, now: func() time.Time { return f.now }}
	authorizer := currentFixture(f, func(*http.Request) (*http.Response, error) { return userInfoResponse(currentAllow), nil })
	c := AssemblyConfig{Session: f.e, Authorizer: authorizer, Reader: portal.UnavailableReader{}, Asks: source, FreshFor: time.Minute, Now: func() time.Time { return f.now }}
	_, record, err := f.e.current(f.request(http.MethodGet, AsksPath, ""))
	if err != nil {
		t.Fatal(err)
	}
	v := url.Values{"csrf": {record.CSRF}, "requestID": {strings.Repeat("a", 43)}, "askID": {operator.items[0].ID}, "optionId": {"small"}, "text": {"Synthetic answer"}}
	return f, c, operator, v
}

func askPost(f *authFixture, c AssemblyConfig, v url.Values) *httptest.ResponseRecorder {
	r := f.request(http.MethodPost, AsksPath+"/answer", v.Encode())
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "https://portal.example")
	w := httptest.NewRecorder()
	NewAssembly(c).ServeHTTP(w, r)
	return w
}

func TestAsksListRenderingEscapingAndCSP(t *testing.T) {
	f, c, o, _ := asksFixture(t)
	o.items[0].Question = `<script>alert("question")</script>`
	o.items[0].Options[0].Label = `<img src=x onerror=bad>`
	closed := syntheticAsk(f.now)
	closed.ID = "ask-1123456789abcdef"
	closed.Type = apiv1.AskTypeApproval
	closed.State = apiv1.AskStateAnswered
	closed.Scope = &apiv1.AskScope{Action: `<script>action</script>`, Target: `<img src=x>`, ExpiresAt: f.now.Add(time.Hour)}
	closed.Answer = &apiv1.AskAnswer{OptionID: "small", Text: `<script>answer</script>`, Approved: true, AnsweredAt: f.now, AnsweredBy: apiv1.AskActor{Issuer: "https://issuer.example/", Subject: `<script>subject</script>`, DisplayName: `<script>name</script>`}}
	o.items = append([]apiv1.Ask{closed}, o.items...)
	w := httptest.NewRecorder()
	NewAssembly(c).ServeHTTP(w, f.request(http.MethodGet, AsksPath, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("list status %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{"Needs attention", `aria-label="1 open asks"`, "Recommended", `sessions?session=00000000-1111-2222-3333-444444444444`, `datetime="2026-10-29T19:05:00Z">29 Oct 2026, 19:05 UTC`, "Approval scope", "Recorded answer", "&lt;script&gt;answer&lt;/script&gt;", "&lt;script&gt;action&lt;/script&gt;", "&lt;script&gt;subject&lt;/script&gt;", "&lt;img src=x onerror=bad&gt;", `name="csrf"`, `name="requestID"`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Contains(body, "<script>") || strings.Contains(body, "<img ") || strings.Index(body, `id="ask-0123456789abcdef"`) > strings.Index(body, `id="ask-1123456789abcdef"`) {
		t.Fatal("unsafe text or wrong order")
	}
	meta := strings.Split(strings.Split(body, `http-equiv="Content-Security-Policy" content="`)[1], `"`)[0]
	if html.UnescapeString(meta) != w.Header().Get("Content-Security-Policy") {
		t.Fatal("CSP header differs from meta")
	}
	style := strings.Split(strings.Split(body, "<style>")[1], "</style>")[0]
	digest := sha256.Sum256([]byte(style))
	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "'sha256-"+base64.StdEncoding.EncodeToString(digest[:])+"'") {
		t.Fatal("stylesheet does not match CSP")
	}
}

func TestAsksAnswersInfoApprovalAndReplay(t *testing.T) {
	for _, approval := range []bool{false, true} {
		t.Run(map[bool]string{false: "info", true: "approval"}[approval], func(t *testing.T) {
			f, c, o, v := asksFixture(t)
			if approval {
				o.items[0].Type = apiv1.AskTypeApproval
				o.items[0].Scope = &apiv1.AskScope{Action: "Run synthetic check", Target: "Fixture", ExpiresAt: f.now.Add(time.Hour)}
				v.Set("decision", "approve")
				v.Set("confirm", "yes")
			}
			if w := askPost(f, c, v); w.Code != http.StatusSeeOther || w.Header().Get("Location") != AsksPath {
				t.Fatalf("answer status %d", w.Code)
			}
			if len(o.answers) != 1 || o.answers[0].Actor.Subject != "synthetic-subject" || o.answers[0].Actor.Issuer != f.e.config.Verifier.issuer || o.answers[0].Approved != approval {
				t.Fatal("answer attribution or decision lost")
			}
			if w := askPost(f, c, v); w.Code != http.StatusSeeOther || len(o.answers) != 1 {
				t.Fatalf("replay status=%d dispatches=%d", w.Code, len(o.answers))
			}
			v.Set("text", "changed answer")
			if w := askPost(f, c, v); w.Code != http.StatusForbidden || len(o.answers) != 1 {
				t.Fatal("changed replay executed")
			}
		})
	}
}

func TestAsksRefuseTamperedAndExpiredAnswers(t *testing.T) {
	for _, mode := range []string{"csrf", "duplicate-csrf", "extra-actor", "missing-text", "bad-request-id", "unknown-ask", "unknown-option", "empty", "expired", "scope-expired", "confirm-missing", "confirm-false", "approval-decision", "info-confirm"} {
		t.Run(mode, func(t *testing.T) {
			f, c, o, v := asksFixture(t)
			switch mode {
			case "csrf":
				v.Set("csrf", "wrong")
			case "duplicate-csrf":
				v.Add("csrf", v.Get("csrf"))
			case "extra-actor":
				v.Set("actor", "Tom")
			case "missing-text":
				v.Del("text")
			case "bad-request-id":
				v.Set("requestID", "bad")
			case "unknown-ask":
				v.Set("askID", "ask-aaaaaaaaaaaaaaaa")
			case "unknown-option":
				v.Set("optionId", "missing")
			case "empty":
				v.Set("optionId", "")
				v.Set("text", "")
			case "expired":
				o.items[0].ExpiresAt = f.now
			case "scope-expired", "confirm-missing", "confirm-false", "approval-decision":
				o.items[0].Type = apiv1.AskTypeApproval
				o.items[0].Scope = &apiv1.AskScope{Action: "Run", Target: "Fixture", ExpiresAt: f.now.Add(time.Hour)}
				v.Set("decision", "approve")
				v.Set("confirm", "yes")
				if mode == "scope-expired" {
					o.items[0].Scope.ExpiresAt = f.now
				}
				if mode == "confirm-missing" {
					v.Del("confirm")
				}
				if mode == "confirm-false" {
					v.Set("confirm", "false")
				}
				if mode == "approval-decision" {
					v.Set("decision", "maybe")
				}
			case "info-confirm":
				v.Set("confirm", "yes")
			}
			if w := askPost(f, c, v); w.Code != http.StatusForbidden || len(o.answers) != 0 {
				t.Fatalf("tampering status=%d dispatches=%d", w.Code, len(o.answers))
			}
		})
	}
}

func TestAsksOriginAndExactRoute(t *testing.T) {
	for _, mode := range []string{"missing-origin", "foreign-origin", "duplicate-origin", "json", "query", "encoded-path", "get-answer", "duplicate-text"} {
		t.Run(mode, func(t *testing.T) {
			f, c, o, v := asksFixture(t)
			r := f.request(http.MethodPost, AsksPath+"/answer", v.Encode())
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.Header.Set("Origin", "https://portal.example")
			switch mode {
			case "missing-origin":
				r.Header.Del("Origin")
			case "foreign-origin":
				r.Header.Set("Origin", "https://foreign.example")
			case "duplicate-origin":
				r.Header.Add("Origin", "https://portal.example")
			case "json":
				r.Header.Set("Content-Type", mediaJSON)
			case "query":
				r.URL.RawQuery = "extra=1"
			case "encoded-path":
				r.URL.RawPath = "/portal/projects/dev-env/environments/internal/asks/%61nswer"
			case "get-answer":
				r.Method = http.MethodGet
			case "duplicate-text":
				r.Body = io.NopCloser(strings.NewReader(v.Encode() + "&text=again"))
			}
			w := httptest.NewRecorder()
			NewAssembly(c).ServeHTTP(w, r)
			if w.Code != http.StatusForbidden || o.reads != 0 || len(o.answers) != 0 {
				t.Fatalf("refusal status=%d reads=%d dispatches=%d", w.Code, o.reads, len(o.answers))
			}
		})
	}
}

func TestAsksStaleAndNonAdmittedSessions(t *testing.T) {
	for _, mode := range []string{"not-admitted", "revoked-on-read", "replaced-on-read", "group-withdrawn", "stale", "get-revoked"} {
		t.Run(mode, func(t *testing.T) {
			f, c, o, v := asksFixture(t)
			checks := 0
			c.Authorizer = currentFixture(f, func(*http.Request) (*http.Response, error) {
				checks++
				if mode == "not-admitted" || (mode == "group-withdrawn" && checks == 2) {
					return userInfoResponse(strings.Replace(currentAllow, `"portal_is_admin":true`, `"portal_is_admin":false`, 1)), nil
				}
				return userInfoResponse(currentAllow), nil
			})
			key, _ := requestHandle(f.request(http.MethodGet, AsksPath, ""), sessionCookie)
			if mode == "stale" {
				f.now = f.now.Add(48 * time.Hour)
			}
			o.onRead = func() {
				switch mode {
				case "revoked-on-read", "get-revoked":
					_ = f.store.RevokeSession(context.Background(), key)
				case "replaced-on-read":
					record := f.store.sessions[key]
					record.accessToken = "changed-token"
					f.store.sessions[key] = record
				}
			}
			var w *httptest.ResponseRecorder
			if mode == "get-revoked" {
				w = httptest.NewRecorder()
				NewAssembly(c).ServeHTTP(w, f.request(http.MethodGet, AsksPath, ""))
			} else {
				w = askPost(f, c, v)
			}
			if w.Code != http.StatusForbidden || len(o.answers) != 0 || strings.Contains(w.Body.String(), o.items[0].Question) {
				t.Fatalf("stale authorization status=%d", w.Code)
			}
		})
	}
}

func TestAsksDisconnectedUnavailableAndEmpty(t *testing.T) {
	for _, mode := range []string{"disconnected", "unavailable", "empty"} {
		t.Run(mode, func(t *testing.T) {
			f, c, o, _ := asksFixture(t)
			want := "No open asks."
			if mode == "disconnected" {
				c.Asks = nil
				want = "Asks are not connected"
			}
			if mode == "unavailable" {
				o.fail = true
				want = "Asks are unavailable"
			}
			if mode == "empty" {
				o.items = []apiv1.Ask{}
			}
			w := httptest.NewRecorder()
			NewAssembly(c).ServeHTTP(w, f.request(http.MethodGet, AsksPath, ""))
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), want) {
				t.Fatalf("state %s status %d", mode, w.Code)
			}
			if mode != "empty" && strings.Contains(w.Body.String(), "No open asks.") {
				t.Fatal("failure faked an empty read")
			}
		})
	}
}

func TestAsksStrictListDecoding(t *testing.T) {
	now := time.Date(2026, 10, 29, 19, 5, 0, 0, time.UTC)
	raw, _ := json.Marshal(apiv1.AskList{Asks: []apiv1.Ask{syntheticAsk(now)}, Complete: true})
	good := string(raw)
	for _, body := range []string{good + `{}`, strings.Replace(good, `"complete":true`, `"complete":true,"complete":false`, 1), strings.Replace(good, `"allowFreeText":true`, `"allowFreeText":null`, 1), strings.Replace(good, `"question":`, `"Question":`, 1), strings.Replace(good, `"question":`, `"extra":0,"question":`, 1), strings.Replace(good, `"sessionName":`, `"SessionName":`, 1), strings.Replace(good, `"complete":true`, `"complete":false`, 1), `{"asks":null,"complete":true}`} {
		if _, err := decodeAsksPage([]byte(body)); err == nil {
			t.Fatalf("malformed page accepted %s", body)
		}
	}
	if _, err := decodeAsksPage(raw); err != nil {
		t.Fatal("valid page refused")
	}
}

func TestAsksConfigDefaultOffAndStrict(t *testing.T) {
	for _, doc := range []string{"", `{"enabled":false}`, `{"enabled":true}`, `{"enabled":null}`, `{"Enabled":false}`, `{"enabled":false,"extra":0}`, `{"enabled":false,"enabled":true}`} {
		root := t.TempDir()
		if doc != "" {
			if err := os.Mkdir(filepath.Join(root, "config"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "config/asks.json"), []byte(doc), 0600); err != nil {
				t.Fatal(err)
			}
		}
		source, err := optionalAsksSource(root, nil)
		if doc == "" || doc == `{"enabled":false}` {
			if err != nil || source != nil {
				t.Fatal("default-off config failed")
			}
		} else if err == nil || source != nil {
			t.Fatal("invalid or incomplete config enabled asks")
		}
	}
}

func TestAsksSourcePinnedTLSAndRotatingCredential(t *testing.T) {
	now := time.Date(2026, 10, 29, 19, 5, 0, 0, time.UTC)
	tokenPath := filepath.Join(t.TempDir(), "operator")
	token := fixtureOperatorToken(apiv1.TokenAudience, now.Add(time.Hour))
	if err := os.WriteFile(tokenPath, []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	cert, ca := mintNamed(t, operatorPortalHost)
	calls := 0
	server := serveTLS(t, cert, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer "+token || r.URL.Path != apiv1.PortalAsksPath || r.Method != http.MethodGet {
			t.Error("fixed operator binding lost")
		}
		w.Header().Set("Content-Type", mediaJSON)
		_ = json.NewEncoder(w).Encode(apiv1.AskList{Asks: []apiv1.Ask{syntheticAsk(now)}, Complete: true})
	}))
	base, err := newSessionSource(tokenPath, ca, func() time.Time { return now }, routeDial(map[string]string{operatorPortalHost: server}))
	if err != nil {
		t.Fatal(err)
	}
	source := &AsksSource{path: base.path, client: base.client, now: base.now}
	if rows, complete, err := source.read(context.Background()); err != nil || !complete || len(rows) != 1 {
		t.Fatal("pinned read failed")
	}
	token = fixtureOperatorToken(apiv1.TokenAudience, now.Add(2*time.Hour))
	if err := os.WriteFile(tokenPath, []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := source.read(context.Background()); err != nil || calls != 4 {
		t.Fatal("rotated token not used")
	}
	_, wrongCA := mintNamed(t, operatorPortalHost)
	untrusted, err := newSessionSource(tokenPath, wrongCA, func() time.Time { return now }, routeDial(map[string]string{operatorPortalHost: server}))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := (&AsksSource{path: untrusted.path, client: untrusted.client, now: untrusted.now}).read(context.Background()); err == nil || calls != 4 {
		t.Fatal("untrusted CA admitted")
	}
	if err := os.WriteFile(tokenPath, []byte(fixtureOperatorToken(apiv1.TokenAudience, now)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := source.read(context.Background()); err == nil || calls != 4 {
		t.Fatal("expired credential dispatched")
	}
}

func TestAsksReadPaginationAndFailures(t *testing.T) {
	for _, mode := range []string{"paged", "partial", "duplicate", "cursor-loop", "disabled", "redirect", "bad-media", "oversized", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			f, c, o, _ := asksFixture(t)
			calls := 0
			perState := map[string]int{}
			c.Asks.client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: tripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				state := r.URL.Query().Get("state")
				perState[state]++
				n := perState[state]
				if n > 1 && r.URL.Query().Get("cursor") == "" {
					t.Error("lost cursor")
				}
				page := apiv1.AskList{Asks: []apiv1.Ask{syntheticAsk(f.now)}, Complete: mode == "paged" && n == 2}
				page.Asks[0].ID = "ask-000000000000000" + strconv.Itoa(n)
				if mode == "duplicate" {
					page.Asks[0].ID = "ask-0000000000000001"
					page.Complete = n == 2
				}
				if !page.Complete {
					page.NextCursor = "cursor-" + strconv.Itoa(n)
				}
				if mode == "cursor-loop" {
					page.NextCursor = "same-cursor"
				}
				raw, _ := json.Marshal(page)
				status, media := http.StatusOK, mediaJSON
				switch mode {
				case "disabled":
					status = http.StatusNotFound
				case "redirect":
					status = http.StatusTemporaryRedirect
				case "bad-media":
					media = "text/html"
				case "oversized":
					raw = []byte(strings.Repeat("x", int(productHALLimit)+1))
				case "malformed":
					raw = []byte(`{"asks":null,"complete":true}`)
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {media}, "Location": {"https://example.com/"}}, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
			})}
			rows, complete, err := c.Asks.read(context.Background())
			switch mode {
			case "paged":
				if err != nil || !complete || len(rows) != 2 || calls != 4 {
					t.Fatal("pagination failed")
				}
			case "partial":
				if err != nil || complete || len(rows) != productPageCap || calls != 2*productPageCap {
					t.Fatal("unbounded or falsely complete read")
				}
			default:
				if err == nil || rows != nil || complete {
					t.Fatal("malformed/failed read accepted")
				}
			}
			if mode == "disabled" {
				calls = 0
				perState = map[string]int{}
				w := httptest.NewRecorder()
				NewAssembly(c).ServeHTTP(w, f.request(http.MethodGet, AsksPath, ""))
				if !strings.Contains(w.Body.String(), "Asks are not connected") || len(o.answers) != 0 {
					t.Fatal("disabled operator faked an empty board")
				}
			}
		})
	}
}

func TestAsksAnswerFormsAndDecline(t *testing.T) {
	f, c, o, v := asksFixture(t)
	o.items[0].Type = apiv1.AskTypeApproval
	o.items[0].Scope = &apiv1.AskScope{Action: "Run one check", Target: "Synthetic fixture", ExpiresAt: f.now.Add(time.Hour)}
	w := httptest.NewRecorder()
	NewAssembly(c).ServeHTTP(w, f.request(http.MethodGet, AsksPath, ""))
	for _, want := range []string{`name="confirm" value="yes" required`, `name="decision" value="approve" required`, `name="decision" value="decline" required`, "Run one check", "Synthetic fixture"} {
		if !strings.Contains(w.Body.String(), want) {
			t.Fatalf("missing approval form %s", want)
		}
	}
	v.Set("decision", "decline")
	v.Set("confirm", "yes")
	if w := askPost(f, c, v); w.Code != http.StatusSeeOther || len(o.answers) != 1 || o.answers[0].Approved {
		t.Fatal("decline failed")
	}
}

func TestAsksOpenQuestionsSurviveBusyAnsweredHistory(t *testing.T) {
	f, c, _, _ := asksFixture(t)
	open := syntheticAsk(f.now)
	c.Asks.client = &http.Client{Transport: tripFunc(func(r *http.Request) (*http.Response, error) {
		page := apiv1.AskList{Asks: []apiv1.Ask{open}, Complete: true}
		if r.URL.Query().Get("state") == "all" {
			closed := open
			closed.ID = "ask-aaaaaaaaaaaaaaaa"
			closed.State = apiv1.AskStateWithdrawn
			page.Asks = []apiv1.Ask{closed}
		}
		raw, _ := json.Marshal(page)
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {mediaJSON}}, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
	})}
	w := httptest.NewRecorder()
	NewAssembly(c).ServeHTTP(w, f.request(http.MethodGet, AsksPath, ""))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `aria-label="1 open asks"`) || !strings.Contains(w.Body.String(), `id="ask-0123456789abcdef"`) || !strings.Contains(w.Body.String(), `id="ask-aaaaaaaaaaaaaaaa"`) {
		t.Fatal("history hid an open question")
	}
}

func TestAsksAmbiguousAnswerNeverResendsRequestID(t *testing.T) {
	f, c, o, v := asksFixture(t)
	posts := 0
	c.Asks.client = &http.Client{Transport: tripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPost {
			posts++
			return nil, errors.New("PRIVATE-DISPATCH-UNKNOWN")
		}
		return o.trip(r)
	})}
	if w := askPost(f, c, v); w.Code != http.StatusServiceUnavailable || posts != 1 {
		t.Fatal("ambiguous dispatch was claimed successful")
	}
	if w := askPost(f, c, v); w.Code != http.StatusSeeOther || posts != 1 {
		t.Fatal("ambiguous request was replayed")
	}
}

func TestAsksAttributeTwoGroupMembersToTheirOwnSession(t *testing.T) {
	f, c, o, v := asksFixture(t)
	first := actionLogin(t, f, "first-reader")
	firstCookie := f.session
	second := actionLogin(t, f, "second-reader")
	secondCookie := f.session
	checks := map[string]int{}
	c.Authorizer = actionGroupAuthorizer(f, map[string]Record{"first-reader": first, "second-reader": second}, checks)
	other := syntheticAsk(f.now)
	other.ID = "ask-aaaaaaaaaaaaaaaa"
	o.items = append(o.items, other)
	for i, user := range []struct {
		cookie *http.Cookie
		record Record
	}{{firstCookie, first}, {secondCookie, second}} {
		f.session = user.cookie
		v.Set("csrf", user.record.CSRF)
		v.Set("askID", o.items[i].ID)
		if w := askPost(f, c, v); w.Code != http.StatusSeeOther {
			t.Fatalf("group member status %d", w.Code)
		}
		if o.answers[i].Actor.Subject != user.record.Principal.Subject || checks[user.record.Principal.Subject] != 2 {
			t.Fatal("wrong request-private principal used")
		}
	}
	// A second browser cannot use the first browser's CSRF secret.
	v.Set("csrf", first.CSRF)
	if w := askPost(f, c, v); w.Code != http.StatusForbidden || len(o.answers) != 2 {
		t.Fatal("cross-session form accepted")
	}
}
