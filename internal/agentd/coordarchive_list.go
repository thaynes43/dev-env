package agentd

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
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
	afterMonth := ""
	if after != "" {
		afterMonth = after[:4] + "-" + after[4:6]
	}
	type candidate struct{ name, month string }
	candidates := make([]candidate, 0, q.Limit+1)
	monthUpper, includeUpper := afterMonth, true
	for len(candidates) < q.Limit+1 {
		// Keep at most one page-sized batch of month names. An exhausted
		// sparse batch advances the parent bound; no month is visited twice.
		months, err := a.coordListNames(base, monthUpper, includeUpper, validCoordArchiveMonth, q.Limit+1)
		if err != nil {
			return protocol.CoordArchiveListResult{}, coordGetCorrupt(base)
		}
		for _, month := range months {
			upper := ""
			if month == afterMonth {
				upper = after
			}
			remaining := q.Limit + 1 - len(candidates)
			names, err := a.coordListNames(path.Join(base, month), upper, false, protocol.ValidCoordArchiveIndexName, remaining)
			if err != nil {
				return protocol.CoordArchiveListResult{}, coordGetCorrupt(path.Join(base, month))
			}
			for _, name := range names {
				candidates = append(candidates, candidate{name: name, month: month})
			}
			if len(candidates) == q.Limit+1 {
				break
			}
		}
		if len(months) < q.Limit+1 {
			break
		}
		monthUpper, includeUpper = months[len(months)-1], false
	}
	encodedItems := 0
	for i, selected := range candidates {
		item := a.coordListItem(q, path.Join(base, selected.month, selected.name), selected.name, selected.month)
		next := ""
		if i+1 < len(candidates) {
			var err error
			next, err = q.Cursor(selected.name)
			if err != nil {
				return protocol.CoordArchiveListResult{}, err
			}
		}
		encoded, itemErr := protocol.CanonicalJSON(item)
		frame, frameErr := protocol.CanonicalJSON(protocol.CoordArchiveListResult{Items: []protocol.CoordArchiveListItem{}, Next: next})
		// Empty brackets are already in frame; add each encoded item once,
		// the separators, and agentd's final newline.
		wireBytes := len(frame) + encodedItems + len(encoded) + len(result.Items) + 1
		if itemErr != nil || frameErr != nil || wireBytes > protocol.MaxCoordArchiveLookupBytes {
			if len(result.Items) == 0 {
				return protocol.CoordArchiveListResult{}, protocol.HistoryErrorf(protocol.HistoryErrCorrupt, path.Join(base, selected.month, selected.name), "list item cannot fit the output bound")
			}
			result.Next, _ = q.Cursor(candidates[i-1].name)
			return result, nil
		}
		result.Items = append(result.Items, item)
		result.Next = next
		encodedItems += len(encoded)
		if len(result.Items) == q.Limit {
			return result, nil
		}
	}
	return result, nil
}

// coordListNames scans a directory once in fixed batches. It retains only the
// greatest limit names below upper, then returns that bounded selection newest
// first. The inclusive upper bound is used only to include a cursor's month;
// subsequent month batches and revision names use an exclusive bound.
func (a CoordArchive) coordListNames(rel, upper string, includeUpper bool, valid func(string) bool, limit int) ([]string, error) {
	if limit < 1 || limit > protocol.MaxCoordArchiveListItems+1 {
		return nil, protocol.HistoryErrorf(protocol.HistoryErrInvalid, rel, "invalid list selection bound")
	}
	d, err := openBeneath(a.Root, filepath.Join(a.Root, filepath.FromSlash(rel)), historyDirFlags)
	if err != nil {
		return nil, err
	}
	defer func() { _ = d.Close() }()
	names := []string{}
	for {
		batch, readErr := d.Readdirnames(128)
		for _, name := range batch {
			if !valid(name) {
				return nil, protocol.HistoryErrorf(protocol.HistoryErrCorrupt, rel, "invalid index directory entry")
			}
			if upper != "" && (name > upper || name == upper && !includeUpper) {
				continue
			}
			if len(names) < limit {
				names = append(names, name)
				continue
			}
			min := 0
			for i := 1; i < len(names); i++ {
				if names[i] < names[min] {
					min = i
				}
			}
			if name > names[min] {
				names[min] = name
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				sort.Slice(names, func(i, j int) bool { return names[i] > names[j] })
				return names, nil
			}
			return nil, readErr
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
	want := coordIndexEntry(got.Manifest)
	want.ArchivedAt = summary.FirstAt
	if entry != want {
		return corrupt()
	}
	projected, err := coordListProjection(got.Object)
	if err != nil {
		return corrupt()
	}
	manifest := got.Manifest
	// The index entry and UID summary already established this UID's immutable
	// first archive time. The latest revision's manifest may be newer.
	manifest.ArchivedAt = summary.FirstAt
	return protocol.CoordArchiveListItem{UID: uid, Manifest: &manifest, Object: projected}
}

func coordListIndexMatches(e protocol.CoordArchiveIndexEntry, q protocol.CoordArchiveListQuery, uid, name, month string) bool {
	if e.Format != protocol.CoordArchiveIndexFormat || e.Resource != q.Resource || e.Project != q.Project || e.UID != uid ||
		e.ArchivedAt.IsZero() || e.ArchivedAt.Location() != time.UTC || e.ArchivedAt.Format("2006-01") != month ||
		e.ArchivedAt.Format("20060102T150405.000000000Z")+"-"+uid+".json" != name {
		return false
	}
	if q.RecipientUID == "" {
		inbox := q.Resource == "inboxmessages" || q.Resource == "inboxreadacks"
		return (inbox && protocol.ValidKubeUID(e.RecipientUID)) || (!inbox && e.RecipientUID == "")
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
