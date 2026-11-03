//go:build linux

package usagecollector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestAcquisitionDiagnosticsDropUnknownAndPrivateOutput(t *testing.T) {
	var out bytes.Buffer
	emitAcquisitionDiagnostic(&out, "anthropic", atAcquisitionStage("version", unqualifiedVersion("2.1.312 (Claude Code)")))
	good := out.String()
	parsed := DecodeAcquisitionDiagnostics([]byte(good))
	if len(parsed) != 1 || parsed[0].ClientVersion != "2.1.312" || parsed[0].Reason != "version_unqualified" {
		t.Fatal("safe public version diagnostic lost")
	}
	for _, raw := range []string{
		"private-provider-error\n", good + "private-provider-error\n", good[:len(good)-1],
		strings.Replace(good, `"stage":"version"`, `"stage":"private-provider-error"`, 1),
		strings.Replace(good, `"reason":"version_unqualified"`, `"reason":"private-provider-error"`, 1),
		strings.Replace(good, `"provider":"anthropic"`, `"provider":"person@example.invalid"`, 1),
		strings.Replace(good, `"clientVersion":"2.1.312"`, `"clientVersion":"private-provider-error"`, 1),
		strings.Replace(good, `"schema":`, `"Schema":`, 1),
		strings.Replace(good, `"schema":`, `"schema":"duplicate","schema":`, 1),
		strings.Replace(good, `"provider":`, `"extra":"private-provider-error","provider":`, 1),
		strings.Repeat(good, 4), strings.Repeat("x", DiagnosticMaxBytes+1),
	} {
		if got := DecodeAcquisitionDiagnostics([]byte(raw)); got != nil {
			t.Fatal("unknown or private stderr accepted")
		}
	}
	for _, value := range []string{"person@example.invalid", "private-provider-error", "https://example.invalid/secret", "2.1.312\nprivate-provider-error"} {
		out.Reset()
		emitAcquisitionDiagnostic(&out, "anthropic", atAcquisitionStage("version", unqualifiedVersion(value)))
		if strings.Contains(out.String(), value) {
			t.Fatal("unqualified raw version escaped")
		}
	}
	out.Reset()
	emitAcquisitionDiagnostic(&out, "cursor", errors.New("private-provider-error"))
	if out.Len() != 0 {
		t.Fatal("arbitrary error became a diagnostic")
	}
}

func TestAcquisitionDiagnosticReportingKeepsReceiptAndCleanupFences(t *testing.T) {
	request := remoteFixture()
	read := func(_ context.Context, _ ProviderBinding, _ func() time.Time) (RemoteCapture, error) {
		return RemoteCapture{}, atAcquisitionStage("identity_before", refused("metadata_not_json", ErrCapture))
	}
	var out bytes.Buffer
	receipt, err := acquireBatchReporting(context.Background(), request, read, &out)
	if err != nil || receipt.State != "complete" || !receipt.CleanupProven || len(receipt.Attempts) != 3 || len(receipt.Captures) != 0 {
		t.Fatal("failure reporting changed complete receipt", err)
	}
	if len(DecodeAcquisitionDiagnostics(out.Bytes())) != 3 {
		t.Fatal("known provider diagnostics lost")
	}
	for _, attempt := range receipt.Attempts {
		if attempt.Success {
			t.Fatal("diagnostic promoted failed acquisition")
		}
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ParseRemoteReceipt(raw, request); err != nil {
		t.Fatal("existing strict receipt schema changed", err)
	}
	calls := 0
	_, err = acquireBatchReporting(context.Background(), request, func(context.Context, ProviderBinding, func() time.Time) (RemoteCapture, error) {
		calls++
		return RemoteCapture{}, atAcquisitionStage("builtin", ErrCleanup)
	}, &out)
	if !errors.Is(err, ErrCleanup) || calls != 1 {
		t.Fatal("diagnostics bypassed uncertain cleanup fence", err, calls)
	}
}

func TestMetadataDiagnosticsDoNotMistakeMissingIdentityForSignedOut(t *testing.T) {
	for _, test := range []struct {
		raw, reason string
	}{
		{`{}`, "metadata_unqualified"},
		{`{"loggedIn":false}`, "not_logged_in"},
		{"not-json", "metadata_not_json"},
	} {
		_, err := claudeMetadata([]byte(test.raw))
		var detail *diagnosticError
		if !errors.As(err, &detail) || detail.reason != test.reason {
			t.Fatal("metadata reason conflated", test.reason)
		}
	}
}
