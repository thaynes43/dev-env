package agentd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func coordSelectorFixture(t *testing.T, base protocol.CoordArchiveManifest, raw []byte, uid, rv string) (protocol.CoordArchiveManifest, []byte) {
	t.Helper()
	var object map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&object); err != nil {
		t.Fatal(err)
	}
	m := base
	m.UID = uid
	m.ResourceVersion = rv
	meta := object["metadata"].(map[string]any)
	meta["uid"] = uid
	meta["resourceVersion"] = rv
	data, err := protocol.CanonicalJSON(object)
	if err != nil {
		t.Fatal(err)
	}
	bindCoordStoredFixture(t, &m, data)
	return m, data
}

func requireCoordSelected(t *testing.T, a CoordArchive, q protocol.CoordArchiveGetQuery, m protocol.CoordArchiveManifest, raw []byte) {
	t.Helper()
	got, err := a.GetSelected(q)
	canonical, canonErr := protocol.CanonicalJSON(json.RawMessage(raw))
	if err != nil || canonErr != nil || got.Manifest.CoordArchiveManifest != m || !bytes.Equal(got.Object, canonical) {
		t.Fatalf("selection differs: %v", err)
	}
}

func TestCoordArchiveSelectorsUseUIDFirstAndRevisionRecordedTimes(t *testing.T) {
	for _, resource := range []string{"taskclaims", "publishedcontexts", "inboxmessages", "inboxreadacks"} {
		t.Run(resource, func(t *testing.T) {
			a := coordArchiveRig(t)
			m, raw := coordStoredFixture(t, resource)
			start := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
			old, oldRaw := coordSelectorFixture(t, m, raw, "ffffffff-ffff-ffff-ffff-ffffffffffff", "zzz-old")
			fresh, freshRaw := coordSelectorFixture(t, m, raw, "11111111-1111-1111-1111-111111111111", "zzz-first")
			recapture, recaptureRaw := coordSelectorFixture(t, old, oldRaw, old.UID, "zzz-recapture")
			latest, latestRaw := coordSelectorFixture(t, fresh, freshRaw, fresh.UID, "aaa-latest")
			for n, item := range []struct {
				m   protocol.CoordArchiveManifest
				raw []byte
			}{{old, oldRaw}, {fresh, freshRaw}, {recapture, recaptureRaw}, {latest, latestRaw}} {
				at := start.Add(time.Duration(n) * time.Hour)
				a.Now = func() time.Time { return at }
				if _, err := a.Put(item.m, bytes.NewReader(item.raw)); err != nil {
					t.Fatal(err)
				}
				if n == 2 {
					requireCoordSelected(t, a, protocol.CoordArchiveGetQuery{Resource: resource, Name: m.Name}, fresh, freshRaw)
				}
			}
			q := protocol.CoordArchiveGetQuery{Resource: resource, Name: m.Name}
			requireCoordSelected(t, a, q, latest, latestRaw)
			q.UID = old.UID
			requireCoordSelected(t, a, q, recapture, recaptureRaw)
			q.UID = fresh.UID
			q.ResourceVersion = fresh.ResourceVersion
			q.SHA256 = fresh.Object.SHA256
			requireCoordSelected(t, a, q, fresh, freshRaw)
			q.UID = ""
			requireCoordSelected(t, a, q, fresh, freshRaw) // pair applies to selected UID
			q.ResourceVersion = recapture.ResourceVersion
			q.SHA256 = recapture.Object.SHA256
			got, err := a.GetSelected(q)
			requireCoordError(t, err, protocol.HistoryErrNotFound)
			if got.Object != nil {
				t.Fatal("searched older UID for requested revision")
			}
		})
	}
}

func TestCoordArchiveSelectorsRefuseTimestampTiesButExactPinsWork(t *testing.T) {
	a := coordArchiveRig(t)
	m, raw := coordStoredFixture(t, "taskclaims")
	first, firstRaw := coordSelectorFixture(t, m, raw, m.UID, "aaa")
	second, secondRaw := coordSelectorFixture(t, m, raw, m.UID, "zzz")
	for _, item := range []struct {
		m   protocol.CoordArchiveManifest
		raw []byte
	}{{first, firstRaw}, {second, secondRaw}} {
		if _, err := a.Put(item.m, bytes.NewReader(item.raw)); err != nil {
			t.Fatal(err)
		}
	}
	q := protocol.CoordArchiveGetQuery{Resource: m.Resource, Name: m.Name, UID: m.UID}
	got, err := a.GetSelected(q)
	requireCoordError(t, err, protocol.HistoryErrAmbiguous)
	if got.Object != nil {
		t.Fatal("revision time tie returned object")
	}
	q.ResourceVersion = first.ResourceVersion
	q.SHA256 = first.Object.SHA256
	requireCoordSelected(t, a, q, first, firstRaw)
	other, otherRaw := coordSelectorFixture(t, m, raw, "ffffffff-ffff-ffff-ffff-ffffffffffff", "aaa")
	if _, err := a.Put(other, bytes.NewReader(otherRaw)); err != nil {
		t.Fatal(err)
	}
	q.UID = ""
	got, err = a.GetSelected(q)
	requireCoordError(t, err, protocol.HistoryErrAmbiguous)
	if got.Object != nil {
		t.Fatal("UID time tie returned object")
	}
	q.UID = other.UID
	q.ResourceVersion = other.ResourceVersion
	q.SHA256 = other.Object.SHA256
	requireCoordSelected(t, a, q, other, otherRaw)
}

func TestCoordArchiveSelectorsNeverFallbackFromCorruptLatest(t *testing.T) {
	for _, kind := range []string{"object", "manifest", "immutable-identity", "empty-uid", "uid-link", "revision-link"} {
		t.Run(kind, func(t *testing.T) {
			a := coordArchiveRig(t)
			m, raw := coordStoredFixture(t, "taskclaims")
			at := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
			a.Now = func() time.Time { return at }
			if _, err := a.Put(m, bytes.NewReader(raw)); err != nil {
				t.Fatal(err)
			}
			later, laterRaw := coordSelectorFixture(t, m, raw, m.UID, "later")
			at = at.Add(time.Hour)
			stored, err := a.Put(later, bytes.NewReader(laterRaw))
			if err != nil {
				t.Fatal(err)
			}
			dir := coordRevisionPath(t, a, later)
			uidDir := filepath.Dir(dir)
			switch kind {
			case "object":
				if err := os.WriteFile(filepath.Join(dir, "object.json"), []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "manifest":
				if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "immutable-identity":
				stored.Namespace = "other"
				data, err := protocol.CanonicalJSON(stored)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0o600); err != nil {
					t.Fatal(err)
				}
			case "empty-uid":
				if err := os.Mkdir(filepath.Join(filepath.Dir(uidDir), "ffffffff-ffff-ffff-ffff-ffffffffffff"), 0o700); err != nil {
					t.Fatal(err)
				}
			case "uid-link":
				target := filepath.Join(t.TempDir(), "uid")
				if err := os.Rename(uidDir, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, uidDir); err != nil {
					t.Fatal(err)
				}
			case "revision-link":
				target := filepath.Join(t.TempDir(), "rev")
				if err := os.Rename(dir, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, dir); err != nil {
					t.Fatal(err)
				}
			}
			q := protocol.CoordArchiveGetQuery{Resource: m.Resource, Name: m.Name}
			got, err := a.GetSelected(q)
			requireCoordError(t, err, protocol.HistoryErrCorrupt)
			if got.Object != nil {
				t.Fatal("corrupt latest returned fallback bytes")
			}
			if kind == "object" || kind == "manifest" || kind == "empty-uid" {
				requireCoordSelected(t, a, protocol.CoordArchiveGetQuery(coordExactQuery(m)), m, raw)
			}
		})
	}
}

func TestCoordArchiveSelectorsValidateAndPreserveProjectAmbiguity(t *testing.T) {
	a := coordArchiveRig(t)
	m, raw := coordStoredFixture(t, "taskclaims")
	q := protocol.CoordArchiveGetQuery{Resource: m.Resource, Name: m.Name}
	bad := []protocol.CoordArchiveGetQuery{{Resource: "../escape", Name: m.Name}, {Resource: m.Resource, Name: "../escape"}, {Resource: m.Resource, Name: m.Name, UID: "../escape"}, {Resource: m.Resource, Name: m.Name, ResourceVersion: "42"}, {Resource: m.Resource, Name: m.Name, SHA256: m.Object.SHA256}}
	for _, query := range bad {
		got, err := a.GetSelected(query)
		requireCoordError(t, err, protocol.HistoryErrInvalid)
		if got.Object != nil {
			t.Fatal("invalid query returned bytes")
		}
	}
	entries, err := os.ReadDir(a.Root)
	if err != nil || len(entries) != 0 {
		t.Fatal("invalid query touched archive")
	}
	if _, err := a.Put(m, bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	other := m
	other.Project = "other"
	var object map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&object); err != nil {
		t.Fatal(err)
	}
	object["spec"].(map[string]any)["identity"].(map[string]any)["project"] = "other"
	otherRaw, err := protocol.CanonicalJSON(object)
	if err != nil {
		t.Fatal(err)
	}
	bindCoordStoredFixture(t, &other, otherRaw)
	if _, err := a.Put(other, bytes.NewReader(otherRaw)); err != nil {
		t.Fatal(err)
	}
	for _, query := range []protocol.CoordArchiveGetQuery{q, {Resource: m.Resource, Name: m.Name, UID: m.UID}, protocol.CoordArchiveGetQuery(coordExactQuery(m))} {
		got, err := a.GetSelected(query)
		requireCoordError(t, err, protocol.HistoryErrAmbiguous)
		if got.Object != nil {
			t.Fatal("ambiguous project returned bytes")
		}
	}
}

func TestCoordArchiveSelectorsReadPastUIDAndRevisionBatches(t *testing.T) {
	a := coordArchiveRig(t)
	m, raw := coordStoredFixture(t, "taskclaims")
	winnerAt := time.Date(2026, 10, 14, 16, 0, 0, 0, time.UTC)
	a.Now = func() time.Time { return winnerAt }
	winner, err := a.Put(m, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	for n := 2; n <= 130; n++ {
		other := winner
		other.UID = fmt.Sprintf("%08x-2222-2222-2222-222222222222", n)
		other.ArchivedAt = winnerAt.Add(-time.Duration(n) * time.Hour)
		writeCoordLookupManifest(t, a, other)
		entry := coordIndexEntry(other)
		file := filepath.Join(a.Root, filepath.FromSlash(coordIndexPaths(entry)[0]))
		if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
			t.Fatal(err)
		}
		data, err := protocol.CanonicalJSON(entry)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	q := protocol.CoordArchiveGetQuery{Resource: m.Resource, Name: m.Name}
	requireCoordSelected(t, a, q, m, raw)
	id := coordLookupIdentity(m)
	recordRel, err := id.RecordDir()
	if err != nil {
		t.Fatal(err)
	}
	nameDir := filepath.Dir(filepath.Join(a.Root, filepath.FromSlash(recordRel)))
	d, err := os.Open(nameDir)
	if err != nil {
		t.Fatal(err)
	}
	names, err := d.Readdirnames(130)
	_ = d.Close()
	if err != nil || len(names) != 130 {
		t.Fatalf("UID batch fixture: %v", err)
	}
	// Change only a child manifest after observing its actual position, never
	// assume directory insertion order establishes the second batch.
	brokenUID := names[128]
	if brokenUID == m.UID {
		brokenUID = names[129]
	}
	broken := winner
	broken.UID = brokenUID
	if err := os.WriteFile(filepath.Join(coordRevisionPath(t, a, broken.CoordArchiveManifest), "manifest.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := a.GetSelected(q)
	requireCoordError(t, err, protocol.HistoryErrCorrupt)
	if got.Object != nil {
		t.Fatal("ignored corruption past UID batch")
	}
	q.UID = m.UID
	for n := 0; n < 129; n++ {
		older := winner
		older.ResourceVersion = fmt.Sprintf("zzz-%03d", n)
		older.ArchivedAt = winnerAt.Add(-time.Duration(n+1) * time.Second)
		writeCoordLookupManifest(t, a, older)
	}
	requireCoordSelected(t, a, q, m, raw)
	uidDir := filepath.Join(a.Root, filepath.FromSlash(recordRel))
	rd, err := os.Open(uidDir)
	if err != nil {
		t.Fatal(err)
	}
	revisions, err := rd.Readdirnames(130)
	_ = rd.Close()
	if err != nil || len(revisions) != 130 {
		t.Fatalf("revision batch fixture: %v", err)
	}
	latestDir := filepath.Base(coordRevisionPath(t, a, m))
	brokenRevision := revisions[128]
	if brokenRevision == latestDir {
		brokenRevision = revisions[129]
	}
	if err := os.WriteFile(filepath.Join(uidDir, brokenRevision, "manifest.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = a.GetSelected(q)
	requireCoordError(t, err, protocol.HistoryErrCorrupt)
	if got.Object != nil {
		t.Fatal("ignored corruption past revision batch")
	}
}

func TestCoordArchiveSelectorsRefuseMissingOrDamagedUIDBirth(t *testing.T) {
	for _, kind := range []string{"missing-index", "damaged-index", "lost-first-manifest"} {
		t.Run(kind, func(t *testing.T) {
			a := coordArchiveRig(t)
			m, raw := coordStoredFixture(t, "taskclaims")
			first, err := a.Put(m, bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			newer, newRaw := coordSelectorFixture(t, m, raw, m.UID, "newer")
			a.Now = func() time.Time { return first.ArchivedAt.Add(time.Hour) }
			if _, err := a.Put(newer, bytes.NewReader(newRaw)); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(a.Root, filepath.FromSlash(coordIndexPaths(coordIndexEntry(first))[0]))
			switch kind {
			case "missing-index":
				if err := os.Rename(file, filepath.Join(t.TempDir(), "preserved-index.json")); err != nil {
					t.Fatal(err)
				}
			case "damaged-index":
				if err := os.WriteFile(file, []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "lost-first-manifest":
				if err := os.Rename(coordRevisionPath(t, a, m), filepath.Join(t.TempDir(), "preserved-first")); err != nil {
					t.Fatal(err)
				}
			}
			q := protocol.CoordArchiveGetQuery{Resource: m.Resource, Name: m.Name}
			got, err := a.GetSelected(q)
			requireCoordError(t, err, protocol.HistoryErrCorrupt)
			if got.Object != nil {
				t.Fatal("unknown birth silently reordered UID")
			}
			q.UID = m.UID
			requireCoordSelected(t, a, q, newer, newRaw)
		})
	}
}
