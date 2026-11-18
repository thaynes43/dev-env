package coordinatorwait

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Directory pins one real directory for the lifetime of a call. Root confines
// all entry metadata reads even if the directory is renamed during a scan.
type Directory struct {
	root *os.Root
	path string
	info os.FileInfo
}

func OpenDirectory(path string) (*Directory, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("directory must be an absolute clean path")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return nil, errors.New("symlinked or unavailable directory refused")
	}
	before, err := os.Lstat(path)
	if err != nil || !before.IsDir() {
		return nil, errors.New("handoff path is not a directory")
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, errors.New("cannot pin handoff directory")
	}
	inside, err := root.Stat(".")
	if err != nil || !os.SameFile(before, inside) {
		_ = root.Close()
		return nil, errors.New("handoff directory changed while opening")
	}
	return &Directory{root: root, path: path, info: inside}, nil
}
func (d *Directory) Close() error { return d.root.Close() }

func (d *Directory) check() error {
	resolved, err := filepath.EvalSymlinks(d.path)
	if err != nil || resolved != d.path {
		return errors.New("handoff directory path changed")
	}
	current, err := os.Lstat(d.path)
	if err != nil || !os.SameFile(current, d.info) {
		return errors.New("handoff directory identity changed")
	}
	return nil
}

// Scan reads at most max+1 names and max metadata records; never file contents
// or subdirectories. A symlinked entry is a refusal, including a dangling link.
func (d *Directory) Scan(max int) (map[string]string, bool, error) {
	if max < 1 || max > 4096 {
		return nil, false, errors.New("invalid scan bound")
	}
	if err := d.check(); err != nil {
		return nil, false, err
	}
	f, err := d.root.Open(".")
	if err != nil {
		return nil, false, errors.New("cannot scan handoff directory")
	}
	defer func() { _ = f.Close() }()
	entries, err := f.ReadDir(max + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, false, errors.New("cannot read handoff metadata")
	}
	overflow := len(entries) > max
	if overflow {
		entries = entries[:max]
	}
	result := make(map[string]string, len(entries))
	for _, e := range entries {
		name := e.Name()
		if !safeEntry(name) {
			return nil, false, errors.New("unsafe handoff entry")
		}
		st, err := d.root.Lstat(name)
		if os.IsNotExist(err) {
			continue
		} // next modest scan detects the change
		if err != nil {
			return nil, false, errors.New("cannot inspect handoff entry")
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return nil, false, errors.New("symlinked handoff entry refused")
		}
		// Names, sizes, modes and modification times are metadata. Include directory
		// identity via the pinned root; no file is followed or opened.
		result[name] = fmt.Sprintf("%d:%d:%d", st.Size(), st.Mode(), st.ModTime().UnixNano())
		if raw, ok := st.Sys().(*syscall.Stat_t); ok {
			result[name] += fmt.Sprintf(":%d:%d", raw.Dev, raw.Ino)
		}
	}
	if err := d.check(); err != nil {
		return nil, false, err
	}
	return result, overflow, nil
}
func safeEntry(name string) bool {
	return name != "" && name != "." && name != ".." && filepath.Base(name) == name && !strings.ContainsAny(name, "/\\\x00\r\n")
}
