package main

import (
	"bytes"
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
	ordered := mustRun(t, "e-gate-stale-order.json", 0)
	if ordered.Outcome != "merge-blocked" || ordered.MergeRelease != "blocked" || !has(ordered.MergeBlockers, "required CI is missing") || countPrefix(ordered.Recommendations, "recommend existing normal") != 0 || len(ordered.Conflicts) != 0 {
		t.Fatalf("stale gate overwrote newer evidence %#v %s", ordered.MergeBlockers, ordered.NextAction)
	}
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
