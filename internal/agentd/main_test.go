package agentd

import (
	"fmt"
	"os"
	"path/filepath"
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

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "agentd-claude-guard-")
	if err == nil {
		err = os.WriteFile(filepath.Join(dir, "claude"), []byte(realClaudeGuardScript), 0o755)
	}
	if err == nil {
		err = os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
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
