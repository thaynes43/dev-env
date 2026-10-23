package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestFixtures(t *testing.T) {
	terminal := mustRun(t, "a-terminal-once.json", 0)
	if terminal.Outcome != "pending-coordinator" {
		t.Fatalf("terminal outcome %s", terminal.Outcome)
	}
	if len(terminal.Reviews) != 1 || !terminal.Reviews[0].Picked || terminal.Reviews[0].Verdict != "approved" || terminal.Reviews[0].Observation != "obs-new" {
		t.Fatalf("terminal review %#v", terminal.Reviews)
	}
	if !has(terminal.Refusals, "stale terminal observation obs-old") || !has(terminal.Refusals, "stale running observation obs-run") {
		t.Fatalf("terminal refusals %#v", terminal.Refusals)
	}
	if countPrefix(terminal.Recommendations, "picked review verdict") != 1 || terminal.IgnoredReplayCount < 1 {
		t.Fatalf("terminal recommendations %#v replays %d", terminal.Recommendations, terminal.IgnoredReplayCount)
	}

	wrongHead := mustRun(t, "a-wrong-head.json", 0)
	if len(wrongHead.Reviews) != 0 || !has(wrongHead.Refusals, "wrong-head observation obs-wrong") {
		t.Fatalf("wrong head %#v", wrongHead.Refusals)
	}
	wrongUID := mustRun(t, "a-wrong-uid.json", 0)
	if len(wrongUID.Reviews) != 0 || !has(wrongUID.Refusals, "wrong-uid observation obs-wrong") {
		t.Fatalf("wrong uid %#v", wrongUID.Refusals)
	}

	same := mustRun(t, "a-same-identity-conflict.json", 3)
	if same.Status != "conflict" || !has(same.Conflicts, "identity obs-terminal reused with a different payload") {
		t.Fatalf("same identity %#v", same.Conflicts)
	}
	equal := mustRun(t, "a-equal-time-conflict.json", 3)
	if !equal.ManualDisposition || len(equal.Reviews) != 1 || equal.Reviews[0].Picked || !has(equal.Conflicts, "equal-time conflicting review verdicts") {
		t.Fatalf("equal review %#v picked %#v", equal.Conflicts, equal.Reviews)
	}

	one := mustRun(t, "b-one-successor.json", 0)
	if one.ActiveSuccessor != "executor-b" || one.RecordedSuccessorCount != 1 || one.BlockedCustody || !one.Executor.VerifiedStop || one.TaskClaim != "unchanged" {
		t.Fatalf("one successor %#v", one)
	}
	dup := mustRun(t, "b-duplicate-replay.json", 0)
	if dup.ActiveSuccessor != "executor-b" || dup.RecordedSuccessorCount != 1 || dup.BlockedCustody || dup.IgnoredReplayCount < 1 {
		t.Fatalf("duplicate successor count %d blocked %v replays %d", dup.RecordedSuccessorCount, dup.BlockedCustody, dup.IgnoredReplayCount)
	}
	if countPrefix(dup.Recommendations, "recorded exactly one active successor") != 1 || !has(dup.Refusals, "duplicate successor") || !has(dup.Refusals, "lease-expiry") || !has(dup.Refusals, "reconnect") {
		t.Fatalf("duplicate recommendations %#v refusals %#v", dup.Recommendations, dup.Refusals)
	}

	active := mustRun(t, "b-active-predecessor.json", 0)
	if active.ActiveSuccessor != "" || active.Outcome != "blocked-custody" || !strings.Contains(active.BlockedReason, "still running") {
		t.Fatalf("active %#v", active.BlockedReason)
	}
	amb := mustRun(t, "b-ambiguous-release.json", 0)
	if amb.ActiveSuccessor != "" || !strings.Contains(amb.BlockedReason, "ambiguous") || amb.Executor.VerifiedStop {
		t.Fatalf("ambiguous %#v verified %v", amb.BlockedReason, amb.Executor.VerifiedStop)
	}
	changed := mustRun(t, "b-changed-assignment.json", 0)
	if changed.ActiveSuccessor != "executor-b" || changed.RecordedSuccessorCount != 1 || !strings.Contains(changed.BlockedReason, "changed same-key") {
		t.Fatalf("changed %#v %s", changed.ActiveSuccessor, changed.BlockedReason)
	}
	none := mustRun(t, "b-no-continuation.json", 0)
	if none.ActiveSuccessor != "" || !strings.Contains(none.BlockedReason, "headless continuation") {
		t.Fatalf("continuation %s", none.BlockedReason)
	}
	exited := mustRun(t, "b-exit-without-release.json", 0)
	if exited.ActiveSuccessor != "" || !strings.Contains(exited.BlockedReason, "executor exit does not release") || exited.TaskClaim != "unchanged" {
		t.Fatalf("exit release %s", exited.BlockedReason)
	}
	unauth := mustRun(t, "b-not-authorized.json", 0)
	if unauth.ActiveSuccessor != "" || !strings.Contains(unauth.BlockedReason, "not externally authorized") {
		t.Fatalf("unauthorized %s", unauth.BlockedReason)
	}
	eqAssign := mustRun(t, "b-equal-time-assignment.json", 3)
	if eqAssign.ActiveSuccessor != "" || eqAssign.RecordedSuccessorCount != 0 || !has(eqAssign.Conflicts, "equal-time conflicting successor") {
		t.Fatalf("equal assignment %#v", eqAssign.Conflicts)
	}

	scope := mustRun(t, "c-scope-holds.json", 0)
	if scope.CurrentScope == nil || scope.CurrentScope.Identity != "scope-merge" || scope.CurrentScope.Verbatim != ownerInstruction() {
		t.Fatalf("current scope %#v", scope.CurrentScope)
	}
	if len(scope.RetainedScope) != 2 || !scope.RetainedScope[0].Superseded || scope.RetainedScope[0].Identity != "scope-old" {
		t.Fatalf("retained %#v", scope.RetainedScope)
	}
	if len(scope.Holds) != 1 || scope.Holds[0].State != "pending" || scope.Holds[0].Action != "media-work" {
		t.Fatalf("holds %#v", scope.Holds)
	}

	recon := mustRun(t, "c-reconsider-specific.json", 0)
	if holdState(recon, "media-work") != "reconsidered" || holdState(recon, "deployment") != "pending" {
		t.Fatalf("reconsider %#v", recon.Holds)
	}
	if recon.CurrentScope == nil || !strings.Contains(recon.CurrentScope.Verbatim, "do not need permission") || !has(recon.Refusals, "matches no pending hold") {
		t.Fatalf("standing scope did not stay with the unrelated hold %#v", recon.Refusals)
	}

	deadline := mustRun(t, "d-deadline.json", 0)
	if deadline.Outcome != "executor-ended-pending-verified-stop" || deadline.Executor.State != "ended-pending-verified-stop" || deadline.Executor.VerifiedStop || deadline.WritesRecommended {
		t.Fatalf("deadline %#v writes %v", deadline.Executor, deadline.WritesRecommended)
	}
	if len(deadline.DeliveryReceipts) != 1 || len(deadline.AcknowledgedDeliveries) != 0 || deadline.Completion || deadline.ActiveSuccessor != "" {
		t.Fatalf("delivery was treated as ack or completion %#v %#v", deadline.DeliveryReceipts, deadline.AcknowledgedDeliveries)
	}
	if !strings.Contains(deadline.NextAction, "owns unresolved review, repair, CI and outcome") || !strings.Contains(deadline.NextAction, "no further write") {
		t.Fatalf("deadline next %s", deadline.NextAction)
	}

	ack := mustRun(t, "d-ack-not-completion.json", 0)
	if len(ack.AcknowledgedDeliveries) != 1 || ack.AcknowledgedDeliveries[0] != "del-1" || ack.Completion || ack.ActiveSuccessor != "" {
		t.Fatalf("ack %#v completion %v", ack.AcknowledgedDeliveries, ack.Completion)
	}

	passes := mustRun(t, "d-distinct-pass.json", 0)
	if len(passes.Passes) != 2 || passes.Passes[0].Budget != "original" || passes.Passes[1].Identity != "pass-2" || passes.Passes[1].Budget != "pass-2" || passes.WritesRecommended {
		t.Fatalf("passes %#v writes %v", passes.Passes, passes.WritesRecommended)
	}
	if passes.Outcome != "executor-ended-pending-verified-stop" {
		t.Fatalf("distinct pass outcome %s", passes.Outcome)
	}

	merge := mustRun(t, "e-merge.json", 0)
	if merge.Outcome != "recommend-normal-merge-release" || merge.MergeRelease != "recommend-normal-workflow" || countPrefix(merge.Recommendations, "recommend existing normal") != 1 {
		t.Fatalf("merge %#v %s", merge.Recommendations, merge.NextAction)
	}
	if merge.ModelProvenance.Serving != "Unknown" || merge.ModelProvenance.Cost != "Unknown" || merge.ModelProvenance.ConfiguredUI != "Grok4.7ExtraHigh" {
		t.Fatalf("provenance %#v", merge.ModelProvenance)
	}
	if holdState(merge, "media-work") != "pending" || merge.Deployment != "not-performed" || merge.Publication != "distinct-from-deployment" {
		t.Fatalf("merge hold or publication %#v", merge.Holds)
	}

	job := mustRun(t, "e-job-success.json", 0)
	if job.Outcome != "merge-blocked" || !has(job.MergeBlockers, "review job success is not a native review verdict") || countPrefix(job.Recommendations, "recommend existing normal") != 0 {
		t.Fatalf("job success %#v", job.MergeBlockers)
	}
	held := mustRun(t, "e-merge-hold.json", 0)
	if held.Outcome != "merge-blocked" || !has(held.MergeBlockers, "hold-merge") || !has(held.MergeBlockers, "merge-release") {
		t.Fatalf("merge hold %#v", held.MergeBlockers)
	}
	missing := mustRun(t, "e-missing-ci.json", 0)
	if missing.Outcome != "merge-blocked" || !has(missing.MergeBlockers, "required CI is missing") {
		t.Fatalf("missing ci %#v", missing.MergeBlockers)
	}

	claim := mustRun(t, "taskclaim-unchanged.json", 0)
	if claim.TaskClaim != "unchanged" || !has(claim.Refusals, "TaskClaim unchanged") {
		t.Fatalf("claim %#v", claim.Refusals)
	}
	refused := mustRun(t, "malformed.json", 2)
	if refused.Status != "refused" {
		t.Fatalf("malformed %s", refused.Status)
	}
	unknown := mustRun(t, "unknown-field.json", 2)
	if unknown.Status != "refused" {
		t.Fatalf("unknown %s", unknown.Status)
	}
}

func TestReplayIsStable(t *testing.T) {
	path := filepath.Join("testdata", "b-duplicate-replay.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	first, code1 := check(data)
	second, code2 := check(data)
	if code1 != code2 || !reflect.DeepEqual(first, second) {
		t.Fatal("replay changed the result")
	}
	if !unique(first.Recommendations) {
		t.Fatalf("duplicate recommendations %#v", first.Recommendations)
	}
}

func TestStdinAndUsage(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "e-merge.json"))
	if err != nil {
		t.Fatal(err)
	}
	var out, errw bytes.Buffer
	if code := run([]string{"-"}, bytes.NewReader(data), &out, &errw); code != 0 {
		t.Fatalf("stdin %d %s", code, errw.String())
	}
	if !strings.Contains(out.String(), "recommend-normal-merge-release") {
		t.Fatalf("stdin output %s", out.String())
	}
	out.Reset()
	if code := run(nil, bytes.NewReader(nil), &out, &errw); code != 2 {
		t.Fatalf("usage %d", code)
	}
}

func TestBoundsAndProvenance(t *testing.T) {
	over, code := check(bytes.Repeat([]byte("x"), maxDocumentBytes+1))
	if code != 2 || !has(over.Refusals, "128KiB") {
		t.Fatalf("oversize %d %#v", code, over.Refusals)
	}
	dup, code := check([]byte(`{"schema":"dev-env.workflow-handoff/v1","schema":"other"}`))
	if code != 2 || !has(dup.Refusals, "duplicate JSON key") {
		t.Fatalf("duplicate key %d %#v", code, dup.Refusals)
	}
	raw, err := os.ReadFile(filepath.Join("testdata", "e-merge.json"))
	if err != nil {
		t.Fatal(err)
	}
	inferred := bytes.Replace(raw, []byte(`"serving": "Unknown"`), []byte(`"serving": "Grok4.7ExtraHigh"`), 1)
	got, code := check(inferred)
	if code != 2 || !has(got.Refusals, "model serving must be Unknown or Reported") {
		t.Fatalf("inferred serving %d %#v", code, got.Refusals)
	}
}

func TestSourceDoesNotImportRuntimeClients(t *testing.T) {
	for _, name := range []string{"main.go", "reduce.go"} {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(body), "\n") {
			trim := strings.TrimSpace(line)
			if !strings.HasPrefix(trim, `"`) {
				continue
			}
			for _, banned := range []string{`"net"`, `"net/`, `"os/exec"`, `"plugin"`} {
				if strings.Contains(trim, banned) {
					t.Fatalf("%s imports %s", name, trim)
				}
			}
		}
	}
}

func TestGateReplay(t *testing.T) {
	replayed := mustRun(t, "e-gate-same-identity.json", 0)
	if replayed.IgnoredReplayCount < 1 || len(replayed.Conflicts) != 0 || replayed.Outcome != "recommend-normal-merge-release" {
		t.Fatalf("same-identity gate replay %#v replays %d", replayed.Conflicts, replayed.IgnoredReplayCount)
	}
	identical := mustRun(t, "e-gate-equal-time-identical.json", 0)
	if identical.ManualDisposition || len(identical.Conflicts) != 0 || identical.MergeRelease != "recommend-normal-workflow" || countPrefix(identical.Recommendations, "recommend existing normal") != 1 {
		t.Fatalf("identical equal-time gates conflicted %#v %s", identical.Conflicts, identical.NextAction)
	}
	differ := mustRun(t, "e-gate-equal-time-differ.json", 3)
	if !differ.ManualDisposition || differ.MergeRelease == "recommend-normal-workflow" || countPrefix(differ.Recommendations, "recommend existing normal") != 0 || !has(differ.Conflicts, "equal-time conflicting gate evidence") {
		t.Fatalf("different equal-time gates %#v %s", differ.Conflicts, differ.MergeRelease)
	}
	// Chronological sorting: the later gate wins even when the file lists it first.
	// This does not claim direct coverage of the defensive earlier-than-stored branch.
	ordered := mustRun(t, "e-gate-stale-order.json", 0)
	if ordered.Outcome != "merge-blocked" || ordered.MergeRelease != "blocked" || !has(ordered.MergeBlockers, "required CI is missing") || countPrefix(ordered.Recommendations, "recommend existing normal") != 0 || len(ordered.Conflicts) != 0 {
		t.Fatalf("stale gate overwrote newer evidence %#v %s", ordered.MergeBlockers, ordered.NextAction)
	}
}

func TestRepair02(t *testing.T) {
	merge := mustRun(t, "e-merge.json", 0)
	if merge.Outcome != "recommend-normal-merge-release" || merge.PullRequest <= 0 || len(merge.Reviews) != 1 || !merge.Reviews[0].Picked || merge.Reviews[0].Verdict != "approved" {
		t.Fatalf("armed merge %#v", merge.Reviews)
	}
	cases := []struct {
		file    string
		code    int
		blocker string
	}{
		{"f1-no-binding.json", 0, "review binding is required"},
		{"f1-no-observation.json", 0, "no picked bound review"},
		{"f1-running-only.json", 0, "no picked bound review"},
		{"f1-negative.json", 0, "picked review verdict is rejected"},
		{"f1-unknown.json", 0, "picked review verdict is unavailable"},
		{"f1-unrelated.json", 0, "no picked bound review"},
		{"f1-wrong-head.json", 0, "no picked bound review"},
		{"f1-pr-zero.json", 0, "pull request number must be positive"},
	}
	for _, tc := range cases {
		got := mustRun(t, tc.file, tc.code)
		if got.Outcome == "recommend-normal-merge-release" || !has(got.MergeBlockers, tc.blocker) {
			t.Fatalf("%s blockers %#v outcome %s", tc.file, got.MergeBlockers, got.Outcome)
		}
	}
	for _, name := range []string{"f2-verified-then-ambiguous.json", "f2-ambiguous-then-verified.json"} {
		got := mustRun(t, name, 3)
		if got.Executor.VerifiedStop || !got.Executor.StopAmbiguous || got.ActiveSuccessor != "" || !has(got.Conflicts, "equal-time ambiguous and verified stop") {
			t.Fatalf("%s stop %#v successor %q", name, got.Executor, got.ActiveSuccessor)
		}
	}
	earlier := mustRun(t, "f2-earlier-ambiguous.json", 0)
	if !earlier.Executor.VerifiedStop || earlier.Executor.StopAmbiguous || earlier.ActiveSuccessor == "" {
		t.Fatalf("earlier ambiguous %#v %s", earlier.Executor, earlier.ActiveSuccessor)
	}
	for _, name := range []string{"m2-review-z-first.json", "m2-review-offset-first.json"} {
		got := mustRun(t, name, 3)
		if !has(got.Conflicts, "equal-time conflicting review") || (len(got.Reviews) > 0 && got.Reviews[0].Picked) {
			t.Fatalf("%s review %#v", name, got.Reviews)
		}
	}
	latest := mustRun(t, "m2-latest-instant.json", 0)
	if len(latest.Reviews) != 1 || !latest.Reviews[0].Picked || latest.Reviews[0].Verdict != "approved" || latest.Reviews[0].Observation != "obs-late" {
		t.Fatalf("latest instant %#v", latest.Reviews)
	}
	for _, name := range []string{"m2-assign-z-first.json", "m2-assign-offset-first.json"} {
		got := mustRun(t, name, 3)
		if got.ActiveSuccessor != "" || !has(got.Conflicts, "equal-time conflicting successor") {
			t.Fatalf("%s assign %q %#v", name, got.ActiveSuccessor, got.Conflicts)
		}
	}
	for _, name := range []string{"m1-verdict-case.json", "m1-identity-case.json", "m3-hold-merge-case.json", "m3-hold-pr-merge.json", "m3-hold-publish.json", "m3-hold-unknown.json"} {
		got := mustRun(t, name, 2)
		if name[:2] == "m1" && !has(got.Refusals, "case-variant JSON key") {
			t.Fatalf("%s %#v", name, got.Refusals)
		}
		if name[:2] == "m3" && !has(got.Refusals, "hold action is not in the vocabulary") {
			t.Fatalf("%s %#v", name, got.Refusals)
		}
	}
	for _, name := range []string{"m4-exit-no-writes.json", "m4-stop-no-writes.json", "m4-new-pass-no-writes.json"} {
		got := mustRun(t, name, 0)
		if got.WritesRecommended || got.Completion || got.TaskClaim != "unchanged" {
			t.Fatalf("%s writes %v", name, got.WritesRecommended)
		}
	}
	if len(mustRun(t, "m4-new-pass-no-writes.json", 0).Passes) != 2 {
		t.Fatal("new pass was not retained")
	}
	diffKey := mustRun(t, "m5-different-key.json", 0)
	if !strings.Contains(diffKey.BlockedReason, "different assignment key") || strings.Contains(diffKey.BlockedReason, "changed same-key") || diffKey.RecordedSuccessorCount != 1 {
		t.Fatalf("different key %s", diffKey.BlockedReason)
	}
	sticky := mustRun(t, "m8-later-approval.json", 3)
	if len(sticky.Reviews) == 0 || sticky.Reviews[0].Picked || countPrefix(sticky.Recommendations, "recommend existing normal") != 0 {
		t.Fatalf("sticky %#v", sticky.Reviews)
	}
}

func TestEachGateFieldConflicts(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "e-merge.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	events := doc["events"].([]any)
	var gate map[string]any
	for _, ev := range events {
		item := ev.(map[string]any)
		if item["kind"] == "gate-evidence" {
			gate = item
		}
	}
	if gate == nil {
		t.Fatal("missing gate")
	}
	same := cloneMap(gate)
	same["identity"] = "gate-same-fields"
	if code := checkGateVariant(t, doc, events, same); code != 0 {
		t.Fatalf("identical gate fields conflicted with exit %d", code)
	}
	flips := []struct {
		field string
		value any
	}{
		{"head", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		{"agentOpened", false},
		{"requiredCI", "failed"},
		{"nativeReviewVerdict", "rejected"},
		{"reviewJobSuccess", false},
		{"findings", "pending"},
		{"headUnchanged", false},
	}
	for _, flip := range flips {
		next := cloneMap(gate)
		next["identity"] = "gate-flip-" + flip.field
		next[flip.field] = flip.value
		if code := checkGateVariant(t, doc, events, next); code != 3 {
			t.Fatalf("field %s was ignored, exit %d", flip.field, code)
		}
	}
}

func checkGateVariant(t *testing.T, doc map[string]any, events []any, gate map[string]any) int {
	t.Helper()
	cloned := map[string]any{}
	for k, v := range doc {
		cloned[k] = v
	}
	nextEvents := append([]any{}, events...)
	nextEvents = append(nextEvents, gate)
	cloned["events"] = nextEvents
	body, err := json.Marshal(cloned)
	if err != nil {
		t.Fatal(err)
	}
	_, code := check(body)
	return code
}

func cloneMap(in map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range in {
		out[k] = v
	}
	return out
}

func mustRun(t *testing.T, name string, code int) result {
	t.Helper()
	path := filepath.Join("testdata", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	before := append([]byte(nil), data...)
	got, gotCode := check(data)
	if !bytes.Equal(data, before) {
		t.Fatalf("%s mutated the input buffer", name)
	}
	disk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(disk, before) {
		t.Fatalf("%s mutated the fixture file", name)
	}
	again, againCode := check(before)
	if gotCode != againCode || !reflect.DeepEqual(got, again) {
		t.Fatalf("%s is not replay-stable", name)
	}
	if gotCode != code || got.ExitCode != code {
		t.Fatalf("%s exit %d/%d status %s next %s conflicts %#v refusals %#v", name, gotCode, got.ExitCode, got.Status, got.NextAction, got.Conflicts, got.Refusals)
	}
	if got.TaskClaim != "unchanged" || got.Completion || got.CheckerAuthority != "none" || got.Deployment != "not-performed" || got.Publication != "distinct-from-deployment" {
		t.Fatalf("%s broke an invariant %#v", name, got)
	}
	if !unique(got.Recommendations) {
		t.Fatalf("%s duplicate recommendations %#v", name, got.Recommendations)
	}
	return got
}

func has(items []string, needle string) bool {
	for _, item := range items {
		if strings.Contains(item, needle) {
			return true
		}
	}
	return false
}

func countPrefix(items []string, prefix string) int {
	n := 0
	for _, item := range items {
		if strings.HasPrefix(item, prefix) {
			n++
		}
	}
	return n
}

func unique(items []string) bool {
	seen := map[string]bool{}
	for _, item := range items {
		if seen[item] {
			return false
		}
		seen[item] = true
	}
	return true
}

func holdState(res result, action string) string {
	for _, hold := range res.Holds {
		if hold.Action == action {
			return hold.State
		}
	}
	return ""
}

func ownerInstruction() string {
	return "I have told you over and over and over and over that agents do not need permission to merge PRs they opened"
}
