package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/summonledger"
	"github.com/thaynes43/dev-env/internal/summonoutcome"
)

const filingRem = "rem-filing"

func filingRemediation() *v1alpha1.AgentSession {
	s := summonedFor(filingRem, callerA, v1alpha1.LaneRemediation, time.Minute)
	s.UID = "00000000-0000-0000-0000-000000000801"
	at := metav1.NewTime(summonT0)
	s.Status.Outcome = &v1alpha1.OutcomeStatus{State: v1alpha1.OutcomeEscalated, Note: "needs a person", At: &at}
	s.Status.Summon = &v1alpha1.SummonStatus{
		Closed:     &v1alpha1.SummonClosedStatus{At: at, Source: summonoutcome.Report, Reason: "escalate", Released: true},
		Escalation: &v1alpha1.SummonEscalationStatus{State: v1alpha1.SummonEscalationPending, Name: summonoutcome.EscalationName(s)},
	}
	return s
}

func filingPolicy() *v1alpha1.CallerPolicy {
	p := summonPolicyObject(callerA, 1, ptr.To(int32(2)))
	p.Spec.Summon.Sessions = []v1alpha1.CallerPolicySummonSession{{Kind: v1alpha1.SummonKindRemote, Lane: v1alpha1.LaneEscalation, Priority: v1alpha1.SummonPriorityUrgent}}
	return p
}

func newFilingWorld(t *testing.T) *summonWorld {
	w := newSummonWorld(t, filingRemediation(), filingPolicy())
	w.r = filingLeader(w, interceptor.Funcs{})
	return w
}

func filingLeader(w *summonWorld, hooks interceptor.Funcs) *Reconciler {
	create := hooks.Create
	hooks.Create = func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
		if s, ok := o.(*v1alpha1.AgentSession); ok {
			s.UID = types.UID("00000000-0000-0000-0000-000000000802")
			s.CreationTimestamp = metav1.NewTime(w.now)
		}
		if create != nil {
			return create(ctx, c, o, opts...)
		}
		return c.Create(ctx, o, opts...)
	}
	r := w.leader(&hooks)
	r.SummonCreateKinds = []v1alpha1.SummonKind{v1alpha1.SummonKindRemote}
	r.SummonFilingClock = &filingTestClock{now: w.now}
	return r
}

func filingLedger(t *testing.T, w *summonWorld) (summonledger.Entry, *corev1.ConfigMap) {
	t.Helper()
	var cm corev1.ConfigMap
	if err := w.base.Get(context.Background(), client.ObjectKey{Namespace: summonledger.Namespace, Name: summonledger.Name}, &cm); err != nil {
		t.Fatal(err)
	}
	raw, ok := cm.Data[callerA]
	e, err := summonledger.Decode(raw, ok)
	if err != nil {
		t.Fatal(err)
	}
	return e, &cm
}

func filingChildren(t *testing.T, w *summonWorld) []v1alpha1.AgentSession {
	t.Helper()
	var list v1alpha1.AgentSessionList
	if err := w.base.List(context.Background(), &list, client.MatchingLabels{summonoutcome.LabelFiledForUID: string(filingRemediation().UID)}); err != nil {
		t.Fatal(err)
	}
	return list.Items
}

func assertOneFiling(t *testing.T, w *summonWorld, at time.Time) {
	t.Helper()
	children := filingChildren(t, w)
	e, _ := filingLedger(t, w)
	id := summonledger.FilingID(filingRemediation().UID)
	count := 0
	for _, ch := range e.Charges {
		if ch.ID == id {
			count++
			if !ch.At.Equal(at) {
				t.Fatalf("charge moved from %s to %s", at, ch.At)
			}
		}
	}
	if len(children) != 1 || count != 1 {
		t.Fatalf("children=%d charges=%d, want one of each", len(children), count)
	}
	st := w.get(filingRem).Status.Summon.Escalation
	if st.State != v1alpha1.SummonEscalationFiled || st.UID != string(children[0].UID) || st.Name != children[0].Name || st.ChargedAt == nil || !st.ChargedAt.Time.Equal(at.Truncate(time.Second)) {
		t.Fatalf("filing not recorded: %+v", st)
	}
}

// Each barrier ends the first leader's pass at one durable boundary. The
// next leader has no memory from it, and runs after the charge's hour.
func TestSummonFilingReplayGaps(t *testing.T) {
	for _, gap := range []string{"after-charge", "after-charged-at", "after-mark", "after-create", "before-filed", "lost-create-not-landed"} {
		t.Run(gap, func(t *testing.T) {
			w := newFilingWorld(t)
			stopped := errors.New("pass killed at barrier")
			fired := false
			writes := 0
			hooks := interceptor.Funcs{
				Update: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.UpdateOption) error {
					if err := c.Update(ctx, o, opts...); err != nil {
						return err
					}
					if cm, ok := o.(*corev1.ConfigMap); ok && cm.Name == summonledger.Name {
						writes++
						if gap == "after-charge" && writes == 1 || gap == "after-mark" && writes == 2 {
							fired = true
							return stopped
						}
					}
					return nil
				},
				SubResourcePatch: func(ctx context.Context, c client.Client, sub string, o client.Object, p client.Patch, opts ...client.SubResourcePatchOption) error {
					s, ok := o.(*v1alpha1.AgentSession)
					if ok && s.Name == filingRem && s.Status.Summon != nil && s.Status.Summon.Escalation != nil {
						st := s.Status.Summon.Escalation
						if gap == "before-filed" && st.State == v1alpha1.SummonEscalationFiled {
							fired = true
							return stopped
						}
						if gap == "after-charged-at" && st.ChargedAt != nil && st.State == v1alpha1.SummonEscalationPending {
							if err := c.SubResource(sub).Patch(ctx, o, p, opts...); err != nil {
								return err
							}
							fired = true
							return stopped
						}
					}
					return c.SubResource(sub).Patch(ctx, o, p, opts...)
				},
				Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
					if _, ok := o.(*v1alpha1.AgentSession); ok {
						if gap == "lost-create-not-landed" {
							fired = true
							return stopped
						}
						if gap == "after-create" {
							if err := c.Create(ctx, o, opts...); err != nil {
								return err
							}
							fired = true
							return stopped
						}
					}
					return c.Create(ctx, o, opts...)
				},
			}
			w.r = filingLeader(w, hooks)
			_ = w.r.SummonAdmissionPass(context.Background())
			if !fired {
				t.Fatal("filing did not reach the requested crash boundary")
			}
			e, _ := filingLedger(t, w)
			if len(e.Charges) != 1 || !e.Charges[0].At.Equal(summonT0) {
				t.Fatalf("crash left %+v", e.Charges)
			}
			w.now = w.now.Add(80 * time.Minute)
			w.r = filingLeader(w, interceptor.Funcs{})
			w.pass()
			assertOneFiling(t, w, summonT0)
			w.r = filingLeader(w, interceptor.Funcs{})
			w.pass()
			assertOneFiling(t, w, summonT0)
		})
	}
}

// Use encoded bytes, including an unknown member that every writer must
// preserve. Identical charge count on either side of the 64 KiB boundary.
func TestSummonFilingEntryByteBound(t *testing.T) {
	for _, extra := range []int{0, 1} {
		t.Run(fmt.Sprintf("cap-plus-%d", extra), func(t *testing.T) {
			w := newFilingWorld(t)
			s := w.get(filingRem)
			charge := summonledger.Charge{ID: summonledger.FilingID(s.UID), At: w.now, Attempt: w.now, Name: summonoutcome.EscalationName(s), Hash: summonledger.RequestHash("remote", "escalation", summonoutcome.EscalationName(s), v1alpha1.SummonPriorityUrgent)}
			empty, err := summonledger.Encode(summonledger.Entry{Clock: w.now})
			if err != nil {
				t.Fatal(err)
			}
			var data map[string]any
			if err := json.Unmarshal([]byte(empty), &data); err != nil {
				t.Fatal(err)
			}
			data["padding"] = ""
			raw, err := json.Marshal(data)
			if err != nil {
				t.Fatal(err)
			}
			e, err := summonledger.Decode(string(raw), true)
			if err != nil {
				t.Fatal(err)
			}
			e.Charges = append(e.Charges, charge)
			withCharge, err := summonledger.Encode(e)
			if err != nil {
				t.Fatal(err)
			}
			data["padding"] = strings.Repeat("p", summonledger.MaxEntry-len(withCharge)-20+extra)
			raw, err = json.Marshal(data)
			if err != nil {
				t.Fatal(err)
			}
			_, cm := filingLedger(t, w)
			cm.Data = map[string]string{callerA: string(raw), "other-caller": "untouched", summonledger.GrantsKey: `{"schema":"dev-env.summon-grants.v1","grants":[]}`}
			// Use the shared encoder for the reserved entry's real schema.
			cm.Data[summonledger.GrantsKey], err = summonledger.EncodeGrants(summonledger.Grants{})
			if err != nil {
				t.Fatal(err)
			}
			if err := w.base.Update(context.Background(), cm); err != nil {
				t.Fatal(err)
			}
			before := cm.Data[callerA]
			w.r = filingLeader(w, interceptor.Funcs{Update: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.UpdateOption) error {
				if err := c.Update(ctx, o, opts...); err != nil {
					return err
				}
				// After the whole-second charge, both clock and attempt grow by
				// ten bytes. The charge must already have reserved those bytes.
				w.now = summonT0.Add(123456789 * time.Nanosecond)
				return nil
			}})
			w.pass()
			got, after := filingLedger(t, w)
			if after.Data["other-caller"] != "untouched" {
				t.Fatal("changed another caller")
			}
			if extra == 0 {
				assertOneFiling(t, w, summonT0)
				if n := len(after.Data[callerA]); n != summonledger.MaxEntry {
					t.Fatalf("encoded bytes=%d, want %d", n, summonledger.MaxEntry)
				}
			} else {
				condition := meta.FindStatusCondition(w.get(filingRem).Status.Conditions, summonoutcome.ConditionEscalationPending)
				if condition == nil || condition.Status != metav1.ConditionTrue || condition.Message != string(summonoutcome.FilingEntryFull) {
					t.Fatalf("want entry-full, got %+v", condition)
				}
				if len(got.Charges) != 0 || after.Data[callerA] != before || len(filingChildren(t, w)) != 0 {
					t.Fatal("over-cap attempt wrote the ledger or a child")
				}
			}
		})
	}
}

// Capacity is a byte bound, not queuedUrgent or createsPerHour. This is the
// documented example with full-size UUIDs, names and fractional timestamps;
// unknown members and other charges reduce the available space.
func TestSummonFilingPendingCapacityExample(t *testing.T) {
	at := time.Date(2026, 11, 14, 6, 0, 0, 123456789, time.UTC)
	for _, n := range []int{232, 233} {
		e := summonledger.Entry{Clock: at}
		for i := range n {
			e.Charges = append(e.Charges, summonledger.Charge{
				ID: summonledger.FilingID(types.UID(fmt.Sprintf("00000000-0000-0000-0000-%012d", i))),
				At: at, Attempt: at, Hash: strings.Repeat("a", 64), Name: strings.Repeat("n", 63),
			})
		}
		encoded, err := summonledger.Encode(e)
		if (err == nil) != (n == 232) {
			t.Fatalf("charges=%d bytes=%d error=%v", n, len(encoded), err)
		}
	}
}
