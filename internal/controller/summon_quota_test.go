package controller

// The proofs of story 790 (slice b of item 743; DESIGN-064 6.7, 6.8 and 6.9,
// rulings Q13 and Q15 in item 743 comment 7458 and ruling (f) in comment
// 7540): the quota seam carries the gate's decision; a curation run held
// QuotaHold past its maxHold closes quota-hold-expired, and every other
// expired hold hold-expired, on one clock from the session's creation; the
// QuotaHold condition names the window, the percentage, the line and the
// reading's readAt and is written only when one of them changes; and the gate
// reads the readings at most once per admission pass. Each test runs the
// real admission pass and the real gate (internal/summonquota) on a fake API
// server, a fake store and the world's clock. No goroutine timing.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/summonledger"
	"github.com/thaynes43/dev-env/internal/summonoutcome"
	"github.com/thaynes43/dev-env/internal/summonquota"
)

// quotaInstallLines are the lines the install is to set (DESIGN-064 6.9,
// C8): normal 95, bulk 80, the weekly line off, 15 minutes max age.
var quotaInstallLines = summonquota.Lines{NormalFiveHour: 95, BulkFiveHour: 80, MaxAge: 15 * time.Minute}

// quotaStore is the gate's fake store: the newest Claude reading, and a
// count of reads.
type quotaStore struct {
	reads    int
	readings map[string]summonquota.Reading
}

func (q *quotaStore) read(context.Context) (map[string]summonquota.Reading, error) {
	q.reads++
	return q.readings, nil
}

// set makes the newest Claude reading: the 5-hour window at fiveHour and,
// when not empty, the 7-day window at sevenDay, read at readAt.
func (q *quotaStore) set(readAt time.Time, fiveHour, sevenDay string) {
	r := summonquota.Reading{ReadAt: readAt}
	if fiveHour != "" {
		r.Windows = append(r.Windows, protocol.AllowanceWindow{Minutes: summonquota.FiveHourMinutes, UsedPercent: fiveHour, ResetsAt: readAt.Add(3 * time.Hour)})
	}
	if sevenDay != "" {
		r.Windows = append(r.Windows, protocol.AllowanceWindow{Minutes: summonquota.SevenDayMinutes, UsedPercent: sevenDay, ResetsAt: readAt.Add(72 * time.Hour)})
	}
	q.readings = map[string]summonquota.Reading{protocol.AgentClaude: r}
}

// quotaGate puts the real gate on the world's leader, on the world's clock,
// with lines and a fake store; seen collects every session the gate is
// asked about, in order. Call it after any w.leader: a new leader has no
// gate.
func (w *summonWorld) quotaGate(lines summonquota.Lines) (*quotaStore, *[]string) {
	q := &quotaStore{}
	g := &summonquota.Gate{Lines: lines, Read: q.read, Now: func() time.Time { return w.now }, Log: logr.Discard()}
	seen := &[]string{}
	w.r.SummonQuota = func(ctx context.Context, s *v1alpha1.AgentSession) summonquota.Decision {
		*seen = append(*seen, s.Name)
		return g.DecideSession(ctx, s)
	}
	return q, seen
}

// condition is the session's SummonAdmitted condition, or nil.
func (w *summonWorld) condition(name string) *metav1.Condition {
	w.t.Helper()
	return meta.FindStatusCondition(w.get(name).Status.Conditions, ConditionSummonAdmitted)
}

// fresh sets the reading a minute old on the world's clock.
func (w *summonWorld) fresh(q *quotaStore, fiveHour, sevenDay string) {
	q.set(w.now.Add(-time.Minute), fiveHour, sevenDay)
}

// Proof 1: a bulk session held QuotaHold closes at its maxHold, 12 hours
// after its creation, by its onUnreported (failed) with reason
// quota-hold-expired, once, and releases at once; one second before, it is
// still held. Its condition says which close it was.
func TestSummonQuotaHeldCurationClosesQuotaHoldExpired(t *testing.T) {
	w := newSummonWorld(t, summonPolicyObject(callerB, 2, nil))
	q, _ := w.quotaGate(quotaInstallLines)
	held := summonedFor("curate", callerB, v1alpha1.LaneCuration, 0)
	w.fresh(q, "81", "")
	w.queue(held)
	w.pass()
	if got := w.hold(held.Name); got != "False/"+SummonReasonQuotaHold {
		t.Fatalf("at 81 %%, line 80: condition %s", got)
	}
	created := w.get(held.Name).CreationTimestamp.Time
	w.now = created.Add(12*time.Hour - time.Second)
	w.fresh(q, "81", "")
	w.pass()
	w.wantOpen("a second before maxHold", held.Name)
	if got := w.hold(held.Name); got != "False/"+SummonReasonQuotaHold {
		t.Fatalf("a second before maxHold: condition %s", got)
	}
	w.now = created.Add(12 * time.Hour)
	w.pass()
	w.wantClosed(held.Name, summonoutcome.ReasonQuotaHoldExpired, v1alpha1.OutcomeFailed)
	s := w.get(held.Name)
	if !s.Status.Summon.Closed.Released || s.Status.Summon.AdmittedAt != nil || s.Status.Summon.Escalation != nil {
		t.Fatalf("closed=%+v admitted=%v filing=%+v", s.Status.Summon.Closed, s.Status.Summon.AdmittedAt, s.Status.Summon.Escalation)
	}
	if s.Status.Outcome.Note != "unreported: "+summonoutcome.ReasonQuotaHoldExpired {
		t.Fatalf("outcome note %q", s.Status.Outcome.Note)
	}
	c := w.condition(held.Name)
	if c == nil || c.Status != metav1.ConditionFalse || c.Reason != SummonReasonQuotaHoldExpired || c.Message != summonHoldMessages[SummonReasonQuotaHoldExpired] {
		t.Fatalf("condition after the close: %+v", c)
	}
	// Once: later passes, with the reading still past the line, change
	// nothing.
	for range 3 {
		w.now = w.now.Add(time.Minute)
		w.fresh(q, "99", "")
		w.pass()
	}
	after := w.get(held.Name)
	if after.ResourceVersion != s.ResourceVersion || !apiequality.Semantic.DeepEqual(after.Status, s.Status) {
		t.Fatalf("a closed session was written again: %+v", after.Status)
	}
	w.wantNoPodOrVolume(held.Name)
}

// Proof 2 (ruling (f)): the hold recorded at the expiry pass decides the
// reason, and the clock runs from the session's creation whatever its hold
// was before.
func TestSummonHoldExpiredReasonIsTheHoldRecordedAtExpiry(t *testing.T) {
	for _, tc := range []struct {
		name   string
		first  string // the hold before the change, at creation
		atExp  string // the hold recorded at expiry
		reason string
		cond   string
	}{
		{"lane busy at expiry", SummonReasonLaneBusy, SummonReasonLaneBusy, summonoutcome.ReasonHoldExpired, SummonReasonHoldExpired},
		{"lane busy then quota", SummonReasonLaneBusy, SummonReasonQuotaHold, summonoutcome.ReasonQuotaHoldExpired, SummonReasonQuotaHoldExpired},
		{"quota then lane busy", SummonReasonQuotaHold, SummonReasonLaneBusy, summonoutcome.ReasonHoldExpired, SummonReasonHoldExpired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newSummonWorld(t, summonPolicyObject(callerB, 2, nil))
			q, _ := w.quotaGate(quotaInstallLines)
			// other is older, so it is admitted first when the lane and
			// the quota let it; its own hold bound never trips here.
			other := summonedFor("other", callerB, v1alpha1.LaneCuration, time.Minute)
			other.Spec.Summon.MaxHold = "48h"
			held := summonedFor("held", callerB, v1alpha1.LaneCuration, 0)
			if tc.first == SummonReasonQuotaHold {
				w.fresh(q, "81", "")
			} else {
				w.fresh(q, "10", "")
			}
			w.queue(other, held)
			w.pass()
			if got := w.hold(held.Name); got != "False/"+tc.first {
				t.Fatalf("at creation: %s, want %s", got, tc.first)
			}
			created := w.get(held.Name).CreationTimestamp.Time
			// Six hours on, the hold changes.
			w.now = created.Add(6 * time.Hour)
			if tc.atExp == SummonReasonQuotaHold {
				// other reports and frees the lane; the plan is now busy.
				w.close(other.Name, summonoutcome.Report)
				w.fresh(q, "81", "")
			} else {
				// The plan frees up: other is admitted and holds the lane.
				w.fresh(q, "10", "")
			}
			w.pass()
			if got := w.hold(held.Name); got != "False/"+tc.atExp {
				t.Fatalf("six hours on: %s, want %s", got, tc.atExp)
			}
			// The bound is 12 hours from creation, not from the change.
			w.now = created.Add(12*time.Hour - time.Second)
			w.fresh(q, map[bool]string{true: "81", false: "10"}[tc.atExp == SummonReasonQuotaHold], "")
			w.pass()
			w.wantOpen("a second before maxHold", held.Name)
			if got := w.hold(held.Name); got != "False/"+tc.atExp {
				t.Fatalf("a second before maxHold: %s, want %s", got, tc.atExp)
			}
			w.now = created.Add(12 * time.Hour)
			w.pass()
			w.wantClosed(held.Name, tc.reason, v1alpha1.OutcomeFailed)
			if !w.released(held.Name) {
				t.Fatal("not released at once")
			}
			if got := w.hold(held.Name); got != "False/"+tc.cond {
				t.Fatalf("condition after the close: %s, want %s", got, tc.cond)
			}
		})
	}
}

// Proof 3: the hold lifts in the pass after a newer reading falls to the
// line, and the session is admitted. The next pass is one second later:
// every pass reads again, so no reading from an earlier pass answers it.
func TestSummonQuotaHoldLiftsInThePassAfterANewerReading(t *testing.T) {
	w := newSummonWorld(t, summonPolicyObject(callerB, 2, nil))
	q, _ := w.quotaGate(quotaInstallLines)
	held := summonedFor("curate", callerB, v1alpha1.LaneCuration, 0)
	w.fresh(q, "81", "")
	w.queue(held)
	w.pass()
	if got := w.hold(held.Name); got != "False/"+SummonReasonQuotaHold {
		t.Fatalf("at 81 %%: %s", got)
	}
	q.set(w.now, "80", "")
	w.now = w.now.Add(time.Second)
	w.pass()
	if !w.admitted(held.Name) || w.hold(held.Name) != "True/"+SummonReasonAdmitted {
		t.Fatalf("a newer reading at 80 %%, the line: admitted %v, condition %s", w.admitted(held.Name), w.hold(held.Name))
	}
	if q.reads != 2 {
		t.Fatalf("%d store reads in two passes, want 2", q.reads)
	}
}

// Proof 4: the QuotaHold message names the window, the percentage read, the
// line and the reading's readAt, and never its age. Ten passes on the same
// reading write the status once; a new percentage rewrites it once.
func TestSummonQuotaHoldMessageIsWrittenOnlyWhenItsNumbersChange(t *testing.T) {
	t.Run("5-hour", func(t *testing.T) {
		held := summonedFor("curate", callerB, v1alpha1.LaneCuration, 0)
		held.Spec.Prompt = "SESSION-CONTROLLED-PROMPT"
		w := newSummonWorld(t, summonPolicyObject(callerB, 2, nil))
		writes := 0
		w.r = w.leader(&interceptor.Funcs{SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
			if sub == "status" && obj.GetName() == held.Name {
				writes++
			}
			return c.SubResource(sub).Patch(ctx, obj, patch, opts...)
		}})
		q, _ := w.quotaGate(quotaInstallLines)
		readAt := summonT0.Add(-time.Minute)
		q.set(readAt, "81", "")
		w.queue(held)
		writes = 0
		for i := range 10 {
			w.now = summonT0.Add(time.Duration(i) * time.Minute)
			w.pass()
		}
		if writes != 1 {
			t.Fatalf("ten passes on one reading: %d status writes, want 1", writes)
		}
		want := "held: quota: the 5-hour window is 81% used, past its line of 80% (reading taken at 2026-11-12T15:59:00Z)"
		c := w.condition(held.Name)
		if c == nil || c.Reason != SummonReasonQuotaHold || c.Message != want {
			t.Fatalf("condition %+v, want message %q", c, want)
		}
		for _, bad := range []string{"age", "ago", "old", "9m", "SESSION-CONTROLLED", callerB, held.Spec.Summon.DispatchID} {
			if strings.Contains(c.Message, bad) {
				t.Fatalf("message %q names %q", c.Message, bad)
			}
		}
		// A new percentage, in a newer reading: one rewrite, then none.
		q.set(w.now, "82.5", "")
		w.now = w.now.Add(time.Minute)
		w.pass()
		w.now = w.now.Add(time.Minute)
		w.pass()
		if writes != 2 {
			t.Fatalf("a new percentage: %d status writes, want 2", writes)
		}
		want = "held: quota: the 5-hour window is 82.5% used, past its line of 80% (reading taken at 2026-11-12T16:09:00Z)"
		if c := w.condition(held.Name); c == nil || c.Message != want {
			t.Fatalf("condition %+v, want message %q", c, want)
		}
	})
	t.Run("7-day", func(t *testing.T) {
		w := newSummonWorld(t, summonPolicyObject(callerB, 2, nil))
		weekly := quotaInstallLines
		weekly.BulkSevenDay = 90
		q, _ := w.quotaGate(weekly)
		held := summonedFor("curate", callerB, v1alpha1.LaneCuration, 0)
		q.set(summonT0.Add(-90*time.Second+500*time.Millisecond), "10", "91")
		w.queue(held)
		w.pass()
		want := "held: quota: the 7-day window is 91% used, past its line of 90% (reading taken at 2026-11-12T15:58:30.5Z)"
		if c := w.condition(held.Name); c == nil || c.Reason != SummonReasonQuotaHold || c.Message != want {
			t.Fatalf("condition %+v, want message %q", c, want)
		}
	})
}

// Proof 6: an urgent session is never held by the gate, so it never closes
// on the quota-hold-expired row; a remediation held for its lane past its
// maxHold closes hold-expired, by its onUnreported (escalated). A normal
// session has no maxHold: held QuotaHold for 30 days, it is still open.
func TestSummonQuotaNeverHoldsUrgentAndNormalNeverExpires(t *testing.T) {
	t.Run("urgent", func(t *testing.T) {
		w := newSummonWorld(t, summonPolicyObject(callerA, 3, ptr.To[int32](1)))
		weekly := quotaInstallLines
		weekly.BulkSevenDay = 1
		q, _ := w.quotaGate(weekly)
		w.fresh(q, "100", "100")
		running := summonedFor("rem-running", callerA, v1alpha1.LaneRemediation, 2*time.Minute)
		running.Spec.Summon.MaxHold = "24h"
		rem := summonedFor("rem-held", callerA, v1alpha1.LaneRemediation, time.Minute)
		esc := summonedFor("esc", callerA, v1alpha1.LaneEscalation, time.Minute)
		w.queue(running, rem, esc)
		w.pass()
		w.wantAdmitted("urgent at 100 %", map[string]bool{running.Name: true, esc.Name: true, rem.Name: false})
		if got := w.hold(rem.Name); got != "False/"+SummonReasonLaneBusy {
			t.Fatalf("a remediation behind its lane: %s", got)
		}
		created := w.get(rem.Name).CreationTimestamp.Time
		w.now = created.Add(20 * time.Minute)
		w.fresh(q, "100", "100")
		w.pass()
		w.wantClosed(rem.Name, summonoutcome.ReasonHoldExpired, v1alpha1.OutcomeEscalated)
		if got := w.hold(rem.Name); got != "False/"+SummonReasonHoldExpired {
			t.Fatalf("condition after the close: %s", got)
		}
	})
	t.Run("normal", func(t *testing.T) {
		w := newSummonWorld(t, summonPolicyObject(callerA, 2, nil))
		q, _ := w.quotaGate(quotaInstallLines)
		up := summonedFor("upgrade", callerA, v1alpha1.LaneUpgrade, 0)
		if up.Spec.Summon.MaxHold != "" {
			t.Fatalf("an upgrade with maxHold %q", up.Spec.Summon.MaxHold)
		}
		w.fresh(q, "96", "")
		w.queue(up)
		for _, after := range []time.Duration{0, 12 * time.Hour, 30 * 24 * time.Hour} {
			w.now = summonT0.Add(after)
			w.fresh(q, "96", "")
			w.pass()
			w.wantOpen("a normal session held for quota", up.Name)
			if got := w.hold(up.Name); got != "False/"+SummonReasonQuotaHold {
				t.Fatalf("%s on: %s", after, got)
			}
		}
	})
}

// Proof 7: only a session with spec.caller reaches the gate (the pass's
// queued list, summonAdmissionAttempt, takes only those); and with every
// line unset nothing is read and every session is admitted as before.
func TestSummonQuotaOnlySummonedSessionsReachTheGateAndUnsetLinesReadNothing(t *testing.T) {
	w := newSummonWorld(t, summonPolicyObject(callerB, 3, nil), summonPolicyObject(callerA, 3, nil))
	ordinary := taskSession()
	ordinary.Name, ordinary.UID = "dev-env-1113-000001", "uid-ordinary"
	w.add(ordinary)
	q, seen := w.quotaGate(summonquota.Lines{MaxAge: 15 * time.Minute})
	w.fresh(q, "100", "100")
	cur := summonedFor("curate", callerB, v1alpha1.LaneCuration, 2*time.Minute)
	up := summonedFor("upgrade", callerA, v1alpha1.LaneUpgrade, time.Minute)
	w.queue(cur, up)
	w.pass()
	w.wantAdmitted("every line unset", map[string]bool{cur.Name: true, up.Name: true})
	if q.reads != 0 {
		t.Fatalf("%d store reads with every line unset", q.reads)
	}
	if strings.Join(*seen, ",") != "curate,upgrade" {
		t.Fatalf("the gate saw %v, want the two summoned sessions only", *seen)
	}
}

// Proof 8: one store read per pass with several Queued sessions, across the
// pass's attempts: a conflict on the first grant write starts the decision
// again, and the gate answers it from the same read. The next pass reads
// once more.
func TestSummonQuotaOneReadPerPass(t *testing.T) {
	t.Run("several held", func(t *testing.T) {
		w := newSummonWorld(t, summonPolicyObject(callerB, 3, nil))
		q, seen := w.quotaGate(quotaInstallLines)
		w.fresh(q, "81", "")
		var names []string
		for _, n := range []string{"c1", "c2", "c3"} {
			s := summonedFor(n, callerB, v1alpha1.LaneCuration, time.Duration(10-len(names))*time.Minute)
			w.queue(s)
			names = append(names, n)
		}
		w.pass()
		if q.reads != 1 || len(*seen) != 3 {
			t.Fatalf("three Queued sessions: %d reads, the gate asked %d times", q.reads, len(*seen))
		}
		for _, n := range names {
			if got := w.hold(n); got != "False/"+SummonReasonQuotaHold {
				t.Fatalf("%s: %s", n, got)
			}
		}
		w.now = w.now.Add(time.Second)
		w.pass()
		if q.reads != 2 {
			t.Fatalf("the next pass: %d reads, want 2", q.reads)
		}
	})
	t.Run("across attempts", func(t *testing.T) {
		w := newSummonWorld(t, summonPolicyObject(callerB, 3, nil), summonPolicyObject(callerA, 3, nil))
		conflicted := false
		w.r = w.leader(&interceptor.Funcs{Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if cm, ok := obj.(*corev1.ConfigMap); ok && cm.Name == summonledger.Name && !conflicted {
				conflicted = true
				return apierrors.NewConflict(schema.GroupResource{Resource: "configmaps"}, cm.Name, nil)
			}
			return c.Update(ctx, obj, opts...)
		}})
		q, seen := w.quotaGate(quotaInstallLines)
		w.fresh(q, "10", "")
		cur := summonedFor("curate", callerB, v1alpha1.LaneCuration, 2*time.Minute)
		up := summonedFor("upgrade", callerA, v1alpha1.LaneUpgrade, time.Minute)
		w.queue(cur, up)
		w.pass()
		if !conflicted {
			t.Fatal("the grant write never conflicted")
		}
		w.wantAdmitted("after the conflict", map[string]bool{cur.Name: true, up.Name: true})
		if q.reads != 1 || len(*seen) < 3 {
			t.Fatalf("one pass of two attempts: %d reads, the gate asked %v", q.reads, *seen)
		}
	})
}
