package controller

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/templates"
)

type workspaceStopFixture struct {
	s     *v1alpha1.AgentSession
	pod   *corev1.Pod
	node  *corev1.Node
	lease *coordinationv1.Lease
	home  *corev1.PersistentVolumeClaim
	now   time.Time
}

func stoppedWorkspaceFixture() workspaceStopFixture {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	s := taskSession()
	s.Generation = 3
	s.Spec.Workspace = &v1alpha1.WorkspaceSpec{ID: "projects-v2"}
	s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
	s.Status.PodName = s.Name
	s.Status.SharedPrivateHomeUID = "home-1"
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: s.Name, Namespace: s.Namespace, UID: "executor-1", ResourceVersion: "10", OwnerReferences: []metav1.OwnerReference{ownerRef(s)}},
		Spec: corev1.PodSpec{NodeName: "worker-a", RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{{Name: ContainerName}}, InitContainers: []corev1.Container{{Name: "init"}},
			EphemeralContainers: []corev1.EphemeralContainer{{EphemeralContainerCommon: corev1.EphemeralContainerCommon{Name: "debug"}}}},
		Status: corev1.PodStatus{Phase: corev1.PodSucceeded}}
	terminated := func(name string) corev1.ContainerStatus {
		return corev1.ContainerStatus{Name: name, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{FinishedAt: metav1.NewTime(now.Add(-time.Second))}}}
	}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{terminated(ContainerName)}
	pod.Status.InitContainerStatuses = []corev1.ContainerStatus{terminated("init")}
	pod.Status.EphemeralContainerStatuses = []corev1.ContainerStatus{terminated("debug")}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-a", UID: "node-1", ResourceVersion: "20"}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
	lease := &coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{Name: node.Name, Namespace: "kube-node-lease", UID: "lease-1", ResourceVersion: "30",
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Node", Name: node.Name, UID: node.UID}}},
		Spec: coordinationv1.LeaseSpec{HolderIdentity: ptr.To(node.Name), RenewTime: &metav1.MicroTime{Time: now.Add(-time.Second)}}}
	home := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: HomeClaimName(s.Name), Namespace: s.Namespace, UID: "home-1", OwnerReferences: []metav1.OwnerReference{ownerRef(s)}, Finalizers: []string{Finalizer}}}
	return workspaceStopFixture{s, pod, node, lease, home, now}
}

func (f workspaceStopFixture) client(extra ...client.Object) client.Client {
	objects := []client.Object{f.s, f.pod, f.node, f.lease, f.home}
	objects = append(objects, extra...)
	return fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&v1alpha1.AgentSession{}, &corev1.Pod{}, &corev1.Node{}).WithObjects(objects...).Build()
}

func TestWorkspaceStopVerifierRequiresExactTerminationAndHealthyNode(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*workspaceStopFixture)
	}{
		{"valid", func(*workspaceStopFixture) {}},
		{"different UID", func(f *workspaceStopFixture) { f.pod.UID = "replacement" }},
		{"different owner", func(f *workspaceStopFixture) { f.pod.OwnerReferences[0].UID = "other-session" }},
		{"deleting", func(f *workspaceStopFixture) {
			at := metav1.NewTime(f.now)
			f.pod.DeletionTimestamp = &at
			f.pod.Finalizers = []string{"keep"}
		}},
		{"Always", func(f *workspaceStopFixture) { f.pod.Spec.RestartPolicy = corev1.RestartPolicyAlways }},
		{"phase alone", func(f *workspaceStopFixture) {
			f.pod.Status.ContainerStatuses[0].State = corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}
		}},
		{"missing init", func(f *workspaceStopFixture) { f.pod.Status.InitContainerStatuses = nil }},
		{"missing ephemeral", func(f *workspaceStopFixture) { f.pod.Status.EphemeralContainerStatuses = nil }},
		{"zero FinishedAt", func(f *workspaceStopFixture) {
			f.pod.Status.EphemeralContainerStatuses[0].State.Terminated.FinishedAt = metav1.Time{}
		}},
		{"duplicate status", func(f *workspaceStopFixture) {
			f.pod.Status.ContainerStatuses = append(f.pod.Status.ContainerStatuses, f.pod.Status.ContainerStatuses[0])
		}},
		{"restartable container", func(f *workspaceStopFixture) {
			f.pod.Spec.InitContainers[0].RestartPolicy = ptr.To(corev1.ContainerRestartPolicyAlways)
		}},
		{"node unknown", func(f *workspaceStopFixture) { f.node.Status.Conditions[0].Status = corev1.ConditionUnknown }},
		{"node not ready", func(f *workspaceStopFixture) { f.node.Status.Conditions[0].Status = corev1.ConditionFalse }},
		{"stale lease", func(f *workspaceStopFixture) {
			f.lease.Spec.RenewTime.Time = f.now.Add(-40*time.Second - time.Nanosecond)
		}},
		{"future lease", func(f *workspaceStopFixture) { f.lease.Spec.RenewTime.Time = f.now.Add(time.Second) }},
		{"missing renewal", func(f *workspaceStopFixture) { f.lease.Spec.RenewTime = nil }},
		{"foreign lease", func(f *workspaceStopFixture) { f.lease.OwnerReferences[0].UID = "another-node" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := stoppedWorkspaceFixture()
			tc.edit(&f)
			v := PodWorkspaceStopVerifier{Reader: f.client(), Now: func() time.Time { return f.now }}
			proof, old, err := v.Verify(context.Background(), f.s, "executor-1")
			if tc.name != "valid" {
				if err == nil {
					t.Fatal("uncertain stop accepted")
				}
				return
			}
			if err != nil || proof.PodUID != string(old.UID) || proof.NodeUID != string(f.node.UID) || proof.LeaseResourceVersion == "" {
				t.Fatalf("proof=%+v, err=%v", proof, err)
			}
		})
	}
	for _, missing := range []string{"pod", "node", "lease"} {
		t.Run("missing "+missing, func(t *testing.T) {
			f := stoppedWorkspaceFixture()
			c := f.client()
			var o client.Object = f.pod
			if missing == "node" {
				o = f.node
			}
			if missing == "lease" {
				o = f.lease
			}
			if err := c.Delete(context.Background(), o); err != nil {
				t.Fatal(err)
			}
			if _, _, err := (PodWorkspaceStopVerifier{Reader: c, Now: func() time.Time { return f.now }}).Verify(context.Background(), f.s, f.pod.UID); err == nil {
				t.Fatal("missing evidence accepted")
			}
		})
	}
}

type workspaceFixtureRescuer struct {
	calls  int
	proofs []*protocol.WorkspaceStopProof
	answer func(*protocol.WorkspaceStopProof) protocol.RescueReport
}

func (r *workspaceFixtureRescuer) Rescue(context.Context, *corev1.Pod) (protocol.RescueReport, error) {
	return protocol.RescueReport{}, fmt.Errorf("private rescue route used")
}
func (r *workspaceFixtureRescuer) RescueWorkspace(_ context.Context, _ *corev1.Pod, p *protocol.WorkspaceStopProof) (protocol.RescueReport, error) {
	r.calls++
	copy := *p
	r.proofs = append(r.proofs, &copy)
	return r.answer(p), nil
}

type workspaceFixtureStopper struct {
	calls int
	uid   types.UID
}

func (s *workspaceFixtureStopper) StopWorkspace(_ context.Context, p *corev1.Pod) error {
	s.calls++
	s.uid = p.UID
	return nil
}

func workspaceFixtureController(t *testing.T, f workspaceStopFixture, c client.Client) (*Reconciler, observation) {
	t.Helper()
	tmpl := exampleTemplates(t)
	tmpl.Workspace = &templates.Workspace{Enabled: true, Claim: "retained-projects", ID: f.s.Spec.Workspace.ID}
	r := &Reconciler{Client: c, APIReader: c, StopVerifier: PodWorkspaceStopVerifier{Reader: c, Now: func() time.Time { return f.now }}}
	return r, observation{pod: owned[*corev1.Pod]{obj: f.pod}, claim: owned[*corev1.PersistentVolumeClaim]{obj: f.home}, templates: tmpl, now: f.now}
}

func readyWorkspaceHold(t *testing.T, c client.Client, s *v1alpha1.AgentSession) *corev1.Pod {
	t.Helper()
	var p corev1.Pod
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: s.Namespace, Name: workspaceHoldName(s)}, &p); err != nil {
		t.Fatal(err)
	}
	p.UID = "hold-1"
	if err := c.Update(context.Background(), &p); err != nil {
		t.Fatal(err)
	}
	p.Status.Phase = corev1.PodRunning
	p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	if err := c.Status().Update(context.Background(), &p); err != nil {
		t.Fatal(err)
	}
	return &p
}

func TestWorkspaceStopLifecycleKeepsExecutorUntilSeparateHoldRescue(t *testing.T) {
	f := stoppedWorkspaceFixture()
	c := f.client()
	r, obs := workspaceFixtureController(t, f, c)
	rescue := &workspaceFixtureRescuer{answer: func(p *protocol.WorkspaceStopProof) protocol.RescueReport {
		rep := cleanReport(f.s.Name)
		rep.SourcePodUID = p.PodUID
		return rep
	}}
	r.Rescuer = rescue
	if _, err := r.reconcileSharedWorkspace(context.Background(), f.s, &obs); err != nil {
		t.Fatal(err)
	}
	hold := readyWorkspaceHold(t, c, f.s)
	if hold.Name == f.pod.Name || hold.Spec.RestartPolicy != corev1.RestartPolicyAlways || hold.Spec.Containers[0].Args[0] != "hold" {
		t.Fatalf("hold is not a distinct idle holder: %+v", hold.Spec)
	}
	if _, err := r.reconcileSharedWorkspace(context.Background(), f.s, &obs); err != nil {
		t.Fatal(err)
	}
	if rescue.calls != 1 || !sharedRescued(f.s) || f.s.Status.Rescue.SourcePodUID != string(f.pod.UID) || f.s.Status.Rescue.PodUID != string(hold.UID) {
		t.Fatalf("rescue not bound: %+v", f.s.Status.Rescue)
	}
	for _, name := range []string{f.pod.Name, hold.Name} {
		if err := c.Get(context.Background(), client.ObjectKey{Namespace: f.s.Namespace, Name: name}, &corev1.Pod{}); err != nil {
			t.Fatal("Pod removed before owned rescue was recorded", err)
		}
	}
	if _, err := r.reconcileSharedWorkspace(context.Background(), f.s, &obs); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(f.pod), &corev1.Pod{}); err != nil {
		t.Fatal("executor was removed before hold absence", err)
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(hold), &corev1.Pod{}); !apierrors.IsNotFound(err) {
		t.Fatalf("hold cleanup: %v", err)
	}
	if _, err := r.reconcileSharedWorkspace(context.Background(), f.s, &obs); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(f.pod), &corev1.Pod{}); !apierrors.IsNotFound(err) {
		t.Fatalf("executor cleanup: %v", err)
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(f.home), &corev1.PersistentVolumeClaim{}); err != nil {
		t.Fatal("home removed before archive", err)
	}
}

func TestWorkspaceStopLifecycleRequestsExitWithoutDeletion(t *testing.T) {
	f := stoppedWorkspaceFixture()
	f.pod.Status.Phase = corev1.PodRunning
	f.pod.Status.ContainerStatuses[0].State = corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}
	c := f.client()
	r, obs := workspaceFixtureController(t, f, c)
	stopper := &workspaceFixtureStopper{}
	r.WorkspaceStopper = stopper
	if _, err := r.reconcileSharedWorkspace(context.Background(), f.s, &obs); err != nil {
		t.Fatal(err)
	}
	if stopper.calls != 1 || stopper.uid != f.pod.UID || obs.removalBlocked == nil {
		t.Fatal("exact supervisor stop was not requested")
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(f.pod), &corev1.Pod{}); err != nil {
		t.Fatal("stop request deleted executor", err)
	}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: f.s.Namespace, Name: workspaceHoldName(f.s)}, &corev1.Pod{}); !apierrors.IsNotFound(err) {
		t.Fatal("hold created before actual termination")
	}
}

func TestWorkspaceStopLifecycleRetriesWithFreshProofAndPreservesPartition(t *testing.T) {
	f := stoppedWorkspaceFixture()
	c := f.client()
	r, obs := workspaceFixtureController(t, f, c)
	if _, err := r.reconcileSharedWorkspace(context.Background(), f.s, &obs); err != nil {
		t.Fatal(err)
	}
	readyWorkspaceHold(t, c, f.s)
	rescue := &workspaceFixtureRescuer{answer: func(p *protocol.WorkspaceStopProof) protocol.RescueReport {
		rep := cleanReport(f.s.Name)
		rep.SourcePodUID = p.PodUID
		rep.OK = false
		return rep
	}}
	r.Rescuer = rescue
	if _, err := r.reconcileSharedWorkspace(context.Background(), f.s, &obs); err != nil {
		t.Fatal(err)
	}
	obs.now = f.s.Status.Rescue.At.Add(time.Second)
	if result, err := r.reconcileSharedWorkspace(context.Background(), f.s, &obs); err != nil || result.RequeueAfter < 14*time.Minute || rescue.calls != 1 {
		t.Fatalf("failed rescue bypassed retry window: %+v, %v, calls=%d", result, err, rescue.calls)
	}
	newNow := f.now.Add(16 * time.Minute)
	var lease coordinationv1.Lease
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(f.lease), &lease); err != nil {
		t.Fatal(err)
	}
	lease.Spec.RenewTime = &metav1.MicroTime{Time: newNow.Add(-time.Second)}
	if err := c.Update(context.Background(), &lease); err != nil {
		t.Fatal(err)
	}
	r.StopVerifier = PodWorkspaceStopVerifier{Reader: c, Now: func() time.Time { return newNow }}
	obs.now = f.s.Status.Rescue.At.Add(16 * time.Minute)
	rescue.answer = func(p *protocol.WorkspaceStopProof) protocol.RescueReport {
		rep := cleanReport(f.s.Name)
		rep.SourcePodUID = p.PodUID
		var node corev1.Node
		if err := c.Get(context.Background(), client.ObjectKeyFromObject(f.node), &node); err != nil {
			t.Fatal(err)
		}
		node.Status.Conditions[0].Status = corev1.ConditionUnknown
		if err := c.Status().Update(context.Background(), &node); err != nil {
			t.Fatal(err)
		}
		return rep
	}
	if _, err := r.reconcileSharedWorkspace(context.Background(), f.s, &obs); err != nil {
		t.Fatal(err)
	}
	if rescue.calls != 2 || !rescue.proofs[1].VerifiedAt.Equal(newNow) || sharedRescued(f.s) || obs.removalBlocked == nil {
		t.Fatal("retry reused immutable proof or accepted a partitioned source")
	}
	for _, name := range []string{f.pod.Name, workspaceHoldName(f.s)} {
		if err := c.Get(context.Background(), client.ObjectKey{Namespace: f.s.Namespace, Name: name}, &corev1.Pod{}); err != nil {
			t.Fatal("partition removed retained Pod", err)
		}
	}
}

func TestWorkspaceMissingExecutorStaysRememberedAndNoPrivateHold(t *testing.T) {
	f := stoppedWorkspaceFixture()
	f.s.Finalizers = []string{Finalizer}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: systemNS, Name: templates.DefaultName}, Data: map[string]string{templates.Key: exampleDoc + "\nworkspace:\n  enabled: true\n  claim: retained-projects\n  id: projects-v2\n"}}
	c := f.client(cm)
	if err := c.Delete(context.Background(), f.pod); err != nil {
		t.Fatal(err)
	}
	r := &Reconciler{Client: c, APIReader: c, Templates: templatesKey}
	for range 2 {
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(f.s)}); err != nil {
			t.Fatal(err)
		}
	}
	var s v1alpha1.AgentSession
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(f.s), &s); err != nil {
		t.Fatal(err)
	}
	if s.Status.PodName != f.s.Name {
		t.Fatal("missing executor was forgotten on later reconcile")
	}
	var pods corev1.PodList
	if err := c.List(context.Background(), &pods); err != nil {
		t.Fatal(err)
	}
	if len(pods.Items) != 0 {
		t.Fatal("uncertain missing executor produced a fallback Pod")
	}
}

func TestWorkspaceGuardBindsBothPodUIDsAndHoldBlocksArchive(t *testing.T) {
	f := stoppedWorkspaceFixture()
	c := f.client()
	r, obs := workspaceFixtureController(t, f, c)
	proof, _, err := r.verifyWorkspaceStop(context.Background(), f.s, f.pod.UID)
	if err != nil {
		t.Fatal(err)
	}
	hold, err := buildWorkspaceHoldPod(f.s, obs.templates, proof, f.home.UID)
	if err != nil {
		t.Fatal(err)
	}
	hold.UID = "hold-1"
	f.s.Status.Rescue = &v1alpha1.RescueStatus{Result: v1alpha1.RescueCleanAndPushed, SourcePodUID: string(f.pod.UID), PodUID: string(hold.UID), Generation: f.s.Generation}
	if err := podRemovalAllowed(f.s, f.pod); err != nil {
		t.Fatal(err)
	}
	if err := podRemovalAllowed(f.s, hold); err != nil {
		t.Fatal(err)
	}
	wrong := f.pod.DeepCopy()
	wrong.UID = "replacement"
	if err := podRemovalAllowed(f.s, wrong); err == nil {
		t.Fatal("different executor cleanup allowed")
	}
	if err := c.Create(context.Background(), hold); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(context.Background(), f.pod); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.archive(context.Background(), f.s, &obs, true); err != nil {
		t.Fatal(err)
	}
	if obs.removalBlocked == nil || !strings.Contains(obs.removalBlocked.Error(), "private home") {
		t.Fatalf("hold did not block archive: %v", obs.removalBlocked)
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(f.home), &corev1.PersistentVolumeClaim{}); err != nil {
		t.Fatal("hold allowed home cleanup", err)
	}
}

func workspacePreparationReport(s *v1alpha1.AgentSession, p *protocol.WorkspaceStopProof, kind string) protocol.RescueReport {
	rep := protocol.RescueReport{SourcePodUID: p.PodUID, Session: s.Name, Stamp: "preparation", OK: true, Agent: &protocol.AgentStop{},
		WorkspacePreservation: &protocol.WorkspacePreservation{Version: 1, Workspace: s.Spec.Workspace.ID, Task: s.Name, SessionUID: string(s.UID), SourcePodUID: p.PodUID, Kind: kind, NoOwner: kind == "NoWorkAdmitted"},
		Repos: []protocol.RepoRescue{{Path: "/home/dev/repos/" + s.Spec.Repo, Absent: true,
			Worktrees: []protocol.WorktreeRescue{{Path: "/home/dev/work/" + s.Name, Absent: true}}}}}
	if kind == "OwnedRefsPreserved" {
		rep.WorkspacePreservation.OwnerGeneration = 1
		rep.WorkspacePreservation.NoOwner = false
		rr := &rep.Repos[0]
		rr.Absent, rr.FullBundle = false, true
		rr.UnpushedRefs = []protocol.Ref{{Name: "refs/heads/agent/" + s.Name, Commit: "aaaa"}}
		rr.Bundle = &protocol.RepoBundle{File: "rescue/task/preparation/repo.bundle", Verified: true,
			Refs: []protocol.BundleRef{{Name: rr.UnpushedRefs[0].Name, Source: rr.UnpushedRefs[0].Name, Commit: "aaaa"}}}
		rep.Bundle = &protocol.BundleReport{Dir: "rescue/task/preparation", Manifest: "rescue/task/preparation/manifest.json"}
	}
	return rep
}

func TestWorkspacePreparationVerdictIsTypedAndSharedOnly(t *testing.T) {
	f := stoppedWorkspaceFixture()
	r, obs := workspaceFixtureController(t, f, f.client())
	p, _, err := r.verifyWorkspaceStop(context.Background(), f.s, f.pod.UID)
	if err != nil {
		t.Fatal(err)
	}
	hold, err := buildWorkspaceHoldPod(f.s, obs.templates, p, f.home.UID)
	if err != nil {
		t.Fatal(err)
	}
	hold.UID = "hold-1"
	for _, kind := range []string{"NoWorkAdmitted", "OwnedRefsPreserved"} {
		rep := workspacePreparationReport(f.s, p, kind)
		rec, reason := verdict(f.s, hold, rep, metav1.NewTime(f.now))
		want := v1alpha1.RescueNoWorkAdmitted
		if kind == "OwnedRefsPreserved" {
			want = v1alpha1.RescueVerified
		}
		if rec.Result != want || rec.PreservationKind != kind || reason != string(want) {
			t.Fatalf("%s: %+v reason=%s", kind, rec, reason)
		}
		private := f.s.DeepCopy()
		private.Spec.Workspace = nil
		if got, _ := verdict(private, hold, rep, metav1.NewTime(f.now)); got.Result != v1alpha1.RescueFailed {
			t.Fatal("private rescue accepted shared preparation proof")
		}
	}
	for _, tc := range []struct {
		name string
		edit func(*protocol.RescueReport)
	}{
		{"clean pushed claim", func(rep *protocol.RescueReport) { rep.CleanAndPushed = true }},
		{"empty home claim", func(rep *protocol.RescueReport) { rep.VolumeEmpty = true }},
		{"wrong source", func(rep *protocol.RescueReport) { rep.WorkspacePreservation.SourcePodUID = "peer" }},
		{"unknown kind", func(rep *protocol.RescueReport) { rep.WorkspacePreservation.Kind = "Unknown" }},
		{"zero writer without no-owner", func(rep *protocol.RescueReport) { rep.WorkspacePreservation.NoOwner = false }},
		{"admitted writer with no-owner", func(rep *protocol.RescueReport) { rep.WorkspacePreservation.OwnerGeneration = 1 }},
		{"missing typed result", func(rep *protocol.RescueReport) { rep.WorkspacePreservation = nil }},
		{"nonabsent worktree", func(rep *protocol.RescueReport) { rep.Repos[0].Worktrees[0].Absent = false }},
		{"task work remains", func(rep *protocol.RescueReport) { rep.Repos[0].Worktrees[0].Dirty = true }},
		{"unowned refs", func(rep *protocol.RescueReport) {
			rep.Repos[0].UnpushedRefs = []protocol.Ref{{Name: "refs/heads/peer", Commit: "aaaa"}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rep := workspacePreparationReport(f.s, p, "NoWorkAdmitted")
			tc.edit(&rep)
			rec, _ := verdict(f.s, hold, rep, metav1.NewTime(f.now))
			if rec.Result != v1alpha1.RescueFailed {
				t.Fatalf("contradictory report accepted: %+v", rec)
			}
			if tc.name == "unknown kind" && rec.PreservationKind != "" {
				t.Fatal("unknown report kind makes the failure status invalid under its API enum")
			}
		})
	}
}

func TestWorkspaceAdmittedTypedRescueRecordsWriterForCleanPushedTask(t *testing.T) {
	f := stoppedWorkspaceFixture()
	r, obs := workspaceFixtureController(t, f, f.client())
	proof, _, err := r.verifyWorkspaceStop(context.Background(), f.s, f.pod.UID)
	if err != nil {
		t.Fatal(err)
	}
	hold, err := buildWorkspaceHoldPod(f.s, obs.templates, proof, f.home.UID)
	if err != nil {
		t.Fatal(err)
	}
	hold.UID = "hold-1"
	rep := cleanReport(f.s.Name)
	rep.SourcePodUID = proof.PodUID
	rep.WorkspacePreservation = &protocol.WorkspacePreservation{Version: 1, Workspace: f.s.Spec.Workspace.ID,
		Task: f.s.Name, SessionUID: string(f.s.UID), SourcePodUID: proof.PodUID, OwnerGeneration: 7, Kind: "TaskWorkPreserved"}
	rec, reason := verdict(f.s, hold, rep, metav1.NewTime(f.now))
	if rec.Result != v1alpha1.RescueCleanAndPushed || reason != string(rec.Result) || rec.SharedProof == nil ||
		rec.SharedProof.WriterGeneration != 7 || rec.SharedProof.NoOwner || rec.SharedProof.PrivateHomeUID != string(f.home.UID) || rec.SharedProof.Manifest != "" {
		t.Fatalf("typed admitted rescue rejected or lost original proof: %+v reason=%s", rec, reason)
	}
}

func TestWorkspaceOriginalHomeReplacementBeforeHoldCreationIsRefused(t *testing.T) {
	f := stoppedWorkspaceFixture()
	f.home.UID = "replacement-home"
	c := f.client()
	r, obs := workspaceFixtureController(t, f, c)
	if _, err := r.reconcileSharedWorkspace(context.Background(), f.s, &obs); err != nil {
		t.Fatal(err)
	}
	if obs.removalBlocked == nil {
		t.Fatal("replacement home admitted a hold Pod")
	}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: f.s.Namespace, Name: workspaceHoldName(f.s)}, &corev1.Pod{}); !apierrors.IsNotFound(err) {
		t.Fatalf("hold Pod created for replaced home: %v", err)
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(f.pod), &corev1.Pod{}); err != nil {
		t.Fatal("executor removed despite uncertain original home", err)
	}
}

func TestWorkspaceMutableHoldHomeBindingCannotAuthorizeRescue(t *testing.T) {
	f := stoppedWorkspaceFixture()
	c := f.client()
	r, obs := workspaceFixtureController(t, f, c)
	if _, err := r.reconcileSharedWorkspace(context.Background(), f.s, &obs); err != nil {
		t.Fatal(err)
	}
	hold := readyWorkspaceHold(t, c, f.s)
	r.Rescuer = &workspaceFixtureRescuer{answer: func(p *protocol.WorkspaceStopProof) protocol.RescueReport {
		var changed corev1.Pod
		if err := c.Get(context.Background(), client.ObjectKeyFromObject(hold), &changed); err != nil {
			t.Fatal(err)
		}
		changed.Annotations[privateHomeUIDAnnotation] = "another-home"
		if err := c.Update(context.Background(), &changed); err != nil {
			t.Fatal(err)
		}
		return workspacePreparationReport(f.s, p, "NoWorkAdmitted")
	}}
	if _, err := r.reconcileSharedWorkspace(context.Background(), f.s, &obs); err != nil {
		t.Fatal(err)
	}
	if f.s.Status.Rescue != nil || obs.removalBlocked == nil {
		t.Fatal("mutated hold annotation created durable rescue authority")
	}
	for _, pod := range []*corev1.Pod{f.pod, hold} {
		if err := c.Get(context.Background(), client.ObjectKeyFromObject(pod), &corev1.Pod{}); err != nil {
			t.Fatal("uncertain rescue removed Pod", err)
		}
	}
}

func TestWorkspaceNoWorkLifecycleRetainsPrivateHomeAfterPodCleanup(t *testing.T) {
	f := stoppedWorkspaceFixture()
	c := f.client()
	r, obs := workspaceFixtureController(t, f, c)
	r.Rescuer = &workspaceFixtureRescuer{answer: func(p *protocol.WorkspaceStopProof) protocol.RescueReport {
		return workspacePreparationReport(f.s, p, "NoWorkAdmitted")
	}}
	if _, err := r.reconcileSharedWorkspace(context.Background(), f.s, &obs); err != nil {
		t.Fatal(err)
	}
	hold := readyWorkspaceHold(t, c, f.s)
	if _, err := r.reconcileSharedWorkspace(context.Background(), f.s, &obs); err != nil {
		t.Fatal(err)
	}
	if !sharedRescued(f.s) || rescued(f.s) || f.s.Status.Rescue.Result != v1alpha1.RescueNoWorkAdmitted {
		t.Fatalf("distinct shared result: %+v", f.s.Status.Rescue)
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(f.pod), &corev1.Pod{}); err != nil {
		t.Fatal("executor removed before durable result")
	}
	if _, err := r.reconcileSharedWorkspace(context.Background(), f.s, &obs); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(hold), &corev1.Pod{}); !apierrors.IsNotFound(err) {
		t.Fatalf("hold removal: %v", err)
	}
	if _, err := r.reconcileSharedWorkspace(context.Background(), f.s, &obs); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(f.pod), &corev1.Pod{}); !apierrors.IsNotFound(err) {
		t.Fatalf("executor removal: %v", err)
	}
	if _, _, err := r.archive(context.Background(), f.s, &obs, true); err != nil {
		t.Fatal(err)
	}
	if obs.removalBlocked == nil || f.s.Status.ArchivedAt != nil {
		t.Fatal("task absence became private-home archive proof")
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(f.home), &corev1.PersistentVolumeClaim{}); err != nil {
		t.Fatal("private home deleted", err)
	}
}

func TestWorkspaceResumeDuringHoldRescueCreatesNewExecutorAfterCleanup(t *testing.T) {
	f := stoppedWorkspaceFixture()
	c := f.client()
	r, obs := workspaceFixtureController(t, f, c)
	resumed := false
	r.Rescuer = &workspaceFixtureRescuer{answer: func(p *protocol.WorkspaceStopProof) protocol.RescueReport {
		if !resumed {
			var fresh v1alpha1.AgentSession
			if err := c.Get(context.Background(), client.ObjectKeyFromObject(f.s), &fresh); err != nil {
				t.Fatal(err)
			}
			fresh.Spec.OperatingMode = v1alpha1.OperatingModeRunning
			fresh.Generation++
			if err := c.Update(context.Background(), &fresh); err != nil {
				t.Fatal(err)
			}
			resumed = true
		}
		return workspacePreparationReport(f.s, p, "NoWorkAdmitted")
	}}
	if _, err := r.reconcileSharedWorkspace(context.Background(), f.s, &obs); err != nil {
		t.Fatal(err)
	}
	hold := readyWorkspaceHold(t, c, f.s)
	if _, err := r.reconcileSharedWorkspace(context.Background(), f.s, &obs); err != nil {
		t.Fatal(err)
	}
	// The in-flight old-generation report cannot authorize cleanup. Reload
	// the resumed generation and run its fresh verified rescue instead.
	var fresh v1alpha1.AgentSession
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(f.s), &fresh); err != nil {
		t.Fatal(err)
	}
	f.s = &fresh
	if f.s.Status.Rescue != nil || !resumed {
		t.Fatal("old-generation rescue was accepted during resume")
	}
	if _, err := r.reconcileSharedWorkspace(context.Background(), f.s, &obs); err != nil {
		t.Fatal(err)
	}
	if !sharedRescued(f.s) || f.s.Status.PodName == "" {
		t.Fatal("current generation rescue lost the retained executor identity")
	}
	if _, err := r.reconcileSharedWorkspace(context.Background(), f.s, &obs); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(hold), &corev1.Pod{}); !apierrors.IsNotFound(err) {
		t.Fatal("hold was not removed first", err)
	}
	if _, err := r.reconcileSharedWorkspace(context.Background(), f.s, &obs); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(f.pod), &corev1.Pod{}); !apierrors.IsNotFound(err) {
		t.Fatal("old executor was not removed after hold absence", err)
	}
	if _, err := r.reconcileSharedWorkspace(context.Background(), f.s, &obs); err != nil {
		t.Fatal(err)
	}
	var superseded v1alpha1.AgentSession
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(f.s), &superseded); err != nil {
		t.Fatal(err)
	}
	if !superseded.Status.Rescue.Superseded || superseded.Status.PodName != "" {
		t.Fatal("supersession did not atomically clear the cleaned executor identity")
	}
	f.s = &superseded
	if _, err := r.reconcileSharedWorkspace(context.Background(), f.s, &obs); err != nil {
		t.Fatal(err)
	}
	var replacement corev1.Pod
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(f.pod), &replacement); err != nil {
		t.Fatal("resume failed to create the next executor", err)
	}
	if isHoldPod(&replacement) || replacement.Spec.RestartPolicy != corev1.RestartPolicyNever || replacement.UID == f.pod.UID {
		t.Fatal("replacement is not a new shared executor")
	}
}

func TestWorkspaceGenerationChangeAfterVerifiedDeleteResumes(t *testing.T) {
	f := stoppedWorkspaceFixture()
	c := f.client()
	r, obs := workspaceFixtureController(t, f, c)
	r.Rescuer = &workspaceFixtureRescuer{answer: func(p *protocol.WorkspaceStopProof) protocol.RescueReport {
		return workspacePreparationReport(f.s, p, "NoWorkAdmitted")
	}}
	if _, err := r.reconcileSharedWorkspace(context.Background(), f.s, &obs); err != nil {
		t.Fatal(err)
	}
	readyWorkspaceHold(t, c, f.s)
	if _, err := r.reconcileSharedWorkspace(context.Background(), f.s, &obs); err != nil {
		t.Fatal(err)
	}
	if _, err := r.reconcileSharedWorkspace(context.Background(), f.s, &obs); err != nil {
		t.Fatal(err)
	}
	if _, err := r.reconcileSharedWorkspace(context.Background(), f.s, &obs); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(f.pod), &corev1.Pod{}); !apierrors.IsNotFound(err) {
		t.Fatal("executor not cleaned", err)
	}
	// Resume races the first missing observation after the controller's
	// verified delete, leaving the completed rescue's generation behind.
	f.s.Spec.OperatingMode = v1alpha1.OperatingModeRunning
	f.s.Generation++
	if err := c.Update(context.Background(), f.s); err != nil {
		t.Fatal(err)
	}
	if sharedRescued(f.s) || !sharedCleanupRescueRecorded(f.s) {
		t.Fatal("historical proof escaped its missing-Pod-only predicate")
	}
	if _, err := r.reconcileSharedWorkspace(context.Background(), f.s, &obs); err != nil {
		t.Fatal(err)
	}
	var fresh v1alpha1.AgentSession
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(f.s), &fresh); err != nil {
		t.Fatal(err)
	}
	if fresh.Status.PodName != "" || !fresh.Status.Rescue.Superseded {
		t.Fatal("historical completed cleanup did not atomically supersede the old name")
	}
	f.s = &fresh
	if _, err := r.reconcileSharedWorkspace(context.Background(), f.s, &obs); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(f.pod), &corev1.Pod{}); err != nil {
		t.Fatal("resumed executor absent", err)
	}
}

func TestWorkspaceHistoricalCleanupRejectsUncertainRecordsAndLivePods(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*v1alpha1.RescueStatus)
	}{
		{"future", func(r *v1alpha1.RescueStatus) { r.Generation = 4 }},
		{"zero generation", func(r *v1alpha1.RescueStatus) { r.Generation = 0 }},
		{"failed", func(r *v1alpha1.RescueStatus) { r.Result = v1alpha1.RescueFailed }},
		{"superseded", func(r *v1alpha1.RescueStatus) { r.Superseded = true }},
		{"same UIDs", func(r *v1alpha1.RescueStatus) { r.PodUID = r.SourcePodUID }},
		{"missing source", func(r *v1alpha1.RescueStatus) { r.SourcePodUID = "" }},
		{"missing hold", func(r *v1alpha1.RescueStatus) { r.PodUID = "" }},
		{"untyped no-work", func(r *v1alpha1.RescueStatus) { r.PreservationKind = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := stoppedWorkspaceFixture()
			f.s.Spec.OperatingMode = v1alpha1.OperatingModeRunning
			f.s.Status.Rescue = &v1alpha1.RescueStatus{Result: v1alpha1.RescueNoWorkAdmitted, PreservationKind: "NoWorkAdmitted", Generation: 2, SourcePodUID: string(f.pod.UID), PodUID: "hold-1"}
			tc.edit(f.s.Status.Rescue)
			c := f.client()
			if err := c.Delete(context.Background(), f.pod); err != nil {
				t.Fatal(err)
			}
			r, obs := workspaceFixtureController(t, f, c)
			if sharedCleanupRescueRecorded(f.s) {
				t.Fatal("uncertain historical record accepted")
			}
			if _, err := r.reconcileSharedWorkspace(context.Background(), f.s, &obs); err != nil {
				t.Fatal(err)
			}
			if obs.removalBlocked == nil || f.s.Status.PodName == "" {
				t.Fatal("uncertain disappearance became resume proof")
			}
			if err := c.Get(context.Background(), client.ObjectKeyFromObject(f.pod), &corev1.Pod{}); !apierrors.IsNotFound(err) {
				t.Fatal("uncertain owner got a new executor", err)
			}
		})
	}
	for _, sourcePresent := range []bool{false, true} {
		t.Run(fmt.Sprintf("older record with live source=%t", sourcePresent), func(t *testing.T) {
			f := stoppedWorkspaceFixture()
			c := f.client()
			r, obs := workspaceFixtureController(t, f, c)
			proof, _, err := r.verifyWorkspaceStop(context.Background(), f.s, f.pod.UID)
			if err != nil {
				t.Fatal(err)
			}
			hold, err := buildWorkspaceHoldPod(f.s, obs.templates, proof, f.home.UID)
			if err != nil {
				t.Fatal(err)
			}
			hold.UID = "hold-1"
			if err := c.Create(context.Background(), hold); err != nil {
				t.Fatal(err)
			}
			if !sourcePresent {
				if err := c.Delete(context.Background(), f.pod); err != nil {
					t.Fatal(err)
				}
			}
			f.s.Status.Rescue = &v1alpha1.RescueStatus{Result: v1alpha1.RescueNoWorkAdmitted, PreservationKind: "NoWorkAdmitted", Generation: 2, SourcePodUID: string(f.pod.UID), PodUID: string(hold.UID)}
			if _, err := r.reconcileSharedWorkspace(context.Background(), f.s, &obs); err != nil {
				t.Fatal(err)
			}
			if sharedRescued(f.s) || obs.removalBlocked == nil {
				t.Fatal("older record authorized live-Pod cleanup")
			}
			if err := c.Get(context.Background(), client.ObjectKeyFromObject(hold), &corev1.Pod{}); err != nil {
				t.Fatal("live hold deleted by historical proof", err)
			}
			if sourcePresent {
				if err := c.Get(context.Background(), client.ObjectKeyFromObject(f.pod), &corev1.Pod{}); err != nil {
					t.Fatal("live source deleted by historical proof", err)
				}
			}
		})
	}
}

func TestWorkspaceHoldPinsEveryRequiredNodeTermAndPreservesPrivateHold(t *testing.T) {
	f := stoppedWorkspaceFixture()
	tmpl := exampleTemplates(t)
	tmpl.Workspace = &templates.Workspace{Enabled: true, Claim: "retained-projects", ID: f.s.Spec.Workspace.ID}
	proof := &protocol.WorkspaceStopProof{Version: 1, Workspace: f.s.Spec.Workspace.ID, Task: f.s.Name, SessionUID: string(f.s.UID), PodName: f.pod.Name, PodUID: string(f.pod.UID),
		PodResourceVersion: "10", NodeName: f.node.Name, NodeUID: string(f.node.UID), LeaseResourceVersion: "20", LeaseRenewedAt: f.now.Add(-time.Second), VerifiedAt: f.now}
	hold, err := buildWorkspaceHoldPod(f.s, tmpl, proof, f.home.UID)
	if err != nil {
		t.Fatal(err)
	}
	if hold.Spec.NodeName != "" {
		t.Fatal("hold bypassed normal scheduler admission")
	}
	terms := hold.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
	if len(terms) != 1 || len(terms[0].MatchFields) != 1 || terms[0].MatchFields[0].Key != "metadata.name" || terms[0].MatchFields[0].Values[0] != f.node.Name {
		t.Fatal("hold was not constrained to verified old node")
	}
	privateSession := f.s.DeepCopy()
	privateSession.Spec.Workspace = nil
	private, err := buildHoldPod(privateSession, tmpl)
	if err != nil {
		t.Fatal(err)
	}
	if len(private.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchFields) != 0 {
		t.Fatal("private hold placement changed")
	}
	custom := private.DeepCopy()
	custom.Spec.NodeSelector = map[string]string{"disk": "ssd"}
	custom.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms = append(custom.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms,
		corev1.NodeSelectorTerm{MatchExpressions: []corev1.NodeSelectorRequirement{{Key: "disk", Operator: corev1.NodeSelectorOpIn, Values: []string{"ssd"}}}, MatchFields: []corev1.NodeSelectorRequirement{{Key: "metadata.name", Operator: corev1.NodeSelectorOpNotIn, Values: []string{"other-node"}}}})
	before := custom.DeepCopy()
	constrainWorkspaceHoldNode(custom, f.node.Name)
	for i, term := range custom.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms {
		old := before.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[i]
		if !apiequality.Semantic.DeepEqual(term.MatchExpressions, old.MatchExpressions) || len(term.MatchFields) != len(old.MatchFields)+1 || term.MatchFields[len(old.MatchFields)].Values[0] != f.node.Name {
			t.Fatal("node pin broadened or replaced a required OR term")
		}
		if len(old.MatchFields) > 0 && !apiequality.Semantic.DeepEqual(term.MatchFields[:len(old.MatchFields)], old.MatchFields) {
			t.Fatal("node pin replaced existing field selectors")
		}
		term.MatchFields = old.MatchFields
		custom.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[i] = term
	}
	if !apiequality.Semantic.DeepEqual(custom, before) {
		t.Fatal("node pin changed selectors, preferred placement, requests or limits")
	}
}
