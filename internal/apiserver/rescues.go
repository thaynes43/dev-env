package apiserver

import (
	"context"
	"errors"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/shelf"
	"github.com/thaynes43/dev-env/internal/templates"
)

// RescueShelf lists the rescues on the shared volume and holds one for a
// restore: shelf.Shelf, which runs `agentd ctl rescues` and `agentd ctl
// hold-rescue` in the shelf pod (D-67).
type RescueShelf interface {
	List(ctx context.Context, session string) (protocol.RescueList, error)
	Hold(ctx context.Context, id string) (protocol.HoldResult, error)
}

// listRescues is GET /v1/rescues[?session=] (D-67).
func (s *Server) listRescues(ctx context.Context, _ http.ResponseWriter, r *http.Request, _ *caller) (int, any, error) {
	session := r.URL.Query().Get("session")
	if session != "" && !protocol.ValidSessionName(session) {
		return 0, nil, invalid(fieldError("session", "%q is not a session name", session))
	}
	list, err := s.rescues(ctx, session)
	if err != nil {
		return 0, nil, err
	}
	keepList, err := shelf.Keep(ctx, s.Client, s.Policy.SessionNamespace)
	if err != nil {
		return 0, nil, fromKubeError(err, "list sessions")
	}
	keep := make(map[string]bool, len(keepList))
	for _, k := range keepList {
		keep[k] = true
	}
	retention := templates.DefaultBundleRetention
	if s.Templates != nil {
		if t, err := s.Templates(ctx); err == nil {
			retention = t.BundleRetention()
		}
	}
	out := apiv1.RescueList{Rescues: []apiv1.Rescue{}, Unrecognized: list.Unrecognized}
	for _, e := range list.Rescues {
		out.Rescues = append(out.Rescues, rescueView(e, keep[e.Session], retention))
	}
	return http.StatusOK, out, nil
}

// rescues lists the shelf.
func (s *Server) rescues(ctx context.Context, session string) (protocol.RescueList, error) {
	if s.Shelf == nil {
		return protocol.RescueList{}, noShelf()
	}
	list, err := s.Shelf.List(ctx, session)
	return list, shelfError(err)
}

func noShelf() error {
	return newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "this API has no shelf to list or restore rescues through (D-67)")
}

// shelfError maps a missing shelf pod to a 503.
func shelfError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, shelf.ErrNoShelf):
		return newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "%v: rescues are listed and restored through it (D-67)", err)
	default:
		return internal("%v", err)
	}
}

func rescueView(e protocol.RescueEntry, sessionExists bool, retention time.Duration) apiv1.Rescue {
	v := apiv1.Rescue{
		ID: e.ID, Session: e.Session, CreatedAt: e.CreatedAt, Finished: e.Finished, Bytes: e.Bytes, Error: e.Error,
		SessionExists: sessionExists,
	}
	if m := e.Manifest; m != nil {
		v.Complete = m.Complete && e.Error == ""
		for _, r := range m.Repos {
			rr := apiv1.RescueRepo{Repo: path.Base(r.Path), Refs: []string{}, Bytes: r.Bundle.Size}
			for _, ref := range r.Bundle.Refs {
				rr.Refs = append(rr.Refs, ref.Name)
			}
			v.Repos = append(v.Repos, rr)
		}
	}
	if !sessionExists {
		at := e.CreatedAt
		if e.ModifiedAt.After(at) {
			at = e.ModifiedAt
		}
		after := at.Add(retention).UTC()
		v.PruneAfter = &after
	}
	return v
}

// checkRestore checks a restore against the shelf before the session exists
// (D-67): the rescue must be complete and hold a bundle for the session's
// repo. With no base asked for, the worktree starts at the old session's
// rescued branch when the bundle holds it; a rescue branch is never the base.
func (s *Server) checkRestore(ctx context.Context, req apiv1.CreateSessionRequest, sess *v1alpha1.AgentSession) error {
	from, _, err := protocol.ParseRescueID(req.Restore)
	if err != nil {
		return invalid(fieldError("restore", "%v", err))
	}
	if s.Shelf == nil {
		return noShelf()
	}
	// The hold also sets the rescue's time, so no prune takes it before the
	// new session keeps it.
	held, err := s.Shelf.Hold(ctx, req.Restore)
	if err != nil {
		return shelfError(err)
	}
	e := held.Rescue
	if !held.Found {
		e = nil
	}
	switch {
	case e == nil:
		return invalid(fieldError("restore", "no rescue %s on the shared volume; GET %s?session=%s lists that session's", req.Restore, apiv1.RescuesPath, from))
	case e.Manifest == nil || e.Error != "" || !e.Manifest.Complete:
		why := "it did not finish"
		if e.Error != "" {
			why = e.Error
		} else if e.Manifest != nil {
			why = "a bundle in it failed"
		}
		return invalid(fieldError("restore", "rescue %s is not complete (%s); only a complete rescue is restored", req.Restore, why))
	}
	var repos []string
	for _, r := range e.Manifest.Repos {
		name := path.Base(r.Path)
		repos = append(repos, name)
		if name != sess.Spec.Repo {
			continue
		}
		if req.Base == "" {
			for _, ref := range r.Bundle.Refs {
				if ref.Name == "refs/heads/agent/"+from {
					sess.Spec.Base = "refs/rescued/heads/agent/" + from
				}
			}
		}
		return nil
	}
	return invalid(fieldError("restore", "rescue %s holds no bundle for %s; it holds: %s", req.Restore, sess.Spec.Repo, strings.Join(repos, ", ")))
}
