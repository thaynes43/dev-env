package reminders

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// Message is what a Sender delivers.
type Message struct {
	Title, Body string
}

// Sender delivers one message to the one Pushover destination the keeper knows.
// It reports what it knows about how far the request got, by wrapping its error:
//
//   - ErrNotSent: no request reached the provider, so retrying is safe;
//   - ErrRejected: the provider answered and refused, so retrying is pointless;
//   - any other error: the request may have been accepted, which is ambiguous.
type Sender interface {
	Send(ctx context.Context, m Message) error
}

var (
	// ErrNotSent wraps an error raised before a request left the process, or one
	// the provider answered with a retryable status.
	ErrNotSent = errors.New("reminder not sent")
	// ErrRejected wraps a final refusal by the provider.
	ErrRejected = errors.New("reminder rejected")
)

// Scheduler delivers due reminders. It runs in the keeper's leader and keeps
// every decision in the Reminder's status, so a restart or a new leader reads
// the state back and repeats nothing:
//
//   - each target's delivery of an occurrence is claimed with a compare-and-set
//     write BEFORE any request is made;
//   - a claim found on a later pass was never resolved, so it becomes
//     "ambiguous" and is never sent again automatically (Pushover has no
//     idempotency key; at-most-once is the honest choice);
//   - a failure before any request reached Pushover is retried a bounded number
//     of times, then failed.
type Scheduler struct {
	// Client writes status; Reader lists and gets from the API server, never a
	// cache.
	Client client.Client
	Reader client.Reader
	// Namespace holds the reminders.
	Namespace string
	// Sender delivers Pushover targets. Nil skips them, honestly.
	Sender Sender
	// Calendar writes calendar targets. Nil skips them with "calendar:
	// unconfigured": the real adapter is a reported gap.
	Calendar CalendarAdapter
	// Every is the polling period; 30 seconds when zero.
	Every time.Duration
	// MissGrace is how late a recurring occurrence may still fire; older ones
	// are recorded as missed. A one-shot always fires, however late. Six hours
	// when zero.
	MissGrace time.Duration
	// MaxAttempts bounds the claims before a send is failed; three when zero.
	MaxAttempts int32
	// Backoff[i] is the wait before retry i+1; 1m then 5m when nil.
	Backoff []time.Duration
	Log     logr.Logger
	// Now is the clock; time.Now when nil.
	Now func() time.Time
}

const (
	calendarBackoff = 5 * time.Minute
	maxAttemptLog   = 20
	// maxCalendarEntries is the CRD's bound on status.calendar. The status is
	// pruned to it on every pass, so a write can never be refused for size.
	maxCalendarEntries = 8
	// supersededReason marks a retry an edit made pointless: it was never sent.
	supersededReason = "superseded by an edit"
)

// NeedLeaderElection makes the scheduler run in the leader only.
func (s *Scheduler) NeedLeaderElection() bool { return true }

// Start runs until ctx ends. It implements controller-runtime's Runnable.
func (s *Scheduler) Start(ctx context.Context) error {
	every := s.Every
	if every <= 0 {
		every = 30 * time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		s.Tick(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

func (s *Scheduler) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Scheduler) grace() time.Duration {
	if s.MissGrace > 0 {
		return s.MissGrace
	}
	return 6 * time.Hour
}

func (s *Scheduler) maxAttempts() int32 {
	if s.MaxAttempts > 0 {
		return s.MaxAttempts
	}
	return 3
}

func (s *Scheduler) backoff(attempt int32) time.Duration {
	b := s.Backoff
	if b == nil {
		b = []time.Duration{time.Minute, 5 * time.Minute}
	}
	if len(b) == 0 {
		return 0
	}
	return b[min(int(attempt)-1, len(b)-1)]
}

// Tick makes one pass over every live reminder.
func (s *Scheduler) Tick(ctx context.Context) {
	var list v1alpha1.ReminderList
	if err := s.Reader.List(ctx, &list, client.InNamespace(s.Namespace)); err != nil {
		s.Log.Error(err, "list reminders")
		return
	}
	for i := range list.Items {
		if ctx.Err() != nil {
			return
		}
		s.tickOne(ctx, &list.Items[i])
	}
}

func finalState(state string) bool {
	return state == v1alpha1.ReminderSent || state == v1alpha1.ReminderFailed || state == v1alpha1.ReminderSkipped || state == v1alpha1.ReminderCancelled
}

func (s *Scheduler) tickOne(ctx context.Context, r *v1alpha1.Reminder) {
	if finalState(r.Status.State) {
		return
	}
	now := s.now()
	if r.Spec.Cancelled {
		s.cancel(ctx, r, now)
		return
	}
	changed := false
	if r.Status.PlanHash != PlanHash(r.Spec) && len(r.Status.Deliveries) == 0 {
		s.plan(r, now)
		changed = true
	}
	if r.Status.State == v1alpha1.ReminderFailed {
		// plan could not schedule it.
		s.write(ctx, r)
		return
	}
	if s.supersedeRetries(r, now) {
		// An edit moved the schedule, or removed the target, while the old
		// occurrence was backing off from a failure that never reached the
		// provider. Nothing was sent, so the retry is dropped rather than sent
		// late; fire then closes the occurrence and finish plans what is next.
		changed = true
	}
	if s.syncCalendar(ctx, r, now) {
		changed = true
	}
	if changed && !s.write(ctx, r) {
		return
	}
	if r.Status.NextDue != nil && !r.Status.NextDue.After(now) {
		s.fire(ctx, r, now)
	}
}

// write stores the status. A conflict means an edit, a cancel or another leader
// got there first: the pass stops and the next one reads the new state.
func (s *Scheduler) write(ctx context.Context, r *v1alpha1.Reminder) bool {
	if err := s.Client.Status().Update(ctx, r); err != nil {
		if apierrors.IsConflict(err) || apierrors.IsNotFound(err) {
			s.Log.V(1).Info("reminder changed under the scheduler; the next pass reads it", "reminder", r.Name)
		} else {
			s.Log.Error(err, "write reminder status", "reminder", r.Name)
		}
		return false
	}
	return true
}

// plan computes NextDue from the schedule fields. A one-shot is always
// occurrence 1, however late; a recurring series starts at the first
// occurrence within the miss grace.
func (s *Scheduler) plan(r *v1alpha1.Reminder, now time.Time) {
	r.Status.PlanHash = PlanHash(r.Spec)
	sch, err := ScheduleOf(r.Spec)
	if err != nil {
		s.fail(r, "invalid schedule: "+truncate(err.Error(), 200))
		return
	}
	var notBefore time.Time
	if sch.Recurring() {
		notBefore = now.Add(-s.grace())
	}
	occ, ok := sch.Next(notBefore)
	if !ok {
		s.fail(r, "no occurrence remains at or after "+notBefore.UTC().Format(time.RFC3339))
		return
	}
	r.Status.State = v1alpha1.ReminderPending
	r.Status.NextDue = &metav1.Time{Time: occ.At.UTC()}
	r.Status.Occurrence = int32(occ.N)
	r.Status.Message = ""
}

func (s *Scheduler) fail(r *v1alpha1.Reminder, msg string) {
	r.Status.State = v1alpha1.ReminderFailed
	r.Status.NextDue = nil
	r.Status.Message = truncate(msg, 256)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func calendarTargets(spec v1alpha1.ReminderSpec) []Target {
	var out []Target
	for _, raw := range spec.Targets {
		if t, err := ParseTarget(raw); err == nil && t.Kind == v1alpha1.ReminderTargetCalendar {
			out = append(out, t)
		}
	}
	return out
}

func pushoverTargets(spec v1alpha1.ReminderSpec) []string {
	var out []string
	for _, raw := range spec.Targets {
		if strings.HasPrefix(raw, v1alpha1.ReminderTargetPushover+":") {
			out = append(out, raw)
		}
	}
	return out
}

func syncEntry(r *v1alpha1.Reminder, target string) *v1alpha1.ReminderCalendarSync {
	for i := range r.Status.Calendar {
		if r.Status.Calendar[i].Target == target {
			return &r.Status.Calendar[i]
		}
	}
	return nil
}

func setSync(r *v1alpha1.Reminder, e v1alpha1.ReminderCalendarSync) {
	if cur := syncEntry(r, e.Target); cur != nil {
		*cur = e
		return
	}
	r.Status.Calendar = append(r.Status.Calendar, e)
}

// syncCalendar brings each calendar target in line with the spec and reports
// whether the status changed. Without an adapter it records an honest skip.
func (s *Scheduler) syncCalendar(ctx context.Context, r *v1alpha1.Reminder, now time.Time) bool {
	changed := false
	stamp := metav1.NewTime(now.UTC())
	want := map[string]bool{}
	hash := CalendarHash(r.Spec)
	for _, t := range calendarTargets(r.Spec) {
		ref := v1alpha1.ReminderTargetCalendar + ":" + t.Ref
		want[ref] = true
		cur := syncEntry(r, ref)
		if s.Calendar == nil {
			const reason = "calendar: unconfigured"
			if cur == nil || cur.State != v1alpha1.CalendarSkipped || cur.Reason != reason {
				setSync(r, v1alpha1.ReminderCalendarSync{Target: ref, State: v1alpha1.CalendarSkipped, Reason: reason, UpdatedAt: stamp})
				changed = true
			}
			continue
		}
		if cur != nil && cur.State == v1alpha1.CalendarSynced && cur.SpecHash == hash {
			continue
		}
		if cur != nil && cur.State == v1alpha1.CalendarFailed && now.Sub(cur.UpdatedAt.Time) < calendarBackoff {
			continue
		}
		sch, err := ScheduleOf(r.Spec)
		if err != nil {
			continue
		}
		id := EventID(r.Namespace, r.Name)
		ev := CalendarEvent{ID: id, Title: r.Spec.Title, Body: r.Spec.Body, Start: r.Spec.Due, TimeZone: r.Spec.TimeZone, RRule: sch.RRule()}
		if err := s.Calendar.Upsert(ctx, t, ev); err != nil {
			s.Log.Info("calendar upsert failed", "reminder", r.Name, "target", ref)
			setSync(r, v1alpha1.ReminderCalendarSync{Target: ref, EventID: id, State: v1alpha1.CalendarFailed, Reason: "upsert failed", UpdatedAt: stamp})
		} else {
			setSync(r, v1alpha1.ReminderCalendarSync{Target: ref, EventID: id, SpecHash: hash, State: v1alpha1.CalendarSynced, UpdatedAt: stamp})
		}
		changed = true
	}
	// A target an edit removed: take its entry down.
	for i := range r.Status.Calendar {
		e := &r.Status.Calendar[i]
		if want[e.Target] || e.State != v1alpha1.CalendarSynced || s.Calendar == nil {
			continue
		}
		if t, err := ParseTarget(e.Target); err == nil && s.Calendar.Delete(ctx, t, e.EventID) == nil {
			*e = v1alpha1.ReminderCalendarSync{Target: e.Target, State: v1alpha1.CalendarDeleted, Reason: "target removed", UpdatedAt: stamp}
			changed = true
		}
	}
	if s.pruneCalendar(r, want) {
		changed = true
	}
	return changed
}

// pruneCalendar bounds status.calendar on every pass, whatever the adapter's
// state, so the CRD's maxItems can never refuse a status write. Entries of
// targets the spec no longer names are dropped unless their calendar entry is
// still standing ("synced": its delete failed, or there is no adapter to make
// it). If those alone exceed the bound, the oldest are dropped anyway and the
// calendar entry they named is orphaned; that is logged.
func (s *Scheduler) pruneCalendar(r *v1alpha1.Reminder, want map[string]bool) bool {
	before := len(r.Status.Calendar)
	kept := make([]v1alpha1.ReminderCalendarSync, 0, before)
	for _, e := range r.Status.Calendar {
		if want[e.Target] || e.State == v1alpha1.CalendarSynced {
			kept = append(kept, e)
		}
	}
	for len(kept) > maxCalendarEntries {
		oldest := -1
		for i, e := range kept {
			if !want[e.Target] && (oldest < 0 || e.UpdatedAt.Before(&kept[oldest].UpdatedAt)) {
				oldest = i
			}
		}
		if oldest < 0 {
			break
		}
		s.Log.Info("calendar status over its bound: dropping the oldest stale entry; its calendar entry may be orphaned",
			"reminder", r.Name, "target", kept[oldest].Target, "eventID", kept[oldest].EventID)
		kept = append(kept[:oldest], kept[oldest+1:]...)
	}
	if len(kept) == before {
		return false
	}
	if len(kept) == 0 {
		kept = nil
	}
	r.Status.Calendar = kept
	return true
}

// cancel turns the owner's cancel into the cancelled state and removes the
// calendar entries first. A delete that fails leaves the reminder pending-
// cancelled, so the next pass retries, and nothing fires meanwhile.
func (s *Scheduler) cancel(ctx context.Context, r *v1alpha1.Reminder, now time.Time) {
	stamp := metav1.NewTime(now.UTC())
	for i := range r.Status.Calendar {
		e := &r.Status.Calendar[i]
		if e.State != v1alpha1.CalendarSynced || s.Calendar == nil {
			continue
		}
		t, err := ParseTarget(e.Target)
		if err != nil {
			continue
		}
		if err := s.Calendar.Delete(ctx, t, e.EventID); err != nil {
			s.Log.Info("calendar delete failed", "reminder", r.Name, "target", e.Target)
			return
		}
		*e = v1alpha1.ReminderCalendarSync{Target: e.Target, State: v1alpha1.CalendarDeleted, Reason: "reminder cancelled", UpdatedAt: stamp}
	}
	r.Status.State = v1alpha1.ReminderCancelled
	r.Status.NextDue = nil
	r.Status.Message = "cancelled by its owner"
	s.write(ctx, r)
}

func findDelivery(r *v1alpha1.Reminder, target string, occ int32) *v1alpha1.ReminderDelivery {
	for i := range r.Status.Deliveries {
		d := &r.Status.Deliveries[i]
		if d.Target == target && d.Occurrence == occ {
			return d
		}
	}
	return nil
}

func addAttempt(r *v1alpha1.Reminder, now time.Time, occ int32, target, outcome, detail string) {
	r.Status.Attempts = append(r.Status.Attempts, v1alpha1.ReminderAttempt{
		At: metav1.NewTime(now.UTC()), Occurrence: occ, Target: target, Outcome: outcome, Detail: truncate(detail, 128)})
	if over := len(r.Status.Attempts) - maxAttemptLog; over > 0 {
		r.Status.Attempts = slices.Clone(r.Status.Attempts[over:])
	}
}

// supersedeRetries drops the retries of the current occurrence that an edit made
// pointless: the schedule changed, or the target is no longer in the spec. A
// claimed, sent or ambiguous delivery is left alone: those resolve for themselves
// and are never sent again.
func (s *Scheduler) supersedeRetries(r *v1alpha1.Reminder, now time.Time) bool {
	replanned := r.Status.PlanHash != PlanHash(r.Spec)
	changed := false
	for i := range r.Status.Deliveries {
		d := &r.Status.Deliveries[i]
		if d.State != v1alpha1.DeliveryRetry || (!replanned && slices.Contains(r.Spec.Targets, d.Target)) {
			continue
		}
		d.State, d.NotBefore, d.Reason, d.UpdatedAt = v1alpha1.DeliverySkipped, nil, supersededReason, metav1.NewTime(now.UTC())
		addAttempt(r, now, d.Occurrence, d.Target, "skipped", supersededReason)
		changed = true
	}
	return changed
}

// fire delivers the due occurrence.
func (s *Scheduler) fire(ctx context.Context, r *v1alpha1.Reminder, now time.Time) {
	occ := r.Status.Occurrence
	due := r.Status.NextDue.Time
	if r.Spec.Recurrence != nil && now.Sub(due) > s.grace() {
		addAttempt(r, now, occ, "", "missed", fmt.Sprintf("due %s, %s late", due.UTC().Format(time.RFC3339), now.Sub(due).Round(time.Minute)))
		s.finish(ctx, r, now, "missed")
		return
	}
	for _, target := range pushoverTargets(r.Spec) {
		d := findDelivery(r, target, occ)
		stamp := metav1.NewTime(now.UTC())
		switch {
		case d == nil:
			if target != v1alpha1.ReminderDefaultPushover || s.Sender == nil {
				r.Status.Deliveries = append(r.Status.Deliveries, v1alpha1.ReminderDelivery{Target: target, Occurrence: occ,
					State: v1alpha1.DeliverySkipped, Reason: "pushover: unconfigured", UpdatedAt: stamp})
				addAttempt(r, now, occ, target, "skipped", "pushover: unconfigured")
				if !s.write(ctx, r) {
					return
				}
				continue
			}
			r.Status.Deliveries = append(r.Status.Deliveries, v1alpha1.ReminderDelivery{Target: target, Occurrence: occ,
				State: v1alpha1.DeliveryClaimed, Attempts: 1, UpdatedAt: stamp})
		case d.State == v1alpha1.DeliveryClaimed:
			// Claimed and never resolved: a request may have been made.
			d.State, d.Reason, d.UpdatedAt = v1alpha1.DeliveryAmbiguous, "the claim was never resolved: a request may have been sent", stamp
			addAttempt(r, now, occ, target, "ambiguous", d.Reason)
			if !s.write(ctx, r) {
				return
			}
			continue
		case d.State == v1alpha1.DeliveryRetry && (d.NotBefore == nil || !d.NotBefore.After(now)):
			d.State, d.NotBefore, d.Attempts, d.UpdatedAt = v1alpha1.DeliveryClaimed, nil, d.Attempts+1, stamp
		default:
			continue
		}
		// The claim is the compare-and-set: it loses to an edit, a cancel or a
		// second scheduler, and then nothing is sent.
		if !s.write(ctx, r) {
			return
		}
		msg := Message{Title: r.Spec.Title, Body: r.Spec.Body}
		if msg.Body == "" {
			msg.Body = "Reminder due"
		}
		s.record(ctx, r, target, occ, s.Sender.Send(ctx, msg))
	}
	if occurrenceDone(r, occ) {
		s.finish(ctx, r, now, "")
	}
}

// record stores a send's result on the claimed delivery. It retries on a
// conflict against a fresh copy, because the result of a request already made
// must not be lost to an edit or a cancel.
func (s *Scheduler) record(ctx context.Context, r *v1alpha1.Reminder, target string, occ int32, sendErr error) {
	apply := func(rr *v1alpha1.Reminder) bool {
		d := findDelivery(rr, target, occ)
		if d == nil || d.State != v1alpha1.DeliveryClaimed {
			return false
		}
		now := s.now()
		d.UpdatedAt = metav1.NewTime(now.UTC())
		switch {
		case sendErr == nil:
			d.State = v1alpha1.DeliverySent
			addAttempt(rr, now, occ, target, "sent", "")
		case errors.Is(sendErr, ErrNotSent) && d.Attempts < s.maxAttempts():
			nb := metav1.NewTime(now.Add(s.backoff(d.Attempts)).UTC())
			d.State, d.NotBefore, d.Reason = v1alpha1.DeliveryRetry, &nb, "not sent; will retry"
			addAttempt(rr, now, occ, target, "retry", d.Reason)
		case errors.Is(sendErr, ErrNotSent):
			d.State, d.Reason = v1alpha1.DeliveryFailed, "not sent; retries exhausted"
			addAttempt(rr, now, occ, target, "failed", d.Reason)
		case errors.Is(sendErr, ErrRejected):
			d.State, d.Reason = v1alpha1.DeliveryFailed, "refused by the provider"
			addAttempt(rr, now, occ, target, "failed", d.Reason)
		default:
			d.State, d.Reason = v1alpha1.DeliveryAmbiguous, "the request may have reached the provider"
			addAttempt(rr, now, occ, target, "ambiguous", d.Reason)
		}
		return true
	}
	for range 3 {
		if !apply(r) {
			return
		}
		err := s.Client.Status().Update(ctx, r)
		if err == nil {
			return
		}
		if !apierrors.IsConflict(err) {
			s.Log.Error(err, "record delivery result", "reminder", r.Name)
			return
		}
		var fresh v1alpha1.Reminder
		if err := s.Reader.Get(ctx, types.NamespacedName{Namespace: r.Namespace, Name: r.Name}, &fresh); err != nil {
			return
		}
		fresh.DeepCopyInto(r)
	}
}

// occurrenceDone reports whether every pushover target of the occurrence has a
// final delivery.
func occurrenceDone(r *v1alpha1.Reminder, occ int32) bool {
	for _, target := range pushoverTargets(r.Spec) {
		d := findDelivery(r, target, occ)
		if d == nil {
			return false
		}
		switch d.State {
		case v1alpha1.DeliverySent, v1alpha1.DeliveryFailed, v1alpha1.DeliveryAmbiguous, v1alpha1.DeliverySkipped:
		default:
			return false
		}
	}
	return true
}

// finish closes the occurrence. The next one is planned from the CURRENT spec:
//
//   - if the schedule fields are the ones the closed occurrence was planned from,
//     the series simply advances;
//   - if an edit changed them while the occurrence was claimed, backing off or
//     sent, the closed occurrence resolved only for itself (it is never sent
//     again), and the new schedule is planned from scratch. An edit therefore
//     always yields the new schedule, and a one-shot is not closed by an
//     occurrence of a schedule that no longer exists.
func (s *Scheduler) finish(ctx context.Context, r *v1alpha1.Reminder, now time.Time, forced string) {
	occ := r.Status.Occurrence
	outcome := forced
	if outcome == "" {
		outcome = occurrenceOutcome(r, occ)
	}
	due := r.Status.NextDue.Time
	curPlan := PlanHash(r.Spec)
	replanned := r.Status.PlanHash != curPlan
	var next Occurrence
	var more bool
	if sch, err := ScheduleOf(r.Spec); err == nil {
		switch {
		case !replanned && sch.Recurring():
			next, more = sch.Next(due.Add(time.Minute))
		case replanned && sch.Recurring():
			next, more = sch.Next(laterOf(due.Add(time.Minute), now.Add(-s.grace())))
		case replanned:
			// The new schedule is a one-shot: its occurrence fires however late,
			// unless it is the instant that was just closed.
			next, more = sch.Next(time.Time{})
			if more && next.At.Equal(due) {
				more = false
			}
		}
	}
	r.Status.PlanHash = curPlan
	raw := outcome
	if outcome == "skipped" || outcome == "superseded" {
		outcome += " (nothing was delivered)"
	}
	suffix := ""
	if replanned {
		suffix = "; rescheduled by an edit"
	}
	if more {
		r.Status.State = v1alpha1.ReminderPending
		r.Status.NextDue = &metav1.Time{Time: next.At.UTC()}
		r.Status.Occurrence = int32(next.N)
		r.Status.Deliveries = nil
		r.Status.Message = fmt.Sprintf("occurrence %d: %s%s", occ, outcome, suffix)
	} else {
		r.Status.NextDue = nil
		switch raw {
		case "sent":
			r.Status.State = v1alpha1.ReminderSent
		case "skipped", "superseded":
			r.Status.State = v1alpha1.ReminderSkipped
		default:
			r.Status.State = v1alpha1.ReminderFailed
		}
		r.Status.Message = fmt.Sprintf("occurrence %d: %s%s", occ, outcome, suffix)
	}
	s.write(ctx, r)
}

// occurrenceOutcome says how an occurrence ended. It is "sent" ONLY if at least
// one delivery was actually sent. Otherwise it is "failed" when a delivery
// failed, was left ambiguous or was claimed and never resolved, "superseded" when
// an edit dropped a pending retry, and "skipped" when nothing was deliverable or
// configured (a calendar-only reminder, an unconfigured Pushover, a removed
// target). A skip is never reported as a send.
func occurrenceOutcome(r *v1alpha1.Reminder, occ int32) string {
	var sent, failed, superseded bool
	for _, d := range r.Status.Deliveries {
		if d.Occurrence != occ {
			continue
		}
		switch {
		case d.State == v1alpha1.DeliverySent:
			sent = true
		case d.State == v1alpha1.DeliveryFailed || d.State == v1alpha1.DeliveryAmbiguous || d.State == v1alpha1.DeliveryClaimed || d.State == v1alpha1.DeliveryRetry:
			failed = true
		case d.Reason == supersededReason:
			superseded = true
		}
	}
	switch {
	case failed:
		return "failed"
	case sent:
		return "sent"
	case superseded:
		return "superseded"
	}
	return "skipped"
}

func laterOf(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
