package agentrun

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/providerallowance"
)

const syntheticUsageSourceBody = `{"provider":"codex","observedAt":"2026-01-08T00:00:00Z","usedPercent":40,"remainingPercent":60,"windowDurationMinutes":10080,"resetsAt":"2026-01-15T00:00:00Z","source":"Codex get_usage_limits tool","accountIdentity":"Unavailable","binding":"Unconfigured","retrievedAt":"2026-01-08T00:00:00Z","providerObservedAt":null,"automationCeilingPercent":90,"remainingToCeilingPercentagePoints":50,"bankedResetCredits":"Unknown"}`

func usageSourceArgs(path string) []string {
	return []string{"--codex-tool-report", path, "--incarnation", "synthetic-codex-1", "--generation", "1", "--max-age", "30m", "-o", "json"}
}

func usageSourceNow() time.Time {
	return time.Date(2026, 1, 8, 0, 5, 0, 0, time.UTC)
}

func runUsageSource(t *testing.T, ctx context.Context, stdin io.Reader, now *time.Time, stdout io.Writer, args ...string) error {
	t.Helper()
	var errOut bytes.Buffer
	env := Env{Stdout: stdout, Stderr: &errOut, Stdin: stdin}
	if now != nil {
		at := *now
		env.Now = func() time.Time { return at }
	}
	err := (&app{env: env}).usageSource(ctx, args)
	if errOut.Len() != 0 {
		t.Fatalf("stderr %q", errOut.String())
	}
	return err
}

func TestUsageSourceStdinFileAndNow(t *testing.T) {
	now := usageSourceNow()
	var stdinOut bytes.Buffer
	if err := runUsageSource(t, context.Background(), strings.NewReader(syntheticUsageSourceBody), &now, &stdinOut, usageSourceArgs("-")...); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(file, []byte(syntheticUsageSourceBody), 0o600); err != nil {
		t.Fatal(err)
	}
	var fileOut bytes.Buffer
	if err := runUsageSource(t, context.Background(), failReader{}, &now, &fileOut, usageSourceArgs(file)...); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stdinOut.Bytes(), fileOut.Bytes()) || !strings.HasSuffix(stdinOut.String(), "\n") || strings.Count(stdinOut.String(), "\n") != 1 {
		t.Fatalf("stdout %q", stdinOut.String())
	}
	got, err := providerallowance.DecodeCurrentSource(bytes.TrimSpace(stdinOut.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	var want providerallowance.CurrentSourceReport
	want.Schema = providerallowance.CurrentSourceSchema
	want.PublisherRef = providerallowance.CurrentSourcePublisher
	want.ProducerIncarnation = "synthetic-codex-1"
	want.SourceGeneration = 1
	want.RetrievedAt = "2026-01-08T00:00:00Z"
	want.UsedPercent = "40"
	want.RemainingPercent = "60"
	want.WindowDurationMinutes = 10080
	want.ResetsAt = "2026-01-15T00:00:00Z"
	if got != want || got.ProviderUpdatedAt != nil {
		t.Fatalf("%#v", got)
	}
	for _, leak := range []string{"2026-01-08T00:05:00Z", "automationCeilingPercent", "remainingToCeilingPercentagePoints", "bankedResetCredits", "accountIdentity", "Unavailable", "Unconfigured", "Unknown", "providerObservedAt", "observedAt", "Codex get_usage_limits tool", "SECRET-CANARY", ":90", ":50"} {
		if strings.Contains(stdinOut.String(), leak) {
			t.Fatalf("stdout contains %s: %s", leak, stdinOut.String())
		}
	}
}

func TestUsageSourceRejectsErrorsAndCancel(t *testing.T) {
	now := usageSourceNow()
	secretPath := filepath.Join(t.TempDir(), "SECRET-CANARY.json")
	if err := os.WriteFile(secretPath, []byte(syntheticUsageSourceBody), 0o600); err != nil {
		t.Fatal(err)
	}
	link := secretPath + "-link"
	if err := os.Symlink(secretPath, link); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	usageCases := [][]string{
		{"--help"},
		{"--codex-tool-report", "-", "--incarnation", "synthetic-codex-1", "--generation", "1", "--max-age", "30m"},
		{"--codex-tool-report", "-", "--incarnation", "synthetic-codex-1", "--generation", "1", "--max-age", "bogus", "-o", "json"},
		{"--codex-tool-report", "-", "--incarnation", "synthetic-codex-1", "--generation", "1", "--generation", "2", "--max-age", "30m", "-o", "json"},
		{"--codex-tool-report", "-", "--incarnation", "synthetic-codex-1", "--generation", "nope", "--max-age", "30m", "-o", "json"},
		append(usageSourceArgs("-"), "extra"),
	}
	for _, args := range usageCases {
		out.Reset()
		err := runUsageSource(t, context.Background(), failReader{}, &now, &out, args...)
		var ee *exitError
		if out.Len() != 0 || !errors.As(err, &ee) || ee.code != ExitUsage || ee.msg != usageSourceUsage || strings.Contains(err.Error(), "bogus") || strings.Contains(err.Error(), "nope") || strings.Contains(err.Error(), "SECRET-CANARY") {
			t.Fatalf("%v %q %v", args, out.String(), err)
		}
	}
	inputCases := []struct {
		args  []string
		stdin string
	}{
		{args: usageSourceArgs(link), stdin: ""},
		{args: usageSourceArgs(t.TempDir()), stdin: ""},
		{args: usageSourceArgs(secretPath + "-missing"), stdin: ""},
		{args: usageSourceArgs("-"), stdin: syntheticUsageSourceBody + strings.Repeat(" ", usageSourceMaxBytes)},
		{args: usageSourceArgs("-"), stdin: withUsageSourceField(`"note":"SECRET-CANARY"`)},
		{args: usageSourceArgs("-"), stdin: withUsageSourceField(`"providerObservedAt":"2026-01-09T03:04:05Z"`)},
		{args: []string{"--codex-tool-report", "-", "--incarnation", "synthetic-codex-1", "--generation", "0", "--max-age", "30m", "-o", "json"}, stdin: syntheticUsageSourceBody},
		{args: []string{"--codex-tool-report", "-", "--incarnation", "synthetic-codex-1", "--generation", "1", "--max-age", "25h", "-o", "json"}, stdin: syntheticUsageSourceBody},
		{args: []string{"--codex-tool-report", "-", "--incarnation", "SECRET-CANARY-" + strings.Repeat("a", 300), "--generation", "1", "--max-age", "30m", "-o", "json"}, stdin: syntheticUsageSourceBody},
	}
	for _, tc := range inputCases {
		out.Reset()
		err := runUsageSource(t, context.Background(), strings.NewReader(tc.stdin), &now, &out, tc.args...)
		var ee *exitError
		if out.Len() != 0 || !errors.As(err, &ee) || ee.code != ExitFailed || ee.msg != "usage-source input unavailable" || strings.Contains(err.Error(), "SECRET-CANARY") || strings.Contains(err.Error(), "2026-01-09T03:04:05Z") || strings.Contains(err.Error(), secretPath) {
			t.Fatalf("%v %q %v", tc.args, out.String(), err)
		}
	}
	out.Reset()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := runUsageSource(t, ctx, strings.NewReader(syntheticUsageSourceBody), &now, &out, usageSourceArgs("-")...)
	var ee *exitError
	if out.Len() != 0 || !errors.As(err, &ee) || ee.code != ExitFailed || ee.msg != "usage-source unavailable" || strings.Contains(err.Error(), "Unavailable") || strings.Contains(err.Error(), "40") {
		t.Fatalf("%q %v", out.String(), err)
	}
	err = runUsageSource(t, context.Background(), strings.NewReader(syntheticUsageSourceBody), &now, failWriter{}, usageSourceArgs("-")...)
	if !errors.As(err, &ee) || ee.code != ExitFailed || ee.msg != "usage-source output unavailable" || strings.Contains(err.Error(), "private") {
		t.Fatal(err)
	}
	var stale bytes.Buffer
	if err := runUsageSource(t, context.Background(), strings.NewReader(syntheticUsageSourceBody), nil, &stale, usageSourceArgs("-")...); err == nil || stale.Len() != 0 {
		t.Fatalf("nil clock %q %v", stale.String(), err)
	}
}

func withUsageSourceField(field string) string {
	body := syntheticUsageSourceBody
	return body[:len(body)-1] + "," + field + "}"
}
