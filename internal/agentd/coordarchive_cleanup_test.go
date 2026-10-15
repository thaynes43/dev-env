package agentd

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func coordIncomingFixture(t *testing.T, a CoordArchive, name string, age time.Duration, files ...string) string {
	t.Helper()
	p := filepath.Join(a.Root, protocol.HistoryArchiveIncoming, name)
	if err := os.MkdirAll(p, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if err := os.WriteFile(filepath.Join(p, file), []byte("synthetic"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	at := a.Now().Add(-age)
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCoordArchivePutCleansOnlyOldExpectedIncoming(t *testing.T) {
	a := coordArchiveRig(t)
	old := coordIncomingFixture(t, a, "0123456789abcdef0123456789abcdef", time.Hour+time.Nanosecond, "object.json", "manifest.json")
	fresh := coordIncomingFixture(t, a, "1123456789abcdef0123456789abcdef", time.Hour-time.Nanosecond, "object.json")
	boundary := coordIncomingFixture(t, a, "2123456789abcdef0123456789abcdef", time.Hour, "index.json")
	future := coordIncomingFixture(t, a, "3123456789abcdef0123456789abcdef", -time.Hour, "event.json")
	m, raw := coordStoredFixture(t, "taskclaims")
	if _, err := a.Put(m, bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(old); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("old expected debris remains: %v", err)
	}
	for _, p := range []string{fresh, boundary, future} {
		if _, err := os.Lstat(p); err != nil {
			t.Fatalf("retained staging entry missing: %v", err)
		}
	}
	got, err := a.Get(m)
	if err != nil || !bytes.Equal(got.Object, mustCoordCanonical(t, m, raw)) {
		t.Fatalf("cleanup changed published revision: %v", err)
	}
}

func TestCoordArchiveCleanupRefusesUnsafeOrUnknownOldEntries(t *testing.T) {
	for _, tc := range []struct {
		name string
		make func(t *testing.T, a CoordArchive) (string, string)
	}{
		{
			name: "linked entry",
			make: func(t *testing.T, a CoordArchive) (string, string) {
				outside := t.TempDir()
				entry := filepath.Join(a.Root, protocol.HistoryArchiveIncoming, "0123456789abcdef0123456789abcdef")
				if err := os.MkdirAll(filepath.Dir(entry), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, entry); err != nil {
					t.Fatal(err)
				}
				return entry, outside
			},
		},
		{
			name: "unknown filename",
			make: func(t *testing.T, a CoordArchive) (string, string) {
				return coordIncomingFixture(t, a, "0123456789abcdef0123456789abcdef", 2*time.Hour, "unexpected"), ""
			},
		},
		{
			name: "linked child",
			make: func(t *testing.T, a CoordArchive) (string, string) {
				entry := coordIncomingFixture(t, a, "0123456789abcdef0123456789abcdef", 2*time.Hour)
				outside := t.TempDir()
				if err := os.Symlink(outside, filepath.Join(entry, "object.json")); err != nil {
					t.Fatal(err)
				}
				return entry, outside
			},
		},
		{
			name: "nested child",
			make: func(t *testing.T, a CoordArchive) (string, string) {
				entry := coordIncomingFixture(t, a, "0123456789abcdef0123456789abcdef", 2*time.Hour)
				if err := os.Mkdir(filepath.Join(entry, "nested"), 0o700); err != nil {
					t.Fatal(err)
				}
				return entry, ""
			},
		},
		{
			name: "unknown entry name",
			make: func(t *testing.T, a CoordArchive) (string, string) {
				return coordIncomingFixture(t, a, "not-a-private-staging-name", 2*time.Hour, "object.json"), ""
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := coordArchiveRig(t)
			entry, outside := tc.make(t, a)
			// A symlink's own timestamp cannot be safely backdated with the
			// portable os package. Advance the injected clock instead.
			a.Now = func() time.Time { return time.Now().UTC().Add(2 * time.Hour) }
			marker := filepath.Join(outside, "marker")
			if outside != "" {
				if err := os.WriteFile(marker, []byte("outside"), 0o600); err != nil {
					t.Fatal(err)
				}
				at := a.Now().Add(-2 * time.Hour)
				if err := os.Chtimes(entry, at, at); err != nil {
					t.Fatal(err)
				}
			}
			m, raw := coordStoredFixture(t, "publishedcontexts")
			_, err := a.Put(m, bytes.NewReader(raw))
			requireCoordError(t, err, protocol.HistoryErrNoArchive)
			if _, err := os.Lstat(entry); err != nil {
				t.Fatalf("unsafe entry was removed: %v", err)
			}
			if outside != "" {
				if b, err := os.ReadFile(marker); err != nil || string(b) != "outside" {
					t.Fatalf("cleanup reached outside: %q, %v", b, err)
				}
			}
			if _, err := a.Get(m); err != nil {
				t.Fatalf("durable revision was hidden after truthful cleanup error: %v", err)
			}
		})
	}
}

func TestCoordArchiveFailedPutDoesNotCleanIncoming(t *testing.T) {
	for _, tc := range []struct {
		name string
		put  func(a CoordArchive, m protocol.CoordArchiveManifest, raw []byte) error
	}{
		{
			name: "invalid",
			put: func(a CoordArchive, m protocol.CoordArchiveManifest, raw []byte) error {
				m.Object.Bytes++
				_, err := a.Put(m, bytes.NewReader(raw))
				return err
			},
		},
		{
			name: "publication sync failure",
			put: func(a CoordArchive, m protocol.CoordArchiveManifest, raw []byte) error {
				a.syncDir = func(string) error { return errors.New("injected publication sync failure") }
				_, err := a.Put(m, bytes.NewReader(raw))
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := coordArchiveRig(t)
			debris := coordIncomingFixture(t, a, "0123456789abcdef0123456789abcdef", 2*time.Hour, "object.json")
			m, raw := coordStoredFixture(t, "inboxmessages")
			if err := tc.put(a, m, raw); err == nil {
				t.Fatal("failed put succeeded")
			}
			if _, err := os.Lstat(debris); err != nil {
				t.Fatalf("failed put discarded debris: %v", err)
			}
		})
	}
}

func TestCoordArchiveCleanupSyncFailureRefusesSuccess(t *testing.T) {
	a := coordArchiveRig(t)
	debris := coordIncomingFixture(t, a, "0123456789abcdef0123456789abcdef", 2*time.Hour, "object.json")
	a.syncDir = func(string) error { return errors.New("injected cleanup sync failure") }
	if err := a.cleanupIncoming(); err == nil {
		t.Fatal("cleanup sync failure reported success")
	}
	if _, err := os.Lstat(debris); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("successful removal was not attempted before failed durability sync: %v", err)
	}
}

func mustCoordCanonical(t *testing.T, m protocol.CoordArchiveManifest, raw []byte) []byte {
	t.Helper()
	got, err := protocol.VerifyCoordArchiveObject(m, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return got
}
