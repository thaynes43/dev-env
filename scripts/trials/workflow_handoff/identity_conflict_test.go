package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestNonbindingIdentityConflictsHaveNoFirstWinner(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "e-merge.json"))
	if err != nil {
		t.Fatal(err)
	}
	var canonical map[string]any
	if err := json.Unmarshal(raw, &canonical); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"successor-assignment", "review-observation", "scope-instruction", "hold", "hold-disposition", "gate-evidence", "delivery-receipt", "acknowledgment", "lease-expiry", "reconnect", "taskclaim-mutation"} {
		var first result
		for _, reverse := range []bool{false, true} {
			doc := cloneMap(canonical)
			events := []any{}
			var selected map[string]any
			for _, rawEvent := range canonical["events"].([]any) {
				event := rawEvent.(map[string]any)
				if event["kind"] == kind {
					selected = cloneMap(event)
				} else {
					events = append(events, event)
				}
			}
			a := map[string]any{"kind": kind, "identity": "nonbinding-collision", "sourceTime": "2026-10-23T14:00:00Z"}
			if selected != nil {
				a = selected
				a["identity"] = "nonbinding-collision"
			}
			switch kind {
			case "successor-assignment":
				a = bindingSuccessor()
				a["identity"] = "nonbinding-collision"
				events = append(events, bindingPass(), map[string]any{"kind": "stop-release", "identity": "valid-stop", "sourceTime": "2026-10-23T13:05:00Z", "executor": "executor-a", "verified": true, "ambiguous": false})
			case "hold-disposition":
				a["action"], a["permitsReconsideration"], a["source"] = "merge", true, dispositionSource
				events = append(events, map[string]any{"kind": "hold", "identity": "retained-hold", "sourceTime": "2026-10-23T13:00:00Z", "action": "merge", "state": "pending", "reason": "retain pending"})
			case "acknowledgment":
				a["delivery"] = "receipt-a"
			case "lease-expiry", "reconnect":
				a["executor"] = "executor-a"
			case "taskclaim-mutation":
				a["note"] = "first note"
			}
			b := cloneMap(a)
			switch kind {
			case "successor-assignment":
				b["successor"] = "executor-c"
			case "review-observation":
				b["verdict"] = "rejected"
			case "scope-instruction":
				b["summary"] = "conflicting summary"
			case "hold":
				b["reason"] = "conflicting hold reason"
			case "hold-disposition":
				b["permitsReconsideration"] = false
			case "gate-evidence":
				b["requiredCI"] = "failed"
			case "delivery-receipt":
				b["sourceTime"] = "2026-10-23T14:01:00Z"
			case "acknowledgment":
				b["delivery"] = "receipt-b"
			case "lease-expiry", "reconnect":
				b["executor"] = "executor-b"
			case "taskclaim-mutation":
				b["note"] = "second note"
			}
			pair := []any{a, b}
			if reverse {
				pair[0], pair[1] = pair[1], pair[0]
			}
			doc["events"] = append(events, pair...)
			body, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			got, code := check(body)
			if reverse {
				assertSameSemantics(t, first, got)
			} else {
				first = got
			}
			if code != 3 || !got.ManualDisposition || !has(got.Conflicts, "dropped ambiguous record kind "+kind+" identity nonbinding-collision") || countPrefix(got.Recommendations, "recommend existing normal") != 0 {
				t.Fatalf("%s reverse=%v %#v", kind, reverse, got)
			}
			switch kind {
			case "successor-assignment":
				if got.ActiveSuccessor != "" || got.RecordedSuccessorCount != 0 || countPrefix(got.Recommendations, "recorded exactly one active successor") != 0 {
					t.Fatalf("assignment winner %#v", got)
				}
			case "review-observation":
				if len(got.Reviews) != 0 || countPrefix(got.Recommendations, "picked review verdict") != 0 {
					t.Fatalf("review winner %#v", got)
				}
			case "scope-instruction":
				if got.CurrentScope != nil || len(got.RetainedScope) != 0 {
					t.Fatal("conflicting scope was retained as a winner")
				}
			case "hold":
				if len(got.Holds) != 0 {
					t.Fatal("conflicting hold was retained as a winner")
				}
			case "hold-disposition":
				if holdState(got, "merge") != "pending" {
					t.Fatal("conflicting disposition cleared a hold")
				}
			case "delivery-receipt":
				if len(got.DeliveryReceipts) != 0 {
					t.Fatal("conflicting receipt was applied")
				}
			case "acknowledgment":
				if len(got.AcknowledgedDeliveries) != 0 {
					t.Fatal("conflicting acknowledgment was applied")
				}
			}
			// Exact repetition of the same nonbinding event remains nonconflicting.
			doc["events"] = append(events, a, cloneMap(a))
			body, err = json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			positive, positiveCode := check(body)
			if positiveCode != 0 || positive.IgnoredReplayCount != 1 || len(positive.Conflicts) != 0 {
				t.Fatalf("%s exact replay %#v", kind, positive)
			}
			doc["events"] = append(events, a)
			singleBody, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			single, _ := check(singleBody)
			assertSameSemantics(t, single, positive)
		}
	}
}
