package usagetotals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

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
	added map[types.UID]string
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

// Reconcile adds a ready Session, then writes AnnotationCounted. The add
// happens before the mark. A failed mark write is remembered in this process,
// so the next reconcile writes the mark again and does not add again. A
// restart without the mark adds the Session again, which is the accepted
// crash window.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if !r.elected() {
		return ctrl.Result{}, nil
	}
	if r.SessionNamespace != "" && req.Namespace != r.SessionNamespace {
		return ctrl.Result{}, nil
	}
	var session v1alpha1.AgentSession
	if err := r.Client.Get(ctx, req.NamespacedName, &session); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if session.UID == "" {
		return ctrl.Result{}, errors.New("task usage session has no uid")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.added == nil {
		r.added = map[types.UID]string{}
	}
	if mark := session.Annotations[AnnotationCounted]; mark != "" {
		r.added[session.UID] = mark
		return ctrl.Result{}, nil
	}
	if mark, ok := r.added[session.UID]; ok {
		if err := r.writeMark(ctx, &session, mark); err != nil {
			return ctrl.Result{}, fmt.Errorf("task usage mark: %w", err)
		}
		return ctrl.Result{}, nil
	}
	con, ok := contributionOf(&session)
	if !ok {
		return ctrl.Result{}, nil
	}
	if r.Counters == nil {
		return ctrl.Result{}, errors.New("task usage counters are not configured")
	}
	r.Counters.apply(con)
	r.added[session.UID] = con.mark
	if err := r.writeMark(ctx, &session, con.mark); err != nil {
		return ctrl.Result{}, fmt.Errorf("task usage mark: %w", err)
	}
	return ctrl.Result{}, nil
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
