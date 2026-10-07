// Package grantexpiry is the operator's expiry backstop for egress grants
// (DESIGN-001 6.12, D-64). It watches AccessGrants and deletes only their expired
// namespaced network policies. It never writes grants or touches session pods.
package grantexpiry

import (
	"context"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/egress"
)

// RetryInterval also closes the race where a broker create in flight crosses
// expiry: while the record remains Active, absence is checked again, not
// remembered as a completed revocation.
const RetryInterval = time.Minute

// Reconciler runs under the operator's existing leader election. The
// AccessGrant informer reconstructs every expiry timer on startup. Policies
// are read by name through APIReader only: its CNP RBAC is get and delete,
// without list, watch, create or patch.
type Reconciler struct {
	Client           client.Client
	APIReader        client.Reader
	SessionNamespace string
	Clock            clock.PassiveClock
}

// SetupWithManager watches grants, never CiliumNetworkPolicies. Keeping this
// controller separate also leaves the session reconciler's deletion guards
// and running pods unchanged.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).Named("egress-grant-expiry").
		For(&v1alpha1.AccessGrant{}).Complete(r)
}

func (r *Reconciler) now() time.Time {
	if r.Clock != nil {
		return r.Clock.Now()
	}
	return time.Now()
}

// Reconcile removes the policy at expiry. Only the broker changes the phase,
// finalizer and audit-retention record. An Active record with no expiry fails
// closed, just as the broker fails it rather than making an unbounded grant.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if req.Namespace != r.SessionNamespace {
		return ctrl.Result{}, nil
	}
	var g v1alpha1.AccessGrant
	if err := r.APIReader.Get(ctx, req.NamespacedName, &g); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if g.Spec.Type != v1alpha1.GrantEgress || !strings.HasPrefix(g.Name, "grant-") ||
		len(validation.IsDNS1123Label(g.Name)) != 0 || g.Spec.Requester.Session == "" {
		return ctrl.Result{}, nil
	}
	if g.Status.ExpiresAt == nil {
		if g.Status.Phase != v1alpha1.GrantActive {
			return ctrl.Result{}, nil
		}
	} else if left := g.Status.ExpiresAt.Sub(r.now()); left > 0 {
		return ctrl.Result{RequeueAfter: left}, nil
	}
	res := ctrl.Result{}
	if g.Status.Phase == v1alpha1.GrantActive || !g.DeletionTimestamp.IsZero() {
		res.RequeueAfter = RetryInterval
	}
	u := egress.Object(r.SessionNamespace, g.Name)
	if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(u), u); err != nil {
		return res, client.IgnoreNotFound(err)
	}
	if !egress.OwnedBy(u, r.SessionNamespace, &g) {
		log.FromContext(ctx).Info("left the expiry policy alone: ownership does not match", "grant", g.Name)
		return res, nil
	}
	uid := u.GetUID()
	if err := r.Client.Delete(ctx, u, client.Preconditions{UID: &uid}); err != nil {
		switch {
		case apierrors.IsNotFound(err):
			return res, nil
		case apierrors.IsConflict(err):
			// A replacement must pass its own ownership check.
			return ctrl.Result{RequeueAfter: time.Second}, nil
		default:
			return ctrl.Result{}, fmt.Errorf("delete the expired egress policy %s: %w", g.Name, err)
		}
	}
	log.FromContext(ctx).Info("deleted an expired egress grant's policy", "grant", g.Name,
		"session", g.Spec.Requester.Session, "policyUID", uid)
	// A policy with a finalizer is not gone on a successful Delete request.
	// Recheck once, and keep checking if it remains or the broker is down.
	return ctrl.Result{RequeueAfter: RetryInterval}, nil
}
