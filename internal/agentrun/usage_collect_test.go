package agentrun

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pa "github.com/thaynes43/dev-env/internal/providerallowance"
	"github.com/thaynes43/dev-env/internal/usagecollector"
)

func TestUsageCollectClaudeCallbackPrivateArtifactNoPublication(t *testing.T) {
	h := newHarness(t)
	dir := filepath.Join(h.dir, "private-callback")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	h.env.Stdin = strings.NewReader(`{"transcript_path":"SECRET-CANARY","rate_limits":{"five_hour":{"used_percentage":12,"resets_at":null}}}`)
	h.mustRun(ExitOK, "usage", "collect", "claude-statusline", "--state-dir", dir, "--producer", "isolated-review", "--incarnation", "review-1")
	if h.stdout.String() != "Claude usage captured\n" || len(h.api.requests()) != 0 || len(h.ran) != 0 {
		t.Fatal("callback contacted provider/operator or leaked input")
	}
	body, err := os.ReadFile(filepath.Join(dir, usagecollector.ClaudeFile))
	if err != nil || strings.Contains(string(body), "SECRET") {
		t.Fatal("private callback failed", err)
	}
}
func TestUsageCollectCodexIsExplicitAndClosed(t *testing.T) {
	h := newHarness(t)
	calls := 0
	h.env.CollectCodex = func(ctx context.Context, executable string, now func() time.Time) (pa.CodexAccountCapture, error) {
		calls++
		if executable != "/official/codex" {
			t.Fatal("executable changed")
		}
		return pa.CodexAccountCapture{Schema: pa.CodexRPCSchema, AccountRef: "openai-email-sha256:" + strings.Repeat("a", 64), IdentityBasis: "provider_email_sha256", PlanType: "pro", ReceivedAt: now().UTC().Format(time.RFC3339Nano), Primary: &pa.CodexWindow{UsedPercent: 12}}, nil
	}
	for _, args := range [][]string{{"collect", "codex"}, {"collect", "codex", "-o", "json", "--executable", "relative"}, {"collect", "codex", "-o", "json", "--executable", "/official/codex", "--publish"}, {"collect", "codex", "-o", "json", "--executable", "/official/codex", "--executable", "/other"}} {
		h.mustRun(ExitUsage, append([]string{"usage"}, args...)...)
	}
	if calls != 0 {
		t.Fatal("invalid arguments started provider")
	}
	h.mustRun(ExitOK, "usage", "collect", "codex", "-o", "json", "--executable", "/official/codex")
	var report pa.CodexAccountCapture
	if json.Unmarshal(h.stdout.Bytes(), &report) != nil || report.Primary.UsedPercent != 12 || report.ProviderObservedAt != nil || calls != 1 || len(h.api.requests()) != 0 {
		t.Fatal("finite result malformed or implicitly published")
	}
	h.env.CollectCodex = func(context.Context, string, func() time.Time) (pa.CodexAccountCapture, error) {
		return pa.CodexAccountCapture{}, errors.New("SECRET-TRANSPORT-CANARY")
	}
	h.mustRun(ExitFailed, "usage", "collect", "codex", "-o", "json", "--executable", "/official/codex")
	if h.stdout.Len() != 0 || strings.Contains(h.stderr.String(), "SECRET") {
		t.Fatal("transport failure echoed")
	}
}
