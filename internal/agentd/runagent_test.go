package agentd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// streamLines is what `claude -p --output-format stream-json --verbose` prints
// for a short task.
const streamLines = `{"type":"system","subtype":"init","session_id":"conv-1","model":"claude-opus-5-5","tools":["Bash"]}
{"type":"assistant","message":{"content":[{"type":"text","text":"Looking at the README."},{"type":"tool_use","name":"Bash","input":{"command":"git   status\n--short"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","content":"ok"}]}}
not json at all
{"type":"result","subtype":"success","is_error":false,"num_turns":3,"result":"Done.","session_id":"conv-1","total_cost_usd":0.0123,"usage":{"input_tokens":100,"output_tokens":20,"cache_read_input_tokens":5000,"cache_creation_input_tokens":300}}
`

// fakeCLI writes a shell script that stands in for the claude CLI.
func fakeCLI(t *testing.T, dir, body string) string {
	t.Helper()
	p := filepath.Join(dir, "fake-claude")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func writeLaunch(t *testing.T, s Settings, l Launch) string {
	t.Helper()
	path := s.statePath(launchFile)
	if err := writeJSONFile(path, l); err != nil {
		t.Fatal(err)
	}
	return path
}

// syncBuffer is a pane the test can read while run-agent writes.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func TestRunAgentTask(t *testing.T) {
	s := testSettings(t, t.TempDir())
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "lines.jsonl"), streamLines)
	writeFile(t, s.GHTokenFile, "gh-minted\n")
	// The fake CLI checks its environment and stdin, then prints the stream.
	cli := fakeCLI(t, dir, `
read -r prompt
[ "$prompt" = "fix the docs" ] || { echo "bad prompt: $prompt" >&2; exit 9; }
[ -z "$ANTHROPIC_API_KEY" ] || { echo "metered key leaked" >&2; exit 8; }
[ "$GH_TOKEN" = "gh-minted" ] || { echo "no gh token" >&2; exit 7; }
[ "$CLAUDE_REMOTE_CONTROL_SESSION_NAME_PREFIX" = "dev-env" ] || exit 6
echo "a warning on stderr" >&2
cat "`+filepath.Join(dir, "lines.jsonl")+`"
exit 0`)
	t.Setenv("ANTHROPIC_API_KEY", "metered")
	l := Launch{
		Session: "s-1", Argv: []string{cli, "-p"}, Prompt: "fix the docs\n", Dir: dir,
		Env: []string{"CLAUDE_REMOTE_CONTROL_SESSION_NAME_PREFIX=dev-env"}, Unset: unsetForAgent, GHTokenFile: s.GHTokenFile,
		LogPath: filepath.Join(s.WorkDir(), "s-1.log"), EventsPath: s.statePath(eventsFile), ConversationID: "conv-1", BootID: "b1",
	}
	if err := os.MkdirAll(s.WorkDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	pane := &syncBuffer{}
	code := RunAgent(writeLaunch(t, s, l), pane, make(chan os.Signal), time.Second)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, pane.String())
	}

	out := pane.String()
	for _, want := range []string{
		"[agentd] task s-1 started",
		"[agentd] claude session conv-1, model claude-opus-5-5",
		"Looking at the README.",
		"-> Bash: git status --short",
		"not json at all",
		"a warning on stderr",
		"[agentd] result success after 3 turns, cost $0.0123",
		"TASK-EXIT:0",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("pane lacks %q:\n%s", want, out)
		}
	}
	if log, _ := os.ReadFile(l.LogPath); string(log) != out {
		t.Errorf("the log differs from the pane:\n%s", log)
	}
	if ev, _ := os.ReadFile(l.EventsPath); strings.Count(string(ev), "\n") != 6 || !strings.Contains(string(ev), `"total_cost_usd":0.0123`) {
		t.Errorf("events file:\n%s", ev)
	}
	var res taskResult
	if err := readJSONFile(s.statePath(resultFile), &res); err != nil {
		t.Fatal(err)
	}
	want := protocol.Usage{CostUSD: 0.0123, InputTokens: 100, OutputTokens: 20, CacheReadInputTokens: 5000, CacheCreationInputTokens: 300}
	if res.ExitCode != 0 || res.Subtype != "success" || res.NumTurns != 3 || res.Usage == nil || *res.Usage != want || res.ConversationID != "conv-1" {
		t.Errorf("result = %+v usage %+v", res, res.Usage)
	}
	if fileExists(s.statePath(pidFile)) {
		t.Error("pid file left after exit")
	}
}

func TestRunAgentTimeout(t *testing.T) {
	s := testSettings(t, t.TempDir())
	dir := t.TempDir()
	// Ignores SIGTERM, so only the group SIGKILL after the grace ends it.
	cli := fakeCLI(t, dir, `trap '' TERM; echo started; sleep 30 & wait; sleep 30`)
	l := Launch{Session: "s-2", Argv: []string{cli}, Dir: dir, LogPath: filepath.Join(dir, "s-2.log"), EventsPath: filepath.Join(dir, "ev"), Timeout: 200 * time.Millisecond}
	start := time.Now()
	code := RunAgent(writeLaunch(t, s, l), &syncBuffer{}, make(chan os.Signal), 300*time.Millisecond)
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("took %v", took)
	}
	if code != 128+int(syscall.SIGKILL) {
		t.Errorf("exit %d, want %d", code, 128+int(syscall.SIGKILL))
	}
	var res taskResult
	if err := readJSONFile(s.statePath(resultFile), &res); err != nil || !res.TimedOut {
		t.Errorf("result %+v, %v", res, err)
	}
}

func TestRunAgentForwardsSignal(t *testing.T) {
	s := testSettings(t, t.TempDir())
	dir := t.TempDir()
	cli := fakeCLI(t, dir, `trap 'kill $!; echo got-term; exit 0' TERM; sleep 30 >/dev/null 2>&1 & echo ready; wait`)
	l := Launch{Session: "s-3", Argv: []string{cli}, Dir: dir, LogPath: filepath.Join(dir, "s-3.log"), EventsPath: filepath.Join(dir, "ev")}
	sigs := make(chan os.Signal, 1)
	pane := &syncBuffer{}
	done := make(chan int, 1)
	go func() { done <- RunAgent(writeLaunch(t, s, l), pane, sigs, 5*time.Second) }()
	waitFor(t, func() bool { return strings.Contains(pane.String(), "ready") })

	// The pid file names the live CLI, with its start time.
	var p agentPid
	if err := readJSONFile(s.statePath(pidFile), &p); err != nil || !pidAlive(p) {
		t.Fatalf("pid file %+v, %v", p, err)
	}
	sigs <- syscall.SIGHUP
	select {
	case code := <-done:
		if code != 0 || !strings.Contains(pane.String(), "got-term") || !strings.Contains(pane.String(), "received hangup") {
			t.Errorf("exit %d\n%s", code, pane.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run-agent did not stop")
	}
	if pidAlive(p) {
		t.Error("the CLI is still alive")
	}
}

func TestRunAgentBadLaunch(t *testing.T) {
	s := testSettings(t, t.TempDir())
	pane := &syncBuffer{}
	if code := RunAgent(filepath.Join(t.TempDir(), "missing.json"), pane, nil, time.Second); code != 1 {
		t.Errorf("missing launch: exit %d", code)
	}
	l := Launch{Session: "s-4", Argv: []string{filepath.Join(t.TempDir(), "no-such-cli")}, Dir: t.TempDir(), LogPath: filepath.Join(t.TempDir(), "l"), EventsPath: filepath.Join(t.TempDir(), "e")}
	if code := RunAgent(writeLaunch(t, s, l), pane, nil, time.Second); code != 127 {
		t.Errorf("missing CLI: exit %d\n%s", code, pane.String())
	}
	var res taskResult
	if err := readJSONFile(s.statePath(resultFile), &res); err != nil || res.ExitCode != 127 {
		t.Errorf("result %+v %v", res, err)
	}
}

func TestRenderStreamLine(t *testing.T) {
	var b bytes.Buffer
	var res streamResult
	long := strings.Repeat("x", 300)
	in, _ := json.Marshal(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{
		map[string]any{"type": "tool_use", "name": "Read", "input": map[string]any{"file_path": "/a/b"}},
		map[string]any{"type": "tool_use", "name": "Bash", "input": map[string]any{"command": long}},
		map[string]any{"type": "tool_use", "name": "TodoWrite", "input": map[string]any{"todos": []any{}}},
		map[string]any{"type": "text", "text": "  "},
	}}})
	renderStreamLine(in, &b, &res)
	renderStreamLine([]byte(`{"type":"assistant","message":{"content":"a string"}}`), &b, &res)
	renderStreamLine([]byte(`{"type":"stream_event","event":{}}`), &b, &res)
	renderStreamLine(nil, &b, &res)
	want := "-> Read: /a/b\n-> Bash: " + strings.Repeat("x", 160) + "…\n-> TodoWrite\n"
	if b.String() != want {
		t.Errorf("got %q\nwant %q", b.String(), want)
	}
	if res.Seen {
		t.Error("a result was recorded")
	}
}

func TestProcStartTime(t *testing.T) {
	me := os.Getpid()
	st := procStartTime(me)
	if st == "" || !pidAlive(agentPid{Pid: me, Start: st}) {
		t.Fatalf("own start time %q", st)
	}
	if pidAlive(agentPid{Pid: me, Start: "1"}) {
		t.Error("a different start time counted as the same process")
	}
	if pidAlive(agentPid{}) || procStartTime(1<<30) != "" {
		t.Error("a missing pid counted as alive")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
