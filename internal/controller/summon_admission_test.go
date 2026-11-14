package controller

// The proofs of story 777 (slice a of item 741; DESIGN-064 5.9 and 6.7): a
// summoned session gets no volume and no pod until the operator leader admits
// it by the create ledger, its lane and its pool, and a closed one never gets
// another pod. Each test runs on a fake API server and a fake clock; where
// order matters, a client hook interleaves the writes. No test depends on
// goroutine timing.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/summonledger"
	"github.com/thaynes43/dev-env/internal/summonoutcome"
	"github.com/thaynes43/dev-env/internal/templates"
)

var summonT0 = time.Date(2026, 11, 12, 16, 0, 0, 0, time.UTC)

const (
	callerA         = "alert-ns.alert-responder"
	callerB         = "cigar-ns.cigar-curator"
	summonTestHash  = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	reconcileRounds = 6
)

func callerParent(caller string) string { return strings.Replace(caller, ".", "/", 1) }

func summonPolicyObject(caller string, concurrent int32, escalations *int32) *v1alpha1.CallerPolicy {
	return &v1alpha1.CallerPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: summonPolicyNamespace, Name: caller, UID: types.UID("policy-" + caller)},
		Spec: v1alpha1.CallerPolicySpec{Summon: &v1alpha1.CallerPolicySummon{ServiceAccount: callerParent(caller),
			Limits: v1alpha1.CallerPolicySummonLimits{Concurrent: concurrent, Escalations: escalations, CreatesPerHour: 10}}},
	}
}

// summonedFor is a summoned session of caller in lane, created age before
// summonT0. Remediation and curation are tasks; escalation and upgrade are
// remote, as the CallerPolicy schema requires.
func summonedFor(name, caller string, lane v1alpha1.Lane, age time.Duration) *v1alpha1.AgentSession {
	s := taskSession()
	s.Name, s.UID, s.Generation = name, types.UID("uid-"+name), 1
	s.CreationTimestamp = metav1.NewTime(summonT0.Add(-age))
	s.Spec.Caller, s.Spec.Lane, s.Spec.Parent, s.Spec.Profile = caller, lane, callerParent(caller), "ops"
	sum := &v1alpha1.SessionSummon{Priority: v1alpha1.SummonPriorityUrgent, FallbackModel: "claude-opus-5", OnUnreported: v1alpha1.SummonOnUnreportedFailed}
	switch lane {
	case v1alpha1.LaneRemediation:
		sum = remediationSummon()
	case v1alpha1.LaneCuration:
		sum.Priority, sum.MaxHold = v1alpha1.SummonPriorityBulk, "12h"
	case v1alpha1.LaneUpgrade:
		sum.Priority = v1alpha1.SummonPriorityNormal
		s.Spec.Mode, s.Spec.Prompt, s.Spec.Limits = v1alpha1.ModeRemote, "brief", nil
	case v1alpha1.LaneEscalation:
		s.Spec.Mode, s.Spec.Prompt, s.Spec.Limits = v1alpha1.ModeRemote, "brief", nil
	}
	sum.DispatchID = "d-" + name
	s.Spec.Summon = sum
	return s
}

// summonWorld is one fake API server with the templates, the create ledger
// and the CallerPolicies, and a leader whose clock the test moves.
type summonWorld struct {
	t      *testing.T
	base   client.WithWatch
	r      *Reconciler
	now    time.Time
	ledger map[string]*summonledger.Entry
	// pods and claims are the creates, in order.
	pods, claims []string
}

func newSummonWorld(t *testing.T, objs ...client.Object) *summonWorld {
	t.Helper()
	w := &summonWorld{t: t, now: summonT0, ledger: map[string]*summonledger.Entry{}}
	tmpl := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: templatesKey.Name, Namespace: templatesKey.Namespace}, Data: map[string]string{templates.Key: exampleDoc}}
	ledger := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: summonledger.Namespace, Name: summonledger.Name}}
	w.base = fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&v1alpha1.AgentSession{}, &corev1.Pod{}).
		WithObjects(append([]client.Object{tmpl, ledger}, objs...)...).Build()
	w.r = w.leader(nil)
	return w
}

// leader is a fresh operator leader on the same API server: nothing of the
// last one's memory survives (a leader restart). hooks wrap its writer.
func (w *summonWorld) leader(hooks *interceptor.Funcs) *Reconciler {
	funcs := interceptor.Funcs{}
	if hooks != nil {
		funcs = *hooks
	}
	create := funcs.Create
	funcs.Create = func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
		switch obj := o.(type) {
		case *corev1.Pod:
			w.pods = append(w.pods, obj.Name)
			obj.UID = types.UID(fmt.Sprintf("pod-%s-%d", obj.Name, len(w.pods)))
		case *corev1.PersistentVolumeClaim:
			w.claims = append(w.claims, obj.Name)
			obj.UID = types.UID("pvc-" + obj.Name)
		}
		if create != nil {
			return create(ctx, c, o, opts...)
		}
		return c.Create(ctx, o, opts...)
	}
	return &Reconciler{Client: interceptor.NewClient(w.base, funcs), APIReader: w.base, Templates: templatesKey, SessionNamespace: "dev-agents",
		SummonedLanes: true, PrivateHistoryRetention: true, PrivateTerminalRecord: true, PrivateExecutorStop: true, TerminalTaskPodReclaim: true,
		Now: func() time.Time { return w.now }}
}

func (w *summonWorld) add(objs ...client.Object) {
	w.t.Helper()
	for _, o := range objs {
		if err := w.base.Create(context.Background(), o); err != nil {
			w.t.Fatal(err)
		}
	}
}

// charge binds s's dispatch id to name in its caller's ledger entry.
func (w *summonWorld) charge(s *v1alpha1.AgentSession, name string) {
	w.t.Helper()
	e := w.ledger[s.Spec.Caller]
	if e == nil {
		e = &summonledger.Entry{}
		w.ledger[s.Spec.Caller] = e
	}
	e.Clock = w.now
	if i := e.Index(s.Spec.Summon.DispatchID); i >= 0 {
		e.Charges[i].Name = name
	} else {
		e.Charges = append(e.Charges, summonledger.Charge{ID: s.Spec.Summon.DispatchID, At: w.now, Attempt: w.now, Hash: summonTestHash, Name: name})
	}
	w.writeLedger()
}

func (w *summonWorld) writeLedger() {
	w.t.Helper()
	var cm corev1.ConfigMap
	if err := w.base.Get(context.Background(), types.NamespacedName{Namespace: summonledger.Namespace, Name: summonledger.Name}, &cm); err != nil {
		w.t.Fatal(err)
	}
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	for caller, e := range w.ledger {
		raw, err := summonledger.Encode(*e)
		if err != nil {
			w.t.Fatal(err)
		}
		cm.Data[caller] = raw
	}
	if err := w.base.Update(context.Background(), &cm); err != nil {
		w.t.Fatal(err)
	}
}

// queue adds charged summoned sessions, and lets the reconciler see each
// create, as the operator does at once: it records the task latch the leader
// waits for, and builds nothing.
func (w *summonWorld) queue(ss ...*v1alpha1.AgentSession) {
	w.t.Helper()
	for _, s := range ss {
		w.charge(s, s.Name)
		w.add(s)
		w.reconcile(s.Name)
	}
}

func (w *summonWorld) get(name string) *v1alpha1.AgentSession {
	w.t.Helper()
	var s v1alpha1.AgentSession
	if err := w.base.Get(context.Background(), types.NamespacedName{Namespace: "dev-agents", Name: name}, &s); err != nil {
		w.t.Fatal(err)
	}
	return &s
}

func (w *summonWorld) pass() {
	w.t.Helper()
	if err := w.r.SummonAdmissionPass(context.Background()); err != nil {
		w.t.Fatal(err)
	}
}

func (w *summonWorld) reconcile(name string) {
	w.t.Helper()
	for range reconcileRounds {
		if _, err := w.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "dev-agents", Name: name}}); err != nil {
			w.t.Fatalf("reconcile %s: %v", name, err)
		}
	}
}

func (w *summonWorld) admitted(name string) bool {
	st := w.get(name).Status.Summon
	return st != nil && st.AdmittedAt != nil
}

// hold is the SummonAdmitted reason of a session that is not admitted.
func (w *summonWorld) hold(name string) string {
	s := w.get(name)
	if c := meta.FindStatusCondition(s.Status.Conditions, ConditionSummonAdmitted); c != nil {
		return string(c.Status) + "/" + c.Reason
	}
	return "none"
}

// wantAdmitted checks exactly which of names are admitted.
func (w *summonWorld) wantAdmitted(step string, names map[string]bool) {
	w.t.Helper()
	for name, want := range names {
		if got := w.admitted(name); got != want {
			w.t.Fatalf("%s: %s admitted=%v, want %v (condition %s)", step, name, got, want, w.hold(name))
		}
	}
}

// close writes a terminal outcome through the one terminal write. A report
// releases at once; an operator close of an admitted session does not.
func (w *summonWorld) close(name, source string) {
	w.t.Helper()
	s := w.get(name)
	d := summonoutcome.Decision{State: v1alpha1.OutcomeDone, Note: "done", Source: source, Reason: "done"}
	if source == summonoutcome.Unreported {
		d = summonoutcome.Decision{State: v1alpha1.OutcomeState(s.Spec.Summon.OnUnreported), Note: "unreported: agent-interrupted", Source: source, Reason: summonoutcome.ReasonAgentInterrupted}
	}
	if changed, err := summonoutcome.Write(context.Background(), w.base, s, d, w.now); err != nil || !changed {
		w.t.Fatalf("close %s: changed=%v err=%v", name, changed, err)
	}
}

// admitDirect stores admittedAt as a later slice's admission would, for a
// record this slice cannot admit itself (a remote escalation).
func (w *summonWorld) admitDirect(s *v1alpha1.AgentSession) {
	w.t.Helper()
	at := metav1.NewTime(w.now)
	live := w.get(s.Name)
	live.Status.Summon = &v1alpha1.SummonStatus{AdmittedAt: &at}
	if err := w.base.Status().Update(context.Background(), live); err != nil {
		w.t.Fatal(err)
	}
}

func (w *summonWorld) exists(obj client.Object, name string) bool {
	w.t.Helper()
	err := w.base.Get(context.Background(), types.NamespacedName{Namespace: "dev-agents", Name: name}, obj)
	if err != nil && !apierrors.IsNotFound(err) {
		w.t.Fatal(err)
	}
	return err == nil
}

func (w *summonWorld) wantNoPodOrVolume(name string) {
	w.t.Helper()
	if w.exists(&corev1.Pod{}, name) || w.exists(&corev1.PersistentVolumeClaim{}, HomeClaimName(name)) {
		w.t.Fatalf("%s has a pod or a volume; creates: pods %v claims %v", name, w.pods, w.claims)
	}
	for _, p := range w.pods {
		if p == name {
			w.t.Fatalf("a pod of %s was created", name)
		}
	}
	for _, c := range w.claims {
		if c == HomeClaimName(name) {
			w.t.Fatalf("a volume of %s was created", name)
		}
	}
}

func (w *summonWorld) podCreates(name string) int {
	n := 0
	for _, p := range w.pods {
		if p == name {
			n++
		}
	}
	return n
}

// "A Queued session has no pod and no volume until it is admitted", with
// --enable-summoned-lanes on and off; an ordinary session is built as before.
func TestSummonedSessionGetsNoPodOrVolumeBeforeAdmission(t *testing.T) {
	for _, lanes := range []bool{true, false} {
		t.Run(fmt.Sprintf("lanes=%v", lanes), func(t *testing.T) {
			rem := summonedFor("rem-1", callerA, v1alpha1.LaneRemediation, time.Minute)
			w := newSummonWorld(t, summonPolicyObject(callerA, 2, ptr.To[int32](1)))
			w.queue(rem)
			// A charged session whose pool is full, so a pass that runs holds it.
			busy := summonedFor("rem-busy", callerA, v1alpha1.LaneRemediation, time.Hour)
			w.queue(busy)
			w.admitDirect(busy)
			ordinary := taskSession()
			ordinary.Name, ordinary.UID, ordinary.Generation = "ordinary-1", "uid-ordinary-1", 1
			w.add(ordinary)
			w.r.SummonedLanes = lanes
			w.pass()
			w.reconcile(rem.Name)
			w.wantNoPodOrVolume(rem.Name)
			if w.admitted(rem.Name) {
				t.Fatal("admitted while its lane was busy")
			}
			if lanes && w.hold(rem.Name) != "False/"+SummonReasonLaneBusy {
				t.Fatalf("condition %s", w.hold(rem.Name))
			}
			if !lanes && w.hold(rem.Name) != "none" {
				t.Fatalf("the admission ran with the flag off: %s", w.hold(rem.Name))
			}
			w.reconcile(ordinary.Name)
			var pod corev1.Pod
			if !w.exists(&pod, ordinary.Name) || !w.exists(&corev1.PersistentVolumeClaim{}, HomeClaimName(ordinary.Name)) {
				t.Fatalf("the ordinary session was not built: pods %v claims %v", w.pods, w.claims)
			}
			if envOf(pod.Spec.Containers[0], protocol.StartDeadlineEnv) != nil {
				t.Fatal("an ordinary pod got a start deadline")
			}
			want, err := w.r.sessionPod(w.get(ordinary.Name), mustTemplates(t), w.r.resolveCodexHost(w.get(ordinary.Name), mustTemplates(t)))
			if err != nil {
				t.Fatal(err)
			}
			if !podSpecsMatch(want, &pod) {
				t.Fatal("the ordinary pod differs from the one the builder makes")
			}
		})
	}
}

// podSpecsMatch compares the parts of a pod the builder decides.
func podSpecsMatch(want, got *corev1.Pod) bool {
	if len(want.Spec.Containers) != len(got.Spec.Containers) {
		return false
	}
	for i := range want.Spec.Containers {
		a, b := want.Spec.Containers[i], got.Spec.Containers[i]
		if a.Image != b.Image || len(a.Env) != len(b.Env) {
			return false
		}
		for j := range a.Env {
			if a.Env[j].Name != b.Env[j].Name || a.Env[j].Value != b.Env[j].Value {
				return false
			}
		}
	}
	return true
}

// Admitted: the pod is built, once, with the start deadline; the gate reads
// the session uncached, so a cached copy that still looks open cannot build a
// pod for a session the API server holds closed (a cached absence).
func TestSummonGateReadsTheSessionUncached(t *testing.T) {
	rem := summonedFor("rem-1", callerA, v1alpha1.LaneRemediation, time.Minute)
	w := newSummonWorld(t, summonPolicyObject(callerA, 1, ptr.To[int32](1)))
	w.queue(rem)
	w.pass()
	if !w.admitted(rem.Name) {
		t.Fatalf("not admitted: %s", w.hold(rem.Name))
	}
	stale := w.get(rem.Name)
	// The API server closes it; the cache still serves the open, admitted copy.
	w.close(rem.Name, summonoutcome.Unreported)
	w.r = w.leader(&interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if s, ok := obj.(*v1alpha1.AgentSession); ok && key.Name == rem.Name {
			stale.DeepCopyInto(s)
			return nil
		}
		return c.Get(ctx, key, obj, opts...)
	}})
	w.reconcile(rem.Name)
	w.wantNoPodOrVolume(rem.Name)
}

// The admission itself never reads through the cache: with every cached read
// failing, a pass still admits from the API server.
func TestSummonAdmissionReadsNothingCached(t *testing.T) {
	rem := summonedFor("rem-1", callerA, v1alpha1.LaneRemediation, time.Minute)
	w := newSummonWorld(t, summonPolicyObject(callerA, 1, ptr.To[int32](1)))
	w.queue(rem)
	w.r = w.leader(&interceptor.Funcs{
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return errors.New("cached read")
		},
		List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
			return errors.New("cached list")
		},
	})
	w.pass()
	if !w.admitted(rem.Name) {
		t.Fatalf("not admitted: %s", w.hold(rem.Name))
	}
}

// Admitted, the session is built once, with its start deadline.
func TestSummonAdmittedPodHasTheStartDeadline(t *testing.T) {
	rem := summonedFor("rem-1", callerA, v1alpha1.LaneRemediation, time.Minute)
	w := newSummonWorld(t, summonPolicyObject(callerA, 1, ptr.To[int32](1)))
	w.queue(rem)
	w.now = summonT0.Add(30 * time.Second)
	w.pass()
	w.reconcile(rem.Name)
	var pod corev1.Pod
	if !w.exists(&pod, rem.Name) || w.podCreates(rem.Name) != 1 {
		t.Fatalf("pods %v", w.pods)
	}
	env := envOf(pod.Spec.Containers[0], protocol.StartDeadlineEnv)
	if env == nil || env.Value != "2026-11-12T16:15:30Z" {
		t.Fatalf("start deadline %+v, want admittedAt plus 15 minutes", env)
	}
	if !podRetainsPrivateHistory(&pod) {
		t.Fatal("the admitted task's pod does not carry the agentd task latch")
	}
}

// The start deadline is on an admitted summoned pod and on no other pod.
func TestSummonStartDeadlineOnlyOnAnAdmittedSummonedPod(t *testing.T) {
	tm := mustTemplates(t)
	at := metav1.NewTime(time.Date(2026, 11, 12, 16, 0, 7, 0, time.UTC))
	ordinary := taskSession()
	queued := summonedFor("rem-q", callerA, v1alpha1.LaneRemediation, 0)
	admitted := summonedFor("rem-a", callerA, v1alpha1.LaneRemediation, 0)
	admitted.Status.Summon = &v1alpha1.SummonStatus{AdmittedAt: &at}
	ordinaryWithStatus := taskSession()
	ordinaryWithStatus.Status.Summon = &v1alpha1.SummonStatus{AdmittedAt: &at}
	for _, tc := range []struct {
		name string
		s    *v1alpha1.AgentSession
		hold bool
		want string
	}{
		{"ordinary", ordinary, false, ""},
		{"ordinary with a stray record", ordinaryWithStatus, false, ""},
		{"summoned, queued", queued, false, ""},
		{"summoned, admitted", admitted, false, "2026-11-12T16:15:07Z"},
		{"summoned, admitted, hold pod", admitted, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var pod *corev1.Pod
			var err error
			if tc.hold {
				pod, err = buildHoldPod(tc.s, tm)
			} else {
				pod, err = buildManagedPod(tc.s, tm, "", false, false, nil, false, (&Reconciler{}).resolveCodexHost(tc.s, tm))
			}
			if err != nil {
				t.Fatal(err)
			}
			got := ""
			if env := envOf(pod.Spec.Containers[0], protocol.StartDeadlineEnv); env != nil {
				got = env.Value
			}
			if got != tc.want {
				t.Fatalf("start deadline %q, want %q", got, tc.want)
			}
		})
	}
}

// F5 (DESIGN-064 6.7) with caps of one and two: the oldest Queued session is
// admitted, the next waits; when the first is released the oldest eligible
// one is admitted and the next stays Queued; a leader restart between the
// two recomputes from the records. Three Queued sessions, two lanes, one pool.
func TestSummonAdmissionOldestFirstWithCapsOfOneAndTwo(t *testing.T) {
	for _, tc := range []struct {
		cap int32
		// admitted after the first pass, and after r1 is released.
		first, second map[string]bool
		firstHold     map[string]string
	}{
		{1,
			map[string]bool{"r1": true, "c1": false, "r2": false},
			map[string]bool{"r1": true, "c1": true, "r2": false},
			map[string]string{"c1": SummonReasonPoolBusy, "r2": SummonReasonLaneBusy}},
		{2,
			map[string]bool{"r1": true, "c1": true, "r2": false},
			map[string]bool{"r1": true, "c1": true, "r2": true},
			map[string]string{"r2": SummonReasonLaneBusy}},
	} {
		t.Run(fmt.Sprintf("concurrent=%d", tc.cap), func(t *testing.T) {
			w := newSummonWorld(t, summonPolicyObject(callerA, tc.cap, ptr.To[int32](1)))
			w.queue(
				summonedFor("r2", callerA, v1alpha1.LaneRemediation, time.Minute),
				summonedFor("c1", callerA, v1alpha1.LaneCuration, 2*time.Minute),
				summonedFor("r1", callerA, v1alpha1.LaneRemediation, 3*time.Minute),
			)
			w.pass()
			w.wantAdmitted("first pass", tc.first)
			for name, reason := range tc.firstHold {
				if got := w.hold(name); got != "False/"+reason {
					t.Fatalf("%s held %s, want %s", name, got, reason)
				}
			}
			// The same pass again changes nothing: no double count.
			w.pass()
			w.wantAdmitted("second pass", tc.first)
			// r1 reports done: released at once.
			w.close("r1", summonoutcome.Report)
			if c := w.get("r1").Status.Summon.Closed; c == nil || !c.Released {
				t.Fatal("a report did not release at once")
			}
			// A leader restart between the release and the next pass.
			w.r = w.leader(nil)
			w.now = w.now.Add(time.Minute)
			w.pass()
			w.wantAdmitted("after the release", tc.second)
			if tc.cap == 1 && w.hold("r2") != "False/"+SummonReasonPoolBusy {
				t.Fatalf("r2 held %s, want PoolBusy: c1 is older and took the slot", w.hold("r2"))
			}
		})
	}
}

// A second create in one single-flight lane waits until the first is
// released, across callers; an unreleased close keeps its lane and its slot.
func TestSummonSingleFlightLaneAcrossCallers(t *testing.T) {
	w := newSummonWorld(t, summonPolicyObject(callerA, 2, ptr.To[int32](1)), summonPolicyObject(callerB, 2, ptr.To[int32](1)))
	a := summonedFor("rem-a", callerA, v1alpha1.LaneRemediation, 2*time.Minute)
	b := summonedFor("rem-b", callerB, v1alpha1.LaneRemediation, time.Minute)
	w.queue(a, b)
	w.pass()
	w.wantAdmitted("first", map[string]bool{"rem-a": true, "rem-b": false})
	if w.hold("rem-b") != "False/"+SummonReasonLaneBusy {
		t.Fatalf("rem-b held %s", w.hold("rem-b"))
	}
	w.reconcile(a.Name)
	if !w.exists(&corev1.Pod{}, a.Name) {
		t.Fatal("rem-a was admitted and not built")
	}
	// An operator close while the pod runs and the agent has not started:
	// not released, so the lane stays held.
	w.close(a.Name, summonoutcome.Unreported)
	w.reconcile(a.Name)
	if c := w.get(a.Name).Status.Summon.Closed; c.Released {
		t.Fatal("released while its pod still runs and no agent started")
	}
	w.pass()
	w.wantAdmitted("closed, unreleased", map[string]bool{"rem-b": false})
	// The pod goes: released, and the next pass admits rem-b.
	var pod corev1.Pod
	if !w.exists(&pod, a.Name) {
		t.Fatal("pod gone early")
	}
	if err := w.base.Delete(context.Background(), &pod); err != nil {
		t.Fatal(err)
	}
	w.reconcile(a.Name)
	if !w.get(a.Name).Status.Summon.Closed.Released {
		t.Fatal("not released once the recorded pod was gone")
	}
	w.pass()
	w.wantAdmitted("released", map[string]bool{"rem-b": true})
}

// Two escalations run at once, apart from any remediation, and two open
// escalations never hold a remediation (C2). Escalations are remote; slice d
// (OP780) built the remote latch, so no RemoteLatchMissing hold remains and
// both are admitted in the first pass.
func TestSummonEscalationsRunAtOnceAndNeverHoldARemediation(t *testing.T) {
	w := newSummonWorld(t, summonPolicyObject(callerA, 1, ptr.To[int32](2)))
	e1 := summonedFor("esc-1", callerA, v1alpha1.LaneEscalation, 3*time.Minute)
	e2 := summonedFor("esc-2", callerA, v1alpha1.LaneEscalation, 2*time.Minute)
	rem := summonedFor("rem-1", callerA, v1alpha1.LaneRemediation, time.Minute)
	w.queue(e1, e2, rem)
	w.pass()
	w.wantAdmitted("remote latch built", map[string]bool{"esc-1": true, "esc-2": true, "rem-1": true})
	// A third escalation waits for the escalations pool, and a second
	// remediation waits for its lane, not for the escalations.
	e3 := summonedFor("esc-3", callerA, v1alpha1.LaneEscalation, 0)
	w.queue(e3)
	w.pass()
	if w.admitted("esc-3") || w.hold("esc-3") != "False/"+SummonReasonPoolBusy {
		t.Fatalf("esc-3 admitted=%v held %s", w.admitted("esc-3"), w.hold("esc-3"))
	}
	w.close("rem-1", summonoutcome.Report)
	rem2 := summonedFor("rem-2", callerA, v1alpha1.LaneRemediation, 0)
	w.queue(rem2)
	w.pass()
	w.wantAdmitted("two open escalations, a free remediation lane", map[string]bool{"rem-2": true, "esc-3": false})
}

// Two open escalations admitted by a later slice never hold a remediation in
// this one: the pools are apart.
func TestSummonOpenEscalationsNeverHoldARemediation(t *testing.T) {
	w := newSummonWorld(t, summonPolicyObject(callerA, 1, ptr.To[int32](2)))
	e1 := summonedFor("esc-1", callerA, v1alpha1.LaneEscalation, time.Hour)
	e2 := summonedFor("esc-2", callerA, v1alpha1.LaneEscalation, time.Hour)
	w.queue(e1, e2)
	w.admitDirect(e1)
	w.admitDirect(e2)
	rem := summonedFor("rem-1", callerA, v1alpha1.LaneRemediation, time.Minute)
	w.queue(rem)
	w.pass()
	w.wantAdmitted("", map[string]bool{"rem-1": true})
}

// Item 741 comment 7228, the ledger's two known windows, as their schedules
// leave the store (the API replays them in
// internal/apiserver/summon_ledger_admission_test.go). F1: the session of X
// was persisted after X was pruned, so the ledger holds no X. F2: two
// sessions carry X, and the ledger binds X to N-2 only. The uncharged and the
// surplus session get no pod and no volume and are closed by onUnreported,
// reason not-charged, released at once; the bound one runs.
func TestSummonLedgerWindowsCloseTheUnboundSession(t *testing.T) {
	t.Run("F1 uncharged", func(t *testing.T) {
		w := newSummonWorld(t, summonPolicyObject(callerA, 4, ptr.To[int32](1)))
		x := summonedFor("rem-responder-1", callerA, v1alpha1.LaneRemediation, 2*time.Minute)
		y := summonedFor("rem-responder-2", callerA, v1alpha1.LaneRemediation, time.Minute)
		w.add(x)
		w.reconcile(x.Name)
		w.queue(y)
		w.pass()
		w.wantNotCharged(x.Name)
		w.wantAdmitted("F1", map[string]bool{y.Name: true})
		w.reconcile(x.Name)
		w.reconcile(y.Name)
		w.wantNoPodOrVolume(x.Name)
		if !w.exists(&corev1.Pod{}, y.Name) {
			t.Fatal("the charged session did not run")
		}
	})
	t.Run("F2 surplus", func(t *testing.T) {
		w := newSummonWorld(t, summonPolicyObject(callerA, 4, ptr.To[int32](1)))
		n := summonedFor("rem-responder-1", callerA, v1alpha1.LaneRemediation, time.Minute)
		n2 := summonedFor("rem-responder-1-2", callerA, v1alpha1.LaneRemediation, 2*time.Minute)
		n2.Spec.Summon.DispatchID = n.Spec.Summon.DispatchID
		w.charge(n2, n2.Name)
		w.add(n, n2)
		w.reconcile(n.Name)
		w.reconcile(n2.Name)
		w.pass()
		w.wantNotCharged(n.Name)
		w.wantAdmitted("F2", map[string]bool{n2.Name: true})
		w.reconcile(n.Name)
		w.reconcile(n2.Name)
		w.wantNoPodOrVolume(n.Name)
		if !w.exists(&corev1.Pod{}, n2.Name) {
			t.Fatal("the bound session did not run")
		}
		// Whichever is older: the surplus is never admitted.
		w.pass()
		if w.admitted(n.Name) {
			t.Fatal("the surplus session was admitted later")
		}
	})
	t.Run("ledger unreadable holds, closes nothing", func(t *testing.T) {
		w := newSummonWorld(t, summonPolicyObject(callerA, 4, ptr.To[int32](1)))
		x := summonedFor("rem-responder-1", callerA, v1alpha1.LaneRemediation, time.Minute)
		w.add(x)
		var cm corev1.ConfigMap
		if err := w.base.Get(context.Background(), types.NamespacedName{Namespace: summonledger.Namespace, Name: summonledger.Name}, &cm); err != nil {
			t.Fatal(err)
		}
		cm.Data = map[string]string{callerA: `{"schema":"x"}`}
		if err := w.base.Update(context.Background(), &cm); err != nil {
			t.Fatal(err)
		}
		w.pass()
		if s := w.get(x.Name); s.Status.Outcome != nil || w.admitted(x.Name) {
			t.Fatalf("an unreadable entry admitted or closed: %+v", s.Status)
		}
	})
}

func (w *summonWorld) wantNotCharged(name string) {
	w.t.Helper()
	s := w.get(name)
	if s.Status.Outcome == nil || s.Status.Outcome.State != v1alpha1.OutcomeState(s.Spec.Summon.OnUnreported) {
		w.t.Fatalf("%s outcome %+v, want its onUnreported %s", name, s.Status.Outcome, s.Spec.Summon.OnUnreported)
	}
	c := s.Status.Summon.Closed
	if c == nil || c.Source != summonoutcome.Unreported || c.Reason != summonoutcome.ReasonNotCharged || !c.Released || s.Status.Summon.AdmittedAt != nil {
		w.t.Fatalf("%s closed %+v admitted %v", name, c, s.Status.Summon.AdmittedAt)
	}
	if s.Spec.Lane == v1alpha1.LaneRemediation && (s.Status.Summon.Escalation == nil || s.Status.Summon.Escalation.State != "Pending") {
		w.t.Fatalf("%s: a remediation closed by onUnreported keeps its pending filing: %+v", name, s.Status.Summon.Escalation)
	}
	if w.hold(name) != "False/"+SummonReasonNotCharged {
		w.t.Fatalf("%s condition %s", name, w.hold(name))
	}
}

// The leader writes nothing to a summoned task before the reconciler has
// recorded its latch, not even a hold: a condition written first would make
// the latch record an Unknown lifecycle, which never starts.
func TestSummonLeaderWaitsForTheTaskLatchRecord(t *testing.T) {
	w := newSummonWorld(t) // no policy: it would be held PolicyGone
	rem := summonedFor("rem-1", callerA, v1alpha1.LaneRemediation, time.Minute)
	w.charge(rem, rem.Name)
	w.add(rem)
	w.pass()
	if s := w.get(rem.Name); len(s.Status.Conditions) != 0 || s.Status.Summon != nil {
		t.Fatalf("the leader wrote before the latch record: %+v", s.Status)
	}
	w.reconcile(rem.Name)
	if h := w.get(rem.Name).Status.PrivateHistory; h == nil || h.Admission != sharedNeverStarted {
		t.Fatalf("latch %+v", h)
	}
	w.pass()
	if w.hold(rem.Name) != "False/"+SummonReasonPolicyGone {
		t.Fatalf("held %s", w.hold(rem.Name))
	}
}

// A policy that is gone, or no longer names the session's account, holds.
func TestSummonPolicyGoneHolds(t *testing.T) {
	for _, tc := range []string{"missing", "other account"} {
		t.Run(tc, func(t *testing.T) {
			var objs []client.Object
			if tc == "other account" {
				p := summonPolicyObject(callerA, 4, ptr.To[int32](1))
				p.Spec.Summon.ServiceAccount = "alert-ns/someone-else"
				objs = append(objs, p)
			}
			w := newSummonWorld(t, objs...)
			rem := summonedFor("rem-1", callerA, v1alpha1.LaneRemediation, time.Minute)
			w.queue(rem)
			w.pass()
			if w.admitted(rem.Name) || w.hold(rem.Name) != "False/"+SummonReasonPolicyGone {
				t.Fatalf("admitted=%v held %s", w.admitted(rem.Name), w.hold(rem.Name))
			}
		})
	}
}

// A summoned task is admitted, and built, only while both task latch flags
// are on.
func TestSummonTaskNeedsBothLatchFlags(t *testing.T) {
	for _, off := range []string{"retention", "reclaim"} {
		t.Run(off, func(t *testing.T) {
			w := newSummonWorld(t, summonPolicyObject(callerA, 4, ptr.To[int32](1)))
			rem := summonedFor("rem-1", callerA, v1alpha1.LaneRemediation, time.Minute)
			w.queue(rem)
			if off == "retention" {
				w.r.PrivateHistoryRetention = false
			} else {
				w.r.TerminalTaskPodReclaim = false
			}
			w.pass()
			if w.admitted(rem.Name) || w.hold(rem.Name) != "False/"+SummonReasonTaskLatchMissing {
				t.Fatalf("admitted=%v held %s", w.admitted(rem.Name), w.hold(rem.Name))
			}
			// Admitted by an earlier operator that had both flags: still no pod.
			w.admitDirect(rem)
			w.reconcile(rem.Name)
			w.wantNoPodOrVolume(rem.Name)
		})
	}
}

// After a terminal outcome no pod is ever built again: a fake-client pod loss after
// admission gives no second pod (the task latch), a close on
// agent-interrupted, an operator restart and a recreated pod. Each step
// checks one pod create, ever. Daemon restart is separately proved by
// internal/agentd's TestTerminalPrivateTaskNeverAutomaticallyResumes.
func TestSummonNoSecondPodAfterLossOrClose(t *testing.T) {
	rem := summonedFor("rem-1", callerA, v1alpha1.LaneRemediation, time.Minute)
	w := newSummonWorld(t, summonPolicyObject(callerA, 4, ptr.To[int32](1)))
	w.queue(rem)
	w.pass()
	w.reconcile(rem.Name)
	var pod corev1.Pod
	if !w.exists(&pod, rem.Name) || w.podCreates(rem.Name) != 1 {
		t.Fatalf("pods %v", w.pods)
	}
	// This controller assertion proves the pod carries the retained-task
	// latch; it does not itself restart agentd or count provider launches.
	if !podRetainsPrivateHistory(&pod) {
		t.Fatal("the pod lacks PrivateHistoryRetained")
	}
	// A real pod loss after admission, the outcome still open.
	if err := w.base.Delete(context.Background(), &pod); err != nil {
		t.Fatal(err)
	}
	w.reconcile(rem.Name)
	if w.podCreates(rem.Name) != 1 {
		t.Fatalf("a lost pod was replaced: %v", w.pods)
	}
	// The close on agent-interrupted, then an operator restart.
	w.close(rem.Name, summonoutcome.Unreported)
	w.r = w.leader(nil)
	w.reconcile(rem.Name)
	w.pass()
	if w.podCreates(rem.Name) != 1 || w.exists(&corev1.Pod{}, rem.Name) {
		t.Fatalf("a closed session got another pod: %v", w.pods)
	}
	// The pool and lane counted it once: released, since its recorded pod is
	// gone, and a later remediation is admitted in its place.
	if !w.get(rem.Name).Status.Summon.Closed.Released {
		t.Fatal("the closed session was not released once its pod was gone")
	}
	next := summonedFor("rem-2", callerA, v1alpha1.LaneRemediation, 0)
	w.queue(next)
	w.pass()
	w.wantAdmitted("next", map[string]bool{"rem-2": true})
}

// A close that lands after the gate's uncached read and before the latch's
// own read (here, while the volume is created): the latch reads the closed
// record and refuses, so no pod is created.
func TestSummonCloseBeforeTheLatchReadCreatesNoPod(t *testing.T) {
	rem := summonedFor("rem-1", callerA, v1alpha1.LaneRemediation, time.Minute)
	w := newSummonWorld(t, summonPolicyObject(callerA, 4, ptr.To[int32](1)))
	w.queue(rem)
	w.pass()
	closed := false
	w.r = w.leader(&interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
		err := c.Create(ctx, o, opts...)
		if _, ok := o.(*corev1.PersistentVolumeClaim); ok && !closed {
			closed = true
			w.close(rem.Name, summonoutcome.Unreported)
		}
		return err
	}})
	w.reconcile(rem.Name)
	if !closed {
		t.Fatal("the schedule did not run: no volume create was seen")
	}
	if w.podCreates(rem.Name) != 0 {
		t.Fatalf("a pod was created for a session closed before its latch read: %v", w.pods)
	}
	if h := w.get(rem.Name).Status.PrivateHistory; h == nil || h.ExecutorAttempted {
		t.Fatalf("the latch recorded an attempt for a closed session: %+v", h)
	}
}

// A close that lands after the latch's read and before its write: the latch
// write is locked on its own read, so it conflicts, and no pod is created.
func TestSummonCloseBetweenGateAndLatchCreatesNoPod(t *testing.T) {
	rem := summonedFor("rem-1", callerA, v1alpha1.LaneRemediation, time.Minute)
	w := newSummonWorld(t, summonPolicyObject(callerA, 4, ptr.To[int32](1)))
	w.queue(rem)
	w.pass()
	closed := false
	w.r = w.leader(&interceptor.Funcs{SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
		if s, ok := obj.(*v1alpha1.AgentSession); ok && !closed && s.Status.PrivateHistory != nil && s.Status.PrivateHistory.ExecutorAttempted {
			closed = true
			w.close(rem.Name, summonoutcome.Unreported)
		}
		return c.SubResource(sub).Update(ctx, obj, opts...)
	}})
	for range reconcileRounds {
		_, _ = w.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(rem)})
	}
	if !closed {
		t.Fatal("the schedule did not run: no latch write was seen")
	}
	if w.podCreates(rem.Name) != 0 {
		t.Fatalf("a pod was created for a session closed before its latch write: %v", w.pods)
	}
	if h := w.get(rem.Name).Status.PrivateHistory; h != nil && h.ExecutorAttempted {
		t.Fatal("the latch was written over the close")
	}
}

// Release (DESIGN-064 5.9): at once for a report; for an operator close, when
// there was no attempt, when the latch's recorded pod is gone by an uncached
// read, or when a stored task result exists on a pod carrying
// PrivateHistoryRetained; otherwise only once the pod is confirmed gone.
func TestSummonReleaseRules(t *testing.T) {
	type state struct {
		admitted, attempted, notCreated, recorded, podPresent, podOther, interrupted, retainedPod, remote, report bool
	}
	for _, tc := range []struct {
		name string
		st   state
		want bool
	}{
		{"report", state{admitted: true, attempted: true, recorded: true, podPresent: true, report: true}, true},
		{"never admitted", state{}, true},
		{"admitted, no attempt", state{admitted: true}, true},
		{"known non-attempt", state{admitted: true, attempted: true, notCreated: true}, true},
		{"recorded pod gone", state{admitted: true, attempted: true, recorded: true}, true},
		{"recorded pod replaced", state{admitted: true, attempted: true, recorded: true, podPresent: true, podOther: true}, true},
		{"interrupted, retained pod", state{admitted: true, attempted: true, recorded: true, podPresent: true, interrupted: true, retainedPod: true}, false},
		{"interrupted, pod not retained", state{admitted: true, attempted: true, recorded: true, podPresent: true, interrupted: true}, false},
		{"agent not started", state{admitted: true, attempted: true, recorded: true, podPresent: true, retainedPod: true}, false},
		{"attempt unknown, pod present", state{admitted: true, attempted: true, podPresent: true}, false},
		{"attempt unknown, pod absent before deadline", state{admitted: true, attempted: true}, false},
		{"remote, pod present", state{admitted: true, remote: true, podPresent: true}, false},
		{"remote, pod gone", state{admitted: true, remote: true}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lane := v1alpha1.LaneRemediation
			if tc.st.remote {
				lane = v1alpha1.LaneEscalation
			}
			s := summonedFor("s-1", callerA, lane, time.Minute)
			at := metav1.NewTime(summonT0)
			s.Status.Summon = &v1alpha1.SummonStatus{}
			if tc.st.admitted {
				s.Status.Summon.AdmittedAt = &at
			}
			source := summonoutcome.Unreported
			if tc.st.report {
				source = summonoutcome.Report
			}
			s.Status.Outcome = &v1alpha1.OutcomeStatus{State: v1alpha1.OutcomeFailed, At: &at}
			s.Status.Summon.Closed = &v1alpha1.SummonClosedStatus{At: at, Source: source, Reason: "agent-interrupted"}
			if !tc.st.remote {
				s.Finalizers = []string{Finalizer}
				s.Status.PrivateHistory = &v1alpha1.PrivateHistoryStatus{Version: 1, SessionUID: string(s.UID), HomeClaim: HomeClaimName(s.Name), BindingGeneration: 1,
					Admission: "Started", Provider: "claude", StopState: "Unknown", ExecutorAttempted: tc.st.attempted, ExecutorNotCreated: tc.st.notCreated}
				if tc.st.attempted {
					s.Status.PrivateHistory.ExecutorIntent = "intent"
				}
				if tc.st.recorded {
					s.Status.PrivateHistory.LastExecutor = &v1alpha1.PrivateExecutorStatus{PodUID: "recorded-pod", PodName: s.Name, Generation: 1}
				}
			}
			if tc.st.interrupted {
				s.Status.Agent = &v1alpha1.AgentStatus{Status: protocol.AgentInterrupted}
			}
			objs := []client.Object{s}
			if tc.st.podPresent {
				uid := types.UID("recorded-pod")
				if tc.st.podOther {
					uid = "another-pod"
				}
				doc := `{"name":"s-1","repo":"haynes-ops","agent":"claude","mode":"task","model":"claude-opus-5-5","prompt":"p"}`
				if tc.st.retainedPod {
					doc = `{"privateHistoryRetained":true,"name":"s-1","repo":"haynes-ops","agent":"claude","mode":"task","model":"claude-opus-5-5","prompt":"p"}`
				}
				objs = append(objs, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: s.Name, Namespace: s.Namespace, UID: uid, OwnerReferences: []metav1.OwnerReference{ownerRef(s)}},
					Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: ContainerName, Env: []corev1.EnvVar{{Name: protocol.SessionEnv, Value: doc}}}}}})
			}
			w := newSummonWorld(t, objs...)
			changed, err := w.r.reconcileSummonRelease(context.Background(), w.get(s.Name))
			if err != nil {
				t.Fatal(err)
			}
			got := w.get(s.Name).Status.Summon.Closed.Released
			if got != tc.want || changed != tc.want {
				t.Fatalf("released=%v changed=%v, want %v", got, changed, tc.want)
			}
		})
	}
}

// The operator's own close of a session it never admitted releases at once,
// in the same write; a report always does.
func TestSummonCloseReleasesAtOnceWithoutAdmission(t *testing.T) {
	rem := summonedFor("rem-1", callerA, v1alpha1.LaneRemediation, time.Minute)
	rem.Status.Agent = &v1alpha1.AgentStatus{Status: protocol.AgentFailed}
	w := newSummonWorld(t, rem)
	if _, err := w.r.reconcileSummonOutcome(context.Background(), w.get(rem.Name)); err != nil {
		t.Fatal(err)
	}
	c := w.get(rem.Name).Status.Summon.Closed
	if c == nil || c.Reason != summonoutcome.ReasonAgentFailed || !c.Released {
		t.Fatalf("closed %+v", c)
	}
}

// The runnable: a pass at start, one per tick and one per nudge; it runs only
// in the leader. Driven by channels, not by time.
func TestSummonAdmissionRunnableTicksAndNudges(t *testing.T) {
	w := newSummonWorld(t, summonPolicyObject(callerA, 1, ptr.To[int32](1)))
	ticks := make(chan time.Time)
	nudge := make(chan struct{}, 1)
	passes := make(chan error)
	a := &summonAdmission{r: w.r, every: time.Minute, nudge: nudge, log: ctrl.Log,
		tick:   func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} },
		passed: func(err error) { passes <- err }}
	if !a.NeedLeaderElection() {
		t.Fatal("the admission must run in the leader only")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Start(ctx) }()
	if err := <-passes; err != nil {
		t.Fatal(err)
	}
	w.queue(summonedFor("rem-1", callerA, v1alpha1.LaneRemediation, 0))
	ticks <- summonT0
	if err := <-passes; err != nil {
		t.Fatal(err)
	}
	w.wantAdmitted("tick", map[string]bool{"rem-1": true})
	w.queue(summonedFor("cur-1", callerA, v1alpha1.LaneCuration, 0))
	w.close("rem-1", summonoutcome.Report)
	w.r.summonNudge = nudge
	w.r.nudgeSummonAdmission()
	if err := <-passes; err != nil {
		t.Fatal(err)
	}
	w.wantAdmitted("nudge", map[string]bool{"cur-1": true})
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// A nudge never blocks the reconciler: one pending nudge is enough.
func TestSummonNudgeNeverBlocks(t *testing.T) {
	r := &Reconciler{}
	r.nudgeSummonAdmission() // no admission running: nothing to do
	r.summonNudge = make(chan struct{}, 1)
	r.nudgeSummonAdmission()
	r.nudgeSummonAdmission()
	if len(r.summonNudge) != 1 {
		t.Fatalf("%d pending nudges", len(r.summonNudge))
	}
}

// The reconciler records a summoned task's latch as a fresh, startable
// lifecycle although the outcome step has already written its Pending
// outcome; any other outcome stays history (Unknown).
func TestSummonedTaskLatchRecordsNeverStarted(t *testing.T) {
	for _, tc := range []struct {
		name   string
		state  v1alpha1.OutcomeState
		caller bool
		want   string
	}{
		{"summoned, pending", v1alpha1.OutcomePending, true, sharedNeverStarted},
		{"summoned, running", v1alpha1.OutcomeRunning, true, "Unknown"},
		{"ordinary, pending", v1alpha1.OutcomePending, false, "Unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := summonedFor("rem-1", callerA, v1alpha1.LaneRemediation, 0)
			if !tc.caller {
				s = taskSession()
				s.Generation = 1
			}
			at := metav1.NewTime(summonT0)
			s.Status.Outcome = &v1alpha1.OutcomeStatus{State: tc.state, At: &at}
			w := newSummonWorld(t, s)
			if err := w.r.initializePrivateHistory(context.Background(), w.get(s.Name)); err != nil {
				t.Fatal(err)
			}
			if h := w.get(s.Name).Status.PrivateHistory; h == nil || h.Admission != tc.want {
				t.Fatalf("admission %+v, want %s", h, tc.want)
			}
		})
	}
}
