package agentd

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func TestCoordArchiveLatestGetUsesUIDFirstArchivedAt(t *testing.T) {
	a := coordArchiveRig(t)
	firstManifest, firstRaw := coordStoredFixture(t, "taskclaims")
	firstAt := time.Date(2026, 10, 15, 10, 15, 0, 0, time.UTC)
	secondManifest, secondRaw := nextCoordFixture(t, firstManifest, firstRaw, "43")
	secondAt := firstAt.Add(time.Hour)

	a.Now = func() time.Time { return firstAt }
	first, err := a.Put(firstManifest, bytes.NewReader(firstRaw))
	if err != nil {
		t.Fatal(err)
	}
	a.Now = func() time.Time { return secondAt }
	second, err := a.Put(secondManifest, bytes.NewReader(secondRaw))
	if err != nil {
		t.Fatal(err)
	}
	before := coordTree(t, a.Root)
	wantObject, err := protocol.CanonicalJSON(json.RawMessage(secondRaw))
	if err != nil {
		t.Fatal(err)
	}

	defaultLatest, err := a.GetSelected(protocol.CoordArchiveGetQuery{Resource: firstManifest.Resource, Name: firstManifest.Name})
	if err != nil || defaultLatest.Manifest.ArchivedAt != first.ArchivedAt || defaultLatest.Manifest.CoordArchiveManifest != second.CoordArchiveManifest || !bytes.Equal(defaultLatest.Object, wantObject) {
		t.Fatalf("default latest GET changed receipt or selected bytes: %+v, %v", defaultLatest.Manifest, err)
	}
	explicitLatest, err := a.GetSelected(protocol.CoordArchiveGetQuery{Resource: firstManifest.Resource, Name: firstManifest.Name, UID: firstManifest.UID})
	if err != nil || explicitLatest.Manifest != second {
		t.Fatalf("explicit UID latest did not retain revision receipt: %+v, %v", explicitLatest.Manifest, err)
	}
	for _, query := range []protocol.CoordArchiveGetQuery{protocol.CoordArchiveGetQuery(coordExactQuery(firstManifest)), protocol.CoordArchiveGetQuery(coordExactQuery(secondManifest))} {
		got, err := a.GetSelected(query)
		want := first
		if query.ResourceVersion == secondManifest.ResourceVersion {
			want = second
		}
		if err != nil || got.Manifest != want {
			t.Fatalf("exact pin did not retain stored receipt: %+v, %v", got.Manifest, err)
		}
	}
	if !reflect.DeepEqual(before, coordTree(t, a.Root)) {
		t.Fatal("GET changed immutable archive manifests")
	}
}
