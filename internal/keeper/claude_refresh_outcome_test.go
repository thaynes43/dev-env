package keeper

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-logr/logr"

	"github.com/thaynes43/dev-env/internal/claudelogin"
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

// stageClaudeLogin writes a synthetic staged login for attempt uid and gives
// the worker that attempt, as the login helper would.
func (f *claudeFix) stageClaudeLogin(t *testing.T, uid string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, uid, ".claude"), 0o700); err != nil {
		t.Fatal("synthetic staging setup failed")
	}
	if err := os.WriteFile(filepath.Join(dir, uid, claudelogin.StagedCredentials), loginCredentials(t, nil), 0o600); err != nil {
		t.Fatal("synthetic staging setup failed")
	}
	r := syntheticClaudeRecord(1)
	f.tr.Prof = &claudeProfile{AccountUUID: r.Access.AccountUUID, OrganizationUUID: r.Access.OrganizationUUID}
	f.w.LoginDir = dir
	f.w.attempt = claudeAttempt{uid: uid, until: f.clk.Now().Add(time.Minute)}
}

// An adoption reports under the journal's own incarnation. After the worker
// has observed the tombstone of this journal, the adopted Ready must replace
// it in the hub; an adoption that reported an unverified empty UID would be
// refused there and leave NeedsLogin showing.
func TestClaudeAdoptionReportsTheJournalIncarnation(t *testing.T) {
	f := newClaudeFix(t)
	hub := NewLoginHub(ProviderClaude)
	f.w.Obs = hub
	r := syntheticClaudeRecord(1)
	r.Stage, r.Refresh, r.RefreshExpiresAt = claudeNeedsLogin, secretValue{}, time.Time{}
	f.seed(t, r)
	f.tick()
	if got := hub.Snapshot()[ProviderClaude].State; got != LoginNeedsLogin {
		t.Fatalf("tombstone not observed: %s", got)
	}
	f.stageClaudeLogin(t, claudeUID)
	if result := f.w.adopt(context.Background(), claudeUID); !result.OK || result.Generation != 2 {
		t.Fatal("synthetic staged adoption failed")
	}
	if got := hub.Snapshot()[ProviderClaude].State; got != LoginReady {
		t.Fatalf("adopted Ready not reported: %s", got)
	}
	hub.mu.Lock()
	uid := hub.claudeUID
	hub.mu.Unlock()
	if uid != claudeFixJournalUID {
		t.Fatal("adoption was not reported under the journal's incarnation")
	}
}
