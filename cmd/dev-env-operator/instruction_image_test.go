package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestInstructionAgentImageFlagsAreDefaultOffAndIndependent(t *testing.T) {
	base, err := parseFlags(nil)
	if err != nil || base.coordinatorInstructionAgentImage != "" {
		t.Fatal("default enrollment image enabled", err)
	}
	image := "ghcr.io/thaynes43/dev-env:2.86.0@sha256:" + strings.Repeat("a", 64)
	got, err := parseFlags([]string{"--enable-private-project-tasks", "--api-url=https://operator.example", "--project-catalog=dev-env-system/dev-env-project-catalog", "--project-clone-owner=thaynes43", "--coordinator-instruction-agent-image=" + image})
	if err != nil || got.coordinatorInstructionAgentImage != image || reconcilerForOptions(got).CoordinatorInstructionAgentImage != image {
		t.Fatal("lost qualification", err)
	}
	if !reflect.DeepEqual(base.pilotAgent, got.pilotAgent) || !reflect.DeepEqual(base.taskPilotAgent, got.taskPilotAgent) || !reflect.DeepEqual(base.remotePilotAgent, got.remotePilotAgent) || !reflect.DeepEqual(base.codexRCAgent, got.codexRCAgent) {
		t.Fatal("changed existing image selectors")
	}
	for _, args := range [][]string{
		{"--coordinator-instruction-agent-image=" + image},
		{"--enable-private-project-tasks", "--coordinator-instruction-agent-image=ghcr.io/thaynes43/dev-env:latest"},
		{"--enable-private-project-tasks", "--coordinator-instruction-agent-image=ghcr.io/thaynes43/dev-env:2.86.0"},
		{"--enable-private-project-tasks", "--coordinator-instruction-agent-image=ghcr.io/foreign/dev-env:2.86.0@sha256:" + strings.Repeat("a", 64)},
	} {
		if _, err := parseFlags(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
