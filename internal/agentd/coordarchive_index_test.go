package agentd

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func requireCoordIndexes(t *testing.T, a CoordArchive, stored protocol.CoordArchiveStoredManifest) []string {
	t.Helper()
	want, err := protocol.CanonicalJSON(coordIndexEntry(stored))
	if err != nil {
		t.Fatal(err)
	}
	paths := coordIndexPaths(coordIndexEntry(stored))
	for _, rel := range paths {
		p := filepath.Join(a.Root, filepath.FromSlash(rel))
		b, err := readArchiveFileLinks(a.Root, p, 4096, 2)
		if err != nil || !bytes.Equal(b, want) || bytes.Contains(b, []byte("synthetic context")) || bytes.Contains(b, []byte("futureField")) {
			t.Fatalf("index identity or content differs: %v", err)
		}
		fi, err := os.Stat(p)
		if err != nil || fi.Mode().Perm() != 0o600 || fi.Sys().(*syscall.Stat_t).Nlink != 2 {
			t.Fatalf("private index/staging links differ: %v", err)
		}
	}
	return paths
}

func nextCoordFixture(t *testing.T, m protocol.CoordArchiveManifest, raw []byte, rv string) (protocol.CoordArchiveManifest, []byte) {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	var metadata map[string]string
	if err := json.Unmarshal(object["metadata"], &metadata); err != nil {
		t.Fatal(err)
	}
	m.ResourceVersion = rv
	metadata["resourceVersion"] = rv
	b, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	object["metadata"] = b
	raw, err = json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	bindCoordStoredFixture(t, &m, raw)
	return m, raw
}

func TestCoordArchiveIndexesKeepFirstTimeAcrossRevisions(t *testing.T) {
	a := coordArchiveRig(t)
	for _, resource := range []string{"taskclaims", "publishedcontexts", "inboxmessages", "inboxreadacks"} {
		t.Run(resource, func(t *testing.T) {
			m, raw := coordStoredFixture(t, resource)
			stored, err := a.Put(m, bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			paths := requireCoordIndexes(t, a, stored)
			want := 1
			if resource == "inboxmessages" {
				want = 2
			}
			if len(paths) != want {
				t.Fatalf("want %d index entries, got %d", want, len(paths))
			}
			if _, err := os.Stat(filepath.Join(a.Root, "v1", resource, m.Project, "index", "recipient")); resource != "inboxmessages" && !errors.Is(err, os.ErrNotExist) {
				t.Fatal("published a recipient index for a non-message")
			}
			indexDir := filepath.Join(a.Root, "v1", resource, m.Project, "index")
			before := coordTree(t, indexDir)
			next, changed := nextCoordFixture(t, m, raw, "43")
			later := a
			later.Now = func() time.Time { return stored.ArchivedAt.AddDate(0, 1, 0) }
			if _, err := later.Put(next, bytes.NewReader(changed)); err != nil {
				t.Fatal(err)
			}
			next, changed = nextCoordFixture(t, m, raw, "44")
			later.Now = func() time.Time { return stored.ArchivedAt.AddDate(0, -1, 0) }
			last, err := later.Put(next, bytes.NewReader(changed))
			if err != nil || !last.ArchivedAt.Equal(stored.ArchivedAt) {
				t.Fatalf("clock rollback changed first time: %v", err)
			}
			if !reflect.DeepEqual(before, coordTree(t, indexDir)) {
				t.Fatal("later revision rewrote or duplicated the UID index")
			}
		})
	}
}

func TestCoordArchiveIndexesRepairPublicationFailures(t *testing.T) {
	for _, boundary := range []string{"revision", "all", "recipient"} {
		t.Run(boundary, func(t *testing.T) {
			a := coordArchiveRig(t)
			m, raw := coordStoredFixture(t, "inboxmessages")
			e := coordIndexEntry(protocol.CoordArchiveStoredManifest{CoordArchiveManifest: m, ArchivedAt: a.Now().UTC()})
			paths := coordIndexPaths(e)
			failDir := filepath.Dir(coordRevisionPath(t, a, m))
			if boundary == "all" {
				failDir = filepath.Dir(filepath.Join(a.Root, paths[0]))
			}
			if boundary == "recipient" {
				failDir = filepath.Dir(filepath.Join(a.Root, paths[1]))
			}
			failed := false
			a.syncDir = func(d string) error {
				if d == failDir && !failed {
					failed = true
					return errors.New("injected post-publication fsync failure")
				}
				return syncArchiveDir(d)
			}
			_, err := a.Put(m, bytes.NewReader(raw))
			requireCoordError(t, err, protocol.HistoryErrNoArchive)
			if !failed {
				t.Fatal("did not reach the real publication boundary")
			}
			visible, err := a.Get(m)
			if err != nil {
				t.Fatal(err)
			}
			before := coordTree(t, coordRevisionPath(t, a, m))
			for i, rel := range paths {
				_, err := os.Stat(filepath.Join(a.Root, rel))
				wantPresent := boundary == "recipient" || boundary == "all" && i == 0
				if wantPresent && err != nil || !wantPresent && !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("wrong partial index state at %s: %v", boundary, err)
				}
			}
			var synced []string
			a.syncDir = func(d string) error { synced = append(synced, d); return syncArchiveDir(d) }
			stored, err := a.Put(m, bytes.NewReader(raw))
			if err != nil || stored != visible.Manifest || !reflect.DeepEqual(before, coordTree(t, coordRevisionPath(t, a, m))) {
				t.Fatalf("repair changed immutable revision: %v", err)
			}
			requireCoordIndexes(t, a, stored)
			for _, rel := range paths {
				want := filepath.Dir(filepath.Join(a.Root, rel))
				found := false
				for _, got := range synced {
					if got == want {
						found = true
					}
				}
				if !found {
					t.Fatal("repair skipped an index publication directory")
				}
			}
			all := coordTree(t, a.Root)
			if _, err := a.Put(m, bytes.NewReader(raw)); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(all, coordTree(t, a.Root)) {
				t.Fatal("completed retry created more staging or indices")
			}
		})
	}
}

func TestCoordArchiveIndexesRefuseDamage(t *testing.T) {
	for _, damage := range []string{"json", "uid", "symlink", "extra-link", "directory-link"} {
		t.Run(damage, func(t *testing.T) {
			a := coordArchiveRig(t)
			m, raw := coordStoredFixture(t, "inboxmessages")
			stored, err := a.Put(m, bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			p := filepath.Join(a.Root, coordIndexPaths(coordIndexEntry(stored))[0])
			outside := t.TempDir()
			switch damage {
			case "json":
				err = os.WriteFile(p, []byte("{}"), 0o600)
			case "uid":
				b, readErr := os.ReadFile(p)
				if readErr != nil {
					t.Fatal(readErr)
				}
				err = os.WriteFile(p, bytes.ReplaceAll(b, []byte(m.UID), []byte("55555555-5555-5555-5555-555555555555")), 0o600)
			case "symlink":
				if err = os.Remove(p); err != nil {
					t.Fatal(err)
				}
				err = os.Symlink(filepath.Join(outside, "unreadable"), p)
			case "extra-link":
				err = os.Link(p, filepath.Join(outside, "third-link"))
			case "directory-link":
				parent := filepath.Dir(p)
				if err = os.Rename(parent, filepath.Join(outside, "moved")); err != nil {
					t.Fatal(err)
				}
				err = os.Symlink(filepath.Join(outside, "moved"), parent)
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = a.Put(m, bytes.NewReader(raw))
			requireCoordError(t, err, protocol.HistoryErrConflict)
			if damage == "json" || damage == "uid" {
				b, err := os.ReadFile(p)
				if err != nil || bytes.Equal(b, func() []byte { b, _ := protocol.CanonicalJSON(coordIndexEntry(stored)); return b }()) {
					t.Fatal("replaced a damaged index")
				}
			}
		})
	}
}

func TestCoordArchiveIndexesRepairRemovalAndRefuseZeroClock(t *testing.T) {
	a := coordArchiveRig(t)
	m, raw := coordStoredFixture(t, "inboxmessages")
	stored, err := a.Put(m, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range coordIndexPaths(coordIndexEntry(stored)) {
		if err := os.Remove(filepath.Join(a.Root, rel)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.Put(m, bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	requireCoordIndexes(t, a, stored)
	next, changed := nextCoordFixture(t, m, raw, "99")
	a.Now = func() time.Time { return time.Time{} }
	before := coordTree(t, a.Root)
	_, err = a.Put(next, bytes.NewReader(changed))
	requireCoordError(t, err, protocol.HistoryErrInvalid)
	if !reflect.DeepEqual(before, coordTree(t, a.Root)) {
		t.Fatal("zero clock created an entry")
	}
}

func TestCoordArchiveIndexesRefuseRecipientSubstitution(t *testing.T) {
	a := coordArchiveRig(t)
	m, raw := coordStoredFixture(t, "inboxmessages")
	if _, err := a.Put(m, bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	next, changed := nextCoordFixture(t, m, raw, "99")
	next.RecipientUID = "55555555-5555-5555-5555-555555555555"
	changed = bytes.ReplaceAll(changed, []byte(m.RecipientUID), []byte(next.RecipientUID))
	bindCoordStoredFixture(t, &next, changed)
	before := coordTree(t, a.Root)
	_, err := a.Put(next, bytes.NewReader(changed))
	requireCoordError(t, err, protocol.HistoryErrConflict)
	if !reflect.DeepEqual(before, coordTree(t, a.Root)) {
		t.Fatal("substitution wrote a new revision or recipient index")
	}
}

func TestCoordArchiveIndexesSeparateReclaimedUID(t *testing.T) {
	a := coordArchiveRig(t)
	m, raw := coordStoredFixture(t, "publishedcontexts")
	first, err := a.Put(m, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	oldUID := m.UID
	m.UID = "55555555-5555-5555-5555-555555555555"
	raw = bytes.ReplaceAll(raw, []byte(oldUID), []byte(m.UID))
	bindCoordStoredFixture(t, &m, raw)
	a.Now = func() time.Time { return first.ArchivedAt.Add(time.Hour) }
	next, err := a.Put(m, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	oldPaths, newPaths := requireCoordIndexes(t, a, first), requireCoordIndexes(t, a, next)
	if oldPaths[0] == newPaths[0] {
		t.Fatal("a reclaimed name overwrote the original UID index")
	}
}

func TestCoordArchiveIndexesFindFirstTimePastDirectoryBatch(t *testing.T) {
	a := coordArchiveRig(t)
	m, raw := coordStoredFixture(t, "taskclaims")
	// Seed one UID with 129 small historical revisions and no indices. The
	// oldest timestamp lies past the first 128-entry directory batch. This
	// is one finite boundary fixture, not repeated puts or a load test.
	for i := 0; i < 129; i++ {
		old, object := nextCoordFixture(t, m, raw, strconv.Itoa(i+1))
		stored := protocol.CoordArchiveStoredManifest{CoordArchiveManifest: old, ArchivedAt: a.Now().UTC()}
		dir := coordRevisionPath(t, a, old)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		canonical, err := protocol.VerifyCoordArchiveObject(old, bytes.NewReader(object))
		if err != nil {
			t.Fatal(err)
		}
		manifest, err := protocol.CanonicalJSON(stored)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "object.json"), canonical, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "manifest.json"), manifest, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Directory order is unspecified. Choose the entry actually beyond the
	// first batch, without changing any directory entries afterward.
	uidDir := filepath.Dir(coordRevisionPath(t, a, m))
	d, err := openBeneath(a.Root, uidDir, historyDirFlags)
	if err != nil {
		t.Fatal(err)
	}
	names, err := d.Readdirnames(129)
	_ = d.Close()
	if err != nil || len(names) != 129 {
		t.Fatalf("directory boundary fixture: %d, %v", len(names), err)
	}
	p := filepath.Join(uidDir, names[128], "manifest.json")
	b, err := readArchiveFile(a.Root, p, 4096)
	if err != nil {
		t.Fatal(err)
	}
	var first protocol.CoordArchiveStoredManifest
	if err := json.Unmarshal(b, &first); err != nil {
		t.Fatal(err)
	}
	first.ArchivedAt = first.ArchivedAt.Add(-time.Hour)
	b, err = protocol.CanonicalJSON(first)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	next, changed := nextCoordFixture(t, m, raw, "500")
	if _, err := a.Put(next, bytes.NewReader(changed)); err != nil {
		t.Fatal(err)
	}
	requireCoordIndexes(t, a, first)
}
