package agentd

import (
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// readCodexRCTarget accepts a regular file or the fixed Kubernetes AtomicWriter
// projection: rc-target.json -> ..data/rc-target.json, ..data -> ..<timestamp>.
// Every directory and the final file are opened without following symlinks.
// Pinning the directory descriptors prevents a projection swap from redirecting
// a read outside this mount. Rotation during a read refuses; no packet is cached.
// This exception applies only to this deployment-owned key, never HOME evidence.
func readCodexRCTarget(configDir string) ([]byte, error) {
	dir := filepath.Join(configDir, "codex")
	fd, err := openCodexRCConfigDirectory(dir)
	if err != nil {
		return nil, errCodexRCRefused
	}
	defer func() { _ = unix.Close(fd) }()
	readLink := func(name string) (string, error) {
		buf := make([]byte, 256)
		n, err := unix.Readlinkat(fd, name, buf)
		if err != nil || n == len(buf) {
			return "", errCodexRCRefused
		}
		return string(buf[:n]), nil
	}
	key := "rc-target.json"
	parent := fd
	generation := ""
	var info unix.Stat_t
	if unix.Fstatat(fd, key, &info, unix.AT_SYMLINK_NOFOLLOW) != nil {
		return nil, errCodexRCRefused
	}
	if info.Mode&unix.S_IFMT == unix.S_IFLNK {
		link, err := readLink(key)
		if err != nil || link != "..data/"+key {
			return nil, errCodexRCRefused
		}
		generation, err = readLink("..data")
		if err != nil || !codexRCProjectionGeneration.MatchString(generation) {
			return nil, errCodexRCRefused
		}
		parent, err = unix.Openat(fd, generation, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return nil, errCodexRCRefused
		}
		defer func() { _ = unix.Close(parent) }()
	}
	leaf, err := unix.Openat(parent, key, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errCodexRCRefused
	}
	f := os.NewFile(uintptr(leaf), key)
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > apiv1.CodexRCMaxPacket {
		return nil, errCodexRCRefused
	}
	raw, err := io.ReadAll(io.LimitReader(f, apiv1.CodexRCMaxPacket+1))
	if err != nil || len(raw) > apiv1.CodexRCMaxPacket {
		return nil, errCodexRCRefused
	}
	if generation != "" {
		current, err := readLink("..data")
		link, keyErr := readLink(key)
		if err != nil || keyErr != nil || current != generation || link != "..data/"+key {
			return nil, errCodexRCRefused
		}
	}
	return raw, nil
}

// Kubernetes AtomicWriter's timestamp directory is a single relative basename.
var codexRCProjectionGeneration = regexp.MustCompile(`^\.\.[0-9]{4}_[0-9]{2}_[0-9]{2}_[0-9]{2}_[0-9]{2}_[0-9]{2}\.[0-9]+$`)

// Walk from a pinned filesystem root, refusing symlinks at every component.
// An lstat walk followed by an absolute open would leave ancestor-swap races.
func openCodexRCConfigDirectory(path string) (int, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return -1, errCodexRCRefused
	}
	flags := unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	fd, err := unix.Open("/", flags, 0)
	if err != nil {
		return -1, err
	}
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		next, err := unix.Openat(fd, part, flags, 0)
		_ = unix.Close(fd)
		if err != nil {
			return -1, err
		}
		fd = next
	}
	return fd, nil
}
