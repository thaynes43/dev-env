package apiserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func TestAsksRoutesDefaultOff(t *testing.T) {
	f := newFixture(t)
	wantError(t, f.do(http.MethodGet, apiv1.AsksPath+"?mine=true", tokHuman, nil), http.StatusNotFound, apiv1.CodeNotFound)
	wantError(t, f.do(http.MethodPost, apiv1.PortalAskAnswerPath("ask-0123456789abcdef"), tokHuman, apiv1.AskAnswerRequest{}), http.StatusNotFound, apiv1.CodeNotFound)
}

func TestAskQueryRejects(t *testing.T) {
	mine := []string{apiv1.AskQueryMine, apiv1.AskQueryLimit, apiv1.AskQueryCursor}
	for _, raw := range []string{"", "mine=false", "mine=true&limit=101", "mine=true&limit=0", "mine=true&extra=1", "mine=true&mine=true"} {
		r := httptest.NewRequest(http.MethodGet, apiv1.AsksPath+"?"+raw, nil)
		if _, _, err := parseAskQuery(r, mine, true, false); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
	portal := httptest.NewRequest(http.MethodGet, apiv1.PortalAsksPath, nil)
	if _, _, err := parseAskQuery(portal, []string{apiv1.AskQueryState, apiv1.AskQueryLimit, apiv1.AskQueryCursor}, false, true); err == nil {
		t.Fatal("missing state was accepted")
	}
	ok := httptest.NewRequest(http.MethodGet, apiv1.AsksPath+"?mine=true&limit=1", nil)
	limit, cursor, err := parseAskQuery(ok, mine, true, false)
	if err != nil || limit != 1 || cursor != "" {
		t.Fatal(limit, cursor, err)
	}
}

func TestPresentAskHidesActorFromSession(t *testing.T) {
	now := time.Date(2026, 10, 29, 20, 0, 0, 0, time.UTC)
	yes := true
	ask := apiv1.Ask{
		ID: "ask-0123456789abcdef", Type: apiv1.AskTypeApproval, Question: "Deploy the change?",
		State: apiv1.AskStateAnswered, CreatedAt: now, ExpiresAt: now.Add(time.Hour),
		Context: apiv1.AskContext{SessionName: "sess", SessionUID: "uid-1"},
		Scope:   &apiv1.AskScope{Action: "deploy", Target: "dev-env", ExpiresAt: now.Add(time.Hour)},
		Answer: &apiv1.AskAnswer{
			Approved: &yes, AnsweredAt: now,
			AnsweredBy: apiv1.AskActor{Issuer: "https://issuer.example", Subject: "subject-1", DisplayName: "Board user"},
		},
		Delivery: &apiv1.AskDelivery{InboxMessage: "inbox-1", DeliveredAt: now},
	}
	raw, err := json.Marshal(presentAsk(ask, now, true))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "issuer") || strings.Contains(string(raw), "subject-1") || strings.Contains(string(raw), "delivery") || strings.Contains(string(raw), "inbox-1") {
		t.Fatal(string(raw))
	}
	if !strings.Contains(string(raw), "Board user") {
		t.Fatal(string(raw))
	}
	portal, err := json.Marshal(presentAsk(ask, now, false))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(portal), "https://issuer.example") || !strings.Contains(string(portal), "subject-1") || strings.Contains(string(portal), "delivery") {
		t.Fatal(string(portal))
	}
	ask.Answer.AnsweredBy.DisplayName = ""
	if got := presentAsk(ask, now, true).Answer.AnsweredBy.DisplayName; got != apiv1.AskActorFallbackDisplayName {
		t.Fatal(got)
	}
	open := ask
	open.Answer = nil
	open.State = apiv1.AskStateOpen
	open.ExpiresAt = now
	if presentAsk(open, now, true).State != apiv1.AskStateExpired {
		t.Fatal("a lapsed open ask was still open")
	}
}
