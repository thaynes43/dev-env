package apiserver

import (
	"context"
	"net/http"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
)

const apiProjectCatalog = `{"version":1,"repositories":{"alias":{"github":"thaynes43/actual-source","defaultBranch":"stable"},"second":{"github":"thaynes43/second"}},"projects":{"sample":{"repositories":[{"name":"alias"},{"name":"second"}],"rules":"EXACT_ACCEPTED_RULES"}}}`

func bindCatalogFixture(t *testing.T, f *fixture) {
	t.Helper()
	key := types.NamespacedName{Namespace: "dev-env-system", Name: "dev-env-project-catalog"}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name}, Data: map[string]string{"catalog.json": apiProjectCatalog}}
	if err := f.c.Create(context.Background(), cm); err != nil {
		t.Fatal(err)
	}
	f.srv.Projects = &CatalogBinding{Key: key, CloneOwner: "thaynes43"}
	f.srv.ManagedCodexTasks = true
}

func coordinatorTask() apiv1.CreateSessionRequest {
	r := task()
	r.Project, r.Repo, r.Base, r.Profile, r.Size = "sample", "alias", "", "dev", "S"
	return r
}

func TestCoordinatorProjectAdmissionDerivesDeclaredSourceAndRules(t *testing.T) {
	f := coordinatorFixture(t)
	bindCatalogFixture(t, f)
	for _, provider := range []string{"claude", "codex"} {
		r := coordinatorTask()
		r.Agent = provider
		if provider == "codex" {
			r.Model, r.Limits = "gpt-6.1-sol", &apiv1.Limits{Timeout: "1m"}
		}
		w := f.do(http.MethodPost, apiv1.SessionsPath, tokCoordinator, r)
		if w.Code != http.StatusCreated {
			t.Fatal("accepted project task refused", w.Code, w.Body.String())
		}
		s := f.session(decode[apiv1.Session](t, w).Name)
		if s.Spec.Parent != coordinatorSA || s.Spec.Repo != "alias" || s.Spec.Base != "stable" || s.Spec.Profile != "dev" {
			t.Fatal("caller overrode child identity or declared source")
		}
		snapshot, err := projectcatalog.ParseSnapshot([]byte(s.Annotations[v1alpha1.AnnotationProjectSnapshot]))
		if err != nil || snapshot.Rules() != "EXACT_ACCEPTED_RULES" || snapshot.Selected().GitHub != "thaynes43/actual-source" {
			t.Fatal("server snapshot lost accepted catalog authority")
		}
	}
}

func TestCoordinatorProjectAdmissionRefusesWideningAndUnavailableAuthority(t *testing.T) {
	for _, kind := range []string{"profile-empty", "ops", "full", "size-empty", "large", "remote", "local", "restore", "base", "name", "lane", "project", "repo", "provider", "catalog-missing", "catalog-invalid", "clone-owner", "no-live-reader", "managed-off", "codex-max-turns"} {
		t.Run(kind, func(t *testing.T) {
			f := coordinatorFixture(t)
			bindCatalogFixture(t, f)
			r := coordinatorTask()
			switch kind {
			case "profile-empty":
				r.Profile = ""
			case "ops", "full":
				r.Profile = kind
			case "size-empty":
				r.Size = ""
			case "large":
				r.Size = "L"
			case "remote", "local":
				r.Mode = kind
			case "restore":
				r.Restore = "task/rescue"
			case "base":
				r.Base = "stable"
			case "name":
				r.Name = "chosen"
			case "lane":
				r.Lane = "operations"
			case "project":
				r.Project = "unrelated"
			case "repo":
				r.Repo = "unrelated"
			case "provider":
				r.Agent = "opencode"
			case "catalog-missing":
				f.srv.Projects = nil
			case "catalog-invalid":
				var cm corev1.ConfigMap
				if err := f.c.Get(context.Background(), f.srv.Projects.Key, &cm); err != nil {
					t.Fatal(err)
				}
				cm.Data["catalog.json"] = `{"version":1,"clientClaim":true}`
				if err := f.c.Update(context.Background(), &cm); err != nil {
					t.Fatal(err)
				}
			case "clone-owner":
				f.srv.Projects.CloneOwner = "foreign"
			case "no-live-reader":
				f.srv.Live = nil
			case "managed-off", "codex-max-turns":
				r.Agent, r.Model = "codex", "gpt-6.1-sol"
				if kind == "managed-off" {
					f.srv.ManagedCodexTasks = false
				} else {
					r.Limits = &apiv1.Limits{MaxTurns: 1}
				}
			}
			w := f.do(http.MethodPost, apiv1.SessionsPath, tokCoordinator, r)
			if w.Code >= 200 && w.Code < 300 {
				t.Fatal("invalid coordinator admission created a task")
			}
			if strings.Contains(w.Body.String(), "EXACT_ACCEPTED_RULES") || strings.Contains(w.Body.String(), "actual-source") {
				t.Fatal("refusal leaked catalog detail")
			}
		})
	}
	for _, claim := range []string{"projectSnapshot", "rules", "catalogRevision", "annotations", "parent"} {
		f := coordinatorFixture(t)
		bindCatalogFixture(t, f)
		body := map[string]any{"project": "sample", "repo": "alias", "profile": "dev", "size": "S", "mode": "task", "agent": "claude", "model": "claude-opus-5-5", "prompt": "synthetic", claim: "client-authority"}
		wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, tokCoordinator, body), http.StatusBadRequest, apiv1.CodeBadRequest)
	}
}
