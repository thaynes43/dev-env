package keeper

import (
	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

var claudeProbeLabels = [...]string{
	claudeProbeHealthy:         "healthy",
	claudeProbeUnauthorized:    "unauthorized",
	claudeProbeForbidden:       "forbidden",
	claudeProbeRateLimited:     "rate-limited",
	claudeProbeServerError:     "server-error",
	claudeProbeTimeout:         "timeout",
	claudeProbeMalformed:       "malformed",
	claudeProbeAccountMismatch: "account-mismatch",
	claudeProbeUnavailable:     "unavailable",
}

var claudeRecoveryPageLabels = [...]string{
	claudeRecoveryNotSent:         "not-sent",
	claudeRecoveryPossiblySpent:   "possibly-spent",
	claudeRecoveryRefreshStatus:   "status",
	claudeRecoveryRead:            "read",
	claudeRecoveryInvalid:         "invalid",
	claudeRecoveryAccountMismatch: "account-mismatch",
	claudeRecoveryDeadline:        "deadline",
	claudeRecoveryFlapping:        "flapping",
	claudeRecoveryAttemptLimit:    "attempt-limit",
}

type claudeEarlyMetrics struct {
	probes, outcomes        *prometheus.CounterVec
	open, hold, unavailable prometheus.Gauge

	// Repeated reads of the same incident must not count its events again.
	// These fields are read and updated under the worker mutex.
	journal     types.UID
	seen, valid bool
	incident    claudeRecoveryIncident
}

func newClaudeEarlyMetrics() *claudeEarlyMetrics {
	m := &claudeEarlyMetrics{
		probes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "dev_env_keeper_claude_probe_results_total",
			Help: "Completed keeper Claude profile probes by fixed result class.",
		}, []string{"result"}),
		outcomes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "dev_env_keeper_claude_early_recovery_outcomes_total",
			Help: "Early recovery incident transitions observed by this keeper, by fixed outcome.",
		}, []string{"outcome"}),
		open: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "dev_env_keeper_claude_early_recovery_incident_open",
			Help: "Whether the keeper has observed an open Claude early recovery incident.",
		}),
		hold: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "dev_env_keeper_claude_early_recovery_flapping_hold",
			Help: "Whether the keeper has observed an early recovery hold, including the daily attempt cap.",
		}),
		unavailable: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "dev_env_keeper_claude_early_recovery_journal_unavailable",
			Help: "Whether the keeper cannot read or decode the Claude early recovery journal.",
		}),
	}
	for _, label := range claudeProbeLabels[1:] {
		m.probes.WithLabelValues(label)
	}
	for _, label := range claudeRecoveryPageLabels[1:] {
		m.outcomes.WithLabelValues(label)
	}
	for _, label := range []string{"replacement-saved", "resolved", "journal-repaired"} {
		m.outcomes.WithLabelValues(label)
	}
	return m
}

func (m *claudeEarlyMetrics) collectors() []prometheus.Collector {
	return []prometheus.Collector{m.probes, m.outcomes, m.open, m.hold, m.unavailable}
}

func (w *claudeWorker) countClaudeProbe(class claudeProbeClass) {
	if m := w.RecoveryMetrics; m != nil {
		m.probes.WithLabelValues(claudeProbeLabels[class]).Inc()
	}
}

func (w *claudeWorker) observeClaudeRecovery(secret *corev1.Secret, generation uint64) {
	if w.RecoveryMetrics == nil {
		return
	}
	state, valid := loadClaudeRecovery(secret, generation)
	w.reportClaudeRecovery(secret.UID, state, valid)
}

func (w *claudeWorker) reportClaudeRecovery(uid types.UID, state claudeRecoveryState, valid bool) {
	m := w.RecoveryMetrics
	if m == nil {
		return
	}
	if m.journal != uid {
		m.incident = claudeRecoveryIncident{}
	}
	setClaudeGauge(m.unavailable, !valid)
	if valid || m.journal != uid {
		setClaudeGauge(m.open, state.Incident.Generation != 0 && !state.Incident.Resolved)
		setClaudeGauge(m.hold, state.Incident.Held)
	}
	if m.seen && m.journal == uid && valid {
		i, previous := state.Incident, m.incident
		same := i.ReferenceHigh == previous.ReferenceHigh && i.ReferenceLow == previous.ReferenceLow
		if !m.valid {
			m.outcomes.WithLabelValues("journal-repaired").Inc()
		}
		if i.Replacement != 0 && (!same || previous.Replacement == 0) {
			m.outcomes.WithLabelValues("replacement-saved").Inc()
		}
		if i.Page.Delivery != 0 && (!same || previous.Page.Delivery == 0) {
			m.outcomes.WithLabelValues(claudeRecoveryPageLabels[i.Page.Failure]).Inc()
		}
		if i.Held && (!same || !previous.Held) && i.Page.Failure != claudeRecoveryAttemptLimit && i.Page.Failure != claudeRecoveryFlapping {
			// A previous page can coexist with a later hold on the incident.
			m.outcomes.WithLabelValues("flapping").Inc()
		}
		if i.Resolved && same && !previous.Resolved {
			m.outcomes.WithLabelValues("resolved").Inc()
		}
	}
	m.journal, m.seen, m.valid = uid, true, valid
	if valid {
		m.incident = state.Incident
	}
}

func setClaudeGauge(g prometheus.Gauge, active bool) {
	if active {
		g.Set(1)
	} else {
		g.Set(0)
	}
}
