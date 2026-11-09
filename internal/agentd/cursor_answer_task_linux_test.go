package agentd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func answerFlag(args []string) (string, int) {
	value, count := "", 0
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--assistant-answer-file" {
			count++
			value = args[i+1]
		}
	}
	return value, count
}

func promptArg(args []string) string {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--prompt-file" {
			return args[i+1]
		}
	}
	return ""
}

func assertAnswerTaskZero(t *testing.T, got cursorBoundAnswer, err error, forbidden ...string) {
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

func answerReceipt(task cursorBoundTask, text string) string {
	return boundAnswerDoc(task, cursorBoundTaskResult{helper: cursorScaffoldResult{
		requestedModel: task.plan.model,
		sessionID:      "synthetic-session",
		promptSHA256:   cursorBoundPromptHash(task.plan.prompt),
	}}, text, 1)
}

func writeExclusiveAnswer(t *testing.T, dir, name string, data []byte) {
	t.Helper()
	if name == "" || strings.Contains(name, "/") || name == "." || name == ".." {
		t.Fatalf("basename %q", name)
	}
	file, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatal(writeErr)
	}
}

func TestCursorAnswerTaskMatchingAnswer(t *testing.T) {
	task, current, _ := boundAnswerFixture(t)
	text := "雪😀\n\uFFFD"
	decisions, _ := cursorScaffoldDecisionPipe(t)
	var promptPath string
	starts := 0
	start := func(c cursorScaffoldCommand) (cursorScaffoldPeer, error) {
		starts++
		if c.name != cursorScaffoldPython || c.dir != task.plan.worktree || c.decisions != decisions {
			t.Fatal("command identity changed")
		}
		if len(c.env) != 3 || !strings.HasPrefix(c.env[0], "PATH=") || !strings.HasPrefix(c.env[1], "HOME=") || !strings.HasPrefix(c.env[2], "TMPDIR=") {
			t.Fatalf("env %#v", c.env)
		}
		home, tmp := strings.TrimPrefix(c.env[1], "HOME="), strings.TrimPrefix(c.env[2], "TMPDIR=")
		promptPath = promptArg(c.args)
		base := task.plan.command(promptPath, home, tmp, nil)
		want := append(append([]string{}, base.args...), "--assistant-answer-file", "")
		name, count := answerFlag(c.args)
		derived, err := cursorAnswerBasename(task.binding)
		if err != nil || count != 1 || name != derived || promptPath == "" {
			t.Fatalf("opt-in count=%d name=%q", count, name)
		}
		want[len(want)-1] = name
		if strings.Join(c.args, "\n") != strings.Join(want, "\n") {
			t.Fatalf("argv %#v", c.args)
		}
		for _, arg := range c.args {
			if strings.Contains(arg, task.plan.prompt) {
				t.Fatal("prompt bytes in argv")
			}
		}
		body, err := os.ReadFile(promptPath)
		if err != nil || string(body) != task.plan.prompt {
			t.Fatal("private prompt")
		}
		writeExclusiveAnswer(t, c.dir, name, []byte(answerReceipt(task, text)))
		return cursorScaffoldExited(cursorScaffoldStream(cursorScaffoldFrames(task.plan)), nil), nil
	}
	got, err := runCursorAnswerTask(context.Background(), task, sameAnswerContext(current), decisions, start)
	if err != nil {
		t.Fatal(err)
	}
	if starts != 1 || got.answer.Text != text || got.answer.UTF8Bytes != len(text) || got.answer.ChunkCount != 1 {
		t.Fatalf("starts=%d %#v", starts, got.answer)
	}
	if got.bound.helper.servedModel != "Unknown" || got.bound.helper.requestedModel != "grok-4.7-xhigh" || got.bound.binding != task.binding {
		t.Fatal("model or binding changed")
	}
	if _, err := os.Stat(promptPath); !os.IsNotExist(err) {
		t.Fatal("private prompt remains")
	}
	fence := filepath.Join(task.plan.stateRoot, "cursor-scaffold-"+task.plan.invocation+".json")
	recorded, err := os.ReadFile(fence)
	if err != nil || strings.Contains(string(recorded), task.plan.prompt) {
		t.Fatal("fence")
	}
	fd := int(decisions.Fd())
	if err := unix.SetNonblock(fd, true); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	n, readErr := unix.Read(fd, buf)
	if n > 0 || (!errors.Is(readErr, unix.EAGAIN) && !errors.Is(readErr, unix.EWOULDBLOCK)) {
		t.Fatal("decision pipe")
	}
}

func TestCursorAnswerTaskRefusesBeforeStart(t *testing.T) {
	task, current, _ := boundAnswerFixture(t)
	decisions, _ := cursorScaffoldDecisionPipe(t)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	cases := []struct {
		name    string
		ctx     context.Context
		current func() (cursorTaskContext, error)
		file    *os.File
		start   cursorScaffoldStart
		want    error
	}{
		{name: "nil context", current: sameAnswerContext(current), file: decisions, start: func(cursorScaffoldCommand) (cursorScaffoldPeer, error) {
			t.Fatal("started")
			return nil, nil
		}, want: errCursorAnswerTask},
		{name: "nil current", ctx: context.Background(), file: decisions, start: func(cursorScaffoldCommand) (cursorScaffoldPeer, error) {
			t.Fatal("started")
			return nil, nil
		}, want: errCursorAnswerTask},
		{name: "nil decisions", ctx: context.Background(), current: sameAnswerContext(current), start: func(cursorScaffoldCommand) (cursorScaffoldPeer, error) {
			t.Fatal("started")
			return nil, nil
		}, want: errCursorAnswerTask},
		{name: "nil start", ctx: context.Background(), current: sameAnswerContext(current), file: decisions, want: errCursorAnswerTask},
		{name: "canceled", ctx: canceled, current: sameAnswerContext(current), file: decisions, start: func(cursorScaffoldCommand) (cursorScaffoldPeer, error) {
			t.Fatal("started")
			return nil, nil
		}, want: errCursorTaskBinding},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := runCursorAnswerTask(tc.ctx, task, tc.current, tc.file, tc.start)
			assertAnswerTaskZero(t, got, err, task.plan.prompt)
			if !errors.Is(err, tc.want) {
				t.Fatal(err)
			}
		})
	}
}

func TestCursorAnswerTaskSyntheticFailures(t *testing.T) {
	cases := []struct {
		name string
		peer func(cursorBoundTask) (cursorScaffoldPeer, error)
		want error
	}{
		{name: "start", peer: func(cursorBoundTask) (cursorScaffoldPeer, error) {
			return nil, errors.New("CANARY-START")
		}, want: errCursorScaffoldProcess},
		{name: "wait", peer: func(task cursorBoundTask) (cursorScaffoldPeer, error) {
			return cursorScaffoldExited(cursorScaffoldStream(cursorScaffoldFrames(task.plan)), errors.New("CANARY-WAIT")), nil
		}, want: errCursorScaffoldProcess},
		{name: "receipt", peer: func(cursorBoundTask) (cursorScaffoldPeer, error) {
			return cursorScaffoldExited("not-a-receipt\n", nil), nil
		}, want: errCursorScaffoldReceipt},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task, current, _ := boundAnswerFixture(t)
			decisions, _ := cursorScaffoldDecisionPipe(t)
			got, err := runCursorAnswerTask(context.Background(), task, sameAnswerContext(current), decisions, func(cursorScaffoldCommand) (cursorScaffoldPeer, error) {
				return tc.peer(task)
			})
			assertAnswerTaskZero(t, got, err, "CANARY-START", "CANARY-WAIT", task.plan.prompt)
			if !errors.Is(err, tc.want) {
				t.Fatal(err)
			}
		})
	}
}

func TestCursorAnswerTaskAnswerFile(t *testing.T) {
	text := "CANARY-ANSWER-TEXT"
	cases := []struct {
		name string
		body string
		want error
	}{
		{name: "missing", want: errCursorAnswerAbsent},
		{name: "malformed", body: "{x}", want: errMalformed},
		{name: "mismatched", body: "", want: errBinding},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task, current, _ := boundAnswerFixture(t)
			decisions, _ := cursorScaffoldDecisionPipe(t)
			body := tc.body
			if tc.name == "mismatched" {
				body = boundAnswerDoc(task, cursorBoundTaskResult{helper: cursorScaffoldResult{
					requestedModel: task.plan.model, sessionID: "other-session", promptSHA256: cursorBoundPromptHash(task.plan.prompt),
				}}, text, 1)
			}
			got, err := runCursorAnswerTask(context.Background(), task, sameAnswerContext(current), decisions, func(c cursorScaffoldCommand) (cursorScaffoldPeer, error) {
				if body != "" {
					name, count := answerFlag(c.args)
					if count != 1 {
						t.Fatal(count)
					}
					writeExclusiveAnswer(t, c.dir, name, []byte(body))
				}
				return cursorScaffoldExited(cursorScaffoldStream(cursorScaffoldFrames(task.plan)), nil), nil
			})
			assertAnswerTaskZero(t, got, err, text, task.plan.prompt)
			if !errors.Is(err, tc.want) {
				t.Fatal(err)
			}
		})
	}
}

func TestCursorAnswerTaskContext(t *testing.T) {
	text := "雪😀\n\uFFFD"
	t.Run("stale before", func(t *testing.T) {
		task, current, _ := boundAnswerFixture(t)
		decisions, _ := cursorScaffoldDecisionPipe(t)
		got, err := runCursorAnswerTask(context.Background(), task, func() (cursorTaskContext, error) {
			changed := copyCursorTaskContext(current)
			changed.launch.WorkspaceOwner.Generation++
			return changed, nil
		}, decisions, func(cursorScaffoldCommand) (cursorScaffoldPeer, error) {
			t.Fatal("started")
			return nil, nil
		})
		assertAnswerTaskZero(t, got, err, text)
		if !errors.Is(err, errCursorTaskBinding) {
			t.Fatal(err)
		}
	})
	t.Run("stale after", func(t *testing.T) {
		task, current, _ := boundAnswerFixture(t)
		decisions, _ := cursorScaffoldDecisionPipe(t)
		calls := 0
		var answerPath string
		got, err := runCursorAnswerTask(context.Background(), task, func() (cursorTaskContext, error) {
			calls++
			if calls > 1 {
				changed := copyCursorTaskContext(current)
				changed.launch.WorkspaceOwner.Generation++
				return changed, nil
			}
			return copyCursorTaskContext(current), nil
		}, decisions, func(c cursorScaffoldCommand) (cursorScaffoldPeer, error) {
			name, _ := answerFlag(c.args)
			answerPath = filepath.Join(c.dir, name)
			writeExclusiveAnswer(t, c.dir, name, []byte(answerReceipt(task, text)))
			return cursorScaffoldExited(cursorScaffoldStream(cursorScaffoldFrames(task.plan)), nil), nil
		})
		assertAnswerTaskZero(t, got, err, text)
		if !errors.Is(err, errCursorTaskBinding) || calls != 2 {
			t.Fatalf("calls=%d err=%v", calls, err)
		}
		if _, statErr := os.Stat(answerPath); statErr != nil {
			t.Fatal(statErr)
		}
	})
	t.Run("canceled after", func(t *testing.T) {
		task, current, _ := boundAnswerFixture(t)
		decisions, _ := cursorScaffoldDecisionPipe(t)
		ctx, cancel := context.WithCancel(context.Background())
		calls := 0
		got, err := runCursorAnswerTask(ctx, task, func() (cursorTaskContext, error) {
			calls++
			if calls > 1 {
				cancel()
			}
			return copyCursorTaskContext(current), nil
		}, decisions, func(c cursorScaffoldCommand) (cursorScaffoldPeer, error) {
			name, _ := answerFlag(c.args)
			writeExclusiveAnswer(t, c.dir, name, []byte(answerReceipt(task, text)))
			return cursorScaffoldExited(cursorScaffoldStream(cursorScaffoldFrames(task.plan)), nil), nil
		})
		assertAnswerTaskZero(t, got, err, text)
		if !errors.Is(err, errCursorTaskBinding) || calls != 2 {
			t.Fatalf("calls=%d err=%v", calls, err)
		}
	})
}

func TestCursorAnswerTaskReplayAndDefaultCommand(t *testing.T) {
	task, current, _ := boundAnswerFixture(t)
	plain := task.plan.command("/synthetic/prompt", "/synthetic/home", "/synthetic/tmp", nil)
	for _, arg := range plain.args {
		if arg == "--assistant-answer-file" || strings.Contains(arg, task.plan.prompt) {
			t.Fatal("default command changed")
		}
	}
	decisions, _ := cursorScaffoldDecisionPipe(t)
	starts := 0
	start := func(c cursorScaffoldCommand) (cursorScaffoldPeer, error) {
		starts++
		name, count := answerFlag(c.args)
		if count != 1 {
			t.Fatal(count)
		}
		writeExclusiveAnswer(t, c.dir, name, []byte(answerReceipt(task, "ok")))
		return cursorScaffoldExited(cursorScaffoldStream(cursorScaffoldFrames(task.plan)), nil), nil
	}
	if _, err := runCursorAnswerTask(context.Background(), task, sameAnswerContext(current), decisions, start); err != nil {
		t.Fatal(err)
	}
	got, err := runCursorAnswerTask(context.Background(), task, sameAnswerContext(current), decisions, start)
	assertAnswerTaskZero(t, got, err, task.plan.prompt)
	if !errors.Is(err, errCursorScaffoldReplay) || starts != 1 {
		t.Fatalf("starts=%d err=%v", starts, err)
	}
	if _, err := os.Stat(filepath.Join(task.plan.stateRoot, "cursor-scaffold-"+task.plan.invocation+".json")); err != nil {
		t.Fatal(err)
	}
}
