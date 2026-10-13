// This file is never compiled. TestHistoryArchiveScanCatchesRemovals parses
// it to prove that TestHistoryArchiveNeverRemoves' scan catches every way
// below of removing or rewriting an archived file. Each function's name is
// the case the scan must report.
package archive

import (
	"os"
	"path/filepath"
	"syscall"
	"time"
)

func removeOutsideIncoming(p string) { _ = os.RemoveAll(p) }

func removeViaSyscall(p string) { _ = syscall.Unlink(p) }

func renameElsewhere(a, b string) { _ = os.Rename(a, b) }

func linkElsewhere(a, b string) { _ = os.Link(a, b) }

func writeOutsideIncoming(p string) { _ = os.WriteFile(p, nil, 0o600) }

func openForWriteOutsideIncoming(p string) { _, _ = os.OpenFile(p, os.O_WRONLY|os.O_TRUNC, 0) }

func mkdirOutsideIncoming(p string) { _ = os.MkdirAll(p, 0o700) }

func chmodEntry(p string) { _ = os.Chmod(p, 0o600) }

func truncateEntry(p string) { _ = os.Truncate(p, 0) }

func symlinkEntry(a, b string) { _ = os.Symlink(a, b) }

func (a HistoryArchive) incomingSneaky(p string) error { return os.WriteFile(p, nil, 0o600) }

func (a HistoryArchive) removeIncoming(p string) error { return os.RemoveAll(p) }

func callsAWriterElsewhere(p string) { _ = writeFileAtomic(p, nil, 0o600) }
