package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/templates"
)

// fakeRestarter is prepare-restart for tests. envtest has no kubelet to exec into.
type fakeRestarter struct {
	mu    sync.Mutex
	fn    func(*corev1.Pod) (protocol.RestartReport, error)
	calls map[string]int
}

func (f *fakeRestarter) PrepareRestart(_ context.Context, pod *corev1.Pod) (protocol.RestartReport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[pod.Name]++
	if f.fn == nil {
		return protocol.RestartReport{}, errors.New("no prepare-restart answer")
	}
	return f.fn(pod)
}

func (f *fakeRestarter) callCount(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[name]
}

// leaveBusy puts the agent back to busy when the test ends, so a later
// template bump in this package does not drain a session this test left idle.
func leaveBusy(t *testing.T, name string) {
	t.Helper()
	t.Cleanup(func() {
		setAgent(t, name, protocol.AgentBusy, time.Now())
	})
}

func currentTemplates(t *testing.T) *templates.Templates {
	t.Helper()
	tmpl, err := templates.Parse(map[string]string{templates.Key: exampleDoc})
	if err != nil {
		t.Fatal(err)
	}
	return tmpl
}

func bumpTemplates(t *testing.T) {
	t.Helper()
	const from = "0000000000000000000000000000000000000000000000000000000000000001"
	const to = "0000000000000000000000000000000000000000000000000000000000000002"
	next := strings.Replace(exampleDoc, from, to, 1)
	if next == exampleDoc {
		t.Fatal("the example image digest was not in the templates")
	}
	setTemplates(t, next)
}

func readyDrainPod(name, revision string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: sessionNS, UID: "pod-1",
			Labels: map[string]string{v1alpha1.LabelRevision: revision},
		},
		Spec: corev1.PodSpec{NodeName: "talosw02", Containers: []corev1.Container{{Name: ContainerName}}},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
		},
	}
}

func drainFixture(name string) *v1alpha1.AgentSession {
	return &v1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sessionNS, Generation: 3},
		Spec:       v1alpha1.AgentSessionSpec{OperatingMode: v1alpha1.OperatingModeRunning, Repo: "haynes-ops"},
		Status:     v1alpha1.AgentSessionStatus{Agent: &v1alpha1.AgentStatus{Status: protocol.AgentIdle}},
	}
}

func homeFor(s *v1alpha1.AgentSession) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{
		Name: HomeClaimName(s.Name), Namespace: s.Namespace, UID: "vol-1",
		Finalizers: []string{Finalizer},
	}}
}

// A current revision is not outdated, so a Running pod is not marked Draining
// and deletePod refuses it.
func TestDrainLeavesACurrentRunningPod(t *testing.T) {
	tmpl := currentTemplates(t)
	s := drainFixture("current")
	pod := readyDrainPod(s.Name, tmpl.Revision())
	if drainEligible(s, pod, tmpl.Revision()) {
		t.Fatal("a current revision was eligible to drain")
	}
	phase, _ := podPhase(s, observation{pod: owned[*corev1.Pod]{obj: pod}, templates: tmpl})
	if phase != v1alpha1.PhaseRunning {
		t.Fatalf("phase %s, want Running", phase)
	}
	s.Status.Phase = v1alpha1.PhaseRunning
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build()
	var stored corev1.Pod
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(pod), &stored); err != nil {
		t.Fatal(err)
	}
	if err := deletePod(context.Background(), c, s, &stored); err == nil {
		t.Fatal("deleted a current Running pod")
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(pod), &stored); err != nil {
		t.Fatalf("the pod is gone: %v", err)
	}

	rt := &fakeRestarter{fn: func(*corev1.Pod) (protocol.RestartReport, error) {
		return protocol.RestartReport{}, errors.New("should not run")
	}}
	startOperator(t, func(r *Reconciler) { r.Restarter = rt })
	live := newSession(t, nil)
	leaveBusy(t, live.Name)
	markRunning(t, waitPod(t, live.Name), "talosw02")
	waitStatus(t, live.Name, "Running", phaseIs(v1alpha1.PhaseRunning))
	before := waitPod(t, live.Name)
	setAgent(t, live.Name, protocol.AgentIdle, time.Now())
	time.Sleep(2 * time.Second)
	got := session(t, live.Name)
	if got.Status.Phase != v1alpha1.PhaseRunning {
		t.Fatalf("phase %s, want Running", got.Status.Phase)
	}
	after := waitPod(t, live.Name)
	if after.UID != before.UID || !after.DeletionTimestamp.IsZero() {
		t.Fatalf("the current pod was deleted: uid %s to %s, deleted %v", before.UID, after.UID, !after.DeletionTimestamp.IsZero())
	}
	if rt.callCount(live.Name) != 0 {
		t.Fatalf("prepare-restart calls %d, want 0", rt.callCount(live.Name))
	}
}

// A busy outdated pod stays Running and is not deleted.
func TestDrainLeavesABusyOutdatedPod(t *testing.T) {
	tmpl := currentTemplates(t)
	s := drainFixture("busy")
	s.Status.Agent.Status = protocol.AgentBusy
	pod := readyDrainPod(s.Name, "old-revision")
	if drainEligible(s, pod, tmpl.Revision()) {
		t.Fatal("a busy session was eligible to drain")
	}
	phase, _ := podPhase(s, observation{pod: owned[*corev1.Pod]{obj: pod}, templates: tmpl})
	if phase != v1alpha1.PhaseRunning {
		t.Fatalf("phase %s, want Running", phase)
	}
	s.Status.Phase = phase
	if err := podRemovalAllowed(s, pod); err == nil {
		t.Fatal("podRemovalAllowed deleted a busy Running pod")
	}

	rt := &fakeRestarter{fn: func(*corev1.Pod) (protocol.RestartReport, error) {
		return protocol.RestartReport{}, errors.New("should not run")
	}}
	startOperator(t, func(r *Reconciler) { r.Restarter = rt })
	live := newSession(t, nil)
	markRunning(t, waitPod(t, live.Name), "talosw02")
	waitStatus(t, live.Name, "Running", phaseIs(v1alpha1.PhaseRunning))
	before := waitPod(t, live.Name)
	bumpTemplates(t)
	setAgent(t, live.Name, protocol.AgentBusy, time.Now().Add(-2*time.Hour))
	waitStatus(t, live.Name, "busy and outdated stays Running", func(st *v1alpha1.AgentSessionStatus) error {
		c := condition(st, ConditionOutdated)
		if c == nil || c.Status != metav1.ConditionTrue {
			return fmt.Errorf("Outdated %+v", c)
		}
		if st.Phase != v1alpha1.PhaseRunning {
			return fmt.Errorf("phase %s", st.Phase)
		}
		return nil
	})
	time.Sleep(2 * time.Second)
	got := session(t, live.Name)
	if got.Status.Phase != v1alpha1.PhaseRunning {
		t.Fatalf("phase %s, want Running", got.Status.Phase)
	}
	after := waitPod(t, live.Name)
	if after.UID != before.UID || !after.DeletionTimestamp.IsZero() {
		t.Fatalf("the busy pod was deleted")
	}
	if rt.callCount(live.Name) != 0 {
		t.Fatalf("prepare-restart calls %d, want 0", rt.callCount(live.Name))
	}
}

// A summoned session is never drained, even when it is idle and outdated.
func TestDrainLeavesASummonedSession(t *testing.T) {
	tmpl := currentTemplates(t)
	s := drainFixture("summoned")
	s.Spec.Caller, s.Spec.Lane = "alert-responder", v1alpha1.LaneRemediation
	pod := readyDrainPod(s.Name, "old-revision")
	if drainEligible(s, pod, tmpl.Revision()) {
		t.Fatal("a summoned session was eligible to drain")
	}
	s.Status.Phase = v1alpha1.PhaseDraining
	s.Status.Restart = &v1alpha1.RestartStatus{ConversationID: "conv-1", PodUID: string(pod.UID), Generation: s.Generation}
	if err := podRemovalAllowed(s, pod); !errors.Is(err, errSummonedNotDrained) {
		t.Fatalf("podRemovalAllowed = %v, want the summoned refusal", err)
	}
	phase, _ := podPhase(s, observation{pod: owned[*corev1.Pod]{obj: pod}, templates: tmpl})
	if phase == v1alpha1.PhaseDraining {
		t.Fatal("a summoned session was marked Draining")
	}

	rt := &fakeRestarter{fn: func(*corev1.Pod) (protocol.RestartReport, error) {
		return protocol.RestartReport{}, errors.New("should not run")
	}}
	startOperator(t, func(r *Reconciler) { r.Restarter = rt })
	live := newSession(t, func(s *v1alpha1.AgentSession) {
		s.Spec.Caller, s.Spec.Lane, s.Spec.Profile = "alert-responder", v1alpha1.LaneRemediation, "ops"
	})
	leaveBusy(t, live.Name)
	markRunning(t, waitPod(t, live.Name), "talosw02")
	before := waitPod(t, live.Name)
	bumpTemplates(t)
	setAgent(t, live.Name, protocol.AgentIdle, time.Now())
	waitStatus(t, live.Name, "summoned outdated stays Running", func(st *v1alpha1.AgentSessionStatus) error {
		c := condition(st, ConditionOutdated)
		if c == nil || c.Status != metav1.ConditionTrue {
			return fmt.Errorf("Outdated %+v", c)
		}
		if st.Phase != v1alpha1.PhaseRunning {
			return fmt.Errorf("phase %s", st.Phase)
		}
		return nil
	})
	time.Sleep(2 * time.Second)
	if session(t, live.Name).Status.Phase != v1alpha1.PhaseRunning {
		t.Fatalf("phase %s", session(t, live.Name).Status.Phase)
	}
	after := waitPod(t, live.Name)
	if after.UID != before.UID || !after.DeletionTimestamp.IsZero() {
		t.Fatal("the summoned pod was deleted")
	}
	if rt.callCount(live.Name) != 0 {
		t.Fatalf("prepare-restart calls %d, want 0", rt.callCount(live.Name))
	}
}

// Draining without a prepare-restart conversation is not deleted.
func TestDrainKeepsAPodWithoutPrepareRestartProof(t *testing.T) {
	pod := readyDrainPod("s", "old-revision")
	s := drainFixture("s")
	s.Status.Phase = v1alpha1.PhaseDraining
	if err := podRemovalAllowed(s, pod); !errors.Is(err, errDrainNeedsRestart) {
		t.Fatalf("missing proof: %v", err)
	}
	s.Status.Restart = &v1alpha1.RestartStatus{ConversationID: "conv-1", PodUID: "other-pod", Generation: s.Generation}
	if err := podRemovalAllowed(s, pod); !errors.Is(err, errDrainNeedsRestart) {
		t.Fatalf("proof for another pod: %v", err)
	}
	s.Status.Restart.PodUID = string(pod.UID)
	s.Status.Restart.Generation = s.Generation - 1
	if err := podRemovalAllowed(s, pod); !errors.Is(err, errDrainNeedsRestart) {
		t.Fatalf("proof from an older generation: %v", err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod, homeFor(s)).Build()
	var stored corev1.Pod
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(pod), &stored); err != nil {
		t.Fatal(err)
	}
	if err := drainPod(context.Background(), c, s, &stored); !errors.Is(err, errDrainNeedsRestart) {
		t.Fatalf("drainPod without proof: %v", err)
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(pod), &stored); err != nil {
		t.Fatalf("the pod was deleted without proof: %v", err)
	}

	rt := &fakeRestarter{fn: func(pod *corev1.Pod) (protocol.RestartReport, error) {
		return protocol.RestartReport{Session: pod.Name}, nil
	}}
	startOperator(t, func(r *Reconciler) { r.Restarter = rt })
	live := newSession(t, func(s *v1alpha1.AgentSession) {
		s.Spec.Lifecycle = &v1alpha1.Lifecycle{IdleSuspendAfter: &metav1.Duration{Duration: 72 * time.Hour}}
	})
	leaveBusy(t, live.Name)
	markRunning(t, waitPod(t, live.Name), "talosw02")
	before := waitPod(t, live.Name)
	bumpTemplates(t)
	setAgent(t, live.Name, protocol.AgentIdle, time.Now())
	waitStatus(t, live.Name, "drain waits for a conversation", func(st *v1alpha1.AgentSessionStatus) error {
		if st.Phase != v1alpha1.PhaseDraining {
			return fmt.Errorf("phase %s", st.Phase)
		}
		cnd := condition(st, ConditionRemovalBlocked)
		if cnd == nil || cnd.Reason != "DrainNeedsRestart" {
			return fmt.Errorf("RemovalBlocked %+v", cnd)
		}
		return nil
	})
	time.Sleep(time.Second)
	after := waitPod(t, live.Name)
	if after.UID != before.UID || !after.DeletionTimestamp.IsZero() {
		t.Fatal("deleted a Draining pod that had no conversation identity")
	}
	if rt.callCount(live.Name) == 0 {
		t.Fatal("prepare-restart was not called")
	}
	claim := waitClaim(t, live.Name)
	if !claim.DeletionTimestamp.IsZero() || !containsFinalizer(claim.Finalizers, Finalizer) {
		t.Fatalf("the volume moved: deleted %v, finalizers %v", !claim.DeletionTimestamp.IsZero(), claim.Finalizers)
	}
}

// An idle outdated session with prepare-restart proof may lose its pod. The
// volume finalizer stays, and ensure starts the replacement on the current
// revision and the same claim.
func TestIdleOutdatedSessionDrainsOntoTheSameVolume(t *testing.T) {
	tmpl := currentTemplates(t)
	s := drainFixture("roll")
	pod := readyDrainPod(s.Name, "old-revision")
	if !drainEligible(s, pod, tmpl.Revision()) {
		t.Fatal("an idle outdated session was not eligible")
	}
	phase, _ := podPhase(s, observation{pod: owned[*corev1.Pod]{obj: pod}, templates: tmpl})
	if phase != v1alpha1.PhaseDraining {
		t.Fatalf("phase %s, want Draining", phase)
	}
	s.Status.Phase = v1alpha1.PhaseDraining
	s.Status.Restart = &v1alpha1.RestartStatus{ConversationID: "conv-1", PodUID: string(pod.UID), Generation: s.Generation}
	if err := podRemovalAllowed(s, pod); err != nil {
		t.Fatalf("proof was refused: %v", err)
	}
	if err := volumeRemovalAllowed(s, false, true); err == nil {
		t.Fatal("drain allowed the volume to be archived")
	}
	claim := homeFor(s)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod, claim).Build()
	var stored corev1.Pod
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(pod), &stored); err != nil {
		t.Fatal(err)
	}
	if err := drainPod(context.Background(), c, s, &stored); err != nil {
		t.Fatalf("drainPod: %v", err)
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(pod), &corev1.Pod{}); !apierrors.IsNotFound(err) {
		t.Fatalf("pod delete: %v", err)
	}
	var kept corev1.PersistentVolumeClaim
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(claim), &kept); err != nil {
		t.Fatal(err)
	}
	if kept.UID != claim.UID || !kept.DeletionTimestamp.IsZero() || !containsFinalizer(kept.Finalizers, Finalizer) {
		t.Fatalf("volume finalizer did not stay: uid %s, deleted %v, finalizers %v", kept.UID, !kept.DeletionTimestamp.IsZero(), kept.Finalizers)
	}
	// A suspend that arrives while the phase is still Draining still needs its
	// rescue. The drain proof does not skip it.
	suspended := s.DeepCopy()
	suspended.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
	if err := podRemovalAllowed(suspended, pod); !errors.Is(err, errNeedsRescue) {
		t.Fatalf("drain proof skipped rescue on suspend: %v", err)
	}

	rt := &fakeRestarter{fn: func(pod *corev1.Pod) (protocol.RestartReport, error) {
		return protocol.RestartReport{Session: pod.Name, ConversationID: "conv-drain", Resumable: true}, nil
	}}
	startOperator(t, func(r *Reconciler) { r.Restarter = rt })
	live := newSession(t, func(s *v1alpha1.AgentSession) {
		s.Spec.Lifecycle = &v1alpha1.Lifecycle{IdleSuspendAfter: &metav1.Duration{Duration: 72 * time.Hour}}
	})
	origClaim := waitClaim(t, live.Name)
	orig := markRunning(t, waitPod(t, live.Name), "talosw02")
	waitStatus(t, live.Name, "Running", phaseIs(v1alpha1.PhaseRunning))
	bumpTemplates(t)
	setAgent(t, live.Name, protocol.AgentIdle, time.Now())
	finishPodDelete(t, orig)
	var rolled *corev1.Pod
	eventually(t, "the replacement pod", func() error {
		p, err := get(t, sessionNS, live.Name, &corev1.Pod{})
		if err != nil {
			return err
		}
		if p.UID == orig.UID {
			return errors.New("the outdated pod is still the only pod")
		}
		if p.Labels[v1alpha1.LabelRevision] == "" || p.Labels[v1alpha1.LabelRevision] == orig.Labels[v1alpha1.LabelRevision] {
			return fmt.Errorf("revision %q, old %q", p.Labels[v1alpha1.LabelRevision], orig.Labels[v1alpha1.LabelRevision])
		}
		rolled = p
		return nil
	})
	keptClaim := waitClaim(t, live.Name)
	if keptClaim.UID != origClaim.UID || !keptClaim.DeletionTimestamp.IsZero() || !containsFinalizer(keptClaim.Finalizers, Finalizer) {
		t.Fatalf("volume changed: uid %s to %s, deleted %v, finalizers %v", origClaim.UID, keptClaim.UID, !keptClaim.DeletionTimestamp.IsZero(), keptClaim.Finalizers)
	}
	got := session(t, live.Name)
	if got.Spec.OperatingMode != v1alpha1.OperatingModeRunning {
		t.Fatalf("operatingMode %s", got.Spec.OperatingMode)
	}
	if got.Status.Restart == nil || got.Status.Restart.ConversationID != "conv-drain" || got.Status.Restart.PodUID != string(orig.UID) {
		t.Fatalf("restart proof %+v", got.Status.Restart)
	}
	if rolled.Spec.Volumes == nil {
		t.Fatal("replacement pod has no volumes")
	}
}

// Private-history sessions and canary originals are not drained, and a drain
// proof does not release their pods or volumes.
func TestDrainLeavesPrivateHistoryAndCanarySessions(t *testing.T) {
	tmpl := currentTemplates(t)
	cases := []struct {
		name string
		edit func(*v1alpha1.AgentSession)
	}{
		{"private history", func(s *v1alpha1.AgentSession) {
			s.Status.PrivateHistory = &v1alpha1.PrivateHistoryStatus{Version: 1, StopState: "Unknown"}
		}},
		{"canary", func(s *v1alpha1.AgentSession) {
			s.Spec.ProjectCanary = &v1alpha1.ProjectCanaryBinding{Version: 1, ID: "canary-a"}
		}},
		{"pilot", func(s *v1alpha1.AgentSession) {
			s.Spec.PrivatePilot = &v1alpha1.PrivatePilotBinding{}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := drainFixture(c.name)
			c.edit(s)
			pod := readyDrainPod(s.Name, "old-revision")
			if drainEligible(s, pod, tmpl.Revision()) {
				t.Fatal("protected session was eligible to drain")
			}
			phase, _ := podPhase(s, observation{pod: owned[*corev1.Pod]{obj: pod}, templates: tmpl})
			if phase == v1alpha1.PhaseDraining {
				t.Fatal("protected session was marked Draining")
			}
			s.Status.Phase = v1alpha1.PhaseDraining
			s.Status.Restart = &v1alpha1.RestartStatus{ConversationID: "conv-1", PodUID: string(pod.UID), Generation: s.Generation}
			claim := homeFor(s)
			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod, claim).Build()
			var stored corev1.Pod
			if err := cl.Get(context.Background(), client.ObjectKeyFromObject(pod), &stored); err != nil {
				t.Fatal(err)
			}
			if err := drainPod(context.Background(), cl, s, &stored); !errors.Is(err, errPrivateHistoryRetained) {
				t.Fatalf("drainPod = %v, want private retention", err)
			}
			if err := cl.Get(context.Background(), client.ObjectKeyFromObject(pod), &stored); err != nil {
				t.Fatalf("the pod was deleted: %v", err)
			}
			var kept corev1.PersistentVolumeClaim
			if err := cl.Get(context.Background(), client.ObjectKeyFromObject(claim), &kept); err != nil {
				t.Fatal(err)
			}
			if !kept.DeletionTimestamp.IsZero() || !containsFinalizer(kept.Finalizers, Finalizer) {
				t.Fatalf("volume finalizer moved: deleted %v, finalizers %v", !kept.DeletionTimestamp.IsZero(), kept.Finalizers)
			}
			if err := volumeRemovalAllowed(s, false, true); !errors.Is(err, errPrivateHistoryRetained) {
				t.Fatalf("volumeRemovalAllowed = %v", err)
			}
		})
	}
}

// Idle for drain is the agent's not-working state with no attached client.
// Waiting counts. Busy, a client, and a current revision do not.
func TestDrainIdleMoment(t *testing.T) {
	tmpl := currentTemplates(t)
	pod := readyDrainPod("s", "old-revision")
	base := drainFixture("s")
	for _, c := range []struct {
		name string
		edit func(*v1alpha1.AgentSession)
		want bool
	}{
		{"idle", func(s *v1alpha1.AgentSession) { s.Status.Agent.Status = protocol.AgentIdle }, true},
		{"waiting", func(s *v1alpha1.AgentSession) { s.Status.Agent.Status = protocol.AgentWaiting }, true},
		{"busy", func(s *v1alpha1.AgentSession) { s.Status.Agent.Status = protocol.AgentBusy }, false},
		{"attached", func(s *v1alpha1.AgentSession) { s.Status.Agent.Attached = 1 }, false},
		{"unreported", func(s *v1alpha1.AgentSession) { s.Status.Agent = nil }, false},
		{"exited", func(s *v1alpha1.AgentSession) { s.Status.Agent.Status = protocol.AgentExited }, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := base.DeepCopy()
			c.edit(s)
			if got := drainEligible(s, pod, tmpl.Revision()); got != c.want {
				t.Fatalf("eligible %v, want %v", got, c.want)
			}
			phase, _ := podPhase(s, observation{pod: owned[*corev1.Pod]{obj: pod}, templates: tmpl})
			wantPhase := v1alpha1.PhaseRunning
			if c.want {
				wantPhase = v1alpha1.PhaseDraining
			}
			if phase != wantPhase {
				t.Fatalf("phase %s, want %s", phase, wantPhase)
			}
		})
	}
}

func TestParseRestartOutput(t *testing.T) {
	ok := &cappedBuffer{max: 1024}
	if _, err := ok.Write([]byte(`{"session":"s","conversationId":"conv-1","resumable":true}`)); err != nil {
		t.Fatal(err)
	}
	rep, err := parseRestartOutput(ok, "", nil)
	if err != nil || !rep.Resumable || rep.ConversationID != "conv-1" {
		t.Fatalf("report %+v, %v", rep, err)
	}
	bad := &cappedBuffer{max: 1024}
	_, _ = bad.Write([]byte(`not json`))
	if _, err := parseRestartOutput(bad, "boom", errors.New("exit 1")); err == nil {
		t.Fatal("an exec failure was accepted")
	}
}

func containsFinalizer(in []string, want string) bool {
	for _, f := range in {
		if f == want {
			return true
		}
	}
	return false
}
