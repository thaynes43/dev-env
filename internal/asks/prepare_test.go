package asks

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func testRequester() Requester {
	return Requester{Namespace: Namespace, Name: "sess", UID: "uid-1"}
}

func infoCreate(id string) apiv1.AskCreateRequest {
	return apiv1.AskCreateRequest{
		RequestID: id, Type: apiv1.AskTypeInfo, Question: "Which option?",
		Options:       []apiv1.AskOption{{ID: "a", Label: "Alpha"}},
		Recommended:   "a",
		AllowFreeText: true,
		ExpiresIn:     "1h",
	}
}

func TestPrepareNormalizesAndRefusesFractionalScope(t *testing.T) {
	now := time.Date(2026, 10, 29, 20, 0, 0, 0, time.UTC)
	req := testRequester()
	crlf := infoCreate("request-01")
	crlf.Question = "line\r\nline"
	crlf.Options[0].Description = "more\r\ndetail"
	got, err := prepare(req, crlf, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Spec.Question != "line\nline" || got.Spec.Options[0].Description != "more\ndetail" {
		t.Fatalf("question %q description %q", got.Spec.Question, got.Spec.Options[0].Description)
	}
	if crlf.Question != "line\r\nline" {
		t.Fatal("prepare mutated the caller's question")
	}
	lf := infoCreate("request-01")
	lf.Question = "line\nline"
	lf.Options[0].Description = "more\ndetail"
	again, err := prepare(req, lf, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Spec.RequestDigest != again.Spec.RequestDigest || got.Name != again.Name {
		t.Fatal("CRLF and LF did not prepare as the same ask")
	}
	if !got.Spec.CreatedAt.Time.Equal(now) || got.Spec.CreatedAt.Nanosecond() != 0 {
		t.Fatalf("createdAt %v", got.Spec.CreatedAt.Time)
	}

	fraction := infoCreate("request-02")
	fraction.Type = apiv1.AskTypeApproval
	fraction.ExpiresIn = ""
	fraction.Options = nil
	fraction.Recommended = ""
	fraction.AllowFreeText = false
	fraction.Scope = &apiv1.AskScope{Action: "deploy", Target: "dev-env", ExpiresAt: now.Add(2*time.Hour + 100*time.Millisecond)}
	if _, err := prepare(req, fraction, now); !errors.Is(err, apiv1.ErrAskInvalid) {
		t.Fatal("fractional scope.expiresAt was accepted", err)
	}

	ok := fraction
	ok.RequestID = "request-03"
	ok.Scope = &apiv1.AskScope{Action: "deploy", Target: "dev-env", ExpiresAt: now.Add(2 * time.Hour)}
	stored, err := prepare(req, ok, now.Add(250*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if !stored.Spec.ExpiresAt.Time.Equal(now.Add(2*time.Hour)) || !stored.Spec.Scope.ExpiresAt.Time.Equal(stored.Spec.ExpiresAt.Time) {
		t.Fatalf("approval expiry created %v expires %v scope %v", stored.Spec.CreatedAt.Time, stored.Spec.ExpiresAt.Time, stored.Spec.Scope.ExpiresAt.Time)
	}
	if stored.Spec.CreatedAt.Nanosecond() != 0 || stored.Spec.ExpiresAt.Nanosecond() != 0 {
		t.Fatal("stored times are not whole seconds")
	}

	over := ok
	over.RequestID = "request-04"
	over.ExpiresIn = "3h"
	if _, err := prepare(req, over, now); !errors.Is(err, apiv1.ErrAskInvalid) {
		t.Fatal("approval ask outliving its scope was accepted", err)
	}
}

func TestPageNewestFirst(t *testing.T) {
	base := time.Date(2026, 10, 29, 20, 0, 0, 0, time.UTC)
	items := []apiv1.Ask{
		{ID: "ask-0000000000000001", Type: apiv1.AskTypeInfo, Question: "q", State: apiv1.AskStateOpen, CreatedAt: base, ExpiresAt: base.Add(time.Hour), Context: apiv1.AskContext{SessionName: "sess", SessionUID: "uid-1"}, Options: []apiv1.AskOption{{ID: "a", Label: "Alpha"}}, AllowFreeText: true},
		{ID: "ask-0000000000000002", Type: apiv1.AskTypeInfo, Question: "q", State: apiv1.AskStateOpen, CreatedAt: base, ExpiresAt: base.Add(time.Hour), Context: apiv1.AskContext{SessionName: "sess", SessionUID: "uid-1"}, Options: []apiv1.AskOption{{ID: "a", Label: "Alpha"}}, AllowFreeText: true},
		{ID: "ask-0000000000000003", Type: apiv1.AskTypeInfo, Question: "q", State: apiv1.AskStateOpen, CreatedAt: base.Add(time.Second), ExpiresAt: base.Add(time.Hour), Context: apiv1.AskContext{SessionName: "sess", SessionUID: "uid-1"}, Options: []apiv1.AskOption{{ID: "a", Label: "Alpha"}}, AllowFreeText: true},
	}
	var got []string
	cursor := ""
	for range items {
		page, err := Page(items, PageMine, 1, cursor)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Asks) != 1 {
			t.Fatalf("page %+v", page)
		}
		got = append(got, page.Asks[0].ID)
		cursor = page.NextCursor
		if page.Complete {
			break
		}
	}
	want := []string{"ask-0000000000000003", "ask-0000000000000002", "ask-0000000000000001"}
	if strings.Join(got, ",") != strings.Join(want, ",") || cursor != "" {
		t.Fatalf("order %v cursor %q", got, cursor)
	}
	if _, err := Page(items, PageMine, 2, encodeCursor(PageOpen, 2, items[0])); !errors.Is(err, apiv1.ErrAskInvalid) {
		t.Fatal("cursor from another list was accepted", err)
	}
	if _, err := Page(items, PageMine, 1, "%%%"); !errors.Is(err, apiv1.ErrAskInvalid) {
		t.Fatal("malformed cursor was accepted", err)
	}
}

func TestEightyPercent(t *testing.T) {
	if atEighty(12, 16) || !atEighty(13, 16) || !atEighty(16, 16) || atEighty(0, 16) {
		t.Fatal("80 percent threshold")
	}
}
