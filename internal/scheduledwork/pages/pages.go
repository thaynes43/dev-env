// Package pages builds and ensures the page Reminders of DESIGN-054 Decision 5
// (step S4a). The operator has no Pushover sender; the keeper does. A page is
// one keyed, one-shot Reminder that is due now and targets only the item's
// spec.notify Pushover references. The keeper sends it with DESIGN-032's
// claim-before-send.
//
//   - Key names the Reminder from (item UID, occurrence, event, attempt).
//   - Build makes the Reminder, or refuses when the item has no notify target.
//   - PageState maps a Reminder's delivery status to the page state.
//   - Pager creates the Reminder if it is absent and returns the page entry to
//     record. It is best effort: it never returns an error, and an unserved
//     Reminder kind, a refused create or a missing notify target is a
//     skipped page. After AlreadyExists, or a create error that may have
//     come after the Reminder was stored, one read under its own
//     VerifyTimeout decides: only this page's own Reminder is recorded. It
//     never updates or deletes a Reminder.
//
// The executor and the releaser (S4b) call Pager.Ensure for an occurrence's
// stored page intent and record the entry with v1alpha1.AppendPage.
// Pager.Refresh, which copies a Reminder's final state onto its page, has no
// caller yet.
package pages

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

const (
	// Owner is the fixed client identity that owns every page Reminder: the
	// operator's scheduled-work identity (DESIGN-054 "Authority").
	Owner = "client/dev-env-system/scheduled-work"

	// KeyPrefix starts every page Reminder name.
	KeyPrefix = "swp-"

	// LabelEvent and LabelAttempt link a page Reminder to its event and
	// attempt. The item UID and the occurrence use v1alpha1.LabelScheduledWork
	// and v1alpha1.AnnotationScheduledWorkOccurrence as label keys.
	LabelEvent   = v1alpha1.LabelPrefix + "scheduled-work-page-event"
	LabelAttempt = v1alpha1.LabelPrefix + "scheduled-work-page-attempt"

	// MaxTitleBytes and MaxBodyBytes are the Reminder limits. The API counts
	// bytes, the schema counts characters; a byte bound satisfies both.
	MaxTitleBytes = 120
	MaxBodyBytes  = 1024

	// TimeZone is the zone of a page Reminder's due time.
	TimeZone = "UTC"

	dueLayout = "2006-01-02T15:04"

	// VerifyTimeout bounds the one verifying read after AlreadyExists or an
	// ambiguous create error. The read runs on a fresh deadline, not the
	// caller's: the caller's deadline may be what ended the create.
	VerifyTimeout = 3 * time.Second
)

// ErrNoNotify is Build's error for an item with no usable notify target.
var ErrNoNotify = errors.New("the item has no pushover notify target")

var pushoverRef = regexp.MustCompile(`^pushover:[a-z0-9][a-z0-9-]{0,62}$`)

var events = []string{
	v1alpha1.PageEventDue, v1alpha1.PageEventLink, v1alpha1.PageEventParked,
	v1alpha1.PageEventFailed, v1alpha1.PageEventResult, v1alpha1.PageEventSkipped,
}

// Key returns the page Reminder's name: "swp-" and 32 hex digits of the
// SHA-256 of (item UID, occurrence in Unix seconds, event, attempt). It is a
// DNS label of 36 characters, deterministic, and distinct for any different
// tuple. A Reminder name is never reused, so a retry of the same page finds
// the Reminder it already made.
func Key(work *v1alpha1.ScheduledWork, at time.Time, event string, attempt int32) string {
	var uid types.UID
	if work != nil {
		uid = work.UID
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{
		string(uid), strconv.FormatInt(at.Unix(), 10), event, strconv.FormatInt(int64(attempt), 10),
	}, "\x00")))
	return KeyPrefix + hex.EncodeToString(sum[:])[:32]
}

// Build returns the one-shot Reminder of a page, due at now (read in UTC to
// the minute; a one-shot fires however late). It targets only the item's
// pushover notify references, is owned by Owner and lives in the item's
// namespace. The title is trimmed and cut to MaxTitleBytes and the body to
// MaxBodyBytes, each on a rune boundary. It refuses an item without a UID, an
// unknown event, an attempt outside 0..ScheduledWorkMaxAttempt, an empty
// title, and (ErrNoNotify) an item with no pushover notify target.
func Build(work *v1alpha1.ScheduledWork, at time.Time, event string, attempt int32, title, body string, now time.Time) (*v1alpha1.Reminder, error) {
	if work == nil || work.UID == "" {
		return nil, errors.New("the item has no UID")
	}
	if !slices.Contains(events, event) {
		return nil, fmt.Errorf("unknown page event %q", event)
	}
	if attempt < 0 || attempt > v1alpha1.ScheduledWorkMaxAttempt {
		return nil, fmt.Errorf("attempt %d is outside 0..%d", attempt, v1alpha1.ScheduledWorkMaxAttempt)
	}
	targets := notifyTargets(work.Spec.Notify)
	if len(targets) == 0 {
		return nil, ErrNoNotify
	}
	title = cut(strings.TrimSpace(title), MaxTitleBytes)
	if title == "" {
		return nil, errors.New("the page has no title")
	}
	return &v1alpha1.Reminder{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: work.Namespace,
			Name:      Key(work, at, event, attempt),
			Labels: map[string]string{
				v1alpha1.LabelScheduledWork:                string(work.UID),
				v1alpha1.AnnotationScheduledWorkOccurrence: strconv.FormatInt(at.Unix(), 10),
				LabelEvent:   event,
				LabelAttempt: strconv.FormatInt(int64(attempt), 10),
			},
			Annotations: map[string]string{
				v1alpha1.AnnotationScheduledWorkName: work.Name,
			},
		},
		Spec: v1alpha1.ReminderSpec{
			Title:    title,
			Body:     cut(body, MaxBodyBytes),
			Owner:    Owner,
			Due:      now.UTC().Format(dueLayout),
			TimeZone: TimeZone,
			Targets:  targets,
		},
	}, nil
}

// notifyTargets keeps the valid, distinct pushover references of notify, in
// order, at most the Reminder's eight. A calendar reference is not a page.
func notifyTargets(notify []string) []string {
	var out []string
	for _, n := range notify {
		if pushoverRef.MatchString(n) && !slices.Contains(out, n) && len(out) < 8 {
			out = append(out, n)
		}
	}
	return out
}

// cut returns s, made valid UTF-8, and at most n bytes long, cut on a rune
// boundary.
func cut(s string, n int) string {
	s = strings.ToValidUTF8(s, "�")
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// PageState maps a Reminder's delivery status to a page state: sent, failed,
// ambiguous (the Reminder failed with a delivery whose request may have
// reached the provider, or whose claim was never resolved) or skipped (the
// Reminder delivered nothing and failed nothing, or was cancelled). A skipped
// or cancelled Reminder still reports what its deliveries show, because a
// cancel can land after a target was sent or while a claim is unresolved:
// sent when any delivery was sent, else ambiguous when any is ambiguous or
// claimed. It returns "" while the Reminder is pending, absent or in a state
// it does not know.
func PageState(r *v1alpha1.Reminder) string {
	if r == nil {
		return ""
	}
	switch r.Status.State {
	case v1alpha1.ReminderSent:
		return "sent"
	case v1alpha1.ReminderFailed:
		if deliveriesShow(r, v1alpha1.DeliveryAmbiguous, v1alpha1.DeliveryClaimed) {
			return "ambiguous"
		}
		return "failed"
	case v1alpha1.ReminderSkipped, v1alpha1.ReminderCancelled:
		switch {
		case deliveriesShow(r, v1alpha1.DeliverySent):
			return "sent"
		case deliveriesShow(r, v1alpha1.DeliveryAmbiguous, v1alpha1.DeliveryClaimed):
			return "ambiguous"
		}
		return "skipped"
	}
	return ""
}

// deliveriesShow reports whether any of r's deliveries is in one of states.
func deliveriesShow(r *v1alpha1.Reminder, states ...string) bool {
	for _, d := range r.Status.Deliveries {
		if slices.Contains(states, d.State) {
			return true
		}
	}
	return false
}

// Pager creates page Reminders through a controller-runtime client. Give it a
// client that reads the API server (uncached) and writes Reminders in the
// items' namespace.
type Pager struct {
	Client client.Client
}

// Ensure creates the page Reminder if it is absent and returns the page entry
// to record: the Reminder's name and, when it already exists, its state so far.
// A Reminder that already exists is success (a retry, or a replay after a
// restart) when one read shows it is this page's own: owned by Owner and
// labelled with the item UID, the occurrence, the event and the attempt. A
// create error that may have come after the API server stored the Reminder
// (a timeout, a server error, a lost connection) gets the same one read, so
// a page that will be sent is not recorded skipped. It never returns an error
// that could block the caller: an item with no notify target, an unserved
// Reminder kind (meta.IsNoMatchError), a refused create, and a read that
// finds no Reminder of this page give a skipped page with no Reminder. It
// never updates or deletes a Reminder.
//
// The entry is always valid for the schema: an unknown event becomes a skipped
// page (event "skipped"), and an attempt outside 0..ScheduledWorkMaxAttempt is
// held to that range in the skipped entry only: it creates nothing, so it
// never takes the key of a real attempt's page.
func (p *Pager) Ensure(ctx context.Context, work *v1alpha1.ScheduledWork, at time.Time, event string, attempt int32, title, body string, now time.Time) v1alpha1.ScheduledWorkPage {
	page, _ := p.EnsureChecked(ctx, work, at, event, attempt, title, body, now)
	return page
}

// EnsureChecked is Ensure that also reports whether its answer is settled.
// It is false only when a Reminder of this page may exist but could not be
// read back: the create met AlreadyExists, or an error after which the
// Reminder may be stored, and the verifying read failed (it did not answer,
// as opposed to finding no Reminder or another's). The entry is then the
// skipped one, and a caller that keeps an intent can leave it pending and
// try again later, instead of recording skipped for a page that will most
// likely be sent.
func (p *Pager) EnsureChecked(ctx context.Context, work *v1alpha1.ScheduledWork, at time.Time, event string, attempt int32, title, body string, now time.Time) (v1alpha1.ScheduledWorkPage, bool) {
	if !slices.Contains(events, event) {
		event = v1alpha1.PageEventSkipped
		title, body = "", ""
	}
	skipped := v1alpha1.ScheduledWorkPage{Event: event, Attempt: min(max(attempt, 0), v1alpha1.ScheduledWorkMaxAttempt), State: "skipped"}
	if p == nil || p.Client == nil {
		return skipped, true
	}
	// Build refuses an attempt out of range, so nothing is created for it.
	rem, err := Build(work, at, event, attempt, title, body, now)
	if err != nil {
		return skipped, true
	}
	err = p.Client.Create(ctx, rem)
	switch {
	case err == nil:
		return v1alpha1.ScheduledWorkPage{Event: event, Attempt: attempt, Reminder: rem.Name}, true
	case refused(err):
		return skipped, true
	}
	// AlreadyExists, or an error after which the Reminder may be stored.
	page, ok, read := p.stored(ctx, rem)
	switch {
	case ok:
		return page, true
	case !read:
		return skipped, false
	}
	return skipped, true
}

// refused reports a create error that proves nothing was stored: the kind is
// not served, or the API server refused the request (forbidden, invalid, bad,
// unauthorized, not found, not supported).
func refused(err error) bool {
	return meta.IsNoMatchError(err) || apierrors.IsForbidden(err) || apierrors.IsInvalid(err) ||
		apierrors.IsBadRequest(err) || apierrors.IsUnauthorized(err) || apierrors.IsNotFound(err) ||
		apierrors.IsMethodNotSupported(err)
}

// stored reads the Reminder of want's name once and returns its page entry,
// with its state so far, when it is want's page: the same Owner and the same
// item UID, occurrence, event and attempt labels. A failed read, an absent
// Reminder and anyone else's Reminder are not. The read keeps ctx's values
// but not its cancellation, under its own VerifyTimeout. read is false when
// the read itself failed (anything but NotFound), so nothing is known.
func (p *Pager) stored(ctx context.Context, want *v1alpha1.Reminder) (page v1alpha1.ScheduledWorkPage, ok, read bool) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), VerifyTimeout)
	defer cancel()
	var got v1alpha1.Reminder
	if err := p.Client.Get(rctx, client.ObjectKeyFromObject(want), &got); err != nil {
		return v1alpha1.ScheduledWorkPage{}, false, apierrors.IsNotFound(err)
	}
	if got.Spec.Owner != want.Spec.Owner {
		return v1alpha1.ScheduledWorkPage{}, false, true
	}
	for _, k := range []string{v1alpha1.LabelScheduledWork, v1alpha1.AnnotationScheduledWorkOccurrence, LabelEvent, LabelAttempt} {
		if got.Labels[k] == "" || got.Labels[k] != want.Labels[k] {
			return v1alpha1.ScheduledWorkPage{}, false, true
		}
	}
	attempt, err := strconv.ParseInt(want.Labels[LabelAttempt], 10, 32)
	if err != nil {
		return v1alpha1.ScheduledWorkPage{}, false, true
	}
	return v1alpha1.ScheduledWorkPage{Event: want.Labels[LabelEvent], Attempt: int32(attempt), Reminder: want.Name, State: PageState(&got)}, true, true
}

// EnsureInto is Ensure, recorded in pages with v1alpha1.AppendPage. It returns
// the new list and leaves pages unchanged. Two cases record nothing and
// return pages as they are, because Ensure's skipped entry would replace a
// real page of the same event and attempt and drop its Reminder name:
//
//   - an unknown event or an attempt outside 0..ScheduledWorkMaxAttempt;
//   - a skipped entry (no Reminder) for an event and attempt whose recorded
//     entry already names a Reminder: a replay whose Create met AlreadyExists
//     and whose verifying read failed. An unconfirmed read is no evidence
//     against a Reminder already recorded, so the entry keeps its name and its
//     last known state; a later replay whose read succeeds updates it.
func (p *Pager) EnsureInto(ctx context.Context, pages []v1alpha1.ScheduledWorkPage, work *v1alpha1.ScheduledWork, at time.Time, event string, attempt int32, title, body string, now time.Time) []v1alpha1.ScheduledWorkPage {
	if !slices.Contains(events, event) || attempt < 0 || attempt > v1alpha1.ScheduledWorkMaxAttempt {
		return pages
	}
	got := p.Ensure(ctx, work, at, event, attempt, title, body, now)
	if got.Reminder == "" && slices.ContainsFunc(pages, func(pg v1alpha1.ScheduledWorkPage) bool {
		return pg.Event == got.Event && pg.Attempt == got.Attempt && pg.Reminder != ""
	}) {
		return pages
	}
	return v1alpha1.AppendPage(pages, got)
}

// Refresh copies the state of the page's Reminder onto the page, with one
// read. It returns the page unchanged when it has no Reminder, the read
// fails, the Reminder is not served, or the Reminder is not this item's page
// (its item label differs), and keeps the old state while the Reminder is
// pending. It writes nothing.
func (p *Pager) Refresh(ctx context.Context, work *v1alpha1.ScheduledWork, page v1alpha1.ScheduledWorkPage) v1alpha1.ScheduledWorkPage {
	if p == nil || p.Client == nil || work == nil || page.Reminder == "" {
		return page
	}
	var rem v1alpha1.Reminder
	if err := p.Client.Get(ctx, client.ObjectKey{Namespace: work.Namespace, Name: page.Reminder}, &rem); err != nil {
		return page
	}
	if rem.Labels[v1alpha1.LabelScheduledWork] != string(work.UID) {
		return page
	}
	if s := PageState(&rem); s != "" {
		page.State = s
	}
	return page
}
