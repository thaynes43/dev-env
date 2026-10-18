package taskbudget

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/thaynes43/dev-env/internal/agentd/hostexecutor"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// Unit observations exercise policy/stage refusals only. The API vertical test
// separately obtains actual kernel peer and native RPC proof; this is not that.
type policyProof struct {
	proof  hostexecutor.Observation
	err    error
	during func()
}

func (p *policyProof) Observe(_ context.Context, _ client.ObjectKey, _ string, b Binding, homeUID, baseline string) (int, error) {
	if p.during != nil {
		p.during()
	}
	return hostexecutor.EvidenceExit(p.proof, hostexecutor.Binding{TaskUID: b.TaskUID, HostID: b.HostID, PodUID: b.PodUID, Epoch: b.Epoch, Deadline: b.Deadline}, homeUID, baseline), p.err
}

type rootPolicyFixture struct {
	inspector *OwnedRootInspector
	ledger    *Ledger
	pod       *corev1.Pod
	home      *corev1.PersistentVolumeClaim
	net       *unstructured.Unstructured
	cm        *corev1.ConfigMap
	probe     *policyProof
	live      client.Client
	now       time.Time
}

func newRootPolicyFixture(t *testing.T) *rootPolicyFixture {
	t.Helper()
	_, _, _, scheme, now := fixtureAdmissionObjects(t)
	p := OwnedRootPolicy{Version: 1, Namespace: "dev-agents", Name: "owned-root", ServiceAccount: "owned-root", TaskUID: "campaign", HostID: "native-host", Image: fixtureTestImage, HomeClaim: "native-home", HomeUID: "home-original", Baseline: strings.Repeat("a", 64), NetworkPolicyName: "native-policy", NetworkPolicyUID: "policy-original", Spec: Spec{SuccessCondition: "bounded source", OverallSeconds: 120, EffortSeconds: 180, CheckpointSeconds: 30}}
	network := NativeFixturePolicy()
	network.SetName(p.NetworkPolicyName)
	network.SetUID("policy-original")
	network.SetResourceVersion("1")
	p.NetworkPolicySHA = OwnedPolicyDigest(network.Object["spec"])
	pod := OwnedRootPod(p)
	pod.UID = "pod-original"
	pod.ResourceVersion = "1"
	pod.CreationTimestamp = metav1.NewTime(now.Add(-time.Second))
	pod.Spec.NodeName = "worker"
	pod.Status.Phase = corev1.PodRunning
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: OwnedRootContainer, Ready: true, ImageID: p.Image, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: pod.CreationTimestamp}}}}
	home := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: p.HomeClaim, Namespace: p.Namespace, UID: "home-original", ResourceVersion: "1"}, Spec: corev1.PersistentVolumeClaimSpec{AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOncePod}, VolumeName: "original-volume"}, Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound}}
	raw, _ := json.Marshal(p)
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "dev-env-system", Name: "root-policy", UID: "config-original", ResourceVersion: "1"}, Data: map[string]string{OwnedRootPolicyKey: string(raw)}}
	ledger := &Ledger{Version: 1, Binding: Binding{TaskUID: p.TaskUID, HostID: p.HostID, PodUID: string(pod.UID), Epoch: 1, Deadline: now.Add(120 * time.Second)}, CreatedAt: now, Parent: p.Namespace + "/" + p.ServiceAccount, Spec: p.Spec}
	probe := &policyProof{proof: hostexecutor.Observation{Version: 1, Binding: hostexecutor.Binding{TaskUID: ledger.Binding.TaskUID, HostID: ledger.Binding.HostID, PodUID: ledger.Binding.PodUID, Epoch: 1, Deadline: ledger.Binding.Deadline}, HomeUID: p.HomeUID, Baseline: p.Baseline, Stage: "Prepared"}}
	live := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod, home, network, cm).Build()
	inspector := &OwnedRootInspector{Reader: live, Policy: client.ObjectKeyFromObject(cm), Probe: probe, Now: func() time.Time { return now }, AssignedHost: p.HostID, AssignedTask: p.TaskUID, AssignedParent: ledger.Parent, AssignedPod: p.Name}
	return &rootPolicyFixture{inspector, ledger, pod, home, network, cm, probe, live, now}
}
func TestOwnedRootPolicyStagesRefuseWorkBeforeReady(t *testing.T) {
	f := newRootPolicyFixture(t)
	ctx := context.Background()
	if err := f.inspector.InspectNativeAdmission(ctx, f.ledger, f.ledger.Binding); err != nil {
		t.Fatal(err)
	}
	f.ledger.Workers = []Worker{{ID: "host:native-host:1", Active: true}}
	for _, stage := range []string{"Prepared", "Starting"} {
		f.probe.proof.Stage = stage
		if !errors.Is(f.inspector.InspectNativeContinuation(ctx, f.ledger, f.ledger.Binding), ErrNativeStarting) {
			t.Fatal("startup became work authority")
		}
	}
	f.probe.proof.Stage = "Ready"
	f.probe.proof.Root = hostexecutor.Identity{PID: 3, StartTicks: 1, BootID: "fixture"}
	f.probe.proof.NativeVersion = hostexecutor.PinnedNativeVersion
	if err := f.inspector.InspectNativeContinuation(ctx, f.ledger, f.ledger.Binding); err != nil {
		t.Fatal(err)
	}
	f.probe.proof.NativeVersion = "wrong"
	if err := f.inspector.InspectNativeContinuation(ctx, f.ledger, f.ledger.Binding); err == nil {
		t.Fatal("wrong version")
	}
}
func TestOwnedRootPolicyRejectsIdentityCapabilityAndPolicyDrift(t *testing.T) {
	for _, scenario := range []string{"other-task", "epoch", "deadline", "wrong-assignment", "wrong-pod-assignment", "home-replaced", "rwo-shared", "unbound-home", "sidecar", "host-pid", "extra-env", "restart", "image", "network-changed", "network-changed-during-probe", "home-changed-during-probe", "unknown-probe", "wrong-home-proof", "wrong-baseline", "wrong-binding", "worker-root-claim"} {
		t.Run(scenario, func(t *testing.T) {
			f := newRootPolicyFixture(t)
			ctx := context.Background()
			update := func(o client.Object) {
				t.Helper()
				if err := f.live.Update(ctx, o); err != nil {
					t.Fatal(err)
				}
			}
			switch scenario {
			case "other-task":
				f.ledger.Binding.TaskUID = "other"
			case "epoch":
				f.ledger.Binding.Epoch++
			case "deadline":
				f.ledger.Binding.Deadline = f.ledger.Binding.Deadline.Add(time.Second)
			case "wrong-assignment":
				f.inspector.AssignedHost = "other"
			case "wrong-pod-assignment":
				f.inspector.AssignedPod = "other"
			case "home-replaced":
				f.home.UID = "other"
				update(f.home)
			case "rwo-shared":
				f.home.Spec.AccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}
				update(f.home)
			case "unbound-home":
				f.home.Spec.VolumeName = ""
				update(f.home)
			case "sidecar":
				f.pod.Spec.Containers = append(f.pod.Spec.Containers, corev1.Container{Name: "other"})
				update(f.pod)
			case "host-pid":
				f.pod.Spec.HostPID = true
				update(f.pod)
			case "extra-env":
				f.pod.Spec.Containers[0].Env = append(f.pod.Spec.Containers[0].Env, corev1.EnvVar{Name: "LD_PRELOAD", Value: "untrusted"})
				update(f.pod)
			case "restart":
				f.pod.Status.ContainerStatuses[0].RestartCount = 1
				if err := f.live.Status().Update(ctx, f.pod); err != nil {
					t.Fatal(err)
				}
			case "image":
				f.pod.Spec.Containers[0].Image = "wrong"
				update(f.pod)
			case "network-changed":
				delete(f.net.Object["spec"].(map[string]any), "egressDeny")
				update(f.net)
			case "network-changed-during-probe":
				f.probe.during = func() { delete(f.net.Object["spec"].(map[string]any), "egressDeny"); update(f.net) }
			case "home-changed-during-probe":
				f.probe.during = func() { f.home.Spec.VolumeName = "other"; update(f.home) }
			case "unknown-probe":
				f.probe.err = errors.New("lost proof")
			case "wrong-home-proof":
				f.probe.proof.HomeUID = "other"
			case "wrong-baseline":
				f.probe.proof.Baseline = strings.Repeat("b", 64)
			case "wrong-binding":
				f.probe.proof.Binding.Epoch++
			case "worker-root-claim":
				f.probe.proof.Root.PID = 3
			}
			if err := f.inspector.InspectNativeAdmission(ctx, f.ledger, f.ledger.Binding); err == nil {
				t.Fatal("unsafe admission")
			}
		})
	}
}
func TestOwnedRootAuthorityLossBeforeLaunchLatchesWithoutReservation(t *testing.T) {
	for _, stage := range []string{"Starting", "Stopped", "unknown"} {
		t.Run(stage, func(t *testing.T) {
			f := newRootPolicyFixture(t)
			ctx := context.Background()
			store := &memoryStore{}
			f.ledger.AccountedAt = f.now
			f.ledger.LastProgress = f.now
			f.ledger.StallAnchor = f.now
			f.ledger.NextCheckpoint = f.now.Add(30 * time.Second)
			f.ledger.CheckpointSeconds = 30
			f.ledger.EffortCeilingMilliseconds = 180000
			f.ledger.Workers = []Worker{{ID: "host:native-host:1", Active: true}, {ID: "child:once", Active: true, Managed: &ManagedWorkerRef{Namespace: "dev-agents", Name: "child", SessionUID: "original"}}}
			if err := store.Create(ctx, f.ledger); err != nil {
				t.Fatal(err)
			}
			f.probe.proof.Stage = stage
			service := &Service{Store: store, NativeInspector: f.inspector, ManagedInspector: allowManagedFixture{}, Now: func() time.Time { return f.now }}
			if _, err := service.ReserveManagedLaunch(ctx, f.ledger.Binding, "child:once"); err == nil {
				t.Fatal("launch without fresh Ready")
			}
			ledger, _, err := store.Read(ctx, "campaign")
			if err != nil || ledger.Workers[1].Managed.LaunchRequested {
				t.Fatal("lost readiness consumed launch")
			}
			if ledger.Latched != (stage != "Starting") {
				t.Fatal("expected startup versus authority-loss latch", ledger.Reason)
			}
		})
	}
}

type allowManagedFixture struct{}

func (allowManagedFixture) InspectManagedAdmission(context.Context, *Ledger, ManagedWorkerRef) error {
	return nil
}
func TestOwnedRootStopProofRequiresWholeOriginalTree(t *testing.T) {
	f := newRootPolicyFixture(t)
	f.ledger.Workers = []Worker{{ID: "host:native-host:1", Active: true}}
	event := apiv1.TaskBudgetEvent{Binding: f.ledger.Binding, ID: "owned-stop:pod-original", Kind: "worker-stop", WorkerID: "host:native-host:1", Evidence: apiv1.TaskBudgetEvidence{ID: "owned-stop:pod-original", Kind: "owned-root-stop", Reference: "pod-original"}}
	f.probe.proof.Stage = "Stopped"
	f.probe.proof.Root.PID = 3
	if f.inspector.ValidateStop(context.Background(), f.ledger, event) == nil {
		t.Fatal("missing Wait/ECHILD accepted")
	}
	f.probe.proof.RootWaitObserved = true
	f.probe.proof.TreeReaped = true
	if err := f.inspector.ValidateStop(context.Background(), f.ledger, event); err != nil {
		t.Fatal(err)
	}
	event.Evidence.Reference = "replacement"
	if f.inspector.ValidateStop(context.Background(), f.ledger, event) == nil {
		t.Fatal("foreign stop accepted")
	}
}

type advancingRootInspector struct {
	now     *time.Time
	advance time.Duration
}

func (a advancingRootInspector) InspectNativeAdmission(context.Context, *Ledger, Binding) error {
	return nil
}
func (a advancingRootInspector) InspectNativeContinuation(context.Context, *Ledger, Binding) error {
	*a.now = a.now.Add(a.advance)
	return nil
}
func TestOwnedRootDispatchRechecksClockAfterReadiness(t *testing.T) {
	for _, scenario := range []struct {
		name                         string
		overall, checkpoint, advance time.Duration
	}{{"checkpoint", 2 * time.Minute, 30 * time.Second, 31 * time.Second}, {"deadline", 30 * time.Second, 30 * time.Second, 31 * time.Second}} {
		t.Run(scenario.name, func(t *testing.T) {
			s, store, now, b := setup(t, scenario.overall, 3*time.Minute, scenario.checkpoint)
			if _, err := s.Admit(context.Background(), b); err != nil {
				t.Fatal(err)
			}
			s.NativeInspector = advancingRootInspector{now, scenario.advance}
			_, err := s.Record(context.Background(), apiv1.TaskBudgetEvent{Binding: b, ID: "dispatch", Kind: "worker-start", WorkerID: "child:once"})
			if !errors.Is(err, ErrDenied) {
				t.Fatal("expired readiness reserved child", err)
			}
			ledger, _, err := store.Read(context.Background(), b.TaskUID)
			if err != nil || !ledger.Latched || len(ledger.Workers) != 1 {
				t.Fatal("expired reservation changed workers", err)
			}
		})
	}
}

func TestOwnedContinuationRequiresFrozenInputsAndOriginalLiveGuard(t *testing.T) {
	f := newRootPolicyFixture(t)
	ctx := context.Background()
	var p OwnedRootPolicy
	_ = json.Unmarshal([]byte(f.cm.Data[OwnedRootPolicyKey]), &p)
	p.AllowContinuation = true
	if p.Valid() {
		t.Fatal("continuation accepted unbound startup inputs")
	}
	inputs := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: p.Namespace, Name: "home-inputs", UID: "inputs-original"}, Data: map[string]string{"input.json": "fixture", "requirements.toml": "fixture"}}
	if err := f.live.Create(ctx, inputs); err != nil {
		t.Fatal(err)
	}
	p.HomeInputsName = inputs.Name
	p.HomeInputsUID = string(inputs.UID)
	p.HomeInputsSHA = OwnedPolicyDigest(inputs.Data)
	raw, _ := json.Marshal(p)
	f.cm.Data[OwnedRootPolicyKey] = string(raw)
	if err := f.live.Update(ctx, f.cm); err != nil {
		t.Fatal(err)
	}
	pod := OwnedRootPod(p)
	pod.UID = f.pod.UID
	pod.CreationTimestamp = f.pod.CreationTimestamp
	pod.Spec.NodeName = f.pod.Spec.NodeName
	pod.Status = f.pod.Status
	pod.ResourceVersion = f.pod.ResourceVersion
	if err := f.live.Update(ctx, pod); err != nil {
		t.Fatal(err)
	}
	f.probe.proof.Stage = "Stopped"
	f.probe.proof.Root.PID = 3
	f.probe.proof.RootWaitObserved = true
	f.probe.proof.TreeReaped = true
	if f.inspector.AuthorizeNativeContinue(ctx, f.ledger, f.ledger.Binding) == nil {
		t.Fatal("startup drift became continuation")
	}
	f.probe.proof.ContinuationReady = true
	if err := f.inspector.AuthorizeNativeContinue(ctx, f.ledger, f.ledger.Binding); err != nil {
		t.Fatal("valid frozen continuation", err)
	}
	inputs.Data["input.json"] = "changed"
	if err := f.live.Update(ctx, inputs); err != nil {
		t.Fatal(err)
	}
	if f.inspector.AuthorizeNativeContinue(ctx, f.ledger, f.ledger.Binding) == nil {
		t.Fatal("changed policy inputs accepted")
	}
}
