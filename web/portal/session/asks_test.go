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
		if r.URL.Path == apiv1.PortalAsksPath {
			if (r.URL.Query().Get(apiv1.AskQueryState) != apiv1.AskStateFilterAll && r.URL.Query().Get(apiv1.AskQueryState) != apiv1.AskStateFilterOpen) || r.URL.Query().Get(apiv1.AskQueryLimit) != strconv.Itoa(apiv1.AskListDefaultLimit) {
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
			if o.onRead != nil {
				o.onRead()
			}
			for _, ask := range o.items {
				if r.URL.Path == apiv1.PortalAskPath(ask.ID) {
					result = ask
				}
			}
			if result == nil {
				return askResponse(http.StatusNotFound, `{}`), nil
			}
		}
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
	approved := true
	closed.Answer = &apiv1.AskAnswer{OptionID: "small", Text: `<script>answer</script>`, Approved: &approved, AnsweredAt: f.now, AnsweredBy: apiv1.AskActor{Issuer: "https://issuer.example/", Subject: `<script>subject</script>`, DisplayName: `<script>name</script>`}}
	o.items = append([]apiv1.Ask{closed}, o.items...)
	w := httptest.NewRecorder()
	NewAssembly(c).ServeHTTP(w, f.request(http.MethodGet, AsksPath, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("list status %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{"Needs attention", `aria-label="1 open asks"`, "Recommended", `sessions?session=00000000-1111-2222-3333-444444444444`, `datetime="2026-10-29T19:05:00Z">29 Oct 2026, 19:05 UTC`, "Approval scope", "Recorded answer", "&lt;script&gt;answer&lt;/script&gt;", "&lt;script&gt;action&lt;/script&gt;", "&lt;script&gt;name&lt;/script&gt;", "&lt;img src=x onerror=bad&gt;", `name="csrf"`, `name="requestID"`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Contains(body, "<script>") || strings.Contains(body, "<img ") || strings.Index(body, `id="ask-0123456789abcdef"`) > strings.Index(body, `id="ask-1123456789abcdef"`) {
		t.Fatal("unsafe text or wrong order")
	}
	meta := strings.Split(strings.Split(body, `http-equiv="Content-Security-Policy" content="`)[1], `"`)[0]
	if html.UnescapeString(meta) != strings.TrimSuffix(w.Header().Get("Content-Security-Policy"), "; frame-ancestors 'none'") || strings.Contains(meta, "frame-ancestors") || !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors") {
		t.Fatal("CSP meta/header frame-ancestors contract lost")
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
				v.Set("optionId", "")
				v.Set("decision", "approve")
				v.Set("confirm", "yes")
			}
			if w := askPost(f, c, v); w.Code != http.StatusSeeOther || w.Header().Get("Location") != AsksPath+"?ask="+v.Get("askID") {
				t.Fatalf("answer status %d", w.Code)
			}
			if len(o.answers) != 1 || o.answers[0].Actor.Subject != "synthetic-subject" || o.answers[0].Actor.Issuer != f.e.config.Verifier.issuer || (approval && (o.answers[0].Approved == nil || !*o.answers[0].Approved)) || (!approval && o.answers[0].Approved != nil) {
				t.Fatal("answer attribution or decision lost")
			}
			if w := askPost(f, c, v); w.Code != http.StatusSeeOther || len(o.answers) != 1 {
				t.Fatalf("replay status=%d dispatches=%d", w.Code, len(o.answers))
			}
			v.Set("text", "changed answer")
			if w := askPost(f, c, v); w.Code != http.StatusConflict || len(o.answers) != 1 {
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
				v.Set("optionId", "")
				v.Set("decision", "approve")
				v.Set("confirm", "yes")
				if mode == "scope-expired" {
					o.items[0].Scope.ExpiresAt = f.now
					o.items[0].ExpiresAt = f.now
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
			want := http.StatusForbidden
			switch mode {
			case "unknown-ask":
				want = http.StatusServiceUnavailable
			case "unknown-option", "empty", "confirm-missing", "confirm-false", "approval-decision":
				want = http.StatusBadRequest
			case "expired", "scope-expired":
				want = http.StatusGone
			}
			if w := askPost(f, c, v); w.Code != want || len(o.answers) != 0 {
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
	if result, err := source.read(context.Background(), ""); err != nil || !result.OpenComplete || len(result.Asks) != 1 {
		t.Fatal("pinned read failed")
	}
	token = fixtureOperatorToken(apiv1.TokenAudience, now.Add(2*time.Hour))
	if err := os.WriteFile(tokenPath, []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := source.read(context.Background(), ""); err != nil || calls != 4 {
		t.Fatal("rotated token not used")
	}
	_, wrongCA := mintNamed(t, operatorPortalHost)
	untrusted, err := newSessionSource(tokenPath, wrongCA, func() time.Time { return now }, routeDial(map[string]string{operatorPortalHost: server}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (&AsksSource{path: untrusted.path, client: untrusted.client, now: untrusted.now}).read(context.Background(), ""); err == nil || calls != 4 {
		t.Fatal("untrusted CA admitted")
	}
	if err := os.WriteFile(tokenPath, []byte(fixtureOperatorToken(apiv1.TokenAudience, now)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := source.read(context.Background(), ""); err == nil || calls != 4 {
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
			result, err := c.Asks.read(context.Background(), "")
			switch mode {
			case "paged":
				if err != nil || !result.OpenComplete || result.HistoryComplete || result.NextCursor != "cursor-1" || len(result.Asks) != 2 || calls != 3 {
					t.Fatal("pagination failed")
				}
			case "partial":
				if err != nil || result.OpenComplete || len(result.Asks) != productPageCap || calls != productPageCap+1 {
					t.Fatal("unbounded or falsely complete read")
				}
			default:
				if err == nil || result.Asks != nil || result.OpenComplete {
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
	for _, want := range []string{`name="confirm" value="yes" required`, `type="submit" name="decision" value="approve">Approve</button>`, `type="submit" name="decision" value="deny">Deny</button>`, "Run one check", "Synthetic fixture"} {
		if !strings.Contains(w.Body.String(), want) {
			t.Fatalf("missing approval form %s", want)
		}
	}
	v.Set("optionId", "")
	v.Set("decision", "deny")
	v.Set("confirm", "yes")
	if w := askPost(f, c, v); w.Code != http.StatusSeeOther || len(o.answers) != 1 || o.answers[0].Approved == nil || *o.answers[0].Approved {
		t.Fatal("decline failed")
	}
	w = httptest.NewRecorder()
	NewAssembly(c).ServeHTTP(w, f.request(http.MethodGet, AsksPath, ""))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "<strong>Denied</strong>") || strings.Contains(w.Body.String(), "<strong>Approved</strong>") {
		t.Fatal("explicit false decision displayed as approval")
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
	if w := askPost(f, c, v); w.Code != http.StatusServiceUnavailable || posts != 1 || !strings.Contains(w.Body.String(), "did not confirm") {
		t.Fatal("ambiguous dispatch was claimed successful")
	}
	if w := askPost(f, c, v); w.Code != http.StatusConflict || w.Header().Get("Location") != "" || posts != 1 || !strings.Contains(w.Body.String(), "was not recorded") || strings.Contains(w.Body.String(), v.Get("requestID")) || !strings.Contains(w.Body.String(), v.Get("text")) || !strings.Contains(w.Body.String(), `value="small" checked`) {
		t.Fatal("unrecorded retry was replayed or reported as success, or lost the fresh form")
	}
	o.items[0].State = apiv1.AskStateAnswered
	o.items[0].Answer = &apiv1.AskAnswer{OptionID: v.Get("optionId"), Text: v.Get("text"), AnsweredBy: apiv1.AskActor{Issuer: f.e.config.Verifier.issuer, Subject: "synthetic-subject"}, AnsweredAt: f.now}
	if w := askPost(f, c, v); w.Code != http.StatusSeeOther || w.Header().Get("Location") != AsksPath+"?ask="+v.Get("askID") || posts != 1 {
		t.Fatal("identical recorded answer was not recognized without replay")
	}
}

func TestAsksReadFailureNotices(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		status     int
		err        error
	}{
		{name: "timeout", err: context.DeadlineExceeded, want: "did not answer within 12 s"},
		{name: "unreachable", err: errors.New("PRIVATE-TRANSPORT"), want: "could not be reached"},
		{name: "refused", status: http.StatusBadGateway, want: "refused the Asks read with HTTP 502"},
		{name: "unauthorized", status: http.StatusUnauthorized, want: "did not authorize"},
		{name: "denied", status: http.StatusForbidden, want: "did not authorize"},
		{name: "unreadable", status: http.StatusOK, want: "unreadable Asks response"},
		{name: "credential", want: "credential is unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, c, _, _ := asksFixture(t)
			if tc.name == "credential" {
				c.Asks.path = filepath.Join(t.TempDir(), "absent")
			}
			c.Asks.client = &http.Client{Transport: tripFunc(func(r *http.Request) (*http.Response, error) {
				if tc.err != nil {
					return nil, tc.err
				}
				return askResponse(tc.status, `{"private":"PRIVATE-RESPONSE"}`), nil
			})}
			w := httptest.NewRecorder()
			NewAssembly(c).ServeHTTP(w, f.request(http.MethodGet, AsksPath, ""))
			body := w.Body.String()
			if w.Code != http.StatusOK || !strings.Contains(body, tc.want) || !strings.Contains(body, "29 Oct 2026, 19:05:00 UTC") || !strings.Contains(body, "Refresh asks") || strings.Contains(body, "PRIVATE-") || strings.Contains(body, "No open asks.") {
				t.Fatalf("read failure missing safe reason/time/retry: %s", tc.name)
			}
		})
	}
}

func TestAsksAnswerReadFailureDoesNotSend(t *testing.T) {
	for _, denied := range []bool{false, true} {
		t.Run(strconv.FormatBool(denied), func(t *testing.T) {
			f, c, o, v := asksFixture(t)
			calls := 0
			c.Asks.client = &http.Client{Transport: tripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != http.MethodGet {
					t.Fatal("answer sent after failed read")
				}
				if denied {
					return askResponse(http.StatusForbidden, `{"error":"PRIVATE-DENIED"}`), nil
				}
				return nil, errors.New("PRIVATE-READ")
			})}
			w := askPost(f, c, v)
			body := w.Body.String()
			if calls != 1 || len(o.answers) != 0 || w.Header().Get("Location") != "" || strings.Contains(body, "PRIVATE-") || strings.Contains(body, o.items[0].Question) {
				t.Fatal("failed read sent an answer or exposed details")
			}
			if denied {
				if w.Code != http.StatusForbidden || strings.Contains(body, `class="topbar"`) {
					t.Fatal("denied read did not stay generic")
				}
			} else if w.Code != http.StatusServiceUnavailable || !strings.Contains(body, "could not be read, so your answer was not sent") || !strings.Contains(body, `href="`+AsksPath+`?ask=`+v.Get("askID")+`">Refresh asks`) || !strings.Contains(body, `class="topbar"`) {
				t.Fatal("failed read lost its notice or selected retry")
			}
		})
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

func TestAsksMultilineAnswersAndApprovalOnlyDecisions(t *testing.T) {
	for _, mode := range []string{"info-multiline", "approval-only", "approval-note"} {
		t.Run(mode, func(t *testing.T) {
			f, c, o, v := asksFixture(t)
			if mode == "info-multiline" {
				v.Set("text", "First line\r\nSecond line")
			} else {
				o.items[0].Type = apiv1.AskTypeApproval
				o.items[0].Options = nil
				o.items[0].Recommended = ""
				o.items[0].AllowFreeText = false
				o.items[0].Scope = &apiv1.AskScope{Action: "Run", Target: "Fixture", ExpiresAt: f.now.Add(time.Hour)}
				v.Set("optionId", "")
				v.Set("text", "")
				v.Set("optionId", "")
				v.Set("decision", "deny")
				v.Set("confirm", "yes")
				if mode == "approval-note" {
					v.Set("text", "First line\r\nSecond line")
				}
			}
			if w := askPost(f, c, v); w.Code != http.StatusSeeOther || len(o.answers) != 1 {
				t.Fatalf("amended answer status %d", w.Code)
			}
			if mode == "approval-only" {
				if o.answers[0].Approved == nil || *o.answers[0].Approved || o.answers[0].Text != "" {
					t.Fatal("decision-only decline lost")
				}
			} else if o.answers[0].Text != "First line\nSecond line" {
				t.Fatal("multiline answer lost or CRLF not normalized")
			}
		})
	}
}

func TestAsksExpiredApprovalIsHistoricalAndRenderingBounded(t *testing.T) {
	f, c, o, _ := asksFixture(t)
	approved := true
	ask := &o.items[0]
	ask.Type = apiv1.AskTypeApproval
	ask.ExpiresAt = f.now.Add(-time.Minute)
	ask.Scope = &apiv1.AskScope{Action: "Run", Target: "Fixture", ExpiresAt: ask.ExpiresAt}
	ask.State = apiv1.AskStateAnswered
	ask.Answer = &apiv1.AskAnswer{OptionID: "small", Approved: &approved, AnsweredBy: apiv1.AskActor{Issuer: "https://identity.example/", Subject: "fixture-reader"}, AnsweredAt: f.now.Add(-2 * time.Minute)}
	w := httptest.NewRecorder()
	NewAssembly(c).ServeHTTP(w, f.request(http.MethodGet, AsksPath, ""))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "<strong>Approved</strong>") || !strings.Contains(w.Body.String(), `Approval expired <time datetime="2026-10-29T19:04:00Z">29 Oct 2026, 19:04 UTC</time>`) || strings.Contains(w.Body.String(), "computed") || strings.Contains(w.Body.String(), "<form") {
		t.Fatal("expired approval presented as usable")
	}
	page := portal.AsksPage{Asks: o.items, Connected: true, Complete: true, Now: f.now}
	if body, policy, err := portal.RenderAsks(page, 100); err == nil || body != nil || policy != "" {
		t.Fatal("partial oversized page released")
	}
	page.Now = time.Time{}
	if _, _, err := portal.RenderAsks(page, 1<<20); err == nil {
		t.Fatal("approval rendered without a current time")
	}
}

func askResponse(status int, raw string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {mediaJSON}}, Body: io.NopCloser(strings.NewReader(raw))}
}

func TestAsksContextLinksUseExactHostAllowlist(t *testing.T) {
	for _, test := range []struct {
		link      string
		clickable bool
	}{
		{"https://claude.ai/chat/fixture", true},
		{"https://github.com/thaynes43/dev-env", true},
		{"https://work.haynesnetwork.com/work_packages/258", true},
		{"https://portal.example/fixture", true},
		{"https://github.com.evil.example/path", false},
		{"https://github.com:443/path", false},
		{"https://claude.ai:8443/path", false},
		{"https://portal.example:443/path", false},
		{"https://portal.example.evil.example/path", false},
		{"https://github.com./path", false},
		{"https://evil.example/github.com", false},
	} {
		t.Run(test.link, func(t *testing.T) {
			f, c, o, _ := asksFixture(t)
			o.items[0].Context.Links = []string{test.link}
			w := httptest.NewRecorder()
			NewAssembly(c).ServeHTTP(w, f.request(http.MethodGet, AsksPath, ""))
			body := w.Body.String()
			if w.Code != http.StatusOK || strings.Contains(body, `href="`+test.link+`"`) != test.clickable {
				t.Fatalf("link allowlist failed, status %d", w.Code)
			}
			u, _ := url.Parse(test.link)
			if !strings.Contains(body, u.Host) {
				t.Fatal("context host hidden")
			}
		})
	}
}

func TestAsksSelectedDetailFirstInAnyState(t *testing.T) {
	for _, state := range []apiv1.AskState{apiv1.AskStateOpen, apiv1.AskStateAnswered, apiv1.AskStateWithdrawn, apiv1.AskStateExpired} {
		t.Run(string(state), func(t *testing.T) {
			f, c, o, _ := asksFixture(t)
			selected := syntheticAsk(f.now)
			selected.ID, selected.State = "ask-aaaaaaaaaaaaaaaa", state
			selected.CreatedAt = selected.CreatedAt.Add(-time.Hour)
			if state == apiv1.AskStateAnswered {
				selected.Answer = &apiv1.AskAnswer{OptionID: "small", AnsweredAt: f.now, AnsweredBy: apiv1.AskActor{Issuer: "https://identity.example/", Subject: "reader"}}
			}
			listReads, detailReads := 0, 0
			c.Asks.client = &http.Client{Transport: tripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == apiv1.PortalAskPath(selected.ID) {
					detailReads++
					if r.URL.RawQuery != "" {
						t.Fatal("query leaked to detail")
					}
					raw, _ := json.Marshal(selected)
					return askResponse(200, string(raw)), nil
				}
				listReads++
				return o.trip(r)
			})}
			w := httptest.NewRecorder()
			NewAssembly(c).ServeHTTP(w, f.request(http.MethodGet, AsksPath+"?ask="+selected.ID, ""))
			body := w.Body.String()
			if w.Code != http.StatusOK || listReads != 2 || detailReads != 1 || strings.Count(body, `id="`+selected.ID+`"`) != 1 || strings.Index(body, `id="`+selected.ID+`"`) > strings.Index(body, `id="`+o.items[0].ID+`"`) {
				t.Fatal("selected detail missing, duplicated, or not first")
			}
		})
	}
}

func TestAsksSelectionQueryRefusesAmbiguity(t *testing.T) {
	id := "ask-0123456789abcdef"
	for _, query := range []string{"ask=", "ask=bad", "ask=" + id + "&ask=" + id, "ask=" + id + "&extra=1", "ask=" + id + ";extra=1", "Ask=" + id, "cursor=", "cursor=one&cursor=two", "ask=%zz"} {
		t.Run(query, func(t *testing.T) {
			f, c, o, _ := asksFixture(t)
			r := f.request(http.MethodGet, AsksPath, "")
			r.URL.RawQuery = query
			w := httptest.NewRecorder()
			NewAssembly(c).ServeHTTP(w, r)
			if w.Code != http.StatusForbidden || o.reads != 0 {
				t.Fatal("ambiguous selection reached operator")
			}
		})
	}
}

func TestAsksPostUsesOnlyDetailRead(t *testing.T) {
	f, c, o, v := asksFixture(t)
	reads := 0
	c.Asks.client = &http.Client{Transport: tripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodGet {
			reads++
			if r.URL.Path != apiv1.PortalAskPath(v.Get("askID")) || r.URL.RawQuery != "" {
				t.Fatal("answer re-read board instead of detail")
			}
		}
		return o.trip(r)
	})}
	if w := askPost(f, c, v); w.Code != http.StatusSeeOther || reads != 1 || w.Header().Get("Location") != AsksPath+"?ask="+v.Get("askID") {
		t.Fatal("detail answer/redirect contract lost")
	}
}

func TestAsksLifecycleAndValidationMessagesStayInShell(t *testing.T) {
	for _, mode := range []string{"answered", "withdrawn", "expired", "empty", "invalid-option", "invalid-text", "upstream-400", "upstream-409", "upstream-410", "upstream-401", "upstream-403"} {
		t.Run(mode, func(t *testing.T) {
			f, c, o, v := asksFixture(t)
			want, message := http.StatusBadRequest, "Check your answer"
			switch mode {
			case "answered":
				o.items[0].State = apiv1.AskStateAnswered
				o.items[0].Answer = &apiv1.AskAnswer{OptionID: "large", AnsweredAt: f.now, AnsweredBy: apiv1.AskActor{Issuer: "https://identity.example/", Subject: "other-reader"}}
				want, message = http.StatusConflict, "already answered or withdrawn"
			case "withdrawn":
				o.items[0].State = apiv1.AskStateWithdrawn
				want, message = http.StatusConflict, "already answered or withdrawn"
			case "expired":
				o.items[0].ExpiresAt = f.now
				want, message = http.StatusGone, "has expired"
			case "empty":
				v.Set("optionId", "")
				v.Set("text", "")
				message = "Write an answer"
			case "invalid-option":
				v.Set("optionId", "missing")
			case "invalid-text":
				v.Set("text", "bad\x00answer")
			default:
				code, _ := strconv.Atoi(strings.TrimPrefix(mode, "upstream-"))
				want = code
				if code == 409 {
					message = "already answered or withdrawn"
				}
				if code == 410 {
					message = "has expired"
				}
				if code == 401 || code == 403 {
					want, message = http.StatusForbidden, ""
				}
				c.Asks.client = &http.Client{Transport: tripFunc(func(r *http.Request) (*http.Response, error) {
					if r.Method == http.MethodPost {
						return askResponse(code, `{"error":"PRIVATE-UPSTREAM-DETAIL"}`), nil
					}
					return o.trip(r)
				})}
			}
			w := askPost(f, c, v)
			body := w.Body.String()
			if w.Code != want || strings.Contains(body, "PRIVATE-UPSTREAM-DETAIL") {
				t.Fatalf("status %d, want %d", w.Code, want)
			}
			if message != "" && (!strings.Contains(body, message) || !strings.Contains(body, `class="topbar"`) || !strings.Contains(body, `role="alert"`)) {
				t.Fatalf("inline message missing: %s", message)
			}
			if message == "" && strings.Contains(body, o.items[0].Question) {
				t.Fatal("auth refusal exposed ask")
			}
			if (want == http.StatusConflict || want == http.StatusGone) && strings.Contains(body, "<form") {
				t.Fatal("lifecycle refusal offered a stale answer form")
			}
		})
	}
}

func TestAsksApprovalReadOnlyOptionsCannotContradictDecision(t *testing.T) {
	for _, decision := range []string{"approve", "deny"} {
		f, c, o, v := asksFixture(t)
		o.items[0].Type = apiv1.AskTypeApproval
		o.items[0].Scope = &apiv1.AskScope{Action: "Run", Target: "Fixture", ExpiresAt: f.now.Add(time.Hour)}
		o.items[0].Options = []apiv1.AskOption{{ID: "reject", Label: "Reject"}, {ID: "approve", Label: "Approve"}}
		o.items[0].Recommended = ""
		v.Set("decision", decision)
		v.Set("confirm", "yes")
		v.Set("optionId", map[string]string{"approve": "reject", "deny": "approve"}[decision])
		if w := askPost(f, c, v); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "read-only context") || len(o.answers) != 0 {
			t.Fatal("contradictory option executed")
		}
	}
}

func TestAsksPastExpiryOnGetUsesComputedState(t *testing.T) {
	f, c, o, _ := asksFixture(t)
	o.items[0].ExpiresAt = f.now
	w := httptest.NewRecorder()
	NewAssembly(c).ServeHTTP(w, f.request(http.MethodGet, AsksPath, ""))
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, `tone-muted">Expired</span>`) || strings.Contains(body, "computed") || strings.Contains(body, "<form") || !strings.Contains(body, `aria-label="0 open asks"`) {
		t.Fatal("open stored state past expiry still answerable")
	}
}

func TestAsksAnswerNormalizesCRAndAllowsTabs(t *testing.T) {
	f, c, o, v := asksFixture(t)
	v.Set("text", "First\r\nSecond\rThird\tcolumn")
	if w := askPost(f, c, v); w.Code != http.StatusSeeOther || len(o.answers) != 1 || o.answers[0].Text != "First\nSecond\nThird\tcolumn" {
		t.Fatal("shared text normalization/tab contract lost")
	}
}

func TestAsksAttributionUsesIssuerAndSubjectOnly(t *testing.T) {
	for _, mode := range []string{"display-name", "issuer", "subject"} {
		t.Run(mode, func(t *testing.T) {
			f, c, o, v := asksFixture(t)
			c.Asks.client = &http.Client{Transport: tripFunc(func(r *http.Request) (*http.Response, error) {
				response, err := o.trip(r)
				if err == nil && r.Method == http.MethodPost {
					actor := &o.items[0].Answer.AnsweredBy
					actor.DisplayName = "Fixture display name"
					if mode == "issuer" {
						actor.Issuer = "https://other.example/"
					}
					if mode == "subject" {
						actor.Subject = "other-reader"
					}
					raw, _ := json.Marshal(o.items[0])
					response = askResponse(200, string(raw))
				}
				return response, err
			})}
			want := http.StatusSeeOther
			if mode != "display-name" {
				want = http.StatusServiceUnavailable
			}
			if w := askPost(f, c, v); w.Code != want {
				t.Fatalf("identity comparison status %d, want %d", w.Code, want)
			}
			if mode == "display-name" {
				w := httptest.NewRecorder()
				NewAssembly(c).ServeHTTP(w, f.request(http.MethodGet, AsksPath, ""))
				if !strings.Contains(w.Body.String(), "<dd>Fixture display name</dd>") || strings.Contains(w.Body.String(), "<dd>synthetic-subject</dd>") {
					t.Fatal("display name did not take precedence")
				}
				if w := askPost(f, c, v); w.Code != http.StatusSeeOther || len(o.answers) != 1 {
					t.Fatal("display name changed identical retry identity")
				}
			}
		})
	}
}

func TestAsksHistoryIsOnePageAndOpenCoverageIsIndependent(t *testing.T) {
	for _, mode := range []string{"paged-history", "unavailable-history", "older-page", "incomplete-open"} {
		t.Run(mode, func(t *testing.T) {
			f, c, _, _ := asksFixture(t)
			openCalls, historyCalls := 0, 0
			cursor := ""
			if mode == "older-page" {
				cursor = "opaque+/= older"
			}
			c.Asks.client = &http.Client{Transport: tripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path != apiv1.PortalAsksPath || r.URL.Query().Get(apiv1.AskQueryLimit) != strconv.Itoa(apiv1.AskListDefaultLimit) {
					t.Fatal("list contract lost")
				}
				page := apiv1.AskList{Complete: true}
				if r.URL.Query().Get(apiv1.AskQueryState) == apiv1.AskStateFilterOpen {
					openCalls++
					if mode != "incomplete-open" && r.URL.Query().Get(apiv1.AskQueryCursor) != "" {
						t.Fatal("history cursor leaked into open query")
					}
					ask := syntheticAsk(f.now)
					ask.ID = "ask-000000000000000" + strconv.Itoa(openCalls)
					page.Asks = []apiv1.Ask{ask}
					if mode == "incomplete-open" {
						page.Complete, page.NextCursor = false, "open-"+strconv.Itoa(openCalls)
					}
				} else {
					historyCalls++
					if historyCalls != 1 || r.URL.Query().Get(apiv1.AskQueryCursor) != cursor {
						t.Fatal("history paged automatically or cursor lost")
					}
					if mode == "unavailable-history" {
						return askResponse(503, `{}`), nil
					}
					ask := syntheticAsk(f.now)
					ask.ID, ask.State = "ask-aaaaaaaaaaaaaaaa", apiv1.AskStateWithdrawn
					page.Asks, page.Complete, page.NextCursor = []apiv1.Ask{ask}, false, "next+/= cursor"
				}
				raw, _ := json.Marshal(page)
				return askResponse(200, string(raw)), nil
			})}
			path := AsksPath
			if cursor != "" {
				path += "?" + url.Values{apiv1.AskQueryCursor: {cursor}}.Encode()
			}
			w := httptest.NewRecorder()
			NewAssembly(c).ServeHTTP(w, f.request(http.MethodGet, path, ""))
			body := w.Body.String()
			if w.Code != http.StatusOK || historyCalls != 1 || !strings.Contains(body, `id="ask-0000000000000001"`) {
				t.Fatal("history suppressed open asks")
			}
			if mode == "incomplete-open" {
				if openCalls != productPageCap || strings.Contains(body, `aria-label="4 open asks"`) || !strings.Contains(body, "partial read") {
					t.Fatal("incomplete open coverage claimed a count")
				}
			} else if openCalls != 1 || !strings.Contains(body, `aria-label="1 open asks"`) {
				t.Fatal("history incompleteness suppressed complete open count")
			}
			if mode == "unavailable-history" {
				if !strings.Contains(body, "Older history is unavailable") {
					t.Fatal("history failure hidden")
				}
			} else if !strings.Contains(body, `href="`+AsksPath+`?cursor=next%2B%2F%3D&#43;cursor"`) || !strings.Contains(body, ">Older asks</a>") || !strings.Contains(body, "history may be cut off") {
				t.Fatal("older asks cursor/note missing")
			}
		})
	}
}

func TestAsksSizeCapDropsHistoryBeforeOpenAsks(t *testing.T) {
	now := time.Date(2026, 10, 29, 20, 0, 0, 0, time.UTC)
	open := syntheticAsk(now)
	page := portal.AsksPage{Asks: []apiv1.Ask{open}, Connected: true, Complete: true, Now: now}
	baseline, _, err := portal.RenderAsks(page, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < apiv1.AskListDefaultLimit; i++ {
		closed := syntheticAsk(now)
		closed.ID = "ask-aaaaaaaaaaaaaa" + strconv.FormatInt(int64(16+i), 16)
		closed.State = apiv1.AskStateWithdrawn
		closed.Question = strings.Repeat("<", apiv1.AskQuestionMaxRunes)
		page.Asks = append(page.Asks, closed)
	}
	limit := len(baseline) + 300
	body, _, err := portal.RenderAsks(page, limit)
	if err != nil || len(body) > limit || !strings.Contains(string(body), `id="`+open.ID+`"`) || !strings.Contains(string(body), `aria-label="1 open asks"`) || !strings.Contains(string(body), "Older history was omitted to fit this page.") || strings.Contains(string(body), `id="ask-aaaaaaaaaaaaaa`) {
		t.Fatal("history exhausted the page before open asks were rendered")
	}
}

func TestAsksAnsweredActorDisplayNameOrSubject(t *testing.T) {
	for _, displayName := range []string{"Fixture reader", ""} {
		f, c, o, _ := asksFixture(t)
		o.items[0].State = apiv1.AskStateAnswered
		o.items[0].Answer = &apiv1.AskAnswer{OptionID: "small", AnsweredAt: f.now, AnsweredBy: apiv1.AskActor{Issuer: "https://identity.example/", Subject: "fixture-subject", DisplayName: displayName}}
		w := httptest.NewRecorder()
		NewAssembly(c).ServeHTTP(w, f.request(http.MethodGet, AsksPath, ""))
		want := displayName
		if want == "" {
			want = "fixture-subject"
		}
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "<dt>Answered by</dt><dd>"+want+"</dd>") {
			t.Fatal("answerer name/fallback lost")
		}
	}
}
