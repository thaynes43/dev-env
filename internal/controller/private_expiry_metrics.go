package controller

// The HOME expiry's metrics (DESIGN-025 section 10). The counters rise only
// in the leader, which alone reconciles. The gauges are read from the
// sessions at scrape time, as D-57's are: every replica reports the same
// values from its own cache, a new leader needs no warm-up, and a reaped
// Session's series end with it. The refusing gauge reads each record's
// refusal, so DevEnvHomeExpiryNeedsOwner fires until the refusal is gone,
// whatever the retry cadence.

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// HomeExpiryCounters are the expiry's counters, exported by the leader, which
// alone reconciles. Register them once per process, whatever the flag, so each
// series exists at 0.
type HomeExpiryCounters struct {
	Refused       *prometheus.CounterVec
	Expired       prometheus.Counter
	ArchivedBytes prometheus.Counter
}

// The expiry's metric names (DESIGN-025 section 10).
const (
	MetricHomeExpiryRefused       = "dev_env_home_expiry_refused_total"
	MetricHomeExpiryExpired       = "dev_env_home_expiry_expired_total"
	MetricHomeExpiryArchivedBytes = "dev_env_home_expiry_archived_bytes_total"
	MetricHomeExpirySessions      = "dev_env_home_expiry_sessions"
	MetricHomeExpiryRefusing      = "dev_env_home_expiry_refusing"
	MetricHomeExpiryOverdue       = "dev_env_home_expiry_overdue_seconds"
)

// NewHomeExpiryCounters returns the counters with every reason at 0.
func NewHomeExpiryCounters() *HomeExpiryCounters {
	c := &HomeExpiryCounters{
		Refused: prometheus.NewCounterVec(prometheus.CounterOpts{Name: MetricHomeExpiryRefused,
			Help: "Refused HOME expiry steps (DESIGN-025 10), by reason. unknown is a read, exec or deadline that could not be judged; the rest are re-checked every 15 minutes."}, []string{"reason"}),
		Expired: prometheus.NewCounter(prometheus.CounterOpts{Name: MetricHomeExpiryExpired,
			Help: "Private HOMEs deleted after a verified archive (DESIGN-025 6.5). Deletion is off until H9."}),
		ArchivedBytes: prometheus.NewCounter(prometheus.CounterOpts{Name: MetricHomeExpiryArchivedBytes,
			Help: "Bytes of provider history archived and verified, counted once per expiry record when Archived is stored."}),
	}
	for _, reason := range homeExpiryReasons {
		c.Refused.WithLabelValues(reason)
	}
	return c
}

// Collectors are the counters to register.
func (c *HomeExpiryCounters) Collectors() []prometheus.Collector {
	return []prometheus.Collector{c.Refused, c.Expired, c.ArchivedBytes}
}

// The phases dev_env_home_expiry_sessions reports, each present at 0.
const (
	expiryGaugeUnproven  = "unproven"
	expiryGaugeWaiting   = "waiting"
	expiryGaugeHeld      = "held"
	expiryGaugeRescuing  = "rescuing"
	expiryGaugeExporting = "exporting"
	expiryGaugeArchived  = "archived"
	expiryGaugeDeleting  = "deleting"
)

var homeExpiryGaugePhases = []string{expiryGaugeUnproven, expiryGaugeWaiting, expiryGaugeHeld, expiryGaugeRescuing,
	expiryGaugeExporting, expiryGaugeArchived, expiryGaugeDeleting}

var (
	homeExpirySessionsDesc = prometheus.NewDesc(MetricHomeExpirySessions,
		"Private Sessions in each HOME expiry state (DESIGN-025 10): unproven (retained, no terminal record, so never expiring), held (an owner hold), or the expiry record's phase.",
		[]string{"phase"}, nil)
	homeExpiryRefusingDesc = prometheus.NewDesc(MetricHomeExpiryRefusing,
		"Private Sessions whose HOME expiry is refused right now, by the reason in their expiry record. A Session leaves it when a step passes, its record goes, or it is gone.",
		[]string{"reason"}, nil)
	homeExpiryOverdueDesc = prometheus.NewDesc(MetricHomeExpiryOverdue,
		"The largest time past its grace of an unheld HOME expiry record not yet Archived (not yet Expired with the delete on), in seconds; 0 when none.",
		nil, nil)
)

// HomeExpiryCollector is a prometheus.Collector of the HOME expiry's gauges
// over the sessions in one namespace. Register it with controller-runtime's
// metrics registry.
type HomeExpiryCollector struct {
	// Reader lists sessions; the manager's cache in the operator.
	Reader client.Reader
	// Namespace is where the sessions live (dev-agents).
	Namespace string
	// Now is the clock of the overdue gauge; nil is time.Now.
	Now func() time.Time
	// DeleteOn is the operator's --enable-home-expiry-delete: with it, a
	// record is overdue until Expired, not until Archived (DESIGN-025 10).
	DeleteOn bool
}

// Describe implements prometheus.Collector.
func (c *HomeExpiryCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- homeExpirySessionsDesc
	ch <- homeExpiryRefusingDesc
	ch <- homeExpiryOverdueDesc
}

// Collect implements prometheus.Collector. A failed list fails the scrape, as
// SessionCollector's does: a partial list could hide a refused Session.
func (c *HomeExpiryCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), collectTimeout)
	defer cancel()
	var list v1alpha1.AgentSessionList
	if err := c.Reader.List(ctx, &list, client.InNamespace(c.Namespace)); err != nil {
		ch <- prometheus.NewInvalidMetric(homeExpirySessionsDesc, err)
		return
	}
	now := time.Now()
	if c.Now != nil {
		now = c.Now()
	}
	phases, refusing, overdue := homeExpiryGauges(list.Items, now, c.DeleteOn)
	for _, p := range homeExpiryGaugePhases {
		ch <- prometheus.MustNewConstMetric(homeExpirySessionsDesc, prometheus.GaugeValue, float64(phases[p]), p)
	}
	for _, reason := range homeExpiryReasons {
		ch <- prometheus.MustNewConstMetric(homeExpiryRefusingDesc, prometheus.GaugeValue, float64(refusing[reason]), reason)
	}
	ch <- prometheus.MustNewConstMetric(homeExpiryOverdueDesc, prometheus.GaugeValue, overdue)
}

// homeExpiryGauges computes the gauges from the sessions: a pure function, so
// every replica reports the same.
func homeExpiryGauges(sessions []v1alpha1.AgentSession, now time.Time, deleteOn bool) (phases, refusing map[string]int, overdue float64) {
	done := v1alpha1.HomeExpiryArchived
	if deleteOn {
		done = v1alpha1.HomeExpiryExpired
	}
	phases, refusing = map[string]int{}, map[string]int{}
	for i := range sessions {
		s := &sessions[i]
		h := s.Status.PrivateHistory
		if s.Spec.Workspace != nil || s.Spec.ProjectCanary != nil || h == nil {
			continue
		}
		e := h.Expiry
		if e == nil {
			if !executorTerminalRecorded(h) {
				phases[expiryGaugeUnproven]++
			}
			continue
		}
		if e.Refusal != "" {
			refusing[e.Refusal]++
		}
		held := homeExpiryHeld(s)
		switch {
		case e.Phase == v1alpha1.HomeExpiryExpired:
			continue
		case held:
			phases[expiryGaugeHeld]++
		default:
			phases[map[string]string{v1alpha1.HomeExpiryWaiting: expiryGaugeWaiting, v1alpha1.HomeExpiryRescuing: expiryGaugeRescuing,
				v1alpha1.HomeExpiryExporting: expiryGaugeExporting, v1alpha1.HomeExpiryArchived: expiryGaugeArchived,
				v1alpha1.HomeExpiryDeleting: expiryGaugeDeleting}[e.Phase]]++
		}
		if !held && !expiryPhaseAtLeast(e.Phase, done) && now.After(e.DueAt.Time) {
			overdue = max(overdue, now.Sub(e.DueAt.Time).Seconds())
		}
	}
	return phases, refusing, overdue
}
