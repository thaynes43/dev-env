package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// Session allowance readings (DESIGN-059 step 5): the newest passive reading
// per provider, reported on running sessions' heartbeats. They live in their
// own ConfigMap, never in the active collector's slot, whose fences compare
// that slot's resource version.
const (
	// SessionAllowanceNamespace and SessionAllowanceName name the one
	// ConfigMap. GitOps precreates it with metadata only; the operator never
	// creates or deletes it.
	SessionAllowanceNamespace = "dev-env-system"
	SessionAllowanceName      = "dev-env-session-allowances"
	// SessionAllowanceFieldManager owns the reading entries.
	SessionAllowanceFieldManager = "dev-env-session-allowances"
	// SessionAllowanceSchema names each entry's document.
	SessionAllowanceSchema = "dev-env.session-allowances.v1"
)

const (
	// sessionAllowanceWriteEvery is the least time between two writes for one
	// provider, unless a window's reset changed.
	sessionAllowanceWriteEvery = 5 * time.Minute
	// sessionAllowanceEvery is the least time between two readings taken from
	// one session: one a minute, less five seconds so that a 60-second
	// heartbeat that arrives a little early is not refused.
	sessionAllowanceEvery = time.Minute - 5*time.Second
	// sessionAllowanceResetMoved is how far a reset must move to count as
	// changed. A CLI that derives the reset from "seconds until reset" moves
	// it by a second or two between records; that is not a new window.
	sessionAllowanceResetMoved = 2 * time.Minute
	// sessionAllowanceWriteFloor is the least time between two writes for one
	// provider, even when a reset moved.
	sessionAllowanceWriteFloor = time.Minute
	// sessionAllowanceKeep is how many readings each provider keeps.
	sessionAllowanceKeep = 2
	// sessionAllowanceSkew is how far ahead of the operator's clock a reading
	// may be, for clock skew between nodes. Such a reading is stored at the
	// operator's time, so it never looks newer than it is.
	sessionAllowanceSkew = time.Minute
	// maxSessionAllowanceEntry bounds one provider's entry.
	maxSessionAllowanceEntry = 8 << 10
	// maxSessionAllowanceSessions bounds the rate limiter's memory.
	maxSessionAllowanceSessions = 512
)

// Why a reading was not stored. Each is a fixed code, safe to log: nothing
// from the reading itself is logged.
var (
	errSessionAllowanceRate     = errors.New("session_allowance_rate_limited")
	errSessionAllowanceInvalid  = errors.New("session_allowance_invalid")
	errSessionAllowanceHeld     = errors.New("session_allowance_not_newer")
	errSessionAllowanceAbsent   = errors.New("session_allowance_configmap_absent")
	errSessionAllowanceConflict = errors.New("session_allowance_conflict")
	errSessionAllowanceStore    = errors.New("session_allowance_store_unavailable")
)

// sessionAllowanceProviders are the agents whose readings are kept, in order.
var sessionAllowanceProviders = []string{protocol.AgentClaude, protocol.AgentCodex}

// sessionAllowanceKey is a provider's entry in the ConfigMap.
func sessionAllowanceKey(agent string) string { return agent + ".json" }

// StoredSessionReading is one stored reading: the session's numbers, which
// session reported them, and when the operator took them.
type StoredSessionReading struct {
	Session    string                     `json:"session"`
	Source     string                     `json:"source"`
	ReadAt     time.Time                  `json:"readAt"`
	ReportedAt time.Time                  `json:"reportedAt"`
	Windows    []protocol.AllowanceWindow `json:"windows"`
}

// sessionAllowanceEntry is one provider's entry: newest reading first.
type sessionAllowanceEntry struct {
	Schema   string                 `json:"schema"`
	Provider string                 `json:"provider"`
	Readings []StoredSessionReading `json:"readings"`
}

// reading is the stored value as a wire reading, for the shared bounds.
func (r StoredSessionReading) reading(agent string) protocol.AllowanceReading {
	return protocol.AllowanceReading{Provider: agent, Source: r.Source, ReadAt: r.ReadAt, Windows: r.Windows}
}

// valid checks a stored reading again: the ConfigMap may have been edited.
func (r StoredSessionReading) valid(agent string) bool {
	return r.Session != "" && len(r.Session) <= 253 && !r.ReportedAt.IsZero() && !r.ReadAt.After(r.ReportedAt) && r.reading(agent).Valid()
}

// SessionAllowanceStore validates heartbeat readings and keeps the newest per
// provider in SessionAllowanceName. Writes are an Update with the read
// resourceVersion, so two operator replicas never overwrite each other.
type SessionAllowanceStore struct {
	Writer objectUpdater
	Live   objectGetter

	mu sync.Mutex
	// seen is each session's last reading taken, for the per-session limit.
	seen map[string]time.Time
	// written is each provider's last write by this replica, so most
	// heartbeats need no read of the ConfigMap.
	written map[string]StoredSessionReading
}

func (s *SessionAllowanceStore) ready() bool { return s != nil && s.Writer != nil && s.Live != nil }

// Record checks one heartbeat reading against the session and its pod
// (DESIGN-059 4.4) and stores it when it is newer and the write limit allows.
// The caller has already proved the pod belongs to the session. It returns a
// fixed code when it stores nothing.
func (s *SessionAllowanceStore) Record(ctx context.Context, sess *v1alpha1.AgentSession, pod *corev1.Pod, r protocol.AllowanceReading, now time.Time) error {
	if !s.ready() || sess == nil || now.IsZero() {
		return errSessionAllowanceStore
	}
	if !s.take(sess.Name, now) {
		return errSessionAllowanceRate
	}
	stored, err := checkSessionReading(sess, pod, r, now)
	if err != nil {
		return err
	}
	agent := r.Provider
	if !s.due(agent, stored) {
		return errSessionAllowanceHeld
	}
	return s.write(ctx, agent, stored, now)
}

// take applies the per-session limit: at most one reading a minute. Every
// reading counts, accepted or not, so a session cannot make the operator do
// more work by sending bad ones.
func (s *SessionAllowanceStore) take(session string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seen == nil {
		s.seen = map[string]time.Time{}
	}
	// A clock that went back counts like one that stood still.
	if last, ok := s.seen[session]; ok && absDuration(now.Sub(last)) < sessionAllowanceEvery {
		return false
	}
	if len(s.seen) >= maxSessionAllowanceSessions {
		for name, at := range s.seen {
			if absDuration(now.Sub(at)) >= sessionAllowanceEvery {
				delete(s.seen, name)
			}
		}
		if len(s.seen) >= maxSessionAllowanceSessions {
			return false
		}
	}
	s.seen[session] = now
	return true
}

// checkSessionReading is the operator-side bound check: the reading's own
// shape and bounds, the provider equal to the session's agent, a Claude
// reading only from an interactive session, and a reading time that is not
// in the future and not before the pod started.
func checkSessionReading(sess *v1alpha1.AgentSession, pod *corev1.Pod, r protocol.AllowanceReading, now time.Time) (StoredSessionReading, error) {
	if pod == nil || !r.Valid() || r.Provider != string(sess.Spec.Agent) {
		return StoredSessionReading{}, errSessionAllowanceInvalid
	}
	if r.Provider == protocol.AgentClaude && sess.Spec.Mode != v1alpha1.ModeRemote && sess.Spec.Mode != v1alpha1.ModeLocal {
		return StoredSessionReading{}, errSessionAllowanceInvalid
	}
	started := pod.CreationTimestamp.Time
	if pod.Status.StartTime != nil && !pod.Status.StartTime.IsZero() {
		started = pod.Status.StartTime.Time
	}
	if started.IsZero() || r.ReadAt.Before(started) || r.ReadAt.After(now.Add(sessionAllowanceSkew)) {
		return StoredSessionReading{}, errSessionAllowanceInvalid
	}
	readAt := r.ReadAt.UTC()
	if readAt.After(now) {
		readAt = now.UTC()
	}
	windows := make([]protocol.AllowanceWindow, 0, len(r.Windows))
	for _, w := range r.Windows {
		windows = append(windows, protocol.AllowanceWindow{Minutes: w.Minutes, UsedPercent: w.UsedPercent, ResetsAt: w.ResetsAt.UTC()})
	}
	stored := StoredSessionReading{Session: sess.Name, Source: r.Source, ReadAt: readAt, ReportedAt: now.UTC(), Windows: windows}
	if !stored.valid(r.Provider) {
		return StoredSessionReading{}, errSessionAllowanceInvalid
	}
	return stored, nil
}

// due is the write limit: a reading newer than the last one written, and
// either five minutes after that write or with a changed reset.
func (s *SessionAllowanceStore) due(agent string, next StoredSessionReading) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	last, ok := s.written[agent]
	return !ok || writeDue(last, next)
}

func writeDue(last, next StoredSessionReading) bool {
	if !next.ReadAt.After(last.ReadAt) {
		return false
	}
	// A clock behind the last write (another replica's, or one that went back)
	// waits rather than skipping the limit.
	since := next.ReportedAt.Sub(last.ReportedAt)
	return since >= sessionAllowanceWriteEvery || (since >= sessionAllowanceWriteFloor && resetsChanged(last.Windows, next.Windows))
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// resetsChanged reports whether a window's reset moved by more than
// sessionAllowanceResetMoved, or a window came or went.
func resetsChanged(a, b []protocol.AllowanceWindow) bool {
	if len(a) != len(b) {
		return true
	}
	for _, w := range a {
		found := false
		for _, v := range b {
			if v.Minutes == w.Minutes && absDuration(v.ResetsAt.Sub(w.ResetsAt)) <= sessionAllowanceResetMoved {
				found = true
				break
			}
		}
		if !found {
			return true
		}
	}
	return false
}

func (s *SessionAllowanceStore) remember(agent string, r StoredSessionReading) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.written == nil {
		s.written = map[string]StoredSessionReading{}
	}
	if last, ok := s.written[agent]; !ok || r.ReadAt.After(last.ReadAt) || (r.ReadAt.Equal(last.ReadAt) && r.ReportedAt.After(last.ReportedAt)) {
		s.written[agent] = r
	}
}

// write reads the live ConfigMap and updates the provider's entry when the
// stored newest reading still allows it.
func (s *SessionAllowanceStore) write(ctx context.Context, agent string, next StoredSessionReading, now time.Time) error {
	var cm corev1.ConfigMap
	if err := s.Live.Get(ctx, client.ObjectKey{Namespace: SessionAllowanceNamespace, Name: SessionAllowanceName}, &cm); err != nil {
		if apierrors.IsNotFound(err) {
			return errSessionAllowanceAbsent
		}
		return errSessionAllowanceStore
	}
	if !cm.DeletionTimestamp.IsZero() || (cm.Immutable != nil && *cm.Immutable) || cm.ResourceVersion == "" {
		return errSessionAllowanceStore
	}
	key := sessionAllowanceKey(agent)
	var prior []StoredSessionReading
	if entry, ok := decodeSessionAllowanceEntry(agent, cm.Data[key]); ok {
		prior = entry.Readings
	}
	if len(prior) > 0 {
		s.remember(agent, prior[0])
		if !writeDue(prior[0], next) {
			return errSessionAllowanceHeld
		}
	}
	entry := sessionAllowanceEntry{Schema: SessionAllowanceSchema, Provider: agent, Readings: append([]StoredSessionReading{next}, prior...)}
	if len(entry.Readings) > sessionAllowanceKeep {
		entry.Readings = entry.Readings[:sessionAllowanceKeep]
	}
	raw, err := json.Marshal(entry)
	if err != nil || len(raw) > maxSessionAllowanceEntry {
		return errSessionAllowanceInvalid
	}
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data[key] = string(raw)
	if err := s.Writer.Update(ctx, &cm, client.FieldOwner(SessionAllowanceFieldManager)); err != nil {
		if apierrors.IsConflict(err) {
			return errSessionAllowanceConflict
		}
		return errSessionAllowanceStore
	}
	s.remember(agent, next)
	return nil
}

// decodeSessionAllowanceEntry reads one provider's entry, keeping only
// readings that still pass every bound, newest first. Anything else is no
// entry.
func decodeSessionAllowanceEntry(agent, raw string) (sessionAllowanceEntry, bool) {
	if raw == "" || len(raw) > maxSessionAllowanceEntry {
		return sessionAllowanceEntry{}, false
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.DisallowUnknownFields()
	var entry sessionAllowanceEntry
	if dec.Decode(&entry) != nil || dec.More() || entry.Schema != SessionAllowanceSchema || entry.Provider != agent {
		return sessionAllowanceEntry{}, false
	}
	var kept []StoredSessionReading
	for _, r := range entry.Readings {
		if len(kept) == sessionAllowanceKeep {
			break
		}
		if !r.valid(agent) || (len(kept) > 0 && !r.ReadAt.Before(kept[len(kept)-1].ReadAt)) {
			continue
		}
		kept = append(kept, r)
	}
	if len(kept) == 0 {
		return sessionAllowanceEntry{}, false
	}
	entry.Readings = kept
	return entry, true
}

// Newest returns each provider's newest stored reading that passes every
// bound. A missing ConfigMap is no readings. The read is uncached.
func (s *SessionAllowanceStore) Newest(ctx context.Context) (map[string]StoredSessionReading, error) {
	if !s.ready() {
		return nil, errSessionAllowanceStore
	}
	var cm corev1.ConfigMap
	if err := s.Live.Get(ctx, client.ObjectKey{Namespace: SessionAllowanceNamespace, Name: SessionAllowanceName}, &cm); err != nil {
		if apierrors.IsNotFound(err) {
			return map[string]StoredSessionReading{}, nil
		}
		return nil, errSessionAllowanceStore
	}
	out := map[string]StoredSessionReading{}
	for _, agent := range sessionAllowanceProviders {
		if entry, ok := decodeSessionAllowanceEntry(agent, cm.Data[sessionAllowanceKey(agent)]); ok {
			out[agent] = entry.Readings[0]
		}
	}
	return out, nil
}
