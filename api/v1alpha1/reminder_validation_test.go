package v1alpha1_test

// The envtest cases of the Reminder CRD (DESIGN-032): bounds on every string,
// list and number, the recurrence rules, and an owner that never changes.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

func reminder() *v1alpha1.Reminder {
	return &v1alpha1.Reminder{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("rem-t-%d", seq.Add(1)), Namespace: ns},
		Spec: v1alpha1.ReminderSpec{
			Title:    "Renew the VPN",
			Body:     "Pay for the month",
			Owner:    "session/haynes-ops-1028-010000",
			Due:      "2026-11-02T09:00",
			TimeZone: "America/New_York",
			Targets:  []string{"pushover:default", "calendar:haynesnetwork"},
		},
	}
}

func TestReminderAccepts(t *testing.T) {
	r := reminder()
	if err := k8s.Create(ctx(t), r); err != nil {
		t.Fatalf("create: %v", err)
	}
	weekly := reminder()
	weekly.Spec.Recurrence = &v1alpha1.ReminderRecurrence{Freq: v1alpha1.ReminderWeekly, ByDay: []string{"MO", "TH"}, Count: 10}
	if err := k8s.Create(ctx(t), weekly); err != nil {
		t.Fatalf("a weekly recurrence: %v", err)
	}
	full := reminder()
	full.Spec.Body = strings.Repeat("x", 1024)
	if err := k8s.Create(ctx(t), full); err != nil {
		t.Fatalf("a body of Pushover's 1024: %v", err)
	}
	until := metav1.NewTime(time.Now().Add(90 * 24 * time.Hour))
	monthly := reminder()
	monthly.Spec.Recurrence = &v1alpha1.ReminderRecurrence{Freq: v1alpha1.ReminderMonthly, ByMonthDay: 31, Until: &until}
	if err := k8s.Create(ctx(t), monthly); err != nil {
		t.Fatalf("a monthly recurrence: %v", err)
	}
}

func TestReminderRefuses(t *testing.T) {
	long := func(n int) string { return strings.Repeat("x", n) }
	for name, c := range map[string]struct {
		edit func(*v1alpha1.Reminder)
		want string
	}{
		"no title":          {func(r *v1alpha1.Reminder) { r.Spec.Title = "" }, "spec.title"},
		"a long title":      {func(r *v1alpha1.Reminder) { r.Spec.Title = long(121) }, "spec.title"},
		"a long body":       {func(r *v1alpha1.Reminder) { r.Spec.Body = long(1025) }, "spec.body"},
		"no owner":          {func(r *v1alpha1.Reminder) { r.Spec.Owner = "" }, "spec.owner"},
		"a long owner":      {func(r *v1alpha1.Reminder) { r.Spec.Owner = long(257) }, "spec.owner"},
		"a due with a zone": {func(r *v1alpha1.Reminder) { r.Spec.Due = "2026-11-02T09:00Z" }, "spec.due"},
		"a due on seconds":  {func(r *v1alpha1.Reminder) { r.Spec.Due = "2026-11-02T09:00:30" }, "spec.due"},
		"no time zone":      {func(r *v1alpha1.Reminder) { r.Spec.TimeZone = "" }, "spec.timeZone"},
		"a spaced zone":     {func(r *v1alpha1.Reminder) { r.Spec.TimeZone = "New York" }, "spec.timeZone"},
		"a long zone":       {func(r *v1alpha1.Reminder) { r.Spec.TimeZone = "A" + long(64) }, "spec.timeZone"},
		"no targets":        {func(r *v1alpha1.Reminder) { r.Spec.Targets = nil }, "spec.targets"},
		"nine targets": {func(r *v1alpha1.Reminder) {
			r.Spec.Targets = nil
			for i := range 9 {
				r.Spec.Targets = append(r.Spec.Targets, fmt.Sprintf("calendar:c%d", i))
			}
		}, "spec.targets"},
		"an address":          {func(r *v1alpha1.Reminder) { r.Spec.Targets = []string{"calendar:tom@example.com"} }, "spec.targets[0]"},
		"a bare destination":  {func(r *v1alpha1.Reminder) { r.Spec.Targets = []string{"uQiRzpo4DXghDmr9QzzfQu27cmVRsG"} }, "spec.targets[0]"},
		"an unknown provider": {func(r *v1alpha1.Reminder) { r.Spec.Targets = []string{"email:default"} }, "spec.targets[0]"},
		"an unknown freq": {func(r *v1alpha1.Reminder) {
			r.Spec.Recurrence = &v1alpha1.ReminderRecurrence{Freq: "yearly"}
		}, "spec.recurrence.freq"},
		"byDay on a daily": {func(r *v1alpha1.Reminder) {
			r.Spec.Recurrence = &v1alpha1.ReminderRecurrence{Freq: v1alpha1.ReminderDaily, ByDay: []string{"MO"}}
		}, "byDay is for a weekly recurrence"},
		"a bad weekday": {func(r *v1alpha1.Reminder) {
			r.Spec.Recurrence = &v1alpha1.ReminderRecurrence{Freq: v1alpha1.ReminderWeekly, ByDay: []string{"XX"}}
		}, "spec.recurrence.byDay"},
		"byMonthDay on a weekly": {func(r *v1alpha1.Reminder) {
			r.Spec.Recurrence = &v1alpha1.ReminderRecurrence{Freq: v1alpha1.ReminderWeekly, ByMonthDay: 3}
		}, "byMonthDay is for a monthly recurrence"},
		"day 32": {func(r *v1alpha1.Reminder) {
			r.Spec.Recurrence = &v1alpha1.ReminderRecurrence{Freq: v1alpha1.ReminderMonthly, ByMonthDay: 32}
		}, "spec.recurrence.byMonthDay"},
		"a count over 1000": {func(r *v1alpha1.Reminder) {
			r.Spec.Recurrence = &v1alpha1.ReminderRecurrence{Freq: v1alpha1.ReminderDaily, Count: 1001}
		}, "spec.recurrence.count"},
		"until and count": {func(r *v1alpha1.Reminder) {
			u := metav1.Now()
			r.Spec.Recurrence = &v1alpha1.ReminderRecurrence{Freq: v1alpha1.ReminderDaily, Count: 3, Until: &u}
		}, "until and count are exclusive"},
	} {
		t.Run(name, func(t *testing.T) {
			r := reminder()
			c.edit(r)
			wantInvalid(t, k8s.Create(ctx(t), r), c.want)
		})
	}
}

func TestReminderOwnerNeverChangesButTheRestEdits(t *testing.T) {
	r := reminder()
	if err := k8s.Create(ctx(t), r); err != nil {
		t.Fatal(err)
	}
	r.Spec.Title = "Renew the VPN today"
	r.Spec.Cancelled = true
	if err := k8s.Update(ctx(t), r); err != nil {
		t.Fatalf("an edit: %v", err)
	}
	r.Spec.Owner = "session/someone-else"
	wantInvalid(t, k8s.Update(ctx(t), r), "the owner of a reminder never changes")
}

func TestReminderStatusBounds(t *testing.T) {
	r := reminder()
	if err := k8s.Create(ctx(t), r); err != nil {
		t.Fatal(err)
	}
	now := metav1.Now()
	r.Status = v1alpha1.ReminderStatus{State: v1alpha1.ReminderPending, NextDue: &now, Occurrence: 1,
		Deliveries: []v1alpha1.ReminderDelivery{{Target: "pushover:default", Occurrence: 1, State: v1alpha1.DeliveryClaimed, Attempts: 1, UpdatedAt: now}}}
	if err := k8s.Status().Update(ctx(t), r); err != nil {
		t.Fatalf("a status: %v", err)
	}
	r.Status.State = v1alpha1.ReminderSkipped
	if err := k8s.Status().Update(ctx(t), r); err != nil {
		t.Fatalf("the skipped state: %v", err)
	}
	r.Status.State = "mostly-sent"
	wantInvalid(t, k8s.Status().Update(ctx(t), r), "status.state")
	r.Status.State = v1alpha1.ReminderPending
	r.Status.Deliveries[0].State = "lost"
	wantInvalid(t, k8s.Status().Update(ctx(t), r), "status.deliveries[0].state")
	r.Status.Deliveries[0].State = v1alpha1.DeliverySent
	for range 21 {
		r.Status.Attempts = append(r.Status.Attempts, v1alpha1.ReminderAttempt{At: now, Occurrence: 1, Outcome: "sent"})
	}
	wantInvalid(t, k8s.Status().Update(ctx(t), r), "status.attempts")
}
