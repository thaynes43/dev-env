package asks

import (
	"errors"

	"github.com/prometheus/client_golang/prometheus"
)

// MetricNearLimit counts create attempts that saw a bound at or above 80 percent.
const MetricNearLimit = "dev_env_asks_near_limit_total"

// Limit scopes reported by MetricNearLimit and LimitError.
const (
	LimitOpen      = "open"
	LimitRequester = "requester"
	LimitNamespace = "namespace"
)

// NearLimit is the create-time observation. The operator registers it on its
// metrics registry when --enable-asks is set.
var NearLimit = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: MetricNearLimit,
	Help: "Ask creates that observed a count at or above 80 percent of an ask bound. The scope label is open, requester, or namespace.",
}, []string{"scope"})

// RegisterMetrics registers NearLimit. A second registration is success.
func RegisterMetrics(r prometheus.Registerer) error {
	if r == nil {
		return ErrUnavailable
	}
	if err := r.Register(NearLimit); err != nil {
		var already prometheus.AlreadyRegisteredError
		if errors.As(err, &already) {
			return nil
		}
		return err
	}
	return nil
}

func atEighty(count, limit int64) bool {
	return limit > 0 && count*100 >= limit*80
}
