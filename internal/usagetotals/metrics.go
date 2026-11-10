package usagetotals

import (
	"errors"

	"github.com/prometheus/client_golang/prometheus"
)

// Metric names (DESIGN-059, totals that survive clean-up).
const (
	MetricTokens   = "dev_env_task_usage_tokens_total"
	MetricCostUSD  = "dev_env_task_usage_cost_usd_total"
	MetricSessions = "dev_env_task_usage_sessions_total"
	MetricSeconds  = "dev_env_task_usage_seconds_total"
)

// Counters are the task-usage totals. Each replica registers them. Only the
// leader adds. A new process starts them at zero.
type Counters struct {
	Tokens   *prometheus.CounterVec
	CostUSD  *prometheus.CounterVec
	Sessions *prometheus.CounterVec
	Seconds  *prometheus.CounterVec
}

// NewCounters returns counters with no series yet. A series appears when a
// Session is added.
func NewCounters() *Counters {
	return &Counters{
		Tokens: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: MetricTokens,
			Help: "Tokens from a final task usage observation, added once per Session. Unknown is omitted. Cursor is omitted. The token label is input, output, cache_read, or cache_write.",
		}, []string{"agent", "model", "repository", "cost_kind", "token"}),
		CostUSD: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: MetricCostUSD,
			Help: "CLI-equivalent cost in USD from a final task usage observation, added once per Session. Unknown is omitted. Cursor is omitted. This is not a billed charge.",
		}, []string{"agent", "model", "repository", "cost_kind"}),
		Sessions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: MetricSessions,
			Help: "Task Sessions added to the usage totals, one per Session. Cursor is added when its task result is recorded. Other agents are added when the usage observation is final.",
		}, []string{"agent", "model"}),
		Seconds: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: MetricSeconds,
			Help: "Whole wall-clock seconds from Session creation to status.agent.task.finishedAt, for each Session added to the usage totals. A missing or earlier finish adds nothing.",
		}, []string{"agent", "model"}),
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

func (c *Counters) apply(con contribution) {
	c.Sessions.WithLabelValues(con.agent, con.model).Add(1)
	if con.seconds > 0 {
		c.Seconds.WithLabelValues(con.agent, con.model).Add(con.seconds)
	}
	for _, sample := range con.tokens {
		c.Tokens.WithLabelValues(con.agent, sample.model, sample.repository, sample.kind, sample.token).Add(sample.amount)
	}
	for _, sample := range con.costs {
		c.CostUSD.WithLabelValues(con.agent, sample.model, sample.repository, sample.kind).Add(sample.usd)
	}
}
