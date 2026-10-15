package agentd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func requireCoordItem(t *testing.T, a CoordArchive, q protocol.CoordArchiveGetQuery, want protocol.CoordArchiveStoredManifest, raw []byte, firstAt time.Time) {
	t.Helper()
	got, err := a.GetItemSelected(q)
	canonical, canonErr := protocol.CanonicalJSON(json.RawMessage(raw))
	want.ArchivedAt = firstAt
	gotAt := got.Manifest.ArchivedAt
	got.Manifest.ArchivedAt = want.ArchivedAt
	if err != nil || canonErr != nil || got.Manifest != want || !bytes.Equal(got.Object, canonical) || !gotAt.Equal(firstAt) || gotAt.Location() != time.UTC {
		t.Fatalf("item selection differs: manifest=%+v err=%v", got.Manifest, err)
	}
}

func TestCoordArchiveItemGetSelectsUIDFirstReceipt(t *testing.T) {
	for _, resource := range []string{"taskclaims", "publishedcontexts", "inboxmessages", "inboxreadacks"} {
		t.Run(resource, func(t *testing.T) {
			a := coordArchiveRig(t)
			m, raw := coordStoredFixture(t, resource)
			start := time.Date(2026, 10, 15, 10, 15, 0, 0, time.UTC)
			old, oldRaw := coordSelectorFixture(t, m, raw, "ffffffff-ffff-ffff-ffff-ffffffffffff", "old")
			fresh, freshRaw := coordSelectorFixture(t, m, raw, "11111111-1111-1111-1111-111111111111", "fresh")
			oldLatest, oldLatestRaw := coordSelectorFixture(t, old, oldRaw, old.UID, "old-latest")
			freshLatest, freshLatestRaw := coordSelectorFixture(t, fresh, freshRaw, fresh.UID, "fresh-latest")
			var stored [4]protocol.CoordArchiveStoredManifest
			for n, item := range []struct {
				m   protocol.CoordArchiveManifest
				raw []byte
			}{{old, oldRaw}, {fresh, freshRaw}, {oldLatest, oldLatestRaw}, {freshLatest, freshLatestRaw}} {
				at := start.Add(time.Duration(n) * time.Hour)
				a.Now = func() time.Time { return at }
				var err error
				stored[n], err = a.Put(item.m, bytes.NewReader(item.raw))
				if err != nil {
					t.Fatal(err)
				}
			}
			before := coordTree(t, a.Root)
			requireCoordItem(t, a, protocol.CoordArchiveGetQuery{Resource: resource, Name: m.Name}, stored[3], freshLatestRaw, stored[1].ArchivedAt)
			requireCoordItem(t, a, protocol.CoordArchiveGetQuery{Resource: resource, Name: m.Name, UID: old.UID}, stored[2], oldLatestRaw, stored[0].ArchivedAt)
			if !reflect.DeepEqual(before, coordTree(t, a.Root)) {
				t.Fatal("item reads changed immutable archive")
			}
		})
	}
}

func TestCoordArchiveItemGetRefusesPinsAndBirthDamage(t *testing.T) {
	for _, kind := range []string{"pin", "missing-index", "damaged-index", "corrupt-latest", "tied-latest"} {
		t.Run(kind, func(t *testing.T) {
			a := coordArchiveRig(t)
			m, raw := coordStoredFixture(t, "taskclaims")
			first, err := a.Put(m, bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			newer, newerRaw := coordSelectorFixture(t, m, raw, m.UID, "newer")
			a.Now = func() time.Time { return first.ArchivedAt.Add(time.Hour) }
			stored, err := a.Put(newer, bytes.NewReader(newerRaw))
			if err != nil {
				t.Fatal(err)
			}
			q := protocol.CoordArchiveGetQuery{Resource: m.Resource, Name: m.Name, UID: m.UID}
			before := coordTree(t, a.Root)
			switch kind {
			case "pin":
				q.ResourceVersion, q.SHA256 = newer.ResourceVersion, newer.Object.SHA256
				got, err := a.GetItemSelected(q)
				requireCoordError(t, err, protocol.HistoryErrInvalid)
				if got.Object != nil {
					t.Fatal("pinned item read returned bytes")
				}
				// Pin rejection precedes mounted-root or identity selection, even
				// when only one pin component is supplied.
				unmounted := a
				unmounted.Root = filepath.Join(a.Root, "unmounted")
				for _, pins := range []struct{ revision, digest string }{{newer.ResourceVersion, ""}, {"", newer.Object.SHA256}, {newer.ResourceVersion, newer.Object.SHA256}} {
					query := q
					query.ResourceVersion, query.SHA256 = pins.revision, pins.digest
					got, err := unmounted.GetItemSelected(query)
					requireCoordError(t, err, protocol.HistoryErrInvalid)
					if got.Object != nil || got.Manifest != (protocol.CoordArchiveStoredManifest{}) {
						t.Fatal("rejected pin exposed item metadata or bytes")
					}
				}
			case "missing-index", "damaged-index":
				file := filepath.Join(a.Root, filepath.FromSlash(coordIndexPaths(coordIndexEntry(first))[0]))
				if kind == "missing-index" {
					err = os.Remove(file)
				} else {
					err = os.WriteFile(file, []byte("{}"), 0o600)
				}
				if err != nil {
					t.Fatal(err)
				}
				got, err := a.GetItemSelected(q)
				requireCoordError(t, err, protocol.HistoryErrCorrupt)
				if got.Object != nil {
					t.Fatal("damaged birth returned item bytes")
				}
				canonical, err := protocol.CanonicalJSON(json.RawMessage(newerRaw))
				if err != nil {
					t.Fatal(err)
				}
				for _, legacyQuery := range []protocol.CoordArchiveGetQuery{q, protocol.CoordArchiveGetQuery(coordExactQuery(newer))} {
					legacy, err := a.GetSelected(legacyQuery)
					if err != nil || legacy.Manifest.CoordArchiveManifest != stored.CoordArchiveManifest || !legacy.Manifest.ArchivedAt.Equal(stored.ArchivedAt) || legacy.Manifest.ArchivedAt.Location() != time.UTC || !bytes.Equal(legacy.Object, canonical) {
						t.Fatalf("birth damage changed legacy receipt or object: %+v, %v", legacy.Manifest, err)
					}
				}
			case "corrupt-latest":
				if err := os.WriteFile(filepath.Join(coordRevisionPath(t, a, newer), "object.json"), []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
				got, err := a.GetItemSelected(q)
				requireCoordError(t, err, protocol.HistoryErrCorrupt)
				if got.Object != nil {
					t.Fatal("corrupt latest returned fallback bytes")
				}
			case "tied-latest":
				other, otherRaw := coordSelectorFixture(t, m, raw, m.UID, "other")
				a.Now = func() time.Time { return stored.ArchivedAt }
				if _, err := a.Put(other, bytes.NewReader(otherRaw)); err != nil {
					t.Fatal(err)
				}
				got, err := a.GetItemSelected(q)
				requireCoordError(t, err, protocol.HistoryErrAmbiguous)
				if got.Object != nil {
					t.Fatal("ambiguous latest returned item bytes")
				}
				if bytes.Contains([]byte(err.Error()), []byte("supply revision/hash")) {
					t.Fatal("item ambiguity recommends pins that item queries reject")
				}
				_, legacyErr := a.GetSelected(q)
				requireCoordError(t, legacyErr, protocol.HistoryErrAmbiguous)
				if !bytes.Contains([]byte(legacyErr.Error()), []byte("supply revision/hash")) {
					t.Fatal("legacy revision recovery guidance changed")
				}
				requireCoordSelected(t, a, protocol.CoordArchiveGetQuery(coordExactQuery(newer)), newer, newerRaw)
			}
			if kind == "pin" && !reflect.DeepEqual(before, coordTree(t, a.Root)) {
				t.Fatal("rejected pinned item read changed archive")
			}
		})
	}
}

func TestCoordArchiveItemGetRefusesUnsafeAndMissingIdentity(t *testing.T) {
	a := coordArchiveRig(t)
	m, raw := coordStoredFixture(t, "taskclaims")
	got, err := a.GetItemSelected(protocol.CoordArchiveGetQuery{Resource: m.Resource, Name: "../escape"})
	requireCoordError(t, err, protocol.HistoryErrInvalid)
	if got.Object != nil {
		t.Fatal("unsafe item query returned bytes")
	}
	if entries, readErr := os.ReadDir(a.Root); readErr != nil || len(entries) != 0 {
		t.Fatalf("unsafe item query touched archive: %v", readErr)
	}
	if _, err := a.Put(m, bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	before := coordTree(t, a.Root)
	got, err = a.GetItemSelected(protocol.CoordArchiveGetQuery{Resource: m.Resource, Name: m.Name, UID: "ffffffff-ffff-ffff-ffff-ffffffffffff"})
	requireCoordError(t, err, protocol.HistoryErrNotFound)
	if got.Object != nil || !reflect.DeepEqual(before, coordTree(t, a.Root)) {
		t.Fatal("missing item identity returned bytes or changed archive")
	}
}
