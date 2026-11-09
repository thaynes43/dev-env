package apiserver

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/templates"
)

// cursorEnabledFixture is a fixture whose templates name the Cursor key
// (DESIGN-058) and whose catalog declares a project.
func cursorEnabledFixture(t *testing.T) *fixture {
	t.Helper()
	f := coordinatorFixture(t)
	bindCatalogFixture(t, f)
	f.tmpl.Cursor = &templates.Cursor{APIKey: templates.SecretKey{Name: "dev-env-cursor", Key: "CURSOR_MEATBALL_KEY"}}
	return f
}

func cursorTask() apiv1.CreateSessionRequest {
	r := coordinatorTask()
	r.Agent, r.Model, r.Effort = "cursor", "grok-code-fast-1", ""
	r.Limits = &apiv1.Limits{Timeout: "30m"}
	return r
}

func noSessions(t *testing.T, f *fixture) {
	t.Helper()
	var sessions v1alpha1.AgentSessionList
	if err := f.c.List(context.Background(), &sessions); err != nil || len(sessions.Items) != 0 {
		t.Fatal("a refused cursor create wrote a Session", err, len(sessions.Items))
	}
}

func TestCursorTaskCreate(t *testing.T) {
	for _, token := range []string{tokHuman, tokClient, tokCoordinator} {
		t.Run(token, func(t *testing.T) {
			f := cursorEnabledFixture(t)
			w := f.do(http.MethodPost, apiv1.SessionsPath, token, cursorTask())
			if w.Code != http.StatusCreated {
				t.Fatal("cursor task refused", w.Code, w.Body.String())
			}
			s := f.session(decode[apiv1.Session](t, w).Name)
			if s.Spec.Agent != v1alpha1.AgentCursor || s.Spec.Model != "grok-code-fast-1" || s.Spec.Effort != "" || s.Spec.Mode != v1alpha1.ModeTask ||
				s.Spec.Workspace != nil || s.Spec.CommonRules != nil || s.Annotations[v1alpha1.AnnotationProjectSnapshot] == "" {
				t.Fatalf("spec %+v", s.Spec)
			}
		})
	}
}

// Without the templates' cursor.apiKey reference, a cursor create is refused
// with its own code before any Session or pod exists.
func TestCursorTaskRefusedWhenTemplatesLackTheKey(t *testing.T) {
	for _, kind := range []string{"absent", "no-templates", "unreadable"} {
		t.Run(kind, func(t *testing.T) {
			f := cursorEnabledFixture(t)
			switch kind {
			case "absent":
				f.tmpl.Cursor = nil
			case "no-templates":
				f.srv.Templates = nil
			case "unreadable":
				f.tmplOK = false
			}
			w := f.do(http.MethodPost, apiv1.SessionsPath, tokHuman, cursorTask())
			if kind == "unreadable" {
				wantError(t, w, http.StatusServiceUnavailable, apiv1.CodeUnavailable)
			} else {
				e := wantError(t, w, http.StatusUnprocessableEntity, apiv1.CodeProviderUnconfigured)
				if !strings.Contains(e.Message, "cursor sessions are not enabled") {
					t.Fatal("message", e.Message)
				}
			}
			noSessions(t, f)
		})
	}
}

func TestCursorTaskFieldRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(*apiv1.CreateSessionRequest)
		field  string
		want   string
	}{
		"effort":     {func(r *apiv1.CreateSessionRequest) { r.Effort = "high" }, "effort", "Cursor encodes effort in the model id"},
		"local":      {func(r *apiv1.CreateSessionRequest) { r.Mode, r.Prompt, r.Limits = "local", "", nil }, "mode", "task mode only"},
		"no project": {func(r *apiv1.CreateSessionRequest) { r.Project, r.Repo = "", "haynes-ops" }, "project", "accepted project snapshot"},
		"max turns":  {func(r *apiv1.CreateSessionRequest) { r.Limits.MaxTurns = 5 }, "limits.maxTurns", "no max-turns flag"},
		"model":      {func(r *apiv1.CreateSessionRequest) { r.Model = "--yolo" }, "model", "not a Cursor model id"},
	} {
		t.Run(name, func(t *testing.T) {
			f := cursorEnabledFixture(t)
			r := cursorTask()
			tc.mutate(&r)
			e := wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, tokHuman, r), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
			found := false
			for _, fe := range e.Fields {
				found = found || fe.Field == tc.field && strings.Contains(fe.Message, tc.want)
			}
			if !found {
				t.Fatalf("fields %+v, want %s: %s", e.Fields, tc.field, tc.want)
			}
			noSessions(t, f)
		})
	}
}
