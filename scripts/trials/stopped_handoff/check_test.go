package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestSourceAcceptedPreservesCustody(t *testing.T) {
	res := mustCheck(t, loadDoc(t), 0)
	if res.Outcome != "source-record-accepted" || !res.RestoreAuthorized || res.RecordedSuccessor != "" || !res.BlockedCustody {
		t.Fatalf("outcome %s restore %v successor %q blocked %v reason %s", res.Outcome, res.RestoreAuthorized, res.RecordedSuccessor, res.BlockedCustody, res.BlockedReason)
	}
	if res.BlockedReason != "supported continuation unavailable" {
		t.Fatalf("reason %s", res.BlockedReason)
	}
	if res.ProofClass != "source-synthetic" || res.ExplicitContextProof != "source-record" || res.FullHistoryProof != "not-claimed" || res.NativeImport != "not-performed" {
		t.Fatalf("proof %s %s %s %s", res.ProofClass, res.ExplicitContextProof, res.FullHistoryProof, res.NativeImport)
	}
	if res.NextOwner != "coordinator-sol" || !res.PickupRecorded || !res.AcknowledgmentRecorded || !res.AcceptedStop {
		t.Fatalf("owner %s pickup %v ack %v stop %v", res.NextOwner, res.PickupRecorded, res.AcknowledgmentRecorded, res.AcceptedStop)
	}
	if len(res.Holds) != 1 || res.Holds[0] != "hold-synthetic-merge" || res.ConsumedBudget != "attempt-1" || res.Attempts != 1 {
		t.Fatalf("holds %#v budget %s attempts %d", res.Holds, res.ConsumedBudget, res.Attempts)
	}
	if res.RulesRevision != "rules-rev-synthetic-1" || res.ScopeRef != "scope-101-synthetic" || res.AuthorityRef != "authority-synthetic-1" {
		t.Fatalf("scope %s %s %s", res.RulesRevision, res.ScopeRef, res.AuthorityRef)
	}
	if res.PendingFindings[0] != "finding-synthetic-1" || res.SourceTime != "2026-10-23T23:00:00Z" {
		t.Fatalf("finding %#v time %s", res.PendingFindings, res.SourceTime)
	}
	if res.AdvisoryAuthority != "none" || res.AdvisoryChecker != advisoryChecker || res.InstalledDrill != "not-performed" {
		t.Fatalf("advisory %s %s drill %s", res.AdvisoryAuthority, res.AdvisoryChecker, res.InstalledDrill)
	}
	if len(res.Phases) != 3 || res.Phases[0].Name != "parse" || res.Phases[1].Name != "validate" || res.Phases[2].Name != "reduce" {
		t.Fatalf("phases %#v", res.Phases)
	}
}

func TestCustodyBlockers(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*document)
		reason string
	}{
		{"wrong-uid", func(d *document) { d.Observed.UID = "uid-other-9" }, "wrong source UID"},
		{"wrong-head", func(d *document) { d.Observed.Head = strings.Repeat("ab", 20) }, "wrong source head"},
		{"running", func(d *document) { d.Predecessor.ExecutorState = "running" }, "predecessor is running"},
		{"uncertain", func(d *document) { d.Predecessor.ExecutorState = "uncertain" }, "predecessor is uncertain"},
		{"unavailable", func(d *document) { d.Predecessor.ExecutorState = "unavailable" }, "predecessor is unavailable"},
		{"exited", func(d *document) { d.Predecessor.ExecutorState = "exited"; d.Predecessor.StopVerified = false }, "exited process is not a verified stop"},
		{"ambiguous", func(d *document) { d.Predecessor.StopAmbiguous = true }, "stop is ambiguous"},
		{"unverified", func(d *document) { d.Predecessor.StopVerified = false }, "verified stop is required"},
		{"missing-context", func(d *document) {
			d.Observed.ContextPresent = false
			d.Observed.ContextChanged = false
			d.Observed.ContextHash = ""
		}, "context is missing"},
		{"changed-context", func(d *document) {
			d.Observed.ContextChanged = true
			d.Observed.ContextHash = strings.Repeat("f", 64)
		}, "context hash changed"},
		{"missing-wip", func(d *document) {
			d.Observed.WIPPresent = false
			d.Observed.WIPChanged = false
			d.Observed.WIPHash = ""
		}, "WIP is missing"},
		{"changed-wip", func(d *document) {
			d.Observed.WIPChanged = true
			d.Observed.WIPHash = strings.Repeat("a", 64)
		}, "WIP hash changed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := loadDoc(t)
			tc.mutate(&doc)
			doc.Events = append(doc.Events, event{
				Kind: "successor-request", Identity: "succ-1", SourceTime: "2026-10-23T23:08:00Z", Successor: "synthetic-successor",
			})
			res := mustCheck(t, doc, 0)
			if res.Outcome != "blocked-custody" || res.RestoreAuthorized || res.RecordedSuccessor != "" || !strings.Contains(res.BlockedReason, tc.reason) {
				t.Fatalf("outcome %s restore %v successor %q reason %s", res.Outcome, res.RestoreAuthorized, res.RecordedSuccessor, res.BlockedReason)
			}
			if !has(res.Refusals, "successor request ignored") {
				t.Fatalf("refusals %#v", res.Refusals)
			}
			if res.Holds[0] != "hold-synthetic-merge" || res.ConsumedBudget != "attempt-1" {
				t.Fatalf("lost custody %#v %s", res.Holds, res.ConsumedBudget)
			}
		})
	}
}

func TestReplayReconnectAndLateResult(t *testing.T) {
	doc := loadDoc(t)
	doc.Events = append(doc.Events, doc.Events[0])
	replay := mustCheck(t, doc, 0)
	if replay.IgnoredReplayCount != 1 || replay.Outcome != "source-record-accepted" || replay.RecordedSuccessorCount != 0 {
		t.Fatalf("replay %#v", replay.IgnoredReplayCount)
	}

	conflicted := loadDoc(t)
	ev := conflicted.Events[0]
	ev.Note = "different synthetic note"
	conflicted.Events = append(conflicted.Events, ev)
	conflict := mustCheck(t, conflicted, 3)
	if conflict.Status != "conflict" || !conflict.ManualDisposition || conflict.RestoreAuthorized || conflict.RecordedSuccessor != "" || conflict.NextOwner != "" {
		t.Fatalf("conflict %#v", conflict)
	}

	delayed := loadDoc(t)
	delayed.Events = append(delayed.Events, event{
		Kind: "delayed-running", Identity: "delay-1", SourceTime: "2026-10-23T23:09:00Z", Note: "synthetic delayed running",
	})
	delayedRes := mustCheck(t, delayed, 0)
	if delayedRes.ExecutorState != "stopped" || delayedRes.AcceptedStop || delayedRes.RestoreAuthorized || !delayedRes.BlockedCustody || delayedRes.ResultCount != 0 || delayedRes.RecordedSuccessor != "" || !strings.Contains(delayedRes.BlockedReason, reasonLaterObservation) || !has(delayedRes.Refusals, "delayed running observation does not replace a verified stop or create another result") {
		t.Fatalf("delayed %#v %s stop %v", delayedRes.Refusals, delayedRes.BlockedReason, delayedRes.AcceptedStop)
	}
	if delayedRes.Holds[0] != "hold-synthetic-merge" || delayedRes.ConsumedBudget != "attempt-1" || delayedRes.Attempts != 1 || delayedRes.AuthorityRef != "authority-synthetic-1" {
		t.Fatalf("delayed lost custody %#v", delayedRes)
	}

	stale := loadDoc(t)
	stale.Events = append(stale.Events, event{
		Kind: "delayed-running", Identity: "delay-old", SourceTime: "2026-10-23T22:30:00Z", Note: "synthetic stale running",
	})
	staleRes := mustCheck(t, stale, 0)
	if !staleRes.AcceptedStop || !staleRes.RestoreAuthorized || staleRes.Outcome != "source-record-accepted" || !has(staleRes.Refusals, "delayed running observation does not replace a verified stop or create another result") {
		t.Fatalf("stale running %#v %s", staleRes.Refusals, staleRes.BlockedReason)
	}

	reconn := loadDoc(t)
	reconn.Events = append(reconn.Events, event{
		Kind: "reconnect", Identity: "re-1", SourceTime: "2026-10-23T23:09:30Z", Note: "synthetic reconnect",
	})
	reconnRes := mustCheck(t, reconn, 0)
	if reconnRes.RecordedSuccessorCount != 0 || reconnRes.RestoreAuthorized || !reconnRes.BlockedCustody || !strings.Contains(reconnRes.BlockedReason, reasonLaterObservation) || !has(reconnRes.Refusals, "reconnect does not create a successor or another result") {
		t.Fatalf("reconnect %#v %s", reconnRes.Refusals, reconnRes.BlockedReason)
	}

	late := loadDoc(t)
	lateHash := strings.Repeat("f", 64)
	late.Events = append(late.Events, event{
		Kind: "result-after-wait", Identity: "late-1", SourceTime: "2026-10-23T23:10:00Z", ContentHash: lateHash,
	})
	lateRes := mustCheck(t, late, 0)
	if lateRes.LateResult != lateHash || lateRes.ResultCount != 1 || lateRes.InstalledJourney || lateRes.RecordedSuccessor != "" {
		t.Fatalf("late %s count %d installed %v", lateRes.LateResult, lateRes.ResultCount, lateRes.InstalledJourney)
	}
	second := late.Events[len(late.Events)-1]
	second.Identity = "late-2"
	second.ContentHash = strings.Repeat("a", 64)
	late.Events = append(late.Events, second)
	secondRes := mustCheck(t, late, 0)
	if secondRes.ResultCount != 1 || secondRes.LateResult != lateHash || !has(secondRes.Refusals, "additional result ignored") {
		t.Fatalf("second result %s %#v", secondRes.LateResult, secondRes.Refusals)
	}
}

func TestPartialPickupAndSuccessorGates(t *testing.T) {
	partial := loadDoc(t)
	partial.Events = partial.Events[1:]
	partialRes := mustCheck(t, partial, 0)
	if partialRes.Outcome != "partial" || partialRes.RestoreAuthorized || partialRes.PickupRecorded || partialRes.NextOwner != "coordinator-sol" {
		t.Fatalf("partial %s owner %s", partialRes.Outcome, partialRes.NextOwner)
	}

	missingOwner := loadDoc(t)
	missingOwner.Events = missingOwner.Events[:2]
	ownerRes := mustCheck(t, missingOwner, 0)
	if ownerRes.Outcome != "partial" || ownerRes.NextOwner != "" {
		t.Fatalf("missing owner %q outcome %s", ownerRes.NextOwner, ownerRes.Outcome)
	}

	ok := authorizeSuccessor(loadDoc(t))
	okRes := mustCheck(t, ok, 0)
	if okRes.RecordedSuccessor != "synthetic-successor" || okRes.RecordedSuccessorCount != 1 || okRes.SuccessorAction != "not-performed" || okRes.BlockedCustody || okRes.InstalledJourney {
		t.Fatalf("successor %#v", okRes)
	}

	dup := authorizeSuccessor(loadDoc(t))
	dup.Events = append(dup.Events, event{
		Kind: "successor-request", Identity: "succ-2", SourceTime: "2026-10-23T23:09:00Z", Successor: "synthetic-other",
	})
	dupRes := mustCheck(t, dup, 0)
	if dupRes.RecordedSuccessor != "synthetic-successor" || dupRes.RecordedSuccessorCount != 1 || !has(dupRes.Refusals, "duplicate successor does not replace the recorded successor") {
		t.Fatalf("duplicate %#v %s", dupRes.Refusals, dupRes.RecordedSuccessor)
	}

	unauth := authorizeSuccessor(loadDoc(t))
	unauth.Assignment.Authorized = false
	unauthRes := mustCheck(t, unauth, 0)
	if unauthRes.RecordedSuccessor != "" || !strings.Contains(unauthRes.BlockedReason, "supported successor assignment is not recorded") {
		t.Fatalf("unauthorized %s %q", unauthRes.BlockedReason, unauthRes.RecordedSuccessor)
	}
}

func TestRefusalsLeaveTaskClaimUnchanged(t *testing.T) {
	claim := loadDoc(t)
	claim.TaskClaim = "rewritten"
	claimRes := mustCheck(t, claim, 2)
	if !strings.Contains(claimRes.Refusals[0], "task claim mutation") || claimRes.TaskClaim != "unchanged" {
		t.Fatalf("claim %#v", claimRes.Refusals)
	}

	native := loadDoc(t)
	native.Excerpt.NativeHistory = true
	nativeRes := mustCheck(t, native, 2)
	if !strings.Contains(nativeRes.Refusals[0], "not native history") {
		t.Fatalf("native %#v", nativeRes.Refusals)
	}

	imp := loadDoc(t)
	imp.Excerpt.NativeImport = true
	if !strings.Contains(mustCheck(t, imp, 2).Refusals[0], "not native import") {
		t.Fatal("import")
	}

	same := loadDoc(t)
	same.Recipient.Identity = same.Predecessor.Identity
	same.Recipient.UID = "uid-still-distinct"
	if !strings.Contains(mustCheck(t, same, 2).Refusals[0], "distinct identities") {
		t.Fatal("identity")
	}

	bad := loadDoc(t)
	bad.Observed.ContextHash = strings.Repeat("f", 64)
	if !strings.Contains(mustCheck(t, bad, 2).Refusals[0], "contextChanged does not match") {
		t.Fatal("inconsistent context")
	}

	frac := loadDoc(t)
	frac.Predecessor.SourceTime = "2026-10-23T23:00:00.5Z"
	if !strings.Contains(mustCheck(t, frac, 2).Refusals[0], "fractional") {
		t.Fatal("fraction")
	}

	raw := string(mustRead(t, "testdata/source-accepted.json"))
	raw = strings.Replace(raw, `"story": "101"`, `"story": "101", "extra": true`, 1)
	res, code := check([]byte(raw))
	if code != 2 || res.TaskClaim != "unchanged" {
		t.Fatalf("unknown field code %d %s", code, res.Refusals)
	}
	dupRaw := strings.Replace(raw, `"extra": true`, `"story": "101"`, 1)
	dupRes, dupCode := check([]byte(dupRaw))
	if dupCode != 2 || !strings.Contains(dupRes.Refusals[0], "duplicate JSON key") {
		t.Fatalf("duplicate %#v", dupRes.Refusals)
	}
	over, overCode := check(bytes.Repeat([]byte("x"), maxDocumentBytes+1))
	if overCode != 2 || !strings.Contains(over.Refusals[0], "128KiB") || over.TaskClaim != "unchanged" {
		t.Fatalf("oversize %#v", over.Refusals)
	}
}

func TestCheckDoesNotDirtyWorktree(t *testing.T) {
	before := worktreeStatus(t)
	var buf bytes.Buffer
	code := run([]string{"check", "testdata/source-accepted.json"}, nil, &buf, &bytes.Buffer{})
	if code != 0 {
		t.Fatalf("check exit %d %s", code, buf.String())
	}
	var res result
	if err := json.Unmarshal(buf.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.InstalledJourney || res.TaskClaim != "unchanged" || res.FixtureVerified {
		t.Fatalf("cli result installed %v fixture %v", res.InstalledJourney, res.FixtureVerified)
	}
	if after := worktreeStatus(t); after != before {
		t.Fatalf("worktree changed\nbefore %q\nafter %q", before, after)
	}
}

func TestPreflightIsSourcePresenceOnly(t *testing.T) {
	var buf bytes.Buffer
	if code := run([]string{"preflight"}, nil, &buf, &bytes.Buffer{}); code != 0 {
		t.Fatalf("preflight %d %s", code, buf.String())
	}
	var report capability
	if err := json.Unmarshal(buf.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.DeployedCapability || report.InstalledDrill != "not-performed" || report.Network != "not-used" || report.Model != "not-used" {
		t.Fatalf("%#v", report)
	}
	if report.LiveStop != "not-performed" || report.LiveRead != "not-performed" || report.LiveRestore != "not-performed" || report.LiveAssignment != "not-performed" {
		t.Fatalf("live %#v", report)
	}
	if report.H7RequiredForExcerptProof || report.OperatorSettings != "not-read" || report.Release != "unknown" || report.Profile != "not-selected" {
		t.Fatalf("activation %#v", report)
	}
	if report.BlockedOperation != blockedOperation || report.BlockedOperationOwner != blockedOwner {
		t.Fatalf("blocker %s %s", report.BlockedOperation, report.BlockedOperationOwner)
	}
	out, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	if report.SourceCommit != strings.TrimSpace(string(out)) || report.SourcePresence != "checker-present" {
		t.Fatalf("commit %s presence %s", report.SourceCommit, report.SourcePresence)
	}
}

func TestCanonicalKeysAndStopOrder(t *testing.T) {
	raw := string(mustRead(t, "testdata/source-accepted.json"))
	cases := []struct {
		name string
		body string
		want string
	}{
		{"executor-case", strings.Replace(raw, `"executorState": "stopped",`, `"executorState": "running", "ExecutorState": "stopped",`, 1), "not canonical"},
		{"holds-case", strings.Replace(raw, `"holds": ["hold-synthetic-merge"],`, `"holds": ["hold-synthetic-merge"], "Holds": [],`, 1), "not canonical"},
		{"unicode-synthetic", strings.Replace(raw, `"synthetic": true,`, `"synthetic": false, "ſynthetic": true,`, 1), "non-ASCII"},
		{"taskclaim-case", strings.Replace(raw, `"taskClaim": "unchanged",`, `"taskClaim": "rewritten", "TASKCLAIM": "unchanged",`, 1), "not canonical"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.body == raw {
				t.Fatal("replacement missed")
			}
			res, code := check([]byte(tc.body))
			if code != 2 || res.TaskClaim != "unchanged" || res.RestoreAuthorized || !strings.Contains(res.Refusals[0], tc.want) {
				t.Fatalf("code %d refusals %#v", code, res.Refusals)
			}
		})
	}

	later := authorizeSuccessor(loadDoc(t))
	later.Events = append(later.Events, event{
		Kind: "delayed-running", Identity: "run-1", SourceTime: "2026-10-24T01:00:00Z", Note: "running seen two hours after the verified stop",
	})
	laterRes := mustCheck(t, later, 0)
	if laterRes.RestoreAuthorized || !laterRes.BlockedCustody || laterRes.AcceptedStop || laterRes.RecordedSuccessor != "" || laterRes.Holds[0] != "hold-synthetic-merge" {
		t.Fatalf("later running %#v %s", laterRes.RecordedSuccessor, laterRes.BlockedReason)
	}

	early := authorizeSuccessor(loadDoc(t))
	for i := range early.Events {
		early.Events[i].SourceTime = "2026-10-23T22:00:00Z"
	}
	earlyRes := mustCheck(t, early, 0)
	if earlyRes.RestoreAuthorized || earlyRes.RecordedSuccessor != "" || !strings.Contains(earlyRes.BlockedReason, reasonBeforeStop) || earlyRes.ConsumedBudget != "attempt-1" || earlyRes.Attempts != 1 {
		t.Fatalf("early pickup %#v %s", earlyRes.RecordedSuccessor, earlyRes.BlockedReason)
	}
}

func authorizeSuccessor(doc document) document {
	doc.Continuation = "available"
	doc.Assignment.Authorized = true
	doc.Assignment.Supported = true
	doc.Assignment.Successor = "synthetic-successor"
	doc.Events = append(doc.Events, event{
		Kind: "successor-request", Identity: "succ-1", SourceTime: "2026-10-23T23:08:00Z", Successor: "synthetic-successor",
	})
	return doc
}

func loadDoc(t *testing.T) document {
	t.Helper()
	var doc document
	dec := json.NewDecoder(bytes.NewReader(mustRead(t, "testdata/source-accepted.json")))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func mustCheck(t *testing.T, doc document, code int) result {
	t.Helper()
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	res, got := check(body)
	if got != code {
		t.Fatalf("exit %d want %d outcome %s reason %s refusals %#v conflicts %#v", got, code, res.Outcome, res.BlockedReason, res.Refusals, res.Conflicts)
	}
	if res.TaskClaim != taskClaimUnchanged || res.InstalledJourney || res.Deployment != "not-performed" || res.CheckerAuthority != checkerAuthority || res.SuccessorAction != "not-performed" {
		t.Fatalf("invariants claim %s installed %v deploy %s authority %s action %s", res.TaskClaim, res.InstalledJourney, res.Deployment, res.CheckerAuthority, res.SuccessorAction)
	}
	return res
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func worktreeStatus(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "status", "--porcelain").Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func has(vals []string, want string) bool {
	for _, v := range vals {
		if v == want {
			return true
		}
	}
	return false
}
