package v1alpha1_test

// The envtest cases of the ScheduledWork CRD (DESIGN-054): bounds on every
// string and list, the reference grammar of notify and calendar, an 8 KiB
// prompt counted in bytes, an owner that never changes, a cancel that cannot be
// undone, and the bounded occurrence record in status.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

const swHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func scheduledWork() *v1alpha1.ScheduledWork {
	return &v1alpha1.ScheduledWork{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("sw-t-%d", seq.Add(1)), Namespace: ns},
		Spec: v1alpha1.ScheduledWorkSpec{
			Title:    "Loki retention trend audit",
			Owner:    "human/dev-env-system/tom",
			Editor:   "human/dev-env-system/tom",
			Due:      "2026-12-03T09:00",
			TimeZone: "America/New_York",
			NotAfter: metav1.NewTime(time.Now().Add(90 * 24 * time.Hour).Truncate(time.Second)),
			Mode:     v1alpha1.ScheduledWorkHeadless,
			Work: v1alpha1.ScheduledWorkOrder{
				Template: "ops-audit",
				Repo:     "haynes-ops",
				Prompt:   "Audit the Loki retention trend and report.",
			},
			Notify:   []string{"pushover:default"},
			Calendar: "calendar:haynesnetwork",
		},
	}
}

func TestScheduledWorkAccepts(t *testing.T) {
	if err := k8s.Create(ctx(t), scheduledWork()); err != nil {
		t.Fatalf("create: %v", err)
	}
	interactive := scheduledWork()
	interactive.Spec.Mode = v1alpha1.ScheduledWorkInteractive
	interactive.Spec.Owner = "session/dev-env-1104-010332/0f5e6a3c-1b2d-4e5f-8a9b-0c1d2e3f4a5b"
	interactive.Spec.Editor = interactive.Spec.Owner
	interactive.Spec.Recurrence = &v1alpha1.ReminderRecurrence{Freq: v1alpha1.ReminderWeekly, ByDay: []string{"MO", "TH"}, Count: 10}
	interactive.Spec.Notify = nil
	interactive.Spec.Calendar = ""
	if err := k8s.Create(ctx(t), interactive); err != nil {
		t.Fatalf("an interactive weekly item with no notify and no calendar: %v", err)
	}
	ascii := scheduledWork()
	ascii.Spec.Work.Prompt = strings.Repeat("x", 8192)
	if err := k8s.Create(ctx(t), ascii); err != nil {
		t.Fatalf("a prompt of 8192 ASCII bytes: %v", err)
	}
	wide := scheduledWork()
	wide.Spec.Work.Prompt = strings.Repeat("é", 4096)
	if err := k8s.Create(ctx(t), wide); err != nil {
		t.Fatalf("a prompt of 4096 two-byte characters, 8192 bytes: %v", err)
	}
	full := scheduledWork()
	full.Spec.Notify = nil
	for i := range 8 {
		full.Spec.Notify = append(full.Spec.Notify, fmt.Sprintf("pushover:p%d", i))
	}
	if err := k8s.Create(ctx(t), full); err != nil {
		t.Fatalf("eight notify references: %v", err)
	}
}

func TestScheduledWorkRefuses(t *testing.T) {
	long := func(n int) string { return strings.Repeat("x", n) }
	for name, c := range map[string]struct {
		edit func(*v1alpha1.ScheduledWork)
		want string
	}{
		"no title":          {func(w *v1alpha1.ScheduledWork) { w.Spec.Title = "" }, "spec.title"},
		"a long title":      {func(w *v1alpha1.ScheduledWork) { w.Spec.Title = long(121) }, "spec.title"},
		"no owner":          {func(w *v1alpha1.ScheduledWork) { w.Spec.Owner = "" }, "spec.owner"},
		"a long owner":      {func(w *v1alpha1.ScheduledWork) { w.Spec.Owner = long(257) }, "spec.owner"},
		"no editor":         {func(w *v1alpha1.ScheduledWork) { w.Spec.Editor = "" }, "spec.editor"},
		"a long editor":     {func(w *v1alpha1.ScheduledWork) { w.Spec.Editor = long(257) }, "spec.editor"},
		"a due with a zone": {func(w *v1alpha1.ScheduledWork) { w.Spec.Due = "2026-12-03T09:00Z" }, "spec.due"},
		"a spaced zone":     {func(w *v1alpha1.ScheduledWork) { w.Spec.TimeZone = "New York" }, "spec.timeZone"},
		"no notAfter":       {func(w *v1alpha1.ScheduledWork) { w.Spec.NotAfter = metav1.Time{} }, "spec.notAfter"},
		"no mode":           {func(w *v1alpha1.ScheduledWork) { w.Spec.Mode = "" }, "spec.mode"},
		"an unknown mode":   {func(w *v1alpha1.ScheduledWork) { w.Spec.Mode = "batch" }, "spec.mode"},
		"no template":       {func(w *v1alpha1.ScheduledWork) { w.Spec.Work.Template = "" }, "spec.work.template"},
		"a bad template":    {func(w *v1alpha1.ScheduledWork) { w.Spec.Work.Template = "Ops_Audit" }, "spec.work.template"},
		"a long template":   {func(w *v1alpha1.ScheduledWork) { w.Spec.Work.Template = long(64) }, "spec.work.template"},
		"no repo":           {func(w *v1alpha1.ScheduledWork) { w.Spec.Work.Repo = "" }, "spec.work.repo"},
		"a repo path":       {func(w *v1alpha1.ScheduledWork) { w.Spec.Work.Repo = "thaynes43/haynes-ops" }, "spec.work.repo"},
		"a repo of ..":      {func(w *v1alpha1.ScheduledWork) { w.Spec.Work.Repo = ".." }, "repo is a repository name, not a path"},
		"no prompt":         {func(w *v1alpha1.ScheduledWork) { w.Spec.Work.Prompt = "" }, "spec.work.prompt"},
		"8193 characters":   {func(w *v1alpha1.ScheduledWork) { w.Spec.Work.Prompt = long(8193) }, "spec.work.prompt"},
		"8194 bytes in 4097 characters": {func(w *v1alpha1.ScheduledWork) {
			w.Spec.Work.Prompt = strings.Repeat("é", 4097)
		}, "prompt is at most 8192 bytes"},
		"a notify address":     {func(w *v1alpha1.ScheduledWork) { w.Spec.Notify = []string{"pushover:tom@example.com"} }, "spec.notify[0]"},
		"a notify provider":    {func(w *v1alpha1.ScheduledWork) { w.Spec.Notify = []string{"email:default"} }, "spec.notify[0]"},
		"a calendar in notify": {func(w *v1alpha1.ScheduledWork) { w.Spec.Notify = []string{"calendar:haynesnetwork"} }, "spec.notify[0]"},
		"a bare notify key":    {func(w *v1alpha1.ScheduledWork) { w.Spec.Notify = []string{"uQiRzpo4DXghDmr9QzzfQu27cmVRsG"} }, "spec.notify[0]"},
		"a notify duplicate": {func(w *v1alpha1.ScheduledWork) {
			w.Spec.Notify = []string{"pushover:default", "pushover:default"}
		}, "spec.notify[1]"},
		"nine notify references": {func(w *v1alpha1.ScheduledWork) {
			w.Spec.Notify = nil
			for i := range 9 {
				w.Spec.Notify = append(w.Spec.Notify, fmt.Sprintf("pushover:p%d", i))
			}
		}, "spec.notify"},
		"a pushover calendar": {func(w *v1alpha1.ScheduledWork) { w.Spec.Calendar = "pushover:default" }, "spec.calendar"},
		"a calendar address":  {func(w *v1alpha1.ScheduledWork) { w.Spec.Calendar = "calendar:admin@haynesnetwork.com" }, "spec.calendar"},
		"a bare calendar":     {func(w *v1alpha1.ScheduledWork) { w.Spec.Calendar = "haynesnetwork" }, "spec.calendar"},
		"until and count": {func(w *v1alpha1.ScheduledWork) {
			u := metav1.Now()
			w.Spec.Recurrence = &v1alpha1.ReminderRecurrence{Freq: v1alpha1.ReminderDaily, Count: 3, Until: &u}
		}, "until and count are exclusive"},
		"byDay on a monthly": {func(w *v1alpha1.ScheduledWork) {
			w.Spec.Recurrence = &v1alpha1.ReminderRecurrence{Freq: v1alpha1.ReminderMonthly, ByDay: []string{"MO"}}
		}, "byDay is for a weekly recurrence"},
	} {
		t.Run(name, func(t *testing.T) {
			w := scheduledWork()
			c.edit(w)
			wantInvalid(t, k8s.Create(ctx(t), w), c.want)
		})
	}
}

func TestScheduledWorkOwnerNeverChangesButTheEditorDoes(t *testing.T) {
	w := scheduledWork()
	if err := k8s.Create(ctx(t), w); err != nil {
		t.Fatal(err)
	}
	w.Spec.Title = "Loki retention audit, monthly"
	w.Spec.Editor = "client/dev-env/workbench"
	w.Spec.Mode = v1alpha1.ScheduledWorkInteractive
	w.Spec.Work.Prompt = "Wait for Tom, then audit."
	if err := k8s.Update(ctx(t), w); err != nil {
		t.Fatalf("an edit by another editor: %v", err)
	}
	w.Spec.Owner = "session/someone-else/uid"
	wantInvalid(t, k8s.Update(ctx(t), w), "the owner of scheduled work never changes")
}

func TestScheduledWorkCancelCannotBeUndone(t *testing.T) {
	w := scheduledWork()
	if err := k8s.Create(ctx(t), w); err != nil {
		t.Fatal(err)
	}
	w.Spec.Cancelled = true
	w.Spec.Editor = "coordinator/dev-agents/dev-env-1027-010058"
	if err := k8s.Update(ctx(t), w); err != nil {
		t.Fatalf("a cancel: %v", err)
	}
	w.Spec.Title = "still cancelled, retitled"
	if err := k8s.Update(ctx(t), w); err != nil {
		t.Fatalf("an edit that keeps the cancel: %v", err)
	}
	w.Spec.Cancelled = false
	wantInvalid(t, k8s.Update(ctx(t), w), "cancelled scheduled work stays cancelled")
}

func occurrence(at time.Time) v1alpha1.ScheduledWorkOccurrence {
	claimed := metav1.NewTime(at.Add(time.Second))
	return v1alpha1.ScheduledWorkOccurrence{
		At:        metav1.NewTime(at),
		State:     v1alpha1.OccurrenceSucceeded,
		Reason:    "task succeeded",
		ClaimedAt: &claimed,
		Session:   "sw-1a2b3c4d5e6f-202612031400",
		SpecHash:  swHash,
		Execution: &v1alpha1.ScheduledWorkExecution{State: v1alpha1.TaskResultSucceeded, Reason: "TaskSucceeded"},
		Reported: &v1alpha1.TaskOutcomeRecordStatus{
			Summary:  "Retention trend flat; no change proposed.",
			PRURLs:   []string{"https://github.com/thaynes43/haynes-ops/pull/4100"},
			HeadSHAs: []string{"0123456789abcdef0123456789abcdef01234567"},
		},
		Pages: []v1alpha1.ScheduledWorkPage{
			{Event: v1alpha1.PageEventDue, Reminder: "sw-1a2b3c4d5e6f-202612031400-due", State: v1alpha1.DeliverySent},
			{Event: v1alpha1.PageEventResult, Reminder: "sw-1a2b3c4d5e6f-202612031400-result"},
		},
	}
}

func TestScheduledWorkStatusAccepts(t *testing.T) {
	w := scheduledWork()
	if err := k8s.Create(ctx(t), w); err != nil {
		t.Fatal(err)
	}
	now := metav1.NewTime(time.Now().Truncate(time.Second))
	w.Status = v1alpha1.ScheduledWorkStatus{
		Approval: &v1alpha1.ScheduledWorkApproval{By: "human/dev-env-system/tom", At: now, SpecHash: swHash},
		NextDue:  &now,
		PlanHash: strings.Repeat("a", 32),
		Counts:   v1alpha1.ScheduledWorkCounts{Succeeded: 1, Failed: 2, Parked: 3, Skipped: 4},
	}
	start := time.Date(2026, 12, 3, 14, 0, 0, 0, time.UTC)
	for i := range v1alpha1.ScheduledWorkMaxOccurrences {
		w.Status.Occurrences = append(w.Status.Occurrences, occurrence(start.Add(time.Duration(i)*24*time.Hour)))
	}
	interactive := &w.Status.Occurrences[1]
	interactive.State = v1alpha1.OccurrenceEnded
	interactive.Reason = "participation not observed"
	interactive.Delivery = v1alpha1.WorkOrderAmbiguous
	interactive.Execution, interactive.Reported = nil, nil
	interactive.Pages = []v1alpha1.ScheduledWorkPage{
		{Event: v1alpha1.PageEventDue, State: v1alpha1.DeliverySkipped},
		{Event: v1alpha1.PageEventLink, Reminder: "sw-x-link", State: v1alpha1.DeliveryAmbiguous},
		{Event: v1alpha1.PageEventFailed, Reminder: "sw-x-failed", State: v1alpha1.DeliveryFailed},
		{Event: v1alpha1.PageEventResult, Reminder: "sw-x-result", State: v1alpha1.DeliverySent},
	}
	retried := &w.Status.Occurrences[3]
	retriedAt := metav1.NewTime(start.Add(3*24*time.Hour + 2*time.Hour))
	retried.RetriedAt = &retriedAt
	retried.State, retried.Reason = v1alpha1.OccurrenceRunning, "approved after park"
	parked := &w.Status.Occurrences[4]
	parked.State, parked.Reason, parked.Session = v1alpha1.OccurrenceParked, "no standing authority", ""
	parked.Execution, parked.Reported = nil, nil
	parked.Pages = []v1alpha1.ScheduledWorkPage{{Event: v1alpha1.PageEventParked, Reminder: "sw-x-parked"}}
	skipped := &w.Status.Occurrences[2]
	skipped.State, skipped.Reason, skipped.Missed = v1alpha1.OccurrenceSkipped, "missed beyond grace", 7
	if err := k8s.Status().Update(ctx(t), w); err != nil {
		t.Fatalf("a full status of %d occurrences: %v", v1alpha1.ScheduledWorkMaxOccurrences, err)
	}
}

func TestScheduledWorkStatusRefuses(t *testing.T) {
	for name, c := range map[string]struct {
		edit func(*v1alpha1.ScheduledWorkStatus)
		want string
	}{
		"an approval by a session": {func(s *v1alpha1.ScheduledWorkStatus) {
			s.Approval.By = "session/dev-env-1104-010332/uid"
		}, "status.approval.by"},
		"a short approval hash": {func(s *v1alpha1.ScheduledWorkStatus) {
			s.Approval.SpecHash = swHash[:32]
		}, "status.approval.specHash"},
		"a negative count": {func(s *v1alpha1.ScheduledWorkStatus) { s.Counts.Failed = -1 }, "status.counts.failed"},
		"a long plan hash": {func(s *v1alpha1.ScheduledWorkStatus) { s.PlanHash = strings.Repeat("a", 65) }, "status.planHash"},
		"65 occurrences": {func(s *v1alpha1.ScheduledWorkStatus) {
			start := time.Date(2026, 12, 3, 14, 0, 0, 0, time.UTC)
			s.Occurrences = nil
			for i := range v1alpha1.ScheduledWorkMaxOccurrences + 1 {
				s.Occurrences = append(s.Occurrences, occurrence(start.Add(time.Duration(i)*time.Hour)))
			}
		}, "status.occurrences"},
		"an unknown state": {func(s *v1alpha1.ScheduledWorkStatus) { s.Occurrences[0].State = "Done" }, "status.occurrences[0].state"},
		"a long reason":    {func(s *v1alpha1.ScheduledWorkStatus) { s.Occurrences[0].Reason = strings.Repeat("r", 257) }, "status.occurrences[0].reason"},
		"a negative missed count": {func(s *v1alpha1.ScheduledWorkStatus) {
			s.Occurrences[0].Missed = -1
		}, "status.occurrences[0].missed"},
		"a session name not derived": {func(s *v1alpha1.ScheduledWorkStatus) {
			s.Occurrences[0].Session = "sw-1A2B3C4D5E6F-202612031400"
		}, "status.occurrences[0].session"},
		"a session name with seconds": {func(s *v1alpha1.ScheduledWorkStatus) {
			s.Occurrences[0].Session = "sw-1a2b3c4d5e6f-20261203140000"
		}, "status.occurrences[0].session"},
		"a short occurrence hash": {func(s *v1alpha1.ScheduledWorkStatus) {
			s.Occurrences[0].SpecHash = "abc"
		}, "status.occurrences[0].specHash"},
		"an unknown delivery": {func(s *v1alpha1.ScheduledWorkStatus) {
			s.Occurrences[0].Delivery = "lost"
		}, "status.occurrences[0].delivery"},
		"an unknown execution state": {func(s *v1alpha1.ScheduledWorkStatus) {
			s.Occurrences[0].Execution.State = "Crashed"
		}, "status.occurrences[0].execution.state"},
		"a long execution reason": {func(s *v1alpha1.ScheduledWorkStatus) {
			s.Occurrences[0].Execution.Reason = strings.Repeat("R", 129)
		}, "status.occurrences[0].execution.reason"},
		"a long reported summary": {func(s *v1alpha1.ScheduledWorkStatus) {
			s.Occurrences[0].Reported.Summary = strings.Repeat("s", 201)
		}, "status.occurrences[0].reported.summary"},
		"a reported PR elsewhere": {func(s *v1alpha1.ScheduledWorkStatus) {
			s.Occurrences[0].Reported.PRURLs = []string{"https://github.com/someone/haynes-ops/pull/1"}
		}, "status.occurrences[0].reported.prURLs[0]"},
		"four reported heads": {func(s *v1alpha1.ScheduledWorkStatus) {
			h := "0123456789abcdef0123456789abcdef01234567"
			s.Occurrences[0].Reported.HeadSHAs = []string{h, h, h, h}
		}, "status.occurrences[0].reported.headSHAs"},
		"an unknown page event": {func(s *v1alpha1.ScheduledWorkStatus) {
			s.Occurrences[0].Pages[0].Event = "reminder"
		}, "status.occurrences[0].pages[0].event"},
		"a page claim state": {func(s *v1alpha1.ScheduledWorkStatus) {
			s.Occurrences[0].Pages[0].State = v1alpha1.DeliveryClaimed
		}, "status.occurrences[0].pages[0].state"},
		"a page reminder not a name": {func(s *v1alpha1.ScheduledWorkStatus) {
			s.Occurrences[0].Pages[0].Reminder = "Rem_1"
		}, "status.occurrences[0].pages[0].reminder"},
		"two pages for one event": {func(s *v1alpha1.ScheduledWorkStatus) {
			s.Occurrences[0].Pages[1].Event = v1alpha1.PageEventDue
		}, "status.occurrences[0].pages[1]"},
		"five pages": {func(s *v1alpha1.ScheduledWorkStatus) {
			s.Occurrences[0].Pages = nil
			for _, e := range []string{v1alpha1.PageEventDue, v1alpha1.PageEventLink, v1alpha1.PageEventParked, v1alpha1.PageEventFailed, v1alpha1.PageEventResult} {
				s.Occurrences[0].Pages = append(s.Occurrences[0].Pages, v1alpha1.ScheduledWorkPage{Event: e, State: v1alpha1.DeliverySkipped})
			}
		}, "status.occurrences[0].pages"},
	} {
		t.Run(name, func(t *testing.T) {
			w := scheduledWork()
			if err := k8s.Create(ctx(t), w); err != nil {
				t.Fatal(err)
			}
			now := metav1.NewTime(time.Now().Truncate(time.Second))
			w.Status = v1alpha1.ScheduledWorkStatus{
				Approval:    &v1alpha1.ScheduledWorkApproval{By: "human/dev-env-system/tom", At: now, SpecHash: swHash},
				Occurrences: []v1alpha1.ScheduledWorkOccurrence{occurrence(time.Date(2026, 12, 3, 14, 0, 0, 0, time.UTC))},
			}
			c.edit(&w.Status)
			wantInvalid(t, k8s.Status().Update(ctx(t), w), c.want)
		})
	}
}

// OccurrenceTerminal decides what the executor may prune: the design's final
// states, and never Parked, which an approval or retry claims again.
func TestOccurrenceTerminal(t *testing.T) {
	terminal := map[string]bool{
		v1alpha1.OccurrencePending: false, v1alpha1.OccurrenceClaimed: false, v1alpha1.OccurrenceDispatched: false,
		v1alpha1.OccurrenceDelivered: false, v1alpha1.OccurrenceOpened: false, v1alpha1.OccurrenceRunning: false,
		v1alpha1.OccurrenceParked:    false,
		v1alpha1.OccurrenceSucceeded: true, v1alpha1.OccurrenceFailed: true, v1alpha1.OccurrenceEnded: true,
		v1alpha1.OccurrenceSkipped: true, v1alpha1.OccurrenceAmbiguous: true, v1alpha1.OccurrenceCancelled: true,
	}
	for state, want := range terminal {
		if got := v1alpha1.OccurrenceTerminal(state); got != want {
			t.Errorf("OccurrenceTerminal(%s) = %v, want %v", state, got, want)
		}
	}
	if v1alpha1.OccurrenceTerminal("") || v1alpha1.OccurrenceTerminal("succeeded") {
		t.Error("an empty or miscased state is terminal")
	}
}
