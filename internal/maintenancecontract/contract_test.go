package maintenancecontract

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
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

func TestAlertPredatesOperation(t *testing.T) {
	start := fixedNow.Add(-time.Minute)

	t.Run("one nanosecond before start", func(t *testing.T) {
		doc := sample(t, "haynes-ops", "claude", fixedNow)
		doc.Snapshot.Alerts[0].StartedAt = start.Add(-time.Nanosecond).Format(time.RFC3339Nano)
		res := mustCheck(t, doc, fixedNow)
		assertPredates(t, res, 0, "appdaemon")
		if len(res.MatchedIDs.Alerts) != 0 {
			t.Fatalf("matched %+v", res.MatchedIDs.Alerts)
		}
	})

	t.Run("before healthy baseline", func(t *testing.T) {
		doc := sample(t, "haynes-ops", "claude", fixedNow)
		doc.Snapshot.Alerts[0].StartedAt = start.Add(-2 * time.Minute).Format(time.RFC3339Nano)
		res := mustCheck(t, doc, fixedNow)
		assertPredates(t, res, 0, "appdaemon")
		if res.Decision == DecisionCandidate || len(res.MatchedIDs.Alerts) != 0 {
			t.Fatalf("decision %s matched %+v", res.Decision, res.MatchedIDs.Alerts)
		}
	})

	t.Run("equal start stays eligible", func(t *testing.T) {
		doc := sample(t, "haynes-ops", "claude", fixedNow)
		doc.Snapshot.Alerts[0].StartedAt = start.Format(time.RFC3339Nano)
		res := mustCheck(t, doc, fixedNow)
		if res.Decision != DecisionCandidate || hasPred(res.FailedPredicates, "alert-predates-operation") || !sameInts(res.MatchedIDs.Alerts, []int{0}) {
			t.Fatalf("equal %s %+v matched %+v", res.Decision, res.FailedPredicates, res.MatchedIDs.Alerts)
		}
	})

	t.Run("later start stays eligible", func(t *testing.T) {
		doc := sample(t, "haynes-ops", "claude", fixedNow)
		doc.Snapshot.Alerts[0].StartedAt = start.Add(time.Second).Format(time.RFC3339Nano)
		res := mustCheck(t, doc, fixedNow)
		if res.Decision != DecisionCandidate || hasPred(res.FailedPredicates, "alert-predates-operation") || !sameInts(res.MatchedIDs.Alerts, []int{0}) {
			t.Fatalf("later %s %+v matched %+v", res.Decision, res.FailedPredicates, res.MatchedIDs.Alerts)
		}
	})

	t.Run("equal start still needs other predicates", func(t *testing.T) {
		doc := sample(t, "haynes-ops", "claude", fixedNow)
		doc.Snapshot.Alerts[0].StartedAt = start.Format(time.RFC3339Nano)
		doc.Snapshot.Alerts[0].Symptom = "crash"
		res := mustCheck(t, doc, fixedNow)
		if res.Decision != DecisionRespond || hasPred(res.FailedPredicates, "alert-predates-operation") || !hasPred(res.FailedPredicates, "unexpected-symptom") || len(res.MatchedIDs.Alerts) != 0 {
			t.Fatalf("equal other %s %+v matched %+v", res.Decision, res.FailedPredicates, res.MatchedIDs.Alerts)
		}
	})

	t.Run("mixed early then valid", func(t *testing.T) {
		doc := sample(t, "haynes-ops", "claude", fixedNow)
		early := doc.Snapshot.Alerts[0]
		early.StartedAt = start.Add(-time.Nanosecond).Format(time.RFC3339Nano)
		valid := doc.Snapshot.Alerts[0]
		valid.StartedAt = start.Format(time.RFC3339Nano)
		doc.Snapshot.Alerts = []observedAlert{early, valid}
		res := mustCheck(t, doc, fixedNow)
		assertPredates(t, res, 0, "appdaemon")
		if predatesIndex(res, 1) || !sameInts(res.MatchedIDs.Alerts, []int{1}) {
			t.Fatalf("preds %+v matched %+v", res.FailedPredicates, res.MatchedIDs.Alerts)
		}
	})

	t.Run("mixed valid then early", func(t *testing.T) {
		doc := sample(t, "haynes-ops", "claude", fixedNow)
		valid := doc.Snapshot.Alerts[0]
		valid.StartedAt = start.Add(time.Second).Format(time.RFC3339Nano)
		early := doc.Snapshot.Alerts[0]
		early.StartedAt = start.Add(-time.Nanosecond).Format(time.RFC3339Nano)
		doc.Snapshot.Alerts = []observedAlert{valid, early}
		res := mustCheck(t, doc, fixedNow)
		assertPredates(t, res, 1, "appdaemon")
		if predatesIndex(res, 0) || !sameInts(res.MatchedIDs.Alerts, []int{0}) {
			t.Fatalf("preds %+v matched %+v", res.FailedPredicates, res.MatchedIDs.Alerts)
		}
	})

	t.Run("early out of scope stays visible beside later match", func(t *testing.T) {
		doc := sample(t, "haynes-ops", "claude", fixedNow)
		early := doc.Snapshot.Alerts[0]
		early.Service = "gatus"
		early.StartedAt = start.Add(-time.Minute).Format(time.RFC3339Nano)
		valid := doc.Snapshot.Alerts[0]
		valid.StartedAt = start.Add(time.Second).Format(time.RFC3339Nano)
		doc.Snapshot.Alerts = []observedAlert{early, valid}
		res := mustCheck(t, doc, fixedNow)
		if res.Decision != DecisionRespond || !predatesIndex(res, 0) || !hasPred(res.FailedPredicates, "service-mismatch") {
			t.Fatalf("preds %+v", res.FailedPredicates)
		}
		if !sameInts(res.MatchedIDs.Alerts, []int{1}) || !visibleMentions(res, "gatus", 0) {
			t.Fatalf("matched %+v visible %+v", res.MatchedIDs.Alerts, res.VisibleConditions)
		}
	})

	t.Run("early unknown baseline stays visible", func(t *testing.T) {
		doc := sample(t, "haynes-ops", "claude", fixedNow)
		doc.Envelope.Baselines[0].Health = "unknown"
		doc.Envelope.Baselines[0].Symptom = ""
		doc.Envelope.Baselines[0].Severity = ""
		early := doc.Snapshot.Alerts[0]
		early.StartedAt = start.Add(-time.Nanosecond).Format(time.RFC3339Nano)
		later := doc.Snapshot.Alerts[0]
		later.StartedAt = start.Format(time.RFC3339Nano)
		doc.Snapshot.Alerts = []observedAlert{early, later}
		res := mustCheck(t, doc, fixedNow)
		if !predatesIndex(res, 0) || !hasPred(res.FailedPredicates, "unknown-baseline") || len(res.MatchedIDs.Alerts) != 0 {
			t.Fatalf("preds %+v matched %+v", res.FailedPredicates, res.MatchedIDs.Alerts)
		}
	})

	t.Run("early missing baseline stays visible", func(t *testing.T) {
		doc := sample(t, "haynes-ops", "claude", fixedNow)
		doc.Envelope.Baselines = nil
		doc.Snapshot.Alerts[0].StartedAt = start.Add(-time.Nanosecond).Format(time.RFC3339Nano)
		res := mustCheck(t, doc, fixedNow)
		if res.Decision != DecisionRespond || !predatesIndex(res, 0) || !hasPred(res.FailedPredicates, "missing-baseline") || len(res.MatchedIDs.Alerts) != 0 {
			t.Fatalf("decision %s preds %+v matched %+v", res.Decision, res.FailedPredicates, res.MatchedIDs.Alerts)
		}
	})
}

func TestBaselineObservedBeforeActivity(t *testing.T) {
	start := fixedNow.Add(-time.Minute)
	reject := func(t *testing.T, health string) {
		t.Helper()
		doc := sample(t, "haynes-ops", "claude", fixedNow)
		doc.Envelope.Baselines[0].Health = health
		if health == "degraded" {
			doc.Envelope.Baselines[0].Symptom = "disk"
			doc.Envelope.Baselines[0].Severity = "warning"
		}
		doc.Envelope.Baselines[0].ObservedAt = start.Add(time.Nanosecond).Format(time.RFC3339Nano)
		_, err := Check(mustJSON(t, doc), fixedNow)
		if err == nil || !strings.Contains(err.Error(), "envelope.baselines[0].observedAt: is after the activity start") {
			t.Fatalf("%s late baseline: %v", health, err)
		}
	}
	t.Run("healthy after start", func(t *testing.T) { reject(t, "healthy") })
	t.Run("degraded after start", func(t *testing.T) { reject(t, "degraded") })

	accept := func(t *testing.T, at time.Time, health string) {
		t.Helper()
		doc := sample(t, "haynes-ops", "claude", fixedNow)
		doc.Envelope.Baselines[0].Health = health
		doc.Envelope.Baselines[0].ObservedAt = at.Format(time.RFC3339Nano)
		if health == "degraded" {
			doc.Envelope.Baselines[0].Symptom = "disk"
			doc.Envelope.Baselines[0].Severity = "warning"
		}
		res, err := Check(mustJSON(t, doc), fixedNow)
		if err != nil {
			t.Fatalf("%s: %v", health, err)
		}
		if health == "healthy" && (res.Decision != DecisionCandidate || hasPred(res.FailedPredicates, "alert-predates-operation")) {
			t.Fatalf("healthy %s %+v", res.Decision, res.FailedPredicates)
		}
		if health == "degraded" && !hasVisible(res.VisibleConditions, "pre-existing-degradation") {
			t.Fatalf("degraded visible %+v", res.VisibleConditions)
		}
	}
	t.Run("healthy at start", func(t *testing.T) { accept(t, start, "healthy") })
	t.Run("healthy before start", func(t *testing.T) { accept(t, start.Add(-time.Minute), "healthy") })
	t.Run("degraded at start", func(t *testing.T) { accept(t, start, "degraded") })
	t.Run("degraded before start", func(t *testing.T) { accept(t, start.Add(-time.Minute), "degraded") })

	t.Run("unknown baseline semantics", func(t *testing.T) {
		doc := sample(t, "haynes-ops", "claude", fixedNow)
		doc.Envelope.Baselines[0].Health = "unknown"
		doc.Envelope.Baselines[0].ObservedAt = start.Format(time.RFC3339Nano)
		res := mustCheck(t, doc, fixedNow)
		if res.Decision != DecisionInsufficient || !hasPred(res.FailedPredicates, "unknown-baseline") || hasPred(res.FailedPredicates, "alert-predates-operation") {
			t.Fatalf("%s %+v", res.Decision, res.FailedPredicates)
		}
	})

	t.Run("missing baseline semantics", func(t *testing.T) {
		doc := sample(t, "haynes-ops", "claude", fixedNow)
		doc.Envelope.Baselines = nil
		res := mustCheck(t, doc, fixedNow)
		if res.Decision != DecisionInsufficient || !hasPred(res.FailedPredicates, "missing-baseline") || hasPred(res.FailedPredicates, "alert-predates-operation") {
			t.Fatalf("%s %+v", res.Decision, res.FailedPredicates)
		}
	})

	t.Run("future baseline still rejected", func(t *testing.T) {
		doc := sample(t, "haynes-ops", "claude", fixedNow)
		doc.Envelope.Baselines[0].ObservedAt = fixedNow.Add(6 * time.Second).Format(time.RFC3339Nano)
		_, err := Check(mustJSON(t, doc), fixedNow)
		if err == nil || !strings.Contains(err.Error(), "future beyond the 5s") {
			t.Fatalf("future baseline: %v", err)
		}
	})

	t.Run("malformed baseline still rejected", func(t *testing.T) {
		doc := sample(t, "haynes-ops", "claude", fixedNow)
		doc.Envelope.Baselines[0].ObservedAt = "not-a-time"
		_, err := Check(mustJSON(t, doc), fixedNow)
		if err == nil || !strings.Contains(err.Error(), "RFC3339") {
			t.Fatalf("malformed baseline: %v", err)
		}
	})
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

func TestExactSchemaNames(t *testing.T) {
	base := func() []byte {
		t.Helper()
		return mustJSON(t, sample(t, "haynes-ops", "claude", fixedNow))
	}
	attempt := `"operationId":"op-174","attemptId":"attempt-1","predecessorAttemptId"`
	noncanonical := `noncanonical field "AttemptId"; schema field is "attemptId"`
	mustSwap(t, base(), attempt, `"operationId":"op-174","attemptId":"attempt-1","AttemptId":"attempt-1","predecessorAttemptId"`, noncanonical)
	mustSwap(t, base(), attempt, `"operationId":"op-174","AttemptId":"attempt-1","attemptId":"attempt-1","predecessorAttemptId"`, noncanonical)
	mustSwap(t, base(), attempt, `"operationId":"op-174","AttemptId":"attempt-1","predecessorAttemptId"`, noncanonical)
	mustSwap(t, base(), attempt, `"operationId":"op-174","\u0041ttemptId":"attempt-1","predecessorAttemptId"`, noncanonical)
	mustSwap(t, base(), attempt, `"operationId":"op-174","attemptId":"attempt-1","\u0041ttemptId":"attempt-1","predecessorAttemptId"`, noncanonical)
	mustSwap(t, base(), attempt, `"operationId":"op-174","attemptId":"attempt-1","\u0061ttemptId":"attempt-1","predecessorAttemptId"`, "duplicate key")
	mustSwap(t, base(), `"heartbeatIntervalSeconds":60,"state":"active","revision":"0123456789abcdef0123456789abcdef01234567","attemptId":"attempt-1"`, `"heartbeatIntervalSeconds":60,"state":"active","revision":"0123456789abcdef0123456789abcdef01234567","AttemptId":"attempt-1"`, `envelope.observation.AttemptId: noncanonical field "AttemptId"`)
	mustSwap(t, base(), `"role":{"version":"upgrade-shepherd-role/v1"`, `"role":{"Version":"upgrade-shepherd-role/v1"`, `envelope.role.Version: noncanonical field "Version"; schema field is "version"`)
	mustSwap(t, base(), `"baselines":[{"service":"appdaemon"`, `"baselines":[{"Service":"appdaemon"`, `envelope.baselines[0].Service: noncanonical field "Service"; schema field is "service"`)
	mustSwap(t, base(), `"metadata":"available"`, `"Metadata":"available"`, `snapshot.Metadata: noncanonical field "Metadata"; schema field is "metadata"`)
	mustSwap(t, base(), `{"envelope":`, `{"Envelope":`, `Envelope: noncanonical field "Envelope"; schema field is "envelope"`)
	mustSwap(t, base(), `{"envelope":`, `{"Envelope":null,"envelope":`, `Envelope: noncanonical field "Envelope"; schema field is "envelope"`)
	mustSwap(t, base(), `"nativeReview":""}}}`, `"nativeReview":""}},"Envelope":null}`, `Envelope: noncanonical field "Envelope"; schema field is "envelope"`)

	var probe struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal([]byte("{\"\u017ftate\":\"active\"}"), &probe); err != nil || probe.State != "active" {
		t.Fatalf("Go decoder precondition: %v state=%q", err, probe.State)
	}
	mustSwap(t, base(), `"state":"active"},"snapshot"`, "\"\u017ftate\":\"active\"},\"snapshot\"", `schema field is "state"`)
	mustSwap(t, base(), `"state":"active"},"snapshot"`, `"\u017ftate":"active"},"snapshot"`, `schema field is "state"`)
	mustSwap(t, base(), `"state":"active"},"snapshot"`, "\"state\":\"active\",\"\u017ftate\":\"active\"},\"snapshot\"", `schema field is "state"`)

	doc := sample(t, "haynes-ops", "claude", fixedNow)
	doc.PriorAttempt = &priorAttempt{AttemptID: "attempt-1", PayloadDigest: "sha256:" + strings.Repeat("ab", 32)}
	mustCheck(t, doc, fixedNow)
	prior := mustJSON(t, doc)
	mustSwap(t, prior, `"priorAttempt":{"attemptId":"attempt-1","payloadDigest"`, `"priorAttempt":{"AttemptId":"attempt-1","payloadDigest"`, `priorAttempt.AttemptId: noncanonical field "AttemptId"`)
	mustSwap(t, prior, `"priorAttempt":{`, `"PriorAttempt":{`, `PriorAttempt: noncanonical field "PriorAttempt"; schema field is "priorAttempt"`)
}

func TestLabelCasePreserved(t *testing.T) {
	serviceOnly := map[string]string{"alertname": "AppDaemonRestart", "namespace": "home-automation", "service": "web"}
	serviceCase := map[string]string{"alertname": "AppDaemonRestart", "namespace": "home-automation", "Service": "web"}
	bothA := map[string]string{"alertname": "AppDaemonRestart", "namespace": "home-automation", "service": "web", "Service": "API"}
	bothB := map[string]string{"alertname": "AppDaemonRestart", "namespace": "home-automation", "service": "API", "Service": "web"}
	for _, labels := range []map[string]string{serviceOnly, serviceCase, bothA} {
		res := mustCheck(t, labelDoc(t, labels, labels), fixedNow)
		if res.Decision != DecisionCandidate {
			t.Fatalf("candidate %s %+v", res.Decision, res.FailedPredicates)
		}
	}
	for _, tc := range []struct {
		name     string
		expected map[string]string
		observed map[string]string
	}{
		{name: "key case", expected: serviceOnly, observed: serviceCase},
		{name: "value case", expected: bothA, observed: bothB},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := mustCheck(t, labelDoc(t, tc.expected, tc.observed), fixedNow)
			if res.Decision == DecisionCandidate || res.Decision != DecisionRespond || !hasPred(res.FailedPredicates, "unexpected-label") {
				t.Fatalf("got %s %+v", res.Decision, res.FailedPredicates)
			}
		})
	}
	left, err := CanonicalDigest(mustJSON(t, labelDoc(t, serviceOnly, serviceOnly)), fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	right, err := CanonicalDigest(mustJSON(t, labelDoc(t, serviceCase, serviceCase)), fixedNow)
	if err != nil || left == right {
		t.Fatalf("key digest %s %s err %v", left, right, err)
	}
	original, err := CanonicalDigest(mustJSON(t, labelDoc(t, bothA, bothA)), fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	swapped, err := CanonicalDigest(mustJSON(t, labelDoc(t, bothB, bothB)), fixedNow)
	if err != nil || original == swapped {
		t.Fatalf("value digest %s %s err %v", original, swapped, err)
	}
}

func labelDoc(t *testing.T, expected, observed map[string]string) checkDoc {
	t.Helper()
	doc := sample(t, "haynes-ops", "claude", fixedNow)
	envAlert := doc.Envelope.ExpectedAlerts[0]
	envAlert.Labels = expected
	doc.Envelope.ExpectedAlerts = []expectedAlert{envAlert}
	snapAlert := doc.Snapshot.Alerts[0]
	snapAlert.Labels = observed
	doc.Snapshot.Alerts = []observedAlert{snapAlert}
	return doc
}

func mustSwap(t *testing.T, raw []byte, old, neu, needle string) {
	t.Helper()
	_, err := Check(replaceOnce(t, raw, old, neu), fixedNow)
	if err == nil || !strings.Contains(err.Error(), needle) {
		t.Fatalf("swap %s: %v", needle, err)
	}
}

func replaceOnce(t *testing.T, raw []byte, old, neu string) []byte {
	t.Helper()
	if n := bytes.Count(raw, []byte(old)); n != 1 {
		tail := raw
		if len(tail) > 160 {
			tail = tail[len(tail)-160:]
		}
		t.Fatalf("count %d for %s tail %s", n, old, tail)
	}
	return bytes.Replace(raw, []byte(old), []byte(neu), 1)
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
			Targets:        targets{Services: []string{"appdaemon"}, Dependencies: []string{"broker"}},
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

func assertPredates(t *testing.T, res Result, index int, service string) {
	t.Helper()
	if res.Decision != DecisionRespond || !predatesIndex(res, index) || !visibleMentions(res, service, index) {
		t.Fatalf("decision %s preds %+v visible %+v", res.Decision, res.FailedPredicates, res.VisibleConditions)
	}
	for _, item := range res.FailedPredicates {
		if item.Name == "alert-predates-operation" && strings.Contains(item.Detail, "caused") {
			t.Fatalf("invented cause %q", item.Detail)
		}
	}
}

func predatesIndex(res Result, index int) bool {
	needle := "snapshot alert " + strconv.Itoa(index) + " "
	for _, item := range res.FailedPredicates {
		if item.Name == "alert-predates-operation" && strings.Contains(item.Detail, needle) && strings.Contains(item.Detail, "no cause is attributed") {
			return true
		}
	}
	return false
}

func visibleMentions(res Result, service string, index int) bool {
	needle := "index " + strconv.Itoa(index)
	for _, item := range res.VisibleConditions {
		if item.Kind == "pre-existing-degradation" && item.Service == service && strings.Contains(item.Detail, needle) && strings.Contains(item.Detail, "no cause is attributed") {
			return true
		}
	}
	return false
}

func sameInts(got, want []int) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
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
