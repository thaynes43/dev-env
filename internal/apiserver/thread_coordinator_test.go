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
	for _, kind := range []string{"human", "client", "target-default", "target-alias", "workbench-created", "default-created", "alias-created", "ordinary-same-profile", "ordinary-ancestor", "raised", "patched-label", "named-account", "wrong-pod-account", "creation-profile-missing", "templates-unreadable", "ordinary-intent", "unlisted-host", "target-account-drift", "target-shape-drift"} {
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
			case "workbench-created", "default-created", "alias-created", "raised", "patched-label", "wrong-pod-account", "creation-profile-missing":
				if kind == "raised" || kind == "patched-label" {
					f.srv.Policy.WorkbenchServiceAccount = templates.WorkbenchServiceAccount
					tok = f.sessionPod("workbench-parent", "dev", 0)
					id := f.auth[tok]
					id.ServiceAccount = templates.WorkbenchServiceAccount
					f.auth[tok] = id
					creationProfile(t, f, "workbench-parent", "dev")
					var p corev1.Pod
					if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: sessionNS, Name: "workbench-parent"}, &p); err != nil {
						t.Fatal(err)
					}
					p.Spec.ServiceAccountName = templates.WorkbenchServiceAccount
					if kind == "patched-label" {
						p.Labels[v1alpha1.LabelProfile] = "workbench"
					}
					if err := f.c.Update(context.Background(), &p); err != nil {
						t.Fatal(err)
					}
				} else {
					tok = hostCaller(t, f, "workbench")
					creationProfile(t, f, "workbench-parent", "workbench")
				}
				switch kind {
				case "default-created":
					f.tmpl.DefaultProfile = "workbench"
					creationProfile(t, f, "workbench-parent", "")
					allowed = true
				case "alias-created":
					creationProfile(t, f, "workbench-parent", "wb-alias")
					allowed = true
				case "raised":
					creationProfile(t, f, "workbench-parent", "dev")
					f.raiseReady(true, "workbench")
					f.putActiveProfile(f.session("workbench-parent"), false, "workbench")
					if raised, err := f.srv.raisedToWorkbench(context.Background(), f.session("workbench-parent")); err != nil || !raised {
						t.Fatalf("raise fixture not active: %v %v", raised, err)
					}
				case "patched-label":
					creationProfile(t, f, "workbench-parent", "dev")
					if raised, err := f.srv.raisedToWorkbench(context.Background(), f.session("workbench-parent")); err != nil || raised {
						t.Fatalf("patched-label fixture is raised: %v %v", raised, err)
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

func TestCoordinatorAPIRejectsDifferentModelOrEffort(t *testing.T) {
	for _, field := range []string{"model", "effort"} {
		t.Run(field, func(t *testing.T) {
			f := newFixture(t)
			ex := coordinatorHostFixture(t, f, "reserved")
			req := coordinatorRequest()
			if field == "model" {
				req.Model = "gpt-6-astra"
			} else {
				req.Effort = "high"
			}
			w := f.do(http.MethodPost, apiv1.SessionThreadsPath("reserved"), tokHuman, req)
			if w.Code != 422 || ex.calls() != 0 {
				t.Fatalf("changed coordinator request reached exec: %d %s calls=%d", w.Code, w.Body, ex.calls())
			}
		})
	}
}

// Ordinary entry uses the same live owned pod as the route's UID fence. No
// templates read (including the caller's profile) may decide this path.
func TestOrdinaryThreadAdmissionUsesLivePod(t *testing.T) {
	for _, route := range []string{"threads", "codex-thread"} {
		for _, setting := range []string{"", "another-host"} {
			for _, shape := range []string{"readable", "unreadable", "removed", "workbench-account", "reserved", "coordinator", "unknown-account", "live-workbench-account", "foreign", "no-pod"} {
				t.Run(route+"/setting="+setting+"/"+shape, func(t *testing.T) {
					var f *fixture
					var body any
					var calls func() int
					wantStatus, wantCalls := 200, 1
					host := "ordinary"
					if route == "threads" {
						f = newFixture(t)
						f.codexHostPod(host)
						ex := &threadExec{out: hostThreadOutput(t, host, apiv1.ThreadResponded, "")}
						f.srv.Exec = ex
						req := threadRequest()
						if shape == "coordinator" {
							req = coordinatorRequest()
						}
						body, calls = req, ex.calls
					} else {
						var req apiv1.CodexRCRequest
						var ex *rcExec
						f, req, ex = rcFixture(t)
						body, calls = req, func() int { return len(ex.calls) }
						host = "rc-host"
						wantStatus, wantCalls = 201, 2 // one preflight, one start, as on main
					}
					creationProfile(t, f, host, "dev")
					f.srv.CodexWorkbenchHosts = codexworkbench.Policy{Name: setting, Namespace: sessionNS}
					if shape == "reserved" {
						f.srv.CodexWorkbenchHosts.Name = host
					}
					if shape == "removed" {
						delete(f.tmpl.Profiles, "dev")
					}
					var pod corev1.Pod
					key := client.ObjectKey{Namespace: sessionNS, Name: host}
					if err := f.c.Get(context.Background(), key, &pod); err != nil {
						t.Fatal(err)
					}
					switch shape {
					case "workbench-account":
						pod.Spec.ServiceAccountName = templates.WorkbenchServiceAccount
					case "unknown-account":
						pod.Spec.ServiceAccountName = "unrecognised"
					case "foreign":
						pod.OwnerReferences[0].UID = "another-session"
					}
					if err := f.c.Update(context.Background(), &pod); err != nil {
						t.Fatal(err)
					}
					if shape == "no-pod" {
						if err := f.c.Delete(context.Background(), &pod); err != nil {
							t.Fatal(err)
						}
					}
					allowed := shape == "readable" || shape == "unreadable" || shape == "removed"
					reads := 0
					f.srv.Templates = func(context.Context) (*templates.Templates, error) {
						reads++
						if shape != "readable" && shape != "removed" {
							return nil, errors.New("templates unavailable")
						}
						return f.tmpl, nil
					}
					path := apiv1.SessionThreadsPath(host)
					if route == "codex-thread" {
						path = apiv1.SessionPath(host) + "/codex-thread"
					}
					// The one-use route has no coordinator purpose field; an attempted
					// coordinator request must fail strict decoding without exec.
					if route == "codex-thread" && shape == "coordinator" {
						body = map[string]any{"coordinator": true}
					}
					sessionReads, podReads := 0, 0
					sessionSincePod := false
					f.srv.Live = interceptor.NewClient(f.c, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						if key.Name == host {
							switch obj.(type) {
							case *v1alpha1.AgentSession:
								sessionReads++
								sessionSincePod = true
							case *corev1.Pod:
								podReads++
								if !sessionSincePod {
									t.Error("pod read before live Session or second account-only pod read")
								}
								sessionSincePod = false
							}
						}
						if err := c.Get(ctx, key, obj, opts...); err != nil {
							return err
						}
						if p, ok := obj.(*corev1.Pod); ok && key.Name == host && shape == "live-workbench-account" {
							p.Spec.ServiceAccountName = templates.WorkbenchServiceAccount
						}
						return nil
					}})
					w := f.do(http.MethodPost, path, tokClient, body)
					if allowed {
						wantPods := 1
						if route == "codex-thread" {
							wantPods = 3
						}
						if podReads != wantPods || sessionReads < 1 {
							t.Fatalf("extra/missing live reads: session=%d pod=%d want pod=%d", sessionReads, podReads, wantPods)
						}
						if w.Code != wantStatus || calls() != wantCalls || reads != 0 {
							t.Fatalf("ordinary changed: status=%d body=%s exec=%d templates=%d", w.Code, w.Body, calls(), reads)
						}
					} else {
						if calls() != 0 || (w.Code < 400 && !strings.Contains(w.Body.String(), "host_not_running")) {
							t.Fatalf("unproven host admitted: %d %s exec=%d", w.Code, w.Body, calls())
						}
						if f.session(host).Annotations[apiv1.CodexRCIntentAnnotation] != "" {
							t.Fatal("refusal reserved one-use intent")
						}
					}
				})
			}
		}
	}
}
