package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

func preservedSession() sessionRecord {
	return sessionRecord{
		Session: "example-session", Phase: "Suspended", Execution: "stopped", Hold: "clear", Reap: "clear",
		NativeThread: "clear", PendingWork: "clear", Activity: "clear",
		Retirement: "verified", Checkpoint: "verified", BranchAndHead: "verified",
		UnpushedCommits: "verified", UncommittedChanges: "verified", Successor: "acknowledged",
		Retention: "verified",
	}
}

func TestClassification(t *testing.T) {
	for _, phase := range []string{"Suspended", "Failed"} {
		for _, successor := range []string{"acknowledged", "not-required"} {
			record := preservedSession()
			record.Phase = phase
			record.Successor = successor
			got := classify(record)
			if got.Class != "retire candidate" || got.Decision != "review" {
				t.Fatalf("fully verified %s candidate: %+v", phase, got)
			}
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
		{"checkpoint-unconfirmed", func(r *sessionRecord) { r.Checkpoint = "local-only" }},
		{"branch-head-unconfirmed", func(r *sessionRecord) { r.BranchAndHead = "branch-only" }},
		{"unpushed-commits-unconfirmed", func(r *sessionRecord) { r.UnpushedCommits = "" }},
		{"uncommitted-changes-unconfirmed", func(r *sessionRecord) { r.UncommittedChanges = "rescue-listed" }},
		{"retention-unconfirmed", func(r *sessionRecord) { r.Retention = "expired-by-age" }},
		{"checkpoint-unconfirmed", func(r *sessionRecord) { r.Checkpoint = "Verified" }},
		{"execution-unconfirmed", func(r *sessionRecord) { r.Phase = "failed" }},
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

func TestUnconfirmedCutoverLeavesInactiveSessionAbandoned(t *testing.T) {
	for _, test := range []struct {
		reason string
		change func(*sessionRecord)
	}{
		{"retirement-unconfirmed", func(r *sessionRecord) { r.Retirement = "" }},
		{"successor-unconfirmed", func(r *sessionRecord) { r.Successor = "sent" }},
	} {
		t.Run(test.reason, func(t *testing.T) {
			record := preservedSession()
			test.change(&record)
			got := classify(record)
			if got.Class != "abandoned" || got.Decision != "keep" || !slices.Contains(got.Reasons, test.reason) {
				t.Fatalf("%+v", got)
			}
		})
	}
}

func TestThreadPendingWorkAndActivityAlwaysProtectCustody(t *testing.T) {
	for _, test := range []struct {
		name, active string
		set          func(*sessionRecord, string)
	}{
		{"native-thread", "live", func(r *sessionRecord, value string) { r.NativeThread = value }},
		{"pending-work", "present", func(r *sessionRecord, value string) { r.PendingWork = value }},
		{"activity", "live", func(r *sessionRecord, value string) { r.Activity = value }},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, value := range []string{test.active, "", "unknown", "Clear"} {
				record := preservedSession()
				test.set(&record, value)
				got := classify(record)
				reason := test.name + "-unconfirmed"
				if value == test.active {
					reason = test.name + "-observed"
				}
				if got.Class != "active" || got.Decision != "keep" || !slices.Contains(got.Reasons, reason) {
					t.Fatalf("%q: %+v", value, got)
				}
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
		`[] []`, `{}`, `[{"session":"a","Retirement":"verified"}]`, `[{"session":"a","hold":true}]`,
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

func TestInventoryInputLimits(t *testing.T) {
	for _, test := range []struct {
		name, input, errorText string
	}{
		{"bytes", "[]" + strings.Repeat(" ", maxInput-1), "inventory input unreadable or over 2 MiB"},
		{"records", "[" + strings.Repeat(`{"session":"a"},`, maxRecords) + `{"session":"b"}]`, "expected at most 2000 valid sanitized session records"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var out bytes.Buffer
			err := run(strings.NewReader(test.input), &out)
			if err == nil || err.Error() != test.errorText || out.Len() != 0 {
				t.Fatalf("unexpected result: output length %d, error %v", out.Len(), err)
			}
		})
	}
}

func TestEmptyInventory(t *testing.T) {
	var out bytes.Buffer
	if err := run(strings.NewReader("[]"), &out); err != nil || out.String() != "[]\n" {
		t.Fatalf("empty inventory: output %q, error %v", &out, err)
	}
}

func TestCommandRejectsExtraArguments(t *testing.T) {
	const child = "DEV_ENV_RETIRE_ARG_TEST_CHILD"
	if os.Getenv(child) == "1" {
		os.Args = []string{"session-retire-dry-run", "unexpected"}
		main()
		return
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "-test.run=^TestCommandRejectsExtraArguments$")
	cmd.Env = []string{child + "=1"}
	cmd.Stdin = strings.NewReader("[]")
	raw, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 2 ||
		string(raw) != "usage: session-retire-dry-run < sanitized-records.json\n" {
		t.Fatalf("extra arguments: output %q, error %v", raw, err)
	}
}
