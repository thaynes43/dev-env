package shelf

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	utilexec "k8s.io/client-go/util/exec"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// HistoryReader runs the history archive's reads in the shelf pod
// (DESIGN-025 8): `agentd ctl history-archive list|manifest|read`. Each takes
// the archive's shared .shelf.lock there and checks every hash it reads. The
// caller takes one of the per-replica shelf exec slots (ExecSlots) first.
type HistoryReader struct {
	Shelf *Shelf
}

// List is `history-archive list --project P --limit N [--after C]`.
func (h *HistoryReader) List(ctx context.Context, project string, limit int, after string) (protocol.HistoryList, error) {
	cmd := []string{"agentd", "ctl", "history-archive", "list", "--project", project, "--limit", strconv.Itoa(limit)}
	if after != "" {
		cmd = append(cmd, "--after", after)
	}
	var out protocol.HistoryList
	err := h.run(ctx, cmd, &out)
	return out, err
}

// Manifest is `history-archive manifest --session-uid U --offset N --limit
// M [--content-sha256 H]`: one page of an entry's verified manifest, the
// Session's newest entry unless content names one.
func (h *HistoryReader) Manifest(ctx context.Context, sessionUID, content string, offset, limit int) (protocol.HistoryManifestPage, error) {
	cmd := []string{"agentd", "ctl", "history-archive", "manifest", "--session-uid", sessionUID,
		"--offset", strconv.Itoa(offset), "--limit", strconv.Itoa(limit)}
	if content != "" {
		cmd = append(cmd, "--content-sha256", content)
	}
	var out protocol.HistoryManifestPage
	err := h.run(ctx, cmd, &out)
	return out, err
}

// Read is `history-archive read --session-uid U --content-sha256 H --file F
// --offset N`: at most protocol.MaxHistoryReadBytes of one archived file,
// each block checked against its hash first.
func (h *HistoryReader) Read(ctx context.Context, sessionUID, content, file string, offset int64) (protocol.HistoryReadResult, error) {
	cmd := []string{"agentd", "ctl", "history-archive", "read", "--session-uid", sessionUID, "--content-sha256", content,
		"--file", file, "--offset", strconv.FormatInt(offset, 10)}
	var out protocol.HistoryReadResult
	err := h.run(ctx, cmd, &out)
	return out, err
}

// run execs one read in the shelf. agentd's refusal, its one JSON object on
// exit status 1, comes back as a *protocol.HistoryError; every other failure
// is an error that is no refusal.
func (h *HistoryReader) run(ctx context.Context, cmd []string, out any) error {
	s := h.Shelf
	if s == nil || s.Exec == nil || s.Reader == nil {
		return errors.New("this operator cannot exec into the shelf")
	}
	pod, err := s.Pod(ctx)
	if err != nil {
		return err
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stdout, stderr := &capped{max: maxOutput}, &capped{max: 4 << 10}
	runErr := s.Exec.Run(ctx, s.Namespace, pod, Container, cmd, nil, stdout, stderr)
	name := strings.Join(cmd[2:4], " ")
	if stdout.over {
		return fmt.Errorf("%s in the shelf pod %s printed more than %d bytes", name, pod, maxOutput)
	}
	if runErr != nil {
		var exit utilexec.ExitError
		var he protocol.HistoryError
		if errors.As(runErr, &exit) && exit.ExitStatus() == 1 && json.Unmarshal(stdout.Bytes(), &he) == nil && he.Code != "" {
			return &he
		}
		return fmt.Errorf("%s in the shelf pod %s: %w: %s", name, pod, runErr, strings.TrimSpace(stderr.String()))
	}
	if err := json.Unmarshal(stdout.Bytes(), out); err != nil {
		return fmt.Errorf("%s in the shelf pod %s: the answer is not JSON: %w", name, pod, err)
	}
	return nil
}
