package apiserver

// The proofs of story 778 (slice b of item 741; DESIGN-064 5.9 "The queues",
// 6.4 "Limits", 6.7 "Holds"): with --enable-summoned-lanes a create past its
// pool is Queued, and limits.queued and limits.queuedUrgent (per pool) hold
// across two API replicas within the attempt-hold boundary because the count
// is taken inside the create ledger's compare-and-set (ruling D2, fix round 1 F1).
// Two replicas are two Server values
// over one store; their writes are interleaved by hooks, not by timing.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
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

// queuePolicy is the test caller with a curation entry (task, priority
// normal) added and every limit set.
func queuePolicy(concurrent, escalations, queued, queuedUrgent, perHour int32) *v1alpha1.CallerPolicy {
	p := summonTestPolicy()
	l := &p.Spec.Summon.Limits
	l.Concurrent, l.CreatesPerHour = concurrent, perHour
	l.Escalations, l.Queued, l.QueuedUrgent = &escalations, &queued, &queuedUrgent
	turns := int32(120)
	p.Spec.Summon.Sessions = append(p.Spec.Summon.Sessions, v1alpha1.CallerPolicySummonSession{
		Kind: v1alpha1.SummonKindTask, Lane: v1alpha1.LaneCuration,
		Priority: v1alpha1.SummonPriorityNormal, NamePrefixes: []string{"wo-cigar-curate-"},
		Repo: "haynes-ops", Profile: "ops",
		Model:  v1alpha1.CallerPolicySummonModel{Default: "claude-opus-5-5", Fallback: "claude-opus-5"},
		Effort: "xhigh", Timeout: "40m", MaxTurns: &turns, MaxHold: "12h",
		OnUnreported: v1alpha1.SummonOnUnreportedFailed,
		Brief:        v1alpha1.CallerPolicySummonBrief{Template: "Curate.\n"},
	})
	return p
}

// lanesFixture is summonFixture with --enable-summoned-lanes on.
func lanesFixture(t *testing.T, objects ...client.Object) *fixture {
	t.Helper()
	f := summonFixture(t, objects...)
	f.srv.SummonedLanes = true
	return f
}

func keyedEscalation(name, key string) map[string]any {
	b := escalationBody(name)
	b["idempotencyKey"] = key
	return b
}

func curationBody(name string) map[string]any {
	return map[string]any{"name": name, "mode": "task", "lane": "curation"}
}

// admit marks a session admitted by the leader, so it holds its pool.
func admit(t *testing.T, f *fixture, name string) {
	t.Helper()
	s := f.session(name)
	at := metav1.NewTime(f.now)
	s.Status.Summon = &v1alpha1.SummonStatus{AdmittedAt: &at}
	if err := f.c.Status().Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
}

// wantQueued checks that a create returned 201 and its session is Queued:
// charged, bound to its name, not admitted, not closed, with no pod.
func wantQueued(t *testing.T, f *fixture, w *httptest.ResponseRecorder, name string) {
	t.Helper()
	wantStatus(t, w, http.StatusCreated)
	s := f.session(name)
	if !summonoutcome.Queued(s, false) {
		t.Fatalf("%s is not Queued: status %+v", name, s.Status.Summon)
	}
	if ch, ok := f.ledger().charge(s.Spec.Summon.DispatchID); !ok || ch.Name != name {
		t.Fatalf("%s is not charged under its name: %+v", name, f.ledger().Charges)
	}
	var pod corev1.Pod
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: name}, &pod); err == nil {
		t.Fatalf("%s has a pod", name)
	}
}

// summonRetention is the sentence a refusal carries when it keeps a charge
// this dispatch already held (summonChargedFailure).
const summonRetention = "there is no refund and normal pruning applies"

// wantQueueRefusal checks a 429 that names its queue. A refusal of a new
// charge spends nothing and so carries no retention sentence.
func wantQueueRefusal(t *testing.T, w *httptest.ResponseRecorder, urgent bool, pool string) {
	t.Helper()
	if e := wantQueueRefusalOf(t, w, urgent, pool); strings.Contains(e.Message, summonRetention) {
		t.Fatalf("a refusal of a new charge says it keeps one: %q", e.Message)
	}
}

func wantQueueRefusalOf(t *testing.T, w *httptest.ResponseRecorder, urgent bool, pool string) apiv1.Error {
	t.Helper()
	e := wantError(t, w, http.StatusTooManyRequests, apiv1.CodeLimitExceeded)
	want, limit := "summon queue full: ", "its limits.queued;"
	if urgent {
		want, limit = "summon urgent queue full: ", "in its "+pool+" pool, its limits.queuedUrgent;"
	}
	if !strings.HasPrefix(e.Message, want) || !strings.Contains(e.Message, limit) {
		t.Fatalf("message %q does not name the queue (%q, %q)", e.Message, want, limit)
	}
	return e
}

func wantNoSession(t *testing.T, f *fixture, name string) {
	t.Helper()
	for _, s := range f.summonedSessions() {
		if s.Name == name {
			t.Fatalf("session %s exists", name)
		}
	}
}

func admittedNames(f *fixture, names ...string) []string {
	var out []string
	for _, n := range names {
		if st := f.session(n).Status.Summon; st != nil && st.AdmittedAt != nil {
			out = append(out, n)
		}
	}
	return out
}

func holdReason(f *fixture, name string) string {
	c := meta.FindStatusCondition(f.session(name).Status.Conditions, controller.ConditionSummonAdmitted)
	if c == nil || c.Status != metav1.ConditionFalse {
		return ""
	}
	return c.Reason
}

// Proof 1 (5.9 "simultaneous API creates", 6.7 "two API creates at once"):
// two creates into a pool of one through two replicas, B's whole create run
// inside A's first ledger write. Both get 201; the leader admits one and the
// other stays Queued, by its lane or by its pool; never two admitted, also
// after another pass.
func TestSummonQueueTwoReplicasIntoAPoolOfOne(t *testing.T) {
	cases := []struct {
		name          string
		first, second map[string]any
		names         []string
		held          string
	}{
		{"one lane", keyedRemediation("rem-responder-1", "alert-1"), keyedRemediation("rem-responder-2", "alert-2"),
			[]string{"rem-responder-1", "rem-responder-2"}, controller.SummonReasonLaneBusy},
		{"one pool", keyedRemediation("rem-responder-1", "alert-1"), curationBody("wo-cigar-curate-1"),
			[]string{"rem-responder-1", "wo-cigar-curate-1"}, controller.SummonReasonPoolBusy},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := lanesFixture(t, queuePolicy(1, 1, 4, 4, 6))
			b := replica(f, f.c)
			var bw *httptest.ResponseRecorder
			ac, writes := interleave(f, func() { bw = post(t, b.Handler(), tokStranger, tc.second) })
			a := replica(f, ac)
			aw := post(t, a.Handler(), tokStranger, tc.first)
			wantStatus(t, bw, http.StatusCreated)
			wantStatus(t, aw, http.StatusCreated)
			wantLostTheWrite(t, writes)
			r := leaderOver(t, f)
			lead(t, f, r, tc.names...)
			lead(t, f, r, tc.names...)
			got := admittedNames(f, tc.names...)
			if len(got) != 1 {
				t.Fatalf("admitted %v, want exactly one", got)
			}
			wantAdmittedAndBuilt(t, f, got[0])
			other := tc.names[0]
			if other == got[0] {
				other = tc.names[1]
			}
			if !summonoutcome.Queued(f.session(other), false) || holdReason(f, other) != tc.held {
				t.Fatalf("%s: summon %+v hold %q, want Queued and held %s", other, f.session(other).Status.Summon, holdReason(f, other), tc.held)
			}
			var pod corev1.Pod
			if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: other}, &pod); err == nil {
				t.Fatalf("the Queued %s has a pod", other)
			}
		})
	}
}

// Proof 2: with the lanes on a create past its pool is Queued (201); with
// them off it is refused with today's 429 at the pool.
func TestSummonQueuePastAPool(t *testing.T) {
	t.Run("lanes on", func(t *testing.T) {
		f := lanesFixture(t, queuePolicy(1, 1, 4, 4, 6))
		wantStatus(t, summonPost(t, f, keyedRemediation("rem-responder-1", "alert-1")), http.StatusCreated)
		wantStatus(t, summonPost(t, f, keyedEscalation("esc-1", "esc-1")), http.StatusCreated)
		admit(t, f, "rem-responder-1")
		admit(t, f, "esc-1")
		wantQueued(t, f, summonPost(t, f, keyedRemediation("rem-responder-2", "alert-2")), "rem-responder-2")
		wantQueued(t, f, summonPost(t, f, curationBody("wo-cigar-curate-1")), "wo-cigar-curate-1")
		wantQueued(t, f, summonPost(t, f, keyedEscalation("esc-2", "esc-2")), "esc-2")
		if e := f.ledger(); e.UrgentRefusals != 0 || len(e.Charges) != 5 {
			t.Fatalf("ledger %+v", e)
		}
	})
	t.Run("lanes off", func(t *testing.T) {
		f := summonFixture(t, queuePolicy(1, 1, 4, 4, 6))
		wantStatus(t, summonPost(t, f, keyedRemediation("rem-responder-1", "alert-1")), http.StatusCreated)
		admit(t, f, "rem-responder-1")
		e := wantError(t, summonPost(t, f, keyedRemediation("rem-responder-2", "alert-2")), http.StatusTooManyRequests, apiv1.CodeLimitExceeded)
		if !strings.Contains(e.Message, "unfinished summoned sessions in the concurrent pool") {
			t.Fatalf("message %q is not the pool's", e.Message)
		}
		if l := f.ledger(); l.UrgentRefusals != 1 || len(l.Charges) != 1 {
			t.Fatalf("ledger %+v", l)
		}
		wantNoSession(t, f, "rem-responder-2")
	})
}

// Proof 3: past limits.queued, 429 naming the queue, no charge, no Session
// and no write; normal and bulk share it whatever the lane. Past
// limits.queuedUrgent in its pool, 429 naming the queue and its pool,
// counted once per refusal, no charge and no Session.
func TestSummonQueueRefusals(t *testing.T) {
	for _, priority := range []v1alpha1.SummonPriority{v1alpha1.SummonPriorityNormal, v1alpha1.SummonPriorityBulk} {
		t.Run("queued/"+string(priority), func(t *testing.T) {
			p := queuePolicy(1, 1, 1, 4, 6)
			summonEntry(p.Spec.Summon.Sessions, "task", "curation").Priority = priority
			f := lanesFixture(t, p)
			wantQueued(t, f, summonPost(t, f, upgradeBody("wo-1")), "wo-1")
			version := f.ledgerVersion()
			wantQueueRefusal(t, summonPost(t, f, upgradeBody("wo-2")), false, "")
			wantQueueRefusal(t, summonPost(t, f, curationBody("wo-cigar-curate-1")), false, "")
			if f.ledgerVersion() != version || len(f.ledger().Charges) != 1 || f.ledger().UrgentRefusals != 0 {
				t.Fatalf("a refused normal or bulk create wrote the ledger: %+v", f.ledger())
			}
			wantNoSession(t, f, "wo-2")
			wantNoSession(t, f, "wo-cigar-curate-1")
			// The urgent queues are apart: a remediation is still Queued.
			wantQueued(t, f, summonPost(t, f, keyedRemediation("rem-responder-1", "alert-1")), "rem-responder-1")
		})
	}
	t.Run("queuedUrgent", func(t *testing.T) {
		f := lanesFixture(t, queuePolicy(1, 1, 4, 1, 6))
		wantQueued(t, f, summonPost(t, f, keyedRemediation("rem-responder-1", "alert-1")), "rem-responder-1")
		for i := 1; i <= 2; i++ {
			f.now = f.now.Add(time.Minute)
			wantQueueRefusal(t, summonPost(t, f, keyedRemediation("rem-responder-2", "alert-2")), true, "concurrent")
			e := f.ledger()
			if e.UrgentRefusals != int64(i) || e.LastUrgentRefusal == nil || !e.LastUrgentRefusal.Equal(f.now) || len(e.Charges) != 1 {
				t.Fatalf("refusal %d: %+v", i, e)
			}
		}
		wantNoSession(t, f, "rem-responder-2")
		wantQueued(t, f, summonPost(t, f, keyedEscalation("esc-1", "esc-1")), "esc-1")
		wantQueueRefusal(t, summonPost(t, f, keyedEscalation("esc-2", "esc-2")), true, "escalations")
		if e := f.ledger(); e.UrgentRefusals != 3 || len(e.Charges) != 2 {
			t.Fatalf("ledger %+v", e)
		}
		wantNoSession(t, f, "esc-2")
	})
}

// Proof 4: two replicas race for a queue's last place. B's create runs inside
// A's first ledger write; A's write conflicts, A decides again on B's
// charge and is refused. One Session, one charge; an urgent refusal is
// counted once in A's second write, a normal one writes nothing.
func TestSummonQueueReplicasRaceForTheLastPlace(t *testing.T) {
	t.Run("urgent", func(t *testing.T) {
		f := lanesFixture(t, queuePolicy(1, 1, 4, 2, 6))
		wantQueued(t, f, summonPost(t, f, keyedRemediation("rem-responder-0", "alert-0")), "rem-responder-0")
		b := replica(f, f.c)
		var bw *httptest.ResponseRecorder
		ac, writes := interleave(f, func() {
			bw = post(t, b.Handler(), tokStranger, keyedRemediation("rem-responder-2", "alert-2"))
		})
		a := replica(f, ac)
		aw := post(t, a.Handler(), tokStranger, keyedRemediation("rem-responder-1", "alert-1"))
		wantQueued(t, f, bw, "rem-responder-2")
		wantQueueRefusal(t, aw, true, "concurrent")
		wantLostTheWrite(t, writes)
		if len(writes.writes) != 2 || writes.writes[1] != nil {
			t.Fatalf("replica A's ledger writes %v, want the conflict and the counted refusal", writes.writes)
		}
		wantNoSession(t, f, "rem-responder-1")
		if e, s := f.ledger(), f.summonedSessions(); len(s) != 2 || len(e.Charges) != 2 || e.UrgentRefusals != 1 {
			t.Fatalf("sessions %d ledger %+v", len(s), e)
		}
	})
	t.Run("normal", func(t *testing.T) {
		f := lanesFixture(t, queuePolicy(1, 1, 2, 4, 6))
		wantQueued(t, f, summonPost(t, f, upgradeBody("wo-0")), "wo-0")
		b := replica(f, f.c)
		var bw *httptest.ResponseRecorder
		ac, writes := interleave(f, func() { bw = post(t, b.Handler(), tokStranger, upgradeBody("wo-2")) })
		a := replica(f, ac)
		aw := post(t, a.Handler(), tokStranger, upgradeBody("wo-1"))
		wantQueued(t, f, bw, "wo-2")
		wantQueueRefusal(t, aw, false, "")
		wantLostTheWrite(t, writes)
		if len(writes.writes) != 1 {
			t.Fatalf("replica A's ledger writes %v, want only the conflict", writes.writes)
		}
		wantNoSession(t, f, "wo-1")
		if e, s := f.ledger(), f.summonedSessions(); len(s) != 2 || len(e.Charges) != 2 || e.UrgentRefusals != 0 {
			t.Fatalf("sessions %d ledger %+v", len(s), e)
		}
	})
}

// Proof 5, R1: with createsPerHour 1, queuedUrgent 4 and the escalation pool
// full, a second distinct urgent key in one hour is refused, charges nothing,
// creates no Session and is counted once. With both limits full, and with the
// queue alone full, each refusal is counted once.
func TestSummonQueueR1(t *testing.T) {
	f := lanesFixture(t, queuePolicy(1, 1, 4, 4, 1))
	wantStatus(t, summonPost(t, f, keyedEscalation("esc-0", "esc-0")), http.StatusCreated)
	admit(t, f, "esc-0")
	f.now = f.now.Add(time.Minute)
	e := wantError(t, summonPost(t, f, keyedEscalation("esc-1", "esc-1")), http.StatusTooManyRequests, apiv1.CodeLimitExceeded)
	if !strings.Contains(e.Message, "createsPerHour") {
		t.Fatalf("message %q", e.Message)
	}
	if l := f.ledger(); l.UrgentRefusals != 1 || len(l.Charges) != 1 {
		t.Fatalf("ledger %+v", l)
	}
	wantNoSession(t, f, "esc-1")

	// One charge an hour fills the queue: four Queued escalations.
	for _, n := range []string{"esc-1", "esc-2", "esc-3", "esc-4"} {
		f.now = f.now.Add(66 * time.Minute)
		wantQueued(t, f, summonPost(t, f, keyedEscalation(n, n)), n)
	}
	// Both full: the hour (esc-4's charge) and the queue. Counted once.
	f.now = f.now.Add(time.Minute)
	wantError(t, summonPost(t, f, keyedEscalation("esc-5", "esc-5")), http.StatusTooManyRequests, apiv1.CodeLimitExceeded)
	if l := f.ledger(); l.UrgentRefusals != 2 {
		t.Fatalf("both full: urgent refusals %d, want 2", l.UrgentRefusals)
	}
	// The queue alone full. Counted once.
	f.now = f.now.Add(66 * time.Minute)
	wantQueueRefusal(t, summonPost(t, f, keyedEscalation("esc-6", "esc-6")), true, "escalations")
	if l := f.ledger(); l.UrgentRefusals != 3 {
		t.Fatalf("queue full: urgent refusals %d, want 3", l.UrgentRefusals)
	}
	wantNoSession(t, f, "esc-5")
	wantNoSession(t, f, "esc-6")
}

// Proof 6, N6: distinct urgent keys every ten minutes for five hours, with
// the escalation pool full, never give more than queuedUrgent Queued
// escalations, and every refusal is counted.
func TestSummonQueueN6(t *testing.T) {
	f := lanesFixture(t, queuePolicy(1, 1, 4, 4, 6))
	wantStatus(t, summonPost(t, f, keyedEscalation("esc-0", "esc-0")), http.StatusCreated)
	admit(t, f, "esc-0")
	queued := func() int {
		n := 0
		sessions := f.summonedSessions()
		for i := range sessions {
			if sessions[i].Spec.Lane == v1alpha1.LaneEscalation && summonoutcome.Queued(&sessions[i], false) {
				n++
			}
		}
		return n
	}
	refused := int64(0)
	for i := 1; i <= 30; i++ {
		f.now = f.now.Add(10 * time.Minute)
		n := "esc-" + itoa(i)
		w := summonPost(t, f, keyedEscalation(n, n))
		switch w.Code {
		case http.StatusCreated:
		case http.StatusTooManyRequests:
			refused++
		default:
			t.Fatalf("create %d: %d %s", i, w.Code, w.Body.String())
		}
		if q := queued(); q > 4 {
			t.Fatalf("after create %d: %d Queued escalations, more than queuedUrgent", i, q)
		}
		if got := f.ledger().UrgentRefusals; got != refused {
			t.Fatalf("after create %d: urgent refusals %d, want %d", i, got, refused)
		}
	}
	if q := queued(); q != 4 || refused != 26 {
		t.Fatalf("%d Queued, %d refused", q, refused)
	}
}

// Proof 7: the two urgent queues are apart. A full escalations queue never
// refuses a remediation, and the reverse, whether the queue is full of
// Queued sessions or of creates still in flight (a charge with no Session
// inside the hold).
func TestSummonQueueEscalationsAndRemediationsApart(t *testing.T) {
	t.Run("escalations full of sessions", func(t *testing.T) {
		f := lanesFixture(t, queuePolicy(1, 1, 4, 1, 6))
		wantQueued(t, f, summonPost(t, f, keyedEscalation("esc-1", "esc-1")), "esc-1")
		wantQueueRefusal(t, summonPost(t, f, keyedEscalation("esc-2", "esc-2")), true, "escalations")
		wantQueued(t, f, summonPost(t, f, keyedRemediation("rem-responder-1", "alert-1")), "rem-responder-1")
	})
	t.Run("remediations full of sessions", func(t *testing.T) {
		f := lanesFixture(t, queuePolicy(1, 1, 4, 1, 6))
		wantQueued(t, f, summonPost(t, f, keyedRemediation("rem-responder-1", "alert-1")), "rem-responder-1")
		wantQueueRefusal(t, summonPost(t, f, keyedRemediation("rem-responder-2", "alert-2")), true, "concurrent")
		wantQueued(t, f, summonPost(t, f, keyedEscalation("esc-1", "esc-1")), "esc-1")
	})
	t.Run("escalation in flight", func(t *testing.T) {
		f := lanesFixture(t, queuePolicy(1, 1, 4, 1, 6))
		crashFirstCreate(t, f, keyedEscalation("esc-1", "esc-1"))
		f.now = f.now.Add(time.Minute)
		wantQueued(t, f, summonPost(t, f, keyedRemediation("rem-responder-1", "alert-1")), "rem-responder-1")
		wantQueueRefusal(t, summonPost(t, f, keyedEscalation("esc-2", "esc-2")), true, "escalations")
	})
	t.Run("remediation in flight", func(t *testing.T) {
		f := lanesFixture(t, queuePolicy(1, 1, 4, 1, 6))
		crashFirstCreate(t, f, keyedRemediation("rem-responder-1", "alert-1"))
		f.now = f.now.Add(time.Minute)
		wantQueued(t, f, summonPost(t, f, keyedEscalation("esc-1", "esc-1")), "esc-1")
		wantQueueRefusal(t, summonPost(t, f, keyedRemediation("rem-responder-2", "alert-2")), true, "concurrent")
	})
}

// A charge with no Session is put in the queue of the entry its request hash
// names, a suffixed name included; one that no current entry names counts in
// every queue (coordinator answer op778-q-1).
func TestSummonQueueChargeAttribution(t *testing.T) {
	p := queuePolicy(1, 1, 4, 4, 6)
	hash := summonRequestHash(summonWire{Mode: "task", Lane: "remediation", Name: "rem-responder-1"}, v1alpha1.SummonPriorityUrgent)
	remediations := []summonQueue{{urgent: true}}
	for _, tc := range []struct {
		name string
		want []summonQueue
	}{
		{"rem-responder-1", remediations},
		{"rem-responder-1-2", remediations},
		{"rem-responder-1-99", remediations},
		{"rem-responder-1-100", summonQueues},
		{"rem-responder-1-02", summonQueues},
		{"rem-responder-1-1", summonQueues},
		{"rem-responder-12", summonQueues},
	} {
		if got := summonChargeQueues(p, hash, tc.name); !slices.Equal(got, tc.want) {
			t.Errorf("%s: queues %v, want %v", tc.name, got, tc.want)
		}
	}
	upgrade := summonRequestHash(summonWire{Mode: "remote", Lane: "upgrade", Name: "wo-1"}, v1alpha1.SummonPriorityNormal)
	if got := summonChargeQueues(p, upgrade, "wo-1"); !slices.Equal(got, []summonQueue{{}}) {
		t.Errorf("upgrade: %v", got)
	}

	t.Run("a suffixed charge in flight", func(t *testing.T) {
		f := lanesFixture(t, queuePolicy(1, 1, 4, 1, 6))
		body := keyedRemediation("rem-responder-1", "alert-1")
		wantStatus(t, summonPost(t, f, body), http.StatusCreated)
		// The first session ends; the re-fire takes the suffix -2 and its
		// create fails after the charge.
		s := f.session("rem-responder-1")
		at := metav1.NewTime(f.now)
		s.Status.Outcome = &v1alpha1.OutcomeStatus{State: v1alpha1.OutcomeDone, At: &at}
		if err := f.c.Status().Update(context.Background(), s); err != nil {
			t.Fatal(err)
		}
		f.now = f.now.Add(time.Minute)
		saved := f.srv.Client
		failSessionCreate(f, false, func(int) bool { return true })
		if w := summonPost(t, f, body); w.Code < 500 {
			t.Fatalf("crash: %d %s", w.Code, w.Body.String())
		}
		f.srv.Client = saved
		x := summonDispatchID(summonTestAccount, "alert-1", s)
		if ch, ok := f.ledger().charge(x); !ok || ch.Name != "rem-responder-1-2" {
			t.Fatalf("not a suffixed charge in flight: %+v", f.ledger().Charges)
		}
		f.now = f.now.Add(time.Minute)
		wantQueueRefusal(t, summonPost(t, f, keyedRemediation("rem-responder-2", "alert-2")), true, "concurrent")
		wantQueued(t, f, summonPost(t, f, keyedEscalation("esc-1", "esc-1")), "esc-1")
	})

	t.Run("an entry edited away", func(t *testing.T) {
		f := lanesFixture(t, queuePolicy(1, 1, 1, 1, 6))
		crashFirstCreate(t, f, map[string]any{"name": "wo-1", "mode": "remote", "lane": "upgrade", "idempotencyKey": "wo-1"})
		var pol v1alpha1.CallerPolicy
		if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: ledgerTestNS, Name: ledgerTestKey}, &pol); err != nil {
			t.Fatal(err)
		}
		pol.Spec.Summon.Sessions = slices.DeleteFunc(pol.Spec.Summon.Sessions, func(e v1alpha1.CallerPolicySummonSession) bool {
			return e.Lane == v1alpha1.LaneUpgrade
		})
		if err := f.c.Update(context.Background(), &pol); err != nil {
			t.Fatal(err)
		}
		f.now = f.now.Add(time.Minute)
		wantQueueRefusal(t, summonPost(t, f, keyedRemediation("rem-responder-1", "alert-1")), true, "concurrent")
		wantQueueRefusal(t, summonPost(t, f, keyedEscalation("esc-1", "esc-1")), true, "escalations")
		wantQueueRefusal(t, summonPost(t, f, curationBody("wo-cigar-curate-1")), false, "")
		// Past the hold the charge is in no queue.
		f.now = f.now.Add(15 * time.Minute)
		wantQueued(t, f, summonPost(t, f, keyedRemediation("rem-responder-1", "alert-1")), "rem-responder-1")
	})
}

// Proof 8: a charge with no Session counts while its attempt mark is inside
// the 15 minute hold and not after. A late retry of that charge needs a
// place again, and a queue that filled meanwhile refuses it (its charge
// stays: no refund).
func TestSummonQueueChargeInsideTheHold(t *testing.T) {
	f := lanesFixture(t, queuePolicy(1, 1, 4, 1, 6))
	t0 := f.now
	x := crashFirstCreate(t, f, keyedRemediation("rem-responder-1", "alert-1"))
	f.now = t0.Add(summonledger.AttemptHold - time.Second)
	wantQueueRefusal(t, summonPost(t, f, keyedRemediation("rem-responder-2", "alert-2")), true, "concurrent")
	f.now = t0.Add(summonledger.AttemptHold)
	wantQueued(t, f, summonPost(t, f, keyedRemediation("rem-responder-2", "alert-2")), "rem-responder-2")
	f.now = t0.Add(summonledger.AttemptHold + time.Minute)
	// The retry's charge stays, so its refusal says so (the advisory review
	// of PR 1094, comment 4554468887).
	if e := wantQueueRefusalOf(t, summonPost(t, f, keyedRemediation("rem-responder-1", "alert-1")), true, "concurrent"); !strings.Contains(e.Message, summonRetention) {
		t.Fatalf("the late retry's refusal does not say its charge is kept: %q", e.Message)
	}
	ch, ok := f.ledger().charge(x)
	if !ok || !ch.Attempt.Equal(t0) {
		t.Fatalf("the late retry marked or dropped its charge: %+v", f.ledger().Charges)
	}
	if e := f.ledger(); e.UrgentRefusals != 2 {
		t.Fatalf("urgent refusals %d", e.UrgentRefusals)
	}
	wantNoSession(t, f, "rem-responder-1")
}

// Proof 9: a session with a grant in the ledger's reserved key and no
// admittedAt yet is admitted, not Queued: the API's count leaves it out, and
// the leader completes it at the grant's time instead of deciding again.
func TestSummonQueueGrantedSessionIsAdmitted(t *testing.T) {
	f := lanesFixture(t, queuePolicy(1, 1, 4, 1, 6))
	wantQueued(t, f, summonPost(t, f, keyedRemediation("rem-responder-1", "alert-1")), "rem-responder-1")
	wantQueueRefusal(t, summonPost(t, f, keyedRemediation("rem-responder-2", "alert-2")), true, "concurrent")
	s := f.session("rem-responder-1")
	grantAt := f.now.Add(-time.Minute).UTC().Truncate(time.Second)
	raw, err := summonledger.EncodeGrants(summonledger.Grants{Items: []summonledger.Grant{{
		Name: s.Name, UID: string(s.UID), Lane: "remediation", Caller: ledgerTestKey, Pool: summonledger.PoolConcurrent, At: grantAt}}})
	if err != nil {
		t.Fatal(err)
	}
	_, cm, err := readLedgerConfigMap(context.Background(), f.c, ledgerTestKey)
	if err != nil {
		t.Fatal(err)
	}
	cm.Data[summonledger.GrantsKey] = raw
	if err := f.c.Update(context.Background(), cm); err != nil {
		t.Fatal(err)
	}
	if summonoutcome.Queued(s, true) || !summonoutcome.Queued(s, false) {
		t.Fatal("the shared predicate does not tell a granted session from a Queued one")
	}
	f.now = f.now.Add(time.Minute)
	wantQueued(t, f, summonPost(t, f, keyedRemediation("rem-responder-2", "alert-2")), "rem-responder-2")
	// The leader, over the same store, completes the grant at its own time.
	lead(t, f, leaderOver(t, f), "rem-responder-1", "rem-responder-2")
	got := f.session("rem-responder-1").Status.Summon
	if got == nil || got.AdmittedAt == nil || !got.AdmittedAt.Time.Equal(grantAt) {
		t.Fatalf("rem-responder-1: %+v, want admitted at the grant's time %s", got, grantAt)
	}
	if r := holdReason(f, "rem-responder-2"); r != controller.SummonReasonLaneBusy || len(admittedNames(f, "rem-responder-2")) != 0 {
		t.Fatalf("rem-responder-2 hold %q", r)
	}
}

// overTheTransport is ruling D4's regression (the reviews of PR 1090): an
// 8192-ampersand template and four 4096-ampersand data values. Its brief is
// within protocol.MaxPromptBytes, but agentd's JSON document escapes each
// ampersand to six bytes and passes protocol.MaxSessionBytes.
func overTheTransport() (*v1alpha1.CallerPolicy, map[string]any) {
	p := summonTestPolicy()
	e := &p.Spec.Summon.Sessions[0]
	e.Brief.Template = strings.Repeat("&", 8192)
	e.Brief.Fields = []string{"alert", "diagnosis", "signature", "extra"}
	data := map[string]string{}
	for _, k := range e.Brief.Fields {
		data[k] = strings.Repeat("&", summonValueMaxBytes)
	}
	return p, remediationBody("rem-responder-1", data)
}

// Proof 10 (ruling D4): the oversized request gets its own 422 before the
// switch, with the switch empty, with task and with both kinds; nothing is
// written. A valid create still gets the switch's 403 when its kind is off.
func TestSummonTransportBoundBeforeTheSwitch(t *testing.T) {
	for _, kinds := range [][]v1alpha1.SummonKind{nil, {v1alpha1.SummonKindTask}, {v1alpha1.SummonKindTask, v1alpha1.SummonKindRemote}} {
		p, body := overTheTransport()
		f := summonFixture(t, p)
		f.srv.SummonCreateKinds = kinds
		before := f.ledgerBytes()
		e := wantError(t, summonPost(t, f, body), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
		if len(e.Fields) != 1 || !strings.Contains(e.Fields[0].Message, "bounded transport") {
			t.Fatalf("kinds %v: %+v is not the transport bound", kinds, e)
		}
		if after := f.ledgerBytes(); string(before) != string(after) || len(f.summonedSessions()) != 0 {
			t.Fatalf("kinds %v: the refusal wrote something", kinds)
		}
	}
	f := summonSwitchFixture(t, v1alpha1.SummonKindRemote)
	wantSwitchRefusal(t, summonPost(t, f, remediationBody("rem-responder-1", map[string]string{"alert": strings.Repeat("&", 64)})), "task")
}

// Proof 11: with the lanes on, every API write still carries the reserved
// key byte for byte: a charge, a mark, and a queue refusal's count. The
// queue decodes it read only.
func TestSummonQueueKeepsAdmissionKey(t *testing.T) {
	const grants = `{ "schema": "dev-env.summon-grants.v1", "grants": [], "future": 7 }`
	cm := summonLedgerObject()
	cm.Data = map[string]string{summonledger.GrantsKey: grants}
	f := lanesFixture(t, queuePolicy(1, 1, 4, 1, 6), cm)
	check := func(step string) {
		t.Helper()
		_, stored, err := readLedgerConfigMap(context.Background(), f.c, ledgerTestKey)
		if err != nil || stored.Data[summonledger.GrantsKey] != grants {
			t.Fatalf("%s changed the admission key: %v", step, err)
		}
	}
	wantQueued(t, f, summonPost(t, f, keyedRemediation("rem-responder-1", "alert-1")), "rem-responder-1")
	check("charge and mark")
	wantQueueRefusal(t, summonPost(t, f, keyedRemediation("rem-responder-2", "alert-2")), true, "concurrent")
	check("urgent queue refusal")
	if f.ledger().UrgentRefusals != 1 {
		t.Fatal("the refusal was not counted")
	}
}

// Coordinator answer op778-q-1, gap B: a present reserved key that cannot be
// read is 503, with no write and no Session, on a write that needs the
// queue count; a missing key is no grants; and an in-flight create's own
// mark, which needs no count, is not blocked and carries the key unchanged.
func TestSummonQueueUnreadableAdmissionKey(t *testing.T) {
	const bad = "not a grants document"
	badLedger := func() *corev1.ConfigMap {
		cm := summonLedgerObject()
		cm.Data = map[string]string{summonledger.GrantsKey: bad}
		return cm
	}
	t.Run("a new charge", func(t *testing.T) {
		f := lanesFixture(t, queuePolicy(1, 1, 4, 4, 6), badLedger())
		before := f.ledgerBytes()
		e := wantError(t, summonPost(t, f, keyedRemediation("rem-responder-1", "alert-1")), http.StatusServiceUnavailable, apiv1.CodeUnavailable)
		if !strings.Contains(e.Message, "admission grants that cannot be read") {
			t.Fatalf("message %q", e.Message)
		}
		if string(before) != string(f.ledgerBytes()) || len(f.summonedSessions()) != 0 {
			t.Fatal("the refusal wrote something")
		}
	})
	t.Run("lanes off", func(t *testing.T) {
		f := summonFixture(t, queuePolicy(1, 1, 4, 4, 6), badLedger())
		wantStatus(t, summonPost(t, f, keyedRemediation("rem-responder-1", "alert-1")), http.StatusCreated)
	})
	t.Run("a missing key", func(t *testing.T) {
		f := lanesFixture(t, queuePolicy(1, 1, 4, 4, 6))
		_, cm, err := readLedgerConfigMap(context.Background(), f.c, ledgerTestKey)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := cm.Data[summonledger.GrantsKey]; ok {
			t.Fatal("the fixture has a reserved key")
		}
		wantQueued(t, f, summonPost(t, f, keyedRemediation("rem-responder-1", "alert-1")), "rem-responder-1")
	})
	t.Run("an in-flight mark", func(t *testing.T) {
		f := lanesFixture(t, queuePolicy(1, 1, 4, 4, 6))
		writes := 0
		f.srv.Client = interceptor.NewClient(f.c, interceptor.Funcs{Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			cm, ok := obj.(*corev1.ConfigMap)
			if !ok || cm.Name != ledgerTestName {
				return c.Update(ctx, obj, opts...)
			}
			if err := c.Update(ctx, obj, opts...); err != nil {
				return err
			}
			writes++
			if writes == 1 {
				// After the charge, before the mark: the key turns unreadable.
				var live corev1.ConfigMap
				if err := c.Get(ctx, client.ObjectKeyFromObject(cm), &live); err != nil {
					return err
				}
				live.Data[summonledger.GrantsKey] = bad
				return c.Update(ctx, &live)
			}
			return nil
		}})
		wantQueued(t, f, summonPost(t, f, keyedRemediation("rem-responder-1", "alert-1")), "rem-responder-1")
		_, cm, err := readLedgerConfigMap(context.Background(), f.c, ledgerTestKey)
		if err != nil || writes != 2 || cm.Data[summonledger.GrantsKey] != bad {
			t.Fatalf("writes %d, key %q: %v", writes, cm.Data[summonledger.GrantsKey], err)
		}
	})
}
