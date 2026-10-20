package keeper

import (
	"sort"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Login gauges and the notifier's input (plan 13, story 13.1).
//
// The workers (claudeWorker, codexWorker) already read their journals every
// tick. They hand the result here as three words and an optional time, never a
// token, and the hub keeps only that. It is the single place the gauges and the
// notifier read, so both follow the journal and neither opens a second reader.
const (
	// ProviderClaude and ProviderCodex are the provider label values.
	ProviderClaude = "claude"
	ProviderCodex  = "codex"

	// LoginReady: the journal holds a usable login.
	LoginReady = "Ready"
	// LoginNeedsLogin: a person must sign in again.
	LoginNeedsLogin = "NeedsLogin"
	// LoginInProgress: a sign-in ceremony holds the journal (Codex pauses
	// refresh for its whole life). The login may be fine or lapsed; it is
	// neither, and the notifier treats it as neither.
	LoginInProgress = "LoginInProgress"
	// LoginUnknown: this process has not read the journal yet (a follower, or
	// the first seconds of a leader).
	LoginUnknown = "Unknown"

	metricLoginState = "dev_env_keeper_login_state"
	metricLoginLapse = "dev_env_keeper_login_lapse_timestamp_seconds"
)

var loginStates = []string{LoginReady, LoginNeedsLogin, LoginInProgress, LoginUnknown}

// LoginStatus is one provider's last observation.
type LoginStatus struct {
	State string
	// LapsesAt is when the whole login lapses; zero when the provider never
	// said. A zero value is exported as no series, never as 0.
	LapsesAt time.Time
}

// LoginHub holds the last LoginStatus of each provider. The zero value is not
// usable; a nil *LoginHub accepts and ignores every call, so a worker built
// without one (tests, a disabled provider) needs no checks.
type LoginHub struct {
	mu sync.Mutex
	m  map[string]LoginStatus
}

// NewLoginHub registers providers in the Unknown state, so their state gauge
// exists from the first scrape.
func NewLoginHub(providers ...string) *LoginHub {
	h := &LoginHub{m: map[string]LoginStatus{}}
	for _, p := range providers {
		h.m[p] = LoginStatus{State: LoginUnknown}
	}
	return h
}

// Report records an observation. An unknown state word is stored as Unknown.
func (h *LoginHub) Report(provider, state string, lapsesAt time.Time) {
	if h == nil {
		return
	}
	known := false
	for _, s := range loginStates {
		known = known || s == state
	}
	if !known {
		state = LoginUnknown
	}
	if state != LoginReady {
		lapsesAt = time.Time{}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.m == nil {
		h.m = map[string]LoginStatus{}
	}
	h.m[provider] = LoginStatus{State: state, LapsesAt: lapsesAt.UTC()}
}

// Snapshot is a copy of every observation.
func (h *LoginHub) Snapshot() map[string]LoginStatus {
	out := map[string]LoginStatus{}
	if h == nil {
		return out
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for k, v := range h.m {
		out[k] = v
	}
	return out
}

// LoginCollector exports the hub as dev_env_keeper_login_state{provider,state}
// (1 for the current state, 0 for the others) and
// dev_env_keeper_login_lapse_timestamp_seconds{provider} (Unix seconds; only
// while the provider reported a lapse date).
type LoginCollector struct {
	hub       *LoginHub
	stateDesc *prometheus.Desc
	lapseDesc *prometheus.Desc
}

// NewLoginCollector makes the collector for hub.
func NewLoginCollector(hub *LoginHub) *LoginCollector {
	return &LoginCollector{
		hub: hub,
		stateDesc: prometheus.NewDesc(metricLoginState,
			"The keeper's view of a shared login: 1 for the current state, 0 for the others.",
			[]string{"provider", "state"}, nil),
		lapseDesc: prometheus.NewDesc(metricLoginLapse,
			"Unix time when the shared login lapses and a person must sign in again. Absent while the provider has not said.",
			[]string{"provider"}, nil),
	}
}

func (c *LoginCollector) Describe(ch chan<- *prometheus.Desc) { ch <- c.stateDesc; ch <- c.lapseDesc }

func (c *LoginCollector) Collect(ch chan<- prometheus.Metric) {
	snap := c.hub.Snapshot()
	providers := make([]string, 0, len(snap))
	for p := range snap {
		providers = append(providers, p)
	}
	sort.Strings(providers)
	for _, p := range providers {
		st := snap[p]
		for _, s := range loginStates {
			v := 0.0
			if s == st.State {
				v = 1
			}
			ch <- prometheus.MustNewConstMetric(c.stateDesc, prometheus.GaugeValue, v, p, s)
		}
		if !st.LapsesAt.IsZero() {
			ch <- prometheus.MustNewConstMetric(c.lapseDesc, prometheus.GaugeValue, float64(st.LapsesAt.Unix()), p)
		}
	}
}
