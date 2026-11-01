package agentrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
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
	want.RemainingPercentBasis = providerallowance.CurrentSourceRemainingBasis
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

		{"--codex-tool-report", "-", "--incarnation", "synthetic-codex-1", "--generation", "1", "--max-age", "30m"},
		{"--codex-tool-report", "-", "--incarnation", "synthetic-codex-1", "--generation", "1", "--max-age", "bogus", "-o", "json"},
		{"--codex-tool-report", "-", "--incarnation", "synthetic-codex-1", "--generation", "1", "--generation", "2", "--max-age", "30m", "-o", "json"},
		{"--codex-tool-report", "-", "--incarnation", "synthetic-codex-1", "--generation", "nope", "--max-age", "30m", "-o", "json"},
		append(usageSourceArgs("-"), "extra"),
		{"--codex-tool-report", "-", "--codex-raw-tool-report", "-", "--observed-at", "2026-01-08T00:00:00Z", "--incarnation", "synthetic-codex-1", "--generation", "1", "--max-age", "30m", "-o", "json"},
		{"--codex-tool-report", "-", "--observed-at", "2026-01-08T00:00:00Z", "--incarnation", "synthetic-codex-1", "--generation", "1", "--max-age", "30m", "-o", "json"},
		{"--codex-raw-tool-report", "-", "--incarnation", "synthetic-codex-1", "--generation", "1", "--max-age", "30m", "-o", "json"},
		{"--codex-raw-tool-report", "-", "--observed-at", "2026-01-08T00:00:00Z", "--observed-at", "2026-01-08T00:01:00Z", "--incarnation", "synthetic-codex-1", "--generation", "1", "--max-age", "30m", "-o", "json"},
		{"--codex-raw-tool-report", "-", "--codex-raw-tool-report", "-", "--observed-at", "2026-01-08T00:00:00Z", "--incarnation", "synthetic-codex-1", "--generation", "1", "--max-age", "30m", "-o", "json"},
		{"--publish", "--publish", "--codex-tool-report", "-", "--incarnation", "synthetic-codex-1", "--generation", "1", "--max-age", "30m", "-o", "json"},
		{"--codex-tool-report", "-", "--incarnation", "synthetic-codex-1", "--generation", "1", "--max-age", "30m", "-o", "json", "--publish=maybe"},
		{"--codex-tool-report", "-", "--incarnation", "synthetic-codex-1", "--generation", "1", "--max-age", "30m", "-o", "json", "--api-url", "https://example.invalid"},
	}
	for _, args := range usageCases {
		out.Reset()
		err := runUsageSource(t, context.Background(), failReader{}, &now, &out, args...)
		var ee *exitError
		if out.Len() != 0 || !errors.As(err, &ee) || ee.code != ExitUsage || ee.msg != usageSourceUsage || strings.Contains(err.Error(), "bogus") || strings.Contains(err.Error(), "nope") || strings.Contains(err.Error(), "SECRET-CANARY") || strings.Contains(err.Error(), "2026-01-08") || strings.Contains(err.Error(), "example.invalid") || strings.Contains(err.Error(), "maybe") {
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

func TestUsageSourceDispatchDerivesAbsentRemaining(t *testing.T) {
	now := usageSourceNow()
	raw := strings.Replace(syntheticUsageSourceBody, `,"remainingPercent":60`, "", 1)
	raw = strings.Replace(raw, "windowDurationMinutes", "windowDurationMins", 1)
	var out, errs bytes.Buffer
	code := Run(context.Background(), append([]string{"usage-source"}, usageSourceArgs("-")...), Env{Stdin: strings.NewReader(raw), Stdout: &out, Stderr: &errs, Now: func() time.Time { return now }})
	if code != 0 || errs.Len() != 0 {
		t.Fatalf("code=%d stderr=%q", code, errs.String())
	}
	report, err := providerallowance.DecodeCurrentSource(bytes.TrimSpace(out.Bytes()))
	if err != nil || report.RemainingPercent != "60" || report.RemainingPercentBasis != providerallowance.CurrentSourceRemainingBasis {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

// UNRUN: nested-report cases below were not executed in this delivery.

const syntheticRawObserved = "2026-06-15T09:04:05-07:00"
const syntheticRawResetUnix = "1782144245"

func syntheticRawNow() time.Time {
	return time.Date(2026, 6, 15, 16, 9, 5, 0, time.UTC)
}

func syntheticRawEnvelope(used, windowMins, resetUnix, balance string) string {
	limit := `{"limitId":"codex","limitName":null,"normalModelSlug":null,"primary":{"usedPercent":` + used + `,"windowDurationMins":` + windowMins + `,"resetsAt":` + resetUnix + `},"secondary":null,"credits":{"hasCredits":true,"unlimited":false,"balance":"` + balance + `"},"individualLimit":null,"spendControlReached":null,"planType":"pro","rateLimitReachedType":null}`
	return `{"rateLimits":` + limit + `,"ordinaryUsageAllowed":null,"rateLimitsByLimitId":{"codex":` + limit + `},"rateLimitResetCredits":null,"accountId":null,"rateLimitUpsell":null}`
}

func syntheticRawBody() string {
	return syntheticRawEnvelope("12", "300", syntheticRawResetUnix, "SECRET-CANARY")
}

func usageSourceRawArgs(path, observed string) []string {
	return []string{"--codex-raw-tool-report", path, "--observed-at", observed, "--incarnation", "synthetic-raw-1", "--generation", "1", "--max-age", "30m", "-o", "json"}
}

func syntheticRawSelection() providerallowance.CurrentSourceSelection {
	return providerallowance.CurrentSourceSelection{
		PublisherRef:        providerallowance.CurrentSourcePublisher,
		ProducerIncarnation: "synthetic-raw-1",
		SourceGeneration:    1,
		MaxAge:              30 * time.Minute,
	}
}

func syntheticRawWant() providerallowance.CurrentSourceReport {
	return providerallowance.CurrentSourceReport{
		Schema:                providerallowance.CurrentSourceSchema,
		PublisherRef:          providerallowance.CurrentSourcePublisher,
		ProducerIncarnation:   "synthetic-raw-1",
		SourceGeneration:      1,
		RetrievedAt:           syntheticRawObserved,
		UsedPercent:           "12",
		RemainingPercent:      "88",
		RemainingPercentBasis: providerallowance.CurrentSourceRemainingBasis,
		WindowDurationMinutes: 300,
		ResetsAt:              time.Unix(1782144245, 0).UTC().Format(time.RFC3339),
	}
}

// UNRUN: Sol runs this. Nested stdin and file input keep the original retrieval
// spelling, derive remaining, and do not turn a 300 minute window into a week.
func TestUsageSourceRawFileStdinAndOriginalTime(t *testing.T) {
	now := syntheticRawNow()
	want := syntheticRawWant()
	canon, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	encoded := append(append([]byte(nil), canon...), '\n')
	var stdinOut bytes.Buffer
	if err := runUsageSource(t, context.Background(), strings.NewReader(syntheticRawBody()), &now, &stdinOut, usageSourceRawArgs("-", syntheticRawObserved)...); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "nested.json")
	if err := os.WriteFile(file, []byte(syntheticRawBody()), 0o600); err != nil {
		t.Fatal(err)
	}
	var fileOut bytes.Buffer
	if err := runUsageSource(t, context.Background(), failReader{}, &now, &fileOut, usageSourceRawArgs(file, syntheticRawObserved)...); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stdinOut.Bytes(), encoded) || !bytes.Equal(fileOut.Bytes(), encoded) {
		t.Fatalf("stdin %s file %s", stdinOut.String(), fileOut.String())
	}
	got, err := providerallowance.DecodeCurrentSource(canon)
	if err != nil {
		t.Fatal(err)
	}
	if err := providerallowance.ValidateCurrentSource(got, syntheticRawSelection(), now); err != nil {
		t.Fatal(err)
	}
	if got != want || got.ProviderUpdatedAt != nil || got.RetrievedAt != syntheticRawObserved || got.WindowDurationMinutes != 300 || got.WindowDurationMinutes == 7*24*60 {
		t.Fatalf("%#v", got)
	}
	// File mtime is the test clock. Retrieval stays the flag, not mtime or reset.
	if strings.Contains(stdinOut.String(), `"retrievedAt":"2026-06-15T16:09:05Z"`) || strings.Contains(stdinOut.String(), `"retrievedAt":"`+want.ResetsAt+`"`) {
		t.Fatalf("observation replaced: %s", stdinOut.String())
	}
	for _, leak := range []string{"SECRET-CANARY", "accountId", "accountIdentity", "planType", "credits", "balance", "rateLimits", "automationCeiling", "bankedResetCredits", "entitled", "authenticated", `"windowDurationMinutes":10080`} {
		if strings.Contains(stdinOut.String(), leak) {
			t.Fatalf("stdout contains %s: %s", leak, stdinOut.String())
		}
	}
}

// UNRUN: Sol runs this. Bad raw input fails closed without echoing the rejected bytes.
func TestUsageSourceRawRejectsTimeAndPrivacy(t *testing.T) {
	now := syntheticRawNow()
	secretPath := filepath.Join(t.TempDir(), "SECRET-CANARY.json")
	if err := os.WriteFile(secretPath, []byte(syntheticRawBody()), 0o600); err != nil {
		t.Fatal(err)
	}
	link := secretPath + "-link"
	if err := os.Symlink(secretPath, link); err != nil {
		t.Fatal(err)
	}
	resetAtNow := strconv.FormatInt(now.Unix(), 10)
	cases := []struct {
		args []string
		body string
		leak string
	}{
		{args: usageSourceRawArgs("-", "now"), body: syntheticRawBody(), leak: syntheticRawObserved},
		{args: usageSourceRawArgs("-", "2026-06-15T20:00:00Z"), body: syntheticRawBody(), leak: "2026-06-15T20:00:00Z"},
		{args: usageSourceRawArgs("-", "2026-06-15T12:00:00Z"), body: syntheticRawBody(), leak: "2026-06-15T12:00:00Z"},
		{args: usageSourceRawArgs("-", syntheticRawObserved), body: syntheticRawEnvelope("12", "300", resetAtNow, "SECRET-CANARY"), leak: resetAtNow},
		{args: usageSourceRawArgs("-", syntheticRawObserved), body: `{"accountId":"SECRET-ACCOUNT"}`, leak: "SECRET-ACCOUNT"},
		{args: usageSourceRawArgs("-", syntheticRawObserved), body: syntheticRawBody() + strings.Repeat(" ", usageSourceMaxBytes), leak: "SECRET-CANARY"},
		{args: usageSourceRawArgs(link, syntheticRawObserved), body: "", leak: secretPath},
		{args: usageSourceRawArgs(t.TempDir(), syntheticRawObserved), body: "", leak: "SECRET-CANARY"},
		{args: usageSourceRawArgs(secretPath+"-missing", syntheticRawObserved), body: "", leak: secretPath},
	}
	for _, tc := range cases {
		var out bytes.Buffer
		err := runUsageSource(t, context.Background(), strings.NewReader(tc.body), &now, &out, tc.args...)
		var ee *exitError
		if out.Len() != 0 || !errors.As(err, &ee) || ee.code != ExitFailed || ee.msg != "usage-source input unavailable" || strings.Contains(err.Error(), tc.leak) || strings.Contains(err.Error(), "SECRET-CANARY") || strings.Contains(err.Error(), "SECRET-ACCOUNT") {
			t.Fatalf("%v %q %v", tc.args, out.String(), err)
		}
	}
}
