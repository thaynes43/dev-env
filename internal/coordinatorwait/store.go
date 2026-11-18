package coordinatorwait

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// Store owns a private record and a nonblocking process lock. Kernel locks are
// released on death; a stale PID file never authorizes a second custodian.
type Store struct {
	root *os.Root
	lock *os.File
	name string
	sync func(*os.File) error
}

// OpenStore uses a fixed private HOME subdirectory. IDs are hashed into names;
// neither a thread ID nor an event reference can choose a record path.
func OpenStore(home, uid, waitID string) (*Store, error) {
	if !identity.MatchString(uid) || !identity.MatchString(waitID) || !filepath.IsAbs(home) {
		return nil, errors.New("private HOME and valid wait identity required")
	}
	resolved, err := filepath.EvalSymlinks(home)
	if err != nil || resolved != filepath.Clean(home) {
		return nil, errors.New("symlinked or unavailable HOME refused")
	}
	dir := filepath.Join(home, ".dev-env-coordinator-waits")
	if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
		return nil, errors.New("cannot create private wait directory")
	}
	st, err := os.Lstat(dir)
	if err != nil || !st.IsDir() || st.Mode().Perm() != 0700 {
		return nil, errors.New("wait directory must be a real private directory with mode 0700")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, errors.New("cannot open private wait directory")
	}
	inside, statErr := root.Stat(".")
	if statErr != nil || !os.SameFile(st, inside) {
		_ = root.Close()
		return nil, errors.New("wait directory changed while opening")
	}
	s := &Store{root: root, name: digest(uid + ":" + waitID), sync: func(f *os.File) error { return f.Sync() }}
	// OpenFile would follow a link, so reject existing links before opening and
	// verify the opened inode against Lstat before taking a lock.
	name := s.name + ".lock"
	if st, err := root.Lstat(name); err == nil && (!st.Mode().IsRegular() || st.Mode().Perm() != 0600) {
		_ = root.Close()
		return nil, errors.New("unsafe wait lock")
	}
	lock, err := root.OpenFile(name, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		_ = root.Close()
		return nil, errors.New("cannot open wait lock")
	}
	actual, err := lock.Stat()
	current, lerr := root.Lstat(name)
	if err != nil || lerr != nil || !current.Mode().IsRegular() || !os.SameFile(actual, current) || actual.Mode().Perm() != 0600 {
		_ = lock.Close()
		_ = root.Close()
		return nil, errors.New("unsafe wait lock")
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		_ = root.Close()
		return nil, errors.New("wait already has a custodian")
	}
	s.lock = lock
	return s, nil
}
func (s *Store) Close() { _ = s.lock.Close(); _ = s.root.Close() }
func (s *Store) Load() (*Record, error) {
	name := s.name + ".json"
	st, err := s.root.Lstat(name)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0600 || st.Size() > 2<<20 {
		return nil, errors.New("unsafe wait record")
	}
	f, err := s.root.Open(name)
	if err != nil {
		return nil, errors.New("cannot read wait record")
	}
	defer func() { _ = f.Close() }()
	actual, err := f.Stat()
	if err != nil || !os.SameFile(st, actual) {
		return nil, errors.New("wait record changed")
	}
	var r Record
	dec := json.NewDecoder(io.LimitReader(f, (2<<20)+1))
	dec.DisallowUnknownFields()
	if dec.Decode(&r) != nil || dec.Decode(&struct{}{}) != io.EOF || r.Options.Validate() != nil || r.validate() != nil || r.Options.MaxPending < len(r.Pending) || len(r.Seen) > r.Options.MaxPending || len(r.Files) > r.Options.MaxEntries || r.NextTimer.IsZero() {
		return nil, errors.New("invalid wait record")
	}
	return &r, nil
}

// Save fsyncs a mode-0600 temporary file, renames within the private directory,
// then fsyncs the directory. A result is never printed unless all steps succeed.
func (s *Store) Save(r *Record) error {
	data, err := json.Marshal(r)
	if err != nil || len(data) > 2<<20 {
		return errors.New("wait record exceeds bound")
	}
	name := s.name + ".tmp"
	// Exclusivity refuses stale files and symlinks without following either.
	f, err := s.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.New("cannot create atomic wait record; inspect stale temporary file")
	}
	defer func() { _ = s.root.Remove(name) }()
	if _, err = f.Write(data); err == nil {
		err = s.sync(f)
	}
	cerr := f.Close()
	if err != nil || cerr != nil {
		return errors.New("cannot persist wait record")
	}
	if err = s.root.Rename(name, s.name+".json"); err != nil {
		return errors.New("cannot replace wait record")
	}
	dir, err := s.root.Open(".")
	if err != nil {
		return errors.New("cannot sync wait directory")
	}
	defer func() { _ = dir.Close() }()
	if err = s.sync(dir); err != nil {
		return errors.New("cannot sync wait directory")
	}
	return nil
}

// StopRequested tests a fixed marker alongside the record. It reads no text.
func (s *Store) StopRequested() (bool, error) {
	st, err := s.root.Lstat(s.name + ".stop")
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil || !st.Mode().IsRegular() {
		return false, errors.New("unsafe wait stop marker")
	}
	return true, nil
}
