// This file is never compiled. TestHistoryExportScanCatchesWrites parses it to
// prove that TestHistoryExportReadsOnly's scan catches every way below of
// writing to HOME or reading through a link. Each function's name is the
// case the scan must report.
package export

import (
	"os"
	"path/filepath"
	"syscall"
	"time"
)

func openWithoutNoFollow(p string) { _, _ = os.OpenFile(p, os.O_RDONLY, 0) }

func openWithLiteralFlags(p string) { _, _ = os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW, 0) }

func openPlain(p string) { _, _ = os.Open(p) }

func readFileFollows(p string) { _, _ = os.ReadFile(p) }

func statFollows(p string) { _, _ = os.Stat(p) }

func createFile(p string) { _, _ = os.Create(p) }

func removeFile(p string) { _ = os.Remove(p) }

func renameFile(a, b string) { _ = os.Rename(a, b) }

func chmodFile(p string) { _ = os.Chmod(p, 0o600) }

func chtimesFile(p string, t time.Time) { _ = os.Chtimes(p, t, t) }

func mkdirAll(p string) { _ = os.MkdirAll(p, 0o700) }

func writeFile(p string) { _ = os.WriteFile(p, nil, 0o600) }

func linkFile(a, b string) { _ = os.Link(a, b) }

func syscallUnlink(p string) { _ = syscall.Unlink(p) }

func walkFollows(p string) { _ = filepath.WalkDir(p, nil) }

func callsAWriterElsewhere(p string) { _ = writeFileAtomic(p, nil, 0o600) }

func readCredentialReference(p string) { _, _ = os.OpenFile(p, os.O_RDWR, 0) }

var literalRemoves = func(p string) { _ = os.RemoveAll(p) }

func openOutsideOpenBeneath(p string) { _, _ = os.OpenFile(p, historyOpenFlags, 0) }

func openBeneathWritable(p string) { _, _ = openBeneath(p, p, os.O_RDWR) }

func openatOutsideOpenBeneath(p string) { _, _ = syscall.Openat(0, p, historyOpenFlags, 0) }

func newFileOutsideOpenBeneath(fd uintptr) { _ = os.NewFile(fd, "x") }

func openBeneath(base, abs string, flags int) (*os.File, error) {
	fd, err := syscall.Openat(0, abs, syscall.O_RDWR, 0)
	return os.NewFile(uintptr(fd), abs), err
}
