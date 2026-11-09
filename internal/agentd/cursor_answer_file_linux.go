package agentd

// acceptCursorAnswerFile is an unused Linux adapter. It opens one derived
// answer artifact and passes that reader to acceptCursorBoundAnswer.
// Production dispatch does not call it.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

var (
	errCursorAnswerPath    = errors.New("answer-file-path")
	errCursorAnswerAbsent  = errors.New("answer-file-absent")
	errCursorAnswerSymlink = errors.New("answer-file-symlink")
	errCursorAnswerType    = errors.New("answer-file-type")
	errCursorAnswerChanged = errors.New("answer-file-changed")
	errCursorAnswerRead    = errors.New("answer-file-read")
)

// cursorAnswerHooks carries test observations. Production passes nil.
// Callbacks are not a lease and their errors are not returned.
type cursorAnswerHooks struct {
	onRead    func()
	afterRead func()
	closeFD   func(int) error
}

type answerCloser struct {
	closeFD func(int) error
	failed  bool
}

func (c *answerCloser) close(fd int) {
	if fd < 0 {
		return
	}
	var err error
	if c != nil && c.closeFD != nil {
		err = c.closeFD(fd)
	} else {
		err = unix.Close(fd)
	}
	if err != nil && c != nil {
		c.failed = true
	}
}

// cursorAnswerBasename is the only leaf name this adapter opens. It is a
// domain-separated digest of the native invocation and launch digest, and it
// fits the helper label limit. The caller does not choose the filename.
func cursorAnswerBasename(binding cursorTaskBinding) (string, error) {
	id, digest := binding.native.InvocationID, binding.native.LaunchDigest
	if id == "" || digest == "" || strings.ContainsRune(id, 0) || strings.ContainsRune(digest, 0) {
		return "", errCursorAnswerPath
	}
	mac := sha256.New()
	_, _ = mac.Write([]byte("cursor-answer-file-v1"))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(id))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(digest))
	name := "a" + hex.EncodeToString(mac.Sum(nil))
	if !cursorScaffoldLabel.MatchString(name) {
		return "", errCursorAnswerPath
	}
	return name, nil
}

func cursorAnswerFileStatOK(st unix.Stat_t, uid int) error {
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFLNK:
		return errCursorAnswerSymlink
	case unix.S_IFREG:
	default:
		return errCursorAnswerType
	}
	if st.Nlink != 1 || int(st.Uid) != uid || st.Mode&0o7777 != 0o600 {
		return errCursorAnswerType
	}
	if st.Size > maxAnswerEncoded {
		return errOversized
	}
	if st.Size < 0 {
		return errCursorAnswerType
	}
	return nil
}

func cursorAnswerDirStatOK(st unix.Stat_t, uid int) error {
	if st.Mode&unix.S_IFMT != unix.S_IFDIR || int(st.Uid) != uid || st.Mode&0o022 != 0 {
		return errCursorAnswerPath
	}
	return nil
}

func cursorAnswerSameStat(a, b unix.Stat_t) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino && a.Size == b.Size && a.Mode == b.Mode &&
		a.Uid == b.Uid && a.Gid == b.Gid && a.Nlink == b.Nlink &&
		a.Mtim == b.Mtim && a.Ctim == b.Ctim
}

func cursorAnswerErrno(err error) error {
	switch {
	case errors.Is(err, unix.ENOENT):
		return errCursorAnswerAbsent
	case errors.Is(err, unix.ELOOP), errors.Is(err, unix.ENOTDIR):
		return errCursorAnswerSymlink
	default:
		return errCursorAnswerPath
	}
}

const answerDirChainMax = 128

type answerDirID struct {
	dev uint64
	ino uint64
}

func answerDirIdentity(fd int) (answerDirID, error) {
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil {
		return answerDirID{}, errCursorAnswerPath
	}
	return answerDirID{dev: st.Dev, ino: st.Ino}, nil
}

func sameAnswerDirChain(a, b []answerDirID) bool {
	if len(a) == 0 || len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func openCursorAnswerDirectory(path string, fds *answerCloser) (int, error) {
	fd, _, err := openCursorAnswerChain(path, fds)
	return fd, err
}

func openCursorAnswerChain(path string, fds *answerCloser) (int, []answerDirID, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsRune(path, 0) {
		return -1, nil, errCursorAnswerPath
	}
	flags := unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	fd, err := unix.Open("/", flags, 0)
	if err != nil {
		return -1, nil, errCursorAnswerPath
	}
	rootID, err := answerDirIdentity(fd)
	if err != nil {
		fds.close(fd)
		return -1, nil, errCursorAnswerPath
	}
	chain := []answerDirID{rootID}
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if part == "" || part == "." || part == ".." || len(chain) >= answerDirChainMax {
			fds.close(fd)
			return -1, nil, errCursorAnswerPath
		}
		next, err := unix.Openat(fd, part, flags, 0)
		fds.close(fd)
		if err != nil {
			return -1, nil, cursorAnswerErrno(err)
		}
		if fds.failed {
			fds.close(next)
			return -1, nil, errCursorAnswerChanged
		}
		id, err := answerDirIdentity(next)
		if err != nil {
			fds.close(next)
			return -1, nil, errCursorAnswerPath
		}
		chain = append(chain, id)
		fd = next
	}
	return fd, chain, nil
}

func readCursorAnswerFD(fd int, size int64) ([]byte, error) {
	if size < 0 || size > maxAnswerEncoded {
		return nil, errOversized
	}
	buf := make([]byte, int(size))
	for off := 0; off < len(buf); {
		n, err := unix.Read(fd, buf[off:])
		if n > 0 {
			off += n
			continue
		}
		if err != nil {
			return nil, errCursorAnswerRead
		}
		return nil, errCursorAnswerChanged
	}
	var extra [1]byte
	n, err := unix.Read(fd, extra[:])
	if n > 0 {
		return nil, errOversized
	}
	if err != nil {
		return nil, errCursorAnswerRead
	}
	return buf, nil
}

func acquireCursorAnswer(task cursorBoundTask, hooks *cursorAnswerHooks) (body []byte, err error) {
	fds := &answerCloser{}
	if hooks != nil {
		fds.closeFD = hooks.closeFD
	}
	dirfd, leaf, dirfd2 := -1, -1, -1
	defer func() {
		fds.close(leaf)
		fds.close(dirfd2)
		fds.close(dirfd)
		if fds.failed {
			body, err = nil, errCursorAnswerChanged
		}
	}()
	if task.plan.worktree == "" || task.plan.worktree != task.binding.worktree || !filepath.IsAbs(task.plan.worktree) {
		return nil, errCursorAnswerPath
	}
	name, err := cursorAnswerBasename(task.binding)
	if err != nil {
		return nil, err
	}
	var chain []answerDirID
	dirfd, chain, err = openCursorAnswerChain(task.plan.worktree, fds)
	if err != nil {
		return nil, err
	}
	var dirStat unix.Stat_t
	if unix.Fstat(dirfd, &dirStat) != nil {
		return nil, errCursorAnswerPath
	}
	if err = cursorAnswerDirStatOK(dirStat, os.Getuid()); err != nil {
		return nil, err
	}
	var entry unix.Stat_t
	if err = unix.Fstatat(dirfd, name, &entry, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, cursorAnswerErrno(err)
	}
	if err = cursorAnswerFileStatOK(entry, os.Getuid()); err != nil {
		return nil, err
	}
	leaf, err = unix.Openat(dirfd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, cursorAnswerErrno(err)
	}
	var opened unix.Stat_t
	if unix.Fstat(leaf, &opened) != nil || !cursorAnswerSameStat(entry, opened) {
		return nil, errCursorAnswerChanged
	}
	if err = cursorAnswerFileStatOK(opened, os.Getuid()); err != nil {
		return nil, err
	}
	body, err = readCursorAnswerFD(leaf, opened.Size)
	if err != nil {
		return nil, err
	}
	if hooks != nil && hooks.afterRead != nil {
		hooks.afterRead()
	}
	var again, entryAgain, dirAgain, entryFar unix.Stat_t
	if unix.Fstat(leaf, &again) != nil || !cursorAnswerSameStat(opened, again) {
		return nil, errCursorAnswerChanged
	}
	if unix.Fstatat(dirfd, name, &entryAgain, unix.AT_SYMLINK_NOFOLLOW) != nil || !cursorAnswerSameStat(opened, entryAgain) {
		return nil, errCursorAnswerChanged
	}
	var chainAgain []answerDirID
	dirfd2, chainAgain, err = openCursorAnswerChain(task.plan.worktree, fds)
	if err != nil || !sameAnswerDirChain(chain, chainAgain) {
		return nil, errCursorAnswerChanged
	}
	if unix.Fstat(dirfd2, &dirAgain) != nil || dirAgain.Dev != dirStat.Dev || dirAgain.Ino != dirStat.Ino {
		return nil, errCursorAnswerChanged
	}
	if cursorAnswerDirStatOK(dirAgain, os.Getuid()) != nil {
		return nil, errCursorAnswerChanged
	}
	if unix.Fstatat(dirfd2, name, &entryFar, unix.AT_SYMLINK_NOFOLLOW) != nil || !cursorAnswerSameStat(opened, entryFar) {
		return nil, errCursorAnswerChanged
	}
	return body, nil
}

type cursorAnswerReader struct {
	task    cursorBoundTask
	hooks   *cursorAnswerHooks
	done    bool
	fileErr error
	body    []byte
	pos     int
}

func (r *cursorAnswerReader) Read(p []byte) (int, error) {
	if !r.done {
		r.done = true
		if r.hooks != nil && r.hooks.onRead != nil {
			r.hooks.onRead()
		}
		body, err := acquireCursorAnswer(r.task, r.hooks)
		if err != nil {
			r.fileErr = err
			r.body = nil
			return 0, err
		}
		r.body = body
	}
	if r.fileErr != nil {
		return 0, r.fileErr
	}
	if r.pos >= len(r.body) {
		return 0, io.EOF
	}
	n := copy(p, r.body[r.pos:])
	r.pos += n
	return n, nil
}

func acceptCursorAnswerFile(ctx context.Context, task cursorBoundTask, completed cursorBoundTaskResult, runErr error, current func() (cursorTaskContext, error)) (cursorBoundAnswer, error) {
	return acceptCursorAnswerFileAt(ctx, task, completed, runErr, current, nil)
}

func acceptCursorAnswerFileAt(ctx context.Context, task cursorBoundTask, completed cursorBoundTaskResult, runErr error, current func() (cursorTaskContext, error), hooks *cursorAnswerHooks) (cursorBoundAnswer, error) {
	var zero cursorBoundAnswer
	reader := &cursorAnswerReader{task: task, hooks: hooks}
	got, err := acceptCursorBoundAnswer(ctx, task, completed, runErr, current, reader)
	if reader.fileErr != nil {
		return zero, reader.fileErr
	}
	return got, err
}
