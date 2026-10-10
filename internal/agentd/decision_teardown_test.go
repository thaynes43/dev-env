package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/codexauth"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
)

// Pause a genuine owned Wait producer after its receipt, while its wrapper
// still owns the lifetime lock. No provider, native model or real tmux runs.
func TestDecisionWaitsForWrapperAndTmuxTeardownBeforeReservation(t *testing.T) {
	s, sess, first, _ := nativeInvocationFixture(t)
	s.RemoteBase, sess.Base = "https://github.com/fixture", "main"
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
	s.CodexBin = fakeCLI(t, t.TempDir(), "cat >/dev/null\n")
	current := nextNativeInvocation(t, first)
	current.Argv[0] = s.CodexBin
	if err := admitNativeContinuation(context.Background(), s, sess, first, &current); err != nil {
		t.Fatal(err)
	}
	if err := writeWorkspaceJSON(s.statePath(resumeFile), current); err != nil {
		t.Fatal(err)
	}
	if err := writeWorkspaceJSON(s.statePath(bootFile), bootRecord{BootID: current.BootID, Boot: protocol.BootReady, BootedAt: now}); err != nil {
		t.Fatal(err)
	}
	lease, err := beginNativeInvocation(current, s.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(lease.unlock) }
	t.Cleanup(release)
	if err := lease.beforeSpawn(current, now); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(current.Argv[0], current.Argv[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = input.Close(); _ = cmd.Process.Kill() })
	if err := lease.recordStarted(current, cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(s.statePath(pidFile), agentPid{cmd.Process.Pid, procStartTime(cmd.Process.Pid)}); err != nil {
		t.Fatal(err)
	}
	question, err := AskDecision(context.Background(), s, protocol.DecisionQuestion{Question: "Synthetic teardown choice?"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AnswerDecision(context.Background(), s, protocol.DecisionAnswer{ID: question.ID, Text: "SYNTHETIC-TEARDOWN-ANSWER"}, now); err != nil {
		t.Fatal(err)
	}
	_ = input.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := lease.afterWait(current, cmd.ProcessState, cmd.Process.Pid, time.Now()); err != nil {
		t.Fatal(err)
	}
	causal, err := readNativeInvocation(s)
	binding, bindingErr := invocationBinding(current)
	if err != nil || bindingErr != nil || !validNativeExit(causal, binding) {
		t.Fatal("fixture lacks genuine owned Wait and group-absence proof", err, bindingErr)
	}
	var oldTmux, starts atomic.Int32
	oldTmux.Store(1)
	observed := make(chan struct{})
	var observedOnce sync.Once
	runner := &fakeRunner{handle: func(c Cmd) (Result, error) {
		switch c.Args[0] {
		case "has-session":
			observedOnce.Do(func() { close(observed) })
			if oldTmux.Load() != 0 {
				return Result{}, nil
			}
			return Result{ExitCode: 1, Stderr: []byte("can't find session: agent")}, &CmdError{Name: s.TmuxBin, Sub: "has-session", ExitCode: 1}
		case "new-session":
			starts.Add(1)
			return Result{}, errors.New("synthetic ambiguous start acknowledgement")
		default:
			return Result{}, errors.New("unexpected synthetic transport")
		}
	}}
	answered, _ := ReadDecision(context.Background(), s)
	serveDecisionAuthority(t, &s, *answered.Decision)
	d := &Daemon{S: s, Session: sess, R: runner}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- d.dispatchDecision(ctx) }()
	assertUnconsumed := func() {
		t.Helper()
		record, err := readPrivateDecision(s)
		result, resultErr := readNativeInvocation(s)
		var saved Launch
		launchErr := readJSONFile(s.statePath(resumeFile), &saved)
		if err != nil || resultErr != nil || launchErr != nil || record.State != "Answered" || record.ResumeInvocationID != "" || result != causal || saved.NativeInvocationID != current.NativeInvocationID || starts.Load() != 0 {
			t.Fatal("answer was consumed or a successor launched before teardown", err, resultErr, launchErr)
		}
	}
	select {
	case err := <-done:
		t.Fatal("dispatcher passed the held original lifetime lock", err)
	case <-time.After(100 * time.Millisecond):
	}
	assertUnconsumed()
	// Complete the prior wrapper's cleanup, then release its lock. The old
	// tmux wrapper still exists, so a new answer reservation must still wait.
	if err := os.Remove(s.statePath(pidFile)); err != nil {
		t.Fatal(err)
	}
	release()
	select {
	case <-observed:
	case <-ctx.Done():
		t.Fatal("dispatcher did not inspect exact old tmux session")
	}
	assertUnconsumed()
	oldTmux.Store(0)
	select {
	case err := <-done:
		if err == nil || starts.Load() != 1 {
			t.Fatal("ready continuation did not attempt exactly one native start", err, starts.Load())
		}
	case <-ctx.Done():
		t.Fatal("dispatcher did not continue after both teardown barriers")
	}
	stored, err := readPrivateDecision(s)
	if err != nil || stored.State != "Uncertain" {
		t.Fatal("ambiguous new start lost its one-shot fence", err)
	}
	if err := d.dispatchDecision(context.Background()); err != nil || starts.Load() != 1 {
		t.Fatal("ambiguous continuation was retried", err)
	}
}

func TestDecisionTmuxAbsenceRejectsUnknownStatus(t *testing.T) {
	for _, detail := range []string{"", "permission denied", "invalid command", "error connecting to synthetic (Permission denied)"} {
		if decisionTmuxAbsent(Result{Stderr: []byte(detail)}, &CmdError{ExitCode: 1}) {
			t.Fatal("unknown tmux failure supplied teardown readiness")
		}
	}
}

func TestDecisionTeardownCancelledProbeCannotSupplyReadiness(t *testing.T) {
	s := testSettings(t, t.TempDir())
	if err := os.MkdirAll(s.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := &fakeRunner{handle: func(Cmd) (Result, error) {
		cancel()
		return Result{Stderr: []byte("can't find session: agent")}, &CmdError{ExitCode: 1}
	}}
	if err := waitDecisionTeardown(ctx, runner, s); err == nil {
		t.Fatal("cancelled absence probe authorized answer reservation")
	}
}
