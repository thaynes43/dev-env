package broker

import (
	"context"
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/egress"
	"github.com/thaynes43/dev-env/internal/summonoutcome"
)

// The retained Pending filing must not postpone revocation, even while its
// executor's stop is held by the owner. Exercise both requester lookup paths.
func TestBrokerHeldReapEndsGrants(t *testing.T) {
	for _, typ := range []v1alpha1.GrantType{v1alpha1.GrantKube, v1alpha1.GrantEgress} {
		for _, bound := range []bool{false, true} {
			for _, ownerHold := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/uid-bound=%v/owner-hold=%v", typ, bound, ownerHold), func(t *testing.T) {
					ctx := context.Background()
					inst := &recorder{}
					b, g, pod, clk, mints := installationFixture(t, inst, 0)
					var s v1alpha1.AgentSession
					if err := b.Client.Get(ctx, client.ObjectKeyFromObject(pod), &s); err != nil {
						t.Fatal(err)
					}
					s.Spec.Caller = "alert-responder"
					s.Spec.Mode = v1alpha1.ModeTask
					s.Spec.Lane = v1alpha1.LaneRemediation
					s.Spec.Summon = &v1alpha1.SessionSummon{DispatchID: "rem-held-reap"}
					s.Status.Outcome = &v1alpha1.OutcomeStatus{State: v1alpha1.OutcomeEscalated}
					s.Status.Summon = &v1alpha1.SummonStatus{Closed: &v1alpha1.SummonClosedStatus{At: metav1.NewTime(clk.Now()), Source: summonoutcome.Report, Reason: "escalate", Released: true}, Escalation: &v1alpha1.SummonEscalationStatus{State: v1alpha1.SummonEscalationPending, Name: "esc-held-reap"}}
					s.Annotations = map[string]string{}
					if ownerHold {
						s.Annotations[v1alpha1.AnnotationHomeExpiry] = "hold"
					}
					if err := b.Client.Update(ctx, &s); err != nil {
						t.Fatal(err)
					}
					g.Spec.Type = typ
					if bound {
						g.Spec.Requester.SessionUID = s.UID
					}
					if typ == v1alpha1.GrantEgress {
						g.Spec.Kube = nil
						g.Spec.Egress = &v1alpha1.EgressGrant{CIDRs: []string{"192.0.2.8/32"}, Ports: tcp(8443)}
					}
					if err := b.Client.Update(ctx, g); err != nil {
						t.Fatal(err)
					}
					approve(g, "authentik/tom", time.Hour, metav1.NewTime(clk.Now()))
					if err := b.Client.Status().Update(ctx, g); err != nil {
						t.Fatal(err)
					}
					reconcileInstallation(t, b, g)
					if g.Status.Phase != v1alpha1.GrantActive {
						t.Fatal("an unreaped closed session lost its active grant")
					}
					expires := g.Status.ExpiresAt.DeepCopy()
					var resources []client.Object
					if typ == v1alpha1.GrantEgress {
						resources = []client.Object{egress.Object(g.Namespace, g.Name)}
					} else {
						resources = []client.Object{&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: g.Namespace, Name: g.Name}}, &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Namespace: "frontend", Name: g.Name}}}
					}
					for _, o := range resources {
						if err := b.APIReader.Get(ctx, client.ObjectKeyFromObject(o), o); err != nil {
							t.Fatal(err)
						}
					}
					before := s.DeepCopy()
					s.Annotations[v1alpha1.AnnotationReapedBy] = "session/worker"
					if err := b.Client.Update(ctx, &s); err != nil {
						t.Fatal(err)
					}
					// A real Session event must wake the grant now rather than at expiry.
					if !requestingSessionEndedUpdate(event.UpdateEvent{ObjectOld: before, ObjectNew: &s}) {
						t.Error("accepted reap did not enqueue its grants")
					}
					reconcileInstallation(t, b, g)
					if g.Status.Phase != v1alpha1.GrantReleased || g.Status.Message != "the session ended" || g.Status.EndedAt == nil || !g.Status.EndedAt.Time.Equal(clk.Now()) {
						t.Errorf("grant outlived accepted reap: phase=%s message=%q endedAt=%v", g.Status.Phase, g.Status.Message, g.Status.EndedAt)
					}
					if !g.Status.ExpiresAt.Equal(expires) {
						t.Fatal("reap changed the original expiry")
					}
					for _, o := range resources {
						if err := b.APIReader.Get(ctx, client.ObjectKeyFromObject(o), o); !apierrors.IsNotFound(err) {
							t.Errorf("accepted reap retained %T: %v", o, err)
						}
					}
					if err := b.APIReader.Get(ctx, client.ObjectKeyFromObject(&s), &s); err != nil || !s.DeletionTimestamp.IsZero() || s.Status.Summon.Escalation.State != v1alpha1.SummonEscalationPending {
						t.Fatal("grant cleanup deleted the retained filing record")
					}
					if ownerHold && s.Annotations[v1alpha1.AnnotationHomeExpiry] != "hold" {
						t.Fatal("grant cleanup removed the owner hold")
					}
					_, removed := inst.of(g.Name)
					if typ == v1alpha1.GrantKube && (*mints != 1 || len(removed) != 1 || removed[0] != pod.UID) {
						t.Fatal("reaped original did not retain identity for token cleanup")
					}
				})
			}
		}
	}
}

func TestBrokerReapEndRequiresClosedSummonedRequester(t *testing.T) {
	for _, which := range []string{"not-reaped", "ordinary", "not-closed", "not-terminal"} {
		t.Run(which, func(t *testing.T) {
			s := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{v1alpha1.AnnotationReapedBy: "session/worker"}}, Spec: v1alpha1.AgentSessionSpec{Caller: "alert-responder", Summon: &v1alpha1.SessionSummon{DispatchID: "rem-held-reap"}}, Status: v1alpha1.AgentSessionStatus{Outcome: &v1alpha1.OutcomeStatus{State: v1alpha1.OutcomeEscalated}, Summon: &v1alpha1.SummonStatus{Closed: &v1alpha1.SummonClosedStatus{}}}}
			switch which {
			case "not-reaped":
				delete(s.Annotations, v1alpha1.AnnotationReapedBy)
			case "ordinary":
				s.Spec.Caller = ""
				s.Spec.Summon = nil
			case "not-closed":
				s.Status.Summon.Closed = nil
			case "not-terminal":
				s.Status.Outcome.State = v1alpha1.OutcomePending
			}
			if requestingSessionEnded(s) {
				t.Fatal("unaffected requester treated as ended")
			}
			if requestingSessionEndedUpdate(event.UpdateEvent{ObjectOld: s.DeepCopy(), ObjectNew: s}) {
				t.Fatal("unaffected update enqueued ended requester")
			}
		})
	}
}
