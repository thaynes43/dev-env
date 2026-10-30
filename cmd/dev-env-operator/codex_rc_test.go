package main

import "testing"

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
