package agentd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"reflect"
	"time"

	"github.com/thaynes43/dev-env/internal/providerallowance"
)

type allowanceCaptureDeps struct {
	host              codexHostDeps
	lifecycle         func(Settings) (codexHostLifecycle, error)
	read              func(context.Context, Settings, int, time.Time, CodexAllowanceCapture) (providerallowance.ReadResponse, error)
	nonce             func() (string, error)
	now               func() time.Time
	preparationAbsent func(Settings) bool
	validateConfig    func(context.Context, Settings, CodexHostOptions, codexHostLifecycle) error
}

func realAllowanceCaptureDeps() allowanceCaptureDeps {
	return allowanceCaptureDeps{host: realCodexHostDeps(), lifecycle: hostLifecycle, read: ReadCodexNativeAllowance, now: time.Now, validateConfig: validateAllowanceHostConfig,
		nonce: func() (string, error) {
			var b [16]byte
			if _, err := rand.Read(b[:]); err != nil {
				return "", err
			}
			return hex.EncodeToString(b[:]), nil
		},
		preparationAbsent: func(s Settings) bool {
			_, err := os.Lstat(s.statePath(privateHostPreparationFile))
			return errors.Is(err, os.ErrNotExist)
		},
	}
}

func validateAllowanceHostConfig(ctx context.Context, s Settings, o CodexHostOptions, saved codexHostLifecycle) error {
	if s.PrivateCodexHost {
		input, err := capturePrivateCodexHostInput(ctx, s, o)
		if err != nil || saved.Private == nil || input.binding != *saved.Private {
			return errCodexHostUnknown
		}
		o.privateInput = input
	}
	return codexHostConfigContext(ctx, s, o, false)
}

// CaptureCodexNativeAllowance performs one explicitly enabled read from the
// already confirmed current host. It never starts or adopts a host.
func CaptureCodexNativeAllowance(ctx context.Context, s Settings, o CodexHostOptions) (providerallowance.ReadResponse, error) {
	return captureCodexNativeAllowance(ctx, s, o, realAllowanceCaptureDeps())
}

func captureCodexNativeAllowance(ctx context.Context, s Settings, o CodexHostOptions, d allowanceCaptureDeps) (providerallowance.ReadResponse, error) {
	unavailable := func() (providerallowance.ReadResponse, error) {
		return providerallowance.ReadResponse{}, errCodexHostUnknown
	}
	if !o.Enabled || ctx.Err() != nil || s.PodUID == "" || (!s.PrivateCodexHost && s.WorkspaceID == "") || d.host.preflight(s) != nil {
		return unavailable()
	}
	first, err := d.host.process(s)
	if err != nil || first.State != "Live" {
		return unavailable()
	}
	saved, err := d.lifecycle(s)
	if err != nil || saved.Phase != "Confirmed" || saved.PodUID != s.PodUID || saved.WorkspaceID != s.WorkspaceID || !sameHostProcess(saved.Process, first) || (s.PrivateCodexHost && !d.preparationAbsent(s)) {
		return unavailable()
	}
	if d.validateConfig(ctx, s, o, saved) != nil {
		return unavailable()
	}
	if hostVersion(ctx, s, d.host, s.CodexBin) != nil {
		return unavailable()
	}
	managed, err := codexHostManagedBin(s)
	if err != nil || hostVersion(ctx, s, d.host, managed) != nil {
		return unavailable()
	}
	// Verify identity again after version checks, before opening a socket.
	before, err := d.host.process(s)
	if err != nil || !sameHostProcess(first, before) {
		return unavailable()
	}
	beforeLifecycle, err := d.lifecycle(s)
	if err != nil || !reflect.DeepEqual(saved, beforeLifecycle) || (s.PrivateCodexHost && !d.preparationAbsent(s)) {
		return unavailable()
	}
	id, err := d.nonce()
	if err != nil || !validAllowanceRef(id) {
		return unavailable()
	}
	at := d.now().UTC()
	capture := CodexAllowanceCapture{ObservationID: "capture-" + id, ProducerRef: "agentd-on-demand", ProducerIncarnation: "capture-" + id, Sequence: 1, EvidenceRef: "capture-" + id, SnapshotRevision: "capture-" + id}
	if !capture.valid() || at.IsZero() {
		return unavailable()
	}
	result, err := d.read(ctx, s, first.PID, at, capture)
	if err != nil {
		return unavailable()
	}
	final, err := d.host.process(s)
	if err != nil || !sameHostProcess(first, final) {
		return unavailable()
	}
	after, err := d.lifecycle(s)
	if err != nil || !reflect.DeepEqual(saved, after) || !sameHostProcess(after.Process, final) || (s.PrivateCodexHost && !d.preparationAbsent(s)) {
		return unavailable()
	}
	if d.validateConfig(ctx, s, o, after) != nil {
		return unavailable()
	}
	last, err := d.host.process(s)
	if err != nil || !sameHostProcess(first, last) {
		return unavailable()
	}
	lastLifecycle, err := d.lifecycle(s)
	if err != nil || !reflect.DeepEqual(saved, lastLifecycle) || (s.PrivateCodexHost && !d.preparationAbsent(s)) {
		return unavailable()
	}
	return result, nil
}
