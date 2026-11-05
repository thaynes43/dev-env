package controller

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/templates"
)

// productionRetention configures r as the operator ran during the storm
// (haynes-ops operator HelmRelease, 2026-11-04): private history retention,
// the terminal record, the executor stop and the terminal task reclaim (with
// its legacy proof) on, a 10 minute reclaim grace, and the HOME history
// archive off. The templates come from the ConfigMap templatesConfigMap makes.
func productionRetention(r *Reconciler) {
	r.Templates = templatesKey
	r.PrivateHistoryRetention, r.PrivateTerminalRecord, r.PrivateExecutorStop = true, true, true
	r.TerminalTaskPodReclaim, r.LegacyTerminalTaskReclaim = true, true
	r.TerminalTaskReclaimGrace = 10 * time.Minute
}

func templatesConfigMap() *corev1.ConfigMap {
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: templatesKey.Name, Namespace: templatesKey.Namespace},
		Data: map[string]string{templates.Key: exampleDoc}}
}

// suspendedThisWeek suspends s a day ago: inside the default 7 day archive
// window, so the archive sets no removal verdict.
func suspendedThisWeek(s *v1alpha1.AgentSession) {
	s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
	s.Status.SuspendedAt = &metav1.Time{Time: time.Now().Add(-24 * time.Hour).Truncate(time.Second)}
}

// recordTerminal adds the confirmed terminal record of f's recorded executor,
// as verifyExecutorTerminated writes it.
func recordTerminal(f terminalFixture) {
	h := f.s.Status.PrivateHistory
	h.ExecutorTerminal = &v1alpha1.PrivateExecutorTerminalStatus{Version: 1, PodUID: h.LastExecutor.PodUID, Phase: string(corev1.PodFailed),
		Reason: "DeadlineExceeded", PodResourceVersion: "41", NodeName: f.node.Name, NodeUID: string(f.node.UID),
		LeaseRenewedAt: recordTime(f.now.Add(-time.Second)), ObservedAt: recordTime(f.now),
		Containers:      []v1alpha1.PrivateExecutorContainerTermination{{Name: ContainerName, ExitCode: 143, Reason: "Error", FinishedAt: recordTime(f.now.Add(-2 * time.Second))}},
		ClaudeConfigDir: ".claude", CodexHome: ".codex"}
}

// endedExecutor gives f's executor Pod the status of one that ended: Failed
// by its deadline, its one container terminated.
func endedExecutor(f terminalFixture) {
	start := f.pod.Status.StartTime
	f.pod.Status = corev1.PodStatus{Phase: corev1.PodFailed, Reason: "DeadlineExceeded", StartTime: start, ContainerStatuses: []corev1.ContainerStatus{{Name: ContainerName,
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 143, Reason: "Error", FinishedAt: metav1.NewTime(f.now.Add(-2 * time.Second))}}}}}
}

// interactiveSession is a remote session with its HOME, which retention never
// marks (ADR-008): it has no private history, so the legacy rescue rules
// alone decide its pod and volume.
func interactiveSession() (*v1alpha1.AgentSession, *corev1.PersistentVolumeClaim) {
	s := taskSession()
	s.Generation = 1
	s.Spec.Mode, s.Spec.Prompt = v1alpha1.ModeRemote, ""
	s.Finalizers = []string{Finalizer}
	home := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: HomeClaimName(s.Name), Namespace: s.Namespace, UID: "home-1",
		OwnerReferences: []metav1.OwnerReference{ownerRef(s)}, Finalizers: []string{Finalizer}}}
	return s, home
}

// steadyCase is a Session in a state the operator keeps reconciling.
type steadyCase struct {
	name string
	// build returns the Session, the other objects the API server holds and
	// how the operator is configured.
	build func() (*v1alpha1.AgentSession, []client.Object, func(*Reconciler))
	// settle is how many reconciles may still write: the first records what
	// the fixture lacks, and a stored mark or a reclaim ends its reconcile
	// before the status write.
	settle int
	// removal, history and ready are the reasons of RemovalBlocked,
	// PrivateHistoryRetained and PodReady once the Session is steady; ""
	// means the condition is absent.
	removal, history, ready string
	// check asserts what else proves the steady state is the one named.
	check func(t *testing.T, s *v1alpha1.AgentSession)
}

// TestReconcileOfAnUnchangedSessionWritesNoStatus is haynes-ops#4663.
// Protected private Sessions (254 of them on 2026-11-04) that wanted their pod
// gone, with no pod and no removal verdict of their own in a reconcile, had
// RemovalBlocked removed and set again on every reconcile. The condition got
// a new lastTransitionTime each time, so every reconcile wrote the status, and
// each write's watch event queued the next reconcile. What made the loop
// sustain itself is not identified. The leading inference is that once a
// reconcile took more than a second to come round, each write changed the
// stored object, whose times are stored to the second, so no write was a
// no-op. On 2026-11-04 the operator sent about 155 status PUTs a second for
// 13 hours, and that storm ended when etcd hit its quota; earlier storms
// ended when the operator restarted. Of the 254 Sessions, 232 showed
// TaskComputeReclaimed and 22 RetentionPending.
//
// Each case starts from a RemovalBlocked condition that has been True for an
// hour, lets the reconcile record what the fixture lacks, then requires that
// two more reconciles, a second apart, write nothing at all: the whole status,
// condition order included, and the resourceVersion stay as they were.
func TestReconcileOfAnUnchangedSessionWritesNoStatus(t *testing.T) {
	transitioned := metav1.NewTime(time.Now().Add(-time.Hour).Truncate(time.Second))
	for _, tc := range []steadyCase{
		// The storm's sessions: suspended this week, the archive not due, so
		// the reconcile sets no removal verdict and private history alone
		// sets RemovalBlocked.
		{name: "suspended inside the archive window", settle: 1, removal: "RetentionPending", history: "RetentionPending", ready: "RetentionPending",
			build: func() (*v1alpha1.AgentSession, []client.Object, func(*Reconciler)) {
				s, home := privateHistoryFixture()
				s.Spec.Lifecycle = &v1alpha1.Lifecycle{ArchiveAfter: &metav1.Duration{Duration: 168 * time.Hour}}
				s.Status.SuspendedAt = &metav1.Time{Time: time.Now().Add(-24 * time.Hour).Truncate(time.Second)}
				return s, []client.Object{home}, nil
			}},
		// The archive is due: archive() sets a removal verdict as well.
		{name: "suspended with the archive due", settle: 1, removal: "RetentionPending", history: "RetentionPending", ready: "RetentionPending",
			build: func() (*v1alpha1.AgentSession, []client.Object, func(*Reconciler)) {
				s, home := privateHistoryFixture()
				s.Spec.Lifecycle = &v1alpha1.Lifecycle{ArchiveAfter: &metav1.Duration{Duration: 168 * time.Hour}}
				return s, []client.Object{home}, nil
			}},
		{name: "reaped", settle: 1, removal: "RetentionPending", history: "RetentionPending", ready: "RetentionPending",
			build: func() (*v1alpha1.AgentSession, []client.Object, func(*Reconciler)) {
				s, home := privateHistoryFixture()
				at := metav1.NewTime(time.Now().Add(-time.Minute).Truncate(time.Second))
				s.DeletionTimestamp = &at
				return s, []client.Object{home}, nil
			}},
		// The 22 RetentionPending Sessions, as the operator was configured:
		// the terminal task reclaim refuses one whose executor was never
		// attempted, and says so in a condition of its own.
		{name: "retention pending with the production flags", settle: 1, removal: "RetentionPending", history: "RetentionPending", ready: "RetentionPending",
			build: func() (*v1alpha1.AgentSession, []client.Object, func(*Reconciler)) {
				s, home := privateHistoryFixture()
				suspendedThisWeek(s)
				return s, []client.Object{home, templatesConfigMap()}, productionRetention
			},
			check: func(t *testing.T, s *v1alpha1.AgentSession) {
				if c := meta.FindStatusCondition(s.Status.Conditions, ConditionTerminalTaskReclaim); c == nil || c.Reason != "ProofUnknown" {
					t.Fatalf("the reclaim did not judge the Session: %+v", c)
				}
			}},
		// The 232 TaskComputeReclaimed Sessions: a terminal task whose ended
		// executor the reclaim removed. The terminal record and the task's
		// terminal observation are stored, the Pod is gone, and the Session
		// is suspended inside its archive window.
		{name: "task compute reclaimed", settle: 1, removal: "TaskComputeReclaimed", history: "TaskComputeReclaimed", ready: "TaskPodReclaimed",
			build: func() (*v1alpha1.AgentSession, []client.Object, func(*Reconciler)) {
				f := terminalTaskFixture(true)
				recordTerminal(f)
				suspendedThisWeek(f.s)
				return f.s, []client.Object{f.home, f.node, f.lease, templatesConfigMap()}, productionRetention
			},
			check: func(t *testing.T, s *v1alpha1.AgentSession) {
				if c := meta.FindStatusCondition(s.Status.Conditions, ConditionTerminalTaskReclaim); c == nil || c.Reason != "Reclaimed" {
					t.Fatalf("the reclaim is not settled: %+v", c)
				}
			}},
		// The same state reached by the operator itself: it records the
		// ended executor, the reclaim deletes its Pod, and the Session's
		// archive timer starts.
		{name: "task compute reclaimed by the reconcile", settle: 3, removal: "TaskComputeReclaimed", history: "TaskComputeReclaimed", ready: "TaskPodReclaimed",
			build: func() (*v1alpha1.AgentSession, []client.Object, func(*Reconciler)) {
				f := terminalTaskFixture(true)
				endedExecutor(f)
				return f.s, []client.Object{f.home, f.pod, f.node, f.lease, templatesConfigMap()}, func(r *Reconciler) {
					productionRetention(r)
					// The fixture's clock, moving: the node lease stays fresh.
					start := time.Now()
					r.Now = func() time.Time { return f.now.Add(time.Since(start)) }
				}
			},
			check: func(t *testing.T, s *v1alpha1.AgentSession) {
				if !executorTerminalRecorded(s.Status.PrivateHistory) || s.Status.PrivateHistory.TaskTerminal == nil || s.Status.SuspendedAt == nil {
					t.Fatalf("the reconcile did not record the ended executor or start the archive timer: %+v", s.Status.PrivateHistory)
				}
				if c := meta.FindStatusCondition(s.Status.Conditions, ConditionTerminalTaskReclaim); c == nil || c.Reason != "Reclaimed" {
					t.Fatalf("the reclaim did not remove the executor: %+v", c)
				}
			}},
		// The recorded executor is gone with no terminal record: absence is
		// Unknown. The reconcile stores the missed marker once (DESIGN-025
		// 3), the next writes the status, and from then on nothing changes.
		{name: "preservation unknown", settle: 2, removal: "PreservationUnknown", history: "PreservationUnknown", ready: "PreservationUnknown",
			build: func() (*v1alpha1.AgentSession, []client.Object, func(*Reconciler)) {
				f := newTerminalFixture()
				suspendedThisWeek(f.s)
				return f.s, []client.Object{f.home, f.node, f.lease, templatesConfigMap()}, productionRetention
			},
			check: func(t *testing.T, s *v1alpha1.AgentSession) {
				h := s.Status.PrivateHistory
				if h.ExecutorMissed == nil || h.ExecutorTerminal != nil {
					t.Fatalf("the missed marker is not stored: %+v", h)
				}
				if c := meta.FindStatusCondition(s.Status.Conditions, ConditionPrivateHistory); c == nil || c.Status != metav1.ConditionUnknown {
					t.Fatalf("an absent executor is not Unknown: %+v", c)
				}
			}},
		// The HOME expiry deleted the HOME after archiving its history
		// (DESIGN-025 6.5) while the archive flag was on; the Session is not
		// reaped, so its Expired record stays. The flags are today's: the
		// archive is off, and its off path leaves a record with no hold pod.
		{name: "home expired", settle: 1, removal: "HomeExpired", history: "HomeExpired", ready: "Archived",
			build: func() (*v1alpha1.AgentSession, []client.Object, func(*Reconciler)) {
				f := terminalTaskFixture(true)
				recordTerminal(f)
				h := f.s.Status.PrivateHistory
				at := metav1.NewTime(time.Now().Add(-time.Hour).Truncate(time.Second))
				h.Expiry = &v1alpha1.PrivateHomeExpiryStatus{Version: 1, ExecutorUID: h.LastExecutor.PodUID, HomeUID: h.HomeUID, GraceStart: at, DueAt: at,
					Phase: v1alpha1.HomeExpiryExpired, RescueStamp: "20261104-1200", DeletingAt: &at, HomeDeletedAt: &at,
					Archive: &v1alpha1.PrivateHomeArchiveStatus{Entry: "v1/_none/" + string(f.s.UID) + "/0123456789abcdef", ContentSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", ArchivedAt: at}}
				f.s.Status.SuspendedAt = &metav1.Time{Time: time.Now().Add(-40 * 24 * time.Hour).Truncate(time.Second)}
				f.s.Status.ArchivedAt = &at
				return f.s, []client.Object{f.node, f.lease, templatesConfigMap()}, productionRetention
			},
			check: func(t *testing.T, s *v1alpha1.AgentSession) {
				if c := meta.FindStatusCondition(s.Status.Conditions, ConditionHomeExpiry); c == nil || c.Reason != v1alpha1.HomeExpiryExpired {
					t.Fatalf("the HOME expiry is not shown Expired: %+v", c)
				}
			}},
		// The legacy rescue path (D-55): an interactive Session, never
		// marked, whose rescue in its hold pod failed a minute ago. The guard
		// keeps the hold pod until the retry, and says so.
		{name: "legacy hold pod after a failed rescue", settle: 1, removal: "SuspendNeedsRescue", ready: "HoldPod",
			build: func() (*v1alpha1.AgentSession, []client.Object, func(*Reconciler)) {
				s, home := interactiveSession()
				s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
				hold := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: s.Name, Namespace: s.Namespace, UID: "hold-1", OwnerReferences: []metav1.OwnerReference{ownerRef(s)},
					Labels: map[string]string{v1alpha1.LabelHold: "true"}},
					Spec: corev1.PodSpec{NodeName: "worker-a", Containers: []corev1.Container{{Name: ContainerName}},
						Volumes: []corev1.Volume{{Name: "home", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: home.Name}}}}},
					Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: ContainerName,
						State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}}}
				at := metav1.NewTime(time.Now().Add(-time.Minute).Truncate(time.Second))
				s.Status.Rescue = &v1alpha1.RescueStatus{Result: v1alpha1.RescueFailed, Message: "the bundle did not verify", Stamp: "20261105-1200",
					At: &at, PodUID: string(hold.UID), Generation: s.Generation}
				return s, []client.Object{home, hold, templatesConfigMap()}, productionRetention
			},
			check: func(t *testing.T, s *v1alpha1.AgentSession) {
				if c := meta.FindStatusCondition(s.Status.Conditions, ConditionRescueFailed); c == nil || c.Status != metav1.ConditionTrue {
					t.Fatalf("the failed rescue is not shown: %+v", c)
				}
			}},
		// Resume clears the condition: a protected Session switched back to
		// Running, its recorded executor still running, wants its pod and
		// keeps it, so nothing blocks a removal.
		{name: "protected and resumed", settle: 1, removal: "", history: "HomeProtected", ready: "PodReady",
			build: func() (*v1alpha1.AgentSession, []client.Object, func(*Reconciler)) {
				f := newTerminalFixture()
				f.pod.Status = corev1.PodStatus{Phase: corev1.PodRunning, StartTime: &metav1.Time{Time: f.now.Add(-time.Hour)},
					Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
					ContainerStatuses: []corev1.ContainerStatus{{Name: ContainerName, Ready: true,
						State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(f.now.Add(-time.Hour))}}}}}
				return f.s, []client.Object{f.home, f.pod, f.node, f.lease, templatesConfigMap()}, productionRetention
			}},
		// An unprotected suspended Session (no private history, no
		// workspace) with a stale RemovalBlocked and no removal verdict: its
		// verified rescue stands and the archive is not due, so it loses the
		// condition.
		{name: "unprotected and suspended", settle: 1, removal: "", history: "", ready: "Suspended",
			build: func() (*v1alpha1.AgentSession, []client.Object, func(*Reconciler)) {
				s, home := interactiveSession()
				suspendedThisWeek(s)
				at := metav1.NewTime(time.Now().Add(-25 * time.Hour).Truncate(time.Second))
				s.Status.Rescue = &v1alpha1.RescueStatus{Result: v1alpha1.RescueVerified, Message: "verified", Stamp: "20261104-1200",
					At: &at, PodUID: "executor-1", Generation: s.Generation}
				return s, []client.Object{home, templatesConfigMap()}, productionRetention
			},
			check: func(t *testing.T, s *v1alpha1.AgentSession) {
				if s.Status.PrivateHistory != nil {
					t.Fatal("retention marked an interactive Session")
				}
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Each case has its own objects, fake API server and Reconciler.
			t.Parallel()
			s, objects, configure := tc.build()
			// The status the previous operator left: RemovalBlocked has been
			// True for an hour.
			s.Status.Conditions = []metav1.Condition{{Type: ConditionRemovalBlocked, Status: metav1.ConditionTrue, Reason: "RetentionPending",
				Message: "an earlier message", ObservedGeneration: s.Generation, LastTransitionTime: transitioned}}
			base := privateHistoryClient(append([]client.Object{s}, objects...)...)
			statusWrites, writes := 0, 0
			c := interceptor.NewClient(base, interceptor.Funcs{
				Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
					writes++
					return c.Create(ctx, o, opts...)
				},
				Update: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.UpdateOption) error {
					writes++
					return c.Update(ctx, o, opts...)
				},
				Patch: func(ctx context.Context, c client.WithWatch, o client.Object, p client.Patch, opts ...client.PatchOption) error {
					writes++
					return c.Patch(ctx, o, p, opts...)
				},
				Apply: func(ctx context.Context, c client.WithWatch, o runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
					writes++
					return c.Apply(ctx, o, opts...)
				},
				Delete: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.DeleteOption) error {
					writes++
					return c.Delete(ctx, o, opts...)
				},
				DeleteAllOf: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.DeleteAllOfOption) error {
					writes++
					return c.DeleteAllOf(ctx, o, opts...)
				},
				SubResourceCreate: func(ctx context.Context, c client.Client, sub string, o, subObj client.Object, opts ...client.SubResourceCreateOption) error {
					writes++
					return c.SubResource(sub).Create(ctx, o, subObj, opts...)
				},
				SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, o client.Object, opts ...client.SubResourceUpdateOption) error {
					statusWrites++
					return c.SubResource(sub).Update(ctx, o, opts...)
				},
				SubResourcePatch: func(ctx context.Context, c client.Client, sub string, o client.Object, p client.Patch, opts ...client.SubResourcePatchOption) error {
					statusWrites++
					return c.SubResource(sub).Patch(ctx, o, p, opts...)
				},
				SubResourceApply: func(ctx context.Context, c client.Client, sub string, o runtime.ApplyConfiguration, opts ...client.SubResourceApplyOption) error {
					statusWrites++
					return c.SubResource(sub).Apply(ctx, o, opts...)
				},
			})
			r := &Reconciler{Client: c, APIReader: base}
			if configure != nil {
				configure(r)
			}
			reconcile := func() ctrl.Result {
				t.Helper()
				res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(s)})
				if err != nil {
					t.Fatal(err)
				}
				return res
			}
			// The first reconciles may record what the fixture lacks.
			for range tc.settle {
				reconcile()
			}
			first := storedHistory(t, base, s)
			c1 := meta.FindStatusCondition(first.Status.Conditions, ConditionRemovalBlocked)
			switch {
			case tc.removal == "" && c1 != nil:
				t.Fatalf("RemovalBlocked stayed after the reconcile: %+v", c1)
			case tc.removal != "" && (c1 == nil || c1.Status != metav1.ConditionTrue):
				t.Fatalf("RemovalBlocked is not True after the first reconcile: %+v", first.Status.Conditions)
			case tc.removal != "" && c1.Reason != tc.removal:
				t.Fatalf("RemovalBlocked's reason is %s, want %s: %s", c1.Reason, tc.removal, c1.Message)
			case tc.removal != "" && !c1.LastTransitionTime.Equal(&transitioned):
				// Go on: the writes below show what the moved time costs.
				t.Errorf("RemovalBlocked stayed True but its lastTransitionTime moved from %s to %s", transitioned, c1.LastTransitionTime)
			}
			for _, want := range []struct{ condition, reason string }{{ConditionPrivateHistory, tc.history}, {ConditionPodReady, tc.ready}} {
				got := meta.FindStatusCondition(first.Status.Conditions, want.condition)
				switch {
				case want.reason == "" && got != nil:
					t.Fatalf("%s is set: %+v", want.condition, got)
				case want.reason != "" && (got == nil || got.Reason != want.reason):
					t.Fatalf("%s is %+v, want reason %s", want.condition, got, want.reason)
				}
			}
			if tc.check != nil {
				tc.check(t, first)
			}

			// Nothing changed since: no further reconcile writes anything,
			// a second later included, and none asks to be called back
			// within a minute.
			statusWrites, writes = 0, 0
			results := []ctrl.Result{reconcile()}
			time.Sleep(1100 * time.Millisecond)
			results = append(results, reconcile())
			if statusWrites != 0 {
				t.Fatalf("reconciles of an unchanged session wrote its status %d times", statusWrites)
			}
			if writes != 0 {
				t.Fatalf("reconciles of an unchanged session wrote %d objects", writes)
			}
			last := storedHistory(t, base, s)
			if last.ResourceVersion != first.ResourceVersion {
				t.Fatalf("the session changed from resourceVersion %s to %s", first.ResourceVersion, last.ResourceVersion)
			}
			if !apiequality.Semantic.DeepEqual(first.Status, last.Status) {
				t.Fatalf("the status changed:\n was %+v\n now %+v", first.Status, last.Status)
			}
			for _, res := range results {
				if res.RequeueAfter > 0 && res.RequeueAfter < time.Minute {
					t.Fatalf("an unchanged session asks to be reconciled again after %s", res.RequeueAfter)
				}
			}
		})
	}
}

// TestObserveKeepsTransitionTimesOfUnchangedConditions pins the rule observe
// follows whatever path sets a condition: lastTransitionTime moves with the
// status and only with it.
func TestObserveKeepsTransitionTimesOfUnchangedConditions(t *testing.T) {
	old := metav1.NewTime(time.Now().Add(-time.Hour).Truncate(time.Second))
	before := []metav1.Condition{
		{Type: "Same", Status: metav1.ConditionTrue, Reason: "A", LastTransitionTime: old},
		{Type: "Flipped", Status: metav1.ConditionTrue, Reason: "A", LastTransitionTime: old},
	}
	after := []metav1.Condition{
		{Type: "Same", Status: metav1.ConditionTrue, Reason: "B", LastTransitionTime: metav1.Now()},
		{Type: "Flipped", Status: metav1.ConditionFalse, Reason: "A", LastTransitionTime: metav1.Now()},
		{Type: "New", Status: metav1.ConditionTrue, Reason: "A", LastTransitionTime: metav1.Now()},
	}
	keepTransitionTimes(before, after)
	if !after[0].LastTransitionTime.Equal(&old) || after[0].Reason != "B" {
		t.Fatalf("an unchanged status lost its transition time or kept an old reason: %+v", after[0])
	}
	if after[1].LastTransitionTime.Equal(&old) || after[2].LastTransitionTime.Equal(&old) {
		t.Fatal("a changed or new condition kept an old transition time")
	}
}
