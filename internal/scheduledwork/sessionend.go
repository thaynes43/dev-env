// Package scheduledwork holds DESIGN-054's pure rules for a scheduled session:
// the session-end rule, the terminal record an ended or reaped session gives
// its occurrence, and how that record is written into a ScheduledWork's
// status. It reads only what it is given and writes nothing, so the
// AgentSession reconciler's releaser (S3a) and the executor (S3b) apply one
// rule and a fresh operator computes the same answer.
package scheduledwork

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/standby"
)

// SessionEnded is DESIGN-054's session-end rule. A scheduled session has ended
// when any of these holds:
//   - its operating mode is Suspended (a user or idle suspend), or its phase is
//     Suspended, Failed or Archived;
//   - in task mode, status.taskResult (TaskResult, which also reads agent.task
//     before the reconciler derives it) or the retained task outcome is set;
//   - its phase is Running or Idle, standby.AgentEnded holds, and the pod-age
//     proof holds (standby.UnprovenAgentEnd is false for pod).
//
// pod is the pod of the session's name, read from the API server; nil is no
// pod, which never proves an agent's end. A drain onto a new revision, a
// booting agent and a young pod are therefore not an end. why says which
// clause held, for logs and events; it is empty when the session has not
// ended. A deletion is not an end by itself: the releaser handles a session
// being deleted on its own.
func SessionEnded(s *v1alpha1.AgentSession, pod *corev1.Pod, now time.Time) (ended bool, why string) {
	if s.Spec.OperatingMode == v1alpha1.OperatingModeSuspended {
		return true, "the session is suspended"
	}
	switch s.Status.Phase {
	case v1alpha1.PhaseSuspended, v1alpha1.PhaseFailed, v1alpha1.PhaseArchived:
		return true, fmt.Sprintf("the session's phase is %s", s.Status.Phase)
	}
	if s.Spec.Mode == v1alpha1.ModeTask {
		if r := TaskResult(s); r != nil {
			return true, fmt.Sprintf("the task result is recorded (%s)", r.State)
		}
		if s.Status.TaskOutcome != nil {
			return true, "the task's retained outcome is recorded"
		}
	}
	if agentEndClaimed(s) && !standby.UnprovenAgentEnd(s, pod, now) {
		return true, fmt.Sprintf("the agent %s and its pod is old enough to prove it", s.Status.Agent.Status)
	}
	return false, ""
}

// NeedsPodProof reports whether SessionEnded's answer depends on the pod: no
// clause that ignores the pod holds, and the agent-end clause would if the
// pod-age proof held. The caller reads the pod, uncached, only then.
func NeedsPodProof(s *v1alpha1.AgentSession) bool {
	if ended, _ := SessionEnded(s, nil, time.Time{}); ended {
		return false
	}
	return agentEndClaimed(s)
}

// agentEndClaimed is the third clause without its proof: a steady phase and an
// agent report of exited, failed or interrupted, made after boot.
func agentEndClaimed(s *v1alpha1.AgentSession) bool {
	switch s.Status.Phase {
	case v1alpha1.PhaseRunning, v1alpha1.PhaseIdle:
		return standby.AgentEnded(s)
	}
	return false
}

// The reasons the terminal record gives when the session's own task result
// does not.
const (
	// ReasonNoTaskResult is a headless session that ended with no task result.
	ReasonNoTaskResult = "ended without a task result"
	// ReasonReaped is a headless session deleted before it ended.
	ReasonReaped = "session reaped before it ended"
	// ReasonNotStarted is a session whose agent agentd reported it refused to
	// launch, its start deadline passed (DESIGN-054 "Start deadline").
	ReasonNotStarted = "not started before grace ended"
	// ReasonParticipationNotObserved is every interactive end: the platform
	// has no signal that Tom joined, so the record claims nothing about it.
	ReasonParticipationNotObserved = "participation not observed"
)

// Bounds of the occurrence fields the record fills.
const (
	maxReason          = 256
	maxExecutionReason = 128
)

// Terminal is the terminal record an occurrence gets from its session.
type Terminal struct {
	// State is Succeeded, Failed or Ended.
	State string
	// Reason says why, in at most 256 bytes.
	Reason string
	// Execution is a headless task's result, copied from status.taskResult;
	// nil when there is none.
	Execution *v1alpha1.ScheduledWorkExecution
	// Reported is a headless agent's retained outcome, copied as its own claim
	// and not validated; nil when there is none.
	Reported *v1alpha1.TaskOutcomeRecordStatus
}

// Outcome maps a session that ended, or that is being deleted, to its
// occurrence's terminal record. The session's mode decides, not the
// ScheduledWork's: an edit may change the item's mode after the occurrence was
// claimed.
//
// Headless (task mode): the task result (TaskResult) decides. Succeeded is
// Succeeded, Failed and TimedOut are Failed, Unknown (exit 0 with no success
// event) is Ended. With no task result it is Failed: "session reaped before it
// ended" while the session is being deleted, else "ended without a task
// result". A deleted session whose operating mode is Suspended is the second:
// a reap never suspends, so the suspend ended it first. Every other end a
// deleted session shows (phase Archived after the reap's archive, Suspended
// while its hold pod rescues, Failed or an exited agent after the rescue
// stopped it) the reap can have made itself, so it reads as the reap; a
// session that ended on its own and whose release was held until after a reap
// reads so too. Execution and Reported are copied from status.
//
// Interactive (any other mode): Ended, "participation not observed", whatever
// ended it, a reap included.
//
// Either mode with no task result is Failed, "not started before grace
// ended", when its start deadline has passed and agentd reported that it
// refused the late first launch (protocol.StartDeadlinePassed). Only that
// positive report counts: a session with no agent report, or with any other,
// gets the reason above, because missing telemetry never proves that an
// agent did not launch.
func Outcome(s *v1alpha1.AgentSession, now time.Time) Terminal {
	headless := s.Spec.Mode == v1alpha1.ModeTask
	var t Terminal
	if headless {
		t.Reported = reported(s)
	}
	if r := TaskResult(s); headless && r != nil {
		t.Execution = &v1alpha1.ScheduledWorkExecution{State: r.State, Reason: clip(r.Reason, maxExecutionReason)}
		switch r.State {
		case v1alpha1.TaskResultSucceeded:
			t.State = v1alpha1.OccurrenceSucceeded
		case v1alpha1.TaskResultFailed, v1alpha1.TaskResultTimedOut:
			t.State = v1alpha1.OccurrenceFailed
		default:
			t.State = v1alpha1.OccurrenceEnded
		}
		reason := r.Message
		if reason == "" {
			reason = r.Reason
		}
		if reason == "" {
			reason = "task result " + string(r.State)
		}
		t.Reason = clip(reason, maxReason)
		return t
	}
	switch {
	case notStarted(s, now):
		t.State, t.Reason = v1alpha1.OccurrenceFailed, ReasonNotStarted
	case !headless:
		t.State, t.Reason = v1alpha1.OccurrenceEnded, ReasonParticipationNotObserved
	case !s.DeletionTimestamp.IsZero() && s.Spec.OperatingMode != v1alpha1.OperatingModeSuspended:
		t.State, t.Reason = v1alpha1.OccurrenceFailed, ReasonReaped
	default:
		t.State, t.Reason = v1alpha1.OccurrenceFailed, ReasonNoTaskResult
	}
	return t
}

// TaskResult is a task-mode session's task result: status.taskResult, or, while
// the reconciler has not yet written it, the same derivation from agent.task
// (and a private session's retained terminal task) that the reconciler's
// observe makes. The heartbeat stores agent.task, and the retained outcome
// after it, before the next observe turns it into status.taskResult, so a
// session read in between must not look like one with no task result. It is
// nil for any other mode and before the task ends.
func TaskResult(s *v1alpha1.AgentSession) *v1alpha1.TaskResultStatus {
	if s.Spec.Mode != v1alpha1.ModeTask {
		return nil
	}
	if r := s.Status.TaskResult; r != nil {
		return r
	}
	a := s.Status.Agent
	if h := s.Status.PrivateHistory; h != nil && h.TaskTerminal != nil {
		a = &v1alpha1.AgentStatus{Task: &h.TaskTerminal.Task}
	}
	if a == nil || a.Task == nil {
		return nil
	}
	t := a.Task
	v := apiv1.TaskResult{ExitCode: t.ExitCode, TimedOut: t.TimedOut, Subtype: t.Subtype, IsError: t.IsError, NumTurns: t.NumTurns}.Verdict()
	return &v1alpha1.TaskResultStatus{State: v1alpha1.TaskResultState(v.State), Reason: v.Reason, Message: v.Message}
}

// notStarted reports a session whose start deadline has passed and whose agent
// agentd reported it never launched: AgentFailed with
// protocol.StartDeadlinePassed, the refused late first launch. No agent
// report is not that proof: a heartbeat can be lost or late while the agent
// runs, so missing telemetry never counts.
func notStarted(s *v1alpha1.AgentSession, now time.Time) bool {
	deadline, err := StartDeadline(s)
	if err != nil || !now.After(deadline) {
		return false
	}
	a := s.Status.Agent
	return a != nil && a.Status == protocol.AgentFailed && a.Message == protocol.StartDeadlinePassed
}

// reported copies the session's retained outcome, when it came from this
// session.
func reported(s *v1alpha1.AgentSession) *v1alpha1.TaskOutcomeRecordStatus {
	o := s.Status.TaskOutcome
	if o == nil || o.SessionUID != string(s.UID) {
		return nil
	}
	return o.Record.DeepCopy()
}

// OccurrenceAt is the occurrence instant in the session's annotation.
func OccurrenceAt(s *v1alpha1.AgentSession) (time.Time, error) {
	raw, ok := s.Annotations[v1alpha1.AnnotationScheduledWorkOccurrence]
	if !ok {
		return time.Time{}, fmt.Errorf("annotation %s is missing", v1alpha1.AnnotationScheduledWorkOccurrence)
	}
	sec, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || sec <= 0 {
		return time.Time{}, fmt.Errorf("annotation %s is not Unix seconds: %q", v1alpha1.AnnotationScheduledWorkOccurrence, raw)
	}
	return time.Unix(sec, 0).UTC(), nil
}

// SessionName is DESIGN-054's name for the session of an occurrence:
// sw-<the first 12 hex digits of the item's UID>-<the occurrence instant in
// UTC, YYYYMMDDHHMM>. A UID that does not start with 12 hex digits, once its
// dashes are dropped, derives no name.
func SessionName(itemUID string, at time.Time) (string, error) {
	digits := strings.ReplaceAll(itemUID, "-", "")
	if len(digits) < 12 || strings.Trim(digits[:12], "0123456789abcdef") != "" {
		return "", fmt.Errorf("the ScheduledWork UID %q does not start with 12 hex digits", itemUID)
	}
	return "sw-" + digits[:12] + "-" + at.UTC().Format("200601021504"), nil
}

// StartDeadline is the start deadline in the session's annotation.
func StartDeadline(s *v1alpha1.AgentSession) (time.Time, error) {
	raw, ok := s.Annotations[v1alpha1.AnnotationScheduledWorkStartDeadline]
	if !ok {
		return time.Time{}, fmt.Errorf("annotation %s is missing", v1alpha1.AnnotationScheduledWorkStartDeadline)
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("annotation %s is not RFC 3339: %q", v1alpha1.AnnotationScheduledWorkStartDeadline, raw)
	}
	return t, nil
}

// clip keeps s within n bytes, cut at a rune boundary.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
