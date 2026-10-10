package main

import (
	"slices"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestNativeFixtureFactoryRequiresExplicitImmutableConfiguration(t *testing.T) {
	reader := fake.NewClientBuilder().Build()
	image := "ghcr.io/thaynes43/dev-env@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if inspector, err := newNativeFixtureInspector(nil, "", false); err != nil || inspector != nil {
		t.Fatal("default factory enabled")
	}
	for _, tc := range []struct {
		image   string
		enabled bool
	}{{image, false}, {"", true}, {"ghcr.io/thaynes43/dev-env:latest", true}} {
		if _, err := newNativeFixtureInspector(reader, tc.image, tc.enabled); err == nil {
			t.Fatal("unreviewed fixture configuration admitted")
		}
	}
	if _, err := newNativeFixtureInspector(nil, image, true); err == nil {
		t.Fatal("missing uncached reader admitted")
	}
	if inspector, err := newNativeFixtureInspector(reader, image, true); err != nil || inspector == nil {
		t.Fatal("explicit immutable profile refused", err)
	}
}

func TestNativeFixtureFlagsRequireExactIsolatedAuthority(t *testing.T) {
	image := "ghcr.io/thaynes43/dev-env@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	base := []string{"--enable-task-budgets", "--enable-coordinator-callers", "--enable-managed-codex-tasks", "--project-catalog=dev-env-system/dev-env-project-catalog", "--project-clone-owner=thaynes43", `--coordinator-hosts=[{"serviceAccount":"dev-agents/dev-env-owned-native-fixture","podName":"dev-env-owned-native-lifecycle","hostID":"owned-native-fixture"}]`, `--assigned-task-budgets={"owned-native-fixture":"native-lifecycle-once"}`, "--task-budget-namespace=protected-budgets", "--task-budget-worker-image=" + image, "--enable-native-lifecycle-fixture", "--native-fixture-image=" + image}
	if o, err := parseFlags(base); err != nil || !o.nativeFixtureEnabled || o.nativeFixtureImage != image {
		t.Fatal("exact fixture authority refused", err)
	}
	if o, err := parseFlags(nil); err != nil || o.nativeFixtureEnabled || o.nativeFixtureImage != "" {
		t.Fatal("native fixture enabled by default", err)
	}
	for _, args := range [][]string{{"--enable-native-lifecycle-fixture"}, {"--native-fixture-image=" + image}, append(slices.Clone(base), "--native-fixture-image=ghcr.io/thaynes43/dev-env:latest"), append(slices.Clone(base), `--coordinator-hosts=[{"serviceAccount":"dev-agents/dev-env-owned-native-fixture","podName":"other-pod","hostID":"owned-native-fixture"}]`), append(slices.Clone(base), "--enable-task-budgets=false")} {
		if _, err := parseFlags(args); err == nil {
			t.Fatalf("missing or foreign fixture authority admitted: %v", args)
		}
	}
}
