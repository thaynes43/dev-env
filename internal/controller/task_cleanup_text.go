package controller

// What a person reads about a task Session the clean-up owns (DESIGN-061
// section 6). PodReady, PrivateHistoryRetained and RemovalBlocked carry one
// text, and HomeExpiry says the same step with its own reason. Each is built
// from the Session's status, the flags, and what this reconcile found
// (obs.homeExpiry), never from a counter or a clock, so a steady Session
// writes no status: a queued Session's text names no position in the queue.

import (
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// graceWords says a grace in days when it is whole days.
func graceWords(d time.Duration) string {
	if d > 0 && d%(24*time.Hour) == 0 {
		if days := int(d / (24 * time.Hour)); days != 1 {
			return fmt.Sprintf("%d days", days)
		}
		return "1 day"
	}
	return d.String()
}

// taskCleanupMarker names the clean-up in its texts; agent-run looks for it
// (internal/agentrun) and the HomeExpiry condition starts with it.
const taskCleanupMarker = "task clean-up (DESIGN-061)"

// taskCleanupPromise is what a task Session in the clean-up's scope is told
// before its task ends (DESIGN-061 6): agent-run's reap text looks for the
// clean-up's name in it.
func taskCleanupPromise(grace time.Duration) string {
	return "once its task ends, the " + taskCleanupMarker + " saves any work that never reached GitHub as a git bundle (kept 30 days), " +
		"then deletes its HOME and this session, " + graceWords(grace) + " after the end, or as soon as its agent has stopped after a reap"
}

// taskCleanupReapPromise is the same for a reaped task whose stop is not
// proven yet.
func taskCleanupReapPromise() string {
	return "once its stop is proven, the " + taskCleanupMarker + " saves any work that never reached GitHub as a git bundle (kept 30 days), " +
		"then deletes its HOME and this session"
}

// taskCleanupText is the one text, and the PodReady and RemovalBlocked reason,
// of a task Session the clean-up owns: the reap, the agent's stop, then the
// clean-up's step. Call it only with a terminal record.
func taskCleanupText(s *v1alpha1.AgentSession, obs observation) (reason, message string) {
	h := s.Status.PrivateHistory
	e := h.Expiry
	var parts []string
	if taskCleanupPurged(s) {
		parts = append(parts, "reap accepted")
	}
	reason = reasonExecutorEnded
	parts = append(parts, agentStoppedText(s))
	parts = append(parts, taskCleanupStep(s, obs))
	if e != nil && expiryPhaseAtLeast(e.Phase, v1alpha1.HomeExpiryDeleting) {
		reason = map[string]string{v1alpha1.HomeExpiryDeleting: "HomeDeleting", v1alpha1.HomeExpiryExpired: "HomeExpired"}[e.Phase]
	}
	return reason, truncate(strings.Join(parts, "; "))
}

// taskCleanupStep says the clean-up's step, from the record and this
// reconcile's note.
func taskCleanupStep(s *v1alpha1.AgentSession, obs observation) string {
	h := s.Status.PrivateHistory
	e := h.Expiry
	n := obs.homeExpiry
	due := func() string {
		if taskCleanupPurged(s) {
			return "the task clean-up deletes its HOME now, once any work that never reached GitHub is saved as a git bundle (DESIGN-061)"
		}
		start := h.ExecutorTerminal.ObservedAt.Time
		grace := obs.taskCleanupGrace
		if e != nil {
			start = e.GraceStart.Time
			grace = e.DueAt.Sub(start)
		}
		return fmt.Sprintf("the task clean-up deletes its HOME and this session from %s, %s after that, once any work that never reached GitHub is saved as a git bundle (DESIGN-061)",
			start.Add(grace).UTC().Format(time.RFC3339), graceWords(grace))
	}
	switch {
	case e == nil:
		return due()
	case homeExpiryHeld(s):
		return "the owner holds the task clean-up, so its HOME is kept and nothing moves on until the hold is removed"
	case e.Refusal != "":
		return taskRefusalText(s, e)
	}
	with := func(base string) string {
		if n != nil && n.message != "" && !n.unknown {
			return base + "; " + n.message
		}
		return base
	}
	switch e.Phase {
	case v1alpha1.HomeExpiryWaiting:
		return with(due())
	case v1alpha1.HomeExpiryRescuing:
		if n != nil && n.message != "" && !n.unknown {
			return "the task clean-up has started; " + n.message
		}
		return "the task clean-up is saving any work that never reached GitHub, in hold pod " + s.Name
	case v1alpha1.HomeExpiryArchived:
		if e.Bundle == nil {
			return "the task clean-up has saved its rescue; the HOME is deleted next"
		}
		return "the task clean-up " + bundleSavedText(s.Name, e.Bundle, rescueRefs(s.Status.Rescue)) + "; the HOME is deleted next"
	case v1alpha1.HomeExpiryDeleting:
		return fmt.Sprintf("the HOME %s is being deleted and the session can no longer be resumed; %s", h.HomeClaim, taskKeptShort(s, e))
	case v1alpha1.HomeExpiryExpired:
		return "the HOME is deleted; this session is removed next; " + bundleKeptText(s, e)
	}
	return "the task clean-up is at " + e.Phase
}

// taskKeptShort names what the clean-up keeps, for the delete's texts.
func taskKeptShort(s *v1alpha1.AgentSession, e *v1alpha1.PrivateHomeExpiryStatus) string {
	if e.Bundle != nil && e.Bundle.Result == bundleVerified {
		return keptName(s, e) + " is kept"
	}
	return "every repository was clean and pushed, so nothing is kept"
}

// taskRefusalText says why the clean-up keeps the HOME, and when it looks
// again.
func taskRefusalText(s *v1alpha1.AgentSession, e *v1alpha1.PrivateHomeExpiryStatus) string {
	at := "now"
	if e.RefusalAt != nil {
		at = e.RefusalAt.Add(taskAttemptRefusalRetry(e)).UTC().Format(time.RFC3339)
	}
	switch e.Refusal {
	case expiryRescueRefused:
		why := "the rescue refused"
		if rec := s.Status.Rescue; rec != nil && rec.Message != "" {
			why += " (" + rec.Message + ")"
		}
		return "the clean-up keeps the HOME: " + why + "; a person decides; it is tried again at " + at
	case expiryVolumeNotReleased:
		node := s.Status.PrivateHistory.ExecutorTerminal.NodeName
		return fmt.Sprintf("the clean-up keeps the HOME: node %s did not show the HOME's volume released within %s, so the stop is unproven; a person decides; it looks again at %s",
			node, graceWords(taskReleaseWait), at)
	case expiryOwnerHold:
		return "the owner's hold on the clean-up is lifted; it goes back into the queue at " + at
	case expiryBundleMissing, expiryBundleMismatch:
		return "the clean-up keeps the HOME: the saved bundle no longer verifies on the shelf (" + e.Refusal + "); a person decides; it checks again at " + at
	}
	return "the clean-up keeps the HOME: it is refused (" + e.Refusal + "); it checks again at " + at
}

// setTaskCleanupCondition is the HomeExpiry condition of a task Session the
// clean-up owns: reason Waiting, Queued, Paused, Saving, Saved, Deleting,
// Deleted, Refused or Held (never Archived), and the step's text.
func setTaskCleanupCondition(s *v1alpha1.AgentSession, obs observation, st *v1alpha1.AgentSessionStatus) {
	h := st.PrivateHistory
	if h == nil || h.Expiry == nil && obs.homeExpiry == nil {
		meta.RemoveStatusCondition(&st.Conditions, ConditionHomeExpiry)
		return
	}
	view := s.DeepCopy()
	view.Status.PrivateHistory = h
	c := metav1.Condition{Type: ConditionHomeExpiry, Status: metav1.ConditionTrue, ObservedGeneration: s.Generation, Reason: taskReasonWaiting}
	n := obs.homeExpiry
	e := h.Expiry
	switch {
	case n != nil && n.unknown:
		c.Status, c.Reason = metav1.ConditionUnknown, "Unknown"
	case homeExpiryHeld(view):
		c.Reason = taskReasonHeld
	case e != nil && e.Refusal != "":
		c.Reason = taskReasonRefused
	case e == nil:
	case e.Phase == v1alpha1.HomeExpiryRescuing:
		c.Reason = taskReasonSaving
	case e.Phase == v1alpha1.HomeExpiryArchived:
		c.Reason = taskReasonSaved
	case e.Phase == v1alpha1.HomeExpiryDeleting:
		c.Reason = taskReasonDeleting
	case e.Phase == v1alpha1.HomeExpiryExpired:
		c.Reason = taskReasonDeleted
	case n != nil && (n.reason == taskReasonQueued || n.reason == taskReasonPaused):
		c.Reason = n.reason
	}
	if executorTerminalRecorded(h) {
		c.Message = taskCleanupMarker + ": " + taskCleanupStep(view, obs)
	} else {
		c.Message = taskCleanupMarker + ": it waits for the agent's stop to be proven"
	}
	if n != nil && n.unknown {
		c.Message = strings.TrimPrefix(c.Message+"; ", "; ") + "this step cannot be judged now: " + n.message
	}
	if e != nil {
		c.Message += fmt.Sprintf("; the record is at %s, stored due time %s", e.Phase, e.DueAt.UTC().Format(time.RFC3339))
	}
	c.Reason = conditionReason(c.Reason)
	c.Message = truncate(c.Message)
	meta.SetStatusCondition(&st.Conditions, c)
}
