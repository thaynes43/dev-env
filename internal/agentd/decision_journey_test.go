package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/codexauth"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
)

// The terminal worker reads only the synthetic recorded answer. The run-agent
// worker uses the real final admission and owned Wait path with a fixture mount
// table. Neither route can contact a provider or read production credentials.
func TestDecisionActualHeadlessToOwnedTTYJourney(t *testing.T) {
	switch os.Getenv("DECISION_JOURNEY_STAGE") {
	case "run-agent":
		home := os.Getenv("HOME")
		readWorkspaceMountInfo = func() ([]byte, error) { return workspaceMountTable(home), nil }
		if code := RunAgent(os.Getenv("DECISION_JOURNEY_LAUNCH"), os.Stdout, make(chan os.Signal), 20*time.Millisecond); code != 0 {
			t.Fatal("synthetic continuation refused", code)
		}
		return
	case "native-tty":
		terminal, err := unix.IoctlGetTermios(int(os.Stdin.Fd()), unix.TCGETS)
		if err != nil {
			t.Fatal("synthetic native child has no terminal")
		}
		terminal.Lflag &^= unix.ICANON | unix.ECHO | unix.ISIG
		terminal.Iflag &^= unix.ICRNL
		terminal.Cc[unix.VMIN], terminal.Cc[unix.VTIME] = 1, 0
		if unix.IoctlSetTermios(int(os.Stdin.Fd()), unix.TCSETS, terminal) != nil {
			t.Fatal("cannot configure fixture terminal")
		}
		_, _ = io.WriteString(os.Stdout, "\x1b[?2004h")
		receipt := os.Getenv("DECISION_JOURNEY_RECEIPT")
		if os.WriteFile(receipt+".ready", nil, 0o600) != nil {
			t.Fatal("cannot mark fixture ready")
		}
		var received strings.Builder
		var b [1]byte
		for received.Len() < 32<<10 {
			if _, err := os.Stdin.Read(b[:]); err != nil {
				t.Fatal("synthetic terminal input ended")
			}
			received.WriteByte(b[0])
			if strings.HasSuffix(received.String(), "\x1b[201~\r") {
				if os.WriteFile(receipt, []byte(received.String()), 0o600) != nil {
					t.Fatal("cannot save synthetic receipt")
				}
				return
			}
		}
		t.Fatal("fixture exceeded its finite input bound")
		return
	}
	if _, err := os.Stat("/usr/bin/tmux"); err != nil {
		if os.Getenv("CI") == "true" {
			t.Fatal("hosted verification requires the actual tmux terminal fixture")
		}
		t.Skip("actual terminal fixture requires tmux")
	}
	g := newGitFixture(t, "demo")
	s, gitRunner := g.settings(t)
	s, sess := sharedSettings(t, s, "decision-native-journey")
	s.ManagedCodexTasks, s.ManagedChildDecisions, s.RemoteBase = true, true, "https://github.com/fixture"
	s.TmuxBin = "/usr/bin/tmux"
	sess.Agent, sess.Model, sess.SessionUID, sess.Base = protocol.AgentCodex, "gpt-6.1-sol", sess.Workspace.SessionUID, "main"
	catalog, err := projectcatalog.Parse([]byte(projectTestCatalog))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := catalog.Snapshot("sample", "demo")
	if err != nil {
		t.Fatal(err)
	}
	sess.ProjectSnapshot, err = json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	s.CodexAccessFile = filepath.Join(s.Home, "projection", "access.json")
	access, err := codexauth.Encode(syntheticAgentCodexAccess(t, now, 1), now)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, s.CodexAccessFile, string(access))
	writer, err := acquireWorkspaceWriter(context.Background(), gitRunner, s, sess, now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(writer.unlock)
	s.writer = writer
	if os.MkdirAll(s.WorktreePath(sess.Name), 0o700) != nil {
		t.Fatal("cannot create fixture worktree")
	}
	receipt := filepath.Join(s.StateDir, "synthetic-answer-receipt")
	spawns, prompts, release := filepath.Join(s.StateDir, "synthetic-spawns"), filepath.Join(s.StateDir, "synthetic-prompts"), filepath.Join(s.StateDir, "synthetic-turn-release")
	testbin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	s.CodexBin = fakeCLI(t, t.TempDir(), `if [ "$1" = "--version" ]; then printf '%s\n' 'codex-cli 0.160.1'; exit 0; fi
printf '%s\n' "$1" >> "$DECISION_JOURNEY_SPAWNS"
if [ "$1" = "resume" ]; then
 printf '%s' "$2" > "$DECISION_JOURNEY_THREAD"
 exec /usr/bin/env DECISION_JOURNEY_STAGE=native-tty "$DECISION_JOURNEY_TESTBIN" -test.run=^TestDecisionActualHeadlessToOwnedTTYJourney$
fi
cat > "$DECISION_JOURNEY_PROMPTS"
printf '%s\n' '{"type":"thread.started","thread_id":"12345678-1234-1234-1234-123456789abc"}' '{"type":"turn.started"}'
i=0
while [ ! -f "$DECISION_JOURNEY_RELEASE" ] && [ "$i" -lt 100 ]; do sleep 0.05; i=$((i+1)); done
[ -f "$DECISION_JOURNEY_RELEASE" ] || exit 1
printf '%s\n' '{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1,"cached_input_tokens":0}}'
`)
	env := map[string]string{"HOME": s.Home, "AGENTD_WORKSPACE_ID": s.WorkspaceID, protocol.PodUIDEnv: s.PodUID, "AGENTD_ENABLE_CODEX_TASKS": "true", "AGENTD_ENABLE_CHILD_DECISIONS": "true", "AGENTD_REMOTE_BASE": s.RemoteBase,
		"AGENTD_CODEX_ACCESS_FILE": s.CodexAccessFile, "DECISION_JOURNEY_RECEIPT": receipt, "DECISION_JOURNEY_SPAWNS": spawns, "DECISION_JOURNEY_PROMPTS": prompts, "DECISION_JOURNEY_RELEASE": release, "DECISION_JOURNEY_THREAD": receipt + ".thread", "DECISION_JOURNEY_TESTBIN": testbin, "GOMAXPROCS": "1", "PATH": "/usr/bin:/bin"}
	data, _ := json.Marshal(sess)
	env[protocol.SessionEnv] = string(data)
	env[protocol.SessionFileEnv] = ""
	for key, value := range env {
		t.Setenv(key, value)
	}
	first, err := BuildLaunch(s, sess, protocol.Workspace{Worktree: s.WorktreePath(sess.Name), Branch: "agent/" + sess.Name}, "same-boot", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := admitWorkspaceLaunch(context.Background(), s, sess, &first); err != nil {
		t.Fatal(err)
	}
	if err := writeWorkspaceJSON(s.statePath(bootFile), bootRecord{BootID: first.BootID, Boot: protocol.BootReady, BootedAt: now}); err != nil {
		t.Fatal(err)
	}
	path := writeLaunch(t, s, first)
	done := make(chan int, 1)
	signals := make(chan os.Signal, 1)
	go func() { done <- RunAgent(path, &syncBuffer{}, signals, 20*time.Millisecond) }()
	t.Cleanup(func() {
		select {
		case signals <- os.Interrupt:
		default:
		}
		_ = os.WriteFile(release, nil, 0o600)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		current, err := privateCurrentLaunch(s)
		if err == nil && current.NativeThreadConfirmed && ownedNativeStarted(s, current) == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("finite native thread confirmation failed")
		case <-tick.C:
		}
	}
	record, err := AskDecision(ctx, s, protocol.DecisionQuestion{Question: "Which synthetic path?", Options: []string{"A", "B"}, Context: "SYNTHETIC-PRIVATE-DECISION"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		if code != 0 {
			t.Fatal("owned initial native turn failed", code)
		}
	case <-ctx.Done():
		t.Fatal("finite owned native Wait failed")
	}
	result, err := readNativeInvocation(s)
	if err != nil || result.Phase != "Exited" || !result.LeaderReaped || !result.GroupAbsent {
		t.Fatal("question native invocation lacks genuine causal exit", err)
	}
	st := CollectStatus(ctx, gitRunner, s, sess.Name, time.Now())
	raw, _ := json.Marshal(st.Decision)
	if st.Decision == nil || st.Decision.ID != record.ID || strings.Contains(string(raw), "SYNTHETIC-PRIVATE-DECISION") {
		t.Fatal("parent discovery omitted identity or exposed private context")
	}
	answer := protocol.DecisionAnswer{ID: record.ID, Text: "EXACT-SYNTHETIC-OWNER-ANSWER\nsecond line"}
	if _, err := AnswerDecision(ctx, s, answer, time.Now()); err != nil {
		t.Fatal(err)
	}
	self := fakeCLI(t, t.TempDir(), `exec /usr/bin/env DECISION_JOURNEY_STAGE=run-agent DECISION_JOURNEY_LAUNCH="$3" "$DECISION_JOURNEY_TESTBIN" -test.run=^TestDecisionActualHeadlessToOwnedTTYJourney$
`)
	socket := filepath.Join(s.Home, "decision-tmux.sock")
	base := []string{}
	for key, value := range env {
		base = append(base, key+"="+value)
	}
	runner := decisionJourneyTmuxRunner{isolatedTmuxRunner: isolatedTmuxRunner{socket: socket, ExecRunner: ExecRunner{BaseEnv: base}}, ready: receipt + ".ready"}
	t.Cleanup(func() { _ = exec.Command(s.TmuxBin, "-S", socket, "kill-server").Run() })
	d := &Daemon{S: s, Session: sess, R: runner, Self: self}
	if err := d.dispatchDecision(ctx); err != nil {
		t.Fatal("recorded answer did not resume exact owned native thread", err)
	}
	waitFixtureFile(t, ctx, receipt)
	received, err := os.ReadFile(receipt)
	if err != nil || !strings.HasPrefix(string(received), "\x1b[200~") || !strings.HasSuffix(string(received), "\x1b[201~\r") || strings.Count(string(received), "\r") != 1 || !strings.Contains(string(received), answer.Text) {
		t.Fatal("actual terminal did not receive exact framed answer and one Enter")
	}
	stored, err := readPrivateDecision(s)
	if err != nil || stored.State != "Delivered" {
		t.Fatal("terminal acknowledgement did not finish durable dispatch fence", err)
	}
	if err := d.dispatchDecision(ctx); err != nil {
		t.Fatal("delivered decision was reconsidered", err)
	}
	commands, _ := os.ReadFile(spawns)
	initial, _ := os.ReadFile(prompts)
	thread, _ := os.ReadFile(receipt + ".thread")
	if string(commands) != "exec\nresume\n" || string(initial) != sess.Prompt || string(thread) != record.ThreadID {
		t.Fatal("continuation replayed initial prompt, changed native thread or spawned another server")
	}
}

type decisionJourneyTmuxRunner struct {
	isolatedTmuxRunner
	ready string
}

func (r decisionJourneyTmuxRunner) Run(ctx context.Context, c Cmd) (Result, error) {
	result, err := r.isolatedTmuxRunner.Run(ctx, c)
	if err != nil || len(c.Args) == 0 || c.Args[0] != "new-session" {
		return result, err
	}
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(r.ready); err == nil {
			return result, nil
		}
		select {
		case <-ctx.Done():
			return Result{}, errors.New("finite synthetic native terminal startup failed")
		case <-tick.C:
		}
	}
}
