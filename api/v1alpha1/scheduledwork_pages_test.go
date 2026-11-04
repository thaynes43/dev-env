package v1alpha1_test

// AppendPage keeps an occurrence's pages within the schema's eight: it
// replaces a page with the same event and attempt, and when the list is full
// it drops the oldest attempt's entries first, in list order. It never fails
// and never writes into the caller's slice.

import (
	"fmt"
	"slices"
	"testing"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

func page(event string, attempt int32) v1alpha1.ScheduledWorkPage {
	return v1alpha1.ScheduledWorkPage{Event: event, Attempt: attempt, Reminder: fmt.Sprintf("sw-x-%s-%d", event, attempt)}
}

func keys(pages []v1alpha1.ScheduledWorkPage) []string {
	out := make([]string, 0, len(pages))
	for _, p := range pages {
		out = append(out, fmt.Sprintf("%s/%d", p.Event, p.Attempt))
	}
	return out
}

func wantKeys(t *testing.T, got []v1alpha1.ScheduledWorkPage, want ...string) {
	t.Helper()
	if !slices.Equal(keys(got), want) {
		t.Fatalf("pages %v, want %v", keys(got), want)
	}
}

func TestAppendPageAppendsAndReplaces(t *testing.T) {
	got := v1alpha1.AppendPage(nil, page(v1alpha1.PageEventDue, 0))
	wantKeys(t, got, "due/0")
	got = v1alpha1.AppendPage(got, page(v1alpha1.PageEventParked, 0))
	got = v1alpha1.AppendPage(got, page(v1alpha1.PageEventParked, 1))
	wantKeys(t, got, "due/0", "parked/0", "parked/1")

	sent := page(v1alpha1.PageEventParked, 0)
	sent.State = v1alpha1.DeliverySent
	got = v1alpha1.AppendPage(got, sent)
	wantKeys(t, got, "due/0", "parked/0", "parked/1")
	if got[1].State != v1alpha1.DeliverySent {
		t.Errorf("the replaced page's state is %q, want sent", got[1].State)
	}
}

// The full list: a ninth key drops the lowest attempt's first entry, wherever
// it sits, and keeps the order of the rest.
func TestAppendPageDropsTheOldestAttemptFirst(t *testing.T) {
	full := []v1alpha1.ScheduledWorkPage{
		page(v1alpha1.PageEventParked, 1), page(v1alpha1.PageEventDue, 0), page(v1alpha1.PageEventSkipped, 1),
		page(v1alpha1.PageEventParked, 0), page(v1alpha1.PageEventParked, 2), page(v1alpha1.PageEventSkipped, 2),
		page(v1alpha1.PageEventLink, 2), page(v1alpha1.PageEventFailed, 2),
	}
	before := slices.Clone(full)

	got := v1alpha1.AppendPage(full, page(v1alpha1.PageEventParked, 3))
	wantKeys(t, got, "parked/1", "skipped/1", "parked/0", "parked/2", "skipped/2", "link/2", "failed/2", "parked/3")
	if !slices.Equal(full, before) {
		t.Error("AppendPage wrote into the caller's slice")
	}

	got = v1alpha1.AppendPage(got, page(v1alpha1.PageEventSkipped, 3))
	wantKeys(t, got, "parked/1", "skipped/1", "parked/2", "skipped/2", "link/2", "failed/2", "parked/3", "skipped/3")
	got = v1alpha1.AppendPage(got, page(v1alpha1.PageEventResult, 3))
	wantKeys(t, got, "skipped/1", "parked/2", "skipped/2", "link/2", "failed/2", "parked/3", "skipped/3", "result/3")

	// Replacing a page in a full list drops nothing.
	sent := page(v1alpha1.PageEventSkipped, 1)
	sent.State = v1alpha1.DeliverySent
	got = v1alpha1.AppendPage(got, sent)
	wantKeys(t, got, "skipped/1", "parked/2", "skipped/2", "link/2", "failed/2", "parked/3", "skipped/3", "result/3")
	if got[0].State != v1alpha1.DeliverySent {
		t.Errorf("the replaced page's state is %q, want sent", got[0].State)
	}
}

// A late page of an attempt older than every kept one is the oldest entry,
// so it is the one dropped; the call still succeeds.
func TestAppendPageDropsALatePageOfAnOlderAttempt(t *testing.T) {
	var full []v1alpha1.ScheduledWorkPage
	for _, a := range []int32{2, 3} {
		for _, e := range []string{v1alpha1.PageEventDue, v1alpha1.PageEventParked, v1alpha1.PageEventFailed, v1alpha1.PageEventSkipped} {
			full = append(full, page(e, a))
		}
	}
	got := v1alpha1.AppendPage(full, page(v1alpha1.PageEventResult, 1))
	if !slices.Equal(keys(got), keys(full)) {
		t.Fatalf("pages %v, want %v unchanged", keys(got), keys(full))
	}
}

// Every event of every attempt, in order: the list never passes eight and
// ends with the newest eight.
func TestAppendPageKeepsTheNewestAttempts(t *testing.T) {
	events := []string{
		v1alpha1.PageEventDue, v1alpha1.PageEventLink, v1alpha1.PageEventParked,
		v1alpha1.PageEventFailed, v1alpha1.PageEventResult, v1alpha1.PageEventSkipped,
	}
	var got []v1alpha1.ScheduledWorkPage
	for a := range int32(v1alpha1.ScheduledWorkMaxAttempt + 1) {
		for _, e := range events {
			got = v1alpha1.AppendPage(got, page(e, a))
			if len(got) > v1alpha1.ScheduledWorkMaxPages {
				t.Fatalf("%d pages after %s/%d", len(got), e, a)
			}
		}
	}
	wantKeys(t, got, "result/2", "skipped/2", "due/3", "link/3", "parked/3", "failed/3", "result/3", "skipped/3")
}

// A list already over the limit, which the schema would refuse, comes back
// within it.
func TestAppendPageTrimsAnOversizedList(t *testing.T) {
	var over []v1alpha1.ScheduledWorkPage
	for a := range int32(2) {
		for _, e := range []string{v1alpha1.PageEventDue, v1alpha1.PageEventLink, v1alpha1.PageEventParked, v1alpha1.PageEventFailed, v1alpha1.PageEventResult} {
			over = append(over, page(e, a))
		}
	}
	got := v1alpha1.AppendPage(over, page(v1alpha1.PageEventDue, 1))
	wantKeys(t, got, "parked/0", "failed/0", "result/0", "due/1", "link/1", "parked/1", "failed/1", "result/1")
}
