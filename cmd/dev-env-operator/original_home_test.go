package main

import (
	"testing"

	"github.com/thaynes43/dev-env/internal/homeguard"
)

func TestOriginalHomeFlags(t *testing.T) {
	o, err := parseFlags(nil)
	if err != nil || o.originalHome != nil {
		t.Fatal("not default off", err)
	}
	b := homeguard.Binding{Namespace: "ns", Session: "remote", SessionUID: "s", PVCUID: "c", PV: "pv", PVUID: "v"}
	o, err = parseFlags([]string{"--original-home-binding=" + b.String()})
	if err != nil || o.originalHome == nil || *o.originalHome != b {
		t.Fatal("binding", err)
	}
	if _, err := parseFlags([]string{"--original-home-binding={}"}); err == nil {
		t.Fatal("invalid binding")
	}
}
