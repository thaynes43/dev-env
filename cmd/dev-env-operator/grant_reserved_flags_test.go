package main

import (
	"slices"
	"testing"

	"github.com/thaynes43/dev-env/internal/grantattest"
)

func TestReservedDecisionFlagsDefaultOffAndMatch(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "dev-env-system")
	for _, args := range [][]string{nil, {"--reserved-decision-classes="}, {"--reserved-decision-classes=rd4-hardware,rd3a", "--reserved-excluded-namespaces=dev-tools,dev-agents"}, {"--reserved-excluded-namespaces="}} {
		op, err := parseFlags(args)
		if err != nil {
			t.Fatal(err)
		}
		b, err := parseBrokerFlags(args)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(op.reservedDecisionClasses, b.reservedDecisionClasses) || !slices.Equal(op.reservedExcludedNamespaces, b.reservedExcludedNamespaces) {
			t.Fatal("issuer bounds differ", op.reservedDecisionClasses, b.reservedDecisionClasses)
		}
		if len(args) == 0 && (len(op.reservedDecisionClasses) != 0 || len(op.reservedExcludedNamespaces) != len(grantattest.ReservedDefaultExclusions())) {
			t.Fatal("default enabled S7 or lost proposed safe bounds")
		}
	}
	for _, flag := range []string{"--reserved-decision-classes=rd2,rd2", "--reserved-decision-classes=unknown", "--reserved-excluded-namespaces=dev-agents,,dev-tools", "--reserved-excluded-namespaces=dev-agents,dev-agents"} {
		if _, err := parseFlags([]string{flag}); err == nil {
			t.Fatal("operator accepted malformed bounds", flag)
		}
		if _, err := parseBrokerFlags([]string{flag}); err == nil {
			t.Fatal("broker accepted malformed bounds", flag)
		}
	}
}
