package apiserver

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/codexworkbench"
	"github.com/thaynes43/dev-env/internal/templates"
)

func coordinatorHostFixture(t *testing.T, f *fixture, name string) *threadExec {
	t.Helper()
	f.srv.CodexWorkbenchHosts = codexworkbench.Policy{Name: name, Namespace: sessionNS}
	f.tmpl.Profiles["workbench"] = templates.Profile{ServiceAccount: templates.WorkbenchServiceAccount}
	f.tmpl.Profiles["wb-alias"] = f.tmpl.Profiles["workbench"]
	f.codexHostPod(name)
	host := f.session(name)
	host.Spec.Profile, host.Spec.Parent = "workbench", "flux-system/kustomize-controller"
	if err := f.c.Update(context.Background(), host); err != nil {
		t.Fatal(err)
	}
	var pod corev1.Pod
	key := client.ObjectKey{Namespace: sessionNS, Name: name}
	if err := f.c.Get(context.Background(), key, &pod); err != nil {
		t.Fatal(err)
	}
	pod.Spec.ServiceAccountName = templates.WorkbenchServiceAccount
	if err := f.c.Update(context.Background(), &pod); err != nil {
		t.Fatal(err)
	}
	ex := &threadExec{out: hostThreadOutput(t, name, apiv1.ThreadResponded, "")}
	f.srv.Exec = ex
	return ex
}
func coordinatorRequest() apiv1.ThreadStartRequest {
	req := threadRequest()
	req.Coordinator = true
	req.Model, req.Effort = "gpt-6.1-sol", "medium"
	return req
}
func creationProfile(t *testing.T, f *fixture, name, profile string) {
	t.Helper()
	s := f.session(name)
	s.Spec.Profile = profile
	if err := f.c.Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
}

func TestCoordinatorThreadEntry(t *testing.T) {
	for _, kind := range []string{"human", "client", "target-default", "target-alias", "workbench-created", "default-created", "alias-created", "ordinary-same-profile", "ordinary-ancestor", "raised", "raised-patched-label", "named-account", "wrong-pod-account", "creation-profile-missing", "templates-unreadable", "ordinary-intent", "unlisted-host", "target-account-drift", "target-shape-drift"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			ex := coordinatorHostFixture(t, f, "reserved")
			tok := tokHuman
			req := coordinatorRequest()
			allowed := false
			switch kind {
			case "human":
				allowed = true
			case "client":
				tok = tokClient
				allowed = true
			case "target-default":
				f.tmpl.DefaultProfile = "workbench"
				creationProfile(t, f, "reserved", "")
				allowed = true
			case "target-alias":
				creationProfile(t, f, "reserved", "wb-alias")
				allowed = true
			case "workbench-created", "default-created", "alias-created", "raised", "raised-patched-label", "wrong-pod-account", "creation-profile-missing":
				tok = hostCaller(t, f, "workbench")
				creationProfile(t, f, "workbench-parent", "workbench")
				switch kind {
				case "default-created":
					f.tmpl.DefaultProfile = "workbench"
					creationProfile(t, f, "workbench-parent", "")
					allowed = true
				case "alias-created":
					creationProfile(t, f, "workbench-parent", "wb-alias")
					allowed = true
				case "raised", "raised-patched-label":
					creationProfile(t, f, "workbench-parent", "dev")
					f.raiseReady(true, "workbench")
					f.putActiveProfile(f.session("workbench-parent"), false, "workbench")
					if raised, err := f.srv.raisedToWorkbench(context.Background(), f.session("workbench-parent")); err != nil || !raised {
						t.Fatalf("raise fixture not active: %v %v", raised, err)
					}
				case "creation-profile-missing":
					creationProfile(t, f, "workbench-parent", "removed")
				case "wrong-pod-account":
					var p corev1.Pod
					if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: sessionNS, Name: "workbench-parent"}, &p); err != nil {
						t.Fatal(err)
					}
					p.Spec.ServiceAccountName = "dev-env-agent"
					if err := f.c.Update(context.Background(), &p); err != nil {
						t.Fatal(err)
					}
				default:
					allowed = true
				}
			case "ordinary-same-profile", "ordinary-ancestor", "named-account":
				tok = f.sessionPod("ordinary", "workbench", 0)
				if kind == "ordinary-ancestor" {
					s := f.session("reserved")
					s.Spec.Parent = "ordinary"
					if err := f.c.Update(context.Background(), s); err != nil {
						t.Fatal(err)
					}
				}
				if kind == "named-account" {
					f.srv.Policy.NamedCoordinatorIdentities = true
					id := f.auth[tok]
					id.ServiceAccount = "dev-env-coordinator-test"
					f.auth[tok] = id
					var pod corev1.Pod
					if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: sessionNS, Name: "ordinary"}, &pod); err != nil {
						t.Fatal(err)
					}
					pod.Spec.ServiceAccountName = id.ServiceAccount
					if err := f.c.Update(context.Background(), &pod); err != nil {
						t.Fatal(err)
					}
					sess := f.session("ordinary")
					sess.Labels[templates.CoordinatorIdentityLabel] = id.ServiceAccount
					if err := f.c.Update(context.Background(), sess); err != nil {
						t.Fatal(err)
					}
					if caller, err := f.srv.resolveCaller(context.Background(), id); err != nil || caller.kind != kindSession {
						t.Fatalf("named caller not authenticated: %+v %v", caller, err)
					}
				}
			case "templates-unreadable":
				f.tmplOK = false
			case "ordinary-intent":
				req.Coordinator = false
			case "unlisted-host":
				f.srv.CodexWorkbenchHosts.Name = "other"
			case "target-account-drift":
				var p corev1.Pod
				if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: sessionNS, Name: "reserved"}, &p); err != nil {
					t.Fatal(err)
				}
				p.Spec.ServiceAccountName = "dev-env-agent"
				if err := f.c.Update(context.Background(), &p); err != nil {
					t.Fatal(err)
				}
			case "target-shape-drift":
				s := f.session("reserved")
				s.Labels[v1alpha1.LabelDepth] = "1"
				if err := f.c.Update(context.Background(), s); err != nil {
					t.Fatal(err)
				}
			}
			w := f.do(http.MethodPost, apiv1.SessionThreadsPath("reserved"), tok, req)
			if allowed {
				if w.Code != 200 || ex.calls() != 1 {
					t.Fatalf("allowed: %d %s calls=%d", w.Code, w.Body, ex.calls())
				}
				host := f.session("reserved")
				if !strings.Contains(ex.cmds[0], "--coordinator --host reserved --expected-session-uid "+string(host.UID)+" --expected-pod-uid ") {
					t.Fatal("missing target UID fences", ex.cmds)
				}
			} else if w.Code < 400 || ex.calls() != 0 {
				t.Fatalf("refused: %d %s calls=%d", w.Code, w.Body, ex.calls())
			}
		})
	}
}

func TestCoordinatorIntentNeverUsesPoolHost(t *testing.T) {
	f := newFixture(t)
	f.codexHostPod("ordinary")
	ex := &threadExec{}
	f.srv.Exec = ex
	w := f.do(http.MethodPost, apiv1.SessionThreadsPath("ordinary"), tokClient, coordinatorRequest())
	if w.Code != 403 || ex.calls() != 0 {
		t.Fatalf("%d %s calls=%d", w.Code, w.Body, ex.calls())
	}
}

func TestCoordinatorThreadTargetRace(t *testing.T) {
	for _, race := range []string{"session-uid", "pod-uid", "account", "profile", "suspended"} {
		t.Run(race, func(t *testing.T) {
			reads := 0
			armed := false
			f := newFixtureWith(t, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, o client.Object, opts ...client.GetOption) error {
				if err := c.Get(ctx, key, o, opts...); err != nil {
					return err
				}
				if armed && key.Name == "reserved" {
					if s, ok := o.(*v1alpha1.AgentSession); ok {
						reads++
						if reads >= 2 {
							switch race {
							case "session-uid":
								s.UID = "replaced"
							case "profile":
								s.Spec.Profile = "dev"
							case "suspended":
								s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
							}
						}
					}
					if p, ok := o.(*corev1.Pod); ok && reads >= 2 {
						switch race {
						case "pod-uid":
							p.UID = "replaced"
						case "account":
							p.Spec.ServiceAccountName = "dev-env-agent"
						}
					}
				}
				return nil
			}})
			ex := coordinatorHostFixture(t, f, "reserved")
			reads = 0
			armed = true
			w := f.do(http.MethodPost, apiv1.SessionThreadsPath("reserved"), tokHuman, coordinatorRequest())
			if w.Code < 400 || ex.calls() != 0 {
				t.Fatalf("race admitted: %d %s calls=%d", w.Code, w.Body, ex.calls())
			}
		})
	}
}

func TestCoordinatorOneUseRCRefusesBeforeReservation(t *testing.T) {
	f, req, ex := rcFixture(t)
	f.srv.CodexWorkbenchHosts = codexworkbench.Policy{Name: "rc-host", Namespace: sessionNS}
	s := f.session("rc-host")
	s.Spec.Profile = "workbench"
	s.Spec.Parent = "flux-system/kustomize-controller"
	s.Labels = map[string]string{v1alpha1.LabelDepth: "0"}
	if err := f.c.Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	f.tmpl.Profiles["workbench"] = templates.Profile{ServiceAccount: templates.WorkbenchServiceAccount}
	w := f.do(http.MethodPost, apiv1.SessionPath("rc-host")+"/codex-thread", tokClient, req)
	if w.Code < 400 || len(ex.calls) != 0 || f.session("rc-host").Annotations[apiv1.CodexRCIntentAnnotation] != "" {
		t.Fatalf("RC admitted: %d %s calls=%v", w.Code, w.Body, ex.calls)
	}
}

func TestCoordinatorGatePinsTemplatesOnce(t *testing.T) {
	f := newFixture(t)
	ex := coordinatorHostFixture(t, f, "reserved")
	tok := hostCaller(t, f, "workbench")
	creationProfile(t, f, "workbench-parent", "wb-alias")
	reads := 0
	f.srv.Templates = func(context.Context) (*templates.Templates, error) {
		reads++
		if reads > 1 {
			return nil, errors.New("changed")
		}
		return f.tmpl, nil
	}
	w := f.do(http.MethodPost, apiv1.SessionThreadsPath("reserved"), tok, coordinatorRequest())
	if w.Code != 200 || ex.calls() != 1 || reads != 1 {
		t.Fatalf("%d %s exec=%d templates=%d", w.Code, w.Body, ex.calls(), reads)
	}
}
