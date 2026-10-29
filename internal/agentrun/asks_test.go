package agentrun

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

const testAskID = "ask-0123456789abcdef"

func askFixture(state apiv1.AskState) apiv1.Ask {
	ask := apiv1.Ask{
		ID: testAskID, Type: apiv1.AskTypeInfo, Question: "Which region?",
		Options: []apiv1.AskOption{{ID: "east", Label: "East"}}, Recommended: "east", AllowFreeText: true,
		Context:   apiv1.AskContext{SessionName: "session-1", SessionUID: "uid-1"},
		CreatedAt: testNow, ExpiresAt: testNow.Add(24 * time.Hour), State: state,
	}
	if state == apiv1.AskStateAnswered {
		ask.Answer = &apiv1.AskAnswer{
			OptionID: "east", Text: "Use east",
			AnsweredBy: apiv1.AskActor{Issuer: "https://id.example", Subject: "user-1", DisplayName: "Tom"},
			AnsweredAt: testNow.Add(time.Minute),
		}
		ask.Delivery = &apiv1.AskDelivery{InboxMessage: "inbox-1", DeliveredAt: testNow.Add(time.Minute + time.Second)}
	}
	return ask
}

func askJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func askClient(t *testing.T, h *harness) (*app, *conn) {
	t.Helper()
	a := &app{env: h.env}
	t.Cleanup(a.close)
	c, err := a.connect(context.Background(), connOpts{})
	if err != nil {
		t.Fatal(err)
	}
	return a, c
}

func askRequest(t *testing.T, raw string) apiv1.AskCreateRequest {
	t.Helper()
	var req apiv1.AskCreateRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatal(err)
	}
	return req
}

func assertAskError(t *testing.T, err error, code int, detail string) {
	t.Helper()
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != code || !strings.Contains(err.Error(), detail) {
		t.Fatalf("error %v, want exit %d containing %q", err, code, detail)
	}
}

func TestAsksClientCreateReplayStatusListWithdraw(t *testing.T) {
	h := newHarness(t)
	a, c := askClient(t, h)
	req := askRequest(t, `{"requestId":"approve-257","type":"approval","question":"Apply the change?","options":[{"id":"yes","label":"Apply"},{"id":"no","label":"Keep"}],"recommended":"yes","context":{"workPackage":257,"links":["https://example.com/change"]},"scope":{"action":"Apply change","target":"test fixture","expiresAt":"2026-10-07T20:00:00Z"},"expiresIn":"24h"}`)
	var record apiv1.Ask
	if err := json.Unmarshal([]byte(`{"id":"`+testAskID+`","type":"approval","question":"Apply the change?","state":"open","createdAt":"2026-10-06T21:00:00Z","expiresAt":"2026-10-07T21:00:00Z","context":{"sessionName":"session-1","sessionUID":"uid-1"},"scope":{"action":"Apply change","target":"test fixture","expiresAt":"2026-10-07T20:00:00Z"}}`), &record); err != nil {
		t.Fatal(err)
	}
	record.Options = req.Options
	record.Recommended = req.Recommended
	creates, gets := 0, 0
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			t.Error("request did not use the discovered token")
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/asks":
			creates++
			var got apiv1.AskCreateRequest
			if err := json.Unmarshal(body, &got); err != nil || !reflect.DeepEqual(got, req) {
				t.Errorf("create intent changed: %s (%v)", body, err)
			}
			status := http.StatusCreated
			if creates > 1 {
				status = http.StatusOK
			}
			writeTestJSON(w, status, record)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/asks/"+testAskID:
			gets++
			if gets > 1 {
				record.State = apiv1.AskStateAnswered
				record.Answer = &apiv1.AskAnswer{OptionID: "yes", Approved: true, AnsweredBy: apiv1.AskActor{Issuer: "https://id.example", Subject: "user-1"}, AnsweredAt: testNow.Add(time.Minute)}
			}
			writeTestJSON(w, http.StatusOK, record)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/asks":
			if r.URL.Query().Get("mine") != "true" || r.URL.Query().Get("limit") != "100" || r.URL.Query().Get("cursor") != "cursor/+?=&" {
				t.Errorf("list query %s", r.URL.RawQuery)
			}
			writeTestJSON(w, http.StatusOK, apiv1.AskList{Asks: []apiv1.Ask{record}, NextCursor: "next"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/asks/ask-fedcba9876543210/withdraw":
			withdrawn := askFixture(apiv1.AskStateWithdrawn)
			withdrawn.ID = "ask-fedcba9876543210"
			writeTestJSON(w, http.StatusOK, withdrawn)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			apiError(w, http.StatusNotFound, apiv1.CodeNotFound, "no route")
		}
	}
	for range 2 {
		got, err := a.CreateAsk(context.Background(), c, req)
		if err != nil || got.ID != testAskID || got.State != "open" {
			t.Fatalf("create/replay: %+v, %v", got, err)
		}
	}
	for _, state := range []string{"open", "answered"} {
		got, err := a.GetAsk(context.Background(), c, testAskID)
		if err != nil || got.ID != testAskID || string(got.State) != state {
			t.Fatalf("status: %+v, %v; want %s", got, err, state)
		}
	}
	page, err := a.ListMyAsks(context.Background(), c, 100, "cursor/+?=&")
	if err != nil || len(page.Asks) != 1 || page.Asks[0].ID != testAskID || page.NextCursor != "next" || page.Complete {
		t.Fatalf("list: %+v, %v", page, err)
	}
	got, err := a.WithdrawAsk(context.Background(), c, "ask-fedcba9876543210")
	if err != nil || got.State != "withdrawn" {
		t.Fatalf("withdraw: %+v, %v", got, err)
	}
	if len(h.api.requests()) != 6 || len(h.sleeps) != 0 {
		t.Fatalf("client waited or polled: requests=%v sleeps=%v", h.api.requests(), h.sleeps)
	}
}

func TestAsksClientCreateRetriesKeepRequestID(t *testing.T) {
	h := newHarness(t)
	a, c := askClient(t, h)
	req := askRequest(t, `{"requestId":"retry-257","type":"info","question":"Which region?","allowFreeText":true}`)
	var bodies [][]byte
	h.api.handle = func(w http.ResponseWriter, _ *http.Request, body []byte) {
		bodies = append(bodies, append([]byte(nil), body...))
		if len(bodies) == 1 {
			apiError(w, http.StatusServiceUnavailable, apiv1.CodeUnavailable, "try later")
			return
		}
		writeTestJSON(w, http.StatusOK, askFixture(apiv1.AskStateOpen))
	}
	got, err := a.CreateAsk(context.Background(), c, req)
	if err != nil || got.ID != testAskID || len(bodies) != 2 || !reflect.DeepEqual(bodies[0], bodies[1]) {
		t.Fatalf("retry changed the intent: ask=%+v err=%v requests=%q", got, err, bodies)
	}
	if !strings.Contains(string(bodies[0]), `"requestId":"retry-257"`) {
		t.Fatalf("missing requestId: %s", bodies[0])
	}
}

func TestAsksClientHTTPErrorClasses(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		code     int
		attempts int
	}{
		{"bad request", 400, ExitFailed, 1},
		{"unauthenticated", 401, ExitAuth, 1},
		{"forbidden or disabled", 403, ExitAuth, 1},
		{"not found or disabled", 404, ExitNotFound, 1},
		{"method refused", 405, ExitFailed, 1},
		{"conflict", 409, ExitFailed, 1},
		{"expired", 410, ExitFailed, 1},
		{"validation", 422, ExitFailed, 1},
		{"limited", 429, ExitRetry, 1},
		{"server failed", 500, ExitFailed, 1},
		{"bad gateway", 502, ExitFailed, 3},
		{"unavailable", 503, ExitRetry, 3},
		{"gateway timeout", 504, ExitFailed, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			a, c := askClient(t, h)
			h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) {
				apiError(w, tc.status, "test_error", "operator detail", apiv1.FieldError{Field: "question", Message: "operator detail"})
			}
			_, err := a.GetAsk(context.Background(), c, testAskID)
			assertAskError(t, err, tc.code, "operator detail")
			if tc.status == 403 || tc.status == 404 {
				contains(t, "disabled error", err.Error(), "asks are not enabled on this operator")
			}
			if len(h.api.requests()) != tc.attempts {
				t.Errorf("requests %d, want %d", len(h.api.requests()), tc.attempts)
			}
		})
	}
}

func TestAsksClientWithdrawUnconfirmedIsNotRetried(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			h := newHarness(t)
			a, c := askClient(t, h)
			h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) {
				apiError(w, status, "unavailable", "acknowledgement lost")
			}
			_, err := a.WithdrawAsk(context.Background(), c, testAskID)
			assertAskError(t, err, ExitUnconfirmed, "do not repeat")
			if len(h.api.requests()) != 1 || len(h.sleeps) != 0 {
				t.Fatalf("withdrawal was retried: %v", h.api.requests())
			}
		})
	}
}

func TestAsksClientRefusesRedirectsAndMalformedResponses(t *testing.T) {
	for _, body := range []string{"", "null", "[]", "not-json", `{"id":`} {
		t.Run("body="+body, func(t *testing.T) {
			h := newHarness(t)
			a, c := askClient(t, h)
			serveRaw(h, map[string]func(*http.Request) (int, string){
				"/v1/asks/" + testAskID: func(*http.Request) (int, string) { return http.StatusOK, body },
			})
			_, err := a.GetAsk(context.Background(), c, testAskID)
			assertAskError(t, err, ExitFailed, "JSON")
		})
	}
	h := newHarness(t)
	a, c := askClient(t, h)
	h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if r.URL.Path != "/v1/asks/"+testAskID {
			t.Errorf("followed redirect to %s", r.URL.Path)
		}
		http.Redirect(w, r, "/redirected", http.StatusTemporaryRedirect)
	}
	_, err := a.GetAsk(context.Background(), c, testAskID)
	assertAskError(t, err, ExitFailed, "307")
	if len(h.api.requests()) != 1 {
		t.Fatal("redirect was followed")
	}
}

func TestAsksClientValidationDoesNotSend(t *testing.T) {
	h := newHarness(t)
	a, c := askClient(t, h)
	_, err := a.CreateAsk(context.Background(), c, apiv1.AskCreateRequest{})
	assertAskError(t, err, ExitUsage, "invalid ask")
	for _, id := range []string{"", "../portal/asks/answer", "ask-ABCDEF0123456789", "ask-0123456789abcdef/answer"} {
		_, err := a.GetAsk(context.Background(), c, id)
		assertAskError(t, err, ExitUsage, "")
		_, err = a.WithdrawAsk(context.Background(), c, id)
		assertAskError(t, err, ExitUsage, "")
	}
	for _, limit := range []int{-1, 101} {
		_, err := a.ListMyAsks(context.Background(), c, limit, "")
		assertAskError(t, err, ExitUsage, "limit")
	}
	if len(h.api.requests()) != 0 {
		t.Fatal("invalid ask input reached the API")
	}
}

func TestAsksClientEmptyListAndRotatingTokens(t *testing.T) {
	h := newHarness(t)
	a, c := askClient(t, h)
	h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if r.URL.Path == "/v1/asks" {
			if r.URL.RawQuery != "mine=true" {
				t.Errorf("default list query %s", r.URL.RawQuery)
			}
			writeTestJSON(w, http.StatusOK, apiv1.AskList{Asks: []apiv1.Ask{}, Complete: true})
			return
		}
		writeTestJSON(w, http.StatusOK, askFixture(apiv1.AskStateOpen))
	}
	page, err := a.ListMyAsks(context.Background(), c, 0, "")
	if err != nil || len(page.Asks) != 0 || !page.Complete || page.NextCursor != "" {
		t.Fatalf("empty page: %+v, %v", page, err)
	}
	h.write("api-token", "rotated-test-token\n")
	if _, err := a.GetAsk(context.Background(), c, testAskID); err != nil {
		t.Fatal(err)
	}
	requests := h.api.requests()
	if len(requests) != 2 || requests[0].auth != "Bearer "+testToken || requests[1].auth != "Bearer rotated-test-token" {
		t.Fatalf("connection did not reread the token file: %v", requests)
	}
}

func TestAsksClientCredentialTLSAndNetworkErrors(t *testing.T) {
	t.Run("missing token", func(t *testing.T) {
		h := newHarness(t)
		a, c := askClient(t, h)
		if err := os.Remove(h.vars[envTokenFile]); err != nil {
			t.Fatal(err)
		}
		_, err := a.GetAsk(context.Background(), c, testAskID)
		assertAskError(t, err, ExitAuth, "no token")
		if len(h.api.requests()) != 0 || len(h.sleeps) != 0 {
			t.Fatal("missing token was sent or retried")
		}
	})
	t.Run("untrusted TLS", func(t *testing.T) {
		h := newHarness(t)
		h.vars[envCAFile] = ""
		a, c := askClient(t, h)
		_, err := a.GetAsk(context.Background(), c, testAskID)
		assertAskError(t, err, ExitAuth, "certificate agent-run does not trust")
		if len(h.api.requests()) != 0 || len(h.sleeps) != 0 {
			t.Fatal("untrusted TLS was sent or retried")
		}
	})
	t.Run("unreachable API", func(t *testing.T) {
		h := newHarness(t)
		a, c := askClient(t, h)
		h.api.srv.Close()
		_, err := a.GetAsk(context.Background(), c, testAskID)
		assertAskError(t, err, ExitRetry, "could not reach")
		if len(h.sleeps) != 2 {
			t.Fatalf("read retries: %v", h.sleeps)
		}
	})
}

func TestAsksClientPlainDisabledResponses(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusNotFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			h := newHarness(t)
			a, c := askClient(t, h)
			serveRaw(h, map[string]func(*http.Request) (int, string){
				"/v1/asks": func(*http.Request) (int, string) { return status, "no route" },
			})
			_, err := a.ListMyAsks(context.Background(), c, 0, "")
			code := ExitAuth
			if status == http.StatusNotFound {
				code = ExitNotFound
			}
			assertAskError(t, err, code, "asks are not enabled on this operator")
		})
	}
}

func TestAsksClientRejectsInvalidReceipts(t *testing.T) {
	for _, body := range []string{`{}`, `{"state":"open"}`, askJSON(t, askFixture("unknown")), askJSON(t, askFixture(apiv1.AskStateAnswered))[:30]} {
		t.Run(body, func(t *testing.T) {
			h := newHarness(t)
			a, c := askClient(t, h)
			serveRaw(h, map[string]func(*http.Request) (int, string){
				"/v1/asks/" + testAskID: func(*http.Request) (int, string) { return http.StatusOK, body },
			})
			_, err := a.GetAsk(context.Background(), c, testAskID)
			assertAskError(t, err, ExitFailed, "JSON")
		})
	}
	h := newHarness(t)
	a, c := askClient(t, h)
	other := askFixture(apiv1.AskStateOpen)
	other.ID = "ask-fedcba9876543210"
	h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		writeTestJSON(w, http.StatusOK, other)
	}
	_, err := a.GetAsk(context.Background(), c, testAskID)
	assertAskError(t, err, ExitFailed, "different ask id")
	_, err = a.WithdrawAsk(context.Background(), c, testAskID)
	assertAskError(t, err, ExitUnconfirmed, "did not confirm")
}

func TestAsksClientRejectsOversizeResponse(t *testing.T) {
	h := newHarness(t)
	a, c := askClient(t, h)
	response := askJSON(t, askFixture(apiv1.AskStateOpen)) + strings.Repeat(" ", maxResponse)
	serveRaw(h, map[string]func(*http.Request) (int, string){
		"/v1/asks/" + testAskID: func(*http.Request) (int, string) { return http.StatusOK, response },
	})
	_, err := a.GetAsk(context.Background(), c, testAskID)
	assertAskError(t, err, ExitFailed, "byte limit")
	if len(h.api.requests()) != 1 {
		t.Fatal("oversized response was retried")
	}
}
