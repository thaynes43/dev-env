//go:build linux

package usagecollector

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func remoteFixture() RemoteRequest {
	return RemoteRequest{Version: 1, Operation: "capture", RootName: "fixture-owner", SessionUID: "11111111-1111-1111-1111-111111111111", PodUID: "22222222-2222-2222-2222-222222222222", Fingerprint: strings.Repeat("a", 64), Cycle: 1, Nonce: "33333333-3333-3333-3333-333333333333", HelperSHA: strings.Repeat("b", 64), IntentHelperSHA: strings.Repeat("b", 64), ValidUntil: time.Now().Add(time.Hour).UTC().Format(time.RFC3339), Providers: []ProviderBinding{{Provider: "openai", AccountRef: "openai-email-sha256:" + strings.Repeat("a", 64), SourceVersion: "codex-app-server-sanitized-2026.11.02"}, {Provider: "anthropic", AccountRef: "anthropic-email-sha256:" + strings.Repeat("a", 64), SourceVersion: "claude-sdk-usage-report-2026.11.02"}, {Provider: "cursor", AccountRef: "cursor-email-sha256:" + strings.Repeat("a", 64), SourceVersion: "cursor-cli-usage-sanitized-2026.11.02"}}}
}

func TestRemoteLedgerCompletionPrecedesLostOutputAndNeverReacquires(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runtime")
	request := remoteFixture()
	calls := 0
	acquire := func(_ context.Context, r RemoteRequest) (RemoteReceipt, error) {
		calls++
		receipt := receiptFor(r, "complete", true)
		for _, p := range r.Providers {
			receipt.Attempts = append(receipt.Attempts, RemoteAttempt{Provider: p.Provider, StartedAt: time.Now(), CompletedAt: time.Now()})
		}
		return receipt, nil
	}
	first, err := RunRemote(context.Background(), request, dir, acquire)
	if err != nil || first.State != "complete" || calls != 1 {
		t.Fatal("initial collection", err, calls)
	}
	// Discard output and recreate the caller as if its stream/leader died.
	request.Operation, request.Providers = "status", []ProviderBinding{}
	recovered, err := RunRemote(context.Background(), request, dir, acquire)
	if err != nil || recovered.State != "complete" || calls != 1 {
		t.Fatal("lost result replayed acquisition", err, calls)
	}
	left, _ := json.Marshal(first)
	right, _ := json.Marshal(recovered)
	if string(left) != string(right) {
		t.Fatal("recovery refreshed receipt or observations")
	}
}

func TestRemoteStatusMissFencesDelayedCaptureAndUncertainCleanup(t *testing.T) {
	for _, mode := range []string{"not-started", "cleanup-uncertain", "source-interrupted"} {
		t.Run(mode, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "runtime")
			request := remoteFixture()
			calls := 0
			acquire := func(context.Context, RemoteRequest) (RemoteReceipt, error) {
				calls++
				if mode == "cleanup-uncertain" {
					return RemoteReceipt{}, ErrCleanup
				}
				return RemoteReceipt{}, ErrCapture
			}
			if mode == "not-started" {
				request.Operation, request.Providers = "status", []ProviderBinding{}
			}
			first, err := RunRemote(context.Background(), request, dir, acquire)
			if mode == "source-interrupted" && !errors.Is(err, ErrCapture) {
				t.Fatal("expected interrupted acquisition")
			}
			if mode != "source-interrupted" && err != nil {
				t.Fatal(err)
			}
			if mode == "not-started" && first.State != "never_started" {
				t.Fatal(first.State)
			}
			// An old transport delivery of the identical capture cannot act after a
			// status tombstone. An uncertain marker cannot be cleared by time/nonce.
			request = remoteFixture()
			replayed, err := RunRemote(context.Background(), request, dir, acquire)
			if err != nil || mode == "not-started" && (calls != 0 || replayed.State != "never_started") || mode != "not-started" && (calls != 1 || replayed.State != "uncertain") {
				t.Fatal("unsafe replay", replayed.State, calls, err)
			}
			if mode != "not-started" {
				request.Cycle++
				request.Nonce = "44444444-4444-4444-4444-444444444444"
				if _, err = RunRemote(context.Background(), request, dir, acquire); err == nil || calls != 1 {
					t.Fatal("uncertain cycle cleared")
				}
			}
		})
	}
}

func TestRemoteLedgerConcurrentAndForeignOrInterruptedFileRefuse(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runtime")
	request := remoteFixture()
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		_, err := RunRemote(context.Background(), request, dir, func(context.Context, RemoteRequest) (RemoteReceipt, error) {
			close(entered)
			<-release
			return RemoteReceipt{}, ErrCleanup
		})
		finished <- err
	}()
	<-entered
	busy, err := RunRemote(context.Background(), request, dir, func(context.Context, RemoteRequest) (RemoteReceipt, error) {
		t.Error("second acquisition")
		return RemoteReceipt{}, nil
	})
	close(release)
	if e := <-finished; e != nil {
		t.Fatal(e)
	}
	if err != nil || busy.State != "busy" || busy.SessionUID != "" {
		t.Fatal("lock receipt forged ownership", err)
	}
	request.Fingerprint = strings.Repeat("c", 64)
	if _, err = RunRemote(context.Background(), request, dir, acquireBatch); err == nil {
		t.Fatal("foreign fingerprint adopted")
	}
	dir = filepath.Join(t.TempDir(), "runtime")
	if os.Mkdir(dir, 0700) != nil {
		t.Fatal("mkdir")
	}
	if os.WriteFile(filepath.Join(dir, "receipt.next"), []byte("interrupted"), 0600) != nil {
		t.Fatal("write")
	}
	request = remoteFixture()
	calls := 0
	if _, err = RunRemote(context.Background(), request, dir, func(context.Context, RemoteRequest) (RemoteReceipt, error) { calls++; return RemoteReceipt{}, nil }); err == nil || calls != 0 {
		t.Fatal("interrupted staging ignored")
	}
}

// A new source-matched helper can reconcile an old intent without relabelling
// its receipt. Only a clean terminal receipt permits a later monotonic cycle.
func TestRemoteHelperUpgradePreservesIntentAndCleanupFences(t *testing.T) {
	for _, state := range []string{"complete", "never_started", "uncertain"} {
		t.Run(state, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "runtime")
			old := remoteFixture()
			calls := 0
			acquire := func(_ context.Context, request RemoteRequest) (RemoteReceipt, error) {
				calls++
				if state == "uncertain" {
					return RemoteReceipt{}, ErrCleanup
				}
				receipt := receiptFor(request, "complete", true)
				for _, p := range request.Providers {
					receipt.Attempts = append(receipt.Attempts, RemoteAttempt{Provider: p.Provider, StartedAt: time.Now(), CompletedAt: time.Now()})
				}
				return receipt, nil
			}
			if state == "never_started" {
				old.Operation, old.Providers = "status", []ProviderBinding{}
			}
			first, err := RunRemote(context.Background(), old, dir, acquire)
			if err != nil || first.State != state {
				t.Fatal("initial", first.State, err)
			}
			upgraded := old
			upgraded.Operation, upgraded.Providers = "status", []ProviderBinding{}
			upgraded.HelperSHA = strings.Repeat("c", 64)
			recovered, err := RunRemote(context.Background(), upgraded, dir, acquire)
			if err != nil {
				t.Fatal("new helper cannot reconcile original intent", err)
			}
			before, _ := json.Marshal(first)
			after, _ := json.Marshal(recovered)
			if string(before) != string(after) {
				t.Fatal("upgrade relabelled original receipt")
			}
			if _, err = ParseRemoteReceipt(after, upgraded); err != nil {
				t.Fatal("caller refused exact original helper receipt", err)
			}
			wrong := upgraded
			wrong.IntentHelperSHA = upgraded.HelperSHA
			if _, err = RunRemote(context.Background(), wrong, dir, acquire); err == nil {
				t.Fatal("same cycle accepted wrong intent helper")
			}
			next := remoteFixture()
			next.HelperSHA, next.IntentHelperSHA = upgraded.HelperSHA, upgraded.HelperSHA
			next.Cycle++
			next.Nonce = "44444444-4444-4444-4444-444444444444"
			priorCalls := calls
			_, err = RunRemote(context.Background(), next, dir, acquire)
			if state == "uncertain" {
				if err == nil || calls != priorCalls {
					t.Fatal("upgrade cleared uncertain work")
				}
				return
			}
			if err != nil || calls != priorCalls+1 {
				t.Fatal("clean upgrade cannot advance", err, calls)
			}
			if _, err = RunRemote(context.Background(), old, dir, acquire); err == nil || calls != priorCalls+1 {
				t.Fatal("delayed old helper captured again")
			}
		})
	}
}

func TestUpgradedStatusMissTombstoneFencesOriginalHelper(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runtime")
	old := remoteFixture()
	status := old
	status.Operation, status.Providers = "status", []ProviderBinding{}
	status.HelperSHA = strings.Repeat("c", 64)
	calls := 0
	acquire := func(context.Context, RemoteRequest) (RemoteReceipt, error) {
		calls++
		return RemoteReceipt{}, ErrCleanup
	}
	receipt, err := RunRemote(context.Background(), status, dir, acquire)
	if err != nil || receipt.State != "never_started" || receipt.HelperSHA != old.HelperSHA {
		t.Fatal("status miss lost original intent", err)
	}
	replayed, err := RunRemote(context.Background(), old, dir, acquire)
	if err != nil || replayed.State != "never_started" || calls != 0 {
		t.Fatal("late original helper bypassed tombstone", err, calls)
	}
	bad := remoteFixture()
	bad.IntentHelperSHA = strings.Repeat("d", 64)
	raw, _ := json.Marshal(bad)
	if _, err = ParseRemoteRequest(raw, time.Now()); err == nil {
		t.Fatal("capture may claim a different intent helper")
	}
}
