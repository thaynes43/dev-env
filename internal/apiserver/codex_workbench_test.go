package apiserver

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/codexworkbench"
	"github.com/thaynes43/dev-env/internal/templates"
)

func codexHostRequest() apiv1.CreateSessionRequest {
	return apiv1.CreateSessionRequest{Repo: "haynes-ops", Agent: "codex", Mode: "remote", Model: "gpt-6.1-sol"}
}
func hostCaller(t *testing.T, f *fixture, kind string) string {
	t.Helper()
	switch kind {
	case "human":
		return tokHuman
	case "client":
		return tokClient
	case "ordinary":
		return f.sessionPod("ordinary-parent", "dev", 0)
	case "workbench":
		f.srv.Policy.WorkbenchServiceAccount = templates.WorkbenchServiceAccount
		tok := f.sessionPod("workbench-parent", "workbench", 0)
		id := saIdentity(sessionNS + "/" + templates.WorkbenchServiceAccount)
		id.PodName, id.PodUID = f.auth[tok].PodName, f.auth[tok].PodUID
		f.auth[tok] = id
		var pod corev1.Pod
		if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: sessionNS, Name: id.PodName}, &pod); err != nil {
			t.Fatal(err)
		}
		pod.Spec.ServiceAccountName = templates.WorkbenchServiceAccount
		if err := f.c.Update(context.Background(), &pod); err != nil {
			t.Fatal(err)
		}
		return tok
	}
	t.Fatal(kind)
	return ""
}
func TestCodexWorkbenchAPICreateEveryCaller(t *testing.T) {
	for _, kind := range []string{"human", "client", "workbench", "ordinary"} {
		for _, profile := range []string{"workbench", "alias", "default"} {
			t.Run(kind+"/"+profile, func(t *testing.T) {
				f := newFixture(t)
				tok := hostCaller(t, f, kind)
				p := templates.Profile{ServiceAccount: templates.WorkbenchServiceAccount}
				f.tmpl.Profiles["workbench"] = p
				f.tmpl.Profiles["alias"] = p
				req := codexHostRequest()
				if profile == "default" {
					f.tmpl.DefaultProfile = "workbench"
				} else {
					req.Profile = profile
				}
				w := f.do(http.MethodPost, apiv1.SessionsPath, tok, req)
				if w.Code != http.StatusForbidden {
					t.Fatalf("status %d: %s", w.Code, w.Body.String())
				}
				e := unmarshal[apiv1.ErrorResponse](t, w.Body.Bytes()).Error
				if kind == "workbench" && profile != "default" {
					if e.Code != apiv1.CodeForbidden || e.Message != "a codex host started by a workbench session runs on the default profile; omit the profile" {
						t.Fatalf("T30 changed: %+v", e)
					}
				} else if e.Code != apiv1.CodeCodexWorkbenchHostGitOnly {
					t.Fatalf("git-only: %+v", e)
				}
				var list v1alpha1.AgentSessionList
				if err := f.c.List(context.Background(), &list); err != nil {
					t.Fatal(err)
				}
				for _, s := range list.Items {
					if s.Spec.Agent == v1alpha1.AgentCodex {
						t.Fatal("API wrote a refused host")
					}
				}
			})
		}
	}
}
func TestCodexWorkbenchAPITemplatesRefuse(t *testing.T) {
	for _, kind := range []string{"missing-reader", "read-error", "missing-profile", "nil-template"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			switch kind {
			case "missing-reader":
				f.srv.Templates = nil
			case "read-error":
				f.tmplOK = false
			case "missing-profile":
				f.tmpl.DefaultProfile = "absent"
			case "nil-template":
				f.srv.Templates = func(context.Context) (*templates.Templates, error) { return nil, nil }
			}
			w := f.do(http.MethodPost, apiv1.SessionsPath, tokHuman, codexHostRequest())
			if w.Code != http.StatusServiceUnavailable {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			e := unmarshal[apiv1.ErrorResponse](t, w.Body.Bytes()).Error
			if e.Reason != codexworkbench.TemplatesUnavailable {
				t.Fatalf("reason %+v", e)
			}
		})
	}
}
func TestCodexWorkbenchAPIFinalAdmission(t *testing.T) {
	f := newFixture(t)
	f.tmpl.Profiles["workbench"] = templates.Profile{ServiceAccount: templates.WorkbenchServiceAccount}
	sess := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: "named-host", Namespace: sessionNS, Labels: map[string]string{v1alpha1.LabelDepth: "0"}}, Spec: v1alpha1.AgentSessionSpec{Agent: v1alpha1.AgentCodex, Mode: v1alpha1.ModeRemote, Profile: "workbench", Parent: "flux-system/kustomize-controller"}}
	f.srv.CodexWorkbenchHosts = codexworkbench.Policy{Name: sess.Name, Namespace: sessionNS}
	if err := f.srv.admitCreate(context.Background(), &caller{kind: kindHuman}, sess); err == nil || !strings.Contains(err.Error(), apiv1.CodeCodexWorkbenchHostGitOnly) {
		t.Fatalf("final admission bypassed: %v", err)
	}
	// Even a generated name may not occupy the reserved name on another account.
	f.srv.CodexWorkbenchHosts.Name = generatedName("haynes-ops", f.now)
	req := codexHostRequest()
	req.Agent = "claude"
	req.Model = "claude-opus-5-5"
	w := f.do(http.MethodPost, apiv1.SessionsPath, tokHuman, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("reserved generated name: %d %s", w.Code, w.Body.String())
	}
}
func TestCodexWorkbenchAPIResumeBeforeWrites(t *testing.T) {
	for _, kind := range []string{"human", "client", "workbench", "ordinary"} {
		t.Run(kind, func(t *testing.T) {
			patches := 0
			f := newFixtureWith(t, interceptor.Funcs{Patch: func(ctx context.Context, c client.WithWatch, o client.Object, p client.Patch, opts ...client.PatchOption) error {
				patches++
				return c.Patch(ctx, o, p, opts...)
			}})
			tok := hostCaller(t, f, kind)
			f.tmpl.Profiles["workbench"] = templates.Profile{ServiceAccount: templates.WorkbenchServiceAccount}
			sess := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: "legacy-host", Namespace: sessionNS, UID: "legacy-uid", Annotations: map[string]string{"keep": "yes"}}, Spec: v1alpha1.AgentSessionSpec{Agent: v1alpha1.AgentCodex, Mode: v1alpha1.ModeRemote, Profile: "workbench", Parent: clientSA, OperatingMode: v1alpha1.OperatingModeSuspended}}
			if err := f.c.Create(context.Background(), sess); err != nil {
				t.Fatal(err)
			}
			before := sess.DeepCopy()
			w := f.do(http.MethodPost, apiv1.SessionPath(sess.Name)+"/resume", tok, nil)
			if w.Code != http.StatusForbidden {
				t.Fatalf("resume %d %s", w.Code, w.Body.String())
			}
			if e := unmarshal[apiv1.ErrorResponse](t, w.Body.Bytes()).Error; e.Reason != codexworkbench.NotNamed {
				t.Fatalf("reason %+v", e)
			}
			var got v1alpha1.AgentSession
			if err := f.c.Get(context.Background(), client.ObjectKeyFromObject(sess), &got); err != nil {
				t.Fatal(err)
			}
			if patches != 0 || !reflect.DeepEqual(before.Spec, got.Spec) || !reflect.DeepEqual(before.Annotations, got.Annotations) {
				t.Fatal("resume wrote before refusal")
			}
		})
	}
}
func TestCodexWorkbenchAPIResolvesOnce(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		f := newFixture(t)
		reads := 0
		ordinary := f.tmpl
		changed := *f.tmpl
		changed.Profiles = map[string]templates.Profile{"workbench": {ServiceAccount: templates.WorkbenchServiceAccount}}
		changed.DefaultProfile = "workbench"
		f.srv.Templates = func(context.Context) (*templates.Templates, error) {
			reads++
			if reads == 1 {
				return ordinary, nil
			}
			return &changed, nil
		}
		w := f.do(http.MethodPost, apiv1.SessionsPath, tokHuman, codexHostRequest())
		if w.Code != http.StatusCreated || reads != 1 {
			t.Fatalf("status %d, reads %d: %s", w.Code, reads, w.Body.String())
		}
	})
	t.Run("resume-retry", func(t *testing.T) {
		patches := 0
		f := newFixtureWith(t, interceptor.Funcs{Patch: func(ctx context.Context, c client.WithWatch, o client.Object, p client.Patch, opts ...client.PatchOption) error {
			patches++
			if patches == 1 {
				return apierrors.NewConflict(schema.GroupResource{Resource: "agentsessions"}, o.GetName(), errors.New("retry"))
			}
			return c.Patch(ctx, o, p, opts...)
		}})
		sess := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: "named-host", Namespace: sessionNS, UID: types.UID("uid"), Labels: map[string]string{v1alpha1.LabelDepth: "0"}}, Spec: v1alpha1.AgentSessionSpec{Agent: v1alpha1.AgentCodex, Mode: v1alpha1.ModeRemote, Profile: "workbench", Parent: "flux-system/kustomize-controller", OperatingMode: v1alpha1.OperatingModeSuspended}}
		if err := f.c.Create(context.Background(), sess); err != nil {
			t.Fatal(err)
		}
		f.srv.CodexWorkbenchHosts = codexworkbench.Policy{Name: sess.Name, Namespace: sessionNS}
		f.tmpl.Profiles["workbench"] = templates.Profile{ServiceAccount: templates.WorkbenchServiceAccount}
		reads := 0
		f.srv.Templates = func(context.Context) (*templates.Templates, error) {
			reads++
			if reads > 1 {
				return nil, errors.New("second read")
			}
			return f.tmpl, nil
		}
		w := f.do(http.MethodPost, apiv1.SessionPath(sess.Name)+"/resume", tokHuman, nil)
		if w.Code != http.StatusAccepted || reads != 1 || patches != 2 {
			t.Fatalf("status %d, reads %d, patches %d: %s", w.Code, reads, patches, w.Body.String())
		}
	})
}

func TestCodexWorkbenchReservedNameAllWrites(t *testing.T) {
	t.Run("standby", func(t *testing.T) {
		f := newFixture(t)
		name := generatedName("haynes-ops", f.now)
		f.srv.CodexWorkbenchHosts = codexworkbench.Policy{Name: name, Namespace: sessionNS}
		req := apiv1.CreateSessionRequest{Repo: "haynes-ops", Agent: "claude", Mode: "remote", Model: "claude-opus-5-5", Profile: "dev"}
		if _, err := f.srv.CreateStandby(context.Background(), req); err == nil {
			t.Fatal("standby occupied reserved name")
		}
		var got v1alpha1.AgentSession
		if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: sessionNS, Name: name}, &got); !apierrors.IsNotFound(err) {
			t.Fatal("standby persisted")
		}
	})
	t.Run("canary", func(t *testing.T) {
		f, b := newCanaryFixture(t, "claude", interceptor.Funcs{})
		f.srv.CodexWorkbenchHosts = codexworkbench.Policy{Name: b.Record.SessionName(), Namespace: sessionNS}
		w := f.do(http.MethodPost, apiv1.SessionsPath, tokClient, map[string]string{"canaryID": b.Record.ID})
		if w.Code != http.StatusForbidden {
			t.Fatalf("canary %d %s", w.Code, w.Body.String())
		}
	})
	t.Run("pilot", func(t *testing.T) {
		f, b := pilotAPI(t, interceptor.Funcs{})
		f.srv.CodexWorkbenchHosts = codexworkbench.Policy{Name: b.Policy.SessionName(0), Namespace: sessionNS}
		w := f.do(http.MethodPost, apiv1.SessionsPath, tokClient, b.Policy.Request(0))
		if w.Code != http.StatusForbidden {
			t.Fatalf("pilot %d %s", w.Code, w.Body.String())
		}
	})
	t.Run("summon-collision", func(t *testing.T) {
		f := summonFixture(t)
		name := "rem-responder-1"
		f.srv.CodexWorkbenchHosts = codexworkbench.Policy{Name: name + "-2", Namespace: sessionNS}
		f.srv.Client = interceptor.NewClient(f.c, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
			if s, ok := o.(*v1alpha1.AgentSession); ok && s.Name == name {
				other := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: name}, Spec: v1alpha1.AgentSessionSpec{Agent: v1alpha1.AgentClaude, Mode: v1alpha1.ModeTask, Parent: humanSA}}
				if err := c.Create(ctx, other); err != nil {
					return err
				}
			}
			return c.Create(ctx, o, opts...)
		}})
		w := summonPost(t, f, keyedRemediation(name, "alert-1"))
		if w.Code != http.StatusForbidden {
			t.Fatalf("summon %d %s", w.Code, w.Body.String())
		}
		var got v1alpha1.AgentSession
		if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: sessionNS, Name: name + "-2"}, &got); !apierrors.IsNotFound(err) {
			t.Fatal("summon occupied reserved suffix")
		}
	})
}

func TestCodexWorkbenchT30PrecedesTemplateRead(t *testing.T) {
	f := newFixture(t)
	tok := hostCaller(t, f, "workbench")
	f.srv.Templates = func(context.Context) (*templates.Templates, error) {
		t.Fatal("T30 must refuse before templates are read")
		return nil, nil
	}
	req := codexHostRequest()
	req.Profile = "workbench"
	w := f.do(http.MethodPost, apiv1.SessionsPath, tok, req)
	if w.Code != http.StatusForbidden || unmarshal[apiv1.ErrorResponse](t, w.Body.Bytes()).Error.Code != apiv1.CodeForbidden {
		t.Fatalf("T30 %d %s", w.Code, w.Body.String())
	}
}

func TestCodexWorkbenchOrdinaryNamedIdentityPolicyUnchanged(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[enabled], func(t *testing.T) {
			f := newFixture(t)
			f.srv.Policy.NamedCoordinatorIdentities = enabled
			f.tmpl.Profiles["delegate"] = templates.Profile{ServiceAccount: templates.CoordinatorServiceAccountPrefix + "synthetic"}
			req := codexHostRequest()
			req.Profile = "delegate"
			w := f.do(http.MethodPost, apiv1.SessionsPath, tokHuman, req)
			want := http.StatusCreated
			if enabled {
				want = http.StatusForbidden
			}
			if w.Code != want {
				t.Fatalf("identity policy %v: %d %s", enabled, w.Code, w.Body.String())
			}
		})
	}
}
