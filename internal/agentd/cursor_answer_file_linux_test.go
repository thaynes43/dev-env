package agentd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func writeCursorAnswer(t *testing.T, task cursorBoundTask, data []byte) string {
	t.Helper()
	name, err := cursorAnswerBasename(task.binding)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(task.plan.worktree, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func sameAnswerContext(current cursorTaskContext) func() (cursorTaskContext, error) {
	return func() (cursorTaskContext, error) {
		return copyCursorTaskContext(current), nil
	}
}

func assertAnswerFileZero(t *testing.T, got cursorBoundAnswer, err error, forbidden ...string) {
	t.Helper()
	if err == nil || got != (cursorBoundAnswer{}) {
		t.Fatalf("got %v %#v", err, got)
	}
	msg := err.Error()
	for _, item := range forbidden {
		if item != "" && len(msg) >= len(item) && containsString(msg, item) {
			t.Fatalf("error echoes %q in %q", item, msg)
		}
	}
}

func containsString(msg, item string) bool {
	return len(item) > 0 && (msg == item || len(msg) > len(item) && (stringIndex(msg, item) >= 0))
}

func stringIndex(msg, item string) int {
	for i := 0; i+len(item) <= len(msg); i++ {
		if msg[i:i+len(item)] == item {
			return i
		}
	}
	return -1
}

func answerFDs(t *testing.T) map[string]struct{} {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	set := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		set[entry.Name()] = struct{}{}
	}
	return set
}

func sameFDSet(t *testing.T, before, after map[string]struct{}) {
	t.Helper()
	if len(before) != len(after) {
		t.Fatalf("fd count %d to %d", len(before), len(after))
	}
	for name := range before {
		if _, ok := after[name]; !ok {
			t.Fatalf("fd set changed")
		}
	}
}

func TestCursorAnswerFileBasename(t *testing.T) {
	_, _, completed := boundAnswerFixture(t)
	name, err := cursorAnswerBasename(completed.binding)
	if err != nil {
		t.Fatal(err)
	}
	if len(name) != 65 || !cursorScaffoldLabel.MatchString(name) || name[0] != 'a' {
		t.Fatalf("basename %q", name)
	}
	again, err := cursorAnswerBasename(completed.binding)
	if err != nil || again != name {
		t.Fatal(err)
	}
	other := completed.binding
	other.native.LaunchDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	second, err := cursorAnswerBasename(other)
	if err != nil || second == name {
		t.Fatal("digest did not change the leaf")
	}
	if _, err := cursorAnswerBasename(cursorTaskBinding{}); err != errCursorAnswerPath {
		t.Fatal(err)
	}
	if containsString(name, completed.binding.native.InvocationID) || containsString(name, completed.binding.native.LaunchDigest) {
		t.Fatal("basename copies an identity field")
	}
}

func TestCursorAnswerFileMatchingUnicode(t *testing.T) {
	task, current, completed := boundAnswerFixture(t)
	text := "雪😀\n\uFFFD"
	before := answerFDs(t)
	writeCursorAnswer(t, task, []byte(boundAnswerDoc(task, completed, text, 2)))
	got, err := acceptCursorAnswerFile(context.Background(), task, completed, nil, sameAnswerContext(current))
	sameFDSet(t, before, answerFDs(t))
	if err != nil {
		t.Fatal(err)
	}
	if got.answer.Text != text || got.answer.UTF8Bytes != len(text) || got.answer.ChunkCount != 2 {
		t.Fatalf("%#v", got.answer)
	}
	if got.bound != completed || got.bound.helper.servedModel != "Unknown" || got.bound.helper.requestedModel != task.plan.model {
		t.Fatal("served model or requested model changed")
	}
	if got.bound.binding != task.binding || got.bound.helper.sessionID == got.bound.binding.native.SessionUID {
		t.Fatal("platform binding was not retained")
	}
}

func TestCursorAnswerFileRejectsBeforeOpen(t *testing.T) {
	cases := []struct {
		name string
		run  error
		edit func(*cursorBoundTaskResult)
		ctx  func() context.Context
		cur  func(cursorTaskContext) func() (cursorTaskContext, error)
		want error
	}{
		{name: "upstream", run: errors.New("CANARY-RUN"), want: errBoundAnswerRun},
		{name: "incomplete", edit: func(c *cursorBoundTaskResult) { c.helper.promptComplete = false }, want: errBoundAnswerHelper},
		{name: "result binding", edit: func(c *cursorBoundTaskResult) { c.binding.native.Generation++ }, want: errBoundAnswerHelper},
		{
			name: "replaced context",
			cur: func(current cursorTaskContext) func() (cursorTaskContext, error) {
				return func() (cursorTaskContext, error) {
					changed := copyCursorTaskContext(current)
					changed.launch.WorkspaceOwner.Generation++
					return changed, nil
				}
			},
			want: errCursorTaskBinding,
		},
		{name: "nil callback", cur: func(cursorTaskContext) func() (cursorTaskContext, error) { return nil }, want: errBoundAnswerContext},
		{
			name: "canceled",
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			want: errBoundAnswerContext,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task, current, completed := boundAnswerFixture(t)
			if tc.edit != nil {
				tc.edit(&completed)
			}
			writeCursorAnswer(t, task, []byte("CANARY-ANSWER-TEXT"))
			ctx := context.Background()
			if tc.ctx != nil {
				ctx = tc.ctx()
			}
			currentFn := sameAnswerContext(current)
			if tc.cur != nil {
				currentFn = tc.cur(current)
			}
			opened := false
			got, err := acceptCursorAnswerFileAt(ctx, task, completed, tc.run, currentFn, &cursorAnswerHooks{onRead: func() { opened = true }})
			assertAnswerFileZero(t, got, err, "CANARY-RUN", "CANARY-ANSWER-TEXT", "private bounded work order")
			if opened || err != tc.want || (tc.run != nil && errors.Is(err, tc.run)) {
				t.Fatalf("opened=%v got %v", opened, err)
			}
		})
	}
}

func TestCursorAnswerFileRejectsArtifact(t *testing.T) {
	canary := "CANARY-ANSWER-TEXT"
	task, current, completed := boundAnswerFixture(t)
	valid := boundAnswerDoc(task, completed, canary, 1)
	name, err := cursorAnswerBasename(task.binding)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(task.plan.worktree, name)

	t.Run("unrelated name", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(task.plan.worktree, "answer.json"), []byte(valid), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := acceptCursorAnswerFile(context.Background(), task, completed, nil, sameAnswerContext(current))
		assertAnswerFileZero(t, got, err, canary, "answer.json")
		if err != errCursorAnswerAbsent {
			t.Fatal(err)
		}
	})
	t.Run("absent", func(t *testing.T) {
		got, err := acceptCursorAnswerFile(context.Background(), task, completed, nil, sameAnswerContext(current))
		assertAnswerFileZero(t, got, err, canary)
		if err != errCursorAnswerAbsent {
			t.Fatal(err)
		}
	})
	t.Run("partial", func(t *testing.T) {
		writeCursorAnswer(t, task, []byte(`{"receipt":"assistant-answer-v1"`))
		got, err := acceptCursorAnswerFile(context.Background(), task, completed, nil, sameAnswerContext(current))
		assertAnswerFileZero(t, got, err, canary)
		if err != errTruncated {
			t.Fatal(err)
		}
		os.Remove(path)
	})
	t.Run("malformed", func(t *testing.T) {
		writeCursorAnswer(t, task, []byte("{x}"))
		got, err := acceptCursorAnswerFile(context.Background(), task, completed, nil, sameAnswerContext(current))
		assertAnswerFileZero(t, got, err, canary)
		if err != errMalformed {
			t.Fatal(err)
		}
		os.Remove(path)
	})
	t.Run("oversized", func(t *testing.T) {
		writeCursorAnswer(t, task, bytes.Repeat([]byte("x"), maxAnswerEncoded+1))
		got, err := acceptCursorAnswerFile(context.Background(), task, completed, nil, sameAnswerContext(current))
		assertAnswerFileZero(t, got, err, canary)
		if err != errOversized {
			t.Fatal(err)
		}
		os.Remove(path)
	})
	t.Run("wrong mode", func(t *testing.T) {
		writeCursorAnswer(t, task, []byte(valid))
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := acceptCursorAnswerFile(context.Background(), task, completed, nil, sameAnswerContext(current))
		assertAnswerFileZero(t, got, err, canary)
		info, statErr := os.Lstat(path)
		if statErr != nil || info.Mode().Perm() != 0o644 || err != errCursorAnswerType {
			t.Fatalf("mode %v err %v", info.Mode(), err)
		}
		os.Remove(path)
	})
	t.Run("symlink leaf", func(t *testing.T) {
		if err := os.Symlink("CANARY-PATH", path); err != nil {
			t.Fatal(err)
		}
		got, err := acceptCursorAnswerFile(context.Background(), task, completed, nil, sameAnswerContext(current))
		assertAnswerFileZero(t, got, err, "CANARY-PATH", canary)
		if err != errCursorAnswerSymlink {
			t.Fatal(err)
		}
		os.Remove(path)
	})
	t.Run("hard link", func(t *testing.T) {
		writeCursorAnswer(t, task, []byte(valid))
		if err := os.Link(path, path+".link"); err != nil {
			t.Fatal(err)
		}
		got, err := acceptCursorAnswerFile(context.Background(), task, completed, nil, sameAnswerContext(current))
		assertAnswerFileZero(t, got, err, canary)
		if err != errCursorAnswerType {
			t.Fatal(err)
		}
		os.Remove(path + ".link")
		os.Remove(path)
	})
	t.Run("directory", func(t *testing.T) {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		got, err := acceptCursorAnswerFile(context.Background(), task, completed, nil, sameAnswerContext(current))
		assertAnswerFileZero(t, got, err, canary)
		if err != errCursorAnswerType {
			t.Fatal(err)
		}
		os.Remove(path)
	})
	t.Run("fifo", func(t *testing.T) {
		if err := unix.Mkfifo(path, 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := acceptCursorAnswerFile(context.Background(), task, completed, nil, sameAnswerContext(current))
		assertAnswerFileZero(t, got, err, canary)
		if err != errCursorAnswerType {
			t.Fatal(err)
		}
		os.Remove(path)
	})
	t.Run("symlink ancestor", func(t *testing.T) {
		root := t.TempDir()
		real := filepath.Join(root, "real")
		link := filepath.Join(root, "link")
		if err := os.Mkdir(real, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		before := answerFDs(t)
		fds := &answerCloser{}
		fd, err := openCursorAnswerDirectory(filepath.Join(link, "leaf"), fds)
		if fd >= 0 {
			fds.close(fd)
		}
		sameFDSet(t, before, answerFDs(t))
		if err != errCursorAnswerSymlink && err != errCursorAnswerPath {
			t.Fatal(err)
		}
		if containsString(err.Error(), real) || containsString(err.Error(), "CANARY-PATH") {
			t.Fatal(err)
		}
	})
	t.Run("group writable directory", func(t *testing.T) {
		writeCursorAnswer(t, task, []byte(valid))
		got, err := acceptCursorAnswerFileAt(context.Background(), task, completed, nil, sameAnswerContext(current), &cursorAnswerHooks{onRead: func() {
			if err := os.Chmod(task.plan.worktree, 0o777); err != nil {
				t.Fatal(err)
			}
		}})
		info, statErr := os.Lstat(task.plan.worktree)
		assertAnswerFileZero(t, got, err, canary)
		if statErr != nil || info.Mode().Perm() != 0o777 || err != errCursorAnswerPath {
			t.Fatalf("dir mode %v err %v", info.Mode(), err)
		}
		if err := os.Chmod(task.plan.worktree, 0o700); err != nil {
			t.Fatal(err)
		}
		os.Remove(path)
	})
}

func TestCursorAnswerFileStatValidator(t *testing.T) {
	uid := os.Getuid()
	regular := unix.Stat_t{Mode: unix.S_IFREG | 0o600, Nlink: 1, Uid: uint32(uid), Size: 4}
	if err := cursorAnswerFileStatOK(regular, uid); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		st   unix.Stat_t
		want error
	}{
		{name: "owner", st: unix.Stat_t{Mode: unix.S_IFREG | 0o600, Nlink: 1, Uid: uint32(uid) + 1, Size: 4}, want: errCursorAnswerType},
		{name: "fifo", st: unix.Stat_t{Mode: unix.S_IFIFO | 0o600, Nlink: 1, Uid: uint32(uid), Size: 0}, want: errCursorAnswerType},
		{name: "socket", st: unix.Stat_t{Mode: unix.S_IFSOCK | 0o600, Nlink: 1, Uid: uint32(uid), Size: 0}, want: errCursorAnswerType},
		{name: "device", st: unix.Stat_t{Mode: unix.S_IFCHR | 0o600, Nlink: 1, Uid: uint32(uid), Size: 0}, want: errCursorAnswerType},
		{name: "hard link", st: unix.Stat_t{Mode: unix.S_IFREG | 0o600, Nlink: 2, Uid: uint32(uid), Size: 4}, want: errCursorAnswerType},
		{name: "setuid", st: unix.Stat_t{Mode: unix.S_IFREG | unix.S_ISUID | 0o600, Nlink: 1, Uid: uint32(uid), Size: 4}, want: errCursorAnswerType},
		{name: "group writable", st: unix.Stat_t{Mode: unix.S_IFREG | 0o620, Nlink: 1, Uid: uint32(uid), Size: 4}, want: errCursorAnswerType},
		{name: "symlink", st: unix.Stat_t{Mode: unix.S_IFLNK | 0o777, Nlink: 1, Uid: uint32(uid), Size: 4}, want: errCursorAnswerSymlink},
		{name: "oversized", st: unix.Stat_t{Mode: unix.S_IFREG | 0o600, Nlink: 1, Uid: uint32(uid), Size: maxAnswerEncoded + 1}, want: errOversized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := cursorAnswerFileStatOK(tc.st, uid)
			if err != tc.want || containsString(err.Error(), "CANARY") {
				t.Fatal(err)
			}
		})
	}
	dir := unix.Stat_t{Mode: unix.S_IFDIR | 0o700, Uid: uint32(uid)}
	if err := cursorAnswerDirStatOK(dir, uid); err != nil {
		t.Fatal(err)
	}
	dir.Mode = unix.S_IFDIR | 0o770
	if err := cursorAnswerDirStatOK(dir, uid); err != errCursorAnswerPath {
		t.Fatal(err)
	}
	dir.Mode = unix.S_IFDIR | 0o700
	dir.Uid = uint32(uid) + 1
	if err := cursorAnswerDirStatOK(dir, uid); err != errCursorAnswerPath {
		t.Fatal(err)
	}
}

func TestCursorAnswerFileObservesReplacement(t *testing.T) {
	text := "雪😀\n\uFFFD"
	task, current, completed := boundAnswerFixture(t)
	name, err := cursorAnswerBasename(task.binding)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(task.plan.worktree, name)
	doc := []byte(boundAnswerDoc(task, completed, text, 1))

	t.Run("truncate", func(t *testing.T) {
		writeCursorAnswer(t, task, doc)
		got, err := acceptCursorAnswerFileAt(context.Background(), task, completed, nil, sameAnswerContext(current), &cursorAnswerHooks{afterRead: func() {
			if err := os.Truncate(path, 0); err != nil {
				t.Fatal(err)
			}
		}})
		assertAnswerFileZero(t, got, err, text, "private bounded work order")
		if err != errCursorAnswerChanged {
			t.Fatal(err)
		}
		os.Remove(path)
	})
	t.Run("leaf replace", func(t *testing.T) {
		writeCursorAnswer(t, task, doc)
		got, err := acceptCursorAnswerFileAt(context.Background(), task, completed, nil, sameAnswerContext(current), &cursorAnswerHooks{afterRead: func() {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("CANARY-ANSWER-TEXT"), 0o600); err != nil {
				t.Fatal(err)
			}
		}})
		assertAnswerFileZero(t, got, err, text, "CANARY-ANSWER-TEXT")
		if err != errCursorAnswerChanged {
			t.Fatal(err)
		}
		os.Remove(path)
	})
	t.Run("directory replace", func(t *testing.T) {
		writeCursorAnswer(t, task, doc)
		work := task.plan.worktree
		got, err := acceptCursorAnswerFileAt(context.Background(), task, completed, nil, sameAnswerContext(current), &cursorAnswerHooks{afterRead: func() {
			if err := os.Rename(work, work+"-old"); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(work, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(work, name), []byte("CANARY-ANSWER-TEXT"), 0o600); err != nil {
				t.Fatal(err)
			}
		}})
		assertAnswerFileZero(t, got, err, text, "CANARY-ANSWER-TEXT")
		if err != errCursorAnswerChanged {
			t.Fatal(err)
		}
	})
	t.Run("canceled during", func(t *testing.T) {
		task, current, completed := boundAnswerFixture(t)
		writeCursorAnswer(t, task, []byte(boundAnswerDoc(task, completed, text, 1)))
		ctx, cancel := context.WithCancel(context.Background())
		got, err := acceptCursorAnswerFileAt(ctx, task, completed, nil, sameAnswerContext(current), &cursorAnswerHooks{afterRead: cancel})
		assertAnswerFileZero(t, got, err, text)
		if err != errBoundAnswerContext {
			t.Fatal(err)
		}
	})
}

func TestCursorAnswerFileAncestorChain(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	client := filepath.Join(root, "fake-client")
	state := filepath.Join(root, "state")
	parent := filepath.Join(root, "nest", "parent")
	work := filepath.Join(parent, "work")
	if os.MkdirAll(work, 0o700) != nil || os.Mkdir(state, 0o700) != nil || os.WriteFile(client, []byte("synthetic-not-executed"), 0o700) != nil {
		t.Fatal("fixture")
	}
	if os.Chmod(work, 0o700) != nil || os.Chmod(state, 0o700) != nil || os.Chmod(client, 0o700) != nil {
		t.Fatal("mode")
	}
	plan, err := newCursorScaffoldPlan("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", client, work, state, "grok-4.7-xhigh", "private bounded work order", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	settings := Settings{PodUID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", WorkspaceID: "synthetic-workspace",
		ManagedCodexTasks: true, ManagedChildDecisions: true, Home: filepath.Dir(state), StateDir: state}
	sess := protocol.Session{Name: "synthetic-task", Repo: "demo", Agent: protocol.AgentCodex,
		Mode: protocol.ModeTask, Model: "gpt-6.1-sol", SessionUID: "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
		Workspace: &protocol.WorkspaceBinding{ID: settings.WorkspaceID, SessionUID: "cccccccc-cccc-4ccc-8ccc-cccccccccccc"}}
	launch := Launch{Provider: sess.Agent, Session: sess.Name, SessionUID: sess.SessionUID, PodUID: settings.PodUID,
		BootID: "synthetic-boot", ChildDecisions: true, NativeInvocationID: plan.invocation,
		Dir: plan.worktree, Prompt: plan.prompt, Timeout: plan.timeout, CreatedAt: time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC),
		WorkspaceOwner: &taskOwner{Version: workspaceVersion, Workspace: settings.WorkspaceID, Task: sess.Name,
			Repo: sess.Repo, Worktree: plan.worktree, SessionUID: sess.SessionUID, PodUID: settings.PodUID,
			Generation: 7, State: "owned"}}
	current := cursorTaskContext{settings, sess, launch, launch.BootID}
	task, err := newCursorBoundTask(plan, current)
	if err != nil {
		t.Fatal(err)
	}
	completed := cursorBoundTaskResult{
		binding: task.binding,
		helper: cursorScaffoldResult{
			requestedModel: plan.model, servedModel: "Unknown", sessionID: "synthetic-session",
			stopReason: "end_turn", promptSHA256: cursorBoundPromptHash(plan.prompt), promptComplete: true,
		},
	}
	text := "雪😀\n\uFFFD"
	writeCursorAnswer(t, task, []byte(boundAnswerDoc(task, completed, text, 1)))
	got, err := acceptCursorAnswerFileAt(context.Background(), task, completed, nil, sameAnswerContext(current), &cursorAnswerHooks{afterRead: func() {
		old := parent + "-old"
		if err := os.Rename(parent, old); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(parent, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(filepath.Join(old, "work"), work); err != nil {
			t.Fatal(err)
		}
	}})
	assertAnswerFileZero(t, got, err, text, "private bounded work order")
	if err != errCursorAnswerChanged {
		t.Fatal(err)
	}
	if _, err := bindCursorTask(plan, current); err != nil {
		t.Fatal(err)
	}
}

func TestCursorAnswerFileDescriptors(t *testing.T) {
	task, current, completed := boundAnswerFixture(t)
	doc := []byte(boundAnswerDoc(task, completed, "snow", 1))
	writeCursorAnswer(t, task, doc)
	before := answerFDs(t)
	if _, err := acceptCursorAnswerFile(context.Background(), task, completed, nil, sameAnswerContext(current)); err != nil {
		t.Fatal(err)
	}
	sameFDSet(t, before, answerFDs(t))

	name, err := cursorAnswerBasename(task.binding)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(task.plan.worktree, name)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	before = answerFDs(t)
	got, err := acceptCursorAnswerFile(context.Background(), task, completed, nil, sameAnswerContext(current))
	sameFDSet(t, before, answerFDs(t))
	assertAnswerFileZero(t, got, err, "snow")
	if err != errCursorAnswerAbsent {
		t.Fatal(err)
	}

	writeCursorAnswer(t, task, doc)
	before = answerFDs(t)
	got, err = acceptCursorAnswerFileAt(context.Background(), task, completed, nil, sameAnswerContext(current), &cursorAnswerHooks{closeFD: func(fd int) error {
		_ = unix.Close(fd)
		return errors.New("CANARY-CLOSE")
	}})
	sameFDSet(t, before, answerFDs(t))
	assertAnswerFileZero(t, got, err, "snow", "CANARY-CLOSE")
	if err != errCursorAnswerChanged {
		t.Fatal(err)
	}
}
