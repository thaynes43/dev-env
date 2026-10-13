package controller

// The relay of DESIGN-025 6.3 and 6.4: the operator runs the history export
// in the expiry's hold pod and pipes its tar stream straight into
// `agentd ctl history-archive put` in the shelf, then verifies the entry
// there. Nothing is written to the operator's disk or logged: the stream goes
// through an unbuffered pipe, so at most one exec write is in flight.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	corev1 "k8s.io/api/core/v1"
	utilexec "k8s.io/client-go/util/exec"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/shelf"
)

// HistoryRelayTimeout is DESIGN-025 6.4's 15 minutes instead of the shelf's
// minute: one deadline for the plan, the stream and put together, and one of
// its own for the verify.
const HistoryRelayTimeout = 15 * time.Minute

// HistoryArchivedBy is what put records as archivedBy in entry.json.
const HistoryArchivedBy = "dev-env-operator"

// historyPlanOutputMax caps the plan the operator takes. agentd's own bounds
// allow a plan of about 129 MiB (a 64 MiB content manifest and the block
// lists), more than the operator can hold in its 256 MiB limit with the plan,
// its decoded form and content.json at once. TestHistoryRelayMeasure
// (DESIGN-025 5.4) measured a 16 MiB plan (42,000 files) at about 80 MiB of
// live heap and 130 MiB of peak RSS above the operator's own, so the operator
// takes plans up to 16 MiB and refuses a longer one as export_too_large: the
// HOME stays and DevEnvHomeExpiryNeedsOwner names it, instead of an operator
// killed for its memory on every leader that tries the export again.
const historyPlanOutputMax = 16 << 20

// historySmallOutputMax caps put's and verify's answers, and a stream's
// error on stderr.
const historySmallOutputMax = 64 << 10

var (
	// errHistoryRelayBusy: another export runs on this operator, or no shelf
	// exec slot is free. Not a refusal: the step is tried again shortly.
	errHistoryRelayBusy = errors.New("another history export runs on this operator, or no shelf exec slot is free")
	// errShelfUnavailable: no shelf pod runs and is ready, or an exec into it
	// failed (shelf_unavailable).
	errShelfUnavailable = errors.New("the shelf is unavailable")
	// errHistoryExec: an exec into the hold pod failed, or its answer could
	// not be read (Unknown).
	errHistoryExec = errors.New("the history export exec failed")
)

// HistoryExportArgs are `history-export`'s flags (DESIGN-025 7.1).
type HistoryExportArgs struct {
	Session         string
	ClaudeConfigDir string
	CodexHome       string
	// Rescue is <session>/<stamp> of a Verified rescue's bundle directory;
	// empty for CleanAndPushed.
	Rescue string
}

func (a HistoryExportArgs) flags() []string {
	f := []string{"--session", a.Session, "--claude-config-dir", a.ClaudeConfigDir, "--codex-home", a.CodexHome}
	if a.Rescue != "" {
		f = append(f, "--rescue", a.Rescue)
	}
	return f
}

// HistoryArchiveContent builds content.json from the hold pod's plan and its
// digest: the canonical bytes and their SHA-256.
type HistoryArchiveContent func(plan protocol.HistoryExportPlan, planSHA256 string) (content []byte, contentSHA256 string, err error)

// HistoryRelay runs the export in the hold pod and the archive commands in
// the shelf. A refusal from agentd is a *protocol.HistoryError with its code;
// errShelfUnavailable, errHistoryExec and errHistoryRelayBusy are the rest.
type HistoryRelay interface {
	// Archive plans the export in the hold pod, builds content.json with
	// content, and pipes the export's stream into put in the shelf.
	Archive(ctx context.Context, hold *corev1.Pod, args HistoryExportArgs, content HistoryArchiveContent) (protocol.HistoryPutResult, error)
	// Verify re-hashes the entry under the archive's shared .shelf.lock.
	Verify(ctx context.Context, sessionUID, contentSHA256 string) (protocol.HistoryVerifyResult, error)
}

// ExecHistoryRelay is the HistoryRelay that execs through the API server, as
// the rescue does (D-51). One export runs at a time per operator, and each
// shelf exec takes one of the per-replica slots (DESIGN-024 section 5).
type ExecHistoryRelay struct {
	Exec  shelf.Executor
	Shelf *shelf.Shelf
	Slots *shelf.ExecSlots
	// Timeout bounds the plan, the stream and put together, and the verify
	// on its own; zero means HistoryRelayTimeout.
	Timeout time.Duration
	// PlanMax caps the plan this operator takes; zero means
	// historyPlanOutputMax. Tests set it.
	PlanMax int

	export sync.Mutex
}

func (e *ExecHistoryRelay) timeout() time.Duration {
	if e.Timeout > 0 {
		return e.Timeout
	}
	return HistoryRelayTimeout
}

func (e *ExecHistoryRelay) slot() (func(), error) {
	if e.Slots == nil {
		return func() {}, nil
	}
	release, ok := e.Slots.TryTake()
	if !ok {
		return nil, errHistoryRelayBusy
	}
	return release, nil
}

func (e *ExecHistoryRelay) shelfPod(ctx context.Context) (string, error) {
	if e.Exec == nil || e.Shelf == nil || e.Shelf.Reader == nil {
		return "", fmt.Errorf("%w: this operator cannot exec into the shelf", errShelfUnavailable)
	}
	name, err := e.Shelf.Pod(ctx)
	if err != nil {
		return "", fmt.Errorf("%w: %w", errShelfUnavailable, err)
	}
	return name, nil
}

// Archive implements HistoryRelay.
func (e *ExecHistoryRelay) Archive(ctx context.Context, hold *corev1.Pod, args HistoryExportArgs, content HistoryArchiveContent) (protocol.HistoryPutResult, error) {
	var res protocol.HistoryPutResult
	if e.Exec == nil {
		return res, fmt.Errorf("%w: this operator cannot exec into pods", errHistoryExec)
	}
	if !e.export.TryLock() {
		return res, errHistoryRelayBusy
	}
	defer e.export.Unlock()
	ctx, cancel := context.WithTimeout(ctx, e.timeout())
	defer cancel()

	// The shelf and a slot first: a plan hashes the whole history, so it runs
	// only when put can follow it at once.
	shelfPod, err := e.shelfPod(ctx)
	if err != nil {
		return res, err
	}
	release, err := e.slot()
	if err != nil {
		return res, err
	}
	defer release()

	// The plan, in the hold pod, then content.json from it. Nothing of the
	// plan is kept past relayPlan: only content.json goes on to put.
	planSHA, data, err := e.relayPlan(ctx, hold, args, content)
	if err != nil {
		return res, err
	}

	// The stream, in the hold pod, piped into put, in the shelf. A pipe holds
	// no bytes of its own: the stream waits for put to read each write.
	pr, pw := io.Pipe()
	streamCmd := append(append([]string{"agentd", "ctl", "history-export", "stream"}, args.flags()...), "--plan-sha256", planSHA)
	streamErrOut := &cappedBuffer{max: historySmallOutputMax}
	streamDone := make(chan error, 1)
	// streamEnded is set before the pipe closes, so a put that ended on that
	// close always finds it set.
	var streamEnded atomic.Bool
	// The stream has a context of its own: a put that ends first (exists
	// reads none of it) ends the exec, which client-go would otherwise drain
	// to its end.
	streamCtx, cancelStream := context.WithCancel(ctx)
	defer cancelStream()
	go func() {
		err := e.Exec.Run(streamCtx, hold.Namespace, hold.Name, ContainerName, streamCmd, nil, pw, streamErrOut)
		streamEnded.Store(true)
		if err != nil {
			_ = pw.CloseWithError(err)
		} else {
			_ = pw.Close()
		}
		streamDone <- err
	}()
	putCmd := []string{"agentd", "ctl", "history-archive", "put", "--archived-by", HistoryArchivedBy}
	stdin := io.MultiReader(bytes.NewReader(data), strings.NewReader("\n"), pr)
	putOut, putErrOut := &cappedBuffer{max: historySmallOutputMax}, &cappedBuffer{max: historySmallOutputMax}
	putErr := e.Exec.Run(ctx, e.Shelf.Namespace, shelfPod, shelf.Container, putCmd, stdin, putOut, putErrOut)
	// Which side ended first says which failure is the cause: a stream that
	// failed first made put read a short tar; a put that ended first closed
	// the pipe under the stream.
	streamFirst := streamEnded.Load()
	if !streamFirst {
		// A put that ended first: the stream is not needed, and must not stay
		// blocked on the pipe or drain the rest of the history.
		_ = pr.CloseWithError(errors.New("put ended"))
		cancelStream()
	}
	streamErr := <-streamDone

	// A refusal of a stream that ended first (export_changed, say) is the
	// cause: put then saw a short tar and refused it. A stream that ended
	// after put was cut off by it, and put's answer stands.
	var streamRefusal *protocol.HistoryError
	if streamFirst && streamErr != nil {
		var he protocol.HistoryError
		var exit utilexec.ExitError
		if errors.As(streamErr, &exit) && exit.ExitStatus() == 1 && json.Unmarshal(bytes.TrimSpace(streamErrOut.Bytes()), &he) == nil && he.Code != "" {
			streamRefusal = &he
		}
	}
	if streamRefusal != nil && streamRefusal.Code != protocol.HistoryErrIO {
		return res, streamRefusal
	}
	if err := decodeHistoryAnswer(putOut, putErr, &res); err != nil {
		var he *protocol.HistoryError
		switch {
		case streamFirst && streamRefusal != nil:
			// The stream could not read HOME (io_error): Unknown.
			return res, streamRefusal
		case streamFirst && streamErr != nil:
			// The exec into the hold pod failed: Unknown, whatever put
			// made of the short stream.
			return res, holdSide(fmt.Errorf("%w: %w", errHistoryExec, streamErr), streamErrOut)
		case errors.As(err, &he):
			return res, he
		}
		return res, fmt.Errorf("%w: put in the shelf pod %s: %w: %s", errShelfUnavailable, shelfPod, err, strings.TrimSpace(putErrOut.String()))
	}
	// put answered. stored: it checked every byte of the stream against the
	// manifest. exists: the same content.json is there, and put read none of
	// the stream, which then failed on its closed pipe. Either way the
	// stream's own failure, short of a refusal, changes nothing, and verify
	// checks the entry next.
	return res, nil
}

// relayPlan runs the plan in the hold pod and builds content.json from it.
// Its digest is the SHA-256 of the canonical line the plan printed, which is
// what stream compares. A plan longer than historyPlanOutputMax refuses
// export_too_large. The plan's bytes and its decoded form are dropped on
// return, before the stream starts.
func (e *ExecHistoryRelay) relayPlan(ctx context.Context, hold *corev1.Pod, args HistoryExportArgs, content HistoryArchiveContent) (string, []byte, error) {
	planCmd := append([]string{"agentd", "ctl", "history-export", "plan"}, args.flags()...)
	out, errOut := &cappedBuffer{max: e.planMax()}, &cappedBuffer{max: historySmallOutputMax}
	runErr := e.Exec.Run(ctx, hold.Namespace, hold.Name, ContainerName, planCmd, nil, out, errOut)
	if out.err != nil && runErr == nil {
		return "", nil, protocol.HistoryErrorf(protocol.HistoryErrTooLarge, "",
			"the plan is longer than the %d bytes this operator takes (DESIGN-025 5.4)", e.planMax())
	}
	if err := historyExecError(out, runErr); err != nil {
		return "", nil, holdSide(err, errOut)
	}
	line := bytes.TrimSuffix(out.Bytes(), []byte("\n"))
	var plan protocol.HistoryExportPlan
	if err := json.Unmarshal(line, &plan); err != nil {
		return "", nil, fmt.Errorf("%w: the plan is not what agentd prints: %w", errHistoryExec, err)
	}
	// The plan must be the canonical encoding of what this operator decoded:
	// that refuses a field the operator does not know, which it would drop
	// from content.json, as well as any other spelling. CanonicalEqual
	// compares as it encodes, so no second copy of the plan is built.
	if eq, err := protocol.CanonicalEqual(plan, line); err != nil || !eq {
		return "", nil, fmt.Errorf("%w: the plan is not the canonical JSON of a plan this operator knows", errHistoryExec)
	}
	sum := sha256.Sum256(line)
	planSHA := hex.EncodeToString(sum[:])
	// The plan's bytes are dead from here: the collector may take them while
	// content.json is built.
	data, _, err := content(plan, planSHA)
	if err != nil {
		return "", nil, err
	}
	return planSHA, data, nil
}

func (e *ExecHistoryRelay) planMax() int {
	if e.PlanMax > 0 {
		return e.PlanMax
	}
	return historyPlanOutputMax
}

// Verify implements HistoryRelay.
func (e *ExecHistoryRelay) Verify(ctx context.Context, sessionUID, contentSHA256 string) (protocol.HistoryVerifyResult, error) {
	var res protocol.HistoryVerifyResult
	shelfPod, err := e.shelfPod(ctx)
	if err != nil {
		return res, err
	}
	release, err := e.slot()
	if err != nil {
		return res, err
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, e.timeout())
	defer cancel()
	cmd := []string{"agentd", "ctl", "history-archive", "verify", "--session-uid", sessionUID, "--content-sha256", contentSHA256}
	out, errOut := &cappedBuffer{max: historySmallOutputMax}, &cappedBuffer{max: historySmallOutputMax}
	runErr := e.Exec.Run(ctx, e.Shelf.Namespace, shelfPod, shelf.Container, cmd, nil, out, errOut)
	if err := decodeHistoryAnswer(out, runErr, &res); err != nil {
		var he *protocol.HistoryError
		if errors.As(err, &he) {
			return res, he
		}
		return res, fmt.Errorf("%w: verify in the shelf pod %s: %w: %s", errShelfUnavailable, shelfPod, err, strings.TrimSpace(errOut.String()))
	}
	return res, nil
}

// decodeHistoryAnswer reads an agentd history command's answer: on exit 0 the
// result, on exit 1 the one JSON error object. Anything else is an exec
// failure.
func decodeHistoryAnswer(out *cappedBuffer, runErr error, v any) error {
	if err := historyExecError(out, runErr); err != nil {
		return err
	}
	if err := json.Unmarshal(out.Bytes(), v); err != nil {
		return fmt.Errorf("%w: the answer is not what agentd prints: %w", errHistoryExec, err)
	}
	return nil
}

// historyExecError is an agentd history command's failure: its one JSON error
// object on exit 1, or an exec failure, or nil when it answered.
func historyExecError(out *cappedBuffer, runErr error) error {
	if out.err != nil {
		return fmt.Errorf("%w: the answer is longer than %d bytes", errHistoryExec, out.max)
	}
	if runErr != nil {
		var exit utilexec.ExitError
		var he protocol.HistoryError
		if errors.As(runErr, &exit) && exit.ExitStatus() == 1 && json.Unmarshal(bytes.TrimSpace(out.Bytes()), &he) == nil && he.Code != "" {
			return &he
		}
		return fmt.Errorf("%w: %w", errHistoryExec, runErr)
	}
	return nil
}

// holdSide adds the hold pod's stderr to an exec failure there.
func holdSide(err error, stderr *cappedBuffer) error {
	var he *protocol.HistoryError
	if errors.As(err, &he) {
		return err
	}
	if msg := strings.TrimSpace(stderr.String()); msg != "" {
		return fmt.Errorf("%w: %s", err, msg)
	}
	return err
}
