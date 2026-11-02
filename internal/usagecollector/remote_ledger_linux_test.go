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
	return RemoteRequest{Version: 1, Operation: "capture", RootName: "fixture-owner", SessionUID: "11111111-1111-1111-1111-111111111111", PodUID: "22222222-2222-2222-2222-222222222222", Fingerprint: strings.Repeat("a", 64), Cycle: 1, Nonce: "33333333-3333-3333-3333-333333333333", HelperSHA: strings.Repeat("b", 64), ValidUntil: time.Now().Add(time.Hour).UTC().Format(time.RFC3339), Providers: []ProviderBinding{{Provider: "openai", AccountRef: "openai-email-sha256:" + strings.Repeat("a", 64), SourceVersion: "codex-app-server-sanitized-2026.11.02"}, {Provider: "anthropic", AccountRef: "anthropic-email-sha256:" + strings.Repeat("a", 64), SourceVersion: "claude-sdk-usage-report-2026.11.02"}, {Provider: "cursor", AccountRef: "cursor-email-sha256:" + strings.Repeat("a", 64), SourceVersion: "cursor-cli-usage-sanitized-2026.11.02"}}}
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
