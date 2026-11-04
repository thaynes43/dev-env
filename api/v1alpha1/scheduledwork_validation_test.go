package v1alpha1_test

// The envtest cases of the ScheduledWork CRD (DESIGN-054): bounds on every
// string and list, the reference grammar of notify and calendar, the caller
// form of owner and editor, an 8 KiB prompt counted in bytes, an owner that
// never changes, a cancel that cannot be undone by any write, a page target
// for interactive work, and the bounded occurrence record in status with its
// transition rules.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

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
	interactive.Spec.Calendar = ""
	if err := k8s.Create(ctx(t), interactive); err != nil {
		t.Fatalf("an interactive weekly item with one notify target and no calendar: %v", err)
	}
	quiet := scheduledWork()
	quiet.Spec.Notify = nil
	quiet.Spec.Calendar = ""
	if err := k8s.Create(ctx(t), quiet); err != nil {
		t.Fatalf("a headless item with no notify and no calendar: %v", err)
	}
	for _, caller := range []string{"client/dev-env/workbench", "coordinator/dev-agents/dev-env-1027-010058"} {
		w := scheduledWork()
		w.Spec.Owner, w.Spec.Editor = caller, caller
		if err := k8s.Create(ctx(t), w); err != nil {
			t.Fatalf("owner and editor %s: %v", caller, err)
		}
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
		"no title":      {func(w *v1alpha1.ScheduledWork) { w.Spec.Title = "" }, "spec.title"},
		"a long title":  {func(w *v1alpha1.ScheduledWork) { w.Spec.Title = long(121) }, "spec.title"},
		"no owner":      {func(w *v1alpha1.ScheduledWork) { w.Spec.Owner = "" }, "spec.owner"},
		"a long owner":  {func(w *v1alpha1.ScheduledWork) { w.Spec.Owner = long(257) }, "spec.owner"},
		"no editor":     {func(w *v1alpha1.ScheduledWork) { w.Spec.Editor = "" }, "spec.editor"},
		"a long editor": {func(w *v1alpha1.ScheduledWork) { w.Spec.Editor = long(257) }, "spec.editor"},
		"an owner of an unknown class": {func(w *v1alpha1.ScheduledWork) {
			w.Spec.Owner = "robot/dev-env-system/tom"
		}, "spec.owner"},
		"an owner with two parts":     {func(w *v1alpha1.ScheduledWork) { w.Spec.Owner = "human/tom" }, "spec.owner"},
		"an owner with four parts":    {func(w *v1alpha1.ScheduledWork) { w.Spec.Owner = "session/a/b/c" }, "spec.owner"},
		"an owner with an empty part": {func(w *v1alpha1.ScheduledWork) { w.Spec.Owner = "human//tom" }, "spec.owner"},
		"a miscased owner class":      {func(w *v1alpha1.ScheduledWork) { w.Spec.Owner = "Human/dev-env-system/tom" }, "spec.owner"},
		"an editor of an unknown class": {func(w *v1alpha1.ScheduledWork) {
			w.Spec.Editor = "operator/dev-env-system/dev-env-operator"
		}, "spec.editor"},
		"an editor with two parts":     {func(w *v1alpha1.ScheduledWork) { w.Spec.Editor = "session/x" }, "spec.editor"},
		"an editor with four parts":    {func(w *v1alpha1.ScheduledWork) { w.Spec.Editor = "human/a/b/c" }, "spec.editor"},
		"an editor with an empty part": {func(w *v1alpha1.ScheduledWork) { w.Spec.Editor = "client/dev-env/" }, "spec.editor"},
		"interactive with no notify": {func(w *v1alpha1.ScheduledWork) {
			w.Spec.Mode = v1alpha1.ScheduledWorkInteractive
			w.Spec.Notify = nil
		}, "interactive scheduled work needs a notify target"},
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

// No patch form undoes a cancel either: setting it false, setting it null or
// removing it with a merge or JSON patch is the same transition to the rule.
func TestScheduledWorkCancelCannotBePatchedAway(t *testing.T) {
	for name, patch := range map[string]client.Patch{
		"merge patch false": client.RawPatch(types.MergePatchType, []byte(`{"spec":{"cancelled":false}}`)),
		"merge patch null":  client.RawPatch(types.MergePatchType, []byte(`{"spec":{"cancelled":null}}`)),
		"JSON patch remove": client.RawPatch(types.JSONPatchType, []byte(`[{"op":"remove","path":"/spec/cancelled"}]`)),
		"JSON patch false":  client.RawPatch(types.JSONPatchType, []byte(`[{"op":"replace","path":"/spec/cancelled","value":false}]`)),
	} {
		t.Run(name, func(t *testing.T) {
			w := scheduledWork()
			w.Spec.Cancelled = true
			if err := k8s.Create(ctx(t), w); err != nil {
				t.Fatal(err)
			}
			wantInvalid(t, k8s.Patch(ctx(t), w, patch), "cancelled scheduled work stays cancelled")
		})
	}
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
		Approval: &v1alpha1.ScheduledWorkApproval{By: "human/dev-env-system/tom", At: now, SpecHash: swHash, TemplateDigest: swDigest},
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
	retried.RetriedAt, retried.Attempt = &retriedAt, 1
	retried.State, retried.Reason = v1alpha1.OccurrenceRunning, "approved after park"
	parked := &w.Status.Occurrences[4]
	parkedAt := metav1.NewTime(start.Add(4*24*time.Hour + 2*time.Hour))
	parked.State, parked.Reason, parked.Session = v1alpha1.OccurrenceParked, "no standing authority", ""
	parked.RetriedAt, parked.Attempt = &parkedAt, v1alpha1.ScheduledWorkMaxAttempt
	parked.Execution, parked.Reported = nil, nil
	parked.Pages = nil
	for attempt := range int32(v1alpha1.ScheduledWorkMaxAttempt + 1) {
		for _, event := range []string{v1alpha1.PageEventParked, v1alpha1.PageEventSkipped} {
			parked.Pages = v1alpha1.AppendPage(parked.Pages, v1alpha1.ScheduledWorkPage{
				Event: event, Attempt: attempt, Reminder: fmt.Sprintf("sw-x-%s-%d", event, attempt), State: v1alpha1.DeliverySent,
			})
		}
	}
	if len(parked.Pages) != v1alpha1.ScheduledWorkMaxPages {
		t.Fatalf("AppendPage kept %d pages, want %d", len(parked.Pages), v1alpha1.ScheduledWorkMaxPages)
	}
	skipped := &w.Status.Occurrences[2]
	skipped.State, skipped.Reason, skipped.Missed = v1alpha1.OccurrenceSkipped, "missed beyond grace", 7
	if err := k8s.Status().Update(ctx(t), w); err != nil {
		t.Fatalf("a full status of %d occurrences: %v", v1alpha1.ScheduledWorkMaxOccurrences, err)
	}
	// The approval's template digest is stored as written, not pruned.
	var got v1alpha1.ScheduledWork
	if err := k8s.Get(ctx(t), client.ObjectKeyFromObject(w), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Approval == nil || got.Status.Approval.TemplateDigest != swDigest {
		t.Fatalf("the stored approval %+v lost its template digest", got.Status.Approval)
	}
}

// swDigest is a template digest in the form status.approval.templateDigest
// takes: 64 lowercase hex characters.
var swDigest = strings.Repeat("0123456789abcdef", 4)

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
		"an uppercase template digest": {func(s *v1alpha1.ScheduledWorkStatus) {
			s.Approval.TemplateDigest = strings.ToUpper(swDigest)
		}, "status.approval.templateDigest"},
		"a short template digest": {func(s *v1alpha1.ScheduledWorkStatus) {
			s.Approval.TemplateDigest = swDigest[:63]
		}, "status.approval.templateDigest"},
		"a long template digest": {func(s *v1alpha1.ScheduledWorkStatus) {
			s.Approval.TemplateDigest = swDigest + "0"
		}, "status.approval.templateDigest"},
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
			s.Occurrences[0].State, s.Occurrences[0].Missed = v1alpha1.OccurrenceSkipped, -1
		}, "status.occurrences[0].missed"},
		"a missed count of 100001": {func(s *v1alpha1.ScheduledWorkStatus) {
			s.Occurrences[0].State, s.Occurrences[0].Missed = v1alpha1.OccurrenceSkipped, 100001
		}, "status.occurrences[0].missed"},
		"a missed count on a Succeeded occurrence": {func(s *v1alpha1.ScheduledWorkStatus) {
			s.Occurrences[0].Missed = 2
		}, "missed is set only on a Skipped occurrence"},
		"attempt 4": {func(s *v1alpha1.ScheduledWorkStatus) {
			s.Occurrences[0].Attempt = v1alpha1.ScheduledWorkMaxAttempt + 1
		}, "status.occurrences[0].attempt"},
		"attempt -1": {func(s *v1alpha1.ScheduledWorkStatus) { s.Occurrences[0].Attempt = -1 }, "status.occurrences[0].attempt"},
		"a page of attempt 4": {func(s *v1alpha1.ScheduledWorkStatus) {
			s.Occurrences[0].Pages[0].Attempt = v1alpha1.ScheduledWorkMaxAttempt + 1
		}, "status.occurrences[0].pages[0].attempt"},
		"a page of attempt -1": {func(s *v1alpha1.ScheduledWorkStatus) {
			s.Occurrences[0].Pages[0].Attempt = -1
		}, "status.occurrences[0].pages[0].attempt"},
		"retriedAt on attempt 0": {func(s *v1alpha1.ScheduledWorkStatus) {
			at := metav1.NewTime(time.Date(2026, 12, 3, 16, 0, 0, 0, time.UTC))
			s.Occurrences[0].RetriedAt = &at
		}, "retriedAt is set only on a retried attempt"},
		"two occurrences at one time": {func(s *v1alpha1.ScheduledWorkStatus) {
			s.Occurrences = append(s.Occurrences, s.Occurrences[0])
		}, "status.occurrences[1]"},
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
		"four reported PRs": {func(s *v1alpha1.ScheduledWorkStatus) {
			s.Occurrences[0].Reported.PRURLs = nil
			for i := range 4 {
				s.Occurrences[0].Reported.PRURLs = append(s.Occurrences[0].Reported.PRURLs, fmt.Sprintf("https://github.com/thaynes43/haynes-ops/pull/%d", 4100+i))
			}
		}, "status.occurrences[0].reported.prURLs"},
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
		"nine pages": {func(s *v1alpha1.ScheduledWorkStatus) {
			s.Occurrences[0].Pages = nil
			for i := int32(0); i < 9; i++ {
				s.Occurrences[0].Pages = append(s.Occurrences[0].Pages, v1alpha1.ScheduledWorkPage{Event: v1alpha1.PageEventParked, Attempt: i, State: v1alpha1.DeliverySkipped})
			}
		}, "status.occurrences[0].pages"},
		"two pages for one event and attempt": {func(s *v1alpha1.ScheduledWorkStatus) {
			s.Occurrences[0].Pages = []v1alpha1.ScheduledWorkPage{{Event: v1alpha1.PageEventDue, Attempt: 1}, {Event: v1alpha1.PageEventDue, Attempt: 1}}
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

// withOccurrence creates a ScheduledWork and writes status with the one
// occurrence o, so the next status write is a transition of that occurrence.
func withOccurrence(t *testing.T, o v1alpha1.ScheduledWorkOccurrence) *v1alpha1.ScheduledWork {
	t.Helper()
	w := scheduledWork()
	if err := k8s.Create(ctx(t), w); err != nil {
		t.Fatal(err)
	}
	w.Status.Occurrences = []v1alpha1.ScheduledWorkOccurrence{o}
	if err := k8s.Status().Update(ctx(t), w); err != nil {
		t.Fatalf("the first status write: %v", err)
	}
	return w
}

// bare is an occurrence with no session, hash, result or pages yet.
func bare(state string) v1alpha1.ScheduledWorkOccurrence {
	return v1alpha1.ScheduledWorkOccurrence{
		At:    metav1.NewTime(time.Date(2026, 12, 3, 14, 0, 0, 0, time.UTC)),
		State: state,
	}
}

// The schema's terminal states are OccurrenceTerminal's: a terminal state
// never changes, and every other state may move on.
func TestScheduledWorkOccurrenceTerminalStateIsFinal(t *testing.T) {
	all := []string{
		v1alpha1.OccurrencePending, v1alpha1.OccurrenceClaimed, v1alpha1.OccurrenceDispatched,
		v1alpha1.OccurrenceDelivered, v1alpha1.OccurrenceOpened, v1alpha1.OccurrenceRunning,
		v1alpha1.OccurrenceSucceeded, v1alpha1.OccurrenceFailed, v1alpha1.OccurrenceEnded,
		v1alpha1.OccurrenceParked, v1alpha1.OccurrenceSkipped, v1alpha1.OccurrenceAmbiguous,
		v1alpha1.OccurrenceCancelled,
	}
	for _, from := range all {
		t.Run(from, func(t *testing.T) {
			w := withOccurrence(t, bare(from))
			to := v1alpha1.OccurrenceFailed
			if from == to {
				to = v1alpha1.OccurrenceSucceeded
			}
			w.Status.Occurrences[0].State = to
			err := k8s.Status().Update(ctx(t), w)
			if v1alpha1.OccurrenceTerminal(from) {
				wantInvalid(t, err, "a terminal occurrence never changes state")
			} else if err != nil {
				t.Fatalf("%s to %s: %v", from, to, err)
			}
		})
	}
}

// A terminal occurrence still takes its result and page records; the
// executor prunes old terminal occurrences and appends new ones; a Parked
// occurrence is claimed again on a new attempt.
func TestScheduledWorkOccurrenceTransitionsAccept(t *testing.T) {
	t.Run("a terminal occurrence takes its pages and result", func(t *testing.T) {
		o := bare(v1alpha1.OccurrenceFailed)
		o.Session, o.SpecHash = "sw-1a2b3c4d5e6f-202612031400", swHash
		w := withOccurrence(t, o)
		got := &w.Status.Occurrences[0]
		got.Reason = "ended without a task result"
		got.Execution = &v1alpha1.ScheduledWorkExecution{State: v1alpha1.TaskResultFailed, Reason: "TaskExitedNonZero"}
		got.Pages = v1alpha1.AppendPage(got.Pages, v1alpha1.ScheduledWorkPage{Event: v1alpha1.PageEventFailed, Reminder: "sw-x-failed"})
		if err := k8s.Status().Update(ctx(t), w); err != nil {
			t.Fatal(err)
		}
		got.Pages = v1alpha1.AppendPage(got.Pages, v1alpha1.ScheduledWorkPage{Event: v1alpha1.PageEventFailed, Reminder: "sw-x-failed", State: v1alpha1.DeliverySent})
		if err := k8s.Status().Update(ctx(t), w); err != nil {
			t.Fatalf("the page's delivery state: %v", err)
		}
	})
	t.Run("a claim sets the session and spec hash once", func(t *testing.T) {
		w := withOccurrence(t, bare(v1alpha1.OccurrencePending))
		got := &w.Status.Occurrences[0]
		claimed := metav1.NewTime(got.At.Add(time.Second))
		got.State, got.ClaimedAt, got.SpecHash = v1alpha1.OccurrenceClaimed, &claimed, swHash
		if err := k8s.Status().Update(ctx(t), w); err != nil {
			t.Fatal(err)
		}
		got.State, got.Session = v1alpha1.OccurrenceDispatched, "sw-1a2b3c4d5e6f-202612031400"
		if err := k8s.Status().Update(ctx(t), w); err != nil {
			t.Fatalf("the session after the spec hash: %v", err)
		}
	})
	t.Run("a parked occurrence is retried up to attempt 3", func(t *testing.T) {
		o := bare(v1alpha1.OccurrenceParked)
		o.SpecHash, o.Reason = swHash, "no standing authority"
		w := withOccurrence(t, o)
		got := &w.Status.Occurrences[0]
		for attempt := int32(1); attempt <= v1alpha1.ScheduledWorkMaxAttempt; attempt++ {
			retried := metav1.NewTime(got.At.Add(time.Duration(attempt) * time.Hour))
			got.State, got.Attempt, got.RetriedAt = v1alpha1.OccurrenceClaimed, attempt, &retried
			if err := k8s.Status().Update(ctx(t), w); err != nil {
				t.Fatalf("the claim of attempt %d: %v", attempt, err)
			}
			got.State = v1alpha1.OccurrenceParked
			if err := k8s.Status().Update(ctx(t), w); err != nil {
				t.Fatalf("the park of attempt %d: %v", attempt, err)
			}
		}
	})
	t.Run("the session's UID is set once, and an ending mark before the final record", func(t *testing.T) {
		w := withOccurrence(t, bare(v1alpha1.OccurrenceClaimed))
		got := &w.Status.Occurrences[0]
		got.Ending = true
		if err := k8s.Status().Update(ctx(t), w); err != nil {
			t.Fatalf("the ending mark on a Claimed occurrence: %v", err)
		}
		got.State, got.Session, got.SessionUID = v1alpha1.OccurrenceDispatched, "sw-1a2b3c4d5e6f-202612031400", "0f0e0d0c-0b0a-0908-0706-050403020100"
		if err := k8s.Status().Update(ctx(t), w); err != nil {
			t.Fatalf("the adoption after the mark: %v", err)
		}
		got.State, got.Reason = v1alpha1.OccurrenceSucceeded, "the task exited 0"
		if err := k8s.Status().Update(ctx(t), w); err != nil {
			t.Fatalf("the final record keeps the mark and the UID: %v", err)
		}
	})
	t.Run("a reported outcome is set once on a running occurrence", func(t *testing.T) {
		w := withOccurrence(t, bare(v1alpha1.OccurrenceRunning))
		w.Status.Occurrences[0].Reported = &v1alpha1.TaskOutcomeRecordStatus{Summary: "done"}
		if err := k8s.Status().Update(ctx(t), w); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("an old terminal occurrence is pruned and a new one appended", func(t *testing.T) {
		o := bare(v1alpha1.OccurrenceSucceeded)
		o.Session = "sw-1a2b3c4d5e6f-202612031400"
		w := withOccurrence(t, o)
		next := bare(v1alpha1.OccurrencePending)
		next.At = metav1.NewTime(o.At.Add(7 * 24 * time.Hour))
		w.Status.Occurrences = []v1alpha1.ScheduledWorkOccurrence{next}
		if err := k8s.Status().Update(ctx(t), w); err != nil {
			t.Fatal(err)
		}
	})
}

func TestScheduledWorkOccurrenceTransitionsRefuse(t *testing.T) {
	const other = "sw-1a2b3c4d5e6f-202612031500"
	retriedAt := metav1.NewTime(time.Date(2026, 12, 3, 16, 0, 0, 0, time.UTC))
	for name, c := range map[string]struct {
		from func(*v1alpha1.ScheduledWorkOccurrence)
		edit func(*v1alpha1.ScheduledWorkOccurrence)
		want string
	}{
		"a session renamed": {
			func(o *v1alpha1.ScheduledWorkOccurrence) { o.Session = "sw-1a2b3c4d5e6f-202612031400" },
			func(o *v1alpha1.ScheduledWorkOccurrence) { o.Session = other },
			"an occurrence's session never changes once set",
		},
		"a session removed": {
			func(o *v1alpha1.ScheduledWorkOccurrence) { o.Session = "sw-1a2b3c4d5e6f-202612031400" },
			func(o *v1alpha1.ScheduledWorkOccurrence) { o.Session = "" },
			"an occurrence's session never changes once set",
		},
		"a spec hash changed": {
			func(o *v1alpha1.ScheduledWorkOccurrence) { o.SpecHash = swHash },
			func(o *v1alpha1.ScheduledWorkOccurrence) { o.SpecHash = strings.Repeat("f", 64) },
			"an occurrence's specHash never changes once set",
		},
		"a spec hash removed": {
			func(o *v1alpha1.ScheduledWorkOccurrence) { o.SpecHash = swHash },
			func(o *v1alpha1.ScheduledWorkOccurrence) { o.SpecHash = "" },
			"an occurrence's specHash never changes once set",
		},
		"a reported outcome changed": {
			func(o *v1alpha1.ScheduledWorkOccurrence) {
				o.Reported = &v1alpha1.TaskOutcomeRecordStatus{Summary: "first"}
			},
			func(o *v1alpha1.ScheduledWorkOccurrence) {
				o.Reported = &v1alpha1.TaskOutcomeRecordStatus{Summary: "second"}
			},
			"an occurrence's reported outcome never changes once set",
		},
		"a reported outcome removed": {
			func(o *v1alpha1.ScheduledWorkOccurrence) {
				o.Reported = &v1alpha1.TaskOutcomeRecordStatus{Summary: "first"}
			},
			func(o *v1alpha1.ScheduledWorkOccurrence) { o.Reported = nil },
			"an occurrence's reported outcome never changes once set",
		},
		"a session UID changed": {
			func(o *v1alpha1.ScheduledWorkOccurrence) { o.SessionUID = "0f0e0d0c-0b0a-0908-0706-050403020100" },
			func(o *v1alpha1.ScheduledWorkOccurrence) { o.SessionUID = "ffffffff-0b0a-0908-0706-050403020100" },
			"an occurrence's sessionUID never changes once set",
		},
		"a session UID removed": {
			func(o *v1alpha1.ScheduledWorkOccurrence) { o.SessionUID = "0f0e0d0c-0b0a-0908-0706-050403020100" },
			func(o *v1alpha1.ScheduledWorkOccurrence) { o.SessionUID = "" },
			"an occurrence's sessionUID never changes once set",
		},
		"an ending mark removed": {
			func(o *v1alpha1.ScheduledWorkOccurrence) { o.Ending = true },
			func(o *v1alpha1.ScheduledWorkOccurrence) { o.Ending = false },
			"an occurrence's ending mark is never removed",
		},
		"an ending mark on a final occurrence": {
			func(o *v1alpha1.ScheduledWorkOccurrence) { o.State = v1alpha1.OccurrenceSkipped },
			func(o *v1alpha1.ScheduledWorkOccurrence) { o.Ending = true },
			"an occurrence is marked ending only while it is not final",
		},
		"attempt 2 back to 1": {
			func(o *v1alpha1.ScheduledWorkOccurrence) { o.Attempt, o.RetriedAt = 2, &retriedAt },
			func(o *v1alpha1.ScheduledWorkOccurrence) { o.Attempt = 1 },
			"an occurrence's attempt never decreases",
		},
		"attempt 1 back to 0": {
			func(o *v1alpha1.ScheduledWorkOccurrence) { o.Attempt, o.RetriedAt = 1, &retriedAt },
			func(o *v1alpha1.ScheduledWorkOccurrence) { o.Attempt, o.RetriedAt = 0, nil },
			"an occurrence's attempt never decreases",
		},
	} {
		t.Run(name, func(t *testing.T) {
			o := bare(v1alpha1.OccurrenceClaimed)
			c.from(&o)
			w := withOccurrence(t, o)
			c.edit(&w.Status.Occurrences[0])
			wantInvalid(t, k8s.Status().Update(ctx(t), w), c.want)
		})
	}
}
