package controller

// DESIGN-061 section 4 on a real API server: the queue's cap holds with two
// operators taking turns on the same Sessions, each with its own memory, as an
// old leader's last reconciles and a new leader's first ones interleave. Each
// reads the API server directly, so this shows the memory of one operator is
// never needed by the other; the four workers of one operator on a cache that
// lags are TestTaskCleanupFourWorkersOnALaggingCacheStayUnderTheCap's. envtest
// has no kubelet, PV controller or pvc-protection controller, so the test
// plays them: it runs hold pods, removes Pods being deleted and lifts
// pvc-protection from a claim no Pod uses.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/shelf"
)

// TestTaskCleanupQueueCapWithTwoOperators reaps four private task Sessions
// whose executors ended, and lets two operators, each with its own memory,
// take turns on every Session with --task-cleanup-max-active 2 until all four
// are cleaned up and released. After every reconcile no more than two records
// hold a slot, and two do at some point; every HOME is deleted once, with its
// UID and resourceVersion as preconditions, and every Session is gone.
func TestTaskCleanupQueueCapWithTwoOperators(t *testing.T) {
	ctx := context.Background()
	// Other cases leave Nodes in this package's ephemeral API server.
	// This case simulates its own kubelet; stale or NotReady fixture Nodes
	// would correctly prevent the new all-Node release proof forever.
	var previousNodes corev1.NodeList
	if err := k8s.List(ctx, &previousNodes); err != nil {
		t.Fatal(err)
	}
	for i := range previousNodes.Items {
		n := &previousNodes.Items[i]
		purgeEnv(&coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{Name: n.Name, Namespace: "kube-node-lease"}}, n)
	}
	if err := k8s.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-node-lease"}}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
	clock := time.Now().UTC().Truncate(time.Second)
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "tq-node"}}
	if err := k8s.Create(ctx, node); err != nil {
		t.Fatal(err)
	}
	node.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue, LastHeartbeatTime: metav1.NewTime(clock), LastTransitionTime: metav1.NewTime(clock), Reason: "KubeletReady"}}
	if err := k8s.Status().Update(ctx, node); err != nil {
		t.Fatal(err)
	}
	lease := &coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{Name: node.Name, Namespace: "kube-node-lease", OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Node", Name: node.Name, UID: node.UID}}},
		Spec: coordinationv1.LeaseSpec{HolderIdentity: ptr.To(node.Name), RenewTime: &metav1.MicroTime{Time: clock.Add(-time.Second)}}}
	if err := k8s.Create(ctx, lease); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		purgeEnv(&coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{Name: node.Name, Namespace: "kube-node-lease"}}, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: node.Name}})
	})
	shelfPod := shelfPodObject(sessionNS)
	shelfPod.UID, shelfPod.Status = "", corev1.PodStatus{}
	if err := k8s.Create(ctx, shelfPod); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		purgeEnv(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: shelfPod.Name, Namespace: sessionNS}})
	})

	var names []string
	for i := range 4 {
		name := fmt.Sprintf("tq-cleanup-%d", i)
		names = append(names, name)
		s := taskSession()
		s.Name, s.Namespace, s.UID, s.ResourceVersion = name, sessionNS, "", ""
		s.Finalizers = []string{Finalizer}
		if err := k8s.Create(ctx, s); err != nil {
			t.Fatal(err)
		}
		home := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: HomeClaimName(name), Namespace: sessionNS, OwnerReferences: []metav1.OwnerReference{ownerRef(s)},
			Finalizers: []string{Finalizer}, Annotations: map[string]string{privateHistoryAdmissionAnnotation: "1"}},
			Spec: corev1.PersistentVolumeClaimSpec{AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, VolumeName: "pv-" + name,
				Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}}}}
		if err := k8s.Create(ctx, home); err != nil {
			t.Fatal(err)
		}
		pv := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "pv-" + name},
			Spec: corev1.PersistentVolumeSpec{Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}, AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				ClaimRef:               &corev1.ObjectReference{Namespace: sessionNS, Name: home.Name, UID: home.UID},
				PersistentVolumeSource: corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{Driver: "rbd.csi.ceph.com", VolumeHandle: "handle-" + name}}}}
		if err := k8s.Create(ctx, pv); err != nil {
			t.Fatal(err)
		}
		tf := newTerminalFixture()
		pod := tf.pod
		pod.Name, pod.Namespace, pod.UID = name, sessionNS, ""
		pod.OwnerReferences = []metav1.OwnerReference{ownerRef(s)}
		pod.Annotations[privateHistoryHomeAnnotation] = string(home.UID)
		pod.Spec.NodeName = node.Name
		pod.Spec.Volumes[0].PersistentVolumeClaim.ClaimName = home.Name
		pod.Status = corev1.PodStatus{}
		if err := k8s.Create(ctx, pod); err != nil {
			t.Fatal(err)
		}
		pod.Status = corev1.PodStatus{Phase: corev1.PodFailed, Reason: "DeadlineExceeded", StartTime: &metav1.Time{Time: clock.Add(-time.Hour)}, ContainerStatuses: []corev1.ContainerStatus{{Name: ContainerName, Image: pod.Spec.Containers[0].Image,
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 143, Reason: "Error", StartedAt: metav1.NewTime(clock.Add(-time.Hour)), FinishedAt: metav1.NewTime(clock.Add(-2 * time.Second))}}}}}
		if err := k8s.Status().Update(ctx, pod); err != nil {
			t.Fatal(err)
		}
		s.Status.PrivateHistory = &v1alpha1.PrivateHistoryStatus{Version: 1, SessionUID: string(s.UID), HomeClaim: home.Name, HomeUID: string(home.UID), BindingGeneration: s.Generation, Admission: sharedStarted,
			Provider: string(s.Spec.Agent), StopState: "Unknown", ExecutorAttempted: true, ExecutorIntent: "original-intent",
			LastExecutor: &v1alpha1.PrivateExecutorStatus{PodUID: string(pod.UID), PodName: pod.Name, NodeName: node.Name, Revision: "original-template", Generation: s.Generation}}
		if err := k8s.Status().Update(ctx, s); err != nil {
			t.Fatal(err)
		}
		if err := k8s.Delete(ctx, s); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			purgeEnv(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sessionNS}},
				&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: HomeClaimName(name), Namespace: sessionNS}},
				&corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "pv-" + name}},
				&v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sessionNS}})
		})
	}

	now := func() time.Time { return clock }
	rescuer := &expiryRescuer{}
	verifier := &fakeVerifier{free: 10 << 30, answers: map[string]protocol.RescueVerifyResult{}}
	var mu sync.Mutex
	claimDeletes := map[string][]bool{}
	operator := func() *Reconciler {
		c := interceptor.NewClient(k8sWatch(t), interceptor.Funcs{Delete: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.DeleteOption) error {
			err := c.Delete(ctx, o, opts...)
			if _, ok := o.(*corev1.PersistentVolumeClaim); ok && err == nil {
				var do client.DeleteOptions
				do.ApplyOptions(opts)
				p := do.Preconditions
				mu.Lock()
				claimDeletes[o.GetName()] = append(claimDeletes[o.GetName()], p != nil && p.UID != nil && *p.UID != "" && p.ResourceVersion != nil && *p.ResourceVersion != "")
				mu.Unlock()
			}
			return err
		}})
		return &Reconciler{Client: c, APIReader: c, Templates: templatesKey, PrivateTerminalRecord: true, PrivateExecutorStop: true,
			PrivateExecutorTerminal: NewPrivateExecutorTerminalCounter(), HomeExpiryCounters: NewHomeExpiryCounters(), Rescuer: rescuer,
			HistoryRelay:       &ExecHistoryRelay{Exec: newAgentdExec(t, "tq"), Shelf: &shelf.Shelf{Reader: c, Namespace: sessionNS}, Slots: shelf.NewExecSlots(shelf.DefaultExecSlots)},
			TaskSessionCleanup: true, TaskCleanupMaxActive: 2, TaskCleanupMinSharedFree: DefaultTaskCleanupMinSharedFree, RescueVerifier: verifier,
			TaskCleanupCounters: NewTaskCleanupCounters(), Now: now}
	}
	ops := []*Reconciler{operator(), operator()}
	active := func() (n int, gone int) {
		for _, name := range names {
			s := &v1alpha1.AgentSession{}
			if err := k8s.Get(ctx, client.ObjectKey{Namespace: sessionNS, Name: name}, s); err != nil {
				if apierrors.IsNotFound(err) {
					gone++
					continue
				}
				t.Fatal(err)
			}
			if taskActive(s) {
				n++
			}
		}
		return n, gone
	}
	kubelet := func(name string) {
		if c := (&corev1.PersistentVolumeClaim{}); k8s.Get(ctx, client.ObjectKey{Namespace: sessionNS, Name: HomeClaimName(name)}, c) == nil && !c.DeletionTimestamp.IsZero() {
			if controllerutil.RemoveFinalizer(c, "kubernetes.io/pvc-protection") {
				if err := k8s.Update(ctx, c); client.IgnoreNotFound(err) != nil && !apierrors.IsConflict(err) {
					t.Fatal(err)
				}
			}
		}
		p := &corev1.Pod{}
		if err := k8s.Get(ctx, client.ObjectKey{Namespace: sessionNS, Name: name}, p); err != nil {
			return
		}
		switch {
		case !p.DeletionTimestamp.IsZero():
			if err := k8s.Delete(ctx, p, client.GracePeriodSeconds(0), client.Preconditions{UID: &p.UID}); client.IgnoreNotFound(err) != nil {
				t.Fatal(err)
			}
		case isHoldPod(p) && p.Status.Phase != corev1.PodRunning:
			markRunning(t, p, node.Name)
		}
	}
	peak := 0
	for round := 0; round < 60; round++ {
		// The clock moves 15 seconds a round, and the kubelet renews.
		clock = clock.Add(15 * time.Second)
		l := &coordinationv1.Lease{}
		if err := k8s.Get(ctx, client.ObjectKeyFromObject(lease), l); err != nil {
			t.Fatal(err)
		}
		l.Spec.RenewTime = &metav1.MicroTime{Time: clock.Add(-time.Second)}
		if err := k8s.Update(ctx, l); err != nil {
			t.Fatal(err)
		}
		for _, name := range names {
			for _, op := range ops {
				if _, err := op.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Namespace: sessionNS, Name: name}}); err != nil {
					t.Fatal(err)
				}
				n, _ := active()
				if n > 2 {
					t.Fatalf("round %d: %d clean-ups hold a slot, cap 2", round, n)
				}
				peak = max(peak, n)
			}
			kubelet(name)
		}
		if _, gone := active(); gone == len(names) {
			break
		}
	}
	if n, gone := active(); gone != len(names) {
		t.Fatalf("%d of %d Sessions are cleaned up, %d still active", gone, len(names), n)
	}
	if peak != 2 {
		t.Fatalf("at most %d clean-ups ran at once; want the cap, 2", peak)
	}
	for _, name := range names {
		if err := k8s.Get(ctx, client.ObjectKey{Namespace: sessionNS, Name: HomeClaimName(name)}, &corev1.PersistentVolumeClaim{}); !apierrors.IsNotFound(err) {
			t.Fatalf("the HOME of %s stays: %v", name, err)
		}
		if d := claimDeletes[HomeClaimName(name)]; len(d) != 1 || !d[0] {
			t.Fatalf("the HOME of %s was deleted %v; want once, with UID and resourceVersion preconditions", name, d)
		}
	}
}

// TestTaskCleanupExpiredTamperedBundleKeepsSession is the kept-work check on
// an Expired record where CEL runs. A tampered bundle must not be written
// onto the record (the API server would reject it) and must not requeue in
// one second.
func TestTaskCleanupExpiredTamperedBundleKeepsSession(t *testing.T) {
	ctx := context.Background()
	const (
		name  = "exp-bundle-tamper"
		stamp = "20261110-0800"
	)
	digest := strings.Repeat("ab", 32)
	at := metav1.NewTime(time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC))
	manifest := "rescue/" + name + "/" + stamp + "/manifest.json"
	s := taskSession()
	s.Name, s.Namespace, s.UID, s.ResourceVersion = name, sessionNS, "", ""
	s.Finalizers = []string{Finalizer}
	s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
	if err := k8s.Create(ctx, s); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		purgeEnv(&v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sessionNS}})
	})
	s.Status.PrivateHistory = &v1alpha1.PrivateHistoryStatus{
		Version: 1, SessionUID: string(s.UID), HomeClaim: HomeClaimName(name), HomeUID: "expired-home-uid",
		BindingGeneration: s.Generation, Admission: sharedStarted, ExecutorAttempted: true, ExecutorIntent: "original-intent",
		Provider: string(s.Spec.Agent), StopState: "Unknown",
		LastExecutor: &v1alpha1.PrivateExecutorStatus{PodUID: "original-executor", PodName: name, NodeName: "worker-a", Revision: "original-template", Generation: s.Generation},
		ExecutorTerminal: &v1alpha1.PrivateExecutorTerminalStatus{Version: 1, PodUID: "original-executor", Phase: "Succeeded", Reason: "Completed", PodResourceVersion: "42",
			NodeName: "worker-a", NodeUID: "node-uid", LeaseRenewedAt: at, ObservedAt: at,
			Containers: []v1alpha1.PrivateExecutorContainerTermination{{Name: ContainerName, ExitCode: 0, Reason: "Completed", FinishedAt: at}}},
		Expiry: &v1alpha1.PrivateHomeExpiryStatus{Version: 1, ExecutorUID: "original-executor", HomeUID: "expired-home-uid", GraceStart: at, DueAt: at,
			Phase: v1alpha1.HomeExpiryRescuing, HoldIntent: "intent-1", HoldPodUID: "hold-1"},
	}
	if err := k8s.Status().Update(ctx, s); err != nil {
		t.Fatal(err)
	}
	step := func(edit func(*v1alpha1.PrivateHomeExpiryStatus)) {
		t.Helper()
		got := &v1alpha1.AgentSession{}
		if err := k8s.Get(ctx, client.ObjectKey{Namespace: sessionNS, Name: name}, got); err != nil {
			t.Fatal(err)
		}
		edit(got.Status.PrivateHistory.Expiry)
		if err := k8s.Status().Update(ctx, got); err != nil {
			t.Fatal(err)
		}
	}
	step(func(e *v1alpha1.PrivateHomeExpiryStatus) {
		e.Phase, e.RescueStamp = v1alpha1.HomeExpiryArchived, stamp
		e.Bundle = &v1alpha1.PrivateHomeBundleStatus{Stamp: stamp, Result: bundleVerified, Manifest: manifest, ManifestSHA256: digest, Bundles: 1, Bytes: 2048, VerifiedAt: at}
	})
	step(func(e *v1alpha1.PrivateHomeExpiryStatus) {
		e.Phase, e.DeletingAt = v1alpha1.HomeExpiryDeleting, &at
	})
	step(func(e *v1alpha1.PrivateHomeExpiryStatus) {
		e.Phase, e.HomeDeletedAt = v1alpha1.HomeExpiryExpired, &at
	})
	stored := &v1alpha1.AgentSession{}
	if err := k8s.Get(ctx, client.ObjectKey{Namespace: sessionNS, Name: name}, stored); err != nil {
		t.Fatal(err)
	}
	before, err := json.Marshal(stored.Status.PrivateHistory.Expiry)
	if err != nil {
		t.Fatal(err)
	}
	id := name + "/" + stamp
	verifier := &fakeVerifier{free: 10 << 30, answers: map[string]protocol.RescueVerifyResult{}}
	verifier.set(id, protocol.RescueVerifyResult{OK: true, Manifest: manifest, ManifestSHA256: strings.Repeat("e", 64), Bundles: 1, Bytes: 2048})
	r := &Reconciler{Client: k8s, APIReader: k8s, Templates: templatesKey, PrivateTerminalRecord: true, PrivateExecutorStop: true,
		TaskSessionCleanup: true, TaskCleanupMaxActive: DefaultTaskCleanupMaxActive, RescueVerifier: verifier,
		TaskCleanupCounters: NewTaskCleanupCounters()}
	res, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Namespace: sessionNS, Name: name}})
	if err != nil {
		t.Fatal(err)
	}
	if res.RequeueAfter == time.Second {
		t.Fatalf("requeue %s; an Expired refusal must not retry every second", res.RequeueAfter)
	}
	if res.RequeueAfter != homeExpiryRefusalRetry {
		t.Fatalf("requeue %s; want %s", res.RequeueAfter, homeExpiryRefusalRetry)
	}
	got := &v1alpha1.AgentSession{}
	if err := k8s.Get(ctx, client.ObjectKey{Namespace: sessionNS, Name: name}, got); err != nil {
		t.Fatal(err)
	}
	if !got.DeletionTimestamp.IsZero() {
		t.Fatal("the Session is being deleted")
	}
	after, err := json.Marshal(got.Status.PrivateHistory.Expiry)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("the Expired record changed\nbefore %s\nafter  %s", before, after)
	}
	c := meta.FindStatusCondition(got.Status.Conditions, ConditionHomeExpiry)
	if c == nil || !strings.Contains(c.Message, "the session is not removed") || !strings.Contains(c.Message, "not the recorded") {
		t.Fatalf("HomeExpiry %+v; want a note that the tampered bundle keeps the session", c)
	}
}
