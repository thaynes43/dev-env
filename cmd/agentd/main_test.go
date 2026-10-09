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
	code := run(context.Background(), args, strings.NewReader(""), &stdout, &stderr, func(k string) string { return env[k] }, noRunner{})
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

func TestWorkspaceStopCommandsRefusePrivateAndUnboundedInput(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home, "AGENTD_SESSION": `{"name":"r-1","repo":"r","agent":"claude","mode":"task","model":"claude-opus-5-5","prompt":"p"}`}
	if code, _, errOut := runArgs([]string{"ctl", "stop-workspace"}, env); code != exitFailure || !strings.Contains(errOut, "shared executor") {
		t.Fatalf("private stop: code=%d,err=%q", code, errOut)
	}
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"ctl", "rescue", "--stop-agent", "--workspace-stop-proof-stdin"}, strings.NewReader(strings.Repeat("x", (16<<10)+1)), &out, &stderr, func(k string) string { return env[k] }, noRunner{})
	if code != exitFailure || !strings.Contains(stderr.String(), "16 KiB") {
		t.Fatalf("proof input: code=%d,err=%q", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".agentd", "workspace-stop-request.json")); !os.IsNotExist(err) {
		t.Fatal("private stop mutated the workspace")
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
	if code, _, errOut := runArgs([]string{"ctl", "deliver"}, env); code != exitUsage || !strings.Contains(errOut, "--from") {
		t.Errorf("ctl deliver without --from: %d %q", code, errOut)
	}
	if code, _, errOut := runArgs([]string{"ctl", "log", "--tail", "0"}, env); code != exitUsage {
		t.Errorf("ctl log --tail 0: %d %q", code, errOut)
	}
	if code, _, errOut := runArgs([]string{"ctl", "log"}, env); code != exitNoLog || !strings.Contains(errOut, "no log yet") {
		t.Errorf("ctl log with no log: %d %q", code, errOut)
	}
	// Nothing launched yet: the next boot starts fresh, and no agent ran.
	if code, out, errOut := runArgs([]string{"ctl", "prepare-restart"}, env); code != exitOK || !strings.Contains(out, `"resumable": false`) || !strings.Contains(out, `"wasRunning": false`) {
		t.Errorf("ctl prepare-restart: %d %s %s", code, out, errOut)
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

func TestCtlRescueWithNoRepos(t *testing.T) {
	home := t.TempDir()
	code, out, errOut := runArgs([]string{"ctl", "rescue"}, map[string]string{"HOME": home})
	if code != exitOK || !strings.Contains(out, `"ok": true`) || !strings.Contains(out, `"cleanAndPushed": true`) || !strings.Contains(out, `"volumeEmpty": true`) || strings.Contains(out, `"agent"`) {
		t.Errorf("ctl rescue: %d %s %s", code, out, errOut)
	}
	// --stop-agent reports what it stopped: here, nothing.
	code, out, errOut = runArgs([]string{"ctl", "rescue", "--stop-agent"}, map[string]string{"HOME": home})
	if code != exitOK || !strings.Contains(out, `"wasRunning": false`) || !strings.Contains(out, `"running": false`) {
		t.Errorf("ctl rescue --stop-agent: %d %s %s", code, out, errOut)
	}
	for _, args := range [][]string{{"ctl"}, {"ctl", "rescue", "--force"}, {"ctl", "status", "--stop-agent"}, {"ctl", "rescue", "--stop-agent", "x"}} {
		if code, _, _ := runArgs(args, map[string]string{"HOME": home}); code != exitUsage {
			t.Errorf("%q: exit %d, want usage", args, code)
		}
	}
}

// hold starts nothing and writes nothing, and ends with its context (D-55).
func TestHold(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home, "AGENTD_SESSION": `{"name":"r-1","repo":"r","agent":"claude","mode":"task","model":"claude-opus-5-5","prompt":"p"}`}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	var stdout, stderr bytes.Buffer
	go func() {
		done <- run(ctx, []string{"hold"}, strings.NewReader(""), &stdout, &stderr, func(k string) string { return env[k] }, noRunner{})
	}()
	cancel()
	if code := <-done; code != exitOK {
		t.Errorf("hold: exit %d\n%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "session=r-1") || !strings.Contains(stderr.String(), "no agent starts") {
		t.Errorf("hold's log:\n%s", stderr.String())
	}
	if entries, err := os.ReadDir(home); err != nil || len(entries) > 0 {
		t.Errorf("hold wrote to the volume: %v %v", entries, err)
	}
	if code, _, _ := runArgs([]string{"hold", "x"}, env); code != exitUsage {
		t.Errorf("hold x: %d", code)
	}
}
