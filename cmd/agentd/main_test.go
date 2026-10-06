package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/agentd"
)

var versionLine = regexp.MustCompile(`^agentd \S+ \(commit \S+, go\S+, [a-z0-9]+/[a-z0-9]+\)\n$`)

// noRunner refuses every command, so a test can never start claude or tmux.
type noRunner struct{}

func (noRunner) Run(context.Context, agentd.Cmd) (agentd.Result, error) {
	return agentd.Result{}, &agentd.CmdError{Name: "refused", ExitCode: 127}
}
func (noRunner) LookPath(string) (string, error) { return "", os.ErrNotExist }

func runArgs(args []string, env map[string]string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), args, &stdout, &stderr, func(k string) string { return env[k] }, noRunner{})
	return code, stdout.String(), stderr.String()
}

func TestVersion(t *testing.T) {
	code, out, _ := runArgs([]string{"version"}, nil)
	if code != exitOK || !versionLine.MatchString(out) {
		t.Errorf("version: code %d, out %q", code, out)
	}
	if code, _, _ := runArgs([]string{"version", "x"}, nil); code != exitUsage {
		t.Errorf("version x: code %d", code)
	}
}

func TestUsage(t *testing.T) {
	if code, _, errOut := runArgs(nil, nil); code != exitUsage || !strings.Contains(errOut, "Usage: agentd") {
		t.Errorf("no args: %d %q", code, errOut)
	}
	if code, _, errOut := runArgs([]string{"fly"}, nil); code != exitUsage || !strings.Contains(errOut, `unknown command "fly"`) {
		t.Errorf("unknown: %d %q", code, errOut)
	}
	if code, out, _ := runArgs([]string{"help"}, nil); code != exitOK || !strings.Contains(out, "render") {
		t.Errorf("help: %d %q", code, out)
	}
}

func TestRender(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{
		"HOME":              home,
		"AGENTD_CONFIG_DIR": filepath.Join(home, "cfg"),
		"AGENTD_SESSION":    `{"name":"r-1","repo":"r","agent":"claude","mode":"task","model":"claude-opus-5-5","prompt":"p"}`,
	}
	code, _, errOut := runArgs([]string{"render"}, env)
	if code != exitOK {
		t.Fatalf("render: code %d\n%s", code, errOut)
	}
	if !strings.Contains(errOut, "step=claude-state state=ok") {
		t.Errorf("log lacks the claude-state step:\n%s", errOut)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude.json")); err != nil {
		t.Errorf("state file not seeded: %v", err)
	}
}

func TestRenderNeedsSession(t *testing.T) {
	code, _, errOut := runArgs([]string{"render"}, map[string]string{"HOME": t.TempDir()})
	if code != exitFailure || !strings.Contains(errOut, "AGENTD_SESSION is not set") {
		t.Errorf("code %d\n%s", code, errOut)
	}
	code, _, errOut = runArgs([]string{"render"}, map[string]string{"HOME": t.TempDir(), "AGENTD_SESSION": `{"name":"r-1","repo":"r","agent":"claude","mode":"task","model":"opus","prompt":"p"}`})
	if code != exitFailure || !strings.Contains(errOut, "aliases are refused") {
		t.Errorf("alias: code %d\n%s", code, errOut)
	}
}

func TestCtl(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home, "AGENTD_SESSION": `{"name":"r-1","repo":"r","agent":"claude","mode":"task","model":"claude-opus-5-5","prompt":"p"}`}
	code, out, errOut := runArgs([]string{"ctl", "status"}, env)
	if code != exitOK || !strings.Contains(out, `"session": "r-1"`) || !strings.Contains(out, `"state": "pending"`) {
		t.Errorf("ctl status: %d %s %s", code, out, errOut)
	}
	if code, _, _ := runArgs([]string{"ctl", "status"}, map[string]string{"HOME": home}); code != exitFailure {
		t.Errorf("ctl status without a session: %d", code)
	}
	for _, c := range []string{"rescue", "prepare-restart", "deliver"} {
		if code, _, errOut := runArgs([]string{"ctl", c}, env); code != exitFailure || !strings.Contains(errOut, "not built yet") {
			t.Errorf("ctl %s: %d %q", c, code, errOut)
		}
	}
	if code, _, _ := runArgs([]string{"ctl"}, env); code != exitUsage {
		t.Errorf("ctl: %d", code)
	}
	if code, _, _ := runArgs([]string{"ctl", "fly"}, env); code != exitUsage {
		t.Errorf("ctl fly: %d", code)
	}
}

func TestRunNeedsASession(t *testing.T) {
	if code, _, errOut := runArgs([]string{"run"}, map[string]string{"HOME": t.TempDir()}); code != exitFailure || !strings.Contains(errOut, "AGENTD_SESSION is not set") {
		t.Errorf("run: %d %s", code, errOut)
	}
	if code, _, _ := runArgs([]string{"run", "x"}, nil); code != exitUsage {
		t.Errorf("run x: %d", code)
	}
	if code, _, _ := runArgs([]string{"run-agent"}, nil); code != exitUsage {
		t.Errorf("run-agent: %d", code)
	}
	if code, out, _ := runArgs([]string{"run-agent", "--launch", filepath.Join(t.TempDir(), "none.json")}, nil); code != exitFailure || !strings.Contains(out, "run-agent") {
		t.Errorf("run-agent missing launch: %d %q", code, out)
	}
}
