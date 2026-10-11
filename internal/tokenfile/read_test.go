package tokenfile

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestReadRefusesFIFOAndOversizedRegularFile(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "synthetic-fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	// No writer is started: a blocking open would hang before any request timeout.
	if _, err := Read(fifo, 16); err == nil || strings.Contains(err.Error(), fifo) {
		t.Fatal("FIFO must be refused with a generic error")
	}
	large := filepath.Join(dir, "synthetic-large")
	if err := os.WriteFile(large, bytes.Repeat([]byte("x"), 17), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(large, 16); err == nil || strings.Contains(err.Error(), large) {
		t.Fatal("oversized file must be refused with a generic error")
	}
	if _, err := Read(dir, 16); err == nil {
		t.Fatal("directory accepted")
	}
	if _, err := Read(filepath.Join(dir, "missing"), 16); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("missing-file classification lost")
	}
}

func TestReadProjectedSymlinkRotationAndExactBound(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"first", "second"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name, "token"), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("first", filepath.Join(dir, "..data")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "token")
	if err := os.Symlink("..data/token", path); err != nil {
		t.Fatal(err)
	}
	if got, err := Read(path, 5); err != nil || string(got) != "first" {
		t.Fatal("projected symlink at exact bound refused")
	}
	if err := os.Symlink("second", filepath.Join(dir, "..data_tmp")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "..data_tmp"), filepath.Join(dir, "..data")); err != nil {
		t.Fatal(err)
	}
	if got, err := Read(path, 6); err != nil || string(got) != "second" {
		t.Fatal("rotated projection was not read anew")
	}
	if _, err := Read(path, 5); err == nil {
		t.Fatal("new oversized projection accepted")
	}
}
