package main

import (
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/homeguard"
)

func TestOriginalHomeFlags(t *testing.T) {
	o, err := parseFlags(nil)
	if err != nil || o.originalHome != nil {
		t.Fatal("not default off", err)
	}
	b := homeguard.Binding{Namespace: "dev-agents", Session: "remote", SessionUID: "s", PVCUID: "c", PV: "pv", PVUID: "v"}
	o, err = parseFlags([]string{"--original-home-binding=" + b.String()})
	if err != nil || o.originalHome == nil || *o.originalHome != b {
		t.Fatal("binding", err)
	}
	if _, err := parseFlags([]string{"--original-home-binding={}"}); err == nil {
		t.Fatal("invalid binding")
	}
}

func TestOriginalHomeFlagNamespace(t *testing.T) {
	b := homeguard.Binding{Namespace: "custom-agents", Session: "remote", SessionUID: "s", PVCUID: "c", PV: "pv", PVUID: "v"}
	for _, args := range [][]string{
		{"--original-home-binding=" + b.String()},
		{"--session-namespace=other", "--original-home-binding=" + b.String()},
		{"--original-home-binding=" + b.String(), "--session-namespace=other"},
	} {
		if _, err := parseFlags(args); err == nil || !strings.Contains(err.Error(), "namespace must match --session-namespace") {
			t.Fatalf("mismatched namespace accepted: args=%v err=%v", args, err)
		}
	}
	for _, args := range [][]string{
		{"--session-namespace=custom-agents", "--original-home-binding=" + b.String()},
		{"--original-home-binding=" + b.String(), "--session-namespace=custom-agents"},
	} {
		if o, err := parseFlags(args); err != nil || o.originalHome == nil || *o.originalHome != b {
			t.Fatalf("matching namespace refused: args=%v err=%v", args, err)
		}
	}
}
