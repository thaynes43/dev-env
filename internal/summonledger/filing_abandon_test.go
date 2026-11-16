package summonledger

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

func TestPendingFilingKeepsQueueUntilAbandoned(t *testing.T) {
	t0 := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	q := Queue{Urgent: true, Escalations: true}
	rem := queueTestSession("rem-held", queueTestCaller, v1alpha1.LaneRemediation, v1alpha1.SummonPriorityUrgent, &v1alpha1.SummonStatus{
		Closed:     &v1alpha1.SummonClosedStatus{At: metav1.NewTime(t0)},
		Escalation: &v1alpha1.SummonEscalationStatus{State: v1alpha1.SummonEscalationPending},
	})
	charges := []Charge{{ID: FilingID(rem.UID), At: t0, Attempt: t0.Add(24*time.Hour - time.Minute), Name: "esc-rem-held", Hash: RequestHash("remote", "escalation", "esc-rem-held", v1alpha1.SummonPriorityUrgent)}}
	sessions := []v1alpha1.AgentSession{rem}
	// No renewed mark: Pending must still occupy its queue at and past 15 minutes.
	for _, age := range []time.Duration{AttemptHold, 24 * time.Hour, 48 * time.Hour} {
		now := charges[0].Attempt.Add(age)
		if got := QueueCount(queueTestCaller, queueTestPolicy(), charges, sessions, nil, Grants{}, q, "", now); got != 1 {
			t.Fatalf("Pending lost queue place at age %s: %d", age, got)
		}
		if got := Prune(charges, now, Keep(sessions)); len(got) != 1 {
			t.Fatal("Pending charge pruned")
		}
	}
	sessions[0].Status.Summon.Escalation.State = v1alpha1.SummonEscalationAbandoned
	for _, elapsed := range []time.Duration{24 * time.Hour, 24*time.Hour + 14*time.Minute - time.Nanosecond, 24*time.Hour + 14*time.Minute} {
		now := t0.Add(elapsed)
		want := 1
		if elapsed >= 24*time.Hour+14*time.Minute {
			want = 0
		}
		if got := QueueCount(queueTestCaller, queueTestPolicy(), charges, sessions, nil, Grants{}, q, "", now); got != want {
			t.Fatalf("Abandoned queue at %s: got=%d want=%d", elapsed, got, want)
		}
		if got := len(Prune(charges, now, Keep(sessions))); got != want {
			t.Fatalf("Abandoned prune at %s: got=%d want=%d", elapsed, got, want)
		}
	}
	// With an already old mark, both are free as soon as Abandoned is recorded.
	charges[0].Attempt = t0
	if got := Prune(charges, t0.Add(24*time.Hour), Keep(sessions)); len(got) != 0 {
		t.Fatal("old Abandoned charge retained")
	}
}

func TestFilingNameReserveAtByteBound(t *testing.T) {
	at := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	for _, base := range []string{"esc-one", "esc-base-2", strings.Repeat("n", 60)} {
		t.Run(base, func(t *testing.T) {
			e := Entry{Clock: at, extra: map[string]json.RawMessage{"padding": json.RawMessage(`""`)}, Charges: []Charge{{ID: FilingID("one"), At: at, Attempt: at, Name: base, Hash: RequestHash("remote", "escalation", base, v1alpha1.SummonPriorityUrgent)}}}
			raw, err := Encode(e)
			if err != nil {
				t.Fatal(err)
			}
			// Clock and attempt each need ten bytes; the longest suffix needs three.
			padding := MaxEntry - len(raw) - 23
			e.extra["padding"] = json.RawMessage(`"` + strings.Repeat("p", padding+1) + `"`)
			if _, err := Encode(e); err == nil {
				t.Fatal("charge consumed the rebound name reserve")
			}
			e.extra["padding"] = json.RawMessage(`"` + strings.Repeat("p", padding) + `"`)
			raw, err = Encode(e)
			if err != nil || len(raw) != MaxEntry-23 {
				t.Fatalf("charge at bound minus reserve: bytes=%d err=%v", len(raw), err)
			}
			// Every shared writer preserves the remaining reserve, including after -2.
			e.Charges[0].Name = base + "-2"
			if _, err := Encode(e); err != nil {
				t.Fatal("short rebind refused", err)
			}
			e.Clock = at.Add(123456789 * time.Nanosecond)
			e.Charges[0].Attempt = e.Clock
			e.Charges[0].Name = base + "-99"
			raw, err = Encode(e)
			if err != nil || len(raw) != MaxEntry {
				t.Fatalf("longest rebind refused: bytes=%d err=%v", len(raw), err)
			}
			if !e.Charges[0].At.Equal(at) {
				t.Fatal("rebind charged again")
			}
		})
	}
}
