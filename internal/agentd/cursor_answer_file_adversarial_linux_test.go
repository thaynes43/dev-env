package agentd

// Adversarial checks for the pinned answer-file adapter. They use owned
// temporary fixtures and the synthetic callbacks only. They do not replace a
// higher ancestor; that regression belongs to the primary author.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func advAnswerPlant(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func advAnswerZero(t *testing.T, got cursorBoundAnswer, err error, forbidden ...string) {
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

type advSuppliedClose struct {
	regular  bool
	dir      bool
	statErr  error
	closeErr error
	hookErr  error
}

func advCloseSupplied(fd int, failRegular bool, log *[]advSuppliedClose) error {
	var st unix.Stat_t
	rec := advSuppliedClose{}
	if err := unix.Fstat(fd, &st); err != nil {
		rec.statErr = err
	} else {
		switch st.Mode & unix.S_IFMT {
		case unix.S_IFREG:
			rec.regular = true
		case unix.S_IFDIR:
			rec.dir = true
		}
	}
	rec.closeErr = unix.Close(fd)
	if failRegular && rec.regular {
		rec.hookErr = errors.New("synthetic-regular-close")
	} else {
		rec.hookErr = rec.closeErr
	}
	*log = append(*log, rec)
	return rec.hookErr
}

func TestCursorAnswerFileAdversarialIdentityIsNotAPath(t *testing.T) {
	task, current, completed := boundAnswerFixture(t)
	name, err := cursorAnswerBasename(task.binding)
	if err != nil {
		t.Fatal(err)
	}
	id := task.binding.native.InvocationID
	digest := task.binding.native.LaunchDigest
	if strings.Contains(name, "/") || strings.Contains(name, id) || strings.Contains(name, digest) || filepath.Base(name) != name {
		t.Fatalf("leaf %q redirects to identity material", name)
	}
	nulled := task.binding
	nulled.native.InvocationID = id + "\x00"
	if _, err := cursorAnswerBasename(nulled); err != errCursorAnswerPath {
		t.Fatal(err)
	}
	decoy := "CANARY-IDENTITY-PATH"
	text := "owned-answer"
	advAnswerPlant(t, filepath.Join(task.plan.worktree, id), []byte(decoy))
	advAnswerPlant(t, filepath.Join(task.plan.worktree, name), []byte(boundAnswerDoc(task, completed, text, 1)))
	got, err := acceptCursorAnswerFile(context.Background(), task, completed, nil, sameAnswerContext(current))
	if err != nil {
		t.Fatal(err)
	}
	if got.answer.Text != text || strings.Contains(got.answer.Text, decoy) || strings.Contains(got.answer.Text, id) {
		t.Fatalf("%q", got.answer.Text)
	}
	left, err := os.ReadFile(filepath.Join(task.plan.worktree, id))
	if err != nil || string(left) != decoy {
		t.Fatal("identity-named decoy was used as the answer path")
	}

	root := filepath.Dir(task.plan.worktree)
	other := filepath.Join(root, "other-work")
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatal(err)
	}
	advAnswerPlant(t, filepath.Join(other, name), []byte("CANARY-OTHER-WORKTREE"))
	swapped := task
	swapped.plan.worktree = other
	swapped.binding.worktree = other
	done := completed
	done.binding.worktree = other
	opened := false
	got, err = acceptCursorAnswerFileAt(context.Background(), swapped, done, nil, sameAnswerContext(current), &cursorAnswerHooks{onRead: func() { opened = true }})
	advAnswerZero(t, got, err, "CANARY-OTHER-WORKTREE", other, text)
	if opened || err != errCursorTaskBinding {
		t.Fatalf("opened=%v err=%v", opened, err)
	}
}

func TestCursorAnswerFileAdversarialRefusesUncleanPaths(t *testing.T) {
	root := t.TempDir()
	keep := filepath.Join(root, "keep")
	if err := os.Mkdir(keep, 0o700); err != nil {
		t.Fatal(err)
	}
	advAnswerPlant(t, filepath.Join(keep, "canary"), []byte("CANARY-UNCLEAN-PATH"))
	var closes []advSuppliedClose
	fds := &answerCloser{closeFD: func(fd int) error {
		return advCloseSupplied(fd, false, &closes)
	}}
	cases := []string{"keep", keep + "/../keep", keep + "/.", keep + "/\x00"}
	for _, path := range cases {
		if filepath.IsAbs(path) && filepath.Clean(path) == path && !strings.ContainsRune(path, 0) {
			t.Fatalf("case %q is a clean absolute path", path)
		}
		fd, err := openCursorAnswerDirectory(path, fds)
		if fd >= 0 {
			fds.close(fd)
			t.Fatalf("opened %q", path)
		}
		if err != errCursorAnswerPath || strings.Contains(err.Error(), root) || strings.Contains(err.Error(), "CANARY-UNCLEAN-PATH") {
			t.Fatal(err)
		}
	}
	for _, rec := range closes {
		if rec.regular || rec.hookErr != nil || rec.closeErr != nil {
			t.Fatalf("unclean walk close %+v", rec)
		}
	}
}

func TestCursorAnswerFileAdversarialSymlinkToOwnedTarget(t *testing.T) {
	task, current, completed := boundAnswerFixture(t)
	name, err := cursorAnswerBasename(task.binding)
	if err != nil {
		t.Fatal(err)
	}
	text := "owned-target-text"
	target := filepath.Join(task.plan.worktree, "owned-target")
	advAnswerPlant(t, target, []byte(boundAnswerDoc(task, completed, text, 1)))
	leaf := filepath.Join(task.plan.worktree, name)
	if err := os.Symlink(target, leaf); err != nil {
		t.Fatal(err)
	}
	got, err := acceptCursorAnswerFile(context.Background(), task, completed, nil, sameAnswerContext(current))
	advAnswerZero(t, got, err, text, target, "owned-target")
	if err != errCursorAnswerSymlink {
		t.Fatal(err)
	}
	info, statErr := os.Lstat(leaf)
	body, readErr := os.ReadFile(target)
	if statErr != nil || info.Mode()&os.ModeSymlink == 0 || readErr != nil || !strings.Contains(string(body), text) {
		t.Fatal("owned symlink target was not left intact")
	}
}

func TestCursorAnswerFileAdversarialObservedGrowth(t *testing.T) {
	task, current, completed := boundAnswerFixture(t)
	name, err := cursorAnswerBasename(task.binding)
	if err != nil {
		t.Fatal(err)
	}
	text := "bounded-answer"
	path := filepath.Join(task.plan.worktree, name)
	doc := []byte(boundAnswerDoc(task, completed, text, 1))
	advAnswerPlant(t, path, doc)
	grew := false
	got, err := acceptCursorAnswerFileAt(context.Background(), task, completed, nil, sameAnswerContext(current), &cursorAnswerHooks{afterRead: func() {
		before, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatal(statErr)
		}
		file, openErr := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
		if openErr != nil {
			t.Fatal(openErr)
		}
		if _, writeErr := file.Write([]byte("GROWTH-CANARY")); writeErr != nil {
			_ = file.Close()
			t.Fatal(writeErr)
		}
		if closeErr := file.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
		after, statErr := os.Stat(path)
		if statErr != nil || after.Size() <= before.Size() || before.Mode().Perm() != 0o600 || after.Mode().Perm() != 0o600 {
			t.Fatal("post-read growth was not observed")
		}
		grew = true
	}})
	advAnswerZero(t, got, err, text, "GROWTH-CANARY", path)
	if !grew || err != errCursorAnswerChanged {
		t.Fatal(err)
	}
}

func TestCursorAnswerFileAdversarialObservedLeafLink(t *testing.T) {
	task, current, completed := boundAnswerFixture(t)
	name, err := cursorAnswerBasename(task.binding)
	if err != nil {
		t.Fatal(err)
	}
	text := "link-observed-answer"
	path := filepath.Join(task.plan.worktree, name)
	advAnswerPlant(t, path, []byte(boundAnswerDoc(task, completed, text, 1)))
	target := filepath.Join(task.plan.worktree, "owned-link-target")
	advAnswerPlant(t, target, []byte("CANARY-LINK-TARGET"))
	replaced := false
	got, err := acceptCursorAnswerFileAt(context.Background(), task, completed, nil, sameAnswerContext(current), &cursorAnswerHooks{afterRead: func() {
		if removeErr := os.Remove(path); removeErr != nil {
			t.Fatal(removeErr)
		}
		if linkErr := os.Symlink(target, path); linkErr != nil {
			t.Fatal(linkErr)
		}
		replaced = true
	}})
	advAnswerZero(t, got, err, text, "CANARY-LINK-TARGET", target)
	if !replaced || err != errCursorAnswerChanged {
		t.Fatal(err)
	}
	info, statErr := os.Lstat(path)
	if statErr != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("leaf was not left as the observed symlink")
	}
}

func TestCursorAnswerFileAdversarialObservedFifo(t *testing.T) {
	task, current, completed := boundAnswerFixture(t)
	name, err := cursorAnswerBasename(task.binding)
	if err != nil {
		t.Fatal(err)
	}
	text := "fifo-observed-answer"
	path := filepath.Join(task.plan.worktree, name)
	advAnswerPlant(t, path, []byte(boundAnswerDoc(task, completed, text, 1)))
	replaced := false
	got, err := acceptCursorAnswerFileAt(context.Background(), task, completed, nil, sameAnswerContext(current), &cursorAnswerHooks{afterRead: func() {
		if removeErr := os.Remove(path); removeErr != nil {
			t.Fatal(removeErr)
		}
		if fifoErr := unix.Mkfifo(path, 0o600); fifoErr != nil {
			t.Fatal(fifoErr)
		}
		replaced = true
	}})
	advAnswerZero(t, got, err, text, path)
	if !replaced || err != errCursorAnswerChanged {
		t.Fatal(err)
	}
	info, statErr := os.Lstat(path)
	if statErr != nil || info.Mode()&os.ModeNamedPipe == 0 {
		t.Fatal("leaf was not left as the observed fifo")
	}
}

func TestCursorAnswerFileAdversarialRegularClose(t *testing.T) {
	task, current, completed := boundAnswerFixture(t)
	name, err := cursorAnswerBasename(task.binding)
	if err != nil {
		t.Fatal(err)
	}
	text := "close-observed-answer"
	advAnswerPlant(t, filepath.Join(task.plan.worktree, name), []byte(boundAnswerDoc(task, completed, text, 1)))
	var closes []advSuppliedClose
	got, err := acceptCursorAnswerFileAt(context.Background(), task, completed, nil, sameAnswerContext(current), &cursorAnswerHooks{closeFD: func(fd int) error {
		return advCloseSupplied(fd, true, &closes)
	}})
	advAnswerZero(t, got, err, text, "synthetic-regular-close", task.plan.worktree)
	if err != errCursorAnswerChanged {
		t.Fatal(err)
	}
	if len(closes) == 0 || !closes[0].dir || closes[0].regular || closes[0].hookErr != nil {
		t.Fatal("first supplied close was not a successful directory close")
	}
	regular := 0
	for _, rec := range closes {
		if rec.statErr != nil || rec.closeErr != nil {
			t.Fatalf("supplied close %+v", rec)
		}
		if rec.regular {
			regular++
			if rec.hookErr == nil {
				t.Fatal("regular-file close was not failed")
			}
			continue
		}
		if rec.hookErr != nil {
			t.Fatalf("non-regular close failed: %+v", rec)
		}
	}
	if regular != 1 {
		t.Fatalf("regular-file closes %d", regular)
	}
}
