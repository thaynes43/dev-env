package agentd

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// cleanupIncoming removes only old, known-private .incoming entries. The
// caller holds the exclusive archive lock. Any unfamiliar or unsafe old entry
// is preserved and makes the completed put refuse success rather than guessing
// that a removal is safe.
func (a CoordArchive) cleanupIncoming() error {
	incoming := filepath.Join(a.Root, protocol.HistoryArchiveIncoming)
	if err := coordArchiveDirectories(a.Root, incoming); err != nil {
		return err
	}
	now := a.Now
	if now == nil {
		now = time.Now
	}
	current := now().UTC()
	d, err := openBeneath(a.Root, incoming, historyDirFlags)
	if err != nil {
		return fmt.Errorf("open incoming directory: %w", err)
	}
	defer func() { _ = d.Close() }()
	for {
		names, readErr := d.Readdirnames(128)
		for _, name := range names {
			entry := filepath.Join(incoming, name)
			fi, err := os.Lstat(entry)
			if err != nil {
				return fmt.Errorf("inspect incoming entry %q: %w", name, err)
			}
			if !current.After(fi.ModTime().UTC().Add(protocol.HistoryIncomingMaxAge)) {
				continue
			}
			if err := validCoordIncomingEntry(a.Root, incoming, name, fi); err != nil {
				return err
			}
			if err := (HistoryArchive{Root: a.Root}).removeIncoming(entry); err != nil {
				return fmt.Errorf("remove incoming entry %q: %w", name, err)
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return fmt.Errorf("read incoming directory: %w", readErr)
		}
	}
	syncDir := a.syncDir
	if syncDir == nil {
		syncDir = syncArchiveDir
	}
	return syncDir(incoming)
}

func validCoordIncomingEntry(root, incoming, name string, fi fs.FileInfo) error {
	if !coordIncomingName(name) || !fi.IsDir() || fi.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("unsafe incoming entry %q", name)
	}
	entry := filepath.Join(incoming, name)
	d, err := openBeneath(root, entry, historyDirFlags)
	if err != nil {
		return fmt.Errorf("inspect incoming entry %q: %w", name, err)
	}
	defer func() { _ = d.Close() }()
	names, err := d.Readdirnames(3)
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("inspect incoming entry %q: %w", name, err)
	}
	if len(names) > 2 {
		return fmt.Errorf("unknown incoming files in %q", name)
	}
	for _, child := range names {
		if !coordIncomingFileName(child) {
			return fmt.Errorf("unknown incoming file %q", child)
		}
		childInfo, err := os.Lstat(filepath.Join(entry, child))
		if err != nil || !childInfo.Mode().IsRegular() {
			return fmt.Errorf("unsafe incoming file %q", child)
		}
	}
	return nil
}

func coordIncomingName(name string) bool {
	if len(name) != 32 {
		return false
	}
	for _, r := range name {
		if r < '0' || r > '9' && r < 'a' || r > 'f' {
			return false
		}
	}
	return true
}

func coordIncomingFileName(name string) bool {
	return strings.Contains("\x00object.json\x00manifest.json\x00index.json\x00event.json\x00", "\x00"+name+"\x00")
}
