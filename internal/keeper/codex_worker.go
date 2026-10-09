package keeper

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/utils/clock"

	"github.com/thaynes43/dev-env/internal/codexauth"
)

// CodexOptions is disabled by default. Enabling requires separately reviewed
// named-Secret RBAC and the isolated login-helper staging mount.
type CodexOptions struct {
	Enabled       bool
	JournalSecret string
	LiveSecret    string
	LoginDir      string
}

type codexPublisher interface {
	publish(context.Context, codexauth.Access, time.Time) error
}

// codexWorker serializes login/adoption and rotating-token requests. It is a
// separate leader runnable, never a generic Keeper.Job or retrying credential job.
type codexWorker struct {
	Journal   *codexJournal
	Transport codexRefreshTransport
	Publisher codexPublisher
	Fence     func(context.Context, time.Duration) error
	Clock     clock.Clock
	LoginDir  string
	Identity  string
	Log       logr.Logger
	mu        sync.Mutex
	published uint64
	halted    bool
	ready     atomic.Bool
}

type codexLoaded struct {
	Secret *corev1.Secret
	Record codexRecord
}

func (*codexWorker) NeedLeaderElection() bool { return true }

func (w *codexWorker) Start(ctx context.Context) error {
	server, err := w.serveControl(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = server.Close() }()
	for {
		if ctx.Err() != nil {
			return nil
		}
		w.mu.Lock()
		err := w.tick(ctx)
		w.mu.Unlock()
		if err != nil && ctx.Err() == nil {
			w.Log.Error(err, "Codex authentication needs attention")
		}
		select {
		case <-ctx.Done():
			return nil
		case <-w.Clock.After(30 * time.Second):
		}
	}
}

func (w *codexWorker) load(ctx context.Context) (*codexLoaded, error) {
	c, cancel := context.WithTimeout(ctx, codexSaveTimeout)
	defer cancel()
	s, r, err := w.Journal.load(c)
	if err != nil {
		return nil, err
	}
	return &codexLoaded{Secret: s, Record: r}, nil
}

func (w *codexWorker) tick(ctx context.Context) error {
	d, err := w.load(ctx)
	if err != nil {
		w.ready.Store(false)
		return err
	}
	if w.expiredLoginReservation(d.Record) {
		if w.finishExpiredLogin(ctx, d) != nil {
			return w.needsLogin(ctx)
		}
		d, err = w.load(ctx)
		if err != nil {
			return w.needsLogin(ctx)
		}
	}
	if w.halted {
		return errCodexRefresh
	}
	r := d.Record
	if r.Stage == codexIntent {
		return w.needsLogin(ctx)
	}
	if r.Stage != codexReady {
		w.ready.Store(false)
		return errCodexRefresh
	}
	now := w.Clock.Now()
	if r.Access.LastRefresh.After(now.Add(time.Minute)) {
		return w.needsLogin(ctx)
	}
	// About a day early for the observed ten-day token, but derive the lead
	// from each actual lifetime. Even the minimum lifetime leaves >5m plus
	// projection/poll latency before native clients try their empty refresh.
	lead := min(24*time.Hour, r.Access.ExpiresAt.Sub(r.Access.LastRefresh)/3)
	if !now.Before(r.Access.ExpiresAt.Add(-lead)) {
		if w.Fence(ctx, codexRequestBudget) != nil {
			w.ready.Store(false)
			return errors.New("codex leadership budget is unavailable")
		}
		r.Stage, r.AttemptUID = codexIntent, string(uuid.NewUUID())
		r.LoginUntil = time.Time{}
		if w.Journal.save(ctx, d.Secret, r) != nil {
			return w.needsLogin(ctx)
		}
		// Re-read the journal and Lease after intent; never POST unless the
		// durable state and the complete remaining budget still match.
		d, err = w.load(ctx)
		if err != nil || d.Record.Stage != codexIntent || d.Record.AttemptUID != r.AttemptUID || !sameCodexMaterial(d.Record, r) {
			return w.needsLogin(ctx)
		}
		if w.Fence(ctx, codexRequestBudget) != nil {
			// This same serialized attempt has not called the transport. Only a
			// confirmed shorter-budget CAS may restore its known unused token.
			if w.Fence(ctx, codexSaveTimeout+codexSafetyMargin) != nil {
				return w.needsLogin(ctx)
			}
			r.Stage, r.AttemptUID = codexReady, ""
			if w.Journal.save(ctx, d.Secret, r) != nil {
				return w.needsLogin(ctx)
			}
			w.ready.Store(false)
			return errors.New("codex refresh deferred for leadership budget")
		}
		id, access, refresh, rerr := w.Transport.refresh(ctx, r.Refresh)
		if rerr != nil || ctx.Err() != nil {
			return w.needsLogin(ctx)
		}
		account, exp, aerr := codexauth.Claims(access.Reveal())
		next := codexRecord{Access: codexauth.Access{Generation: r.Access.Generation + 1, AccountID: account, IDToken: id.Reveal(), AccessToken: access.Reveal(), ExpiresAt: exp, LastRefresh: w.Clock.Now().UTC()}, Refresh: refresh, Stage: codexIntent, AttemptUID: r.AttemptUID}
		if aerr != nil || account != r.Access.AccountID || next.Access.Validate(w.Clock.Now()) != nil || !exp.After(w.Clock.Now().Add(codexauth.MinLifetime)) || w.Fence(ctx, codexSaveTimeout+codexSafetyMargin) != nil {
			return w.needsLogin(ctx)
		}
		// A CAS stores replacement refresh material BEFORE access publication.
		// Any response/persist ambiguity stops this process and records a
		// NeedsLogin tombstone when the current leader can still persist it.
		if w.Journal.save(ctx, d.Secret, next) != nil {
			return w.needsLogin(ctx)
		}
		// Replacement and readiness are separate transitions. If the material
		// write's acknowledgement is lost, even an applied write is still an
		// Intent and recovery refuses it. Only confirmed material becomes Ready.
		d, err = w.load(ctx)
		if err != nil || d.Record.Stage != codexIntent || d.Record.AttemptUID != next.AttemptUID || !sameCodexMaterial(d.Record, next) || w.Fence(ctx, codexSaveTimeout+codexSafetyMargin) != nil {
			return w.needsLogin(ctx)
		}
		next.Stage, next.AttemptUID = codexReady, ""
		if w.Journal.save(ctx, d.Secret, next) != nil {
			return w.needsLogin(ctx)
		}
		r = next
	}
	if r.Access.Validate(w.Clock.Now()) != nil {
		return w.needsLogin(ctx)
	}
	if w.published != r.Access.Generation {
		if w.Fence(ctx, codexSaveTimeout+codexSafetyMargin) != nil {
			w.ready.Store(false)
			return errors.New("codex leadership budget is unavailable")
		}
		if err = w.Publisher.publish(ctx, r.Access, w.Clock.Now()); err != nil {
			w.ready.Store(false)
			return err
		}
		w.published = r.Access.Generation
	}
	w.ready.Store(true)
	return nil
}

// needsLogin never dispatches or retries a rotating request. An interrupted
// intent is itself a durable refusal after leadership loss; a live leader also
// clears refresh material and tombstones even a CAS whose response was lost.
func (w *codexWorker) needsLogin(ctx context.Context) error {
	w.halted = true
	w.ready.Store(false)
	if w.Fence(ctx, codexSaveTimeout+codexSafetyMargin) == nil {
		if d, err := w.load(ctx); err == nil {
			r := d.Record
			r.Stage, r.Refresh, r.LoginUntil, r.LoginOwner = codexNeedsLogin, secretValue{}, time.Time{}, ""
			r.ResumeOnCancel = false
			_ = w.Journal.save(ctx, d.Secret, r)
		}
	}
	return errCodexRefresh
}
