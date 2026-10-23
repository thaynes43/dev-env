package main

import "testing"

func custodyStop(identity, at string, verified, ambiguous bool) map[string]any {
	return map[string]any{"kind": "stop-release", "identity": identity, "sourceTime": at, "executor": "executor-a", "verified": verified, "ambiguous": ambiguous}
}

func TestAddedCustodyAmbiguityCannotPromoteSuccessor(t *testing.T) {
	for _, scenario := range []string{"collided-stop", "cross-kind-stop", "collided-assignment", "equal-time-assignment"} {
		for _, reverse := range []bool{false, true} {
			for _, replay := range []bool{false, true} {
				assignment := bindingSuccessor()
				base := []map[string]any{bindingPass(), custodyStop("earlier-verified", "2026-10-23T13:05:00Z", true, false)}
				var added []map[string]any
				switch scenario {
				case "collided-stop":
					ambiguous := custodyStop("later-stop", "2026-10-23T13:08:00Z", false, true)
					base = append(base, ambiguous, assignment)
					added = []map[string]any{custodyStop("later-stop", "2026-10-23T13:08:00Z", true, false)}
				case "cross-kind-stop":
					base = append(base, custodyStop("later-stop", "2026-10-23T13:08:00Z", false, true), assignment)
					added = []map[string]any{{"kind": "executor-state", "identity": "other-executor", "sourceTime": "2026-10-23T09:08:00-04:00", "executor": "executor-b", "state": "running"}}
				default:
					later := cloneMap(assignment)
					later["identity"], later["assignmentKey"], later["successor"], later["sourceTime"] = "later-key", "key-2", "executor-c", "2026-10-23T13:15:00Z"
					base = append(base, assignment, later)
					conflict := cloneMap(assignment)
					conflict["successor"] = "executor-d"
					if scenario == "equal-time-assignment" {
						conflict["identity"] = "other-assignment"
					}
					added = []map[string]any{conflict}
				}
				baseline := bindingFixture(t, base...)
				switch scenario {
				case "collided-stop", "cross-kind-stop":
					if baseline.ActiveSuccessor != "" || baseline.RecordedSuccessorCount != 0 || !baseline.BlockedCustody || !baseline.Executor.StopAmbiguous {
						t.Fatalf("%s baseline did not retain blocked ambiguous stop", scenario)
					}
				default:
					if baseline.ActiveSuccessor != "executor-b" || baseline.RecordedSuccessorCount != 1 || !baseline.BlockedCustody {
						t.Fatalf("%s baseline did not retain original K1 successor", scenario)
					}
				}
				events := append(append([]map[string]any{}, base...), added...)
				if reverse {
					for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
						events[i], events[j] = events[j], events[i]
					}
				}
				if replay {
					events = append(events, append([]map[string]any{}, events...)...)
				}
				got := bindingFixture(t, events...)
				if got.ActiveSuccessor != "" && got.ActiveSuccessor != baseline.ActiveSuccessor {
					t.Errorf("added ambiguity promoted %s from baseline %s (%s)", got.ActiveSuccessor, baseline.ActiveSuccessor, scenario)
				}
				if got.ExitCode != 3 || !got.BlockedCustody || got.ActiveSuccessor != "" || got.RecordedSuccessorCount != 0 || got.WritesRecommended || got.MergeRelease != "blocked" || len(got.Recommendations) != 0 {
					t.Errorf("%s reverse=%v replay=%v successor=%s count=%d blocked=%v reason=%s", scenario, reverse, replay, got.ActiveSuccessor, got.RecordedSuccessorCount, got.BlockedCustody, got.BlockedReason)
				}
			}
		}
	}
}

func TestSuccessorStrictlyBeforeCustodyAmbiguityRemainsObservation(t *testing.T) {
	assignment := bindingSuccessor()
	assignment["sourceTime"] = "2026-10-23T13:07:00Z"
	events := []map[string]any{bindingPass(), custodyStop("verified", "2026-10-23T13:05:00Z", true, false), assignment,
		custodyStop("conflicted", "2026-10-23T13:08:00Z", true, false), custodyStop("conflicted", "2026-10-23T13:08:00Z", false, true)}
	for _, reverse := range []bool{false, true} {
		if reverse {
			for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
				events[i], events[j] = events[j], events[i]
			}
		}
		got := bindingFixture(t, events...)
		if got.ExitCode != 3 || got.ActiveSuccessor != "executor-b" || got.RecordedSuccessorCount != 1 || got.WritesRecommended || len(got.Recommendations) != 0 {
			t.Fatalf("earlier successor observation was erased or advised %#v", got)
		}
	}
}
