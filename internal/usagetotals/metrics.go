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

const helpSettle = " A new series in a process stays at 0 for five minutes before its first add."

// settleFor is how long a series exists at 0 in this process before its
// first add. Tests shorten it. There is no flag.
var settleFor = 5 * time.Minute

// Counters are the task-usage totals. Each replica registers them. Only the
// leader adds. A new process starts them at zero.
//
// A series is created at 0 and receives no add until it has existed here for
// settleFor. Collect does not change a series.
type Counters struct {
	Tokens   *prometheus.CounterVec
	CostUSD  *prometheus.CounterVec
	Sessions *prometheus.CounterVec
	Seconds  *prometheus.CounterVec

	mu   sync.Mutex
	born map[seriesKey]time.Time
	now  func() time.Time
}

type seriesKey struct {
	metric, agent, model, repository, kind, token string
}

// NewCounters returns counters with no series yet. A series appears at 0
// when a Session is prepared, and receives its add only after settleFor.
func NewCounters() *Counters {
	return &Counters{
		Tokens: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: MetricTokens,
			Help: "Tokens from a final task usage observation, added once per Session. Unknown is omitted. Cursor is omitted. The token label is input, output, cache_read, or cache_write." + helpSettle,
		}, []string{"agent", "model", "repository", "cost_kind", "token"}),
		CostUSD: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: MetricCostUSD,
			Help: "CLI-equivalent cost in USD from a final task usage observation, added once per Session. Unknown is omitted. Cursor is omitted. This is not a billed charge." + helpSettle,
		}, []string{"agent", "model", "repository", "cost_kind"}),
		Sessions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: MetricSessions,
			Help: "Task Sessions added to the usage totals, one per Session. Cursor is added when its task result is recorded. Other agents are added when the usage observation is final." + helpSettle,
		}, []string{"agent", "model"}),
		Seconds: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: MetricSeconds,
			Help: "Whole wall-clock seconds from Session creation to status.agent.task.finishedAt, for each Session added to the usage totals. A missing or earlier finish adds nothing." + helpSettle,
		}, []string{"agent", "model"}),
		born: map[seriesKey]time.Time{},
	}
}

// Register registers the counters. Registering the same collectors again is success.
func (c *Counters) Register(r prometheus.Registerer) error {
	if c == nil || r == nil {
		return errors.New("task usage counters are not configured")
	}
	for _, col := range []prometheus.Collector{c.Tokens, c.CostUSD, c.Sessions, c.Seconds} {
		if err := r.Register(col); err != nil {
			var already prometheus.AlreadyRegisteredError
			if errors.As(err, &already) && already.ExistingCollector == col {
				continue
			}
			return err
		}
	}
	return nil
}

// addIfSettled creates every series this contribution will add to, at 0.
// It adds the contribution only when each of those series has existed for
// settleFor. Otherwise it adds nothing and returns the longest remainder.
func (c *Counters) addIfSettled(con contribution) (bool, time.Duration) {
	if c == nil {
		return false, settleFor
	}
	now := c.clock()
	c.mu.Lock()
	defer c.mu.Unlock()
	var wait time.Duration
	for _, id := range seriesOf(con) {
		c.touch(id, now)
		remain := settleFor - now.Sub(c.born[id])
		if remain > wait {
			wait = remain
		}
	}
	if wait > 0 {
		return false, wait
	}
	c.Sessions.WithLabelValues(con.agent, con.model).Add(1)
	if con.seconds > 0 {
		c.Seconds.WithLabelValues(con.agent, con.model).Add(con.seconds)
	}
	for _, sample := range con.tokens {
		if sample.amount <= 0 {
			continue
		}
		c.Tokens.WithLabelValues(con.agent, sample.model, sample.repository, sample.kind, sample.token).Add(sample.amount)
	}
	for _, sample := range con.costs {
		if sample.usd <= 0 {
			continue
		}
		c.CostUSD.WithLabelValues(con.agent, sample.model, sample.repository, sample.kind).Add(sample.usd)
	}
	return true, 0
}

func (c *Counters) touch(id seriesKey, now time.Time) {
	switch id.metric {
	case MetricTokens:
		c.Tokens.WithLabelValues(id.agent, id.model, id.repository, id.kind, id.token)
	case MetricCostUSD:
		c.CostUSD.WithLabelValues(id.agent, id.model, id.repository, id.kind)
	case MetricSeconds:
		c.Seconds.WithLabelValues(id.agent, id.model)
	default:
		c.Sessions.WithLabelValues(id.agent, id.model)
	}
	if _, ok := c.born[id]; !ok {
		c.born[id] = now
	}
}

func seriesOf(con contribution) []seriesKey {
	out := []seriesKey{{metric: MetricSessions, agent: con.agent, model: con.model}}
	if con.seconds > 0 {
		out = append(out, seriesKey{metric: MetricSeconds, agent: con.agent, model: con.model})
	}
	for _, sample := range con.tokens {
		if sample.amount <= 0 {
			continue
		}
		out = append(out, seriesKey{metric: MetricTokens, agent: con.agent, model: sample.model, repository: sample.repository, kind: sample.kind, token: sample.token})
	}
	for _, sample := range con.costs {
		if sample.usd <= 0 {
			continue
		}
		out = append(out, seriesKey{metric: MetricCostUSD, agent: con.agent, model: sample.model, repository: sample.repository, kind: sample.kind})
	}
	return out
}

// clock is the time the settle and the count window both use. Tests set now.
// A nil receiver or an unset now uses time.Now, and nowhere else decides
// the time.
func (c *Counters) clock() time.Time {
	if c != nil && c.now != nil {
		return c.now()
	}
	return time.Now()
}
