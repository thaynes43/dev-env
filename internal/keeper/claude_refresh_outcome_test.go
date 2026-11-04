package keeper

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/go-logr/logr"
)

// claudeSpentClasses are the transport outcomes after dispatch. Each may have
// spent the refresh token, so none may roll back or be sent again.
var claudeSpentClasses = []struct {
	name string
	err  error
}{
	{"dispatch", errClaudeDispatch},
	{"status", errClaudeStatus},
	{"read", errClaudeRead},
	{"parse", errClaudeParse},
	{"wrapped status", fmt.Errorf("synthetic wrapper: %w", errClaudeStatus)},
	{"unclassified", errors.New("synthetic unclassified failure")},
}

// successorWorker is a restarted keeper on the same journal, with a fence that
// always holds.
func (f *claudeFix) successorWorker() *claudeWorker {
	return &claudeWorker{Journal: f.w.Journal, Transport: f.tr, Publisher: f.w.Publisher,
		Fence: func(context.Context, time.Duration) error { return nil }, Clock: f.clk, Log: logr.Discard()}
}

// tombstoned reports whether the journal holds NeedsLogin with no refresh
// credential and no attempt.
func (f *claudeFix) tombstoned(t *testing.T) bool {
	t.Helper()
	r := f.journal(t)
	return r.Stage == claudeNeedsLogin && r.Refresh.Empty() && r.RefreshExpiresAt.IsZero() && r.AttemptUID == ""
}

// Only a failure proven before dispatch rolls back. Every class after dispatch
// tombstones, and neither this worker nor a restarted one sends the possibly
// spent token again, past the unsent-retry backoff and past the due time.
func TestClaudeWorkerNeverRetriesAPossiblySpentRefresh(t *testing.T) {
	for _, tc := range claudeSpentClasses {
		t.Run(tc.name, func(t *testing.T) {
			f := newClaudeFix(t)
			f.seed(t, syntheticClaudeRecord(1))
			f.clk.Step(6 * time.Hour)
			f.tr.Run = func(int, secretValue) (claudeTokens, error) { return claudeTokens{}, tc.err }
			f.tick()
			if f.tr.Calls != 1 || !f.tombstoned(t) {
				t.Fatalf("possibly spent refresh was not tombstoned: calls=%d stage=%s", f.tr.Calls, f.journal(t).Stage)
			}
			f.clk.Step(2 * time.Minute)
			f.tick()
			f.clk.Step(8 * time.Hour)
			f.tick()
			f.successorWorker().tick(context.Background())
			if f.tr.Calls != 1 || !f.tombstoned(t) {
				t.Fatalf("possibly spent refresh was sent again: calls=%d stage=%s", f.tr.Calls, f.journal(t).Stage)
			}
		})
	}
}

// Shutdown while the request is in flight: the worker's context is cancelled
// before the transport returns a failure after dispatch. The tombstone is
// still written at once, not left for a successor to find, and the successor
// sends nothing.
func TestClaudeShutdownAfterDispatchTombstonesAtOnce(t *testing.T) {
	for _, tc := range claudeSpentClasses {
		t.Run(tc.name, func(t *testing.T) {
			f := newClaudeFix(t)
			f.seed(t, syntheticClaudeRecord(1))
			f.clk.Step(6 * time.Hour)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f.tr.Run = func(int, secretValue) (claudeTokens, error) {
				cancel()
				return claudeTokens{}, tc.err
			}
			f.w.tick(ctx)
			if f.tr.Calls != 1 || !f.tombstoned(t) {
				t.Fatalf("shutdown after dispatch left no tombstone: calls=%d stage=%s", f.tr.Calls, f.journal(t).Stage)
			}
			f.clk.Step(8 * time.Hour)
			f.successorWorker().tick(context.Background())
			if f.tr.Calls != 1 || !f.tombstoned(t) {
				t.Fatalf("successor sent the possibly spent token: calls=%d stage=%s", f.tr.Calls, f.journal(t).Stage)
			}
		})
	}
}
