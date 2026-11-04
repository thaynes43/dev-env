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
	"sigs.k8s.io/controller-runtime/pkg/client"

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

// claudeShutdownClient starts a shutdown at one journal call: the numbered Get
// fails, or the numbered Patch commits and then loses its acknowledgement.
type claudeShutdownClient struct {
	client.Client
	name           string
	cancel         func()
	getAt, patchAt int
	gets, patches  int
}

func (c *claudeShutdownClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if key.Name == c.name {
		c.gets++
		if c.gets == c.getAt {
			c.cancel()
			return errors.New("synthetic get failure")
		}
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

func (c *claudeShutdownClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	if err := c.Client.Patch(ctx, obj, patch, opts...); err != nil || obj.GetName() != c.name {
		return err
	}
	c.patches++
	if c.patches == c.patchAt {
		c.cancel()
		return errors.New("synthetic lost acknowledgement")
	}
	return nil
}

// A shutdown that stops an attempt before its request is dispatched must still
// return the intent to Ready: nothing was sent, so a successor must not mark
// NeedsLogin. Each case cancels the worker's context at one proven-unsent exit.
func TestClaudeShutdownBeforeDispatchRecordsTheRollback(t *testing.T) {
	for _, tc := range []struct {
		name           string
		getAt, patchAt int // journal calls counted from the tick's first load
		fenceAt        int // fence calls counted from the tick
	}{
		{name: "intent save loses its acknowledgement", patchAt: 1},
		{name: "intent re-read fails", getAt: 3},
		{name: "pre-dispatch fence fails", fenceAt: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newClaudeFix(t)
			before := syntheticClaudeRecord(1)
			f.seed(t, before)
			f.clk.Step(6 * time.Hour)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f.w.Journal = &claudeJournal{Client: &claudeShutdownClient{Client: f.c, name: f.w.Journal.Secret.Name, cancel: cancel, getAt: tc.getAt, patchAt: tc.patchAt},
				Secret: f.w.Journal.Secret}
			// The real Lease fence, which also refuses a cancelled context.
			f.holdLease(t, "keeper-a")
			lease, fences := f.leaseFence(t, "keeper-a"), 0
			f.w.Fence = func(c context.Context, budget time.Duration) error {
				fences++
				if fences == tc.fenceAt {
					cancel()
					return errors.New("synthetic shutdown")
				}
				return lease(c, budget)
			}
			f.w.tick(ctx)
			r := f.journal(t)
			if f.tr.Calls != 0 || r.Stage != claudeReady || r.AttemptUID != "" || r.Refresh.Reveal() != before.Refresh.Reveal() || r.Access.Generation != 1 {
				t.Fatalf("proven-unsent attempt was not rolled back: calls=%d stage=%s", f.tr.Calls, r.Stage)
			}
			successor := f.successorWorker()
			successor.Journal = &claudeJournal{Client: f.c, Secret: f.w.Journal.Secret}
			successor.tick(context.Background())
			if r := f.journal(t); r.Stage != claudeReady {
				t.Fatalf("successor marked an unsent attempt: stage=%s", r.Stage)
			}
		})
	}
}

// A proven-unsent attempt whose rollback write fails keeps its proof in this
// process: the next tick retries the fenced rollback of that exact attempt and
// sends nothing, and the unsent-retry backoff then applies as usual.
func TestClaudeUnsentRollbackWriteFailureKeepsTheProof(t *testing.T) {
	f := newClaudeFix(t)
	hub := NewLoginHub(ProviderClaude)
	f.w.Obs = hub
	before := syntheticClaudeRecord(1)
	f.seed(t, before)
	f.clk.Step(6 * time.Hour)
	f.tr.Run = func(call int, r secretValue) (claudeTokens, error) {
		if call == 1 {
			f.c.Fail.Store(true) // the rollback write fails once
			return claudeTokens{}, errClaudeNotSent
		}
		return rotated(call, r)
	}
	f.tick()
	f.c.Fail.Store(false)
	if r := f.journal(t); f.tr.Calls != 1 || r.Stage != claudeIntent {
		t.Fatalf("setup: calls=%d stage=%s", f.tr.Calls, r.Stage)
	}
	f.tick()
	r := f.journal(t)
	if f.tr.Calls != 1 || r.Stage != claudeReady || r.AttemptUID != "" || r.Refresh.Reveal() != before.Refresh.Reveal() || r.Access.Generation != 1 {
		t.Fatalf("proven-unsent attempt was not rolled back on the next tick: calls=%d stage=%s", f.tr.Calls, r.Stage)
	}
	f.tick()
	if f.tr.Calls != 1 {
		t.Fatal("rolled-back attempt retried without the unsent backoff")
	}
	f.clk.Step(time.Minute)
	f.tick()
	if r := f.journal(t); f.tr.Calls != 2 || r.Stage != claudeReady || r.Access.Generation != 2 {
		t.Fatalf("refresh after the backoff failed: calls=%d stage=%s", f.tr.Calls, r.Stage)
	}
	if got := hub.Snapshot()[ProviderClaude].State; got != LoginReady {
		t.Fatalf("hub shows %s, want Ready", got)
	}
}

// The rollback write commits but its acknowledgement is lost. The next tick
// reads the journal back as Ready, drops the proof and never tombstones.
func TestClaudeUnsentRollbackLostAckReadsBackReady(t *testing.T) {
	f := newClaudeFix(t)
	before := syntheticClaudeRecord(1)
	f.seed(t, before)
	f.clk.Step(6 * time.Hour)
	f.w.Journal = &claudeJournal{Client: &claudeLostAckClient{Client: f.c, loseAt: 2}, Secret: f.w.Journal.Secret}
	f.tr.Run = func(int, secretValue) (claudeTokens, error) { return claudeTokens{}, errClaudeNotSent }
	f.tick()
	if r := f.journal(t); r.Stage != claudeReady || f.w.unsent.attempt == "" {
		t.Fatalf("setup: stage=%s proof kept=%v", r.Stage, f.w.unsent.attempt != "")
	}
	f.tick()
	if r := f.journal(t); r.Stage != claudeReady || r.Refresh.Reveal() != before.Refresh.Reveal() || f.w.unsent.attempt != "" {
		t.Fatalf("lost acknowledgement was not read back: stage=%s", r.Stage)
	}
}

// Without the proof the worker stays conservative: a restarted worker, a proof
// that outlived its bound, a recreated journal, or another attempt UID each
// tombstone the intent. A fence that refuses leaves the intent untouched.
func TestClaudeUnsentProofBounds(t *testing.T) {
	for _, tc := range []struct {
		name string
		then func(t *testing.T, f *claudeFix) *claudeWorker
		want string
	}{
		{name: "restart has no proof", want: claudeNeedsLogin, then: func(t *testing.T, f *claudeFix) *claudeWorker { return f.successorWorker() }},
		{name: "proof expired", want: claudeNeedsLogin, then: func(t *testing.T, f *claudeFix) *claudeWorker {
			f.clk.Step(claudeUnsentProofLife)
			return f.w
		}},
		{name: "recreated journal", want: claudeNeedsLogin, then: func(t *testing.T, f *claudeFix) *claudeWorker {
			f.w.unsent.journal = "another-synthetic-incarnation"
			return f.w
		}},
		{name: "another attempt", want: claudeNeedsLogin, then: func(t *testing.T, f *claudeFix) *claudeWorker {
			f.w.unsent.attempt = "another-synthetic-attempt"
			return f.w
		}},
		{name: "fence refuses", want: claudeIntent, then: func(t *testing.T, f *claudeFix) *claudeWorker {
			f.w.Fence = func(context.Context, time.Duration) error { return errors.New("synthetic leadership loss") }
			return f.w
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newClaudeFix(t)
			f.seed(t, syntheticClaudeRecord(1))
			f.clk.Step(6 * time.Hour)
			f.tr.Run = func(int, secretValue) (claudeTokens, error) {
				f.c.Fail.Store(true)
				return claudeTokens{}, errClaudeNotSent
			}
			f.tick()
			f.c.Fail.Store(false)
			if f.journal(t).Stage != claudeIntent || f.w.unsent.attempt == "" {
				t.Fatal("setup: no retained proof")
			}
			tc.then(t, f).tick(context.Background())
			if got := f.journal(t).Stage; got != tc.want || f.tr.Calls != 1 {
				t.Fatalf("stage=%s calls=%d, want %s and no second request", got, f.tr.Calls, tc.want)
			}
		})
	}
}
