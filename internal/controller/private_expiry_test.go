package controller

// DESIGN-025 unit H5: the HOME expiry up to Archived. These tests drive
// Reconcile against the fake client with a fake clock, the real agentd export
// and archive behind an in-process exec, and a fake rescuer. The envtest test
// (private_expiry_envtest_test.go) drives the same flow on a real API server.

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/shelf"
	"github.com/thaynes43/dev-env/internal/templates"
)

type expiryFixture struct {
	terminalFixture
	observed metav1.Time
	clock    *time.Time
	x        *agentdExec
	rescuer  *expiryRescuer
	counters *HomeExpiryCounters
	c        client.WithWatch
	// readErr, when set, fails the reads it matches (T10); statusErr the
	// status writes.
	readErr   func(key client.ObjectKey, obj runtime.Object) error
	statusErr func(obj client.Object) error
}

// newExpiryFixture is a reaped private Session whose recorded executor ended
// (DeadlineExceeded) and has its confirmed terminal record, with a bound HOME,
// a shelf pod, the templates, and a HOME of history for the export. edit runs
// before the objects are stored.
func newExpiryFixture(t *testing.T, edit func(*terminalFixture)) (f *expiryFixture) {
	t.Helper()
	tf := newTerminalFixture()
	s := tf.s
	// Kubernetes UIDs, as the archive's manifest checks.
	s.UID = "4f1c2d3e-0000-4000-8000-000000000001"
	tf.home.UID, tf.pod.UID = "4f1c2d3e-0000-4000-8000-000000000002", "4f1c2d3e-0000-4000-8000-000000000003"
	tf.home.OwnerReferences, tf.pod.OwnerReferences = []metav1.OwnerReference{ownerRef(s)}, []metav1.OwnerReference{ownerRef(s)}
	tf.pod.Annotations[privateHistoryHomeAnnotation] = string(tf.home.UID)
	s.Status.PrivateHistory.SessionUID, s.Status.PrivateHistory.HomeUID = string(s.UID), string(tf.home.UID)
	s.Status.PrivateHistory.LastExecutor.PodUID = string(tf.pod.UID)
	s.Finalizers = []string{Finalizer}
	reaped := metav1.NewTime(tf.now)
	s.DeletionTimestamp = &reaped
	s.Status.Rescue, s.Status.SuspendedAt = nil, nil
	h := s.Status.PrivateHistory
	observed := recordTime(tf.now.Add(-time.Minute))
	h.ExecutorTerminal = &v1alpha1.PrivateExecutorTerminalStatus{Version: 1, PodUID: string(tf.pod.UID), Phase: "Failed", Reason: "DeadlineExceeded",
		PodResourceVersion: "42", NodeName: "worker-a", NodeUID: "node-1", LeaseRenewedAt: observed, ObservedAt: observed,
		Containers:      []v1alpha1.PrivateExecutorContainerTermination{{Name: ContainerName, ExitCode: 143, Reason: "Error", FinishedAt: observed}},
		ClaudeConfigDir: ".claude", CodexHome: ".codex"}
	tf.home.Annotations[privateHistoryAdmissionAnnotation] = "3"
	if edit != nil {
		edit(&tf)
	}
	clock := tf.now
	f = &expiryFixture{terminalFixture: tf, observed: observed, clock: &clock, x: newAgentdExec(t, s.Name), rescuer: &expiryRescuer{}, counters: NewHomeExpiryCounters()}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: templatesKey.Name, Namespace: templatesKey.Namespace}, Data: map[string]string{templates.Key: exampleDoc}}
	objects := []client.Object{tf.s, tf.home, tf.node, tf.lease, cm, shelfPodObject(s.Namespace)}
	if tf.pod != nil {
		objects = append(objects, tf.pod)
	}
	base := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&v1alpha1.AgentSession{}, &corev1.Pod{}, &corev1.Node{}).
		WithObjects(objects...).WithInterceptorFuncs(interceptor.Funcs{SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
		if f.statusErr != nil {
			if err := f.statusErr(obj); err != nil {
				return err
			}
		}
		return c.SubResource(sub).Update(ctx, obj, opts...)
	}}).Build()
	f.c = interceptor.NewClient(base, interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			// The API server gives every object a UID; the fake client does not.
			if obj.GetUID() == "" {
				obj.SetUID(types.UID(uuid.NewUUID()))
			}
			return c.Create(ctx, obj, opts...)
		},
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if f.readErr != nil {
				if err := f.readErr(key, obj); err != nil {
					return err
				}
			}
			return c.Get(ctx, key, obj, opts...)
		},
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if f.readErr != nil {
				if err := f.readErr(client.ObjectKey{}, list); err != nil {
					return err
				}
			}
			return c.List(ctx, list, opts...)
		},
	})
	return f
}

func (f *expiryFixture) reconciler() *Reconciler {
	return &Reconciler{Client: f.c, APIReader: f.c, Templates: templatesKey, PrivateTerminalRecord: true, HomeHistoryArchive: true,
		HistoryRelay:       &ExecHistoryRelay{Exec: f.x, Shelf: &shelf.Shelf{Reader: f.c, Namespace: f.s.Namespace}, Slots: shelf.NewExecSlots(shelf.DefaultExecSlots)},
		HomeExpiryCounters: f.counters, Rescuer: f.rescuer, Now: func() time.Time { return *f.clock }}
}

func (f *expiryFixture) at(t time.Time) { *f.clock = t }

// reconcile runs times reconciles of a fresh operator, as a restart would.
func (f *expiryFixture) reconcile(t *testing.T, times int) {
	t.Helper()
	r := f.reconciler()
	for range times {
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(f.s)}); err != nil {
			t.Fatal(err)
		}
	}
}

func (f *expiryFixture) session(t *testing.T) *v1alpha1.AgentSession {
	t.Helper()
	var s v1alpha1.AgentSession
	if err := f.c.Get(context.Background(), client.ObjectKeyFromObject(f.s), &s); err != nil {
		t.Fatal(err)
	}
	return &s
}

func (f *expiryFixture) expiry(t *testing.T) *v1alpha1.PrivateHomeExpiryStatus {
	t.Helper()
	return f.session(t).Status.PrivateHistory.Expiry
}

// pod is the Pod of the Session's name, or nil.
func (f *expiryFixture) named(t *testing.T) *corev1.Pod {
	t.Helper()
	var p corev1.Pod
	if err := f.c.Get(context.Background(), client.ObjectKeyFromObject(f.s), &p); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		t.Fatal(err)
	}
	return &p
}

// runHold plays the scheduler and the kubelet for the hold pod.
func (f *expiryFixture) runHold(t *testing.T) *corev1.Pod {
	t.Helper()
	p := f.named(t)
	if p == nil || !isHoldPod(p) {
		t.Fatalf("no hold pod to run: %+v", p)
	}
	p.Status.Phase = corev1.PodRunning
	if err := f.c.Status().Update(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	return p
}

// claimIntact fails unless the HOME claim is the bound one, with the
// operator's finalizer and no deletion.
func (f *expiryFixture) claimIntact(t *testing.T) {
	t.Helper()
	var c corev1.PersistentVolumeClaim
	if err := f.c.Get(context.Background(), client.ObjectKeyFromObject(f.home), &c); err != nil {
		t.Fatalf("the HOME claim is gone: %v", err)
	}
	if c.UID != f.home.UID || !c.DeletionTimestamp.IsZero() || !controllerutil.ContainsFinalizer(&c, Finalizer) {
		t.Fatalf("the HOME claim changed: %+v", c.ObjectMeta)
	}
}

func counterValue(c prometheus.Collector) float64 {
	ch := make(chan prometheus.Metric, 1)
	c.Collect(ch)
	var m dto.Metric
	if err := (<-ch).Write(&m); err != nil {
		panic(err)
	}
	return m.GetCounter().GetValue()
}

func refusedCount(c *HomeExpiryCounters, reason string) float64 {
	return terminalCount(c.Refused, reason)
}

// T1 up to Archived, and T5's case once the Pod is gone: nothing before the
// grace; then the ended executor goes, a hold pod bound to the HOME UID takes
// its name, the rescue passes in it, the history is exported and archived, the
// entry verifies, and the record is Archived with the entry's digest. The
// hold pod goes, and the HOME stays: H5 deletes no HOME.
func TestHomeExpiryArchivesAfterTheGraceAndKeepsTheHome(t *testing.T) {
	f := newExpiryFixture(t, nil)
	due := f.observed.Add(DefaultHomeExpiryGrace)

	f.reconcile(t, 4)
	e := f.expiry(t)
	if e == nil || e.Phase != v1alpha1.HomeExpiryWaiting || !e.GraceStart.Equal(&f.observed) || !e.DueAt.Time.Equal(due) ||
		e.ExecutorUID != string(f.pod.UID) || e.HomeUID != string(f.home.UID) {
		t.Fatalf("the expiry record is %+v; want Waiting from the terminal record's observedAt", e)
	}
	if c := meta.FindStatusCondition(f.session(t).Status.Conditions, ConditionHomeExpiry); c == nil || c.Reason != v1alpha1.HomeExpiryWaiting {
		t.Fatalf("HomeExpiry condition %+v", c)
	}

	// 30 days minus a minute: nothing moves, and the ended executor stands.
	f.at(due.Add(-time.Minute))
	f.reconcile(t, 4)
	if e := f.expiry(t); e.Phase != v1alpha1.HomeExpiryWaiting {
		t.Fatalf("moved before the grace ended: %s", e.Phase)
	}
	if p := f.named(t); p == nil || p.UID != f.pod.UID {
		t.Fatal("the ended executor went before the grace ended")
	}

	// After the grace: the executor goes, an intent is recorded, the hold
	// pod is made, then adopted.
	f.at(due.Add(time.Minute))
	f.reconcile(t, 6)
	hold := f.named(t)
	e = f.expiry(t)
	if hold == nil || !isHoldPod(hold) || e.Phase != v1alpha1.HomeExpiryRescuing || e.HoldIntent == "" || e.HoldPodUID != string(hold.UID) {
		t.Fatalf("hold pod %v, record %+v; want the adopted hold pod in Rescuing", hold, e)
	}
	if hold.Annotations[privateHistoryHomeAnnotation] != e.HomeUID || hold.Annotations[homeExpiryExecutorAnnotation] != e.ExecutorUID ||
		hold.Annotations[homeExpiryHoldIntentAnnotation] != e.HoldIntent || !privateExecutorUsesHome(hold, f.home.Name) || !controlledBySession(hold, f.s) {
		t.Fatalf("the hold pod is not bound to the HOME and the record: %v", hold.Annotations)
	}
	if len(f.rescuer.ranIn()) != 0 {
		t.Fatal("a rescue ran in a hold pod that has not started")
	}

	f.runHold(t)
	f.reconcile(t, 8)
	got := f.session(t)
	e = got.Status.PrivateHistory.Expiry
	if e.Phase != v1alpha1.HomeExpiryArchived || e.Archive == nil || e.RescueStamp != "20261111-1200" || e.Refusal != "" {
		t.Fatalf("the record is %+v; want Archived", e)
	}
	if r := got.Status.Rescue; r == nil || r.Result != v1alpha1.RescueCleanAndPushed || r.PodUID != string(hold.UID) {
		t.Fatalf("the rescue in the hold pod is not recorded: %+v", r)
	}
	if ran := f.rescuer.ranIn(); len(ran) != 1 || ran[0] != string(hold.UID) {
		t.Fatalf("rescues ran in %v; want once, in the hold pod", ran)
	}
	// The stream and put run at once, piped; the plan comes first and the
	// verify last.
	if calls := f.x.called(); len(calls) != 4 || calls[0] != "plan" || calls[3] != "verify" || !slices.Contains(calls, "stream") || !slices.Contains(calls, "put") {
		t.Fatalf("history execs %v; want the plan, the stream into put, then verify", calls)
	}
	entries := f.x.entries(t)
	if len(entries) != 1 || entries[0] != e.Archive.Entry {
		t.Fatalf("archive entries %v; want the one the record names, %s", entries, e.Archive.Entry)
	}
	ver, err := f.x.archive.Verify(string(f.s.UID), e.Archive.ContentSHA256)
	if err != nil || ver.ContentSHA256 != e.Archive.ContentSHA256 || ver.Files != int(e.Archive.Files) || e.Archive.Files < 5 {
		t.Fatalf("the recorded archive does not verify: %+v, %v (record %+v)", ver, err, e.Archive)
	}
	get, err := f.x.archive.Get(string(f.s.UID), e.Archive.ContentSHA256)
	if err != nil || get.Content.Session.UID != string(f.s.UID) || get.Content.HomeUID != string(f.home.UID) || get.Content.Terminal.PodUID != string(f.pod.UID) ||
		get.Content.Rescue.PodUID != string(hold.UID) || get.Content.Provider != "claude" || get.Content.Project != "" {
		t.Fatalf("content.json does not name the Session, HOME, terminal record and rescue: %+v, %v", get.Content, err)
	}
	excluded := map[string]string{}
	for _, x := range get.Content.Excluded {
		excluded[x.Root+"/"+x.Path] = x.Class
		if x.Class == protocol.HistoryClassCredential && x.Bytes != 0 {
			t.Errorf("a credential's size is listed: %+v", x)
		}
	}
	if excluded["claude/file-history"] != protocol.HistoryClassUserCopy || excluded["codex/auth.json"] != protocol.HistoryClassCredential ||
		excluded["claude/.credentials.json"] != protocol.HistoryClassCredential {
		t.Errorf("the manifest does not list the copies and logins it left out: %v", excluded)
	}
	exported := map[string]bool{}
	for _, file := range get.Content.Files {
		exported[file.Name()] = true
	}
	for _, want := range []string{"agentd/decisions/019a-thread.json", "agentd/managed-output/inv-1/0.txt", "log/" + f.s.Name + ".log",
		"codex/sessions/2026/10/12/rollout-2026-10-12T01-00-00-abc.jsonl", "claude/history.jsonl"} {
		if !exported[want] {
			t.Errorf("%s is not exported: %v", want, exported)
		}
	}
	for _, file := range get.Content.Files {
		if strings.Contains(file.Path, "auth.json") || strings.Contains(file.Path, "credentials") || strings.HasPrefix(file.Path, "repos/") {
			t.Errorf("exported %s", file.Name())
		}
	}
	if p := f.named(t); p != nil {
		t.Fatalf("the hold pod stays after Archived: %s", p.UID)
	}
	f.claimIntact(t)
	if counterValue(f.counters.ArchivedBytes) != float64(e.Archive.Bytes) {
		t.Fatalf("archived bytes counted %v, want %d", counterValue(f.counters.ArchivedBytes), e.Archive.Bytes)
	}

	// At Archived, with no deletion in H5: nothing more happens, ever.
	f.at(due.Add(90 * 24 * time.Hour))
	f.reconcile(t, 4)
	if e2 := f.expiry(t); e2.Phase != v1alpha1.HomeExpiryArchived || len(f.x.called()) != 4 || f.named(t) != nil {
		t.Fatalf("Archived moved on: %+v, execs %v", e2, f.x.called())
	}
	f.claimIntact(t)
}
