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
//     Reminder kind, a create error or a missing notify target is a skipped
//     page. It never updates or deletes a Reminder.
//
// The package has no caller yet. The executor and the releaser (S4b) call
// Pager.EnsureInto when an occurrence reaches a page event, and Pager.Refresh
// to copy a Reminder's final state onto its page.
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
// Reminder delivered nothing and failed nothing, or was cancelled). It returns
// "" while the Reminder is pending, absent or in a state it does not know.
func PageState(r *v1alpha1.Reminder) string {
	if r == nil {
		return ""
	}
	switch r.Status.State {
	case v1alpha1.ReminderSent:
		return "sent"
	case v1alpha1.ReminderFailed:
		for _, d := range r.Status.Deliveries {
			if d.State == v1alpha1.DeliveryAmbiguous || d.State == v1alpha1.DeliveryClaimed {
				return "ambiguous"
			}
		}
		return "failed"
	case v1alpha1.ReminderSkipped, v1alpha1.ReminderCancelled:
		return "skipped"
	}
	return ""
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
// restart). It never returns an error that could block the caller: an item
// with no notify target, an unserved Reminder kind (meta.IsNoMatchError) and
// any create error give a skipped page with no Reminder. It never updates or
// deletes a Reminder.
//
// The entry is always valid for the schema: an unknown event becomes a skipped
// page (event "skipped") and the attempt is held to 0..ScheduledWorkMaxAttempt.
func (p *Pager) Ensure(ctx context.Context, work *v1alpha1.ScheduledWork, at time.Time, event string, attempt int32, title, body string, now time.Time) v1alpha1.ScheduledWorkPage {
	if !slices.Contains(events, event) {
		event = v1alpha1.PageEventSkipped
		title, body = "", ""
	}
	attempt = min(max(attempt, 0), v1alpha1.ScheduledWorkMaxAttempt)
	skipped := v1alpha1.ScheduledWorkPage{Event: event, Attempt: attempt, State: "skipped"}
	if p == nil || p.Client == nil {
		return skipped
	}
	rem, err := Build(work, at, event, attempt, title, body, now)
	if err != nil {
		return skipped
	}
	err = p.Client.Create(ctx, rem)
	switch {
	case err == nil:
		return v1alpha1.ScheduledWorkPage{Event: event, Attempt: attempt, Reminder: rem.Name}
	case apierrors.IsAlreadyExists(err):
		page := v1alpha1.ScheduledWorkPage{Event: event, Attempt: attempt, Reminder: rem.Name}
		return p.Refresh(ctx, work, page)
	default:
		// An unserved kind (meta.IsNoMatchError) and every other create error.
		return skipped
	}
}

// EnsureInto is Ensure, recorded in pages with v1alpha1.AppendPage. It returns
// the new list and leaves pages unchanged.
func (p *Pager) EnsureInto(ctx context.Context, pages []v1alpha1.ScheduledWorkPage, work *v1alpha1.ScheduledWork, at time.Time, event string, attempt int32, title, body string, now time.Time) []v1alpha1.ScheduledWorkPage {
	return v1alpha1.AppendPage(pages, p.Ensure(ctx, work, at, event, attempt, title, body, now))
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
