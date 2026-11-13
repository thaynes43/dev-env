package controller

// The proofs of story 780 (slice d of item 741; DESIGN-064 5.9, F3 and N2):
// a summoned remote session gets one pod, recorded by the remote executor
// latch, and never a second by any path; its loss closes it session-lost;
// a known non-attempt re-arms once; and an operator close of a launch that
// may be in flight, its pod still present, releases its reservation only in
// a pod whose agentd carries the start fence, once the start deadline plus
// one minute and the close plus five minutes have both passed (ruling C1).
// Every test runs on the fake API server and clock of
// summon_admission_test.go; writes are interleaved by client hooks, never by
// goroutine timing.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/summonoutcome"
	"github.com/thaynes43/dev-env/internal/templates"
)

const (
	fencedImage   = "ghcr.io/thaynes43/dev-env:2.108.0@sha256:0000000000000000000000000000000000000000000000000000000000000001"
	unfencedImage = "ghcr.io/thaynes43/dev-env:2.107.9@sha256:0000000000000000000000000000000000000000000000000000000000000001"
)

// useAgentImage makes the templates pin image, the one every summoned pod runs.
func (w *summonWorld) useAgentImage(image string) {
	w.t.Helper()
	var cm corev1.ConfigMap
	if err := w.base.Get(context.Background(), templatesKey, &cm); err != nil {
		w.t.Fatal(err)
	}
	cm.Data[templates.Key] = strings.Replace(exampleDoc, "ghcr.io/thaynes43/dev-env:2.0.0@sha256:0000000000000000000000000000000000000000000000000000000000000001", image, 1)
	if cm.Data[templates.Key] == exampleDoc && image != "ghcr.io/thaynes43/dev-env:2.0.0@sha256:0000000000000000000000000000000000000000000000000000000000000001" {
		w.t.Fatal("the example templates' image moved; update useAgentImage")
	}
	if err := w.base.Update(context.Background(), &cm); err != nil {
		w.t.Fatal(err)
	}
}

func (w *summonWorld) pod(name string) *corev1.Pod {
	w.t.Helper()
	var pod corev1.Pod
	if !w.exists(&pod, name) {
		return nil
	}
	return &pod
}

// runPod marks the session's pod running, as the kubelet would, so a suspend
// must rescue it before it goes (this world has no rescuer: it stays).
func (w *summonWorld) runPod(name string) {
	w.t.Helper()
	pod := w.pod(name)
	if pod == nil {
		w.t.Fatalf("%s has no pod", name)
	}
	pod.Spec.NodeName = summonNode
	if err := w.base.Update(context.Background(), pod); err != nil {
		w.t.Fatal(err)
	}
	pod = w.pod(name)
	pod.Status = corev1.PodStatus{Phase: corev1.PodRunning, StartTime: &metav1.Time{Time: w.now}, ContainerStatuses: []corev1.ContainerStatus{{Name: ContainerName,
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(w.now)}}}}}
	if err := w.base.Status().Update(context.Background(), pod); err != nil {
		w.t.Fatal(err)
	}
}

func (w *summonWorld) executor(name string) *v1alpha1.SummonExecutorStatus {
	if st := w.get(name).Status.Summon; st != nil {
		return st.Executor
	}
	return nil
}

// admittedRemote queues a remote upgrade (a single-flight lane) and admits it
// by the leader's own pass.
func admittedRemote(t *testing.T, w *summonWorld, name string, age time.Duration) *v1alpha1.AgentSession {
	t.Helper()
	s := summonedFor(name, callerA, v1alpha1.LaneUpgrade, age)
	w.queue(s)
	w.pass()
	if !w.admitted(name) {
		t.Fatalf("%s not admitted: %s", name, w.hold(name))
	}
	return s
}

// The latch (F3): the attempt is recorded and confirmed before the one pod
// create, and the pod's UID once it appears; the pod carries the intent.
// After an eviction (the pod gone) or with another pod under its name, the
// controller creates no second pod and the leader's watchdog closes the
// session session-lost, once. 7265 point 2 and 5.9's "real pod loss".
func TestSummonRemoteLatchOnePodThenSessionLost(t *testing.T) {
	for _, loss := range []string{"evicted", "replaced"} {
		t.Run(loss, func(t *testing.T) {
			w := newSummonWorld(t, summonPolicyObject(callerA, 1, ptr.To[int32](1)))
			s := admittedRemote(t, w, "wo-1", time.Minute)
			if w.executor(s.Name) != nil {
				t.Fatal("a latch before any build")
			}
			w.reconcile(s.Name)
			pod := w.pod(s.Name)
			ex := w.executor(s.Name)
			if pod == nil || w.podCreates(s.Name) != 1 || ex == nil || !ex.Attempted || ex.Intent == "" || ex.NotCreated {
				t.Fatalf("pod %v creates %d latch %+v", pod != nil, w.podCreates(s.Name), ex)
			}
			if pod.Annotations[summonExecutorAnnotation] != ex.Intent || ex.PodUID != string(pod.UID) {
				t.Fatalf("latch %+v does not name pod %s with intent %q", ex, pod.UID, pod.Annotations[summonExecutorAnnotation])
			}
			// The leader's first pass starts its heartbeat window; the agent
			// runs and heartbeats.
			w.beat(s.Name, protocol.AgentBusy, w.now)
			w.pass()
			w.wantOpen("running", s.Name)
			// The pod is lost.
			if err := w.base.Delete(context.Background(), pod); err != nil {
				t.Fatal(err)
			}
			if loss == "replaced" {
				other := pod.DeepCopy()
				other.ResourceVersion, other.UID = "", "pod-someone-else"
				w.add(other)
			}
			for range 3 {
				w.reconcile(s.Name)
			}
			if w.podCreates(s.Name) != 1 {
				t.Fatalf("a second pod was created for a latched remote session: %v", w.pods)
			}
			if got := w.executor(s.Name); got.PodUID != ex.PodUID || got.Intent != ex.Intent || !got.Attempted {
				t.Fatalf("the latch changed: %+v", got)
			}
			w.now = w.now.Add(time.Minute)
			w.beat(s.Name, protocol.AgentBusy, w.now) // a heartbeat hides no loss
			w.pass()
			w.wantClosed(s.Name, summonoutcome.ReasonSessionLost, v1alpha1.OutcomeFailed)
			w.pass()
			w.reconcile(s.Name)
			if c := w.closed(s.Name); c.Reason != summonoutcome.ReasonSessionLost {
				t.Fatalf("closed twice: %+v", c)
			}
			if w.podCreates(s.Name) != 1 {
				t.Fatalf("a closed lost session got a pod: %v", w.pods)
			}
		})
	}
}

// A close that lands after the gate's read and before the latch write
// conflicts with that write: no attempt is recorded and no pod is created.
func TestSummonRemoteCloseBeforeLatchWriteCreatesNoPod(t *testing.T) {
	w := newSummonWorld(t, summonPolicyObject(callerA, 1, ptr.To[int32](1)))
	s := admittedRemote(t, w, "wo-1", time.Minute)
	closed := false
	w.r = w.leader(&interceptor.Funcs{SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, o client.Object, opts ...client.SubResourceUpdateOption) error {
		if live, ok := o.(*v1alpha1.AgentSession); ok && !closed && live.Status.Summon != nil && live.Status.Summon.Executor != nil {
			closed = true
			w.close(s.Name, summonoutcome.Unreported)
		}
		return c.SubResource(sub).Update(ctx, o, opts...)
	}})
	w.reconcile(s.Name)
	if !closed {
		t.Fatal("the latch write never ran")
	}
	if w.podCreates(s.Name) != 0 || w.executor(s.Name) != nil {
		t.Fatalf("closed before its latch committed, yet pods %v latch %+v", w.pods, w.executor(s.Name))
	}
}

// A same-pod agentd restart (the container restarts in place: same pod UID)
// is not a loss: the latch holds the one pod and the controller does nothing.
// agentd in it starts nothing (TestSummonedLaunchRecordStartsNothingOnBoot);
// its interrupted report closes the session once, agent-interrupted, and the
// close's suspend never brings a second pod.
func TestSummonRemoteSamePodRestartClosesOnce(t *testing.T) {
	w := newSummonWorld(t, summonPolicyObject(callerA, 1, ptr.To[int32](1)))
	s := admittedRemote(t, w, "wo-1", time.Minute)
	w.reconcile(s.Name)
	w.runPod(s.Name)
	w.beat(s.Name, protocol.AgentBusy, w.now)
	w.pass()
	w.now = w.now.Add(time.Minute)
	w.beat(s.Name, protocol.AgentInterrupted, w.now)
	w.reconcile(s.Name)
	w.wantClosed(s.Name, summonoutcome.ReasonAgentInterrupted, v1alpha1.OutcomeFailed)
	live := w.get(s.Name)
	if live.Spec.OperatingMode != v1alpha1.OperatingModeSuspended || live.Annotations[v1alpha1.AnnotationSuspendedBy] != SummonCloseSuspender {
		t.Fatalf("the close did not suspend: %s by %q", live.Spec.OperatingMode, live.Annotations[v1alpha1.AnnotationSuspendedBy])
	}
	// The pod goes on the suspend; nothing makes another, even after the
	// operator's suspend is lifted by someone (the resume route refuses it).
	if err := w.base.Delete(context.Background(), w.pod(s.Name)); err != nil {
		t.Fatal(err)
	}
	live = w.get(s.Name)
	live.Spec.OperatingMode = v1alpha1.OperatingModeRunning
	if err := w.base.Update(context.Background(), live); err != nil {
		t.Fatal(err)
	}
	w.pass()
	w.reconcile(s.Name)
	if w.podCreates(s.Name) != 1 {
		t.Fatalf("pods %v", w.pods)
	}
	if c := w.closed(s.Name); c.Reason != summonoutcome.ReasonAgentInterrupted || !c.Released {
		t.Fatalf("closed %+v", c)
	}
}

// DESIGN-019's known non-attempt re-arms once and only once: a mark over the
// reservation's own write lets the same intent be reserved again; with no new
// mark, never again; a mark over a later write is not made.
func TestSummonRemoteKnownNonAttemptRearmsOnce(t *testing.T) {
	w := newSummonWorld(t, summonPolicyObject(callerA, 1, ptr.To[int32](1)))
	s := admittedRemote(t, w, "wo-1", time.Minute)
	ctx := context.Background()
	res, err := w.r.reserveSummonExecutor(ctx, w.get(s.Name))
	if err != nil || !res.summon || res.intent == "" {
		t.Fatalf("first reservation %+v %v", res, err)
	}
	w.r.recordExecutorNotCreated(ctx, w.get(s.Name), res)
	if ex := w.executor(s.Name); !ex.NotCreated || ex.Intent != res.intent {
		t.Fatalf("no known non-attempt recorded: %+v", ex)
	}
	if why := summonBuildRefusal(w.get(s.Name), w.r.summonTaskLatchOn()); why != "" {
		t.Fatalf("a known non-attempt is refused by the gate: %s", why)
	}
	again, err := w.r.reserveSummonExecutor(ctx, w.get(s.Name))
	if err != nil || again.intent != res.intent {
		t.Fatalf("re-arm %+v %v: want the same intent", again, err)
	}
	if ex := w.executor(s.Name); ex.NotCreated || !ex.Attempted {
		t.Fatalf("the re-arm did not clear its mark: %+v", ex)
	}
	if _, err := w.r.reserveSummonExecutor(ctx, w.get(s.Name)); err == nil {
		t.Fatal("re-armed twice on one mark")
	}
	if why := summonBuildRefusal(w.get(s.Name), w.r.summonTaskLatchOn()); why == "" {
		t.Fatal("the gate lets a re-armed attempt build again")
	}
	// A mark over a stale reservation (another write since) is not made.
	w.r.recordExecutorNotCreated(ctx, w.get(s.Name), res)
	if ex := w.executor(s.Name); ex.NotCreated {
		t.Fatalf("a stale reservation marked a non-attempt: %+v", ex)
	}
	// A failed confirmation after an acknowledged latch write is a known
	// non-attempt ensure marks: the reconcile records it and the next one
	// builds the one pod with the same intent.
	w2 := newSummonWorld(t, summonPolicyObject(callerA, 1, ptr.To[int32](1)))
	s2 := admittedRemote(t, w2, "wo-2", time.Minute)
	failed := false
	r := w2.leader(nil)
	r.APIReader = interceptor.NewClient(w2.base, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, o client.Object, opts ...client.GetOption) error {
		if live, ok := o.(*v1alpha1.AgentSession); ok && !failed {
			if err := c.Get(ctx, key, o, opts...); err != nil {
				return err
			}
			if live.Status.Summon != nil && live.Status.Summon.Executor != nil && !live.Status.Summon.Executor.NotCreated {
				failed = true
				return errors.New("the confirming read failed")
			}
			return nil
		}
		return c.Get(ctx, key, o, opts...)
	}})
	w2.r = r
	w2.reconcile(s2.Name)
	ex := w2.executor(s2.Name)
	if !failed || ex == nil || ex.PodUID == "" || w2.podCreates(s2.Name) != 1 {
		t.Fatalf("failed=%v latch %+v pods %v", failed, ex, w2.pods)
	}
}

// The record "this pod's agentd carries the start fence" (coordinator answer
// op780-q-1): the agent container's released image, at or above 2.108.
func TestPodCarriesStartFence(t *testing.T) {
	digest := "@sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		image string
		want  bool
	}{
		{"ghcr.io/thaynes43/dev-env:2.108.0" + digest, true},
		{"ghcr.io/thaynes43/dev-env:2.108.7" + digest, true},
		{"ghcr.io/thaynes43/dev-env:2.115.0" + digest, true},
		{"ghcr.io/thaynes43/dev-env:2.107.99" + digest, false},
		{"ghcr.io/thaynes43/dev-env:2.105.0" + digest, false},
		{"ghcr.io/thaynes43/dev-env:2.0108.0" + digest, false},
		{"ghcr.io/thaynes43/dev-env:2.108" + digest, false},
		{"ghcr.io/thaynes43/dev-env:2.108.0", false},
		{"ghcr.io/thaynes43/dev-env" + digest, false},
		{"ghcr.io/thaynes43/dev-env:sha-59169f1" + digest, false},
		{"ghcr.io/thaynes43/dev-env:3.0.0" + digest, false},
		{"ghcr.io/someone/dev-env:2.108.0" + digest, false},
		{"", false},
	} {
		pod := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "sidecar", Image: fencedImage}, {Name: ContainerName, Image: tc.image}}}}
		if got := podCarriesStartFence(pod); got != tc.want {
			t.Errorf("%q: fence %v, want %v", tc.image, got, tc.want)
		}
	}
	if podCarriesStartFence(&corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "sidecar", Image: fencedImage}}}}) || podCarriesStartFence(nil) {
		t.Error("a pod with no agent container carries the fence")
	}
}

// Release (proof 6) and the operator's suspend together: an operator close of
// an admitted session whose first launch may be in flight suspends it at
// once (OP779), and releases its reservation at the start deadline plus one
// minute when its pod's agentd carries the fence; without the fence it holds
// until the pod is gone. The close is at five minutes, so the deadline is the
// later of ruling C1's two bounds. For a remote session and a task.
func TestSummonReleaseAtStartDeadlineOnlyWithTheFence(t *testing.T) {
	for _, lane := range []v1alpha1.Lane{v1alpha1.LaneUpgrade, v1alpha1.LaneRemediation} {
		for _, image := range []string{fencedImage, unfencedImage} {
			t.Run(string(lane)+"/"+map[string]string{fencedImage: "fenced", unfencedImage: "unfenced"}[image], func(t *testing.T) {
				w := newSummonWorld(t, summonPolicyObject(callerA, 2, ptr.To[int32](1)))
				w.useAgentImage(image)
				a := summonedFor("a-1", callerA, lane, 2*time.Minute)
				b := summonedFor("b-1", callerA, lane, time.Minute)
				w.queue(a, b)
				w.pass()
				w.wantAdmitted("first", map[string]bool{"a-1": true, "b-1": false})
				w.reconcile(a.Name)
				pod := w.pod(a.Name)
				if pod == nil || pod.Spec.Containers[0].Image != image {
					t.Fatalf("pod %+v", pod)
				}
				w.runPod(a.Name)
				// Closed by the operator before any agent start, five minutes in.
				w.now = summonT0.Add(5 * time.Minute)
				w.close(a.Name, summonoutcome.Unreported)
				w.reconcile(a.Name)
				live := w.get(a.Name)
				if live.Spec.OperatingMode != v1alpha1.OperatingModeSuspended || live.Annotations[v1alpha1.AnnotationSuspendedBy] != SummonCloseSuspender {
					t.Fatalf("no operator suspend: %s by %q", live.Spec.OperatingMode, live.Annotations[v1alpha1.AnnotationSuspendedBy])
				}
				deadline := summonT0.Add(SummonStartWindow + time.Minute)
				for _, at := range []time.Time{deadline.Add(-time.Second), deadline} {
					w.now = at
					if w.pod(a.Name) == nil {
						t.Fatal("the pod went in this world; the test needs it present")
					}
					w.reconcile(a.Name)
					want := image == fencedImage && !at.Before(deadline)
					if w.released(a.Name) != want {
						t.Fatalf("at %s: released=%v, want %v", at.Sub(summonT0), w.released(a.Name), want)
					}
					w.pass()
					w.wantAdmitted(at.Sub(summonT0).String(), map[string]bool{"b-1": want})
				}
				if image == fencedImage {
					return
				}
				if err := w.base.Delete(context.Background(), w.pod(a.Name)); err != nil {
					t.Fatal(err)
				}
				w.reconcile(a.Name)
				if !w.released(a.Name) {
					t.Fatal("not released once its pod was gone")
				}
				w.pass()
				w.wantAdmitted("pod gone", map[string]bool{"b-1": true})
			})
		}
	}
}

// digestOnlyImage is an agent image pinned by digest with no release tag: it
// carries no start fence (podCarriesStartFence).
const digestOnlyImage = "ghcr.io/thaynes43/dev-env@sha256:0000000000000000000000000000000000000000000000000000000000000001"

// bindUnstarted binds name's pod to a node whose kubelet never starts it (a
// partitioned node): phase Pending with no start time and no container
// status. A remote session's suspend must rescue such a pod before it goes,
// and in this world, which has no rescuer, the pod stays.
func (w *summonWorld) bindUnstarted(name string) {
	w.t.Helper()
	pod := w.pod(name)
	if pod == nil {
		w.t.Fatalf("%s has no pod", name)
	}
	pod.Spec.NodeName = summonNode
	if err := w.base.Update(context.Background(), pod); err != nil {
		w.t.Fatal(err)
	}
	pod = w.pod(name)
	pod.Status = corev1.PodStatus{Phase: corev1.PodPending}
	if err := w.base.Status().Update(context.Background(), pod); err != nil {
		w.t.Fatal(err)
	}
}

// Ruling C1 (story 780 fix round 1, coordinator ruling in PR 1100 comment
// 6837450502): with the session's pod still present, the start fence alone
// frees a lane only once the start deadline plus one minute and
// closed.at plus five minutes have both passed; until then the release waits
// for the pod to go, as before the fence. Every close here is past the start
// deadline plus one minute, so the close is the later bound.
//
//   - A fenced pod that still runs holds its lane at 4m59s after the close
//     and frees it at 5m; the next session of the lane is admitted only then.
//     The task is closed deadline by the leader's watchdog: its own timer did
//     not stop it. The remote session is closed agent-interrupted.
//   - The same pod ending one minute after the close frees the lane then: a
//     task's pod by its terminal phase with its terminal record, a remote
//     session's recorded pod by going (a remote session has no terminal
//     record, and its release rule is unchanged).
//   - A fenced pod that never started, which the operator's stop cannot end,
//     frees its lane at 5m after its never-started close: a task's pod never
//     scheduled, a remote session's pod bound to a node that never starts it.
//     An unscheduled remote pod is deleted by the operator's suspend and
//     frees its lane at once.
//   - An unfenced pod, an image below the floor or a digest-only image,
//     never frees its lane on time alone, only by going.
func TestSummonFencedReleaseWaitsFiveMinutesAfterTheClose(t *testing.T) {
	const never = time.Duration(-1)
	for _, mode := range []struct {
		name string
		lane v1alpha1.Lane
	}{{"task", v1alpha1.LaneRemediation}, {"remote", v1alpha1.LaneUpgrade}} {
		for _, tc := range []struct {
			name  string
			image string
			// pod is how the pod stands at the close: running, unscheduled
			// or bound to a node that never starts it.
			pod string
			// endAt, when set, is when after the close the pod ends.
			endAt time.Duration
			// releaseAt is when after the close the lane is freed, or never.
			releaseAt            time.Duration
			remoteOnly, taskOnly bool
		}{
			{name: "fenced, running", image: fencedImage, pod: "running", releaseAt: 5 * time.Minute},
			{name: "fenced, ends a minute after the close", image: fencedImage, pod: "running", endAt: time.Minute, releaseAt: time.Minute},
			{name: "fenced, never scheduled", image: fencedImage, pod: "unscheduled", releaseAt: 5 * time.Minute, taskOnly: true},
			{name: "fenced, bound and never started", image: fencedImage, pod: "unstarted", releaseAt: 5 * time.Minute, remoteOnly: true},
			{name: "fenced, never scheduled, deleted by the suspend", image: fencedImage, pod: "unscheduled", releaseAt: 0, remoteOnly: true},
			{name: "below the floor, running", image: unfencedImage, pod: "running", releaseAt: never},
			{name: "digest only, running", image: digestOnlyImage, pod: "running", releaseAt: never},
		} {
			if tc.remoteOnly && mode.lane != v1alpha1.LaneUpgrade || tc.taskOnly && mode.lane == v1alpha1.LaneUpgrade {
				continue
			}
			t.Run(mode.name+"/"+tc.name, func(t *testing.T) {
				w := newSummonWorld(t, summonPolicyObject(callerA, 2, ptr.To[int32](1)))
				w.useAgentImage(tc.image)
				a := summonedFor("a-1", callerA, mode.lane, 2*time.Minute)
				b := summonedFor("b-1", callerA, mode.lane, time.Minute)
				b.Spec.Summon.MaxHold = "24h" // b waits out a's whole run without its own hold bound tripping
				w.queue(a, b)
				w.pass()
				w.wantAdmitted("first", map[string]bool{"a-1": true, "b-1": false})
				w.reconcile(a.Name)
				pod := w.pod(a.Name)
				if pod == nil || pod.Spec.Containers[0].Image != tc.image || podCarriesStartFence(pod) != (tc.image == fencedImage) {
					t.Fatalf("pod %+v", pod)
				}
				if mode.lane == v1alpha1.LaneUpgrade {
					if ex := w.executor(a.Name); ex == nil || ex.PodUID != string(pod.UID) {
						t.Fatalf("the remote latch records no pod: %+v", ex)
					}
				}

				// The close, past the start deadline plus one minute.
				switch {
				case tc.pod == "running" && mode.lane == v1alpha1.LaneRemediation:
					w.schedule(a.Name, nil)
					w.heartbeat(a.Name, protocol.AgentBusy)
					w.reconcile(a.Name)
					// 15m start allowance + the task's 40m timeout + 10m.
					w.now = summonT0.Add(65 * time.Minute)
					w.pass()
					w.wantClosed(a.Name, summonoutcome.ReasonDeadline, v1alpha1.OutcomeEscalated)
				case tc.pod == "running":
					w.schedule(a.Name, nil)
					w.heartbeat(a.Name, protocol.AgentBusy)
					w.reconcile(a.Name)
					w.now = summonT0.Add(30 * time.Minute)
					w.heartbeat(a.Name, protocol.AgentInterrupted)
					w.reconcile(a.Name)
					w.wantClosed(a.Name, summonoutcome.ReasonAgentInterrupted, v1alpha1.OutcomeFailed)
				default:
					if tc.pod == "unstarted" {
						w.bindUnstarted(a.Name)
					}
					w.now = summonT0.Add(SummonStartWindow + time.Minute)
					w.pass()
					w.wantClosed(a.Name, summonoutcome.ReasonNeverStarted, v1alpha1.OutcomeState(a.Spec.Summon.OnUnreported))
				}
				closedAt := w.closed(a.Name).At.Time
				if !closedAt.Equal(w.now) {
					t.Fatalf("closed at %s, want %s", closedAt.Sub(summonT0), w.now.Sub(summonT0))
				}

				// The operator suspends it at once, and the lane waits.
				w.reconcile(a.Name)
				if live := w.get(a.Name); live.Spec.OperatingMode != v1alpha1.OperatingModeSuspended || live.Annotations[v1alpha1.AnnotationSuspendedBy] != SummonCloseSuspender {
					t.Fatalf("no operator suspend: %s by %q", live.Spec.OperatingMode, live.Annotations[v1alpha1.AnnotationSuspendedBy])
				}
				if tc.releaseAt == 0 {
					if w.pod(a.Name) != nil || !w.released(a.Name) {
						t.Fatalf("an unscheduled remote pod: present %v, released %v", w.pod(a.Name) != nil, w.released(a.Name))
					}
					w.pass()
					w.wantAdmitted("the pod is gone", map[string]bool{"b-1": true})
					return
				}
				step := func(after time.Duration, want bool) {
					t.Helper()
					w.now = closedAt.Add(after)
					w.reconcile(a.Name)
					if w.released(a.Name) != want {
						t.Fatalf("%s after the close: released=%v, want %v", after, w.released(a.Name), want)
					}
					w.pass()
					w.wantAdmitted(after.String()+" after the close", map[string]bool{"b-1": want})
				}
				if tc.endAt > 0 {
					step(tc.endAt-time.Second, false)
					w.now = closedAt.Add(tc.endAt)
					if mode.lane == v1alpha1.LaneRemediation {
						w.containersEnd(a.Name, corev1.PodFailed)
					} else if err := w.base.Delete(context.Background(), w.pod(a.Name)); err != nil {
						t.Fatal(err)
					}
					step(tc.endAt, true)
					if mode.lane == v1alpha1.LaneRemediation && !executorTerminalRecorded(w.get(a.Name).Status.PrivateHistory) {
						t.Fatal("released without the terminal record")
					}
					return
				}
				for _, after := range []time.Duration{time.Minute, 5*time.Minute - time.Second} {
					step(after, false)
				}
				if w.pod(a.Name) == nil {
					t.Fatal("the pod went in this world; the test needs it present")
				}
				if tc.releaseAt != never {
					step(tc.releaseAt, true)
					if w.pod(a.Name) == nil {
						t.Fatal("released with the pod gone; the test needs it present")
					}
					return
				}
				for _, after := range []time.Duration{5 * time.Minute, time.Hour} {
					step(after, false)
				}
				// Only the pod going frees an unfenced lane.
				if err := w.base.Delete(context.Background(), w.pod(a.Name)); err != nil {
					t.Fatal(err)
				}
				step(time.Hour+time.Minute, true)
			})
		}
	}
}

// The recheck of ruling C1 (coordinator answer op780-fix1-q-1): a fenced pod
// that cannot go is held by the two time bounds alone, and no event marks
// them, so Reconcile asks to run again by the later one: the time left, at
// least a second and at most a minute. Followed with no other event, from the
// never-started close (both bounds: 16m and 21m after admission), the
// release is written exactly at closed.at plus five minutes. A released
// session and an unfenced pod get no recheck from this step. For a task with
// a never-scheduled pod and a remote session whose pod is bound to a node
// that never starts it.
func TestSummonFencedReleaseRechecksAtTheLaterBound(t *testing.T) {
	for _, lane := range []v1alpha1.Lane{v1alpha1.LaneRemediation, v1alpha1.LaneUpgrade} {
		for _, image := range []string{fencedImage, unfencedImage} {
			fenced := image == fencedImage
			t.Run(string(lane)+"/"+map[bool]string{true: "fenced", false: "unfenced"}[fenced], func(t *testing.T) {
				ctx := context.Background()
				w := newSummonWorld(t, summonPolicyObject(callerA, 2, ptr.To[int32](1)))
				w.useAgentImage(image)
				a := summonedFor("a-1", callerA, lane, 2*time.Minute)
				b := summonedFor("b-1", callerA, lane, time.Minute)
				b.Spec.Summon.MaxHold = "24h"
				w.queue(a, b)
				w.pass()
				w.reconcile(a.Name)
				if lane == v1alpha1.LaneUpgrade {
					w.bindUnstarted(a.Name)
				}
				w.now = summonT0.Add(SummonStartWindow + time.Minute)
				w.pass()
				w.wantClosed(a.Name, summonoutcome.ReasonNeverStarted, v1alpha1.OutcomeState(a.Spec.Summon.OnUnreported))
				closedAt := w.closed(a.Name).At.Time
				bound := closedAt.Add(5 * time.Minute)
				req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "dev-agents", Name: a.Name}}

				// The step alone, once suspended: the time left, clamped.
				w.reconcile(a.Name)
				if w.get(a.Name).Spec.OperatingMode != v1alpha1.OperatingModeSuspended || w.pod(a.Name) == nil || w.released(a.Name) {
					t.Fatalf("not suspended and held with its pod: released %v", w.released(a.Name))
				}
				for _, tc := range []struct{ after, want time.Duration }{
					{time.Minute, time.Minute},
					{4*time.Minute + 30*time.Second, 30 * time.Second},
					{5*time.Minute - 500*time.Millisecond, time.Second},
					{time.Hour, 0},
				} {
					if !fenced && tc.want > 0 {
						tc.want = 0
					}
					if fenced && tc.after >= 5*time.Minute {
						continue // past the bound the step releases; checked below
					}
					w.now = closedAt.Add(tc.after)
					changed, recheck, err := w.r.reconcileSummonClosed(ctx, w.get(a.Name))
					if err != nil || changed || recheck != tc.want {
						t.Fatalf("%s after the close: changed=%v recheck=%s err=%v, want recheck %s", tc.after, changed, recheck, err, tc.want)
					}
				}
				if !fenced {
					if w.released(a.Name) {
						t.Fatal("an unfenced pod released on time alone")
					}
					return
				}

				// Reconcile followed by its own requeue and nothing else.
				w.now = closedAt
				for i := 0; ; i++ {
					if i > 400 {
						t.Fatalf("not released by %s after the close", w.now.Sub(closedAt))
					}
					res, err := w.r.Reconcile(ctx, req)
					if err != nil {
						t.Fatal(err)
					}
					if w.released(a.Name) {
						break
					}
					if res.RequeueAfter <= 0 || res.RequeueAfter > time.Minute {
						t.Fatalf("%s after the close, held: RequeueAfter %s", w.now.Sub(closedAt), res.RequeueAfter)
					}
					w.now = w.now.Add(res.RequeueAfter)
				}
				if !w.now.Equal(bound) || w.pod(a.Name) == nil {
					t.Fatalf("released %s after the close, want 5m0s, pod present %v", w.now.Sub(closedAt), w.pod(a.Name) != nil)
				}
				w.pass()
				w.wantAdmitted("released at the later bound", map[string]bool{"b-1": true})
				if changed, recheck, err := w.r.reconcileSummonClosed(ctx, w.get(a.Name)); err != nil || changed || recheck != 0 {
					t.Fatalf("a released session: changed=%v recheck=%s err=%v", changed, recheck, err)
				}
			})
		}
	}
}

// agentdSettings is an agentd pod's settings under home, with the provider
// CLI a script that leaves a marker if it ever runs.
func agentdSettings(t *testing.T, home string) (agentd.Settings, string) {
	t.Helper()
	env := map[string]string{
		"HOME": home, "CLAUDE_CONFIG_DIR": filepath.Join(home, ".claude"), "XDG_RUNTIME_DIR": filepath.Join(home, "run"),
		"AGENTD_CONFIG_DIR": filepath.Join(home, "cfg"), "AGENTD_PLAYWRIGHT_STAGING": filepath.Join(home, "image", "ms-playwright"),
		"AGENTD_BASHRC": filepath.Join(home, "image", "bashrc.sh"), "AGENTD_GH_TOKEN_FILE": filepath.Join(home, "creds", "gh_token"),
		"CLAUDE_CODE_OAUTH_TOKEN": "static-token",
	}
	s, err := agentd.LoadSettings(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	s.SystemBin = filepath.Join(home, "image", "bin")
	marker := filepath.Join(home, "provider-ran")
	cli := filepath.Join(home, "fake-claude")
	if err := os.WriteFile(cli, []byte("#!/bin/sh\n: > \""+marker+"\"\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	s.ClaudeBin = cli
	for _, dir := range []string{s.StateDir, s.WorkDir()} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return s, marker
}

// tmuxHeld is agentd's tmux as the test holds it: no agent session exists,
// and the run-agent tmux would start is recorded, not run.
type tmuxHeld struct{ launch string }

func (f *tmuxHeld) Run(_ context.Context, c agentd.Cmd) (agentd.Result, error) {
	switch {
	case len(c.Args) > 0 && c.Args[0] == "has-session":
		return agentd.Result{}, errors.New("can't find session")
	case len(c.Args) > 0 && c.Args[0] == "new-session":
		f.launch = c.Args[len(c.Args)-1]
	}
	return agentd.Result{}, nil
}

func (f *tmuxHeld) LookPath(name string) (string, error) { return "/fake/bin/" + name, nil }

// N2, the paused launch (DESIGN-064 5.9, fix round 2), both halves together.
// agentd writes a summoned session's first launch record before its start
// deadline, with the deadline in it, and tmux is to start run-agent: the
// path pauses there. The clock passes the start deadline; the leader's
// watchdog closes the session never-started, the operator suspends it and,
// its pod's agentd carrying the fence, releases it once the clock is past
// both the deadline plus one minute and the close plus five minutes (ruling
// C1; the remote session's unscheduled pod is deleted by the suspend and
// releases at once), and admits the next session in that lane. The paused
// path resumes: run-agent refuses, and no provider starts. For a task and a
// remote session.
//
// run-agent's own clock is the wall clock, which is past summonT0's start
// deadline for any run of this test after 2026-11-12T16:15Z.
func TestSummonPausedLaunchSpawnsNoProviderAfterRelease(t *testing.T) {
	for _, lane := range []v1alpha1.Lane{v1alpha1.LaneUpgrade, v1alpha1.LaneRemediation} {
		t.Run(string(lane), func(t *testing.T) {
			w := newSummonWorld(t, summonPolicyObject(callerA, 2, ptr.To[int32](1)))
			w.useAgentImage(fencedImage)
			a := summonedFor("a-1", callerA, lane, 2*time.Minute)
			b := summonedFor("b-1", callerA, lane, time.Minute)
			b.Spec.Summon.MaxHold = "24h" // b waits past both bounds without its own hold bound tripping
			w.queue(a, b)
			w.pass() // admits a-1 at summonT0 and starts the heartbeat window
			w.wantAdmitted("first", map[string]bool{"a-1": true, "b-1": false})
			w.reconcile(a.Name)
			pod := w.pod(a.Name)
			if pod == nil || !podCarriesStartFence(pod) {
				t.Fatalf("pod %+v", pod)
			}
			deadline := ""
			doc := ""
			for _, e := range pod.Spec.Containers[0].Env {
				switch e.Name {
				case protocol.StartDeadlineEnv:
					deadline = e.Value
				case protocol.SessionEnv:
					doc = e.Value
				}
			}
			if deadline != summonT0.Add(SummonStartWindow).Format(time.RFC3339) {
				t.Fatalf("the pod's start deadline %q", deadline)
			}
			sess, err := protocol.ParseSession([]byte(doc))
			if err != nil || sess.Caller != callerA {
				t.Fatalf("the pod's session document: %+v %v", sess, err)
			}

			// agentd's first launch, four minutes in: the record, then tmux.
			s, marker := agentdSettings(t, t.TempDir())
			s.StartDeadline = deadline
			ws := protocol.Workspace{Clone: s.ClonePath(sess.Repo), Worktree: s.WorktreePath(sess.Name), Branch: "agent/" + sess.Name}
			l, err := agentd.BuildLaunch(s, sess, ws, "boot-1", summonT0.Add(4*time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			if l.StartDeadline != deadline {
				t.Fatalf("the launch record's start deadline %q", l.StartDeadline)
			}
			tmux := &tmuxHeld{}
			if err := agentd.StartAgent(context.Background(), tmux, s, l, "/agentd"); err != nil {
				t.Fatal(err)
			}
			if tmux.launch == "" {
				t.Fatal("tmux was not asked to start run-agent")
			}

			// Paused. The operator: past the deadline plus one minute.
			w.now = summonT0.Add(SummonStartWindow + time.Minute)
			w.pass()
			w.wantClosed(a.Name, summonoutcome.ReasonNeverStarted, v1alpha1.OutcomeState(a.Spec.Summon.OnUnreported))
			w.reconcile(a.Name)
			if live := w.get(a.Name); live.Spec.OperatingMode != v1alpha1.OperatingModeSuspended {
				t.Fatalf("suspend %s", live.Spec.OperatingMode)
			}
			// Ruling C1: with its pod still present, the lane also waits for
			// the close plus five minutes. Move the clock past both bounds.
			w.now = w.closed(a.Name).At.Add(5 * time.Minute)
			w.reconcile(a.Name)
			if !w.released(a.Name) {
				t.Fatalf("not released past both bounds (pod present %v)", w.pod(a.Name) != nil)
			}
			w.pass()
			w.wantAdmitted("the lane went to the next", map[string]bool{"b-1": true})

			// Resumed: run-agent reaches its spawn.
			pane := &strings.Builder{}
			code := agentd.RunAgent(tmux.launch, pane, make(chan os.Signal), time.Second)
			if _, err := os.Stat(marker); err == nil || code == 0 {
				t.Fatalf("a provider started after its release (exit %d, marker %v)\n%s", code, err, pane)
			}
			if _, err := os.Stat(filepath.Join(s.StateDir, "start-refused.json")); err != nil || !strings.Contains(pane.String(), "the provider was not started") {
				t.Fatalf("not refused by the start fence: %v\n%s", err, pane)
			}
			if err := os.WriteFile(filepath.Join(s.StateDir, "boot.json"), []byte(`{"bootId":"boot-1","bootedAt":"`+summonT0.Format(time.RFC3339)+`","boot":"ready"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if st := agentd.CollectStatus(context.Background(), tmux, s, sess, time.Now()); st.Agent.State != protocol.AgentFailed {
				t.Fatalf("agentd reports %+v, want failed", st.Agent)
			}
			if w.podCreates(a.Name) != 1 {
				t.Fatalf("pods %v", w.pods)
			}
		})
	}
}

// Today's sessions (proof 7): an ordinary remote session gets no latch, no
// latch annotation and no gate; its pod is built as before and, once evicted,
// rebuilt as before.
func TestOrdinaryRemoteSessionBuildsAsToday(t *testing.T) {
	w := newSummonWorld(t)
	s := taskSession()
	s.Name, s.UID, s.Generation = "ordinary-remote", "uid-ordinary-remote", 1
	s.Spec.Mode, s.Spec.Prompt, s.Spec.Limits = v1alpha1.ModeRemote, "", nil
	w.add(s)
	w.reconcile(s.Name)
	pod := w.pod(s.Name)
	if pod == nil || w.podCreates(s.Name) != 1 {
		t.Fatalf("an ordinary remote session was not built: %v", w.pods)
	}
	if _, ok := pod.Annotations[summonExecutorAnnotation]; ok || w.get(s.Name).Status.Summon != nil {
		t.Fatal("an ordinary session got the summoned latch")
	}
	if err := w.base.Delete(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	w.reconcile(s.Name)
	if w.podCreates(s.Name) != 2 || w.pod(s.Name) == nil {
		t.Fatalf("an evicted ordinary remote session was not rebuilt as before: %v", w.pods)
	}
}

// stopRequested reports whether name's recorded executor has its stop: the
// intent stored for the recorded executor's UID and the pod with that UID
// carrying activeDeadlineSeconds (DESIGN-025 4).
func (w *summonWorld) stopRequested(name string) bool {
	w.t.Helper()
	pod := w.pod(name)
	h := w.get(name).Status.PrivateHistory
	return pod != nil && h != nil && h.LastExecutor != nil && h.StopRequest != nil &&
		h.StopRequest.PodUID == h.LastExecutor.PodUID && string(pod.UID) == h.LastExecutor.PodUID && pod.Spec.ActiveDeadlineSeconds != nil
}

// F2 of story 780 fix round 2 (coordinator rulings in PR 1100 comment
// 6839380816): the stop of a summoned task the operator closed outlives the
// release of its lane. Each task runs on the fenced image, is closed
// deadline by the leader's watchdog (its own timer did not stop it), and its
// pod keeps running, so only the start fence and the two time bounds
// release it (ruling C1).
//
//   - The first reconcile after the close comes only once both bounds have
//     passed: the operator suspends the session, releases it, the next
//     session of the lane is admitted, and a later reconcile still asks the
//     recorded executor to end.
//   - The stop request fails through the five minutes after the close: the
//     lane is released at five minutes all the same, and the stop, retried,
//     succeeds after the release.
//   - The owner's HOME-expiry hold still blocks the stop after the release,
//     until it is lifted.
//   - A session that reported its own outcome gets no stop, even suspended
//     by a person after its release: its pod stays for the post-mortem.
func TestSummonTaskStopOutlivesItsRelease(t *testing.T) {
	ctx := context.Background()
	// closedTask is a remediation task admitted and running on the fenced
	// image, closed deadline at 65 minutes, with b queued behind it.
	closedTask := func(t *testing.T, hooks *interceptor.Funcs) (*summonWorld, string, string, time.Time) {
		t.Helper()
		w := newSummonWorld(t, summonPolicyObject(callerA, 2, ptr.To[int32](1)))
		w.useAgentImage(fencedImage)
		if hooks != nil {
			w.r = w.leader(hooks)
		}
		a := summonedFor("a-1", callerA, v1alpha1.LaneRemediation, 2*time.Minute)
		b := summonedFor("b-1", callerA, v1alpha1.LaneRemediation, time.Minute)
		b.Spec.Summon.MaxHold = "24h"
		w.queue(a, b)
		w.pass()
		w.reconcile(a.Name)
		w.schedule(a.Name, nil)
		w.heartbeat(a.Name, protocol.AgentBusy)
		w.reconcile(a.Name)
		if h := w.get(a.Name).Status.PrivateHistory; h == nil || h.LastExecutor == nil || h.LastExecutor.PodUID != string(w.pod(a.Name).UID) {
			t.Fatalf("no recorded executor: %+v", h)
		}
		// 15m start allowance + the task's 40m timeout + 10m.
		w.now = summonT0.Add(65 * time.Minute)
		w.pass()
		w.wantClosed(a.Name, summonoutcome.ReasonDeadline, v1alpha1.OutcomeEscalated)
		return w, a.Name, b.Name, w.closed(a.Name).At.Time
	}
	req := func(name string) ctrl.Request {
		return ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "dev-agents", Name: name}}
	}

	t.Run("the first reconcile comes after both bounds", func(t *testing.T) {
		w, a, b, closedAt := closedTask(t, nil)
		w.now = closedAt.Add(6 * time.Minute)
		suspended := false
		for i := 0; !w.released(a); i++ {
			if i == 3 {
				t.Fatal("not released past both bounds")
			}
			if _, err := w.r.Reconcile(ctx, req(a)); err != nil {
				t.Fatal(err)
			}
			if s := w.get(a); s.Spec.OperatingMode == v1alpha1.OperatingModeSuspended && s.Annotations[v1alpha1.AnnotationSuspendedBy] == SummonCloseSuspender {
				suspended = true
			}
		}
		if !suspended || w.pod(a) == nil {
			t.Fatalf("released without the operator's suspend first (%v), or with the pod gone", suspended)
		}
		w.pass()
		w.wantAdmitted("a released", map[string]bool{b: true})
		for i := 0; !w.stopRequested(a); i++ {
			if i == 3 {
				t.Fatalf("no stop for the recorded executor after the release: %+v", w.get(a).Status.PrivateHistory.StopRequest)
			}
			if _, err := w.r.Reconcile(ctx, req(a)); err != nil {
				t.Fatal(err)
			}
		}
	})

	t.Run("the stop fails through five minutes and succeeds after the release", func(t *testing.T) {
		failing, patches := true, 0
		w, a, b, closedAt := closedTask(t, &interceptor.Funcs{Patch: func(ctx context.Context, c client.WithWatch, o client.Object, p client.Patch, opts ...client.PatchOption) error {
			if _, ok := o.(*corev1.Pod); ok {
				patches++
				if failing {
					return apierrors.NewServiceUnavailable("the API server is unavailable")
				}
			}
			return c.Patch(ctx, o, p, opts...)
		}})
		for after := time.Duration(0); after < 5*time.Minute; after += 30 * time.Second {
			w.now = closedAt.Add(after)
			before := patches
			w.reconcile(a)
			if w.released(a) || w.stopRequested(a) || patches == before {
				t.Fatalf("%s after the close: released %v, stop %v, stop patches %d then %d", after, w.released(a), w.stopRequested(a), before, patches)
			}
		}
		if w.get(a).Spec.OperatingMode != v1alpha1.OperatingModeSuspended {
			t.Fatal("not suspended")
		}
		w.now = closedAt.Add(5 * time.Minute)
		w.reconcile(a)
		if !w.released(a) || w.pod(a) == nil {
			t.Fatalf("at five minutes: released %v, pod present %v", w.released(a), w.pod(a) != nil)
		}
		w.pass()
		w.wantAdmitted("a released", map[string]bool{b: true})
		failing = false
		released := patches
		w.now = closedAt.Add(5*time.Minute + 30*time.Second)
		w.reconcile(a)
		if !w.stopRequested(a) || patches == released {
			t.Fatalf("the stop was not retried after the release: stop %v, patches %d then %d", w.stopRequested(a), released, patches)
		}
	})

	t.Run("the owner's HOME-expiry hold still blocks the stop after the release", func(t *testing.T) {
		w, a, _, closedAt := closedTask(t, nil)
		s := w.get(a)
		if s.Annotations == nil {
			s.Annotations = map[string]string{}
		}
		s.Annotations[v1alpha1.AnnotationHomeExpiry] = v1alpha1.HomeExpiryHold
		if err := w.base.Update(ctx, s); err != nil {
			t.Fatal(err)
		}
		w.now = closedAt.Add(6 * time.Minute)
		w.reconcile(a)
		w.reconcile(a)
		if !w.released(a) || w.stopRequested(a) || w.pod(a).Spec.ActiveDeadlineSeconds != nil {
			t.Fatalf("held: released %v, stop %v", w.released(a), w.stopRequested(a))
		}
		s = w.get(a)
		delete(s.Annotations, v1alpha1.AnnotationHomeExpiry)
		if err := w.base.Update(ctx, s); err != nil {
			t.Fatal(err)
		}
		w.reconcile(a)
		if !w.stopRequested(a) {
			t.Fatal("no stop once the hold was lifted")
		}
	})

	t.Run("a reported close gets no stop", func(t *testing.T) {
		w := newSummonWorld(t, summonPolicyObject(callerA, 2, ptr.To[int32](1)))
		w.useAgentImage(fencedImage)
		a := summonedFor("a-1", callerA, v1alpha1.LaneRemediation, 2*time.Minute)
		w.queue(a)
		w.pass()
		w.reconcile(a.Name)
		w.schedule(a.Name, nil)
		w.heartbeat(a.Name, protocol.AgentBusy)
		w.reconcile(a.Name)
		w.now = summonT0.Add(3 * time.Minute)
		w.close(a.Name, summonoutcome.Report)
		w.reconcile(a.Name)
		if !w.released(a.Name) {
			t.Fatal("a report did not release at once")
		}
		// A person suspends it after its release.
		s := w.get(a.Name)
		s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
		if s.Annotations == nil {
			s.Annotations = map[string]string{}
		}
		s.Annotations[v1alpha1.AnnotationSuspendedBy] = "human/dev-env-system/dev-env-human"
		if err := w.base.Update(ctx, s); err != nil {
			t.Fatal(err)
		}
		for _, after := range []time.Duration{0, 10 * time.Minute, time.Hour} {
			w.now = summonT0.Add(3*time.Minute + after)
			w.reconcile(a.Name)
			if pod := w.pod(a.Name); pod == nil || pod.Spec.ActiveDeadlineSeconds != nil || w.get(a.Name).Status.PrivateHistory.StopRequest != nil {
				t.Fatalf("%s after the report: a reported session's pod was stopped or went (present %v, stop intent %+v)", after, pod != nil, w.get(a.Name).Status.PrivateHistory.StopRequest)
			}
		}
	})
}
