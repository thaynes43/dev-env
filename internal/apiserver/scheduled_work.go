package apiserver

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/reminders"
	"github.com/thaynes43/dev-env/internal/scheduledwork"
	"github.com/thaynes43/dev-env/internal/scheduledwork/authority"
)

// The scheduled-work routes (DESIGN-054 S5, OpenProject #397), the ones
// agent-run schedule calls (internal/agentrun/schedule.go):
//
//	POST   /v1/scheduled-work                  create: any caller; the owner and editor are its token
//	GET    /v1/scheduled-work                  the caller's own; all=true for a human or a coordinator
//	GET    /v1/scheduled-work/{name}           the owner, a human or a coordinator
//	PATCH  /v1/scheduled-work/{name}           the owner or a human; the editor is the caller, in the same write
//	POST   /v1/scheduled-work/{name}/cancel    the owner, a human or a coordinator
//	POST   /v1/scheduled-work/{name}/approve   a human only: binds the spec hash and the template digest
//	POST   /v1/scheduled-work/{name}/retry     a human only: one Parked occurrence again, from now
//
// They are served only when Server.ScheduledWork is set, which the
// operator's --enable-scheduled-work-api flag does; nil, the
// default, leaves every route 404.
//
// Every read is uncached (Live) and every write carries the resourceVersion it
// read, so a write that races the executor or another request loses with a
// conflict and is decided again on the new state; nothing is overwritten
// blind. The routes grant no authority: an occurrence runs only on a basis
// that holds at its due time (authority.Decide), checked by the executor.
// Anyone who may not see an item gets a 404, so a name tells nothing about
// whose it is.

// ScheduledWorkConfig turns on /v1/scheduled-work. A nil
// Server.ScheduledWork, or one with no Namespace, registers none of the
// routes: each answers 404.
type ScheduledWorkConfig struct {
	// Namespace holds the ScheduledWork objects and the dispatch templates
	// ConfigMap (authority.TemplatesConfigMap): the operator's own
	// namespace, where the executor and the releaser read them.
	Namespace string
	// DecisionWindow is how long a Parked occurrence waits for a human, as
	// the executor's (executor.Executor.DecisionWindow); zero is its
	// default, 24 hours. A retry after it ends is refused: the executor
	// records the occurrence Skipped.
	DecisionWindow time.Duration
}

// ScheduledWorkAPI is the Server's setting for the operator's
// default-off --enable-scheduled-work-api flag: nil while the flag is off,
// and nil without a namespace, so the routes are never served unscoped.
func ScheduledWorkAPI(enabled bool, namespace string) *ScheduledWorkConfig {
	if !enabled || namespace == "" {
		return nil
	}
	return &ScheduledWorkConfig{Namespace: namespace}
}

// defaultScheduledWorkDecisionWindow is the executor's DefaultDecisionWindow
// (TestScheduledWorkDecisionWindowIsTheExecutors pins the two together; this
// package cannot import the executor, whose tests import it).
const defaultScheduledWorkDecisionWindow = 24 * time.Hour

func (c *ScheduledWorkConfig) decisionWindow() time.Duration {
	if c.DecisionWindow > 0 {
		return c.DecisionWindow
	}
	return defaultScheduledWorkDecisionWindow
}

const (
	// maxScheduledWorkBody bounds a request body: an 8 KiB prompt can grow
	// sixfold as JSON escapes.
	maxScheduledWorkBody = 64 << 10
	// maxLiveScheduledWorkPerOwner is how many items one owner may hold that
	// are neither cancelled nor ended. Like the reminders' cap it lists and
	// then creates, so concurrent creates can pass it together: a guard
	// against runaway creation, not a quota.
	maxLiveScheduledWorkPerOwner = 32
	// scheduledWorkNamePrefix starts an item's name. It is not sw-, the
	// prefix of the sessions the executor derives.
	scheduledWorkNamePrefix = "sched-"
	// scheduledWorkCRDName is the CustomResourceDefinition the approve
	// route's schema preflight reads; S6 grants get on this one object.
	scheduledWorkCRDName = "scheduledworks.dev-env.haynesops.com"
	// maxScheduledWorkReason bounds the reason a retry records, inside the
	// schema's 256.
	maxScheduledWorkReason = 256
)

var (
	scheduledWorkCRDKind = schema.GroupVersionKind{Group: "apiextensions.k8s.io", Version: "v1", Kind: "CustomResourceDefinition"}

	// The schema's own patterns (api/v1alpha1/scheduledwork_types.go),
	// checked here so a refusal names the request's field.
	swDue      = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}$`)
	swZone     = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_+/-]*$`)
	swTemplate = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	swRepo     = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	swCalendar = regexp.MustCompile(`^calendar:[a-z0-9][a-z0-9-]{0,62}$`)
	swHash     = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func (s *Server) registerScheduledWork(mux *http.ServeMux) {
	if s.ScheduledWork == nil || s.ScheduledWork.Namespace == "" {
		return
	}
	// A coordinator may create, list everyone's, show and cancel; each
	// handler decides by class and ownership, so the routes admit it.
	mux.Handle(apiv1.ScheduledWorkPath, s.serve(route{coordinator: true, methods: map[string]handler{
		http.MethodPost: s.createScheduledWork, http.MethodGet: s.listScheduledWork}}))
	mux.Handle(apiv1.ScheduledWorkPath+"/{name}", s.serve(route{coordinator: true, methods: map[string]handler{
		http.MethodGet: s.showScheduledWork, http.MethodPatch: s.editScheduledWork}}))
	mux.Handle(apiv1.ScheduledWorkPath+"/{name}/cancel", s.serve(route{coordinator: true, methods: map[string]handler{
		http.MethodPost: s.cancelScheduledWork}}))
	mux.Handle(apiv1.ScheduledWorkPath+"/{name}/approve", s.serve(route{coordinator: true, methods: map[string]handler{
		http.MethodPost: s.approveScheduledWork}}))
	mux.Handle(apiv1.ScheduledWorkPath+"/{name}/retry", s.serve(route{coordinator: true, methods: map[string]handler{
		http.MethodPost: s.retryScheduledWork}}))
}

func unavailable(format string, args ...any) *apiError {
	return newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, format, args...)
}

func conflict(format string, args ...any) *apiError {
	return newError(http.StatusConflict, apiv1.CodeConflict, format, args...)
}

// scheduledWorkPrincipal is the caller as spec.owner, spec.editor and
// status.approval.by record it, from its token alone: reminderOwner's form,
// session/<name>/<uid>, or <class>/<namespace>/<name>. A caller with no such
// form is refused.
func scheduledWorkPrincipal(c *caller) (string, error) {
	p := reminderOwner(c)
	if _, err := authority.ParsePrincipal(p); err != nil {
		return "", forbidden("%s cannot own or change scheduled work", c.String())
	}
	return p, nil
}

func canSeeScheduledWork(c *caller, me string, w *v1alpha1.ScheduledWork) bool {
	return w.Spec.Owner == me || c.kind == kindHuman || c.kind == kindCoordinator
}

// canEditScheduledWork: the owner or a human, never a coordinator that does
// not own it (DESIGN-054 "Who may edit").
func canEditScheduledWork(c *caller, me string, w *v1alpha1.ScheduledWork) bool {
	return w.Spec.Owner == me || c.kind == kindHuman
}

// scheduledWorkName is an item's name: from the owner and key, so a retried
// create names the same item, or random without a key.
func scheduledWorkName(owner, key string) (string, error) {
	if key != "" {
		sum := sha256.Sum256([]byte(owner + "\x00" + key))
		return scheduledWorkNamePrefix + hex.EncodeToString(sum[:])[:20], nil
	}
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return scheduledWorkNamePrefix + hex.EncodeToString(b), nil
}

// readScheduledWork reads an item uncached for a caller that may see it; any
// other caller gets the same 404 as for no item.
func (s *Server) readScheduledWork(ctx context.Context, name string, c *caller, me string) (*v1alpha1.ScheduledWork, error) {
	if name == "" || len(name) > 253 {
		return nil, notFound("no scheduled work %q", name)
	}
	var w v1alpha1.ScheduledWork
	if err := s.Live.Get(ctx, types.NamespacedName{Namespace: s.ScheduledWork.Namespace, Name: name}, &w); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, notFound("no scheduled work %s", name)
		}
		return nil, fromKubeError(err, "scheduled work "+name)
	}
	if !canSeeScheduledWork(c, me, &w) {
		return nil, notFound("no scheduled work %s", name)
	}
	return &w, nil
}

// workRecurrence is the stored recurrence of a request's, to the second, as
// the API server keeps a time. It refuses an integer that would wrap.
func workRecurrence(in *apiv1.ReminderRecurrence) (*v1alpha1.ReminderRecurrence, []apiv1.FieldError) {
	if in == nil {
		return nil, nil
	}
	switch {
	case in.ByMonthDay < 0 || in.ByMonthDay > 31:
		return nil, []apiv1.FieldError{fieldError("recurrence", "byMonthDay %d is not 1 to 31", in.ByMonthDay)}
	case in.Count < 0 || in.Count > 1000:
		return nil, []apiv1.FieldError{fieldError("recurrence", "count %d is not 1 to 1000", in.Count)}
	case len(in.ByDay) > 7:
		return nil, []apiv1.FieldError{fieldError("recurrence", "%d byDay entries, more than 7", len(in.ByDay))}
	}
	out := toSpecRecurrence(in)
	if out.Until != nil {
		u := metav1.NewTime(out.Until.UTC().Truncate(time.Second))
		out.Until = &u
	}
	return out, nil
}

func workInstant(t time.Time) metav1.Time {
	if t.IsZero() {
		return metav1.Time{}
	}
	return metav1.NewTime(t.UTC().Truncate(time.Second))
}

// validateScheduledWork checks a whole spec the way the executor will read
// it, naming each fault by the request's JSON path. created bounds notAfter
// (at most 180 days after it). checkSchedule checks that the schedule still
// has an occurrence ahead and before notAfter, and checkNotAfter that notAfter
// is ahead: a create checks both, an edit only what it changes.
func validateScheduledWork(spec *v1alpha1.ScheduledWorkSpec, now, created time.Time, checkSchedule, checkNotAfter bool) []apiv1.FieldError {
	var fields []apiv1.FieldError
	add := func(field, format string, args ...any) { fields = append(fields, fieldError(field, format, args...)) }
	switch n := utf8.RuneCountInString(spec.Title); {
	case n == 0:
		add("title", "required")
	case n > apiv1.MaxScheduledWorkTitle:
		add("title", "%d characters, more than %d", n, apiv1.MaxScheduledWorkTitle)
	}
	zoneOK := false
	switch {
	case spec.TimeZone == "":
		add("timeZone", "required")
	case len(spec.TimeZone) > 64 || !swZone.MatchString(spec.TimeZone) || spec.TimeZone == "Local":
		add("timeZone", "%q is not an IANA zone name such as America/New_York", spec.TimeZone)
	default:
		if _, err := time.LoadLocation(spec.TimeZone); err != nil {
			add("timeZone", "%q is not an IANA zone name such as America/New_York", spec.TimeZone)
		} else {
			zoneOK = true
		}
	}
	dueOK := swDue.MatchString(spec.Due)
	if !dueOK {
		add("due", "%q is not a local time such as 2026-12-03T09:00", spec.Due)
	}
	notAfter := spec.NotAfter.Time
	switch {
	case notAfter.IsZero():
		add("notAfter", "required: every scheduled item ends")
	case checkNotAfter && !notAfter.After(now):
		add("notAfter", "%s is not in the future", notAfter.UTC().Format(time.RFC3339))
	case notAfter.After(created.Add(apiv1.MaxScheduledWorkNotAfter)):
		add("notAfter", "%s is more than 180 days after the item's creation", notAfter.UTC().Format(time.RFC3339))
	}
	if zoneOK && dueOK {
		sch, err := reminders.ScheduleOfWork(*spec)
		switch {
		case err != nil && spec.Recurrence != nil:
			add("recurrence", "%v", err)
		case err != nil:
			add("due", "%v", err)
		case checkSchedule:
			first, ok := sch.Next(time.Time{})
			switch {
			case !ok:
				add("due", "the recurrence has no occurrence")
			case !sch.Recurring() && first.At.Before(now.Add(-pastGrace)):
				add("due", "%s is in the past", first.At.UTC().Format(time.RFC3339))
			default:
				next, ok := sch.Next(now.Add(-pastGrace))
				switch {
				case !ok:
					add("recurrence", "the series has already ended")
				case !notAfter.IsZero() && next.At.After(notAfter):
					add("notAfter", "%s is before the next occurrence, %s: nothing would run", notAfter.UTC().Format(time.RFC3339), next.At.UTC().Format(time.RFC3339))
				}
			}
		}
	}
	switch spec.Mode {
	case v1alpha1.ScheduledWorkHeadless, v1alpha1.ScheduledWorkInteractive:
	case "":
		add("mode", "required: headless or interactive")
	default:
		add("mode", "%q is not headless or interactive", spec.Mode)
	}
	switch t := spec.Work.Template; {
	case t == "":
		add("work.template", "required")
	case len(t) > 63 || !swTemplate.MatchString(t):
		add("work.template", "%q is not a template name (a DNS label)", t)
	}
	switch r := spec.Work.Repo; {
	case r == "":
		add("work.repo", "required")
	case len(r) > 100 || !swRepo.MatchString(r) || r == "." || r == "..":
		add("work.repo", "%q is not a repository name", r)
	}
	switch n := len(spec.Work.Prompt); {
	case n == 0:
		add("work.prompt", "required: the work order")
	case n > apiv1.MaxScheduledWorkPromptBytes:
		add("work.prompt", "%d bytes, more than %d", n, apiv1.MaxScheduledWorkPromptBytes)
	}
	if len(spec.Notify) > apiv1.MaxScheduledWorkNotify {
		add("notify", "%d targets, more than %d", len(spec.Notify), apiv1.MaxScheduledWorkNotify)
	}
	for i, t := range spec.Notify {
		switch {
		case t != v1alpha1.ReminderDefaultPushover:
			// Pages go through Reminders, which know this one reference.
			add("notify", "%q is not a page target: the only one is %s", t, v1alpha1.ReminderDefaultPushover)
		case slices.Contains(spec.Notify[:i], t):
			add("notify", "%q is listed twice", t)
		}
	}
	if c := spec.Calendar; c != "" && (len(c) > 128 || !swCalendar.MatchString(c)) {
		add("calendar", "%q is not a calendar reference such as calendar:haynesnetwork", c)
	}
	if spec.Mode == v1alpha1.ScheduledWorkInteractive && len(spec.Notify) == 0 {
		add("notify", "interactive work needs a notify target: Tom is paged the session's link")
	}
	return fields
}

// sameScheduledWork reports whether a retried create's spec is the stored
// one: the same owner, editor, cancel and spec hash (which compares times to
// the second and notify as a set).
func sameScheduledWork(a, b v1alpha1.ScheduledWorkSpec) bool {
	return a.Owner == b.Owner && a.Editor == b.Editor && a.Cancelled == b.Cancelled && authority.SpecHash(a) == authority.SpecHash(b)
}

// createScheduledWork serves POST /v1/scheduled-work.
func (s *Server) createScheduledWork(ctx context.Context, w http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	// A summoned session hears the refusal here. A record that already exists
	// is still refused at admitCreate, which reads the parent live.
	if err := s.refuseSummonedChild(ctx, c); err != nil {
		return 0, nil, err
	}
	me, err := scheduledWorkPrincipal(c)
	if err != nil {
		return 0, nil, err
	}
	var req apiv1.CreateScheduledWorkRequest
	if err := decodeJSON(w, r, maxScheduledWorkBody, &req, true); err != nil {
		return 0, nil, err
	}
	if len(req.Key) > apiv1.MaxScheduledWorkKeyBytes {
		return 0, nil, invalid(fieldError("key", "%d bytes, more than %d", len(req.Key), apiv1.MaxScheduledWorkKeyBytes))
	}
	rec, fields := workRecurrence(req.Recurrence)
	if len(fields) > 0 {
		return 0, nil, invalid(fields...)
	}
	now := s.now()
	spec := v1alpha1.ScheduledWorkSpec{
		Title: strings.TrimSpace(req.Title), Owner: me, Editor: me,
		Due: strings.TrimSpace(req.Due), TimeZone: strings.TrimSpace(req.TimeZone), Recurrence: rec,
		NotAfter: workInstant(req.NotAfter), Mode: req.Mode,
		Work:   v1alpha1.ScheduledWorkOrder{Template: req.Work.Template, Repo: req.Work.Repo, Prompt: req.Work.Prompt},
		Notify: slices.Clone(req.Notify), Calendar: req.Calendar,
	}
	name, err := scheduledWorkName(me, req.Key)
	if err != nil {
		return 0, nil, internal("name the scheduled work: %v", err)
	}
	ns := s.ScheduledWork.Namespace
	var list v1alpha1.ScheduledWorkList
	if err := s.Live.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return 0, nil, fromKubeError(err, "scheduled work")
	}
	var existing *v1alpha1.ScheduledWork
	held := 0
	for i := range list.Items {
		it := &list.Items[i]
		switch {
		case it.Name == name:
			existing = it
		case it.Spec.Owner == me && scheduledWorkState(it, now) == apiv1.ScheduledWorkActive:
			held++
		}
	}
	// A retried create answers with what the first one made, before the
	// checks that depend on the clock: its due time may have passed since.
	// Another spec under the key is a 409 even when its schedule, notAfter or
	// other fields would fail the checks below; only the key's length and the
	// recurrence's integers, checked above, are refused first (a wrapped
	// integer must not match a stored spec).
	switch {
	case existing != nil && sameScheduledWork(existing.Spec, spec):
		return http.StatusOK, s.scheduledWorkAnswer(existing, now), nil
	case existing != nil:
		return 0, nil, conflict("key %q already names scheduled work %s of yours with another spec, or one edited or cancelled since", req.Key, name)
	}
	if fields := validateScheduledWork(&spec, now, now, true, true); len(fields) > 0 {
		return 0, nil, invalid(fields...)
	}
	if held >= maxLiveScheduledWorkPerOwner {
		return 0, nil, tooManyRequests("you hold %d active scheduled items, the most allowed; cancel one first", held)
	}
	sw := &v1alpha1.ScheduledWork{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}, Spec: spec}
	if err := s.Client.Create(ctx, sw); err != nil {
		if apierrors.IsAlreadyExists(err) && req.Key != "" {
			// A concurrent create with the same key won the race: that is
			// the item this request asked for, unless its spec differs.
			var existing v1alpha1.ScheduledWork
			if gerr := s.Live.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &existing); gerr == nil {
				if !sameScheduledWork(existing.Spec, spec) {
					return 0, nil, conflict("key %q already names scheduled work %s of yours with another spec, or one edited or cancelled since", req.Key, name)
				}
				return http.StatusOK, s.scheduledWorkAnswer(&existing, now), nil
			}
		}
		return 0, nil, fromKubeError(err, "scheduled work")
	}
	s.Log.Info("scheduled work created", "scheduledWork", name, "owner", me, "mode", spec.Mode, "template", spec.Work.Template)
	return http.StatusCreated, s.scheduledWorkAnswer(sw, now), nil
}

// listScheduledWork serves GET /v1/scheduled-work: the caller's own, or with
// all=true everyone's, for a human or a coordinator only. A list leaves the
// occurrences out.
func (s *Server) listScheduledWork(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	me, err := scheduledWorkPrincipal(c)
	if err != nil {
		return 0, nil, err
	}
	all := r.URL.Query().Get("all") == "true"
	if all && c.kind != kindHuman && c.kind != kindCoordinator {
		return 0, nil, forbidden("only a human or a coordinator may list everyone's scheduled work")
	}
	var list v1alpha1.ScheduledWorkList
	if err := s.Live.List(ctx, &list, client.InNamespace(s.ScheduledWork.Namespace)); err != nil {
		return 0, nil, fromKubeError(err, "scheduled work")
	}
	now := s.now()
	out := apiv1.ScheduledWorkList{Items: []apiv1.ScheduledWork{}}
	for i := range list.Items {
		if all || list.Items[i].Spec.Owner == me {
			v := scheduledWorkView(&list.Items[i], now)
			v.Occurrences = nil
			out.Items = append(out.Items, v)
		}
	}
	slices.SortFunc(out.Items, func(a, b apiv1.ScheduledWork) int {
		if d := b.CreatedAt.Compare(a.CreatedAt); d != 0 {
			return d
		}
		return strings.Compare(a.Name, b.Name)
	})
	return http.StatusOK, out, nil
}

// showScheduledWork serves GET /v1/scheduled-work/{name}.
func (s *Server) showScheduledWork(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	me, err := scheduledWorkPrincipal(c)
	if err != nil {
		return 0, nil, err
	}
	sw, err := s.readScheduledWork(ctx, r.PathValue("name"), c, me)
	if err != nil {
		return 0, nil, err
	}
	v := s.scheduledWorkAnswer(sw, s.now())
	v.Message = joinNotes(v.Message, s.approvalNote(ctx, sw))
	return http.StatusOK, v, nil
}

// editScheduledWork serves PATCH /v1/scheduled-work/{name}: the owner or a
// human, never a coordinator that does not own it. The spec and its editor
// change in one write, which carries the resourceVersion it read.
func (s *Server) editScheduledWork(ctx context.Context, w http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	me, err := scheduledWorkPrincipal(c)
	if err != nil {
		return 0, nil, err
	}
	var req apiv1.EditScheduledWorkRequest
	if err := decodeJSON(w, r, maxScheduledWorkBody, &req, true); err != nil {
		return 0, nil, err
	}
	if req.ClearRecurrence && req.Recurrence != nil {
		return 0, nil, invalid(fieldError("recurrence", "clearRecurrence and recurrence are exclusive"))
	}
	rec, fields := workRecurrence(req.Recurrence)
	if len(fields) > 0 {
		return 0, nil, invalid(fields...)
	}
	for range editRetries {
		sw, err := s.readScheduledWork(ctx, r.PathValue("name"), c, me)
		if err != nil {
			return 0, nil, err
		}
		if !canEditScheduledWork(c, me, sw) {
			return 0, nil, forbidden("only the owner or a human may edit scheduled work %s; a coordinator may cancel it", sw.Name)
		}
		if sw.Spec.Cancelled {
			return 0, nil, conflict("scheduled work %s is cancelled; a cancelled item is never edited", sw.Name)
		}
		spec := *sw.Spec.DeepCopy()
		applyScheduledWorkEdit(&spec, &req, rec)
		now := s.now()
		if sameScheduledWork(spec, sw.Spec) {
			return http.StatusOK, s.scheduledWorkAnswer(sw, now), nil
		}
		notAfterChanged := !spec.NotAfter.Equal(&sw.Spec.NotAfter)
		scheduleChanged := notAfterChanged || reminders.PlanHashOf(spec.Due, spec.TimeZone, spec.Recurrence) != reminders.PlanHashOf(sw.Spec.Due, sw.Spec.TimeZone, sw.Spec.Recurrence)
		if fields := validateScheduledWork(&spec, now, sw.CreationTimestamp.Time, scheduleChanged, notAfterChanged); len(fields) > 0 {
			return 0, nil, invalid(fields...)
		}
		spec.Editor = me
		sw.Spec = spec
		err = s.Client.Update(ctx, sw)
		if err == nil {
			s.Log.Info("scheduled work edited", "scheduledWork", sw.Name, "caller", c.String())
			return http.StatusOK, s.scheduledWorkAnswer(sw, now), nil
		}
		if !apierrors.IsConflict(err) {
			return 0, nil, fromKubeError(err, "scheduled work "+sw.Name)
		}
	}
	return 0, nil, conflict("the scheduled work kept changing; read it again and retry")
}

func applyScheduledWorkEdit(spec *v1alpha1.ScheduledWorkSpec, req *apiv1.EditScheduledWorkRequest, rec *v1alpha1.ReminderRecurrence) {
	if req.Title != nil {
		spec.Title = strings.TrimSpace(*req.Title)
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
	if rec != nil {
		spec.Recurrence = rec
	}
	if req.NotAfter != nil {
		spec.NotAfter = workInstant(*req.NotAfter)
	}
	if req.Mode != nil {
		spec.Mode = *req.Mode
	}
	if wk := req.Work; wk != nil {
		if wk.Template != nil {
			spec.Work.Template = *wk.Template
		}
		if wk.Repo != nil {
			spec.Work.Repo = *wk.Repo
		}
		if wk.Prompt != nil {
			spec.Work.Prompt = *wk.Prompt
		}
	}
	if req.Notify != nil {
		spec.Notify = slices.Clone(*req.Notify)
		if len(spec.Notify) == 0 {
			spec.Notify = nil
		}
	}
	if req.Calendar != nil {
		spec.Calendar = *req.Calendar
	}
}

// cancelScheduledWork serves POST /v1/scheduled-work/{name}/cancel: the
// owner, a human or a coordinator. It is idempotent. It stops no session
// already running; the answer warns when one may be.
func (s *Server) cancelScheduledWork(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	me, err := scheduledWorkPrincipal(c)
	if err != nil {
		return 0, nil, err
	}
	for range editRetries {
		sw, err := s.readScheduledWork(ctx, r.PathValue("name"), c, me)
		if err != nil {
			return 0, nil, err
		}
		now := s.now()
		if !sw.Spec.Cancelled {
			sw.Spec.Cancelled, sw.Spec.Editor = true, me
			if err := s.Client.Update(ctx, sw); err != nil {
				if apierrors.IsConflict(err) {
					continue
				}
				return 0, nil, fromKubeError(err, "scheduled work "+sw.Name)
			}
			s.Log.Info("scheduled work cancelled", "scheduledWork", sw.Name, "caller", c.String())
		}
		v := s.scheduledWorkAnswer(sw, now)
		for _, o := range sw.Status.Occurrences {
			if inFlightOccurrence(o.State) {
				v.Warning = "an occurrence was already under way; a cancel does not stop a session already running (agent-run reap <session> does)"
			}
		}
		return http.StatusOK, v, nil
	}
	return 0, nil, conflict("the scheduled work kept changing; read it again and retry")
}

func inFlightOccurrence(state string) bool {
	switch state {
	case v1alpha1.OccurrenceClaimed, v1alpha1.OccurrenceDispatched, v1alpha1.OccurrenceDelivered, v1alpha1.OccurrenceOpened, v1alpha1.OccurrenceRunning:
		return true
	}
	return false
}

// approveScheduledWork serves POST /v1/scheduled-work/{name}/approve, a human
// only. status.approval records the authenticated human, the current spec's
// hash, which the request must name, and the digest of the dispatch template
// the spec names, read now, which the request may pin too: an edit of either
// voids it (authority.Decide).
// With at, the same write retries that Parked occurrence. A refusal writes
// nothing.
func (s *Server) approveScheduledWork(ctx context.Context, w http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	if c.kind != kindHuman {
		return 0, nil, forbidden("approve is for a human (Tom) only; %s cannot approve scheduled work", c.String())
	}
	by, err := scheduledWorkPrincipal(c)
	if err != nil {
		return 0, nil, err
	}
	var req apiv1.ApproveScheduledWorkRequest
	if err := decodeJSON(w, r, maxScheduledWorkBody, &req, true); err != nil {
		return 0, nil, err
	}
	if !swHash.MatchString(req.SpecHash) {
		return 0, nil, invalid(fieldError("specHash", "required: the 64 lowercase hex characters of the spec hash shown"))
	}
	if req.At != nil && req.At.IsZero() {
		return 0, nil, invalid(fieldError("at", "a zero time names no occurrence"))
	}
	if req.TemplateDigest != "" && !swHash.MatchString(req.TemplateDigest) {
		return 0, nil, invalid(fieldError("templateDigest", "the 64 lowercase hex characters of the template digest shown, or absent"))
	}
	if err := s.scheduledWorkSchemaReady(ctx); err != nil {
		return 0, nil, err
	}
	for range editRetries {
		sw, err := s.readScheduledWork(ctx, r.PathValue("name"), c, by)
		if err != nil {
			return 0, nil, err
		}
		hash, err := scheduledWorkDecidable(sw, req.SpecHash)
		if err != nil {
			return 0, nil, err
		}
		digest, err := s.scheduledWorkTemplate(ctx, &sw.Spec)
		if err != nil {
			return 0, nil, err
		}
		if req.TemplateDigest != "" && req.TemplateDigest != digest {
			return 0, nil, conflict("template %s changed after it was shown: its digest is now %s, not %s; nothing was approved", sw.Spec.Work.Template, digest, req.TemplateDigest)
		}
		now := s.now()
		var o *v1alpha1.ScheduledWorkOccurrence
		if req.At != nil {
			if o, err = s.retryableOccurrence(ctx, sw, *req.At, hash, now); err != nil {
				return 0, nil, err
			}
		}
		if a := sw.Status.Approval; o == nil && a != nil && a.By == by && a.SpecHash == hash && a.TemplateDigest == digest {
			// Approved already, as asked: answer without a second write.
			return http.StatusOK, s.scheduledWorkAnswer(sw, now), nil
		}
		sw.Status.Approval = &v1alpha1.ScheduledWorkApproval{By: by, At: metav1.NewTime(now.UTC().Truncate(time.Second)), SpecHash: hash, TemplateDigest: digest}
		if o != nil {
			retryOccurrence(o, now, "approved and retried by "+by)
		}
		if err := s.Client.Status().Update(ctx, sw); err != nil {
			if apierrors.IsConflict(err) {
				continue
			}
			return 0, nil, fromKubeError(err, "scheduled work "+sw.Name)
		}
		fresh, err := s.approvalKept(ctx, sw, o)
		if err != nil {
			return 0, nil, err
		}
		s.Log.Info("scheduled work approved", "scheduledWork", sw.Name, "by", by, "specHash", hash, "templateDigest", digest, "retried", o != nil)
		return http.StatusOK, s.scheduledWorkAnswer(fresh, now), nil
	}
	return 0, nil, conflict("the scheduled work kept changing; read it again and retry")
}

// retryScheduledWork serves POST /v1/scheduled-work/{name}/retry, a human
// only: one Parked occurrence runs again, late on purpose, in a fresh window
// from now. It grants no authority and writes no approval: the executor
// checks the occurrence again as at its due time.
func (s *Server) retryScheduledWork(ctx context.Context, w http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	if c.kind != kindHuman {
		return 0, nil, forbidden("retry is for a human (Tom) only; %s cannot retry scheduled work", c.String())
	}
	by, err := scheduledWorkPrincipal(c)
	if err != nil {
		return 0, nil, err
	}
	var req apiv1.RetryScheduledWorkRequest
	if err := decodeJSON(w, r, maxScheduledWorkBody, &req, true); err != nil {
		return 0, nil, err
	}
	var fields []apiv1.FieldError
	if req.At.IsZero() {
		fields = append(fields, fieldError("at", "required: the Parked occurrence to retry"))
	}
	if !swHash.MatchString(req.SpecHash) {
		fields = append(fields, fieldError("specHash", "required: the 64 lowercase hex characters of the spec hash shown"))
	}
	if len(fields) > 0 {
		return 0, nil, invalid(fields...)
	}
	for range editRetries {
		sw, err := s.readScheduledWork(ctx, r.PathValue("name"), c, by)
		if err != nil {
			return 0, nil, err
		}
		hash, err := scheduledWorkDecidable(sw, req.SpecHash)
		if err != nil {
			return 0, nil, err
		}
		now := s.now()
		o, err := s.retryableOccurrence(ctx, sw, req.At, hash, now)
		if err != nil {
			return 0, nil, err
		}
		retryOccurrence(o, now, "retried by "+by)
		if err := s.Client.Status().Update(ctx, sw); err != nil {
			if apierrors.IsConflict(err) {
				continue
			}
			return 0, nil, fromKubeError(err, "scheduled work "+sw.Name)
		}
		s.Log.Info("scheduled work occurrence retried", "scheduledWork", sw.Name, "by", by, "occurrence", req.At.UTC().Format(time.RFC3339), "attempt", o.Attempt)
		return http.StatusOK, s.scheduledWorkAnswer(sw, now), nil
	}
	return 0, nil, conflict("the scheduled work kept changing; read it again and retry")
}

// scheduledWorkDecidable refuses a decision on a cancelled item or on a spec
// other than the one the client showed, and returns the current spec hash.
func scheduledWorkDecidable(sw *v1alpha1.ScheduledWork, shown string) (string, error) {
	if sw.Spec.Cancelled {
		return "", conflict("scheduled work %s is cancelled; there is nothing to decide", sw.Name)
	}
	hash := authority.SpecHash(sw.Spec)
	if shown != hash {
		return "", conflict("the spec of %s changed after it was shown: its hash is now %s, not %s; nothing was recorded", sw.Name, hash, shown)
	}
	return hash, nil
}

// retryableOccurrence is the occurrence of sw at at, when a human may retry
// it now: it is Parked, not marked ending, has a retry left, holds no
// session, was not claimed with another spec than hash, notAfter has not
// passed, no other occurrence is unfinished, and no session of its derived
// name is still held (a retry must not move a start deadline past that
// session's). Each refusal is a 409 and records nothing.
func (s *Server) retryableOccurrence(ctx context.Context, sw *v1alpha1.ScheduledWork, at time.Time, hash string, now time.Time) (*v1alpha1.ScheduledWorkOccurrence, error) {
	when := at.UTC().Format(time.RFC3339)
	o := scheduledwork.Find(sw, at)
	switch {
	case o == nil:
		return nil, conflict("scheduled work %s has no occurrence at %s", sw.Name, when)
	case o.State != v1alpha1.OccurrenceParked:
		return nil, conflict("the occurrence of %s at %s is %s, not Parked; only a parked occurrence is retried", sw.Name, when, o.State)
	case o.Ending:
		return nil, conflict("the occurrence of %s at %s is ending; it is never reopened", sw.Name, when)
	case o.Attempt >= v1alpha1.ScheduledWorkMaxAttempt:
		return nil, conflict("the occurrence of %s at %s has used its %d retries; a fourth is refused", sw.Name, when, v1alpha1.ScheduledWorkMaxAttempt)
	case o.Session != "":
		return nil, conflict("the occurrence of %s at %s already has session %s; it is not retried", sw.Name, when, o.Session)
	case o.SpecHash != "" && o.SpecHash != hash:
		return nil, conflict("the occurrence of %s at %s was claimed with an earlier spec (hash %s); it is not retried. A plain approve approves the current spec for later occurrences", sw.Name, when, o.SpecHash)
	case now.After(sw.Spec.NotAfter.Time):
		return nil, conflict("scheduled work %s ended at its notAfter, %s; nothing runs after it", sw.Name, sw.Spec.NotAfter.UTC().Format(time.RFC3339))
	}
	if end := s.decisionEnd(sw, o); !now.Before(end) {
		return nil, conflict("the occurrence of %s at %s waited for a decision until %s; it is no longer retried, and the executor records it Skipped", sw.Name, when, end.UTC().Format(time.RFC3339))
	}
	for i := range sw.Status.Occurrences {
		other := &sw.Status.Occurrences[i]
		if other != o && !v1alpha1.OccurrenceTerminal(other.State) && other.State != v1alpha1.OccurrenceParked {
			return nil, conflict("the occurrence of %s at %s is still %s; one occurrence runs at a time, so retry this one after it ends", sw.Name, other.At.UTC().Format(time.RFC3339), other.State)
		}
	}
	name, err := scheduledwork.SessionName(string(sw.UID), at)
	if err != nil {
		return nil, internal("derive the occurrence's session name: %v", err)
	}
	var sess v1alpha1.AgentSession
	switch err := s.Live.Get(ctx, types.NamespacedName{Namespace: s.Policy.SessionNamespace, Name: name}, &sess); {
	case err == nil:
		return nil, conflict("session %s of the occurrence of %s at %s still exists; retry once it is gone", name, sw.Name, when)
	case !apierrors.IsNotFound(err):
		return nil, fromKubeError(err, "session "+name)
	}
	return o, nil
}

// decisionEnd is when o, Parked, stops waiting for a human, as the executor
// reckons it (executor.go, parked): the decision window from its due time or
// its last retriedAt, or the item's next occurrence if that comes first.
func (s *Server) decisionEnd(sw *v1alpha1.ScheduledWork, o *v1alpha1.ScheduledWorkOccurrence) time.Time {
	start := o.At.Time
	if o.RetriedAt != nil {
		start = o.RetriedAt.Time
	}
	end := start.Add(s.ScheduledWork.decisionWindow())
	if sch, err := reminders.ScheduleOfWork(sw.Spec); err == nil {
		if next, ok := sch.Next(o.At.Add(time.Nanosecond)); ok && next.At.Before(end) {
			end = next.At
		}
	}
	return end
}

// retryOccurrence opens a fresh window for o from now: Pending again, its
// next attempt, retriedAt now. The executor claims it again as it claims a
// due occurrence, with its grace, the claim-without-session rule and the
// start deadline counted from retriedAt.
func retryOccurrence(o *v1alpha1.ScheduledWorkOccurrence, now time.Time, reason string) {
	t := metav1.NewTime(now.UTC().Truncate(time.Second))
	o.State, o.Attempt, o.RetriedAt = v1alpha1.OccurrencePending, o.Attempt+1, &t
	if len(reason) > maxScheduledWorkReason {
		reason = reason[:maxScheduledWorkReason]
	}
	o.Reason = reason
}

// scheduledWorkSchemaReady is the approve route's schema preflight, as the
// executor's (S3b): it reads the ScheduledWork CRD uncached and requires its
// served v1alpha1 status schema to carry approval.templateDigest. An older
// CRD would prune it silently, and an approval with no recorded digest is
// void (authority.Decide), so an approval stored then would authorise
// nothing while the person was told it was recorded. A CRD that cannot be
// read (a missing grant, for example) is refused the same way.
func (s *Server) scheduledWorkSchemaReady(ctx context.Context) error {
	crd := &unstructured.Unstructured{}
	crd.SetGroupVersionKind(scheduledWorkCRDKind)
	if err := s.Live.Get(ctx, client.ObjectKey{Name: scheduledWorkCRDName}, crd); err != nil {
		s.Log.Error(err, "the approve preflight could not read the ScheduledWork CRD")
		return unavailable("the operator cannot read the ScheduledWork CRD, so it cannot confirm that an approval keeps its template digest; nothing was approved")
	}
	if !approvalDigestServed(crd) {
		return unavailable("the installed ScheduledWork CRD has no status.approval.templateDigest, so an approval stored now would bind no dispatch template and authorise nothing; nothing was approved")
	}
	return nil
}

// approvalDigestServed reports whether crd serves v1alpha1 with a status
// schema that keeps approval.templateDigest.
func approvalDigestServed(crd *unstructured.Unstructured) bool {
	versions, _, _ := unstructured.NestedSlice(crd.Object, "spec", "versions")
	for _, v := range versions {
		version, ok := v.(map[string]any)
		if !ok || version["name"] != v1alpha1.GroupVersion.Version || version["served"] != true {
			continue
		}
		props, _, _ := unstructured.NestedMap(version, "schema", "openAPIV3Schema", "properties", "status", "properties", "approval", "properties")
		_, ok = props["templateDigest"]
		return ok
	}
	return false
}

// approvalKept reads sw back from the API server after an approval's write,
// into a fresh object (the written copy keeps a value the API server pruned),
// and refuses when the stored approval lost its template digest: a CRD that
// changed between the preflight and the write prunes it. That approval is
// void, and the answer says so, and says when the same write retried the
// occurrence retried, which stands.
func (s *Server) approvalKept(ctx context.Context, sw *v1alpha1.ScheduledWork, retried *v1alpha1.ScheduledWorkOccurrence) (*v1alpha1.ScheduledWork, error) {
	also := ""
	if retried != nil {
		also = fmt.Sprintf("; the same write did retry the occurrence at %s (now Pending, attempt %d), which is checked again on its own basis: cancel %s if it must not run",
			retried.At.UTC().Format(time.RFC3339), retried.Attempt, sw.Name)
	}
	fresh := &v1alpha1.ScheduledWork{}
	if err := s.Live.Get(ctx, client.ObjectKeyFromObject(sw), fresh); err != nil {
		return nil, unavailable("the approval of %s was written but could not be read back; agent-run schedule show %s says whether it holds%s", sw.Name, sw.Name, also)
	}
	if a := fresh.Status.Approval; a == nil || a.TemplateDigest == "" {
		s.Log.Error(nil, "the stored approval has no template digest: the installed ScheduledWork CRD prunes it", "scheduledWork", sw.Name)
		return nil, unavailable("the API server did not keep the approval's template digest (the installed ScheduledWork CRD prunes it), so the approval of %s binds no template and authorises nothing%s", sw.Name, also)
	}
	return fresh, nil
}

// scheduledWorkTemplate reads the dispatch templates uncached and resolves
// the one spec names, for an approval: it returns that template's digest,
// which the approval records. A refusal (409) says why the spec cannot be
// approved under the templates as they are now; a failed read is a 503.
func (s *Server) scheduledWorkTemplate(ctx context.Context, spec *v1alpha1.ScheduledWorkSpec) (string, error) {
	key := types.NamespacedName{Namespace: s.ScheduledWork.Namespace, Name: authority.TemplatesConfigMap}
	var cm corev1.ConfigMap
	if err := s.Live.Get(ctx, key, &cm); err != nil {
		if apierrors.IsNotFound(err) {
			return "", conflict("no dispatch templates: ConfigMap %s does not exist, so no template can be approved", key)
		}
		return "", fromKubeError(err, "the dispatch templates")
	}
	ts, err := authority.ParseTemplates(cm.Data)
	if err != nil {
		return "", conflict("the dispatch templates are invalid, so no template can be approved: %s", clipText(err.Error(), 300))
	}
	name := spec.Work.Template
	t, err := authority.Resolve(ts, name, spec.Work.Repo)
	switch {
	case err != nil:
		return "", conflict("%v", err)
	case !t.AllowsMode(spec.Mode):
		return "", conflict("template %s does not serve %s work", name, spec.Mode)
	case !t.AdmitsBasis(authority.BasisApproved):
		return "", conflict("template %s does not admit approved work, so an approval could never run this item", name)
	}
	return authority.TemplateDigest(t), nil
}

func clipText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "..."
}

// approvalNote says when sw's approval no longer applies, for a show: of an
// earlier spec, with no template digest, or under a template that has
// changed or no longer resolves. A template that cannot be read now gives no
// note.
func (s *Server) approvalNote(ctx context.Context, sw *v1alpha1.ScheduledWork) string {
	a := sw.Status.Approval
	switch {
	case a == nil:
		return ""
	case a.SpecHash != authority.SpecHash(sw.Spec):
		return "the approval by " + a.By + " is of an earlier spec and no longer applies"
	case a.TemplateDigest == "":
		return "the approval by " + a.By + " records no template digest, so it applies to nothing; approve again"
	}
	digest, err := s.scheduledWorkTemplate(ctx, &sw.Spec)
	var refusal *apiError
	switch {
	case err == nil && digest != a.TemplateDigest:
		return "the approval by " + a.By + " was given under another version of template " + sw.Spec.Work.Template + " and no longer applies; approve again"
	case errors.As(err, &refusal) && refusal.status == http.StatusConflict:
		return "the approval by " + a.By + " no longer applies: " + refusal.body.Message
	}
	return ""
}

// standingNote says what an item needs to run, from its owner and editor
// (authority.Decide's standing basis): nothing for a human owner who made
// the last edit.
func standingNote(sw *v1alpha1.ScheduledWork) string {
	p, err := authority.ParsePrincipal(sw.Spec.Owner)
	switch {
	case err != nil || sw.Spec.Cancelled:
		return ""
	case sw.Spec.Editor != sw.Spec.Owner:
		return "the last edit was not the owner's, so the item has no standing authority: an occurrence runs only on Tom's approval of this exact spec"
	case p.Class == authority.ClassSession:
		return "the owner is a session: an occurrence runs while that session is live and made the last edit, or on Tom's approval of this exact spec"
	case p.Class == authority.ClassClient || p.Class == authority.ClassCoordinator:
		return "a client or coordinator owner has no standing authority: an occurrence runs only on Tom's approval of this exact spec"
	}
	return ""
}

func joinNotes(notes ...string) string {
	var kept []string
	for _, n := range notes {
		if n != "" {
			kept = append(kept, n)
		}
	}
	return strings.Join(kept, "; ")
}

// scheduledWorkAnswer is the view a single-item route answers with: every
// occurrence and the standing note.
func (s *Server) scheduledWorkAnswer(sw *v1alpha1.ScheduledWork, now time.Time) apiv1.ScheduledWork {
	v := scheduledWorkView(sw, now)
	v.Message = standingNote(sw)
	return v
}

// scheduledWorkState is the item's state: cancelled; ended once every
// recorded occurrence is final and notAfter has passed or the series has no
// occurrence ahead; else active.
func scheduledWorkState(sw *v1alpha1.ScheduledWork, now time.Time) string {
	if sw.Spec.Cancelled {
		return apiv1.ScheduledWorkCancelled
	}
	for _, o := range sw.Status.Occurrences {
		if !v1alpha1.OccurrenceTerminal(o.State) {
			return apiv1.ScheduledWorkActive
		}
	}
	if now.After(sw.Spec.NotAfter.Time) {
		return apiv1.ScheduledWorkEnded
	}
	if sw.Status.PlanHash != "" && sw.Status.NextDue == nil {
		sch, err := reminders.ScheduleOfWork(sw.Spec)
		if err != nil {
			return apiv1.ScheduledWorkEnded
		}
		if next, ok := sch.Next(now); !ok || next.At.After(sw.Spec.NotAfter.Time) {
			return apiv1.ScheduledWorkEnded
		}
	}
	return apiv1.ScheduledWorkActive
}

// scheduledWorkView is sw as the API shows it: the stored spec, every field,
// and SpecHash of exactly that spec.
func scheduledWorkView(sw *v1alpha1.ScheduledWork, now time.Time) apiv1.ScheduledWork {
	sp := sw.Spec
	v := apiv1.ScheduledWork{
		Name: sw.Name,
		Spec: apiv1.ScheduledWorkSpec{
			Title: sp.Title, Owner: sp.Owner, Editor: sp.Editor, Due: sp.Due, TimeZone: sp.TimeZone,
			NotAfter: sp.NotAfter.UTC(), Mode: sp.Mode,
			Work:   apiv1.ScheduledWorkOrder{Template: sp.Work.Template, Repo: sp.Work.Repo, Prompt: sp.Work.Prompt},
			Notify: slices.Clone(sp.Notify), Calendar: sp.Calendar, Cancelled: sp.Cancelled,
		},
		SpecHash:  authority.SpecHash(sp),
		State:     scheduledWorkState(sw, now),
		CreatedAt: sw.CreationTimestamp.UTC(),
		Counts: apiv1.ScheduledWorkCounts{
			Succeeded: int(sw.Status.Counts.Succeeded), Failed: int(sw.Status.Counts.Failed), Parked: int(sw.Status.Counts.Parked),
			Skipped: int(sw.Status.Counts.Skipped), Ended: int(sw.Status.Counts.Ended), Ambiguous: int(sw.Status.Counts.Ambiguous),
			Cancelled: int(sw.Status.Counts.Cancelled),
		},
	}
	if rec := sp.Recurrence; rec != nil {
		v.Spec.Recurrence = &apiv1.ReminderRecurrence{Freq: rec.Freq, ByDay: slices.Clone(rec.ByDay), ByMonthDay: int(rec.ByMonthDay), Count: int(rec.Count)}
		if rec.Until != nil {
			u := rec.Until.UTC()
			v.Spec.Recurrence.Until = &u
		}
	}
	if d := sw.Status.NextDue; d != nil && !sp.Cancelled {
		t := d.UTC()
		v.NextDue = &t
	}
	if a := sw.Status.Approval; a != nil {
		v.Approval = &apiv1.ScheduledWorkApproval{By: a.By, At: a.At.UTC(), SpecHash: a.SpecHash, TemplateDigest: a.TemplateDigest}
	}
	for i := range sw.Status.Occurrences {
		v.Occurrences = append(v.Occurrences, occurrenceView(&sw.Status.Occurrences[i]))
	}
	return v
}

func occurrenceView(o *v1alpha1.ScheduledWorkOccurrence) apiv1.ScheduledWorkOccurrence {
	out := apiv1.ScheduledWorkOccurrence{
		At: o.At.UTC(), State: o.State, Reason: o.Reason, Attempt: int(o.Attempt), Missed: int(o.Missed),
		Session: o.Session, SpecHash: o.SpecHash, Delivery: o.Delivery,
	}
	if o.ClaimedAt != nil {
		t := o.ClaimedAt.UTC()
		out.ClaimedAt = &t
	}
	if o.RetriedAt != nil {
		t := o.RetriedAt.UTC()
		out.RetriedAt = &t
	}
	if e := o.Execution; e != nil {
		out.Execution = &apiv1.ScheduledWorkExecution{State: string(e.State), Reason: e.Reason}
	}
	if r := o.Reported; r != nil {
		out.Reported = &apiv1.ScheduledWorkReported{Summary: r.Summary, PRURLs: slices.Clone(r.PRURLs), HeadSHAs: slices.Clone(r.HeadSHAs)}
	}
	for _, p := range o.Pages {
		out.Pages = append(out.Pages, apiv1.ScheduledWorkPage{Event: p.Event, Attempt: int(p.Attempt), Reminder: p.Reminder, State: p.State})
	}
	return out
}
