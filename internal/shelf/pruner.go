package shelf

import (
	"context"
	"sort"
	"time"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/templates"
)

// Pruner removes rescues and session logs past the templates' bundleRetention
// (D-09, D-67) from the shared volume, through the shelf. It runs in the
// operator's leader.
type Pruner struct {
	Shelf *Shelf
	// Reader lists the AgentSessions from the API server, so a session created
	// a moment ago is kept.
	Reader client.Reader
	// Namespace is the session namespace.
	Namespace string
	// Templates returns the current templates: the retention.
	Templates func(context.Context) (*templates.Templates, error)
	// First is the wait before the first prune (zero: 10 minutes), Every the
	// wait between prunes (zero: 6 hours).
	First, Every time.Duration
	Log          logr.Logger
}

// NeedLeaderElection makes the pruner run in the leader only.
func (p *Pruner) NeedLeaderElection() bool { return true }

// Start runs until ctx ends. It implements controller-runtime's Runnable.
func (p *Pruner) Start(ctx context.Context) error {
	wait := p.First
	if wait <= 0 {
		wait = 10 * time.Minute
	}
	every := p.Every
	if every <= 0 {
		every = 6 * time.Hour
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
		wait = every
		rep, err := p.Prune(ctx)
		if err != nil {
			p.Log.Error(err, "prune the shelf; the next try is in "+every.String())
			continue
		}
		for _, r := range rep.Removed {
			p.Log.Info("pruned", "path", r.Path, "session", r.Session, "age", r.Age, "bytes", r.Bytes)
		}
		for _, e := range rep.Errors {
			p.Log.Info("could not prune", "error", e)
		}
		p.Log.Info("shelf pruned", "retention", rep.OlderThan, "removed", len(rep.Removed), "kept", rep.Kept, "errors", len(rep.Errors))
	}
}

// Prune runs one prune: the retention from the templates, and every session
// that still exists, or that a session restores from, kept.
func (p *Pruner) Prune(ctx context.Context) (protocol.PruneReport, error) {
	t, err := p.Templates(ctx)
	if err != nil {
		return protocol.PruneReport{}, err
	}
	keep, err := Keep(ctx, p.Reader, p.Namespace)
	if err != nil {
		return protocol.PruneReport{}, err
	}
	return p.Shelf.Prune(ctx, protocol.PruneRequest{OlderThan: t.BundleRetention().String(), Keep: keep})
}

// Keep is the prune's keep list: every session in the namespace, and every
// session one of them restores from, whose rescue its first boot still needs.
func Keep(ctx context.Context, r client.Reader, namespace string) ([]string, error) {
	var list v1alpha1.AgentSessionList
	if err := r.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for i := range list.Items {
		s := &list.Items[i]
		set[s.Name] = true
		if from, _, err := protocol.ParseRescueID(s.Spec.Restore); err == nil {
			set[from] = true
		}
	}
	keep := make([]string, 0, len(set))
	for k := range set {
		keep = append(keep, k)
	}
	sort.Strings(keep)
	return keep, nil
}
