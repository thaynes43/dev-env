package keeper

import (
	"context"
	"errors"
	"sync"
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
	// LoginDir is the memory-backed directory the login helper shares with the
	// keeper; the keeper serves its private control socket there (increment 5).
	LoginDir string
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
// One cycle: fence -> journal RefreshIntent and its unsent marker (one CAS) ->
// fence -> dispatch commit: remove the marker (CAS) -> fence -> one POST ->
// journal the replacement (CAS) -> fence -> publish the access-only projection.
// The journal is the only memory that matters: a Ready record is safe to act
// on, and a RefreshIntent found at the start of a tick means a POST may have
// spent the refresh token without its replacement being saved, so the login is
// marked NeedsLogin. A fresh login (increment 5) is the only way back. The
// exception is an attempt proved unsent, by the journal's unsent marker or by
// this process itself when its rollback write did not commit
// (claudeUnsentProof): that intent is returned to Ready instead.
type claudeWorker struct {
	Journal   *claudeJournal
	Transport claudeRefreshTransport
	Publisher claudePublisher
	Fence     func(context.Context, time.Duration) error
	Clock     clock.Clock
	Log       logr.Logger
	// LoginDir enables the login control socket and the adoption of a staged
	// login; empty leaves the worker refresh-only.
	LoginDir string
	// Obs receives the journal's state for the login gauges and the notifier;
	// nil keeps the worker as it was.
	Obs *LoginHub

	// mu serializes a refresh tick with every control request, so an adoption
	// never overlaps a refresh in flight. Everything below is guarded by it.
	mu         sync.Mutex
	published  uint64        // last generation this process published
	last       string        // last state logged
	attempt    claudeAttempt // the one staging directory a helper may have open
	retryAfter time.Time     // bounded delay after a provably unsent attempt
	// unsent is the proof that an attempt still journaled as RefreshIntent
	// was never sent, kept while its rollback write has not committed: this
	// process's own knowledge, or the journal's unsent marker.
	unsent claudeUnsentProof
	// expired is the attempt whose proof outlived claudeUnsentProofLife; its
	// marker is not taken as a proof again, so the intent is tombstoned.
	expired string
}

// claudeUnsentProof names one attempt proved unsent: the journal incarnation
// it was written to and its attempt UID. It holds no credential. marked means
// the proof is the journal's marker, which the rollback must still find.
type claudeUnsentProof struct {
	journal, attempt string
	until            time.Time // after this the proof is dropped and the intent tombstoned
	marked           bool
}

// claudeUnsentProofLife bounds how long a failing rollback is retried before
// the worker falls back to the conservative tombstone, which pages.
const claudeUnsentProofLife = 5 * time.Minute

// claudeRollbackTime bounds one rollback: a load and a save of 2 s each and the
// fence's safety margin. The rollback is detached from shutdown cancellation
// but not from this deadline, and the fence refuses it past the Lease budget.
const claudeRollbackTime = 2*claudeSaveTimeout + claudeSafetyMargin

func (*claudeWorker) NeedLeaderElection() bool { return true }

func (w *claudeWorker) Start(ctx context.Context) error {
	if w.LoginDir != "" {
		if err := w.serveControl(ctx); err != nil {
			return err
		}
	}
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
	w.mu.Lock()
	defer w.mu.Unlock()
	secret, r, err := w.load(ctx)
	if err != nil {
		w.note("journal-unavailable")
		return
	}
	if r.Stage != claudeIntent {
		w.observe(ctx, string(secret.UID), r)
	}
	proof := w.unsent
	if proof.attempt != "" && !w.Clock.Now().Before(proof.until) {
		w.expired = proof.attempt
	}
	if r.Stage != claudeIntent || r.AttemptUID != proof.attempt || string(secret.UID) != proof.journal || !w.Clock.Now().Before(proof.until) {
		w.unsent = claudeUnsentProof{}
	}
	if r.Stage != claudeIntent || r.AttemptUID != w.expired {
		w.expired = ""
	}
	switch r.Stage {
	case claudeIntent:
		if w.unsent.attempt == "" && w.expired == "" && claudeUnsentMarked(secret, r) {
			// The journal's own marker proves this attempt was never
			// dispatched, so any keeper may return it to Ready.
			w.unsent = claudeUnsentProof{journal: string(secret.UID), attempt: r.AttemptUID, until: w.Clock.Now().Add(claudeUnsentProofLife), marked: true}
		}
		if w.unsent.attempt != "" {
			// The attempt is proved unsent, but its rollback has not
			// committed. Retry the fenced, CAS-guarded rollback of this exact
			// attempt instead of tombstoning an unspent token.
			ready := r
			ready.Stage, ready.AttemptUID = claudeReady, ""
			if w.rollbackUnsent(ctx, string(secret.UID), ready, r.AttemptUID) {
				w.retryAfter = w.Clock.Now().Add(time.Minute)
				w.note("rolled-back")
			} else {
				w.note("rollback-pending")
			}
			return
		}
		w.needsLogin(ctx, r.AttemptUID)
		return
	case claudeNeedsLogin:
		w.note("needs-login")
		return
	}
	if w.due(r) && !w.Clock.Now().Before(w.retryAfter) {
		if r, err = w.refresh(ctx); err != nil {
			return
		}
		// The replacement is saved; publish it even if shutdown has begun. The
		// fence and the publisher each carry their own 2 s timeout.
		ctx = context.WithoutCancel(ctx)
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
	journalUID := string(secret.UID)
	floor, err := w.Publisher.generation(ctx)
	if err != nil {
		w.note("publish-failed")
		return claudeRecord{}, err
	}
	gen, err := nextClaudeGeneration(prev.Access.Generation, floor)
	if err != nil {
		w.note("generation-unavailable")
		return claudeRecord{}, err
	}
	// Leadership with room for the POST and the save, before anything is written.
	if w.Fence(ctx, claudeRequestBudget) != nil {
		w.note("leadership-unavailable")
		return claudeRecord{}, errClaudeLeadership
	}
	intent := prev
	intent.Stage, intent.AttemptUID = claudeIntent, string(uuid.NewUUID())
	// The intent and its unsent marker are one compare-and-swap.
	if w.Journal.saveIntent(ctx, secret, intent) != nil {
		// A lost acknowledgement may still have stored the intent.
		w.rollbackUnsent(ctx, journalUID, prev, intent.AttemptUID)
		return claudeRecord{}, errClaudeJournal
	}
	// Re-read the stored intent and the Lease; POST only if both still hold.
	secret, stored, err := w.load(ctx)
	if err != nil || stored.Stage != claudeIntent || stored.AttemptUID != intent.AttemptUID {
		// Nothing has been sent yet: return our own intent to Ready if it is
		// still what the journal holds.
		w.rollbackUnsent(ctx, journalUID, prev, intent.AttemptUID)
		return claudeRecord{}, errClaudeJournal
	}
	requestCtx, cancelRequest := context.WithDeadline(ctx, time.Now().Add(claudeRequestTime))
	defer cancelRequest()
	// Leadership for the dispatch commit's save (claudeSaveTimeout plus the
	// margin fit in claudeRequestBudget); commitDispatch checks the full
	// request budget again after the commit.
	if w.Fence(ctx, claudeRequestBudget) != nil {
		w.rollbackUnsent(ctx, journalUID, prev, intent.AttemptUID)
		w.note("leadership-unavailable")
		return claudeRecord{}, errClaudeLeadership
	}
	var committed *corev1.Secret
	commit := func() error {
		var cerr error
		committed, cerr = w.commitDispatch(ctx, secret, intent.AttemptUID)
		return cerr
	}
	started := time.Now()
	resp, err := w.Transport.refresh(requestCtx, prev.Refresh, prev.Access.Scopes, commit)
	if err != nil {
		phase, class := claudeFailureLabels(err)
		w.Log.Info("claude refresh failed", "phase", phase, "class", class, "duration_ms", time.Since(started).Milliseconds())
		if errors.Is(err, errClaudeNotSent) {
			if w.rollbackUnsent(ctx, journalUID, prev, intent.AttemptUID) {
				w.retryAfter = w.Clock.Now().Add(time.Minute)
			}
			return claudeRecord{}, errClaudeRefresh
		}
		w.needsLogin(context.WithoutCancel(ctx), intent.AttemptUID)
		return claudeRecord{}, errClaudeRefresh
	}
	// The provider has accepted the POST and spent the old refresh token. From
	// here on nothing may be lost to a shutdown, so the remaining steps run on
	// a context detached from cancellation. Each step (fence, load, save,
	// publish) carries its own 2 s timeout, which bounds the whole.
	ctx = context.WithoutCancel(ctx)
	if resp.AccountUUID != "" && resp.AccountUUID != prev.Access.AccountUUID {
		w.Log.Info("claude refresh failed", "phase", "validate", "class", "account-mismatch", "duration_ms", time.Since(started).Milliseconds())
		w.needsLogin(ctx, intent.AttemptUID)
		return claudeRecord{}, errClaudeAccount
	}
	next, err := applyClaudeRefresh(prev, resp, gen, w.Clock.Now())
	if err != nil || next.Access.Validate(w.Clock.Now()) != nil {
		w.Log.Info("claude refresh failed", "phase", "validate", "class", "invalid", "duration_ms", time.Since(started).Milliseconds())
		w.needsLogin(ctx, intent.AttemptUID)
		return claudeRecord{}, errClaudeRefresh
	}
	// The replacement is stored BEFORE anything is published, and with no Lease
	// check first: the old refresh token is already spent, so the save must not
	// depend on leadership budget. The compare-and-swap on the resourceVersion
	// read after our intent is the guard: if a newer leader has acted on the
	// journal since, the save conflicts and nothing is overwritten. If the save
	// fails the journal still says RefreshIntent (or already holds the
	// replacement); the next tick marks the first NeedsLogin and never repeats
	// the POST.
	if committed == nil {
		// A transport that sent without the dispatch commit breaks its
		// contract and may have left the unsent marker behind a spent token:
		// save on the pre-commit Secret, which removes the marker, or else
		// tombstone the attempt now.
		if w.Journal.save(ctx, secret, next) != nil {
			w.needsLogin(ctx, intent.AttemptUID)
			w.note("journal-unavailable")
			return claudeRecord{}, errClaudeJournal
		}
	} else if w.Journal.save(ctx, committed, next) != nil {
		w.Log.Info("claude refresh failed", "phase", "save", "class", "unavailable", "duration_ms", time.Since(started).Milliseconds())
		w.note("journal-unavailable")
		return claudeRecord{}, errClaudeJournal
	}
	w.note("refreshed")
	w.observe(ctx, string(secret.UID), next)
	return next, nil
}

// observe hands the journal's state to the gauges: words and a time, no token.
func (w *claudeWorker) observe(ctx context.Context, uid string, r claudeRecord) {
	w.Obs.reportClaudeAt(uid, r.Access.Generation, r.Stage != claudeReady, r.RefreshExpiresAt, func() bool {
		current, _, err := w.load(ctx)
		return err == nil && string(current.UID) == uid
	})
}

// commitDispatch is the dispatch commit, called by the transport immediately
// before the POST, after every check that can prove the request unsent. It
// removes this attempt's unsent marker by compare-and-swap on the Secret as
// re-read after the intent, so a successor that rolled the intent back, or any
// other writer since, makes it fail and nothing is sent. A lost
// acknowledgement is read back: the commit holds only if the journal still
// holds this intent and no marker. Then the Lease must again cover the
// request, the replacement save and the margin (claudeRequestBudget). From the
// commit on, shutdown does not stop the attempt: this fence and the request run
// detached from cancellation, each within its own time bound.
//
// Lease arithmetic, pinned by TestClaudeDispatchCommitLeaseBudget: the fence
// before the commit needs claudeRequestBudget (7 s), which covers the commit's
// 2 s save and the 2 s margin; the fence after it needs claudeRequestBudget
// again: the 3 s request, the 2 s replacement save and the 2 s margin. Each is
// under the Lease fence's 10 s freshness bound.
func (w *claudeWorker) commitDispatch(ctx context.Context, intentSecret *corev1.Secret, uid string) (*corev1.Secret, error) {
	committed, err := w.Journal.clearUnsent(ctx, intentSecret)
	if err != nil {
		s, r, lerr := w.load(context.WithoutCancel(ctx))
		if lerr != nil || s.UID != intentSecret.UID || r.Stage != claudeIntent || r.AttemptUID != uid || len(s.Data[claudeUnsentKey]) != 0 {
			return nil, errClaudeJournal
		}
		committed = s
	}
	if w.Fence(context.WithoutCancel(ctx), claudeRequestBudget) != nil {
		return nil, errClaudeLeadership
	}
	return committed, nil
}

// rollbackUnsent returns an attempt this process proved unsent to Ready. When
// the write does not commit, it keeps the proof so the next tick retries this
// rollback instead of tombstoning; a commit, a restart, any other journal
// state or the proof's lifetime ends it.
func (w *claudeWorker) rollbackUnsent(ctx context.Context, journalUID string, ready claudeRecord, uid string) bool {
	if w.abandon(ctx, journalUID, ready, uid, w.unsent.marked && w.unsent.attempt == uid && w.unsent.journal == journalUID) {
		w.unsent = claudeUnsentProof{}
		return true
	}
	if w.unsent.attempt != uid || w.unsent.journal != journalUID {
		w.unsent = claudeUnsentProof{journal: journalUID, attempt: uid, until: w.Clock.Now().Add(claudeUnsentProofLife)}
	}
	return false
}

// abandon returns an intent that was never sent to Ready, if this attempt's
// intent is still what this journal incarnation holds and leadership can still
// write. When it cannot, the intent stays. The outcome is proven unsent, so
// the rollback runs detached from shutdown cancellation, within
// claudeRollbackTime; the fence still refuses a writer that lost leadership or
// whose Lease budget does not cover the save.
//
// marked means only the journal's marker proves the attempt unsent, so the
// marker must still be there. The journal UID check is defence in depth: the
// intent's compare-and-swap targeted the original object, so a recreated
// journal cannot hold this attempt UID unless its data was copied.
func (w *claudeWorker) abandon(ctx context.Context, journalUID string, ready claudeRecord, uid string, marked bool) bool {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), claudeRollbackTime)
	defer cancel()
	secret, r, err := w.load(ctx)
	if err == nil && marked && !claudeUnsentMarked(secret, r) {
		return false
	}
	if err != nil || string(secret.UID) != journalUID || r.Stage != claudeIntent || r.AttemptUID != uid || w.Fence(ctx, claudeSaveTimeout+claudeSafetyMargin) != nil {
		return false
	}
	return w.Journal.save(ctx, secret, ready) == nil
}

// needsLogin tombstones an interrupted attempt: the stage becomes NeedsLogin
// and the refresh credential, which may be spent, is dropped.
func (w *claudeWorker) needsLogin(ctx context.Context, uid string) {
	if uid == "" || w.Fence(ctx, claudeSaveTimeout+claudeSafetyMargin) != nil {
		return
	}
	secret, r, err := w.load(ctx)
	if err != nil || r.Stage != claudeIntent || r.AttemptUID != uid || w.Fence(ctx, claudeSaveTimeout+claudeSafetyMargin) != nil {
		return
	}
	r.Stage, r.AttemptUID, r.Refresh, r.RefreshExpiresAt = claudeNeedsLogin, "", secretValue{}, time.Time{}
	if w.Journal.save(ctx, secret, r) != nil {
		return
	}
	// Do not publish an observation if a newer adoption or intent won since
	// the CAS, or if the committed state cannot be verified.
	committed, current, err := w.load(ctx)
	if err != nil || current.Stage != claudeNeedsLogin || current.Access.Generation != r.Access.Generation || !sameClaudeAccess(current.Access, r.Access) {
		return
	}
	w.note("needs-login")
	w.observe(ctx, string(committed.UID), current)
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
	if a.LoginDir == "" {
		a.LoginDir = DefaultClaudeLoginDir
	}
	return &claudeWorker{
		LoginDir:  a.LoginDir,
		Journal:   &claudeJournal{Client: c, Secret: types.NamespacedName{Namespace: o.Namespace, Name: a.JournalSecret}},
		Transport: newClaudeHTTPRefresh(),
		Publisher: &claudePublicSecret{Client: c, Secret: types.NamespacedName{Namespace: o.GHTokenSecret.Namespace, Name: a.LiveSecret}},
		Fence:     fence.CheckBudget,
		Clock:     clk,
		Log:       o.Log.WithName("claude-auth"),
	}
}
