package agentd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// realClaudeGuard is the directory TestMain puts first on PATH for this
// package's tests (ruling 6 of story 791 fix round 1, PR 1120 comment
// 6843361182; not a ruling by Tom). Its claude answers nothing and exits 97,
// so a test that starts "claude" by name, as Settings.ClaudeBin defaults to,
// never reaches the real Claude CLI and so never calls a provider.
// TestNoTestReachesTheRealClaude shows it.
var realClaudeGuard string

// realClaudeGuardScript is the guard's claude.
const realClaudeGuardScript = `#!/bin/sh
echo "agentd test guard: this is not the real claude; a test must start a fake CLI by its path" >&2
exit 97
`

// realClaudeGuardPrefix names the guard's directory.
const realClaudeGuardPrefix = "agentd-claude-guard-"

// TestMain makes the guard only in the top-level test process. Some tests
// start this test binary again as a child (cursor_scaffold_test.go), which
// may exit or be killed before it could remove a guard of its own. A child
// that inherited the environment finds the guard first on PATH and reuses
// it; one started with no PATH cannot reach any claude by name and needs
// none.
func TestMain(m *testing.M) {
	path := os.Getenv("PATH")
	first, _, _ := strings.Cut(path, string(os.PathListSeparator))
	switch {
	case path == "":
		os.Exit(m.Run())
	case strings.HasPrefix(filepath.Base(first), realClaudeGuardPrefix):
		if raw, err := os.ReadFile(filepath.Join(first, "claude")); err == nil && string(raw) == realClaudeGuardScript {
			realClaudeGuard = first
			os.Exit(m.Run())
		}
	}
	dir, err := os.MkdirTemp("", realClaudeGuardPrefix)
	if err == nil {
		err = os.WriteFile(filepath.Join(dir, "claude"), []byte(realClaudeGuardScript), 0o755)
	}
	if err == nil {
		err = os.Setenv("PATH", dir+string(os.PathListSeparator)+path)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "agentd tests: the claude guard could not be put on PATH: %v\n", err)
		os.Exit(2)
	}
	realClaudeGuard = dir
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// guardProbeEnv makes TestGuardProbeChild exit at once, as a re-executed
// child that is killed or calls os.Exit does, before TestMain could remove
// a guard of its own.
const guardProbeEnv = "AGENTD_TEST_GUARD_PROBE"

func TestGuardProbeChild(t *testing.T) {
	if os.Getenv(guardProbeEnv) == "1" {
		os.Exit(0)
	}
}

// A re-executed child of this test binary leaves no guard directory behind,
// whether it inherited the environment (and so the guard on PATH) or was
// started with a cleared one. Each child gets its own TMPDIR, where a guard
// of its own would be made, and only that directory is checked, so another
// test run's guard elsewhere in /tmp cannot fail this test.
func TestReexecutedChildLeavesNoGuard(t *testing.T) {
	scrubClusterEnv(t)
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"inherited", "cleared"} {
		tmp := t.TempDir()
		env := []string{guardProbeEnv + "=1", "TMPDIR=" + tmp}
		if name == "inherited" {
			env = append(os.Environ(), env...)
		}
		cmd := exec.Command(bin, "-test.run=^TestGuardProbeChild$")
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s child: %v %s", name, err, out)
		}
		if left, _ := filepath.Glob(filepath.Join(tmp, realClaudeGuardPrefix+"*")); len(left) != 0 {
			t.Fatalf("a child with the %s environment left a guard directory: %q", name, left)
		}
	}
}
