package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// Compare action-bearing state, not diagnostic order or replay counters.
func handoffSemantics(got result) any {
	holds, reviews, passes, scopes := []string{}, []string{}, []string{}, []string{}
	for _, hold := range got.Holds {
		holds = append(holds, hold.Identity+"|"+hold.Action+"|"+hold.State)
	}
	for _, review := range got.Reviews {
		body, _ := json.Marshal([]any{review.Task, review.UID, review.Head, review.Verdict, review.Picked})
		reviews = append(reviews, string(body))
	}
	for _, pass := range got.Passes {
		passes = append(passes, pass.Identity+"|"+pass.Executor)
	}
	for _, scope := range got.RetainedScope {
		body, _ := json.Marshal([]any{scope.Identity, scope.Scope, scope.Superseded})
		scopes = append(scopes, string(body))
	}
	for _, list := range [][]string{holds, reviews, passes, scopes} {
		sort.Strings(list)
	}
	current := ""
	if got.CurrentScope != nil {
		current = got.CurrentScope.Identity
	}
	return struct {
		Exit, Successors, MergeAdvice, SuccessorAdvice, ReviewAdvice int
		Outcome, Merge, Executor, Successor, Scope                   string
		Writes, Verified, Ambiguous, Ended, Blocked, Manual          bool
		Holds, Reviews, Passes, Scopes                               []string
	}{got.ExitCode, got.RecordedSuccessorCount,
		countPrefix(got.Recommendations, "recommend existing normal"),
		countPrefix(got.Recommendations, "recorded exactly one active successor"),
		countPrefix(got.Recommendations, "picked review verdict"),
		got.Outcome, got.MergeRelease, got.Executor.Identity, got.ActiveSuccessor, current,
		got.WritesRecommended, got.Executor.VerifiedStop, got.Executor.StopAmbiguous,
		got.Executor.TurnEnded, got.BlockedCustody, got.ManualDisposition,
		holds, reviews, passes, scopes}
}

func assertSameSemantics(t *testing.T, want, got result) {
	t.Helper()
	if !reflect.DeepEqual(handoffSemantics(want), handoffSemantics(got)) {
		t.Fatalf("semantic result changed with equivalent evidence:\nwant %#v\ngot  %#v", handoffSemantics(want), handoffSemantics(got))
	}
}

func TestFiniteHandoffPermutationAndReplayInvariants(t *testing.T) {
	// Bounded representative permutations: reverse, rotate and adjacent swaps.
	// Include valid/later-time flows and conflict classes, not just one repro.
	for _, name := range []string{"e-merge.json", "b-one-successor.json", "followup-pass-revokes.json", "followup-pass-allows.json", "pass-conflict-offset.json", "followup-scope-cycle.json", "a-same-identity-conflict.json"} {
		raw, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		var doc document
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		want, _ := check(raw)
		original := append([]json.RawMessage{}, doc.Events...)
		variants := [][]json.RawMessage{append([]json.RawMessage{}, original...)}
		reverse := append([]json.RawMessage{}, original...)
		for i, j := 0, len(reverse)-1; i < j; i, j = i+1, j-1 {
			reverse[i], reverse[j] = reverse[j], reverse[i]
		}
		variants = append(variants, reverse)
		if len(original) > 1 {
			variants = append(variants, append(append([]json.RawMessage{}, original[1:]...), original[0]))
			for i := 0; i+1 < len(original); i++ {
				next := append([]json.RawMessage{}, original...)
				next[i], next[i+1] = next[i+1], next[i]
				variants = append(variants, next)
			}
		}
		for _, events := range variants {
			for _, replay := range []bool{false, true} {
				doc.Events = append([]json.RawMessage{}, events...)
				if replay {
					doc.Events = append(doc.Events, events...)
				}
				body, err := json.Marshal(doc)
				if err != nil {
					t.Fatal(err)
				}
				got, _ := check(body)
				assertSameSemantics(t, want, got)
			}
		}
	}
}
