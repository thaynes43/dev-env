package main

import (
	"bytes"
	"slices"
	"strings"
	"testing"
)

func preservedSession() sessionRecord {
	return sessionRecord{
		Session: "example-session", Phase: "Suspended", Execution: "stopped", Hold: "clear", Reap: "clear",
		Retirement: "verified", Checkpoint: "verified", BranchAndHead: "verified",
		UnpushedCommits: "verified", UncommittedChanges: "verified", Successor: "acknowledged",
		Retention: "verified",
	}
}

func TestClassification(t *testing.T) {
	for _, successor := range []string{"acknowledged", "not-required"} {
		record := preservedSession()
		record.Successor = successor
		got := classify(record)
		if got.Class != "retire candidate" || got.Decision != "review" {
			t.Fatalf("fully verified candidate: %+v", got)
		}
	}
	for _, test := range []struct {
		reason string
		clear  func(*sessionRecord)
	}{
		{"execution-unconfirmed", func(r *sessionRecord) { r.Execution = "" }},
		{"execution-unconfirmed", func(r *sessionRecord) { r.Phase = "Unknown" }},
		{"execution-unconfirmed", func(r *sessionRecord) { r.Phase = "Archived" }},
		{"hold-unconfirmed", func(r *sessionRecord) { r.Hold = "unknown" }},
		{"reap-unconfirmed", func(r *sessionRecord) { r.Reap = "pending" }},
		{"reap-unconfirmed", func(r *sessionRecord) { r.Reap = "" }},
		{"retirement-unconfirmed", func(r *sessionRecord) { r.Retirement = "" }},
		{"checkpoint-unconfirmed", func(r *sessionRecord) { r.Checkpoint = "local-only" }},
		{"branch-head-unconfirmed", func(r *sessionRecord) { r.BranchAndHead = "branch-only" }},
		{"unpushed-commits-unconfirmed", func(r *sessionRecord) { r.UnpushedCommits = "" }},
		{"uncommitted-changes-unconfirmed", func(r *sessionRecord) { r.UncommittedChanges = "rescue-listed" }},
		{"successor-unconfirmed", func(r *sessionRecord) { r.Successor = "sent" }},
		{"retention-unconfirmed", func(r *sessionRecord) { r.Retention = "expired-by-age" }},
	} {
		t.Run(test.reason, func(t *testing.T) {
			record := preservedSession()
			test.clear(&record)
			got := classify(record)
			if got.Class != "blocked" || got.Decision != "keep" || !slices.Contains(got.Reasons, test.reason) {
				t.Fatalf("%+v", got)
			}
		})
	}
}

func TestLiveOrHeldSessionAlwaysStays(t *testing.T) {
	for _, record := range []sessionRecord{
		{Phase: "Running"},
		{Phase: "Idle"},
		{Phase: "Pending"},
		{Phase: "Draining"},
		{Phase: "Suspended", Execution: "live", Reap: "pending"},
		{Phase: "Failed", Hold: "present"},
	} {
		got := classify(record)
		if got.Class != "active" || got.Decision != "keep" {
			t.Fatalf("%+v", got)
		}
	}
}

func TestInventoryInputFailsClosed(t *testing.T) {
	for _, input := range []string{
		`null`, `[{"session":"."}]`, `[{"session":"a"},{"session":"a"}]`,
		`[{"session":"a","reap":"pending","reap":"clear"}]`,
		`[{"session":"a","approvalText":"unknown field"}]`,
		`[] []`,
	} {
		var out bytes.Buffer
		if err := run(strings.NewReader(input), &out); err == nil || out.Len() != 0 {
			t.Fatalf("%s was accepted", input)
		}
	}
	var out bytes.Buffer
	if err := run(strings.NewReader(`[{"session":"a","phase":"Suspended"}]`), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"decision":"keep"`) {
		t.Fatalf("%s", &out)
	}
}
