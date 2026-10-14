package protocol

import (
	"path"
	"strings"
)

// Get selectors use recorded archive times only when pins are omitted.
// Resource versions and hashes are identities, never chronological keys.
type CoordArchiveGetQuery CoordArchiveExactQuery

func (q CoordArchiveGetQuery) Validate() error {
	prefix, _ := coordArchiveResource(q.Resource)
	if prefix == "" || !coordArchiveName.MatchString(q.Name) || !strings.HasPrefix(q.Name, prefix+".") || q.UID != "" && !ValidKubeUID(q.UID) {
		return HistoryErrorf(HistoryErrInvalid, "", "coordination resource/name and optional UID required")
	}
	if (q.ResourceVersion != "" || q.SHA256 != "") && (!coordArchiveRevision.MatchString(q.ResourceVersion) || !coordArchiveDigest.MatchString(q.SHA256)) {
		return HistoryErrorf(HistoryErrInvalid, "", "resourceVersion and complete SHA256 must be supplied together")
	}
	return nil
}

func (q CoordArchiveGetQuery) NameDir(project string) (string, error) {
	if err := q.Validate(); err != nil {
		return "", err
	}
	if !coordArchiveLabel.MatchString(project) {
		return "", HistoryErrorf(HistoryErrCorrupt, "", "invalid archived project")
	}
	return path.Join("v1", q.Resource, project, "records", q.Name), nil
}
