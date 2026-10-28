package apiserver

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/reminders"
)

// ReminderConfig turns the reminders routes on (DESIGN-032). A nil
// Server.Reminders registers none of them.
type ReminderConfig struct {
	// Namespace holds the Reminder objects (dev-env-system).
	Namespace string
	// CalendarRefs are the calendar references a reminder may name, from the
	// operator's flag. Empty means no calendar target is accepted.
	CalendarRefs []string
}

const (
	maxReminderBody = 32 << 10
	// A caller may hold this many live reminders; a finished one does not count.
	maxLiveRemindersPerOwner = 100
	// pastGrace is how far back a due time may be and still be accepted: a
	// "5pm" typed at 5:00:20 is fine, a date last week is a typo.
	pastGrace   = 5 * time.Minute
	editRetries = 4
)

func (s *Server) registerReminders(mux *http.ServeMux) {
	if s.Reminders == nil {
		return
	}
	mux.Handle(apiv1.RemindersPath, s.serve(route{coordinator: true, methods: map[string]handler{
		http.MethodPost: s.createReminder, http.MethodGet: s.listReminders}}))
	mux.Handle(apiv1.RemindersPath+"/{name}", s.serve(route{coordinator: true, methods: map[string]handler{
		http.MethodGet: s.showReminder, http.MethodPatch: s.editReminder}}))
	mux.Handle(apiv1.RemindersPath+"/{name}/cancel", s.serve(route{coordinator: true, methods: map[string]handler{
		http.MethodPost: s.cancelReminder}}))
}

// reminderOwner is the owner a reminder records: the caller as its token says.
func reminderOwner(c *caller) string { return c.String() }

// canManage: the owner, a human or a coordinator.
func canManage(c *caller, r *v1alpha1.Reminder) bool {
	return r.Spec.Owner == reminderOwner(c) || c.kind == kindHuman || c.kind == kindCoordinator
}

func reminderName(owner, key string) (string, error) {
	if key != "" {
		sum := sha256.Sum256([]byte(owner + "\x00" + key))
		return "rem-" + hex.EncodeToString(sum[:])[:20], nil
	}
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "rem-" + hex.EncodeToString(b), nil
}

func toSpecRecurrence(in *apiv1.ReminderRecurrence) *v1alpha1.ReminderRecurrence {
	if in == nil {
		return nil
	}
	out := &v1alpha1.ReminderRecurrence{Freq: in.Freq, ByDay: slices.Clone(in.ByDay), ByMonthDay: int32(in.ByMonthDay), Count: int32(in.Count)}
	if in.Until != nil {
		t := metav1.NewTime(in.Until.UTC())
		out.Until = &t
	}
	return out
}

// validateReminder checks a whole spec the way the scheduler will read it. The
// due time must also not be far in the past.
func (s *Server) validateReminder(spec *v1alpha1.ReminderSpec, now time.Time, requireFuture bool) []apiv1.FieldError {
	var fields []apiv1.FieldError
	add := func(field, format string, args ...any) { fields = append(fields, fieldError(field, format, args...)) }
	spec.Title = strings.TrimSpace(spec.Title)
	switch {
	case spec.Title == "":
		add("title", "required")
	case len(spec.Title) > 120:
		add("title", "%d bytes, more than 120", len(spec.Title))
	}
	if len(spec.Body) > 2000 {
		add("body", "%d bytes, more than 2000", len(spec.Body))
	}
	if len(spec.Targets) == 0 {
		spec.Targets = []string{v1alpha1.ReminderDefaultPushover}
	}
	if err := reminders.CheckTargets(spec.Targets, s.Reminders.CalendarRefs); err != nil {
		add("targets", "%v", err)
	}
	sch, err := reminders.ScheduleOf(*spec)
	if err != nil {
		add("schedule", "%v", err)
		return fields
	}
	if first, ok := sch.Next(time.Time{}); !ok {
		add("due", "the recurrence has no occurrence")
	} else if !requireFuture {
		// An edit of the text alone need not be a future one.
	} else if !sch.Recurring() && first.At.Before(now.Add(-pastGrace)) {
		add("due", "%s is in the past", first.At.UTC().Format(time.RFC3339))
	} else if sch.Recurring() {
		if _, ok := sch.Next(now.Add(-pastGrace)); !ok {
			add("recurrence", "the series has already ended")
		}
	}
	return fields
}

// createReminder serves POST /v1/reminders.
func (s *Server) createReminder(ctx context.Context, w http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	var req apiv1.CreateReminderRequest
	if err := decodeJSON(w, r, maxReminderBody, &req, true); err != nil {
		return 0, nil, err
	}
	if len(req.Key) > 128 {
		return 0, nil, invalid(fieldError("key", "%d bytes, more than 128", len(req.Key)))
	}
	owner := reminderOwner(c)
	spec := v1alpha1.ReminderSpec{
		Title: req.Title, Body: req.Body, Owner: owner, Due: strings.TrimSpace(req.Due), TimeZone: strings.TrimSpace(req.TimeZone),
		Recurrence: toSpecRecurrence(req.Recurrence), Targets: slices.Clone(req.Targets),
	}
	if fields := s.validateReminder(&spec, s.now(), true); len(fields) > 0 {
		return 0, nil, invalid(fields...)
	}
	name, err := reminderName(owner, req.Key)
	if err != nil {
		return 0, nil, internal("name the reminder: %v", err)
	}
	var list v1alpha1.ReminderList
	if err := s.Live.List(ctx, &list, client.InNamespace(s.Reminders.Namespace)); err != nil {
		return 0, nil, fromKubeError(err, "reminders")
	}
	live := 0
	for i := range list.Items {
		it := &list.Items[i]
		if it.Name == name {
			// A retried create returns what the first one made.
			if it.Spec.Owner != owner || !reflect.DeepEqual(it.Spec, spec) {
				return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict, "key %q already names a different reminder of yours", req.Key)
			}
			return http.StatusOK, reminderView(it), nil
		}
		if it.Spec.Owner == owner && !it.Spec.Cancelled && !finalReminder(it.Status.State) {
			live++
		}
	}
	if live >= maxLiveRemindersPerOwner {
		return 0, nil, tooManyRequests("you hold %d live reminders, the most allowed; cancel one first", live)
	}
	rem := &v1alpha1.Reminder{ObjectMeta: metav1.ObjectMeta{Namespace: s.Reminders.Namespace, Name: name}, Spec: spec}
	if err := s.Client.Create(ctx, rem); err != nil {
		return 0, nil, fromKubeError(err, "reminder")
	}
	s.Log.Info("reminder created", "reminder", name, "owner", owner, "targets", strings.Join(spec.Targets, ","))
	return http.StatusCreated, reminderView(rem), nil
}

func finalReminder(state string) bool {
	return state == v1alpha1.ReminderSent || state == v1alpha1.ReminderFailed || state == v1alpha1.ReminderCancelled
}

// listReminders serves GET /v1/reminders: the caller's own. all=true lists
// everyone's, for a human or a coordinator only.
func (s *Server) listReminders(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	all := r.URL.Query().Get("all") == "true"
	if all && c.kind != kindHuman && c.kind != kindCoordinator {
		return 0, nil, forbidden("only a human or a coordinator may list everyone's reminders")
	}
	var list v1alpha1.ReminderList
	if err := s.Live.List(ctx, &list, client.InNamespace(s.Reminders.Namespace)); err != nil {
		return 0, nil, fromKubeError(err, "reminders")
	}
	owner := reminderOwner(c)
	out := apiv1.ReminderList{Reminders: []apiv1.Reminder{}}
	for i := range list.Items {
		if all || list.Items[i].Spec.Owner == owner {
			out.Reminders = append(out.Reminders, reminderView(&list.Items[i]))
		}
	}
	slices.SortFunc(out.Reminders, func(a, b apiv1.Reminder) int {
		if d := b.CreatedAt.Compare(a.CreatedAt); d != 0 {
			return d
		}
		return strings.Compare(a.Name, b.Name)
	})
	return http.StatusOK, out, nil
}

// getManaged reads a reminder the caller may manage. Anyone else gets a 404, so
// a name tells nothing about whose it is.
func (s *Server) getManaged(ctx context.Context, name string, c *caller) (*v1alpha1.Reminder, error) {
	var rem v1alpha1.Reminder
	if err := s.Live.Get(ctx, types.NamespacedName{Namespace: s.Reminders.Namespace, Name: name}, &rem); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, notFound("no reminder %s", name)
		}
		return nil, fromKubeError(err, "reminder "+name)
	}
	if !canManage(c, &rem) {
		return nil, notFound("no reminder %s", name)
	}
	return &rem, nil
}

// showReminder serves GET /v1/reminders/{name}.
func (s *Server) showReminder(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	rem, err := s.getManaged(ctx, r.PathValue("name"), c)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, reminderView(rem), nil
}

// editReminder serves PATCH /v1/reminders/{name}. The write carries the
// resourceVersion it read, so an edit that races the scheduler loses cleanly
// and is retried on the new state.
func (s *Server) editReminder(ctx context.Context, w http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	var req apiv1.EditReminderRequest
	if err := decodeJSON(w, r, maxReminderBody, &req, true); err != nil {
		return 0, nil, err
	}
	if req.ClearRecurrence && req.Recurrence != nil {
		return 0, nil, invalid(fieldError("recurrence", "clearRecurrence and recurrence are exclusive"))
	}
	for range editRetries {
		rem, err := s.getManaged(ctx, r.PathValue("name"), c)
		if err != nil {
			return 0, nil, err
		}
		if rem.Spec.Cancelled || finalReminder(rem.Status.State) {
			state := rem.Status.State
			if rem.Spec.Cancelled {
				state = v1alpha1.ReminderCancelled
			}
			return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict, "reminder %s is %s; only a pending reminder can be edited", rem.Name, state)
		}
		spec := *rem.Spec.DeepCopy()
		if req.Title != nil {
			spec.Title = *req.Title
		}
		if req.Body != nil {
			spec.Body = *req.Body
		}
		if req.Due != nil {
			spec.Due = strings.TrimSpace(*req.Due)
		}
		if req.TimeZone != nil {
			spec.TimeZone = strings.TrimSpace(*req.TimeZone)
		}
		if req.ClearRecurrence {
			spec.Recurrence = nil
		}
		if req.Recurrence != nil {
			spec.Recurrence = toSpecRecurrence(req.Recurrence)
		}
		if req.Targets != nil {
			spec.Targets = slices.Clone(req.Targets)
		}
		if reflect.DeepEqual(spec, rem.Spec) {
			return http.StatusOK, reminderView(rem), nil
		}
		// A schedule edit must still be a future one; an edit of the text alone
		// need not be.
		if fields := s.validateReminder(&spec, s.now(), reminders.PlanHash(spec) != reminders.PlanHash(rem.Spec)); len(fields) > 0 {
			return 0, nil, invalid(fields...)
		}
		rem.Spec = spec
		err = s.Client.Update(ctx, rem)
		if err == nil {
			s.Log.Info("reminder edited", "reminder", rem.Name, "caller", c.String())
			return http.StatusOK, reminderView(rem), nil
		}
		if !apierrors.IsConflict(err) {
			return 0, nil, fromKubeError(err, "reminder "+rem.Name)
		}
	}
	return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict, "the reminder kept changing; try again")
}

// cancelReminder serves POST /v1/reminders/{name}/cancel. It is idempotent.
func (s *Server) cancelReminder(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	for range editRetries {
		rem, err := s.getManaged(ctx, r.PathValue("name"), c)
		if err != nil {
			return 0, nil, err
		}
		if rem.Spec.Cancelled {
			return http.StatusOK, reminderView(rem), nil
		}
		if finalReminder(rem.Status.State) {
			return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict, "reminder %s is already %s; there is nothing to cancel", rem.Name, rem.Status.State)
		}
		rem.Spec.Cancelled = true
		err = s.Client.Update(ctx, rem)
		if err == nil {
			s.Log.Info("reminder cancelled", "reminder", rem.Name, "caller", c.String())
			v := reminderView(rem)
			for _, d := range rem.Status.Deliveries {
				if d.State == v1alpha1.DeliveryClaimed {
					v.Warning = "a delivery was already in flight and may still be sent; a request that has left cannot be recalled"
				}
			}
			return http.StatusOK, v, nil
		}
		if !apierrors.IsConflict(err) {
			return 0, nil, fromKubeError(err, "reminder "+rem.Name)
		}
	}
	return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict, "the reminder kept changing; try again")
}

func reminderView(r *v1alpha1.Reminder) apiv1.Reminder {
	v := apiv1.Reminder{
		Name: r.Name, Owner: r.Spec.Owner, Title: r.Spec.Title, Body: r.Spec.Body, Due: r.Spec.Due, TimeZone: r.Spec.TimeZone,
		Targets: slices.Clone(r.Spec.Targets), State: r.Status.State, Occurrence: int(r.Status.Occurrence), Message: r.Status.Message,
		CreatedAt: r.CreationTimestamp.UTC(),
	}
	if v.State == "" {
		v.State = v1alpha1.ReminderPending
	}
	if r.Spec.Cancelled {
		v.State = v1alpha1.ReminderCancelled
	}
	if rec := r.Spec.Recurrence; rec != nil {
		v.Recurrence = &apiv1.ReminderRecurrence{Freq: rec.Freq, ByDay: slices.Clone(rec.ByDay), ByMonthDay: int(rec.ByMonthDay), Count: int(rec.Count)}
		if rec.Until != nil {
			u := rec.Until.UTC()
			v.Recurrence.Until = &u
		}
	}
	if r.Status.NextDue != nil && !r.Spec.Cancelled {
		t := r.Status.NextDue.UTC()
		v.NextDue = &t
	}
	for _, d := range r.Status.Deliveries {
		out := apiv1.ReminderDelivery{Target: d.Target, Occurrence: int(d.Occurrence), State: d.State, Attempts: int(d.Attempts), Reason: d.Reason, Receipt: d.Receipt, UpdatedAt: d.UpdatedAt.UTC()}
		if d.NotBefore != nil {
			t := d.NotBefore.UTC()
			out.NotBefore = &t
		}
		v.Deliveries = append(v.Deliveries, out)
	}
	for _, cs := range r.Status.Calendar {
		v.Calendar = append(v.Calendar, apiv1.ReminderCalendar{Target: cs.Target, EventID: cs.EventID, State: cs.State, Reason: cs.Reason, UpdatedAt: cs.UpdatedAt.UTC()})
	}
	for _, a := range r.Status.Attempts {
		v.Attempts = append(v.Attempts, apiv1.ReminderAttempt{At: a.At.UTC(), Occurrence: int(a.Occurrence), Target: a.Target, Outcome: a.Outcome, Detail: a.Detail})
	}
	return v
}
