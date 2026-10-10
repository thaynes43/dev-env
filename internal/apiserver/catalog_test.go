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
	"github.com/thaynes43/dev-env/internal/controller"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
	"github.com/thaynes43/dev-env/internal/templates"
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
	f.srv.PrivateProjectTasks = true
	f.srv.ManagedCodexTasks = true
}

func coordinatorTask() apiv1.CreateSessionRequest {
	r := task()
	r.Project, r.Repo, r.Base, r.Profile, r.Size = "sample", "alias", "", "dev", "S"
	return r
}

func TestProjectCodexModelEffortAdmission(t *testing.T) {
	for _, tc := range []struct {
		model, effort string
		accepted      bool
	}{
		{"gpt-6.1-sol", "ultra", true}, {"gpt-6-luna", "max", true},
		{"gpt-6-luna", "ultra", false}, {"gpt-5.6-luna", "ultra", false},
		{"gpt-5.5", "max", false}, {"gpt-5.5", "ultra", false},
		{"gpt-5.5", "", true},
	} {
		t.Run(tc.model+"/"+tc.effort, func(t *testing.T) {
			f := coordinatorFixture(t)
			bindCatalogFixture(t, f)
			r := coordinatorTask()
			r.Agent, r.Model, r.Effort = "codex", tc.model, tc.effort
			r.Limits = &apiv1.Limits{Timeout: "1m"}
			w := f.do(http.MethodPost, apiv1.SessionsPath, tokCoordinator, r)
			if tc.accepted {
				if w.Code != http.StatusCreated {
					t.Fatal("pinned model/effort refused", w.Code, w.Body.String())
				}
				return
			}
			e := wantError(t, w, http.StatusUnprocessableEntity, apiv1.CodeInvalid)
			if len(e.Fields) != 1 || e.Fields[0].Field != "effort" {
				t.Fatal("unsupported effort did not refuse before Session admission", e)
			}
			var sessions v1alpha1.AgentSessionList
			if err := f.c.List(context.Background(), &sessions); err != nil || len(sessions.Items) != 0 {
				t.Fatal("unsupported model/effort wrote a Session", err)
			}
		})
	}
}

func TestPrivateProjectTaskAdmissionDisabledDefault(t *testing.T) {
	for _, token := range []string{tokHuman, tokClient, tokCoordinator} {
		for _, provider := range []string{"claude", "codex"} {
			t.Run(token+"/"+provider, func(t *testing.T) {
				f := coordinatorFixture(t)
				if f.srv.PrivateProjectTasks {
					t.Fatal("private project task admission was enabled by default")
				}
				bindCatalogFixture(t, f)
				f.srv.PrivateProjectTasks = false
				r := coordinatorTask()
				r.Agent = provider
				if provider == "codex" {
					r.Model = "gpt-6.1-sol"
				}
				e := wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, token, r), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
				if len(e.Fields) != 1 || e.Fields[0].Field != "project" || !strings.Contains(e.Message, "private project tasks are not enabled") {
					t.Fatal("disabled private project gate did not refuse explicitly", e)
				}
				var sessions v1alpha1.AgentSessionList
				if err := f.c.List(context.Background(), &sessions); err != nil || len(sessions.Items) != 0 {
					t.Fatal("disabled private project task admission wrote a session", err)
				}
			})
		}
	}
	f := newFixture(t)
	if w := f.do(http.MethodPost, apiv1.SessionsPath, tokHuman, task()); w.Code != http.StatusCreated {
		t.Fatal("private project gate changed legacy task admission", w.Code, w.Body.String())
	}
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
		if s.Spec.Parent != coordinatorSA || s.Spec.Repo != "alias" || s.Spec.Base != "stable" || s.Spec.Profile != "dev" || s.Spec.Workspace != nil {
			t.Fatal("caller overrode child identity or declared source")
		}
		snapshot, err := projectcatalog.ParseSnapshot([]byte(s.Annotations[v1alpha1.AnnotationProjectSnapshot]))
		if err != nil || snapshot.Rules() != "EXACT_ACCEPTED_RULES" || snapshot.Selected().GitHub != "thaynes43/actual-source" {
			t.Fatal("server snapshot lost accepted catalog authority")
		}
		// The fake client does not assign an API UID; model that assignment only
		// for the controller's actual runtime-document check.
		s.UID = "allocated-api-session-uid"
		if err := controller.CheckAgentdSession(s); err != nil {
			t.Fatal("admitted private project task did not bind the actual Session UID", err)
		}
	}
}

func TestProjectAdmissionIndependentOfSharedWorkspace(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		for _, kind := range []string{"missing", "disabled", "claim", "id", "enabled"} {
			for _, token := range []string{tokHuman, tokClient, tokCoordinator} {
				t.Run(provider+"/"+kind+"/"+token, func(t *testing.T) {
					f := coordinatorFixture(t)
					bindCatalogFixture(t, f)
					f.tmpl.Workspace = &templates.Workspace{Enabled: true, Claim: "accepted-projects", ID: "projects-v2"}
					switch kind {
					case "missing":
						f.tmpl.Workspace = nil
					case "disabled":
						f.tmpl.Workspace.Enabled = false
					case "claim":
						f.tmpl.Workspace.Claim = ""
					case "id":
						f.tmpl.Workspace.ID = ""
					}
					r := coordinatorTask()
					r.Agent = provider
					if provider == "codex" {
						r.Model = "gpt-6.1-sol"
					}
					w := f.do(http.MethodPost, apiv1.SessionsPath, token, r)
					if w.Code != http.StatusCreated {
						t.Fatal("private project admission depended on shared workspace", w.Code, w.Body.String())
					}
					s := f.session(decode[apiv1.Session](t, w).Name)
					if s.Spec.Workspace != nil || s.Annotations[v1alpha1.AnnotationProjectSnapshot] == "" {
						t.Fatal("project admission enabled shared storage or lost the accepted snapshot")
					}
				})
			}
		}
	}
}

func TestProjectAdmissionPreservesCoordinatorTemplateRequirement(t *testing.T) {
	for _, kind := range []string{"missing", "unavailable"} {
		for _, token := range []string{tokHuman, tokClient, tokCoordinator} {
			t.Run(kind+"/"+token, func(t *testing.T) {
				f := coordinatorFixture(t)
				bindCatalogFixture(t, f)
				if kind == "missing" {
					f.srv.Templates = nil
				} else {
					f.tmplOK = false
				}
				w := f.do(http.MethodPost, apiv1.SessionsPath, token, coordinatorTask())
				if token == tokCoordinator {
					wantError(t, w, http.StatusForbidden, apiv1.CodeForbidden)
					return
				}
				if w.Code != http.StatusCreated {
					t.Fatal("trusted private project admission depended on templates", w.Code, w.Body.String())
				}
				if f.session(decode[apiv1.Session](t, w).Name).Spec.Workspace != nil {
					t.Fatal("private admission enabled shared storage")
				}
			})
		}
	}
}

func TestPrivateProjectAdmissionPreservesDeclaredSourceRestrictions(t *testing.T) {
	for _, token := range []string{tokHuman, tokClient} {
		for _, kind := range []string{"default-base", "foreign-base", "actual-name", "github-identity", "ambiguous-repo"} {
			t.Run(token+"/"+kind, func(t *testing.T) {
				f := newFixture(t)
				bindCatalogFixture(t, f)
				r := coordinatorTask()
				switch kind {
				case "default-base":
					r.Base = "stable"
				case "foreign-base":
					r.Base = "foreign"
				case "actual-name":
					r.Repo = "actual-source"
				case "github-identity":
					r.Repo = "thaynes43/actual-source"
				case "ambiguous-repo":
					r.Repo = ""
				}
				w := f.do(http.MethodPost, apiv1.SessionsPath, token, r)
				if kind == "default-base" {
					if w.Code != http.StatusCreated {
						t.Fatal("declared default base was refused", w.Code, w.Body.String())
					}
					return
				}
				if kind == "foreign-base" {
					wantError(t, w, http.StatusForbidden, apiv1.CodeForbidden)
				} else {
					wantError(t, w, http.StatusUnprocessableEntity, apiv1.CodeInvalid)
				}
				var sessions v1alpha1.AgentSessionList
				if err := f.c.List(context.Background(), &sessions); err != nil || len(sessions.Items) != 0 {
					t.Fatal("source override created a private project session", err)
				}
			})
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
