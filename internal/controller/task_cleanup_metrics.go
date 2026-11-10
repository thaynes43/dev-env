package controller

// The task clean-up's metrics (DESIGN-061 section 6). The counters rise only
// in the leader. The state gauges are read from the cache at scrape time on
// every replica, so alerts use max; a sum doubles. The paused gauge reads the
// leader's memory and the flag, so a replica that is not the leader reports
// only max_active_zero.

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/projectcanary"
)

// The clean-up's metric names.
const (
	MetricTaskCleanupSessions = "dev_env_task_cleanup_sessions"
	MetricTaskCleanupOldest   = "dev_env_task_cleanup_oldest_seconds"
	MetricTaskCleanupPaused   = "dev_env_task_cleanup_paused"
	MetricTaskCleanupDeleted  = "dev_env_task_cleanup_deleted_total"
	MetricTaskCleanupRefused  = "dev_env_task_cleanup_refused_total"
	MetricTaskCleanupDuration = "dev_env_task_cleanup_duration_seconds"
)

// The states of dev_env_task_cleanup_sessions, each present at 0.
const (
	taskStateWaiting   = "waiting"
	taskStateQueued    = "queued"
	taskStateRescuing  = "rescuing"
	taskStateSaved     = "saved"
	taskStateDeleting  = "deleting"
	taskStateRefused   = "refused"
	taskStateHeld      = "held"
	taskStateUnproven  = "unproven"
	taskStateProtected = "protected"
)

var taskCleanupStates = []string{taskStateWaiting, taskStateQueued, taskStateRescuing, taskStateSaved, taskStateDeleting,
	taskStateRefused, taskStateHeld, taskStateUnproven, taskStateProtected}

// TaskCleanupCounters are the clean-up's counters. Register them once per
// process, whatever the flag, so each series exists at 0.
type TaskCleanupCounters struct {
	Deleted  *prometheus.CounterVec
	Refused  *prometheus.CounterVec
	Duration prometheus.Histogram
}

// NewTaskCleanupCounters returns the counters with every cause and reason at 0.
func NewTaskCleanupCounters() *TaskCleanupCounters {
	c := &TaskCleanupCounters{
		Deleted: prometheus.NewCounterVec(prometheus.CounterOpts{Name: MetricTaskCleanupDeleted,
			Help: "Task Sessions the clean-up removed after deleting their HOME (DESIGN-061), by cause: purged or finished."}, []string{"cause"}),
		Refused: prometheus.NewCounterVec(prometheus.CounterOpts{Name: MetricTaskCleanupRefused,
			Help: "Refused task clean-up steps (DESIGN-061), by reason; the HOME is kept."}, []string{"reason"}),
		Duration: prometheus.NewHistogram(prometheus.HistogramOpts{Name: MetricTaskCleanupDuration,
			Help:    "Seconds from a clean-up's start (it left Waiting, as this leader saw it) to its Session's removal (DESIGN-061).",
			Buckets: []float64{60, 120, 180, 300, 600, 900, 1800, 3600, 7200}}),
	}
	for _, cause := range []string{"purged", "finished"} {
		c.Deleted.WithLabelValues(cause)
	}
	for _, reason := range homeExpiryReasons {
		c.Refused.WithLabelValues(reason)
	}
	return c
}

// Collectors are the counters to register.
func (c *TaskCleanupCounters) Collectors() []prometheus.Collector {
	return []prometheus.Collector{c.Deleted, c.Refused, c.Duration}
}

var (
	taskCleanupSessionsDesc = prometheus.NewDesc(MetricTaskCleanupSessions,
		"Finished or purged task Sessions by clean-up state (DESIGN-061 6). 0 everywhere while the clean-up is off.", []string{"state"}, nil)
	taskCleanupOldestDesc = prometheus.NewDesc(MetricTaskCleanupOldest,
		"Seconds since the clock (the recorded stop; for unproven, the missed executor or the reap) of the oldest Session in each state; 0 when none.",
		[]string{"state"}, nil)
	taskCleanupPausedDesc = prometheus.NewDesc(MetricTaskCleanupPaused,
		"1 while the task clean-up starts no new clean-up for this reason (DESIGN-061 4).", []string{"reason"}, nil)
)

// TaskCleanupCollector is a prometheus.Collector of the clean-up's gauges over
// the sessions in one namespace.
type TaskCleanupCollector struct {
	Reader    client.Reader
	Namespace string
	Now       func() time.Time
	// Reconciler is the operator's, for its flags and the leader's memory.
	Reconciler *Reconciler
}

// Describe implements prometheus.Collector.
func (c *TaskCleanupCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- taskCleanupSessionsDesc
	ch <- taskCleanupOldestDesc
	ch <- taskCleanupPausedDesc
}

// Collect implements prometheus.Collector.
func (c *TaskCleanupCollector) Collect(ch chan<- prometheus.Metric) {
	now := time.Now()
	if c.Now != nil {
		now = c.Now()
	}
	paused := c.Reconciler.TaskCleanupPaused(now)
	for _, reason := range taskCleanupPauseReasons {
		v := 0.0
		if paused[reason] {
			v = 1
		}
		ch <- prometheus.MustNewConstMetric(taskCleanupPausedDesc, prometheus.GaugeValue, v, reason)
	}
	counts, oldest := map[string]int{}, map[string]float64{}
	if c.Reconciler.taskCleanupOn() {
		ctx, cancel := context.WithTimeout(context.Background(), collectTimeout)
		defer cancel()
		var list v1alpha1.AgentSessionList
		if err := c.Reader.List(ctx, &list, client.InNamespace(c.Namespace), client.UnsafeDisableDeepCopy); err != nil {
			ch <- prometheus.NewInvalidMetric(taskCleanupSessionsDesc, err)
			return
		}
		counts, oldest = c.Reconciler.taskCleanupGauges(list.Items, now)
	}
	for _, st := range taskCleanupStates {
		ch <- prometheus.MustNewConstMetric(taskCleanupSessionsDesc, prometheus.GaugeValue, float64(counts[st]), st)
		ch <- prometheus.MustNewConstMetric(taskCleanupOldestDesc, prometheus.GaugeValue, oldest[st], st)
	}
}

// taskCleanupGauges sorts the finished or purged task Sessions into the
// gauge's states, and finds each state's oldest clock.
func (r *Reconciler) taskCleanupGauges(sessions []v1alpha1.AgentSession, now time.Time) (map[string]int, map[string]float64) {
	counts, oldest := map[string]int{}, map[string]float64{}
	add := func(state string, clock time.Time) {
		counts[state]++
		if !clock.IsZero() && now.After(clock) {
			oldest[state] = max(oldest[state], now.Sub(clock).Seconds())
		}
	}
	for i := range sessions {
		s := &sessions[i]
		h := s.Status.PrivateHistory
		if s.Spec.Mode != v1alpha1.ModeTask || s.Spec.Workspace != nil || h == nil {
			continue
		}
		var clock time.Time
		switch {
		case executorTerminalRecorded(h):
			clock = h.ExecutorTerminal.ObservedAt.Time
		case h.ExecutorMissed != nil:
			clock = h.ExecutorMissed.ObservedAt.Time
		case !s.DeletionTimestamp.IsZero():
			clock = s.DeletionTimestamp.Time
		default:
			// Still running: not finished or purged.
			continue
		}
		if s.Spec.ProjectCanary != nil || projectcanary.IsPilot(s) {
			add(taskStateProtected, clock)
			continue
		}
		if !taskPolicyOf(s, true) {
			// An original-HOME binding, or a record DESIGN-025 keeps.
			add(taskStateProtected, clock)
			continue
		}
		if !executorTerminalRecorded(h) {
			add(taskStateUnproven, clock)
			continue
		}
		e := h.Expiry
		switch {
		case e != nil && e.Phase == v1alpha1.HomeExpiryExpired:
			continue
		case homeExpiryHeld(s):
			add(taskStateHeld, clock)
		case e != nil && e.Refusal == expiryVolumeNotReleased:
			add(taskStateUnproven, clock)
		case e != nil && e.Refusal != "":
			add(taskStateRefused, clock)
		case e == nil || e.Phase == v1alpha1.HomeExpiryWaiting:
			due := clock.Add(r.taskCleanupGrace())
			if e != nil {
				due = r.taskCleanupDueAt(s)
			} else if taskCleanupPurged(s) {
				due = clock
			}
			if now.Before(due) {
				add(taskStateWaiting, clock)
			} else {
				add(taskStateQueued, clock)
			}
		case e.Phase == v1alpha1.HomeExpiryRescuing:
			add(taskStateRescuing, clock)
		case e.Phase == v1alpha1.HomeExpiryArchived:
			add(taskStateSaved, clock)
		case e.Phase == v1alpha1.HomeExpiryDeleting:
			add(taskStateDeleting, clock)
		}
	}
	return counts, oldest
}
