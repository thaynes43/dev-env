package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func TestCodexRCFlagsDefaultOffAndQualified(t *testing.T) {
	o, err := parseFlags(nil)
	if err != nil || o.codexRCEnabled || o.codexRCTarget != nil || o.codexRCTargetFile != "" {
		t.Fatal("codex RC default changed", err)
	}
	for _, args := range [][]string{
		{"--enable-codex-rc-thread"},
		{"--codex-rc-target-file=/missing"},
		{"--enable-codex-rc-thread", "--project-catalog=dev-env-system/dev-env-project-catalog", "--project-clone-owner=thaynes43", "--codex-rc-target-file=/missing"},
	} {
		if _, err := parseFlags(args); err == nil {
			t.Fatal("unqualified runtime enabled", args)
		}
	}
}

func TestCodexRCFlagsExpiredQualificationDoesNotBlockStartup(t *testing.T) {
	now := time.Now().UTC()
	target := apiv1.CodexRCTarget{SessionName: "rc-host", SessionUID: "session", PodUID: "pod", Project: "project", Repository: "repo", SnapshotSHA256: strings.Repeat("a", 64), Root: "/home/dev/work/accepted", RootDevice: 1, RootInode: 2, PreparationSHA256: strings.Repeat("b", 64), PID: 123, BootID: "boot", StartTicks: 42, InstallationID: "installation", EnvironmentID: "environment", PairingSource: "supported-client", PairingSHA256: strings.Repeat("c", 64), VerifiedAt: now.Add(-time.Hour), ExpiresAt: now.Add(-50 * time.Minute)}
	path := filepath.Join(t.TempDir(), "target.json")
	args := []string{"--enable-codex-rc-thread", "--project-catalog=dev-env-system/dev-env-project-catalog", "--project-clone-owner=thaynes43", "--codex-rc-target-file=" + path}
	for _, mode := range []string{"expired", "invalid-window", "invalid-binding", "malformed", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			packet := target
			switch mode {
			case "invalid-window":
				packet.ExpiresAt = packet.VerifiedAt.Add(16 * time.Minute)
			case "invalid-binding":
				packet.SessionUID = ""
			}
			raw, err := json.Marshal(packet)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "malformed":
				raw = []byte(`{"sessionName":"rc-host","sessionName":"other"}`)
			case "oversize":
				raw = []byte(strings.Repeat(" ", apiv1.CodexRCMaxPacket+1))
			}
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			o, err := parseFlags(args)
			if mode != "expired" {
				if err == nil {
					t.Fatal("invalid target admitted at startup")
				}
				return
			}
			if err != nil || !o.codexRCEnabled || o.codexRCTarget == nil {
				t.Fatal("expired target blocked operator startup", err)
			}
			if o.codexRCTarget.Valid(now) {
				t.Fatal("expired target became usable")
			}
		})
	}
}
