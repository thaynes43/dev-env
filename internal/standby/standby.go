// Package standby keeps one remote Claude session ready (Story110 T25, plan 03).
// It is v1's post-ready standby for v2: after an operator start, and whenever the
// last standby is gone, the leader creates one `remote` AgentSession so Tom can
// open it from his phone without a coordinator already running.
//
// The standby is an ordinary AgentSession, made through the API's own creation
// path. The runnable finds it again by LabelStandby and never reads, changes or
// deletes any other session. A circuit breaker, kept in a ConfigMap so it
// survives an operator restart, stops a crash loop from minting sessions.
package standby

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

const (
	// DefaultStateName is the breaker's ConfigMap.
	DefaultStateName = "dev-env-standby"
	// stateKey is the ConfigMap key holding the creation times.
	stateKey = "creations"
	// DefaultMaxCreations and DefaultWindow are v1 post-ready's breaker: three
	// creations in 30 minutes, and a fourth is refused until one ages out.
	DefaultMaxCreations = 3
	DefaultWindow       = 30 * time.Minute
)

// Runnable ensures exactly one standby session exists, in the leader only.
type Runnable struct {
	// Reader lists sessions and reads the breaker's state from the API server;
	// Client writes the state. Neither is the cache.
	Reader client.Reader
	Client client.Client
	// SessionNamespace holds the AgentSessions; StateNamespace and StateName the
	// breaker's ConfigMap (StateName empty: DefaultStateName).
	SessionNamespace string
	StateNamespace   string
	StateName        string
	// Request is the standby session to create.
	Request apiv1.CreateSessionRequest
	// Create makes the session through the API's creation path and returns its
	// name (apiserver.Server.CreateStandby).
	Create func(ctx context.Context, req apiv1.CreateSessionRequest) (string, error)
	// Every is how often it looks; zero means a minute.
	Every time.Duration
	// MaxCreations in Window trip the breaker; zero values take the defaults.
	MaxCreations int
	Window       time.Duration
	Log          logr.Logger
	// Now is the clock; nil is time.Now.
	Now func() time.Time

	tripped bool
}

// NeedLeaderElection makes the standby run in the leader only.
func (r *Runnable) NeedLeaderElection() bool { return true }

// Start runs until ctx ends. It implements controller-runtime's Runnable.
func (r *Runnable) Start(ctx context.Context) error {
	every := r.Every
	if every <= 0 {
		every = time.Minute
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if name, err := r.Ensure(ctx); err != nil {
			r.Log.Error(err, "ensure the standby session")
		} else if name != "" {
			r.Log.Info("created the standby session", "session", name)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// Alive reports whether a standby session still counts as the standby. A
// suspended one counts: it resumes as the same phone entry with its history, so
// replacing it would only leave two. A session being deleted (reaped), Archived
// or Failed, or whose agent has ended, does not.
func Alive(s *v1alpha1.AgentSession) bool {
	if !s.DeletionTimestamp.IsZero() {
		return false
	}
	switch s.Status.Phase {
	case v1alpha1.PhaseArchived, v1alpha1.PhaseFailed:
		return false
	}
	// Before the agent's status: a suspended session's agent has exited, and
	// that is how it suspends, not an end.
	if s.Spec.OperatingMode == v1alpha1.OperatingModeSuspended || s.Status.Phase == v1alpha1.PhaseSuspended {
		return true
	}
	if a := s.Status.Agent; a != nil {
		switch a.Status {
		case protocol.AgentExited, protocol.AgentFailed, protocol.AgentInterrupted:
			return false
		}
	}
	return true
}

// Ensure runs one pass. It returns the new session's name when it created one,
// and "" when it did not (a standby exists, the breaker is open, or an error).
func (r *Runnable) Ensure(ctx context.Context) (string, error) {
	var list v1alpha1.AgentSessionList
	if err := r.Reader.List(ctx, &list, client.InNamespace(r.SessionNamespace),
		client.MatchingLabels{v1alpha1.LabelStandby: "true"}); err != nil {
		return "", fmt.Errorf("list standby sessions: %w", err)
	}
	for i := range list.Items {
		if Alive(&list.Items[i]) {
			r.tripped = false
			return "", nil
		}
	}

	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	window, limit := r.Window, r.MaxCreations
	if window <= 0 {
		window = DefaultWindow
	}
	if limit <= 0 {
		limit = DefaultMaxCreations
	}
	key := types.NamespacedName{Namespace: r.StateNamespace, Name: r.stateName()}
	var cm corev1.ConfigMap
	exists := true
	if err := r.Reader.Get(ctx, key, &cm); apierrors.IsNotFound(err) {
		exists = false
	} else if err != nil {
		return "", fmt.Errorf("read the breaker state: %w", err)
	}
	recent, err := recentCreations(cm.Data[stateKey], now, window)
	if err != nil {
		// Fail closed: an unreadable record is not an empty one.
		return "", fmt.Errorf("the breaker state in %s is unreadable; no standby is created until it is fixed: %w", key, err)
	}
	if len(recent) >= limit {
		if !r.tripped {
			r.Log.Info("loop suspected: standby circuit breaker open, no standby session until the window clears",
				"creations", len(recent), "window", window.String())
		}
		r.tripped = true
		return "", nil
	}
	r.tripped = false

	// Record the creation before making it. A crash between the two then costs
	// one slot, never an unbounded loop. The Update carries the read
	// resourceVersion, so two writers cannot both take the last slot.
	recent = append(recent, now.UTC())
	raw, _ := json.Marshal(recent)
	if exists {
		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		cm.Data[stateKey] = string(raw)
		if err := r.Client.Update(ctx, &cm); err != nil {
			return "", fmt.Errorf("record the creation: %w", err)
		}
	} else {
		cm = corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name},
			Data:       map[string]string{stateKey: string(raw)},
		}
		if err := r.Client.Create(ctx, &cm); err != nil {
			return "", fmt.Errorf("record the creation: %w", err)
		}
	}
	name, err := r.Create(ctx, r.Request)
	if err != nil {
		return "", fmt.Errorf("create the standby session: %w", err)
	}
	return name, nil
}

func (r *Runnable) stateName() string {
	if r.StateName != "" {
		return r.StateName
	}
	return DefaultStateName
}

// recentCreations parses the stored times and keeps those inside the window.
// Unreadable state is an error. A time in the future is kept, the cautious side.
func recentCreations(raw string, now time.Time, window time.Duration) ([]time.Time, error) {
	var all []time.Time
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &all); err != nil {
			return nil, err
		}
	}
	var out []time.Time
	for _, t := range all {
		if now.Sub(t) < window {
			out = append(out, t)
		}
	}
	return out, nil
}
