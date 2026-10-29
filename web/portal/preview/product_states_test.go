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
	for _, name := range []string{"usage", "usage-empty", "usage-timed-out", "sessions-not-connected", "sessions-credential-unavailable", "sessions-unreachable", "sessions-timed-out", "sessions-refused", "sessions-contract-refused", "sessions-clock-ahead", "sessions-cancelled", "sessions-empty", "sessions-stale", "sessions-current"} {
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
		if strings.HasPrefix(name, "usage") && (strings.Count(text, "<h1") != 1 || !strings.Contains(text, `<h1 id="usage-title">Usage</h1>`)) {
			t.Errorf("%s lacks its single Usage h1", name)
		}
		if strings.Contains(text, ">Retry</a>") && strings.Count(text, ">Retry</a>") != 1 {
			t.Errorf("%s repeats its failure recovery block", name)
		}
		if name == "sessions-timed-out" && !strings.Contains(text, `<form class="launch"`) {
			t.Error("timeout preview lost the independent New agent form")
		}
		if name == "usage" && !strings.Contains(text, `?session=uid-synthetic-agent">synthetic-agent</a>`) {
			t.Error("Usage preview does not select its session by UID")
		}
		if name == "usage-empty" && (strings.Count(text, "No sessions in this read.") != 1 || strings.Count(text, "<dd>0</dd>") != 2 || strings.Contains(text, "<table>")) {
			t.Error("empty Usage preview lost zeros or has empty tables")
		}
		if strings.Contains(name, "timed-out") && (!strings.Contains(text, "timed out") || !strings.Contains(text, ">Retry</a>")) {
			t.Errorf("%s lacks timeout recovery", name)
		}
		if strings.Contains(name, "contract-refused") && !strings.Contains(text, "Page 2, row index 1") {
			t.Error("contract preview lost refusal position")
		}
	}
}
