package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/thaynes43/dev-env/internal/keeper"
)

func TestCodexAuthDefaultsAndExplicitPrerequisites(t *testing.T) {
	o, err := parseFlags(nil)
	if err != nil || o.codexAuth.Enabled {
		t.Fatal("Codex auth enabled by default")
	}
	for _, args := range [][]string{{"--enable-codex-auth", "--leader-elect=false"}, {"--enable-codex-auth", "--codex-auth-journal-secret="}, {"--enable-codex-auth", "--codex-live-secret="}, {"--enable-codex-auth", "--codex-login-dir=relative"}, {"--enable-codex-auth", "--secret-namespace=dev-env-system"}} {
		if _, err := parseFlags(args); err == nil {
			t.Fatal("unsafe Codex auth configuration accepted")
		}
	}
	if o, err := parseFlags([]string{"--enable-codex-auth"}); err != nil || !o.codexAuth.Enabled {
		t.Fatal("explicit auth configuration rejected")
	}
}

func TestCodexLoginRefusesPodLogOutputBeforeNativeLogin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control-result")
	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = out.Close() }()
	in, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = in.Close() }()
	if codexLoginCommand([]string{"codex-login"}, in, out) != 1 {
		t.Fatal("noninteractive ceremony accepted")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var result keeper.CodexControlResponse
	if json.Unmarshal(raw, &result) != nil || result.OK || result.Code != "AuthenticatedTerminalRequired" {
		t.Fatal("helper failed to emit a fixed refusal")
	}
}
