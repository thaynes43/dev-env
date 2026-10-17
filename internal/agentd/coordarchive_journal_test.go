package agentd

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func coordEventFixture(resource, state string, at time.Time) protocol.CoordArchiveEvent {
	e := protocol.CoordArchiveEvent{Format: protocol.CoordArchiveEventFormat, State: state, At: at, By: "dev-env-operator fixture", Sessions: []protocol.CoordArchiveObservation{}}
	roles := []string{}
	switch resource {
	case "taskclaims":
		e.Rule = "owner-absent"
		roles = []string{"owner"}
	case "publishedcontexts":
		e.Rule = "source-absent"
		roles = []string{"source"}
	case "inboxmessages":
		e.Rule = "sender-absent-recipient-done"
		roles = []string{"sender", "recipient"}
	case "inboxreadacks":
		e.Rule = "message-released"
		roles = []string{"message"}
	}
	if state == "reset" {
		return e
	}
	for n, role := range roles {
		observed := "not-found"
		if role == "message" {
			observed = "message-released"
		}
		if (state == "present" || state == "hold") && n == 0 {
			observed = "present"
		}
		e.Sessions = append(e.Sessions, protocol.CoordArchiveObservation{Role: role, Namespace: "dev-agents", Name: "synthetic-source", UID: "44444444-4444-4444-4444-444444444444", Observed: observed})
	}
	return e
}

func requireCoordGrace(t *testing.T, a CoordArchive, id protocol.CoordArchiveIdentity, want *time.Time) {
	t.Helper()
	got, err := a.Lookup([]protocol.CoordArchiveIdentity{id})
	if err != nil || len(got) != 1 || !reflect.DeepEqual(got[0].GraceStart, want) {
		t.Fatalf("grace: %v %+v", err, got)
	}
}

func journalEventPath(t *testing.T, a CoordArchive, id protocol.CoordArchiveIdentity, seq int, state string) string {
	t.Helper()
	rel, err := id.RecordDir()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(a.Root, filepath.FromSlash(rel), "events", fmt.Sprintf("%08d-%s.json", seq, state))
}

func writeCoordEvent(t *testing.T, p string, e protocol.CoordArchiveEvent) {
	t.Helper()
	raw, err := protocol.CanonicalJSON(e)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCoordArchiveJournalGraceTransitions(t *testing.T) {
	for _, resource := range []string{"taskclaims", "publishedcontexts", "inboxmessages", "inboxreadacks"} {
		t.Run(resource, func(t *testing.T) {
			a := coordArchiveRig(t)
			m, raw := coordStoredFixture(t, resource)
			id := coordLookupIdentity(m)
			at := time.Date(2026, 10, 14, 11, 0, 0, 0, time.UTC)
			e := coordEventFixture(resource, "absent", at)
			first, err := a.PutRecord(m, bytes.NewReader(raw), &e)
			if err != nil || first.GraceStart == nil || !first.GraceStart.Equal(at) {
				t.Fatalf("first mark: %v", err)
			}
			original, err := os.ReadFile(journalEventPath(t, a, id, 1, "absent"))
			if err != nil {
				t.Fatal(err)
			}
			e.At = at.Add(time.Hour)
			repeated, err := a.PutRecord(m, bytes.NewReader(raw), &e)
			if err != nil || !repeated.GraceStart.Equal(at) {
				t.Fatalf("repeated mark: %v", err)
			}
			plain, err := a.PutRecord(m, bytes.NewReader(raw), nil)
			if err != nil || !plain.GraceStart.Equal(at) {
				t.Fatalf("plain marked put: %v", err)
			}
			requireCoordGrace(t, a, id, &at)
			present := coordEventFixture(resource, "present", at.Add(2*time.Hour))
			result, err := a.Mark([]protocol.CoordArchiveMarkRequest{{CoordArchiveIdentity: id, Event: present}})
			if err != nil || !result[0].Appended || result[0].GraceStart != nil {
				t.Fatalf("present: %v", err)
			}
			requireCoordGrace(t, a, id, nil)
			result, err = a.Mark([]protocol.CoordArchiveMarkRequest{{CoordArchiveIdentity: id, Event: present}})
			if err != nil || result[0].Appended {
				t.Fatalf("repeat clear: %v", err)
			}
			e.At = at.Add(3 * time.Hour)
			if _, err := a.PutRecord(m, bytes.NewReader(raw), &e); err != nil {
				t.Fatal(err)
			}
			requireCoordGrace(t, a, id, &e.At)
			reset := coordEventFixture(resource, "reset", at.Add(4*time.Hour))
			if _, err := a.Mark([]protocol.CoordArchiveMarkRequest{{CoordArchiveIdentity: id, Event: reset}}); err != nil {
				t.Fatal(err)
			}
			requireCoordGrace(t, a, id, nil)
			e.At = at.Add(5 * time.Hour)
			if _, err := a.PutRecord(m, bytes.NewReader(raw), &e); err != nil {
				t.Fatal(err)
			}
			requireCoordGrace(t, a, id, &e.At)
			now, err := os.ReadFile(journalEventPath(t, a, id, 1, "absent"))
			if err != nil || !bytes.Equal(now, original) {
				t.Fatal("old event rewritten")
			}
			// Reader folds first absent after last reset even if a preserved/copied
			// journal contains another absent; the current writer skips that append.
			duplicate := e
			duplicate.At = at.Add(6 * time.Hour)
			writeCoordEvent(t, journalEventPath(t, a, id, 6, "absent"), duplicate)
			requireCoordGrace(t, a, id, &e.At)
		})
	}
}

func TestCoordArchiveHoldKeepsGraceAndReadsBack(t *testing.T) {
	a := coordArchiveRig(t)
	m, raw := coordStoredFixture(t, "taskclaims")
	id := coordLookupIdentity(m)
	at := time.Date(2026, 10, 14, 11, 0, 0, 0, time.UTC)
	absent := coordEventFixture("taskclaims", "absent", at)
	if _, err := a.PutRecord(m, bytes.NewReader(raw), &absent); err != nil {
		t.Fatal(err)
	}
	hold := coordEventFixture("taskclaims", "hold", at.Add(time.Hour))
	result, err := a.Mark([]protocol.CoordArchiveMarkRequest{{CoordArchiveIdentity: id, Event: hold}})
	if err != nil || len(result) != 1 || !result[0].Appended || result[0].GraceStart == nil || !result[0].GraceStart.Equal(at) {
		t.Fatalf("hold: %v %+v", err, result)
	}
	got, err := a.Lookup([]protocol.CoordArchiveIdentity{id})
	if err != nil || len(got) != 1 || got[0].LatestState != "hold" || got[0].GraceStart == nil || !got[0].GraceStart.Equal(at) {
		t.Fatalf("lookup: %v %+v", err, got)
	}
}

func TestCoordArchiveJournalRefusesUnknownAndCorruptState(t *testing.T) {
	for _, kind := range []string{"gap", "duplicate", "malformed", "unknown-field", "unknown-state", "backward-time", "symlink", "extra-link", "oversize"} {
		t.Run(kind, func(t *testing.T) {
			a := coordArchiveRig(t)
			m, raw := coordStoredFixture(t, "taskclaims")
			id := coordLookupIdentity(m)
			at := time.Date(2026, 10, 14, 11, 0, 0, 0, time.UTC)
			e := coordEventFixture(m.Resource, "absent", at)
			if _, err := a.PutRecord(m, bytes.NewReader(raw), &e); err != nil {
				t.Fatal(err)
			}
			file := journalEventPath(t, a, id, 1, "absent")
			switch kind {
			case "gap":
				if err := os.Rename(file, journalEventPath(t, a, id, 2, "absent")); err != nil {
					t.Fatal(err)
				}
			case "duplicate":
				writeCoordEvent(t, journalEventPath(t, a, id, 1, "reset"), coordEventFixture(m.Resource, "reset", at))
			case "malformed":
				if err := os.WriteFile(file, []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "unknown-field":
				data, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				data = append([]byte(`{"unknown":true,`), data[1:]...)
				if err := os.WriteFile(file, data, 0o600); err != nil {
					t.Fatal(err)
				}
			case "unknown-state":
				if err := os.Rename(file, journalEventPath(t, a, id, 1, "unknown")); err != nil {
					t.Fatal(err)
				}
			case "backward-time":
				writeCoordEvent(t, journalEventPath(t, a, id, 2, "present"), coordEventFixture(m.Resource, "present", at.Add(-time.Hour)))
			case "symlink":
				target := filepath.Join(t.TempDir(), "event.json")
				if err := os.Rename(file, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, file); err != nil {
					t.Fatal(err)
				}
			case "extra-link":
				if err := os.Link(file, filepath.Join(t.TempDir(), "third-link")); err != nil {
					t.Fatal(err)
				}
			case "oversize":
				if err := os.WriteFile(file, bytes.Repeat([]byte(" "), protocol.MaxCoordArchiveEventBytes+1), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			before := coordTree(t, a.Root)
			missing := id
			missing.UID = "99999999-9999-9999-9999-999999999999"
			got, err := a.Lookup([]protocol.CoordArchiveIdentity{missing, id})
			requireCoordError(t, err, protocol.HistoryErrCorrupt)
			if got != nil {
				t.Fatal("partial corrupt lookup")
			}
			put, err := a.PutRecord(m, bytes.NewReader(raw), nil)
			requireCoordError(t, err, protocol.HistoryErrCorrupt)
			if put.Manifest != (protocol.CoordArchiveStoredManifest{}) {
				t.Fatal("corrupt journal returned put receipt")
			}
			_, err = a.Mark([]protocol.CoordArchiveMarkRequest{{CoordArchiveIdentity: id, Event: coordEventFixture(m.Resource, "reset", at.Add(time.Hour))}})
			requireCoordError(t, err, protocol.HistoryErrCorrupt)
			if !reflect.DeepEqual(before, coordTree(t, a.Root)) {
				t.Fatal("refused corrupt state changed archive")
			}
		})
	}
}

func TestCoordArchiveJournalRepairsFailedPublicationSync(t *testing.T) {
	a := coordArchiveRig(t)
	m, raw := coordStoredFixture(t, "taskclaims")
	id := coordLookupIdentity(m)
	at := time.Date(2026, 10, 14, 11, 0, 0, 0, time.UTC)
	e := coordEventFixture(m.Resource, "absent", at)
	fail := true
	syncs := 0
	a.syncDir = func(p string) error {
		if filepath.Base(p) == "events" {
			syncs++
			if fail {
				return errors.New("synthetic event parent sync failure")
			}
		}
		return syncArchiveDir(p)
	}
	failed, err := a.PutRecord(m, bytes.NewReader(raw), &e)
	requireCoordError(t, err, protocol.HistoryErrNoArchive)
	if failed.Manifest != (protocol.CoordArchiveStoredManifest{}) {
		t.Fatal("failed publish claimed receipt")
	}
	before := coordTree(t, a.Root)
	fail = false
	receipt, err := a.PutRecord(m, bytes.NewReader(raw), &e)
	if err != nil || receipt.GraceStart == nil || !receipt.GraceStart.Equal(at) {
		t.Fatalf("repair: %v", err)
	}
	if syncs < 2 || !reflect.DeepEqual(before, coordTree(t, a.Root)) {
		t.Fatal("retry failed to sync or rewrote event")
	}
	clear := protocol.CoordArchiveMarkRequest{CoordArchiveIdentity: id, Event: coordEventFixture(m.Resource, "present", at.Add(time.Hour))}
	fail = true
	result, err := a.Mark([]protocol.CoordArchiveMarkRequest{clear})
	requireCoordError(t, err, protocol.HistoryErrNoArchive)
	if result != nil {
		t.Fatal("failed clear claimed result")
	}
	before = coordTree(t, a.Root)
	count := syncs
	fail = false
	result, err = a.Mark([]protocol.CoordArchiveMarkRequest{clear})
	if err != nil || result[0].Appended || syncs <= count || !reflect.DeepEqual(before, coordTree(t, a.Root)) {
		t.Fatalf("clear retry repair: %v", err)
	}
	requireCoordGrace(t, a, id, nil)
}

func TestCoordArchiveJournalRejectsBadInputBeforeWrites(t *testing.T) {
	a := coordArchiveRig(t)
	m, raw := coordStoredFixture(t, "taskclaims")
	id := coordLookupIdentity(m)
	at := time.Date(2026, 10, 14, 11, 0, 0, 0, time.UTC)
	e := coordEventFixture(m.Resource, "absent", at)
	bad := e
	bad.Sessions[0].Observed = "unknown"
	if _, err := a.PutRecord(m, bytes.NewReader(raw), &bad); err == nil {
		t.Fatal("unknown observation accepted")
	}
	e = coordEventFixture(m.Resource, "absent", at)
	wrong := m
	wrong.Object.SHA256 = strings.Repeat("0", 64)
	if _, err := a.PutRecord(wrong, bytes.NewReader(raw), &e); err == nil {
		t.Fatal("unverified object marked")
	}
	if _, err := a.Mark([]protocol.CoordArchiveMarkRequest{{CoordArchiveIdentity: id, Event: e}}); err == nil {
		t.Fatal("mark appended absent")
	}
	entries, err := os.ReadDir(a.Root)
	if err != nil || len(entries) != 0 {
		t.Fatal("bad input touched archive")
	}
	if _, err := a.PutRecord(m, bytes.NewReader(raw), &e); err != nil {
		t.Fatal(err)
	}
	present := coordEventFixture(m.Resource, "present", at.Add(-time.Hour))
	before := coordTree(t, a.Root)
	if _, err := a.Mark([]protocol.CoordArchiveMarkRequest{{CoordArchiveIdentity: id, Event: present}}); err == nil {
		t.Fatal("backward transition accepted")
	}
	if !reflect.DeepEqual(before, coordTree(t, a.Root)) {
		t.Fatal("backward transition changed state")
	}
	items := make([]protocol.CoordArchiveMarkRequest, 1024)
	missing := id
	missing.UID = "99999999-9999-9999-9999-999999999999"
	for n := range items {
		items[n] = protocol.CoordArchiveMarkRequest{CoordArchiveIdentity: missing, Event: coordEventFixture(m.Resource, "reset", at)}
	}
	result, err := a.Mark(items)
	if err != nil || len(result) != 1024 {
		t.Fatalf("maximum mark batch: %v", err)
	}
	if _, err := a.Mark(append(items, items[0])); err == nil {
		t.Fatal("unbounded mark accepted")
	}
	if _, err := a.Mark(nil); err == nil {
		t.Fatal("empty mark accepted")
	}
}

func TestCoordArchiveJournalNeverMarksBeforeRevisionAndIndices(t *testing.T) {
	a := coordArchiveRig(t)
	m, raw := coordStoredFixture(t, "taskclaims")
	id := coordLookupIdentity(m)
	at := time.Date(2026, 10, 14, 11, 0, 0, 0, time.UTC)
	event := coordEventFixture(m.Resource, "absent", at)
	fail := true
	a.syncDir = func(p string) error {
		if fail && strings.Contains(p, string(filepath.Separator)+"index"+string(filepath.Separator)) {
			return errors.New("synthetic index parent sync failure")
		}
		return syncArchiveDir(p)
	}
	receipt, err := a.PutRecord(m, bytes.NewReader(raw), &event)
	requireCoordError(t, err, protocol.HistoryErrNoArchive)
	if receipt.Manifest != (protocol.CoordArchiveStoredManifest{}) {
		t.Fatal("failed prerequisite claimed receipt")
	}
	if _, err := os.Lstat(journalEventPath(t, a, id, 1, "absent")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("event published before required index durability")
	}
	requireCoordGrace(t, a, id, nil)
	fail = false
	receipt, err = a.PutRecord(m, bytes.NewReader(raw), &event)
	if err != nil || receipt.GraceStart == nil || !receipt.GraceStart.Equal(at) {
		t.Fatalf("prerequisite repair: %v", err)
	}
}
