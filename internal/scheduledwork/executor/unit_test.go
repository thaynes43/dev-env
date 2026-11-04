package executor

import (
	"fmt"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// record keeps the occurrences in instant order, the newest
// ScheduledWorkTerminalKept terminal ones and every one that is not final.
func TestRecordPrunesOnlyTheOldestTerminal(t *testing.T) {
	w := &v1alpha1.ScheduledWork{}
	base := time.Date(2030, 1, 1, 9, 0, 0, 0, time.UTC)
	parked := base.Add(time.Hour)
	for i := range v1alpha1.ScheduledWorkTerminalKept {
		state := v1alpha1.OccurrenceSucceeded
		at := base.Add(time.Duration(i+2) * 24 * time.Hour)
		record(w, v1alpha1.ScheduledWorkOccurrence{At: metav1.NewTime(at), State: state})
	}
	// A Parked occurrence older than every terminal one is never pruned.
	record(w, v1alpha1.ScheduledWorkOccurrence{At: metav1.NewTime(parked), State: v1alpha1.OccurrenceParked})
	newest := base.Add(time.Duration(v1alpha1.ScheduledWorkTerminalKept+5) * 24 * time.Hour)
	record(w, v1alpha1.ScheduledWorkOccurrence{At: metav1.NewTime(newest), State: v1alpha1.OccurrenceSkipped})
	occ := w.Status.Occurrences
	if len(occ) != v1alpha1.ScheduledWorkTerminalKept+1 {
		t.Fatalf("%d occurrences, want %d", len(occ), v1alpha1.ScheduledWorkTerminalKept+1)
	}
	if !occ[0].At.Time.Equal(parked) || occ[0].State != v1alpha1.OccurrenceParked {
		t.Errorf("the first is %s %s, want the Parked one", occ[0].At, occ[0].State)
	}
	if !occ[1].At.Time.Equal(base.Add(3 * 24 * time.Hour)) {
		t.Errorf("the oldest terminal kept is %s; the oldest one should be pruned", occ[1].At)
	}
	if !occ[len(occ)-1].At.Time.Equal(newest) {
		t.Errorf("the last is %s, want the newest", occ[len(occ)-1].At)
	}
	for i := 1; i < len(occ); i++ {
		if !occ[i-1].At.Before(&occ[i].At) {
			t.Fatalf("occurrences out of order at %d", i)
		}
	}
}

// The plan hash covers the schedule fields and notAfter, and nothing else.
func TestPlanHash(t *testing.T) {
	base := v1alpha1.ScheduledWorkSpec{Due: "2030-06-03T14:00", TimeZone: "UTC", NotAfter: metav1.NewTime(time.Date(2030, 9, 1, 0, 0, 0, 0, time.UTC)), Title: "a"}
	h := planHash(base)
	if len(h) != 32 {
		t.Fatalf("hash %q", h)
	}
	for name, edit := range map[string]func(*v1alpha1.ScheduledWorkSpec){
		"due":  func(s *v1alpha1.ScheduledWorkSpec) { s.Due = "2030-06-03T15:00" },
		"zone": func(s *v1alpha1.ScheduledWorkSpec) { s.TimeZone = "America/New_York" },
		"recurrence": func(s *v1alpha1.ScheduledWorkSpec) {
			s.Recurrence = &v1alpha1.ReminderRecurrence{Freq: v1alpha1.ReminderDaily}
		},
		"notAfter": func(s *v1alpha1.ScheduledWorkSpec) { s.NotAfter = metav1.NewTime(s.NotAfter.Add(time.Hour)) },
	} {
		s := base
		edit(&s)
		if planHash(s) == h {
			t.Errorf("an edit of %s keeps the plan hash", name)
		}
	}
	s := base
	s.Title, s.Cancelled, s.Work.Prompt = "b", true, "other"
	if planHash(s) != h {
		t.Error("an edit outside the schedule changed the plan hash")
	}
}

// The request carries only what the template fixes and the work order.
func TestRequestFromTheTemplate(t *testing.T) {
	w := &v1alpha1.ScheduledWork{Spec: v1alpha1.ScheduledWorkSpec{Mode: v1alpha1.ScheduledWorkHeadless,
		Work: v1alpha1.ScheduledWorkOrder{Template: "ops-audit", Repo: "haynes-ops", Prompt: "do it"}}}
	req, reason := request(w, opsAudit)
	if reason != "" || req.Mode != "task" || req.Prompt != "do it" || req.Limits == nil || req.Limits.Timeout != "40m0s" ||
		req.Agent != "claude" || req.Model != "claude-opus-5-5" || req.Profile != "dev" || req.Lifecycle != nil || req.IdempotencyKey != "" {
		t.Errorf("headless request %+v (%q)", req, reason)
	}
	noMax := opsAudit
	noMax.MaxDuration = 0
	if _, reason := request(w, noMax); reason == "" {
		t.Error("headless work without a maximum duration was accepted")
	}
	w.Spec.Mode = v1alpha1.ScheduledWorkInteractive
	req, reason = request(w, opsAudit)
	if reason != "" || req.Mode != "remote" || req.Prompt != "" || req.Lifecycle == nil || req.Lifecycle.IdleSuspendAfter != DefaultIdleSuspendAfter.String() || req.Limits != nil {
		t.Errorf("interactive request %+v (%q)", req, reason)
	}
	if got := missedReason(3, time.Date(2030, 6, 5, 14, 0, 0, 0, time.UTC), DefaultHeadlessGrace); got != fmt.Sprintf("missed beyond grace (15m0s): 3 occurrences, the last at %s", "2030-06-05T14:00:00Z") {
		t.Errorf("missed reason %q", got)
	}
}
