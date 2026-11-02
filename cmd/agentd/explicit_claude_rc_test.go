package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

type explicitRCRunner struct {
	newSessions int
	state       string
}

func (r *explicitRCRunner) Run(_ context.Context, c agentd.Cmd) (agentd.Result, error) {
	if len(c.Args) > 0 && c.Args[0] == "has-session" {
		// Both the pre-check and StartAgent ask before the pane exists.
		if r.newSessions == 0 {
			return agentd.Result{}, &agentd.CmdError{Name: "tmux", Sub: "has-session", ExitCode: 1}
		}
		return agentd.Result{}, nil
	}
	if len(c.Args) > 0 && c.Args[0] == "new-session" {
		r.newSessions++
		raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", os.Getpid()))
		if err != nil {
			return agentd.Result{}, err
		}
		tail := strings.Fields(string(raw)[strings.LastIndexByte(string(raw), ')')+1:])
		pid, _ := json.Marshal(map[string]any{"pid": os.Getpid(), "start": tail[19]})
		if err := os.WriteFile(filepath.Join(r.state, "agent.pid"), pid, 0o600); err != nil {
			return agentd.Result{}, err
		}
	}
	return agentd.Result{}, nil
}

func (r *explicitRCRunner) LookPath(name string) (string, error) { return name, nil }

func TestExplicitClaudeRCCommand(t *testing.T) {
	if code := run(context.Background(), []string{"ctl", "explicit-claude-rc-continuation", "extra"}, strings.NewReader("{}"), &bytes.Buffer{}, &bytes.Buffer{}, func(string) string { return "" }, &explicitRCRunner{}); code != exitUsage {
		t.Fatalf("extra arg exit %d", code)
	}
	var badOut, badErr bytes.Buffer
	if code := run(context.Background(), []string{"ctl", "explicit-claude-rc-continuation"}, strings.NewReader("{"), &badOut, &badErr, func(string) string { return "" }, &explicitRCRunner{}); code != exitFailure || badOut.Len() != 0 || !strings.Contains(badErr.String(), "invalid") {
		t.Fatalf("bad json code %d out %q err %q", code, badOut.String(), badErr.String())
	}

	home := t.TempDir()
	sessionName := "haynes-ops-1006-120000"
	worktree := filepath.Join(home, "work", sessionName)
	conv := "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	state := filepath.Join(home, ".agentd")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(state, 0o700); err != nil {
		t.Fatal(err)
	}
	sess := protocol.Session{
		Name: sessionName, Repo: "haynes-ops", Agent: protocol.AgentClaude, Mode: protocol.ModeTask,
		Model: "claude-opus-5-5", Prompt: "ORIGINAL_PROMPT_CANARY", SessionUID: "11111111-2222-4333-8444-555555555555",
		PrivateHistoryRetained: true,
	}
	doc, err := json.Marshal(sess)
	if err != nil {
		t.Fatal(err)
	}
	write := func(name string, v any) {
		t.Helper()
		body, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(state, name)
		if err := os.WriteFile(path, append(body, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("launch.json", agentd.Launch{
		Session: sessionName, ConversationID: conv, Dir: worktree, BootID: "original-boot",
		Provider: protocol.AgentClaude, SessionUID: sess.SessionUID, PodUID: "pod-uid-1",
		Argv: []string{"claude", "-p"}, Prompt: "ORIGINAL_PROMPT_CANARY",
		CreatedAt: time.Date(2026, 11, 2, 14, 0, 0, 0, time.UTC),
	})
	write("task-result.json", map[string]any{"exitCode": 0, "startedAt": time.Date(2026, 11, 2, 14, 0, 0, 0, time.UTC), "finishedAt": time.Date(2026, 11, 2, 14, 30, 0, 0, time.UTC), "conversationId": conv, "bootId": "original-boot"})
	rawProc, err := os.ReadFile("/proc/" + strconv.Itoa(os.Getpid()) + "/stat")
	if err != nil {
		t.Fatal(err)
	}
	procFields := strings.Fields(string(rawProc)[strings.LastIndexByte(string(rawProc), ')')+1:])
	write("boot.json", map[string]any{
		"daemon": map[string]any{"sessionUID": sess.SessionUID, "podUID": "pod-uid-1", "pid": os.Getpid(), "start": procFields[19]},
		"bootId": "boot-ready", "bootedAt": time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC), "boot": "ready",
		"workspace": map[string]string{"clone": filepath.Join(home, "repos", "haynes-ops"), "worktree": worktree, "branch": "agent/" + sessionName},
	})
	projectKey := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '-'
	}, worktree)
	transcript := filepath.Join(home, ".claude", "projects", projectKey, conv+".jsonl")
	if err := os.MkdirAll(filepath.Dir(transcript), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(transcript, []byte("{\"type\":\"user\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte(`{"claudeAiOauth":{"accessToken":"synthetic-test-access"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	req := agentd.ExplicitClaudeRCRequest{
		Version: 1, RequestID: "rc-user-1", SessionUID: sess.SessionUID, NativeConversation: conv,
		OriginalHOME: home, Title: "dev-env UI delivery coordinator",
		UserAuthority: "Tom 2026-11-02 phone/web remote control", FreezeEvidence: "cooperative-freeze-ack-1",
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"HOME": home, "DEV_ENV_POD_UID": "pod-uid-1", "CLAUDE_CODE_OAUTH_TOKEN": "static-token",
		"AGENTD_SESSION": string(doc),
	}
	getenv := func(k string) string { return env[k] }
	runner := &explicitRCRunner{state: state}
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"ctl", "explicit-claude-rc-continuation"}, bytes.NewReader(body), &out, &errOut, getenv, runner); code != exitOK || !strings.Contains(out.String(), `"phase": "supervised"`) || strings.Contains(out.String(), "ORIGINAL_PROMPT_CANARY") {
		t.Fatalf("start code %d out %s err %s", code, out.String(), errOut.String())
	}
	if runner.newSessions != 1 {
		t.Fatalf("new sessions %d", runner.newSessions)
	}
	launch, err := os.ReadFile(filepath.Join(state, "launch.json"))
	if err != nil || !bytes.Contains(launch, []byte("ORIGINAL_PROMPT_CANARY")) || !bytes.Contains(launch, []byte("-p")) {
		t.Fatal("original launch was not preserved")
	}
	resume, err := os.ReadFile(filepath.Join(state, "resume.json"))
	if err != nil || bytes.Contains(resume, []byte(`"-p"`)) || !bytes.Contains(resume, []byte("--remote-control")) || !bytes.Contains(resume, []byte("--resume")) {
		t.Fatalf("resume %s", resume)
	}
	var again bytes.Buffer
	if code := run(context.Background(), []string{"ctl", "explicit-claude-rc-continuation"}, bytes.NewReader(body), &again, &errOut, getenv, runner); code != exitOK || runner.newSessions != 1 {
		t.Fatalf("repeat code %d sessions %d err %s", code, runner.newSessions, errOut.String())
	}
	other := req
	other.RequestID = "rc-user-2"
	otherBody, err := json.Marshal(other)
	if err != nil {
		t.Fatal(err)
	}
	var denied bytes.Buffer
	if code := run(context.Background(), []string{"ctl", "explicit-claude-rc-continuation"}, bytes.NewReader(otherBody), &denied, &errOut, getenv, runner); code != exitFailure || !strings.Contains(errOut.String(), "different request") || runner.newSessions != 1 {
		t.Fatalf("second request code %d sessions %d err %s", code, runner.newSessions, errOut.String())
	}
}
