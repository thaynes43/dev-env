package agentd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func coordExactQuery(m protocol.CoordArchiveManifest) protocol.CoordArchiveExactQuery {
	return protocol.CoordArchiveExactQuery{Resource: m.Resource, Name: m.Name, UID: m.UID, ResourceVersion: m.ResourceVersion, SHA256: m.Object.SHA256}
}

func TestCoordArchiveExactGetRefusesMissingAndDamagedManifest(t *testing.T) {
	a := coordArchiveRig(t)
	m, raw := coordStoredFixture(t, "taskclaims")
	q := coordExactQuery(m)
	got, err := a.GetExact(q)
	requireCoordError(t, err, protocol.HistoryErrNotFound)
	if got.Object != nil {
		t.Fatal("missing revision returned bytes")
	}
	if _, err := a.Put(m, bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	got, err = a.GetExact(q)
	if err != nil || got.Manifest.CoordArchiveManifest != m {
		t.Fatalf("exact get differs: %v", err)
	}
	if err := os.Remove(filepath.Join(coordRevisionPath(t, a, m), "manifest.json")); err != nil {
		t.Fatal(err)
	}
	got, err = a.GetExact(q)
	requireCoordError(t, err, protocol.HistoryErrCorrupt)
	if got.Object != nil {
		t.Fatal("missing stored manifest returned bytes")
	}
}

func TestCoordArchiveExactGetRefusesUnsafeOrAmbiguousNames(t *testing.T) {
	a := coordArchiveRig(t)
	m, raw := coordStoredFixture(t, "taskclaims")
	q := coordExactQuery(m)
	for _, bad := range []protocol.CoordArchiveExactQuery{
		{Resource: "../escape"},
		func() protocol.CoordArchiveExactQuery { b := q; b.Name = "../escape"; return b }(),
		func() protocol.CoordArchiveExactQuery { b := q; b.UID = "../escape"; return b }(),
		func() protocol.CoordArchiveExactQuery { b := q; b.ResourceVersion = "../escape"; return b }(),
		func() protocol.CoordArchiveExactQuery { b := q; b.SHA256 = strings.Repeat("z", 64); return b }(),
	} {
		got, err := a.GetExact(bad)
		requireCoordError(t, err, protocol.HistoryErrInvalid)
		if got.Object != nil {
			t.Fatal("unsafe query returned bytes")
		}
	}
	if _, err := a.Put(m, bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&object); err != nil {
		t.Fatal(err)
	}
	object["spec"].(map[string]any)["identity"].(map[string]any)["project"] = "other"
	changed, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	other := m
	other.Project = "other"
	bindCoordStoredFixture(t, &other, changed)
	if _, err := a.Put(other, bytes.NewReader(changed)); err != nil {
		t.Fatal(err)
	}
	got, err := a.GetExact(q)
	requireCoordError(t, err, protocol.HistoryErrAmbiguous)
	if got.Object != nil {
		t.Fatal("ambiguous name returned an object")
	}
}

func TestCoordArchiveUnmarkedPutRefusesUnknownGraceState(t *testing.T) {
	a := coordArchiveRig(t)
	m, raw := coordStoredFixture(t, "taskclaims")
	stored, err := a.PutUnmarked(m, bytes.NewReader(raw))
	if err != nil || stored.GraceStart != nil {
		t.Fatalf("unmarked put: %v", err)
	}
	events := filepath.Join(filepath.Dir(coordRevisionPath(t, a, m)), "events")
	if err := os.Mkdir(events, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := a.PutUnmarked(m, bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(events, "00000001-absent.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := coordTree(t, a.Root)
	refused, err := a.PutUnmarked(m, bytes.NewReader(raw))
	requireCoordError(t, err, protocol.HistoryErrInvalid)
	if refused.Manifest != (protocol.CoordArchiveStoredManifest{}) || !reflect.DeepEqual(before, coordTree(t, a.Root)) {
		t.Fatal("unknown journal state returned a receipt or changed the archive")
	}
}
