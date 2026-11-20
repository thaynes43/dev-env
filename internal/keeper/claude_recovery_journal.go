package keeper

import (
	"bytes"
	"encoding/json"
	"errors"
	"time"

	corev1 "k8s.io/api/core/v1"
	kubejson "sigs.k8s.io/json"
)

// Slice 6 keeps its evidence separate so that a keeper which only understands
// slice 3 can still append its v1 rows without erasing the early recovery record.
const claudeRecoveryKey = "early-recovery.json"

var errClaudeRecoveryJournal = errors.New("claude early recovery journal is invalid")

type claudeRecoveryFailure uint8

const (
	claudeRecoveryNotSent claudeRecoveryFailure = iota + 1
	claudeRecoveryPossiblySpent
	claudeRecoveryRefreshStatus
	claudeRecoveryRead
	claudeRecoveryInvalid
	claudeRecoveryAccountMismatch
	claudeRecoveryDeadline
	claudeRecoveryFlapping
	claudeRecoveryAttemptLimit
)

const claudePageUndelivered = 1

// All times are UTC Unix nanoseconds. The incident reference is random, and
// every result and delivery code is from a closed set. No provider values,
// credentials, hashes or raw error text are written here.
type claudeRecoveryPage struct {
	At         int64                 `json:"atUnixNano"`
	Generation uint64                `json:"generation"`
	Failure    claudeRecoveryFailure `json:"failure"`
	Delivery   uint8                 `json:"delivery"`
}

type claudeRecoveryIncident struct {
	ReferenceHigh uint64             `json:"referenceHigh"`
	ReferenceLow  uint64             `json:"referenceLow"`
	Generation    uint64             `json:"generation"` // generation whose attempt is consumed
	OpenedAt      int64              `json:"openedAtUnixNano"`
	Replacement   uint64             `json:"replacement"` // initial saved early replacement, if any
	Published     bool               `json:"publicationConfirmed"`
	Held          bool               `json:"flappingHold"`
	Resolved      bool               `json:"resolved"`
	Page          claudeRecoveryPage `json:"page"`
}

func (i *claudeRecoveryIncident) page(now int64, generation uint64, failure claudeRecoveryFailure) {
	if i.Generation != 0 && !i.Resolved && i.Page.Delivery == 0 && now > 0 {
		i.Page = claudeRecoveryPage{At: now, Generation: generation, Failure: failure, Delivery: claudePageUndelivered}
	}
}

type claudeProbeRecord struct {
	Generation           uint64           `json:"generation"`
	LastHealthy          int64            `json:"lastHealthyAtUnixNano"`
	FirstUsable          int64            `json:"firstUsableAtUnixNano"`
	LastHealthyBefore401 int64            `json:"lastHealthyBefore401UnixNano"`
	First401             int64            `json:"first401AtUnixNano"`
	ObservedAt           int64            `json:"observedAtUnixNano"`
	Class                claudeProbeClass `json:"class"`
}

type claudeRecoveryState struct {
	Version          uint8                  `json:"version"`
	IncarnationHigh  uint64                 `json:"incarnationHigh"`
	IncarnationLow   uint64                 `json:"incarnationLow"`
	LastAttempt      int64                  `json:"lastAttemptAtUnixNano"`
	CooldownUntil    int64                  `json:"cooldownUntilUnixNano"`
	AttemptTimes     []int64                `json:"attemptTimesUnixNano,omitempty"`
	Incident         claudeRecoveryIncident `json:"incident"`
	Probes           []claudeProbeRecord    `json:"probes,omitempty"`
	EarlyTransitions []claudeTransition     `json:"earlyTransitions,omitempty"`
}

func (s claudeRecoveryState) valid() bool {
	if s.Version != 1 || len(s.Probes) > claudeHistoryLimit || len(s.EarlyTransitions) > claudeHistoryLimit ||
		len(s.AttemptTimes) > claudeEarlyMaxDailyLimit ||
		s.LastAttempt < 0 || s.CooldownUntil < s.LastAttempt || (s.LastAttempt == 0) != (s.CooldownUntil == 0) {
		return false
	}
	var lastAttempt int64
	for _, at := range s.AttemptTimes {
		if at <= lastAttempt || at > s.LastAttempt {
			return false
		}
		lastAttempt = at
	}
	i := s.Incident
	if i.Generation == 0 {
		if i != (claudeRecoveryIncident{}) {
			return false
		}
	} else if i.ReferenceHigh|i.ReferenceLow == 0 || i.OpenedAt <= 0 ||
		(i.OpenedAt > s.LastAttempt && i.Page.Failure != claudeRecoveryAttemptLimit) ||
		(i.Replacement != 0 && i.Replacement <= i.Generation) || (i.Published && i.Replacement == 0) || (i.Resolved && i.Held) {
		return false
	}
	p := i.Page
	if p.Delivery == 0 {
		if p != (claudeRecoveryPage{}) || i.Held {
			return false
		}
	} else if p.Delivery != claudePageUndelivered || p.At <= 0 || p.Generation < i.Generation ||
		p.Failure < claudeRecoveryNotSent || p.Failure > claudeRecoveryAttemptLimit {
		return false
	}
	if p.Failure == claudeRecoveryAttemptLimit && (i.Replacement != 0 || p.At < i.OpenedAt || p.Generation != i.Generation || (!i.Held && !i.Resolved)) {
		return false
	}
	var previous uint64
	for _, p := range s.Probes {
		if p.Generation <= previous || p.ObservedAt <= 0 || p.LastHealthy < 0 || p.FirstUsable < 0 || p.First401 < 0 || p.LastHealthyBefore401 < 0 ||
			p.Class < claudeProbeHealthy || p.Class > claudeProbeUnavailable {
			return false
		}
		previous = p.Generation
	}
	previous = 0
	for _, t := range s.EarlyTransitions {
		if t.Generation <= previous || t.Operation != claudeEarlyRefresh || !t.validObservations() || t.AccessExpirySource != claudeResponseExpiry {
			return false
		}
		previous = t.Generation
	}
	return true
}

func decodeClaudeRecovery(raw []byte) (claudeRecoveryState, error) {
	if len(raw) == 0 {
		return claudeRecoveryState{Version: 1}, nil
	}
	var s claudeRecoveryState
	if len(raw) > 4096 || bytes.Contains(raw, []byte("null")) {
		return s, errClaudeRecoveryJournal
	}
	if strict, err := kubejson.UnmarshalStrict(raw, &s); err != nil || len(strict) != 0 || !s.valid() {
		return claudeRecoveryState{}, errClaudeRecoveryJournal
	}
	return s, nil
}

func (s claudeRecoveryState) encode() []byte {
	if !s.valid() {
		return nil
	}
	raw, _ := json.Marshal(s)
	return raw
}

func (s claudeRecoveryState) floor(interval time.Duration) int64 {
	if s.LastAttempt == 0 {
		return 0
	}
	return max(s.CooldownUntil, int64(min(uint64(s.LastAttempt)+uint64(interval), uint64(1<<63-1))))
}

func (s *claudeRecoveryState) addAttempt(at int64, limit int) bool {
	oldest := at - int64(claudeEarlyAttemptWindow)
	start := 0
	for start < len(s.AttemptTimes) && s.AttemptTimes[start] <= oldest {
		start++
	}
	if len(s.AttemptTimes)-start >= limit {
		return false
	}
	// The decision starts from a copy of the observed state. Leave that
	// snapshot intact if the new latch cannot be committed.
	s.AttemptTimes = append(append([]int64(nil), s.AttemptTimes[start:]...), at)
	return true
}

// A new adoption clears a hold, while the inter-attempt floor survives. An
// upgrade can start with a Ready record from before slice 3 and no known
// incarnation. In that case the journal UID and generation still fence the
// probe. An unavailable provenance row never clears an incident.
func (s *claudeRecoveryState) useIncarnation(secret *corev1.Secret, generation uint64) {
	p, _ := decodeClaudeProvenance(secret.Data[claudeProvenanceKey])
	for n := len(p.Transitions) - 1; n >= 0; n-- {
		t := p.Transitions[n]
		if t.Generation > generation || t.IncarnationHigh|t.IncarnationLow == 0 {
			continue
		}
		s.setIncarnation(t.IncarnationHigh, t.IncarnationLow)
		return
	}
}

func (s *claudeRecoveryState) setIncarnation(high, low uint64) {
	if s.IncarnationHigh != high || s.IncarnationLow != low {
		s.IncarnationHigh, s.IncarnationLow = high, low
		s.AttemptTimes = nil
		if s.Incident.Generation != 0 {
			s.Incident.Resolved, s.Incident.Held = true, false
		}
	}
}

func (s *claudeRecoveryState) addTransition(t claudeTransition) {
	t.IncarnationHigh, t.IncarnationLow = s.IncarnationHigh, s.IncarnationLow
	s.Incident.Replacement = t.Generation
	s.EarlyTransitions = append(s.EarlyTransitions, t)
	if len(s.EarlyTransitions) > claudeHistoryLimit {
		s.EarlyTransitions = s.EarlyTransitions[len(s.EarlyTransitions)-claudeHistoryLimit:]
	}
}

func (s *claudeRecoveryState) observe(generation uint64, at int64, class claudeProbeClass, lastHealthy int64) *claudeProbeRecord {
	if n := len(s.Probes); n == 0 || s.Probes[n-1].Generation < generation {
		s.Probes = append(s.Probes, claudeProbeRecord{Generation: generation})
		if len(s.Probes) > claudeHistoryLimit {
			s.Probes = s.Probes[len(s.Probes)-claudeHistoryLimit:]
		}
	}
	p := &s.Probes[len(s.Probes)-1]
	p.ObservedAt, p.Class = at, class
	p.LastHealthy = max(p.LastHealthy, lastHealthy)
	if class == claudeProbeUnauthorized && p.First401 == 0 {
		p.First401, p.LastHealthyBefore401 = at, p.LastHealthy
	}
	return p
}

// These classes carry diagnostics only. Healthy and own-token 401 inform
// recovery decisions, so transitions to and from them are saved immediately.
func (c claudeProbeClass) diagnosticOnly() bool {
	switch c {
	case claudeProbeForbidden, claudeProbeRateLimited, claudeProbeServerError, claudeProbeTimeout,
		claudeProbeMalformed, claudeProbeAccountMismatch, claudeProbeUnavailable:
		return true
	default:
		return false
	}
}

// Coalesce timestamps and diagnostic-only changes between heartbeats. A first
// usable probe or first 401 is evidence even if the class was already recorded.
func (p *claudeProbeRecord) coalesce(previous claudeProbeRecord) {
	if p.Generation == previous.Generation &&
		(p.Class == previous.Class || (p.Class.diagnosticOnly() && previous.Class.diagnosticOnly())) &&
		p.FirstUsable == previous.FirstUsable && p.First401 == previous.First401 &&
		p.ObservedAt-previous.ObservedAt < int64(claudeProbeHeartbeat) {
		*p = previous
	}
}

func (s claudeRecoveryState) lastProbe(generation uint64) claudeProbeRecord {
	if n := len(s.Probes); n > 0 && s.Probes[n-1].Generation == generation {
		return s.Probes[n-1]
	}
	return claudeProbeRecord{}
}
