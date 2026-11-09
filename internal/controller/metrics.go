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
		"1 for each session whose newest rescue failed (condition RescueFailed True): D-10's mark, which keeps the volume and blocks archive until a human looks.",
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
	Namespace            string
	TerminalTaskReclaim  bool
	Now                  func() time.Time
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
			ch <- prometheus.MustNewConstMetric(rescueFailedDesc, prometheus.GaugeValue, 1, s.Name, cond.Reason)
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
