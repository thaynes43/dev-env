package coordretention

import "github.com/prometheus/client_golang/prometheus"

// Metric names from DESIGN-024 section 5. The leader exports them; other
// replicas do not. Every pass result is created at 0 when the runnable starts.
const (
	metricPasses       = "dev_env_coordination_retention_passes_total"
	metricLastProgress = "dev_env_coordination_retention_last_progress_timestamp_seconds"
	metricMarked       = "dev_env_coordination_retention_marked_total"
	metricGraceResets  = "dev_env_coordination_retention_grace_resets_total"
	metricDeleted      = "dev_env_coordination_retention_deleted_total"
	metricPending      = "dev_env_coordination_retention_pending"
	metricRefused      = "dev_env_coordination_retention_refused_total"
)

const (
	resultComplete           = "complete"
	resultCapped             = "capped"
	resultPartial            = "partial"
	resultShelfUnavailable   = "shelf_unavailable"
	resultFailed             = "failed"
	reasonUnknown            = "unknown"
	reasonArchiveOutdated    = "archive_outdated"
	reasonOwnerPresent       = "owner_present"
	reasonArchiveInvalid     = "archive_invalid"
	reasonArchiveMissing     = "archive_missing"
	reasonArchiveMismatch    = "archive_mismatch"
	reasonPreconditionFailed = "precondition_failed"
	reasonForbidden          = "forbidden"
)

var (
	passResults   = []string{resultComplete, resultCapped, resultPartial, resultShelfUnavailable, resultFailed}
	refuseReasons = []string{
		reasonUnknown, reasonArchiveOutdated, reasonOwnerPresent, reasonArchiveInvalid, reasonArchiveMissing,
		reasonArchiveMismatch, reasonPreconditionFailed, reasonForbidden,
	}
	metricKinds = []Kind{TaskClaim, PublishedContext, InboxMessage, InboxReadAck}
)

type retentionMetrics struct {
	Passes       *prometheus.CounterVec
	LastProgress prometheus.Gauge
	Marked       *prometheus.CounterVec
	GraceResets  *prometheus.CounterVec
	Deleted      *prometheus.CounterVec
	Pending      *prometheus.GaugeVec
	Refused      *prometheus.CounterVec
}

func newRetentionMetrics() *retentionMetrics {
	return &retentionMetrics{
		Passes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: metricPasses,
			Help: "Coordination retention passes by result. Every result is 0 when the leader's runnable starts.",
		}, []string{"result"}),
		LastProgress: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: metricLastProgress,
			Help: "Unix time when the retention leader started, then the end of each complete or capped pass.",
		}),
		Marked: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: metricMarked,
			Help: "Coordination records marked absent by the retention runnable.",
		}, []string{"kind"}),
		GraceResets: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: metricGraceResets,
			Help: "Coordination records whose grace was cleared with a present event.",
		}, []string{"kind"}),
		Deleted: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: metricDeleted,
			Help: "Coordination records deleted after the archive was verified.",
		}, []string{"kind"}),
		Pending: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: metricPending,
			Help: "Released coordination records still inside the grace, from the last complete or capped pass.",
		}, []string{"kind"}),
		Refused: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: metricRefused,
			Help: "Coordination records the retention runnable refused to delete.",
		}, []string{"kind", "reason"}),
	}
}

func (m *retentionMetrics) collectors() []prometheus.Collector {
	return []prometheus.Collector{m.Passes, m.LastProgress, m.Marked, m.GraceResets, m.Deleted, m.Pending, m.Refused}
}

func (m *retentionMetrics) prime() {
	for _, result := range passResults {
		m.Passes.WithLabelValues(result)
	}
	for _, kind := range metricKinds {
		name := string(kind)
		m.Marked.WithLabelValues(name)
		m.GraceResets.WithLabelValues(name)
		m.Deleted.WithLabelValues(name)
		m.Pending.WithLabelValues(name).Set(0)
		for _, reason := range refuseReasons {
			m.Refused.WithLabelValues(name, reason)
		}
	}
}

func (r *Runnable) countMarked(kind Kind) {
	if r.metrics != nil {
		r.metrics.Marked.WithLabelValues(string(kind)).Inc()
	}
}

func (r *Runnable) countGrace(kind Kind) {
	if r.metrics != nil {
		r.metrics.GraceResets.WithLabelValues(string(kind)).Inc()
	}
}

func (r *Runnable) countDeleted(kind Kind) {
	if r.metrics != nil {
		r.metrics.Deleted.WithLabelValues(string(kind)).Inc()
	}
}

func (r *Runnable) countRefused(kind Kind, reason string) {
	if r.metrics != nil {
		r.metrics.Refused.WithLabelValues(string(kind), reason).Inc()
	}
}

func (r *Runnable) finishPass(result string, pending map[Kind]int) string {
	if r.metrics == nil {
		return result
	}
	r.metrics.Passes.WithLabelValues(result).Inc()
	if result == resultComplete || result == resultCapped {
		r.metrics.LastProgress.Set(float64(r.now().Unix()))
		for _, kind := range metricKinds {
			r.metrics.Pending.WithLabelValues(string(kind)).Set(float64(pending[kind]))
		}
	}
	return result
}
