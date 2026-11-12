package apiserver

// OP779 (slice c of item 741; DESIGN-064 5.9 N3 and 6.6, ruling D1): nobody
// resumes a summoned session, and a reap of one records who reaped it.

import (
	"context"
	"net/http"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/controller"
	"github.com/thaynes43/dev-env/internal/summonoutcome"
)

// summonedLifecycleSession is a summoned remediation as the summon route
// makes it, in the given operating mode.
func summonedLifecycleSession(t *testing.T, f *fixture, name string, mode v1alpha1.OperatingMode) {
	t.Helper()
	f.summonLimit = true // stored as the summon route left it; its charge is not this test's
	s := &v1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sessionNS, UID: uuid.NewUUID(), Finalizers: []string{controller.Finalizer}},
		Spec: v1alpha1.AgentSessionSpec{Repo: "haynes-ops", Agent: v1alpha1.AgentClaude, Mode: v1alpha1.ModeTask, Model: "claude-opus-5-5", Prompt: "brief",
			Parent: "upgrade-agent/alert-responder", Caller: "upgrade-agent.alert-responder", Lane: v1alpha1.LaneRemediation, Profile: "ops",
			OperatingMode: mode,
			Limits:        &v1alpha1.SessionLimits{Timeout: &metav1.Duration{Duration: 40 * time.Minute}},
			Summon: &v1alpha1.SessionSummon{DispatchID: "d-" + name, Priority: v1alpha1.SummonPriorityUrgent, FallbackModel: "claude-opus-5",
				OnUnreported: v1alpha1.SummonOnUnreportedEscalated, MaxHold: "20m"}},
	}
	if err := f.c.Create(context.Background(), s); err != nil {
		t.Fatal(err)
	}
}

func liveSession(t *testing.T, f *fixture, name string) *v1alpha1.AgentSession {
	t.Helper()
	var s v1alpha1.AgentSession
	if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: sessionNS, Name: name}, &s); err != nil {
		t.Fatal(err)
	}
	return &s
}

// N3: a resume of a summoned session is refused for a person, a session, a
// trusted client and its summoning caller, whether it is suspended or not,
// and nothing changes. An ordinary session's resume is unchanged.
func TestResumeRefusesEverySummonedSession(t *testing.T) {
	f := summonFixture(t)
	sessionTok := f.sessionPod("worker", "default", 0)
	for _, mode := range []v1alpha1.OperatingMode{v1alpha1.OperatingModeSuspended, v1alpha1.OperatingModeRunning} {
		name := "rem-responder-running"
		if mode == v1alpha1.OperatingModeSuspended {
			name = "rem-responder-suspended"
		}
		summonedLifecycleSession(t, f, name, mode)
		for _, who := range []struct{ name, tok string }{{"person", tokHuman}, {"session", sessionTok}, {"client", tokClient}, {"caller", tokStranger}} {
			t.Run(string(mode)+"/"+who.name, func(t *testing.T) {
				before := liveSession(t, f, name)
				w := f.do(http.MethodPost, apiv1.SessionResumePath(name), who.tok, nil)
				if w.Code == http.StatusOK || w.Code == http.StatusAccepted {
					t.Fatalf("resume of a summoned session answered %d: %s", w.Code, w.Body.String())
				}
				want := http.StatusConflict
				if who.name == "caller" {
					want = http.StatusForbidden // a summoning caller has no lifecycle route (6.4)
				}
				if w.Code != want {
					t.Fatalf("code %d, want %d: %s", w.Code, want, w.Body.String())
				}
				after := liveSession(t, f, name)
				if after.ResourceVersion != before.ResourceVersion || after.Spec.OperatingMode != mode {
					t.Fatalf("a refused resume changed the session: %s -> %s", before.ResourceVersion, after.ResourceVersion)
				}
			})
		}
	}
	created := decode[apiv1.Session](t, f.do(http.MethodPost, apiv1.SessionsPath, tokHuman, task()))
	if w := f.do(http.MethodPost, apiv1.SessionSuspendPath(created.Name), tokHuman, nil); w.Code != http.StatusAccepted {
		t.Fatalf("suspend: %d", w.Code)
	}
	if w := f.do(http.MethodPost, apiv1.SessionResumePath(created.Name), tokHuman, nil); w.Code != http.StatusAccepted {
		t.Fatalf("an ordinary resume changed: %d %s", w.Code, w.Body.String())
	}
}

// Ruling D1: the reap route records who reaped a summoned session, before
// its delete, under the API's own name for the caller. An ordinary session's
// reap writes nothing new. A suspend already records its caller.
func TestReapRecordsWhoReapedASummonedSession(t *testing.T) {
	f := summonFixture(t)
	sessionTok := f.sessionPod("worker", "default", 0)
	for _, who := range []struct{ name, tok, want string }{
		{"rem-responder-person", tokHuman, "human/" + humanSA},
		{"rem-responder-session", sessionTok, "session/worker"},
		{"rem-responder-client", tokClient, "client/" + clientSA},
	} {
		summonedLifecycleSession(t, f, who.name, v1alpha1.OperatingModeRunning)
		if w := f.do(http.MethodDelete, apiv1.SessionPath(who.name), who.tok, nil); w.Code != http.StatusAccepted {
			t.Fatalf("reap %s: %d %s", who.name, w.Code, w.Body.String())
		}
		s := liveSession(t, f, who.name)
		if s.DeletionTimestamp.IsZero() || s.Annotations[v1alpha1.AnnotationReapedBy] != who.want {
			t.Fatalf("%s: deleting=%v reaped-by=%q, want %q", who.name, !s.DeletionTimestamp.IsZero(), s.Annotations[v1alpha1.AnnotationReapedBy], who.want)
		}
		// A second reap by someone else changes nothing: the first stop counts.
		if w := f.do(http.MethodDelete, apiv1.SessionPath(who.name), tokHuman, nil); w.Code != http.StatusAccepted {
			t.Fatalf("second reap: %d", w.Code)
		}
		if got := liveSession(t, f, who.name).Annotations[v1alpha1.AnnotationReapedBy]; got != who.want {
			t.Fatalf("a second reap rewrote reaped-by to %q", got)
		}
	}
	summonedLifecycleSession(t, f, "rem-responder-suspend", v1alpha1.OperatingModeRunning)
	if w := f.do(http.MethodPost, apiv1.SessionSuspendPath("rem-responder-suspend"), tokHuman, nil); w.Code != http.StatusAccepted {
		t.Fatalf("suspend: %d %s", w.Code, w.Body.String())
	}
	if got := liveSession(t, f, "rem-responder-suspend").Annotations[v1alpha1.AnnotationSuspendedBy]; got != "human/"+humanSA {
		t.Fatalf("suspended-by %q", got)
	}
	created := decode[apiv1.Session](t, f.do(http.MethodPost, apiv1.SessionsPath, tokHuman, task()))
	s := liveSession(t, f, created.Name)
	s.Finalizers = []string{controller.Finalizer}
	if err := f.c.Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if w := f.do(http.MethodDelete, apiv1.SessionPath(created.Name), tokHuman, nil); w.Code != http.StatusAccepted {
		t.Fatalf("ordinary reap: %d", w.Code)
	}
	if _, ok := liveSession(t, f, created.Name).Annotations[v1alpha1.AnnotationReapedBy]; ok {
		t.Fatal("an ordinary reap wrote reaped-by")
	}
}

// The other side of TestSummonQueuedSessionKeepsItsChargePast80Minutes
// (coordinator answer op779-q-2): the same remediation with its policy's
// 20m maxHold is closed hold-expired at 20 minutes by the leader, never
// admitted, and its charge is then released by the normal prune, not kept.
func TestHoldExpiredRemediationChargeIsPruned(t *testing.T) {
	f := summonFixture(t, ledgerPolicy(4, 6))
	t0 := f.now
	wantStatus(t, summonPost(t, f, keyedRemediation("rem-responder-1", "alert-1")), http.StatusCreated)
	sess := f.session("rem-responder-1")
	x := sess.Spec.Summon.DispatchID
	f.now = t0.Add(20 * time.Minute)
	lead(t, f, leaderOver(t, f), sess.Name)
	s := f.session(sess.Name)
	if c := s.Status.Summon; s.Status.Outcome == nil || s.Status.Outcome.State != v1alpha1.OutcomeEscalated || c == nil || c.Closed == nil ||
		c.Closed.Reason != summonoutcome.ReasonHoldExpired || !c.Closed.Released || c.AdmittedAt != nil || c.Escalation == nil {
		t.Fatalf("outcome %+v summon %+v, want closed escalated hold-expired, released, never admitted, its filing pending", s.Status.Outcome, c)
	}
	f.now = t0.Add(80 * time.Minute)
	wantStatus(t, summonPost(t, f, keyedRemediation("rem-responder-2", "alert-2")), http.StatusCreated)
	if _, kept := f.ledger().charge(x); kept {
		t.Fatalf("a hold-expired session's old charge was kept: %+v", f.ledger().Charges)
	}
}
