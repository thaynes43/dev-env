package keeper

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/codexauth"
)

func freshCodexRecord(t *testing.T, w *codexWorker) codexRecord {
	t.Helper()
	return codexRecord{Access: syntheticCodexAccess(t, w.Clock.Now(), 1, "synthetic-account", 10*24*time.Hour), Refresh: newSecretValue("old-refresh-synthetic-canary"), Stage: codexReady}
}

func TestCodexRefreshOncePrivateGenerationAndDuplicateFence(t *testing.T) {
	w, x, p := codexFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, err := w.serveControl(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	old := freshCodexRecord(t, w)
	saveCodexFixture(t, w, old)
	if w.tick(ctx) != nil || x.Calls != 0 || p.Calls != 1 {
		t.Fatal("fresh credential refreshed before its actual due time")
	}
	status, err := CodexControl(ctx, w.LoginDir, "status", "")
	if err != nil || !status.OK || status.Code != "Ready" || status.Generation != 1 {
		t.Fatal("confirmed generation was not available through fixed status")
	}
	n := syntheticCodexAccess(t, w.Clock.Now(), 2, "synthetic-account", 10*24*time.Hour)
	x.Run = func(ctx context.Context, _ secretValue) (secretValue, secretValue, secretValue, error) {
		d, err := w.load(ctx)
		if err != nil || d.Record.Stage != codexIntent || d.Record.Access.Generation != 1 || !d.Record.Access.ExpiresAt.Equal(old.Access.ExpiresAt) {
			t.Error("acceptance request skipped intent or changed expiry")
			return secretValue{}, secretValue{}, secretValue{}, errors.New("synthetic intent was not confirmed")
		}
		return newSecretValue(n.IDToken), newSecretValue(n.AccessToken), newSecretValue("next-refresh-synthetic-canary"), nil
	}
	p.Run = func(codexauth.Access) error {
		d, err := w.load(ctx)
		if err != nil || d.Record.Stage != codexReady || d.Record.Access.Generation != 2 || d.Record.Refresh.Reveal() != "next-refresh-synthetic-canary" {
			t.Error("replacement published before confirmed Ready durability")
			return errors.New("synthetic replacement was not confirmed")
		}
		return nil
	}
	result, err := CodexRefreshOnce(ctx, w.LoginDir, status.Generation)
	if err != nil || !result.OK || result.Code != "Refreshed" || result.Generation != 2 || x.Calls != 1 {
		t.Fatal("one-shot acceptance failed")
	}
	raw, _ := json.Marshal(result)
	if bytes.Contains(raw, []byte("canary")) || bytes.Contains(raw, []byte("account")) {
		t.Fatal("control result exposed credential or account material")
	}
	// Simulate a caller losing the successful response and repeating its input.
	result, err = CodexRefreshOnce(ctx, w.LoginDir, 1)
	if err != nil || result.OK || result.Code != "GenerationMismatch" || x.Calls != 1 || p.Calls != 2 {
		t.Fatal("duplicate control request spent a replacement token")
	}
	w.published = 0
	if w.tick(ctx) != nil || x.Calls != 1 {
		t.Fatal("ordinary leader publication changed refresh scheduling")
	}
	w.mu.Lock()
	result, err = CodexRefreshOnce(ctx, w.LoginDir, 2)
	w.mu.Unlock()
	if err != nil || result.OK || result.Code != "Busy" || x.Calls != 1 {
		t.Fatal("control bypassed the shared serializer")
	}
}

func TestCodexRefreshOnceRefusesUnsafeStateBeforePOST(t *testing.T) {
	for _, kind := range []string{"zero", "mismatch", "login", "recovered-intent", "halted", "needs-login", "expired", "budget", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			w, x, p := codexFixture(t)
			r := freshCodexRecord(t, w)
			expected := uint64(1)
			ctx := context.Background()
			switch kind {
			case "zero":
				expected = 0
			case "mismatch":
				expected = 2
			case "login":
				r.Stage, r.AttemptUID, r.LoginOwner = codexNeedsLogin, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", w.Identity
				r.LoginUntil, r.ResumeOnCancel = w.Clock.Now().Add(time.Minute), true
			case "recovered-intent":
				r.Stage, r.AttemptUID = codexIntent, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
			case "halted":
				w.halted = true
			case "needs-login":
				r.Stage, r.Refresh = codexNeedsLogin, secretValue{}
			case "expired":
				r.Access = syntheticCodexAccess(t, w.Clock.Now().Add(-11*24*time.Hour), 1, "synthetic-account", 10*24*time.Hour)
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			saveCodexFixture(t, w, r)
			if kind == "budget" {
				w.Fence = func(ctx context.Context, budget time.Duration) error {
					if budget != codexRequestBudget {
						t.Fatal("explicit refresh omitted the full remaining leadership budget")
					}
					return errors.New("synthetic lease budget unavailable")
				}
			}
			if result := w.refreshOnce(ctx, expected); result.OK || x.Calls != 0 || p.Calls != 0 {
				t.Fatal("unsafe acceptance request dispatched or published")
			}
		})
	}
}

func TestCodexRefreshOnceAmbiguousCASCannotReplay(t *testing.T) {
	for _, stage := range []string{codexIntent, codexReady} {
		t.Run(stage, func(t *testing.T) {
			w, x, p := codexFixture(t)
			saveCodexFixture(t, w, freshCodexRecord(t, w))
			w.Journal.Client = &codexAmbiguousClient{Client: w.Journal.Client, Stage: stage, BlockTombstone: true}
			n := syntheticCodexAccess(t, w.Clock.Now(), 2, "synthetic-account", 10*24*time.Hour)
			x.Run = func(context.Context, secretValue) (secretValue, secretValue, secretValue, error) {
				return newSecretValue(n.IDToken), newSecretValue(n.AccessToken), newSecretValue("next-refresh-synthetic-canary"), nil
			}
			if result := w.refreshOnce(context.Background(), 1); result.OK || x.Calls != 1 || p.Calls != 0 {
				t.Fatal("ambiguous replacement was accepted or published")
			}
			if result := w.refreshOnce(context.Background(), 1); result.OK || x.Calls != 1 {
				t.Fatal("same leader replayed an ambiguous acceptance request")
			}
			leader := &codexWorker{Journal: w.Journal, Transport: x, Publisher: p, Fence: w.Fence, Clock: w.Clock}
			if result := leader.refreshOnce(context.Background(), 1); result.OK || x.Calls != 1 {
				t.Fatal("new leader replayed an old expected generation")
			}
			if stage == codexIntent {
				if result := leader.refreshOnce(context.Background(), 2); result.OK || x.Calls != 1 || p.Calls != 0 {
					t.Fatal("recovered intent was treated as a fresh acceptance request")
				}
			}
		})
	}
}

func TestCodexRefreshOnceRequesterDisconnectAndLeaderCancellation(t *testing.T) {
	for _, kind := range []string{"caller-disconnect", "leader-loss"} {
		t.Run(kind, func(t *testing.T) {
			w, x, p := codexFixture(t)
			saveCodexFixture(t, w, freshCodexRecord(t, w))
			leaderCtx, leaderCancel := context.WithCancel(context.Background())
			defer leaderCancel()
			s, err := w.serveControl(leaderCtx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			started := make(chan context.Context, 1)
			release := make(chan struct{})
			n := syntheticCodexAccess(t, w.Clock.Now(), 2, "synthetic-account", 10*24*time.Hour)
			x.Run = func(ctx context.Context, _ secretValue) (secretValue, secretValue, secretValue, error) {
				started <- ctx // A rotating POST has been dispatched.
				select {
				case <-release:
					return newSecretValue(n.IDToken), newSecretValue(n.AccessToken), newSecretValue("next-refresh-synthetic-canary"), nil
				case <-ctx.Done():
					return secretValue{}, secretValue{}, secretValue{}, ctx.Err()
				}
			}
			callerCtx, callerCancel := context.WithCancel(context.Background())
			defer callerCancel()
			returned := make(chan error, 1)
			go func() {
				_, err := CodexRefreshOnce(callerCtx, w.LoginDir, 1)
				returned <- err
			}()
			var transaction context.Context
			select {
			case transaction = <-started:
			case <-time.After(time.Second):
				t.Fatal("accepted transaction did not dispatch")
			}
			deadline, ok := transaction.Deadline()
			if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > codexRefreshControlBound {
				t.Fatal("transaction lacks the ratified overall bound")
			}
			if kind == "caller-disconnect" {
				callerCancel()
			} else {
				leaderCancel()
			}
			select {
			case err := <-returned:
				if err == nil {
					t.Fatal("disconnected caller unexpectedly received a success")
				}
			case <-time.After(time.Second):
				t.Fatal("disconnected control request did not return")
			}
			if kind == "caller-disconnect" {
				if transaction.Err() != nil {
					t.Fatal("caller disconnect cancelled the admitted transaction")
				}
				close(release)
			}
			// The shared lock waits for the admitted operation to finish; no polling.
			w.mu.Lock()
			d, err := w.load(context.Background())
			calls, published, halted := x.Calls, p.Calls, w.halted
			w.mu.Unlock()
			if err != nil || calls != 1 {
				t.Fatal("transaction was not durably inspectable")
			}
			if kind == "caller-disconnect" {
				if d.Record.Stage != codexReady || d.Record.Access.Generation != 2 || d.Record.Refresh.Reveal() != "next-refresh-synthetic-canary" || published != 1 || halted {
					t.Fatal("lost caller reply interrupted replacement durability/publication")
				}
				result, err := CodexRefreshOnce(context.Background(), w.LoginDir, 1)
				if err != nil || result.OK || result.Code != "GenerationMismatch" || x.Calls != 1 {
					t.Fatal("lost-reply duplicate dispatched another POST")
				}
			} else {
				if d.Record.Stage != codexIntent || published != 0 || !halted || transaction.Err() == nil {
					t.Fatal("leadership loss failed to cancel and fence the intent")
				}
				newLeader := &codexWorker{Journal: w.Journal, Transport: x, Publisher: p, Fence: w.Fence, Clock: w.Clock}
				if result := newLeader.refreshOnce(context.Background(), 1); result.OK || x.Calls != 1 || p.Calls != 0 {
					t.Fatal("new leader replayed a consumed intent")
				}
			}
		})
	}
}
