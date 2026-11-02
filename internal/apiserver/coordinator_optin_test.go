package apiserver

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/projectcanary"
	"github.com/thaynes43/dev-env/internal/taskbudget"
)

func bindInstructionCatalogFixture(t *testing.T, f *fixture) {
	bindCatalogFixture(t, f)
	f.srv.CoordinatorInstructionAgentImage = "ghcr.io/thaynes43/dev-env:2.86.0@sha256:" + strings.Repeat("a", 64)
}

func optInRequest() apiv1.CreateSessionRequest {
	req := coordinatorTask()
	req.CoordinatorInstructions = true
	return req
}

func TestCoordinatorInstructionOptInBindsLiveParentAndStaysDefaultOff(t *testing.T) {
	f := newFixture(t)
	bindInstructionCatalogFixture(t, f)
	token := f.sessionPod("cap-parent", "dev", 0)
	parent := f.session("cap-parent")
	if parent.Spec.CoordinatorInstructions || parent.Spec.CoordinatorInstructionParentUID != "" {
		t.Fatal("sender parent was enrolled")
	}
	w := f.do(http.MethodPost, apiv1.SessionsPath, token, optInRequest())
	if w.Code != http.StatusCreated {
		t.Fatalf("opt-in create %d %s", w.Code, w.Body.String())
	}
	child := f.session(decode[apiv1.Session](t, w).Name)
	if !child.Spec.CoordinatorInstructions || child.Spec.CoordinatorInstructionParentUID != string(parent.UID) || child.Spec.Parent != "cap-parent" || child.Spec.Profile != "dev" || child.Spec.Workspace != nil || child.Spec.ProjectCanary != nil || child.Spec.PrivatePilot != nil || child.Annotations[taskbudget.UIDAnnotation] != "" {
		t.Fatalf("opt-in child %+v annotations %v", child.Spec, child.Annotations)
	}
	if f.session("cap-parent").Spec.CoordinatorInstructions || f.session("cap-parent").Spec.CoordinatorInstructionParentUID != "" {
		t.Fatal("creating a receiver enrolled the sender")
	}
	plain := f.do(http.MethodPost, apiv1.SessionsPath, token, coordinatorTask())
	if plain.Code != http.StatusCreated {
		t.Fatalf("default create %d %s", plain.Code, plain.Body.String())
	}
	off := f.session(decode[apiv1.Session](t, plain).Name)
	if off.Spec.CoordinatorInstructions || off.Spec.CoordinatorInstructionParentUID != string(parent.UID) || off.Spec.Parent != "cap-parent" {
		t.Fatalf("default path %+v", off.Spec)
	}
}

func TestCoordinatorInstructionOptInRejectsForgedParentAndOutOfScope(t *testing.T) {
	t.Run("forged parent field", func(t *testing.T) {
		f := newFixture(t)
		bindInstructionCatalogFixture(t, f)
		token := f.sessionPod("cap-parent", "dev", 0)
		body := `{"project":"sample","repo":"alias","agent":"claude","mode":"task","model":"claude-opus-5-5","effort":"xhigh","prompt":"fix the docs","profile":"dev","size":"S","coordinatorInstructions":true,"coordinatorInstructionParentUID":"forged"}`
		wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, token, body), http.StatusBadRequest, apiv1.CodeBadRequest)
		body = `{"project":"sample","repo":"alias","agent":"claude","mode":"task","model":"claude-opus-5-5","effort":"xhigh","prompt":"fix the docs","profile":"dev","size":"S","coordinatorInstructions":true,"parent":"other-session"}`
		wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, token, body), http.StatusBadRequest, apiv1.CodeBadRequest)
		assertOnlyParent(t, f, "cap-parent")
	})
	t.Run("sibling cannot impersonate", func(t *testing.T) {
		f := newFixture(t)
		bindInstructionCatalogFixture(t, f)
		_ = f.sessionPod("cap-parent", "dev", 0)
		sibling := f.sessionPod("sibling", "dev", 0)
		w := f.do(http.MethodPost, apiv1.SessionsPath, sibling, optInRequest())
		if w.Code != http.StatusCreated {
			t.Fatalf("sibling create %d %s", w.Code, w.Body.String())
		}
		child := f.session(decode[apiv1.Session](t, w).Name)
		if child.Spec.Parent != "sibling" || child.Spec.CoordinatorInstructionParentUID != string(f.session("sibling").UID) || child.Spec.CoordinatorInstructionParentUID == string(f.session("cap-parent").UID) {
			t.Fatalf("sibling parent binding %+v", child.Spec)
		}
	})
	for _, tc := range []struct {
		name, token, message string
		mutate               func(*testing.T, *fixture)
		edit                 func(*apiv1.CreateSessionRequest)
	}{
		{name: "human", token: tokHuman, message: "live authenticated parent"},
		{name: "client", token: tokClient, message: "live authenticated parent"},
		{name: "coordinator host", token: tokCoordinator, message: "live authenticated parent"},
		{name: "shared workspace", token: "parent", message: "shared workspace", mutate: func(t *testing.T, f *fixture) {
			parent := f.session("cap-parent")
			parent.Spec.Workspace = &v1alpha1.WorkspaceSpec{ID: "projects-v2"}
			if err := f.c.Update(context.Background(), parent); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "budget", token: "parent", message: "budget-bound", mutate: func(t *testing.T, f *fixture) {
			parent := f.session("cap-parent")
			if parent.Annotations == nil {
				parent.Annotations = map[string]string{}
			}
			parent.Annotations[taskbudget.UIDAnnotation] = "budget-uid"
			if err := f.c.Update(context.Background(), parent); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "remote", token: "parent", message: "unsupported mode", edit: func(r *apiv1.CreateSessionRequest) { r.Mode = "remote" }},
		{name: "local", token: "parent", message: "unsupported mode", edit: func(r *apiv1.CreateSessionRequest) { r.Mode = "local"; r.Prompt = "" }},
		{name: "missing project", token: "parent", message: "accepted project binding", edit: func(r *apiv1.CreateSessionRequest) { r.Project = "" }},
		{name: "private gate", token: "parent", message: "accepted project binding", mutate: func(_ *testing.T, f *fixture) { f.srv.PrivateProjectTasks = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			if tc.token == tokCoordinator {
				f = coordinatorFixture(t)
			}
			bindInstructionCatalogFixture(t, f)
			token := tc.token
			if token == "parent" || token == tokHuman || token == tokClient || token == tokCoordinator {
				if token == "parent" {
					token = f.sessionPod("cap-parent", "dev", 0)
				}
			}
			if tc.mutate != nil {
				tc.mutate(t, f)
			}
			req := optInRequest()
			if tc.edit != nil {
				tc.edit(&req)
			}
			refuseOptIn(t, f, token, req, tc.message)
		})
	}
	t.Run("canary and pilot parents stay refused", func(t *testing.T) {
		for _, kind := range []string{"canary", "pilot"} {
			t.Run(kind, func(t *testing.T) {
				f := newFixture(t)
				bindInstructionCatalogFixture(t, f)
				token := f.sessionPod("cap-parent", "dev", 0)
				parent := f.session("cap-parent")
				if kind == "canary" {
					parent.Spec.ProjectCanary = &v1alpha1.ProjectCanaryBinding{}
				} else {
					if parent.Annotations == nil {
						parent.Annotations = map[string]string{}
					}
					parent.Annotations[projectcanary.PilotAnnotation] = "marked"
				}
				if err := f.c.Update(context.Background(), parent); err != nil {
					t.Fatal(err)
				}
				before := sessionNames(t, f)
				wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, token, optInRequest()), http.StatusForbidden, apiv1.CodeForbidden)
				if after := sessionNames(t, f); len(after) != len(before) {
					t.Fatalf("canary or pilot parent created a session: %v", after)
				}
				fresh := f.session("cap-parent")
				_, err := f.srv.newSession(context.Background(), optInRequest(), &caller{kind: kindSession, session: fresh, parent: fresh.Name, profile: "dev", depth: 1})
				var got *apiError
				if !errors.As(err, &got) || len(got.body.Fields) != 1 || got.body.Fields[0].Field != "coordinatorInstructions" || !strings.Contains(got.body.Fields[0].Message, "canary or pilot") {
					t.Fatalf("direct admission %v", err)
				}
			})
		}
	})
	t.Run("ops profile", func(t *testing.T) {
		f := newFixture(t)
		bindInstructionCatalogFixture(t, f)
		token := f.sessionPod("cap-parent", "ops", 0)
		req := optInRequest()
		req.Profile = "ops"
		refuseOptIn(t, f, token, req, "unsupported scope")
	})
	t.Run("deleted parent", func(t *testing.T) {
		f := newFixture(t)
		bindInstructionCatalogFixture(t, f)
		token := f.sessionPod("cap-parent", "dev", 0)
		if err := f.c.Delete(context.Background(), f.session("cap-parent")); err != nil {
			t.Fatal(err)
		}
		refuseOptIn(t, f, token, optInRequest(), "live authenticated parent")
	})
}

func refuseOptIn(t *testing.T, f *fixture, token string, req apiv1.CreateSessionRequest, message string) {
	t.Helper()
	before := sessionNames(t, f)
	e := wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, token, req), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
	if len(e.Fields) != 1 || e.Fields[0].Field != "coordinatorInstructions" || !strings.Contains(e.Fields[0].Message, message) {
		t.Fatalf("error %+v", e)
	}
	if after := sessionNames(t, f); len(after) != len(before) {
		t.Fatalf("refusal created a session: before %v after %v", before, after)
	}
}

func assertOnlyParent(t *testing.T, f *fixture, name string) {
	t.Helper()
	got := sessionNames(t, f)
	if len(got) != 1 || got[0] != name {
		t.Fatalf("sessions %v", got)
	}
}

func sessionNames(t *testing.T, f *fixture) []string {
	t.Helper()
	var list v1alpha1.AgentSessionList
	if err := f.c.List(context.Background(), &list); err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(list.Items))
	for i := range list.Items {
		names = append(names, list.Items[i].Name)
	}
	return names
}

func TestCoordinatorInstructionOptInNeedsQualifiedImage(t *testing.T) {
	for _, image := range []string{"", "ghcr.io/thaynes43/dev-env:latest", "ghcr.io/foreign/dev-env:2.86.0@sha256:" + strings.Repeat("a", 64)} {
		t.Run(image, func(t *testing.T) {
			f := newFixture(t)
			bindInstructionCatalogFixture(t, f)
			token := f.sessionPod("cap-parent", "dev", 0)
			f.srv.CoordinatorInstructionAgentImage = image
			refuseOptIn(t, f, token, optInRequest(), "qualified coordinator instruction receiver image")
			// Plain catalog tasks keep their existing path while enrollment is off.
			if w := f.do(http.MethodPost, apiv1.SessionsPath, token, coordinatorTask()); w.Code != http.StatusCreated {
				t.Fatalf("plain create %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestCoordinatorInstructionImageRollbackPreservesAcceptedCreateRetry(t *testing.T) {
	f := newFixture(t)
	bindInstructionCatalogFixture(t, f)
	token := f.sessionPod("cap-parent", "dev", 0)
	req := optInRequest()
	req.IdempotencyKey = "instruction-receiver-once"
	first := f.do(http.MethodPost, apiv1.SessionsPath, token, req)
	if first.Code != http.StatusCreated {
		t.Fatalf("first create %d %s", first.Code, first.Body.String())
	}
	created := decode[apiv1.Session](t, first)
	before := sessionNames(t, f)
	f.srv.CoordinatorInstructionAgentImage = ""
	retry := f.do(http.MethodPost, apiv1.SessionsPath, token, req)
	if retry.Code != http.StatusOK || decode[apiv1.Session](t, retry).Name != created.Name {
		t.Fatalf("rollback lost accepted result: %d %s", retry.Code, retry.Body.String())
	}
	changed := req
	changed.Prompt += " different intent"
	wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, token, changed), http.StatusConflict, apiv1.CodeConflict)
	req.IdempotencyKey = "fresh-instruction-receiver"
	refuseOptIn(t, f, token, req, "qualified coordinator instruction receiver image")
	if after := sessionNames(t, f); len(after) != len(before) {
		t.Fatalf("rollback made another child: %v -> %v", before, after)
	}
}
