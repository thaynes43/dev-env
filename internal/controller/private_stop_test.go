package controller

// DESIGN-025 unit H3: the stop request for a Reaping private Session's
// recorded executor (section 4, as ADR-006 narrowed it; T14). The unit tests
// drive Reconcile against the fake client with a fake clock; the envtest test
// drives it against a real API server with the CRD's rules.

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// newStopFixture is newTerminalFixture's private Session, reaped, with its
// recorded executor running for an hour: what section 4 stops.
func newStopFixture() terminalFixture {
	f := newTerminalFixture()
	f.s.DeletionTimestamp = &metav1.Time{Time: f.now.Add(-time.Minute)}
	f.pod.Status = corev1.PodStatus{Phase: corev1.PodRunning, StartTime: &metav1.Time{Time: f.now.Add(-time.Hour)},
		ContainerStatuses: []corev1.ContainerStatus{{Name: ContainerName, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(f.now.Add(-time.Hour))}}}}}
	return f
}

func (f terminalFixture) stopReconciler(c client.Client, reader client.Reader, stops *prometheus.CounterVec) *Reconciler {
	r := f.reconciler(c, reader, NewPrivateExecutorTerminalCounter())
	r.PrivateExecutorStop, r.PrivateExecutorStops = true, stops
	return r
}

// podPatches records the Pod patches a client sends and how many landed.
type podPatches struct {
	mu     sync.Mutex
	sent   [][]byte
	landed int
}

func (p *podPatches) count() (sent, landed int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.sent), p.landed
}

// watchPodPatches wraps base so that every Pod patch is recorded; before, when
// set, runs before a Pod patch reaches base, and may return an error in its
// place.
func watchPodPatches(base client.WithWatch, p *podPatches, before func(ctx context.Context) error) client.WithWatch {
	return interceptor.NewClient(base, interceptor.Funcs{Patch: func(ctx context.Context, c client.WithWatch, o client.Object, patch client.Patch, opts ...client.PatchOption) error {
		if _, ok := o.(*corev1.Pod); !ok {
			return c.Patch(ctx, o, patch, opts...)
		}
		data, err := patch.Data(o)
		if err != nil {
			return err
		}
		p.mu.Lock()
		p.sent = append(p.sent, data)
		p.mu.Unlock()
		if before != nil {
			if err := before(ctx); err != nil {
				return err
			}
		}
		err = c.Patch(ctx, o, patch, opts...)
		if err == nil {
			p.mu.Lock()
			p.landed++
			p.mu.Unlock()
		}
		return err
	}})
}

func stopCount(c *prometheus.CounterVec, result string) float64 { return terminalCount(c, result) }

func storedPod(t *testing.T, c client.Reader, pod *corev1.Pod) *corev1.Pod {
	t.Helper()
	var got corev1.Pod
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(pod), &got); err != nil {
		t.Fatal(err)
	}
	return &got
}

// T14 and the order's first case: a reap records the intent, then exactly one
// patch with the two tests sets the deadline; more reconciles send nothing;
// once the kubelet ends the Pod, the terminal record follows.
func TestPrivateStopReapRecordsIntentThenOnePatchThenTerminal(t *testing.T) {
	f := newStopFixture()
	base := f.client()
	patches := &podPatches{}
	c := watchPodPatches(base, patches, nil)
	stops := NewPrivateExecutorStopCounter()
	r := f.stopReconciler(c, c, stops)
	reconcileSession(t, r, f.s, 4)

	h := storedHistory(t, base, f.s).Status.PrivateHistory
	want := &v1alpha1.PrivateExecutorStopRequestStatus{Version: 1, PodUID: "original-executor", RequestedAt: metav1.NewTime(f.now)}
	if !apiequality.Semantic.DeepEqual(h.StopRequest, want) {
		t.Fatalf("stop intent\n got %+v\nwant %+v", h.StopRequest, want)
	}
	if h.StopState != "Unknown" || h.ExecutorTerminal != nil {
		t.Fatalf("the stop request claimed a stop: %+v", h)
	}
	sent, landed := patches.count()
	if sent != 1 || landed != 1 {
		t.Fatalf("pod patches: %d sent, %d landed; want exactly one", sent, landed)
	}
	var ops []map[string]any
	if err := json.Unmarshal(patches.sent[0], &ops); err != nil {
		t.Fatal(err)
	}
	pod := storedPod(t, base, f.pod)
	wantOps := []map[string]any{
		{"op": "test", "path": "/metadata/uid", "value": "original-executor"},
		{"op": "test", "path": "/metadata/resourceVersion", "value": ops[1]["value"]},
		{"op": "add", "path": "/spec/activeDeadlineSeconds", "value": float64(3600 + 30)},
	}
	if len(ops) != 3 || !apiequality.Semantic.DeepEqual(ops, wantOps) || ops[1]["value"] == "" {
		t.Fatalf("the stop patch is not exactly two tests and the deadline: %s", patches.sent[0])
	}
	if pod.Spec.ActiveDeadlineSeconds == nil || *pod.Spec.ActiveDeadlineSeconds != 3630 {
		t.Fatalf("activeDeadlineSeconds %v, want 3630", pod.Spec.ActiveDeadlineSeconds)
	}
	if stopCount(stops, stopSent) != 1 || stopCount(stops, stopConflict) != 0 || stopCount(stops, stopHeld) != 0 {
		t.Fatal("the stop was not counted once as sent")
	}

	// Ten minutes on, the deadline is lower than a new one would be: nothing
	// more is sent.
	r.Now = func() time.Time { return f.now.Add(10 * time.Minute) }
	reconcileSession(t, r, f.s, 3)
	if sent, _ := patches.count(); sent != 1 {
		t.Fatalf("%d pod patches after more reconciles; the deadline must be set once", sent)
	}

	// The kubelet ends the Pod at its deadline; the terminal record follows
	// and the intent stays as it was.
	pod.Status = corev1.PodStatus{Phase: corev1.PodFailed, Reason: "DeadlineExceeded", StartTime: pod.Status.StartTime, ContainerStatuses: []corev1.ContainerStatus{{Name: ContainerName,
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 143, Reason: "Error", FinishedAt: metav1.NewTime(f.now.Add(-2 * time.Second))}}}}}
	if err := base.Status().Update(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	r.Now = func() time.Time { return f.now }
	reconcileSession(t, r, f.s, 3)
	h = storedHistory(t, base, f.s).Status.PrivateHistory
	if h.ExecutorTerminal == nil || h.ExecutorTerminal.Reason != "DeadlineExceeded" || !apiequality.Semantic.DeepEqual(h.StopRequest, want) {
		t.Fatalf("no terminal record after the stop, or the intent changed: %+v", h)
	}
	if sent, _ := patches.count(); sent != 1 || stopCount(stops, stopSent) != 1 {
		t.Fatal("a terminal executor was patched or counted again")
	}
}

// Nothing is stopped while the flag is off, without the terminal record flag,
// or for any Session or Pod outside section 4's shape.
func TestPrivateStopRefusals(t *testing.T) {
	for _, tc := range []struct {
		name     string
		edit     func(*terminalFixture)
		flagsOff bool
		noRecord bool
		held     bool
	}{
		{name: "flag off", flagsOff: true},
		{name: "terminal record flag off", noRecord: true},
		{name: "hold", held: true, edit: func(f *terminalFixture) {
			f.s.Annotations = map[string]string{v1alpha1.AnnotationHomeExpiry: v1alpha1.HomeExpiryHold}
		}},
		{name: "unknown override value", held: true, edit: func(f *terminalFixture) {
			f.s.Annotations = map[string]string{v1alpha1.AnnotationHomeExpiry: "Hold"}
		}},
		{name: "empty override value", held: true, edit: func(f *terminalFixture) {
			f.s.Annotations = map[string]string{v1alpha1.AnnotationHomeExpiry: ""}
		}},
		{name: "suspended before its archive deadline", edit: func(f *terminalFixture) {
			f.s.DeletionTimestamp = nil
			f.s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
			f.s.Status.SuspendedAt = &metav1.Time{Time: f.now.Add(-time.Hour)}
		}},
		{name: "suspended by the idle timer, past its archive deadline", edit: func(f *terminalFixture) {
			f.s.DeletionTimestamp = nil
			f.s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
			f.s.Annotations = map[string]string{v1alpha1.AnnotationSuspendedBy: IdleTimerSuspender}
			f.s.Status.SuspendedAt = &metav1.Time{Time: f.now.Add(-30 * 24 * time.Hour)}
		}},
		{name: "running", edit: func(f *terminalFixture) { f.s.DeletionTimestamp = nil }},
		{name: "canary original", edit: func(f *terminalFixture) {
			f.s.Spec.ProjectCanary = &v1alpha1.ProjectCanaryBinding{Version: 1, ID: "canary", Client: "dev/dev-env"}
		}},
		{name: "unbound HOME", edit: func(f *terminalFixture) { f.s.Status.PrivateHistory.HomeUID = "" }},
		{name: "unknown admission", edit: func(f *terminalFixture) {
			f.s.Status.PrivateHistory.Admission = "Unknown"
			f.s.Status.PrivateHistory.HomeUID = ""
		}},
		{name: "unrecorded executor of another intent", edit: func(f *terminalFixture) {
			f.s.Status.PrivateHistory.LastExecutor = nil
			f.pod.Annotations[privateHistoryExecutorAnnotation] = "another-intent"
		}},
		{name: "replaced executor", edit: func(f *terminalFixture) { f.pod.UID = "replacement" }},
		{name: "hold pod", edit: func(f *terminalFixture) { f.pod.Labels[v1alpha1.LabelHold] = "true" }},
		{name: "not started", edit: func(f *terminalFixture) { f.pod.Status.StartTime = nil }},
		{name: "already terminal", edit: func(f *terminalFixture) { f.pod.Status.Phase = corev1.PodSucceeded }},
		{name: "a lower deadline already set", edit: func(f *terminalFixture) { f.pod.Spec.ActiveDeadlineSeconds = ptr.To(int64(60)) }},
		{name: "missed marker", edit: func(f *terminalFixture) {
			f.s.Status.PrivateHistory.ExecutorMissed = &v1alpha1.PrivateExecutorMissedStatus{Version: 1, PodUID: "original-executor", ObservedAt: metav1.NewTime(f.now)}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newStopFixture()
			if tc.edit != nil {
				tc.edit(&f)
			}
			base := f.client()
			patches := &podPatches{}
			c := watchPodPatches(base, patches, nil)
			stops := NewPrivateExecutorStopCounter()
			r := f.stopReconciler(c, c, stops)
			r.PrivateExecutorStop = !tc.flagsOff
			r.PrivateTerminalRecord = !tc.noRecord
			for range 3 {
				// Some shapes are refused with an error by other guards; the
				// stop is judged on what was written either way.
				_, _ = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(f.s)})
			}
			if sent, _ := patches.count(); sent != 0 {
				t.Fatalf("%d pod patches sent", sent)
			}
			if h := storedHistory(t, base, f.s).Status.PrivateHistory; h != nil && h.StopRequest != nil {
				t.Fatalf("a stop intent was recorded: %+v", h.StopRequest)
			}
			if pod := storedPod(t, base, f.pod); !apiequality.Semantic.DeepEqual(pod.Spec.ActiveDeadlineSeconds, f.pod.Spec.ActiveDeadlineSeconds) {
				t.Fatalf("activeDeadlineSeconds changed: %v", pod.Spec.ActiveDeadlineSeconds)
			}
			if held := stopCount(stops, stopHeld) > 0; held != tc.held || stopCount(stops, stopSent) != 0 {
				t.Fatalf("held counted %v (want %v), sent %v", stopCount(stops, stopHeld), tc.held, stopCount(stops, stopSent))
			}
		})
	}
}

// A shared-workspace executor is never stopped here: its shape fails first,
// and its Session never reaches the private path.
func TestPrivateStopNeverForASharedExecutor(t *testing.T) {
	f := stoppedWorkspaceFixture()
	f.s.DeletionTimestamp = &metav1.Time{Time: f.now}
	f.pod.Status = corev1.PodStatus{Phase: corev1.PodRunning, StartTime: &metav1.Time{Time: f.now.Add(-time.Hour)}}
	if privateStopShape(f.s) {
		t.Fatal("a shared Session passes the private stop shape")
	}
	patches := &podPatches{}
	base := f.client().(client.WithWatch)
	c := watchPodPatches(base, patches, nil)
	r := &Reconciler{Client: c, APIReader: c, PrivateTerminalRecord: true, PrivateExecutorStop: true, PrivateExecutorStops: NewPrivateExecutorStopCounter(), Now: func() time.Time { return f.now }}
	for range 3 {
		_, _ = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(f.s)})
	}
	if sent, _ := patches.count(); sent != 0 {
		t.Fatalf("%d pod patches sent to a shared executor", sent)
	}
}

// release-now skips only the expiry's grace; it is no hold, so the stop goes.
func TestPrivateStopReleaseNowIsNoHold(t *testing.T) {
	f := newStopFixture()
	f.s.Annotations = map[string]string{v1alpha1.AnnotationHomeExpiry: v1alpha1.HomeExpiryReleaseNow}
	base := f.client()
	patches := &podPatches{}
	c := watchPodPatches(base, patches, nil)
	reconcileSession(t, f.stopReconciler(c, c, NewPrivateExecutorStopCounter()), f.s, 2)
	if _, landed := patches.count(); landed != 1 || storedHistory(t, base, f.s).Status.PrivateHistory.StopRequest == nil {
		t.Fatal("release-now blocked the stop")
	}
}

// A deadline set higher than the stop's (by a template, say) is lowered.
func TestPrivateStopLowersAHigherDeadline(t *testing.T) {
	f := newStopFixture()
	f.pod.Spec.ActiveDeadlineSeconds = ptr.To(int64(30 * 24 * 3600))
	base := f.client()
	patches := &podPatches{}
	c := watchPodPatches(base, patches, nil)
	reconcileSession(t, f.stopReconciler(c, c, NewPrivateExecutorStopCounter()), f.s, 3)
	if pod := storedPod(t, base, f.pod); pod.Spec.ActiveDeadlineSeconds == nil || *pod.Spec.ActiveDeadlineSeconds != 3630 {
		t.Fatalf("activeDeadlineSeconds %v, want 3630", pod.Spec.ActiveDeadlineSeconds)
	}
	if _, landed := patches.count(); landed != 1 {
		t.Fatalf("%d patches landed, want 1", landed)
	}
}

// A hold written between the Session read and the intent write makes that
// write conflict: no patch is sent then or later.
func TestPrivateStopHoldBetweenReadAndIntentWins(t *testing.T) {
	f := newStopFixture()
	base := f.client()
	patches := &podPatches{}
	once := false
	c := interceptor.NewClient(watchPodPatches(base, patches, nil), interceptor.Funcs{SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, o client.Object, opts ...client.SubResourceUpdateOption) error {
		if s, ok := o.(*v1alpha1.AgentSession); ok && !once && s.Status.PrivateHistory != nil && s.Status.PrivateHistory.StopRequest != nil {
			once = true
			var latest v1alpha1.AgentSession
			if err := base.Get(ctx, client.ObjectKeyFromObject(s), &latest); err != nil {
				return err
			}
			latest.Annotations = map[string]string{v1alpha1.AnnotationHomeExpiry: v1alpha1.HomeExpiryHold}
			if err := base.Update(ctx, &latest); err != nil {
				return err
			}
		}
		return c.SubResource(sub).Update(ctx, o, opts...)
	}})
	stops := NewPrivateExecutorStopCounter()
	r := f.stopReconciler(c, c, stops)
	for range 4 {
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(f.s)}); err != nil {
			t.Fatal(err)
		}
	}
	if !once {
		t.Fatal("the hold was never written between the read and the intent")
	}
	if sent, _ := patches.count(); sent != 0 {
		t.Fatalf("%d pod patches sent past a hold", sent)
	}
	if h := storedHistory(t, base, f.s).Status.PrivateHistory; h.StopRequest != nil {
		t.Fatalf("the intent was stored over the hold: %+v", h.StopRequest)
	}
	if stopCount(stops, stopConflict) != 1 || stopCount(stops, stopHeld) == 0 || stopCount(stops, stopSent) != 0 {
		t.Fatalf("conflict %v held %v sent %v", stopCount(stops, stopConflict), stopCount(stops, stopHeld), stopCount(stops, stopSent))
	}
}

// A stale CRD that prunes the intent fails its confirmation: no patch.
func TestPrivateStopPrunedIntentSendsNothing(t *testing.T) {
	f := newStopFixture()
	base := f.client()
	patches := &podPatches{}
	c := interceptor.NewClient(watchPodPatches(base, patches, nil), interceptor.Funcs{SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, o client.Object, opts ...client.SubResourceUpdateOption) error {
		if s, ok := o.(*v1alpha1.AgentSession); ok && s.Status.PrivateHistory != nil {
			s.Status.PrivateHistory.StopRequest = nil
		}
		return c.SubResource(sub).Update(ctx, o, opts...)
	}})
	r := f.stopReconciler(c, c, NewPrivateExecutorStopCounter())
	for range 3 {
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(f.s)}); err == nil {
			t.Fatal("a pruned intent was confirmed")
		}
	}
	if sent, _ := patches.count(); sent != 0 {
		t.Fatalf("%d pod patches sent on an unconfirmed intent", sent)
	}
}

// An operator restart between the confirmed intent and the patch: the next
// operator adopts the stored intent unchanged and sends the one patch. A
// restart after the patch landed sends none.
func TestPrivateStopRestartAdoptsTheIntentAndNeverPatchesTwice(t *testing.T) {
	for _, when := range []string{"before the patch", "after the patch landed"} {
		t.Run(when, func(t *testing.T) {
			f := newStopFixture()
			base := f.client()
			patches := &podPatches{}
			killed := errors.New("the operator stopped here")
			var a client.WithWatch
			if when == "before the patch" {
				a = watchPodPatches(base, patches, func(context.Context) error { return killed })
			} else {
				a = interceptor.NewClient(watchPodPatches(base, patches, nil), interceptor.Funcs{Patch: func(ctx context.Context, c client.WithWatch, o client.Object, p client.Patch, opts ...client.PatchOption) error {
					if err := c.Patch(ctx, o, p, opts...); err != nil {
						return err
					}
					return killed
				}})
			}
			if _, err := f.stopReconciler(a, a, NewPrivateExecutorStopCounter()).Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(f.s)}); !errors.Is(err, killed) {
				t.Fatalf("the first operator did not stop at the patch: %v", err)
			}
			intent := storedHistory(t, base, f.s).Status.PrivateHistory.StopRequest
			if intent == nil {
				t.Fatal("no confirmed intent before the patch")
			}
			// New operators, five minutes later.
			for range 2 {
				c := watchPodPatches(base, patches, nil)
				r := f.stopReconciler(c, c, NewPrivateExecutorStopCounter())
				r.Now = func() time.Time { return f.now.Add(5 * time.Minute) }
				reconcileSession(t, r, f.s, 2)
			}
			if got := storedHistory(t, base, f.s).Status.PrivateHistory.StopRequest; !apiequality.Semantic.DeepEqual(got, intent) {
				t.Fatalf("a restart changed the intent: %+v -> %+v", intent, got)
			}
			_, landed := patches.count()
			want := int64(3630)
			if when == "before the patch" {
				want = 3600 + 300 + 30
			}
			if pod := storedPod(t, base, f.pod); landed != 1 || pod.Spec.ActiveDeadlineSeconds == nil || *pod.Spec.ActiveDeadlineSeconds != want {
				t.Fatalf("%d patches landed, deadline %v; want 1 and %d", landed, pod.Spec.ActiveDeadlineSeconds, want)
			}
		})
	}
}

// Two replicas overlap: B runs whole inside A, between A's Session read and
// its intent write, or between A's intent and its patch. One intent is
// stored, and one patch lands.
func TestPrivateStopTwoReplicasPatchOnce(t *testing.T) {
	for _, when := range []string{"before A's intent write", "before A's patch"} {
		t.Run(when, func(t *testing.T) {
			f := newStopFixture()
			base := f.client()
			patches := &podPatches{}
			stopsA, stopsB := NewPrivateExecutorStopCounter(), NewPrivateExecutorStopCounter()
			cb := watchPodPatches(base, patches, nil)
			b := f.stopReconciler(cb, cb, stopsB)
			ran := false
			runB := func(ctx context.Context) error {
				if !ran {
					ran = true
					for range 2 {
						if _, err := b.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(f.s)}); err != nil {
							t.Errorf("B: %v", err)
						}
					}
				}
				return nil
			}
			var ca client.WithWatch
			if when == "before A's intent write" {
				ca = interceptor.NewClient(watchPodPatches(base, patches, nil), interceptor.Funcs{SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, o client.Object, opts ...client.SubResourceUpdateOption) error {
					if s, ok := o.(*v1alpha1.AgentSession); ok && s.Status.PrivateHistory != nil && s.Status.PrivateHistory.StopRequest != nil {
						_ = runB(ctx)
					}
					return c.SubResource(sub).Update(ctx, o, opts...)
				}})
			} else {
				ca = watchPodPatches(base, patches, runB)
			}
			a := f.stopReconciler(ca, ca, stopsA)
			_, errA := a.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(f.s)})
			if !ran {
				t.Fatal("B never ran inside A")
			}
			// A's stale write or stale patch is refused; nothing A did after
			// it lands.
			for range 2 {
				_, _ = a.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(f.s)})
			}
			_, landed := patches.count()
			if landed != 1 {
				t.Fatalf("%d patches landed, want exactly 1 (A: %v)", landed, errA)
			}
			if stopCount(stopsA, stopSent)+stopCount(stopsB, stopSent) != 1 {
				t.Fatalf("sent counted %v by A and %v by B", stopCount(stopsA, stopSent), stopCount(stopsB, stopSent))
			}
			if when == "before A's intent write" && stopCount(stopsA, stopConflict) != 1 {
				t.Fatalf("A's stale intent write was not a conflict: %v", errA)
			}
			if h := storedHistory(t, base, f.s).Status.PrivateHistory; h.StopRequest == nil || h.StopRequest.PodUID != "original-executor" {
				t.Fatalf("no single stored intent: %+v", h.StopRequest)
			}
		})
	}
}

// envtest (T14 on a real API server): reap, intent, one patch the API server
// accepts on a running Pod, a terminal record after the kubelet ends it; a
// hold written between the read and the intent conflicts; a stale Pod
// resourceVersion fails the patch's test.
func TestPrivateStopOnTheAPIServer(t *testing.T) {
	ctx := context.Background()
	if err := k8s.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-node-lease"}}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
	watching, err := client.NewWithWatch(restCfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	envFixture := func(t *testing.T, name string) (*v1alpha1.AgentSession, *corev1.Pod) {
		t.Helper()
		f := newTerminalFixture()
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
		home := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: HomeClaimName(s.Name), Namespace: s.Namespace, OwnerReferences: []metav1.OwnerReference{ownerRef(s)}, Finalizers: []string{Finalizer},
			Annotations: map[string]string{privateHistoryAdmissionAnnotation: "1"}},
			Spec: corev1.PersistentVolumeClaimSpec{AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}}}}
		if err := k8s.Create(ctx, home); err != nil {
			t.Fatal(err)
		}
		pod := f.pod
		pod.Name, pod.Namespace, pod.UID = s.Name, s.Namespace, ""
		pod.OwnerReferences = []metav1.OwnerReference{ownerRef(s)}
		pod.Annotations[privateHistoryHomeAnnotation] = string(home.UID)
		pod.Spec.NodeName = node.Name
		pod.Spec.Volumes[0].PersistentVolumeClaim.ClaimName = home.Name
		pod.Status = corev1.PodStatus{}
		if err := k8s.Create(ctx, pod); err != nil {
			t.Fatal(err)
		}
		pod.Status = corev1.PodStatus{Phase: corev1.PodRunning, StartTime: &metav1.Time{Time: now.Add(-time.Hour)}, ContainerStatuses: []corev1.ContainerStatus{{Name: ContainerName, Image: pod.Spec.Containers[0].Image,
			State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(now.Add(-time.Hour))}}}}}
		if err := k8s.Status().Update(ctx, pod); err != nil {
			t.Fatal(err)
		}
		s.Status.PrivateHistory = &v1alpha1.PrivateHistoryStatus{Version: 1, SessionUID: string(s.UID), HomeClaim: home.Name, HomeUID: string(home.UID), BindingGeneration: s.Generation, Admission: sharedStarted,
			Provider: string(s.Spec.Agent), StopState: "Unknown", ExecutorAttempted: true, ExecutorIntent: "original-intent",
			LastExecutor: &v1alpha1.PrivateExecutorStatus{PodUID: string(pod.UID), PodName: pod.Name, NodeName: node.Name, Revision: "original-template", Generation: s.Generation}}
		if err := k8s.Status().Update(ctx, s); err != nil {
			t.Fatal(err)
		}
		return s, pod
	}
	reap := func(t *testing.T, s *v1alpha1.AgentSession) {
		t.Helper()
		if err := k8s.Delete(ctx, s); err != nil {
			t.Fatal(err)
		}
		// The reap bumps the generation; the recorded executor is observed
		// again at the new one before any stop.
	}
	operatorAt := func(c client.Client, stops *prometheus.CounterVec) *Reconciler {
		return &Reconciler{Client: c, APIReader: c, PrivateTerminalRecord: true, PrivateExecutorTerminal: NewPrivateExecutorTerminalCounter(),
			PrivateExecutorStop: true, PrivateExecutorStops: stops, Now: func() time.Time { return now }}
	}
	history := func(t *testing.T, s *v1alpha1.AgentSession) *v1alpha1.PrivateHistoryStatus {
		t.Helper()
		got := &v1alpha1.AgentSession{}
		if err := k8s.Get(ctx, client.ObjectKeyFromObject(s), got); err != nil {
			t.Fatal(err)
		}
		return got.Status.PrivateHistory
	}

	t.Run("reap, intent, one patch, then the terminal record", func(t *testing.T) {
		s, pod := envFixture(t, "h3-stop-reaped")
		patches := &podPatches{}
		c := watchPodPatches(watching, patches, nil)
		stops := NewPrivateExecutorStopCounter()
		reconcileSession(t, operatorAt(c, stops), s, 2)
		if h := history(t, s); h.StopRequest != nil {
			t.Fatal("a running Session's executor got a stop intent")
		}
		reap(t, s)
		reconcileSession(t, operatorAt(c, stops), s, 4)
		h := history(t, s)
		if h.StopRequest == nil || h.StopRequest.PodUID != string(pod.UID) || !h.StopRequest.RequestedAt.Equal(&metav1.Time{Time: now}) {
			t.Fatalf("no confirmed stop intent on the API server: %+v", h.StopRequest)
		}
		got := storedPod(t, k8s, pod)
		if got.Spec.ActiveDeadlineSeconds == nil || *got.Spec.ActiveDeadlineSeconds != 3630 {
			t.Fatalf("activeDeadlineSeconds %v, want 3630", got.Spec.ActiveDeadlineSeconds)
		}
		if sent, landed := patches.count(); sent != 1 || landed != 1 || stopCount(stops, stopSent) != 1 {
			t.Fatalf("%d sent, %d landed, sent counted %v; want one", sent, landed, stopCount(stops, stopSent))
		}
		// The kubelet's report at the deadline.
		got.Status = corev1.PodStatus{Phase: corev1.PodFailed, Reason: "DeadlineExceeded", StartTime: got.Status.StartTime, ContainerStatuses: []corev1.ContainerStatus{{Name: ContainerName, Image: got.Spec.Containers[0].Image,
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 143, Reason: "Error", StartedAt: metav1.NewTime(now.Add(-time.Hour)), FinishedAt: metav1.NewTime(now.Add(-2 * time.Second))}}}}}
		if err := k8s.Status().Update(ctx, got); err != nil {
			t.Fatal(err)
		}
		reconcileSession(t, operatorAt(c, stops), s, 3)
		h = history(t, s)
		if h.ExecutorTerminal == nil || h.ExecutorTerminal.PodUID != string(pod.UID) || h.ExecutorTerminal.Reason != "DeadlineExceeded" || h.StopRequest == nil {
			t.Fatalf("the terminal record did not follow the stop: %+v", h)
		}
		if sent, _ := patches.count(); sent != 1 {
			t.Fatalf("%d patches; the deadline is set once", sent)
		}
	})

	t.Run("a hold between the read and the intent conflicts", func(t *testing.T) {
		s, pod := envFixture(t, "h3-stop-held")
		reap(t, s)
		patches := &podPatches{}
		once := false
		c := interceptor.NewClient(watchPodPatches(watching, patches, nil), interceptor.Funcs{SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, o client.Object, opts ...client.SubResourceUpdateOption) error {
			if typed, ok := o.(*v1alpha1.AgentSession); ok && !once && typed.Status.PrivateHistory != nil && typed.Status.PrivateHistory.StopRequest != nil {
				once = true
				var latest v1alpha1.AgentSession
				if err := k8s.Get(ctx, client.ObjectKeyFromObject(typed), &latest); err != nil {
					return err
				}
				latest.Annotations = map[string]string{v1alpha1.AnnotationHomeExpiry: v1alpha1.HomeExpiryHold}
				if err := k8s.Update(ctx, &latest); err != nil {
					return err
				}
			}
			return c.SubResource(sub).Update(ctx, o, opts...)
		}})
		stops := NewPrivateExecutorStopCounter()
		for range 4 {
			if _, err := operatorAt(c, stops).Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(s)}); err != nil {
				t.Fatal(err)
			}
		}
		if !once || stopCount(stops, stopConflict) != 1 || stopCount(stops, stopHeld) == 0 {
			t.Fatalf("hold written %v, conflict %v, held %v", once, stopCount(stops, stopConflict), stopCount(stops, stopHeld))
		}
		if sent, _ := patches.count(); sent != 0 || history(t, s).StopRequest != nil || storedPod(t, k8s, pod).Spec.ActiveDeadlineSeconds != nil {
			t.Fatal("the stop went past a hold")
		}
	})

	t.Run("two replicas: a stale Pod revision fails the test", func(t *testing.T) {
		s, pod := envFixture(t, "h3-stop-replicas")
		reap(t, s)
		patches := &podPatches{}
		stopsA, stopsB := NewPrivateExecutorStopCounter(), NewPrivateExecutorStopCounter()
		cb := watchPodPatches(watching, patches, nil)
		ran := false
		ca := watchPodPatches(watching, patches, func(ctx context.Context) error {
			if !ran {
				ran = true
				reconcileSession(t, operatorAt(cb, stopsB), s, 2)
			}
			return nil
		})
		if _, err := operatorAt(ca, stopsA).Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(s)}); err != nil {
			t.Fatal(err)
		}
		reconcileSession(t, operatorAt(ca, stopsA), s, 2)
		if _, landed := patches.count(); !ran || landed != 1 {
			t.Fatalf("B ran %v, %d patches landed; want exactly one", ran, landed)
		}
		if stopCount(stopsA, stopConflict) != 1 || stopCount(stopsA, stopSent) != 0 || stopCount(stopsB, stopSent) != 1 {
			t.Fatalf("A conflict %v sent %v, B sent %v", stopCount(stopsA, stopConflict), stopCount(stopsA, stopSent), stopCount(stopsB, stopSent))
		}
		if got := storedPod(t, k8s, pod); got.Spec.ActiveDeadlineSeconds == nil || *got.Spec.ActiveDeadlineSeconds != 3630 {
			t.Fatalf("activeDeadlineSeconds %v", got.Spec.ActiveDeadlineSeconds)
		}
	})
}
