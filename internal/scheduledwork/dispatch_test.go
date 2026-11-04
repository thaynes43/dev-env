package scheduledwork

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

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
	s.Finalizers = []string{v1alpha1.ScheduledWorkFinalizer}
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
		"no finalizer":     CreatedFor(&v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Labels: s.Labels, Annotations: s.Annotations}}, "audit", "uid-1", at),
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

// The launch gate refuses a session whose occurrence would never record it,
// and lets an open occurrence, or one naming the session, start it.
func TestLaunchRefusal(t *testing.T) {
	at := time.Date(2030, 6, 3, 14, 0, 0, 0, time.UTC)
	const name = "sw-0123456789ab-203006031400"
	s := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: name, UID: "session-uid-1",
		Labels:      map[string]string{v1alpha1.LabelScheduledWork: "uid-1"},
		Annotations: map[string]string{v1alpha1.AnnotationScheduledWorkOccurrence: OccurrenceAnnotation(at)}}}
	item := func(uid, state, session string) *v1alpha1.ScheduledWork {
		w := &v1alpha1.ScheduledWork{ObjectMeta: metav1.ObjectMeta{UID: types.UID(uid)}}
		if state != "" {
			w.Status.Occurrences = []v1alpha1.ScheduledWorkOccurrence{{At: metav1.NewTime(at), State: state, Session: session}}
		}
		return w
	}
	for _, c := range []struct {
		name   string
		w      *v1alpha1.ScheduledWork
		refuse bool
	}{
		{"claimed, no session yet", item("uid-1", v1alpha1.OccurrenceClaimed, ""), false},
		{"dispatched to it", item("uid-1", v1alpha1.OccurrenceDispatched, name), false},
		{"parked, no session", item("uid-1", v1alpha1.OccurrenceParked, ""), false},
		{"final and naming it", item("uid-1", v1alpha1.OccurrenceSucceeded, name), false},
		{"final without it", item("uid-1", v1alpha1.OccurrenceSkipped, ""), true},
		{"cancelled without it", item("uid-1", v1alpha1.OccurrenceCancelled, ""), true},
		{"names another session", item("uid-1", v1alpha1.OccurrenceDispatched, "sw-ffffffffffff-203006031400"), true},
		{"no occurrence", item("uid-1", "", ""), true},
		{"another item", item("uid-2", v1alpha1.OccurrenceClaimed, ""), true},
		{"the item is gone", nil, true},
		{"dispatched to this session object", withUID(item("uid-1", v1alpha1.OccurrenceDispatched, name), "session-uid-1"), false},
		{"recorded for another session object of the name", withUID(item("uid-1", v1alpha1.OccurrenceFailed, name), "session-uid-0"), true},
		{"dispatched to another session object of the name", withUID(item("uid-1", v1alpha1.OccurrenceDispatched, name), "session-uid-0"), true},
		{"ending without a session", ending(item("uid-1", v1alpha1.OccurrenceClaimed, "")), true},
		{"parked and ending without a session", ending(item("uid-1", v1alpha1.OccurrenceParked, "")), true},
		{"ending, adopted by this session", ending(item("uid-1", v1alpha1.OccurrenceDispatched, name)), false},
	} {
		if reason, refuse := LaunchRefusal(s, c.w); refuse != c.refuse {
			t.Errorf("%s: refuse %v (%q), want %v", c.name, refuse, reason, c.refuse)
		}
	}
	failed := s.DeepCopy()
	failed.Status.Phase = v1alpha1.PhaseFailed
	if _, refuse := LaunchRefusal(failed, withUID(item("uid-1", v1alpha1.OccurrenceFailed, name), "session-uid-1")); !refuse {
		t.Error("a session refused before may start once its occurrence names it")
	}
	if _, refuse := LaunchRefusal(&v1alpha1.AgentSession{}, item("uid-1", v1alpha1.OccurrenceClaimed, "")); !refuse {
		t.Error("a session without its occurrence annotation may start")
	}
	deadline := "2030-06-03T14:15:00Z"
	s.Annotations[v1alpha1.AnnotationScheduledWorkStartDeadline] = deadline
	if until, err := ReleaseHeldUntil(s); err != nil || !until.Equal(at.Add(15*time.Minute+ReleaseMargin)) {
		t.Errorf("held until %v, %v", until, err)
	}
	delete(s.Annotations, v1alpha1.AnnotationScheduledWorkStartDeadline)
	if _, err := ReleaseHeldUntil(s); err == nil {
		t.Error("no deadline gave a release time")
	}
}

func withUID(w *v1alpha1.ScheduledWork, uid string) *v1alpha1.ScheduledWork {
	w.Status.Occurrences[0].SessionUID = uid
	return w
}

func ending(w *v1alpha1.ScheduledWork) *v1alpha1.ScheduledWork {
	w.Status.Occurrences[0].Ending = true
	return w
}
