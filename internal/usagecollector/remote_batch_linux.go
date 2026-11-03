//go:build linux

package usagecollector

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"time"
)

const codexExecutable = "/usr/local/bin/codex"
const claudeExecutable = "/usr/local/bin/claude"
const cursorExecutable = "/home/dev/.local/bin/cursor-agent"

// acquireBatch has no arbitrary executable/argument hooks. Every selected source
// uses the existing normal client and independently brackets its account.
func acquireBatch(ctx context.Context, request RemoteRequest) (RemoteReceipt, error) {
	return acquireBatchReporting(ctx, request, acquireOne, os.Stderr)
}

func acquireBatchUsing(ctx context.Context, request RemoteRequest, read func(context.Context, ProviderBinding, func() time.Time) (RemoteCapture, error)) (RemoteReceipt, error) {
	return acquireBatchReporting(ctx, request, read, io.Discard)
}

func acquireBatchReporting(ctx context.Context, request RemoteRequest, read func(context.Context, ProviderBinding, func() time.Time) (RemoteCapture, error), diagnostics io.Writer) (RemoteReceipt, error) {
	until, err := time.Parse(time.RFC3339Nano, request.ValidUntil)
	if err != nil {
		return RemoteReceipt{}, ErrCapture
	}
	ctx, cancel := context.WithDeadline(ctx, until)
	defer cancel()
	receipt := receiptFor(request, "complete", true)
	for _, binding := range request.Providers {
		started := time.Now().UTC()
		var capture RemoteCapture
		err := ErrCapture
		if started.Before(until) {
			capture, err = read(ctx, binding, time.Now)
		}
		if err != nil {
			emitAcquisitionDiagnostic(diagnostics, binding.Provider, err)
		}
		if errors.Is(err, ErrCleanup) {
			return RemoteReceipt{}, ErrCleanup
		}
		completed := time.Now().UTC()
		success := err == nil && ctx.Err() == nil
		receipt.Attempts = append(receipt.Attempts, RemoteAttempt{Provider: binding.Provider, StartedAt: started, CompletedAt: completed, Success: success})
		if success {
			receipt.Captures = append(receipt.Captures, capture)
			receipt.Bindings = append(receipt.Bindings, binding)
		}
	}
	return receipt, nil
}

func acquireOne(ctx context.Context, binding ProviderBinding, now func() time.Time) (RemoteCapture, error) {
	if ctx.Err() != nil {
		return RemoteCapture{}, ErrCapture
	}
	var payload any
	var captured, received string
	switch binding.Provider {
	case "openai":
		version, err := runMetadata(ctx, codexExecutable, []string{"--version"}, false)
		if err != nil {
			return RemoteCapture{}, atAcquisitionStage("version", err)
		}
		if strings.TrimSpace(string(version)) != "codex-cli 0.160.1" {
			return RemoteCapture{}, atAcquisitionStage("version", unqualifiedVersion(string(version)))
		}
		report, err := CollectCodex(ctx, codexExecutable, now)
		if err != nil {
			return RemoteCapture{}, atAcquisitionStage("builtin", err)
		}
		if report.AccountRef != binding.AccountRef || binding.WorkspaceRef != nil {
			return RemoteCapture{}, atAcquisitionStage("binding_after", ErrCapture)
		}
		payload, captured, received = report, report.ReceivedAt, report.ReceivedAt
	case "anthropic":
		version, err := runMetadata(ctx, claudeExecutable, []string{"--version"}, true)
		if err != nil {
			return RemoteCapture{}, atAcquisitionStage("version", err)
		}
		if strings.TrimSpace(string(version)) != "2.1.296 (Claude Code)" {
			return RemoteCapture{}, atAcquisitionStage("version", unqualifiedVersion(string(version)))
		}
		before, err := readMetadata(ctx, binding.Provider)
		if err != nil {
			return RemoteCapture{}, atAcquisitionStage("identity_before", err)
		}
		if !before.matches(binding) {
			return RemoteCapture{}, atAcquisitionStage("binding_before", ErrCapture)
		}
		report, err := collectClaudeBuiltin(ctx, claudeExecutable, now)
		if err != nil {
			return RemoteCapture{}, atAcquisitionStage("builtin", err)
		}
		after, err := readMetadata(ctx, binding.Provider)
		if err != nil {
			return RemoteCapture{}, atAcquisitionStage("identity_after", err)
		}
		if before != after || !after.matches(binding) {
			return RemoteCapture{}, atAcquisitionStage("binding_after", ErrCapture)
		}
		payload, captured, received = report, report.ReceivedAt, report.ReceivedAt
	case "cursor":
		before, err := readMetadata(ctx, binding.Provider)
		if err != nil {
			return RemoteCapture{}, atAcquisitionStage("identity_before", err)
		}
		if !before.matches(binding) {
			return RemoteCapture{}, atAcquisitionStage("binding_before", ErrCapture)
		}
		workspace, err := os.MkdirTemp("/tmp", "dev-env-allowance-cursor-")
		if err != nil {
			return RemoteCapture{}, atAcquisitionStage("workspace", ErrCapture)
		}
		defer func() { _ = os.RemoveAll(workspace) }()
		report, err := CollectCursor(ctx, CursorOptions{Executable: cursorExecutable, Workspace: workspace, AllowWorkspaceTrust: true}, now)
		if err != nil {
			return RemoteCapture{}, atAcquisitionStage("builtin", err)
		}
		after, err := readMetadata(ctx, binding.Provider)
		if err != nil {
			return RemoteCapture{}, atAcquisitionStage("identity_after", err)
		}
		if before != after || !after.matches(binding) || report.Plan != before.plan {
			return RemoteCapture{}, atAcquisitionStage("binding_after", ErrCapture)
		}
		ref := before.account
		report.AccountRef = &ref
		payload, captured, received = report, report.CollectionStartedAt, report.ReceivedAt
	default:
		return RemoteCapture{}, ErrCapture
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return RemoteCapture{}, ErrCapture
	}
	return RemoteCapture{Provider: binding.Provider, CapturedAt: captured, ReceivedAt: received, Payload: raw}, nil
}

func readMetadata(ctx context.Context, provider string) (accountMetadata, error) {
	var raw []byte
	var err error
	switch provider {
	case "cursor":
		raw, err = runMetadata(ctx, cursorExecutable, []string{"about", "--format", "json"}, false)
	case "anthropic":
		raw, err = runMetadata(ctx, claudeExecutable, []string{"auth", "status"}, true)
	default:
		return accountMetadata{}, ErrCapture
	}
	if err != nil {
		return accountMetadata{}, err
	}
	if provider == "cursor" {
		return cursorMetadata(raw)
	}
	return claudeMetadata(raw)
}
