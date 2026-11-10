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
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
