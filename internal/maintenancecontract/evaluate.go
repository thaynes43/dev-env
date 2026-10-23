package maintenancecontract

import (
	"sort"
	"strconv"
	"time"
)

type accumulator struct {
	failed  []Predicate
	visible []Condition
	matched []int
	replay  bool
}

func (a *accumulator) respond(name, detail string) {
	a.failed = append(a.failed, Predicate{Name: name, Effect: DecisionRespond, Detail: detail})
}

func (a *accumulator) insufficient(name, detail string) {
	a.failed = append(a.failed, Predicate{Name: name, Effect: DecisionInsufficient, Detail: detail})
}

func (a *accumulator) show(kind, service, detail string) {
	a.visible = append(a.visible, Condition{Kind: kind, Service: service, Detail: detail})
}

func (a *accumulator) decision() string {
	respond := false
	insufficient := false
	for _, item := range a.failed {
		switch item.Effect {
		case DecisionRespond:
			respond = true
		case DecisionInsufficient:
			insufficient = true
		}
	}
	if respond {
		return DecisionRespond
	}
	if insufficient {
		return DecisionInsufficient
	}
	return DecisionCandidate
}

func evaluate(doc parsed, now time.Time) Result {
	var acc accumulator
	env := doc.doc.Envelope
	snap := doc.doc.Snapshot
	if doc.doc.PriorAttempt != nil {
		if doc.doc.PriorAttempt.PayloadDigest == doc.digest {
			acc.replay = true
		} else {
			acc.respond("conflicting-payload", "the same attempt id has a different canonical envelope digest; the payloads are not merged")
		}
	}
	correlateOwner(doc, now, &acc)
	correlateWindow(doc, now, &acc)
	correlateLaunch(snap, &acc)
	correlateAlerts(doc, &acc)
	if len(env.Actions.AuthorizationEvidence) == 0 {
		acc.insufficient("absent-authority-reference", "no authorization evidence reference is recorded, and a boolean cannot grant one")
	}
	if !contains(env.Actions.Allowed, snap.RequestedAction) {
		acc.respond("action-outside-scope", "the requested action is outside the recorded action scope")
	}
	actual := env.Provenance.Actual
	if actual.Provider != "" && actual.Provider != ProvenanceUnknown && actual.Provider != env.Provenance.Requested.Provider {
		acc.respond("provenance-provider-mismatch", "the actual provider differs from the request, and this contract does not switch providers")
	}
	acc.show("unverified-adapter", "", adapterDetail(doc.doc))
	if len(env.Custody.HandoffEvidence) > 0 {
		acc.show("custody-handoff-ignored", "", "recorded handoff evidence does not transfer custody or authorize a candidate")
	}
	if acc.failed == nil {
		acc.failed = []Predicate{}
	}
	if acc.visible == nil {
		acc.visible = []Condition{}
	}
	if acc.matched == nil {
		acc.matched = []int{}
	}
	return Result{
		Decision: acc.decision(),
		MatchedIDs: MatchedIDs{
			OperationID: env.OperationID,
			AttemptID:   env.AttemptID,
			Revision:    snap.Revision,
			Services:    matchedServices(snap, acc.matched),
			Alerts:      acc.matched,
		},
		FailedPredicates:               acc.failed,
		VisibleConditions:              acc.visible,
		Provenance:                     provenanceOut(env.Provenance),
		NextReevaluationDeadline:       formatTime(reevaluateAt(doc)),
		Retry:                          false,
		Replay:                         acc.replay,
		UnverifiedInput:                true,
		OfflineEvidence:                OfflineEvidenceUnverified,
		CandidateIsOperationalDeferral: false,
		AuthorizationGranted:           false,
		Notice:                         Notice,
	}
}

func correlateOwner(doc parsed, now time.Time, acc *accumulator) {
	env := doc.doc.Envelope
	snap := doc.doc.Snapshot
	if env.Session.UID != env.Claim.OwnerUID || env.Session.UID != env.Observation.OwnerUID || env.Session.UID != snap.Owner.UID || env.Session.UID != snap.Owner.SessionUID || snap.Owner.ClaimReference != env.Claim.Reference {
		acc.respond("conflicting-owner", "owner, session, and claim identities do not bind to one accountable principal; custody is not transferred")
	}
	if snap.Owner.AttemptID != env.AttemptID || env.Observation.AttemptID != env.AttemptID {
		acc.respond("attempt-mismatch", "the live attempt does not match the envelope attempt")
	}
	if snap.Revision != env.Observation.Revision || snap.Owner.UID != env.Observation.OwnerUID || snap.Owner.State != env.Observation.State || snap.Owner.ObservedAt != env.Observation.ObservedAt || snap.Owner.AttemptID != env.Observation.AttemptID {
		acc.respond("observation-conflict", "the live snapshot does not match the envelope's recorded owner observation")
	}
	if !contains(env.Repository.DesiredRevisions, snap.Revision) {
		acc.respond("revision-mismatch", "the snapshot revision is not one of the desired revisions")
	}
	age := now.Sub(doc.when.snapshotObservation)
	if age < 0 {
		age = 0
	}
	limit := time.Duration(env.Observation.HeartbeatIntervalSeconds) * time.Second
	if limit > MaxStaleness {
		limit = MaxStaleness
	}
	switch snap.Owner.State {
	case "active":
		if age > limit {
			acc.respond("owner-stale", "the live owner observation is older than the declared heartbeat")
		}
	case "stale":
		acc.respond("owner-stale", "the owner state is stale")
	case "failed":
		acc.respond("owner-failed", "the owner has failed")
	case "stopped":
		acc.respond("owner-stopped", "the owner is stopped")
	case "lost":
		acc.respond("owner-lost", "the owner is lost and custody is not transferred")
	default:
		acc.insufficient("owner-unknown", "the owner state is unknown")
	}
}

func correlateWindow(doc parsed, now time.Time, acc *accumulator) {
	if !now.Before(doc.when.expiry) {
		acc.respond("activity-expired", "the activity expiry has been reached")
	}
	if !now.Before(doc.when.deadline) {
		acc.respond("task-expired", "the task deadline has been reached")
	}
	if !now.Before(doc.when.authLimit) {
		acc.respond("authorization-window-expired", "the recorded authorization window has been reached")
	}
	switch doc.doc.Envelope.State {
	case "active":
	case "planned":
		acc.respond("operation-not-active", "a planned operation is not an active maintenance window")
	case "completed":
		acc.respond("terminal-completed", "the operation is already completed")
	case "rolled_back":
		acc.respond("terminal-rolled-back", "the operation is already rolled back")
	case "failed":
		acc.respond("terminal-failed", "the operation has failed")
	case "held":
		acc.respond("terminal-held", "the operation is held")
	default:
		acc.respond("operation-not-active", "the operation state is not active")
	}
	if terminalStates[doc.doc.Envelope.State] && doc.doc.Envelope.Terminal.ActivityCleanup != "succeeded" {
		acc.respond("cleanup-failed", "activity cleanup is not succeeded and is not claimed as success")
	}
}

func correlateLaunch(snap snapshot, acc *accumulator) {
	switch snap.Launch {
	case "ok":
	case "denied":
		acc.respond("launch-denied", "launch was denied; do not retry it")
	case "uncertain":
		acc.respond("launch-uncertain", "launch is uncertain; do not retry it")
	default:
		acc.respond("launch-uncertain", "launch evidence is missing; do not retry it")
	}
	if snap.Metadata == "unavailable" {
		acc.insufficient("metadata-unavailable", "metadata is unavailable")
	}
}

func alertPredatesOperation(when instants, index int) bool {
	if index < 0 || index >= len(when.alertStarts) || when.start.IsZero() || when.alertStarts[index].IsZero() {
		return false
	}
	return when.alertStarts[index].Before(when.start)
}

func correlateAlerts(doc parsed, acc *accumulator) {
	env := doc.doc.Envelope
	snap := doc.doc.Snapshot
	if snap.Impact.AffectedServices > env.Task.Impact.MaxServices || snap.Impact.IntervalSeconds > env.Task.Impact.WindowSeconds || rank(snap.Impact.Severity) > rank(env.Task.Impact.MaxSeverity) || len(snap.Alerts) > env.Task.Impact.MaxServices {
		acc.respond("impact-exceeded", "the snapshot impact exceeds the recorded bounds")
	}
	baselines := map[string]baseline{}
	for _, item := range env.Baselines {
		baselines[item.Service] = item
	}
	for _, svc := range env.Targets.Services {
		item, ok := baselines[svc]
		if !ok {
			acc.insufficient("missing-baseline", "no pre-operation baseline is recorded for "+svc)
			continue
		}
		switch item.Health {
		case "unknown":
			acc.insufficient("unknown-baseline", svc+" baseline health is unknown and stays Unknown")
			acc.show("unknown-baseline", svc, "Unknown baseline stays Unknown and blocks a candidate")
		case "degraded":
			acc.show("pre-existing-degradation", svc, "pre-existing symptom "+item.Symptom+" is visible and is not presumed to be caused by this operation")
		}
	}
	if len(snap.Alerts) == 0 {
		acc.insufficient("no-alert", "the snapshot has no alert to correlate")
		return
	}
	for i, alert := range snap.Alerts {
		if alertPredatesOperation(doc.when, i) {
			acc.respond("alert-predates-operation", "snapshot alert "+strconv.Itoa(i)+" for service "+alert.Service+" started before the activity; the pre-existing condition is visible and no cause is attributed")
			acc.show("pre-existing-degradation", alert.Service, "snapshot alert index "+strconv.Itoa(i)+" for service "+alert.Service+" started before the activity and stays visible; no cause is attributed")
		}
		if !contains(env.Targets.Services, alert.Service) {
			acc.respond("service-mismatch", "alert service "+alert.Service+" is outside the operation")
			continue
		}
		base, ok := baselines[alert.Service]
		if !ok || base.Health == "unknown" {
			continue
		}
		if alertPredatesOperation(doc.when, i) {
			continue
		}
		if exactAlert(env.ExpectedAlerts, alert) {
			acc.matched = append(acc.matched, i)
			continue
		}
		mismatch(alert, env.ExpectedAlerts, base, acc)
	}
	if env.Repository.Adapter.Runtime == "applicable" && (snap.Adapter.RuntimeHealth == "unknown" || snap.Adapter.Reconciliation == "unknown" || snap.Adapter.GatusStatus == "unknown" || snap.Adapter.RenovateTier == "unknown") {
		acc.insufficient("adapter-metadata-unknown", "a platform adapter field is Unknown")
	}
}

func mismatch(alert observedAlert, predicates []expectedAlert, base baseline, acc *accumulator) {
	var same *expectedAlert
	for i := range predicates {
		if predicates[i].Service == alert.Service {
			same = &predicates[i]
			break
		}
	}
	if same == nil {
		acc.respond("unexpected-alert", "no expected predicate names service "+alert.Service)
	} else {
		if !labelsEqual(same.Labels, alert.Labels) {
			acc.respond("unexpected-label", "alert labels do not exactly match the expected predicate")
		}
		if same.Severity != alert.Severity {
			acc.respond("unexpected-severity", "alert severity does not exactly match the expected predicate")
		}
		if same.Symptom != alert.Symptom {
			acc.respond("unexpected-symptom", "alert symptom does not exactly match the expected predicate")
		}
		if same.Resource != alert.Resource {
			acc.respond("unexpected-alert", "alert resource does not exactly match the expected predicate")
		}
	}
	switch base.Health {
	case "healthy":
		acc.respond("unexplained-worsening", "the alert is not an expected predicate and the baseline was healthy")
	case "degraded":
		if rank(alert.Severity) > rank(base.Severity) || alert.Symptom != base.Symptom {
			acc.respond("unexplained-worsening", "the alert is worse than the pre-existing baseline and is not an expected predicate")
		}
	}
}

func exactAlert(predicates []expectedAlert, alert observedAlert) bool {
	for _, item := range predicates {
		if item.Service == alert.Service && item.Resource == alert.Resource && item.Severity == alert.Severity && item.Symptom == alert.Symptom && labelsEqual(item.Labels, alert.Labels) {
			return true
		}
	}
	return false
}

func labelsEqual(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func adapterDetail(doc checkDoc) string {
	snap := doc.Snapshot.Adapter
	if doc.Envelope.Repository.Adapter.Runtime == "not-applicable" {
		return "repository adapter " + snap.Repository + " marks runtimeHealth and reconciliation NotApplicable; source-only observations are not cluster health and are not a grant"
	}
	return "repository adapter " + snap.Repository + " reports runtimeHealth=" + snap.RuntimeHealth + " reconciliation=" + snap.Reconciliation + " gatusStatus=" + snap.GatusStatus + " renovateTier=" + snap.RenovateTier + "; these observations are unverified and are not a grant"
}

func matchedServices(snap snapshot, indexes []int) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, index := range indexes {
		if index < 0 || index >= len(snap.Alerts) {
			continue
		}
		svc := snap.Alerts[index].Service
		if seen[svc] {
			continue
		}
		seen[svc] = true
		out = append(out, svc)
	}
	sort.Strings(out)
	return out
}

func provenanceOut(in provenance) ProvenanceOut {
	return ProvenanceOut{
		Requested: ModelOut{Provider: in.Requested.Provider, Model: in.Requested.Model, Effort: in.Requested.Effort, State: in.Requested.State},
		Actual: ModelOut{
			Provider: unknownIfEmpty(in.Actual.Provider),
			Model:    unknownIfEmpty(in.Actual.Model),
			Effort:   in.Actual.Effort,
			State:    unknownIfEmpty(in.Actual.State),
		},
	}
}

func unknownIfEmpty(value string) string {
	if value == "" {
		return ProvenanceUnknown
	}
	return value
}

func reevaluateAt(doc parsed) time.Time {
	limit := time.Duration(doc.doc.Envelope.Observation.HeartbeatIntervalSeconds) * time.Second
	deadline := doc.when.expiry
	for _, item := range []time.Time{doc.when.deadline, doc.when.authLimit, doc.when.snapshotObservation.Add(limit)} {
		if item.Before(deadline) {
			deadline = item
		}
	}
	return deadline
}
