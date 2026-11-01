package agentrun

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"time"

	"golang.org/x/sys/unix"

	"github.com/thaynes43/dev-env/internal/providerallowance"
)

// errUsageSourceInput is internal. Callers replace it with a fixed message
// that does not include the path or the bytes that were read.
var errUsageSourceInput = errors.New("usage-source input unavailable")

const usageSourceMaxBytes = 16 << 10

const usageSourceUsage = "usage-source: expected -o json --codex-tool-report FILE|- --incarnation ID --generation N --max-age DURATION"

// usageSource reads one caller-supplied Codex tool report and writes the
// sanitized current-source JSON. It does not open an operator connection,
// scan a credential store, or contact a provider. Stdout is only the
// sanitized report. The caller supplies incarnation and generation; the
// command does not prove publisher identity or entitlement.
func (a *app) usageSource(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("usage-source", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var reportPath, incarnation, maxAgeText, output string
	var generation int64
	fs.StringVar(&reportPath, "codex-tool-report", "", "")
	fs.StringVar(&incarnation, "incarnation", "", "")
	fs.Int64Var(&generation, "generation", 0, "")
	fs.StringVar(&maxAgeText, "max-age", "", "")
	fs.StringVar(&output, "o", "", "")
	counts := flagCounts(args, "codex-tool-report", "incarnation", "generation", "max-age", "o")
	if err := fs.Parse(args); err != nil || counts["codex-tool-report"] != 1 || counts["incarnation"] != 1 || counts["generation"] != 1 || counts["max-age"] != 1 || counts["o"] != 1 || output != "json" || reportPath == "" || incarnation == "" || maxAgeText == "" || fs.NArg() != 0 {
		return usageError(usageSourceUsage)
	}
	maxAge, err := time.ParseDuration(maxAgeText)
	if err != nil {
		return usageError(usageSourceUsage)
	}
	if ctx.Err() != nil {
		return &exitError{code: ExitFailed, msg: "usage-source unavailable"}
	}
	raw, err := a.readCodexToolReport(reportPath)
	if err != nil {
		return &exitError{code: ExitFailed, msg: "usage-source input unavailable"}
	}
	if ctx.Err() != nil {
		return &exitError{code: ExitFailed, msg: "usage-source unavailable"}
	}
	now := time.Now
	if a.env.Now != nil {
		now = a.env.Now
	}
	var selected providerallowance.CurrentSourceSelection
	selected.PublisherRef = providerallowance.CurrentSourcePublisher
	selected.ProducerIncarnation = incarnation
	selected.SourceGeneration = generation
	selected.MaxAge = maxAge
	report, err := providerallowance.CodexToolReport(raw, selected, now())
	if err != nil {
		return &exitError{code: ExitFailed, msg: "usage-source input unavailable"}
	}
	body, err := json.Marshal(report)
	if err != nil {
		return &exitError{code: ExitFailed, msg: "usage-source unavailable"}
	}
	if _, err := providerallowance.DecodeCurrentSource(body); err != nil || ctx.Err() != nil {
		return &exitError{code: ExitFailed, msg: "usage-source unavailable"}
	}
	encoded := append(body, '\n')
	if n, err := a.env.Stdout.Write(encoded); err != nil || n != len(encoded) {
		return &exitError{code: ExitFailed, msg: "usage-source output unavailable"}
	}
	return nil
}

func (a *app) readCodexToolReport(path string) ([]byte, error) {
	r := a.env.Stdin
	if path != "-" {
		fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		if err != nil {
			return nil, errUsageSourceInput
		}
		f := os.NewFile(uintptr(fd), "usage-source")
		defer func() { _ = f.Close() }()
		st, err := f.Stat()
		if err != nil || !st.Mode().IsRegular() {
			return nil, errUsageSourceInput
		}
		r = f
	}
	if r == nil {
		return nil, errUsageSourceInput
	}
	raw, err := io.ReadAll(io.LimitReader(r, int64(usageSourceMaxBytes)+1))
	if err != nil || len(raw) == 0 || len(raw) > usageSourceMaxBytes {
		return nil, errUsageSourceInput
	}
	return raw, nil
}
