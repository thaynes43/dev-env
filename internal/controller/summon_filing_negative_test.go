package controller

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/summonledger"
	"github.com/thaynes43/dev-env/internal/summonoutcome"
)

func TestSummonFilingRejectsForeignEvidence(t *testing.T) {
	for _, kind := range []string{"label-foreign", "label-two", "dispatch-foreign", "finish-foreign"} {
		t.Run(kind, func(t *testing.T) {
			w := newFilingWorld(t)
			s := w.get(filingRem)
			child, err := summonFilingChild(s, "esc-rem-foreign", "0123456789abcdef")
			if err != nil {
				t.Fatal(err)
			}
			child.UID = "00000000-0000-0000-0000-000000000899"
			if kind != "label-two" {
				child.Spec.Parent = "foreign/account"
			}
			if kind == "dispatch-foreign" {
				child.Labels = nil
			}
			w.add(child)
			if kind == "label-two" {
				other := child.DeepCopy()
				other.Name = "esc-rem-another"
				other.UID = "00000000-0000-0000-0000-000000000898"
				other.ResourceVersion = ""
				w.add(other)
			}
			if kind == "finish-foreign" {
				at := metav1.NewTime(w.now)
				s.Status.Summon.Escalation.ChargedAt = &at
				if err := w.r.finishSummonFiling(context.Background(), s, child); err == nil {
					t.Fatal("finish accepted foreign child")
				}
				if w.get(filingRem).Status.Summon.Escalation.State != v1alpha1.SummonEscalationPending {
					t.Fatal("foreign child recorded Filed")
				}
				return
			}
			err = w.r.summonFile(context.Background(), s)
			if err == nil {
				t.Fatal("foreign evidence accepted")
			}
			st := w.get(filingRem).Status.Summon
			if st.Escalation.State != v1alpha1.SummonEscalationPending || st.Escalation.Reason != summonoutcome.FilingLedgerUnavailable || len(st.Pages) != 1 {
				t.Fatalf("foreign evidence did not block: %+v", st)
			}
			var list v1alpha1.AgentSessionList
			if err := w.base.List(context.Background(), &list); err != nil {
				t.Fatal(err)
			}
			want := 2
			if kind == "label-two" {
				want++
			}
			if len(list.Items) != want {
				t.Fatal("foreign evidence allowed a create")
			}
		})
	}
}

func TestSummonFilingMarkRefusals(t *testing.T) {
	for _, kind := range []string{"queue-full", "entry-full", "hash-mismatch", "time-mismatch"} {
		t.Run(kind, func(t *testing.T) {
			w := newFilingWorld(t)
			s := w.get(filingRem)
			b, err := w.r.filingLedgerStep(context.Background(), s, filingCharge, "")
			if err != nil || b.reason != "" {
				t.Fatalf("charge: %+v %v", b, err)
			}
			at := metav1.NewTime(b.charge.At)
			s.Status.Summon.Escalation.ChargedAt = &at
			if err := w.base.Status().Update(context.Background(), s); err != nil {
				t.Fatal(err)
			}
			e, cm := filingLedger(t, w)
			reason := summonoutcome.FilingLedgerUnavailable
			var cause string
			switch kind {
			case "queue-full":
				reason = summonoutcome.FilingQueueFull
				var p v1alpha1.CallerPolicy
				if err := w.base.Get(context.Background(), client.ObjectKeyFromObject(filingPolicy()), &p); err != nil {
					t.Fatal(err)
				}
				p.Spec.Summon.Limits.QueuedUrgent = ptr.To(int32(1))
				if err := w.base.Update(context.Background(), &p); err != nil {
					t.Fatal(err)
				}
				w.add(summonedFor("esc-queue-race", callerA, v1alpha1.LaneEscalation, 0))
			case "hash-mismatch":
				e.Charges[0].Hash = strings.Repeat("a", 64)
				cause = "different request hash"
			case "time-mismatch":
				e.Charges[0].At = e.Charges[0].At.Add(-time.Minute)
				cause = "differs from chargedAt"
			case "entry-full":
				reason = summonoutcome.FilingEntryFull
			}
			raw, err := summonledger.Encode(e)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "entry-full" {
				// A legacy/direct writer consumed the reserved bytes after the
				// charge. Decode still accepts the on-wire 64 KiB document.
				var data map[string]any
				if err := json.Unmarshal([]byte(raw), &data); err != nil {
					t.Fatal(err)
				}
				data["padding"] = ""
				base, err := json.Marshal(data)
				if err != nil {
					t.Fatal(err)
				}
				data["padding"] = strings.Repeat("p", summonledger.MaxEntry-len(base))
				full, err := json.Marshal(data)
				if err != nil {
					t.Fatal(err)
				}
				raw = string(full)
				w.now = w.now.Add(123456789 * time.Nanosecond)
			}
			cm.Data[callerA] = raw
			if err := w.base.Update(context.Background(), cm); err != nil {
				t.Fatal(err)
			}
			before := cm.ResourceVersion
			b, err = w.r.filingLedgerStep(context.Background(), w.get(filingRem), filingMark, "")
			if b.reason != reason {
				t.Fatalf("mark reason=%s, want %s", b.reason, reason)
			}
			if cause != "" && (err == nil || !strings.Contains(err.Error(), cause)) {
				t.Fatalf("lost mismatch cause: %v", err)
			}
			_, after := filingLedger(t, w)
			if before != after.ResourceVersion || len(filingChildren(t, w)) != 0 {
				t.Fatal("refused mark wrote ledger or created")
			}
		})
	}
}
