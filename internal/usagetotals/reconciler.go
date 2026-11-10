package usagetotals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// Reconciler adds each ready Session to the task-usage counters once.
// SetupWithManager starts it on the leader only. Elected is checked again
// so a replica that is not the leader, and an operator started without
// leader election, does not add. Nil Elected does not add.
type Reconciler struct {
	Client           client.Client
	Counters         *Counters
	SessionNamespace string
	Elected          func() bool

	mu    sync.Mutex
	added map[types.UID]remembered
	names map[types.NamespacedName]types.UID
}

type remembered struct {
	mark string
}

// NeedLeaderElection makes the controller run in the leader only.
func (r *Reconciler) NeedLeaderElection() bool { return true }

// SetupWithManager watches AgentSessions. The leader's start list is the
// first sight of Sessions that already exist.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	need := r.NeedLeaderElection()
	return ctrl.NewControllerManagedBy(mgr).
		Named("task-usage-totals").
		For(&v1alpha1.AgentSession{}).
		WithOptions(controller.Options{NeedLeaderElection: &need, MaxConcurrentReconciles: 1}).
		Complete(r)
}

func (r *Reconciler) elected() bool {
	return r != nil && r.Elected != nil && r.Elected()
}

// Reconcile creates the series a ready Session needs at 0. When every one of
// those series has existed for settleFor, it adds the Session and then writes
// AnnotationCounted, in that order, in the same reconcile. A ready Session
// with no mark whose task finished more than countWindow before this
// reconcile is marked skipped and not added: writeMark does that write, no
// series is created, and a failed write is retried on the next reconcile
// without adding. A failed mark write after an add is remembered in this
// process, so the next reconcile writes the mark again and does not add
// again. A restart without the mark adds a Session that is still inside the
// window, which is the accepted crash window. Any mark already stored,
// including the skip, is left alone.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if !r.elected() {
		return ctrl.Result{}, nil
	}
	if r.SessionNamespace != "" && req.Namespace != r.SessionNamespace {
		return ctrl.Result{}, nil
	}
	var session v1alpha1.AgentSession
	if err := r.Client.Get(ctx, req.NamespacedName, &session); err != nil {
		if apierrors.IsNotFound(err) {
			r.forget(req.NamespacedName)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	if session.UID == "" {
		return ctrl.Result{}, errors.New("task usage session has no uid")
	}
	r.mu.Lock()
	if r.added == nil {
		r.added = map[types.UID]remembered{}
		r.names = map[types.NamespacedName]types.UID{}
	}
	if session.Annotations[AnnotationCounted] != "" {
		r.mu.Unlock()
		return ctrl.Result{}, nil
	}
	if mem, ok := r.added[session.UID]; ok {
		r.mu.Unlock()
		if err := r.writeMark(ctx, &session, mem.mark); err != nil {
			return ctrl.Result{}, fmt.Errorf("task usage mark: %w", err)
		}
		return ctrl.Result{}, nil
	}
	con, ok := contributionOf(&session)
	if !ok {
		r.mu.Unlock()
		return ctrl.Result{}, nil
	}
	// The counters' clock is the one the settle uses. A nil Counters still
	// has that clock, so a skip does not need the collectors to be configured.
	if olderThanCountWindow(&session, r.Counters.clock()) {
		r.mu.Unlock()
		if err := r.writeMark(ctx, &session, markSkippedOld); err != nil {
			return ctrl.Result{}, fmt.Errorf("task usage mark: %w", err)
		}
		return ctrl.Result{}, nil
	}
	if r.Counters == nil {
		r.mu.Unlock()
		return ctrl.Result{}, errors.New("task usage counters are not configured")
	}
	added, wait := r.Counters.addIfSettled(con)
	if !added {
		r.mu.Unlock()
		return ctrl.Result{RequeueAfter: wait}, nil
	}
	r.added[session.UID] = remembered{mark: con.mark}
	r.names[req.NamespacedName] = session.UID
	r.mu.Unlock()
	if err := r.writeMark(ctx, &session, con.mark); err != nil {
		return ctrl.Result{}, fmt.Errorf("task usage mark: %w", err)
	}
	return ctrl.Result{}, nil
}

func (r *Reconciler) forget(name types.NamespacedName) {
	r.mu.Lock()
	defer r.mu.Unlock()
	uid, ok := r.names[name]
	if !ok {
		return
	}
	delete(r.added, uid)
	delete(r.names, name)
}

// writeMark patches only the annotation. The Session's status stays on its
// subresource, and a large spec is not part of the diff.
func (r *Reconciler) writeMark(ctx context.Context, session *v1alpha1.AgentSession, mark string) error {
	if session.Annotations[AnnotationCounted] == mark {
		return nil
	}
	body, err := json.Marshal(map[string]any{
		"metadata": map[string]any{
			"annotations": map[string]string{AnnotationCounted: mark},
		},
	})
	if err != nil {
		return err
	}
	return r.Client.Patch(ctx, session, client.RawPatch(types.MergePatchType, body))
}
