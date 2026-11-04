package apiserver

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// jsonFields lists a struct type's JSON field names, in order.
func jsonFields(tp reflect.Type) []string {
	var out []string
	for i := range tp.NumField() {
		f := tp.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		out = append(out, name)
	}
	return out
}

func sortedFields(tp reflect.Type) []string {
	f := jsonFields(tp)
	slices.Sort(f)
	return f
}

// The wire types (apiv1, standard library only) and the CRD types name the
// same fields, so a field added to one is not silently dropped by the view or
// the create. The two occurrence fields the view leaves out are the
// executor's own bookkeeping.
func TestScheduledWorkWireFieldSets(t *testing.T) {
	same := []struct{ wire, crd reflect.Type }{
		{reflect.TypeOf(apiv1.ScheduledWorkSpec{}), reflect.TypeOf(v1alpha1.ScheduledWorkSpec{})},
		{reflect.TypeOf(apiv1.ScheduledWorkOrder{}), reflect.TypeOf(v1alpha1.ScheduledWorkOrder{})},
		{reflect.TypeOf(apiv1.ReminderRecurrence{}), reflect.TypeOf(v1alpha1.ReminderRecurrence{})},
		{reflect.TypeOf(apiv1.ScheduledWorkApproval{}), reflect.TypeOf(v1alpha1.ScheduledWorkApproval{})},
		{reflect.TypeOf(apiv1.ScheduledWorkCounts{}), reflect.TypeOf(v1alpha1.ScheduledWorkCounts{})},
		{reflect.TypeOf(apiv1.ScheduledWorkPage{}), reflect.TypeOf(v1alpha1.ScheduledWorkPage{})},
		{reflect.TypeOf(apiv1.ScheduledWorkExecution{}), reflect.TypeOf(v1alpha1.ScheduledWorkExecution{})},
		{reflect.TypeOf(apiv1.ScheduledWorkReported{}), reflect.TypeOf(v1alpha1.TaskOutcomeRecordStatus{})},
	}
	for _, c := range same {
		if w, k := sortedFields(c.wire), sortedFields(c.crd); !slices.Equal(w, k) {
			t.Errorf("%s has %v, %s has %v", c.wire, w, c.crd, k)
		}
	}
	occ := sortedFields(reflect.TypeOf(v1alpha1.ScheduledWorkOccurrence{}))
	occ = slices.DeleteFunc(occ, func(f string) bool { return f == "sessionUID" || f == "ending" })
	if w := sortedFields(reflect.TypeOf(apiv1.ScheduledWorkOccurrence{})); !slices.Equal(w, occ) {
		t.Errorf("the occurrence view has %v, the CRD %v (less sessionUID and ending)", w, occ)
	}
	// A create carries the spec's fields but owner, editor and cancelled,
	// plus its key.
	spec := slices.DeleteFunc(jsonFields(reflect.TypeOf(v1alpha1.ScheduledWorkSpec{})), func(f string) bool {
		return f == "owner" || f == "editor" || f == "cancelled"
	})
	spec = append(spec, "key")
	slices.Sort(spec)
	if w := sortedFields(reflect.TypeOf(apiv1.CreateScheduledWorkRequest{})); !slices.Equal(w, spec) {
		t.Errorf("a create has %v, want %v", w, spec)
	}
	// An edit carries each create field but the key, and clearRecurrence.
	edit := slices.DeleteFunc(slices.Clone(spec), func(f string) bool { return f == "key" })
	edit = append(edit, "clearRecurrence")
	slices.Sort(edit)
	if w := sortedFields(reflect.TypeOf(apiv1.EditScheduledWorkRequest{})); !slices.Equal(w, edit) {
		t.Errorf("an edit has %v, want %v", w, edit)
	}
}

// The view copies every stored value: a fully set item's view, marshalled,
// carries each one.
func TestScheduledWorkViewCopiesEveryField(t *testing.T) {
	at := func(d int) metav1.Time { return metav1.NewTime(time.Date(2026, 12, d, 14, 0, 0, 0, time.UTC)) }
	until, claimed, retried, next := at(30), at(3), at(4), at(10)
	sw := &v1alpha1.ScheduledWork{
		ObjectMeta: metav1.ObjectMeta{Name: "sched-0123456789abcdef0123", CreationTimestamp: at(1)},
		Spec: v1alpha1.ScheduledWorkSpec{
			Title: "t-title", Owner: "session/s-owner/uid-owner", Editor: "human/dev-env-system/editor", Due: "2026-12-03T09:00", TimeZone: "America/New_York",
			Recurrence: &v1alpha1.ReminderRecurrence{Freq: "monthly", ByMonthDay: 17, Until: &until},
			NotAfter:   at(31), Mode: v1alpha1.ScheduledWorkInteractive,
			Work:   v1alpha1.ScheduledWorkOrder{Template: "t-template", Repo: "t-repo", Prompt: "t-prompt"},
			Notify: []string{"pushover:default"}, Calendar: "calendar:t-calendar",
		},
		Status: v1alpha1.ScheduledWorkStatus{
			Approval: &v1alpha1.ScheduledWorkApproval{By: "human/dev-env-system/t-approver", At: at(2), SpecHash: strings.Repeat("1", 64), TemplateDigest: strings.Repeat("2", 64)},
			NextDue:  &next,
			Counts:   v1alpha1.ScheduledWorkCounts{Succeeded: 101, Failed: 102, Parked: 103, Skipped: 104, Ended: 105, Ambiguous: 106, Cancelled: 107},
			Occurrences: []v1alpha1.ScheduledWorkOccurrence{{
				At: at(3), State: v1alpha1.OccurrenceSucceeded, Reason: "t-reason", Attempt: 2, ClaimedAt: &claimed, RetriedAt: &retried,
				Session: "sw-0123456789ab-202612031400", SpecHash: strings.Repeat("3", 64), Delivery: v1alpha1.WorkOrderAccepted,
				Execution: &v1alpha1.ScheduledWorkExecution{State: v1alpha1.TaskResultSucceeded, Reason: "t-exec-reason"},
				Reported:  &v1alpha1.TaskOutcomeRecordStatus{Summary: "t-summary", PRURLs: []string{"https://github.com/thaynes43/haynes-ops/pull/1"}, HeadSHAs: []string{strings.Repeat("4", 40)}},
				Pages:     []v1alpha1.ScheduledWorkPage{{Event: v1alpha1.PageEventLink, Attempt: 1, Reminder: "t-reminder", State: v1alpha1.DeliverySent}},
			}, {At: at(5), State: v1alpha1.OccurrenceSkipped, Missed: 9}},
		},
	}
	v := scheduledWorkView(sw, at(1).Time)
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"t-title", "session/s-owner/uid-owner", "human/dev-env-system/editor", "2026-12-03T09:00", "America/New_York", `"freq":"monthly"`, `"byMonthDay":17`,
		"2026-12-30T14:00:00Z", "2026-12-31T14:00:00Z", `"mode":"interactive"`, "t-template", "t-repo", "t-prompt", "pushover:default", "calendar:t-calendar",
		"human/dev-env-system/t-approver", strings.Repeat("1", 64), strings.Repeat("2", 64), "2026-12-10T14:00:00Z",
		`"succeeded":101`, `"failed":102`, `"parked":103`, `"skipped":104`, `"ended":105`, `"ambiguous":106`, `"cancelled":107`,
		"t-reason", `"attempt":2`, "2026-12-04T14:00:00Z", "sw-0123456789ab-202612031400", strings.Repeat("3", 64), `"delivery":"accepted"`,
		`"state":"Succeeded"`, "t-exec-reason", "t-summary", "pull/1", strings.Repeat("4", 40), `"event":"link"`, "t-reminder", `"missed":9`,
		`"createdAt":"2026-12-01T14:00:00Z"`, `"name":"sched-0123456789abcdef0123"`,
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("the view has no %s:\n%s", want, b)
		}
	}
	if v.SpecHash != scheduledWorkView(sw, at(1).Time).SpecHash || len(v.SpecHash) != 64 {
		t.Errorf("spec hash %q", v.SpecHash)
	}
}
