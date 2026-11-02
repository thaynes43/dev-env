package agentrun

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/providerallowance"
)

const captureEmptyTrust = `{"maxAgeSeconds":1800,"contexts":[],"registry":{"sources":[],"metrics":[],"accounts":[],"entitlements":[],"projections":[]}}`

func usageCaptureFixture(t *testing.T, h *harness) (string, string) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"schema": providerallowance.CaptureBundleSchema, "captures": []any{map[string]any{"provider": "anthropic", "producerRef": "passive-fixture", "producerIncarnation": "passive-fixture-1", "sequence": 1, "capturedAt": h.now.Add(-2 * time.Minute).Format(time.RFC3339), "receivedAt": h.now.Add(-time.Minute).Format(time.RFC3339), "payload": map[string]any{"rate_limits": map[string]any{"five_hour": nil, "seven_day": map[string]any{"used_percentage": 5.5, "resets_at": nil}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	return h.write("captures.json", string(raw)), h.write("trust.json", captureEmptyTrust)
}

func TestUsageCaptureIsExplicitLocalAssembly(t *testing.T) {
	h := newHarness(t)
	input, trust := usageCaptureFixture(t, h)
	h.mustRun(ExitOK, "usage", "capture", "-o", "json", "--input", input, "--trust-file", trust)
	doc, err := providerallowance.DecodeReadResponse(bytesWithoutNewline(h.stdout.Bytes()), providerallowance.FixtureParseLimits)
	if err != nil || len(doc.Items) != 1 || len(doc.SourceStatuses) != 3 {
		t.Fatalf("local assembly %v %s", err, h.stdout.String())
	}
	item := doc.Items[0]
	if item.Observation.CapturedAt != h.now.Add(-2*time.Minute).Format(time.RFC3339) || item.ReceivedAt != h.now.Add(-time.Minute).Format(time.RFC3339) || item.Observation.ProviderObservedAt != nil || item.Binding.State != providerallowance.BindingUnknown {
		t.Fatal("capture clocks/binding changed")
	}
	if len(h.api.requests()) != 0 || len(h.ran) != 0 || len(h.sleeps) != 0 {
		t.Fatal("local capture acquired/published or ran a provider")
	}
	if item.CurrentMeasures[1].Value.Decimal != nil || *item.Observation.Measures[1].Value.Decimal != "5.5" {
		t.Fatal("historical input promoted or discarded")
	}
}

func TestUsageCaptureReadsCompleteStdin(t *testing.T) {
	for _, mode := range []string{"buffer", "pipe"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t)
			input, trust := usageCaptureFixture(t, h)
			raw, err := os.ReadFile(input)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "buffer" {
				h.env.Stdin = strings.NewReader(string(raw))
			} else {
				reader, writer, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
				if _, err := writer.Write(raw); err != nil {
					t.Fatal(err)
				}
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
				h.env.Stdin = reader
			}
			h.mustRun(ExitOK, "usage", "capture", "-o", "json", "--input", "-", "--trust-file", trust)
			if _, err := providerallowance.DecodeReadResponse(bytesWithoutNewline(h.stdout.Bytes()), providerallowance.FixtureParseLimits); err != nil || len(h.api.requests()) != 0 || len(h.ran) != 0 {
				t.Fatal("complete stdin did not remain local", err)
			}
		})
	}
}

func bytesWithoutNewline(raw []byte) []byte { return []byte(strings.TrimSuffix(string(raw), "\n")) }

func TestUsageCaptureRejectsUnsafeInputWithoutEcho(t *testing.T) {
	for _, mode := range []string{"symlink", "directory", "bad-input", "bad-trust", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t)
			input, trust := usageCaptureFixture(t, h)
			switch mode {
			case "symlink":
				p := filepath.Join(h.dir, "link")
				if err := os.Symlink(input, p); err != nil {
					t.Fatal(err)
				}
				input = p
			case "directory":
				input = h.dir
			case "bad-input":
				input = h.write("secret-input", `{"token":"SECRET-CAPTURE-CANARY"}`)
			case "bad-trust":
				trust = h.write("secret-trust", `{"token":"SECRET-CAPTURE-CANARY"}`)
			case "oversize":
				input = h.write("large", strings.Repeat("x", providerallowance.FixtureParseLimits.MaxBytes+1))
			}
			h.mustRun(ExitFailed, "usage", "capture", "-o", "json", "--input", input, "--trust-file", trust)
			if h.stdout.Len() != 0 || strings.Contains(h.stderr.String(), "SECRET") || strings.Contains(h.stderr.String(), h.dir) || len(h.api.requests()) != 0 || len(h.ran) != 0 {
				t.Fatal("refused input leaked or performed remote action")
			}
		})
	}
}

func TestUsageCaptureRefusesMixedModesAndCancellation(t *testing.T) {
	h := newHarness(t)
	input, trust := usageCaptureFixture(t, h)
	for _, args := range [][]string{
		{"-o", "json", "--input", input},
		{"-o", "json", "--input", input, "--trust-file", "-"},
		{"-o", "json", "--input", input, "--input", input, "--trust-file", trust},
		{"-o", "json", "--input", input, "--trust-file", trust, "--publish"},
	} {
		h.mustRun(ExitUsage, append([]string{"usage", "capture"}, args...)...)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h.stdout.Reset()
	h.stderr.Reset()
	if code := Run(ctx, []string{"usage", "capture", "-o", "json", "--input", input, "--trust-file", trust}, h.env); code != ExitFailed || h.stdout.Len() != 0 || len(h.api.requests()) != 0 || len(h.ran) != 0 {
		t.Fatal("cancelled assembly performed work")
	}
}
