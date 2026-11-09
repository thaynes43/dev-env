package agentd

// Late acceptance checks for runCursorAnswerTask. The runner's two current
// observations stay intact; these cases act on the third and fourth.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type advTaskRun struct {
	starts      int
	calls       int
	sawOriginal bool
	mutated     bool
	leaf        string
	original    []byte
	base        cursorTaskContext
	task        cursorBoundTask
	completed   cursorBoundTaskResult
	cancel      func()
	peer        *cursorScaffoldFakePeer
}

func advTaskAnswerName(args []string) (string, int) {
	value, count := "", 0
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--assistant-answer-file" {
			count++
			value = args[i+1]
		}
	}
	return value, count
}

func advTaskPlant(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func advTaskZero(t *testing.T, got cursorBoundAnswer, err error, forbidden ...string) {
	t.Helper()
	if err == nil || got != (cursorBoundAnswer{}) {
		t.Fatalf("got %v %#v", err, got)
	}
	msg := err.Error()
	for _, item := range forbidden {
		if item != "" && strings.Contains(msg, item) {
			t.Fatalf("error echoes %q in %q", item, msg)
		}
	}
}

func (run *advTaskRun) current(base cursorTaskContext, alter func(call int, run *advTaskRun) (cursorTaskContext, bool)) func() (cursorTaskContext, error) {
	return func() (cursorTaskContext, error) {
		run.calls++
		if run.calls == 2 && run.leaf != "" {
			body, err := os.ReadFile(run.leaf)
			run.sawOriginal = err == nil && string(body) == string(run.original)
		}
		if alter != nil {
			if next, use := alter(run.calls, run); use {
				return next, nil
			}
		}
		return copyCursorTaskContext(base), nil
	}
}

func advTaskChanged(base cursorTaskContext) cursorTaskContext {
	changed := copyCursorTaskContext(base)
	changed.launch.WorkspaceOwner.Generation++
	return changed
}

func advTaskDrive(t *testing.T, text string, alter func(call int, run *advTaskRun) (cursorTaskContext, bool), want error, forbidden ...string) *advTaskRun {
	t.Helper()
	task, current, completed := boundAnswerFixture(t)
	decisions, _ := cursorScaffoldDecisionPipe(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	run := &advTaskRun{
		original:  []byte(boundAnswerDoc(task, completed, text, 1)),
		base:      current,
		task:      task,
		completed: completed,
		cancel:    cancel,
	}
	start := func(c cursorScaffoldCommand) (cursorScaffoldPeer, error) {
		run.starts++
		name, count := advTaskAnswerName(c.args)
		derived, err := cursorAnswerBasename(task.binding)
		if err != nil || count != 1 || name != derived || strings.Contains(name, "/") {
			t.Fatalf("answer name count=%d", count)
		}
		run.leaf = filepath.Join(c.dir, name)
		advTaskPlant(t, run.leaf, run.original)
		run.peer = cursorScaffoldExited(cursorScaffoldStream(cursorScaffoldFrames(task.plan)), nil)
		return run.peer, nil
	}
	got, err := runCursorAnswerTask(ctx, task, run.current(current, alter), decisions, start)
	advTaskZero(t, got, err, append([]string{text, task.plan.prompt, task.plan.worktree, run.leaf}, forbidden...)...)
	signals := 0
	if run.peer != nil {
		run.peer.mu.Lock()
		signals = len(run.peer.signals)
		run.peer.mu.Unlock()
	}
	if run.starts != 1 || run.peer == nil || signals != 0 || !run.sawOriginal || !errors.Is(err, want) {
		t.Fatalf("starts=%d calls=%d signals=%d saw=%v err=%v", run.starts, run.calls, signals, run.sawOriginal, err)
	}
	return run
}

func TestCursorAnswerTaskAdversarialThirdObservation(t *testing.T) {
	run := advTaskDrive(t, "late-third-answer", func(call int, run *advTaskRun) (cursorTaskContext, bool) {
		if call != 3 {
			return cursorTaskContext{}, false
		}
		return advTaskChanged(run.base), true
	}, errCursorTaskBinding)
	if run.calls != 3 || run.mutated {
		t.Fatalf("calls=%d", run.calls)
	}
	body, err := os.ReadFile(run.leaf)
	if err != nil || string(body) != string(run.original) {
		t.Fatal("acceptance opened or replaced the artifact")
	}
}

func TestCursorAnswerTaskAdversarialFourthObservation(t *testing.T) {
	run := advTaskDrive(t, "late-fourth-answer", func(call int, run *advTaskRun) (cursorTaskContext, bool) {
		if call != 4 {
			return cursorTaskContext{}, false
		}
		return advTaskChanged(run.base), true
	}, errCursorTaskBinding)
	if run.calls != 4 {
		t.Fatalf("calls=%d", run.calls)
	}
}

func TestCursorAnswerTaskAdversarialCancelAtThird(t *testing.T) {
	run := advTaskDrive(t, "late-cancel-third", func(call int, run *advTaskRun) (cursorTaskContext, bool) {
		if call == 3 {
			run.cancel()
		}
		return cursorTaskContext{}, false
	}, errBoundAnswerContext)
	if run.calls != 3 {
		t.Fatalf("calls=%d", run.calls)
	}
}

func TestCursorAnswerTaskAdversarialCancelAtFourth(t *testing.T) {
	run := advTaskDrive(t, "late-cancel-fourth", func(call int, run *advTaskRun) (cursorTaskContext, bool) {
		if call == 4 {
			run.cancel()
		}
		return cursorTaskContext{}, false
	}, errBoundAnswerContext)
	if run.calls != 4 {
		t.Fatalf("calls=%d", run.calls)
	}
}

func TestCursorAnswerTaskAdversarialMissingBeforeOpen(t *testing.T) {
	run := advTaskDrive(t, "late-missing-answer", func(call int, run *advTaskRun) (cursorTaskContext, bool) {
		if call == 3 {
			if err := os.Remove(run.leaf); err != nil {
				t.Fatal(err)
			}
			run.mutated = true
		}
		return cursorTaskContext{}, false
	}, errCursorAnswerAbsent)
	if run.calls != 4 || !run.mutated {
		t.Fatalf("calls=%d mutated=%v", run.calls, run.mutated)
	}
	if _, err := os.Lstat(run.leaf); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestCursorAnswerTaskAdversarialReplacedBeforeOpen(t *testing.T) {
	const canary = "CANARY-TASK-REPLACED"
	var target string
	run := advTaskDrive(t, "late-replaced-answer", func(call int, run *advTaskRun) (cursorTaskContext, bool) {
		if call == 3 {
			target = filepath.Join(filepath.Dir(run.leaf), "owned-peer-target")
			advTaskPlant(t, target, []byte(canary))
			if err := os.Remove(run.leaf); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, run.leaf); err != nil {
				t.Fatal(err)
			}
			run.mutated = true
		}
		return cursorTaskContext{}, false
	}, errCursorAnswerSymlink, canary, "owned-peer-target")
	if run.calls != 4 || !run.mutated || target == "" {
		t.Fatalf("calls=%d", run.calls)
	}
	info, err := os.Lstat(run.leaf)
	body, readErr := os.ReadFile(target)
	if err != nil || info.Mode()&os.ModeSymlink == 0 || readErr != nil || string(body) != canary {
		t.Fatal("owned replacement target was not left intact")
	}
}

func TestCursorAnswerTaskAdversarialMismatchedBeforeOpen(t *testing.T) {
	const canary = "CANARY-TASK-MISMATCH"
	var wrong []byte
	run := advTaskDrive(t, "late-matched-answer", func(call int, run *advTaskRun) (cursorTaskContext, bool) {
		if call == 3 {
			completed := run.completed
			completed.helper.sessionID = "other-session"
			wrong = []byte(boundAnswerDoc(run.task, completed, canary, 1))
			advTaskPlant(t, run.leaf, wrong)
			run.mutated = true
		}
		return cursorTaskContext{}, false
	}, errBinding, canary, "other-session")
	if run.calls != 4 || !run.mutated {
		t.Fatalf("calls=%d", run.calls)
	}
	body, err := os.ReadFile(run.leaf)
	if err != nil || string(body) != string(wrong) || string(body) == string(run.original) {
		t.Fatal("mismatched artifact was not the opened leaf")
	}
}
