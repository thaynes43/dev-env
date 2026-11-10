package usagetotals

import (
	"errors"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// Metric names (DESIGN-059, totals that survive clean-up).
const (
	MetricTokens   = "dev_env_task_usage_tokens_total"
	MetricCostUSD  = "dev_env_task_usage_cost_usd_total"
	MetricSessions = "dev_env_task_usage_sessions_total"
	MetricSeconds  = "dev_env_task_usage_seconds_total"
)

const scrapeNote = " The first scrape of a series in a process is 0, and the next scrape reports the total, so increase() counts that step."

// Counters are the task-usage totals. Each replica registers them. Only the
// leader adds. A new process starts them at zero.
//
// Prometheus increase() ignores the gap before a series' first sample. The
// first Collect of a series therefore reports 0, even when Sessions were
// already added, and later Collects report the total.
type Counters struct {
	mu     sync.Mutex
	epoch  uint64
	series map[seriesID]seriesState
	desc   map[string]*prometheus.Desc
}

type seriesID struct {
	metric, agent, model, repository, kind, token string
}

type seriesState struct {
	actual    float64
	epoch     uint64
	primed    bool
	published uint64
}

// NewCounters returns counters with no series yet. A series appears when a
// Session is added.
func NewCounters() *Counters {
	return &Counters{
		series: map[seriesID]seriesState{},
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

// Collect implements prometheus.Collector. The first observation of a series
// is 0. Later observations are the total added so far.
func (c *Counters) Collect(ch chan<- prometheus.Metric) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, st := range c.series {
		value := st.actual
		if !st.primed {
			value = 0
			st.primed = true
		} else {
			st.published = st.epoch
		}
		c.series[id] = st
		ch <- prometheus.MustNewConstMetric(c.desc[id.metric], prometheus.CounterValue, value, id.labelValues()...)
	}
}

// apply adds one Session's contribution. The returned epoch and series are
// ready once a later Collect has reported those series at this total.
func (c *Counters) apply(con contribution) (uint64, []seriesID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.epoch++
	epoch := c.epoch
	var touched []seriesID
	bump := func(id seriesID, amount float64) {
		if amount <= 0 {
			return
		}
		st := c.series[id]
		st.actual += amount
		st.epoch = epoch
		c.series[id] = st
		touched = append(touched, id)
	}
	bump(seriesID{metric: MetricSessions, agent: con.agent, model: con.model}, 1)
	bump(seriesID{metric: MetricSeconds, agent: con.agent, model: con.model}, con.seconds)
	for _, sample := range con.tokens {
		bump(seriesID{metric: MetricTokens, agent: con.agent, model: sample.model, repository: sample.repository, kind: sample.kind, token: sample.token}, sample.amount)
	}
	for _, sample := range con.costs {
		bump(seriesID{metric: MetricCostUSD, agent: con.agent, model: sample.model, repository: sample.repository, kind: sample.kind}, sample.usd)
	}
	return epoch, touched
}

// ready reports whether a Collect has reported every series at or after epoch.
func (c *Counters) ready(epoch uint64, touched []seriesID) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(touched) == 0 {
		return false
	}
	for _, id := range touched {
		if c.series[id].published < epoch {
			return false
		}
	}
	return true
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
