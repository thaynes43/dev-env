package agentd

import (
	"context"
	"os"
	"os/exec"
	"regexp"
	"syscall"
	"testing"
	"time"
)

const isolatedPreflightTestEnv = "AGENTD_PREFLIGHT_ISOLATED_TEST"

// Re-execute the whole test, including its seams, in an owned group using
// the lifecycle helper's scrubbed environment. The package test process
// must never become a pre-flight launcher or own unrelated test children.
func isolatePreflightTest(t *testing.T) bool {
	t.Helper()
	if os.Getenv(isolatedPreflightTestEnv) == t.Name() {
		return false
	}
	runPreflightTestChild(t, true, isolatedPreflightTestEnv+"="+t.Name())
	return true
}

func runPreflightTestChild(t *testing.T, ownGroup bool, values ...string) {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-test.run=^"+regexp.QuoteMeta(t.Name())+"$", "-test.count=1")
	cmd.Env = lifecycleEnv(os.Environ(), values...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: ownGroup}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("isolated pre-flight test: %v\n%s", err, out)
	}
}

func TestPreflightIsolationPreservesParentGroup(t *testing.T) {
	const probeEnv = "AGENTD_PREFLIGHT_GROUP_PROBE"
	if os.Getenv(probeEnv) != "1" {
		// Start a fresh supervisor in our group, not as a group leader.
		// Earlier tests cannot mask a Setpgid in the test being checked.
		runPreflightTestChild(t, false, probeEnv+"=1")
		return
	}
	before := syscall.Getpgrp()
	TestPreflightFirstModelAnswers(t)
	if after := syscall.Getpgrp(); after != before {
		t.Errorf("pre-flight changed the parent test process group from %d to %d", before, after)
	}
}
