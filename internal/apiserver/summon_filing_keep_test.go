package apiserver

// Story 800, slice a of item 768 (DESIGN-064 5.9 "The create ledger" item 4,
// 6.6 step 2; coordinator rulings on item 768 comment 7702, not rulings by
// Tom): the create ledger keeps a Pending filing's charge. The platform's
// filing charges the dispatch id filing:<remediation UID> before its child
// exists, so while a blocked filing waits no Session carries that id, and the
// existing rule, which keeps an old charge only for an existing Session that
// is neither admitted nor closed, would drop it at the caller's next ledger
// write. These tests close a remediation escalated through
// summonoutcome.Write, the one terminal write that records its filing
// Pending, put the filing's charge in the ledger by hand, as story 768b's
// leader will, and drive the caller's creates on the fake clock.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/summonledger"
	"github.com/thaynes43/dev-env/internal/summonoutcome"
)

// filingTestID is the filing's dispatch id, written out here on purpose:
// story 768b's leader charges this form, so a change must fail a test.
func filingTestID(uid types.UID) string {
	return "filing:" + string(uid)
}

// escalateRemediation creates the caller's remediation through the API and
// closes it escalated by the one terminal write, which records its filing
// Pending (summonoutcome.Write). It returns the stored session.
func escalateRemediation(t *testing.T, f *fixture, name, key string) *v1alpha1.AgentSession {
	t.Helper()
	wantStatus(t, summonPost(t, f, keyedRemediation(name, key)), http.StatusCreated)
	changed, err := summonoutcome.Write(context.Background(), f.c, f.session(name), summonoutcome.Decision{
		State: v1alpha1.OutcomeEscalated, Note: "needs Tom", Source: summonoutcome.Report, Reason: "escalate",
	}, f.now)
	if err != nil || !changed {
		t.Fatalf("close %s escalated: changed=%v err=%v", name, changed, err)
	}
	s := f.session(name)
	if s.Status.Summon == nil || s.Status.Summon.Escalation == nil || s.Status.Summon.Escalation.State != "Pending" {
		t.Fatalf("%s has no Pending filing: %+v", name, s.Status.Summon)
	}
	return s
}

// filingCharge is the charge story 768b's leader commits for rem's filing
// (6.6 step 2): its id, bound to the remediation's escalation name, with the
// request hash of the caller's escalation entry for that name.
func filingCharge(rem *v1alpha1.AgentSession, at time.Time) summonledger.Charge {
	name := rem.Status.Summon.Escalation.Name
	return summonledger.Charge{
		ID: filingTestID(rem.UID), At: at, Attempt: at, Name: name,
		Hash: summonRequestHash(summonWire{Mode: "remote", Lane: "escalation", Name: name}, v1alpha1.SummonPriorityUrgent),
	}
}

// putLedgerCharge adds ch to the caller's entry in the ledger object.
func putLedgerCharge(t *testing.T, f *fixture, ch summonledger.Charge) {
	t.Helper()
	_, cm, err := readLedgerConfigMap(context.Background(), f.c, ledgerTestKey)
	if err != nil {
		t.Fatal(err)
	}
	raw, present := cm.Data[ledgerTestKey]
	e, err := summonledger.Decode(raw, present)
	if err != nil {
		t.Fatal(err)
	}
	e.Charges = append(e.Charges, ch)
	if e.Clock.Before(ch.At) {
		e.Clock = ch.At
	}
	if raw, err = summonledger.Encode(e); err != nil {
		t.Fatal(err)
	}
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data[ledgerTestKey] = raw
	if err := f.c.Update(context.Background(), cm); err != nil {
		t.Fatal(err)
	}
}

// setFiling replaces rem's filing record.
func setFiling(t *testing.T, f *fixture, name string, esc *v1alpha1.SummonEscalationStatus) {
	t.Helper()
	s := f.session(name)
	s.Status.Summon.Escalation = esc
	if err := f.c.Status().Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
}

// fileChild creates the filing's child as story 768b's leader will: the
// filing's charge's name, its id and its request hash. settle sets its
// summon status (admitted, closed or neither).
func fileChild(t *testing.T, f *fixture, ch summonledger.Charge, settle *v1alpha1.SummonStatus) {
	t.Helper()
	child := &v1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: ch.Name,
			Annotations: map[string]string{v1alpha1.AnnotationRequestHash: ch.Hash}},
		Spec: v1alpha1.AgentSessionSpec{
			Caller: ledgerTestKey, Lane: v1alpha1.LaneEscalation, Mode: v1alpha1.ModeRemote,
			Summon: &v1alpha1.SessionSummon{DispatchID: ch.ID, Priority: v1alpha1.SummonPriorityUrgent},
		},
	}
	if err := f.c.Create(context.Background(), child); err != nil {
		t.Fatal(err)
	}
	if settle != nil {
		s := f.session(ch.Name)
		s.Status.Summon = settle
		if err := f.c.Status().Update(context.Background(), s); err != nil {
			t.Fatal(err)
		}
	}
}

func wantCharge(t *testing.T, f *fixture, id string, kept bool) {
	t.Helper()
	if _, ok := f.ledger().charge(id); ok != kept {
		t.Fatalf("charge %s kept=%v, want %v: %+v", id, ok, kept, f.ledger().Charges)
	}
}

// Proof 4: a caller's create 80 minutes after a filing's charge keeps that
// charge while the remediation's filing is Pending, and prunes it once the
// filing is no longer Pending and no Session carries the id, or once the
// remediation's Session is gone. A child that carries the id and is neither
// admitted nor closed keeps it by the existing rule; admitted or closed, it
// does not.
func TestSummonLedgerKeepsAPendingFilingsCharge(t *testing.T) {
	// start is a remediation closed escalated at t0, Pending, with its
	// filing's charge at t0; the caller's create comes 80 minutes later, when
	// the charge is outside its window and its attempt mark past the hold.
	start := func(t *testing.T) (*fixture, *v1alpha1.AgentSession, summonledger.Charge) {
		f := summonFixture(t, ledgerPolicy(4, 6))
		t0 := f.now
		rem := escalateRemediation(t, f, "rem-responder-1", "alert-1")
		ch := filingCharge(rem, t0)
		putLedgerCharge(t, f, ch)
		f.now = t0.Add(80 * time.Minute)
		return f, rem, ch
	}

	t.Run("kept while Pending", func(t *testing.T) {
		f, _, ch := start(t)
		wantStatus(t, summonPost(t, f, keyedRemediation("rem-responder-2", "alert-2")), http.StatusCreated)
		wantCharge(t, f, ch.ID, true)
		// And at every later write while it stays Pending.
		f.now = f.now.Add(10 * time.Hour)
		wantStatus(t, summonPost(t, f, keyedRemediation("rem-responder-3", "alert-3")), http.StatusCreated)
		wantCharge(t, f, ch.ID, true)
	})

	// OP801 adds Filed and Abandoned. The fake store applies no enum, so it
	// also checks that a differently cased word never counts as Pending.
	// An empty state here stands for no filing record at all.
	for _, state := range []v1alpha1.SummonEscalationState{"", "Filed", "Abandoned", "pending"} {
		label := string(state)
		if label == "" {
			label = "no filing record"
		}
		t.Run("pruned when not Pending: "+label, func(t *testing.T) {
			f, rem, ch := start(t)
			var esc *v1alpha1.SummonEscalationStatus
			if state != "" {
				esc = &v1alpha1.SummonEscalationStatus{State: state, Name: ch.Name}
			}
			setFiling(t, f, rem.Name, esc)
			wantStatus(t, summonPost(t, f, keyedRemediation("rem-responder-2", "alert-2")), http.StatusCreated)
			wantCharge(t, f, ch.ID, false)
		})
	}

	t.Run("pruned when the remediation's Session is gone", func(t *testing.T) {
		f, rem, ch := start(t)
		if err := f.c.Delete(context.Background(), rem); err != nil {
			t.Fatal(err)
		}
		wantStatus(t, summonPost(t, f, keyedRemediation("rem-responder-2", "alert-2")), http.StatusCreated)
		wantCharge(t, f, ch.ID, false)
	})

	t.Run("a child neither admitted nor closed keeps it by the existing rule", func(t *testing.T) {
		for _, gone := range []bool{false, true} {
			t.Run(fmt.Sprintf("remediation gone %v", gone), func(t *testing.T) {
				f, rem, ch := start(t)
				fileChild(t, f, ch, nil)
				if gone {
					if err := f.c.Delete(context.Background(), rem); err != nil {
						t.Fatal(err)
					}
				} else {
					setFiling(t, f, rem.Name, &v1alpha1.SummonEscalationStatus{State: "Filed", Name: ch.Name})
				}
				wantStatus(t, summonPost(t, f, keyedRemediation("rem-responder-2", "alert-2")), http.StatusCreated)
				wantCharge(t, f, ch.ID, true)
			})
		}
	})

	t.Run("a child admitted or closed does not keep it", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			settle func(at metav1.Time) *v1alpha1.SummonStatus
		}{
			{"admitted", func(at metav1.Time) *v1alpha1.SummonStatus { return &v1alpha1.SummonStatus{AdmittedAt: &at} }},
			{"closed", func(at metav1.Time) *v1alpha1.SummonStatus {
				return &v1alpha1.SummonStatus{Closed: &v1alpha1.SummonClosedStatus{At: at, Source: summonoutcome.Unreported, Reason: "hold-expired", Released: true}}
			}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				f, rem, ch := start(t)
				fileChild(t, f, ch, tc.settle(metav1.NewTime(f.now)))
				setFiling(t, f, rem.Name, &v1alpha1.SummonEscalationStatus{State: "Filed", Name: ch.Name})
				wantStatus(t, summonPost(t, f, keyedRemediation("rem-responder-2", "alert-2")), http.StatusCreated)
				wantCharge(t, f, ch.ID, false)
			})
		}
	})
}

// filingWorld is one store for proof 5: at t0 the caller's remediation
// rem-responder-1 is created and closed escalated. With pending, its filing
// stays Pending and the filing's charge is put in the ledger at t0; without,
// the filing record is removed and no filing was ever charged. The clock is
// then 80 minutes on, past the charge's window and its attempt hold.
func filingWorld(t *testing.T, pending, lanes bool, policy *v1alpha1.CallerPolicy) (*fixture, string) {
	t.Helper()
	f := summonFixture(t, policy)
	f.srv.SummonedLanes = lanes
	t0 := f.now
	rem := escalateRemediation(t, f, "rem-responder-1", "alert-1")
	ch := filingCharge(rem, t0)
	if pending {
		putLedgerCharge(t, f, ch)
	} else {
		setFiling(t, f, rem.Name, nil)
	}
	f.now = t0.Add(80 * time.Minute)
	return f, ch.ID
}

// answer is what a create answered, minus what differs between two stores
// by construction (UIDs): the status, then the session's name and whether it
// is Queued, or the error's code and message.
func answer(t *testing.T, f *fixture, w *httptest.ResponseRecorder) string {
	t.Helper()
	if w.Code >= 300 {
		e := decode[apiv1.ErrorResponse](t, w).Error
		return fmt.Sprintf("%d %s %s", w.Code, e.Code, e.Message)
	}
	name := decode[apiv1.Session](t, w).Name
	return fmt.Sprintf("%d %s queued=%v", w.Code, name, summonoutcome.Queued(f.session(name), false))
}

// ledgerWithout is the caller's entry with the charge id left out, as text.
func ledgerWithout(f *fixture, id string) string {
	e := f.ledger()
	e.Charges = slices.DeleteFunc(e.Charges, func(c ledgerTestCharge) bool { return c.ID == id })
	return fmt.Sprintf("%+v", e)
}

// compareFilingWorlds runs steps over both stores of filingWorld and wants the
// same answers, the same ledger but for the filing's charge, which only the
// Pending store still holds, and the same steps' own checks.
func compareFilingWorlds(t *testing.T, lanes bool, policy func() *v1alpha1.CallerPolicy, steps func(t *testing.T, f *fixture) []string) {
	t.Helper()
	with, id := filingWorld(t, true, lanes, policy())
	without, _ := filingWorld(t, false, lanes, policy())
	got, want := steps(t, with), steps(t, without)
	t.Logf("answers with a Pending filing: %q", got)
	if !slices.Equal(got, want) {
		t.Fatalf("answers with a Pending filing differ from those without:\nwith    %q\nwithout %q", got, want)
	}
	if _, ok := with.ledger().charge(id); !ok {
		t.Fatalf("the Pending filing's charge %s was pruned: %+v", id, with.ledger().Charges)
	}
	if _, ok := without.ledger().charge(id); ok {
		t.Fatalf("a filing that was never charged has a charge: %+v", without.ledger().Charges)
	}
	if a, b := ledgerWithout(with, id), ledgerWithout(without, id); a != b {
		t.Fatalf("ledgers differ but for the filing's charge:\nwith    %s\nwithout %s", a, b)
	}
}

func keyedCuration(name, key string) map[string]any {
	b := curationBody(name)
	b["idempotencyKey"] = key
	return b
}

// Proof 5, the queues: with --enable-summoned-lanes, each of the caller's
// three queues fills and refuses at the same create with a Pending filing
// present as without it, the leader admits the same sessions, and a queue
// that the admission frees takes the same next create. The filing's charge
// hashes to the escalations' urgent queue, the one it would count in.
func TestSummonPendingFilingChangesNoQueueCount(t *testing.T) {
	compareFilingWorlds(t, true, func() *v1alpha1.CallerPolicy { return queuePolicy(1, 1, 1, 1, 20) }, func(t *testing.T, f *fixture) []string {
		var out []string
		// A second apart, so the leader's oldest-first order does not fall
		// back to the UIDs, which differ between the two stores.
		post := func(body map[string]any) {
			out = append(out, answer(t, f, summonPost(t, f, body)))
			f.now = f.now.Add(time.Second)
		}
		post(keyedEscalation("esc-a", "esc-a"))
		post(keyedEscalation("esc-b", "esc-b"))
		post(keyedRemediation("rem-responder-2", "alert-2"))
		post(keyedRemediation("rem-responder-3", "alert-3"))
		post(keyedCuration("wo-cigar-curate-1", "curate-1"))
		post(keyedCuration("wo-cigar-curate-2", "curate-2"))
		lead(t, f, leaderOver(t, f), "esc-a", "rem-responder-2", "wo-cigar-curate-1")
		out = append(out, fmt.Sprintf("admitted %v", admittedNames(f, "esc-a", "rem-responder-2", "wo-cigar-curate-1")))
		post(keyedEscalation("esc-b", "esc-b"))
		post(keyedRemediation("rem-responder-3", "alert-3"))
		post(keyedCuration("wo-cigar-curate-2", "curate-2"))
		return out
	})
}

// Proof 5, the hour: createsPerHour 2. The filing's charge is outside its
// window, so with a Pending filing present the hour admits two creates and
// refuses the third, counts the urgent refusal once, returns a repeated key,
// and frees the hour 65 minutes later, as without it.
func TestSummonPendingFilingChangesNoHourlyCount(t *testing.T) {
	compareFilingWorlds(t, false, func() *v1alpha1.CallerPolicy { return ledgerPolicy(4, 2) }, func(t *testing.T, f *fixture) []string {
		var out []string
		post := func(body map[string]any) {
			out = append(out, answer(t, f, summonPost(t, f, body)))
		}
		post(keyedRemediation("rem-responder-2", "alert-2"))
		post(keyedRemediation("rem-responder-3", "alert-3"))
		post(keyedRemediation("rem-responder-4", "alert-4"))
		post(keyedRemediation("rem-responder-2", "alert-2"))
		out = append(out, fmt.Sprintf("urgent refusals %d", f.ledger().UrgentRefusals))
		f.now = f.now.Add(66 * time.Minute)
		post(keyedRemediation("rem-responder-4", "alert-4"))
		post(keyedRemediation("rem-responder-5", "alert-5"))
		return out
	})
}
