package executor

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/scheduledwork/pages"
)

// Pages (DESIGN-054 Decision 5, S4b). A page is one keyed, one-shot Reminder
// that the keeper sends; the executor only creates it, through Pager. Pages
// are best effort and never hold a state change. They are sent in two steps,
// so that no page goes out for a transition that was never stored:
//
//  1. The transition's own compare-and-set status write records the intent:
//     a page entry with the event and the occurrence's attempt, and no
//     Reminder and no state yet (intend). A write that loses its
//     compare-and-set loses its intent with it.
//  2. A later step finds the stored intent, calls Pager.Ensure, which creates
//     the Reminder named by pages.Key(item UID, occurrence, event, attempt)
//     or finds the one a retry, a replay or another leader already made, and
//     records the returned entry with v1alpha1.AppendPage in its own
//     compare-and-set write (deliver). Ensure never fails: a Reminder kind the
//     API server does not serve, or any create error, is a skipped entry,
//     and a skipped entry is final. An answer Ensure cannot confirm (its
//     Reminder may exist but the read back failed, Pager.EnsureChecked) is
//     not recorded: the intent stays pending, is tried once per pass, and a
//     later pass whose read works records the Reminder.
//
// An item with no notify target, or an executor with no Pager, records no
// intent and so pages nothing. Neither step updates or deletes a Reminder.
// Each Ensure gets its own deadline (Executor.PageTimeout), so a slow API
// server cannot hold the pass.

// DefaultPageTimeout bounds one Ensure's create when Executor.PageTimeout is
// zero. The pager's verifying read has its own pages.VerifyTimeout.
const DefaultPageTimeout = 10 * time.Second

// DefaultMaxUnconfirmedPageTries bounds, when
// Executor.MaxUnconfirmedPageTries is zero, the passes in which a page whose
// Ensure cannot be confirmed is tried before it is settled skipped.
const DefaultMaxUnconfirmedPageTries = 5

// NewPager returns the executor's page Reminder creator. It creates through
// c, the operator's writing client, and makes its one read (the existing
// Reminder after AlreadyExists) through r, the uncached API reader, so it
// starts no informer on Reminders.
func NewPager(c client.Client, r client.Reader) *pages.Pager {
	return &pages.Pager{Client: uncachedReads{Client: c, reader: r}}
}

// uncachedReads is a client whose reads go to reader and whose writes go to
// the embedded Client.
type uncachedReads struct {
	client.Client
	reader client.Reader
}

func (c uncachedReads) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	return c.reader.Get(ctx, key, obj, opts...)
}

func (c uncachedReads) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	return c.reader.List(ctx, list, opts...)
}

// pageEvent is the page event the executor sends when an occurrence reaches
// state: due once it is Dispatched, parked, skipped, and failed for an
// Ambiguous occurrence (the executor's own failure). Cancelled and the
// states before Dispatched send none; a session's outcome (Succeeded,
// Failed, Ended) is the releaser's page.
func pageEvent(state string) string {
	switch state {
	case v1alpha1.OccurrenceDispatched:
		return v1alpha1.PageEventDue
	case v1alpha1.OccurrenceParked:
		return v1alpha1.PageEventParked
	case v1alpha1.OccurrenceSkipped:
		return v1alpha1.PageEventSkipped
	case v1alpha1.OccurrenceAmbiguous:
		return v1alpha1.PageEventFailed
	}
	return ""
}

// intendFor records the intent to page the event o's state sends, if any.
// Call it on the occurrence of the transition, before the transition's
// status write.
func (p *pass) intendFor(w *v1alpha1.ScheduledWork, o *v1alpha1.ScheduledWorkOccurrence) {
	if event := pageEvent(o.State); event != "" {
		p.intend(w, o, event)
	}
}

// intend adds the intent to page event for o's attempt: a page entry with no
// Reminder and no state. It records nothing without a Pager, for an item
// with no notify target, or when o already has an entry for the event and
// attempt (a page is sent once).
func (p *pass) intend(w *v1alpha1.ScheduledWork, o *v1alpha1.ScheduledWorkOccurrence, event string) {
	if p.e.Pager == nil || len(w.Spec.Notify) == 0 || pageEntry(o, event, o.Attempt) != nil {
		return
	}
	o.Pages = v1alpha1.AppendPage(o.Pages, v1alpha1.ScheduledWorkPage{Event: event, Attempt: o.Attempt})
}

// pageEntry is o's page entry for (event, attempt), or nil.
func pageEntry(o *v1alpha1.ScheduledWorkOccurrence, event string, attempt int32) *v1alpha1.ScheduledWorkPage {
	for i := range o.Pages {
		if o.Pages[i].Event == event && o.Pages[i].Attempt == attempt {
			return &o.Pages[i]
		}
	}
	return nil
}

// pendingPage reports a stored intent: an entry with no Reminder and no
// state.
func pendingPage(pg v1alpha1.ScheduledWorkPage) bool {
	return pg.Reminder == "" && pg.State == ""
}

// executorSends reports whether the executor sends the pending page pg of o:
// the events it records itself. A failed page of any state but Ambiguous,
// and a result page, are the releaser's: it sends them once the agent's
// reported outcome is in.
func executorSends(o *v1alpha1.ScheduledWorkOccurrence, pg v1alpha1.ScheduledWorkPage) bool {
	switch pg.Event {
	case v1alpha1.PageEventDue, v1alpha1.PageEventParked, v1alpha1.PageEventSkipped:
		return true
	case v1alpha1.PageEventFailed:
		return o.State == v1alpha1.OccurrenceAmbiguous
	}
	return false
}

// deliver sends every stored intent of w the executor sends, through the
// Pager, and records the entries Ensure returns in one status write. It
// reports whether it wrote. A conflict reads the item again, and the next
// step finds what is still pending: Ensure finds the Reminder it made.
func (p *pass) deliver(ctx context.Context, w *v1alpha1.ScheduledWork, now time.Time, lg logr.Logger) (bool, error) {
	if p.e.Pager == nil {
		return false, nil
	}
	sent := 0
	for i := range w.Status.Occurrences {
		o := &w.Status.Occurrences[i]
		for _, pg := range o.Pages {
			key := pageTryKey(o, pg)
			if !pendingPage(pg) || !executorSends(o, pg) || p.pagesTried[key] {
				continue
			}
			title, body := pageText(w, o, pg.Event)
			pctx, cancel := context.WithTimeout(ctx, positiveOr(p.e.PageTimeout, DefaultPageTimeout))
			got, settled := p.e.Pager.EnsureChecked(pctx, w, o.At.Time, pg.Event, pg.Attempt, title, body, now)
			cancel()
			if !settled {
				// The Reminder may exist; a later pass reads it again.
				p.pagesTried[key] = true
				lg.Info("a page Reminder could not be confirmed; it is tried again next pass", "occurrence", o.At.UTC().Format(time.RFC3339), "event", pg.Event)
				continue
			}
			o.Pages = settle(o.Pages, pg, got)
			sent++
		}
	}
	if sent == 0 {
		return false, nil
	}
	lg.Info("recorded the scheduled work's pages", "pages", sent)
	return true, p.e.write(ctx, w)
}

// pageTryKey names an occurrence's page for the pass's set of pages tried.
func pageTryKey(o *v1alpha1.ScheduledWorkOccurrence, pg v1alpha1.ScheduledWorkPage) string {
	return fmt.Sprintf("%d/%s/%d", o.At.Unix(), pg.Event, pg.Attempt)
}

// settle records got, Ensure's entry for the intent pg, in pages. An entry
// for another event or attempt (Ensure holds an invalid one to a valid
// skipped entry) settles the intent as skipped, so it is never sent again.
func settle(pages []v1alpha1.ScheduledWorkPage, pg, got v1alpha1.ScheduledWorkPage) []v1alpha1.ScheduledWorkPage {
	if got.Event != pg.Event || got.Attempt != pg.Attempt {
		got = v1alpha1.ScheduledWorkPage{Event: pg.Event, Attempt: pg.Attempt, State: "skipped"}
	}
	return v1alpha1.AppendPage(pages, got)
}

// pageText is the title and body of the executor's page for event on o.
// pages.Build cuts both to the Reminder's limits.
func pageText(w *v1alpha1.ScheduledWork, o *v1alpha1.ScheduledWorkOccurrence, event string) (title, body string) {
	head := map[string]string{
		v1alpha1.PageEventDue:     "Scheduled work started",
		v1alpha1.PageEventParked:  "Scheduled work parked",
		v1alpha1.PageEventSkipped: "Scheduled work skipped",
		v1alpha1.PageEventFailed:  "Scheduled work failed",
	}[event]
	if o.State == v1alpha1.OccurrenceAmbiguous {
		head = "Scheduled work ambiguous"
	}
	lines := []string{fmt.Sprintf("%s, due %s, attempt %d.", w.Name, o.At.UTC().Format("2006-01-02 15:04 UTC"), o.Attempt)}
	state := o.State
	if o.Reason != "" {
		state += ": " + o.Reason
	}
	lines = append(lines, state+".")
	if o.Session != "" {
		lines = append(lines, "Session "+o.Session+".")
	}
	if event == v1alpha1.PageEventParked {
		lines = append(lines, "It waits for a human to approve, retry or cancel it.")
	}
	return head + ": " + w.Spec.Title, strings.Join(lines, "\n")
}
