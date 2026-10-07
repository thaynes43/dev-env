package controller

import (
	"context"
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
	MetricRescueFailed = "dev_env_session_rescue_failed"
	MetricSessions     = "dev_env_sessions"
)

// collectTimeout bounds one scrape's read of the sessions.
const collectTimeout = 10 * time.Second

var (
	rescueFailedDesc = prometheus.NewDesc(MetricRescueFailed,
		"1 for each session whose newest rescue failed (condition RescueFailed True): D-10's mark, which keeps the volume and blocks archive until a human looks.",
		[]string{"session", "reason"}, nil)
	sessionsDesc = prometheus.NewDesc(MetricSessions,
		"The number of sessions in each phase. A phase with no session reports 0, so the series exist whenever the operator is scraped.",
		[]string{"phase"}, nil)
)

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
	Namespace string
}

// Describe implements prometheus.Collector.
func (c *SessionCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- rescueFailedDesc
	ch <- sessionsDesc
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
		if cond := meta.FindStatusCondition(s.Status.Conditions, ConditionRescueFailed); cond != nil && cond.Status == metav1.ConditionTrue {
			ch <- prometheus.MustNewConstMetric(rescueFailedDesc, prometheus.GaugeValue, 1, s.Name, cond.Reason)
		}
	}
	for phase, n := range counts {
		ch <- prometheus.MustNewConstMetric(sessionsDesc, prometheus.GaugeValue, float64(n), phase)
	}
}
