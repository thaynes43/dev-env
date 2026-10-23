package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnicodeSchemaKeys(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "e-merge.json"))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ name, from, to string }{
		{"literal-long-s", `"findings": "disposed"`, `"findings":"pending","findingſ":"disposed"`},
		{"escaped-long-s", `"findings": "disposed"`, `"findings":"pending","finding\u017f":"disposed"`},
		{"events", `"events": [`, `"eventſ": [ ] , "events": [`},
		{"kelvin", `"kind": "gate-evidence"`, `"\u212Aind": "gate-evidence"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !bytes.Contains(raw, []byte(tc.from)) {
				t.Fatal("missing mutation target")
			}
			got, code := check(bytes.Replace(raw, []byte(tc.from), []byte(tc.to), 1))
			if code != 2 || !has(got.Refusals, "case-variant JSON key") {
				t.Fatalf("exit %d refusals %v", code, got.Refusals)
			}
		})
	}
	disposition := `{"kind":"hold-disposition","identity":"disp-1","sourceTime":"2026-10-23T17:00:00Z","action":"merge","source":"externally-established-trusted-review","permitsReconsideration":false,"permitsReconſideration":true}`
	mutated := bytes.Replace(raw, []byte(`"events": [`), []byte(`"events": [`+disposition+`,`), 1)
	got, code := check(mutated)
	if code != 2 || !has(got.Refusals, "case-variant JSON key") {
		t.Fatalf("disposition exit %d refusals %v", code, got.Refusals)
	}
	positive := bytes.Replace(raw, []byte("no media action is assigned"), []byte("literal ſ remains a string value"), 1)
	got, code = check(positive)
	if code != 0 || got.Outcome != "recommend-normal-merge-release" || got.Holds[0].Reason != "literal ſ remains a string value" {
		t.Fatalf("string value changed: %d %#v", code, got)
	}
	got, code = check(bytes.Replace(raw, []byte(`"findings": "disposed"`), []byte(`"findings":"disposed","unknownExactKey":true`), 1))
	if code != 2 || !has(got.Refusals, `unknown field "unknownExactKey"`) {
		t.Fatalf("unknown field exit %d %v", code, got.Refusals)
	}
	mustRun(t, "e-merge.json", 0)
}

func TestRootRequiresEOF(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "e-merge.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"}", "]", "{}", "null"} {
		body := append(append([]byte{}, raw...), suffix...)
		got, code := check(body)
		if code != 2 || !has(got.Refusals, "trailing JSON") {
			t.Fatalf("suffix %q exit %d %v", suffix, code, got.Refusals)
		}
		// Exercise the typed parser independently of the token guard.
		if _, err := decode[document](body); err == nil || !strings.Contains(err.Error(), "trailing JSON") {
			t.Fatalf("typed parser accepted suffix %q: %v", suffix, err)
		}
	}
	body := append(append([]byte{}, raw...), []byte(" \n\t\r")...)
	if _, code := check(body); code != 0 {
		t.Fatalf("whitespace exit %d", code)
	}
	if _, err := decode[document](body); err != nil {
		t.Fatalf("typed parser refused whitespace: %v", err)
	}
}

func TestWritableExecutorBlocksMerge(t *testing.T) {
	got := mustRun(t, "repair-writable-merge.json", 0)
	if !got.WritesRecommended || got.MergeRelease != "blocked" || got.Outcome != "merge-blocked" || !has(got.MergeBlockers, "current executor writes remain recommended") || countPrefix(got.Recommendations, "recommend existing normal") != 0 {
		t.Fatalf("writable merge %#v", got)
	}
	stopped := mustRun(t, "repair-stopped-merge.json", 0)
	if stopped.WritesRecommended || !stopped.Executor.VerifiedStop || stopped.Outcome != "recommend-normal-merge-release" {
		t.Fatalf("stopped merge %#v", stopped)
	}
}

func TestRecordedDeadlineNeedsExplicitEvent(t *testing.T) {
	got := mustRun(t, "repair-writable-merge.json", 0)
	// Deadline is 14:00; the gate observation is 16:00. The reducer has no clock.
	if len(got.Passes) != 1 || got.Passes[0].Deadline != "2026-10-23T14:00:00Z" || !got.WritesRecommended || got.Executor.TurnEnded {
		t.Fatalf("deadline field unexpectedly enforced %#v", got)
	}
	ended := mustRun(t, "d-deadline.json", 0)
	if ended.WritesRecommended || !ended.Executor.TurnEnded {
		t.Fatalf("recorded deadline event ignored %#v", ended)
	}
}

func TestEqualTimeHoldDispositionFailsClosed(t *testing.T) {
	for _, name := range []string{"repair-hold-first.json", "repair-disposition-first.json"} {
		got := mustRun(t, name, 0)
		if holdState(got, "merge") != "pending" || got.MergeRelease != "blocked" || countPrefix(got.Recommendations, "recommend existing normal") != 0 {
			t.Fatalf("%s %#v", name, got)
		}
	}
	later := mustRun(t, "repair-later-disposition.json", 0)
	if holdState(later, "merge") != "reconsidered" || later.Outcome != "recommend-normal-merge-release" {
		t.Fatalf("later disposition %#v", later)
	}
}

func TestMutuallySupersedingScopeFailsClosed(t *testing.T) {
	for _, name := range []string{"repair-mutual-scopes.json", "repair-mutual-scopes-reversed.json"} {
		got := mustRun(t, name, 3)
		if got.CurrentScope != nil || len(got.RetainedScope) != 2 || !got.RetainedScope[0].Superseded || !got.RetainedScope[1].Superseded || !has(got.Conflicts, "no current scope") || got.MergeRelease != "blocked" || countPrefix(got.Recommendations, "recommend existing normal") != 0 || holdState(got, "media-work") != "pending" {
			t.Fatalf("%s %#v", name, got)
		}
	}
}

func TestReplayTimestampRepresentationIsExact(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "e-merge.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	events := doc["events"].([]any)
	replay := cloneMap(events[0].(map[string]any))
	replay["sourceTime"] = "2026-10-23T11:00:00-04:00"
	doc["events"] = append(events, replay)
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	got, code := check(body)
	if code != 3 || !has(got.Conflicts, "identity obs-approved reused with a different payload") || got.MergeRelease != "blocked" {
		t.Fatalf("timestamp replay exit %d %#v", code, got)
	}
}

func TestVerifiedStopDisplayAfterDeadline(t *testing.T) {
	for _, name := range []string{"repair-deadline-then-stop.json", "repair-stop-then-deadline.json"} {
		got := mustRun(t, name, 0)
		if !got.Executor.VerifiedStop || got.Executor.State != "exited" || !got.Executor.TurnEnded || got.WritesRecommended || got.Outcome == "executor-ended-pending-verified-stop" || strings.Contains(got.NextAction, "pending verified stop") {
			t.Fatalf("%s %#v", name, got)
		}
	}
}
