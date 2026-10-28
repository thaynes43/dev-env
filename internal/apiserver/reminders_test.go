package apiserver

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// reminderFixture is the fixture with the reminders routes on. The default
// fixture leaves them off, which TestRemindersAreOffByDefault relies on.
func reminderFixture(t *testing.T, funcs interceptor.Funcs) *fixture {
	t.Helper()
	f := newFixtureWith(t, funcs)
	f.srv.Reminders = &ReminderConfig{Namespace: "dev-env-system", CalendarRefs: []string{"haynesnetwork"}}
	f.h = f.srv.Handler()
	return f
}

func newReminder() apiv1.CreateReminderRequest {
	return apiv1.CreateReminderRequest{Title: "Renew the VPN", Body: "pay it", Due: "2026-10-30T09:00", TimeZone: "America/New_York"}
}

func (f *fixture) createReminder(token string, req apiv1.CreateReminderRequest) apiv1.Reminder {
	f.t.Helper()
	w := f.do(http.MethodPost, apiv1.RemindersPath, token, req)
	if w.Code != http.StatusCreated {
		f.t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	return decode[apiv1.Reminder](f.t, w)
}

func (f *fixture) reminder(name string) *v1alpha1.Reminder {
	f.t.Helper()
	var r v1alpha1.Reminder
	if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: "dev-env-system", Name: name}, &r); err != nil {
		f.t.Fatal(err)
	}
	return &r
}

func (f *fixture) setReminderStatus(name string, st v1alpha1.ReminderStatus) {
	f.t.Helper()
	r := f.reminder(name)
	r.Status = st
	if err := f.c.Update(context.Background(), r); err != nil {
		f.t.Fatal(err)
	}
}

func TestRemindersAreOffByDefault(t *testing.T) {
	f := newFixture(t)
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, apiv1.RemindersPath}, {http.MethodGet, apiv1.RemindersPath},
		{http.MethodGet, apiv1.ReminderPath("rem-a")}, {http.MethodPost, apiv1.ReminderCancelPath("rem-a")},
	} {
		if w := f.do(c.method, c.path, tokHuman, newReminder()); w.Code != http.StatusNotFound {
			t.Errorf("%s %s answered %d with the routes off", c.method, c.path, w.Code)
		}
	}
}

func TestCreateReminderTakesTheOwnerFromTheToken(t *testing.T) {
	f := reminderFixture(t, interceptor.Funcs{})
	tok := f.sessionPod("haynes-ops-1028-010000", "full", 0)
	rem := f.createReminder(tok, newReminder())
	owner := "session/haynes-ops-1028-010000/" + string(f.session("haynes-ops-1028-010000").UID)
	if rem.Owner != owner || rem.State != "pending" || len(rem.Targets) != 1 || rem.Targets[0] != "pushover:default" || rem.Title != "Renew the VPN" {
		t.Errorf("reminder %+v", rem)
	}
	if got := f.reminder(rem.Name).Spec.Owner; got != owner {
		t.Errorf("stored owner %q", got)
	}
	// An owner in the body is not a field: the request is strict.
	wantError(t, f.do(http.MethodPost, apiv1.RemindersPath, tok, `{"title":"x","owner":"human/x","due":"2026-10-30T09:00","timeZone":"UTC"}`), http.StatusBadRequest, apiv1.CodeBadRequest)
	if w := f.do(http.MethodPost, apiv1.RemindersPath, "", newReminder()); w.Code != http.StatusUnauthorized {
		t.Errorf("no token: %d", w.Code)
	}
	wantError(t, f.do(http.MethodPost, apiv1.RemindersPath, tokStranger, newReminder()), http.StatusForbidden, apiv1.CodeForbidden)
}

func TestCreateReminderIsIdempotentByKey(t *testing.T) {
	f := reminderFixture(t, interceptor.Funcs{})
	tok := f.sessionPod("s-one", "full", 0)
	req := newReminder()
	req.Key = "vpn-renewal"
	first := f.createReminder(tok, req)
	w := f.do(http.MethodPost, apiv1.RemindersPath, tok, req)
	if w.Code != http.StatusOK {
		t.Fatalf("replay: %d %s", w.Code, w.Body.String())
	}
	if again := decode[apiv1.Reminder](t, w); again.Name != first.Name {
		t.Errorf("a replay made %s, not %s", again.Name, first.Name)
	}
	var list v1alpha1.ReminderList
	if err := f.c.List(context.Background(), &list); err != nil || len(list.Items) != 1 {
		t.Fatalf("%d reminders after a replay (%v)", len(list.Items), err)
	}
	req.Title = "something else"
	wantError(t, f.do(http.MethodPost, apiv1.RemindersPath, tok, req), http.StatusConflict, apiv1.CodeConflict)
	// Another caller's same key is another reminder.
	tok2 := f.sessionPod("s-two", "full", 0)
	other := newReminder()
	other.Key = "vpn-renewal"
	if o := f.createReminder(tok2, other); o.Name == first.Name {
		t.Error("two callers' keys collided")
	}
}

func TestCreateReminderValidatesAndAllowlistsTargets(t *testing.T) {
	f := reminderFixture(t, interceptor.Funcs{})
	tok := f.sessionPod("s-one", "full", 0)
	good := func(edit func(*apiv1.CreateReminderRequest)) apiv1.CreateReminderRequest {
		r := newReminder()
		edit(&r)
		return r
	}
	for name, c := range map[string]struct {
		req   apiv1.CreateReminderRequest
		field string
	}{
		"no title":         {good(func(r *apiv1.CreateReminderRequest) { r.Title = " " }), "title"},
		"a long title":     {good(func(r *apiv1.CreateReminderRequest) { r.Title = string(make([]byte, 121)) }), "title"},
		"a bad zone":       {good(func(r *apiv1.CreateReminderRequest) { r.TimeZone = "Mars/Olympus" }), "schedule"},
		"a bad due":        {good(func(r *apiv1.CreateReminderRequest) { r.Due = "tomorrow" }), "schedule"},
		"a past due":       {good(func(r *apiv1.CreateReminderRequest) { r.Due = "2026-10-01T09:00" }), "due"},
		"an address":       {good(func(r *apiv1.CreateReminderRequest) { r.Targets = []string{"calendar:tom@haynesnetwork.com"} }), "targets"},
		"a URL":            {good(func(r *apiv1.CreateReminderRequest) { r.Targets = []string{"https://example.com/hook"} }), "targets"},
		"another pushover": {good(func(r *apiv1.CreateReminderRequest) { r.Targets = []string{"pushover:other"} }), "targets"},
		"an unlisted cal":  {good(func(r *apiv1.CreateReminderRequest) { r.Targets = []string{"calendar:elsewhere"} }), "targets"},
		"a bad recurrence": {good(func(r *apiv1.CreateReminderRequest) { r.Recurrence = &apiv1.ReminderRecurrence{Freq: "hourly"} }), "schedule"},
		"an ended series": {good(func(r *apiv1.CreateReminderRequest) {
			r.Due = "2020-01-01T09:00"
			r.Recurrence = &apiv1.ReminderRecurrence{Freq: "daily", Count: 3}
		}), "recurrence"},
		"a body over Pushover's limit":     {good(func(r *apiv1.CreateReminderRequest) { r.Body = strings.Repeat("x", 1025) }), "body"},
		"a multi-byte body over the limit": {good(func(r *apiv1.CreateReminderRequest) { r.Body = strings.Repeat("日", 342) }), "body"},
		"a very long key":                  {good(func(r *apiv1.CreateReminderRequest) { r.Key = string(make([]byte, 129)) }), "key"},
		"duplicate targets":                {good(func(r *apiv1.CreateReminderRequest) { r.Targets = []string{"pushover:default", "pushover:default"} }), "targets"},
	} {
		t.Run(name, func(t *testing.T) {
			e := wantError(t, f.do(http.MethodPost, apiv1.RemindersPath, tok, c.req), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
			found := false
			for _, fe := range e.Fields {
				found = found || fe.Field == c.field
			}
			if !found {
				t.Errorf("fields %+v lack %q", e.Fields, c.field)
			}
		})
	}
	// A body of exactly Pushover's 1024 bytes, multi-byte at the end, is accepted.
	edge := newReminder()
	edge.Body = strings.Repeat("x", 1021) + "日"
	if rem := f.createReminder(tok, edge); len(rem.Body) != 1024 {
		t.Errorf("body of %d bytes", len(rem.Body))
	}
	// The listed calendar reference and a recurrence are accepted.
	ok := good(func(r *apiv1.CreateReminderRequest) {
		r.Targets = []string{"pushover:default", "calendar:haynesnetwork"}
		r.Recurrence = &apiv1.ReminderRecurrence{Freq: "weekly", ByDay: []string{"MO", "TH"}}
	})
	if rem := f.createReminder(tok, ok); len(rem.Targets) != 2 || rem.Recurrence == nil {
		t.Errorf("accepted reminder %+v", rem)
	}
	// With no calendar configured, a calendar target is refused.
	f.srv.Reminders.CalendarRefs = nil
	wantError(t, f.do(http.MethodPost, apiv1.RemindersPath, tok, ok), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
}

func TestCreateReminderCapsLiveRemindersPerOwner(t *testing.T) {
	f := reminderFixture(t, interceptor.Funcs{})
	tok := f.sessionPod("s-one", "full", 0)
	for i := range maxLiveRemindersPerOwner {
		r := &v1alpha1.Reminder{ObjectMeta: metav1.ObjectMeta{Namespace: "dev-env-system", Name: fmt.Sprintf("rem-fill-%d", i)},
			Spec: v1alpha1.ReminderSpec{Title: "x", Owner: "session/s-one/" + string(f.session("s-one").UID), Due: "2026-10-30T09:00", TimeZone: "UTC", Targets: []string{"pushover:default"}}}
		if i == 0 {
			r.Status.State = v1alpha1.ReminderSent // a finished one does not count
		}
		if err := f.c.Create(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
	f.createReminder(tok, newReminder()) // 99 live + 1
	wantError(t, f.do(http.MethodPost, apiv1.RemindersPath, tok, newReminder()), http.StatusTooManyRequests, apiv1.CodeLimitExceeded)
}

func TestListAndShowRespectOwnership(t *testing.T) {
	f := reminderFixture(t, interceptor.Funcs{})
	one, two := f.sessionPod("s-one", "full", 0), f.sessionPod("s-two", "full", 0)
	a := f.createReminder(one, newReminder())
	b := f.createReminder(two, newReminder())

	l := decode[apiv1.ReminderList](t, f.do(http.MethodGet, apiv1.RemindersPath, one, nil))
	if len(l.Reminders) != 1 || l.Reminders[0].Name != a.Name {
		t.Errorf("one's list %+v", l)
	}
	wantError(t, f.do(http.MethodGet, apiv1.RemindersPath+"?all=true", one, nil), http.StatusForbidden, apiv1.CodeForbidden)
	if l := decode[apiv1.ReminderList](t, f.do(http.MethodGet, apiv1.RemindersPath+"?all=true", tokHuman, nil)); len(l.Reminders) != 2 {
		t.Errorf("the human's all list %+v", l)
	}
	// A client that is not the owner sees none of them.
	if l := decode[apiv1.ReminderList](t, f.do(http.MethodGet, apiv1.RemindersPath, tokClient, nil)); len(l.Reminders) != 0 {
		t.Errorf("a client's list %+v", l)
	}

	if w := f.do(http.MethodGet, apiv1.ReminderPath(a.Name), one, nil); w.Code != http.StatusOK {
		t.Errorf("owner show: %d", w.Code)
	}
	wantError(t, f.do(http.MethodGet, apiv1.ReminderPath(b.Name), one, nil), http.StatusNotFound, apiv1.CodeNotFound)
	if w := f.do(http.MethodGet, apiv1.ReminderPath(b.Name), tokHuman, nil); w.Code != http.StatusOK {
		t.Errorf("human show: %d", w.Code)
	}
	wantError(t, f.do(http.MethodGet, apiv1.ReminderPath("rem-nope"), tokHuman, nil), http.StatusNotFound, apiv1.CodeNotFound)
}

func TestCanManage(t *testing.T) {
	r := &v1alpha1.Reminder{Spec: v1alpha1.ReminderSpec{Owner: "session/a"}}
	for name, c := range map[string]struct {
		c    *caller
		want bool
	}{
		"the owner":       {&caller{kind: kindSession, parent: "a"}, true},
		"another session": {&caller{kind: kindSession, parent: "b"}, false},
		"a client":        {&caller{kind: kindClient, parent: "dev/dev-env"}, false},
		"a human":         {&caller{kind: kindHuman, parent: "h"}, true},
		"a coordinator":   {&caller{kind: kindCoordinator, parent: "dev/host"}, true},
	} {
		if got := canManage(c.c, r); got != c.want {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}

func TestEditReminder(t *testing.T) {
	f := reminderFixture(t, interceptor.Funcs{})
	one, two := f.sessionPod("s-one", "full", 0), f.sessionPod("s-two", "full", 0)
	a := f.createReminder(one, newReminder())
	title := "Renew the VPN today"
	w := f.do(http.MethodPatch, apiv1.ReminderPath(a.Name), one, apiv1.EditReminderRequest{Title: &title})
	if w.Code != http.StatusOK || decode[apiv1.Reminder](t, w).Title != title {
		t.Fatalf("edit: %d %s", w.Code, w.Body.String())
	}
	if f.reminder(a.Name).Spec.Owner != "session/s-one/"+string(f.session("s-one").UID) {
		t.Error("an edit changed the owner")
	}
	wantError(t, f.do(http.MethodPatch, apiv1.ReminderPath(a.Name), two, apiv1.EditReminderRequest{Title: &title}), http.StatusNotFound, apiv1.CodeNotFound)
	due := "2026-10-31T10:30"
	if w := f.do(http.MethodPatch, apiv1.ReminderPath(a.Name), tokHuman, apiv1.EditReminderRequest{Due: &due, Recurrence: &apiv1.ReminderRecurrence{Freq: "daily", Count: 3}}); w.Code != http.StatusOK {
		t.Errorf("a human's edit: %d %s", w.Code, w.Body.String())
	}
	if got := f.reminder(a.Name).Spec; got.Due != due || got.Recurrence == nil || got.Recurrence.Count != 3 {
		t.Errorf("spec %+v", got)
	}
	if w := f.do(http.MethodPatch, apiv1.ReminderPath(a.Name), one, apiv1.EditReminderRequest{ClearRecurrence: true}); w.Code != http.StatusOK || f.reminder(a.Name).Spec.Recurrence != nil {
		t.Errorf("clear recurrence: %d", w.Code)
	}
	bad := "Mars/Olympus"
	wantError(t, f.do(http.MethodPatch, apiv1.ReminderPath(a.Name), one, apiv1.EditReminderRequest{TimeZone: &bad}), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
	wantError(t, f.do(http.MethodPatch, apiv1.ReminderPath(a.Name), one, apiv1.EditReminderRequest{Targets: []string{"pushover:other"}}), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
	wantError(t, f.do(http.MethodPatch, apiv1.ReminderPath(a.Name), one, apiv1.EditReminderRequest{ClearRecurrence: true, Recurrence: &apiv1.ReminderRecurrence{Freq: "daily"}}), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
	past := "2026-10-01T09:00"
	wantError(t, f.do(http.MethodPatch, apiv1.ReminderPath(a.Name), one, apiv1.EditReminderRequest{Due: &past}), http.StatusUnprocessableEntity, apiv1.CodeInvalid)

	// Only a pending reminder is edited.
	f.setReminderStatus(a.Name, v1alpha1.ReminderStatus{State: v1alpha1.ReminderSent})
	wantError(t, f.do(http.MethodPatch, apiv1.ReminderPath(a.Name), one, apiv1.EditReminderRequest{Title: &title}), http.StatusConflict, apiv1.CodeConflict)
}

func TestEditAfterAFireDoesNotRequireAFutureDueForTextOnlyEdits(t *testing.T) {
	f := reminderFixture(t, interceptor.Funcs{})
	one := f.sessionPod("s-one", "full", 0)
	a := f.createReminder(one, newReminder())
	// Time passes: the due time is now behind the clock, and the reminder is still pending.
	f.now = f.now.Add(30 * 24 * 3600 * 1e9)
	title := "new text"
	if w := f.do(http.MethodPatch, apiv1.ReminderPath(a.Name), one, apiv1.EditReminderRequest{Title: &title}); w.Code != http.StatusOK {
		t.Errorf("a text edit: %d %s", w.Code, w.Body.String())
	}
}

// An edit that races the scheduler's write loses the compare-and-set once and
// succeeds on the retry against the new state.
func TestEditAndCancelRetryOnAConflict(t *testing.T) {
	conflicts := 0
	f := reminderFixture(t, interceptor.Funcs{Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
		if _, ok := obj.(*v1alpha1.Reminder); ok && conflicts > 0 {
			conflicts--
			return apierrors.NewConflict(schema.GroupResource{Group: "dev-env.haynesops.com", Resource: "reminders"}, obj.GetName(), fmt.Errorf("the object has been modified"))
		}
		return c.Update(ctx, obj, opts...)
	}})
	one := f.sessionPod("s-one", "full", 0)
	a := f.createReminder(one, newReminder())
	title := "raced"
	conflicts = 2
	if w := f.do(http.MethodPatch, apiv1.ReminderPath(a.Name), one, apiv1.EditReminderRequest{Title: &title}); w.Code != http.StatusOK {
		t.Fatalf("edit after two conflicts: %d %s", w.Code, w.Body.String())
	}
	conflicts = 100
	wantError(t, f.do(http.MethodPost, apiv1.ReminderCancelPath(a.Name), one, nil), http.StatusConflict, apiv1.CodeConflict)
	conflicts = 1
	if w := f.do(http.MethodPost, apiv1.ReminderCancelPath(a.Name), one, nil); w.Code != http.StatusOK {
		t.Fatalf("cancel after a conflict: %d %s", w.Code, w.Body.String())
	}
}

func TestCancelReminder(t *testing.T) {
	f := reminderFixture(t, interceptor.Funcs{})
	one, two := f.sessionPod("s-one", "full", 0), f.sessionPod("s-two", "full", 0)
	a := f.createReminder(one, newReminder())
	wantError(t, f.do(http.MethodPost, apiv1.ReminderCancelPath(a.Name), two, nil), http.StatusNotFound, apiv1.CodeNotFound)
	w := f.do(http.MethodPost, apiv1.ReminderCancelPath(a.Name), one, nil)
	if w.Code != http.StatusOK || decode[apiv1.Reminder](t, w).State != "cancelled" {
		t.Fatalf("cancel: %d %s", w.Code, w.Body.String())
	}
	if !f.reminder(a.Name).Spec.Cancelled {
		t.Error("the spec is not cancelled")
	}
	// Idempotent, and a human may cancel too.
	if w := f.do(http.MethodPost, apiv1.ReminderCancelPath(a.Name), tokHuman, nil); w.Code != http.StatusOK {
		t.Errorf("second cancel: %d", w.Code)
	}
	title := "x"
	wantError(t, f.do(http.MethodPatch, apiv1.ReminderPath(a.Name), one, apiv1.EditReminderRequest{Title: &title}), http.StatusConflict, apiv1.CodeConflict)

	// Cancel loses to a send that already finished.
	b := f.createReminder(one, newReminder())
	f.setReminderStatus(b.Name, v1alpha1.ReminderStatus{State: v1alpha1.ReminderSent})
	wantError(t, f.do(http.MethodPost, apiv1.ReminderCancelPath(b.Name), one, nil), http.StatusConflict, apiv1.CodeConflict)

	// A reminder that ended skipped is final too.
	d := f.createReminder(one, newReminder())
	f.setReminderStatus(d.Name, v1alpha1.ReminderStatus{State: v1alpha1.ReminderSkipped})
	wantError(t, f.do(http.MethodPost, apiv1.ReminderCancelPath(d.Name), one, nil), http.StatusConflict, apiv1.CodeConflict)

	// Cancel racing a delivery in flight says so.
	c := f.createReminder(one, newReminder())
	f.setReminderStatus(c.Name, v1alpha1.ReminderStatus{State: v1alpha1.ReminderPending, Deliveries: []v1alpha1.ReminderDelivery{
		{Target: "pushover:default", Occurrence: 1, State: v1alpha1.DeliveryClaimed, Attempts: 1, UpdatedAt: metav1.Now()}}})
	got := decode[apiv1.Reminder](t, f.do(http.MethodPost, apiv1.ReminderCancelPath(c.Name), one, nil))
	if got.Warning == "" {
		t.Errorf("no warning for an in-flight delivery: %+v", got)
	}
}

// Two creates with one key race: the loser's create finds the winner's reminder
// and returns it, as a retry would, instead of a 409.
func TestConcurrentCreatesWithOneKeyReturnTheExistingReminder(t *testing.T) {
	var winner func(obj client.Object)
	f := reminderFixture(t, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		if r, ok := obj.(*v1alpha1.Reminder); ok && winner != nil {
			w := r.DeepCopy()
			winner(w)
			winner = nil
			if err := c.Create(ctx, w); err != nil { // the other request got there first
				return err
			}
			return apierrors.NewAlreadyExists(schema.GroupResource{Group: "dev-env.haynesops.com", Resource: "reminders"}, r.Name)
		}
		return c.Create(ctx, obj, opts...)
	}})
	tok := f.sessionPod("s-one", "full", 0)
	req := newReminder()
	req.Key = "vpn"

	winner = func(obj client.Object) {} // the same content: a retry's twin
	w := f.do(http.MethodPost, apiv1.RemindersPath, tok, req)
	if w.Code != http.StatusOK {
		t.Fatalf("the losing twin: %d %s", w.Code, w.Body.String())
	}
	var list v1alpha1.ReminderList
	if err := f.c.List(context.Background(), &list); err != nil || len(list.Items) != 1 || list.Items[0].Name != decode[apiv1.Reminder](t, w).Name {
		t.Fatalf("%d reminders (%v)", len(list.Items), err)
	}

	// The winner made something else under the same key: that is a real conflict.
	req2 := newReminder()
	req2.Key = "other"
	winner = func(obj client.Object) { obj.(*v1alpha1.Reminder).Spec.Title = "not what was asked" }
	wantError(t, f.do(http.MethodPost, apiv1.RemindersPath, tok, req2), http.StatusConflict, apiv1.CodeConflict)
}

// A later session that reuses a session's name inherits none of its reminders.
func TestASessionThatReusesANameInheritsNoReminders(t *testing.T) {
	f := reminderFixture(t, interceptor.Funcs{})
	old := f.sessionPod("s-one", "full", 0)
	a := f.createReminder(old, newReminder())

	ctx := context.Background()
	s := f.session("s-one")
	s.Finalizers = nil
	if err := f.c.Update(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := f.c.Delete(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := f.c.Delete(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "s-one", Namespace: sessionNS}}); err != nil {
		t.Fatal(err)
	}
	fresh := f.sessionPod("s-one", "full", 0) // same name, new UID

	if l := decode[apiv1.ReminderList](t, f.do(http.MethodGet, apiv1.RemindersPath, fresh, nil)); len(l.Reminders) != 0 {
		t.Errorf("the new session lists the old one's reminders: %+v", l)
	}
	wantError(t, f.do(http.MethodGet, apiv1.ReminderPath(a.Name), fresh, nil), http.StatusNotFound, apiv1.CodeNotFound)
	wantError(t, f.do(http.MethodPost, apiv1.ReminderCancelPath(a.Name), fresh, nil), http.StatusNotFound, apiv1.CodeNotFound)
	if w := f.do(http.MethodGet, apiv1.ReminderPath(a.Name), tokHuman, nil); w.Code != http.StatusOK {
		t.Errorf("a human no longer sees it: %d", w.Code)
	}
}
