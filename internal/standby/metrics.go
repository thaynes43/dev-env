package standby

import (
	"context"
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// Standby observation metrics (OpenProject #111, source slice). Every replica
// registers one collector and reads the API at scrape time. The series are
// recorded categories for a later alert. They are not Alive, admission,
// ownership, phone reachability, or a heartbeat expiry.

const (
	MetricEnabled         = "dev_env_standby_enabled"
	MetricSnapshotValid   = "dev_env_standby_snapshot_valid"
	MetricSessionCount    = "dev_env_standby_session_count"
	MetricLatestHeartbeat = "dev_env_standby_latest_heartbeat_timestamp_seconds"
	MetricBudgetExhausted = "dev_env_standby_creation_budget_exhausted"
	metricLabelState      = "state"
	collectTimeout        = 10 * time.Second
	stateRegistered       = "registered"
	stateStarting         = "starting"
	stateLoginRequired    = "login_required"
	stateOffline          = "offline"
	stateBooting          = "booting"
	stateMoving           = "moving"
	stateSuspended        = "suspended"
	stateTerminal         = "terminal"
	stateUnknown          = "unknown"
)

// observationStates is the only label value set. A valid scrape emits a count
// for each of them.
var observationStates = []string{
	stateRegistered,
	stateStarting,
	stateLoginRequired,
	stateOffline,
	stateBooting,
	stateMoving,
	stateSuspended,
	stateTerminal,
	stateUnknown,
}

var (
	standbyEnabledDesc = prometheus.NewDesc(MetricEnabled,
		"1 when --enable-standby-session is set for this process, otherwise 0. This is the configured flag.",
		nil, nil)
	standbySnapshotValidDesc = prometheus.NewDesc(MetricSnapshotValid,
		"1 when this scrape's required standby reads and decoding succeeded. 0 when a read, permission, or decode failed. A disabled collector reports 1 and does not read the API. Consumers need enabled 1 and snapshot_valid 1. An absent scrape is not a healthy standby.",
		nil, nil)
	standbySessionCountDesc = prometheus.NewDesc(MetricSessionCount,
		"Labeled standby AgentSessions in one recorded observation category. A valid scrape emits every fixed state, including zero. The categories are not phone acceptance.",
		[]string{metricLabelState}, nil)
	standbyHeartbeatDesc = prometheus.NewDesc(MetricLatestHeartbeat,
		"Latest valid recorded heartbeat for one observation category, as Unix seconds from the session record. Omitted when that category has no valid heartbeat. The value stays at the stored time as the collector clock advances. This is not a freshness threshold.",
		[]string{metricLabelState}, nil)
	standbyBudgetDesc = prometheus.NewDesc(MetricBudgetExhausted,
		"1 when the stored standby creation count has reached the existing limit of three creations in 30 minutes, otherwise 0. Reaching the limit is not a new creation and is not itself a missing phone entry.",
		nil, nil)
)

// Collector reports standby observations from a read-only API reader.
type Collector struct {
	// Reader lists labeled AgentSessions and gets the breaker ConfigMap.
	// A disabled collector does not call it.
	Reader client.Reader
	// SessionNamespace is the AgentSession namespace. StateNamespace holds
	// ConfigMap dev-env-standby.
	SessionNamespace string
	StateNamespace   string
	// Enabled is the existing --enable-standby-session flag.
	Enabled bool
	// Now is the clock. Nil means time.Now.
	Now func() time.Time
	// timeout bounds one scrape's reads. Zero means 10 seconds. Tests set a
	// shorter value.
	timeout time.Duration
}

// Describe implements prometheus.Collector.
func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- standbyEnabledDesc
	ch <- standbySnapshotValidDesc
	ch <- standbySessionCountDesc
	ch <- standbyHeartbeatDesc
	ch <- standbyBudgetDesc
}

// Collect implements prometheus.Collector. A failed read emits enabled and
// snapshot_valid 0 and omits the other standby series. It does not publish an
// invalid metric, so unrelated exporter series stay.
func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	enabled := 0.0
	if c.Enabled {
		enabled = 1
	}
	ch <- prometheus.MustNewConstMetric(standbyEnabledDesc, prometheus.GaugeValue, enabled)
	if !c.Enabled {
		emitValidStandby(ch, standbySnapshot{counts: zeroCounts(), budget: 0})
		return
	}
	snap, ok := c.snapshot()
	if !ok {
		ch <- prometheus.MustNewConstMetric(standbySnapshotValidDesc, prometheus.GaugeValue, 0)
		return
	}
	emitValidStandby(ch, snap)
}

func emitValidStandby(ch chan<- prometheus.Metric, snap standbySnapshot) {
	ch <- prometheus.MustNewConstMetric(standbySnapshotValidDesc, prometheus.GaugeValue, 1)
	for _, state := range observationStates {
		ch <- prometheus.MustNewConstMetric(standbySessionCountDesc, prometheus.GaugeValue, float64(snap.counts[state]), state)
	}
	ch <- prometheus.MustNewConstMetric(standbyBudgetDesc, prometheus.GaugeValue, snap.budget)
	for _, state := range observationStates {
		ts, ok := snap.latest[state]
		if !ok {
			continue
		}
		ch <- prometheus.MustNewConstMetric(standbyHeartbeatDesc, prometheus.GaugeValue, float64(ts), state)
	}
}

// RegisterCollector registers c on reg. A second registration returns
// prometheus.AlreadyRegisteredError and leaves the registry unchanged.
func RegisterCollector(reg prometheus.Registerer, c *Collector) error {
	if reg == nil || c == nil {
		return fmt.Errorf("standby metrics: registerer and collector are required")
	}
	return reg.Register(c)
}

type standbySnapshot struct {
	counts map[string]int
	latest map[string]int64
	budget float64
}

func zeroCounts() map[string]int {
	counts := make(map[string]int, len(observationStates))
	for _, state := range observationStates {
		counts[state] = 0
	}
	return counts
}

func (c *Collector) readTimeout() time.Duration {
	if c.timeout > 0 {
		return c.timeout
	}
	return collectTimeout
}

func (c *Collector) clock() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// snapshot reads the labeled sessions and the breaker ConfigMap. ok is false
// when either read fails, the ConfigMap cannot be decoded, or there is no reader.
// NotFound on the ConfigMap is empty creation history.
func (c *Collector) snapshot() (standbySnapshot, bool) {
	if c.Reader == nil {
		return standbySnapshot{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.readTimeout())
	defer cancel()
	now := c.clock()
	var list v1alpha1.AgentSessionList
	listErr := c.Reader.List(ctx, &list, client.InNamespace(c.SessionNamespace),
		client.MatchingLabels{v1alpha1.LabelStandby: "true"})
	var cm corev1.ConfigMap
	getErr := c.Reader.Get(ctx, types.NamespacedName{Namespace: c.StateNamespace, Name: DefaultStateName}, &cm)
	if listErr != nil || (getErr != nil && !apierrors.IsNotFound(getErr)) {
		return standbySnapshot{}, false
	}
	raw := ""
	if getErr == nil && cm.Data != nil {
		raw = cm.Data[stateKey]
	}
	recent, err := recentCreations(raw, now, DefaultWindow)
	if err != nil {
		return standbySnapshot{}, false
	}
	snap := standbySnapshot{counts: zeroCounts(), latest: map[string]int64{}}
	for i := range list.Items {
		item := &list.Items[i]
		state := observationState(item)
		if _, known := snap.counts[state]; !known {
			state = stateUnknown
		}
		snap.counts[state]++
		ts, ok := validHeartbeat(item, now)
		if !ok {
			continue
		}
		if prev, seen := snap.latest[state]; !seen || ts > prev {
			snap.latest[state] = ts
		}
	}
	if len(recent) >= DefaultMaxCreations {
		snap.budget = 1
	}
	return snap, true
}

// observationState puts one labeled record into exactly one fixed category.
// Foreign agent or mode wins, then Terminal, then suspended, then moving,
// then boot and the ready Running or Idle cases.
func observationState(s *v1alpha1.AgentSession) string {
	if s.Spec.Agent != v1alpha1.AgentClaude || s.Spec.Mode != v1alpha1.ModeRemote {
		return stateUnknown
	}
	if Terminal(s) {
		return stateTerminal
	}
	if s.Spec.OperatingMode == v1alpha1.OperatingModeSuspended || s.Status.Phase == v1alpha1.PhaseSuspended {
		return stateSuspended
	}
	if s.Status.Phase == v1alpha1.PhasePending || s.Status.Phase == v1alpha1.PhaseDraining {
		return stateMoving
	}
	agent := s.Status.Agent
	if agent == nil || (s.Status.Phase != v1alpha1.PhaseRunning && s.Status.Phase != v1alpha1.PhaseIdle) {
		return stateUnknown
	}
	switch agent.Boot {
	case protocol.BootBooting:
		return stateBooting
	case protocol.BootReady:
	default:
		return stateUnknown
	}
	remote := ""
	if s.Status.RemoteControl != nil {
		remote = s.Status.RemoteControl.State
	}
	if agent.Status == protocol.AgentLoginRequired || remote == protocol.RemoteLoginRequired {
		return stateLoginRequired
	}
	if stoppedAgent(agent.Status) || remote == protocol.RemoteOffline {
		return stateOffline
	}
	if remote == protocol.RemoteRegistered && ordinaryAgent(agent.Status) {
		return stateRegistered
	}
	if remote == protocol.RemoteStarting {
		return stateStarting
	}
	return stateUnknown
}

func ordinaryAgent(status string) bool {
	switch status {
	case protocol.AgentBusy, protocol.AgentIdle, protocol.AgentWaiting:
		return true
	default:
		return false
	}
}

func stoppedAgent(status string) bool {
	switch status {
	case protocol.AgentExited, protocol.AgentFailed, protocol.AgentInterrupted:
		return true
	default:
		return false
	}
}

// validHeartbeat reports a stored heartbeat's Unix seconds. The time has to be
// present, strictly after the Unix epoch, and not after now. An old time stays
// old; the function does not substitute the collector clock.
func validHeartbeat(s *v1alpha1.AgentSession, now time.Time) (int64, bool) {
	if s.Status.Agent == nil || s.Status.Agent.LastHeartbeat == nil {
		return 0, false
	}
	hb := s.Status.Agent.LastHeartbeat.Time
	if hb.IsZero() || hb.Unix() <= 0 || hb.After(now) {
		return 0, false
	}
	return hb.Unix(), true
}
