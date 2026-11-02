package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

const (
	explicitConv    = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	explicitPrompt  = "ORIGINAL_PROMPT_CANARY"
	explicitRCTitle = "dev-env UI delivery coordinator"
)

var explicitNow = time.Date(2026, 11, 2, 15, 5, 0, 0, time.UTC)

type explicitTestPane struct {
	present     bool
	starts      int
	publishPID  bool
	startErr    bool
	queryErr    bool
	beforeStart func()
	pid         *agentPid
}

func explicitSelf(t *testing.T) string {
	t.Helper()
	p, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func explicitBoot(s Settings, sess protocol.Session, ws protocol.Workspace, id, phase string) bootRecord {
	return bootRecord{BootID: id, BootedAt: explicitNow, Boot: phase, Workspace: &ws, Daemon: &daemonProcess{SessionUID: sess.SessionUID, PodUID: s.PodUID, Pid: os.Getpid(), Start: procStartTime(os.Getpid())}}
}
func explicitFixture(t *testing.T) (Settings, protocol.Session, protocol.Workspace, ExplicitClaudeRCRequest, *fakeRunner, *explicitTestPane) {
	t.Helper()
	s, sess, ws := launchFixture(t)
	sess.SessionUID = "11111111-2222-4333-8444-555555555555"
	sess.PrivateHistoryRetained = true
	s.PodUID = "pod-uid-1"
	if err := os.MkdirAll(s.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(s.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	first := Launch{Session: sess.Name, ConversationID: explicitConv, BootID: "original-boot", Dir: ws.Worktree, Provider: protocol.AgentClaude, SessionUID: sess.SessionUID, PodUID: s.PodUID, Argv: []string{"claude", "-p"}, Prompt: explicitPrompt, CreatedAt: explicitNow.Add(-time.Hour)}
	result := taskResult{TaskResult: protocol.TaskResult{ExitCode: 0, FinishedAt: explicitNow.Add(-time.Minute)}, StartedAt: first.CreatedAt, ConversationID: explicitConv, BootID: first.BootID}
	for name, value := range map[string]any{launchFile: first, resultFile: result, bootFile: explicitBoot(s, sess, ws, "boot-ready", protocol.BootReady)} {
		if err := writeJSONFile(s.statePath(name), value); err != nil {
			t.Fatal(err)
		}
	}
	req := ExplicitClaudeRCRequest{Version: 1, RequestID: "rc-user-1", SessionUID: sess.SessionUID, NativeConversation: explicitConv, OriginalHOME: s.Home, Title: explicitRCTitle}
	path := filepath.Join(s.ClaudeConfigDir, "projects", claudeProjectKey(ws.Worktree), explicitConv+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("synthetic transcript metadata fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeCredentials(t, s, 0)
	pane := &explicitTestPane{publishPID: true}
	runner := &fakeRunner{handle: func(c Cmd) (Result, error) {
		if len(c.Args) == 0 {
			return Result{}, nil
		}
		switch c.Args[0] {
		case "has-session":
			if pane.queryErr {
				return Result{}, errors.New("unavailable")
			}
			if !pane.present {
				return Result{}, &CmdError{Name: "tmux", Sub: "has-session", ExitCode: 1}
			}
		case "new-session":
			if pane.beforeStart != nil {
				pane.beforeStart()
			}
			pane.starts++
			pane.present = true
			if pane.publishPID {
				p := agentPid{os.Getpid(), procStartTime(os.Getpid())}
				if pane.pid != nil {
					p = *pane.pid
				}
				if err := writeJSONFile(s.statePath(pidFile), p); err != nil {
					t.Fatal(err)
				}
			}
			if pane.startErr {
				return Result{}, errors.New("response lost")
			}
		}
		return Result{}, nil
	}}
	return s, sess, ws, req, runner, pane
}
func explicitStart(t *testing.T, s Settings, sess protocol.Session, req ExplicitClaudeRCRequest, r *fakeRunner) ExplicitClaudeRCRecord {
	t.Helper()
	rec, err := StartExplicitClaudeRC(context.Background(), r, s, sess, req, explicitNow, explicitSelf(t))
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestExplicitClaudeRCStartsOneInteractiveResume(t *testing.T) {
	s, sess, ws, req, r, pane := explicitFixture(t)
	first, _ := os.ReadFile(s.statePath(launchFile))
	result, _ := os.ReadFile(s.statePath(resultFile))
	credentials, _ := os.ReadFile(filepath.Join(s.ClaudeConfigDir, credentialsFileName))
	pane.beforeStart = func() {
		rec, err := readExplicitClaudeRCRecord(s)
		if err != nil || rec.Phase != explicitClaudeRCStarting || rec.ResumeSHA256 == "" || rec.WriterGeneration != 1 {
			t.Fatalf("no durable prior intent: %+v %v", rec, err)
		}
	}
	rec := explicitStart(t, s, sess, req, r)
	if rec.Phase != explicitClaudeRCSupervised || rec.Pid != os.Getpid() || rec.Start == "" || rec.WriterGeneration != 1 || pane.starts != 1 {
		t.Fatalf("record %+v starts%d", rec, pane.starts)
	}
	a, _ := os.ReadFile(s.statePath(launchFile))
	b, _ := os.ReadFile(s.statePath(resultFile))
	c, _ := os.ReadFile(filepath.Join(s.ClaudeConfigDir, credentialsFileName))
	if string(a) != string(first) || string(b) != string(result) || string(c) != string(credentials) {
		t.Fatal("original launch/result/login changed")
	}
	var l Launch
	if err := readJSONFile(s.statePath(resumeFile), &l); err != nil {
		t.Fatal(err)
	}
	if !explicitInteractiveRC(l) || l.Prompt != "" || l.ConversationID != explicitConv || !slices.Contains(l.Argv, "--remote-control") || !slices.Contains(l.Unset, "CLAUDE_CODE_OAUTH_TOKEN") || !slices.Contains(l.Unset, "CLAUDE_CODE_OAUTH_REFRESH_TOKEN") {
		t.Fatalf("invalid RC child %+v", l)
	}
	if _, err := BuildResume(s, sess, ws, l, "later", explicitNow); err == nil {
		t.Fatal("automatic retained-task resume became allowed")
	}
	if strings.Contains(strings.Join(l.Argv, " "), "when the task is complete") {
		t.Fatal("interactive launch received a task-ending guard")
	}
}
func TestExplicitClaudeRCIdempotentAndConflictPreservesResume(t *testing.T) {
	s, sess, _, req, r, pane := explicitFixture(t)
	explicitStart(t, s, sess, req, r)
	before, _ := os.ReadFile(s.statePath(resumeFile))
	if _, err := StartExplicitClaudeRC(context.Background(), r, s, sess, req, explicitNow.Add(time.Minute), explicitSelf(t)); err != nil {
		t.Fatal(err)
	}
	req.RequestID = "another-request"
	if _, err := StartExplicitClaudeRC(context.Background(), r, s, sess, req, explicitNow, explicitSelf(t)); err == nil {
		t.Fatal("second intent accepted")
	}
	after, _ := os.ReadFile(s.statePath(resumeFile))
	if pane.starts != 1 || string(after) != string(before) {
		t.Fatal("conflict changed prior resume or started another writer")
	}
}
func TestExplicitClaudeRCLostStartResponseDoesNotReplay(t *testing.T) {
	for _, pid := range []bool{false, true} {
		t.Run(strconv.FormatBool(pid), func(t *testing.T) {
			s, sess, _, req, r, pane := explicitFixture(t)
			pane.startErr = true
			pane.publishPID = pid
			if _, err := StartExplicitClaudeRC(context.Background(), r, s, sess, req, explicitNow, explicitSelf(t)); err == nil {
				t.Fatal("uncertain start reported success")
			}
			rec, err := readExplicitClaudeRCRecord(s)
			if err != nil || rec.Phase != explicitClaudeRCUncertain {
				t.Fatalf("intent lost %+v %v", rec, err)
			}
			_, _ = StartExplicitClaudeRC(context.Background(), r, s, sess, req, explicitNow.Add(time.Minute), explicitSelf(t))
			if pane.starts != 1 {
				t.Fatal("uncertain start replayed")
			}
			st := CollectStatus(context.Background(), r, s, sess, explicitNow.Add(time.Minute))
			if !pid && st.Agent.State == protocol.AgentExited {
				t.Fatal("unconfirmed start became reclaimable")
			}
		})
	}
}
func TestExplicitClaudeRCResumeWriteFailureRetainsIntent(t *testing.T) {
	s, sess, _, req, r, pane := explicitFixture(t)
	if err := os.Mkdir(s.statePath(resumeFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := StartExplicitClaudeRC(context.Background(), r, s, sess, req, explicitNow, explicitSelf(t)); err == nil {
		t.Fatal("failed resume write accepted")
	}
	if _, err := readExplicitClaudeRCRecord(s); err != nil {
		t.Fatal("failed-start intent not retained", err)
	}
	if err := os.Remove(s.statePath(resumeFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := StartExplicitClaudeRC(context.Background(), r, s, sess, req, explicitNow, explicitSelf(t)); err == nil {
		t.Fatal("failed-start intent retried")
	}
	if pane.starts != 0 {
		t.Fatal("failed resume write started a provider")
	}
}
func TestExplicitClaudeRCBoundExitAndDaemonRestartNeverReplay(t *testing.T) {
	s, sess, ws, req, r, pane := explicitFixture(t)
	child := exec.Command("sleep", "60")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	pane.pid = &agentPid{child.Process.Pid, procStartTime(child.Process.Pid)}
	explicitStart(t, s, sess, req, r)
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	first, _ := os.ReadFile(s.statePath(launchFile))
	result, _ := os.ReadFile(s.statePath(resultFile))
	pane.present = false
	if err := os.Remove(s.statePath(pidFile)); err != nil {
		t.Fatal(err)
	}
	exit := tuiExit{BootID: "boot-ready", ConversationID: explicitConv, StartedAt: explicitNow, FinishedAt: explicitNow.Add(time.Second), ExitCode: 0}
	if err := writeJSONFile(s.statePath(tuiExitFile), exit); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{S: s, R: r, Session: sess, Self: explicitSelf(t), Now: func() time.Time { return explicitNow.Add(time.Minute) }}
	for _, id := range []string{"boot-2", "boot-3"} {
		if err := writeJSONFile(s.statePath(bootFile), explicitBoot(s, sess, ws, id, protocol.BootBooting)); err != nil {
			t.Fatal(err)
		}
		step, why := d.startAgent(context.Background(), ws, Step{State: StepOK}, id)
		if why != "" || step.State == StepFail {
			t.Fatalf("reconcile %s %+v %s", id, step, why)
		}
	}
	rec, err := readExplicitClaudeRCRecord(s)
	if err != nil || rec.Phase != explicitClaudeRCExited || pane.starts != 1 {
		t.Fatalf("exit replayed %+v %v starts%d", rec, err, pane.starts)
	}
	if err := writeJSONFile(s.statePath(bootFile), explicitBoot(s, sess, ws, "boot-3", protocol.BootReady)); err != nil {
		t.Fatal(err)
	}
	st := CollectStatus(context.Background(), r, s, sess, explicitNow.Add(time.Minute))
	if st.Agent.State != protocol.AgentExited || st.Agent.Task == nil {
		t.Fatalf("bound exit %+v", st.Agent)
	}
	a, _ := os.ReadFile(s.statePath(launchFile))
	b, _ := os.ReadFile(s.statePath(resultFile))
	if string(a) != string(first) || string(b) != string(result) {
		t.Fatal("reconciliation rewrote original history")
	}
}
func TestExplicitClaudeRCUncertainStopCannotRestartOrReclaim(t *testing.T) {
	s, sess, ws, req, r, pane := explicitFixture(t)
	explicitStart(t, s, sess, req, r)
	pane.present = false
	if err := os.Remove(s.statePath(pidFile)); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(s.statePath(bootFile), explicitBoot(s, sess, ws, "boot-2", protocol.BootBooting)); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{S: s, R: r, Session: sess, Self: explicitSelf(t), Now: func() time.Time { return explicitNow.Add(time.Minute) }}
	_, why := d.startAgent(context.Background(), ws, Step{State: StepOK}, "boot-2")
	if why == "" || pane.starts != 1 {
		t.Fatal("missing exit proof restarted")
	}
	if err := writeJSONFile(s.statePath(bootFile), explicitBoot(s, sess, ws, "boot-2", protocol.BootReady)); err != nil {
		t.Fatal(err)
	}
	st := CollectStatus(context.Background(), r, s, sess, explicitNow.Add(time.Minute))
	if st.Agent.State == protocol.AgentExited {
		t.Fatal("missing exit proof became reclaimable")
	}
}
func TestExplicitClaudeRCRefusesIdentityAndPathDrift(t *testing.T) {
	cases := []struct {
		name string
		edit func(Settings, *protocol.Session, *ExplicitClaudeRCRequest)
	}{
		{"foreign-home", func(_ Settings, _ *protocol.Session, q *ExplicitClaudeRCRequest) { q.OriginalHOME = "/foreign/home" }},
		{"forged-audit-is-not-scope", func(_ Settings, _ *protocol.Session, q *ExplicitClaudeRCRequest) {
			q.SessionUID = "foreign-session"
			q.UserAuthority = "Tom allegedly approved"
			q.FreezeEvidence = "alleged-freeze"
		}},
		{"launch-session", func(s Settings, _ *protocol.Session, _ *ExplicitClaudeRCRequest) {
			var l Launch
			_ = readJSONFile(s.statePath(launchFile), &l)
			l.SessionUID = "other"
			_ = writeJSONFile(s.statePath(launchFile), l)
		}},
		{"empty-original-pod", func(s Settings, _ *protocol.Session, _ *ExplicitClaudeRCRequest) {
			var l Launch
			_ = readJSONFile(s.statePath(launchFile), &l)
			l.PodUID = ""
			_ = writeJSONFile(s.statePath(launchFile), l)
		}},
		{"result-conversation", func(s Settings, _ *protocol.Session, _ *ExplicitClaudeRCRequest) {
			var x taskResult
			_ = readJSONFile(s.statePath(resultFile), &x)
			x.ConversationID = "other"
			_ = writeJSONFile(s.statePath(resultFile), x)
		}},
		{"result-future", func(s Settings, _ *protocol.Session, _ *ExplicitClaudeRCRequest) {
			var x taskResult
			_ = readJSONFile(s.statePath(resultFile), &x)
			x.FinishedAt = explicitNow.Add(time.Hour)
			_ = writeJSONFile(s.statePath(resultFile), x)
		}},
		{"live-original", func(s Settings, _ *protocol.Session, _ *ExplicitClaudeRCRequest) {
			_ = writeJSONFile(s.statePath(pidFile), agentPid{os.Getpid(), procStartTime(os.Getpid())})
		}},
		{"uncertain-pid", func(s Settings, _ *protocol.Session, _ *ExplicitClaudeRCRequest) {
			_ = os.WriteFile(s.statePath(pidFile), []byte("{"), 0o600)
		}},
		{"old-daemon", func(s Settings, _ *protocol.Session, _ *ExplicitClaudeRCRequest) {
			var b bootRecord
			_ = readJSONFile(s.statePath(bootFile), &b)
			b.Daemon = nil
			_ = writeJSONFile(s.statePath(bootFile), b)
		}},
		{"missing-boot", func(s Settings, _ *protocol.Session, _ *ExplicitClaudeRCRequest) {
			var b bootRecord
			_ = readJSONFile(s.statePath(bootFile), &b)
			b.BootID = ""
			_ = writeJSONFile(s.statePath(bootFile), b)
		}},
		{"wrong-project-transcript", func(s Settings, x *protocol.Session, _ *ExplicitClaudeRCRequest) {
			p := filepath.Join(s.ClaudeConfigDir, "projects", claudeProjectKey(s.WorktreePath(x.Name)))
			_ = os.Rename(p, p+"-other")
		}},
		{"symlink-project", func(s Settings, x *protocol.Session, _ *ExplicitClaudeRCRequest) {
			p := filepath.Join(s.ClaudeConfigDir, "projects", claudeProjectKey(s.WorktreePath(x.Name)))
			_ = os.Rename(p, p+"-other")
			_ = os.Symlink(p+"-other", p)
		}},
		{"symlink-launch", func(s Settings, _ *protocol.Session, _ *ExplicitClaudeRCRequest) {
			p := s.statePath(launchFile)
			_ = os.Rename(p, p+".other")
			_ = os.Symlink(p+".other", p)
		}},
		{"no-existing-login", func(s Settings, _ *protocol.Session, _ *ExplicitClaudeRCRequest) {
			_ = os.Remove(filepath.Join(s.ClaudeConfigDir, credentialsFileName))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, sess, _, q, r, pane := explicitFixture(t)
			tc.edit(s, &sess, &q)
			if _, err := StartExplicitClaudeRC(context.Background(), r, s, sess, q, explicitNow, explicitSelf(t)); err == nil {
				t.Fatal("unsafe start accepted")
			}
			if pane.starts != 0 {
				t.Fatal("unsafe start spawned")
			}
		})
	}
}
func TestExplicitClaudeRCStatusNeedsActualBridgeAndScope(t *testing.T) {
	s, sess, ws, req, r, pane := explicitFixture(t)
	explicitStart(t, s, sess, req, r)
	st := CollectStatus(context.Background(), r, s, sess, explicitNow.Add(time.Second))
	if st.RemoteControl == nil || st.RemoteControl.State == protocol.RemoteRegistered || st.RemoteControl.URL != "" {
		t.Fatalf("fabricated registration %+v", st.RemoteControl)
	}
	path := filepath.Join(s.ClaudeConfigDir, "sessions", strconv.Itoa(os.Getpid())+".json")
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	body, _ := json.Marshal(map[string]any{"bridgeSessionId": "session_fixture123", "procStart": procStartTime(os.Getpid()), "status": "busy"})
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	st = CollectStatus(context.Background(), r, s, sess, explicitNow.Add(time.Second))
	if st.RemoteControl == nil || st.RemoteControl.State != protocol.RemoteRegistered || st.RemoteControl.URL == "" || st.RemoteControl.Name != explicitRCTitle {
		t.Fatalf("fixture bridge not projected %+v", st.RemoteControl)
	}
	if err := writeJSONFile(s.statePath(bootFile), explicitBoot(s, sess, ws, "later", protocol.BootReady)); err != nil {
		t.Fatal(err)
	}
	st = CollectStatus(context.Background(), r, s, sess, explicitNow.Add(time.Minute))
	if st.Agent.State != protocol.AgentBusy || st.RemoteControl == nil || st.RemoteControl.URL == "" {
		t.Fatal("daemon boot hid live continuation")
	}
	s.PodUID = "replacement-pod"
	st = CollectStatus(context.Background(), r, s, sess, explicitNow.Add(time.Minute))
	if st.Agent.State == protocol.AgentExited || st.RemoteControl != nil {
		t.Fatal("pod drift hid ownership uncertainty")
	}
	s.PodUID = "pod-uid-1"
	if pane.starts != 1 {
		t.Fatal("status started another process")
	}
}

func TestExplicitClaudeRCUnregisteredStatusReportsMissingLogin(t *testing.T) {
	s, sess, _, req, r, pane := explicitFixture(t)
	pane.publishPID = false
	explicitStart(t, s, sess, req, r)
	if err := os.Remove(filepath.Join(s.ClaudeConfigDir, credentialsFileName)); err != nil {
		t.Fatal(err)
	}
	st := CollectStatus(context.Background(), r, s, sess, explicitNow.Add(time.Second))
	if st.RemoteControl == nil || st.RemoteControl.State != protocol.RemoteLoginRequired || st.RemoteControl.URL != "" {
		t.Fatalf("unregistered missing login hidden: %+v", st.RemoteControl)
	}
}
func TestExplicitClaudeRCRejectsHeadlessFlagVariants(t *testing.T) {
	s, sess, _, req, r, _ := explicitFixture(t)
	explicitStart(t, s, sess, req, r)
	var l Launch
	_ = readJSONFile(s.statePath(resumeFile), &l)
	for _, flag := range []string{"-p", "--print", "--clear", "--fork", "--fork-session", "--session-id"} {
		x := l
		x.Argv = append(append([]string(nil), l.Argv...), flag)
		if explicitInteractiveRC(x) {
			t.Fatal("accepted", flag)
		}
	}
}

func TestExplicitClaudeRCProjectRulesStayInteractive(t *testing.T) {
	s, sess, _, req, r, _ := explicitFixture(t)
	_, project, _, _, _ := ruleInputLaunchFixture(t, protocol.AgentClaude, false)
	sess.ProjectSnapshot = project.ProjectSnapshot
	sess.Repo, sess.Base = project.Repo, project.Base
	explicitStart(t, s, sess, req, r)
	var l Launch
	if err := readJSONFile(s.statePath(resumeFile), &l); err != nil {
		t.Fatal(err)
	}
	i := slices.Index(l.Argv, "--append-system-prompt-file")
	if i < 0 || i+1 == len(l.Argv) {
		t.Fatal("accepted project rules were dropped")
	}
	rules, err := os.ReadFile(l.Argv[i+1])
	if err != nil || !strings.Contains(string(rules), "PROJECT_RULE_IDENTIFIER") {
		t.Fatalf("project rule input missing: %v", err)
	}
	guard, ok := argvFlagValue(l.Argv, "--append-system-prompt")
	if !ok || !strings.Contains(guard, "worktree") || strings.Contains(guard, "When the task is complete") {
		t.Fatal("interactive guard missing or task-ending prompt retained")
	}
}

func TestExplicitClaudeRCRemovedPIDDoesNotHideLiveWriter(t *testing.T) {
	s, sess, _, req, r, pane := explicitFixture(t)
	explicitStart(t, s, sess, req, r)
	pane.present = false
	if err := os.Remove(s.statePath(pidFile)); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(s.statePath(tuiExitFile), tuiExit{BootID: "boot-ready", ConversationID: explicitConv, StartedAt: explicitNow, FinishedAt: explicitNow.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	if _, err := StartExplicitClaudeRC(context.Background(), r, s, sess, req, explicitNow.Add(time.Minute), explicitSelf(t)); err == nil {
		t.Fatal("live recorded PID accepted forged stop file")
	}
	st := CollectStatus(context.Background(), r, s, sess, explicitNow.Add(time.Minute))
	if st.Agent.State == protocol.AgentExited || pane.starts != 1 {
		t.Fatal("live writer hidden or replayed")
	}
}

func TestExplicitClaudeRCDeadOriginalPIDIsAStartingWindow(t *testing.T) {
	s, sess, _, req, r, pane := explicitFixture(t)
	pane.publishPID = false
	if err := writeJSONFile(s.statePath(pidFile), agentPid{Pid: 99999999, Start: "dead-original-process"}); err != nil {
		t.Fatal(err)
	}
	rec := explicitStart(t, s, sess, req, r)
	if rec.Phase != explicitClaudeRCStarting || pane.starts != 1 {
		t.Fatalf("stale dead PID became failed start: %+v", rec)
	}
	if _, err := StartExplicitClaudeRC(context.Background(), r, s, sess, req, explicitNow.Add(time.Second), explicitSelf(t)); err != nil {
		t.Fatal(err)
	}
	if pane.starts != 1 {
		t.Fatal("starting window duplicated writer")
	}
}
