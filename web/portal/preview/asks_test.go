package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func TestAsksPreviewsAreSyntheticAndAnswersDisabled(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "asks")
	if err := writeAsks(dir); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ name, want string }{{"asks.html", "Approval scope"}, {"asks-empty.html", "No open asks."}, {"asks-disconnected.html", "Asks are not connected"}, {"asks-unavailable.html", "Asks are unavailable"}, {"asks-partial.html", "partial read"}, {"asks-history-page.html", "Older asks"}, {"asks-selected.html", "Selected ask"}, {"asks-validation.html", "Write an answer"}} {
		raw, err := os.ReadFile(filepath.Join(dir, test.name))
		if err != nil {
			t.Fatal(err)
		}
		body := string(raw)
		if !strings.Contains(body, test.want) || !strings.Contains(body, "Fixture preview · synthetic data") || strings.Contains(body, "<form") || strings.Contains(body, "<script") || strings.Contains(body, `name="csrf"`) {
			t.Fatalf("unsafe/incomplete preview %s", test.name)
		}
		if test.name == "asks.html" && (!strings.Contains(body, `<fieldset disabled`) || !strings.Contains(body, "Recommended") || !strings.Contains(body, `name="decision" value="deny">Deny</button>`)) {
			t.Fatal("answer controls missing from preview")
		}
		if test.name == "asks-partial.html" && strings.Contains(body, `aria-label="2 open asks"`) {
			t.Fatal("partial read claimed a full count")
		}
	}
	if err := writeAsks(dir); err == nil {
		t.Fatal("existing preview overwritten")
	}
}

func TestAsksPreviewRecordsRespectFinalWireCaps(t *testing.T) {
	now := time.Date(2026, 10, 29, 20, 0, 0, 0, time.UTC)
	for _, ask := range asksPreviewRecords(now) {
		if err := apiv1.ValidateAsk(ask); err != nil {
			t.Fatal(err)
		}
		if ask.CreatedAt.Nanosecond() != 0 || ask.ExpiresAt.Nanosecond() != 0 {
			t.Fatal("fixture must use whole seconds")
		}
		if ask.Scope != nil && (ask.Scope.ExpiresAt.After(ask.CreatedAt.Add(apiv1.AskScopeMaxTTL)) || ask.ExpiresAt.After(ask.Scope.ExpiresAt) || ask.Scope.ExpiresAt.Nanosecond() != 0) {
			t.Fatal("fixture exceeds approval caps")
		}
	}
}
