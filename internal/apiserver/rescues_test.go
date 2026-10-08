package apiserver

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/shelf"
)

// fakeShelf answers List from a fixed list, filtered by session as agentd
// filters it.
type fakeShelf struct {
	list  protocol.RescueList
	err   error
	asked []string
	held  []string
}

func (f *fakeShelf) Hold(_ context.Context, id string) (protocol.HoldResult, error) {
	f.held = append(f.held, id)
	if f.err != nil {
		return protocol.HoldResult{}, f.err
	}
	for i := range f.list.Rescues {
		if f.list.Rescues[i].ID == id {
			e := f.list.Rescues[i]
			return protocol.HoldResult{Found: true, Rescue: &e}, nil
		}
	}
	return protocol.HoldResult{}, nil
}

func (f *fakeShelf) List(_ context.Context, session string) (protocol.RescueList, error) {
	f.asked = append(f.asked, session)
	if f.err != nil {
		return protocol.RescueList{}, f.err
	}
	out := protocol.RescueList{Unrecognized: f.list.Unrecognized}
	for _, e := range f.list.Rescues {
		if session == "" || e.Session == session {
			out.Rescues = append(out.Rescues, e)
		}
	}
	return out, nil
}

var rescueAt = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func rescueEntry(session, name string, complete bool, repos map[string][]string) protocol.RescueEntry {
	e := protocol.RescueEntry{ID: session + "/" + name, Session: session, Name: name, Dir: "rescue/" + session + "/" + name,
		CreatedAt: rescueAt, ModifiedAt: rescueAt, Finished: true, Bytes: 4096}
	m := &protocol.RescueManifest{Version: 1, Session: session, Stamp: name, Dir: e.Dir, CreatedAt: rescueAt, Complete: complete}
	for repo, refs := range repos {
		r := protocol.ManifestRepo{Path: "/home/dev/repos/" + repo, Bundle: protocol.RepoBundle{File: e.Dir + "/" + repo + ".bundle", Size: 2048}}
		for _, ref := range refs {
			r.Bundle.Refs = append(r.Bundle.Refs, protocol.BundleRef{Name: ref, Source: ref, Commit: "abc"})
		}
		m.Repos = append(m.Repos, r)
	}
	e.Manifest = m
	return e
}

func TestListRescues(t *testing.T) {
	f := newFixture(t)
	f.sessionPod("haynes-ops-1006-100000", "full", 0)
	unfinished := protocol.RescueEntry{ID: "gone-1001-000000/20261001-1200", Session: "gone-1001-000000", Name: "20261001-1200",
		CreatedAt: rescueAt, ModifiedAt: rescueAt.Add(time.Hour)}
	sh := &fakeShelf{list: protocol.RescueList{
		Rescues: []protocol.RescueEntry{
			rescueEntry("haynes-ops-1006-100000", "20261006-1000", true, map[string][]string{"haynes-ops": {"refs/heads/agent/haynes-ops-1006-100000"}}),
			unfinished,
		},
		Unrecognized: []string{"rescue/notes.txt"},
	}}
	f.srv.Shelf = sh

	l := decode[apiv1.RescueList](t, f.do(http.MethodGet, apiv1.RescuesPath, tokHuman, nil))
	if len(l.Rescues) != 2 || len(l.Unrecognized) != 1 {
		t.Fatalf("list %+v", l)
	}
	kept, gone := l.Rescues[0], l.Rescues[1]
	if !kept.SessionExists || kept.PruneAfter != nil || !kept.Complete || len(kept.Repos) != 1 || kept.Repos[0].Repo != "haynes-ops" ||
		kept.Repos[0].Refs[0] != "refs/heads/agent/haynes-ops-1006-100000" {
		t.Errorf("a kept rescue: %+v", kept)
	}
	// The pruner counts from the later of the manifest and the last change,
	// with D-09's 30 days when the templates do not say.
	if gone.SessionExists || gone.Complete || gone.Finished || gone.PruneAfter == nil || !gone.PruneAfter.Equal(rescueAt.Add(time.Hour+720*time.Hour)) {
		t.Errorf("a rescue of a gone session: %+v", gone)
	}

	decode[apiv1.RescueList](t, f.do(http.MethodGet, apiv1.RescuesPath+"?session=gone-1001-000000", tokHuman, nil))
	if got := sh.asked[len(sh.asked)-1]; got != "gone-1001-000000" {
		t.Errorf("the shelf was asked for %q", got)
	}
	wantError(t, f.do(http.MethodGet, apiv1.RescuesPath+"?session=../x", tokHuman, nil), http.StatusUnprocessableEntity, apiv1.CodeInvalid)

	sh.err = shelf.ErrNoShelf
	wantError(t, f.do(http.MethodGet, apiv1.RescuesPath, tokHuman, nil), http.StatusServiceUnavailable, apiv1.CodeUnavailable)
	f.srv.Shelf = nil
	wantError(t, f.do(http.MethodGet, apiv1.RescuesPath, tokHuman, nil), http.StatusServiceUnavailable, apiv1.CodeUnavailable)
}

func TestCreateRestore(t *testing.T) {
	const old = "haynes-ops-1001-090000"
	f := newFixture(t)
	sh := &fakeShelf{list: protocol.RescueList{Rescues: []protocol.RescueEntry{
		rescueEntry(old, "20261001-1200", true, map[string][]string{
			"haynes-ops": {"refs/heads/agent/" + old, "refs/heads/rescue/" + old + "-20261001-1200"},
			"other":      {"refs/heads/main"},
		}),
		rescueEntry(old, "20261001-1100", false, map[string][]string{"haynes-ops": {"refs/heads/agent/" + old}}),
	}}}
	f.srv.Shelf = sh

	req := task()
	req.Restore = old + "/20261001-1200"
	w := f.do(http.MethodPost, apiv1.SessionsPath, tokHuman, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("restore: %d %s", w.Code, w.Body.String())
	}
	got := decode[apiv1.Session](t, w)
	s := f.session(got.Name)
	// The old session's branch is the base; its rescue branch never is.
	if s.Spec.Restore != req.Restore || s.Spec.Base != "refs/rescued/heads/agent/"+old || got.Restore != req.Restore {
		t.Errorf("spec restore %q base %q, view restore %q", s.Spec.Restore, s.Spec.Base, got.Restore)
	}
	// The check is a hold, which keeps the rescue from the next prune.
	if len(sh.held) != 1 || sh.held[0] != req.Restore {
		t.Errorf("held %q", sh.held)
	}

	req.Base = "origin/feature"
	req.IdempotencyKey = "with-a-base"
	w = f.do(http.MethodPost, apiv1.SessionsPath, tokHuman, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("restore with a base: %d %s", w.Code, w.Body.String())
	}
	if s := f.session(decode[apiv1.Session](t, w).Name); s.Spec.Base != "origin/feature" {
		t.Errorf("the asked-for base became %q", s.Spec.Base)
	}

	for name, c := range map[string]struct {
		mutate func(*apiv1.CreateSessionRequest)
		want   string
	}{
		"unknown":    {func(r *apiv1.CreateSessionRequest) { r.Restore = old + "/20261001-0900" }, "no rescue"},
		"incomplete": {func(r *apiv1.CreateSessionRequest) { r.Restore = old + "/20261001-1100" }, "not complete"},
		"other repo": {func(r *apiv1.CreateSessionRequest) { r.Repo = "haynesnetwork" }, "holds no bundle for haynesnetwork; it holds: haynes-ops, other"},
		"bad id":     {func(r *apiv1.CreateSessionRequest) { r.Restore = "../etc" }, "not <session>/<stamp>"},
	} {
		t.Run(name, func(t *testing.T) {
			r := task()
			r.Restore = old + "/20261001-1200"
			c.mutate(&r)
			e := wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, tokHuman, r), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
			if len(e.Fields) != 1 || e.Fields[0].Field != "restore" || !strings.Contains(e.Fields[0].Message, c.want) {
				t.Errorf("fields %+v, want restore: %q", e.Fields, c.want)
			}
		})
	}

	// A rescued snapshot is never a base, asked for or not.
	for _, base := range []string{"refs/rescued/heads/rescue/" + old + "-20261001-1200", "rescued/heads/rescue/x", "refs/rescued/agentd-rescue/stash/0"} {
		r := task()
		r.Restore, r.Base = old+"/20261001-1200", base
		e := wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, tokHuman, r), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
		if len(e.Fields) != 1 || e.Fields[0].Field != "base" {
			t.Errorf("base %q: fields %+v", base, e.Fields)
		}
	}

	f.srv.Shelf = &fakeShelf{err: shelf.ErrNoShelf}
	r := task()
	r.Restore = old + "/20261001-1200"
	wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, tokHuman, r), http.StatusServiceUnavailable, apiv1.CodeUnavailable)
}
