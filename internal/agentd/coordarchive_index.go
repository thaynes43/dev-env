package agentd

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// parseCoordStoredManifest binds canonical metadata to its exact relative path.
func parseCoordStoredManifest(raw []byte, rel string) (protocol.CoordArchiveStoredManifest, error) {
	var m protocol.CoordArchiveStoredManifest
	err := json.Unmarshal(raw, &m)
	want, pathErr := protocol.CoordArchiveRevisionDir(m.CoordArchiveManifest)
	canonical, canonicalErr := protocol.CanonicalJSON(m)
	if err != nil || pathErr != nil || canonicalErr != nil || want != rel ||
		m.ArchivedAt.IsZero() || m.ArchivedAt.Location() != time.UTC || !bytes.Equal(raw, canonical) {
		return protocol.CoordArchiveStoredManifest{}, protocol.HistoryErrorf(protocol.HistoryErrCorrupt, rel, "invalid stored revision manifest")
	}
	return m, nil
}

func coordIndexEntry(m protocol.CoordArchiveStoredManifest) protocol.CoordArchiveIndexEntry {
	return protocol.CoordArchiveIndexEntry{Format: protocol.CoordArchiveIndexFormat,
		Resource: m.Resource, Namespace: m.Namespace, Project: m.Project, Name: m.Name,
		UID: m.UID, RecipientUID: m.RecipientUID, ArchivedAt: m.ArchivedAt}
}

// firstIndexEntry inspects only this UID, in finite-memory directory batches.
// All manifests must bind the same immutable identity. The selected first
// revision's full object is verified before it can supply index metadata.
// The exclusive shelf lock is held throughout the caller's put.
func (a CoordArchive) firstIndexEntry(m protocol.CoordArchiveManifest, dir string) (*protocol.CoordArchiveIndexEntry, error) {
	d, err := openBeneath(a.Root, dir, historyDirFlags)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, coordArchiveWriteError("", err)
	}
	defer func() { _ = d.Close() }()
	var first *protocol.CoordArchiveStoredManifest
	var firstRel string
	for {
		names, readErr := d.Readdirnames(128)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, coordArchiveWriteError("", readErr)
		}
		for _, name := range names {
			if name == "events" {
				continue
			}
			rel, err := filepath.Rel(a.Root, filepath.Join(dir, name))
			if err != nil {
				return nil, coordArchiveWriteError("", err)
			}
			rel = filepath.ToSlash(rel)
			raw, err := readArchiveFile(a.Root, filepath.Join(dir, name, "manifest.json"), 4096)
			if err != nil {
				return nil, protocol.HistoryErrorf(protocol.HistoryErrConflict, rel, "unreadable UID revision manifest")
			}
			stored, err := parseCoordStoredManifest(raw, rel)
			got, want := stored.CoordArchiveManifest, m
			got.ResourceVersion, want.ResourceVersion = "", ""
			got.Object, want.Object = protocol.CoordArchiveObjectDigest{}, protocol.CoordArchiveObjectDigest{}
			got.ArchivedBy, want.ArchivedBy = "", ""
			if err != nil || got != want {
				return nil, protocol.HistoryErrorf(protocol.HistoryErrConflict, rel, "UID revision identity differs or is damaged")
			}
			if first == nil || stored.ArchivedAt.Before(first.ArchivedAt) {
				first, firstRel = &stored, rel
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	if first == nil {
		return nil, nil
	}
	if _, err := a.readRevision(first.CoordArchiveManifest, firstRel); err != nil {
		return nil, protocol.HistoryErrorf(protocol.HistoryErrConflict, firstRel, "first UID revision differs or is damaged")
	}
	entry := coordIndexEntry(*first)
	return &entry, nil
}

func coordIndexPaths(e protocol.CoordArchiveIndexEntry) []string {
	base := path.Join("v1", e.Resource, e.Project, "index")
	month := e.ArchivedAt.Format("2006-01")
	name := e.ArchivedAt.Format("20060102T150405.000000000Z") + "-" + e.UID + ".json"
	paths := []string{path.Join(base, "all", month, name)}
	if e.Resource == "inboxmessages" {
		paths = append(paths, path.Join(base, "recipient", e.RecipientUID, month, name))
	}
	return paths
}

// publishIndexes repairs either missing entry, but never replaces an existing
// file. An exclusive hard link publishes a fsynced staging file atomically.
// Each entry uses its own staging inode, so at most two links exist while the
// later age-bounded incoming cleanup is pending.
func (a CoordArchive) publishIndexes(e protocol.CoordArchiveIndexEntry) error {
	raw, err := protocol.CanonicalJSON(e)
	if err != nil {
		return coordArchiveWriteError("", err)
	}
	paths := coordIndexPaths(e)
	exists := make([]bool, len(paths))
	// Refuse damaged existing entries before publishing another index.
	for i, rel := range paths {
		final := filepath.Join(a.Root, filepath.FromSlash(rel))
		if _, err := os.Lstat(final); err == nil {
			old, err := readArchiveFileLinks(a.Root, final, 4096, 2)
			if err != nil || !bytes.Equal(old, raw) {
				return protocol.HistoryErrorf(protocol.HistoryErrConflict, rel, "index differs or is damaged")
			}
			exists[i] = true
		} else if !errors.Is(err, fs.ErrNotExist) {
			return coordArchiveWriteError(rel, err)
		}
	}
	for i, rel := range paths {
		final := filepath.Join(a.Root, filepath.FromSlash(rel))
		if !exists[i] {
			for _, dir := range []string{filepath.Dir(final), filepath.Join(a.Root, protocol.HistoryArchiveIncoming)} {
				if err := coordArchiveDirectories(a.Root, dir); err != nil {
					return coordArchiveWriteError(rel, err)
				}
			}
			files := HistoryArchive{Root: a.Root}
			tmp, err := files.newIncomingDir()
			if err != nil {
				return coordArchiveWriteError(rel, err)
			}
			staged := filepath.Join(tmp, "index.json")
			if err := files.incomingFile(staged, raw); err != nil {
				return coordArchiveWriteError(rel, err)
			}
			if err := files.incomingSyncTree(tmp); err != nil {
				return coordArchiveWriteError(rel, err)
			}
			if err := os.Link(staged, final); err != nil {
				if errors.Is(err, fs.ErrExist) {
					return protocol.HistoryErrorf(protocol.HistoryErrConflict, rel, "index appeared during publication")
				}
				return coordArchiveWriteError(rel, err)
			}
		}
		f, _, err := openHistoryFileLinks(a.Root, final, 2)
		if err != nil {
			return coordArchiveWriteError(rel, err)
		}
		err = f.Sync()
		closeErr := f.Close()
		if err != nil {
			return coordArchiveWriteError(rel, err)
		}
		if closeErr != nil {
			return coordArchiveWriteError(rel, closeErr)
		}
		if err := a.syncPublicationDirectories(filepath.Dir(final)); err != nil {
			return coordArchiveWriteError(rel, err)
		}
	}
	return nil
}
