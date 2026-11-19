package keeper

import (
	"context"
	"errors"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/thaynes43/dev-env/internal/claudeauth"
)

const (
	DefaultClaudeProbeInterval = time.Minute
	DefaultClaudeEarlyInterval = 5 * time.Minute
)

// ClaudeEarlyRecoveryOptions is independent of the scheduled refresh policy.
type ClaudeEarlyRecoveryOptions struct {
	Enabled       bool
	ProbeInterval time.Duration
	MinInterval   time.Duration
}

func (o ClaudeOptions) ValidateEarlyRecovery() error {
	if o.EarlyRecovery.Enabled && (!o.Enabled || o.EarlyRecovery.ProbeInterval <= 0 || o.EarlyRecovery.MinInterval <= 0) {
		return errors.New("claude early recovery requires Claude auth and positive probe and minimum intervals")
	}
	return nil
}

// Probe times are anchored to the keeper's start. A long probe or tick skips
// missed slots instead of issuing a burst of catch-up requests.
func (w *claudeWorker) runClaudeProbes(ctx context.Context) {
	interval := w.EarlyRecovery.ProbeInterval
	start := w.Clock.Now()
	for ctx.Err() == nil {
		now := w.Clock.Now()
		wait := interval - now.Sub(start)%interval
		select {
		case <-ctx.Done():
			return
		case <-w.Clock.After(wait):
			w.probeClaude(ctx)
		}
	}
}

// The only input to this probe is the elected keeper's private Ready record.
// The GET spends nothing. Run it outside the write mutex so a slow response
// cannot delay scheduled refresh or the control socket.
func (w *claudeWorker) probeClaude(ctx context.Context) {
	if !w.EarlyRecovery.Enabled {
		return
	}
	secret, snapshot, err := w.load(ctx)
	if err != nil || snapshot.Stage != claudeReady || snapshot.Access.Validate(w.Clock.Now()) != nil ||
		w.Fence(ctx, claudeRequestTime+claudeSafetyMargin) != nil {
		return
	}
	state, ok := loadClaudeRecovery(secret, snapshot.Access.Generation)
	if !ok {
		w.Log.Info("claude early recovery journal is unavailable")
		return
	}
	profile, err := w.Transport.profile(ctx, newSecretValue(snapshot.Access.AccessToken))
	class := classifyClaudeProbe(snapshot.Access, profile, err)
	at := claudeObservedTime(w.Clock.Now())

	w.mu.Lock()
	defer w.mu.Unlock()
	if at == 0 || w.Fence(ctx, 2*claudeSaveTimeout+claudeSafetyMargin) != nil {
		return
	}
	current, ready, err := w.load(ctx)
	if err != nil || current.UID != secret.UID || ready.Stage != claudeReady || !sameClaudeAccess(ready.Access, snapshot.Access) ||
		ready.Access.Validate(w.Clock.Now()) != nil {
		return
	}
	gen := ready.Access.Generation
	recovery, ok := loadClaudeRecovery(current, gen)
	if !ok || recovery.IncarnationHigh != state.IncarnationHigh || recovery.IncarnationLow != state.IncarnationLow {
		return
	}
	observation := recovery.observe(gen, at, class)
	attempt := recovery
	if w.considerEarlyRefresh(&attempt, observation) {
		saved, replacement, err := w.dispatchClaudeRefresh(ctx, current, ready, &attempt)
		if err == nil {
			w.publishClaudeReady(context.WithoutCancel(ctx), saved, replacement)
		} else if w.Fence(ctx, claudeSaveTimeout+claudeSafetyMargin) == nil {
			// Retain the observed 401 if preflight failed before changing the
			// journal. A committed intent or any other writer wins this CAS.
			w.recordClaudeRecovery(ctx, current, recovery)
		}
		return
	}
	w.recordClaudeRecovery(ctx, current, attempt)
}

func classifyClaudeProbe(access claudeauth.Access, profile claudeProfile, err error) claudeProbeClass {
	if err != nil {
		var class claudeProbeClass
		if errors.As(err, &class) {
			return class
		}
		return claudeProbeUnavailable
	}
	if profile.AccountUUID != access.AccountUUID || profile.OrganizationUUID != access.OrganizationUUID {
		return claudeProbeAccountMismatch
	}
	return claudeProbeHealthy
}

// considerEarlyRefresh consumes only journal state and a fresh own-token
// observation. The caller persists the latch and intent in a single CAS.
func (w *claudeWorker) considerEarlyRefresh(state *claudeRecoveryState, p *claudeProbeRecord) bool {
	i := &state.Incident
	floor := state.floor(w.EarlyRecovery.MinInterval)
	if i.Generation != 0 && !i.Resolved {
		if p.Generation <= i.Generation || i.Held {
			return false
		}
		switch p.Class {
		case claudeProbeUnauthorized:
			// Missing a successful probe through the floor is insufficient
			// to distinguish a late observation from an early cutoff.
			i.Held = true
			i.page(p.ObservedAt, p.Generation, claudeRecoveryFlapping)
		case claudeProbeHealthy:
			if w.recoveryPublished(*i, p.Generation) {
				if p.FirstUsable == 0 {
					p.FirstUsable = p.ObservedAt
				}
				if p.ObservedAt >= floor {
					if p.FirstUsable > floor {
						i.page(p.ObservedAt, p.Generation, claudeRecoveryDeadline)
					}
					i.Resolved = true
				}
			}
		}
		return false
	}
	if p.Class != claudeProbeUnauthorized || p.Generation == i.Generation || p.ObservedAt < floor ||
		w.Clock.Now().Before(w.retryAfter) {
		return false
	}
	now := w.Clock.Now()
	at := claudeObservedTime(now)
	until := claudeObservedTime(now.Add(w.EarlyRecovery.MinInterval))
	if at < p.ObservedAt || until <= at {
		return false
	}
	high, low := newClaudeIncarnation()
	*i = claudeRecoveryIncident{Generation: p.Generation, OpenedAt: p.First401, ReferenceHigh: high, ReferenceLow: low}
	state.LastAttempt, state.CooldownUntil = at, until
	return true
}

func loadClaudeRecovery(secret *corev1.Secret, generation uint64) (claudeRecoveryState, bool) {
	s, err := decodeClaudeRecovery(secret.Data[claudeRecoveryKey])
	if err != nil || s.Incident.Generation > generation || s.Incident.Replacement > generation ||
		(len(s.Probes) != 0 && s.Probes[len(s.Probes)-1].Generation > generation) ||
		(len(s.EarlyTransitions) != 0 && s.EarlyTransitions[len(s.EarlyTransitions)-1].Generation > generation) {
		return claudeRecoveryState{}, false
	}
	s.useIncarnation(secret, generation)
	return s, true
}

func (w *claudeWorker) recoveryPublished(i claudeRecoveryIncident, generation uint64) bool {
	return w.published == generation || (i.Replacement == generation && i.Published)
}

func (w *claudeWorker) earlyPublicationPending(secret *corev1.Secret, generation uint64) bool {
	if !w.EarlyRecovery.Enabled {
		return false
	}
	s, ok := loadClaudeRecovery(secret, generation)
	return ok && s.Incident.Replacement == generation && !s.Incident.Published
}

func (w *claudeWorker) confirmEarlyPublication(ctx context.Context, secret *corev1.Secret, generation uint64, written bool) {
	state, ok := loadClaudeRecovery(secret, generation)
	if !ok || state.Incident.Replacement != generation || state.Incident.Published {
		return
	}
	if written && len(state.EarlyTransitions) != 0 {
		last := &state.EarlyTransitions[len(state.EarlyTransitions)-1]
		if last.Generation == generation {
			last.PublishedAt = claudeObservedTime(w.Clock.Now())
		}
	}
	state.Incident.Published = true
	if w.Fence(ctx, claudeSaveTimeout+claudeSafetyMargin) == nil {
		w.recordClaudeRecovery(ctx, secret, state)
	}
}

// A saved early replacement is published before another refresh can happen.
// If its own access expires before publication can succeed, routine rotation
// can use the saved refresh credential; the previous attempt is never replayed.
func (w *claudeWorker) reconcileClaudeRecovery(ctx context.Context, secret *corev1.Secret, ready claudeRecord) (*corev1.Secret, bool) {
	if !w.EarlyRecovery.Enabled {
		return secret, false
	}
	gen := ready.Access.Generation
	state, ok := loadClaudeRecovery(secret, gen)
	if !ok {
		return secret, false
	}
	i := &state.Incident
	pending := i.Replacement == gen && !i.Published && w.Clock.Now().Before(ready.Access.ExpiresAt)
	now := claudeObservedTime(w.Clock.Now())
	if i.Generation != 0 && !i.Resolved && i.Page.Delivery == 0 && now > state.floor(w.EarlyRecovery.MinInterval) &&
		state.firstUsable(gen) == 0 &&
		w.Fence(ctx, claudeSaveTimeout+claudeSafetyMargin) == nil {
		i.page(now, gen, claudeRecoveryDeadline)
		secret = w.recordClaudeRecovery(ctx, secret, state)
	}
	return secret, pending
}

func (w *claudeWorker) recordClaudeRecovery(ctx context.Context, secret *corev1.Secret, state claudeRecoveryState) *corev1.Secret {
	if raw := state.encode(); raw != nil {
		if saved, err := w.Journal.patch(ctx, secret, map[string]any{claudeRecoveryKey: raw}); err == nil {
			return saved
		}
	}
	w.Log.Info("claude early recovery observation was not confirmed in the journal")
	return secret
}

// Saving a failure and returning the attempt to Ready or NeedsLogin are one
// CAS. Crash recovery can use the same code with the marker's proof or the
// conservative possibly-spent class.
func (w *claudeWorker) claudeRecoveryFailureData(secret *corev1.Secret, generation uint64, failure claudeRecoveryFailure) []byte {
	if !w.EarlyRecovery.Enabled {
		return nil
	}
	state, ok := loadClaudeRecovery(secret, generation)
	if !ok || state.Incident.Generation != generation || state.Incident.Replacement != 0 || state.Incident.Page.Delivery != 0 {
		return nil
	}
	state.Incident.page(claudeObservedTime(w.Clock.Now()), generation, failure)
	return state.encode()
}

func claudeRefreshFailure(err error) claudeRecoveryFailure {
	switch {
	case errors.Is(err, errClaudeStatus):
		return claudeRecoveryRefreshStatus
	case errors.Is(err, errClaudeRead):
		return claudeRecoveryRead
	case errors.Is(err, errClaudeParse):
		return claudeRecoveryInvalid
	default:
		return claudeRecoveryPossiblySpent
	}
}
