package reminders

import (
	"context"
	"errors"
	"sync"
)

// CalendarEvent is the calendar entry of one reminder series. The whole series
// is one entry with an RRULE; the calendar keeps the recurrence.
type CalendarEvent struct {
	// ID is deterministic (EventID), so Upsert is idempotent.
	ID    string
	Title string
	Body  string
	// Start is the local wall time of the first occurrence, YYYY-MM-DDTHH:MM,
	// read in TimeZone.
	Start    string
	TimeZone string
	// RRule is empty for a one-shot.
	RRule string
}

// CalendarAdapter writes calendar entries for a target reference. The real
// haynesnetwork.com adapter does not exist yet (DESIGN-032, gaps): the
// scheduler runs without one and says so, and the fake stands in for tests.
type CalendarAdapter interface {
	// Upsert creates the entry, or replaces the entry with the same ID. It is
	// safe to call again after any failure.
	Upsert(ctx context.Context, target Target, ev CalendarEvent) error
	// Delete removes the entry; an entry already gone is not an error.
	Delete(ctx context.Context, target Target, eventID string) error
}

// FakeCalendar is an in-memory CalendarAdapter that counts calls.
type FakeCalendar struct {
	mu      sync.Mutex
	Events  map[string]CalendarEvent
	Upserts int
	Deletes int
	// FailUpsert and FailDelete make the next calls fail.
	FailUpsert, FailDelete bool
}

var errFakeCalendar = errors.New("fake calendar failure")

func (f *FakeCalendar) Upsert(_ context.Context, t Target, ev CalendarEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.FailUpsert {
		return errFakeCalendar
	}
	if f.Events == nil {
		f.Events = map[string]CalendarEvent{}
	}
	f.Upserts++
	f.Events[t.Ref+"/"+ev.ID] = ev
	return nil
}

func (f *FakeCalendar) Delete(_ context.Context, t Target, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.FailDelete {
		return errFakeCalendar
	}
	f.Deletes++
	delete(f.Events, t.Ref+"/"+id)
	return nil
}

// Count is how many entries the fake holds.
func (f *FakeCalendar) Count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.Events)
}
