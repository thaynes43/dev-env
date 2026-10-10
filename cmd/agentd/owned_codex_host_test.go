package main

import (
	"os"
	"strings"
	"testing"
)

func TestOwnedCodexHostDisabled(t *testing.T) {
	home := t.TempDir()
	code, out, stderr := runArgs([]string{"owned-codex-host", "--enabled", "--task-uid", "untrusted-local-flags"}, map[string]string{"HOME": home})
	if code != exitFailure || out != "" || !strings.Contains(stderr, "no budget authority adapter") {
		t.Fatalf("disabled command: %d %q %q", code, out, stderr)
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 0 {
		t.Fatal("disabled command mutated home")
	}
}
func TestOwnedCodexHostHelperRequiresPrivateParentSocket(t *testing.T) {
	code, out, stderr := runArgs([]string{"owned-codex-host-helper"}, nil)
	if code != exitFailure || out != "" || stderr != "owned executor helper failed\n" {
		t.Fatalf("helper was publicly admitted: %d %q %q", code, out, stderr)
	}
}
