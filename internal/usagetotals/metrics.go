package usagetotals

import (
	"errors"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Metric names (DESIGN-059, totals that survive clean-up).
const (
	MetricTokens   = "dev_env_task_usage_tokens_total"
	MetricCostUSD  = "dev_env_task_usage_cost_usd_total"
	MetricSessions = "dev_env_task_usage_sessions_total"
	MetricSeconds  = "dev_env_task_usage_seconds_total"
)

const scrapeNote = " A new series reports 0 for its first two minutes, then the total, so increase() counts that step. Reading the endpoint does not change the value."

// The operator ServiceMonitor scrapes every minute (haynes-ops
// kubernetes/main/apps/dev-env-system/operator/app/helmrelease.yaml,
// serviceMonitor.app). Two minutes covers one scrape of the zero and, after
// the total is visible, one scrape of the total.
const (
	zeroFor   = 2 * time.Minute
	markAfter = 2 * time.Minute
)

// Counters are the task-usage totals. Each replica registers them. Only the
// leader adds. A new process starts them at zero.
//
// Prometheus increase() ignores the gap before a series' first sample. A new
// series therefore reports 0 until it is zeroFor old, then the total. Collect
// does not change that: a curl, a timed-out scrape, or a second Prometheus
// during the two minutes still reads 0.
type Counters struct {
	mu        sync.Mutex
	series    map[seriesID]seriesState
	desc      map[string]*prometheus.Desc
	zeroFor   time.Duration
	markAfter time.Duration
	now       func() time.Time
}

type seriesID struct {
	metric, agent, model, repository, kind, token string
}

type seriesState struct {
	actual float64
	showAt time.Time
}

// NewCounters returns counters with no series yet. A series appears when a
// Session is added.
func NewCounters() *Counters {
	return &Counters{
		series:    map[seriesID]seriesState{},
		zeroFor:   zeroFor,
		markAfter: markAfter,
		desc: map[string]*prometheus.Desc{
			MetricTokens: prometheus.NewDesc(MetricTokens,
				"Tokens from a final task usage observation, added once per Session. Unknown is omitted. Cursor is omitted. The token label is input, output, cache_read, or cache_write."+scrapeNote,
				[]string{"agent", "model", "repository", "cost_kind", "token"}, nil),
			MetricCostUSD: prometheus.NewDesc(MetricCostUSD,
				"CLI-equivalent cost in USD from a final task usage observation, added once per Session. Unknown is omitted. Cursor is omitted. This is not a billed charge."+scrapeNote,
				[]string{"agent", "model", "repository", "cost_kind"}, nil),
			MetricSessions: prometheus.NewDesc(MetricSessions,
				"Task Sessions added to the usage totals, one per Session. Cursor is added when its task result is recorded. Other agents are added when the usage observation is final."+scrapeNote,
				[]string{"agent", "model"}, nil),
			MetricSeconds: prometheus.NewDesc(MetricSeconds,
				"Whole wall-clock seconds from Session creation to status.agent.task.finishedAt, for each Session added to the usage totals. A missing or earlier finish adds nothing."+scrapeNote,
				[]string{"agent", "model"}, nil),
		},
	}
}

// Register registers the counters. Registering the same collector again is success.
func (c *Counters) Register(r prometheus.Registerer) error {
	if c == nil || r == nil {
		return errors.New("task usage counters are not configured")
	}
	if err := r.Register(c); err != nil {
		var already prometheus.AlreadyRegisteredError
		if errors.As(err, &already) && already.ExistingCollector == c {
			return nil
		}
		return err
	}
	return nil
}

// Describe implements prometheus.Collector.
func (c *Counters) Describe(ch chan<- *prometheus.Desc) {
	for _, name := range []string{MetricTokens, MetricCostUSD, MetricSessions, MetricSeconds} {
		ch <- c.desc[name]
	}
}

// Collect implements prometheus.Collector. It does not change the counters.
// A series created less than zeroFor ago is reported as 0.
func (c *Counters) Collect(ch chan<- prometheus.Metric) {
	now := c.clock()
	c.mu.Lock()
	type sample struct {
		id    seriesID
		value float64
	}
	snap := make([]sample, 0, len(c.series))
	for id, st := range c.series {
		value := st.actual
		if now.Before(st.showAt) {
			value = 0
		}
		snap = append(snap, sample{id: id, value: value})
	}
	c.mu.Unlock()
	for _, s := range snap {
		ch <- prometheus.MustNewConstMetric(c.desc[s.id.metric], prometheus.CounterValue, s.value, s.id.labelValues()...)
	}
}

// apply adds one Session's contribution. The returned time is when that add
// is visible. A series still inside its first two minutes keeps the add
// hidden until that series' show time. A series that is already showing its
// total includes the add immediately. The annotation waits markAfter after
// the returned time.
func (c *Counters) apply(con contribution) time.Time {
	now := c.clock()
	visibleAt := now
	c.mu.Lock()
	defer c.mu.Unlock()
	bump := func(id seriesID, amount float64) {
		if amount <= 0 {
			return
		}
		st, ok := c.series[id]
		if !ok {
			st.showAt = now.Add(c.zeroFor)
		}
		st.actual += amount
		c.series[id] = st
		if st.showAt.After(visibleAt) {
			visibleAt = st.showAt
		}
	}
	bump(seriesID{metric: MetricSessions, agent: con.agent, model: con.model}, 1)
	bump(seriesID{metric: MetricSeconds, agent: con.agent, model: con.model}, con.seconds)
	for _, sample := range con.tokens {
		bump(seriesID{metric: MetricTokens, agent: con.agent, model: sample.model, repository: sample.repository, kind: sample.kind, token: sample.token}, sample.amount)
	}
	for _, sample := range con.costs {
		bump(seriesID{metric: MetricCostUSD, agent: con.agent, model: sample.model, repository: sample.repository, kind: sample.kind}, sample.usd)
	}
	return visibleAt
}

// ready reports whether the add has been visible for markAfter.
func (c *Counters) ready(visibleAt time.Time) bool {
	if c == nil || visibleAt.IsZero() {
		return false
	}
	return !c.clock().Before(visibleAt.Add(c.markAfter))
}

// untilReady is the wait until ready. A short positive wait avoids a tight
// requeue when the clock is within a second of the deadline.
func (c *Counters) untilReady(visibleAt time.Time) time.Duration {
	if c == nil || visibleAt.IsZero() {
		return markAfter
	}
	wait := visibleAt.Add(c.markAfter).Sub(c.clock())
	if wait < time.Second {
		return time.Second
	}
	return wait
}

func (c *Counters) clock() time.Time {
	if c != nil && c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *Counters) actual(metric, agent, model string) float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.series[seriesID{metric: metric, agent: agent, model: model}].actual
}

func (id seriesID) labelValues() []string {
	switch id.metric {
	case MetricTokens:
		return []string{id.agent, id.model, id.repository, id.kind, id.token}
	case MetricCostUSD:
		return []string{id.agent, id.model, id.repository, id.kind}
	default:
		return []string{id.agent, id.model}
	}
}
