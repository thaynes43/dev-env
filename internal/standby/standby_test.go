package standby

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

const sessNS, stateNS = "dev-agents", "dev-env-system"

type rig struct {
	t     *testing.T
	c     client.Client
	r     *Runnable
	now   time.Time
	made  int
	reqs  []apiv1.CreateSessionRequest
	fails bool
}

func newRig(t *testing.T, objs ...client.Object) *rig {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	g := &rig{t: t, now: time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)}
	g.c = fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).WithStatusSubresource(&v1alpha1.AgentSession{}).Build()
	g.r = &Runnable{
		Reader: g.c, Client: g.c, SessionNamespace: sessNS, StateNamespace: stateNS,
		Request: apiv1.CreateSessionRequest{Repo: "haynes-ops", Agent: "claude", Mode: "remote", Model: "claude-opus-5-5", Profile: "full"},
		Log:     logr.Discard(), Now: func() time.Time { return g.now },
		Create: func(ctx context.Context, req apiv1.CreateSessionRequest) (string, error) {
			if g.fails {
				return "", fmt.Errorf("refused")
			}
			g.made++
			g.reqs = append(g.reqs, req)
			name := fmt.Sprintf("haynes-ops-%d", g.made)
			return name, g.c.Create(ctx, standbySession(name))
		},
	}
	return g
}

func standbySession(name string) *v1alpha1.AgentSession {
	return &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Namespace: sessNS, Name: name,
		Labels: map[string]string{v1alpha1.LabelStandby: "true"}}}
}

func (g *rig) ensure() string {
	g.t.Helper()
	name, err := g.r.Ensure(context.Background())
	if err != nil {
		g.t.Fatal(err)
	}
	return name
}

// reap deletes every standby session, as the reaper does once its pod is done.
func (g *rig) reap() {
	g.t.Helper()
	var list v1alpha1.AgentSessionList
	if err := g.c.List(context.Background(), &list); err != nil {
		g.t.Fatal(err)
	}
	for i := range list.Items {
		if err := g.c.Delete(context.Background(), &list.Items[i]); err != nil {
			g.t.Fatal(err)
		}
	}
}

func TestCreatesOneWhenNone(t *testing.T) {
	g := newRig(t)
	if name := g.ensure(); name != "haynes-ops-1" {
		t.Fatalf("created %q", name)
	}
	if g.made != 1 || g.reqs[0].Mode != "remote" || g.reqs[0].Model != "claude-opus-5-5" || g.reqs[0].Repo != "haynes-ops" {
		t.Errorf("made %d, request %+v", g.made, g.reqs)
	}
	// The next pass finds it and creates nothing.
	if name := g.ensure(); name != "" || g.made != 1 {
		t.Errorf("a second pass made %q (%d in all)", name, g.made)
	}
	var cm corev1.ConfigMap
	if err := g.c.Get(context.Background(), client.ObjectKey{Namespace: stateNS, Name: DefaultStateName}, &cm); err != nil || cm.Data[stateKey] == "" {
		t.Errorf("the breaker state was not recorded: %v %+v", err, cm.Data)
	}
}

func TestExistingStandbyIsLeftAlone(t *testing.T) {
	suspended := standbySession("suspended")
	suspended.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
	suspended.Status.Phase = v1alpha1.PhaseSuspended
	// A real suspend leaves the agent exited; the session is still the standby.
	suspendedExited := standbySession("suspended-exited")
	suspendedExited.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
	suspendedExited.Status.Agent = &v1alpha1.AgentStatus{Status: protocol.AgentExited}
	suspendedPhase := standbySession("suspended-phase")
	suspendedPhase.Status.Phase = v1alpha1.PhaseSuspended
	suspendedPhase.Status.Agent = &v1alpha1.AgentStatus{Status: protocol.AgentInterrupted}
	idle := standbySession("idle")
	idle.Status.Phase = v1alpha1.PhaseIdle
	for _, s := range []*v1alpha1.AgentSession{standbySession("running"), idle, suspended, suspendedExited, suspendedPhase} {
		g := newRig(t, s)
		if name := g.ensure(); name != "" || g.made != 0 {
			t.Errorf("%s: created %q with a standby present", s.Name, name)
		}
	}
}

func TestGoneStandbyIsReplaced(t *testing.T) {
	archived := standbySession("archived")
	archived.Status.Phase = v1alpha1.PhaseArchived
	failed := standbySession("failed")
	failed.Status.Phase = v1alpha1.PhaseFailed
	// The agent ended on its own in the current pod: the heartbeat is newer than the pod.
	hb := metav1.NewTime(time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC))
	exited := standbySession("exited")
	exited.Status.Agent = &v1alpha1.AgentStatus{Status: protocol.AgentExited, LastHeartbeat: &hb}
	reaping := standbySession("reaping")
	reaping.Finalizers = []string{"test/keep"}
	for _, s := range []*v1alpha1.AgentSession{archived, failed, exited, reaping} {
		objs := []client.Object{s}
		if s == exited {
			objs = append(objs, sessionPod(s.Name, hb.Add(-time.Hour)))
		}
		g := newRig(t, objs...)
		if s == reaping {
			if err := g.c.Delete(context.Background(), s); err != nil {
				t.Fatal(err)
			}
		}
		if name := g.ensure(); name == "" {
			t.Errorf("%s: no replacement was created", s.Name)
		}
	}
}

// movingStandby is a standby the operator is moving onto a new template
// revision (drain): idle, agent stopped on purpose, pod deleted and recreated.
func movingStandby(name string, phase v1alpha1.SessionPhase, agent string, hb *metav1.Time) *v1alpha1.AgentSession {
	s := standbySession(name)
	s.Status.Phase = phase
	s.Status.Agent = &v1alpha1.AgentStatus{Status: agent, LastHeartbeat: hb}
	return s
}

func sessionPod(name string, created time.Time) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: sessNS, Name: name, CreationTimestamp: metav1.NewTime(created)}}
}

// TestStandbyMovingToANewRevisionIsLeftAlone is NB-10: at the template revision
// move the drain stops the idle standby's agent and recreates its pod, and the
// runnable created a second standby two seconds later. Each state the standby
// passes through must count as present.
func TestStandbyMovingToANewRevisionIsLeftAlone(t *testing.T) {
	t0 := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	before := metav1.NewTime(t0.Add(-time.Minute))
	cases := map[string][]client.Object{
		// Stop recorded, pod still there: the agent reports exited or interrupted.
		"draining-agent-exited":      {movingStandby("s", v1alpha1.PhaseDraining, protocol.AgentExited, &before), sessionPod("s", t0.Add(-time.Hour))},
		"draining-agent-interrupted": {movingStandby("s", v1alpha1.PhaseDraining, protocol.AgentInterrupted, &before)},
		// Old pod deleted or terminating, new one not placed: the old report remains.
		"pending-creating-exited":      {movingStandby("s", v1alpha1.PhasePending, protocol.AgentExited, &before)},
		"pending-starting-interrupted": {movingStandby("s", v1alpha1.PhasePending, protocol.AgentInterrupted, &before), sessionPod("s", t0)},
		// Old pod deleted, session still reads Running with the old report: a
		// missing pod cannot prove the agent ended.
		"running-pod-missing-exited": {movingStandby("s", v1alpha1.PhaseRunning, protocol.AgentExited, &before)},
		"idle-pod-missing-exited":    {movingStandby("s", v1alpha1.PhaseIdle, protocol.AgentInterrupted, &before)},
		"no-phase-pod-missing":       {movingStandby("s", "", protocol.AgentExited, &before)},
		// New pod is Ready (no readiness probe) but has sent no heartbeat yet.
		"running-new-pod-stale-report": {movingStandby("s", v1alpha1.PhaseRunning, protocol.AgentExited, &before), sessionPod("s", t0)},
		"running-new-pod-no-heartbeat": {movingStandby("s", v1alpha1.PhaseRunning, protocol.AgentExited, nil), sessionPod("s", t0)},
		// Back to normal.
		"running-new-pod-reported": {movingStandby("s", v1alpha1.PhaseRunning, protocol.AgentIdle, &before), sessionPod("s", t0.Add(-time.Hour))},
		// A user suspend or the idle timer: the same entry resumes.
		"user-suspended-exited": func() []client.Object {
			s := movingStandby("s", v1alpha1.PhaseSuspended, protocol.AgentExited, &before)
			s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
			return []client.Object{s}
		}(),
	}
	for name, objs := range cases {
		g := newRig(t, objs...)
		if got := g.ensure(); got != "" || g.made != 0 {
			t.Errorf("%s: created %q while the standby was moving", name, got)
		}
	}
}

// An agent that ended on its own in the current pod is still replaced.
func TestAgentEndedInTheCurrentPodIsReplaced(t *testing.T) {
	t0 := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	after := metav1.NewTime(t0.Add(time.Minute))
	for _, phase := range []v1alpha1.SessionPhase{v1alpha1.PhaseRunning, v1alpha1.PhaseIdle, ""} {
		g := newRig(t, movingStandby("s", phase, protocol.AgentExited, &after), sessionPod("s", t0))
		if got := g.ensure(); got == "" {
			t.Errorf("phase %q: no replacement after the agent exited in its current pod", phase)
		}
	}
}

// A standby reaped after the move (or at any time) is replaced, and the
// breaker still counts the creation.
func TestReapedStandbyAfterAMoveIsReplaced(t *testing.T) {
	g := newRig(t, movingStandby("s", v1alpha1.PhaseDraining, protocol.AgentExited, nil))
	if got := g.ensure(); got != "" {
		t.Fatalf("created %q mid-drain", got)
	}
	g.reap()
	if got := g.ensure(); got == "" || g.made != 1 {
		t.Errorf("no replacement after the reap (made %d)", g.made)
	}
}

func TestOtherSessionsAreNeverTouched(t *testing.T) {
	other := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Namespace: sessNS, Name: "dev-env-1019-080904"}}
	elsewhere := standbySession("standby-elsewhere")
	elsewhere.Namespace = "other"
	g := newRig(t, other, elsewhere)
	// Neither an unlabelled session nor a standby in another namespace counts.
	if name := g.ensure(); name == "" {
		t.Fatal("no standby was created")
	}
	for _, key := range []client.ObjectKey{{Namespace: sessNS, Name: other.Name}, {Namespace: "other", Name: elsewhere.Name}} {
		var got v1alpha1.AgentSession
		if err := g.c.Get(context.Background(), key, &got); err != nil || got.ResourceVersion != "999" {
			t.Errorf("%v changed or vanished: %v rv=%s", key, err, got.ResourceVersion)
		}
	}
}

func TestBreakerTripsAtThirdCreationAndRecovers(t *testing.T) {
	g := newRig(t)
	for i := 1; i <= 3; i++ {
		if name := g.ensure(); name == "" {
			t.Fatalf("creation %d refused", i)
		}
		g.reap()
		g.now = g.now.Add(5 * time.Minute)
	}
	// 15 minutes in, three creations on record: the fourth is refused, on every pass.
	for i := 0; i < 3; i++ {
		if name := g.ensure(); name != "" || g.made != 3 {
			t.Fatalf("the open breaker made %q (%d in all)", name, g.made)
		}
		g.now = g.now.Add(time.Minute)
	}
	// The first creation (t=0) leaves the 30-minute window at t=30.
	g.now = time.Date(2026, 10, 19, 12, 30, 0, 0, time.UTC)
	if name := g.ensure(); name == "" || g.made != 4 {
		t.Fatalf("the breaker did not recover after the window: %q (%d)", name, g.made)
	}
}

func TestBreakerStateSurvivesANewRunnable(t *testing.T) {
	g := newRig(t)
	for i := 0; i < 3; i++ {
		g.ensure()
		g.reap()
	}
	// An operator restart is a new Runnable on the same ConfigMap.
	restarted := *g.r
	restarted.tripped = false
	if name, err := restarted.Ensure(context.Background()); err != nil || name != "" || g.made != 3 {
		t.Errorf("a restarted runnable made %q (%d), err %v", name, g.made, err)
	}
}

func TestFailedCreateStillSpendsASlot(t *testing.T) {
	g := newRig(t)
	g.fails = true
	for i := 0; i < 3; i++ {
		if _, err := g.r.Ensure(context.Background()); err == nil {
			t.Fatal("expected the create error")
		}
	}
	g.fails = false
	if name := g.ensure(); name != "" || g.made != 0 {
		t.Errorf("a crash loop of failed creates was not stopped: %q", name)
	}
}

func TestCorruptBreakerStateFailsClosed(t *testing.T) {
	g := newRig(t, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: stateNS, Name: DefaultStateName},
		Data: map[string]string{stateKey: "not json"}})
	if name, err := g.r.Ensure(context.Background()); err == nil || name != "" || g.made != 0 {
		t.Errorf("corrupt state: created %q (%d), err %v", name, g.made, err)
	}
}

func TestLeaderOnly(t *testing.T) {
	if !(&Runnable{}).NeedLeaderElection() {
		t.Error("the standby must run in the leader only")
	}
}
