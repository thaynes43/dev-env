package agentd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func boundAnswerFixture(t *testing.T) (cursorBoundTask, cursorTaskContext, cursorBoundTaskResult) {
	t.Helper()
	plan, current := cursorTaskFixture(t)
	task, err := newCursorBoundTask(plan, current)
	if err != nil {
		t.Fatal(err)
	}
	completed := cursorBoundTaskResult{
		binding: task.binding,
		helper: cursorScaffoldResult{
			requestedModel: plan.model,
			servedModel:    "Unknown",
			sessionID:      "synthetic-session",
			stopReason:     "end_turn",
			promptSHA256:   cursorBoundPromptHash(plan.prompt),
			promptComplete: true,
		},
	}
	return task, current, completed
}

func boundAnswerExpect(task cursorBoundTask, completed cursorBoundTaskResult) answerExpectation {
	return answerExpectation{
		SessionID:      completed.helper.sessionID,
		PromptSHA256:   completed.helper.promptSHA256,
		ModelRequested: task.plan.model,
	}
}

func boundAnswerDoc(task cursorBoundTask, completed cursorBoundTaskResult, text string, chunks int) string {
	return producerDoc(boundAnswerExpect(task, completed), text, chunks)
}

type boundAnswerSpy struct {
	r    io.Reader
	n    int
	fail *testing.T
}

func (s *boundAnswerSpy) Read(p []byte) (int, error) {
	s.n++
	if s.fail != nil {
		s.fail.Helper()
		s.fail.Fatal("answer read before acceptance")
	}
	if s.r == nil {
		return 0, io.EOF
	}
	return s.r.Read(p)
}

type boundAnswerErrReader struct {
	data []byte
	err  error
}

func (r *boundAnswerErrReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, r.err
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	if len(r.data) == 0 {
		return n, r.err
	}
	return n, nil
}

func assertBoundAnswerZero(t *testing.T, got cursorBoundAnswer, err error, forbidden ...string) {
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

func TestCursorBoundAnswerMatchingUnicode(t *testing.T) {
	task, current, completed := boundAnswerFixture(t)
	text := "雪😀 grok-4.7-xhigh\n\uFFFD"
	doc := boundAnswerDoc(task, completed, text, 2)
	spy := &boundAnswerSpy{r: strings.NewReader(doc)}
	calls := 0
	got, err := acceptCursorBoundAnswer(context.Background(), task, completed, nil, func() (cursorTaskContext, error) {
		calls++
		return copyCursorTaskContext(current), nil
	}, spy)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || spy.n == 0 {
		t.Fatalf("observations=%d reads=%d", calls, spy.n)
	}
	if got.answer.Text != text || got.answer.UTF8Bytes != len(text) || got.answer.ChunkCount != 2 {
		t.Fatalf("%#v", got.answer)
	}
	if got.bound != completed || got.bound.helper.servedModel != "Unknown" || got.bound.helper.requestedModel != task.plan.model {
		t.Fatal("requested model or Unknown served model was replaced")
	}
	if got.bound.binding != task.binding || got.bound.binding.native.InvocationID == "" || len(got.bound.binding.native.LaunchDigest) != 64 {
		t.Fatal("platform binding was not retained")
	}
	if got.bound.helper.sessionID == got.bound.binding.native.Session || got.bound.helper.sessionID == got.bound.binding.native.SessionUID {
		t.Fatal("helper session was treated as the platform Session")
	}
}

func TestCursorBoundAnswerRejectsRunAndHelperBeforeRead(t *testing.T) {
	canary := "CANARY-RUN"
	cases := []struct {
		name string
		run  error
		edit func(*cursorBoundTaskResult)
	}{
		{name: "upstream error", run: errors.New(canary)},
		{name: "incomplete", edit: func(c *cursorBoundTaskResult) { c.helper.promptComplete = false }},
		{name: "wrong stop", edit: func(c *cursorBoundTaskResult) { c.helper.stopReason = "cancelled" }},
		{name: "missing session", edit: func(c *cursorBoundTaskResult) { c.helper.sessionID = "" }},
		{name: "invalid session", edit: func(c *cursorBoundTaskResult) { c.helper.sessionID = "bad session" }},
		{name: "missing hash", edit: func(c *cursorBoundTaskResult) { c.helper.promptSHA256 = "" }},
		{name: "wrong hash", edit: func(c *cursorBoundTaskResult) { c.helper.promptSHA256 = strings.Repeat("ab", 32) }},
		{name: "missing model", edit: func(c *cursorBoundTaskResult) { c.helper.requestedModel = "" }},
		{name: "wrong model", edit: func(c *cursorBoundTaskResult) { c.helper.requestedModel = "other-model" }},
		{name: "missing served model", edit: func(c *cursorBoundTaskResult) { c.helper.servedModel = "" }},
		{name: "claimed served model", edit: func(c *cursorBoundTaskResult) { c.helper.servedModel = "grok-4.7-xhigh" }},
		{name: "binding mismatch", edit: func(c *cursorBoundTaskResult) { c.binding.native.Generation++ }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task, current, completed := boundAnswerFixture(t)
			if tc.edit != nil {
				tc.edit(&completed)
			}
			spy := &boundAnswerSpy{fail: t}
			got, err := acceptCursorBoundAnswer(context.Background(), task, completed, tc.run, func() (cursorTaskContext, error) {
				t.Fatal("context read before helper acceptance")
				return current, nil
			}, spy)
			want := errBoundAnswerHelper
			if tc.run != nil {
				want = errBoundAnswerRun
			}
			assertBoundAnswerZero(t, got, err, canary, "private bounded work order", "synthetic-session")
			if !errors.Is(err, want) || (tc.run != nil && errors.Is(err, tc.run)) {
				t.Fatalf("got %v want %v", err, want)
			}
		})
	}
}

func TestCursorBoundAnswerRejectsReader(t *testing.T) {
	task, current, completed := boundAnswerFixture(t)
	expect := boundAnswerExpect(task, completed)
	text := "CANARY-ANSWER-TEXT"
	base := producerFields(expect, quoteAnswer("hello"), len("hello"), 1)
	valid := boundAnswerDoc(task, completed, text, 1)
	cases := []struct {
		name string
		raw  io.Reader
		want error
	}{
		{name: "cross session", raw: strings.NewReader(producerDoc(answerExpectation{SessionID: "other-session", PromptSHA256: expect.PromptSHA256, ModelRequested: expect.ModelRequested}, text, 1)), want: errBinding},
		{name: "cross prompt", raw: strings.NewReader(producerDoc(answerExpectation{SessionID: expect.SessionID, PromptSHA256: strings.Repeat("cd", 32), ModelRequested: expect.ModelRequested}, text, 1)), want: errBinding},
		{name: "cross model", raw: strings.NewReader(producerDoc(answerExpectation{SessionID: expect.SessionID, PromptSHA256: expect.PromptSHA256, ModelRequested: "other-model"}, text, 1)), want: errBinding},
		{name: "malformed", raw: strings.NewReader("{x}"), want: errMalformed},
		{name: "oversized", raw: bytes.NewReader(bytes.Repeat([]byte("x"), maxAnswerEncoded+1)), want: errOversized},
		{name: "truncated", raw: strings.NewReader(`{"receipt":"assistant-answer-v1"`), want: errTruncated},
		{name: "read error", raw: &boundAnswerErrReader{data: []byte(valid[:12]), err: errors.New("CANARY-READ")}, want: errRead},
		{name: "nil reader", raw: nil, want: errRead},
		{name: "document served model", raw: strings.NewReader(answerObject(replaceField(base, "servedModel", quoteAnswer("grok-4.7-xhigh")))), want: errBadServedModel},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := acceptCursorBoundAnswer(context.Background(), task, completed, nil, func() (cursorTaskContext, error) {
				return copyCursorTaskContext(current), nil
			}, tc.raw)
			assertBoundAnswerZero(t, got, err, "CANARY-ANSWER-TEXT", "CANARY-READ", "private bounded work order", expect.PromptSHA256)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
		})
	}
}

func boundAnswerMutate(c *cursorTaskContext, name string) {
	switch name {
	case "missing task":
		c.launch.WorkspaceOwner.Task = ""
	case "replaced task":
		c.launch.WorkspaceOwner.Task = "another-task"
		c.session.Name = "another-task"
		c.launch.Session = "another-task"
	case "missing session":
		c.session.Name = ""
	case "replaced session":
		uid := "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
		c.session.SessionUID, c.session.Workspace.SessionUID = uid, uid
		c.launch.SessionUID, c.launch.WorkspaceOwner.SessionUID = uid, uid
	case "missing pod":
		c.settings.PodUID = ""
	case "replaced pod":
		uid := "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
		c.settings.PodUID, c.launch.PodUID, c.launch.WorkspaceOwner.PodUID = uid, uid, uid
	case "missing boot":
		c.bootID = ""
	case "replaced boot":
		c.bootID = "replacement-boot"
		c.launch.BootID = c.bootID
	case "zero generation":
		c.launch.WorkspaceOwner.Generation = 0
	case "replaced generation":
		c.launch.WorkspaceOwner.Generation++
	case "replaced workspace":
		c.launch.WorkspaceOwner.Workspace = "other-workspace"
		c.settings.WorkspaceID = "other-workspace"
		c.session.Workspace.ID = "other-workspace"
	case "replaced repo":
		c.launch.WorkspaceOwner.Repo = "other"
		c.session.Repo = "other"
	case "replaced worktree":
		c.launch.WorkspaceOwner.Worktree = "/synthetic-other"
		c.launch.Dir = "/synthetic-other"
	default:
		panic("unknown mutation " + name)
	}
}

func TestCursorBoundAnswerRejectsContext(t *testing.T) {
	text := "雪😀\n\uFFFD"
	canary := errors.New("CANARY-CONTEXT")
	t.Run("nil callback", func(t *testing.T) {
		task, _, completed := boundAnswerFixture(t)
		spy := &boundAnswerSpy{fail: t}
		got, err := acceptCursorBoundAnswer(context.Background(), task, completed, nil, nil, spy)
		assertBoundAnswerZero(t, got, err, "private bounded work order")
		if !errors.Is(err, errBoundAnswerContext) {
			t.Fatal(err)
		}
	})
	t.Run("nil context", func(t *testing.T) {
		task, current, completed := boundAnswerFixture(t)
		spy := &boundAnswerSpy{fail: t}
		got, err := acceptCursorBoundAnswer(nil, task, completed, nil, func() (cursorTaskContext, error) { //nolint:staticcheck // SA1012: this case asserts that a nil Context is refused
			return current, nil
		}, spy)
		assertBoundAnswerZero(t, got, err)
		if !errors.Is(err, errBoundAnswerContext) {
			t.Fatal(err)
		}
	})
	t.Run("callback error before", func(t *testing.T) {
		task, _, completed := boundAnswerFixture(t)
		spy := &boundAnswerSpy{fail: t}
		got, err := acceptCursorBoundAnswer(context.Background(), task, completed, nil, func() (cursorTaskContext, error) {
			return cursorTaskContext{}, canary
		}, spy)
		assertBoundAnswerZero(t, got, err, "CANARY-CONTEXT")
		if !errors.Is(err, errBoundAnswerContext) || errors.Is(err, canary) {
			t.Fatal(err)
		}
	})
	t.Run("callback error after", func(t *testing.T) {
		task, current, completed := boundAnswerFixture(t)
		calls := 0
		got, err := acceptCursorBoundAnswer(context.Background(), task, completed, nil, func() (cursorTaskContext, error) {
			calls++
			if calls == 1 {
				return copyCursorTaskContext(current), nil
			}
			return cursorTaskContext{}, canary
		}, strings.NewReader(boundAnswerDoc(task, completed, text, 1)))
		assertBoundAnswerZero(t, got, err, "CANARY-CONTEXT", text)
		if !errors.Is(err, errBoundAnswerContext) || errors.Is(err, canary) || calls != 2 {
			t.Fatalf("calls=%d err=%v", calls, err)
		}
	})
	t.Run("canceled before", func(t *testing.T) {
		task, current, completed := boundAnswerFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		spy := &boundAnswerSpy{fail: t}
		got, err := acceptCursorBoundAnswer(ctx, task, completed, nil, func() (cursorTaskContext, error) {
			t.Fatal("callback after cancel")
			return current, nil
		}, spy)
		assertBoundAnswerZero(t, got, err)
		if !errors.Is(err, errBoundAnswerContext) {
			t.Fatal(err)
		}
	})
	t.Run("canceled during", func(t *testing.T) {
		task, current, completed := boundAnswerFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		doc := boundAnswerDoc(task, completed, text, 1)
		reader := &boundAnswerCancelReader{data: []byte(doc), cancel: cancel}
		got, err := acceptCursorBoundAnswer(ctx, task, completed, nil, func() (cursorTaskContext, error) {
			return copyCursorTaskContext(current), nil
		}, reader)
		assertBoundAnswerZero(t, got, err, text)
		if !errors.Is(err, errBoundAnswerContext) || reader.n == 0 {
			t.Fatalf("reads=%d err=%v", reader.n, err)
		}
	})

	names := []string{
		"missing task", "replaced task", "missing session", "replaced session",
		"missing pod", "replaced pod", "missing boot", "replaced boot",
		"zero generation", "replaced generation", "replaced workspace",
		"replaced repo", "replaced worktree",
	}
	for _, name := range names {
		for _, after := range []bool{false, true} {
			when := "before"
			if after {
				when = "after"
			}
			t.Run(name+"/"+when, func(t *testing.T) {
				task, current, completed := boundAnswerFixture(t)
				calls := 0
				var raw io.Reader
				if after {
					raw = strings.NewReader(boundAnswerDoc(task, completed, text, 1))
				} else {
					raw = &boundAnswerSpy{fail: t}
				}
				got, err := acceptCursorBoundAnswer(context.Background(), task, completed, nil, func() (cursorTaskContext, error) {
					calls++
					if after && calls == 1 {
						return copyCursorTaskContext(current), nil
					}
					changed := copyCursorTaskContext(current)
					boundAnswerMutate(&changed, name)
					return changed, nil
				}, raw)
				assertBoundAnswerZero(t, got, err, text, "private bounded work order")
				if !errors.Is(err, errCursorTaskBinding) {
					t.Fatal(err)
				}
				if after && calls != 2 {
					t.Fatalf("calls=%d", calls)
				}
				if !after && calls != 1 {
					t.Fatalf("calls=%d", calls)
				}
			})
		}
	}
}

type boundAnswerCancelReader struct {
	data   []byte
	cancel context.CancelFunc
	n      int
}

func (r *boundAnswerCancelReader) Read(p []byte) (int, error) {
	r.n++
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}
