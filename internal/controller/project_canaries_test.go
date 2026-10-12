package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/projectcanary"
	"github.com/thaynes43/dev-env/internal/projectcanary/testutil"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
)

func TestProjectCanaryPodWholeExecutorBounds(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			f := testutil.New(t, provider, "dev/dev-env")
			pod, err := buildManagedPod(f.Session, f.Templates, "https://operator.example", true, true, nil)
			if err != nil {
				t.Fatal(err)
			}
			if pod.Spec.RestartPolicy != corev1.RestartPolicyNever || pod.Spec.ActiveDeadlineSeconds == nil || *pod.Spec.ActiveDeadlineSeconds != 600 || pod.Spec.TerminationGracePeriodSeconds == nil || *pod.Spec.TerminationGracePeriodSeconds != 5 || pod.Annotations["k8tz.io/inject"] != "false" || len(pod.Spec.Containers) != 1 || len(pod.Spec.InitContainers) != 0 || len(pod.Spec.EphemeralContainers) != 0 || pod.Spec.Containers[0].Image != f.Record.Image {
				t.Fatal("canary lost whole-Pod finite bounds")
			}
			for _, name := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
				request, limit := pod.Spec.Containers[0].Resources.Requests[name], pod.Spec.Containers[0].Resources.Limits[name]
				if request.Sign() <= 0 || limit.Sign() <= 0 {
					t.Fatal("unbounded canary resources")
				}
			}
			ownPVC := 0
			for _, v := range pod.Spec.Volumes {
				if v.PersistentVolumeClaim != nil {
					ownPVC++
					if v.Name != "home" || v.PersistentVolumeClaim.ClaimName != HomeClaimName(f.Session.Name) {
						t.Fatal("canary exposed another PVC")
					}
				}
			}
			if ownPVC != 1 || slices.ContainsFunc(pod.Spec.Containers[0].VolumeMounts, func(m corev1.VolumeMount) bool { return m.Name == "shared" || m.MountPath == "/home/dev/.shared" }) {
				t.Fatal("canary still exposes shared scratch")
			}
			if envOf(pod.Spec.Containers[0], "AGENTD_WORKSPACE_ID").Value != "" || slices.ContainsFunc(pod.Spec.Volumes, func(v corev1.Volume) bool { return v.Name == "workspace" }) {
				t.Fatal("canary acquired shared project storage")
			}
			if provider == "codex" {
				if envOf(pod.Spec.Containers[0], "AGENTD_CODEX_ACCESS_FILE").Value != projectcanary.CodexAccessPath+"/access.json" {
					t.Fatal("missing access-only projection")
				}
				if _, err := buildManagedPod(f.Session, f.Templates, "https://operator.example", false, false, nil); err == nil {
					t.Fatal("disabled managed Codex rendered")
				}
			}
			f.Session.Spec.Prompt += "caller change"
			if _, err := buildManagedPod(f.Session, f.Templates, "https://operator.example", true, false, nil); err == nil {
				t.Fatal("foreign bound prompt rendered")
			}
		})
	}
}

// The canary's evidence is the agent reporting the rule markers it loaded, so
// the operator must hand them over only as rules, never as the prompt: a
// marker in what the agent is told to do proves nothing about what it loaded.
// This renders the pod and checks every value it carries against every line of
// the bound snapshot's rules, so it fails whether the record's prompt or the
// operator's rendering puts a rule line where the agent reads instructions.
func TestProjectCanaryRenderedPromptAndArgsCarryNoRuleLine(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			f := testutil.New(t, provider, "dev/dev-env")
			snapshot, err := projectcatalog.ParseSnapshot([]byte(f.Session.Annotations[v1alpha1.AnnotationProjectSnapshot]))
			if err != nil {
				t.Fatal(err)
			}
			lines := ruleLines(snapshot.Rules())
			if len(lines) == 0 {
				t.Fatal("the fixture binds no rule line, so this proves nothing")
			}
			pod, err := buildManagedPod(f.Session, f.Templates, "https://operator.example", true, true, nil)
			if err != nil {
				t.Fatal(err)
			}
			c := pod.Spec.Containers[0]
			var doc protocol.Session
			if err := json.Unmarshal([]byte(envOf(c, protocol.SessionEnv).Value), &doc); err != nil {
				t.Fatal(err)
			}
			if doc.Prompt != f.Record.Prompt {
				t.Fatalf("rendered prompt %q is not the bound record's", doc.Prompt)
			}
			delivered, err := projectcatalog.ParseSnapshot(doc.ProjectSnapshot)
			if err != nil || delivered.Rules() != snapshot.Rules() {
				t.Fatal("the rules do not reach agentd through the bound snapshot")
			}
			values := map[string]string{"prompt": doc.Prompt}
			for i, v := range append(slices.Clone(c.Command), c.Args...) {
				values[fmt.Sprintf("argv[%d]", i)] = v
			}
			for _, e := range c.Env {
				if e.Name != protocol.SessionEnv {
					values["env "+e.Name] = e.Value
				}
			}
			for where, v := range values {
				for _, line := range lines {
					if strings.Contains(v, line) {
						t.Errorf("%s carries rule line %q", where, line)
					}
				}
			}
		})
	}
}

// ruleLines are the non-blank lines of a rules text, trimmed.
func ruleLines(rules string) []string {
	var out []string
	for _, line := range strings.Split(rules, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

func canaryReconciler(c client.Client, reader client.Reader, f testutil.Fixture) *Reconciler {
	return &Reconciler{Client: c, APIReader: reader, Templates: client.ObjectKeyFromObject(f.Template), APIURL: "https://operator.example", ManagedCodexTasks: true, PrivateHistoryRetention: true, PrivateProjectCanaries: f.Policy, PrivateProjectCloneOwner: testutil.Owner}
}

func TestProjectCanaryIndependentAuthorityAndLatchBeforeOnePod(t *testing.T) {
	f := testutil.New(t, "codex", "dev/dev-env")
	base := privateHistoryClient(f.Session, f.Catalog, f.Template)
	podCreates := 0
	c := interceptor.NewClient(base, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
		switch typed := o.(type) {
		case *corev1.PersistentVolumeClaim:
			typed.UID = "original-home"
		case *corev1.Pod:
			podCreates++
			typed.UID = "original-pod"
			var retained v1alpha1.AgentSession
			if err := base.Get(ctx, client.ObjectKeyFromObject(f.Session), &retained); err != nil {
				t.Fatal(err)
			}
			h := retained.Status.PrivateHistory
			if h == nil || !h.ExecutorAttempted || h.ExecutorIntent == "" || h.HomeUID != "original-home" || typed.Annotations[privateHistoryExecutorAnnotation] != h.ExecutorIntent || !slices.Contains(retained.Finalizers, v1alpha1.ProjectCanaryFinalizer) || !slices.Contains(retained.Finalizers, Finalizer) {
				t.Fatal("Pod created before retained/latch confirmation")
			}
		}
		return c.Create(ctx, o, opts...)
	}})
	r := canaryReconciler(c, base, f)
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(f.Session)}
	if _, err := r.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	var pod corev1.Pod
	if err := base.Get(context.Background(), request.NamespacedName, &pod); err != nil || podCreates != 1 {
		t.Fatal("first exact canary not admitted", err, podCreates)
	}
	if err := base.Delete(context.Background(), &pod); err != nil {
		t.Fatal(err)
	}
	// Reconstructing with an absent original executor cannot start another.
	r = canaryReconciler(c, base, f)
	if _, err := r.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if podCreates != 1 {
		t.Fatal("missing original Pod rearmed canary")
	}
	if err := base.Get(context.Background(), request.NamespacedName, &pod); !apierrors.IsNotFound(err) {
		t.Fatal("lost original was recreated", err)
	}
	var kept v1alpha1.AgentSession
	if err := base.Get(context.Background(), request.NamespacedName, &kept); err != nil || kept.Status.PrivateHistory.StopState != "Unknown" || !kept.Status.PrivateHistory.ExecutorAttempted {
		t.Fatal("missing Pod inferred stop/reset latch", err)
	}
}

func TestProjectCanaryDriftAndUnconfirmedRetentionCreateNoPod(t *testing.T) {
	for _, name := range []string{"disabled", "retention-off", "catalog-drift", "template-drift", "status-unconfirmed"} {
		t.Run(name, func(t *testing.T) {
			f := testutil.New(t, "claude", "dev/dev-env")
			if name == "catalog-drift" {
				f.Catalog.ResourceVersion = "2"
			}
			if name == "template-drift" {
				f.Template.Data["templates.yaml"] = "invalid"
			}
			base := privateHistoryClient(f.Session, f.Catalog, f.Template)
			creates := 0
			c := interceptor.NewClient(base, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
				creates++
				return c.Create(ctx, o, opts...)
			}})
			r := canaryReconciler(c, base, f)
			switch name {
			case "disabled":
				r.PrivateProjectCanaries = nil
			case "retention-off":
				r.PrivateHistoryRetention = false
			case "status-unconfirmed":
				reads := 0
				r.APIReader = retentionReader{Reader: base, beforeGet: func(o client.Object) error {
					if _, ok := o.(*v1alpha1.AgentSession); ok {
						reads++
						if reads > 1 {
							return errors.New("synthetic retention confirmation unavailable")
						}
					}
					return nil
				}}
			}
			_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(f.Session)})
			if name == "status-unconfirmed" && err == nil {
				t.Fatal("unknown retained write succeeded")
			}
			if creates != 0 {
				t.Fatal("failed independent admission created resources", creates)
			}
			var kept v1alpha1.AgentSession
			if err := base.Get(context.Background(), client.ObjectKeyFromObject(f.Session), &kept); err != nil || !slices.Contains(kept.Finalizers, v1alpha1.ProjectCanaryFinalizer) {
				t.Fatal("refusal lost permanent identity", err)
			}
		})
	}
}

func TestProjectCanaryEarlyDeletePreservesOriginalWithoutRetentionStatus(t *testing.T) {
	f := testutil.New(t, "claude", "dev/dev-env")
	base := privateHistoryClient(f.Session, f.Catalog, f.Template)
	if err := base.Delete(context.Background(), f.Session); err != nil {
		t.Fatal(err)
	}
	r := canaryReconciler(base, base, f)
	r.PrivateHistoryRetention = false
	r.PrivateProjectCanaries = nil
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(f.Session)}); err != nil {
		t.Fatal(err)
	}
	var retained v1alpha1.AgentSession
	if err := base.Get(context.Background(), client.ObjectKeyFromObject(f.Session), &retained); err != nil || retained.UID != f.Session.UID || retained.DeletionTimestamp.IsZero() || !slices.Contains(retained.Finalizers, v1alpha1.ProjectCanaryFinalizer) || retained.Status.PrivateHistory.Admission != "Unknown" || retained.Status.PrivateHistory.StopState != "Unknown" {
		t.Fatal("delete-before-reconcile released/guessed original", err)
	}
	if err := releaseSession(context.Background(), base, &retained); err == nil {
		t.Fatal("missing retention finalizer escaped permanent canary guard")
	}
	var pods corev1.PodList
	if err := base.List(context.Background(), &pods); err != nil || len(pods.Items) != 0 {
		t.Fatal("early-delete canary executed", err)
	}
}

func TestProjectCanaryMissingStatusStillBlocksPrivateHomeCleanup(t *testing.T) {
	f := testutil.New(t, "claude", "dev/dev-env")
	s := f.Session
	s.Finalizers = append(s.Finalizers, Finalizer)
	home := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: HomeClaimName(s.Name), Namespace: s.Namespace, UID: "original-home", Finalizers: []string{Finalizer}, OwnerReferences: []metav1.OwnerReference{ownerRef(s)}}}
	base := privateHistoryClient(s, home)
	if err := deleteVolume(context.Background(), base, s, home, false, true); err == nil {
		t.Fatal("missing status permitted HOME delete")
	}
	if err := releaseVolume(context.Background(), base, s, home, false, true); err == nil {
		t.Fatal("missing status permitted HOME release")
	}
	if err := releaseSession(context.Background(), base, s); err == nil {
		t.Fatal("missing status permitted Session release")
	}
	var kept corev1.PersistentVolumeClaim
	if err := base.Get(context.Background(), client.ObjectKeyFromObject(home), &kept); err != nil || kept.UID != home.UID {
		t.Fatal("retained HOME lost", err)
	}
}

func TestProjectCanaryAuthorityLossAfterIntentNeverRearms(t *testing.T) {
	f := testutil.New(t, "claude", "dev/dev-env")
	base := privateHistoryClient(f.Session, f.Catalog, f.Template)
	creates := 0
	c := interceptor.NewClient(base, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
		if home, ok := o.(*corev1.PersistentVolumeClaim); ok {
			home.UID = "original-home"
		}
		if _, ok := o.(*corev1.Pod); ok {
			creates++
		}
		return c.Create(ctx, o, opts...)
	}})
	reader := interceptor.NewClient(base, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, o client.Object, opts ...client.GetOption) error {
		if err := c.Get(ctx, key, o, opts...); err != nil {
			return err
		}
		if cm, ok := o.(*corev1.ConfigMap); ok && key == client.ObjectKeyFromObject(f.Catalog) {
			var retained v1alpha1.AgentSession
			if err := base.Get(ctx, client.ObjectKeyFromObject(f.Session), &retained); err != nil {
				return err
			}
			if retained.Status.PrivateHistory != nil && retained.Status.PrivateHistory.ExecutorAttempted {
				cm.ResourceVersion = "2"
			}
		}
		return nil
	}})
	r := canaryReconciler(c, reader, f)
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(f.Session)}
	if _, err := r.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	var retained v1alpha1.AgentSession
	if err := base.Get(context.Background(), request.NamespacedName, &retained); err != nil {
		t.Fatal(err)
	}
	if creates != 0 || retained.Status.PrivateHistory == nil || !retained.Status.PrivateHistory.ExecutorAttempted || retained.Status.PrivateHistory.ExecutorIntent == "" {
		t.Fatal("late authority loss executed or discarded reserved intent")
	}
	// Healthy authority later cannot turn that reserved attempt into a fresh one.
	r = canaryReconciler(c, base, f)
	if _, err := r.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if creates != 0 {
		t.Fatal("restored authority rearmed unconfirmed original attempt")
	}
	var home corev1.PersistentVolumeClaim
	if err := base.Get(context.Background(), client.ObjectKey{Namespace: f.Session.Namespace, Name: HomeClaimName(f.Session.Name)}, &home); err != nil || home.UID != "original-home" || !slices.Contains(home.Finalizers, Finalizer) {
		t.Fatal("late refusal lost retained original HOME", err)
	}
}

func TestProjectCanaryFinalExecutorRefusesForeignPVCAndMountAliases(t *testing.T) {
	f := testutil.New(t, "claude", "dev/dev-env")
	pod, err := buildManagedPod(f.Session, f.Templates, "https://operator.example", true, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*corev1.Pod){
		func(p *corev1.Pod) {
			p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{Name: "foreign", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "other-home"}}})
		},
		func(p *corev1.Pod) {
			p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{Name: "alias", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: HomeClaimName(f.Session.Name)}}})
		},
		func(p *corev1.Pod) {
			p.Spec.Containers[0].VolumeMounts = append(p.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{Name: "home", MountPath: "/alias"})
		},
		func(p *corev1.Pod) {
			p.Spec.Containers[0].VolumeMounts = append(p.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{Name: "tmp", MountPath: "/home/dev/.shared"})
		},
		func(p *corev1.Pod) { p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{Name: "workspace"}) },
	} {
		copy := pod.DeepCopy()
		mutate(copy)
		if validateProjectCanaryExecutor(f.Session, copy) == nil {
			t.Fatal("foreign/alias executor storage accepted")
		}
	}
}
