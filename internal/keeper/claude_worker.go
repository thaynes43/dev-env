package keeper

import (
	"context"
	"errors"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/thaynes43/dev-env/internal/claudeauth"
)

// ClaudeOptions is disabled by default (--enable-claude-auth). Enabling needs
// the named-Secret RBAC for both Secrets and a leader-elected keeper.
type ClaudeOptions struct {
	Enabled       bool
	JournalSecret string
	LiveSecret    string
}

const claudePollInterval = 30 * time.Second

var (
	errClaudeLeadership = errors.New("claude leadership budget is unavailable")
	errClaudeAccount    = errors.New("claude refresh changed the account")
)

type claudePublisher interface {
	publish(ctx context.Context, a claudeauth.Access, refreshExpiresAt, now time.Time) error
	generation(ctx context.Context) (uint64, error)
}

// claudeWorker keeps the shared Claude login fresh. It is a leader-only
// runnable and never a Keeper.Job: a refresh spends the old refresh token, so
// it is journaled, fenced and never replayed.
//
// One cycle: fence -> journal RefreshIntent (CAS) -> fence -> one POST ->
// journal the replacement (CAS) -> fence -> publish the access-only projection.
// The journal is the only memory that matters: a Ready record is safe to act
// on, and a RefreshIntent found at the start of a tick means a POST may have
// spent the refresh token without its replacement being saved, so the login is
// marked NeedsLogin. A fresh login (increment 5) is the only way back.
type claudeWorker struct {
	Journal   *claudeJournal
	Transport claudeRefreshTransport
	Publisher claudePublisher
	Fence     func(context.Context, time.Duration) error
	Clock     clock.Clock
	Log       logr.Logger

	published uint64 // last generation this process published
	last      string // last state logged
}

func (*claudeWorker) NeedLeaderElection() bool { return true }

func (w *claudeWorker) Start(ctx context.Context) error {
	for ctx.Err() == nil {
		w.tick(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-w.Clock.After(claudePollInterval):
		}
	}
	return nil
}

// note logs a state change once. States are fixed words, never values.
func (w *claudeWorker) note(state string) {
	if state != w.last {
		w.Log.Info("claude auth state", "state", state)
		w.last = state
	}
}

func (w *claudeWorker) load(ctx context.Context) (*corev1.Secret, claudeRecord, error) {
	c, cancel := context.WithTimeout(ctx, claudeSaveTimeout)
	defer cancel()
	return w.Journal.load(c)
}

// due: refresh once per access-token life, with the last third of the life as
// the lead, and never sooner than a minute after the last refresh.
func (w *claudeWorker) due(r claudeRecord) bool {
	life := r.Access.ExpiresAt.Sub(r.LastRefresh)
	at := r.Access.ExpiresAt.Add(-life / 3)
	if floor := r.LastRefresh.Add(time.Minute); at.Before(floor) {
		at = floor
	}
	return !w.Clock.Now().Before(at)
}

// tick runs at most one refresh. Its result is only logged as a state.
func (w *claudeWorker) tick(ctx context.Context) {
	_, r, err := w.load(ctx)
	if err != nil {
		w.note("journal-unavailable")
		return
	}
	switch r.Stage {
	case claudeIntent:
		w.needsLogin(ctx)
		return
	case claudeNeedsLogin:
		w.note("needs-login")
		return
	}
	if w.due(r) {
		if r, err = w.refresh(ctx); err != nil {
			return
		}
	}
	if w.published == r.Access.Generation {
		w.note("ready")
		return
	}
	if w.Fence(ctx, claudeSaveTimeout+claudeSafetyMargin) != nil {
		w.note("leadership-unavailable")
		return
	}
	if w.Publisher.publish(ctx, r.Access, r.RefreshExpiresAt, w.Clock.Now()) != nil {
		w.note("publish-failed")
		return
	}
	w.published = r.Access.Generation
	w.note("ready")
}

// refresh performs one journaled refresh and returns the Ready record that
// follows it. Any error leaves the journal either Ready (nothing was sent) or
// RefreshIntent/NeedsLogin (a send may have happened); it never retries.
func (w *claudeWorker) refresh(ctx context.Context) (claudeRecord, error) {
	secret, prev, err := w.load(ctx)
	if err != nil || prev.Stage != claudeReady {
		w.note("journal-unavailable")
		return claudeRecord{}, errClaudeJournal
	}
	floor, err := w.Publisher.generation(ctx)
	if err != nil {
		w.note("publish-failed")
		return claudeRecord{}, err
	}
	gen, err := nextClaudeGeneration(prev.Access.Generation, floor)
	if err != nil {
		w.note("needs-login")
		return claudeRecord{}, err
	}
	// Leadership with room for the POST and the save, before anything is written.
	if w.Fence(ctx, claudeRequestBudget) != nil {
		w.note("leadership-unavailable")
		return claudeRecord{}, errClaudeLeadership
	}
	intent := prev
	intent.Stage, intent.AttemptUID = claudeIntent, string(uuid.NewUUID())
	if w.Journal.save(ctx, secret, intent) != nil {
		// A lost acknowledgement may still have stored the intent.
		w.abandon(ctx, prev, intent.AttemptUID)
		return claudeRecord{}, errClaudeJournal
	}
	// Re-read the stored intent and the Lease; POST only if both still hold.
	secret, stored, err := w.load(ctx)
	if err != nil || stored.Stage != claudeIntent || stored.AttemptUID != intent.AttemptUID {
		// Nothing has been sent yet: return our own intent to Ready if it is
		// still what the journal holds.
		w.abandon(ctx, prev, intent.AttemptUID)
		return claudeRecord{}, errClaudeJournal
	}
	if w.Fence(ctx, claudeRequestBudget) != nil {
		w.abandon(ctx, prev, intent.AttemptUID)
		w.note("leadership-unavailable")
		return claudeRecord{}, errClaudeLeadership
	}
	resp, err := w.Transport.refresh(ctx, prev.Refresh, prev.Access.Scopes)
	if err != nil || ctx.Err() != nil {
		w.needsLogin(ctx)
		return claudeRecord{}, errClaudeRefresh
	}
	if resp.AccountUUID != "" && resp.AccountUUID != prev.Access.AccountUUID {
		w.needsLogin(ctx)
		return claudeRecord{}, errClaudeAccount
	}
	next, err := applyClaudeRefresh(prev, resp, gen, w.Clock.Now())
	if err != nil || next.Access.Validate(w.Clock.Now()) != nil {
		w.needsLogin(ctx)
		return claudeRecord{}, errClaudeRefresh
	}
	// The replacement is stored BEFORE anything is published. If this fails the
	// journal still says RefreshIntent (or already holds the replacement); the
	// next tick marks the first NeedsLogin and never repeats the POST.
	if w.Fence(ctx, claudeSaveTimeout+claudeSafetyMargin) != nil {
		w.note("leadership-unavailable")
		return claudeRecord{}, errClaudeLeadership
	}
	if w.Journal.save(ctx, secret, next) != nil {
		w.note("journal-unavailable")
		return claudeRecord{}, errClaudeJournal
	}
	w.note("refreshed")
	return next, nil
}

// abandon returns an intent that was never sent to Ready, if this attempt's
// intent is still what the journal holds and leadership can still write. When
// it cannot, the intent stays and the next tick marks NeedsLogin.
func (w *claudeWorker) abandon(ctx context.Context, ready claudeRecord, uid string) {
	secret, r, err := w.load(ctx)
	if err != nil || r.Stage != claudeIntent || r.AttemptUID != uid || w.Fence(ctx, claudeSaveTimeout+claudeSafetyMargin) != nil {
		return
	}
	_ = w.Journal.save(ctx, secret, ready)
}

// needsLogin tombstones an interrupted attempt: the stage becomes NeedsLogin
// and the refresh credential, which may be spent, is dropped.
func (w *claudeWorker) needsLogin(ctx context.Context) {
	w.note("needs-login")
	if w.Fence(ctx, claudeSaveTimeout+claudeSafetyMargin) != nil {
		return
	}
	secret, r, err := w.load(ctx)
	if err != nil || r.Stage != claudeIntent {
		return
	}
	r.Stage, r.AttemptUID, r.Refresh, r.RefreshExpiresAt = claudeNeedsLogin, "", secretValue{}, time.Time{}
	_ = w.Journal.save(ctx, secret, r)
}

// claudeRunnable is the worker Run registers, or nil when the flag is off.
func claudeRunnable(o Options, c client.Client, fence *LeaseFence, clk clock.Clock) manager.Runnable {
	a := o.ClaudeAuth
	if !a.Enabled {
		return nil
	}
	if a.JournalSecret == "" {
		a.JournalSecret = DefaultClaudeJournalSecret
	}
	if a.LiveSecret == "" {
		a.LiveSecret = DefaultClaudeLiveSecret
	}
	return &claudeWorker{
		Journal:   &claudeJournal{Client: c, Secret: types.NamespacedName{Namespace: o.Namespace, Name: a.JournalSecret}},
		Transport: newClaudeHTTPRefresh(),
		Publisher: &claudePublicSecret{Client: c, Secret: types.NamespacedName{Namespace: o.GHTokenSecret.Namespace, Name: a.LiveSecret}},
		Fence:     fence.CheckBudget,
		Clock:     clk,
		Log:       o.Log.WithName("claude-auth"),
	}
}
