//go:build linux

package usagecollector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

// RunRemote serializes fixed collection and reconciliation in an existing
// private runtime directory. A status-only miss durably fences its nonce before
// returning never_started, so a delayed old exec cannot acquire afterwards.
// The receipt is committed before stdout; stream loss cannot erase completion.
func RunRemote(ctx context.Context, request RemoteRequest, dir string, acquire func(context.Context, RemoteRequest) (RemoteReceipt, error)) (RemoteReceipt, error) {
	if ctx == nil || ctx.Err() != nil || !filepath.IsAbs(dir) || acquire == nil {
		return RemoteReceipt{}, ErrCapture
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return RemoteReceipt{}, ErrCapture
	}
	if _, err = ParseRemoteRequest(encoded, time.Now()); err != nil {
		return RemoteReceipt{}, ErrCapture
	}
	if err = os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return RemoteReceipt{}, ErrCapture
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 || !owned(info) {
		return RemoteReceipt{}, ErrCapture
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil || resolved != dir {
		return RemoteReceipt{}, ErrCapture
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return RemoteReceipt{}, ErrCapture
	}
	defer func() { _ = root.Close() }()
	lock, err := root.OpenFile("lock", os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return RemoteReceipt{}, ErrCapture
	}
	defer func() { _ = lock.Close() }()
	if !privateFile(lock) {
		return RemoteReceipt{}, ErrCapture
	}
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return RemoteReceipt{Version: RemoteSchemaVersion, State: "busy"}, nil
		}
		return RemoteReceipt{}, ErrCapture
	}
	defer func() { _ = unix.Flock(int(lock.Fd()), unix.LOCK_UN) }()
	current, exists, err := readRemoteLedger(root, request)
	if err != nil {
		return RemoteReceipt{}, ErrCapture
	}
	if exists {
		if current.RootName != request.RootName || current.SessionUID != request.SessionUID || current.PodUID != request.PodUID || current.Fingerprint != request.Fingerprint {
			return RemoteReceipt{}, ErrCapture
		}
		if current.Cycle == request.Cycle {
			if current.Nonce != request.Nonce || current.HelperSHA != request.IntentHelperSHA {
				return RemoteReceipt{}, ErrCapture
			}
			return current, nil
		}
		if current.Cycle >= request.Cycle || !current.CleanupProven || current.State != "complete" && current.State != "never_started" {
			return RemoteReceipt{}, ErrCapture
		}
	}
	receipt := receiptFor(request, "uncertain", false)
	if request.Operation == "status" {
		receipt.State, receipt.CleanupProven = "never_started", true
	}
	if writeRemoteLedger(root, receipt) != nil {
		return RemoteReceipt{}, ErrCapture
	}
	if request.Operation == "status" {
		return receipt, nil
	}
	completed, err := acquire(ctx, request)
	if errors.Is(err, ErrCleanup) {
		return receipt, nil
	}
	if err != nil {
		return RemoteReceipt{}, ErrCapture
	}
	raw, err := json.Marshal(completed)
	if err != nil {
		return RemoteReceipt{}, ErrCapture
	}
	if _, err = ParseRemoteReceipt(raw, request); err != nil || completed.State != "complete" {
		return RemoteReceipt{}, ErrCapture
	}
	if writeRemoteLedger(root, completed) != nil {
		return RemoteReceipt{}, ErrCapture
	}
	return completed, nil
}

func receiptFor(request RemoteRequest, state string, clean bool) RemoteReceipt {
	return RemoteReceipt{Version: RemoteSchemaVersion, State: state, RootName: request.RootName, SessionUID: request.SessionUID, PodUID: request.PodUID, Fingerprint: request.Fingerprint, Cycle: request.Cycle, Nonce: request.Nonce, HelperSHA: request.IntentHelperSHA, CleanupProven: clean, Captures: []RemoteCapture{}, Attempts: []RemoteAttempt{}, Bindings: []ProviderBinding{}}
}

func readRemoteLedger(root *os.Root, request RemoteRequest) (RemoteReceipt, bool, error) {
	f, err := root.OpenFile("receipt.json", os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return RemoteReceipt{}, false, nil
	}
	if err != nil {
		return RemoteReceipt{}, false, ErrCapture
	}
	defer func() { _ = f.Close() }()
	if !privateFile(f) {
		return RemoteReceipt{}, false, ErrCapture
	}
	raw, err := io.ReadAll(io.LimitReader(f, RemoteMaxBytes+1))
	if err != nil || len(raw) > RemoteMaxBytes {
		return RemoteReceipt{}, false, ErrCapture
	}
	// Decode only the stored intent coordinates, including its original helper,
	// to select the closed parser. Identity and advancement are checked by the
	// caller under the lock. All other
	// fields, including unknown/duplicate keys, are validated by that parser.
	var coordinates struct {
		Cycle     int64  `json:"cycle"`
		Nonce     string `json:"nonce"`
		HelperSHA string `json:"helperSHA256"`
	}
	if json.Unmarshal(raw, &coordinates) != nil {
		return RemoteReceipt{}, false, ErrCapture
	}
	request.Cycle, request.Nonce, request.IntentHelperSHA = coordinates.Cycle, coordinates.Nonce, coordinates.HelperSHA
	if request.Cycle < 1 || !remoteUUID.MatchString(request.Nonce) || !remoteHash.MatchString(request.IntentHelperSHA) {
		return RemoteReceipt{}, false, ErrCapture
	}
	held, err := ParseRemoteReceipt(raw, request)
	if err != nil || held.State == "busy" {
		return RemoteReceipt{}, false, ErrCapture
	}
	return held, true, nil
}

func writeRemoteLedger(root *os.Root, receipt RemoteReceipt) error {
	raw, err := json.Marshal(receipt)
	if err != nil || len(raw) > RemoteMaxBytes {
		return ErrCapture
	}
	// A leftover staging file is evidence of interrupted persistence. Never
	// truncate or replace it on the assumption that the old operation stopped.
	f, err := root.OpenFile("receipt.next", os.O_CREATE|os.O_EXCL|os.O_WRONLY|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return ErrCapture
	}
	n, writeErr := io.Copy(f, bytes.NewReader(raw))
	syncErr, closeErr := f.Sync(), f.Close()
	if writeErr != nil || n != int64(len(raw)) || syncErr != nil || closeErr != nil {
		return ErrCapture
	}
	if root.Rename("receipt.next", "receipt.json") != nil {
		return ErrCapture
	}
	d, err := root.Open(".")
	if err != nil {
		return ErrCapture
	}
	err = d.Sync()
	_ = d.Close()
	if err != nil {
		return ErrCapture
	}
	return nil
}
