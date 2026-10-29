package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProductStatePreviews(t *testing.T) {
	dir := t.TempDir()
	if err := writeProductStates(dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"usage", "usage-timed-out", "sessions-not-connected", "sessions-credential-unavailable", "sessions-unreachable", "sessions-timed-out", "sessions-refused", "sessions-contract-refused", "sessions-clock-ahead", "sessions-cancelled", "sessions-empty", "sessions-stale", "sessions-current"} {
		body, err := os.ReadFile(filepath.Join(dir, name+".html"))
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		if !strings.Contains(text, "Fixture preview") || strings.Contains(text, "<script") {
			t.Errorf("%s lacks fixture label or contains script", name)
		}
		if strings.HasPrefix(name, "usage") && (strings.Contains(text, `class="rail"`) || strings.Contains(text, `class="list"`) || strings.Count(text, `?view=usage" aria-current="page"`) != 2) {
			t.Errorf("%s has the session browser or wrong current nav", name)
		}
		if strings.Contains(name, "timed-out") && (!strings.Contains(text, "timed out") || !strings.Contains(text, ">Retry</a>")) {
			t.Errorf("%s lacks timeout recovery", name)
		}
		if strings.Contains(name, "contract-refused") && !strings.Contains(text, "Page 2, row index 1") {
			t.Error("contract preview lost refusal position")
		}
	}
}
