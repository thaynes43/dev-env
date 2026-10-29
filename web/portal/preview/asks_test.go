package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAsksPreviewsAreSyntheticAndAnswersDisabled(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "asks")
	if err := writeAsks(dir); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ name, want string }{{"asks.html", "Approval scope"}, {"asks-empty.html", "No open asks."}, {"asks-disconnected.html", "Asks are not connected"}, {"asks-unavailable.html", "Asks are unavailable"}, {"asks-partial.html", "partial read"}} {
		raw, err := os.ReadFile(filepath.Join(dir, test.name))
		if err != nil {
			t.Fatal(err)
		}
		body := string(raw)
		if !strings.Contains(body, test.want) || !strings.Contains(body, "Fixture preview · synthetic data") || strings.Contains(body, "<form") || strings.Contains(body, "<script") || strings.Contains(body, `name="csrf"`) {
			t.Fatalf("unsafe/incomplete preview %s", test.name)
		}
		if test.name == "asks.html" && (!strings.Contains(body, `<fieldset disabled`) || !strings.Contains(body, "Recommended") || !strings.Contains(body, "Record decision")) {
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
