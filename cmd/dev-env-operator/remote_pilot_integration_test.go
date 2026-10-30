package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/controller"
	"github.com/thaynes43/dev-env/internal/homeguard"
	fixture "github.com/thaynes43/dev-env/internal/homeguard/testutil"
	"github.com/thaynes43/dev-env/internal/templates"
)

func rootPilotFlags(t *testing.T, remote bool) []string {
	t.Helper()
	f := fixture.Load(t)
	var s v1alpha1.AgentSession
	fixture.Decode(t, f.Session, &s)
	args := []string{
		"--session-namespace=" + s.Namespace,
		"--pilot-agent-image=" + f.UsageImage, "--pilot-agent-session=" + f.UsageParent + "/" + f.UsageKey,
		"--task-pilot-agent-image=" + f.TaskImage, "--task-pilot-agent-session=" + f.TaskParent + "/" + f.TaskKey,
	}
	if remote {
		args = append(args, "--remote-pilot-agent-session="+s.Spec.Parent+"|"+s.Labels[v1alpha1.LabelIdempotencyKey],
			"--remote-pilot-agent-image="+f.RemoteImage, "--original-home-binding="+f.Binding(t).String())
	}
	return args
}

func rootPilotObjects(t *testing.T) (*v1alpha1.AgentSession, *corev1.PersistentVolumeClaim, *corev1.PersistentVolume) {
	t.Helper()
	f := fixture.Load(t)
	var s v1alpha1.AgentSession
	var pvc corev1.PersistentVolumeClaim
	var pv corev1.PersistentVolume
	fixture.Decode(t, f.Session, &s)
	fixture.Decode(t, f.PVC, &pvc)
	fixture.Decode(t, f.PV, &pv)
	// Witness the API's already completed sticky admission, as after a guarded
	// suspend. This test exercises Reconcile, not the API admission handler.
	s.Annotations = map[string]string{homeguard.Annotation: f.Binding(t).String()}
	return &s, &pvc, &pv
}

func rootPilotClient(t *testing.T, o options, objects ...client.Object) (*controller.Reconciler, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	yaml, err := os.ReadFile("../../internal/templates/testdata/templates.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: o.templatesNamespace, Name: o.templatesName}, Data: map[string]string{templates.Key: string(yaml)}}
	if _, err := templates.Parse(cm.Data); err != nil {
		t.Fatal(err)
	}
	objects = append(objects, cm)
	cl := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&v1alpha1.AgentSession{}).WithObjects(objects...).Build()
	r := reconcilerForOptions(o) // exactly the flag-derived constructor used by run
	r.Client, r.APIReader = cl, cl
	return r, cl
}

func reconcileRootPilot(t *testing.T, r *controller.Reconciler, s *v1alpha1.AgentSession) *corev1.Pod {
	t.Helper()
	ctx := context.Background()
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(s)}); err != nil {
		t.Fatal(err)
	}
	var p corev1.Pod
	if err := r.Client.Get(ctx, client.ObjectKeyFromObject(s), &p); err != nil {
		t.Fatal("created Pod", err)
	}
	return &p
}

func rootPodWitness(t *testing.T, p *corev1.Pod) fixture.PodWitness {
	t.Helper()
	project := func(c corev1.Container) fixture.Container {
		env := map[string]string{}
		for _, e := range c.Env {
			if e.Name == protocol.SessionEnv || e.Name == homeguard.Env {
				if _, duplicate := env[e.Name]; duplicate || e.ValueFrom != nil {
					t.Fatal("ambiguous witness env")
				}
				env[e.Name] = e.Value
			}
		}
		var readOnly bool
		for _, m := range c.VolumeMounts {
			if m.Name == "home" {
				readOnly = m.ReadOnly
			}
		}
		return fixture.Container{Image: c.Image, Command: c.Command, Args: c.Args, Env: env, HomeReadOnly: readOnly}
	}
	if len(p.OwnerReferences) != 1 || len(p.Spec.Containers) != 1 || len(p.Spec.InitContainers) != 1 {
		t.Fatal("unexpected Pod shape")
	}
	w := fixture.PodWitness{Namespace: p.Namespace, Name: p.Name, SessionUID: string(p.OwnerReferences[0].UID), Main: project(p.Spec.Containers[0]), Init: project(p.Spec.InitContainers[0])}
	for _, v := range p.Spec.Volumes {
		if v.Name == "home" {
			w.HomeClaim = v.PersistentVolumeClaim.ClaimName
		}
	}
	return w
}

// Stage A: real operator flags -> the same constructor as run -> exported
// Reconcile on local fake Kubernetes objects -> committed Pod-field golden.
func TestRemotePilotRootStageA(t *testing.T) {
	f := fixture.Load(t)
	b := f.Binding(t)
	s, pvc, pv := rootPilotObjects(t)
	usage := s.DeepCopy()
	usage.Name, usage.UID, usage.Spec.Parent = f.UsageName, "offline-usage-uid", f.UsageParent
	usage.Labels = map[string]string{v1alpha1.LabelIdempotencyKey: f.UsageKey}
	usage.Annotations = nil
	task := usage.DeepCopy()
	task.Name, task.UID, task.Spec.Parent = f.TaskName, "offline-task-uid", f.TaskParent
	task.Labels = map[string]string{v1alpha1.LabelIdempotencyKey: f.TaskKey}
	task.Spec.Mode, task.Spec.Prompt = v1alpha1.ModeTask, "offline fixture"
	// Ordinary Codex task construction needs the existing explicit gate.
	managedTaskFlags := []string{"--enable-managed-codex-tasks", "--project-catalog=dev-env-system/dev-env-project-catalog", "--project-clone-owner=thaynes43"}
	taskArgs := append(rootPilotFlags(t, true), managedTaskFlags...)
	o, err := parseFlags(taskArgs)
	if err != nil {
		t.Fatal(err)
	}
	r, cl := rootPilotClient(t, o, s, pvc, pv, usage, task)
	p := reconcileRootPilot(t, r, s)
	witness := rootPodWitness(t, p)
	var renderedSession protocol.Session
	fixture.Decode(t, []byte(witness.Main.Env[protocol.SessionEnv]), &renderedSession)
	if s.Spec.Size != v1alpha1.SizeS || s.Spec.Effort != "" || s.Spec.Profile != "" || renderedSession.Effort != "" || strings.Contains(witness.Main.Env[protocol.SessionEnv], `"effort"`) {
		t.Fatal("Root size or omitted effort/profile changed")
	}
	if witness.Namespace != b.Namespace || witness.Name != b.Session || witness.SessionUID != b.SessionUID || witness.HomeClaim != b.Claim() {
		t.Fatal("HOME/session identity changed")
	}
	if !metav1.IsControlledBy(p, s) {
		t.Fatal("Pod owner changed")
	}
	if witness.Main.Image != f.RemoteImage || witness.Init.Image != f.RemoteImage || !witness.Init.HomeReadOnly || !slices.Equal(witness.Init.Args, []string{"home-verify"}) || !slices.Equal(witness.Init.Command, []string{"/usr/local/bin/agentd"}) {
		t.Fatal("selected image or init boundary changed")
	}
	if witness.Main.Env[homeguard.Env] != b.String() || witness.Init.Env[homeguard.Env] != b.String() {
		t.Fatal("binding env differs")
	}
	var afterPVC corev1.PersistentVolumeClaim
	var afterPV corev1.PersistentVolume
	var afterSession v1alpha1.AgentSession
	ctx := context.Background()
	if err := cl.Get(ctx, client.ObjectKeyFromObject(pvc), &afterPVC); err != nil {
		t.Fatal(err)
	}
	if err := cl.Get(ctx, client.ObjectKeyFromObject(pv), &afterPV); err != nil {
		t.Fatal(err)
	}
	if err := cl.Get(ctx, client.ObjectKeyFromObject(s), &afterSession); err != nil {
		t.Fatal(err)
	}
	// Ignore only fake-client assigned resourceVersion. No HOME/PV write is allowed.
	afterPVC.ResourceVersion, afterPV.ResourceVersion = pvc.ResourceVersion, pv.ResourceVersion
	if !apiequality.Semantic.DeepEqual(pvc.Spec, afterPVC.Spec) || !apiequality.Semantic.DeepEqual(pvc.Status, afterPVC.Status) || afterPVC.UID != pvc.UID || !reflect.DeepEqual(pvc.OwnerReferences, afterPVC.OwnerReferences) || !reflect.DeepEqual(pvc.Finalizers, afterPVC.Finalizers) || afterPV.UID != pv.UID || !apiequality.Semantic.DeepEqual(pv.Spec, afterPV.Spec) || !apiequality.Semantic.DeepEqual(pv.Status, afterPV.Status) || !reflect.DeepEqual(pv.Finalizers, afterPV.Finalizers) || afterSession.UID != s.UID || afterSession.Annotations[homeguard.Annotation] != b.String() {
		t.Fatal("sticky admission, original HOME or PV changed")
	}
	if got := reconcileRootPilot(t, r, usage).Spec.Containers[0].Image; got != f.UsageImage {
		t.Fatal("usage pilot displaced", got)
	}
	if got := reconcileRootPilot(t, r, task).Spec.Containers[0].Image; got != f.TaskImage {
		t.Fatal("task pilot displaced", got)
	}
	without, err := parseFlags(append(rootPilotFlags(t, false), managedTaskFlags...))
	if err != nil {
		t.Fatal(err)
	}
	// Remove both remote flags AND startup binding; admitted annotation persists.
	rs, rc, rv := rootPilotObjects(t)
	rRemoved, _ := rootPilotClient(t, without, rs, rc, rv)
	removedPod := reconcileRootPilot(t, rRemoved, rs)
	removed := rootPodWitness(t, removedPod)
	var cm corev1.ConfigMap
	if err := rRemoved.Client.Get(ctx, rRemoved.Templates, &cm); err != nil {
		t.Fatal(err)
	}
	parsedTemplates, err := templates.Parse(cm.Data)
	if err != nil {
		t.Fatal(err)
	}
	if removedPod.Spec.InitContainers[0].Name != "original-home-verify" || removed.Init.Image != parsedTemplates.Image || removed.Main.Image != parsedTemplates.Image || !slices.Equal(removed.Init.Command, []string{"/usr/local/bin/agentd"}) {
		t.Fatal("selector removal did not retain real init on templates image")
	}
	// Removing only image selection must preserve the complete main container
	// after accounting for the expected return to the templates image.
	wantMain := p.Spec.Containers[0].DeepCopy()
	wantMain.Image = parsedTemplates.Image
	if !apiequality.Semantic.DeepEqual(*wantMain, removedPod.Spec.Containers[0]) {
		t.Fatal("selector removal changed main container beyond its image")
	}
	t.Run("all pilot flags unset preserve default bytes", func(t *testing.T) {
		defaults, err := parseFlags(append([]string{"--session-namespace=" + b.Namespace}, managedTaskFlags...))
		if err != nil {
			t.Fatal(err)
		}
		if defaults.pilotAgent != nil || defaults.taskPilotAgent != nil || defaults.remotePilotAgent != nil {
			t.Fatal("pilot default enabled")
		}
		ds, dc, dv := rootPilotObjects(t)
		dr, _ := rootPilotClient(t, defaults, ds, dc, dv)
		got, err := json.Marshal(reconcileRootPilot(t, dr, ds))
		if err != nil {
			t.Fatal(err)
		}
		want, err := json.Marshal(removedPod)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatal("nonmatching pilots changed default Pod bytes")
		}
	})
	if removed.Init.Env[homeguard.Env] != b.String() || removed.Main.Env[homeguard.Env] != b.String() || !removed.Init.HomeReadOnly || !slices.Equal(removed.Init.Args, []string{"home-verify"}) || removed.Main.Image == f.RemoteImage {
		t.Fatal("selector removal lost sticky guard")
	}
	golden, err := json.MarshalIndent(fixture.Golden{Selected: witness, SelectorRemoved: removed}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden = append(golden, '\n')
	if !bytes.Equal(golden, fixture.Read(t, "root-pod.golden.json")) {
		t.Fatalf("stage A differs from committed link to stage B:\n%s", golden)
	}
}

func TestRemotePilotRootStageANegatives(t *testing.T) {
	f := fixture.Load(t)
	for _, name := range []string{"Session UID mismatch", "PVC UID mismatch", "PV UID mismatch", "Session deletion path", "PVC deleting", "same-name replaced Session", "forbidden PV read", "parent mismatch", "key mismatch"} {
		t.Run(name, func(t *testing.T) {
			s, pvc, pv := rootPilotObjects(t)
			switch name {
			case "Session UID mismatch":
				s.UID = "mismatched-session"
			case "PVC UID mismatch":
				pvc.UID = "mismatched-home"
			case "PV UID mismatch":
				pv.UID = "mismatched-pv"
			case "Session deletion path":
				now := metav1.Now()
				s.DeletionTimestamp = &now
			case "PVC deleting":
				now := metav1.Now()
				pvc.DeletionTimestamp = &now
			case "parent mismatch":
				s.Spec.Parent += "-other"
			case "key mismatch":
				s.Labels[v1alpha1.LabelIdempotencyKey] += "-other"
			}
			o, err := parseFlags(rootPilotFlags(t, true))
			if err != nil {
				t.Fatal(err)
			}
			r, cl := rootPilotClient(t, o, s, pvc, pv)
			r.APIReader = interceptor.NewClient(cl.(client.WithWatch), interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*corev1.PersistentVolume); ok && name == "forbidden PV read" {
					return apierrors.NewForbidden(corev1.Resource("persistentvolumes"), key.Name, errors.New("offline denied"))
				}
				if err := c.Get(ctx, key, obj, opts...); err != nil {
					return err
				}
				if ss, ok := obj.(*v1alpha1.AgentSession); ok && name == "same-name replaced Session" {
					ss.UID = "replaced-live-session"
				}
				return nil
			}})
			_, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(s)})
			if name == "forbidden PV read" {
				if !apierrors.IsForbidden(err) {
					t.Fatal("PV denial lost", err)
				}
				if !strings.Contains(err.Error(), "persistentvolumes") || !strings.Contains(err.Error(), "offline denied") {
					t.Fatal("wrong forbidden read caused refusal", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			var after v1alpha1.AgentSession
			if err := cl.Get(context.Background(), client.ObjectKeyFromObject(s), &after); err != nil {
				t.Fatal(err)
			}
			blocked := meta.FindStatusCondition(after.Status.Conditions, controller.ConditionRemovalBlocked)
			if name == "parent mismatch" || name == "key mismatch" {
				if blocked != nil {
					t.Fatal("selector mismatch incorrectly blocked guarded default", blocked)
				}
			} else if name != "forbidden PV read" {
				reason, prefix := "SuspendNeedsRescue", "suspend waits: "
				if name == "Session deletion path" {
					reason, prefix = "DeleteNeedsRescue", "delete is a reap and waits: "
				}
				if blocked == nil || blocked.Status != metav1.ConditionTrue || blocked.Reason != reason || blocked.Message != prefix+homeguard.ErrUnsafe.Error() {
					t.Fatalf("wrong preservation refusal: %+v", blocked)
				}
			}
			var pods corev1.PodList
			if err := cl.List(context.Background(), &pods); err != nil {
				t.Fatal(err)
			}
			if name == "parent mismatch" || name == "key mismatch" {
				if len(pods.Items) != 1 || pods.Items[0].Spec.Containers[0].Image == f.RemoteImage || len(pods.Items[0].Spec.InitContainers) != 1 {
					t.Fatal("mismatch selected pilot or lost guard")
				}
			} else if len(pods.Items) != 0 {
				t.Fatal("unsafe identity mounted HOME")
			}
		})
	}
	t.Run("binding namespace mismatch both orders", func(t *testing.T) {
		for _, first := range []bool{false, true} {
			args := rootPilotFlags(t, true)
			if first {
				args = append([]string{"--session-namespace=other"}, args[1:]...)
			} else {
				args = append(args, "--session-namespace=other")
			}
			if _, err := parseFlags(args); err == nil || !strings.Contains(err.Error(), "namespace must match") {
				t.Fatal("namespace mismatch accepted", err)
			}
		}
	})
}
