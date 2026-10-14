package agentd

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func coordLookupIdentity(m protocol.CoordArchiveManifest) protocol.CoordArchiveIdentity {
	return protocol.CoordArchiveIdentity{Resource: m.Resource, Project: m.Project, Name: m.Name, UID: m.UID}
}

func writeCoordLookupManifest(t *testing.T, a CoordArchive, m protocol.CoordArchiveStoredManifest) {
	t.Helper()
	dir := coordRevisionPath(t, a, m.CoordArchiveManifest)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := protocol.CanonicalJSON(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCoordArchiveLookupDiscoversMetadataWithoutReadingObjects(t *testing.T) {
	for _, resource := range []string{"taskclaims", "publishedcontexts", "inboxmessages", "inboxreadacks"} {
		t.Run(resource, func(t *testing.T) {
			a := coordArchiveRig(t)
			m, raw := coordStoredFixture(t, resource)
			if _, err := a.Put(m, bytes.NewReader(raw)); err != nil {
				t.Fatal(err)
			}
			id := coordLookupIdentity(m)
			missing := id
			missing.UID = "99999999-9999-9999-9999-999999999999"
			if err := os.WriteFile(filepath.Join(coordRevisionPath(t, a, m), "object.json"), []byte("{}"), 0o600); err != nil {
				t.Fatal(err)
			}
			before := coordTree(t, a.Root)
			got, err := a.LookupUnmarked([]protocol.CoordArchiveIdentity{missing, id, id})
			if err != nil || len(got) != 3 {
				t.Fatalf("lookup: %v", err)
			}
			if got[0].Holds || got[0].GraceStart != nil || len(got[0].Revisions) != 0 || got[0].CoordArchiveIdentity != missing {
				t.Fatal("absent identity differs")
			}
			want := protocol.CoordArchiveLookupResult{CoordArchiveIdentity: id, Holds: true, Revisions: []protocol.CoordArchiveRevision{{ResourceVersion: m.ResourceVersion, SHA256: m.Object.SHA256}}}
			if !reflect.DeepEqual(got[1], want) || !reflect.DeepEqual(got[2], want) {
				t.Fatal("metadata selector/order differs")
			}
			if !reflect.DeepEqual(before, coordTree(t, a.Root)) {
				t.Fatal("lookup changed archive")
			}
			verified, err := a.GetExact(coordExactQuery(m))
			requireCoordError(t, err, protocol.HistoryErrCorrupt)
			if verified.Object != nil {
				t.Fatal("corrupt object returned")
			}
		})
	}
}

func TestCoordArchiveLookupBatchesAndExactOutputBudget(t *testing.T) {
	a := coordArchiveRig(t)
	m, raw := coordStoredFixture(t, "taskclaims")
	stored, err := a.Put(m, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	// Metadata-only fixtures cross the 128-name directory batch, without 129 puts
	// or reading object bytes. Hyphens remain legal opaque resource versions.
	for n := 0; n < 129; n++ {
		other := stored
		other.ResourceVersion = fmt.Sprintf("%03d-with-hyphen", n)
		writeCoordLookupManifest(t, a, other)
	}
	id := coordLookupIdentity(m)
	rel, err := id.RecordDir()
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := lockHistoryArchive(a.Root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	got, err := a.lookupUnmarked(id, rel, protocol.MaxCoordArchiveLookupBytes)
	if err != nil || len(got.Revisions) != 130 || got.Revisions[0].ResourceVersion != "000-with-hyphen" || got.Revisions[128].ResourceVersion != "128-with-hyphen" || got.Revisions[129].ResourceVersion != "42" {
		t.Fatalf("batch/sort: %v; %d", err, len(got.Revisions))
	}
	encoded, err := protocol.CanonicalJSON(got)
	if err != nil {
		t.Fatal(err)
	}
	exact, err := a.lookupUnmarked(id, rel, len(encoded))
	if err != nil || !reflect.DeepEqual(exact, got) {
		t.Fatalf("exact budget: %v", err)
	}
	refused, err := a.lookupUnmarked(id, rel, len(encoded)-1)
	requireCoordError(t, err, protocol.HistoryErrInvalid)
	if refused.Revisions != nil {
		t.Fatal("overflow returned partial selectors")
	}
}

func TestCoordArchiveLookupRefusesUnknownOrCorruptMetadata(t *testing.T) {
	for _, kind := range []string{"journal", "journal-link", "manifest", "missing-manifest", "manifest-link", "revision-link", "wrong-identity", "bad-directory"} {
		t.Run(kind, func(t *testing.T) {
			a := coordArchiveRig(t)
			m, raw := coordStoredFixture(t, "taskclaims")
			stored, err := a.Put(m, bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			dir := coordRevisionPath(t, a, m)
			uidDir := filepath.Dir(dir)
			mf := filepath.Join(dir, "manifest.json")
			want := protocol.HistoryErrCorrupt
			switch kind {
			case "journal":
				events := filepath.Join(uidDir, "events")
				if err := os.Mkdir(events, 0o700); err != nil {
					t.Fatal(err)
				}
				if _, err := a.LookupUnmarked([]protocol.CoordArchiveIdentity{coordLookupIdentity(m)}); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(events, "00000001-absent.json"), []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
				want = protocol.HistoryErrInvalid
			case "journal-link":
				if err := os.Symlink(t.TempDir(), filepath.Join(uidDir, "events")); err != nil {
					t.Fatal(err)
				}
				want = protocol.HistoryErrNoArchive
			case "manifest":
				if err := os.WriteFile(mf, []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "missing-manifest":
				if err := os.Remove(mf); err != nil {
					t.Fatal(err)
				}
			case "manifest-link":
				target := filepath.Join(t.TempDir(), "manifest.json")
				if err := os.Rename(mf, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(target, mf); err != nil {
					t.Fatal(err)
				}
			case "revision-link":
				target := filepath.Join(t.TempDir(), "revision")
				if err := os.Rename(dir, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, dir); err != nil {
					t.Fatal(err)
				}
			case "wrong-identity":
				stored.UID = "99999999-9999-9999-9999-999999999999"
				data, err := protocol.CanonicalJSON(stored)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(mf, data, 0o600); err != nil {
					t.Fatal(err)
				}
			case "bad-directory":
				if err := os.Mkdir(filepath.Join(uidDir, "unknown"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			id := coordLookupIdentity(m)
			missing := id
			missing.UID = "88888888-8888-8888-8888-888888888888"
			got, err := a.LookupUnmarked([]protocol.CoordArchiveIdentity{missing, id})
			requireCoordError(t, err, want)
			if got != nil {
				t.Fatal("failed batch returned partial results")
			}
		})
	}
}

func TestCoordArchiveLookupValidatesWholeBatchBeforeAccess(t *testing.T) {
	a := coordArchiveRig(t)
	m, _ := coordStoredFixture(t, "taskclaims")
	id := coordLookupIdentity(m)
	for _, field := range []string{"resource", "project", "name", "uid"} {
		bad := id
		switch field {
		case "resource":
			bad.Resource = "../escape"
		case "project":
			bad.Project = "../escape"
		case "name":
			bad.Name = "../escape"
		case "uid":
			bad.UID = "../escape"
		}
		got, err := a.LookupUnmarked([]protocol.CoordArchiveIdentity{id, bad})
		requireCoordError(t, err, protocol.HistoryErrInvalid)
		if got != nil {
			t.Fatal("invalid identity returned results")
		}
	}
	for _, ids := range [][]protocol.CoordArchiveIdentity{nil, make([]protocol.CoordArchiveIdentity, 1025)} {
		_, err := a.LookupUnmarked(ids)
		requireCoordError(t, err, protocol.HistoryErrInvalid)
	}
	entries, err := os.ReadDir(a.Root)
	if err != nil || len(entries) != 0 {
		t.Fatal("invalid batch touched archive")
	}
	ids := make([]protocol.CoordArchiveIdentity, 1024)
	for n := range ids {
		ids[n] = id
	}
	got, err := a.LookupUnmarked(ids)
	if err != nil || len(got) != 1024 {
		t.Fatalf("maximum batch refused: %v", err)
	}
}
