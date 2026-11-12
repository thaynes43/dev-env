package v1alpha1_test

// OP779 (ruling D9 on item 741, DESIGN-064 6.8): status.summon.pages[] is
// a bounded, append-only list of {event, at}. Each rule accepts and refuses.

import (
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

func TestSummonPagesAreBoundedAndAppendOnly(t *testing.T) {
	s := summonedRemote()
	if err := k8s.Create(ctx(t), s); err != nil {
		t.Fatal(err)
	}
	at := metav1.NewTime(time.Date(2026, 11, 12, 16, 0, 0, 0, time.UTC))
	later := metav1.NewTime(at.Add(24 * time.Hour))
	write := func(pages ...v1alpha1.SummonPage) error {
		cur := get(t, s)
		if cur.Status.Summon == nil {
			cur.Status.Summon = &v1alpha1.SummonStatus{}
		}
		cur.Status.Summon.Pages = pages
		return k8s.Status().Update(ctx(t), cur)
	}
	held := v1alpha1.SummonPage{Event: v1alpha1.PageEscalationHeld, At: at}
	reminder := v1alpha1.SummonPage{Event: v1alpha1.PageEscalationReminder, At: later}
	// Accepted: the first event, then a second appended.
	if err := write(held); err != nil {
		t.Fatalf("first page refused: %v", err)
	}
	if err := write(held, reminder); err != nil {
		t.Fatalf("appended page refused: %v", err)
	}
	// Refused: an event removed, an event changed, the list dropped.
	for name, pages := range map[string][]v1alpha1.SummonPage{
		"removed": {reminder},
		"changed": {{Event: v1alpha1.PageEscalationHeld, At: later}, reminder},
		"dropped": nil,
	} {
		if err := write(pages...); !apierrors.IsInvalid(err) {
			t.Errorf("%s: want Invalid, got %v", name, err)
		}
	}
	if got := get(t, s).Status.Summon.Pages; len(got) != 2 || got[0].Event != held.Event || got[1].Event != reminder.Event {
		t.Fatalf("stored pages changed: %+v", got)
	}

	// Refused: an event outside the enum, and a ninth entry.
	other := summonedRemote()
	if err := k8s.Create(ctx(t), other); err != nil {
		t.Fatal(err)
	}
	cur := get(t, other)
	cur.Status.Summon = &v1alpha1.SummonStatus{Pages: []v1alpha1.SummonPage{{Event: "escalated-twice", At: at}}}
	if err := k8s.Status().Update(ctx(t), cur); !apierrors.IsInvalid(err) {
		t.Errorf("unknown event: want Invalid, got %v", err)
	}
	cur = get(t, other)
	cur.Status.Summon = &v1alpha1.SummonStatus{}
	for range 8 {
		cur.Status.Summon.Pages = append(cur.Status.Summon.Pages, held)
	}
	if err := k8s.Status().Update(ctx(t), cur); err != nil {
		t.Fatalf("eight pages refused: %v", err)
	}
	cur = get(t, other)
	cur.Status.Summon.Pages = append(cur.Status.Summon.Pages, reminder)
	if err := k8s.Status().Update(ctx(t), cur); !apierrors.IsInvalid(err) {
		t.Errorf("a ninth page: want Invalid, got %v", err)
	}
}
