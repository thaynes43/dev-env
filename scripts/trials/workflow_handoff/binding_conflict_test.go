package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func bindingFixture(t *testing.T, events ...map[string]any) result {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "e-merge.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		doc["events"] = append(doc["events"].([]any), event)
	}
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := check(body)
	if got.TaskClaim != "unchanged" || got.CheckerAuthority != "none" || got.Completion {
		t.Fatal("fixture changed authority invariants")
	}
	return got
}

func bindingPass() map[string]any {
	return map[string]any{"kind": "worker-pass", "identity": "pass-binding", "sourceTime": "2026-10-23T13:00:00Z", "executor": "executor-a", "deadline": "2026-10-23T14:00:00Z", "budget": "original", "authorized": true, "authorizationRef": "tracker:177", "preserves": []string{}, "writesAllowed": true}
}

func assertBindingConflict(t *testing.T, got result) {
	t.Helper()
	if got.ExitCode != 3 || !got.ManualDisposition || got.WritesRecommended || got.MergeRelease != "blocked" || got.Executor.Identity != "" || countPrefix(got.Recommendations, "recommend existing normal") != 0 || len(got.Passes) != 0 {
		t.Fatalf("input chose conflicting binding %#v", got)
	}
}

func TestCrossKindEqualTimeExecutorBindings(t *testing.T) {
	for _, kind := range []string{"executor-state", "deadline-reached", "stop-release"} {
		for _, reverse := range []bool{false, true} {
			for _, offset := range []bool{false, true} {
				pass := bindingPass()
				event := map[string]any{"kind": kind, "identity": "other-binding", "sourceTime": pass["sourceTime"], "executor": "executor-b"}
				if offset {
					event["sourceTime"] = "2026-10-23T09:00:00-04:00"
				}
				if kind == "executor-state" {
					event["state"] = "running"
				}
				if kind == "stop-release" {
					event["verified"], event["ambiguous"] = true, false
				}
				events := []map[string]any{pass, event}
				if reverse {
					events[0], events[1] = events[1], events[0]
				}
				got := bindingFixture(t, events...)
				assertBindingConflict(t, got)
				if !has(got.Conflicts, "equal-time conflicting executor bindings") {
					t.Fatalf("%s reverse=%v offset=%v %v", kind, reverse, offset, got.Conflicts)
				}
				// The same executor is nonconflicting in either order/offset.
				event["executor"] = "executor-a"
				positive := bindingFixture(t, events...)
				if positive.ExitCode != 0 || positive.Executor.Identity != "executor-a" || len(positive.Passes) != 1 {
					t.Fatalf("same executor %s %#v", kind, positive)
				}
				if kind == "executor-state" {
					if !positive.WritesRecommended || positive.MergeRelease != "blocked" {
						t.Fatalf("same-executor running advice %#v", positive)
					}
				} else if positive.WritesRecommended || positive.Outcome != "recommend-normal-merge-release" {
					t.Fatalf("same-executor ended advice %#v", positive)
				}
			}
		}
	}
}

func TestConflictingPassIdentityDropsEveryCopy(t *testing.T) {
	for _, variant := range []string{"writes", "executor", "time", "kind"} {
		for _, reverse := range []bool{false, true} {
			a := bindingPass()
			b := cloneMap(a)
			switch variant {
			case "writes":
				b["writesAllowed"] = false
			case "executor":
				b["executor"] = "executor-b"
			case "time":
				b["sourceTime"], b["writesAllowed"] = "2026-10-23T13:01:00Z", false
			case "kind":
				b = map[string]any{"kind": "stop-release", "identity": a["identity"], "sourceTime": "2026-10-23T13:01:00Z", "executor": "executor-b", "verified": true, "ambiguous": false}
			}
			events := []map[string]any{a, b}
			if reverse {
				events[0], events[1] = events[1], events[0]
			}
			got := bindingFixture(t, events...)
			assertBindingConflict(t, got)
			if !has(got.Conflicts, "identity pass-binding reused with a different payload") {
				t.Fatalf("%s reverse=%v %v", variant, reverse, got.Conflicts)
			}
			// Each copy's instant must also suppress otherwise valid binding events.
			for _, source := range []any{a["sourceTime"], b["sourceTime"]} {
				extra := map[string]any{"kind": "executor-state", "identity": "extra-state", "sourceTime": source, "executor": "executor-c", "state": "exited"}
				assertBindingConflict(t, bindingFixture(t, append(events, extra)...))
			}
		}
	}
	pass := bindingPass()
	positive := bindingFixture(t, pass, cloneMap(pass))
	if positive.ExitCode != 0 || !positive.WritesRecommended || positive.IgnoredReplayCount != 1 || len(positive.Passes) != 1 {
		t.Fatalf("exact replay %#v", positive)
	}
}

func TestPreservesTemporalChecksPrecedeUnknownChecks(t *testing.T) {
	for _, refs := range [][]string{{"unknown", "future"}, {"future", "unknown"}} {
		pass, future := bindingPass(), bindingPass()
		pass["preserves"] = refs
		future["identity"], future["sourceTime"] = "future", "2026-10-23T13:02:00Z"
		got := bindingFixture(t, pass, future)
		if got.ExitCode != 3 || got.WritesRecommended || got.MergeRelease != "blocked" || !has(got.Conflicts, "preservation requires a strictly earlier pass") {
			t.Fatalf("refs %v %#v", refs, got)
		}
	}
	pass := bindingPass()
	pass["preserves"] = []string{"unknown"}
	got := bindingFixture(t, pass)
	if got.ExitCode != 0 || got.WritesRecommended || len(got.Passes) != 0 || !has(got.Refusals, "does not preserve unknown pass") {
		t.Fatalf("unknown only %#v", got)
	}
	if mustRun(t, "followup-pass-revokes.json", 0).WritesRecommended || !mustRun(t, "followup-pass-allows.json", 0).WritesRecommended {
		t.Fatal("later distinct pass behavior changed")
	}
}
