package pages

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

const itemUID = "11111111-2222-3333-4444-555555555555"

var (
	occAt = time.Date(2026, 12, 3, 14, 0, 0, 0, time.UTC)
	nowAt = time.Date(2026, 12, 3, 14, 0, 41, 0, time.UTC)
)

func item(notify ...string) *v1alpha1.ScheduledWork {
	return &v1alpha1.ScheduledWork{
		ObjectMeta: metav1.ObjectMeta{Namespace: "dev-env-system", Name: "nightly-audit", UID: itemUID},
		Spec:       v1alpha1.ScheduledWorkSpec{Title: "Nightly audit", Notify: notify},
	}
}

func scheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func fakeClient(t *testing.T, funcs interceptor.Funcs, objs ...client.Object) client.Client {
	t.Helper()
	b := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(objs...)
	return interceptor.NewClient(b.Build(), funcs)
}

func TestKeyGolden(t *testing.T) {
	// A changed name orphans every page Reminder of an occurrence a restart
	// replays: change this golden only with a migration.
	const want = "swp-4dd9844d68e665cf3c77c5352bb296c0"
	if got := Key(item("pushover:default"), occAt, v1alpha1.PageEventDue, 0); got != want {
		t.Fatalf("Key = %q, want %q", got, want)
	}
}

func TestKey(t *testing.T) {
	base := Key(item(), occAt, v1alpha1.PageEventDue, 0)
	label := regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	if !label.MatchString(base) || len(base) > 63 || !strings.HasPrefix(base, KeyPrefix) {
		t.Fatalf("Key %q is not a DNS label with the prefix", base)
	}
	other := item()
	other.UID = "99999999-2222-3333-4444-555555555555"
	cases := []struct {
		name string
		got  string
		same bool
	}{
		{"same tuple", Key(item(), occAt, v1alpha1.PageEventDue, 0), true},
		{"other notify and name do not matter", Key(&v1alpha1.ScheduledWork{ObjectMeta: metav1.ObjectMeta{Name: "x", UID: itemUID}}, occAt, v1alpha1.PageEventDue, 0), true},
		{"the same instant in another zone", Key(item(), occAt.In(time.FixedZone("x", 3600)), v1alpha1.PageEventDue, 0), true},
		{"other item UID", Key(other, occAt, v1alpha1.PageEventDue, 0), false},
		{"other occurrence", Key(item(), occAt.Add(time.Minute), v1alpha1.PageEventDue, 0), false},
		{"other event", Key(item(), occAt, v1alpha1.PageEventLink, 0), false},
		{"other attempt", Key(item(), occAt, v1alpha1.PageEventDue, 1), false},
		{"fields are not concatenated", Key(item(), occAt, v1alpha1.PageEventDue+"0", 0), false},
	}
	for _, c := range cases {
		if (c.got == base) != c.same {
			t.Errorf("%s: Key = %q, base %q, same = %v", c.name, c.got, base, c.same)
		}
	}
}

func TestBuild(t *testing.T) {
	long := strings.Repeat("é", 100) // 200 bytes
	cases := []struct {
		name    string
		work    *v1alpha1.ScheduledWork
		event   string
		attempt int32
		title   string
		body    string
		wantErr string
		check   func(t *testing.T, r *v1alpha1.Reminder)
	}{
		{name: "builds the one-shot reminder", work: item("pushover:default"), event: v1alpha1.PageEventParked, attempt: 2, title: " Parked ", body: "why",
			check: func(t *testing.T, r *v1alpha1.Reminder) {
				if r.Namespace != "dev-env-system" || r.Name != Key(item(), occAt, v1alpha1.PageEventParked, 2) {
					t.Errorf("namespace/name = %s/%s", r.Namespace, r.Name)
				}
				if r.Spec.Owner != "client/dev-env-system/scheduled-work" {
					t.Errorf("owner = %q", r.Spec.Owner)
				}
				if r.Spec.Title != "Parked" || r.Spec.Body != "why" {
					t.Errorf("title/body = %q/%q", r.Spec.Title, r.Spec.Body)
				}
				if r.Spec.Due != "2026-12-03T14:00" || r.Spec.TimeZone != "UTC" || r.Spec.Recurrence != nil || r.Spec.Cancelled {
					t.Errorf("schedule = %+v", r.Spec)
				}
				want := map[string]string{
					v1alpha1.LabelScheduledWork:                itemUID,
					v1alpha1.AnnotationScheduledWorkOccurrence: "1796306400",
					LabelEvent:   "parked",
					LabelAttempt: "2",
				}
				for k, v := range want {
					if r.Labels[k] != v {
						t.Errorf("label %s = %q, want %q", k, r.Labels[k], v)
					}
				}
				if r.Annotations[v1alpha1.AnnotationScheduledWorkName] != "nightly-audit" {
					t.Errorf("annotations = %v", r.Annotations)
				}
			}},
		{name: "only the pushover refs, distinct, in order", work: item("pushover:b", "pushover:a", "pushover:b", "calendar:work", "pushover:BAD", "tel:1"), event: v1alpha1.PageEventDue,
			title: "t", check: func(t *testing.T, r *v1alpha1.Reminder) {
				if got := strings.Join(r.Spec.Targets, ","); got != "pushover:b,pushover:a" {
					t.Errorf("targets = %s", got)
				}
			}},
		{name: "at most eight targets", work: item("pushover:a1", "pushover:a2", "pushover:a3", "pushover:a4", "pushover:a5", "pushover:a6", "pushover:a7", "pushover:a8", "pushover:a9"), event: v1alpha1.PageEventDue,
			title: "t", check: func(t *testing.T, r *v1alpha1.Reminder) {
				if len(r.Spec.Targets) != 8 {
					t.Errorf("targets = %v", r.Spec.Targets)
				}
			}},
		{name: "title cut to the limit on a rune boundary", work: item("pushover:default"), event: v1alpha1.PageEventDue, title: long + "x",
			check: func(t *testing.T, r *v1alpha1.Reminder) {
				if len(r.Spec.Title) != MaxTitleBytes || !utf8.ValidString(r.Spec.Title) {
					t.Errorf("title = %d bytes, valid %v", len(r.Spec.Title), utf8.ValidString(r.Spec.Title))
				}
			}},
		{name: "odd limit cuts before a split rune", work: item("pushover:default"), event: v1alpha1.PageEventDue, title: "a" + strings.Repeat("é", 100),
			check: func(t *testing.T, r *v1alpha1.Reminder) {
				if len(r.Spec.Title) != MaxTitleBytes-1 || !utf8.ValidString(r.Spec.Title) {
					t.Errorf("title = %d bytes", len(r.Spec.Title))
				}
			}},
		{name: "body cut to the limit", work: item("pushover:default"), event: v1alpha1.PageEventDue, title: "t", body: strings.Repeat("日", 400),
			check: func(t *testing.T, r *v1alpha1.Reminder) {
				if len(r.Spec.Body) > MaxBodyBytes || len(r.Spec.Body) < MaxBodyBytes-3 || !utf8.ValidString(r.Spec.Body) {
					t.Errorf("body = %d bytes", len(r.Spec.Body))
				}
			}},
		{name: "invalid utf-8 is replaced", work: item("pushover:default"), event: v1alpha1.PageEventDue, title: "a\xffb", body: "c\xfe",
			check: func(t *testing.T, r *v1alpha1.Reminder) {
				if !utf8.ValidString(r.Spec.Title) || !utf8.ValidString(r.Spec.Body) {
					t.Errorf("title/body not valid utf-8")
				}
			}},
		{name: "no notify target", work: item(), event: v1alpha1.PageEventDue, title: "t", wantErr: ErrNoNotify.Error()},
		{name: "calendar-only notify", work: item("calendar:work"), event: v1alpha1.PageEventDue, title: "t", wantErr: ErrNoNotify.Error()},
		{name: "no UID", work: &v1alpha1.ScheduledWork{Spec: v1alpha1.ScheduledWorkSpec{Notify: []string{"pushover:default"}}}, event: v1alpha1.PageEventDue, title: "t", wantErr: "UID"},
		{name: "nil item", work: nil, event: v1alpha1.PageEventDue, title: "t", wantErr: "UID"},
		{name: "unknown event", work: item("pushover:default"), event: "bogus", title: "t", wantErr: "event"},
		{name: "attempt too high", work: item("pushover:default"), event: v1alpha1.PageEventDue, attempt: 4, title: "t", wantErr: "attempt"},
		{name: "attempt negative", work: item("pushover:default"), event: v1alpha1.PageEventDue, attempt: -1, title: "t", wantErr: "attempt"},
		{name: "blank title", work: item("pushover:default"), event: v1alpha1.PageEventDue, title: " \t ", wantErr: "title"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, err := Build(c.work, occAt, c.event, c.attempt, c.title, c.body, nowAt)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) || r != nil {
					t.Fatalf("Build = %v, %v; want error containing %q", r, err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			c.check(t, r)
		})
	}
}

func TestBuildDueReadsNowNotOccurrence(t *testing.T) {
	r, err := Build(item("pushover:default"), occAt, v1alpha1.PageEventResult, 0, "t", "", time.Date(2026, 12, 3, 16, 5, 59, 0, time.FixedZone("x", -5*3600)))
	if err != nil {
		t.Fatal(err)
	}
	if r.Spec.Due != "2026-12-03T21:05" {
		t.Fatalf("due = %q, want now in UTC to the minute", r.Spec.Due)
	}
}

func TestPageState(t *testing.T) {
	rem := func(state string, ds ...string) *v1alpha1.Reminder {
		r := &v1alpha1.Reminder{Status: v1alpha1.ReminderStatus{State: state}}
		for _, d := range ds {
			r.Status.Deliveries = append(r.Status.Deliveries, v1alpha1.ReminderDelivery{State: d})
		}
		return r
	}
	cases := []struct {
		name string
		r    *v1alpha1.Reminder
		want string
	}{
		{"nil", nil, ""},
		{"no status yet", rem(""), ""},
		{"pending", rem(v1alpha1.ReminderPending, v1alpha1.DeliveryClaimed), ""},
		{"unknown state", rem("weird"), ""},
		{"sent", rem(v1alpha1.ReminderSent, v1alpha1.DeliverySent), "sent"},
		{"failed", rem(v1alpha1.ReminderFailed, v1alpha1.DeliveryFailed), "failed"},
		{"failed with a retry that ran out", rem(v1alpha1.ReminderFailed, v1alpha1.DeliveryRetry), "failed"},
		{"failed with an ambiguous delivery", rem(v1alpha1.ReminderFailed, v1alpha1.DeliveryAmbiguous), "ambiguous"},
		{"failed with an unresolved claim", rem(v1alpha1.ReminderFailed, v1alpha1.DeliveryClaimed), "ambiguous"},
		{"failed, one sent one ambiguous", rem(v1alpha1.ReminderFailed, v1alpha1.DeliverySent, v1alpha1.DeliveryAmbiguous), "ambiguous"},
		{"skipped", rem(v1alpha1.ReminderSkipped, v1alpha1.DeliverySkipped), "skipped"},
		{"cancelled", rem(v1alpha1.ReminderCancelled), "skipped"},
		// A cancel can land while a delivery is claimed and unresolved, or
		// after one target was sent: the deliveries say what reached Tom.
		{"cancelled with a sent delivery", rem(v1alpha1.ReminderCancelled, v1alpha1.DeliverySkipped, v1alpha1.DeliverySent), "sent"},
		{"cancelled with a sent and an ambiguous delivery", rem(v1alpha1.ReminderCancelled, v1alpha1.DeliveryAmbiguous, v1alpha1.DeliverySent), "sent"},
		{"cancelled with an unresolved claim", rem(v1alpha1.ReminderCancelled, v1alpha1.DeliveryClaimed), "ambiguous"},
		{"cancelled with an ambiguous delivery", rem(v1alpha1.ReminderCancelled, v1alpha1.DeliveryAmbiguous), "ambiguous"},
		{"cancelled with a skipped delivery", rem(v1alpha1.ReminderCancelled, v1alpha1.DeliverySkipped, v1alpha1.DeliveryFailed), "skipped"},
		{"skipped with a sent delivery", rem(v1alpha1.ReminderSkipped, v1alpha1.DeliverySent), "sent"},
		{"skipped with an unresolved claim", rem(v1alpha1.ReminderSkipped, v1alpha1.DeliveryClaimed), "ambiguous"},
	}
	for _, c := range cases {
		if got := PageState(c.r); got != c.want {
			t.Errorf("%s: PageState = %q, want %q", c.name, got, c.want)
		}
	}
}

func list(t *testing.T, c client.Client) []v1alpha1.Reminder {
	t.Helper()
	var l v1alpha1.ReminderList
	if err := c.List(context.Background(), &l); err != nil {
		t.Fatal(err)
	}
	return l.Items
}

func TestEnsureCreatesOnceAndIsIdempotent(t *testing.T) {
	creates := 0
	c := fakeClient(t, interceptor.Funcs{Create: func(ctx context.Context, cl client.WithWatch, o client.Object, opts ...client.CreateOption) error {
		creates++
		return cl.Create(ctx, o, opts...)
	}})
	p := &Pager{Client: c}
	w := item("pushover:default")
	want := Key(w, occAt, v1alpha1.PageEventDue, 0)

	first := p.Ensure(context.Background(), w, occAt, v1alpha1.PageEventDue, 0, "Due", "body", nowAt)
	if first.Reminder != want || first.State != "" || first.Event != "due" || first.Attempt != 0 {
		t.Fatalf("first = %+v", first)
	}
	second := p.Ensure(context.Background(), w, occAt, v1alpha1.PageEventDue, 0, "Due", "body", nowAt.Add(time.Hour))
	if second != first {
		t.Fatalf("retry = %+v, want %+v", second, first)
	}
	if creates != 2 || len(list(t, c)) != 1 {
		t.Fatalf("creates = %d, reminders = %d; the retry must find the first", creates, len(list(t, c)))
	}
	if got := list(t, c)[0]; got.Spec.Due != "2026-12-03T14:00" {
		t.Fatalf("the retry changed the reminder: due %q", got.Spec.Due)
	}
	// Another attempt of the same event is its own Reminder.
	p.Ensure(context.Background(), w, occAt, v1alpha1.PageEventDue, 1, "Due", "", nowAt)
	if len(list(t, c)) != 2 {
		t.Fatalf("reminders = %d, want one per attempt", len(list(t, c)))
	}
}

func TestEnsureExistingReminderGivesItsState(t *testing.T) {
	w := item("pushover:default")
	name := Key(w, occAt, v1alpha1.PageEventResult, 0)
	existing, err := Build(w, occAt, v1alpha1.PageEventResult, 0, "Result", "", nowAt.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	existing.Status.State = v1alpha1.ReminderSent
	p := &Pager{Client: fakeClient(t, interceptor.Funcs{}, existing)}
	got := p.Ensure(context.Background(), w, occAt, v1alpha1.PageEventResult, 0, "Result", "", nowAt)
	if got.Reminder != name || got.State != "sent" {
		t.Fatalf("page = %+v", got)
	}
}

func TestEnsureNeverUpdatesOrDeletes(t *testing.T) {
	w := item("pushover:default")
	existing := &v1alpha1.Reminder{ObjectMeta: metav1.ObjectMeta{Namespace: w.Namespace, Name: Key(w, occAt, v1alpha1.PageEventDue, 0)}}
	var calls []string
	record := func(verb string) error { calls = append(calls, verb); return errors.New("forbidden " + verb) }
	c := fakeClient(t, interceptor.Funcs{
		Update: func(context.Context, client.WithWatch, client.Object, ...client.UpdateOption) error {
			return record("update")
		},
		Patch: func(context.Context, client.WithWatch, client.Object, client.Patch, ...client.PatchOption) error {
			return record("patch")
		},
		Delete: func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error {
			return record("delete")
		},
		DeleteAllOf: func(context.Context, client.WithWatch, client.Object, ...client.DeleteAllOfOption) error {
			return record("deleteallof")
		},
		SubResourceUpdate: func(context.Context, client.Client, string, client.Object, ...client.SubResourceUpdateOption) error {
			return record("status update")
		},
		SubResourcePatch: func(context.Context, client.Client, string, client.Object, client.Patch, ...client.SubResourcePatchOption) error {
			return record("status patch")
		},
	}, existing)
	p := &Pager{Client: c}
	for range 2 {
		p.Ensure(context.Background(), w, occAt, v1alpha1.PageEventDue, 0, "Due", "", nowAt)
		p.Refresh(context.Background(), w, v1alpha1.ScheduledWorkPage{Event: "due", Reminder: existing.Name})
	}
	if len(calls) != 0 {
		t.Fatalf("the pager wrote an existing reminder: %v", calls)
	}
	if got := list(t, c); len(got) != 1 || got[0].Spec.Title != "" {
		t.Fatalf("the existing reminder was changed: %+v", got)
	}
}

func TestEnsureSkipped(t *testing.T) {
	gr := schema.GroupResource{Group: v1alpha1.GroupVersion.Group, Resource: "reminders"}
	cases := []struct {
		name string
		work *v1alpha1.ScheduledWork
		err  error
	}{
		{"kind not served", item("pushover:default"), &meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: gr.Group, Kind: "Reminder"}, SearchedVersions: []string{"v1alpha1"}}},
		{"resource not served", item("pushover:default"), apierrors.NewNotFound(gr, "x")},
		{"create error", item("pushover:default"), errors.New("boom")},
		{"forbidden", item("pushover:default"), apierrors.NewForbidden(gr, "x", errors.New("no"))},
		{"no notify target", item(), nil},
		{"calendar-only notify", item("calendar:work"), nil},
		{"nil item", nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			creates := 0
			cl := fakeClient(t, interceptor.Funcs{Create: func(ctx context.Context, w client.WithWatch, o client.Object, opts ...client.CreateOption) error {
				creates++
				if c.err != nil {
					return c.err
				}
				return w.Create(ctx, o, opts...)
			}})
			p := &Pager{Client: cl}
			got := p.Ensure(context.Background(), c.work, occAt, v1alpha1.PageEventFailed, 1, "Failed", "why", nowAt)
			want := v1alpha1.ScheduledWorkPage{Event: "failed", Attempt: 1, State: "skipped"}
			if got != want {
				t.Fatalf("page = %+v, want %+v", got, want)
			}
			if c.err == nil && creates != 0 {
				t.Fatalf("created %d reminders for a page that cannot be sent", creates)
			}
			if n := len(list(t, cl)); n != 0 {
				t.Fatalf("%d reminders exist", n)
			}
		})
	}
}

func TestEnsureNilPagerIsSkipped(t *testing.T) {
	want := v1alpha1.ScheduledWorkPage{Event: "due", State: "skipped"}
	for _, p := range []*Pager{nil, {}} {
		if got := p.Ensure(context.Background(), item("pushover:default"), occAt, v1alpha1.PageEventDue, 0, "t", "", nowAt); got != want {
			t.Errorf("page = %+v", got)
		}
	}
}

func TestEnsureKeepsTheEntryValid(t *testing.T) {
	p := &Pager{Client: fakeClient(t, interceptor.Funcs{})}
	w := item("pushover:default")
	got := p.Ensure(context.Background(), w, occAt, "bogus", 7, "t", "", nowAt)
	if got != (v1alpha1.ScheduledWorkPage{Event: "skipped", Attempt: 3, State: "skipped"}) {
		t.Fatalf("unknown event and attempt: %+v", got)
	}
	// An attempt outside 0..ScheduledWorkMaxAttempt is held to the range in
	// the entry only: it creates nothing, so it never takes the key of a
	// real attempt's page.
	got = p.Ensure(context.Background(), w, occAt, v1alpha1.PageEventDue, -2, "t", "", nowAt)
	if got != (v1alpha1.ScheduledWorkPage{Event: "due", Attempt: 0, State: "skipped"}) {
		t.Fatalf("negative attempt: %+v", got)
	}
	got = p.Ensure(context.Background(), w, occAt, v1alpha1.PageEventDue, v1alpha1.ScheduledWorkMaxAttempt+1, "t", "", nowAt)
	if got != (v1alpha1.ScheduledWorkPage{Event: "due", Attempt: v1alpha1.ScheduledWorkMaxAttempt, State: "skipped"}) {
		t.Fatalf("attempt past the last: %+v", got)
	}
	if n := len(list(t, p.Client)); n != 0 {
		t.Fatalf("reminders = %d; an unknown event or an attempt out of range must create none", n)
	}
	// The real attempts' pages are then their own.
	for _, attempt := range []int32{0, v1alpha1.ScheduledWorkMaxAttempt} {
		got = p.Ensure(context.Background(), w, occAt, v1alpha1.PageEventDue, attempt, "real", "", nowAt)
		if got.Reminder != Key(w, occAt, v1alpha1.PageEventDue, attempt) || got.State != "" {
			t.Fatalf("attempt %d: %+v", attempt, got)
		}
	}
	for _, r := range list(t, p.Client) {
		if r.Spec.Title != "real" {
			t.Errorf("reminder %s has title %q, made by an out-of-range attempt", r.Name, r.Spec.Title)
		}
	}
}

func TestEnsureIntoRecordsWithAppendPage(t *testing.T) {
	p := &Pager{Client: fakeClient(t, interceptor.Funcs{})}
	w := item("pushover:default")
	var pages []v1alpha1.ScheduledWorkPage
	for attempt := range int32(4) {
		for _, ev := range []string{v1alpha1.PageEventDue, v1alpha1.PageEventParked, v1alpha1.PageEventFailed} {
			pages = p.EnsureInto(context.Background(), pages, w, occAt, ev, attempt, ev, "", nowAt)
			if len(pages) > v1alpha1.ScheduledWorkMaxPages {
				t.Fatalf("pages = %d, over the limit", len(pages))
			}
		}
	}
	if len(pages) != v1alpha1.ScheduledWorkMaxPages {
		t.Fatalf("pages = %d, want the limit", len(pages))
	}
	// AppendPage drops the lowest attempt first.
	for _, pg := range pages {
		if pg.Attempt == 0 {
			t.Errorf("attempt 0 page %+v survived the cap while later attempts exist", pg)
		}
	}
	// A replay replaces its own entry and creates no second Reminder.
	n := len(list(t, p.Client))
	again := p.EnsureInto(context.Background(), pages, w, occAt, v1alpha1.PageEventDue, 3, "due", "", nowAt)
	if len(again) != len(pages) || len(list(t, p.Client)) != n {
		t.Fatalf("a replay grew the list or the reminders")
	}
}

func TestRefresh(t *testing.T) {
	w := item("pushover:default")
	name := Key(w, occAt, v1alpha1.PageEventDue, 0)
	mk := func(state, uid string) *v1alpha1.Reminder {
		return &v1alpha1.Reminder{
			ObjectMeta: metav1.ObjectMeta{Namespace: w.Namespace, Name: name, Labels: map[string]string{v1alpha1.LabelScheduledWork: uid}},
			Status:     v1alpha1.ReminderStatus{State: state},
		}
	}
	page := v1alpha1.ScheduledWorkPage{Event: "due", Reminder: name}
	cases := []struct {
		name string
		objs []client.Object
		fn   interceptor.Funcs
		in   v1alpha1.ScheduledWorkPage
		want string
	}{
		{"final state is copied", []client.Object{mk(v1alpha1.ReminderSent, itemUID)}, interceptor.Funcs{}, page, "sent"},
		{"pending keeps the old state", []client.Object{mk(v1alpha1.ReminderPending, itemUID)}, interceptor.Funcs{}, v1alpha1.ScheduledWorkPage{Event: "due", Reminder: name, State: "skipped"}, "skipped"},
		{"absent reminder", nil, interceptor.Funcs{}, page, ""},
		{"another item's reminder", []client.Object{mk(v1alpha1.ReminderSent, "other-uid")}, interceptor.Funcs{}, page, ""},
		{"read error", []client.Object{mk(v1alpha1.ReminderSent, itemUID)}, interceptor.Funcs{Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return &meta.NoKindMatchError{GroupKind: schema.GroupKind{Kind: "Reminder"}}
		}}, page, ""},
		{"no reminder on the page", []client.Object{mk(v1alpha1.ReminderSent, itemUID)}, interceptor.Funcs{}, v1alpha1.ScheduledWorkPage{Event: "due", State: "skipped"}, "skipped"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := &Pager{Client: fakeClient(t, c.fn, c.objs...)}
			got := p.Refresh(context.Background(), w, c.in)
			if got.State != c.want || got.Reminder != c.in.Reminder || got.Event != c.in.Event {
				t.Fatalf("Refresh = %+v, want state %q", got, c.want)
			}
		})
	}
}

// A create error that may have come after the API server stored the
// Reminder (a timeout, a server error, a lost connection) is followed by one
// read of the page's key: a stored Reminder of this page is recorded, so a
// page that will be sent is not recorded skipped. A definite refusal reads
// nothing.
func TestEnsureAfterAnAmbiguousCreateError(t *testing.T) {
	gr := schema.GroupResource{Group: v1alpha1.GroupVersion.Group, Resource: "reminders"}
	cases := []struct {
		name   string
		err    error
		stored bool
		want   string // "" is skipped; else the entry names the Reminder
		reads  int
	}{
		{"timeout after the store", apierrors.NewTimeoutError("injected", 1), true, "named", 1},
		{"server error after the store", apierrors.NewInternalError(errors.New("injected")), true, "named", 1},
		{"connection lost after the store", errors.New("injected: connection reset by peer"), true, "named", 1},
		{"deadline before the store", context.DeadlineExceeded, false, "", 1},
		{"kind not served", &meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: gr.Group, Kind: "Reminder"}, SearchedVersions: []string{"v1alpha1"}}, false, "", 0},
		{"forbidden", apierrors.NewForbidden(gr, "x", errors.New("no")), false, "", 0},
		{"invalid", apierrors.NewInvalid(schema.GroupKind{Group: gr.Group, Kind: "Reminder"}, "x", nil), false, "", 0},
		{"bad request", apierrors.NewBadRequest("injected"), false, "", 0},
		{"unauthorized", apierrors.NewUnauthorized("injected"), false, "", 0},
		{"namespace not found", apierrors.NewNotFound(schema.GroupResource{Resource: "namespaces"}, "dev-env-system"), false, "", 0},
		{"method not supported", apierrors.NewMethodNotSupported(gr, "create"), false, "", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reads := 0
			cl := fakeClient(t, interceptor.Funcs{
				Create: func(ctx context.Context, w client.WithWatch, o client.Object, opts ...client.CreateOption) error {
					if c.stored {
						if err := w.Create(ctx, o, opts...); err != nil {
							return err
						}
					}
					return c.err
				},
				Get: func(ctx context.Context, w client.WithWatch, key client.ObjectKey, o client.Object, opts ...client.GetOption) error {
					reads++
					return w.Get(ctx, key, o, opts...)
				},
			})
			p := &Pager{Client: cl}
			w := item("pushover:default")
			got := p.Ensure(context.Background(), w, occAt, v1alpha1.PageEventResult, 2, "Result", "body", nowAt)
			want := v1alpha1.ScheduledWorkPage{Event: "result", Attempt: 2, State: "skipped"}
			if c.want != "" {
				want = v1alpha1.ScheduledWorkPage{Event: "result", Attempt: 2, Reminder: Key(w, occAt, v1alpha1.PageEventResult, 2)}
			}
			if got != want {
				t.Fatalf("page = %+v, want %+v", got, want)
			}
			if reads != c.reads {
				t.Fatalf("%d reads after the create error, want %d", reads, c.reads)
			}
		})
	}
}

// AlreadyExists records the existing Reminder only when it is this page's:
// owned by Owner and labelled with the item UID, the occurrence, the event
// and the attempt. A Reminder of the same name that is not (made by another
// identity, or another item's) is never recorded, and the entry is skipped.
func TestEnsureRecordsOnlyItsOwnExistingReminder(t *testing.T) {
	w := item("pushover:default")
	own := func(edit func(*v1alpha1.Reminder)) *v1alpha1.Reminder {
		r, err := Build(w, occAt, v1alpha1.PageEventDue, 1, "Due", "body", nowAt.Add(-time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		r.Status.State = v1alpha1.ReminderSent
		if edit != nil {
			edit(r)
		}
		return r
	}
	cases := []struct {
		name  string
		r     *v1alpha1.Reminder
		named bool
	}{
		{"its own", own(nil), true},
		{"another item's", own(func(r *v1alpha1.Reminder) { r.Labels[v1alpha1.LabelScheduledWork] = "other-uid" }), false},
		{"another occurrence's", own(func(r *v1alpha1.Reminder) { r.Labels[v1alpha1.AnnotationScheduledWorkOccurrence] = "1" }), false},
		{"another event's", own(func(r *v1alpha1.Reminder) { r.Labels[LabelEvent] = v1alpha1.PageEventParked }), false},
		{"another attempt's", own(func(r *v1alpha1.Reminder) { r.Labels[LabelAttempt] = "0" }), false},
		{"another owner's", own(func(r *v1alpha1.Reminder) { r.Spec.Owner = "human/dev-env-system/tom" }), false},
		{"no labels", own(func(r *v1alpha1.Reminder) { r.Labels = nil }), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := &Pager{Client: fakeClient(t, interceptor.Funcs{}, c.r)}
			got := p.Ensure(context.Background(), w, occAt, v1alpha1.PageEventDue, 1, "Due again", "body", nowAt)
			want := v1alpha1.ScheduledWorkPage{Event: "due", Attempt: 1, State: "skipped"}
			if c.named {
				want = v1alpha1.ScheduledWorkPage{Event: "due", Attempt: 1, Reminder: c.r.Name, State: "sent"}
			}
			if got != want {
				t.Fatalf("page = %+v, want %+v", got, want)
			}
		})
	}
	t.Run("unreadable", func(t *testing.T) {
		p := &Pager{Client: fakeClient(t, interceptor.Funcs{Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return errors.New("injected: read failed")
		}}, own(nil))}
		if got := p.Ensure(context.Background(), w, occAt, v1alpha1.PageEventDue, 1, "Due", "body", nowAt); got != (v1alpha1.ScheduledWorkPage{Event: "due", Attempt: 1, State: "skipped"}) {
			t.Fatalf("an existing Reminder that cannot be read was recorded: %+v", got)
		}
	})
}

// The verifying read after an ambiguous create error runs under its own short
// deadline, not the caller's: when the caller's deadline is what ended the
// create (a create stored just as the caller's time ran out), the read still
// finds the stored Reminder and records it.
func TestEnsureVerifiesUnderItsOwnDeadline(t *testing.T) {
	var readDeadline time.Duration
	var readHadDeadline bool
	cl := fakeClient(t, interceptor.Funcs{
		Create: func(_ context.Context, w client.WithWatch, o client.Object, opts ...client.CreateOption) error {
			if err := w.Create(context.Background(), o, opts...); err != nil {
				return err
			}
			return context.DeadlineExceeded
		},
		Get: func(ctx context.Context, w client.WithWatch, key client.ObjectKey, o client.Object, opts ...client.GetOption) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			var dl time.Time
			dl, readHadDeadline = ctx.Deadline()
			readDeadline = time.Until(dl)
			return w.Get(ctx, key, o, opts...)
		},
	})
	p := &Pager{Client: cl}
	w := item("pushover:default")
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	got := p.Ensure(ctx, w, occAt, v1alpha1.PageEventResult, 0, "Result", "body", nowAt)
	if want := (v1alpha1.ScheduledWorkPage{Event: "result", Attempt: 0, Reminder: Key(w, occAt, v1alpha1.PageEventResult, 0)}); got != want {
		t.Fatalf("page = %+v, want %+v: the stored Reminder was not found after the caller's deadline", got, want)
	}
	// About 3 seconds: short, so a dead API server cannot hold the caller.
	if !readHadDeadline || readDeadline <= 0 || readDeadline > 3*time.Second {
		t.Errorf("the verifying read had deadline %v (set %v), want one within 3s", readDeadline, readHadDeadline)
	}
}

// An invalid call (an unknown event, or an attempt outside
// 0..ScheduledWorkMaxAttempt) leaves the pages list unchanged: its clamped,
// skipped entry would otherwise replace a real page of the same event and
// attempt, and drop that page's Reminder name.
func TestEnsureIntoKeepsRealPagesFromAnInvalidCall(t *testing.T) {
	p := &Pager{Client: fakeClient(t, interceptor.Funcs{})}
	w := item("pushover:default")
	real := []v1alpha1.ScheduledWorkPage{
		{Event: v1alpha1.PageEventParked, Attempt: 3, Reminder: "swp-real-parked-3", State: "sent"},
		{Event: v1alpha1.PageEventSkipped, Attempt: 1, Reminder: "swp-real-skipped-1", State: "sent"},
	}
	for _, c := range []struct {
		name    string
		event   string
		attempt int32
	}{
		{"attempt past the last", v1alpha1.PageEventParked, v1alpha1.ScheduledWorkMaxAttempt + 2},
		{"negative attempt", v1alpha1.PageEventParked, -1},
		{"unknown event", "bogus", 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			in := slices.Clone(real)
			got := p.EnsureInto(context.Background(), in, w, occAt, c.event, c.attempt, "t", "b", nowAt)
			if !slices.Equal(got, real) || !slices.Equal(in, real) {
				t.Fatalf("pages = %+v, want the real pages unchanged %+v", got, real)
			}
		})
	}
	if n := len(list(t, p.Client)); n != 0 {
		t.Fatalf("%d reminders created by invalid calls", n)
	}
	// A valid call still records.
	got := p.EnsureInto(context.Background(), slices.Clone(real), w, occAt, v1alpha1.PageEventDue, 2, "Due", "b", nowAt)
	if len(got) != 3 || got[2].Reminder != Key(w, occAt, v1alpha1.PageEventDue, 2) {
		t.Fatalf("a valid call: %+v", got)
	}
}
