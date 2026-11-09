package agentd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func exclusiveAnswerSame(t *testing.T, path string, before unix.Stat_t, original []byte) {
	t.Helper()
	var after unix.Stat_t
	if unix.Stat(path, &after) != nil {
		t.Fatal("stat")
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, original) {
		t.Fatal("bytes")
	}
	if after.Dev != before.Dev || after.Ino != before.Ino || after.Mode != before.Mode || after.Size != before.Size ||
		after.Nlink != before.Nlink || after.Uid != before.Uid || after.Mtim != before.Mtim || after.Ctim != before.Ctim {
		t.Fatal("identity")
	}
}

func TestCursorAnswerTaskExclusive(t *testing.T) {
	task, current, _ := boundAnswerFixture(t)
	name, err := cursorAnswerBasename(task.binding)
	if err != nil {
		t.Fatal(err)
	}
	text := "EXCLUSIVE-ANSWER-TEXT"
	doc := boundAnswerDoc(task, cursorBoundTaskResult{helper: cursorScaffoldResult{
		requestedModel: task.plan.model,
		sessionID:      "synthetic-session",
		promptSHA256:   cursorBoundPromptHash(task.plan.prompt),
	}}, text, 1)
	if !strings.Contains(doc, `"sessionID":"synthetic-session"`) || !strings.Contains(doc, `"servedModel":"Unknown"`) || !strings.Contains(doc, task.plan.model) {
		t.Fatal("receipt shape")
	}
	path := filepath.Join(task.plan.worktree, name)
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	var before unix.Stat_t
	if unix.Stat(path, &before) != nil || before.Mode&0o7777 != 0o600 {
		t.Fatal("planted mode")
	}
	decisions, _ := cursorScaffoldDecisionPipe(t)
	starts := 0
	start := func(c cursorScaffoldCommand) (cursorScaffoldPeer, error) {
		starts++
		got, count := answerFlag(c.args)
		if count != 1 || got != name || c.dir != task.plan.worktree {
			t.Fatal("answer option")
		}
		file, openErr := os.OpenFile(filepath.Join(c.dir, got), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if file != nil {
			_ = file.Close()
		}
		if !errors.Is(openErr, os.ErrExist) {
			t.Fatal("exclusive create")
		}
		return nil, errors.New("CANARY-EXCL")
	}
	got, err := runCursorAnswerTask(context.Background(), task, sameAnswerContext(current), decisions, start)
	if starts != 1 || err != errCursorScaffoldProcess || got != (cursorBoundAnswer{}) {
		t.Fatalf("starts=%d err=%v", starts, err)
	}
	msg := err.Error()
	if strings.Contains(msg, text) || strings.Contains(msg, task.plan.prompt) || strings.Contains(msg, path) || strings.Contains(msg, "CANARY-EXCL") {
		t.Fatal("error exposes input")
	}
	exclusiveAnswerSame(t, path, before, []byte(doc))
	fence := filepath.Join(task.plan.stateRoot, "cursor-scaffold-"+task.plan.invocation+".json")
	if _, err := os.Stat(fence); err != nil {
		t.Fatal(err)
	}
	got, err = runCursorAnswerTask(context.Background(), task, sameAnswerContext(current), decisions, start)
	if starts != 1 || err != errCursorScaffoldReplay || got != (cursorBoundAnswer{}) {
		t.Fatalf("replay starts=%d err=%v", starts, err)
	}
	if strings.Contains(err.Error(), text) || strings.Contains(err.Error(), path) || strings.Contains(err.Error(), "CANARY-EXCL") {
		t.Fatal("replay exposes input")
	}
	exclusiveAnswerSame(t, path, before, []byte(doc))
	if _, err := os.Stat(fence); err != nil {
		t.Fatal(err)
	}
}
