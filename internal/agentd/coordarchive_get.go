package agentd

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"path/filepath"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// GetExact retains the complete-identity entry point without default ordering.
func (a CoordArchive) GetExact(q protocol.CoordArchiveExactQuery) (protocol.CoordArchiveGetResult, error) {
	if err := q.Validate(); err != nil {
		return protocol.CoordArchiveGetResult{}, err
	}
	return a.GetSelected(protocol.CoordArchiveGetQuery(q))
}

// GetSelected resolves omitted pins from recorded archive timestamps under one
// shared lock. Equal greatest times refuse; opaque identities never break ties.
// The chosen object is fully verified before any result is returned.
func (a CoordArchive) GetSelected(q protocol.CoordArchiveGetQuery) (protocol.CoordArchiveGetResult, error) {
	zero := protocol.CoordArchiveGetResult{}
	if err := q.Validate(); err != nil {
		return zero, err
	}
	if err := a.CheckMounted(); err != nil {
		return zero, err
	}
	unlock, err := lockHistoryArchive(a.Root, false)
	if err != nil {
		return zero, err
	}
	defer unlock()
	project, err := a.coordNameProject(q)
	if err != nil {
		return zero, err
	}
	var selected coordUIDSelection
	if q.UID == "" {
		selected, err = a.coordSelectUID(q, project)
		if err != nil {
			return zero, err
		}
		q.UID = selected.Latest.UID
	}
	if q.ResourceVersion != "" {
		exact := protocol.CoordArchiveExactQuery(q)
		rel, err := exact.RevisionDir(project)
		if err != nil {
			return zero, err
		}
		d, err := openBeneath(a.Root, filepath.Join(a.Root, filepath.FromSlash(rel)), historyDirFlags)
		if errors.Is(err, fs.ErrNotExist) {
			return zero, coordGetNotFound()
		}
		if err != nil {
			return zero, coordGetCorrupt(rel)
		}
		_ = d.Close()
		raw, err := readArchiveFile(a.Root, filepath.Join(a.Root, filepath.FromSlash(rel), "manifest.json"), 4096)
		if err != nil {
			return zero, coordGetCorrupt(rel)
		}
		manifest, err := parseCoordStoredManifest(raw, rel)
		if err != nil {
			return zero, err
		}
		if manifest.Object.SHA256 != q.SHA256 {
			return zero, coordGetNotFound()
		}
		return a.readRevision(manifest.CoordArchiveManifest, rel)
	}
	if selected.Latest.UID == "" {
		selected, err = a.coordUIDSummary(protocol.CoordArchiveIdentity{Resource: q.Resource, Project: project, Name: q.Name, UID: q.UID})
		if err != nil {
			return zero, err
		}
	}
	if selected.Latest.UID == "" {
		return zero, coordGetNotFound()
	}
	if selected.LatestTied {
		return zero, protocol.HistoryErrorf(protocol.HistoryErrAmbiguous, "", "revision archive time does not identify one latest revision; supply revision/hash")
	}
	return a.readRevision(selected.Latest.CoordArchiveManifest, selected.LatestRel)
}

func coordGetNotFound() error {
	return protocol.HistoryErrorf(protocol.HistoryErrNotFound, "", "revision is absent")
}
func coordGetCorrupt(rel string) error {
	return protocol.HistoryErrorf(protocol.HistoryErrCorrupt, rel, "unreadable or invalid selection metadata")
}

// Project discovery precedes object reads and retains the existing ambiguity
// refusal even when one UID/revision is explicitly pinned.
func (a CoordArchive) coordNameProject(q protocol.CoordArchiveGetQuery) (string, error) {
	d, err := openBeneath(a.Root, filepath.Join(a.Root, "v1", q.Resource), historyDirFlags)
	if errors.Is(err, fs.ErrNotExist) {
		return "", coordGetNotFound()
	}
	if err != nil {
		return "", coordArchiveWriteError("", err)
	}
	defer func() { _ = d.Close() }()
	found := ""
	for {
		projects, readErr := d.Readdirnames(128)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return "", coordGetCorrupt("")
		}
		for _, project := range projects {
			rel, err := q.NameDir(project)
			if err != nil {
				return "", err
			}
			nd, err := openBeneath(a.Root, filepath.Join(a.Root, filepath.FromSlash(rel)), historyDirFlags)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return "", coordGetCorrupt(rel)
			}
			_ = nd.Close()
			if found != "" {
				return "", protocol.HistoryErrorf(protocol.HistoryErrAmbiguous, "", "name exists under multiple projects")
			}
			found = project
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	if found == "" {
		return "", coordGetNotFound()
	}
	return found, nil
}

type coordUIDSelection struct {
	FirstAt    time.Time
	Latest     protocol.CoordArchiveStoredManifest
	LatestRel  string
	LatestTied bool
}

func (a CoordArchive) coordSelectUID(q protocol.CoordArchiveGetQuery, project string) (coordUIDSelection, error) {
	zero := coordUIDSelection{}
	rel, err := q.NameDir(project)
	if err != nil {
		return zero, err
	}
	d, err := openBeneath(a.Root, filepath.Join(a.Root, filepath.FromSlash(rel)), historyDirFlags)
	if err != nil {
		return zero, coordGetCorrupt(rel)
	}
	defer func() { _ = d.Close() }()
	best := zero
	tied := false
	for {
		names, readErr := d.Readdirnames(128)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return zero, coordGetCorrupt(rel)
		}
		for _, uid := range names {
			id := protocol.CoordArchiveIdentity{Resource: q.Resource, Project: project, Name: q.Name, UID: uid}
			if _, err := id.RecordDir(); err != nil {
				return zero, coordGetCorrupt(rel)
			}
			candidate, err := a.coordUIDSummary(id)
			if err != nil {
				return zero, err
			}
			// An empty UID directory supplies no first time: lost metadata and an
			// interrupted first publication are indistinguishable. Do not skip it.
			if candidate.Latest.UID == "" {
				return zero, coordGetCorrupt(rel)
			}
			if err := a.coordCheckUIDBirth(candidate); err != nil {
				return zero, err
			}
			if best.Latest.UID == "" || candidate.FirstAt.After(best.FirstAt) {
				best = candidate
				tied = false
			} else if candidate.FirstAt.Equal(best.FirstAt) {
				tied = true
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	if best.Latest.UID == "" {
		return zero, coordGetNotFound()
	}
	if tied {
		return zero, protocol.HistoryErrorf(protocol.HistoryErrAmbiguous, rel, "UID archive time does not identify one latest UID; supply UID")
	}
	return best, nil
}

// Default UID ordering requires the same immutable first-publication entry
// written by put. Missing or inconsistent birth metadata cannot rerank a UID.
func (a CoordArchive) coordCheckUIDBirth(candidate coordUIDSelection) error {
	entry := coordIndexEntry(candidate.Latest)
	entry.ArchivedAt = candidate.FirstAt
	rel := coordIndexPaths(entry)[0]
	raw, err := readArchiveFileLinks(a.Root, filepath.Join(a.Root, filepath.FromSlash(rel)), 4096, 2)
	if err != nil {
		return coordGetCorrupt(rel)
	}
	expected, err := protocol.CanonicalJSON(entry)
	if err != nil || !bytes.Equal(raw, expected) {
		return coordGetCorrupt(rel)
	}
	return nil
}

// Structural manifests supply order; only the final selected object is read.
// Candidate memory stays constant while traversing 128-name directory batches.
func (a CoordArchive) coordUIDSummary(id protocol.CoordArchiveIdentity) (coordUIDSelection, error) {
	zero := coordUIDSelection{}
	rel, err := id.RecordDir()
	if err != nil {
		return zero, err
	}
	dir := filepath.Join(a.Root, filepath.FromSlash(rel))
	d, err := openBeneath(a.Root, dir, historyDirFlags)
	if errors.Is(err, fs.ErrNotExist) {
		return zero, nil
	}
	if err != nil {
		return zero, coordGetCorrupt(rel)
	}
	defer func() { _ = d.Close() }()
	result := zero
	for {
		names, readErr := d.Readdirnames(128)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return zero, coordGetCorrupt(rel)
		}
		for _, name := range names {
			if name == "events" {
				continue
			}
			revision := filepath.ToSlash(filepath.Join(rel, name))
			raw, err := readArchiveFile(a.Root, filepath.Join(dir, name, "manifest.json"), 4096)
			if err != nil {
				return zero, coordGetCorrupt(revision)
			}
			m, err := parseCoordStoredManifest(raw, revision)
			if err != nil {
				return zero, err
			}
			if result.Latest.UID != "" {
				old, got := result.Latest.CoordArchiveManifest, m.CoordArchiveManifest
				old.ResourceVersion, got.ResourceVersion = "", ""
				old.Object, got.Object = protocol.CoordArchiveObjectDigest{}, protocol.CoordArchiveObjectDigest{}
				old.ArchivedBy, got.ArchivedBy = "", ""
				if old != got {
					return zero, coordGetCorrupt(revision)
				}
			}
			if result.FirstAt.IsZero() || m.ArchivedAt.Before(result.FirstAt) {
				result.FirstAt = m.ArchivedAt
			}
			if result.Latest.UID == "" || m.ArchivedAt.After(result.Latest.ArchivedAt) {
				result.Latest = m
				result.LatestRel = revision
				result.LatestTied = false
			} else if m.ArchivedAt.Equal(result.Latest.ArchivedAt) {
				result.LatestTied = true
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	return result, nil
}
