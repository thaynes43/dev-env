package broker

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clocktesting "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

func maintenanceFixture(t *testing.T) (*Broker, *v1alpha1.AccessGrant) {
	t.Helper()
	now := time.Date(2026, 10, 30, 17, 0, 0, 0, time.UTC)
	g := &v1alpha1.AccessGrant{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: "grant-maintenance", UID: "grant-uid", CreationTimestamp: metav1.NewTime(now)}, Spec: v1alpha1.AccessGrantSpec{Type: v1alpha1.GrantMaintenance, Requester: v1alpha1.GrantRequester{Session: "parent", SessionUID: "parent-uid"}, TTL: metav1.Duration{Duration: 30 * time.Minute}, Reason: "bounded maintenance", Maintenance: &v1alpha1.MaintenanceTask{
		Coordinator: v1alpha1.MaintenanceIdentity{Name: "parent", UID: "parent-uid", PodUID: "parent-pod"}, Workers: []v1alpha1.MaintenanceIdentity{{Name: "worker", UID: "worker-uid", PodUID: "worker-pod"}}, Node: "worker-node", NodeUID: "node-uid", Resource: "device-identity", Operation: "device administration", Capabilities: []string{"SYS_ADMIN"}, Devices: []string{"/dev/nvidia0"}, Image: "registry.example/tool@sha256:" + strings.Repeat("a", 64), CPUMillis: 100, MemoryMiB: 128, Restore: "restore and read back authorized prior value",
	}}}
	objects := []client.Object{g, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-node", UID: "node-uid"}}}
	for _, id := range append([]v1alpha1.MaintenanceIdentity{g.Spec.Maintenance.Coordinator}, g.Spec.Maintenance.Workers...) {
		s := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: id.Name, UID: types.UID(id.UID)}}
		if id.Name == "worker" {
			s.Spec.Parent = "parent"
		}
		p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: id.Name, UID: types.UID(id.PodUID), OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(s, v1alpha1.GroupVersion.WithKind("AgentSession"))}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
		objects = append(objects, s, p)
	}
	localScheme := runtime.NewScheme()
	if err := errors.Join(v1alpha1.AddToScheme(localScheme), corev1.AddToScheme(localScheme), networkingv1.AddToScheme(localScheme), coordinationv1.AddToScheme(localScheme)); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(localScheme).WithStatusSubresource(&v1alpha1.AccessGrant{}, &corev1.Pod{}).WithObjects(objects...).Build()
	b := &Broker{Client: c, APIReader: c, SessionNamespace: sessionNS, PolicyNamespace: systemNS, EnableMaintenanceGrants: true, Clock: clocktesting.NewFakeClock(now)}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(g), g); err != nil {
		t.Fatal(err)
	}
	return b, g
}

func TestMaintenanceEnvelope(t *testing.T) {
	_, base := maintenanceFixture(t)
	if err := ValidateMaintenance(base); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*v1alpha1.AccessGrant){
		"target UID":       func(g *v1alpha1.AccessGrant) { g.Spec.Maintenance.NodeUID = "" },
		"capability":       func(g *v1alpha1.AccessGrant) { g.Spec.Maintenance.Capabilities = []string{"SYS_PTRACE"} },
		"mount":            func(g *v1alpha1.AccessGrant) { g.Spec.Maintenance.ReadOnlyMounts = []string{"/var/lib/kubelet"} },
		"device directory": func(g *v1alpha1.AccessGrant) { g.Spec.Maintenance.Devices = []string{"/dev"} },
		"device traversal": func(g *v1alpha1.AccessGrant) { g.Spec.Maintenance.Devices = []string{"/dev/../dev/nvidia0"} },
		"cpu":              func(g *v1alpha1.AccessGrant) { g.Spec.Maintenance.CPUMillis = 3000 },
		"ttl":              func(g *v1alpha1.AccessGrant) { g.Spec.TTL.Duration = 2 * time.Hour },
		"coordinator":      func(g *v1alpha1.AccessGrant) { g.Spec.Maintenance.Coordinator.UID = "forged" },
		"worker duplicate": func(g *v1alpha1.AccessGrant) {
			g.Spec.Maintenance.Workers = append(g.Spec.Maintenance.Workers, g.Spec.Maintenance.Workers[0])
		},
		"image":       func(g *v1alpha1.AccessGrant) { g.Spec.Maintenance.Image = "tool:latest" },
		"other grant": func(g *v1alpha1.AccessGrant) { g.Spec.Kube = &v1alpha1.KubeGrant{Role: v1alpha1.RoleNodes} },
	} {
		t.Run(name, func(t *testing.T) {
			g := base.DeepCopy()
			mut(g)
			if ValidateMaintenance(g) == nil {
				t.Fatal("unsafe envelope accepted")
			}
			if MaintenanceDigest(g) == MaintenanceDigest(base) {
				t.Fatal("mutation not bound by digest")
			}
		})
	}
}

func TestMaintenanceIdentitiesAndActor(t *testing.T) {
	for _, which := range []string{"parent UID", "worker UID", "worker parent", "worker pod", "node UID"} {
		t.Run(which, func(t *testing.T) {
			b, g := maintenanceFixture(t)
			ctx := context.Background()
			if err := CheckMaintenanceIdentities(ctx, b.APIReader, sessionNS, g); err != nil {
				t.Fatal(err)
			}
			switch which {
			case "parent UID":
				g.Spec.Requester.SessionUID = "new"
				g.Spec.Maintenance.Coordinator.UID = "new"
			case "worker UID":
				g.Spec.Maintenance.Workers[0].UID = "new"
			case "worker pod":
				g.Spec.Maintenance.Workers[0].PodUID = "new"
			case "node UID":
				g.Spec.Maintenance.NodeUID = "new"
			case "worker parent":
				var s v1alpha1.AgentSession
				if err := b.Client.Get(ctx, client.ObjectKey{Namespace: sessionNS, Name: "worker"}, &s); err != nil {
					t.Fatal(err)
				}
				s.Spec.Parent = "other"
				if err := b.Client.Update(ctx, &s); err != nil {
					t.Fatal(err)
				}
			}
			if CheckMaintenanceIdentities(ctx, b.APIReader, sessionNS, g) == nil {
				t.Fatal("replaced binding accepted")
			}
		})
	}
	b, g := maintenanceFixture(t)
	approve(g, "human/dev-env-system/dev-env-human", g.Spec.TTL.Duration, b.now())
	if err := MaintenanceActor(g, "worker", "worker-uid", "worker-pod", b.now().Time); err != nil {
		t.Fatal(err)
	}
	for _, id := range []v1alpha1.MaintenanceIdentity{{Name: "other", UID: "worker-uid", PodUID: "worker-pod"}, {Name: "worker", UID: "replaced", PodUID: "worker-pod"}, {Name: "worker", UID: "worker-uid", PodUID: "replaced"}} {
		if MaintenanceActor(g, id.Name, id.UID, id.PodUID, b.now().Time) == nil {
			t.Fatal("unapproved actor accepted")
		}
	}
	if MaintenanceActor(g, "worker", "worker-uid", "worker-pod", g.Status.ExpiresAt.Time) == nil {
		t.Fatal("expired lease accepted")
	}
	g.Spec.Release = true
	if MaintenanceActor(g, "worker", "worker-uid", "worker-pod", b.now().Time) == nil {
		t.Fatal("released lease accepted")
	}
}

func TestMaintenanceDecisionReplayAndDefaultOff(t *testing.T) {
	b, g := maintenanceFixture(t)
	ctx := context.Background()
	d := MaintenanceDecision{GrantUID: string(g.UID), Digest: MaintenanceDigest(g), By: "human/dev-env-system/dev-env-human", Approve: true, At: b.now().Time}
	raw, _ := json.Marshal(d)
	g.Annotations = map[string]string{MaintenanceDecisionAnnotation: string(raw)}
	if err := b.Client.Update(ctx, g); err != nil {
		t.Fatal(err)
	}
	if handled, err := b.maintenanceDecision(ctx, g); !handled || err != nil {
		t.Fatal(handled, err)
	}
	if _, err := b.maintenanceDecision(ctx, g); !errors.Is(err, ErrNotPending) {
		t.Fatal("double decision", err)
	}
	for _, mut := range []func(*MaintenanceDecision){func(d *MaintenanceDecision) { d.GrantUID = "replaced" }, func(d *MaintenanceDecision) { d.Digest = "stale" }, func(d *MaintenanceDecision) { d.By = "coordinator:claim" }, func(d *MaintenanceDecision) { d.At = d.At.Add(time.Hour) }} {
		bad := d
		mut(&bad)
		raw, _ := json.Marshal(bad)
		g.Annotations[MaintenanceDecisionAnnotation] = string(raw)
		if _, err := b.maintenanceDecision(ctx, g); !errors.Is(err, ErrRefused) {
			t.Fatal("bad decision", err)
		}
	}
	b, g = maintenanceFixture(t)
	b.EnableMaintenanceGrants = false
	if err := b.Decide(ctx, g.Name, Decision{Approve: true, By: d.By, MaintenanceUID: string(g.UID), MaintenanceDigest: MaintenanceDigest(g)}); !errors.Is(err, ErrRefused) {
		t.Fatal("off accepted", err)
	}
}

func TestMaintenanceExecutorStopAndEvidence(t *testing.T) {
	b, g := maintenanceFixture(t)
	ctx := context.Background()
	approve(g, "human/dev-env-system/dev-env-human", g.Spec.TTL.Duration, b.now())
	if err := b.Client.Status().Update(ctx, g); err != nil {
		t.Fatal(err)
	}
	p := MaintenancePod(g, b.now().Time)
	p.UID = "executor-uid"
	p.Status.Phase = corev1.PodRunning
	if p.Spec.HostNetwork || p.Spec.HostPID || *p.Spec.AutomountServiceAccountToken || p.Spec.Containers[0].SecurityContext == nil {
		t.Fatal("unsafe executor")
	}
	if err := b.Client.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, err := b.activeMaintenance(ctx, g, b.now()); err != nil {
		t.Fatal(err)
	}
	g.Spec.Release = true
	if err := b.stopMaintenance(ctx, g); !errors.Is(err, errCredentialCleanupPending) {
		t.Fatal(err)
	}
	if g.Status.Maintenance.Executor != "StopPending" {
		t.Fatal(g.Status)
	}
	if err := b.Client.Get(ctx, client.ObjectKeyFromObject(p), p); err != nil {
		t.Fatal(err)
	}
	if *p.Spec.ActiveDeadlineSeconds != 1 {
		t.Fatal("stop did not shorten deadline")
	}
	// Unreachable node/read failure must remain visible, not imply termination.
	base := b.Client.(client.WithWatch)
	b.APIReader = interceptor.NewClient(base, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, k client.ObjectKey, o client.Object, opts ...client.GetOption) error {
		if _, ok := o.(*corev1.Pod); ok {
			return errors.New("node observation unavailable")
		}
		return c.Get(ctx, k, o, opts...)
	}})
	if b.stopMaintenance(ctx, g) == nil || g.Status.Maintenance.Executor != "Unknown" {
		t.Fatal("unreachable termination claimed")
	}
	b.APIReader = base
	p.Status.Phase = corev1.PodFailed
	// A terminal phase alone is insufficient, especially for an unreachable node.
	if err := b.Client.Status().Update(ctx, p); err != nil {
		t.Fatal(err)
	}
	if b.stopMaintenance(ctx, g) == nil {
		t.Fatal("terminal phase without container/node proof accepted")
	}
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "maintenance", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{FinishedAt: metav1.NewTime(b.Clock.Now())}}}}
	var node corev1.Node
	if err := b.Client.Get(ctx, client.ObjectKey{Name: g.Spec.Maintenance.Node}, &node); err != nil {
		t.Fatal(err)
	}
	node.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}
	if err := b.Client.Status().Update(ctx, &node); err != nil {
		t.Fatal(err)
	}
	lease := &coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{Namespace: "kube-node-lease", Name: node.Name, OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Node", Name: node.Name, UID: node.UID}}}, Spec: coordinationv1.LeaseSpec{HolderIdentity: ptr.To(node.Name), RenewTime: &metav1.MicroTime{Time: b.Clock.Now()}}}
	if err := b.Client.Create(ctx, lease); err != nil {
		t.Fatal(err)
	}
	if err := b.Client.Status().Update(ctx, p); err != nil {
		t.Fatal(err)
	}
	g.Annotations = map[string]string{MaintenanceRestoreAnnotation: "actor=worker success=false report=readback failed"}
	if err := b.stopMaintenance(ctx, g); err != nil {
		t.Fatal(err)
	}
	if g.Status.Maintenance.Executor != "Terminated" || !strings.Contains(g.Status.Maintenance.RestoreEvidence, "success=false") {
		t.Fatal(g.Status)
	}
	if err := b.stopMaintenance(ctx, g); err != nil {
		t.Fatal("idempotent cleanup", err)
	}
}

func TestMaintenanceStopPatchIsExact(t *testing.T) {
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "executor-uid", ResourceVersion: "17"}}
	var ops []map[string]any
	if err := json.Unmarshal(StopMaintenancePatch(p), &ops); err != nil {
		t.Fatal(err)
	}
	want := []map[string]any{{"op": "test", "path": "/metadata/uid", "value": "executor-uid"}, {"op": "test", "path": "/metadata/resourceVersion", "value": "17"}, {"op": "add", "path": "/spec/activeDeadlineSeconds", "value": float64(1)}}
	if !reflect.DeepEqual(ops, want) {
		t.Fatal(ops)
	}
}
