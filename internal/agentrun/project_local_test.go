package agentrun

import (
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// claudeProjectLocalRefused is the project guard for restore, interactive, and non-Claude local.
const claudeProjectLocalRefused = "agent-run: --project selects a new task or a fresh claude --local session; it cannot combine with rescue restore, --interactive, or non-claude --local\n"

func TestProjectLocalClaudeCreatesOnce(t *testing.T) {
	cases := []struct {
		name string
		key  string
		args []string
	}{
		{
			name: "default-claude",
			key:  "project-local-default",
			args: []string{
				"--project", "sample", "--repo", "haynes-ops", "--local",
				"--model", "claude-sonnet-5-5", "--effort", "medium",
				"--idempotency-key", "project-local-default",
				"--wait", "0", "-o", "json",
			},
		},
		{
			name: "explicit-claude",
			key:  "project-local-explicit",
			args: []string{
				"--project", "sample", "--repo", "haynes-ops", "--local", "--agent", protocol.AgentClaude,
				"--model", "claude-sonnet-5-5", "--effort", "medium",
				"--idempotency-key", "project-local-explicit",
				"--wait", "0", "-o", "json",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			serveCreate(h, runningOn(testSession(name, ""), "talosw01"))
			h.mustRun(ExitOK, tc.args...)

			reqs := h.api.requests()
			if len(reqs) != 1 {
				t.Fatalf("got %d requests, want one create and no poll or attach", len(reqs))
			}
			if reqs[0].auth != "Bearer "+testToken {
				t.Errorf("Authorization = %q, want the harness token", reqs[0].auth)
			}
			got := decodeCreate(t, reqs[0].body)
			want := apiv1.CreateSessionRequest{
				Project: "sample", Repo: "haynes-ops", Agent: protocol.AgentClaude, Mode: protocol.ModeLocal,
				Model: "claude-sonnet-5-5", Effort: "medium", Prompt: "", Limits: nil, IdempotencyKey: tc.key,
			}
			if g, w := mustJSON(t, got), mustJSON(t, want); g != w {
				t.Errorf("create body = %s, want %s", g, w)
			}
			if len(h.sleeps) != 0 {
				t.Errorf("sleeps = %v, want no poll", h.sleeps)
			}
		})
	}
}

func TestProjectLocalRefusalsSendNothing(t *testing.T) {
	cases := []struct {
		name   string
		stderr string
		args   []string
	}{
		{
			name:   "codex-local",
			stderr: claudeProjectLocalRefused,
			args: []string{
				"--project", "sample", "--repo", "haynes-ops", "--local", "--agent", protocol.AgentCodex,
				"--wait", "0", "-o", "json",
			},
		},
		{
			name:   "interactive",
			stderr: claudeProjectLocalRefused,
			args:   []string{"run", "--project", "sample", "--interactive", "--repo", "haynes-ops"},
		},
		{
			name:   "restore",
			stderr: claudeProjectLocalRefused,
			args:   []string{"rescue", "restore", "--project", "sample", name + "/20060102T150405Z"},
		},
		{
			name:   "prompt",
			stderr: "agent-run: -p runs a task headless, so it cannot combine with --interactive or --local; pick one\n",
			args:   []string{"--project", "sample", "--repo", "haynes-ops", "--local", "-p", "do the task"},
		},
		{
			name:   "timeout",
			stderr: "agent-run: --timeout and --max-turns limit a task; a --local or --interactive session has none\n",
			args:   []string{"--project", "sample", "--repo", "haynes-ops", "--local", "--timeout", "40m"},
		},
		{
			name:   "max-turns",
			stderr: "agent-run: --timeout and --max-turns limit a task; a --local or --interactive session has none\n",
			args:   []string{"--project", "sample", "--repo", "haynes-ops", "--local", "--max-turns", "50"},
		},
		{
			name:   "once",
			stderr: "agent-run: --once requires a new --project task, explicit --idempotency-key, --wait=0 and -o json; no dry-run or restore\n",
			args: []string{
				"--project", "sample", "--repo", "haynes-ops", "--local", "--once",
				"--idempotency-key", "once-key", "--wait", "0", "-o", "json",
			},
		},
		{
			name:   "dry-run",
			stderr: "agent-run: --dry-run=server requires a new declared-project task\n",
			args:   []string{"--project", "sample", "--repo", "haynes-ops", "--local", "--dry-run=server"},
		},
		{
			name:   "receiver",
			stderr: "agent-run: --coordinator-instructions applies only to a new --project task; it cannot combine with rescue restore, --local or --interactive\n",
			args:   []string{"--project", "sample", "--repo", "haynes-ops", "--local", "--coordinator-instructions"},
		},
		{
			name:   "publisher",
			stderr: "agent-run: --coordinator-instruction-publisher requires a new --project task without receiver enrollment, restore, --local or --interactive\n",
			args:   []string{"--project", "sample", "--repo", "haynes-ops", "--local", "--coordinator-instruction-publisher"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			serveCreate(h, runningOn(testSession(name, ""), "talosw01"))
			h.mustRun(ExitUsage, tc.args...)
			if h.stderr.String() != tc.stderr {
				t.Fatalf("stderr = %q\nwant %q", h.stderr.String(), tc.stderr)
			}
			if got := len(h.api.requests()); got != 0 {
				t.Fatalf("got %d requests, want zero", got)
			}
		})
	}
}

func TestHelpRunDeclaredProjectClaudeLocal(t *testing.T) {
	h := newHarness(t)
	h.mustRun(ExitOK, "help", "run")
	output := h.stdout.String()
	contains(t, "stdout", output, "fresh Claude --local", "not --interactive", "cannot combine with rescue restore")
	for _, blanket := range []string{
		"Declared projects take task mode only.",
		"(tasks only; cannot combine with rescue restore)",
	} {
		if strings.Contains(output, blanket) {
			t.Fatalf("stdout still contains blanket task-only wording %q", blanket)
		}
	}
}
