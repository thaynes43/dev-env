package agentd

import (
	"bytes"
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

// CoordArchive stores exact immutable revisions (DESIGN-024 section 4).
// General selectors and retention are separate R2 slices.
type CoordArchive struct {
	Root    string
	Home    string
	Now     func() time.Time
	syncDir func(string) error
}

func NewCoordArchive(s Settings) CoordArchive {
	return CoordArchive{Root: filepath.Join(s.Home, protocol.CoordArchiveDirName), Home: s.Home, Now: time.Now}
}

// Separate injection keeps the coordination tests from changing history tests.
var coordArchiveIsMounted = func(root, home string) error {
	return historyArchiveIsMounted(root, home)
}

func (a CoordArchive) CheckMounted() error {
	fi, err := os.Lstat(a.Root)
	if err != nil || !filepath.IsAbs(a.Root) || filepath.Clean(a.Root) != a.Root || !fi.IsDir() {
		return protocol.HistoryErrorf(protocol.HistoryErrNoArchive, "", "coordination archive root is not a directory")
	}
	if err := coordArchiveIsMounted(a.Root, a.Home); err != nil {
		return protocol.HistoryErrorf(protocol.HistoryErrNoArchive, "", "%v", err)
	}
	return nil
}

// Put verifies the bounded object before writing. Identical revisions retain
// their original manifest, including archivedAt and archivedBy. A conflicting
// or damaged revision is never overwritten. Incomplete incoming writes remain
// private; a later R2 cleanup command will handle old debris.
func (a CoordArchive) Put(m protocol.CoordArchiveManifest, r io.Reader) (protocol.CoordArchiveStoredManifest, error) {
	result, err := a.put(m, r, false, nil)
	return result.Manifest, err
}

// PutUnmarked retains the strict unmarked-only storage entry point.
func (a CoordArchive) PutUnmarked(m protocol.CoordArchiveManifest, r io.Reader) (protocol.CoordArchivePutResult, error) {
	stored, err := a.put(m, r, true, nil)
	if err != nil {
		return protocol.CoordArchivePutResult{}, err
	}
	return stored, nil
}

// PutRecord reports actual journal grace; an absent event is appended only
// after the verified revision and indices are durably published.
func (a CoordArchive) PutRecord(m protocol.CoordArchiveManifest, r io.Reader, event *protocol.CoordArchiveEvent) (protocol.CoordArchivePutResult, error) {
	return a.put(m, r, false, event)
}

func (a CoordArchive) put(m protocol.CoordArchiveManifest, r io.Reader, unmarkedOnly bool, event *protocol.CoordArchiveEvent) (protocol.CoordArchivePutResult, error) {
	zero := protocol.CoordArchivePutResult{}
	object, err := protocol.VerifyCoordArchiveObject(m, r)
	if err != nil {
		return zero, err
	}
	rel, err := protocol.CoordArchiveRevisionDir(m)
	if err != nil {
		return zero, err
	}
	if event != nil {
		if err := event.Validate(m.Resource); err != nil {
			return zero, err
		}
		if event.State != "absent" {
			return zero, protocol.HistoryErrorf(protocol.HistoryErrInvalid, rel, "marked put requires absent event")
		}
	}
	if err := a.CheckMounted(); err != nil {
		return zero, err
	}
	unlock, err := lockHistoryArchive(a.Root, true)
	if err != nil {
		return zero, err
	}
	defer unlock()
	final := filepath.Join(a.Root, filepath.FromSlash(rel))
	if unmarkedOnly {
		if err := a.requireUnmarked(filepath.Dir(final), rel); err != nil {
			return zero, err
		}
	}
	id := protocol.CoordArchiveIdentity{Resource: m.Resource, Project: m.Project, Name: m.Name, UID: m.UID}
	uidRel, _ := id.RecordDir()
	// Preserve the writer's no_archive refusal for unsafe archive parents.
	// Journal entries themselves are classified as corrupt by readJournal.
	uidDir, pathErr := openBeneath(a.Root, filepath.Join(a.Root, filepath.FromSlash(uidRel)), historyDirFlags)
	if pathErr == nil {
		_ = uidDir.Close()
	} else if !errors.Is(pathErr, fs.ErrNotExist) {
		return zero, protocol.HistoryErrorf(protocol.HistoryErrNoArchive, rel, "unsafe archived record directory")
	}
	state, err := a.readJournal(id, uidRel)
	if err != nil {
		return zero, err
	}
	if event != nil && state.GraceStart == nil && event.At.Before(state.LastAt) {
		return zero, protocol.HistoryErrorf(protocol.HistoryErrInvalid, rel, "backward event timestamp")
	}
	stored, err := a.putRevisionLocked(m, object, rel)
	if err != nil {
		return zero, err
	}
	if event != nil {
		state, _, err = a.appendEvent(uidRel, state, *event)
	} else {
		err = a.syncJournal(state)
	}
	if err != nil {
		return zero, coordArchiveWriteError(rel, err)
	}
	return protocol.CoordArchivePutResult{Manifest: stored, GraceStart: state.GraceStart}, nil
}

// Caller holds the exclusive shelf lock; journal preflight already succeeded.
func (a CoordArchive) putRevisionLocked(m protocol.CoordArchiveManifest, object []byte, rel string) (protocol.CoordArchiveStoredManifest, error) {
	zero := protocol.CoordArchiveStoredManifest{}
	final := filepath.Join(a.Root, filepath.FromSlash(rel))
	first, err := a.firstIndexEntry(m, filepath.Dir(final))
	if err != nil {
		return zero, err
	}
	if _, err := os.Lstat(final); err == nil {
		stored, err := a.readRevision(m, rel)
		if err != nil || !bytes.Equal(stored.Object, object) {
			return zero, protocol.HistoryErrorf(protocol.HistoryErrConflict, rel, "existing revision differs or is damaged")
		}
		// A previous rename may have succeeded before a parent fsync failed.
		// Visible, verified bytes alone do not prove durable publication.
		if err := a.syncRevision(final); err != nil {
			return zero, coordArchiveWriteError(rel, err)
		}
		if err := a.publishIndexes(*first); err != nil {
			return zero, err
		}
		return stored.Manifest, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return zero, protocol.HistoryErrorf(protocol.HistoryErrNoArchive, rel, "cannot inspect revision: %v", err)
	}
	clock := a.Now
	if clock == nil {
		clock = time.Now
	}
	stored := protocol.CoordArchiveStoredManifest{CoordArchiveManifest: m, ArchivedAt: clock().UTC()}
	if stored.ArchivedAt.IsZero() {
		return zero, protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "invalid archive clock")
	}
	if first != nil && stored.ArchivedAt.Before(first.ArchivedAt) {
		stored.ArchivedAt = first.ArchivedAt
	}
	manifest, err := protocol.CanonicalJSON(stored)
	if err != nil {
		return zero, protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "invalid archive clock")
	}
	// Existing directory components must be directories, never symbolic links.
	// The shelf lock serializes cooperating writers; administrator access remains
	// trusted, as in the existing history archive.
	for _, dir := range []string{filepath.Dir(final), filepath.Join(a.Root, protocol.HistoryArchiveIncoming)} {
		if err := coordArchiveDirectories(a.Root, dir); err != nil {
			return zero, protocol.HistoryErrorf(protocol.HistoryErrNoArchive, rel, "archive directory: %v", err)
		}
	}
	files := HistoryArchive{Root: a.Root, syncDir: a.syncDir}
	tmp, err := files.newIncomingDir()
	if err != nil {
		return zero, protocol.HistoryErrorf(protocol.HistoryErrNoArchive, rel, "incoming directory: %v", err)
	}
	if err := files.incomingFile(filepath.Join(tmp, "object.json"), object); err != nil {
		return zero, coordArchiveWriteError(rel, err)
	}
	if err := files.incomingFile(filepath.Join(tmp, "manifest.json"), manifest); err != nil {
		return zero, coordArchiveWriteError(rel, err)
	}
	if err := files.incomingSyncTree(tmp); err != nil {
		return zero, coordArchiveWriteError(rel, err)
	}
	if err := files.publishIncoming(tmp, final); err != nil {
		return zero, coordArchiveWriteError(rel, err)
	}
	if first == nil {
		entry := coordIndexEntry(stored)
		first = &entry
	}
	if err := a.publishIndexes(*first); err != nil {
		return zero, err
	}
	return stored, nil
}

// Get reads one exact revision under the shared shelf lock. It returns no
// manifest or object on failure and never treats .incoming as a stored record.
func (a CoordArchive) Get(m protocol.CoordArchiveManifest) (protocol.CoordArchiveGetResult, error) {
	var zero protocol.CoordArchiveGetResult
	if err := a.CheckMounted(); err != nil {
		return zero, err
	}
	rel, err := protocol.CoordArchiveRevisionDir(m)
	if err != nil {
		return zero, err
	}
	unlock, err := lockHistoryArchive(a.Root, false)
	if err != nil {
		return zero, err
	}
	defer unlock()
	return a.readRevision(m, rel)
}

// readRevision is called only while holding .shelf.lock.
func (a CoordArchive) readRevision(m protocol.CoordArchiveManifest, rel string) (protocol.CoordArchiveGetResult, error) {
	var zero protocol.CoordArchiveGetResult
	corrupt := func() (protocol.CoordArchiveGetResult, error) {
		return zero, protocol.HistoryErrorf(protocol.HistoryErrCorrupt, rel, "revision identity, bytes or digest differ")
	}
	dir := filepath.Join(a.Root, filepath.FromSlash(rel))
	d, err := openBeneath(a.Root, dir, historyDirFlags)
	if errors.Is(err, fs.ErrNotExist) {
		return zero, protocol.HistoryErrorf(protocol.HistoryErrNotFound, rel, "revision is absent")
	}
	if err != nil {
		return corrupt()
	}
	_ = d.Close()
	manifest, err := readArchiveFile(a.Root, filepath.Join(dir, "manifest.json"), 4096)
	if err != nil {
		return corrupt()
	}
	stored, err := parseCoordStoredManifest(manifest, rel)
	if err != nil {
		return corrupt()
	}
	expected, actual := m, stored.CoordArchiveManifest
	expected.ArchivedBy, actual.ArchivedBy = "", "" // First publisher is immutable provenance.
	if expected != actual {
		return corrupt()
	}
	object, err := readArchiveFile(a.Root, filepath.Join(dir, "object.json"), protocol.MaxCoordArchiveObjectBytes)
	if err != nil {
		return corrupt()
	}
	canonicalObject, err := protocol.VerifyCoordArchiveObject(stored.CoordArchiveManifest, bytes.NewReader(object))
	if err != nil || !bytes.Equal(object, canonicalObject) {
		return corrupt()
	}
	return protocol.CoordArchiveGetResult{Manifest: stored, Object: canonicalObject}, nil
}

func coordArchiveWriteError(rel string, err error) error {
	var archiveErr *protocol.HistoryError
	if errors.As(err, &archiveErr) {
		return err
	}
	return protocol.HistoryErrorf(protocol.HistoryErrNoArchive, rel, "archive write: %v", err)
}

// syncRevision repairs durability after an interrupted publication without
// changing the verified object or its immutable provenance. The lock is held.
func (a CoordArchive) syncRevision(dir string) error {
	for _, name := range []string{"object.json", "manifest.json"} {
		f, _, err := openHistoryFile(a.Root, filepath.Join(dir, name))
		if err != nil {
			return err
		}
		err = f.Sync()
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return a.syncPublicationDirectories(dir)
}

// Both revision rename and index link need their publication chain synced.
func (a CoordArchive) syncPublicationDirectories(dir string) error {
	syncDir := a.syncDir
	if syncDir == nil {
		syncDir = syncArchiveDir
	}
	for d := dir; ; d = filepath.Dir(d) {
		if err := syncDir(d); err != nil {
			return err
		}
		if d == a.Root {
			break
		}
	}
	incoming := filepath.Join(a.Root, protocol.HistoryArchiveIncoming)
	if _, err := os.Lstat(incoming); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	return syncDir(incoming)
}

// coordArchiveDirectories creates only directories beneath this archive and
// refuses existing non-directories, including symbolic links.
func coordArchiveDirectories(root, dir string) error {
	rel, err := filepath.Rel(root, dir)
	if err != nil || !filepath.IsLocal(rel) {
		return fmt.Errorf("directory is outside the archive")
	}
	cur := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		if err := os.Mkdir(cur, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		fi, err := os.Lstat(cur)
		if err != nil || !fi.IsDir() {
			return fmt.Errorf("archive path component is not a directory")
		}
	}
	return nil
}
