package agentd

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func coordArchiveRig(t *testing.T) CoordArchive {
	t.Helper()
	home := t.TempDir()
	a := NewCoordArchive(Settings{Home: home})
	if err := os.Mkdir(a.Root, 0o700); err != nil {
		t.Fatal(err)
	}
	old := coordArchiveIsMounted
	coordArchiveIsMounted = func(string, string) error { return nil }
	t.Cleanup(func() { coordArchiveIsMounted = old })
	a.Now = func() time.Time { return time.Date(2026, 10, 14, 6, 0, 0, 123456789, time.FixedZone("offset", 3600)) }
	return a
}

func coordStoredFixture(t *testing.T, resource string) (protocol.CoordArchiveManifest, []byte) {
	t.Helper()
	kinds := map[string][2]string{
		"taskclaims": {"task-claim", "TaskClaim"}, "publishedcontexts": {"published-context", "PublishedContext"},
		"inboxmessages": {"message", "InboxMessage"}, "inboxreadacks": {"read-ack", "InboxReadAck"},
	}
	kind := kinds[resource]
	m := protocol.CoordArchiveManifest{Format: protocol.CoordArchiveFormat, APIVersion: protocol.CoordArchiveAPIVersion,
		Resource: resource, Kind: kind[1], Namespace: "dev-env-system", Project: "dev-env",
		Name: kind[0] + "." + strings.Repeat("a", 32) + "." + strings.Repeat("b", 32),
		UID:  "11111111-1111-1111-1111-111111111111", ResourceVersion: "42", ArchivedBy: "dev-env-operator fixture"}
	spec := map[string]any{"project": m.Project, "text": "<synthetic context>"}
	if resource == "taskclaims" {
		delete(spec, "project")
		spec["identity"] = map[string]any{"project": m.Project}
	}
	if resource == "inboxmessages" || resource == "inboxreadacks" {
		m.RecipientUID = "22222222-2222-2222-2222-222222222222"
		spec["recipient"] = map[string]any{"uid": m.RecipientUID}
	}
	if resource == "inboxreadacks" {
		m.MessageUID = "33333333-3333-3333-3333-333333333333"
		spec["messageUID"] = m.MessageUID
	}
	o := map[string]any{"apiVersion": m.APIVersion, "kind": m.Kind, "spec": spec,
		"metadata":    map[string]any{"namespace": m.Namespace, "name": m.Name, "uid": m.UID, "resourceVersion": m.ResourceVersion},
		"futureField": json.Number("90071992547409931234567890")}
	raw, err := json.MarshalIndent(o, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	bindCoordStoredFixture(t, &m, raw)
	return m, raw
}

func bindCoordStoredFixture(t *testing.T, m *protocol.CoordArchiveManifest, raw []byte) {
	t.Helper()
	canonical, err := protocol.CanonicalJSON(json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(canonical)
	m.Object = protocol.CoordArchiveObjectDigest{SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(canonical))}
}

func coordRevisionPath(t *testing.T, a CoordArchive, m protocol.CoordArchiveManifest) string {
	t.Helper()
	rel, err := protocol.CoordArchiveRevisionDir(m)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(a.Root, filepath.FromSlash(rel))
}

func requireCoordError(t *testing.T, err error, code string) {
	t.Helper()
	var got *protocol.HistoryError
	if !errors.As(err, &got) || got.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}

func coordTree(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if d.IsDir() {
			result[rel] = "directory"
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		result[rel] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestCoordArchiveStoredRoundTripAndIdempotence(t *testing.T) {
	a := coordArchiveRig(t)
	for _, resource := range []string{"taskclaims", "publishedcontexts", "inboxmessages", "inboxreadacks"} {
		t.Run(resource, func(t *testing.T) {
			m, raw := coordStoredFixture(t, resource)
			stored, err := a.Put(m, bytes.NewReader(raw))
			if err != nil || !stored.ArchivedAt.Equal(a.Now()) || stored.ArchivedAt.Location() != time.UTC {
				t.Fatalf("publication: %v, %v", stored.ArchivedAt, err)
			}
			expected, err := protocol.VerifyCoordArchiveObject(m, bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			got, err := a.Get(m)
			if err != nil || got.Manifest != stored || !bytes.Equal(got.Object, expected) {
				t.Fatalf("round trip differs: %v", err)
			}
			if !bytes.Contains(got.Object, []byte("90071992547409931234567890")) {
				t.Fatal("lost unknown field or number precision")
			}
			before := coordTree(t, a.Root)
			m.ArchivedBy = "dev-env-operator later-version"
			later := a
			later.Now = func() time.Time { return a.Now().Add(time.Hour) }
			again, err := later.Put(m, bytes.NewReader(raw))
			if err != nil || again != stored || !reflect.DeepEqual(before, coordTree(t, a.Root)) {
				t.Fatalf("identical revision was changed: %v", err)
			}
			for _, name := range []string{"object.json", "manifest.json"} {
				fi, err := os.Stat(filepath.Join(coordRevisionPath(t, a, m), name))
				if err != nil || fi.Mode().Perm() != 0o600 {
					t.Fatalf("private file mode: %v", err)
				}
			}
		})
	}
}

func TestCoordArchiveSeparatesUIDsAndRevisionForms(t *testing.T) {
	a := coordArchiveRig(t)
	m, raw := coordStoredFixture(t, "publishedcontexts")
	variants := []struct{ uid, rv, text string }{
		{m.UID, m.ResourceVersion, "first"}, {m.UID, m.ResourceVersion, "defaulted form"},
		{m.UID, "43", "later revision"}, {"44444444-4444-4444-4444-444444444444", m.ResourceVersion, "reclaimed name"},
	}
	paths := map[string]bool{}
	for _, variant := range variants {
		var object map[string]any
		if err := json.Unmarshal(raw, &object); err != nil {
			t.Fatal(err)
		}
		object["metadata"].(map[string]any)["uid"] = variant.uid
		object["metadata"].(map[string]any)["resourceVersion"] = variant.rv
		object["spec"].(map[string]any)["text"] = variant.text
		changed, err := json.Marshal(object)
		if err != nil {
			t.Fatal(err)
		}
		next := m
		next.UID, next.ResourceVersion = variant.uid, variant.rv
		bindCoordStoredFixture(t, &next, changed)
		if _, err := a.Put(next, bytes.NewReader(changed)); err != nil {
			t.Fatal(err)
		}
		p := coordRevisionPath(t, a, next)
		if paths[p] {
			t.Fatal("different revision or UID shared a directory")
		}
		paths[p] = true
		if _, err := a.Get(next); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCoordArchiveConcurrentIdenticalPutsPublishOneRevision(t *testing.T) {
	a := coordArchiveRig(t)
	m, raw := coordStoredFixture(t, "taskclaims")
	var results [2]protocol.CoordArchiveStoredManifest
	var errs [2]error
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = a.Put(m, bytes.NewReader(raw))
		}(i)
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || results[0] != results[1] {
		t.Fatalf("concurrent publication differed: %v", errs)
	}
	entries, err := os.ReadDir(filepath.Dir(coordRevisionPath(t, a, m)))
	if err != nil || len(entries) != 1 {
		t.Fatalf("want one immutable revision, got %d: %v", len(entries), err)
	}
}

func TestCoordArchiveInvalidInputWritesNothing(t *testing.T) {
	a := coordArchiveRig(t)
	m, raw := coordStoredFixture(t, "inboxmessages")
	before := coordTree(t, a.Root)
	for _, bad := range []protocol.CoordArchiveManifest{
		{Resource: "../../escape"},
		func() protocol.CoordArchiveManifest { bad := m; bad.UID = "../escape"; return bad }(),
	} {
		_, err := a.Put(bad, bytes.NewReader(raw))
		requireCoordError(t, err, protocol.HistoryErrInvalid)
		got, err := a.Get(bad)
		requireCoordError(t, err, protocol.HistoryErrInvalid)
		if got.Object != nil || !reflect.DeepEqual(before, coordTree(t, a.Root)) {
			t.Fatal("invalid identity wrote files or returned bytes")
		}
	}
	wrongSize := m
	wrongSize.Object.Bytes++
	_, err := a.Put(wrongSize, bytes.NewReader(raw))
	requireCoordError(t, err, protocol.HistoryErrInvalid)
	_, err = a.Put(m, bytes.NewReader([]byte("not-json")))
	requireCoordError(t, err, protocol.HistoryErrInvalid)
	if !reflect.DeepEqual(before, coordTree(t, a.Root)) {
		t.Fatal("invalid object wrote files")
	}
}

func TestCoordArchiveCorruptionRefusesReadAndNeverOverwrites(t *testing.T) {
	for _, damage := range []string{"object", "whitespace", "manifest", "missing", "symlink", "hardlink", "prefix-collision"} {
		t.Run(damage, func(t *testing.T) {
			a := coordArchiveRig(t)
			m, raw := coordStoredFixture(t, "inboxreadacks")
			stored, err := a.Put(m, bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			dir := coordRevisionPath(t, a, m)
			object := filepath.Join(dir, "object.json")
			manifest := filepath.Join(dir, "manifest.json")
			switch damage {
			case "object":
				err = os.WriteFile(object, []byte("{}"), 0o600)
			case "whitespace":
				err = os.WriteFile(object, raw, 0o600)
			case "manifest":
				stored.RecipientUID = "55555555-5555-5555-5555-555555555555"
				b, marshalErr := protocol.CanonicalJSON(stored)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				err = os.WriteFile(manifest, b, 0o600)
			case "missing":
				err = os.Remove(manifest)
			case "symlink", "hardlink":
				outside := filepath.Join(t.TempDir(), "outside")
				b, readErr := os.ReadFile(object)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if err = os.WriteFile(outside, b, 0o600); err != nil {
					t.Fatal(err)
				}
				if err = os.Remove(object); err != nil {
					t.Fatal(err)
				}
				if damage == "symlink" {
					err = os.Symlink(outside, object)
				} else {
					err = os.Link(outside, object)
				}
			case "prefix-collision":
				// A different full digest sharing this revision's 16-character
				// directory prefix must never substitute an existing revision.
				stored.Object.SHA256 = m.Object.SHA256[:16] + strings.Repeat("0", 48)
				b, marshalErr := protocol.CanonicalJSON(stored)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				err = os.WriteFile(manifest, b, 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			before := coordTree(t, a.Root)
			got, err := a.Get(m)
			requireCoordError(t, err, protocol.HistoryErrCorrupt)
			if got.Object != nil || got.Manifest != (protocol.CoordArchiveStoredManifest{}) {
				t.Fatal("corruption returned evidence or object bytes")
			}
			_, err = a.Put(m, bytes.NewReader(raw))
			requireCoordError(t, err, protocol.HistoryErrConflict)
			if !reflect.DeepEqual(before, coordTree(t, a.Root)) {
				t.Fatal("a conflicting revision was changed")
			}
		})
	}
}

func TestCoordArchiveInterruptedStagingIsInvisibleAndPreserved(t *testing.T) {
	a := coordArchiveRig(t)
	m, raw := coordStoredFixture(t, "taskclaims")
	debris := filepath.Join(a.Root, ".incoming", "crashed-write")
	if err := os.MkdirAll(debris, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(debris, "object.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := a.Get(m)
	requireCoordError(t, err, protocol.HistoryErrNotFound)
	if got.Object != nil {
		t.Fatal("returned an incomplete staging object")
	}
	if _, err := a.Put(m, bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Get(m); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(debris, "object.json")); err != nil || !bytes.Equal(b, raw) {
		t.Fatalf("removed or changed pre-existing staging debris: %v", err)
	}
}

func TestCoordArchiveRequiresMountAndRefusesDirectoryLinks(t *testing.T) {
	for _, failure := range []string{"unmounted", "root-link", "v1-link", "incoming-link"} {
		t.Run(failure, func(t *testing.T) {
			a := coordArchiveRig(t)
			m, raw := coordStoredFixture(t, "publishedcontexts")
			outside := t.TempDir()
			switch failure {
			case "unmounted":
				coordArchiveIsMounted = func(string, string) error { return errors.New("not mounted") }
			case "root-link":
				if err := os.Remove(a.Root); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, a.Root); err != nil {
					t.Fatal(err)
				}
			case "v1-link":
				if err := os.Symlink(outside, filepath.Join(a.Root, "v1")); err != nil {
					t.Fatal(err)
				}
			case "incoming-link":
				if err := os.Symlink(outside, filepath.Join(a.Root, ".incoming")); err != nil {
					t.Fatal(err)
				}
			}
			before := coordTree(t, outside)
			_, err := a.Put(m, bytes.NewReader(raw))
			requireCoordError(t, err, protocol.HistoryErrNoArchive)
			if !reflect.DeepEqual(before, coordTree(t, outside)) {
				t.Fatal("wrote through a directory link")
			}
		})
	}
}
