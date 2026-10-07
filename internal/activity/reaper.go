// Package activity deletes expired declare-activity declarations (DESIGN-001
// 6.9, D-17, D-66). It runs in the operator's leader, beside the reconciler.
package activity

import (
	"context"
	"time"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// Reaper deletes each Activity in Namespace once it has expired. A stale
// declaration is how a real fault gets waved off, so expiry is never late by
// more than Every.
type Reaper struct {
	// Client deletes; Reader lists from the API server (the operator's cache
	// does not hold dev-env-system's activities).
	Client client.Client
	Reader client.Reader
	// Namespace holds the activities (dev-env-system).
	Namespace string
	// Every is how often it looks; zero means a minute.
	Every time.Duration
	Log   logr.Logger
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

// NeedLeaderElection makes the reaper run in the leader only.
func (r *Reaper) NeedLeaderElection() bool { return true }

// Start runs until ctx ends. It implements controller-runtime's Runnable.
func (r *Reaper) Start(ctx context.Context) error {
	every := r.Every
	if every <= 0 {
		every = time.Minute
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		r.Reap(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// Reap deletes the expired activities once, and reports how many went.
func (r *Reaper) Reap(ctx context.Context) int {
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	var list v1alpha1.ActivityList
	if err := r.Reader.List(ctx, &list, client.InNamespace(r.Namespace)); err != nil {
		r.Log.Error(err, "list activities")
		return 0
	}
	n := 0
	for i := range list.Items {
		a := &list.Items[i]
		if a.Spec.ExpiresAt.After(now) {
			continue
		}
		if err := r.Client.Delete(ctx, a, client.Preconditions{UID: &a.UID}); client.IgnoreNotFound(err) != nil {
			r.Log.Error(err, "delete an expired activity", "activity", a.Name)
			continue
		}
		r.Log.Info("deleted an expired activity", "activity", a.Name, "declaredBy", a.Spec.DeclaredBy)
		n++
	}
	return n
}
