package agentd

import (
	"errors"
	"io"
	"io/fs"
	"path/filepath"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// GetExact resolves an exact CLI identity. A name in multiple projects refuses
// even when one UID matches; the caller must not receive an ambiguous object.
func (a CoordArchive) GetExact(q protocol.CoordArchiveExactQuery) (protocol.CoordArchiveGetResult, error) {
	var zero protocol.CoordArchiveGetResult
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
	d, err := openBeneath(a.Root, filepath.Join(a.Root, "v1", q.Resource), historyDirFlags)
	if errors.Is(err, fs.ErrNotExist) {
		return zero, protocol.HistoryErrorf(protocol.HistoryErrNotFound, "", "revision is absent")
	}
	if err != nil {
		return zero, coordArchiveWriteError("", err)
	}
	defer func() { _ = d.Close() }()
	var found protocol.CoordArchiveGetResult
	namesFound := 0
	for {
		projects, readErr := d.Readdirnames(128)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return zero, coordArchiveWriteError("", readErr)
		}
		for _, project := range projects {
			rel, err := q.RevisionDir(project)
			if err != nil {
				return zero, err
			}
			nameDir := filepath.Join(a.Root, "v1", q.Resource, project, "records", q.Name)
			nd, err := openBeneath(a.Root, nameDir, historyDirFlags)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return zero, protocol.HistoryErrorf(protocol.HistoryErrCorrupt, "", "unreadable archived name")
			}
			_ = nd.Close()
			namesFound++
			if namesFound > 1 {
				return zero, protocol.HistoryErrorf(protocol.HistoryErrAmbiguous, "", "name exists under multiple projects")
			}
			rd, err := openBeneath(a.Root, filepath.Join(a.Root, filepath.FromSlash(rel)), historyDirFlags)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return zero, protocol.HistoryErrorf(protocol.HistoryErrCorrupt, rel, "unreadable revision")
			}
			_ = rd.Close()
			raw, err := readArchiveFile(a.Root, filepath.Join(a.Root, filepath.FromSlash(rel), "manifest.json"), 4096)
			if err != nil {
				return zero, protocol.HistoryErrorf(protocol.HistoryErrCorrupt, rel, "unreadable revision manifest")
			}
			m, err := parseCoordStoredManifest(raw, rel)
			if err != nil {
				return zero, err
			}
			if m.Object.SHA256 != q.SHA256 {
				continue
			} // Another complete digest with this directory prefix.
			found, err = a.readRevision(m.CoordArchiveManifest, rel)
			if err != nil {
				return zero, err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	if found.Object == nil {
		return zero, protocol.HistoryErrorf(protocol.HistoryErrNotFound, "", "revision is absent")
	}
	return found, nil
}
