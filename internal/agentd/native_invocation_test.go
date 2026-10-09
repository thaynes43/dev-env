package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func nativeInvocationFixture(t *testing.T) (Settings, protocol.Session, Launch, string) {
	t.Helper()
	g := newGitFixture(t, "demo")
	s, runner := g.settings(t)
	s, sess := sharedSettings(t, s, "native-decision-task")
	s.ManagedCodexTasks, s.ManagedChildDecisions = true, true
	sess.Agent, sess.Model, sess.SessionUID = protocol.AgentCodex, "gpt-6.1-sol", sess.Workspace.SessionUID
	w, err := acquireWorkspaceWriter(context.Background(), runner, s, sess, rescueNow)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.unlock)
	s.writer = w
	if err := os.MkdirAll(s.WorktreePath(sess.Name), 0o700); err != nil {
		t.Fatal(err)
	}
	spawns := filepath.Join(s.StateDir, "synthetic-native-spawns")
	prompts := filepath.Join(s.StateDir, "synthetic-initial-prompts")
	cli := fakeCLI(t, t.TempDir(), `if [ "$1" = "--version" ]; then printf '%s\n' 'codex-cli 0.160.1'; exit 0; fi
printf '%s\n' "$1" >> "$SYNTHETIC_NATIVE_SPAWNS"
if [ "$1" = "resume" ]; then exit 0; fi
cat >> "$SYNTHETIC_INITIAL_PROMPTS"
printf '%s\n' '{"type":"thread.started","thread_id":"12345678-1234-1234-1234-123456789abc"}' '{"type":"turn.started"}' '{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1,"cached_input_tokens":0}}'
`)
	l := Launch{Provider: protocol.AgentCodex, Session: sess.Name, SessionUID: sess.SessionUID, PodUID: s.PodUID,
		ChildDecisions: true, NativeInvocationID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Argv: []string{cli, "exec", "--json"},
		Dir: s.WorktreePath(sess.Name), Prompt: "EXACT-SYNTHETIC-INITIAL-PROMPT", BootID: "same-boot", CreatedAt: rescueNow,
		Env:     []string{"SYNTHETIC_NATIVE_SPAWNS=" + spawns, "SYNTHETIC_INITIAL_PROMPTS=" + prompts},
		LogPath: s.LogPath(sess.Name), EventsPath: s.statePath(eventsFile)}
	if err := admitWorkspaceLaunch(context.Background(), s, sess, &l); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(sess)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", s.Home)
	t.Setenv("AGENTD_WORKSPACE_ID", s.WorkspaceID)
	t.Setenv(protocol.PodUIDEnv, s.PodUID)
	t.Setenv(protocol.SessionEnv, string(data))
	t.Setenv(protocol.SessionFileEnv, "")
	t.Setenv("AGENTD_ENABLE_CODEX_TASKS", "true")
	t.Setenv("AGENTD_ENABLE_CHILD_DECISIONS", "true")
	path := writeLaunch(t, s, l)
	if code := RunAgent(path, &syncBuffer{}, make(chan os.Signal), 20*time.Millisecond); code != 0 {
		t.Fatal("initial native invocation refused", code)
	}
	if err := readJSONFile(path, &l); err != nil {
		t.Fatal(err)
	}
	return s, sess, l, path
}

func nextNativeInvocation(t *testing.T, l Launch) Launch {
	t.Helper()
	l.NativeInvocationID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	l.CreatedAt = time.Now().UTC()
	l.Prompt, l.TUI, l.Resume = "", true, true
	l.Argv = []string{l.Argv[0], "resume", l.ConversationID, "--no-daemon"}
	l.Continuation = nil
	return l
}

func TestNativeInvocationOwnedWaitAllowsOneSameWriterContinuation(t *testing.T) {
	s, sess, first, path := nativeInvocationFixture(t)
	result, err := readNativeInvocation(s)
	binding, bindingErr := invocationBinding(first)
	if err != nil || bindingErr != nil || !validNativeExit(result, binding) {
		t.Fatal("real owned Wait did not publish exact native exit proof", err, bindingErr)
	}
	var before taskOwner
	if err := readWorkspaceJSON(s.ownerPath(sess.Name), &before); err != nil || !before.Launched {
		t.Fatal("initial native finalization missing", err)
	}
	if code := RunAgent(path, &syncBuffer{}, make(chan os.Signal), 20*time.Millisecond); code == 0 {
		t.Fatal("first invocation was replayed")
	}
	next := nextNativeInvocation(t, first)
	if err := admitNativeContinuation(context.Background(), s, sess, first, &next); err != nil {
		t.Fatal(err)
	}
	if err := writeWorkspaceJSON(s.statePath(resumeFile), next); err != nil {
		t.Fatal(err)
	}
	if code := RunAgent(s.statePath(resumeFile), &syncBuffer{}, make(chan os.Signal), 20*time.Millisecond); code != 0 {
		t.Fatal("same-owner continuation was rejected", code)
	}
	if code := RunAgent(s.statePath(resumeFile), &syncBuffer{}, make(chan os.Signal), 20*time.Millisecond); code == 0 {
		t.Fatal("causal receipt was consumed twice")
	}
	var after taskOwner
	if readWorkspaceJSON(s.ownerPath(sess.Name), &after) != nil || !sameOwner(before, after) {
		t.Fatal("continuation reset Launched, reassigned owner or changed generation")
	}
	spawns, err := os.ReadFile(s.statePath("synthetic-native-spawns"))
	if err != nil || string(spawns) != "exec\nresume\n" {
		t.Fatal("native invocation was duplicated or first prompt replayed", err)
	}
	prompt, err := os.ReadFile(s.statePath("synthetic-initial-prompts"))
	if err != nil || string(prompt) != first.Prompt {
		t.Fatal("initial prompt changed or replayed", err)
	}
}

func TestNativeContinuationRejectsLostUncertainOrForeignCausalResult(t *testing.T) {
	s, sess, first, _ := nativeInvocationFixture(t)
	original, err := readNativeInvocation(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"missing", "running", "uncertain", "not-reaped", "group-present", "thread", "session", "pod", "boot", "generation", "digest", "mode"} {
		t.Run(change, func(t *testing.T) {
			result := original
			switch change {
			case "running":
				result.Phase = "Running"
			case "uncertain":
				result.Phase = "Uncertain"
			case "not-reaped":
				result.LeaderReaped = false
			case "group-present":
				result.GroupAbsent = false
			case "thread":
				result.Binding.ThreadID = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
			case "session":
				result.Binding.SessionUID = "another-session"
			case "pod":
				result.Binding.PodUID = "another-pod"
			case "boot":
				result.Binding.BootID = "another-boot"
			case "generation":
				result.Binding.Generation++
			case "digest":
				result.Binding.LaunchDigest = strings.Repeat("0", 64)
			}
			if err := writeWorkspaceJSON(s.statePath(nativeInvocationFile), result); err != nil {
				t.Fatal(err)
			}
			if change == "missing" {
				if err := os.Remove(s.statePath(nativeInvocationFile)); err != nil {
					t.Fatal(err)
				}
			}
			if change == "mode" {
				if err := os.Chmod(s.statePath(nativeInvocationFile), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			// No native process is running and the lock is available in every
			// case; neither observation can substitute for the causal result.
			unlock, err := nativeInvocationLock(s)
			if err != nil {
				t.Fatal(err)
			}
			unlock()
			next := nextNativeInvocation(t, first)
			if admitNativeContinuation(context.Background(), s, sess, first, &next) == nil || next.Continuation != nil {
				t.Fatal("missing or foreign owned result admitted continuation")
			}
		})
	}
}

func TestNativeInvocationLockIsPrivateExclusiveAndNotInherited(t *testing.T) {
	s := testSettings(t, t.TempDir())
	if err := os.MkdirAll(s.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	unlock, err := nativeInvocationLock(s)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if other, err := nativeInvocationLock(s); err == nil {
		other()
		t.Fatal("two native invocations acquired the private lock")
	}
	cmd := exec.Command("/bin/sh", "-c", `for fd in /proc/$$/fd/*; do readlink "$fd"; done`)
	out, _ := cmd.Output()
	if bytes.Contains(out, []byte("native-invocation.lock")) {
		t.Fatal("native child inherited the platform lock")
	}
	if err := os.Chmod(s.statePath("native-invocation.lock"), 0o644); err != nil {
		t.Fatal(err)
	}
	if other, err := nativeInvocationLock(s); err == nil {
		other()
		t.Fatal("readable private lock was accepted")
	}
}

func TestNativeInvocationLockRefusesSymlinkAndSharedState(t *testing.T) {
	s := testSettings(t, t.TempDir())
	if err := os.MkdirAll(s.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "foreign-lock")
	writeFile(t, target, "SYNTHETIC-FOREIGN-CONTENT")
	if err := os.Symlink(target, s.statePath("native-invocation.lock")); err != nil {
		t.Fatal(err)
	}
	if unlock, err := nativeInvocationLock(s); err == nil {
		unlock()
		t.Fatal("symlinked native lock accepted")
	}
	s.StateDir = filepath.Join(s.Home, "work", "platform")
	if err := os.MkdirAll(s.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if unlock, err := nativeInvocationLock(s); err == nil {
		unlock()
		t.Fatal("shared task path accepted as private platform state")
	}
}

func TestNativeOwnedWaitDoesNotInferDescendantExitFromLeaderReap(t *testing.T) {
	s, _, first, _ := nativeInvocationFixture(t)
	binding, err := invocationBinding(first)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeWorkspaceJSON(s.statePath(nativeInvocationFile), nativeInvocation{Version: 1, Binding: binding, Phase: "Running", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", "-c", "sleep 2 & exit 0")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	lease := &nativeInvocationLease{s: s}
	err = lease.afterWait(first, cmd.ProcessState, cmd.Process.Pid, time.Now())
	result, readErr := readNativeInvocation(s)
	if readErr != nil || !result.LeaderReaped {
		t.Fatal("actual direct child reap was not distinguished", readErr)
	}
	if !result.GroupAbsent && (err == nil || result.Phase != "Uncertain") {
		t.Fatal("leader exit was promoted to descendant group absence")
	}
	if result.GroupAbsent && (err != nil || result.Phase != "Exited") {
		t.Fatal("exact group absence did not retain its owned Wait proof")
	}
}

func TestNativeOwnedWaitRefusesAnotherProcessGroup(t *testing.T) {
	s, _, first, _ := nativeInvocationFixture(t)
	binding, err := invocationBinding(first)
	if err != nil {
		t.Fatal(err)
	}
	running := nativeInvocation{Version: 1, Binding: binding, Phase: "Running", StartedAt: time.Now().UTC()}
	if err := writeWorkspaceJSON(s.statePath(nativeInvocationFile), running); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	lease := &nativeInvocationLease{s: s}
	if err := lease.afterWait(first, cmd.ProcessState, cmd.Process.Pid+1, time.Now()); err == nil {
		t.Fatal("owned Wait authorized a different process group")
	}
	result, err := readNativeInvocation(s)
	if err != nil || result != running {
		t.Fatal("foreign process-group refusal changed the causal receipt", err)
	}
}
