package agentd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

const initialUsageDir = "initial-usage"

type initialUsageReceipt struct {
	Source       protocol.UsageSource `json:"source"`
	LaunchSHA256 string               `json:"launchSHA256"`
}

func bindInitialUsage(l *Launch, sess protocol.Session, podUID string) error {
	if sess.Mode != protocol.ModeTask || !protocol.ValidTaskBudgetIdentifier(sess.SessionUID) || !protocol.ValidTaskBudgetIdentifier(podUID) {
		return nil // legacy identity is unavailable, never reconstructed.
	}
	nonce, err := newUUID()
	if err != nil {
		return err
	}
	b, err := json.Marshal(struct {
		Purpose, SessionUID, PodUID, Provider, Model, BootID, Nonce string
		CreatedAt                                                   time.Time
	}{
		"dev-env/initial-usage-attempt/v1", sess.SessionUID, podUID, sess.Agent, sess.Model, l.BootID, nonce, l.CreatedAt})
	if err != nil {
		return err
	}
	h := sha256.Sum256(b)
	l.UsageSource = &protocol.UsageSource{SessionUID: sess.SessionUID, PodUID: podUID, Provider: sess.Agent, RequestedModel: sess.Model, AttemptID: hex.EncodeToString(h[:])}
	l.Provider, l.SessionUID, l.PodUID = sess.Agent, sess.SessionUID, podUID
	return nil
}

func usageReceipt(l Launch) (initialUsageReceipt, error) {
	if l.UsageSource == nil || !l.UsageSource.Valid() || l.TUI || l.Resume || l.SessionUID != l.UsageSource.SessionUID || l.PodUID != l.UsageSource.PodUID || l.Provider != l.UsageSource.Provider {
		return initialUsageReceipt{}, errors.New("initial usage source is unconfirmed")
	}
	// Codex confirms these fields after spawn; they cannot change the original
	// command/source proof. Claude's --session-id remains in the saved argv.
	l.ConversationID, l.NativeThreadConfirmed = "", false
	b, err := json.Marshal(l)
	if err != nil {
		return initialUsageReceipt{}, err
	}
	h := sha256.Sum256(b)
	return initialUsageReceipt{Source: *l.UsageSource, LaunchSHA256: hex.EncodeToString(h[:])}, nil
}

// A new exclusive, private intent precedes execution. An existing or partial
// intent never permits the initial invocation to run a second time.
func beginInitialUsage(l Launch, stateDir string) error {
	if l.UsageSource == nil {
		return nil
	}
	receipt, err := usageReceipt(l)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(filepath.Join(stateDir, resultFile)); !errors.Is(err, os.ErrNotExist) {
		return errors.New("an original task result already exists or is ambiguous")
	}
	dir := filepath.Join(stateDir, initialUsageDir)
	if noSymlinkComponents(stateDir) != nil || os.Mkdir(dir, 0o700) != nil || ownedCaptureDir(dir) != nil {
		return errors.New("original usage intent is unconfirmed")
	}
	return captureJSON(dir, "intent.json", receipt)
}

func confirmedInitialUsage(l Launch, stateDir string) bool {
	want, err := usageReceipt(l)
	if err != nil {
		return false
	}
	raw, err := ownedCaptureRead(filepath.Join(stateDir, initialUsageDir), "intent.json", 8192)
	if err != nil {
		return false
	}
	var saved initialUsageReceipt
	return json.Unmarshal(raw, &saved) == nil && saved == want
}

func retainInitialUsage(l Launch, stateDir string, res streamResult, finished time.Time, partial bool) *protocol.UsageObservation {
	if !confirmedInitialUsage(l, stateDir) {
		return nil
	}
	u := res.Observation
	if !res.Seen || u.Version != 1 {
		u = protocol.UnknownUsage()
	}
	u.Source, u.ID, u.ObservedAt = *l.UsageSource, protocol.UsageObservationID(*l.UsageSource), finished.UTC().Truncate(time.Second)
	if partial || res.IsError || res.UsageConflict {
		u.ResultState = "Partial"
	}
	if res.UsageConflict {
		if captureJSON(filepath.Join(stateDir, initialUsageDir), "conflict.json", struct{ State, Reason, OriginalID string }{protocol.UsageUnknown, "ConflictingResult", u.ID}) != nil {
			return nil
		}
	}
	if u.Validate() != nil || captureJSON(filepath.Join(stateDir, initialUsageDir), "observation.json", u) != nil {
		return nil
	}
	return &u
}

func collectedInitialUsage(l Launch, stateDir string, result *protocol.UsageObservation) *protocol.UsageObservation {
	if result == nil || !confirmedInitialUsage(l, stateDir) {
		return nil
	}
	raw, err := ownedCaptureRead(filepath.Join(stateDir, initialUsageDir), "observation.json", 8192)
	if err != nil {
		return nil
	}
	var saved protocol.UsageObservation
	if json.Unmarshal(raw, &saved) != nil || saved.Validate() != nil || saved.Source != *l.UsageSource || !reflect.DeepEqual(saved, *result) {
		return nil
	}
	return &saved
}
