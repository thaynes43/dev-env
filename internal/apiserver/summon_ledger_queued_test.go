package apiserver

// OP777, item 741 comment 7233 (DESIGN-064 5.9 item 4): with queues a session
// can wait longer than a charge's window and its attempt hold, and the leader
// admits it by that charge. So a write's prune keeps the charge of a session
// that exists and is neither admitted nor closed, however old; once the
// session is admitted or closed, normal pruning applies.

import (
	"context"
	"net/http"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

func TestSummonQueuedSessionKeepsItsChargePast80Minutes(t *testing.T) {
	for _, state := range []string{"queued", "admitted", "closed"} {
		t.Run(state, func(t *testing.T) {
			// OP779: the leader's hold bound closes a remediation Queued past
			// its maxHold (20m in the test policy); this proof waits 81 minutes.
			p := ledgerPolicy(4, 6)
			p.Spec.Summon.Sessions[0].MaxHold = "24h"
			f := summonFixture(t, p)
			t0 := f.now
			wantStatus(t, summonPost(t, f, keyedRemediation("rem-responder-1", "alert-1")), http.StatusCreated)
			sess := f.session("rem-responder-1")
			x := sess.Spec.Summon.DispatchID
			at := metav1.NewTime(t0.Add(time.Minute))
			switch state {
			case "admitted":
				sess.Status.Summon = &v1alpha1.SummonStatus{AdmittedAt: &at}
			case "closed":
				sess.Status.Outcome = &v1alpha1.OutcomeStatus{State: v1alpha1.OutcomeEscalated, At: &at}
				sess.Status.Summon = &v1alpha1.SummonStatus{Closed: &v1alpha1.SummonClosedStatus{At: at, Source: "unreported", Reason: "not-charged", Released: true}}
			}
			if state != "queued" {
				if err := f.c.Status().Update(context.Background(), sess); err != nil {
					t.Fatal(err)
				}
			}
			// 80 minutes on: the charge is outside its 65 minute window and
			// its attempt mark is far past the 15 minute hold. Another create
			// of the same caller writes the entry, and that write prunes.
			f.now = t0.Add(80 * time.Minute)
			wantStatus(t, summonPost(t, f, keyedRemediation("rem-responder-2", "alert-2")), http.StatusCreated)
			ch, kept := f.ledger().charge(x)
			switch {
			case state == "queued" && !kept:
				t.Fatalf("the Queued session lost its charge: %+v", f.ledger().Charges)
			case state == "queued" && ch.Name != sess.Name:
				t.Fatalf("the kept charge binds %s, not %s", ch.Name, sess.Name)
			case state != "queued" && kept:
				t.Fatalf("a %s session's old charge was kept: %+v", state, f.ledger().Charges)
			}
			if state == "queued" {
				// The retained charge must actually fund the leader's decision,
				// including after the waiter is older than both ageing windows.
				f.now = t0.Add(81 * time.Minute)
				lead(t, f, leaderOver(t, f), sess.Name, "rem-responder-2")
				wantAdmittedAndBuilt(t, f, sess.Name)
				if st := f.session("rem-responder-2").Status.Summon; st != nil && st.AdmittedAt != nil {
					t.Fatal("younger waiter overtook the aged waiter")
				}
			}
		})
	}
}
