package main

import (
	"slices"
	"testing"
)

func TestOwnedRootFlagsAreExplicitAndSeparateFromFixture(t *testing.T) {
	if o, err := parseFlags(nil); err != nil || o.ownedRootPolicy != "" {
		t.Fatal("root enabled by default", err)
	}
	image := "ghcr.io/thaynes43/dev-env@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	base := []string{"--enable-task-budgets", "--enable-coordinator-callers", "--enable-managed-codex-tasks", "--project-catalog=dev-env-system/dev-env-project-catalog", "--project-clone-owner=thaynes43", `--coordinator-hosts=[{"serviceAccount":"dev-agents/owned-root","podName":"owned-root","hostID":"native-host"}]`, `--assigned-task-budgets={"native-host":"campaign"}`, "--task-budget-namespace=protected-budgets", "--task-budget-worker-image=" + image, "--owned-root-policy=owned-root-policy"}
	if o, err := parseFlags(base); err != nil || o.ownedRootPolicy != "owned-root-policy" {
		t.Fatal("explicit root refused", err)
	}
	for _, args := range [][]string{{"--owned-root-policy=owned-root-policy"}, append(slices.Clone(base), "--owned-root-policy=../untrusted"), append(slices.Clone(base), "--enable-native-lifecycle-fixture"), append(slices.Clone(base), "--enable-task-budgets=false")} {
		if _, err := parseFlags(args); err == nil {
			t.Fatal("ambiguous root authority", args)
		}
	}
}
