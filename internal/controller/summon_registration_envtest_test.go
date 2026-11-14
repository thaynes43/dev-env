package controller

// Story 794 fix round 1, ruling 5 (coordinator rulings in PR 1132 comment
// 6856616130; not a ruling by Tom): the registration step's own locked merge
// patches, sent to a real API server with the AgentSession CRD and its rules
// installed (envtest), for the agent-seen write and then a settlement, and
// read back. No operator runs here: the test calls the step itself, on a
// fixed clock, with the suite's client.

import (
	"context"
	"testing"
	"time"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// summonedRemoteEscalation makes a session a summoned remote escalation that
// the CRD accepts.
func summonedRemoteEscalation(s *v1alpha1.AgentSession) {
	s.Spec.Mode, s.Spec.Prompt, s.Spec.Limits = v1alpha1.ModeRemote, "", nil
	s.Spec.Caller, s.Spec.Lane, s.Spec.Profile = "alert-responder", v1alpha1.LaneEscalation, "ops"
	s.Spec.Parent = "alert-ns/alert-responder"
	s.Spec.Summon = &v1alpha1.SessionSummon{DispatchID: "d-" + s.Name, Priority: v1alpha1.SummonPriorityUrgent,
		FallbackModel: "claude-opus-5", OnUnreported: v1alpha1.SummonOnUnreportedFailed}
}

func TestSummonRegistrationPatchOnTheCRD(t *testing.T) {
	t0 := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		settle time.Duration
		state  string
		result v1alpha1.SummonRegistrationResult
		reason v1alpha1.SummonRegistrationReason
	}{
		{"registered", 2 * time.Minute, protocol.RemoteRegistered, v1alpha1.RegistrationRegistered, ""},
		{"unavailable", 6 * time.Minute, protocol.RemoteOffline, v1alpha1.RegistrationUnavailable, v1alpha1.RegistrationReasonOffline},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s := newSession(t, summonedRemoteEscalation)
			now := t0
			r := &Reconciler{Client: k8s, Now: func() time.Time { return now }}
			// beat stores one heartbeat as the API's route does, at now.
			beat := func(agent, state string) {
				t.Helper()
				live := session(t, s.Name)
				at := metav1.NewTime(now)
				live.Status.Agent = &v1alpha1.AgentStatus{Status: agent, LastHeartbeat: &at, LastActivity: &at}
				live.Status.RemoteControl = rcFor(s.Name, state)
				if live.Status.Summon == nil {
					admitted := metav1.NewTime(t0)
					live.Status.Summon = &v1alpha1.SummonStatus{AdmittedAt: &admitted}
				}
				if err := k8s.Status().Update(ctx, live); err != nil {
					t.Fatal(err)
				}
			}
			// step runs the step once on a fresh read and checks that the
			// object in memory is the stored one.
			step := func() *v1alpha1.AgentSession {
				t.Helper()
				live := session(t, s.Name)
				if !summonRegistrationDue(live) {
					t.Fatalf("not due: %+v", live.Status.Summon)
				}
				if err := r.summonRegistration(ctx, live); err != nil {
					t.Fatalf("the step's patch: %v", err)
				}
				stored := session(t, s.Name)
				if live.ResourceVersion != stored.ResourceVersion || !apiequality.Semantic.DeepEqual(live.Status, stored.Status) {
					t.Fatalf("the object in memory is not the stored one: %+v, stored %+v", live.Status.Summon, stored.Status.Summon)
				}
				return stored
			}

			now = t0.Add(time.Minute)
			beat(protocol.AgentBusy, protocol.RemoteStarting)
			stored := step()
			reg := stored.Status.Summon.Registration
			if reg == nil || reg.AgentSeenAt == nil || !reg.AgentSeenAt.Time.Equal(now) || reg.Result != "" || reg.At != nil || len(stored.Status.Summon.Pages) != 0 {
				t.Fatalf("after the agent-seen patch: %+v", stored.Status.Summon)
			}

			now = t0.Add(tc.settle)
			beat(protocol.AgentBusy, tc.state)
			stored = step()
			reg = stored.Status.Summon.Registration
			if reg == nil || reg.AgentSeenAt == nil || !reg.AgentSeenAt.Time.Equal(t0.Add(time.Minute)) ||
				reg.Result != tc.result || reg.Reason != tc.reason || reg.At == nil || !reg.At.Time.Equal(now) {
				t.Fatalf("after the settlement patch: %+v", reg)
			}
			if p := stored.Status.Summon.Pages; len(p) != 1 || p[0].Event != v1alpha1.PageEscalationStarted || !p[0].At.Time.Equal(now) {
				t.Fatalf("pages after the settlement patch: %+v", p)
			}
			if summonRegistrationDue(stored) {
				t.Fatal("a settled session is still due")
			}
		})
	}
}
