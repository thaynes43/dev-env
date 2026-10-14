package agentd

import (
	"errors"
	"io"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// LookupUnmarked reads only requested UID metadata under the shared lock.
// Nonempty journals require the later event implementation. No object is read
// or verified, and no partial batch is returned when any item fails.
func (a CoordArchive) LookupUnmarked(ids []protocol.CoordArchiveIdentity) ([]protocol.CoordArchiveLookupResult, error) {
	return a.lookup(ids, false)
}

// Lookup reconstructs recorded journal grace, without verifying object bytes.
func (a CoordArchive) Lookup(ids []protocol.CoordArchiveIdentity) ([]protocol.CoordArchiveLookupResult, error) {
	return a.lookup(ids, true)
}

func (a CoordArchive) lookup(ids []protocol.CoordArchiveIdentity, marked bool) ([]protocol.CoordArchiveLookupResult, error) {
	if len(ids) == 0 || len(ids) > protocol.MaxCoordArchiveLookupItems {
		return nil, protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "lookup requires 1 to 1024 identities")
	}
	paths := make([]string, len(ids))
	for n, id := range ids {
		rel, err := id.RecordDir()
		if err != nil {
			return nil, err
		}
		paths[n] = rel
	}
	if err := a.CheckMounted(); err != nil {
		return nil, err
	}
	unlock, err := lockHistoryArchive(a.Root, false)
	if err != nil {
		return nil, err
	}
	defer unlock()
	results := make([]protocol.CoordArchiveLookupResult, 0, len(ids))
	remaining := protocol.MaxCoordArchiveLookupBytes - 2 // enclosing array
	for n, id := range ids {
		if n > 0 {
			remaining-- // comma between results
		}
		result, err := a.lookupRecord(id, paths[n], remaining, marked)
		if err != nil {
			return nil, err
		}
		encoded, err := protocol.CanonicalJSON(result)
		if err != nil || len(encoded) > remaining {
			return nil, coordLookupOverflow()
		}
		remaining -= len(encoded)
		results = append(results, result)
	}
	return results, nil
}

func coordLookupOverflow() error {
	return protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "complete lookup response exceeds 4 MiB")
}

// Caller holds the shelf lock. Read one name to distinguish empty from unknown
// journal state; no event is interpreted or changed by this partial R2 slice.
func (a CoordArchive) requireUnmarked(uidDir, rel string) error {
	d, err := openBeneath(a.Root, filepath.Join(uidDir, "events"), historyDirFlags)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return coordArchiveWriteError(rel, err)
	}
	defer func() { _ = d.Close() }()
	names, err := d.Readdirnames(1)
	if len(names) != 0 {
		return protocol.HistoryErrorf(protocol.HistoryErrInvalid, rel, "event journal support required for this UID")
	}
	if !errors.Is(err, io.EOF) {
		return coordArchiveWriteError(rel, err)
	}
	return nil
}

func (a CoordArchive) lookupUnmarked(id protocol.CoordArchiveIdentity, rel string, budget int) (protocol.CoordArchiveLookupResult, error) {
	return a.lookupRecord(id, rel, budget, false)
}

func (a CoordArchive) lookupRecord(id protocol.CoordArchiveIdentity, rel string, budget int, marked bool) (protocol.CoordArchiveLookupResult, error) {
	zero := protocol.CoordArchiveLookupResult{}
	result := protocol.CoordArchiveLookupResult{CoordArchiveIdentity: id, Revisions: []protocol.CoordArchiveRevision{}}
	base, _ := protocol.CanonicalJSON(result)
	if len(base) > budget {
		return zero, coordLookupOverflow()
	}
	dir := filepath.Join(a.Root, filepath.FromSlash(rel))
	d, err := openBeneath(a.Root, dir, historyDirFlags)
	if errors.Is(err, fs.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return zero, protocol.HistoryErrorf(protocol.HistoryErrCorrupt, rel, "unreadable record directory")
	}
	defer func() { _ = d.Close() }()
	if marked {
		state, err := a.readJournal(id, rel)
		if err != nil {
			return zero, err
		}
		result.GraceStart = state.GraceStart
	} else if err := a.requireUnmarked(dir, rel); err != nil {
		return zero, err
	}
	base, _ = protocol.CanonicalJSON(result)
	used := len(base)
	if used > budget {
		return zero, coordLookupOverflow()
	}

	for {
		names, readErr := d.Readdirnames(128)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return zero, protocol.HistoryErrorf(protocol.HistoryErrCorrupt, rel, "unreadable revision names")
		}
		for _, name := range names {
			if name == "events" {
				continue
			}
			// Validate the path component before using it; the manifest below
			// must bind the full hash and exact identity back to this directory.
			// Resource versions may themselves contain hyphens.
			rv, digest := "", ""
			last := strings.LastIndexByte(name, '-')
			if last >= 0 {
				rv, digest = name[:last], name[last+1:]
			}
			q := protocol.CoordArchiveExactQuery{Resource: id.Resource, Name: id.Name, UID: id.UID,
				ResourceVersion: rv, SHA256: digest + strings.Repeat("0", 48)}
			if last < 0 || len(digest) != 16 || q.Validate() != nil {
				return zero, protocol.HistoryErrorf(protocol.HistoryErrCorrupt, rel, "invalid revision directory")
			}
			revision := filepath.Join(rel, name)
			raw, err := readArchiveFile(a.Root, filepath.Join(dir, name, "manifest.json"), 4096)
			if err != nil {
				return zero, protocol.HistoryErrorf(protocol.HistoryErrCorrupt, revision, "unreadable revision manifest")
			}
			manifest, err := parseCoordStoredManifest(raw, filepath.ToSlash(revision))
			if err != nil {
				return zero, err
			}
			r := protocol.CoordArchiveRevision{ResourceVersion: manifest.ResourceVersion, SHA256: manifest.Object.SHA256}
			encoded, _ := protocol.CanonicalJSON(r)
			if len(result.Revisions) > 0 {
				used++
			} else {
				used-- // holds:false becomes holds:true
			}
			used += len(encoded)
			if used > budget {
				return zero, coordLookupOverflow()
			}
			result.Revisions = append(result.Revisions, r)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	result.Holds = len(result.Revisions) != 0
	slices.SortFunc(result.Revisions, func(a, b protocol.CoordArchiveRevision) int {
		if c := strings.Compare(a.ResourceVersion, b.ResourceVersion); c != 0 {
			return c
		}
		return strings.Compare(a.SHA256, b.SHA256)
	})
	return result, nil
}
