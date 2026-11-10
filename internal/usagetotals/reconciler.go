package usagetotals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

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

// markDelay is how soon a reconcile looks again for the scrape that reports
// the total. The annotation is written on that later reconcile.
const markDelay = 15 * time.Second

type remembered struct {
	mark    string
	epoch   uint64
	touched []seriesID
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

// Reconcile adds a ready Session, then writes AnnotationCounted after a scrape
// has reported that total. The add happens before the mark. A failed mark
// write is remembered in this process, so the next reconcile writes the mark
// again and does not add again. A restart without the mark adds the Session
// again, which is the accepted crash window.
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
		ready := r.Counters != nil && r.Counters.ready(mem.epoch, mem.touched)
		r.mu.Unlock()
		if !ready {
			return ctrl.Result{RequeueAfter: markDelay}, nil
		}
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
	if r.Counters == nil {
		r.mu.Unlock()
		return ctrl.Result{}, errors.New("task usage counters are not configured")
	}
	epoch, touched := r.Counters.apply(con)
	r.added[session.UID] = remembered{mark: con.mark, epoch: epoch, touched: touched}
	r.names[req.NamespacedName] = session.UID
	r.mu.Unlock()
	return ctrl.Result{RequeueAfter: markDelay}, nil
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
