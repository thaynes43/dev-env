package controller

import (
	"context"
	"slices"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// The operator's session metrics (D-57). They are read from the sessions at
// scrape time, so they come from what the API server holds, not from memory:
// every replica reports the same values from its own cache, a new leader needs
// no warm-up, and a reaped session's series end with it (DESIGN-001 5.1).

// The metric names. haynes-ops' alert on RescueFailed (D-10: "blocks archive and
// pages") reads the first. Neither has a namespace label: the scrape adds the
// target's (the operator's, dev-env-system), and every session lives in the one
// namespace the collector reads (D-02).
const (
	MetricRescueFailed            = "dev_env_session_rescue_failed"
	MetricRescueNotification      = "dev_env_session_rescue_notification"
	MetricRescueLatestAttempt     = "dev_env_session_rescue_latest_attempt_timestamp_seconds"
	MetricSessions                = "dev_env_sessions"
	MetricTerminalTaskRetained    = "dev_env_terminal_task_pods_retained"
	MetricTerminalTaskRetainedAge = "dev_env_terminal_task_pods_oldest_seconds"
	MetricReapedRetained          = "dev_env_reaped_sessions_retained"
	MetricReapedRetainedAge       = "dev_env_reaped_sessions_oldest_seconds"
)

// collectTimeout bounds one scrape's read of the sessions.
const collectTimeout = 10 * time.Second

var (
	rescueFailedDesc = prometheus.NewDesc(MetricRescueFailed,
		"1 for each session whose newest rescue failed (condition RescueFailed True), except a rescue the task clean-up ran in its own hold pod, which its gauges count instead (DESIGN-061). D-10's mark otherwise keeps the volume and blocks archive until a human looks.",
		[]string{"session", "reason"}, nil)
	rescueNotificationDesc = prometheus.NewDesc(MetricRescueNotification,
		"Notification-only failed rescue identity and bounded native ownership; never preservation or removal authority.",
		[]string{"session", "session_uid", "session_generation", "reason", "risk_key", "owner_uid", "pending_delete"}, nil)
	rescueLatestAttemptDesc = prometheus.NewDesc(MetricRescueLatestAttempt,
		"Newest recorded rescue attempt time, not incident onset or provider/transcript evidence.", []string{"session", "session_uid"}, nil)
	sessionsDesc = prometheus.NewDesc(MetricSessions,
		"The number of sessions in each phase. A phase with no session reports 0, so the series exist whenever the operator is scraped.",
		[]string{"phase"}, nil)
	terminalTaskRetainedDesc = prometheus.NewDesc(MetricTerminalTaskRetained,
		"Terminal headless task compute retained by current reclaim reason (DESIGN-025 24).", []string{"reason"}, nil)
	terminalTaskRetainedAgeDesc = prometheus.NewDesc(MetricTerminalTaskRetainedAge,
		"Oldest retained terminal headless task compute age in seconds by current reclaim reason.", []string{"reason"}, nil)
	reapedRetainedDesc = prometheus.NewDesc(MetricReapedRetained,
		"Reaped sessions the operator still keeps, by the reason of their RemovalBlocked condition (DESIGN-025 25). RetentionPending: a private agent's stop is not proven yet; ExecutorEnded: it stopped, and its HOME waits for the HOME expiry.",
		[]string{"reason"}, nil)
	reapedRetainedAgeDesc = prometheus.NewDesc(MetricReapedRetainedAge,
		"Seconds since the reap of the oldest reaped session still kept, by the reason of its RemovalBlocked condition; 0 when none.", []string{"reason"}, nil)
)

var terminalTaskRetainedReasons = []string{"ProofUnknown", "IdentityUnknown", "Protected", "Held", "ChildrenActive", "Grace", "Stopping"}

// reapedRetainedReasons are the RemovalBlocked reasons a reaped session can
// show, each reported even at 0; any other reason counts as Other.
var reapedRetainedReasons = []string{reasonRetentionPending, reasonExecutorEnded, "TaskComputeReclaimed", "PreservationUnknown",
	"HomeDeleting", "HomeExpired", "DeleteNeedsRescue", "Other"}

// allReportedPhases are the phases dev_env_sessions always reports, with "" as
// "Unknown" for a session the operator has not observed yet.
var allReportedPhases = []v1alpha1.SessionPhase{v1alpha1.PhasePending, v1alpha1.PhaseRunning, v1alpha1.PhaseIdle,
	v1alpha1.PhaseDraining, v1alpha1.PhaseSuspended, v1alpha1.PhaseArchived, v1alpha1.PhaseFailed}

// SessionCollector is a prometheus.Collector over the sessions in one
// namespace. Register it with controller-runtime's metrics registry, so the
// manager's metrics endpoint serves it.
type SessionCollector struct {
	// Reader lists sessions; the manager's cache in the operator.
	Reader client.Reader
	// Namespace is where the sessions live (dev-agents).
	Namespace           string
	TerminalTaskReclaim bool
	Now                 func() time.Time
	// TaskCleanupOn is the operator's task clean-up. A failed rescue that ran
	// in that clean-up's hold pod is omitted from dev_env_session_rescue_failed,
	// so DevEnvRescueFailed does not page for it (DESIGN-061 6).
	TaskCleanupOn        bool
	RescueIncidentOwners *RescueIncidentOwners
}

// Describe implements prometheus.Collector.
func (c *SessionCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- rescueFailedDesc
	ch <- rescueNotificationDesc
	ch <- rescueLatestAttemptDesc
	ch <- sessionsDesc
	ch <- terminalTaskRetainedDesc
	ch <- terminalTaskRetainedAgeDesc
	ch <- reapedRetainedDesc
	ch <- reapedRetainedAgeDesc
}

// Collect implements prometheus.Collector. A failed list reports an invalid
// metric, which fails the whole scrape: a partial list could show a session
// healthy that it never read. Every series vanishes then, as when the operator
// is down, so haynes-ops pages on their absence too (D-57).
func (c *SessionCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), collectTimeout)
	defer cancel()
	var list v1alpha1.AgentSessionList
	if err := c.Reader.List(ctx, &list, client.InNamespace(c.Namespace)); err != nil {
		ch <- prometheus.NewInvalidMetric(sessionsDesc, err)
		return
	}
	counts := map[string]int{"Unknown": 0}
	retained, oldest := map[string]float64{}, map[string]float64{}
	reaped, reapedOldest := map[string]float64{}, map[string]float64{}
	now := time.Now()
	if c.Now != nil {
		now = c.Now()
	}
	for _, p := range allReportedPhases {
		counts[string(p)] = 0
	}
	for i := range list.Items {
		s := &list.Items[i]
		phase := string(s.Status.Phase)
		if phase == "" {
			phase = "Unknown"
		}
		counts[phase]++
		if cond := meta.FindStatusCondition(s.Status.Conditions, ConditionTerminalTaskReclaim); c.TerminalTaskReclaim && s.Status.PodName != "" && cond != nil && cond.Status == metav1.ConditionTrue {
			reason := cond.Reason
			if !slices.Contains(terminalTaskRetainedReasons, reason) {
				reason = "ProofUnknown"
			}
			retained[reason]++
			at := s.CreationTimestamp.Time
			if h := s.Status.PrivateHistory; h != nil && h.TaskTerminal != nil {
				at = h.TaskTerminal.ObservedAt.Time
			}
			if !at.IsZero() {
				oldest[reason] = max(oldest[reason], max(0, now.Sub(at).Seconds()))
			}
		}
		if cond := meta.FindStatusCondition(s.Status.Conditions, ConditionRemovalBlocked); !s.DeletionTimestamp.IsZero() && cond != nil && cond.Status == metav1.ConditionTrue {
			reason := cond.Reason
			if !slices.Contains(reapedRetainedReasons, reason) {
				reason = "Other"
			}
			reaped[reason]++
			reapedOldest[reason] = max(reapedOldest[reason], max(0, now.Sub(s.DeletionTimestamp.Time).Seconds()))
		}
		if cond := meta.FindStatusCondition(s.Status.Conditions, ConditionRescueFailed); cond != nil && cond.Status == metav1.ConditionTrue {
			// The clean-up's own rescue is counted by dev_env_task_cleanup_*,
			// not this gauge, so the critical page is not raised for it.
			if !taskCleanupRescueExcluded(s, c.TaskCleanupOn) {
				ch <- prometheus.MustNewConstMetric(rescueFailedDesc, prometheus.GaugeValue, 1, s.Name, cond.Reason)
			}
			key := "unclassified"
			if r := s.Status.Rescue; r != nil && rescueKeyPattern.MatchString(r.NotificationKey) {
				key = r.NotificationKey
			}
			ch <- prometheus.MustNewConstMetric(rescueNotificationDesc, prometheus.GaugeValue, 1,
				s.Name, string(s.UID), strconv.FormatInt(s.Generation, 10), cond.Reason, key, c.RescueIncidentOwners.ownerFor(s, cond, list.Items, now), strconv.FormatBool(!s.DeletionTimestamp.IsZero()))
			if r := s.Status.Rescue; r != nil && r.At != nil {
				ch <- prometheus.MustNewConstMetric(rescueLatestAttemptDesc, prometheus.GaugeValue, float64(r.At.Unix()), s.Name, string(s.UID))
			}
		}
	}
	for phase, n := range counts {
		ch <- prometheus.MustNewConstMetric(sessionsDesc, prometheus.GaugeValue, float64(n), phase)
	}
	for _, reason := range reapedRetainedReasons {
		ch <- prometheus.MustNewConstMetric(reapedRetainedDesc, prometheus.GaugeValue, reaped[reason], reason)
		ch <- prometheus.MustNewConstMetric(reapedRetainedAgeDesc, prometheus.GaugeValue, reapedOldest[reason], reason)
	}
	if c.TerminalTaskReclaim {
		for _, reason := range terminalTaskRetainedReasons {
			ch <- prometheus.MustNewConstMetric(terminalTaskRetainedDesc, prometheus.GaugeValue, retained[reason], reason)
			ch <- prometheus.MustNewConstMetric(terminalTaskRetainedAgeDesc, prometheus.GaugeValue, oldest[reason], reason)
		}
	}
}

// taskCleanupRescueExcluded reports whether this failed rescue is the task
// clean-up's own, so dev_env_session_rescue_failed leaves it out. The exact
// test is the clean-up's (taskRescueRefusedIn): under the task policy, the
// newest rescue ran in the record's hold pod. rescue_refused is that fact
// stored on the next reconcile, and only for a refusal a retry does not
// clear, so the gauge drops when the clean-up's rescue replaces an earlier
// one, including before the refusal is stored and including a transient
// failure in the same pod. The daily retry clears the hold pod UID before
// the next rescue is stored; that gap stays out when the failed rescue is
// at or after the clock taskCleanupDueAt uses and did not run in the
// recorded executor. An earlier rescue keeps its own pod UID and still counts.
// DESIGN-025's hold pod is outside the task policy, so its failure still
// counts.
func taskCleanupRescueExcluded(s *v1alpha1.AgentSession, cleanupOn bool) bool {
	if !taskPolicyOf(s, cleanupOn) {
		return false
	}
	e := s.Status.PrivateHistory.Expiry
	rec := s.Status.Rescue
	if e == nil || rec == nil || rec.Superseded || rec.Result != v1alpha1.RescueFailed {
		return false
	}
	if e.HoldPodUID != "" && rec.PodUID == e.HoldPodUID {
		return true
	}
	// The daily retry clears HoldPodUID and writes a new HoldIntent before
	// the next hold pod's rescue replaces status.rescue. The volume-release
	// wait in that gap is 30 minutes, longer than DevEnvRescueFailed's 20
	// minutes. The first attempt reaches the same status shape, so the gap
	// is left out only when this failed rescue is one the clean-up wrote:
	// at or after the clock taskCleanupDueAt uses, and not in the recorded
	// executor. An earlier failure, including a D-55 hold pod, still counts.
	return e.Phase == v1alpha1.HomeExpiryRescuing && e.HoldIntent != "" && e.HoldPodUID == "" &&
		rec.PodUID != e.ExecutorUID && taskCleanupRescueAfterDue(s, e, rec)
}

// taskCleanupRescueAfterDue reports whether the rescue is at or after the
// time the clean-up was allowed to run it. A reaped Session is due at grace
// start, not the stored dueAt (taskCleanupDueAt). Release-now can be earlier
// than both.
func taskCleanupRescueAfterDue(s *v1alpha1.AgentSession, e *v1alpha1.PrivateHomeExpiryStatus, rec *v1alpha1.RescueStatus) bool {
	if rec.At == nil {
		return false
	}
	due := e.DueAt.Time
	if !s.DeletionTimestamp.IsZero() {
		due = e.GraceStart.Time
	}
	if e.ReleaseNowAt != nil && !e.ReleaseNowAt.IsZero() && (due.IsZero() || e.ReleaseNowAt.Time.Before(due)) {
		due = e.ReleaseNowAt.Time
	}
	return !due.IsZero() && !rec.At.Time.Before(due)
}
