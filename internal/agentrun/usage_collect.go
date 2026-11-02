package agentrun

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"path/filepath"
	"time"

	"github.com/thaynes43/dev-env/internal/providerallowance"
	"github.com/thaynes43/dev-env/internal/usagecollector"
)

const usageCollectUsage = "usage collect claude-statusline: expected --state-dir DIR --producer REF --incarnation REF"

func (a *app) usageCollect(ctx context.Context, args []string) error {
	if len(args) > 0 && args[0] == "claude-report" {
		return a.usageCollectClaudeReport(ctx, args[1:])
	}
	if len(args) > 0 && args[0] == "codex" {
		return a.usageCollectCodex(ctx, args[1:])
	}
	if len(args) == 0 || args[0] != "claude-statusline" {
		return usageError(usageCollectUsage)
	}
	fs := flag.NewFlagSet("usage collect claude-statusline", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var dir, producer, incarnation string
	fs.StringVar(&dir, "state-dir", "", "")
	fs.StringVar(&producer, "producer", "", "")
	fs.StringVar(&incarnation, "incarnation", "", "")
	counts := flagCounts(args[1:], "state-dir", "producer", "incarnation")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, err = io.WriteString(a.env.Stdout, usageCollectUsage+"\n")
			return err
		}
		return usageError(usageCollectUsage)
	}
	if fs.NArg() != 0 || dir == "" || producer == "" || incarnation == "" || counts["state-dir"] > 1 || counts["producer"] > 1 || counts["incarnation"] > 1 {
		return usageError(usageCollectUsage)
	}
	fail := func() error { return &exitError{code: ExitFailed, msg: "usage collection unavailable"} }
	if ctx.Err() != nil {
		return fail()
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	raw, err := readCaptureInput(ctx, a, "-")
	if err != nil {
		return fail()
	}
	now := time.Now
	if a.env.Now != nil {
		now = a.env.Now
	}
	if usagecollector.RecordClaude(ctx, dir, producer, incarnation, raw, now()) != nil {
		return fail()
	}
	// No raw callback or private artifact is copied to the UI statusline.
	_, err = io.WriteString(a.env.Stdout, "Claude usage captured\n")
	return err
}

const usageCodexCollectUsage = "usage collect codex: expected -o json --executable /absolute/official/codex --access-only-auth"

func (a *app) usageCollectCodex(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("usage collect codex", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var output, executable string
	var accessOnly bool
	fs.StringVar(&output, "o", "", "")
	fs.StringVar(&executable, "executable", "", "")
	fs.BoolVar(&accessOnly, "access-only-auth", false, "")
	counts := flagCounts(args, "o", "executable", "access-only-auth")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, err = io.WriteString(a.env.Stdout, usageCodexCollectUsage+"\n")
			return err
		}
		return usageError(usageCodexCollectUsage)
	}
	if fs.NArg() != 0 || output != "json" || !filepath.IsAbs(executable) || counts["o"] > 1 || counts["executable"] > 1 || !accessOnly || counts["access-only-auth"] > 1 {
		return usageError(usageCodexCollectUsage)
	}
	fail := func() error { return &exitError{code: ExitFailed, msg: "usage collection unavailable"} }
	if ctx.Err() != nil || a.env.CollectCodex == nil {
		return fail()
	}
	now := time.Now
	if a.env.Now != nil {
		now = a.env.Now
	}
	report, err := a.env.CollectCodex(ctx, executable, now)
	if err != nil {
		return &exitError{code: ExitFailed, msg: "usage collection unavailable (" + usagecollector.FailureCode(err) + ")"}
	}
	body, err := json.Marshal(report)
	if err != nil {
		return fail()
	}
	// Do not print a malformed or a future capture supplied by the transport.
	if _, err = providerallowance.DecodeCodexAccountCapture(body); err != nil {
		return fail()
	}
	at, err := time.Parse(time.RFC3339Nano, report.ReceivedAt)
	if err != nil || at.After(now()) || ctx.Err() != nil {
		return fail()
	}
	body = append(body, '\n')
	n, err := a.env.Stdout.Write(body)
	if err != nil || n != len(body) {
		return fail()
	}
	return nil
}

func (a *app) usageCollectClaudeReport(ctx context.Context, args []string) error {
	const usage = "usage collect claude-report: expected -o json --input FILE|- --received-at RFC3339"
	fs := flag.NewFlagSet("usage collect claude-report", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var output, input, receivedAt string
	fs.StringVar(&output, "o", "", "")
	fs.StringVar(&input, "input", "", "")
	fs.StringVar(&receivedAt, "received-at", "", "")
	counts := flagCounts(args, "o", "input", "received-at")
	if err := fs.Parse(args); err != nil {
		return usageError(usage)
	}
	if fs.NArg() != 0 || output != "json" || input == "" || counts["o"] > 1 || counts["input"] > 1 || counts["received-at"] > 1 || receivedAt == "" {
		return usageError(usage)
	}
	received, parseErr := time.Parse(time.RFC3339Nano, receivedAt)
	if parseErr != nil || received.IsZero() {
		return usageError(usage)
	}
	now := time.Now
	if a.env.Now != nil {
		now = a.env.Now
	}
	if received.After(now()) {
		return usageError(usage)
	}
	fail := func() error { return &exitError{code: ExitFailed, msg: "usage collection unavailable"} }
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	raw, err := readCaptureInput(ctx, a, input)
	if err != nil {
		return fail()
	}
	report, err := providerallowance.SanitizeClaudeUsageReport(raw, received)
	if err != nil {
		return fail()
	}
	body, err := json.Marshal(report)
	if err != nil || ctx.Err() != nil {
		return fail()
	}
	body = append(body, '\n')
	n, err := a.env.Stdout.Write(body)
	if err != nil || n != len(body) {
		return fail()
	}
	return nil
}
