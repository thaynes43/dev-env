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
	"time"

	corev1 "k8s.io/api/core/v1"
	utilexec "k8s.io/client-go/util/exec"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/shelf"
)

// HistoryRelayTimeout bounds the plan, the stream and put pair, and the
// verify: DESIGN-025 6.4's 15 minutes instead of the shelf's minute.
const HistoryRelayTimeout = 15 * time.Minute

// HistoryArchivedBy is what put records as archivedBy in entry.json.
const HistoryArchivedBy = "dev-env-operator"

// historyPlanOutputMax caps what the plan may print: the content manifest's
// bound and the plan's block lists, with room to spare.
const historyPlanOutputMax = 2*protocol.MaxHistoryContentBytes + 1<<20

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
	// Timeout bounds each exec; zero means HistoryRelayTimeout.
	Timeout time.Duration

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

	// The plan, in the hold pod. Its digest is the SHA-256 of the canonical
	// line it printed, which is what stream compares.
	planCmd := append([]string{"agentd", "ctl", "history-export", "plan"}, args.flags()...)
	out, errOut := &capped{max: historyPlanOutputMax}, &capped{max: historySmallOutputMax}
	runErr := e.Exec.Run(ctx, hold.Namespace, hold.Name, ContainerName, planCmd, nil, out, errOut)
	var plan protocol.HistoryExportPlan
	if err := decodeHistoryAnswer(out, runErr, &plan, true); err != nil {
		return res, holdSide(err, errOut)
	}
	line := bytes.TrimSuffix(out.Bytes(), []byte("\n"))
	sum := sha256.Sum256(line)
	planSHA := hex.EncodeToString(sum[:])
	if canonical, err := protocol.CanonicalJSON(plan); err != nil || !bytes.Equal(canonical, line) {
		return res, fmt.Errorf("%w: the plan is not canonical JSON", errHistoryExec)
	}
	data, _, err := content(plan, planSHA)
	if err != nil {
		return res, err
	}

	shelfPod, err := e.shelfPod(ctx)
	if err != nil {
		return res, err
	}
	release, err := e.slot()
	if err != nil {
		return res, err
	}
	defer release()

	// The stream, in the hold pod, piped into put, in the shelf. A pipe holds
	// no bytes of its own: the stream waits for put to read each write.
	pr, pw := io.Pipe()
	streamCmd := append(append([]string{"agentd", "ctl", "history-export", "stream"}, args.flags()...), "--plan-sha256", planSHA)
	streamErrOut := &capped{max: historySmallOutputMax}
	streamDone := make(chan error, 1)
	go func() {
		err := e.Exec.Run(ctx, hold.Namespace, hold.Name, ContainerName, streamCmd, nil, pw, streamErrOut)
		if err != nil {
			_ = pw.CloseWithError(err)
		} else {
			_ = pw.Close()
		}
		streamDone <- err
	}()
	putCmd := []string{"agentd", "ctl", "history-archive", "put", "--archived-by", HistoryArchivedBy}
	stdin := io.MultiReader(bytes.NewReader(append(append([]byte(nil), data...), '\n')), pr)
	putOut, putErrOut := &capped{max: historySmallOutputMax}, &capped{max: historySmallOutputMax}
	putErr := e.Exec.Run(ctx, e.Shelf.Namespace, shelfPod, shelf.Container, putCmd, stdin, putOut, putErrOut)
	// A put that ended early must not leave the stream blocked on the pipe.
	_ = pr.CloseWithError(errors.New("put ended"))
	streamErr := <-streamDone

	// A refusal of the stream (export_changed, say) is the cause: put then
	// saw a short tar and refused it. A stream that failed only because put
	// ended first (io_error) is not: put's own answer is.
	var streamRefusal *protocol.HistoryError
	if streamErr != nil {
		var he protocol.HistoryError
		var exit utilexec.ExitError
		if errors.As(streamErr, &exit) && exit.ExitStatus() == 1 && json.Unmarshal(bytes.TrimSpace(streamErrOut.Bytes()), &he) == nil && he.Code != "" {
			streamRefusal = &he
		}
	}
	if streamRefusal != nil && streamRefusal.Code != protocol.HistoryErrIO {
		return res, streamRefusal
	}
	if err := decodeHistoryAnswer(putOut, putErr, &res, false); err != nil {
		var he *protocol.HistoryError
		switch {
		case errors.As(err, &he):
			return res, he
		case streamRefusal != nil:
			return res, streamRefusal
		case streamErr != nil:
			return res, holdSide(fmt.Errorf("%w: %w", errHistoryExec, streamErr), streamErrOut)
		}
		return res, fmt.Errorf("%w: put in the shelf pod %s: %w: %s", errShelfUnavailable, shelfPod, err, strings.TrimSpace(putErrOut.String()))
	}
	if streamErr != nil {
		// put answered although the stream failed: treat the relay as
		// unconfirmed, and judge it again.
		if streamRefusal != nil {
			return res, streamRefusal
		}
		return res, holdSide(fmt.Errorf("%w: %w", errHistoryExec, streamErr), streamErrOut)
	}
	return res, nil
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
	out, errOut := &capped{max: historySmallOutputMax}, &capped{max: historySmallOutputMax}
	runErr := e.Exec.Run(ctx, e.Shelf.Namespace, shelfPod, shelf.Container, cmd, nil, out, errOut)
	if err := decodeHistoryAnswer(out, runErr, &res, false); err != nil {
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
// failure. strict refuses a field the operator does not know: the plan's
// fields go into content.json, so one the operator would drop must stop it.
func decodeHistoryAnswer(out *capped, runErr error, v any, strict bool) error {
	if out.over {
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
	dec := json.NewDecoder(bytes.NewReader(out.Bytes()))
	if strict {
		dec.DisallowUnknownFields()
	}
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: the answer is not what agentd prints: %w", errHistoryExec, err)
	}
	return nil
}

// holdSide adds the hold pod's stderr to an exec failure there.
func holdSide(err error, stderr *capped) error {
	var he *protocol.HistoryError
	if errors.As(err, &he) {
		return err
	}
	if msg := strings.TrimSpace(stderr.String()); msg != "" {
		return fmt.Errorf("%w: %s", err, msg)
	}
	return err
}

// capped keeps the first max bytes written and notes that more came. It has
// no WriteString or ReadFrom, so io.Copy cannot pass the cap.
type capped struct {
	buf  bytes.Buffer
	max  int
	over bool
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.max - c.buf.Len(); room < len(p) {
		c.over = true
		if room > 0 {
			c.buf.Write(p[:room])
		}
		return len(p), nil
	}
	return c.buf.Write(p)
}

func (c *capped) Bytes() []byte  { return c.buf.Bytes() }
func (c *capped) String() string { return c.buf.String() }
