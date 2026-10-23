package main

import (
	"os"
	"strings"
	"testing"
)

func TestExistingFoundationFixtureRendersOffline(t *testing.T) {
	f, err := os.Open("../../../scripts/trials/portal_attention/fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			t.Error(err)
		}
	}()
	body, err := render(f)
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	for _, title := range []string{"Needs you · 1", "Needs worker · 1", "Review returned · 1", "Handoff missing · 1", "Source preview", "Stale evidence", "Freshness unknown", "2026-10-23T14:18:10Z"} {
		if !strings.Contains(s, title) {
			t.Errorf("missing fixture presentation %s", title)
		}
	}
	if strings.Contains(s, "Listing counts") {
		t.Fatal("synthetic listing evidence missing")
	}
	if strings.Count(s, "<article ") != 4 || strings.Count(s, "href=") != 4 {
		t.Fatal("fixture rows or fixed source links missing")
	}
	for _, active := range []string{"<script", "<iframe", "<form", "<img"} {
		if strings.Contains(s, active) {
			t.Fatalf("unexpected active content %s", active)
		}
	}
	// A second independent render from the same accepted fixture must match.
	if _, err = f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	again, err := render(f)
	if err != nil || string(again) != s {
		t.Fatal("preview is not repeatable")
	}
}

func TestInvalidFixtureWritesNoPartialPage(t *testing.T) {
	for _, raw := range []string{"", "{", `{"version":1,"asOf":"2026-10-23T16:00:00Z","readState":"verified","items":[],"prompt":"SECRET"}`, `{"version":2}`, strings.Repeat("x", 65537)} {
		body, err := render(strings.NewReader(raw))
		if err == nil || len(body) != 0 {
			t.Fatal("invalid fixture returned partial HTML")
		}
	}
}
