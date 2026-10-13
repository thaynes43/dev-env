package controller

// DESIGN-025 unit H5 on a real API server: the AgentSession CRD with its
// rules, real UIDs and preconditions, and Node and Lease objects. envtest has
// no kubelet, so the test reports Pod status and plays the kubelet's removal
// of a Pod that is being deleted. The history export and archive are agentd's
// own code behind an in-process exec. Every step runs in a fresh Reconciler,
// as after an operator restart (T12).

import (
	"context"
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
	"github.com/thaynes43/dev-env/internal/shelf"
)

func TestHomeExpiryOnTheAPIServer(t *testing.T) {
	ctx := context.Background()
	if err := k8s.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-node-lease"}}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
	shelfPod := shelfPodObject(sessionNS)
	shelfPod.UID = ""
	shelfPod.Status = corev1.PodStatus{}
	switch err := k8s.Create(ctx, shelfPod); {
	case err == nil:
		shelfPod.Status = shelfPodObject(sessionNS).Status
		if err := k8s.Status().Update(ctx, shelfPod); err != nil {
			t.Fatal(err)
		}
	case !apierrors.IsAlreadyExists(err):
		t.Fatal(err)
	}
	t.Cleanup(func() {
		purgeEnv(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: shelfPod.Name, Namespace: sessionNS}})
	})
	now := time.Now().UTC().Truncate(time.Second)

	// envFixture is a reaped private Session whose recorded executor ended
	// with DeadlineExceeded on a Ready node with a fresh lease.
	envFixture := func(t *testing.T, name string) (*v1alpha1.AgentSession, *corev1.Pod, *corev1.PersistentVolumeClaim) {
		t.Helper()
		tf := newTerminalFixture()
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name + "-node"}}
		if err := k8s.Create(ctx, node); err != nil {
			t.Fatal(err)
		}
		node.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue, LastHeartbeatTime: metav1.NewTime(now), LastTransitionTime: metav1.NewTime(now), Reason: "KubeletReady"}}
		if err := k8s.Status().Update(ctx, node); err != nil {
			t.Fatal(err)
		}
		lease := &coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{Name: node.Name, Namespace: "kube-node-lease", OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Node", Name: node.Name, UID: node.UID}}},
			Spec: coordinationv1.LeaseSpec{HolderIdentity: ptr.To(node.Name), RenewTime: &metav1.MicroTime{Time: now.Add(-time.Second)}}}
		if err := k8s.Create(ctx, lease); err != nil {
			t.Fatal(err)
		}
		s := taskSession()
		s.Name, s.Namespace, s.UID, s.ResourceVersion = name, sessionNS, "", ""
		s.Finalizers = []string{Finalizer}
		if err := k8s.Create(ctx, s); err != nil {
			t.Fatal(err)
		}
		// Nothing of this Session is left for later tests' operators, which
		// reconcile every Session in the namespace: the Pod of its name (the
		// executor or the hold pod the expiry made), its HOME, the Session,
		// its Node and Lease.
		t.Cleanup(func() {
			purgeEnv(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sessionNS}},
				&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: HomeClaimName(name), Namespace: sessionNS}},
				&v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sessionNS}},
				&coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{Name: node.Name, Namespace: "kube-node-lease"}},
				&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: node.Name}})
		})
		home := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: HomeClaimName(s.Name), Namespace: s.Namespace, OwnerReferences: []metav1.OwnerReference{ownerRef(s)}, Finalizers: []string{Finalizer},
			Annotations: map[string]string{privateHistoryAdmissionAnnotation: "1"}},
			Spec: corev1.PersistentVolumeClaimSpec{AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}}}}
		if err := k8s.Create(ctx, home); err != nil {
			t.Fatal(err)
		}
		pod := tf.pod
		pod.Name, pod.Namespace, pod.UID = s.Name, s.Namespace, ""
		pod.OwnerReferences = []metav1.OwnerReference{ownerRef(s)}
		pod.Annotations[privateHistoryHomeAnnotation] = string(home.UID)
		pod.Spec.NodeName = node.Name
		pod.Spec.Volumes[0].PersistentVolumeClaim.ClaimName = home.Name
		pod.Status = corev1.PodStatus{}
		if err := k8s.Create(ctx, pod); err != nil {
			t.Fatal(err)
		}
		pod.Status = corev1.PodStatus{Phase: corev1.PodFailed, Reason: "DeadlineExceeded", StartTime: &metav1.Time{Time: now.Add(-time.Hour)}, ContainerStatuses: []corev1.ContainerStatus{{Name: ContainerName, Image: pod.Spec.Containers[0].Image,
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 143, Reason: "Error", StartedAt: metav1.NewTime(now.Add(-time.Hour)), FinishedAt: metav1.NewTime(now.Add(-2 * time.Second))}}}}}
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
		return s, pod, home
	}

	type rig struct {
		s       *v1alpha1.AgentSession
		pod     *corev1.Pod
		home    *corev1.PersistentVolumeClaim
		x       *agentdExec
		rescuer *expiryRescuer
		clock   time.Time
		mu      sync.Mutex
		// archivedAfterVerify: the first status write that holds Archived
		// came after a verify ran in the shelf.
		archivedWrites, archivedAfterVerify int
	}
	newRig := func(t *testing.T, name string) *rig {
		s, pod, home := envFixture(t, name)
		return &rig{s: s, pod: pod, home: home, x: newAgentdExec(t, name), rescuer: &expiryRescuer{}, clock: now}
	}
	operator := func(g *rig) *Reconciler {
		c := interceptor.NewClient(k8sWatch(t), interceptor.Funcs{SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, o client.Object, opts ...client.SubResourceUpdateOption) error {
			if s, ok := o.(*v1alpha1.AgentSession); ok && s.Status.PrivateHistory != nil && s.Status.PrivateHistory.Expiry != nil &&
				s.Status.PrivateHistory.Expiry.Phase == v1alpha1.HomeExpiryArchived && s.Status.PrivateHistory.Expiry.Refusal == "" {
				g.mu.Lock()
				g.archivedWrites++
				for _, call := range g.x.called() {
					if call == "verify" {
						g.archivedAfterVerify++
						break
					}
				}
				g.mu.Unlock()
			}
			return c.SubResource(sub).Update(ctx, o, opts...)
		}})
		return &Reconciler{Client: c, APIReader: c, Templates: templatesKey, PrivateTerminalRecord: true, PrivateExecutorTerminal: NewPrivateExecutorTerminalCounter(),
			HomeHistoryArchive: true, HomeExpiryCounters: NewHomeExpiryCounters(), Rescuer: g.rescuer,
			HistoryRelay: &ExecHistoryRelay{Exec: g.x, Shelf: &shelf.Shelf{Reader: c, Namespace: sessionNS}, Slots: shelf.NewExecSlots(shelf.DefaultExecSlots)},
			Now:          func() time.Time { g.mu.Lock(); defer g.mu.Unlock(); return g.clock }}
	}
	// step runs one reconcile in a fresh operator: a restart between every
	// two steps.
	step := func(t *testing.T, g *rig, times int) {
		t.Helper()
		for range times {
			if _, err := operator(g).Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(g.s)}); err != nil {
				t.Fatal(err)
			}
		}
	}
	history := func(t *testing.T, g *rig) *v1alpha1.PrivateHistoryStatus {
		t.Helper()
		got := &v1alpha1.AgentSession{}
		if err := k8s.Get(ctx, client.ObjectKeyFromObject(g.s), got); err != nil {
			t.Fatal(err)
		}
		return got.Status.PrivateHistory
	}
	named := func(t *testing.T, g *rig) *corev1.Pod {
		t.Helper()
		p := &corev1.Pod{}
		if err := k8s.Get(ctx, client.ObjectKeyFromObject(g.s), p); err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			t.Fatal(err)
		}
		return p
	}
	// kubelet runs a hold pod on the node, and removes a Pod that is being
	// deleted, as a kubelet does once its containers stopped.
	kubelet := func(t *testing.T, g *rig) {
		t.Helper()
		p := named(t, g)
		switch {
		case p == nil:
		case !p.DeletionTimestamp.IsZero():
			if err := k8s.Delete(ctx, p, client.GracePeriodSeconds(0), client.Preconditions{UID: &p.UID}); client.IgnoreNotFound(err) != nil {
				t.Fatal(err)
			}
		case isHoldPod(p) && p.Status.Phase != corev1.PodRunning:
			markRunning(t, p, g.pod.Spec.NodeName)
		}
	}
	claimIntact := func(t *testing.T, g *rig) {
		t.Helper()
		c := &corev1.PersistentVolumeClaim{}
		if err := k8s.Get(ctx, client.ObjectKeyFromObject(g.home), c); err != nil {
			t.Fatalf("the HOME claim is gone: %v", err)
		}
		if c.UID != g.home.UID || !c.DeletionTimestamp.IsZero() || !controllerutil.ContainsFinalizer(c, Finalizer) {
			t.Fatalf("the HOME claim changed: %+v", c.ObjectMeta)
		}
	}
	// untilRecord reconciles until H1's terminal record and the expiry
	// record are on the API server.
	untilRecord := func(t *testing.T, g *rig) {
		t.Helper()
		for range 8 {
			step(t, g, 1)
			if h := history(t, g); h.ExecutorTerminal != nil && h.Expiry != nil {
				return
			}
		}
		t.Fatalf("no terminal and expiry record: %+v", history(t, g))
	}
	// run reconciles in fresh operators, playing the kubelet between steps,
	// until the record is Archived and the hold pod is gone.
	run := func(t *testing.T, g *rig) *v1alpha1.PrivateHomeExpiryStatus {
		t.Helper()
		for range 40 {
			step(t, g, 1)
			kubelet(t, g)
			if e := history(t, g).Expiry; e != nil && e.Phase == v1alpha1.HomeExpiryArchived && named(t, g) == nil {
				return e
			}
		}
		t.Fatalf("not Archived after 40 steps: %+v", history(t, g).Expiry)
		return nil
	}

	for _, tc := range []struct {
		name string
		// lose plays a node reboot after the terminal record (T5).
		lose bool
	}{{"the expiry removes the ended executor", false}, {"the executor is lost after its record (T5)", true}} {
		t.Run(tc.name, func(t *testing.T) {
			g := newRig(t, map[bool]string{false: "h5-expiry-removed", true: "h5-expiry-lost"}[tc.lose])
			untilRecord(t, g)
			h := history(t, g)
			if h.ExecutorTerminal == nil || h.Expiry == nil || h.Expiry.Phase != v1alpha1.HomeExpiryWaiting || !h.Expiry.GraceStart.Equal(&h.ExecutorTerminal.ObservedAt) {
				t.Fatalf("no terminal record and Waiting record on the API server: %+v", h)
			}
			if tc.lose {
				if err := k8s.Delete(ctx, g.pod, client.Preconditions{UID: &g.pod.UID}); err != nil {
					t.Fatal(err)
				}
				step(t, g, 2)
				if c := conditionOf(t, g.s, ConditionPrivateHistory); c == nil || c.Status != metav1.ConditionTrue {
					t.Fatalf("a Pod lost after its record reads %+v; want ExecutorEnded, not PreservationUnknown", c)
				}
			}
			due := h.Expiry.DueAt.Time
			g.mu.Lock()
			g.clock = due.Add(-time.Minute)
			g.mu.Unlock()
			step(t, g, 3)
			if e := history(t, g).Expiry; e.Phase != v1alpha1.HomeExpiryWaiting || len(g.x.called()) != 0 {
				t.Fatalf("moved before the grace: %+v", e)
			}
			g.mu.Lock()
			g.clock = due.Add(time.Minute)
			g.mu.Unlock()
			e := run(t, g)
			if e.Archive == nil || e.HoldPodUID == "" || e.RescueStamp != "20261111-1200" {
				t.Fatalf("Archived without its archive, hold pod or rescue: %+v", e)
			}
			if g.archivedWrites == 0 || g.archivedAfterVerify != g.archivedWrites {
				t.Fatalf("%d Archived writes, %d after a verify: verify comes first", g.archivedWrites, g.archivedAfterVerify)
			}
			if entries := g.x.entries(t); len(entries) != 1 || entries[0] != e.Archive.Entry {
				t.Fatalf("archive entries %v, want only %s", entries, e.Archive.Entry)
			}
			if ver, err := g.x.archive.Verify(string(g.s.UID), e.Archive.ContentSHA256); err != nil || ver.ContentSHA256 != e.Archive.ContentSHA256 {
				t.Fatalf("the recorded entry does not verify: %v", err)
			}
			if ran := g.rescuer.ranIn(); len(ran) != 1 || ran[0] != e.HoldPodUID {
				t.Fatalf("rescues ran in %v; want once, in the hold pod %s", ran, e.HoldPodUID)
			}
			claimIntact(t, g)
			// Nothing more: no deletion in H5.
			g.mu.Lock()
			g.clock = due.Add(90 * 24 * time.Hour)
			g.mu.Unlock()
			calls := len(g.x.called())
			step(t, g, 3)
			if e2 := history(t, g).Expiry; e2.Phase != v1alpha1.HomeExpiryArchived || len(g.x.called()) != calls || named(t, g) != nil {
				t.Fatalf("Archived moved on: %+v", e2)
			}
			claimIntact(t, g)
		})
	}

	t.Run("two operators at once land one entry (T13)", func(t *testing.T) {
		g := newRig(t, "h5-expiry-two")
		untilRecord(t, g)
		g.mu.Lock()
		g.clock = history(t, g).Expiry.DueAt.Add(time.Minute)
		g.mu.Unlock()
		for range 40 {
			var wg sync.WaitGroup
			errs := make(chan error, 2)
			for range 2 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if _, err := operator(g).Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(g.s)}); err != nil {
						errs <- err
					}
				}()
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				t.Fatal(err)
			}
			kubelet(t, g)
			if e := history(t, g).Expiry; e != nil && e.Phase == v1alpha1.HomeExpiryArchived && named(t, g) == nil {
				break
			}
		}
		e := history(t, g).Expiry
		if e == nil || e.Phase != v1alpha1.HomeExpiryArchived {
			t.Fatalf("two operators did not reach Archived: %+v", e)
		}
		if entries := g.x.entries(t); len(entries) != 1 || entries[0] != e.Archive.Entry {
			t.Fatalf("two operators left entries %v", entries)
		}
		claimIntact(t, g)
	})

	// Section 14: a cancel must record an expiryClearedAt newer than the last
	// one, or the schema refuses it. Two cancels by new Pods, on an operator
	// clock that does not move, both land: the second one's time moves
	// forward a second, and each new record's grace starts no earlier than
	// the cancel before it.
	t.Run("two cancels on one clock both land, expiryClearedAt forward", func(t *testing.T) {
		g := newRig(t, "h5-expiry-cancel")
		untilRecord(t, g)
		var cleared []metav1.Time
		for _, name := range []string{"h5-expiry-cancel-debug-a", "h5-expiry-cancel-debug-b"} {
			debug := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sessionNS},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "debug", Image: "synthetic.invalid/debug:1"}},
					Volumes: []corev1.Volume{{Name: "home", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: g.home.Name}}}}}}
			if err := k8s.Create(ctx, debug); err != nil {
				t.Fatal(err)
			}
			step(t, g, 2)
			h := history(t, g)
			if h.Expiry != nil || h.ExpiryClearedAt == nil {
				t.Fatalf("a Pod mounting the HOME did not cancel on the API server: %+v", h)
			}
			cleared = append(cleared, *h.ExpiryClearedAt)
			purgeEnv(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sessionNS}})
			step(t, g, 2)
			h = history(t, g)
			if h.Expiry == nil || h.Expiry.GraceStart.Before(h.ExpiryClearedAt) {
				t.Fatalf("no new record after the cancel, or its grace starts before it: %+v", h)
			}
		}
		if !cleared[1].After(cleared[0].Time) {
			t.Fatalf("expiryClearedAt %v then %v; want it to move forward", cleared[0], cleared[1])
		}
		claimIntact(t, g)
	})

	t.Run("the schema refuses a record past the terminal record", func(t *testing.T) {
		g := newRig(t, "h5-expiry-schema")
		untilRecord(t, g)
		got := &v1alpha1.AgentSession{}
		if err := k8s.Get(ctx, client.ObjectKeyFromObject(g.s), got); err != nil {
			t.Fatal(err)
		}
		got.Status.PrivateHistory.Expiry.Phase = v1alpha1.HomeExpiryArchived
		if err := k8s.Status().Update(ctx, got); !apierrors.IsInvalid(err) {
			t.Fatalf("Archived without an archive or a rescue accepted: %v", err)
		}
	})
}

// purgeEnv removes test objects for good: it lifts their finalizers and
// deletes them at once. envtest has no garbage collector or kubelet, so
// nothing else would.
func purgeEnv(objs ...client.Object) {
	ctx := context.Background()
	for _, o := range objs {
		cur, ok := o.DeepCopyObject().(client.Object)
		if !ok || k8s.Get(ctx, client.ObjectKeyFromObject(o), cur) != nil {
			continue
		}
		if len(cur.GetFinalizers()) > 0 {
			cur.SetFinalizers(nil)
			_ = k8s.Update(ctx, cur)
		}
		_ = k8s.Delete(ctx, cur, client.GracePeriodSeconds(0))
	}
}

// k8sWatch is a client with watch on the envtest API server.
func k8sWatch(t *testing.T) client.WithWatch {
	t.Helper()
	c, err := client.NewWithWatch(restCfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func conditionOf(t *testing.T, s *v1alpha1.AgentSession, typ string) *metav1.Condition {
	t.Helper()
	got := &v1alpha1.AgentSession{}
	if err := k8s.Get(context.Background(), client.ObjectKeyFromObject(s), got); err != nil {
		t.Fatal(err)
	}
	return condition(&got.Status, typ)
}
