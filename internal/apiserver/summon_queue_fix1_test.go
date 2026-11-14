package apiserver

// OP778 fix round 1: coordinator rulings in PR 1094 comment 6833388211.
// These schedules use synchronous request hooks and a fake clock, never sleeps.

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/controller"
	"github.com/thaynes43/dev-env/internal/summonledger"
	"github.com/thaynes43/dev-env/internal/summonoutcome"
)

func wantQueueNoResources(t *testing.T, f *fixture, name string) {
	t.Helper()
	for _, obj := range []client.Object{&corev1.Pod{}, &corev1.PersistentVolumeClaim{}} {
		n := name
		if _, ok := obj.(*corev1.PersistentVolumeClaim); ok {
			n = controller.HomeClaimName(name)
		}
		if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: n}, obj); !apierrors.IsNotFound(err) {
			t.Fatalf("%s: want no %T, got %v", name, obj, err)
		}
	}
}

func releaseQueueOccupant(t *testing.T, f *fixture, name string) {
	t.Helper()
	s := f.session(name)
	at := metav1.NewTime(f.now)
	s.Status.Outcome = &v1alpha1.OutcomeStatus{State: v1alpha1.OutcomeDone, At: &at}
	s.Status.Summon.Closed = &v1alpha1.SummonClosedStatus{At: at, Source: summonoutcome.Report, Reason: "done", Released: true}
	if err := f.c.Status().Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
}

// F1's stated limit: a fresh charge remains bound when a received Session
// create lands after the attempt hold. One extra Queued Session is possible;
// neither is not-charged, and admission still bounds the lane and pool.
func TestSummonQueueLateLandingStatedLimit(t *testing.T) {
	for _, queue := range []string{"queuedUrgent", "queued"} {
		t.Run(queue, func(t *testing.T) {
			body := func(n string) map[string]any { return keyedRemediation("rem-responder-"+n, "alert-"+n) }
			if queue == "queued" {
				body = func(n string) map[string]any {
					b := curationBody("wo-cigar-curate-" + n)
					b["idempotencyKey"] = "curation-" + n
					return b
				}
			}
			f := lanesFixture(t, queuePolicy(1, 1, 1, 1, 6))
			busy, first, second := curationBody("wo-cigar-curate-0"), body("1"), body("2")
			if queue == "queued" {
				busy = keyedRemediation("rem-responder-0", "alert-0")
			}
			busyName, firstName, secondName := busy["name"].(string), first["name"].(string), second["name"].(string)
			wantQueued(t, f, summonPost(t, f, busy), busyName)
			r := leaderOver(t, f)
			lead(t, f, r, busyName)
			wantAdmittedAndBuilt(t, f, busyName)
			t0 := f.now
			clock := newFakeSummonClock()
			b := replica(f, f.c)
			var bw *httptest.ResponseRecorder
			received := false
			ac := interceptor.NewClient(f.c, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				s, ok := obj.(*v1alpha1.AgentSession)
				if !ok || received {
					return c.Create(ctx, obj, opts...)
				}
				received = true
				if err := ctx.Err(); err != nil {
					t.Fatalf("create was not received inside its deadline: %v", err)
				}
				ch, ok := f.ledger().charge(s.Spec.Summon.DispatchID)
				if !ok || !ch.At.Equal(t0) || !ch.Attempt.Equal(t0) {
					t.Fatalf("the pending create has no fresh charge and mark: %+v", ch)
				}
				clock.Advance(16 * time.Minute)
				f.now = t0.Add(16 * time.Minute)
				wantNoSession(t, f, firstName)
				bw = post(t, b.Handler(), tokStranger, second)
				wantQueued(t, f, bw, secondName)
				// The server received the write before the pause. Like the
				// existing received-create hook, persist despite cancellation.
				return c.Create(context.Background(), obj, opts...)
			}})
			a := replica(f, ac)
			a.SummonClock = clock
			aw := post(t, a.Handler(), tokStranger, first)
			if !received {
				t.Fatal("the received-create schedule did not run")
			}
			wantQueued(t, f, aw, firstName)
			wantQueued(t, f, bw, secondName)
			lead(t, f, r, firstName, secondName)
			for _, n := range []string{firstName, secondName} {
				s := f.session(n)
				if !summonoutcome.Queued(s, false) || holdReason(f, n) != controller.SummonReasonPoolBusy {
					t.Fatalf("%s: want Queued and pool held, got %+v", n, s.Status)
				}
				if ch, ok := f.ledger().charge(s.Spec.Summon.DispatchID); !ok || ch.Name != n {
					t.Fatalf("%s lost its binding: %+v", n, ch)
				}
				wantQueueNoResources(t, f, n)
			}
			if got := admittedNames(f, busyName, firstName, secondName); len(got) != 1 || got[0] != busyName {
				t.Fatalf("occupied pool: admitted %v", got)
			}
			// After release the same leader admits just one of the two,
			// including on a second pass. The other still has no resources.
			releaseQueueOccupant(t, f, busyName)
			lead(t, f, r, firstName, secondName)
			lead(t, f, r, firstName, secondName)
			got := admittedNames(f, firstName, secondName)
			if len(got) != 1 {
				t.Fatalf("freed pool: admitted %v, want exactly one", got)
			}
			wantAdmittedAndBuilt(t, f, got[0])
			other := firstName
			if other == got[0] {
				other = secondName
			}
			if !summonoutcome.Queued(f.session(other), false) || holdReason(f, other) != controller.SummonReasonLaneBusy {
				t.Fatalf("%s should remain Queued: %+v", other, f.session(other).Status)
			}
			wantQueueNoResources(t, f, other)
		})
	}
}

func editCurationPriority(t *testing.T, f *fixture, priority v1alpha1.SummonPriority) {
	t.Helper()
	var p v1alpha1.CallerPolicy
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: ledgerTestNS, Name: ledgerTestKey}, &p); err != nil {
		t.Fatal(err)
	}
	summonEntry(p.Spec.Summon.Sessions, "task", "curation").Priority = priority
	if err := f.c.Update(context.Background(), &p); err != nil {
		t.Fatal(err)
	}
}

// Count persisted Queued Sessions by their immutable snapshots, independently
// of the API's charge classification and of the current policy.
func actualQueueCounts(f *fixture) map[summonQueue]int {
	counts := map[summonQueue]int{}
	for _, s := range f.summonedSessions() {
		if summonoutcome.Queued(&s, false) {
			counts[summonledger.QueueOf(s.Spec.Summon.Priority, s.Spec.Lane)]++
		}
	}
	return counts
}

func TestSummonQueuePriorityEditRefusesRetry(t *testing.T) {
	for _, from := range []v1alpha1.SummonPriority{v1alpha1.SummonPriorityNormal, v1alpha1.SummonPriorityUrgent} {
		t.Run(string(from), func(t *testing.T) {
			to := v1alpha1.SummonPriorityUrgent
			occupant := keyedRemediation("rem-responder-1", "alert-1")
			if from == v1alpha1.SummonPriorityUrgent {
				to, occupant = v1alpha1.SummonPriorityNormal, upgradeBody("wo-1")
			}
			p := queuePolicy(1, 1, 1, 1, 6)
			summonEntry(p.Spec.Summon.Sessions, "task", "curation").Priority = from
			f := lanesFixture(t, p)
			wantQueued(t, f, summonPost(t, f, occupant), occupant["name"].(string))
			body := curationBody("wo-cigar-curate-1")
			body["idempotencyKey"] = "curation-1"
			x := crashFirstCreate(t, f, body)
			before, counts := f.ledgerBytes(), actualQueueCounts(f)
			editCurationPriority(t, f, to)
			f.now = f.now.Add(30 * time.Second)
			w := summonPost(t, f, body)
			// Check every effect even on the unfixed head that returns 201.
			if w.Code != http.StatusConflict || decode[apiv1.ErrorResponse](t, w).Error.Code != apiv1.CodeConflict {
				t.Errorf("priority edit retry: status %d, want the different-request 409", w.Code)
			}
			if !bytes.Equal(before, f.ledgerBytes()) {
				t.Error("priority edit retry changed ledger bytes (charge or attempt mark)")
			}
			if got := actualQueueCounts(f); !reflect.DeepEqual(got, counts) {
				t.Errorf("priority edit retry changed actual Queued counts: %v -> %v", counts, got)
			}
			if got := sessionsOfID(f, x); len(got) != 0 || len(f.summonedSessions()) != 1 {
				t.Errorf("priority edit retry created a Session: %v", got)
			}
			// The existing reservation must still occupy its original queue
			// (or every queue when its original entry can no longer be told).
			probe := keyedRemediation("rem-responder-2", "alert-2")
			if from == v1alpha1.SummonPriorityNormal {
				probe = upgradeBody("wo-2")
			}
			wantQueueRefusal(t, summonPost(t, f, probe), from == v1alpha1.SummonPriorityUrgent, "concurrent")
		})
	}
}

func TestSummonQueueUnchangedPriorityRetry(t *testing.T) {
	f := lanesFixture(t, queuePolicy(1, 1, 1, 1, 6))
	body := curationBody("wo-cigar-curate-1")
	body["idempotencyKey"] = "curation-1"
	x := crashFirstCreate(t, f, body)
	f.now = f.now.Add(30 * time.Second)
	wantQueued(t, f, summonPost(t, f, body), "wo-cigar-curate-1")
	if len(f.ledger().Charges) != 1 || len(sessionsOfID(f, x)) != 1 {
		t.Fatal("the unchanged entry's retry did not reuse its charge")
	}
}

func TestSummonQueueSessionKeepsPrioritySnapshot(t *testing.T) {
	for _, from := range []v1alpha1.SummonPriority{v1alpha1.SummonPriorityNormal, v1alpha1.SummonPriorityUrgent} {
		t.Run(string(from), func(t *testing.T) {
			to := v1alpha1.SummonPriorityUrgent
			oldQueue, newQueue := upgradeBody("wo-1"), keyedRemediation("rem-responder-1", "alert-1")
			if from == v1alpha1.SummonPriorityUrgent {
				to, oldQueue, newQueue = v1alpha1.SummonPriorityNormal, newQueue, oldQueue
			}
			p := queuePolicy(1, 1, 1, 1, 6)
			summonEntry(p.Spec.Summon.Sessions, "task", "curation").Priority = from
			f := lanesFixture(t, p)
			wantQueued(t, f, summonPost(t, f, curationBody("wo-cigar-curate-1")), "wo-cigar-curate-1")
			editCurationPriority(t, f, to)
			wantQueueRefusal(t, summonPost(t, f, oldQueue), from == v1alpha1.SummonPriorityUrgent, "concurrent")
			wantQueued(t, f, summonPost(t, f, newQueue), newQueue["name"].(string))
			if f.session("wo-cigar-curate-1").Spec.Summon.Priority != from {
				t.Fatal("a policy edit changed the Session's snapshot")
			}
		})
	}
}

func TestSummonQueueUnreadableAdmissionRetention(t *testing.T) {
	for _, kept := range []bool{false, true} {
		name := "new charge"
		if kept {
			name = "kept charge"
		}
		t.Run(name, func(t *testing.T) {
			f := lanesFixture(t, queuePolicy(1, 1, 1, 1, 6))
			body := keyedRemediation("rem-responder-1", "alert-1")
			if kept {
				crashFirstCreate(t, f, body)
				f.now = f.now.Add(16 * time.Minute)
			}
			_, cm, err := readLedgerConfigMap(context.Background(), f.c, ledgerTestKey)
			if err != nil {
				t.Fatal(err)
			}
			if cm.Data == nil {
				cm.Data = map[string]string{}
			}
			cm.Data[summonledger.GrantsKey] = "not a grants document"
			if err := f.c.Update(context.Background(), cm); err != nil {
				t.Fatal(err)
			}
			before := f.ledgerBytes()
			e := wantError(t, summonPost(t, f, body), http.StatusServiceUnavailable, apiv1.CodeUnavailable)
			if !strings.Contains(e.Message, "admission grants that cannot be read") || strings.Contains(e.Message, summonRetention) != kept {
				t.Errorf("kept=%v: wrong admission fault retention: %q", kept, e.Message)
			}
			if !bytes.Equal(before, f.ledgerBytes()) || len(f.summonedSessions()) != 0 {
				t.Fatal("the admission fault wrote something")
			}
		})
	}
}

// A leader grant lands between the API's ledger read and its refusing write.
// Its conflict must rebuild the count, allowing the create into the freed queue.
func TestSummonQueueLeaderGrantConflictsWithCreate(t *testing.T) {
	f := lanesFixture(t, queuePolicy(1, 1, 1, 1, 6))
	wantQueued(t, f, summonPost(t, f, keyedRemediation("rem-responder-0", "alert-0")), "rem-responder-0")
	r := leaderOver(t, f)
	lead(t, f, r, "rem-responder-0")
	wantAdmittedAndBuilt(t, f, "rem-responder-0")
	wantQueued(t, f, summonPost(t, f, keyedRemediation("rem-responder-1", "alert-1")), "rem-responder-1")
	lead(t, f, r, "rem-responder-1")
	if !summonoutcome.Queued(f.session("rem-responder-1"), false) {
		t.Fatal("the grant candidate was not Queued behind the occupied pool")
	}
	releaseQueueOccupant(t, f, "rem-responder-0")
	granted := false
	ac, writes := interleave(f, func() {
		if err := r.SummonAdmissionPass(context.Background()); err != nil {
			t.Fatal(err)
		}
		_, cm, err := readLedgerConfigMap(context.Background(), f.c, ledgerTestKey)
		if err != nil {
			t.Fatal(err)
		}
		g, err := summonledger.DecodeGrants(cm.Data[summonledger.GrantsKey], true)
		if err != nil || !summonledger.Granted(g, f.session("rem-responder-1")) {
			t.Fatalf("leader did not commit the grant: %+v, %v", g, err)
		}
		granted = true
	})
	a := replica(f, ac)
	wantQueued(t, f, post(t, a.Handler(), tokStranger, keyedRemediation("rem-responder-2", "alert-2")), "rem-responder-2")
	wantLostTheWrite(t, writes)
	if !granted || len(writes.writes) != 3 || writes.writes[1] != nil || writes.writes[2] != nil {
		t.Fatalf("want conflict, rebuilt charge, mark; got %v (granted=%v)", writes.writes, granted)
	}
	if e := f.ledger(); len(e.Charges) != 3 || e.UrgentRefusals != 0 {
		t.Fatalf("the superseded refusal was written: %+v", e)
	}
	if got := actualQueueCounts(f); got[summonQueue{Urgent: true}] != 1 {
		t.Fatalf("the granted Session still counts as Queued: %v", got)
	}
}
