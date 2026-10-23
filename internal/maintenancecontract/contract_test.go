package maintenancecontract

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const (
	rev = "0123456789abcdef0123456789abcdef01234567"
	pre = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

var fixedNow = time.Date(2026, 10, 23, 5, 1, 0, 0, time.UTC)

func TestCandidate(t *testing.T) {
	res := mustCheck(t, sample(t, "haynes-ops", "claude", fixedNow), fixedNow)
	if res.Decision != DecisionCandidate {
		t.Fatalf("decision %s predicates %+v", res.Decision, res.FailedPredicates)
	}
	if len(res.FailedPredicates) != 0 || !res.Replay == false {
		t.Fatalf("predicates %+v replay %v", res.FailedPredicates, res.Replay)
	}
	if res.MatchedIDs.OperationID != "op-174" || res.MatchedIDs.AttemptID != "attempt-1" || len(res.MatchedIDs.Alerts) != 1 || len(res.MatchedIDs.Services) != 1 {
		t.Fatalf("ids %+v", res.MatchedIDs)
	}
	if res.Provenance.Actual.Model != ProvenanceUnknown || res.Provenance.Actual.Provider != ProvenanceUnknown || res.Provenance.Requested.Model != "claude-opus-5-5" {
		t.Fatalf("provenance %+v", res.Provenance)
	}
	if res.NextReevaluationDeadline == "" {
		t.Fatal("missing reevaluation deadline")
	}
}

func TestCorrelationCases(t *testing.T) {
	other := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	cases := []struct {
		name   string
		mutate func(*checkDoc)
		want   string
		pred   string
	}{
		{name: "wrong owner uid", mutate: func(d *checkDoc) { d.Snapshot.Owner.UID = "uid-2000" }, want: DecisionRespond, pred: "conflicting-owner"},
		{name: "claim owner differs", mutate: func(d *checkDoc) { d.Envelope.Claim.OwnerUID = "uid-2000" }, want: DecisionRespond, pred: "conflicting-owner"},
		{name: "wrong attempt", mutate: func(d *checkDoc) { d.Snapshot.Owner.AttemptID = "attempt-2" }, want: DecisionRespond, pred: "attempt-mismatch"},
		{name: "wrong revision", mutate: func(d *checkDoc) {
			d.Snapshot.Revision = other
			d.Envelope.Observation.Revision = other
		}, want: DecisionRespond, pred: "revision-mismatch"},
		{name: "wrong service", mutate: func(d *checkDoc) { d.Snapshot.Alerts[0].Service = "gatus" }, want: DecisionRespond, pred: "service-mismatch"},
		{name: "stale owner state", mutate: func(d *checkDoc) {
			d.Snapshot.Owner.State = "stale"
			d.Envelope.Observation.State = "stale"
		}, want: DecisionRespond, pred: "owner-stale"},
		{name: "stale observation", mutate: func(d *checkDoc) {
			setTimes(d, fixedNow.Add(-10*time.Minute), fixedNow.Add(-121*time.Second), fixedNow.Add(30*time.Minute), fixedNow.Add(time.Hour))
			d.Envelope.Observation.HeartbeatIntervalSeconds = 120
		}, want: DecisionRespond, pred: "owner-stale"},
		{name: "failed owner", mutate: func(d *checkDoc) {
			d.Snapshot.Owner.State = "failed"
			d.Envelope.Observation.State = "failed"
		}, want: DecisionRespond, pred: "owner-failed"},
		{name: "stopped owner", mutate: func(d *checkDoc) {
			d.Snapshot.Owner.State = "stopped"
			d.Envelope.Observation.State = "stopped"
		}, want: DecisionRespond, pred: "owner-stopped"},
		{name: "lost owner", mutate: func(d *checkDoc) {
			d.Snapshot.Owner.State = "lost"
			d.Envelope.Observation.State = "lost"
		}, want: DecisionRespond, pred: "owner-lost"},
		{name: "unexpected label", mutate: func(d *checkDoc) {
			d.Snapshot.Alerts[0].Labels = map[string]string{"alertname": "SomethingElse", "namespace": "home-automation"}
		}, want: DecisionRespond, pred: "unexpected-label"},
		{name: "unexpected severity", mutate: func(d *checkDoc) { d.Snapshot.Alerts[0].Severity = "critical" }, want: DecisionRespond, pred: "unexpected-severity"},
		{name: "unexpected symptom", mutate: func(d *checkDoc) { d.Snapshot.Alerts[0].Symptom = "crash" }, want: DecisionRespond, pred: "unexpected-symptom"},
		{name: "exceeded impact", mutate: func(d *checkDoc) { d.Snapshot.Impact.AffectedServices = 9 }, want: DecisionRespond, pred: "impact-exceeded"},
		{name: "unknown baseline", mutate: func(d *checkDoc) {
			d.Envelope.Baselines[0].Health = "unknown"
			d.Envelope.Baselines[0].Symptom = ""
			d.Envelope.Baselines[0].Severity = ""
		}, want: DecisionInsufficient, pred: "unknown-baseline"},
		{name: "missing baseline", mutate: func(d *checkDoc) { d.Envelope.Baselines = nil }, want: DecisionInsufficient, pred: "missing-baseline"},
		{name: "absent authority", mutate: func(d *checkDoc) { d.Envelope.Actions.AuthorizationEvidence = nil }, want: DecisionInsufficient, pred: "absent-authority-reference"},
		{name: "action outside scope", mutate: func(d *checkDoc) { d.Snapshot.RequestedAction = "rollback" }, want: DecisionRespond, pred: "action-outside-scope"},
		{name: "metadata unavailable", mutate: func(d *checkDoc) { d.Snapshot.Metadata = "unavailable" }, want: DecisionInsufficient, pred: "metadata-unavailable"},
		{name: "launch denied", mutate: func(d *checkDoc) { d.Snapshot.Launch = "denied" }, want: DecisionRespond, pred: "launch-denied"},
		{name: "launch uncertain", mutate: func(d *checkDoc) { d.Snapshot.Launch = "uncertain" }, want: DecisionRespond, pred: "launch-uncertain"},
		{name: "planned", mutate: func(d *checkDoc) { d.Envelope.State = "planned" }, want: DecisionRespond, pred: "operation-not-active"},
		{name: "unknown platform field", mutate: func(d *checkDoc) { d.Snapshot.Adapter.GatusStatus = "unknown" }, want: DecisionInsufficient, pred: "adapter-metadata-unknown"},
		{name: "denied and unavailable", mutate: func(d *checkDoc) {
			d.Snapshot.Launch = "denied"
			d.Snapshot.Metadata = "unavailable"
		}, want: DecisionRespond, pred: "launch-denied"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := sample(t, "haynes-ops", "claude", fixedNow)
			tc.mutate(&doc)
			res := mustCheck(t, doc, fixedNow)
			if res.Decision != tc.want || !hasPred(res.FailedPredicates, tc.pred) || res.Retry {
				t.Fatalf("got %s retry %v preds %+v", res.Decision, res.Retry, res.FailedPredicates)
			}
		})
	}
}

func TestExpiryEdge(t *testing.T) {
	expiry := time.Date(2026, 10, 23, 6, 0, 0, 0, time.UTC)
	doc := sample(t, "haynes-ops", "claude", expiry)
	setTimes(&doc, expiry.Add(-30*time.Minute), expiry.Add(-30*time.Second), expiry, expiry.Add(time.Hour))
	doc.Envelope.Observation.HeartbeatIntervalSeconds = 60
	open := mustCheck(t, doc, expiry.Add(-time.Nanosecond))
	if open.Decision != DecisionCandidate {
		t.Fatalf("before expiry: %s %+v", open.Decision, open.FailedPredicates)
	}
	closed := mustCheck(t, doc, expiry)
	if closed.Decision != DecisionRespond || !hasPred(closed.FailedPredicates, "activity-expired") || hasPred(closed.FailedPredicates, "owner-stale") {
		t.Fatalf("at expiry: %+v", closed.FailedPredicates)
	}
}

func TestFutureAndTolerance(t *testing.T) {
	doc := sample(t, "haynes-ops", "claude", fixedNow)
	setTimes(&doc, fixedNow.Add(-time.Minute), fixedNow.Add(6*time.Second), fixedNow.Add(time.Hour), fixedNow.Add(2*time.Hour))
	if _, err := Check(mustJSON(t, doc), fixedNow); err == nil || !strings.Contains(err.Error(), "future beyond the 5s") {
		t.Fatalf("future: %v", err)
	}
	ok := sample(t, "haynes-ops", "claude", fixedNow)
	setTimes(&ok, fixedNow.Add(-time.Minute), fixedNow.Add(5*time.Second), fixedNow.Add(time.Hour), fixedNow.Add(2*time.Hour))
	res := mustCheck(t, ok, fixedNow)
	if res.Decision != DecisionCandidate {
		t.Fatalf("tolerance: %s %+v", res.Decision, res.FailedPredicates)
	}
}

func TestWindowLimits(t *testing.T) {
	three := sample(t, "haynes-ops", "claude", fixedNow)
	setTimes(&three, fixedNow, fixedNow, fixedNow.Add(3*time.Hour), fixedNow.Add(4*time.Hour))
	if res := mustCheck(t, three, fixedNow); res.Decision != DecisionCandidate {
		t.Fatalf("3h window: %s %+v", res.Decision, res.FailedPredicates)
	}
	nine := sample(t, "haynes-ops", "claude", fixedNow)
	setTimes(&nine, fixedNow, fixedNow, fixedNow.Add(9*time.Hour), fixedNow.Add(10*time.Hour))
	if _, err := Check(mustJSON(t, nine), fixedNow); err == nil || !strings.Contains(err.Error(), "8h") {
		t.Fatalf("9h: %v", err)
	}
	shorter := sample(t, "haynes-ops", "claude", fixedNow)
	shorter.Envelope.Task.Deadline = fixedNow.Add(10 * time.Minute).Format(time.RFC3339Nano)
	if _, err := Check(mustJSON(t, shorter), fixedNow); err == nil || !strings.Contains(err.Error(), "shorter limit") {
		t.Fatalf("deadline: %v", err)
	}
	cluster := sample(t, "haynes-ops", "claude", fixedNow)
	cluster.Envelope.Targets.Services = []string{"cluster"}
	cluster.Envelope.ExpectedAlerts[0].Service = "cluster"
	cluster.Envelope.Baselines[0].Service = "cluster"
	cluster.Envelope.Rollback.Scope = []string{"cluster"}
	cluster.Snapshot.Alerts[0].Service = "cluster"
	if _, err := Check(mustJSON(t, cluster), fixedNow); err == nil || !strings.Contains(err.Error(), "cluster wildcard") {
		t.Fatalf("cluster: %v", err)
	}
}

func TestPreExisting(t *testing.T) {
	doc := sample(t, "haynes-ops", "claude", fixedNow)
	doc.Envelope.Baselines[0].Health = "degraded"
	doc.Envelope.Baselines[0].Symptom = "disk"
	doc.Envelope.Baselines[0].Severity = "warning"
	res := mustCheck(t, doc, fixedNow)
	if res.Decision != DecisionCandidate || !hasVisible(res.VisibleConditions, "pre-existing-degradation") {
		t.Fatalf("degraded baseline candidate: %s %+v %+v", res.Decision, res.FailedPredicates, res.VisibleConditions)
	}
	doc.Snapshot.Alerts[0].Symptom = "disk"
	again := mustCheck(t, doc, fixedNow)
	if again.Decision != DecisionRespond || !hasPred(again.FailedPredicates, "unexpected-symptom") || !hasVisible(again.VisibleConditions, "pre-existing-degradation") {
		t.Fatalf("pre-existing alert: %s %+v", again.Decision, again.FailedPredicates)
	}
	for _, item := range again.VisibleConditions {
		if item.Kind == "pre-existing-degradation" && !strings.Contains(item.Detail, "not presumed") {
			t.Fatalf("detail %q", item.Detail)
		}
	}
}

func TestReplay(t *testing.T) {
	doc := sample(t, "haynes-ops", "claude", fixedNow)
	raw := mustJSON(t, doc)
	sum, err := CanonicalDigest(raw, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	doc.PriorAttempt = &priorAttempt{AttemptID: doc.Envelope.AttemptID, PayloadDigest: sum}
	replay := mustCheck(t, doc, fixedNow)
	if replay.Decision != DecisionCandidate || !replay.Replay {
		t.Fatalf("replay: %+v", replay)
	}
	changed := sample(t, "haynes-ops", "claude", fixedNow)
	changed.Envelope.Activity.Expiry = fixedNow.Add(50 * time.Minute).Format(time.RFC3339Nano)
	changed.PriorAttempt = &priorAttempt{AttemptID: changed.Envelope.AttemptID, PayloadDigest: sum}
	conflict := mustCheck(t, changed, fixedNow)
	if conflict.Decision != DecisionRespond || !hasPred(conflict.FailedPredicates, "conflicting-payload") || conflict.Replay {
		t.Fatalf("conflict: %+v", conflict.FailedPredicates)
	}
	again, err := Check(raw, fixedNow)
	if err != nil || again.Decision != DecisionCandidate || again.NextReevaluationDeadline != replay.NextReevaluationDeadline {
		t.Fatalf("original changed: %v %+v", err, again)
	}
}

func TestTerminal(t *testing.T) {
	cases := []struct {
		state, cleanup, pred string
		failed               bool
	}{
		{state: "completed", cleanup: "failed", pred: "terminal-completed", failed: true},
		{state: "rolled_back", cleanup: "succeeded", pred: "terminal-rolled-back", failed: false},
		{state: "failed", cleanup: "failed", pred: "terminal-failed", failed: true},
	}
	for _, tc := range cases {
		t.Run(tc.state, func(t *testing.T) {
			doc := sample(t, "haynes-ops", "claude", fixedNow)
			asTerminal(&doc, tc.state, tc.cleanup, fixedNow)
			res := mustCheck(t, doc, fixedNow)
			if res.Decision != DecisionRespond || !hasPred(res.FailedPredicates, tc.pred) || hasPred(res.FailedPredicates, "cleanup-failed") != tc.failed {
				t.Fatalf("%+v", res.FailedPredicates)
			}
		})
	}
}

func TestProvenance(t *testing.T) {
	unknown := mustCheck(t, sample(t, "haynes-ops", "claude", fixedNow), fixedNow)
	if unknown.Provenance.Actual != (ModelOut{Provider: ProvenanceUnknown, Model: ProvenanceUnknown, State: ProvenanceUnknown}) {
		t.Fatalf("unknown %+v", unknown.Provenance.Actual)
	}
	partial := sample(t, "haynes-ops", "claude", fixedNow)
	partial.Envelope.Provenance.Actual.Provider = "claude"
	res := mustCheck(t, partial, fixedNow)
	if res.Decision != DecisionCandidate || res.Provenance.Actual.Provider != "claude" || res.Provenance.Actual.Model != ProvenanceUnknown || res.Provenance.Actual.State != ProvenanceUnknown {
		t.Fatalf("partial %+v %s", res.Provenance.Actual, res.Decision)
	}
	if res.Provenance.Actual.Model == res.Provenance.Requested.Model {
		t.Fatal("actual model was copied from the request")
	}
	alias := sample(t, "haynes-ops", "claude", fixedNow)
	alias.Envelope.Provenance.Requested.Model = "opus"
	if _, err := Check(mustJSON(t, alias), fixedNow); err == nil || !strings.Contains(err.Error(), "full model id") {
		t.Fatalf("alias: %v", err)
	}
}

func TestProviderRepoParity(t *testing.T) {
	type view struct {
		Decision string
		Failed   []string
		Alerts   []int
		Services []string
	}
	var got []view
	for _, repo := range []string{"haynes-ops", "dev-env"} {
		for _, provider := range []string{"claude", "codex"} {
			res := mustCheck(t, sample(t, repo, provider, fixedNow), fixedNow)
			if res.Decision != DecisionCandidate {
				t.Fatalf("%s %s: %s %+v", provider, repo, res.Decision, res.FailedPredicates)
			}
			text := conditionsText(res.VisibleConditions)
			if repo == "dev-env" && !strings.Contains(text, "NotApplicable") {
				t.Fatalf("dev-env adapter text %q", text)
			}
			if repo == "haynes-ops" && strings.Contains(text, "NotApplicable") {
				t.Fatalf("haynes-ops adapter text %q", text)
			}
			got = append(got, view{Decision: res.Decision, Failed: names(res.FailedPredicates), Alerts: res.MatchedIDs.Alerts, Services: res.MatchedIDs.Services})
		}
	}
	for _, item := range got[1:] {
		if !reflect.DeepEqual(item, got[0]) {
			t.Fatalf("core mismatch %+v vs %+v", got[0], item)
		}
	}
}

func TestSyntaxAndLimits(t *testing.T) {
	now := fixedNow
	read := func(name string) []byte {
		t.Helper()
		b, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	if _, err := Check(read("duplicate-key.json"), now); err == nil || !strings.Contains(err.Error(), "duplicate key") {
		t.Fatalf("duplicate: %v", err)
	}
	if _, err := Check(read("trailing.json"), now); err == nil || !strings.Contains(err.Error(), "trailing JSON") {
		t.Fatalf("trailing: %v", err)
	}
	if _, err := Check(read("self-authorized.json"), now); err == nil || !strings.Contains(err.Error(), "cannot grant authority") {
		t.Fatalf("self: %v", err)
	}
	raw := mustJSON(t, sample(t, "haynes-ops", "claude", now))
	if _, err := Check(insertField(raw, `"authorized":true`), now); err == nil || !strings.Contains(err.Error(), "cannot grant authority") {
		t.Fatalf("inserted auth: %v", err)
	}
	if _, err := Check(insertField(raw, `"prompt":"run"`), now); err == nil || !strings.Contains(err.Error(), "unrecognized field") {
		t.Fatalf("prompt: %v", err)
	}
	if _, err := Check(append(append([]byte{}, raw...), []byte("{}")...), now); err == nil || !strings.Contains(err.Error(), "trailing JSON") {
		t.Fatalf("appended: %v", err)
	}
	if _, err := Check(bytes.Repeat([]byte(" "), MaxDocumentBytes+1), now); err == nil || !strings.Contains(err.Error(), "proposed limit") {
		t.Fatalf("size: %v", err)
	}
	wide := sample(t, "haynes-ops", "claude", now)
	wide.Envelope.Targets.Services = make([]string, 33)
	for i := range wide.Envelope.Targets.Services {
		wide.Envelope.Targets.Services[i] = "svc" + strconvI(i+1)
	}
	if _, err := Check(mustJSON(t, wide), now); err == nil || !strings.Contains(err.Error(), "32") {
		t.Fatalf("entries: %v", err)
	}
	hb := sample(t, "haynes-ops", "claude", now)
	hb.Envelope.Observation.HeartbeatIntervalSeconds = 121
	if _, err := Check(mustJSON(t, hb), now); err == nil || !strings.Contains(err.Error(), "120") {
		t.Fatalf("heartbeat: %v", err)
	}
}

func TestDigestNormalizesTime(t *testing.T) {
	raw := mustJSON(t, sample(t, "haynes-ops", "claude", fixedNow))
	from := []byte(`"start":"2026-10-23T05:00:00Z"`)
	to := []byte(`"start":"2026-10-23T05:00:00.000Z"`)
	if bytes.Count(raw, from) != 1 {
		t.Fatalf("start count %d in %s", bytes.Count(raw, from), raw)
	}
	left, err := CanonicalDigest(raw, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	right, err := CanonicalDigest(bytes.Replace(raw, from, to, 1), fixedNow)
	if err != nil || left != right {
		t.Fatalf("digest %s vs %s err %v", left, right, err)
	}
}

func TestPure(t *testing.T) {
	raw := mustJSON(t, sample(t, "dev-env", "codex", fixedNow))
	before := append([]byte(nil), raw...)
	first, err := Check(raw, fixedNow)
	second, err2 := Check(raw, fixedNow)
	if err != nil || err2 != nil || !bytes.Equal(raw, before) {
		t.Fatal(err, err2)
	}
	left, _ := json.Marshal(first)
	right, _ := json.Marshal(second)
	if !bytes.Equal(left, right) || first.Decision != DecisionCandidate {
		t.Fatalf("unstable %s %s", left, right)
	}
}

func TestDocsExamples(t *testing.T) {
	dir := filepath.Join("..", "..", "docs", "examples", "upgrade-shepherd")
	for _, repo := range []string{"haynes-ops", "dev-env"} {
		for _, provider := range []string{"claude", "codex"} {
			body := mustJSON(t, sample(t, repo, provider, fixedNow))
			path := filepath.Join(dir, provider+"-"+repo+".json")
			if os.Getenv("UPDATE_UPGRADE_EXAMPLES") == "1" {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, body, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, body) {
				t.Fatalf("%s drifted from the synthetic builder", path)
			}
			res, err := Check(got, fixedNow)
			if err != nil || res.Decision != DecisionCandidate || res.CandidateIsOperationalDeferral || res.AuthorizationGranted {
				t.Fatalf("%s: %v %+v", path, err, res)
			}
		}
	}
}

func TestRoleCorePinned(t *testing.T) {
	core := roleCore(t)
	if !strings.Contains(core, "not alert deferral") || !strings.Contains(core, "NotApplicable") {
		t.Fatalf("core %q", core)
	}
	sum := sha256.Sum256([]byte(core))
	want := "sha256:" + hex.EncodeToString(sum[:])
	doc := sample(t, "haynes-ops", "claude", fixedNow)
	if doc.Envelope.Role.ContentDigest != want {
		t.Fatalf("digest %s want %s", doc.Envelope.Role.ContentDigest, want)
	}
}

func sample(t *testing.T, repo, provider string, now time.Time) checkDoc {
	t.Helper()
	start := now.Add(-time.Minute)
	obs := now.Add(-30 * time.Second)
	model := "claude-opus-5-5"
	if provider == "codex" {
		model = "gpt-6-astra"
	}
	runtime := "applicable"
	adapter := adapterSnap{Repository: repo, Provider: provider, RuntimeHealth: "healthy", Reconciliation: "ready", FluxRevision: rev, GatusStatus: "healthy", RenovateTier: "patch"}
	if repo == "dev-env" {
		runtime = "not-applicable"
		adapter = adapterSnap{Repository: repo, Provider: provider, RuntimeHealth: "NotApplicable", Reconciliation: "NotApplicable", SourceCI: "success", NativeReview: "recorded"}
	}
	labels := map[string]string{"alertname": "AppDaemonRestart", "namespace": "home-automation"}
	sum := sha256.Sum256([]byte(roleCore(t)))
	return checkDoc{
		Envelope: envelope{
			ContractVersion:      ContractVersion,
			OperationID:          "op-174",
			AttemptID:            "attempt-1",
			PredecessorAttemptID: "",
			WorkPackageURL:       "https://example.test/work-packages/174",
			Role:                 roleRef{Version: RoleVersion, ContentDigest: "sha256:" + hex.EncodeToString(sum[:])},
			Repository: repository{
				Identity:         "github.com/thaynes43/" + repo,
				DesiredRevisions: []string{rev},
				Adapter:          adapterRef{Name: repo, Version: "fixture-v1", Digest: digestText(repo + "-fixture-v1"), Runtime: runtime},
			},
			Session:  sessionRef{Namespace: "dev-agents", Name: "dev-env-example", UID: "uid-1000"},
			Claim:    claimRef{Reference: "claim-174", OwnerUID: "uid-1000"},
			Executor: executorRef{Identity: "dev"},
			Custody:  custody{HandoffEvidence: []string{}},
			Provenance: provenance{
				Requested: modelIn{Provider: provider, Model: model, Effort: "high", State: "configured"},
			},
			Actions:        actions{Allowed: []string{"inspect", "reconcile", "postcheck"}, AuthorizationEvidence: []string{"https://example.test/authority/174"}},
			Targets:        targets{Services: []string{"appdaemon"}, Dependencies: []string{"mosquitto"}},
			Activity:       activityRef{UID: "activity-1", Start: start.Format(time.RFC3339Nano), Expiry: start.Add(45 * time.Minute).Format(time.RFC3339Nano)},
			Task:           taskRef{Deadline: start.Add(time.Hour).Format(time.RFC3339Nano), Impact: impact{MaxServices: 2, MaxSeverity: "warning", WindowSeconds: 300}},
			Authorization:  authorization{LimitExpiry: start.Add(time.Hour).Format(time.RFC3339Nano)},
			Baselines:      []baseline{{Service: "appdaemon", ObservedAt: start.Add(-time.Minute).Format(time.RFC3339Nano), Health: "healthy", Revision: pre}},
			ExpectedAlerts: []expectedAlert{{Service: "appdaemon", Labels: labels, Resource: "deployment/appdaemon", Severity: "warning", Symptom: "restart", ImpactIntervalSeconds: 120}},
			Observation:    observation{OwnerUID: "uid-1000", ObservedAt: obs.Format(time.RFC3339Nano), ProgressAt: obs.Format(time.RFC3339Nano), HeartbeatIntervalSeconds: 60, State: "active", Revision: rev, AttemptID: "attempt-1"},
			Rollback:       rollback{Trigger: "postcheck-failed", Owner: "uid-1000", Scope: []string{"appdaemon"}},
			Terminal:       terminal{PostcheckEvidence: []string{}, UnresolvedSymptoms: []string{}},
			Evidence:       []evidenceRef{{URL: "https://example.test/work-packages/174", Kind: "work-package"}},
			State:          "active",
		},
		Snapshot: snapshot{
			Alerts:          []observedAlert{{Service: "appdaemon", Labels: map[string]string{"alertname": "AppDaemonRestart", "namespace": "home-automation"}, Resource: "deployment/appdaemon", Severity: "warning", Symptom: "restart", StartedAt: obs.Format(time.RFC3339Nano)}},
			Owner:           ownerSnap{UID: "uid-1000", SessionUID: "uid-1000", ClaimReference: "claim-174", State: "active", ObservedAt: obs.Format(time.RFC3339Nano), AttemptID: "attempt-1"},
			Revision:        rev,
			Metadata:        "available",
			Launch:          "ok",
			RequestedAction: "reconcile",
			Impact:          impactSnap{AffectedServices: 1, Severity: "warning", IntervalSeconds: 120},
			Adapter:         adapter,
		},
	}
}

func setTimes(doc *checkDoc, start, obs, expiry, deadline time.Time) {
	doc.Envelope.Activity.Start = start.Format(time.RFC3339Nano)
	doc.Envelope.Activity.Expiry = expiry.Format(time.RFC3339Nano)
	doc.Envelope.Task.Deadline = deadline.Format(time.RFC3339Nano)
	doc.Envelope.Authorization.LimitExpiry = deadline.Format(time.RFC3339Nano)
	doc.Envelope.Observation.ObservedAt = obs.Format(time.RFC3339Nano)
	doc.Envelope.Observation.ProgressAt = obs.Format(time.RFC3339Nano)
	doc.Snapshot.Owner.ObservedAt = obs.Format(time.RFC3339Nano)
	doc.Envelope.Baselines[0].ObservedAt = start.Add(-time.Minute).Format(time.RFC3339Nano)
	doc.Snapshot.Alerts[0].StartedAt = obs.Format(time.RFC3339Nano)
}

func asTerminal(doc *checkDoc, state, cleanup string, now time.Time) {
	doc.Envelope.State = state
	doc.Envelope.Terminal.Outcome = state
	doc.Envelope.Terminal.ActivityCleanup = cleanup
	doc.Envelope.Terminal.ReceiptAt = now.Format(time.RFC3339Nano)
	switch state {
	case "completed", "rolled_back":
		doc.Envelope.Terminal.PostcheckEvidence = []string{"https://example.test/postcheck/1"}
	default:
		doc.Envelope.Terminal.UnresolvedSymptoms = []string{"crash"}
	}
}

func mustCheck(t *testing.T, doc checkDoc, now time.Time) Result {
	t.Helper()
	res, err := Check(mustJSON(t, doc), now)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if res.Retry || res.AuthorizationGranted || res.CandidateIsOperationalDeferral || !res.UnverifiedInput || res.OfflineEvidence != OfflineEvidenceUnverified || res.Notice != Notice {
		t.Fatalf("closure %+v", res)
	}
	return res
}

func mustJSON(t *testing.T, doc checkDoc) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func roleCore(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "upgrade-shepherd-role.md"))
	if err != nil {
		t.Fatal(err)
	}
	const start = "<!-- upgrade-shepherd-role-core:start -->\n"
	const end = "\n<!-- upgrade-shepherd-role-core:end -->"
	i := bytes.Index(b, []byte(start))
	j := bytes.Index(b, []byte(end))
	if i < 0 || j < 0 || j < i {
		t.Fatal("role core markers missing")
	}
	return string(b[i+len(start) : j])
}

func digestText(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func insertField(raw []byte, field string) []byte {
	out := make([]byte, 0, len(raw)+len(field)+1)
	out = append(out, '{')
	out = append(out, field...)
	out = append(out, ',')
	return append(out, raw[1:]...)
}

func hasPred(items []Predicate, name string) bool {
	for _, item := range items {
		if item.Name == name {
			return true
		}
	}
	return false
}

func hasVisible(items []Condition, kind string) bool {
	for _, item := range items {
		if item.Kind == kind {
			return true
		}
	}
	return false
}

func names(items []Predicate) []string {
	out := []string{}
	for _, item := range items {
		out = append(out, item.Name)
	}
	return out
}

func conditionsText(items []Condition) string {
	var b strings.Builder
	for _, item := range items {
		b.WriteString(item.Detail)
		b.WriteByte('\n')
	}
	return b.String()
}

func strconvI(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return strconvI(n/10) + string(rune('0'+n%10))
}
