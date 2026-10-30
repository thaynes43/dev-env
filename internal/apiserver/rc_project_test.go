package apiserver

import (
	"context"
	"net/http"
	"testing"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
)

// Pin the existing general-create route, without adding a launch privilege.
func TestRCClaudeProjectRemoteCreateSnapshotAndCoordinatorRefusal(t *testing.T) {
	for _, token := range []string{tokHuman, "session", tokCoordinator} {
		t.Run(token, func(t *testing.T) {
			f := coordinatorFixture(t)
			bindCatalogFixture(t, f)
			if token == "session" {
				token = f.sessionPod("parent", "dev", 0)
			}
			req := coordinatorTask()
			req.Mode, req.Prompt, req.Limits = "remote", "", nil
			w := f.do(http.MethodPost, apiv1.SessionsPath, token, req)
			if token == tokCoordinator {
				wantError(t, w, http.StatusForbidden, apiv1.CodeForbidden)
				var list v1alpha1.AgentSessionList
				if err := f.c.List(context.Background(), &list); err != nil || len(list.Items) != 0 {
					t.Fatal("coordinator remote create wrote a Session", err)
				}
				return
			}
			if w.Code != http.StatusCreated {
				t.Fatal(w.Code, w.Body.String())
			}
			view := decode[apiv1.Session](t, w)
			stored := f.session(view.Name)
			snapshot, err := projectcatalog.ParseSnapshot([]byte(stored.Annotations[v1alpha1.AnnotationProjectSnapshot]))
			if err != nil || snapshot.Rules() != "EXACT_ACCEPTED_RULES" || snapshot.Selected().Name != "alias" || snapshot.Selected().DefaultBranch != "stable" || view.Repo != "alias" || view.Base != "stable" || stored.Spec.Mode != v1alpha1.ModeRemote || stored.Spec.Workspace != nil {
				t.Fatal("Claude remote create lost accepted project source/rules", err)
			}
		})
	}
}
