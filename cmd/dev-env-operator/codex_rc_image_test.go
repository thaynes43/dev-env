package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestCodexRCImageFlags(t *testing.T) {
	const tuple = "rc-host/242846d8-feb0-4f52-8164-01622206b88a"
	image := "ghcr.io/thaynes43/dev-env:2.70.0@sha256:" + strings.Repeat("a", 64)
	base, err := parseFlags(nil)
	if err != nil || base.codexRCAgent != nil {
		t.Fatalf("defaults: %+v %v", base.codexRCAgent, err)
	}
	got, err := parseFlags([]string{"--codex-rc-agent-image=" + image, "--codex-rc-agent-session=" + tuple})
	if err != nil || got.codexRCAgent == nil || got.codexRCAgent.Name != "rc-host" || got.codexRCAgent.UID != "242846d8-feb0-4f52-8164-01622206b88a" || got.codexRCAgent.Image != image {
		t.Fatalf("parse: %+v %v", got.codexRCAgent, err)
	}
	if reconcilerForOptions(got).CodexRCAgent != got.codexRCAgent {
		t.Fatal("reconciler lost target")
	}
	for _, args := range [][]string{
		{"--codex-rc-agent-image=" + image}, {"--codex-rc-agent-session=" + tuple},
		{"--codex-rc-agent-image=ghcr.io/thaynes43/dev-env:2.70.0", "--codex-rc-agent-session=" + tuple},
		{"--codex-rc-agent-image=" + image, "--codex-rc-agent-session=rc-host/not-uid"},
		{"--codex-rc-agent-image=" + image, "--codex-rc-agent-session=../" + strings.Split(tuple, "/")[1]},
		{"--codex-rc-agent-image=" + image, "--codex-rc-agent-session=" + tuple + "/other"},
	} {
		if _, err := parseFlags(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	before, err := parseFlags([]string{"--remote-pilot-agent-image=" + image, "--remote-pilot-agent-session=dev/dev-env|recovery205125"})
	if err != nil {
		t.Fatal(err)
	}
	after, err := parseFlags([]string{"--remote-pilot-agent-image=" + image, "--remote-pilot-agent-session=dev/dev-env|recovery205125", "--codex-rc-agent-image=" + image, "--codex-rc-agent-session=" + tuple})
	if err != nil || !reflect.DeepEqual(before.remotePilotAgent, after.remotePilotAgent) {
		t.Fatal("existing mapping changed", err)
	}
}
