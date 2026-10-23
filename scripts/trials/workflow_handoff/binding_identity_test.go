package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestEveryBindingIdentityConflictSuppressesAllCopies(t *testing.T) {
	variants := []struct{ kind, difference string }{
		{"executor-state", "state"}, {"executor-state", "executor"},
		{"deadline-reached", "executor"}, {"stop-release", "ambiguity"},
		{"stop-release", "executor"}, {"worker-pass", "unauthorized"},
	}
	for _, variant := range variants {
		var baseline result
		haveBaseline := false
		for _, spelling := range []string{"same", "offset", "later"} {
			for _, reverse := range []bool{false, true} {
				a := map[string]any{"kind": variant.kind, "identity": "duplicate-binding", "sourceTime": "2026-10-23T13:00:00Z", "executor": "executor-a"}
				switch variant.kind {
				case "executor-state":
					a["state"] = "running"
				case "stop-release":
					a["verified"], a["ambiguous"] = true, false
				case "worker-pass":
					a = bindingPass()
					a["identity"], a["authorized"] = "duplicate-binding", false
				}
				b := cloneMap(a)
				switch variant.difference {
				case "state":
					b["state"] = "exited"
				case "ambiguity":
					b["verified"], b["ambiguous"] = false, true
				default:
					b["executor"] = "executor-b"
				}
				switch spelling {
				case "offset":
					b["sourceTime"] = "2026-10-23T09:00:00-04:00"
				case "later":
					b["sourceTime"] = "2026-10-23T13:01:00Z"
				}
				events := []map[string]any{bindingPass(), a, b, bindingSuccessor()}
				if reverse {
					events[1], events[2] = events[2], events[1]
				}
				got := bindingFixture(t, events...)
				assertBindingConflict(t, got)
				if haveBaseline {
					assertSameSemantics(t, baseline, got)
				} else {
					baseline, haveBaseline = got, true
				}
				if got.ActiveSuccessor != "" || got.RecordedSuccessorCount != 0 || !has(got.Conflicts, "dropped ambiguous record kind "+variant.kind+" identity duplicate-binding") {
					t.Fatalf("%s %s reverse=%v %#v", variant.kind, spelling, reverse, got)
				}
			}
		}
	}
	for _, kind := range []string{"executor-state", "deadline-reached", "stop-release"} {
		event := map[string]any{"kind": kind, "identity": "exact-binding", "sourceTime": "2026-10-23T13:00:00Z", "executor": "executor-a"}
		switch kind {
		case "executor-state":
			event["state"] = "running"
		case "stop-release":
			event["verified"], event["ambiguous"] = true, false
		}
		got := bindingFixture(t, bindingPass(), event, cloneMap(event))
		if got.ExitCode != 0 || got.IgnoredReplayCount != 1 || got.Executor.Identity != "executor-a" {
			t.Fatalf("exact %s replay %#v", kind, got)
		}
	}
}

func bindingSuccessor() map[string]any {
	return map[string]any{"kind": "successor-assignment", "identity": "assignment-after-binding", "sourceTime": "2026-10-23T13:10:00Z", "assignmentKey": "assignment-1", "predecessor": "executor-a", "successor": "executor-b", "authorized": true, "authorizationRef": "tracker:177", "supportedContinuation": true}
}

func TestPreservationConflictPrecedesTiedStop(t *testing.T) {
	for _, future := range []bool{false, true} {
		for _, reverse := range []bool{false, true} {
			pass := bindingPass()
			ref := "pass-binding"
			if future {
				ref = "future-pass"
			}
			pass["preserves"] = []string{ref}
			stop := map[string]any{"kind": "stop-release", "identity": "tied-stop", "sourceTime": pass["sourceTime"], "executor": "executor-a", "verified": true, "ambiguous": false}
			events := []map[string]any{pass, stop, bindingSuccessor()}
			if reverse {
				events[0], events[1] = events[1], events[0]
			}
			if future {
				later := bindingPass()
				later["identity"], later["sourceTime"] = ref, "2026-10-23T13:02:00Z"
				events = append(events, later)
			}
			got := bindingFixture(t, events...)
			if got.ExitCode != 3 || got.WritesRecommended || got.Executor.VerifiedStop || got.ActiveSuccessor != "" || got.RecordedSuccessorCount != 0 || got.MergeRelease != "blocked" || countPrefix(got.Recommendations, "recorded exactly one active successor") != 0 || !has(got.Conflicts, "preservation requires a strictly earlier pass") {
				t.Fatalf("future=%v reverse=%v %#v", future, reverse, got)
			}
		}
	}
}

func TestDroppedHoldOutputRequiresManualDisposition(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "e-merge.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	hold := map[string]any{"kind": "hold", "identity": "pass-binding", "sourceTime": "2026-10-23T13:00:00Z", "action": "merge", "state": "pending", "reason": "collision is not a disposition"}
	doc["events"] = append(doc["events"].([]any), hold, bindingPass())
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	got, code := check(body)
	if code != 3 || got.WritesRecommended || got.MergeRelease != "blocked" || !got.ManualDisposition || !has(got.Conflicts, "dropped ambiguous record kind hold identity pass-binding") || !has(got.Conflicts, "dropped ambiguous record kind worker-pass identity pass-binding") || holdState(got, "merge") != "" || holdState(got, "media-work") != "pending" || len(got.Holds) != 1 {
		t.Fatalf("ambiguous hold output failed to retain manual disposition %#v", got)
	}
}
