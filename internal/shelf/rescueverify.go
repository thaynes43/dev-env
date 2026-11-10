package shelf

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	utilexec "k8s.io/client-go/util/exec"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// ErrSlotsBusy is RescueVerifier's answer when every shelf exec slot of this
// replica is taken. The caller comes back later; nothing queues on the shelf.
var ErrSlotsBusy = errors.New("every shelf exec slot of this operator is taken")

// RescueVerifier runs `agentd ctl rescue-verify` in the shelf for a task
// Session's clean-up (DESIGN-061 section 3). Each call takes one of the
// per-replica ExecSlots first, with TryTake, so the clean-up shares the
// shelf's exec budget with the archive execs.
type RescueVerifier struct {
	Shelf *Shelf
	Slots *ExecSlots
}

// Verify is `rescue-verify` with --id when id is set. A refusal
// (bundle_missing, bundle_mismatch) comes back as a result with OK false and
// a nil error. An error is anything that proves nothing either way: no slot
// (ErrSlotsBusy), no shelf (ErrNoShelf), a failed exec, or an answer that is
// not the document asked for.
func (v *RescueVerifier) Verify(ctx context.Context, id string) (protocol.RescueVerifyResult, error) {
	var res protocol.RescueVerifyResult
	s := v.Shelf
	if s == nil || s.Exec == nil || s.Reader == nil {
		return res, errors.New("this operator cannot exec into the shelf")
	}
	if v.Slots == nil {
		return res, errors.New("rescue-verify runs only under the shelf exec slots")
	}
	if id != "" {
		if _, _, err := protocol.ParseRescueID(id); err != nil {
			return res, err
		}
	}
	release, ok := v.Slots.TryTake()
	if !ok {
		return res, ErrSlotsBusy
	}
	defer release()
	pod, err := s.Pod(ctx)
	if err != nil {
		return res, err
	}
	cmd := []string{"agentd", "ctl", "rescue-verify"}
	if id != "" {
		cmd = append(cmd, "--id", id)
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stdout, stderr := &capped{max: 64 << 10}, &capped{max: 4 << 10}
	runErr := s.Exec.Run(ctx, s.Namespace, pod, Container, cmd, nil, stdout, stderr)
	if stdout.over {
		return res, fmt.Errorf("rescue-verify in the shelf pod %s printed more than %d bytes", pod, stdout.max)
	}
	refusal := false
	if runErr != nil {
		var exit utilexec.ExitError
		if !errors.As(runErr, &exit) || exit.ExitStatus() != 1 || len(stdout.Bytes()) == 0 {
			return res, fmt.Errorf("rescue-verify in the shelf pod %s: %w: %s", pod, runErr, strings.TrimSpace(stderr.String()))
		}
		refusal = true
	}
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		return protocol.RescueVerifyResult{}, fmt.Errorf("rescue-verify in the shelf pod %s: the answer is not JSON: %w", pod, err)
	}
	if err := checkVerifyResult(res, id, refusal); err != nil {
		return protocol.RescueVerifyResult{}, fmt.Errorf("rescue-verify in the shelf pod %s: %w", pod, err)
	}
	return res, nil
}

// checkVerifyResult refuses an answer that is not the one asked for, or that
// contradicts its exit status.
func checkVerifyResult(res protocol.RescueVerifyResult, id string, refusal bool) error {
	switch {
	case res.Version != protocol.RescueVerifyVersion:
		return fmt.Errorf("the answer is version %d, not %d", res.Version, protocol.RescueVerifyVersion)
	case res.ID != id:
		return fmt.Errorf("the answer is for rescue %q, not %q", res.ID, id)
	case res.SharedTotalBytes <= 0 || res.SharedFreeBytes < 0 || res.SharedFreeBytes > res.SharedTotalBytes:
		return errors.New("the answer's shared volume figures contradict each other")
	case refusal && res.OK:
		return errors.New("the answer is ok on exit status 1")
	case !refusal && !res.OK:
		return errors.New("the answer is not ok on exit status 0")
	case res.OK && res.Refusal != "":
		return errors.New("the answer is ok and refused")
	case !res.OK && res.Refusal != protocol.RescueVerifyBundleMissing && res.Refusal != protocol.RescueVerifyBundleMismatch:
		return fmt.Errorf("the answer refuses with %q, which is no refusal of rescue-verify", res.Refusal)
	case res.OK && id != "" && len(res.ManifestSHA256) != 64:
		return errors.New("the answer is ok without the manifest's SHA-256")
	}
	return nil
}
