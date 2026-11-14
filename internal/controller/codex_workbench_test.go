package controller

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/codexworkbench"
	"github.com/thaynes43/dev-env/internal/templates"
)

func workbenchHostSession() *v1alpha1.AgentSession {
	s := taskSession()
	s.Name = "named-codex"
	s.UID = "host-uid"
	s.Spec.Agent = v1alpha1.AgentCodex
	s.Spec.Mode = v1alpha1.ModeRemote
	s.Spec.Model = "gpt-6.1-sol"
	s.Spec.Prompt = ""
	s.Spec.Limits = nil
	s.Spec.Profile = "workbench"
	s.Spec.Parent = "flux-system/kustomize-controller"
	s.Labels = map[string]string{v1alpha1.LabelDepth: "0"}
	s.Finalizers = []string{Finalizer}
	return s
}
func workbenchHostTemplate() *corev1.ConfigMap {
	doc := strings.Replace(exampleDoc, "profiles:\n", "profiles:\n  workbench:\n    serviceAccount: dev-env-workbench\n  alias:\n    serviceAccount: dev-env-workbench\n", 1)
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: templatesKey.Name, Namespace: templatesKey.Namespace}, Data: map[string]string{templates.Key: doc}}
}
func hostReconciler(t *testing.T, s *v1alpha1.AgentSession, cm *corev1.ConfigMap, hooks interceptor.Funcs, more ...client.Object) *Reconciler {
	t.Helper()
	objs := []client.Object{s}
	if cm != nil {
		objs = append(objs, cm)
	}
	objs = append(objs, more...)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).WithStatusSubresource(&v1alpha1.AgentSession{}).WithInterceptorFuncs(hooks).Build()
	return &Reconciler{Client: c, APIReader: c, Templates: templatesKey, SessionNamespace: s.Namespace, CodexWorkbenchHosts: codexworkbench.Policy{Name: s.Name, Namespace: s.Namespace}}
}
func runHostReconcile(t *testing.T, r *Reconciler, s *v1alpha1.AgentSession) *v1alpha1.AgentSession {
	t.Helper()
	ctx := context.Background()
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(s)}); err != nil {
		t.Fatal(err)
	}
	got := new(v1alpha1.AgentSession)
	if err := r.Client.Get(ctx, client.ObjectKeyFromObject(s), got); err != nil {
		t.Fatal(err)
	}
	return got
}
func assertNoHostResources(t *testing.T, r *Reconciler, s *v1alpha1.AgentSession) {
	t.Helper()
	for _, o := range []client.Object{&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: s.Name, Namespace: s.Namespace}}, &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: HomeClaimName(s.Name), Namespace: s.Namespace}}} {
		if err := r.Client.Get(context.Background(), client.ObjectKeyFromObject(o), o); !apierrors.IsNotFound(err) {
			t.Fatalf("refusal made resource %T: %v", o, err)
		}
	}
}
func assertHostReason(t *testing.T, s *v1alpha1.AgentSession, want string) {
	t.Helper()
	c := meta.FindStatusCondition(s.Status.Conditions, ConditionPodReady)
	if c == nil || c.Reason != want || c.Status != metav1.ConditionFalse {
		t.Fatalf("PodReady=%+v, want %s", c, want)
	}
	if want == codexworkbench.NotNamed && c.Message != codexworkbench.NotNamedMessage {
		t.Fatal(c.Message)
	}
}

// These objects go straight into the fake Kubernetes API, never the create route.
func TestCodexWorkbenchRawManifestAdmission(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*v1alpha1.AgentSession, *corev1.ConfigMap, *Reconciler)
		want string
	}{
		{"named", func(*v1alpha1.AgentSession, *corev1.ConfigMap, *Reconciler) {}, ""},
		{"empty", func(s *v1alpha1.AgentSession, cm *corev1.ConfigMap, r *Reconciler) { r.CodexWorkbenchHosts.Name = "" }, codexworkbench.NotNamed},
		{"unlisted", func(s *v1alpha1.AgentSession, cm *corev1.ConfigMap, r *Reconciler) {
			r.CodexWorkbenchHosts.Name = "other"
		}, codexworkbench.NotNamed},
		{"alias", func(s *v1alpha1.AgentSession, cm *corev1.ConfigMap, r *Reconciler) { s.Spec.Profile = "alias" }, ""},
		{"default", func(s *v1alpha1.AgentSession, cm *corev1.ConfigMap, r *Reconciler) {
			s.Spec.Profile = ""
			cm.Data[templates.Key] = strings.Replace(cm.Data[templates.Key], "defaultProfile: full", "defaultProfile: workbench", 1)
			r.CodexWorkbenchHosts.Name = ""
		}, codexworkbench.NotNamed},
		{"wrong-parent", func(s *v1alpha1.AgentSession, cm *corev1.ConfigMap, r *Reconciler) { s.Spec.Parent = "human/account" }, codexworkbench.InvalidShape},
		{"wrong-depth", func(s *v1alpha1.AgentSession, cm *corev1.ConfigMap, r *Reconciler) {
			s.Labels[v1alpha1.LabelDepth] = "1"
		}, codexworkbench.InvalidShape},
		{"wrong-namespace", func(s *v1alpha1.AgentSession, cm *corev1.ConfigMap, r *Reconciler) {
			r.CodexWorkbenchHosts.Namespace = "other"
		}, codexworkbench.InvalidShape},
		{"wrong-shape", func(s *v1alpha1.AgentSession, cm *corev1.ConfigMap, r *Reconciler) {
			s.Spec.Workspace = &v1alpha1.WorkspaceSpec{ID: "shared"}
		}, codexworkbench.InvalidShape},
		{"reserved-account", func(s *v1alpha1.AgentSession, cm *corev1.ConfigMap, r *Reconciler) { s.Spec.Profile = "dev" }, codexworkbench.InvalidShape},
		{"reserved-agent", func(s *v1alpha1.AgentSession, cm *corev1.ConfigMap, r *Reconciler) {
			s.Spec.Agent = v1alpha1.AgentClaude
		}, codexworkbench.InvalidShape},
		{"reserved-mode", func(s *v1alpha1.AgentSession, cm *corev1.ConfigMap, r *Reconciler) { s.Spec.Mode = v1alpha1.ModeLocal }, codexworkbench.InvalidShape},
		{"missing-profile", func(s *v1alpha1.AgentSession, cm *corev1.ConfigMap, r *Reconciler) { s.Spec.Profile = "absent" }, codexworkbench.TemplatesUnavailable},
		{"unreadable-templates", func(s *v1alpha1.AgentSession, cm *corev1.ConfigMap, r *Reconciler) {
			cm.Data[templates.Key] = "unreadable: ["
		}, codexworkbench.TemplatesUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, cm := workbenchHostSession(), workbenchHostTemplate()
			r := &Reconciler{CodexWorkbenchHosts: codexworkbench.Policy{Name: s.Name, Namespace: s.Namespace}}
			tc.edit(s, cm, r)
			p := r.CodexWorkbenchHosts
			r = hostReconciler(t, s, cm, interceptor.Funcs{})
			r.CodexWorkbenchHosts = p
			got := runHostReconcile(t, r, s)
			if tc.want != "" {
				assertNoHostResources(t, r, s)
				assertHostReason(t, got, tc.want)
				if got.Status.Phase != v1alpha1.PhasePending {
					t.Fatal(got.Status.Phase)
				}
			} else {
				var pod corev1.Pod
				if err := r.Client.Get(context.Background(), client.ObjectKeyFromObject(s), &pod); err != nil {
					t.Fatal(err)
				}
				if pod.Spec.ServiceAccountName != templates.WorkbenchServiceAccount {
					t.Fatal(pod.Spec.ServiceAccountName)
				}
			}
		})
	}
	t.Run("missing-templates", func(t *testing.T) {
		s := workbenchHostSession()
		r := hostReconciler(t, s, nil, interceptor.Funcs{})
		got := runHostReconcile(t, r, s)
		assertNoHostResources(t, r, s)
		assertHostReason(t, got, codexworkbench.TemplatesUnavailable)
	})
}

func TestCodexWorkbenchRunningPodPreserved(t *testing.T) {
	for _, failure := range []string{"empty", "shape", "templates", "unlisted-templates"} {
		t.Run(failure, func(t *testing.T) {
			s, cm := workbenchHostSession(), workbenchHostTemplate()
			s.Status.Phase = v1alpha1.PhaseRunning
			s.Status.Agent = &v1alpha1.AgentStatus{Status: protocol.AgentIdle, LastActivity: &metav1.Time{Time: time.Now().Add(-time.Hour)}}
			s.Spec.Lifecycle = &v1alpha1.Lifecycle{IdleSuspendAfter: &metav1.Duration{Duration: time.Second}}
			tm, err := templates.Parse(cm.Data)
			if err != nil {
				t.Fatal(err)
			}
			r := &Reconciler{CodexWorkbenchHosts: codexworkbench.Policy{Name: s.Name, Namespace: s.Namespace}}
			pod, err := r.sessionPod(s, tm, r.resolveCodexHost(s, tm))
			if err != nil {
				t.Fatal(err)
			}
			pod.UID = types.UID("keep-pod")
			pod.Status.Phase = corev1.PodRunning
			pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
			claim := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: HomeClaimName(s.Name), Namespace: s.Namespace, UID: "keep-home", OwnerReferences: pod.OwnerReferences}}
			if failure == "unlisted-templates" {
				// Only the retained pod proves its account during the outage.
				s.Spec.Profile = "dev"
			}
			r = hostReconciler(t, s, cm, interceptor.Funcs{Delete: func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error {
				t.Fatal("guard deleted a running resource")
				return nil
			}, Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
				t.Fatal("guard created a replacement")
				return nil
			}}, pod, claim)
			switch failure {
			case "empty", "unlisted-templates":
				r.CodexWorkbenchHosts.Name = ""
			case "shape":
				r.CodexWorkbenchHosts.Namespace = "other"
			}
			// Missing-template variant uses a read failure rather than deleting any resource.
			if failure == "templates" || failure == "unlisted-templates" {
				r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, k client.ObjectKey, o client.Object, opts ...client.GetOption) error {
					if _, ok := o.(*corev1.ConfigMap); ok {
						return errors.New("template unavailable")
					}
					return c.Get(ctx, k, o, opts...)
				}})
			}
			if err := r.Client.Get(context.Background(), client.ObjectKeyFromObject(pod), pod); err != nil {
				t.Fatal(err)
			}
			got := runHostReconcile(t, r, s)
			if !reflect.DeepEqual(s.Spec, got.Spec) || !reflect.DeepEqual(s.Annotations, got.Annotations) {
				t.Fatal("changed creation record")
			}
			var kept corev1.Pod
			if err := r.Client.Get(context.Background(), client.ObjectKeyFromObject(pod), &kept); err != nil || !reflect.DeepEqual(pod, &kept) {
				t.Fatalf("pod changed: %v", err)
			}
			if got.Status.Phase != v1alpha1.PhaseRunning {
				t.Fatal(got.Status.Phase)
			}
			var home corev1.PersistentVolumeClaim
			if err := r.Client.Get(context.Background(), client.ObjectKeyFromObject(claim), &home); err != nil || home.UID != claim.UID {
				t.Fatalf("HOME changed: %v", err)
			}

			want := codexworkbench.NotNamed
			if failure == "shape" {
				want = codexworkbench.InvalidShape
			}
			if failure == "templates" || failure == "unlisted-templates" {
				want = codexworkbench.TemplatesUnavailable
			}
			assertHostReason(t, got, want)
			kept.Status.Phase = corev1.PodFailed
			if err := r.Client.Status().Update(context.Background(), &kept); err != nil {
				t.Fatal(err)
			}
			ended := runHostReconcile(t, r, s)
			if ended.Status.Phase != v1alpha1.PhaseFailed {
				t.Fatal("preserved pod ended but phase is", ended.Status.Phase)
			}
			assertHostReason(t, ended, want)

		})
	}
}

func TestCodexWorkbenchBuildersAndRescue(t *testing.T) {
	s, cm := workbenchHostSession(), workbenchHostTemplate()
	tm, err := templates.Parse(cm.Data)
	if err != nil {
		t.Fatal(err)
	}
	for _, build := range []func() (*corev1.Pod, error){func() (*corev1.Pod, error) { return buildPod(s, tm, "") }, func() (*corev1.Pod, error) {
		return (&Reconciler{}).sessionPod(s, tm, (&Reconciler{}).resolveCodexHost(s, tm))
	}} {
		if p, e := build(); p != nil || e == nil {
			t.Fatalf("builder admitted unlisted host: %v", e)
		}
	}
	p, err := buildHoldPod(s, tm)
	if err != nil {
		t.Fatal(err)
	}
	if !isHoldPod(p) || p.Spec.ServiceAccountName != ServiceAccountName || !reflect.DeepEqual(p.Spec.Containers[0].Args, HoldArgs) {
		t.Fatal("rescue hold changed")
	}
	// Even a direct ensure from an alternate/recovery route cannot allocate HOME.
	r := hostReconciler(t, s, cm, interceptor.Funcs{})
	r.CodexWorkbenchHosts.Name = ""
	obs := &observation{pod: owned[*corev1.Pod]{missing: true}, claim: owned[*corev1.PersistentVolumeClaim]{missing: true}}
	if err := r.ensure(context.Background(), s, tm, obs); err != nil || obs.codexHostErr == nil {
		t.Fatalf("ensure guard: %v", err)
	}
	assertNoHostResources(t, r, s)
	// A refused host with an existing HOME can still create its ordinary rescue hold.
	claim := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: HomeClaimName(s.Name), Namespace: s.Namespace, UID: "retained-home", OwnerReferences: []metav1.OwnerReference{ownerRef(s)}}}
	if err := r.Client.Create(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	obs.claim = owned[*corev1.PersistentVolumeClaim]{obj: claim}
	obs.templates = tm
	obs.removalBlocked = errors.New("rescue needs the retained HOME")
	if err := r.startHoldPod(context.Background(), s, obs); err != nil {
		t.Fatal(err)
	}
	if obs.pod.missing || !isHoldPod(obs.pod.obj) || obs.pod.obj.Spec.ServiceAccountName != ServiceAccountName {
		t.Fatal("rescue hold was blocked or privileged")
	}
	var kept corev1.PersistentVolumeClaim
	if err := r.Client.Get(context.Background(), client.ObjectKeyFromObject(claim), &kept); err != nil || kept.UID != claim.UID {
		t.Fatal("rescue lost HOME", err)
	}
}

func TestCodexWorkbenchFinalBoundaryUsesOneAccount(t *testing.T) {
	s, cm := workbenchHostSession(), workbenchHostTemplate()
	reads := 0
	r := hostReconciler(t, s, cm, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, k client.ObjectKey, o client.Object, opts ...client.GetOption) error {
		if _, ok := o.(*corev1.ConfigMap); ok {
			reads++
			if reads > 1 {
				return errors.New("second templates read")
			}
		}
		return c.Get(ctx, k, o, opts...)
	}})
	runHostReconcile(t, r, s)
	if reads != 1 {
		t.Fatalf("templates reads %d", reads)
	}
	// The builder must consume the pinned profile, not resolve the account again.
	pinnedTemplates, err := templates.Parse(cm.Data)
	if err != nil {
		t.Fatal(err)
	}
	admission := r.resolveCodexHost(s, pinnedTemplates)
	pinnedTemplates.Profiles["workbench"] = templates.Profile{}
	planned, err := r.sessionPod(s, pinnedTemplates, admission)
	if err != nil || planned.Spec.ServiceAccountName != templates.WorkbenchServiceAccount {
		t.Fatalf("pod builder used a different account answer: %v", err)
	}
	// Change shape after admission/build, at HOME creation, to exercise the final check.
	s2 := workbenchHostSession()
	s2.Name = "boundary-host"
	tm, err := templates.Parse(cm.Data)
	if err != nil {
		t.Fatal(err)
	}
	r2 := hostReconciler(t, s2, cm, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
		if _, ok := o.(*corev1.PersistentVolumeClaim); ok {
			s2.Spec.Parent = "changed-after-build"
		}
		return c.Create(ctx, o, opts...)
	}})
	obs := &observation{pod: owned[*corev1.Pod]{missing: true}, claim: owned[*corev1.PersistentVolumeClaim]{missing: true}}
	if err := r2.ensure(context.Background(), s2, tm, obs); err != nil || obs.codexHostErr == nil {
		t.Fatalf("final boundary: %v", err)
	}
	var pod corev1.Pod
	if err := r2.Client.Get(context.Background(), client.ObjectKeyFromObject(s2), &pod); !apierrors.IsNotFound(err) {
		t.Fatalf("created final-boundary pod: %v", err)
	}
}

func TestCodexWorkbenchEmptySettingOrdinaryReconcileUnchanged(t *testing.T) {
	for _, tc := range []struct {
		agent   v1alpha1.AgentKind
		profile string
	}{{v1alpha1.AgentCodex, "dev"}, {v1alpha1.AgentClaude, "dev"}, {v1alpha1.AgentClaude, "workbench"}} {
		agent := tc.agent
		t.Run(string(agent)+"-"+tc.profile, func(t *testing.T) {
			s := workbenchHostSession()
			s.Name = "ordinary"
			s.Spec.Agent = agent
			if agent == v1alpha1.AgentClaude {
				s.Spec.Model = "claude-opus-5-5"
			}
			s.Spec.Profile = tc.profile
			cm := workbenchHostTemplate()
			r := hostReconciler(t, s, cm, interceptor.Funcs{})
			r.CodexWorkbenchHosts.Name = ""
			runHostReconcile(t, r, s)
			var before corev1.Pod
			if err := r.Client.Get(context.Background(), client.ObjectKeyFromObject(s), &before); err != nil {
				t.Fatal(err)
			}
			got := runHostReconcile(t, r, s)
			var after corev1.Pod
			if err := r.Client.Get(context.Background(), client.ObjectKeyFromObject(s), &after); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(s.Spec, got.Spec) || !reflect.DeepEqual(s.Annotations, got.Annotations) {
				t.Fatal("ordinary session/pod changed")
			}
		})
	}
}

func TestCodexWorkbenchEnvtestRefusesManifestBeforeHOME(t *testing.T) {
	startOperator(t)
	setTemplates(t, workbenchHostTemplate().Data[templates.Key])
	s := newSession(t, func(s *v1alpha1.AgentSession) {
		s.Spec = workbenchHostSession().Spec
		s.Labels = map[string]string{v1alpha1.LabelDepth: "0"}
	})
	got := waitStatus(t, s.Name, "the account guard refusal", func(st *v1alpha1.AgentSessionStatus) error {
		c := meta.FindStatusCondition(st.Conditions, ConditionPodReady)
		if c == nil || c.Reason != codexworkbench.NotNamed {
			return errors.New("waiting for named-host refusal")
		}
		return nil
	})
	assertHostReason(t, got, codexworkbench.NotNamed)
	for _, o := range []client.Object{&corev1.Pod{}, &corev1.PersistentVolumeClaim{}} {
		name := s.Name
		if _, ok := o.(*corev1.PersistentVolumeClaim); ok {
			name = HomeClaimName(s.Name)
		}
		if err := k8s.Get(context.Background(), client.ObjectKey{Namespace: s.Namespace, Name: name}, o); !apierrors.IsNotFound(err) {
			t.Fatalf("envtest resource %T: %v", o, err)
		}
	}
}

// These whole-status and result expectations are characterized against
// origin/main bda614f53c662f7171ef981cf778fe13ce6200ea. Seed transition times
// so an exact comparison needs no clock normalization. The rescue row proves
// the ordinary reconcile continues beyond admission, before the templates break.
func TestOrdinaryRunningSessionTemplatesErrorMatchesMain(t *testing.T) {
	for _, agent := range []v1alpha1.AgentKind{v1alpha1.AgentCodex, v1alpha1.AgentClaude} {
		for _, supersede := range []bool{false, true} {
			name := string(agent) + "/observe"
			if supersede {
				name = string(agent) + "/supersede-rescue"
			}
			t.Run(name, func(t *testing.T) {
				s := workbenchHostSession()
				s.Name, s.Generation = "ordinary", 3
				s.Spec.Agent, s.Spec.Profile = agent, "dev"
				if agent == v1alpha1.AgentClaude {
					s.Spec.Model = "claude-opus-5-5"
				}
				s.Spec.Lifecycle = &v1alpha1.Lifecycle{IdleSuspendAfter: &metav1.Duration{Duration: time.Second}}
				at := metav1.NewTime(time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC))
				pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
					Name: s.Name, Namespace: s.Namespace, UID: "ordinary-pod", OwnerReferences: []metav1.OwnerReference{ownerRef(s)},
					Labels: map[string]string{v1alpha1.LabelRevision: "old-revision"},
				}, Spec: corev1.PodSpec{ServiceAccountName: ServiceAccountName, NodeName: "test-node"},
					Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
				claim := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{
					Name: HomeClaimName(s.Name), Namespace: s.Namespace, UID: "ordinary-home", OwnerReferences: pod.OwnerReferences,
				}}
				s.Status = v1alpha1.AgentSessionStatus{
					Phase: v1alpha1.PhaseRunning, PodName: s.Name, NodeName: "test-node", Revision: "old-revision",
					Agent: &v1alpha1.AgentStatus{Status: protocol.AgentIdle, LastActivity: &at},
					Conditions: []metav1.Condition{
						{Type: ConditionPodReady, Status: metav1.ConditionTrue, Reason: "PodReady", Message: "the pod is Ready on test-node", ObservedGeneration: 3, LastTransitionTime: at},
						{Type: ConditionOutdated, Status: metav1.ConditionUnknown, Reason: "TemplatesInvalid", Message: "previous templates error", ObservedGeneration: 3, LastTransitionTime: at},
					},
				}
				if supersede {
					s.Status.Rescue = &v1alpha1.RescueStatus{Result: v1alpha1.RescueVerified, PodUID: "earlier-pod", At: &at}
				}
				want := s.Status.DeepCopy()
				wantResult := ctrl.Result{}
				if supersede {
					want.Rescue.Superseded = true
					wantResult.RequeueAfter = time.Second
				} else {
					want.Conditions[1].Message = "the current template revision is unknown: templates: template unavailable"
				}
				reads := 0
				r := hostReconciler(t, s, nil, interceptor.Funcs{
					Get: func(ctx context.Context, c client.WithWatch, k client.ObjectKey, o client.Object, opts ...client.GetOption) error {
						if _, ok := o.(*corev1.ConfigMap); ok && k == templatesKey {
							reads++
							return errors.New("template unavailable")
						}
						return c.Get(ctx, k, o, opts...)
					},
					Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
						t.Fatal("templates outage created a resource")
						return nil
					},
					Delete: func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error {
						t.Fatal("templates outage deleted a resource")
						return nil
					},
				}, pod, claim)
				r.CodexWorkbenchHosts.Name = ""
				ctx := context.Background()
				var before corev1.Pod
				if err := r.Client.Get(ctx, client.ObjectKeyFromObject(pod), &before); err != nil {
					t.Fatal(err)
				}
				result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(s)})
				if err != nil || result != wantResult {
					t.Errorf("reconcile = %+v, %v; main = %+v, nil", result, err, wantResult)
				}
				var got v1alpha1.AgentSession
				if err := r.Client.Get(ctx, client.ObjectKeyFromObject(s), &got); err != nil {
					t.Fatal(err)
				}
				if !apiequality.Semantic.DeepEqual(&got.Status, want) {
					t.Errorf("whole status differs from main:\ngot  %+v\nwant %+v", got.Status, *want)
				}
				if reads != 1 || !reflect.DeepEqual(s.Spec, got.Spec) || !reflect.DeepEqual(s.Annotations, got.Annotations) {
					t.Fatalf("templates reads = %d, or session spec/annotations changed", reads)
				}
				var kept corev1.Pod
				if err := r.Client.Get(ctx, client.ObjectKeyFromObject(pod), &kept); err != nil || !reflect.DeepEqual(before, kept) {
					t.Fatalf("pod changed: %v", err)
				}
				var home corev1.PersistentVolumeClaim
				if err := r.Client.Get(ctx, client.ObjectKeyFromObject(claim), &home); err != nil || home.UID != claim.UID {
					t.Fatalf("HOME changed: %v", err)
				}
			})
		}
	}
}

func TestCodexHostTemplatesErrorNeverBuildsPod(t *testing.T) {
	for _, profile := range []string{"dev", "workbench", "alias", ""} {
		for _, setting := range []string{"", "other", "named-codex"} {
			t.Run(profile+"/setting="+setting, func(t *testing.T) {
				s := workbenchHostSession()
				s.Spec.Profile = profile
				r := hostReconciler(t, s, nil, interceptor.Funcs{
					Get: func(ctx context.Context, c client.WithWatch, k client.ObjectKey, o client.Object, opts ...client.GetOption) error {
						if _, ok := o.(*corev1.ConfigMap); ok && k == templatesKey {
							return errors.New("template unavailable")
						}
						return c.Get(ctx, k, o, opts...)
					},
					Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
						t.Fatal("built a resource without templates")
						return nil
					},
				})
				r.CodexWorkbenchHosts.Name = setting
				got := runHostReconcile(t, r, s)
				assertHostReason(t, got, codexworkbench.TemplatesUnavailable)
				assertNoHostResources(t, r, s)
			})
		}
	}
}
