package main

import "testing"

func TestEqualTimePassEvidenceFailsClosed(t *testing.T) {
	for _, stem := range []string{"writes", "preserves", "executors", "offset"} {
		for _, suffix := range []string{"", "-reversed"} {
			name := "pass-conflict-" + stem + suffix + ".json"
			got := mustRun(t, name, 3)
			if got.WritesRecommended || got.MergeRelease != "blocked" || !got.ManualDisposition || !has(got.Conflicts, "equal-time conflicting worker passes") || countPrefix(got.Recommendations, "recommend existing normal") != 0 {
				t.Fatalf("%s %#v", name, got)
			}
			if len(got.Passes) != 0 {
				t.Fatalf("%s selected a pass from conflicting instant: %#v", name, got.Passes)
			}
			if stem == "executors" && got.Executor.Identity != "" {
				t.Fatalf("%s selected an initial executor %s", name, got.Executor.Identity)
			}
		}
	}
}

func TestEqualTimeIdenticalPassAdviceAndReplay(t *testing.T) {
	for _, name := range []string{"pass-identical.json", "pass-identical-reversed.json"} {
		got := mustRun(t, name, 0)
		if !got.WritesRecommended || len(got.Passes) != 2 || got.IgnoredReplayCount != 1 || len(got.Conflicts) != 0 || got.Executor.Identity != "executor-a" {
			t.Fatalf("%s %#v", name, got)
		}
	}
	// Later, distinct instants retain normal revocation/allow behavior.
	for _, name := range []string{"followup-pass-revokes.json", "followup-pass-revokes-reversed.json"} {
		if mustRun(t, name, 0).WritesRecommended {
			t.Fatalf("%s did not revoke writes", name)
		}
	}
	if !mustRun(t, "followup-pass-allows.json", 0).WritesRecommended {
		t.Fatal("later matching pass did not enable writes")
	}
}

func TestMismatchedFalsePassCannotRevokeMatchingTrue(t *testing.T) {
	got := mustRun(t, "pass-mismatch-false.json", 0)
	if !got.WritesRecommended || len(got.Passes) != 1 || got.Executor.Identity != "executor-a" || !has(got.Refusals, "worker pass executor mismatch pass-2") || got.MergeRelease != "blocked" {
		t.Fatalf("mismatched false pass changed advice %#v", got)
	}
}

func TestPassPreservationRequiresEarlierInstant(t *testing.T) {
	for _, name := range []string{"pass-self-preserve.json", "pass-future-preserve.json"} {
		got := mustRun(t, name, 3)
		if got.WritesRecommended || got.MergeRelease != "blocked" || !has(got.Conflicts, "preservation requires a strictly earlier pass") {
			t.Fatalf("%s %#v", name, got)
		}
	}
}
