package agentd

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"path"
	"path/filepath"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// List walks only the requested immutable index. It deliberately derives the
// current revision from the UID's manifests rather than trusting an index to
// carry a mutable selector.
func (a CoordArchive) List(q protocol.CoordArchiveListQuery) (protocol.CoordArchiveListResult, error) {
	var zero protocol.CoordArchiveListResult
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
	return a.listLocked(q)
}

func (a CoordArchive) listLocked(q protocol.CoordArchiveListQuery) (protocol.CoordArchiveListResult, error) {
	var after string
	if q.After != "" {
		decoded, err := protocol.CoordArchiveListCursorName(q)
		if err != nil {
			return protocol.CoordArchiveListResult{}, err
		}
		after = decoded
	}
	base := path.Join("v1", q.Resource, q.Project, "index", "all")
	if q.RecipientUID != "" {
		base = path.Join("v1", q.Resource, q.Project, "index", "recipient", q.RecipientUID)
	}
	baseDir, err := openBeneath(a.Root, filepath.Join(a.Root, filepath.FromSlash(base)), historyDirFlags)
	if errors.Is(err, fs.ErrNotExist) {
		return protocol.CoordArchiveListResult{Items: []protocol.CoordArchiveListItem{}}, nil
	}
	if err != nil {
		return protocol.CoordArchiveListResult{}, coordGetCorrupt(base)
	}
	_ = baseDir.Close()
	result := protocol.CoordArchiveListResult{Items: make([]protocol.CoordArchiveListItem, 0, q.Limit)}
	lastEmitted := ""
	afterMonth := ""
	if after != "" {
		afterMonth = after[:4] + "-" + after[4:6]
	}
	monthUpper := ""
	for {
		month, err := a.coordListNextName(base, monthUpper, validCoordArchiveMonth)
		if errors.Is(err, fs.ErrNotExist) {
			break
		}
		if err != nil {
			return protocol.CoordArchiveListResult{}, coordGetCorrupt(base)
		}
		if month == "" {
			break
		}
		monthUpper = month
		if afterMonth != "" && month > afterMonth {
			continue
		}
		nameUpper := ""
		if month == afterMonth {
			nameUpper = after
		}
		for {
			name, err := a.coordListNextName(path.Join(base, month), nameUpper, protocol.ValidCoordArchiveIndexName)
			if err != nil {
				return protocol.CoordArchiveListResult{}, coordGetCorrupt(path.Join(base, month))
			}
			if name == "" {
				break
			}
			nameUpper = name
			item := a.coordListItem(q, path.Join(base, month, name), name, month)
			candidate := append(append([]protocol.CoordArchiveListItem(nil), result.Items...), item)
			next, err := q.Cursor(name)
			if err != nil {
				return protocol.CoordArchiveListResult{}, err
			}
			trial := protocol.CoordArchiveListResult{Items: candidate, Next: next}
			encoded, err := protocol.CanonicalJSON(trial)
			if err != nil || len(encoded) > protocol.MaxCoordArchiveLookupBytes {
				if len(result.Items) == 0 {
					return protocol.CoordArchiveListResult{}, protocol.HistoryErrorf(protocol.HistoryErrCorrupt, path.Join(base, month, name), "list item cannot fit the output bound")
				}
				result.Next, _ = q.Cursor(lastEmitted)
				return result, nil
			}
			result.Items = candidate
			lastEmitted = name
			if len(result.Items) == q.Limit {
				result.Next = next
				return result, nil
			}
		}
	}
	return result, nil
}

// coordListNextName finds one greatest acceptable entry below upper while
// retaining only one name. Repeated bounded scans avoid relying on filesystem
// directory order or accumulating an unbounded month/index directory.
func (a CoordArchive) coordListNextName(rel, upper string, valid func(string) bool) (string, error) {
	d, err := openBeneath(a.Root, filepath.Join(a.Root, filepath.FromSlash(rel)), historyDirFlags)
	if err != nil {
		return "", err
	}
	defer func() { _ = d.Close() }()
	best := ""
	for {
		batch, readErr := d.Readdirnames(128)
		for _, name := range batch {
			if !valid(name) {
				return "", protocol.HistoryErrorf(protocol.HistoryErrCorrupt, rel, "invalid index directory entry")
			}
			if (upper == "" || name < upper) && name > best {
				best = name
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return best, nil
			}
			return "", readErr
		}
	}
}

func (a CoordArchive) coordListItem(q protocol.CoordArchiveListQuery, rel, indexName, month string) protocol.CoordArchiveListItem {
	uid := ""
	if protocol.ValidCoordArchiveIndexName(indexName) {
		uid = protocol.CoordArchiveIndexNameUID(indexName)
	}
	corrupt := func() protocol.CoordArchiveListItem { return protocol.CoordArchiveListItem{UID: uid, Error: "corrupt"} }
	if uid == "" {
		return corrupt()
	}
	raw, err := readArchiveFileLinks(a.Root, filepath.Join(a.Root, filepath.FromSlash(rel)), 4096, 2)
	if err != nil {
		return corrupt()
	}
	var entry protocol.CoordArchiveIndexEntry
	if err := json.Unmarshal(raw, &entry); err != nil || !coordListIndexMatches(entry, q, uid, indexName, month) {
		return corrupt()
	}
	canonical, err := protocol.CanonicalJSON(entry)
	if err != nil || !bytes.Equal(raw, canonical) {
		return corrupt()
	}
	id := protocol.CoordArchiveIdentity{Resource: q.Resource, Project: q.Project, Name: entry.Name, UID: uid}
	summary, err := a.coordUIDSummary(id)
	if err != nil || summary.Latest.UID == "" || summary.LatestTied || summary.FirstAt != entry.ArchivedAt || a.coordCheckUIDBirth(summary) != nil {
		return corrupt()
	}
	got, err := a.readRevision(summary.Latest.CoordArchiveManifest, summary.LatestRel)
	if err != nil {
		return corrupt()
	}
	projected, err := coordListProjection(got.Object)
	if err != nil {
		return corrupt()
	}
	manifest := got.Manifest
	return protocol.CoordArchiveListItem{UID: uid, Manifest: &manifest, Object: projected}
}

func coordListIndexMatches(e protocol.CoordArchiveIndexEntry, q protocol.CoordArchiveListQuery, uid, name, month string) bool {
	if e.Format != protocol.CoordArchiveIndexFormat || e.Resource != q.Resource || e.Project != q.Project || e.UID != uid ||
		e.ArchivedAt.IsZero() || e.ArchivedAt.Location() != time.UTC || e.ArchivedAt.Format("2006-01") != month ||
		e.ArchivedAt.Format("20060102T150405.000000000Z")+"-"+uid+".json" != name {
		return false
	}
	if q.RecipientUID == "" {
		return (q.Resource == "inboxmessages" && protocol.ValidKubeUID(e.RecipientUID)) || (q.Resource != "inboxmessages" && e.RecipientUID == "")
	}
	return e.RecipientUID == q.RecipientUID
}

func coordListProjection(raw json.RawMessage) (json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var object map[string]any
	if err := dec.Decode(&object); err != nil {
		return nil, err
	}
	spec, ok := object["spec"].(map[string]any)
	if !ok {
		return nil, errors.New("missing spec")
	}
	delete(spec, "text")
	return protocol.CanonicalJSON(object)
}

func validCoordArchiveMonth(month string) bool {
	if len(month) != len("2006-01") {
		return false
	}
	_, err := time.Parse("2006-01", month)
	return err == nil
}
