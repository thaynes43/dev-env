package controller

// The proofs of story 779 (slice c of item 741; DESIGN-064 6.6 and 6.7): a
// hung, held, lost or stopped summoned session is closed once and frees its
// lane. Each test runs on a fake API server and a fake clock (the world's
// w.now); writes are interleaved by client hooks, never by goroutine timing.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/summonoutcome"
)

const summonNode = "worker-a"

// schedule puts name's pod on a live node with a start time, its agent
// container in the given state (nil: running), as a kubelet would.
func (w *summonWorld) schedule(name string, waiting *corev1.ContainerStateWaiting) {
	w.t.Helper()
	ctx := context.Background()
	if !w.exists(&corev1.Node{}, summonNode) {
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: summonNode, UID: "node-1"}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
		w.add(node, &coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{Name: summonNode, Namespace: "kube-node-lease", OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Node", Name: summonNode, UID: node.UID}}},
			Spec: coordinationv1.LeaseSpec{HolderIdentity: ptr.To(summonNode), RenewTime: &metav1.MicroTime{Time: w.now}}})
	}
	var pod corev1.Pod
	if !w.exists(&pod, name) {
		w.t.Fatalf("%s has no pod to schedule", name)
	}
	pod.Spec.NodeName = summonNode
	if err := w.base.Update(ctx, &pod); err != nil {
		w.t.Fatal(err)
	}
	state := corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(w.now)}}
	phase := corev1.PodRunning
	if waiting != nil {
		state, phase = corev1.ContainerState{Waiting: waiting}, corev1.PodPending
	}
	pod.Status = corev1.PodStatus{Phase: phase, StartTime: &metav1.Time{Time: w.now}}
	for _, c := range pod.Spec.Containers {
		pod.Status.ContainerStatuses = append(pod.Status.ContainerStatuses, corev1.ContainerStatus{Name: c.Name, Image: c.Image, State: state})
	}
	if err := w.base.Status().Update(ctx, &pod); err != nil {
		w.t.Fatal(err)
	}
}

// containersEnd marks every container of name's pod terminated in phase,
// with the node's lease fresh, as a kubelet that ended them would.
func (w *summonWorld) containersEnd(name string, phase corev1.PodPhase) {
	w.t.Helper()
	ctx := context.Background()
	var pod corev1.Pod
	if !w.exists(&pod, name) {
		w.t.Fatalf("%s has no pod", name)
	}
	pod.Status.Phase = phase
	if phase == corev1.PodFailed {
		pod.Status.Reason = "DeadlineExceeded"
	}
	pod.Status.ContainerStatuses = nil
	for _, c := range pod.Spec.Containers {
		pod.Status.ContainerStatuses = append(pod.Status.ContainerStatuses, corev1.ContainerStatus{Name: c.Name, Image: c.Image,
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 143, Reason: "Error", FinishedAt: metav1.NewTime(w.now.Add(-time.Second))}}})
	}
	if err := w.base.Status().Update(ctx, &pod); err != nil {
		w.t.Fatal(err)
	}
	var lease coordinationv1.Lease
	if err := w.base.Get(ctx, types.NamespacedName{Namespace: "kube-node-lease", Name: summonNode}, &lease); err != nil {
		w.t.Fatal(err)
	}
	lease.Spec.RenewTime = &metav1.MicroTime{Time: w.now.Add(-time.Second)}
	if err := w.base.Update(ctx, &lease); err != nil {
		w.t.Fatal(err)
	}
}

// heartbeat stores agentd's report as the API's heartbeat route does.
func (w *summonWorld) heartbeat(name, state string) {
	w.t.Helper()
	s := w.get(name)
	at := metav1.NewTime(w.now)
	s.Status.Agent = &v1alpha1.AgentStatus{Status: state, LastHeartbeat: &at, LastActivity: &at}
	if err := w.base.Status().Update(context.Background(), s); err != nil {
		w.t.Fatal(err)
	}
}

func (w *summonWorld) closed(name string) *v1alpha1.SummonClosedStatus {
	if st := w.get(name).Status.Summon; st != nil {
		return st.Closed
	}
	return nil
}

func (w *summonWorld) wantClosed(name, reason string, state v1alpha1.OutcomeState) {
	w.t.Helper()
	s := w.get(name)
	c := w.closed(name)
	if c == nil || c.Reason != reason || c.Source != summonoutcome.Unreported || s.Status.Outcome == nil || s.Status.Outcome.State != state {
		w.t.Fatalf("%s: closed=%+v outcome=%+v, want %s by %s", name, c, s.Status.Outcome, state, reason)
	}
}

func (w *summonWorld) wantOpen(step, name string) {
	w.t.Helper()
	if c := w.closed(name); c != nil {
		w.t.Fatalf("%s: %s closed early: %+v", step, name, c)
	}
}

func (w *summonWorld) released(name string) bool {
	c := w.closed(name)
	return c != nil && c.Released
}

func (w *summonWorld) grantNames() []string {
	var names []string
	for _, g := range readTestGrants(w.t, w.base) {
		names = append(names, g.Name)
	}
	return names
}

// launchSummoned queues a and b in one single-flight lane, admits a, builds
// its volume and pod, and starts it on a live node with a Busy heartbeat.
func launchSummoned(t *testing.T) (*summonWorld, *v1alpha1.AgentSession, *v1alpha1.AgentSession) {
	t.Helper()
	w, a, b := admissionPair(t, "lane")
	b.Spec.Summon.MaxHold = "24h" // b waits out a's whole run without its own hold bound tripping
	w.queue(a, b)
	w.pass()
	w.wantAdmitted("admit the oldest", map[string]bool{a.Name: true, b.Name: false})
	w.reconcile(a.Name)
	if !w.exists(&corev1.Pod{}, a.Name) || !w.exists(&corev1.PersistentVolumeClaim{}, HomeClaimName(a.Name)) {
		t.Fatal("the admitted session has no pod or volume")
	}
	return w, a, b
}

// Proofs 1 and 7, and story 779 comment 7377: an operator close whose
// release waits for the pod closes once, then the operator suspends the
// session, the private executor stop is set on its pod, and only the pod's
// terminal phase with its terminal record releases the lane. The grant
// leaves the ledger, the next session in the lane is admitted, the volume
// stays, and a second pass or a new leader writes nothing more.
func TestSummonOperatorCloseSuspendsStopsAndFreesTheLane(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reason string
		// close drives the world from a started pod to the close.
		close func(w *summonWorld, a string)
	}{
		{"deadline", summonoutcome.ReasonDeadline, func(w *summonWorld, a string) {
			w.schedule(a, nil)
			w.heartbeat(a, protocol.AgentBusy)
			w.reconcile(a)
			// 15m start allowance + the task's 40m timeout + 10m.
			w.now = summonT0.Add(65*time.Minute - time.Second)
			w.pass()
			w.wantOpen("a second before the task deadline", a)
			w.now = summonT0.Add(65 * time.Minute)
			w.pass()
		}},
		{"agent-interrupted", summonoutcome.ReasonAgentInterrupted, func(w *summonWorld, a string) {
			w.schedule(a, nil)
			w.heartbeat(a, protocol.AgentBusy)
			w.reconcile(a)
			w.now = summonT0.Add(3 * time.Minute)
			w.heartbeat(a, protocol.AgentInterrupted)
			w.reconcile(a)
		}},
		{"agent-failed before any agent start (agentd reported its start refusal, D10)", summonoutcome.ReasonAgentFailed, func(w *summonWorld, a string) {
			w.schedule(a, nil)
			w.now = summonT0.Add(16 * time.Minute)
			w.heartbeat(a, protocol.AgentFailed)
			w.reconcile(a)
		}},
		{"never-started (agentd never reported, D10)", summonoutcome.ReasonNeverStarted, func(w *summonWorld, a string) {
			w.schedule(a, &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"})
			w.reconcile(a)
			w.now = summonT0.Add(16*time.Minute - time.Second)
			w.pass()
			w.wantOpen("a second before the start deadline plus one minute", a)
			w.now = summonT0.Add(16 * time.Minute)
			w.pass()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, a, b := launchSummoned(t)
			tc.close(w, a.Name)
			w.wantClosed(a.Name, tc.reason, v1alpha1.OutcomeEscalated)
			if e := w.get(a.Name).Status.Summon.Escalation; e == nil || e.State != "Pending" {
				t.Fatalf("a remediation closed by its onUnreported records its pending filing: %+v", e)
			}
			first := w.get(a.Name).Status.DeepCopy()
			if w.released(a.Name) {
				t.Fatal("released while the pod can still launch")
			}
			w.pass()
			w.wantAdmitted("the closed session's pod still runs", map[string]bool{b.Name: false})

			// The release waits for the pod: the operator suspends the
			// session with its own value, and the stop is set on the pod.
			w.reconcile(a.Name)
			s := w.get(a.Name)
			if s.Spec.OperatingMode != v1alpha1.OperatingModeSuspended || s.Annotations[v1alpha1.AnnotationSuspendedBy] != SummonCloseSuspender {
				t.Fatalf("not suspended by the operator: %s %v", s.Spec.OperatingMode, s.Annotations)
			}
			var pod corev1.Pod
			if !w.exists(&pod, a.Name) || pod.Spec.ActiveDeadlineSeconds == nil {
				t.Fatalf("the private executor stop was not set on the pod: %+v", pod.Spec.ActiveDeadlineSeconds)
			}
			if h := s.Status.PrivateHistory; h.StopRequest == nil || h.StopRequest.PodUID != string(pod.UID) {
				t.Fatalf("no stop intent for the recorded executor: %+v", h.StopRequest)
			}
			if w.released(a.Name) {
				t.Fatal("released before the pod ended")
			}

			// Every container terminated under phase Running is not the end:
			// RestartPolicyAlways can start them again.
			w.now = w.now.Add(time.Minute)
			w.containersEnd(a.Name, corev1.PodRunning)
			w.reconcile(a.Name)
			w.pass()
			if w.released(a.Name) {
				t.Fatal("every container terminated in phase Running released the lane")
			}
			w.wantAdmitted("containers ended, phase Running", map[string]bool{b.Name: false})

			// The kubelet ends the pod: its terminal phase and terminal record
			// release the lane, the grant leaves the ledger, b is admitted.
			w.containersEnd(a.Name, corev1.PodFailed)
			w.reconcile(a.Name)
			s = w.get(a.Name)
			if !executorTerminalRecorded(s.Status.PrivateHistory) || !w.released(a.Name) {
				t.Fatalf("terminal pod did not release: terminal=%+v closed=%+v", s.Status.PrivateHistory.ExecutorTerminal, w.closed(a.Name))
			}
			w.pass()
			w.wantAdmitted("a's pod ended", map[string]bool{b.Name: true})
			for _, g := range w.grantNames() {
				if g == a.Name {
					t.Fatal("a's grant is still in the ledger")
				}
			}
			if !w.exists(&corev1.PersistentVolumeClaim{}, HomeClaimName(a.Name)) || !w.exists(&corev1.Pod{}, a.Name) {
				t.Fatal("the volume or the pod object was deleted")
			}

			// One close: a second pass and a new leader write nothing more.
			w.r = w.leader(nil)
			w.pass()
			w.reconcile(a.Name)
			got := w.get(a.Name).Status
			if !apiequality.Semantic.DeepEqual(first.Outcome, got.Outcome) || got.Summon.Closed.At != first.Summon.Closed.At || got.Summon.Closed.Reason != tc.reason ||
				!apiequality.Semantic.DeepEqual(first.Summon.Escalation, got.Summon.Escalation) {
				t.Fatalf("a second close: %+v then %+v", first.Summon, got.Summon)
			}
		})
	}
}

// summonStopFixture is summonOutcomeFixture's remediation with a stop
// recorded as the API's routes, the idle timer or kubectl record it.
func summonStopFixture(t *testing.T, stop, row string) (client.WithWatch, *Reconciler, *v1alpha1.AgentSession) {
	t.Helper()
	c, r, s, p := summonOutcomeFixture(t)
	now := r.Now()
	ctx := context.Background()
	s.Finalizers = []string{Finalizer}
	s.Annotations = map[string]string{}
	by := map[string]string{"person": "human/dev-env-system/dev-env-human", "session": "session/dev-env-1112-000000", "client": "client/dev-agents/dev-env-workbench", "idle-timer": IdleTimerSuspender}
	kind, who, _ := strings.Cut(stop, "-")
	switch kind {
	case "suspend":
		s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
		if who != "unattributed" {
			s.Annotations[v1alpha1.AnnotationSuspendedBy] = by[who]
		}
	case "reap":
		if who != "unattributed" {
			s.Annotations[v1alpha1.AnnotationReapedBy] = by[who]
		}
	}
	if err := c.Update(ctx, s); err != nil {
		t.Fatal(err)
	}
	if kind == "reap" {
		if err := c.Delete(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(s), s); err != nil {
		t.Fatal(err)
	}
	admitted := metav1.NewTime(now.Add(-2 * time.Minute))
	beat := metav1.NewTime(now.Add(-30 * time.Second))
	s.Status.Summon = &v1alpha1.SummonStatus{AdmittedAt: &admitted}
	s.Status.Outcome = &v1alpha1.OutcomeStatus{State: v1alpha1.OutcomeRunning, At: &admitted}
	s.Status.Agent = &v1alpha1.AgentStatus{Status: protocol.AgentBusy, LastHeartbeat: &beat}
	switch row {
	case "task-result":
		s.Status.TaskResult = &v1alpha1.TaskResultStatus{State: v1alpha1.TaskResultSucceeded}
	case "exited", "failed", "interrupted":
		s.Status.Agent.Status = row
	case "pod-terminated":
		p.Status.Phase = corev1.PodFailed
		if err := c.Status().Update(ctx, p); err != nil {
			t.Fatal(err)
		}
	case "session-lost":
		s.Status.PrivateHistory = &v1alpha1.PrivateHistoryStatus{LastExecutor: &v1alpha1.PrivateExecutorStatus{PodName: p.Name, PodUID: "an-earlier-pod"}}
	case "never-started":
		at := metav1.NewTime(now.Add(-16 * time.Minute))
		s.Status.Summon.AdmittedAt, s.Status.Agent = &at, nil
		s.Status.Outcome = &v1alpha1.OutcomeStatus{State: v1alpha1.OutcomePending, At: &at}
	case "deadline":
		at := metav1.NewTime(now.Add(-65 * time.Minute))
		s.Status.Summon.AdmittedAt = &at
	case "queued":
		s.Status.Summon, s.Status.Agent = nil, nil
		s.Status.Outcome = &v1alpha1.OutcomeStatus{State: v1alpha1.OutcomePending, At: &admitted}
	case "running":
	default:
		t.Fatalf("row %s", row)
	}
	if err := c.Status().Update(ctx, s); err != nil {
		t.Fatal(err)
	}
	return c, r, s
}

// Proof 5, ruling D1 row by row (it replaces slice a's
// TestSummonOutcomeOperatorStopSkipsCrashInputs): a person's reap or suspend
// of a running summoned session closes it failed, reason stopped, with no
// pending filing, in every row of 6.6's table; a session's or a client's
// stop, an unattributed change and the idle timer close it by onUnreported
// (here escalated, with its pending filing); the start failure agent-failed
// still wins. The reconciler's step and the watchdog's agree.
func TestSummonStopRowByRow(t *testing.T) {
	stops := []string{"reap-person", "suspend-person", "reap-session", "suspend-session", "suspend-client", "suspend-unattributed", "reap-unattributed", "suspend-idle-timer"}
	rows := []string{"task-result", "exited", "failed", "interrupted", "pod-terminated", "session-lost", "never-started", "deadline", "queued", "running"}
	for _, stop := range stops {
		for _, row := range rows {
			for _, watchdog := range []bool{false, true} {
				name := stop + "/" + row
				if watchdog {
					name += "/watchdog"
				}
				t.Run(name, func(t *testing.T) {
					c, r, s := summonStopFixture(t, stop, row)
					changed, err := r.closeSummon(context.Background(), s, watchdog)
					if err != nil || !changed {
						t.Fatalf("changed=%v err=%v", changed, err)
					}
					if err := c.Get(context.Background(), client.ObjectKeyFromObject(s), s); err != nil {
						t.Fatal(err)
					}
					closed := s.Status.Summon.Closed
					wantReason, wantState, filing := summonoutcome.ReasonSuspendedUnreported, v1alpha1.OutcomeEscalated, true
					switch {
					case row == "failed":
						wantReason = summonoutcome.ReasonAgentFailed
					case strings.HasSuffix(stop, "-person"):
						wantReason, wantState, filing = summonoutcome.ReasonStopped, v1alpha1.OutcomeFailed, false
					}
					if closed == nil || closed.Reason != wantReason || s.Status.Outcome.State != wantState || (s.Status.Summon.Escalation != nil) != filing {
						t.Fatalf("closed=%+v outcome=%+v filing=%+v; want %s %s filing=%v", closed, s.Status.Outcome, s.Status.Summon.Escalation, wantState, wantReason, filing)
					}
					if closed.Released != (row == "queued") {
						t.Fatalf("released=%v: only a never-admitted close releases at once", closed.Released)
					}
				})
			}
		}
	}
	// A curation's onUnreported is failed: any other stop closes it failed.
	c, r, s := summonStopFixture(t, "suspend-session", "running")
	s.Spec.Lane, s.Spec.Summon.OnUnreported = v1alpha1.LaneCuration, v1alpha1.SummonOnUnreportedFailed
	if err := c.Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if _, err := r.reconcileSummonOutcome(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if s.Status.Outcome.State != v1alpha1.OutcomeFailed || s.Status.Summon.Closed.Reason != summonoutcome.ReasonSuspendedUnreported || s.Status.Summon.Escalation != nil {
		t.Fatalf("curation stop: %+v %+v", s.Status.Outcome, s.Status.Summon)
	}
}

// Proof 1, session-lost: the latch's recorded executor gone by an uncached
// read, with no stop on the Session, closes once by onUnreported; the release
// rule frees the lane at once (the recorded pod is gone).
func TestSummonWatchdogSessionLostFreesTheLane(t *testing.T) {
	w, a, b := launchSummoned(t)
	w.schedule(a.Name, nil)
	w.heartbeat(a.Name, protocol.AgentBusy)
	w.reconcile(a.Name)
	var pod corev1.Pod
	if !w.exists(&pod, a.Name) {
		t.Fatal("no pod")
	}
	if err := w.base.Delete(context.Background(), &pod); err != nil {
		t.Fatal(err)
	}
	w.now = summonT0.Add(5 * time.Minute)
	w.pass()
	w.wantClosed(a.Name, summonoutcome.ReasonSessionLost, v1alpha1.OutcomeEscalated)
	w.reconcile(a.Name)
	if !w.released(a.Name) || w.get(a.Name).Spec.OperatingMode == v1alpha1.OperatingModeSuspended {
		t.Fatalf("a lost pod releases at once and needs no suspend: %+v", w.closed(a.Name))
	}
	w.pass()
	w.wantAdmitted("a lost", map[string]bool{b.Name: true})
	if w.podCreates(a.Name) != 1 {
		t.Fatalf("a lost session got another pod: %v", w.pods)
	}
}

// never-started with no executor attempt (the latch never started it): the
// close releases at once, and no suspend is needed.
func TestSummonWatchdogNeverStartedWithoutAttemptReleasesAtOnce(t *testing.T) {
	w, a, b := admissionPair(t, "lane")
	b.Spec.Summon.MaxHold = "24h"
	w.queue(a, b)
	w.pass()
	// No reconcile of a: no executor was ever attempted, agentd never reported.
	w.now = summonT0.Add(16 * time.Minute)
	w.pass()
	w.wantClosed(a.Name, summonoutcome.ReasonNeverStarted, v1alpha1.OutcomeEscalated)
	w.reconcile(a.Name)
	if !w.released(a.Name) {
		t.Fatal("a close with no attempt did not release")
	}
	w.wantNoPodOrVolume(a.Name)
	w.pass()
	w.wantAdmitted("a never started", map[string]bool{b.Name: true})
}

// The stated limit (coordinator answer op779-q-1): a recorded pod that is
// never scheduled has no start time, so the stop cannot end it. Its session
// is still closed, so a remediation escalates, but the lane holds.
func TestSummonUnscheduledRecordedPodHoldsTheLane(t *testing.T) {
	w, a, b := launchSummoned(t)
	w.now = summonT0.Add(16 * time.Minute)
	w.pass()
	w.wantClosed(a.Name, summonoutcome.ReasonNeverStarted, v1alpha1.OutcomeEscalated)
	w.reconcile(a.Name)
	var pod corev1.Pod
	if !w.exists(&pod, a.Name) || pod.Spec.NodeName != "" || pod.Spec.ActiveDeadlineSeconds != nil || w.released(a.Name) {
		t.Fatalf("an unscheduled pod: node=%q stop=%v released=%v", pod.Spec.NodeName, pod.Spec.ActiveDeadlineSeconds, w.released(a.Name))
	}
	if w.get(a.Name).Spec.OperatingMode != v1alpha1.OperatingModeSuspended {
		t.Fatal("the operator's suspend still applies")
	}
	w.pass()
	w.wantAdmitted("lane held by an unscheduled pod", map[string]bool{b.Name: false})
}

// Proof 3: a remediation Queued past its maxHold closes escalated with its
// pending filing; a curation closes failed with none. Both release at once.
func TestSummonHoldExpired(t *testing.T) {
	for _, lane := range []v1alpha1.Lane{v1alpha1.LaneRemediation, v1alpha1.LaneCuration} {
		t.Run(string(lane), func(t *testing.T) {
			running := summonedFor("running", callerA, lane, 2*time.Minute)
			held := summonedFor("held", callerA, lane, time.Minute)
			hold := 20 * time.Minute
			if lane == v1alpha1.LaneCuration {
				hold = 12 * time.Hour
				running.Spec.Summon.MaxHold = "24h"
			} else {
				running.Spec.Summon.MaxHold = "24h"
			}
			w := newSummonWorld(t, summonPolicyObject(callerA, 2, ptr.To[int32](1)))
			w.queue(running, held)
			w.pass()
			w.wantAdmitted("lane busy", map[string]bool{running.Name: true, held.Name: false})
			created := held.CreationTimestamp.Time
			w.now = created.Add(hold - time.Second)
			w.pass()
			w.wantOpen("a second before maxHold", held.Name)
			w.now = created.Add(hold)
			w.pass()
			want, filing := v1alpha1.OutcomeEscalated, true
			if lane == v1alpha1.LaneCuration {
				want, filing = v1alpha1.OutcomeFailed, false
			}
			w.wantClosed(held.Name, summonoutcome.ReasonHoldExpired, want)
			s := w.get(held.Name)
			if (s.Status.Summon.Escalation != nil) != filing || !s.Status.Summon.Closed.Released || s.Status.Summon.AdmittedAt != nil {
				t.Fatalf("filing=%+v closed=%+v admitted=%v", s.Status.Summon.Escalation, s.Status.Summon.Closed, s.Status.Summon.AdmittedAt)
			}
			if w.hold(held.Name) != "False/"+SummonReasonHoldExpired {
				t.Fatalf("condition %s", w.hold(held.Name))
			}
			w.wantNoPodOrVolume(held.Name)
		})
	}
}

// Proof 2 (F4): an agentd that keeps heartbeating while it reports
// interrupted or failed with no task result closes the session once; the
// heartbeats that follow and later passes change nothing.
func TestSummonHeartbeatingAgentClosesOnce(t *testing.T) {
	for _, tc := range []struct{ state, reason string }{{protocol.AgentInterrupted, summonoutcome.ReasonAgentInterrupted}, {protocol.AgentFailed, summonoutcome.ReasonAgentFailed}} {
		t.Run(tc.state, func(t *testing.T) {
			w, a, _ := launchSummoned(t)
			w.schedule(a.Name, nil)
			w.heartbeat(a.Name, protocol.AgentBusy)
			w.reconcile(a.Name)
			var first *v1alpha1.AgentSessionStatus
			for i := range 5 {
				w.now = summonT0.Add(time.Duration(2+i) * time.Minute)
				w.heartbeat(a.Name, tc.state)
				w.reconcile(a.Name)
				w.pass()
				if i == 0 {
					w.wantClosed(a.Name, tc.reason, v1alpha1.OutcomeEscalated)
					first = w.get(a.Name).Status.DeepCopy()
					continue
				}
				got := w.get(a.Name).Status
				if !apiequality.Semantic.DeepEqual(first.Outcome, got.Outcome) || got.Summon.Closed.At != first.Summon.Closed.At || got.Summon.Closed.Reason != tc.reason {
					t.Fatalf("heartbeat %d changed the close: %+v -> %+v", i, first.Summon.Closed, got.Summon.Closed)
				}
			}
		})
	}
}

// Proof 4: a late heartbeat, a late task result or a late report after a
// close changes nothing: one terminal write.
func TestSummonLateReportAfterCloseChangesNothing(t *testing.T) {
	w, a, _ := launchSummoned(t)
	w.schedule(a.Name, nil)
	w.heartbeat(a.Name, protocol.AgentBusy)
	w.reconcile(a.Name)
	w.now = summonT0.Add(65 * time.Minute)
	w.pass()
	w.wantClosed(a.Name, summonoutcome.ReasonDeadline, v1alpha1.OutcomeEscalated)
	before := w.get(a.Name).Status.DeepCopy()
	w.now = w.now.Add(time.Minute)
	s := w.get(a.Name)
	at := metav1.NewTime(w.now)
	s.Status.Agent = &v1alpha1.AgentStatus{Status: protocol.AgentExited, LastHeartbeat: &at, Task: &v1alpha1.TaskStatus{ExitCode: 0, FinishedAt: at}}
	if err := w.base.Status().Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	changed, err := summonoutcome.Write(context.Background(), w.base, w.get(a.Name), summonoutcome.Decision{State: v1alpha1.OutcomeDone, Note: "done late", Source: summonoutcome.Report, Reason: "done"}, w.now)
	if err != nil || changed {
		t.Fatalf("a late report: changed=%v err=%v", changed, err)
	}
	w.reconcile(a.Name)
	w.pass()
	got := w.get(a.Name).Status
	if !apiequality.Semantic.DeepEqual(before.Outcome, got.Outcome) || got.Summon.Closed.Reason != summonoutcome.ReasonDeadline || got.Summon.Closed.At != before.Summon.Closed.At ||
		!apiequality.Semantic.DeepEqual(before.Summon.Escalation, got.Summon.Escalation) {
		t.Fatalf("a late heartbeat or report changed the close: %+v -> %+v", before.Summon, got.Summon)
	}
}

// Proof 6: a reaped summoned session's close is written before its finalizer
// lifts. Every write that removes the finalizer finds the close stored.
func TestSummonReapedSessionClosedBeforeFinalizerLifts(t *testing.T) {
	for _, by := range []string{"human/dev-env-system/dev-env-human", "session/dev-env-1112-000000"} {
		t.Run(by, func(t *testing.T) {
			s := summonedFor("rem-reaped", callerA, v1alpha1.LaneRemediation, time.Minute)
			w := newSummonWorld(t, summonPolicyObject(callerA, 1, ptr.To[int32](1)))
			w.queue(s)
			lifted := 0
			check := func(ctx context.Context, c client.WithWatch, obj client.Object) {
				sess, ok := obj.(*v1alpha1.AgentSession)
				if !ok || controllerutilHas(sess.Finalizers, Finalizer) {
					return
				}
				var stored v1alpha1.AgentSession
				if err := c.Get(ctx, client.ObjectKeyFromObject(sess), &stored); err != nil || !controllerutilHas(stored.Finalizers, Finalizer) {
					return
				}
				lifted++
				if !summonoutcome.Terminal(stored.Status.Outcome) || stored.Status.Summon == nil || stored.Status.Summon.Closed == nil {
					t.Fatalf("the finalizer lifts before the close: %+v", stored.Status.Outcome)
				}
			}
			w.r = w.leader(&interceptor.Funcs{
				Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
					check(ctx, c, obj)
					return c.Patch(ctx, obj, patch, opts...)
				},
				Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
					check(ctx, c, obj)
					return c.Update(ctx, obj, opts...)
				},
			})
			live := w.get(s.Name)
			live.Annotations = map[string]string{v1alpha1.AnnotationReapedBy: by}
			if err := w.base.Update(context.Background(), live); err != nil {
				t.Fatal(err)
			}
			if err := w.base.Delete(context.Background(), live); err != nil {
				t.Fatal(err)
			}
			for range 4 {
				if !w.exists(&v1alpha1.AgentSession{}, s.Name) {
					break
				}
				w.reconcile(s.Name)
			}
			if w.exists(&v1alpha1.AgentSession{}, s.Name) || lifted == 0 {
				t.Fatalf("the reaped session was not released (lifted=%d)", lifted)
			}
		})
	}
}

func controllerutilHas(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// Proof 9 (item 779 comment 7384): a Session whose status write always fails
// does not stop another lane's ready Session from being admitted in the same
// pass. The failing one's grant keeps its slot; the pass reports the error.
func TestSummonKeptGrantFailureDoesNotBlockAnotherLane(t *testing.T) {
	stuck := summonedFor("stuck", callerA, v1alpha1.LaneRemediation, 2*time.Minute)
	other := summonedFor("other", callerB, v1alpha1.LaneCuration, time.Minute)
	contender := summonedFor("contender", callerB, v1alpha1.LaneRemediation, time.Minute)
	w := newSummonWorld(t, summonPolicyObject(callerA, 1, ptr.To[int32](1)), summonPolicyObject(callerB, 1, ptr.To[int32](1)))
	w.queue(stuck, other, contender)
	putTestGrants(t, w.base, grantFor(w.get(stuck.Name), summonT0))
	failing := errors.New("fixture: this Session's status write always fails")
	w.r = w.leader(&interceptor.Funcs{SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
		if obj.GetName() == stuck.Name {
			return failing
		}
		return c.SubResource(sub).Patch(ctx, obj, patch, opts...)
	}})
	err := w.r.SummonAdmissionPass(context.Background())
	if !errors.Is(err, failing) {
		t.Fatalf("the pass hid the failing write: %v", err)
	}
	w.wantAdmitted("another lane in the same pass", map[string]bool{other.Name: true, stuck.Name: false, contender.Name: false})
	if got := w.hold(contender.Name); got != "False/"+SummonReasonLaneBusy {
		t.Fatalf("the failing grant's lane was not held against its contender: %s", got)
	}
	if names := w.grantNames(); len(names) != 2 {
		t.Fatalf("grants %v: the failing one keeps its slot", names)
	}
}

// Proof 11: nothing changes for a session without spec.caller. A pass over
// ordinary sessions reads exactly as before (one uncached list), the
// watchdog reads nothing for them, and an ordinary private task's suspend
// never asks its executor to end.
func TestSummonWatchdogLeavesOrdinarySessionsAlone(t *testing.T) {
	ordinary := taskSession()
	ordinary.Name, ordinary.Namespace = "ordinary", "dev-agents"
	w := newSummonWorld(t, ordinary)
	reads := 0
	counting := interceptor.NewClient(w.base, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			reads++
			return c.Get(ctx, key, obj, opts...)
		},
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			reads++
			return c.List(ctx, list, opts...)
		},
	})
	w.r.APIReader = counting
	w.now = summonT0.Add(100 * time.Hour)
	w.pass()
	if reads != 1 {
		t.Fatalf("a pass over ordinary sessions made %d reads, want 1", reads)
	}
	s := w.get(ordinary.Name)
	if summonStopDue(s) {
		t.Fatal("an ordinary session's suspend is a summoned stop")
	}
	s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
	if summonStopDue(s) {
		t.Fatal("an ordinary suspended task's executor is asked to end")
	}
	byPerson, other := summonStop(s)
	if byPerson == other {
		// summonStop is read only for summoned sessions; it still classifies.
		t.Fatalf("summonStop(%v, %v)", byPerson, other)
	}
}

// beat is a heartbeat whose agent last did something at activity.
func (w *summonWorld) beat(name, state string, activity time.Time) {
	w.t.Helper()
	s := w.get(name)
	at, act := metav1.NewTime(w.now), metav1.NewTime(activity)
	s.Status.Agent = &v1alpha1.AgentStatus{Status: state, LastHeartbeat: &at, LastActivity: &act}
	if err := w.base.Status().Update(context.Background(), s); err != nil {
		w.t.Fatal(err)
	}
}

// remoteAdmitted queues a remote summoned session, admits it as slice d's
// latch would (this slice holds every remote one), and gives it a pod.
func remoteAdmitted(t *testing.T, lane v1alpha1.Lane, name string) (*summonWorld, *v1alpha1.AgentSession) {
	t.Helper()
	s := summonedFor(name, callerA, lane, 0)
	if lane == v1alpha1.LaneUpgrade {
		s.Spec.Summon.StaleAfter = "3h"
	}
	w := newSummonWorld(t, summonPolicyObject(callerA, 1, ptr.To[int32](1)))
	w.queue(s)
	w.admitDirect(s)
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "dev-agents", UID: types.UID("pod-" + name), OwnerReferences: []metav1.OwnerReference{ownerRef(w.get(name))}},
		Spec: corev1.PodSpec{NodeName: summonNode, RestartPolicy: corev1.RestartPolicyAlways, Containers: []corev1.Container{{Name: ContainerName}}}}
	w.add(pod)
	pod.Status = corev1.PodStatus{Phase: corev1.PodRunning, StartTime: &metav1.Time{Time: w.now}, ContainerStatuses: []corev1.ContainerStatus{{Name: ContainerName,
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(w.now)}}}}}
	if err := w.base.Status().Update(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	return w, s
}

func (w *summonWorld) pages(name string) []v1alpha1.SummonPage {
	if st := w.get(name).Status.Summon; st != nil {
		return st.Pages
	}
	return nil
}

// Part B, heartbeat-lost: a remote session with no heartbeat for 10
// minutes while its pod exists closes by onUnreported; with no pod it does
// not. The operator then suspends it so its pod goes (rescued and deleted as
// on any suspend; this world has no rescuer, so the pod stays); the release
// waits for the pod.
func TestSummonWatchdogHeartbeatLost(t *testing.T) {
	for _, withPod := range []bool{true, false} {
		t.Run(map[bool]string{true: "pod exists", false: "pod gone"}[withPod], func(t *testing.T) {
			w, s := remoteAdmitted(t, v1alpha1.LaneEscalation, "esc-lost")
			w.beat(s.Name, protocol.AgentIdle, summonT0)
			w.pass() // start this leader's ten-minute heartbeat recovery window
			if !withPod {
				var pod corev1.Pod
				w.exists(&pod, s.Name)
				if err := w.base.Delete(context.Background(), &pod); err != nil {
					t.Fatal(err)
				}
			}
			w.now = summonT0.Add(10*time.Minute - time.Second)
			w.pass()
			w.wantOpen("a second before 10 minutes", s.Name)
			w.now = summonT0.Add(10 * time.Minute)
			w.pass()
			if !withPod {
				w.wantOpen("no pod: heartbeat-lost needs its pod", s.Name)
				return
			}
			w.wantClosed(s.Name, summonoutcome.ReasonHeartbeatLost, v1alpha1.OutcomeFailed)
			w.reconcile(s.Name)
			live := w.get(s.Name)
			if live.Spec.OperatingMode != v1alpha1.OperatingModeSuspended || live.Annotations[v1alpha1.AnnotationSuspendedBy] != SummonCloseSuspender || w.released(s.Name) {
				t.Fatalf("suspend=%s by=%q released=%v", live.Spec.OperatingMode, live.Annotations[v1alpha1.AnnotationSuspendedBy], w.released(s.Name))
			}
			var pod corev1.Pod
			w.exists(&pod, s.Name)
			if err := w.base.Delete(context.Background(), &pod); err != nil {
				t.Fatal(err)
			}
			w.reconcile(s.Name)
			if !w.released(s.Name) {
				t.Fatal("the pod gone did not release")
			}
		})
	}
}

// Part B, stale: a remote upgrade with no agent activity and no working
// report for staleAfter closes; a working report moves the bound.
func TestSummonWatchdogStale(t *testing.T) {
	w, s := remoteAdmitted(t, v1alpha1.LaneUpgrade, "wo-stale")
	w.beat(s.Name, protocol.AgentIdle, summonT0)
	w.now = summonT0.Add(30 * time.Minute)
	if changed, err := summonoutcome.Write(context.Background(), w.base, w.get(s.Name), summonoutcome.Decision{State: v1alpha1.OutcomeRunning, Note: "working"}, w.now); err != nil || !changed {
		t.Fatalf("working report: %v %v", changed, err)
	}
	w.now = summonT0.Add(30*time.Minute + 3*time.Hour - time.Second)
	w.beat(s.Name, protocol.AgentIdle, summonT0)
	w.pass()
	w.wantOpen("a second before staleAfter from the working report", s.Name)
	w.now = summonT0.Add(30*time.Minute + 3*time.Hour)
	w.beat(s.Name, protocol.AgentIdle, summonT0)
	w.pass()
	w.wantClosed(s.Name, summonoutcome.ReasonStale, v1alpha1.OutcomeFailed)
}

// Part B: a remote escalation idle 24 hours gets one reminder page event,
// never two; idle 72 hours it closes failed, reason escalation-bound, and the
// note says so. A fresh heartbeat hides neither. Since story 794 the same
// session also settles its registration (it reports no Remote Control
// record), so the reminder is counted apart from escalation-started.
func TestSummonWatchdogEscalationReminderAndBound(t *testing.T) {
	w, s := remoteAdmitted(t, v1alpha1.LaneEscalation, "esc-idle")
	step := func(at time.Duration) {
		w.now = summonT0.Add(at)
		w.beat(s.Name, protocol.AgentIdle, summonT0)
		w.pass()
	}
	reminders := func() []v1alpha1.SummonPage {
		var out []v1alpha1.SummonPage
		for _, p := range w.pages(s.Name) {
			if p.Event == v1alpha1.PageEscalationReminder {
				out = append(out, p)
			}
		}
		return out
	}
	step(24*time.Hour - time.Second)
	if p := reminders(); len(p) != 0 {
		t.Fatalf("a reminder before 24 hours: %+v", p)
	}
	step(24 * time.Hour)
	step(25 * time.Hour)
	step(72*time.Hour - time.Second)
	w.wantOpen("a second before 72 hours", s.Name)
	if p := reminders(); len(p) != 1 || !p[0].At.Time.Equal(summonT0.Add(24*time.Hour)) {
		t.Fatalf("want one reminder at 24 hours: %+v", p)
	}
	if p := startedPages(w.pages(s.Name)); len(p) != 1 || !p[0].At.Time.Equal(summonT0.Add(25*time.Hour)) {
		t.Fatalf("want one escalation-started at 25 hours, the first pass after the bound (the agent was seen at 24 hours less a second, so its bound fell 5 minutes later): %+v", p)
	}
	step(72 * time.Hour)
	w.wantClosed(s.Name, summonoutcome.ReasonEscalationBound, v1alpha1.OutcomeFailed)
	if note := w.get(s.Name).Status.Outcome.Note; !strings.Contains(note, "escalation-bound") || !strings.Contains(note, "72 hours") {
		t.Fatalf("note %q", note)
	}
	if p := w.pages(s.Name); len(p) != 2 {
		t.Fatalf("pages after the close: %+v", p)
	}
}

// Part B: an escalation Queued 15 minutes gets one held page event, never
// two; an upgrade held as long gets none.
func TestSummonHeldEscalationPagesOnce(t *testing.T) {
	esc := summonedFor("esc-held", callerA, v1alpha1.LaneEscalation, 0)
	up := summonedFor("wo-held", callerA, v1alpha1.LaneUpgrade, 0)
	// The escalations pool is full, so esc-held is held for it (PoolBusy).
	// Until OP780 the remote latch's absence held it.
	busy := summonedFor("esc-busy", callerA, v1alpha1.LaneEscalation, time.Hour)
	w := newSummonWorld(t, summonPolicyObject(callerA, 1, ptr.To[int32](1)))
	w.queue(busy)
	w.admitDirect(busy)
	w.queue(esc, up)
	for _, at := range []time.Duration{15*time.Minute - time.Second, 15 * time.Minute, 16 * time.Minute, 30 * time.Minute, 24 * time.Hour} {
		w.now = summonT0.Add(at)
		w.pass()
		want := 1
		if at < 15*time.Minute {
			want = 0
		}
		if p := w.pages(esc.Name); len(p) != want || want == 1 && (p[0].Event != v1alpha1.PageEscalationHeld || !p[0].At.Time.Equal(summonT0.Add(15*time.Minute))) {
			t.Fatalf("at %s: pages %+v, want %d held", at, p, want)
		}
		if p := w.pages(up.Name); len(p) != 0 {
			t.Fatalf("an upgrade got a page: %+v", p)
		}
	}
	if w.admitted(esc.Name) || w.hold(esc.Name) != "False/"+SummonReasonPoolBusy {
		t.Fatalf("the held escalation: %s", w.hold(esc.Name))
	}
}
