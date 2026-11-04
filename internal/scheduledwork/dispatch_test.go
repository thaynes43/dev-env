package scheduledwork

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// The spec hash covers every spec field but editor and cancelled, and reads
// notify and byDay as sets.
func TestSpecHash(t *testing.T) {
	base := v1alpha1.ScheduledWorkSpec{
		Title: "audit", Owner: "human/dev-env-system/tom", Editor: "human/dev-env-system/tom",
		Due: "2030-06-03T14:00", TimeZone: "UTC", NotAfter: metav1.NewTime(time.Date(2030, 9, 1, 0, 0, 0, 0, time.UTC)),
		Mode: v1alpha1.ScheduledWorkHeadless, Recurrence: &v1alpha1.ReminderRecurrence{Freq: v1alpha1.ReminderWeekly, ByDay: []string{"MO", "TH"}},
		Work:   v1alpha1.ScheduledWorkOrder{Template: "ops-audit", Repo: "haynes-ops", Prompt: "p"},
		Notify: []string{"pushover:default", "pushover:phone"},
	}
	h := SpecHash(base)
	if len(h) != 64 {
		t.Fatalf("hash %q is not 64 hex", h)
	}
	same := *base.DeepCopy()
	same.Editor, same.Cancelled = "session/s/u", true
	same.Notify = []string{"pushover:phone", "pushover:default"}
	same.Recurrence.ByDay = []string{"TH", "MO"}
	if SpecHash(same) != h {
		t.Error("editor, cancelled or a set's order changed the hash")
	}
	if base.Notify[0] != "pushover:default" || base.Recurrence.ByDay[0] != "MO" {
		t.Error("SpecHash changed its argument")
	}
	for name, edit := range map[string]func(*v1alpha1.ScheduledWorkSpec){
		"title":    func(s *v1alpha1.ScheduledWorkSpec) { s.Title = "other" },
		"notify":   func(s *v1alpha1.ScheduledWorkSpec) { s.Notify = s.Notify[:1] },
		"calendar": func(s *v1alpha1.ScheduledWorkSpec) { s.Calendar = "calendar:haynesnetwork" },
		"prompt":   func(s *v1alpha1.ScheduledWorkSpec) { s.Work.Prompt = "q" },
		"template": func(s *v1alpha1.ScheduledWorkSpec) { s.Work.Template = "other" },
		"mode":     func(s *v1alpha1.ScheduledWorkSpec) { s.Mode = v1alpha1.ScheduledWorkInteractive },
		"notAfter": func(s *v1alpha1.ScheduledWorkSpec) { s.NotAfter = metav1.NewTime(s.NotAfter.Add(time.Hour)) },
		"due":      func(s *v1alpha1.ScheduledWorkSpec) { s.Due = "2030-06-03T15:00" },
	} {
		s := *base.DeepCopy()
		edit(&s)
		if SpecHash(s) == h {
			t.Errorf("an edit of %s kept the hash", name)
		}
	}
}

// CreatedFor matches the label and both annotations the fixed-name entry
// sets, and nothing less.
func TestCreatedFor(t *testing.T) {
	at := time.Date(2030, 6, 3, 14, 0, 0, 0, time.UTC)
	s := &v1alpha1.AgentSession{}
	s.Labels = map[string]string{v1alpha1.LabelScheduledWork: "uid-1"}
	s.Annotations = map[string]string{v1alpha1.AnnotationScheduledWorkName: "audit", v1alpha1.AnnotationScheduledWorkOccurrence: OccurrenceAnnotation(at)}
	if !CreatedFor(s, "audit", "uid-1", at) {
		t.Fatal("the session of the occurrence does not match")
	}
	for name, ok := range map[string]bool{
		"other uid":        CreatedFor(s, "audit", "uid-2", at),
		"other item":       CreatedFor(s, "other", "uid-1", at),
		"other occurrence": CreatedFor(s, "audit", "uid-1", at.Add(time.Minute)),
		"no uid":           CreatedFor(s, "audit", "", at),
	} {
		if ok {
			t.Errorf("%s matched", name)
		}
	}
	got, err := OccurrenceAt(&v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Annotations: s.Annotations}})
	if err != nil || !got.Equal(at) || StartDeadlineAnnotation(at.Add(15*time.Minute+500*time.Millisecond)) != "2030-06-03T14:15:00Z" {
		t.Errorf("annotations do not round-trip: %v %v", got, err)
	}
}
