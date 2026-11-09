package agentd

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// Always-allow is every launch path's contract, as in v1 (Tom, 2026-11-09;
// DESIGN-057 section 6a). These tests pin the effective mode on each path; the
// thread-launch result reports the same contract.

func assertOnce(t *testing.T, argv []string, flag string) {
	t.Helper()
	n := 0
	for _, a := range argv {
		if a == flag {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("argv carries %s %d times: %q", flag, n, argv)
	}
	for _, banned := range []string{"--permission-mode", "-s", "--sandbox", "-a", "--ask-for-approval", "--approve-for-me", "--full-auto"} {
		if slices.Contains(argv, banned) {
			t.Fatalf("argv picks a permission flag by hand (%s): %q", banned, argv)
		}
	}
}

// Claude: fresh task, local and remote (the --interactive session and the
// standby, which is a remote session) and their resume (a pod roll, resume
// and a rescue restore all resume the first launch's conversation).
func TestClaudeLaunchPathsAreAlwaysAllow(t *testing.T) {
	now := time.Date(2026, 11, 9, 18, 0, 0, 0, time.UTC)
	for _, mode := range []string{protocol.ModeTask, protocol.ModeLocal, protocol.ModeRemote} {
		t.Run(mode, func(t *testing.T) {
			s, sess, ws := launchFixture(t)
			sess.Mode = mode
			if mode != protocol.ModeTask {
				sess.Prompt, sess.Limits = "", nil
			}
			first, err := BuildLaunch(s, sess, ws, "boot1", now)
			if err != nil {
				t.Fatal(err)
			}
			resume, err := BuildResume(s, sess, ws, first, "boot2", now)
			if err != nil {
				t.Fatal(err)
			}
			for name, l := range map[string]Launch{"first": first, "resume": resume} {
				assertOnce(t, l.Argv, "--dangerously-skip-permissions")
				// The critical-path override is off by default and cannot come
				// in from the pod's own environment.
				if !slices.Contains(l.Unset, claudeCriticalPathEnv) || slices.ContainsFunc(l.Env, func(e string) bool { return strings.HasPrefix(e, claudeCriticalPathEnv) }) {
					t.Fatalf("%s: critical-path override not default-off: env %q", name, l.Env)
				}
			}
			s.ClaudeSkipCriticalPathPrompt = true
			on, err := BuildLaunch(s, sess, ws, "boot3", now)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(on.Env, claudeCriticalPathEnv+"=1") {
				t.Fatalf("the enabled override is missing: %q", on.Env)
			}
		})
	}
}

// The setting is read from one clearly named variable and is off otherwise.
func TestClaudeCriticalPathSettingDefaultOff(t *testing.T) {
	home := t.TempDir()
	for value, want := range map[string]bool{"": false, "1": false, "TRUE": false, "true": true} {
		s, err := LoadSettings(envOf(map[string]string{"HOME": home, "AGENTD_CLAUDE_SKIP_CRITICAL_PATH_PROMPT": value}))
		if err != nil {
			t.Fatal(err)
		}
		if s.ClaudeSkipCriticalPathPrompt != want {
			t.Errorf("%q gave %v", value, s.ClaudeSkipCriticalPathPrompt)
		}
	}
}

// Every Claude agent argv in agentd is built by claudeArgs, so the explicit RC
// continuation and any future path inherit --dangerously-skip-permissions.
// (mcp.go's `claude mcp` calls and the login window are not agent launches.)
func TestEveryClaudeAgentArgvUsesClaudeArgs(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	direct := regexp.MustCompile(`\[\]string\{s\.ClaudeBin`)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(raw)
		if direct.MatchString(src) && f != "launch.go" {
			t.Errorf("%s builds a Claude argv without claudeArgs", f)
		}
		if strings.Contains(src, `"claude", "--`) {
			t.Errorf("%s builds a literal claude argv", f)
		}
	}
	explicit, err := os.ReadFile("explicit_claude_rc.go")
	if err != nil || !strings.Contains(string(explicit), "argv := append(claudeArgs(s, sess),") || !strings.Contains(string(explicit), "Env: claudeLaunchEnv(s)") {
		t.Fatal("the explicit RC continuation no longer builds on claudeArgs and claudeLaunchEnv")
	}
}

// Codex managed tasks: the first exec and the resumed TUI both carry the one
// bypass flag and no sandbox or approval choice.
func TestManagedCodexLaunchIsAlwaysAllow(t *testing.T) {
	s, sess, ws, now := managedCodexFixture(t)
	l, err := BuildLaunch(s, sess, ws, "boot", now)
	if err != nil {
		t.Fatal(err)
	}
	assertOnce(t, l.Argv, "--dangerously-bypass-approvals-and-sandbox")
	l.ConversationID, l.NativeThreadConfirmed = "12345678-1234-1234-1234-123456789abc", true
	resume, err := BuildResume(s, sess, ws, l, "resume", now)
	if err != nil || !resume.Resume {
		t.Fatalf("resume: %v", err)
	}
	assertOnce(t, resume.Argv, "--dangerously-bypass-approvals-and-sandbox")
}
