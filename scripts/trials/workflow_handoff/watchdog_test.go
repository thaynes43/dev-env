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

func TestWatchdogFixtureMatrix(t *testing.T) {
	cases := []struct {
		file    string
		code    int
		status  string
		intents int
		stage   string
		kind    string
		owner   string
		state   string
		reason  string
	}{
		{"watchdog-missed-start.json", 0, "planned", 1, "owner_wake", "start_ack", "codex-native", "current", ""},
		{"watchdog-pickup.json", 0, "planned", 1, "owner_wake", "pickup", "codex-native", "current", ""},
		{"watchdog-progress.json", 0, "planned", 1, "owner_wake", "progress", "codex-native", "current", ""},
		{"watchdog-user-question.json", 0, "planned", 1, "owner_wake", "user_question", "codex-native", "current", ""},
		{"watchdog-claude-rc.json", 0, "planned", 1, "owner_wake", "start_ack", "claude-rc", "current", ""},
		{"watchdog-late-ack.json", 0, "planned", 1, "user_escalation", "start_ack", "codex-native", "current", ""},
		{"watchdog-wait.json", 0, "waiting", 0, "", "start_ack", "codex-native", "waiting", ""},
		{"watchdog-wait-expired.json", 0, "planned", 1, "owner_wake", "start_ack", "codex-native", "current", ""},
		{"watchdog-missing-due.json", 0, "unknown", 0, "", "start_ack", "codex-native", "unknown", ""},
		{"watchdog-resolved.json", 0, "resolved", 0, "", "start_ack", "codex-native", "resolved", ""},
		{"watchdog-hold.json", 0, "blocked", 0, "", "", "", "", "unresolved hold"},
		{"watchdog-custody.json", 0, "blocked", 0, "", "", "", "", "still running"},
		{"watchdog-owner-mismatch.json", 0, "blocked", 0, "", "start_ack", "claude-rc", "blocked", "does not match"},
		{"watchdog-duplicate.json", 0, "current", 0, "", "start_ack", "codex-native", "current", ""},
		{"watchdog-conflict-dup.json", 0, "blocked", 0, "", "", "", "", "equal-time conflicting receipt"},
		{"watchdog-offset-tie.json", 0, "current", 0, "", "start_ack", "codex-native", "current", ""},
		{"watchdog-cross-kind.json", 2, "refused", 0, "", "", "", "", "obligation id"},
		{"watchdog-ambiguous.json", 0, "current", 0, "", "start_ack", "codex-native", "current", ""},
		{"watchdog-accepted.json", 0, "planned", 1, "user_escalation", "start_ack", "codex-native", "current", ""},
		{"watchdog-pre-pr.json", 0, "blocked", 0, "", "", "", "", "pull request is not positive"},
		{"watchdog-malformed.json", 2, "refused", 0, "", "", "", "", ""},
		{"watchdog-wrong-binding.json", 2, "refused", 0, "", "", "", "", "receipt obligation"},
		{"watchdog-delivered.json", 2, "refused", 0, "", "", "", "", "receipt fields"},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			plan := planFile(t, tc.file, tc.code)
			if plan.Status != tc.status || len(plan.Intents) != tc.intents || len(plan.NextReceipts) != tc.intents {
				t.Fatalf("status %s intents %d next %d reasons %#v", plan.Status, len(plan.Intents), len(plan.NextReceipts), plan.Reasons)
			}
			if plan.Schema != watchdogSchema || !plan.DryRun || plan.Executed || plan.CheckerAuthority != "none" || plan.OverallDone || plan.HandoffCompletion {
				t.Fatalf("authority %#v", plan)
			}
			if plan.Publication != "distinct-from-deployment" || plan.Deployment != "not-performed" {
				t.Fatalf("publication %s deployment %s", plan.Publication, plan.Deployment)
			}
			if tc.reason != "" && !has(plan.Reasons, tc.reason) {
				t.Fatalf("reasons %#v", plan.Reasons)
			}
			if tc.intents == 1 {
				intent := plan.Intents[0]
				if intent.Stage != tc.stage || intent.Kind != tc.kind || intent.Owner != tc.owner || intent.Task != "watchdog-211" || intent.UID != "uid-watchdog-211" || intent.PullRequest != 211 || intent.Head != strings.Repeat("a", 40) {
					t.Fatalf("intent %#v", intent)
				}
				if !intent.DryRun || intent.Executed || intent.CheckerAuthority != "none" {
					t.Fatalf("intent authority %#v", intent)
				}
				next := plan.NextReceipts[0]
				if next.State != "planned" || next.Evidence != dryRunEvidence || next.Stage != tc.stage || next.Repository != intent.Repository || next.PullRequest != intent.PullRequest || next.Head != intent.Head || next.Task != intent.Task || next.UID != intent.UID || next.Kind != intent.Kind || next.Owner != intent.Owner || next.Obligation != intent.Obligation {
					t.Fatalf("next %#v", next)
				}
			}
			if tc.state != "" {
				if len(plan.Obligations) != 1 || plan.Obligations[0].State != tc.state || plan.Obligations[0].Kind != tc.kind {
					t.Fatalf("obligation %#v", plan.Obligations)
				}
			}
		})
	}
}

func TestWatchdogFixturesHaveNoAuthority(t *testing.T) {
	matches, err := filepath.Glob("testdata/watchdog-*.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) < 20 {
		t.Fatalf("fixtures %d", len(matches))
	}
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		plan, code := planWatchdog(data)
		if code != 0 && code != 2 {
			t.Fatalf("%s exit %d", path, code)
		}
		raw, err := json.Marshal(plan)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "://") || plan.Executed || plan.CheckerAuthority != "none" || !plan.DryRun || plan.OverallDone {
			t.Fatalf("%s leaked authority or completion", path)
		}
		for _, intent := range plan.Intents {
			body, err := json.Marshal(intent)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(body), "evidence") || strings.Contains(string(body), "://") || !intent.DryRun || intent.Executed {
				t.Fatalf("%s intent %s", path, body)
			}
		}
	}
}

func TestWatchdogReusesCheck(t *testing.T) {
	data := readFixture(t, "watchdog-missed-start.json")
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(doc["handoff"])
	if err != nil {
		t.Fatal(err)
	}
	handoff, code := check(raw)
	plan := planFile(t, "watchdog-missed-start.json", 0)
	if plan.HandoffOutcome != handoff.Outcome || plan.HandoffExitCode != code || plan.HandoffStatus != handoff.Status || plan.Coordinator != handoff.Coordinator || plan.PullRequest != handoff.PullRequest || plan.Head != handoff.Head {
		t.Fatalf("plan did not reuse check: plan %s/%d handoff %s/%d", plan.HandoffOutcome, plan.HandoffExitCode, handoff.Outcome, code)
	}
	if plan.Obligations[0].ResolutionEvidence != "" || plan.Obligations[0].State == "resolved" {
		t.Fatal("unresolved obligation looked resolved")
	}
	if plan.Task != "watchdog-211" || !handoff.Reviews[0].Picked {
		t.Fatalf("task %s picked %#v", plan.Task, handoff.Reviews)
	}
}

func TestWatchdogAdapterLifecycle(t *testing.T) {
	for _, tc := range []struct {
		file  string
		owner string
	}{
		{"watchdog-missed-start.json", "codex-native"},
		{"watchdog-claude-rc.json", "claude-rc"},
	} {
		t.Run(tc.owner, func(t *testing.T) {
			doc := loadDoc(t, tc.file)
			first := planDoc(t, doc, 0)
			if len(first.Intents) != 1 || first.Intents[0].Stage != "owner_wake" || first.Intents[0].Owner != tc.owner {
				t.Fatalf("wake %#v", first.Intents)
			}
			doc["receipts"] = receiptAny(first.NextReceipts)
			second := planDoc(t, doc, 0)
			if len(second.Intents) != 0 || len(second.NextReceipts) != 0 || second.Obligations[0].State == "resolved" {
				t.Fatalf("replay %#v state %s", second.Intents, second.Obligations[0].State)
			}
			doc["receipts"] = append(receiptAny(first.NextReceipts), receiptAny(first.NextReceipts)...)
			dup := planDoc(t, doc, 0)
			if len(dup.Intents) != 0 || len(dup.Receipts) != 1 {
				t.Fatalf("duplicate replay intents %d receipts %d", len(dup.Intents), len(dup.Receipts))
			}
			obs := doc["obligations"].([]any)
			ob := obs[0].(map[string]any)
			ob["ownerEscalationDueAt"] = "2026-10-28T17:30:00Z"
			doc["receipts"] = []any{receiptMap(tc.owner, "owner_wake", "acknowledged", "native-ack", "2026-10-28T17:15:00Z")}
			acked := planDoc(t, doc, 0)
			if len(acked.Intents) != 1 || acked.Intents[0].Stage != "user_escalation" || acked.OverallDone || acked.Obligations[0].State != "current" {
				t.Fatalf("escalation %#v state %s done %v", acked.Intents, acked.Obligations[0].State, acked.OverallDone)
			}
			doc["receipts"] = append(doc["receipts"].([]any), receiptAny(acked.NextReceipts)...)
			quiet := planDoc(t, doc, 0)
			if len(quiet.Intents) != 0 || len(quiet.NextReceipts) != 0 {
				t.Fatalf("escalation replay %#v", quiet.Intents)
			}
			ob["resolutionEvidence"] = "tracker:211-resolved"
			resolved := planDoc(t, doc, 0)
			if resolved.Status != "resolved" || len(resolved.Intents) != 0 || resolved.OverallDone || resolved.HandoffCompletion || resolved.Obligations[0].State != "resolved" {
				t.Fatalf("resolution status %s done %v completion %v", resolved.Status, resolved.OverallDone, resolved.HandoffCompletion)
			}
		})
	}
}

func TestWatchdogPermutations(t *testing.T) {
	base := loadDoc(t, "watchdog-late-ack.json")
	ob := base["obligations"].([]any)[0].(map[string]any)
	ob["ownerEscalationDueAt"] = "2026-10-28T17:30:00Z"
	receipts := []any{
		receiptMap("codex-native", "owner_wake", "planned", dryRunEvidence, "2026-10-28T17:05:00Z"),
		receiptMap("codex-native", "owner_wake", "accepted", "transport-accepted", "2026-10-28T17:10:00Z"),
		receiptMap("codex-native", "owner_wake", "acknowledged", "native-ack", "2026-10-28T17:15:00Z"),
	}
	perms := [][]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}}
	var want string
	for i, perm := range perms {
		doc := cloneDoc(base)
		ordered := make([]any, 0, len(perm))
		for _, idx := range perm {
			ordered = append(ordered, receipts[idx])
		}
		doc["receipts"] = ordered
		plan := planDoc(t, doc, 0)
		got := planSignature(plan)
		if i == 0 {
			want = got
		}
		if got != want || len(plan.Intents) != 1 || plan.Intents[0].Stage != "user_escalation" {
			t.Fatalf("perm %v\n%s", perm, got)
		}
	}
	left := loadDoc(t, "watchdog-missed-start.json")
	right := cloneDoc(left)
	right["obligations"] = []any{
		map[string]any{"id": "ob-pick", "kind": "pickup", "owner": "codex-native", "dueAt": "2026-10-28T17:00:00Z"},
		map[string]any{"id": "ob-start", "kind": "start_ack", "owner": "codex-native", "dueAt": "2026-10-28T17:00:00Z"},
	}
	left["obligations"] = []any{right["obligations"].([]any)[1], right["obligations"].([]any)[0]}
	if !reflect.DeepEqual(planDoc(t, left, 0).Intents, planDoc(t, right, 0).Intents) {
		t.Fatal("obligation order changed intents")
	}
}

func TestWatchdogDueWaitAndHistory(t *testing.T) {
	early := loadDoc(t, "watchdog-missed-start.json")
	early["obligations"].([]any)[0].(map[string]any)["dueAt"] = "2026-10-28T19:00:00Z"
	if plan := planDoc(t, early, 0); len(plan.Intents) != 0 || plan.Status != "current" || plan.Obligations[0].State != "current" {
		t.Fatalf("early %#v", plan.Obligations)
	}
	exact := loadDoc(t, "watchdog-missed-start.json")
	exact["observedAt"] = "2026-10-28T14:00:00-04:00"
	exact["obligations"].([]any)[0].(map[string]any)["dueAt"] = "2026-10-28T18:00:00Z"
	if plan := planDoc(t, exact, 0); len(plan.Intents) != 1 || plan.ObservedAt != "2026-10-28T18:00:00Z" {
		t.Fatalf("equal instant %#v at %s", plan.Intents, plan.ObservedAt)
	}
	waiting := loadDoc(t, "watchdog-wait-expired.json")
	waiting["obligations"].([]any)[0].(map[string]any)["waitUntil"] = "2026-10-28T18:00:01Z"
	if plan := planDoc(t, waiting, 0); len(plan.Intents) != 0 || plan.Status != "waiting" {
		t.Fatalf("unexpired %#v", plan.Status)
	}
	old := loadDoc(t, "watchdog-missed-start.json")
	old["receipts"] = []any{receiptMap("coordinator-sol", "owner_wake", "acknowledged", "native-ack", "2026-10-28T17:15:00Z")}
	woken := planDoc(t, old, 0)
	if len(woken.Intents) != 1 || woken.Intents[0].Owner != "codex-native" || len(woken.Receipts) != 1 || woken.Receipts[0].Owner != "coordinator-sol" {
		t.Fatalf("old owner %#v receipts %#v", woken.Intents, woken.Receipts)
	}
	heart := loadDoc(t, "watchdog-missed-start.json")
	heart["receipts"] = []any{receiptMap("codex-native", "owner_wake", "accepted", "heartbeat", "2026-10-28T17:15:00Z")}
	beat := planDoc(t, heart, 0)
	if len(beat.Intents) != 0 || beat.Obligations[0].State == "resolved" || beat.OverallDone {
		t.Fatalf("heartbeat %#v %s", beat.Intents, beat.Obligations[0].State)
	}
}

func TestWatchdogExitDoesNotComplete(t *testing.T) {
	doc := loadDoc(t, "watchdog-missed-start.json")
	handoff := doc["handoff"].(map[string]any)
	events := handoff["events"].([]any)
	events = append(events,
		map[string]any{"kind": "executor-state", "identity": "ex-exit", "sourceTime": "2026-10-23T16:00:00Z", "executor": "executor-a", "state": "exited"},
		map[string]any{"kind": "delivery-receipt", "identity": "del-1", "sourceTime": "2026-10-23T16:01:00Z"},
		map[string]any{"kind": "acknowledgment", "identity": "ack-1", "sourceTime": "2026-10-23T16:02:00Z", "delivery": "del-1"},
	)
	handoff["events"] = events
	plan := planDoc(t, doc, 0)
	if plan.OverallDone || plan.HandoffCompletion || len(plan.Intents) != 1 {
		t.Fatalf("exit completion %v handoff %v intents %d outcome %s", plan.OverallDone, plan.HandoffCompletion, len(plan.Intents), plan.HandoffOutcome)
	}
}

func TestWatchdogRefusalsAndConflicts(t *testing.T) {
	over, code := planWatchdog(bytes.Repeat([]byte("x"), maxDocumentBytes+1))
	if code != 2 || len(over.Intents) != 0 || strings.Contains(mustJSON(t, over), "owner_wake") {
		t.Fatalf("oversize %d %#v", code, over.Reasons)
	}
	dup := []byte(`{"schema":"dev-env.workflow-watchdog-plan/v1","schema":"dev-env.workflow-watchdog-plan/v1"}`)
	got, code := planWatchdog(dup)
	if code != 2 || !has(got.Reasons, "duplicate JSON key") {
		t.Fatalf("duplicate %d %#v", code, got.Reasons)
	}
	url := loadDoc(t, "watchdog-resolved.json")
	url["obligations"].([]any)[0].(map[string]any)["resolutionEvidence"] = "https://example.com/done"
	refused, code := planWatchdog(mustMarshal(t, url))
	if code != 2 || strings.Contains(mustJSON(t, refused), "example.com") {
		t.Fatalf("url %d %s", code, mustJSON(t, refused))
	}
	unpaired := loadDoc(t, "watchdog-wait.json")
	delete(unpaired["obligations"].([]any)[0].(map[string]any), "waitReason")
	if _, code := planWatchdog(mustMarshal(t, unpaired)); code != 2 {
		t.Fatalf("unpaired wait %d", code)
	}
	extra := loadDoc(t, "watchdog-accepted.json")
	extra["receipts"].([]any)[0].(map[string]any)["sponsor"] = "not-a-binding"
	if _, code := planWatchdog(mustMarshal(t, extra)); code != 2 {
		t.Fatalf("unknown receipt field %d", code)
	}
	regressed := loadDoc(t, "watchdog-late-ack.json")
	regressed["receipts"] = []any{
		receiptMap("codex-native", "owner_wake", "acknowledged", "native-ack", "2026-10-28T17:15:00Z"),
		receiptMap("codex-native", "owner_wake", "planned", dryRunEvidence, "2026-10-28T17:20:00Z"),
	}
	back := planDoc(t, regressed, 0)
	if back.Status != "blocked" || len(back.Intents) != 0 || !has(back.Reasons, "does not advance") {
		t.Fatalf("regression %#v", back.Reasons)
	}
	both := loadDoc(t, "watchdog-missed-start.json")
	both["obligations"] = []any{
		map[string]any{"id": "ob-start", "kind": "start_ack", "owner": "codex-native", "dueAt": "2026-10-28T17:00:00Z"},
		map[string]any{"id": "ob-other", "kind": "pickup", "owner": "claude-rc", "dueAt": "2026-10-28T17:00:00Z"},
	}
	mixed := planDoc(t, both, 0)
	if mixed.Status != "blocked" || len(mixed.Intents) != 0 {
		t.Fatalf("mixed owner %#v", mixed.Intents)
	}
	bad := loadDoc(t, "watchdog-missed-start.json")
	bad["handoff"] = map[string]any{"schema": "dev-env.workflow-handoff/v1"}
	blocked := planDoc(t, bad, 0)
	if blocked.Status != "blocked" || len(blocked.Intents) != 0 || !has(blocked.Reasons, "handoff record was refused") {
		t.Fatalf("bad handoff %#v", blocked.Reasons)
	}
	variants := []struct {
		name string
		file string
		from string
		to   string
	}{
		{"wrapper", "watchdog-missed-start.json", `"observedAt"`, `"ObservedAt"`},
		{"obligation", "watchdog-missed-start.json", `"dueAt"`, `"DueAt"`},
		{"receipt", "watchdog-accepted.json", `"recordedAt"`, `"RecordedAt"`},
	}
	for _, tc := range variants {
		variant := bytes.Replace(readFixture(t, tc.file), []byte(tc.from), []byte(tc.to), 1)
		folded, foldCode := planWatchdog(variant)
		if foldCode != 2 || len(folded.Intents) != 0 || len(folded.NextReceipts) != 0 || !has(folded.Reasons, "case-variant JSON key") {
			t.Fatalf("%s case variant %d intents %d reasons %#v", tc.name, foldCode, len(folded.Intents), folded.Reasons)
		}
	}
}

func TestWatchdogReceiptBindingReplay(t *testing.T) {
	doc := loadDoc(t, "watchdog-missed-start.json")
	first := planDoc(t, doc, 0)
	if len(first.NextReceipts) != 1 || !boundReceipt(first.NextReceipts[0], "start_ack", "ob-start", "codex-native", "owner_wake") {
		t.Fatalf("generated binding %#v", first.NextReceipts)
	}
	doc["receipts"] = receiptAny(first.NextReceipts)
	same := planDoc(t, doc, 0)
	if len(same.Intents) != 0 || len(same.NextReceipts) != 0 || len(same.Receipts) != 1 || !boundReceipt(same.Receipts[0], "start_ack", "ob-start", "codex-native", "owner_wake") {
		t.Fatalf("same binding replay intents %d receipts %#v", len(same.Intents), same.Receipts)
	}
	old := cloneDoc(loadDoc(t, "watchdog-missed-start.json"))
	old["receipts"] = []any{receiptMap("coordinator-sol", "owner_wake", "acknowledged", "native-ack", "2026-10-28T17:15:00Z")}
	woken := planDoc(t, old, 0)
	if len(woken.Intents) != 1 || woken.Intents[0].Owner != "codex-native" || len(woken.Receipts) != 1 || woken.Receipts[0].Owner != "coordinator-sol" {
		t.Fatalf("old owner %#v %#v", woken.Intents, woken.Receipts)
	}
	baseReceipt := receiptAny(first.NextReceipts)
	foreign := []struct {
		name   string
		adjust func(map[string]any)
	}{
		{"head", func(doc map[string]any) { setReviewIdentity(doc, "head", strings.Repeat("b", 40)) }},
		{"task", func(doc map[string]any) { setReviewIdentity(doc, "task", "watchdog-212") }},
		{"uid", func(doc map[string]any) { setReviewIdentity(doc, "uid", "uid-watchdog-212") }},
		{"repository", func(doc map[string]any) {
			doc["handoff"].(map[string]any)["repository"] = "github.com/thaynes43/other-repo"
		}},
		{"pullRequest", func(doc map[string]any) { doc["handoff"].(map[string]any)["pullRequest"] = 212 }},
		{"kind", func(doc map[string]any) { doc["obligations"].([]any)[0].(map[string]any)["kind"] = "pickup" }},
	}
	for _, tc := range foreign {
		t.Run(tc.name, func(t *testing.T) {
			next := cloneDoc(loadDoc(t, "watchdog-missed-start.json"))
			tc.adjust(next)
			rawHandoff, err := json.Marshal(next["handoff"])
			if err != nil {
				t.Fatal(err)
			}
			checked, code := check(rawHandoff)
			if code != 0 || checked.Status != "reduced" || len(checked.Reviews) != 1 || !checked.Reviews[0].Picked {
				t.Fatalf("replacement handoff %d %s picked %#v", code, checked.Status, checked.Reviews)
			}
			next["receipts"] = baseReceipt
			refused, foldCode := planWatchdog(mustMarshal(t, next))
			if foldCode != 2 || len(refused.Intents) != 0 || len(refused.NextReceipts) != 0 {
				t.Fatalf("foreign %s exit %d intents %#v reasons %#v", tc.name, foldCode, refused.Intents, refused.Reasons)
			}
		})
	}
	missing := loadDoc(t, "watchdog-duplicate.json")
	delete(missing["receipts"].([]any)[0].(map[string]any), "repository")
	if refused, code := planWatchdog(mustMarshal(t, missing)); code != 2 || len(refused.Intents) != 0 || len(refused.NextReceipts) != 0 {
		t.Fatalf("missing binding %d %#v", code, refused.Reasons)
	}
	typed := loadDoc(t, "watchdog-duplicate.json")
	typed["receipts"].([]any)[0].(map[string]any)["pullRequest"] = "211"
	if refused, code := planWatchdog(mustMarshal(t, typed)); code != 2 || len(refused.Intents) != 0 {
		t.Fatalf("type binding %d %#v", code, refused.Reasons)
	}
	foldedDoc := loadDoc(t, "watchdog-duplicate.json")
	receipt := foldedDoc["receipts"].([]any)[0].(map[string]any)
	delete(receipt, "repository")
	receipt["REPOSITORY"] = "github.com/thaynes43/dev-env"
	if refused, code := planWatchdog(mustMarshal(t, foldedDoc)); code != 2 || len(refused.Intents) != 0 || len(refused.NextReceipts) != 0 || !has(refused.Reasons, "case-variant JSON key") {
		t.Fatalf("unicode fold %d %#v", code, refused.Reasons)
	}
	conflict := loadDoc(t, "watchdog-duplicate.json")
	other := receiptMap("codex-native", "owner_wake", "planned", dryRunEvidence, "2026-10-28T17:40:00Z")
	other["head"] = strings.Repeat("b", 40)
	conflict["receipts"] = append(conflict["receipts"].([]any), other)
	if refused, code := planWatchdog(mustMarshal(t, conflict)); code != 2 || len(refused.Intents) != 0 || len(refused.NextReceipts) != 0 {
		t.Fatalf("conflicting binding %d %#v", code, refused.Reasons)
	}
}

func TestWatchdogAcceptedEscalationWithoutAck(t *testing.T) {
	plan := planFile(t, "watchdog-accepted.json", 0)
	if len(plan.Intents) != 1 || plan.Intents[0].Stage != "user_escalation" || plan.OverallDone || plan.Obligations[0].State != "current" || !boundReceipt(plan.NextReceipts[0], "start_ack", "ob-start", "codex-native", "user_escalation") {
		t.Fatalf("accepted escalation %#v done %v", plan.Intents, plan.OverallDone)
	}
	doc := loadDoc(t, "watchdog-accepted.json")
	doc["receipts"] = append(doc["receipts"].([]any), receiptAny(plan.NextReceipts)...)
	quiet := planDoc(t, doc, 0)
	if len(quiet.Intents) != 0 || len(quiet.NextReceipts) != 0 || quiet.OverallDone {
		t.Fatalf("escalation replay %#v done %v", quiet.Intents, quiet.OverallDone)
	}
	early := loadDoc(t, "watchdog-accepted.json")
	early["observedAt"] = "2026-10-28T17:20:00Z"
	if before := planDoc(t, early, 0); len(before.Intents) != 0 || before.OverallDone {
		t.Fatalf("before deadline %#v", before.Intents)
	}
	undated := loadDoc(t, "watchdog-accepted.json")
	delete(undated["obligations"].([]any)[0].(map[string]any), "ownerEscalationDueAt")
	if missing := planDoc(t, undated, 0); len(missing.Intents) != 0 {
		t.Fatalf("missing deadline %#v", missing.Intents)
	}
	waiting := loadDoc(t, "watchdog-accepted.json")
	ob := waiting["obligations"].([]any)[0].(map[string]any)
	ob["waitUntil"] = "2026-10-28T20:00:00Z"
	ob["waitReason"] = "maintenance window"
	if held := planDoc(t, waiting, 0); len(held.Intents) != 0 || held.Status != "waiting" {
		t.Fatalf("wait %#v", held.Status)
	}
	blocked := loadDoc(t, "watchdog-hold.json")
	blocked["obligations"].([]any)[0].(map[string]any)["ownerEscalationDueAt"] = "2026-10-28T17:30:00Z"
	blocked["receipts"] = loadDoc(t, "watchdog-accepted.json")["receipts"]
	if hold := planDoc(t, blocked, 0); hold.Status != "blocked" || len(hold.Intents) != 0 || !has(hold.Reasons, "unresolved hold") {
		t.Fatalf("hold %#v %#v", hold.Status, hold.Reasons)
	}
	done := loadDoc(t, "watchdog-accepted.json")
	done["obligations"].([]any)[0].(map[string]any)["resolutionEvidence"] = "tracker:211-resolved"
	if resolved := planDoc(t, done, 0); len(resolved.Intents) != 0 || resolved.OverallDone || resolved.Obligations[0].State != "resolved" {
		t.Fatalf("resolved %#v %s", resolved.Intents, resolved.Obligations[0].State)
	}
	rejected := loadDoc(t, "watchdog-missed-start.json")
	rejected["obligations"].([]any)[0].(map[string]any)["ownerEscalationDueAt"] = "2026-10-28T17:30:00Z"
	rejected["receipts"] = []any{receiptMap("codex-native", "owner_wake", "rejected", "transport-rejected", "2026-10-28T17:10:00Z")}
	if stop := planDoc(t, rejected, 0); len(stop.Intents) != 0 || stop.OverallDone || stop.Obligations[0].State == "resolved" {
		t.Fatalf("rejected %#v %s", stop.Intents, stop.Obligations[0].State)
	}
	planned := loadDoc(t, "watchdog-missed-start.json")
	wake := planDoc(t, planned, 0)
	planned["obligations"].([]any)[0].(map[string]any)["ownerEscalationDueAt"] = "2026-10-28T17:30:00Z"
	planned["receipts"] = receiptAny(wake.NextReceipts)
	if onlyPlanned := planDoc(t, planned, 0); len(onlyPlanned.Intents) != 0 {
		t.Fatalf("planned wake escalated %#v", onlyPlanned.Intents)
	}
	ambiguous := planFile(t, "watchdog-ambiguous.json", 0)
	if len(ambiguous.Intents) != 0 || ambiguous.OverallDone {
		t.Fatalf("ambiguous escalated %#v", ambiguous.Intents)
	}
}

func boundReceipt(rc receiptView, kind, obligation, owner, stage string) bool {
	return rc.Kind == kind && rc.Obligation == obligation && rc.Owner == owner && rc.Stage == stage && rc.Repository == "github.com/thaynes43/dev-env" && rc.PullRequest == 211 && rc.Head == strings.Repeat("a", 40) && rc.Task == "watchdog-211" && rc.UID == "uid-watchdog-211" && rc.Evidence != ""
}

func setReviewIdentity(doc map[string]any, field, value string) {
	handoff := doc["handoff"].(map[string]any)
	if field == "head" {
		handoff["head"] = value
	}
	if field == "task" || field == "uid" {
		handoff["reviewBinding"].(map[string]any)[field] = value
	}
	for _, ev := range handoff["events"].([]any) {
		item := ev.(map[string]any)
		if item["kind"] == "review-observation" {
			item[field] = value
		}
	}
}

func TestWatchdogCLIStaysBackwardCompatible(t *testing.T) {
	var out, errw bytes.Buffer
	if code := run([]string{"testdata/e-merge.json"}, nil, &out, &errw); code != 0 || !strings.Contains(out.String(), "dev-env.workflow-handoff/v1") || !strings.Contains(out.String(), "recommend-normal-merge-release") {
		t.Fatalf("handoff cli %d %s", code, out.String())
	}
	out.Reset()
	errw.Reset()
	if code := run([]string{"--watchdog", "testdata/watchdog-missed-start.json"}, nil, &out, &errw); code != 0 || !strings.Contains(out.String(), "owner_wake") || strings.Contains(out.String(), "://") {
		t.Fatalf("watchdog cli %d %s %s", code, out.String(), errw.String())
	}
	data := readFixture(t, "watchdog-user-question.json")
	out.Reset()
	if code := run([]string{"--watchdog", "-"}, bytes.NewReader(data), &out, &errw); code != 0 || !strings.Contains(out.String(), "user_question") {
		t.Fatalf("stdin %d %s", code, errw.String())
	}
	out.Reset()
	errw.Reset()
	if code := run([]string{"--watchdog", "testdata/watchdog-malformed.json"}, nil, &out, &errw); code != 2 || strings.Contains(out.String(), "owner_wake") {
		t.Fatalf("malformed cli %d %s", code, out.String())
	}
	out.Reset()
	if code := run(nil, bytes.NewReader(nil), &out, &errw); code != 2 || !strings.Contains(out.String(), "dev-env.workflow-handoff/v1") {
		t.Fatalf("usage %d %s", code, out.String())
	}
	for _, name := range []string{"watchdog.go", "main.go"} {
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

func planFile(t *testing.T, name string, code int) watchdogPlan {
	t.Helper()
	plan, got := planWatchdog(readFixture(t, name))
	if got != code {
		t.Fatalf("%s exit %d status %s reasons %#v", name, got, plan.Status, plan.Reasons)
	}
	return plan
}

func planDoc(t *testing.T, doc map[string]any, code int) watchdogPlan {
	t.Helper()
	plan, got := planWatchdog(mustMarshal(t, doc))
	if got != code {
		t.Fatalf("exit %d status %s reasons %#v", got, plan.Status, plan.Reasons)
	}
	return plan
}

func loadDoc(t *testing.T, name string) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(readFixture(t, name), &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func cloneDoc(doc map[string]any) map[string]any {
	raw, err := json.Marshal(doc)
	if err != nil {
		panic(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		panic(err)
	}
	return out
}

func receiptMap(owner, stage, state, evidence, at string) map[string]any {
	return map[string]any{
		"obligation":  "ob-start",
		"owner":       owner,
		"stage":       stage,
		"state":       state,
		"evidence":    evidence,
		"recordedAt":  at,
		"repository":  "github.com/thaynes43/dev-env",
		"pullRequest": 211,
		"head":        strings.Repeat("a", 40),
		"task":        "watchdog-211",
		"uid":         "uid-watchdog-211",
		"kind":        "start_ack",
	}
}

func receiptAny(items []receiptView) []any {
	raw, err := json.Marshal(items)
	if err != nil {
		panic(err)
	}
	var out []any
	if err := json.Unmarshal(raw, &out); err != nil {
		panic(err)
	}
	return out
}

func planSignature(plan watchdogPlan) string {
	var b strings.Builder
	b.WriteString(plan.Status)
	for _, ob := range plan.Obligations {
		b.WriteString("|")
		b.WriteString(ob.ID)
		b.WriteString(ob.State)
	}
	for _, intent := range plan.Intents {
		b.WriteString("|")
		b.WriteString(intent.Obligation)
		b.WriteString(intent.Stage)
		b.WriteString(intent.Owner)
		b.WriteString(intent.Kind)
	}
	for _, rc := range plan.Receipts {
		b.WriteString("|")
		b.WriteString(rc.Obligation)
		b.WriteString(rc.Owner)
		b.WriteString(rc.Stage)
		b.WriteString(rc.State)
		b.WriteString(rc.RecordedAt)
	}
	return b.String()
}

func mustMarshal(t *testing.T, doc map[string]any) []byte {
	t.Helper()
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func mustJSON(t *testing.T, plan watchdogPlan) string {
	t.Helper()
	body, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
