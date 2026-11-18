package agentd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/agentd/hostexecutor"
	"github.com/thaynes43/dev-env/internal/codexrc"
)

// An image-only Renovate bump must not bypass protocol/source qualification.
func TestCodexAgentVersionPinsAgree(t *testing.T) {
	root := filepath.Join("..", "..")
	raw, err := os.ReadFile(filepath.Join(root, "docs", "codex-cli-source-audit.json"))
	if err != nil {
		t.Fatal(err)
	}
	var audit struct {
		Version string `json:"version"`
		Commit  string `json:"commit"`
	}
	if json.Unmarshal(raw, &audit) != nil || len(audit.Commit) != 40 {
		t.Fatal("invalid source audit pin")
	}
	for _, version := range []string{managedCodexVersion, hostexecutor.PinnedNativeVersion, codexrc.SupportedCLIVersion} {
		if audit.Version != version {
			t.Fatal("native version guards and source audit disagree")
		}
	}
	raw, err = os.ReadFile(filepath.Join(root, "images", "agent", "Dockerfile"))
	if err != nil || !strings.Contains(string(raw), "\nARG CODEX_VERSION="+audit.Version+"\n") {
		t.Fatal("agent image pin is not source-qualified", err)
	}
	// Deliberate split: the keeper's native-login helper is a separate prerequisite.
	raw, err = os.ReadFile(filepath.Join(root, "images", "codex-login", "Dockerfile"))
	if err != nil || !strings.Contains(string(raw), "\nARG CODEX_VERSION=0.160.1\n") {
		t.Fatal("login-helper prerequisite was bypassed", err)
	}
}

func TestCodexRetainedRolloutVersions(t *testing.T) {
	id, cwd := "12345678-1234-1234-1234-123456789abc", "/work/fixture"
	for _, version := range []string{"0.160.1", managedCodexVersion, "0.169.0", "0.170.0", "unknown"} {
		raw, err := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]any{"id": id, "cli_version": version, "cwd": cwd, "account_scoped": true, "session_id": id}})
		if err != nil {
			t.Fatal(err)
		}
		got := portalSourceVersion("codex", id, cwd, raw)
		if want := version == "0.160.1" || version == managedCodexVersion; got != want {
			t.Fatalf("%s accepted=%v, want %v", version, got, want)
		}
	}
}
