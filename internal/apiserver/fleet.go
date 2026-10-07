package apiserver

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// fleet serves GET /v1/fleet: every session that holds or waits for a pod, the
// scheduler's reason for each that waits, the nodes they run on, the current
// template revision and the sessions behind it. It reports; it gates nothing
// (D-21).
func (s *Server) fleet(ctx context.Context, _ http.ResponseWriter, r *http.Request, _ *caller) (int, any, error) {
	if len(r.URL.Query()) > 0 {
		return 0, nil, badRequest("GET /v1/fleet takes no query parameters")
	}
	var list v1alpha1.AgentSessionList
	if err := s.Client.List(ctx, &list, client.InNamespace(s.Policy.SessionNamespace)); err != nil {
		return 0, nil, fromKubeError(err, "list sessions")
	}
	f := apiv1.Fleet{
		Phases:   map[string]int{},
		Sessions: []apiv1.FleetSession{},
		Nodes:    []apiv1.FleetNode{},
		Outdated: []string{},
	}
	if s.Templates != nil {
		if t, err := s.Templates(ctx); err != nil {
			f.RevisionError = err.Error()
		} else {
			f.Revision = t.Revision()
		}
	} else {
		f.RevisionError = "the operator reads no templates"
	}

	nodes := map[string]int{}
	for i := range list.Items {
		sess := &list.Items[i]
		phase := phaseOf(sess)
		f.Phases[phase]++
		if phase == string(v1alpha1.PhaseSuspended) || phase == string(v1alpha1.PhaseArchived) {
			continue
		}
		v := view(sess, false)
		fs := apiv1.FleetSession{
			Name: v.Name, Repo: v.Repo, Mode: v.Mode, Phase: v.Phase, Pending: v.Pending,
			Node: v.Node, Revision: v.Revision, Outdated: v.Outdated, Parent: v.Parent,
			Reaping: v.Reaping, CreatedAt: v.CreatedAt,
		}
		if a := v.AgentStatus; a != nil {
			fs.AgentState, fs.LastHeartbeat = a.State, a.LastHeartbeat
		}
		f.Sessions = append(f.Sessions, fs)
		if fs.Node != "" {
			nodes[fs.Node]++
		}
		if fs.Outdated {
			f.Outdated = append(f.Outdated, fs.Name)
		}
	}
	slices.SortFunc(f.Sessions, func(a, b apiv1.FleetSession) int {
		if c := strings.Compare(a.Phase, b.Phase); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	for n, c := range nodes {
		f.Nodes = append(f.Nodes, apiv1.FleetNode{Name: n, Sessions: c})
	}
	slices.SortFunc(f.Nodes, func(a, b apiv1.FleetNode) int { return strings.Compare(a.Name, b.Name) })
	slices.Sort(f.Outdated)
	return http.StatusOK, f, nil
}
