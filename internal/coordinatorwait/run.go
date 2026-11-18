package coordinatorwait

import (
	"context"
	"errors"
	"time"
)

// Note is already addressed and validated by the parent wait-any API adapter.
// Waiting responses carry a cursor only. No note text crosses this boundary.
type Note struct {
	Name, Cursor string
	Resync       bool
}
type Notes func(context.Context, string, time.Duration) (Note, error)
type Runtime struct {
	Clock Clock
	Notes Notes
	Scan  func() (map[string]string, bool, error)
	Stop  func() (bool, error)
	Save  func(*Record) error
}

type noteAnswer struct {
	note Note
	err  error
}

// Run holds at most one parent read. A timer/file result cancels that read and
// leaves its last unconsumed cursor intact, so an arrival in the race is retried
// by the next parent-owned wait, never skipped. There is no source-list loop.
func (rt Runtime) Run(ctx context.Context, r *Record) (*Result, error) {
	waitCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var answers <-chan noteAnswer
	var done <-chan struct{}
	defer func() {
		cancel()
		if done != nil {
			<-done
		}
	}()
	nextScan := rt.Clock.Now()
	nextRead := rt.Clock.Now()
	for {
		now := rt.Clock.Now()
		stop, err := rt.Stop()
		if err != nil {
			return nil, err
		}
		stop = stop || ctx.Err() != nil
		// Stop/budget never starts another read or scan, and wins over pending work.
		if stop || !now.Before(r.Options.BudgetEnd) {
			return rt.finish(r, now, stop)
		}
		if !now.Before(nextScan) {
			files, overflow, err := rt.Scan()
			if err != nil {
				return nil, err
			}
			r.Scan(files, overflow)
			nextScan = now.Add(r.Options.ScanEvery)
			if err := rt.Save(r); err != nil {
				return nil, err
			}
		}
		if result, err := rt.finish(r, now, false); result != nil || err != nil {
			return result, err
		}
		if answers == nil && !now.Before(nextRead) {
			duration := min(15*time.Minute, r.Options.BudgetEnd.Sub(now), r.NextTimer.Sub(now))
			// The API accepts whole seconds only. Local timer/budget enforce the exact
			// boundary even if the final held request extends by a fractional second.
			duration = ((duration + time.Second - 1) / time.Second) * time.Second
			ch := make(chan noteAnswer, 1)
			ended := make(chan struct{})
			answers = ch
			done = ended
			cursor := r.Cursor
			go func() { defer close(ended); n, err := rt.Notes(waitCtx, cursor, duration); ch <- noteAnswer{n, err} }()
		}
		at := minTime(nextScan, r.NextTimer, r.Options.BudgetEnd)
		if answers == nil {
			at = minTime(at, nextRead)
		}
		timer := rt.Clock.NewTimer(max(0, at.Sub(now)))
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C():
		case answer := <-answers:
			timer.Stop()
			<-done
			answers = nil
			done = nil
			if ctx.Err() != nil || !rt.Clock.Now().Before(r.Options.BudgetEnd) {
				continue
			}
			if answer.err != nil {
				return nil, answer.err
			} // no automatic retry on refusal/loss
			if answer.note.Cursor == "" {
				return nil, errors.New("parent wait returned no cursor")
			}
			r.Note(answer.note.Name, answer.note.Cursor, answer.note.Resync)
			if err := rt.Save(r); err != nil {
				return nil, err
			}
			// Even a server that returns immediately cannot make a tight retry loop.
			// Rearming always uses the cursor that read returned.
			nextRead = rt.Clock.Now().Add(r.Options.ScanEvery)
		}
	}
}
func (rt Runtime) finish(r *Record, now time.Time, stop bool) (*Result, error) {
	result := r.Take(now, stop)
	if result == nil {
		return nil, nil
	}
	if err := rt.Save(r); err != nil {
		return nil, err
	}
	return result, nil
}
func minTime(ts ...time.Time) time.Time {
	m := ts[0]
	for _, t := range ts[1:] {
		if t.Before(m) {
			m = t
		}
	}
	return m
}
