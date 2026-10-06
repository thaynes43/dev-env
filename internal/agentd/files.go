package agentd

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// writeFileAtomic writes data to a temporary file beside path and renames it
// over path, so a reader sees the old file or the new one, never half of one.
// It keeps the mode of an existing file and uses mode for a new one.
func writeFileAtomic(path string, data []byte, mode fs.FileMode) error {
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".agentd-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	return nil
}

// symlinkForce points link at target, replacing a file or link already there
// (ln -sf). It refuses to replace a directory.
func symlinkForce(target, link string) error {
	if cur, err := os.Readlink(link); err == nil && cur == target {
		return nil
	}
	if fi, err := os.Lstat(link); err == nil {
		if fi.IsDir() {
			return fmt.Errorf("%s is a directory, not replacing it with a link", link)
		}
		if err := os.Remove(link); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return err
	}
	return os.Symlink(target, link)
}

// exists reports whether path exists, following links.
func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// isNotExist is errors.Is(err, fs.ErrNotExist).
func isNotExist(err error) bool { return errors.Is(err, fs.ErrNotExist) }
