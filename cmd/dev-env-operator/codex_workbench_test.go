package main

import "testing"

func TestCodexWorkbenchFlags(t *testing.T) {
	for _, args := range [][]string{nil, {"--codex-workbench-hosts="}, {"--codex-workbench-hosts=named-host"}, {"--codex-workbench-hosts=named-host", "--session-namespace=elsewhere"}} {
		o, err := parseFlags(args)
		if err != nil {
			t.Fatalf("valid %v: %v", args, err)
		}
		r := reconcilerForOptions(o)
		if r.CodexWorkbenchHosts != o.codexWorkbenchHosts || r.CodexWorkbenchHosts.Namespace != o.sessionNamespace {
			t.Fatal("flag not wired into controller")
		}
	}
	for _, args := range [][]string{{"--codex-workbench-hosts=a,a"}, {"--codex-workbench-hosts=a,b"}, {"--codex-workbench-hosts=*"}, {"--codex-workbench-hosts=a/b"}, {"--codex-workbench-hosts=a", "--codex-workbench-hosts=a"}} {
		if _, err := parseFlags(args); err == nil {
			t.Errorf("accepted %v", args)
		}
	}
}
