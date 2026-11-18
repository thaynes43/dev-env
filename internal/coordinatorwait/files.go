package coordinatorwait

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"
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

// DirectoryIdentity binds the resolved path and filesystem identity across calls.
type DirectoryIdentity struct {
	Path   string `json:"path"`
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

func (r *Record) BindDirectory(d *Directory) error {
	if err := d.check(); err != nil {
		return err
	}
	raw, ok := d.info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("handoff directory has no stable identity")
	}
	pin := DirectoryIdentity{Path: d.path, Device: raw.Dev, Inode: raw.Ino}
	if pin.Path != r.Options.Directory || r.DirectoryPin != nil && *r.DirectoryPin != pin {
		return errors.New("wait handoff directory identity differs from the saved record")
	}
	r.DirectoryPin = &pin
	return nil
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

// Snapshot contains bounded metadata. Refused keys hash raw name bytes and file
// identity; reasons are fixed words. Neither names nor link targets are retained.
type Snapshot struct {
	Files    map[string]string
	Refused  map[string]string
	Overflow bool
}

// Scan reads at most max+1 names and max metadata records; never file contents
// or subdirectories. A symlinked entry is a refusal, including a dangling link.
func (d *Directory) Scan(max int) (Snapshot, error) {
	if max < 1 || max > 4096 {
		return Snapshot{}, errors.New("invalid scan bound")
	}
	if err := d.check(); err != nil {
		return Snapshot{}, err
	}
	f, err := d.root.Open(".")
	if err != nil {
		return Snapshot{}, errors.New("cannot scan handoff directory")
	}
	defer func() { _ = f.Close() }()
	entries, err := f.ReadDir(max + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return Snapshot{}, errors.New("cannot read handoff metadata")
	}
	overflow := len(entries) > max
	if overflow {
		entries = entries[:max]
	}
	result := Snapshot{Files: make(map[string]string, len(entries)), Refused: make(map[string]string), Overflow: overflow}
	for _, e := range entries {
		name := e.Name()
		st, err := d.root.Lstat(name)
		if os.IsNotExist(err) {
			continue
		} // next modest scan detects the change
		if err != nil {
			return Snapshot{}, errors.New("cannot inspect handoff entry")
		}
		// Names, sizes, modes and modification times are metadata. Include directory
		// identity via the pinned root; no file is followed or opened.
		stamp := fmt.Sprintf("%d:%d:%d", st.Size(), st.Mode(), st.ModTime().UnixNano())
		if raw, ok := st.Sys().(*syscall.Stat_t); ok {
			stamp += fmt.Sprintf(":%d:%d", raw.Dev, raw.Ino)
		}
		reason := ""
		if !safeEntry(name) {
			reason = entryReason(name)
		}
		if st.Mode()&os.ModeSymlink != 0 {
			reason = "symlink"
		}
		if reason != "" {
			result.Refused[digest(fmt.Sprintf("%d:%s:%s", len(name), name, stamp))] = reason
			continue
		}
		result.Files[name] = stamp
	}
	if err := d.check(); err != nil {
		return Snapshot{}, err
	}
	return result, nil
}
func safeEntry(name string) bool { return entryReason(name) == "" }
func entryReason(name string) string {
	if !utf8.ValidString(name) {
		return "encoding"
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "control"
		}
	}
	if len(name) > 255 {
		return "length"
	}
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name || strings.ContainsAny(name, "/\\") {
		return "name"
	}
	return ""
}
func refusalReason(reason string) bool {
	switch reason {
	case "encoding", "control", "length", "name", "symlink":
		return true
	}
	return false
}
