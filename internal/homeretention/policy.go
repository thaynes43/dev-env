package homeretention

import (
	"errors"
	"sort"
	"time"
)

// Evaluate is pure: the caller supplies the entire snapshot, trust, and clock.
// Even an immediate candidate is a proposal, never deletion authority.
func Evaluate(snapshot Snapshot, now time.Time, policy Policy, trust EvidenceVerifier) (Report, error) {
	if err := validateSnapshot(snapshot); err != nil {
		return Report{}, err
	}
	if now.IsZero() {
		return Report{}, errors.New("explicit evaluation time required")
	}
	if policy.WarningLead <= 0 || policy.WarningLead > Retention {
		return Report{}, errors.New("warning lead must be positive and at most 168h")
	}
	if policy.ObservationMaxAge <= 0 || policy.ObservationMaxAge > time.Hour {
		return Report{}, errors.New("observation maximum age must be positive and at most 1h")
	}
	evidence := verifier{source: trust}
	report := Report{Version: Version, DryRun: true, DeleteEnabled: false, EvaluatedAt: formatTime(now), Retention: Retention.String(), WarningLead: policy.WarningLead.String(), ObservationMaxAge: policy.ObservationMaxAge.String(), ImmediateCandidates: []Entry{}, StoppedWorkQueue: []Entry{}}
	for _, record := range snapshot.Records {
		entry := Entry{UID: record.UID, Binding: record.Binding, Reasons: []string{}, Provenance: record.Provenance}
		completed, clock, clockReason := completionClock(record, now)
		entry.ClockSource = clock.Source
		if clockReason != "" {
			entry.Reasons = append(entry.Reasons, clockReason)
		}
		if !completed.IsZero() {
			deadline := completed.Add(Retention)
			warning := deadline.Add(-policy.WarningLead)
			entry.CompletedAt, entry.WarningAt, entry.Deadline = formatTime(completed), formatTime(warning), formatTime(deadline)
			switch {
			case !now.Before(deadline):
				entry.Status, entry.DecisionRequired = "decision_required", true
			case !now.Before(warning):
				entry.Status = "warning"
			default:
				entry.Status = "retained"
			}
		} else {
			entry.Status, entry.DecisionRequired = "decision_required", true
		}
		entry.Reasons = append(entry.Reasons, evidence.preservation(record, clock, completed, now, policy.ObservationMaxAge)...)
		protected := false
		if record.State == "active" {
			protected = true
			entry.Reasons = append(entry.Reasons, "active_session")
		}
		if record.Hold != "" {
			protected = true
			entry.Reasons = append(entry.Reasons, "owner_hold")
		}
		switch record.Disposition {
		case "", "normal":
		case "canary", "pilot", "fixture", "rescue_hold":
			protected = true
			entry.Reasons = append(entry.Reasons, "special_disposition")
		default:
			entry.Reasons = append(entry.Reasons, "disposition_unknown")
		}
		verifiedProtection := protected && evidence.verifiedSafety(record, now, policy.ObservationMaxAge)
		if protected && !verifiedProtection {
			entry.Reasons = append(entry.Reasons, "protection_unverified")
		}
		entry.Reasons = sortedUnique(entry.Reasons)
		if protected {
			entry.Status = "protected"
			// Active work is protected without expiry actions. Stopped holds and
			// fixtures retain their explicit overdue/clockless disposition decision.
			if record.State == "active" {
				entry.DecisionRequired = false
			} else if entry.DecisionRequired {
				entry.DispositionDecisionRequired = true
				entry.Reasons = sortedUnique(append(entry.Reasons, "owner_disposition_required"))
			}
		} else if len(entry.Reasons) == 0 {
			entry.Status, entry.DecisionRequired = "immediate_preserved", false
			report.ImmediateCandidates = append(report.ImmediateCandidates, entry)
			continue
		}
		report.StoppedWorkQueue = append(report.StoppedWorkQueue, entry)
	}
	sort.Slice(report.ImmediateCandidates, func(i, j int) bool { return report.ImmediateCandidates[i].UID < report.ImmediateCandidates[j].UID })
	sort.Slice(report.StoppedWorkQueue, func(i, j int) bool { return report.StoppedWorkQueue[i].UID < report.StoppedWorkQueue[j].UID })
	return report, nil
}

func completionClock(record Record, now time.Time) (time.Time, Clock, string) {
	clock := Clock{Source: "unknown"}
	var value string
	if record.TaskCompletion != nil {
		clock.Source = "task_finished_at"
		if !validExecutor(record.Binding.ExecutorBinding) || record.TaskCompletion.Executor != record.Binding.ExecutorBinding {
			return time.Time{}, clock, "task_clock_binding_invalid"
		}
		value = record.TaskCompletion.FinishedAt
	} else if record.TerminalRecord != nil {
		clock.Source = "executor_terminal_observed_at"
		if !validExecutor(record.Binding.ExecutorBinding) || record.TerminalRecord.Executor != record.Binding.ExecutorBinding || record.TerminalRecord.Source != "actual_executor_terminal" {
			return time.Time{}, clock, "terminal_clock_binding_or_source_invalid"
		}
		value = record.TerminalRecord.ObservedAt
	} else {
		return time.Time{}, clock, "completion_clock_missing"
	}
	t, ok := parseTime(value)
	if !ok {
		return time.Time{}, clock, "completion_clock_invalid"
	}
	if t.After(now) {
		return time.Time{}, clock, "completion_clock_future"
	}
	clock.At = formatTime(t)
	return t, clock, ""
}

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func sortedUnique(values []string) []string {
	sort.Strings(values)
	out := make([]string, 0, len(values))
	for _, value := range values {
		if len(out) == 0 || out[len(out)-1] != value {
			out = append(out, value)
		}
	}
	return out
}
