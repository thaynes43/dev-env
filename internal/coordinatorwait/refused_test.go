package coordinatorwait

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRefusedEntryRoundTripAndOtherLanes(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		link         bool
	}{
		{"bad\xffname", "encoding", false}, {"bad\nname", "control", false}, {"bad\tname", "control", false},
		{"bad\u0085name", "control", false}, {"link", "symlink", true},
	} {
		t.Run(tc.reason+tc.name[:3], func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, tc.name)
			if tc.link {
				must(t, os.Symlink("missing", path))
			} else {
				must(t, os.WriteFile(path, nil, 0600))
			}
			d, err := OpenDirectory(dir)
			must(t, err)
			defer func() { _ = d.Close() }()
			snapshot, err := d.Scan(10)
			must(t, err)
			if len(snapshot.Files) != 0 || len(snapshot.Refused) != 1 {
				t.Fatalf("unsafe entry accepted: %#v", snapshot)
			}
			r := record()
			r.Options.Directory = dir
			r.Refuse(snapshot.Refused)
			v := requireKind(t, r, epoch, false, "resync")
			if v.Reason != tc.reason || v.Note != "" || v.Path != "" {
				t.Fatalf("unredacted refusal: %#v", v)
			}
			s, err := OpenStore(t.TempDir(), r.Options.SessionUID, r.Options.WaitID)
			must(t, err)
			defer s.Close()
			must(t, s.Save(r))
			r, err = s.Load()
			must(t, err)
			data, err := json.Marshal(r)
			must(t, err)
			rawName, err := json.Marshal(tc.name)
			must(t, err)
			if strings.Contains(string(data), string(rawName)) || strings.Contains(string(data), "missing") || strings.Contains(string(data), "\\ufffd") {
				t.Fatalf("raw refusal metadata persisted: %s", data)
			}
			ack(t, r, false)
			snapshot, err = d.Scan(10)
			must(t, err)
			r.Refuse(snapshot.Refused)
			if r.Take(epoch, false) != nil {
				t.Fatal("unchanged refusal repeated")
			}
			// A second call can deliver a child note despite the refused entry.
			rt, _, _ := fakeRuntime(t)
			rt.Scan = func() (Snapshot, error) { return d.Scan(10) }
			rt.Notes = func(context.Context, string, time.Duration) (Note, error) {
				return Note{Name: "child-note", Cursor: "44"}, nil
			}
			out, err := rt.Run(context.Background(), r)
			must(t, err)
			if out.Kind != "note" || out.Note != "child-note" {
				t.Fatalf("refusal blocked note: %#v", out)
			}
			ack(t, r, false)
			must(t, os.WriteFile(filepath.Join(dir, "safe"), nil, 0600))
			snapshot, err = d.Scan(10)
			must(t, err)
			r.Scan(snapshot.Files, snapshot.Overflow)
			r.Refuse(snapshot.Refused)
			requireKind(t, r, epoch, false, "file")
			ack(t, r, false)
			requireKind(t, r, epoch.Add(time.Hour), false, "timer")
			ack(t, r, false)
			// The hash includes identity: replacing the entry is a new refusal.
			must(t, os.Rename(path, path+"-old"))
			if tc.link {
				must(t, os.Symlink("other", path))
			} else {
				must(t, os.WriteFile(path, []byte("changed"), 0600))
			}
			snapshot, err = d.Scan(10)
			must(t, err)
			r.Refuse(snapshot.Refused)
			requireKind(t, r, epoch, false, "resync")
		})
	}
}
func TestEntryNameBound(t *testing.T) {
	if safeEntry(strings.Repeat("a", 256)) || safeEntry("bad\xffname") || safeEntry("bad\tname") {
		t.Fatal("unsafe name accepted")
	}
}
func TestDirectoryExistingChildBehindSymlinkRefused(t *testing.T) {
	parent := t.TempDir()
	real := filepath.Join(parent, "real")
	must(t, os.MkdirAll(filepath.Join(real, "child"), 0700))
	link := filepath.Join(parent, "link")
	must(t, os.Symlink(real, link))
	d, err := OpenDirectory(filepath.Join(link, "child"))
	if err == nil {
		defer func() { _ = d.Close() }()
		_, err = d.Scan(10)
	}
	if err == nil {
		t.Fatal("existing child behind symlinked parent accepted")
	}
}

func TestRefusedHashIncludesRawNameAndIdentity(t *testing.T) {
	dir := t.TempDir()
	name := "bad\xffname"
	path := filepath.Join(dir, name)
	must(t, os.WriteFile(path, []byte("one"), 0600))
	must(t, os.Chtimes(path, epoch, epoch))
	other := filepath.Join(dir, "bad\xfename")
	must(t, os.Link(path, other))
	d, err := OpenDirectory(dir)
	must(t, err)
	defer func() { _ = d.Close() }()
	before, err := d.Scan(10)
	must(t, err)
	if len(before.Refused) != 2 {
		t.Fatal("raw name bytes missing from refusal hash")
	}
	// Keep the original inode alive outside the watched directory, then replace
	// with identical ordinary metadata so only device/inode distinguishes it.
	must(t, os.Remove(other))
	must(t, os.Rename(path, filepath.Join(t.TempDir(), "parked")))
	must(t, os.WriteFile(path, []byte("two"), 0600))
	must(t, os.Chtimes(path, epoch, epoch))
	after, err := d.Scan(10)
	must(t, err)
	for hash := range after.Refused {
		if _, ok := before.Refused[hash]; ok {
			t.Fatal("replacement identity missing from refusal hash")
		}
	}
}
func TestRefusalQueuedBeforeOverflowIsNotSilentlyForgotten(t *testing.T) {
	r := record()
	hash := digest("raw-name-and-identity")
	refused := map[string]string{hash: "encoding"}
	r.Refuse(refused)
	r.Note("", "43", true)
	requireKind(t, r, epoch, false, "resync")
	ack(t, r, false)
	r.Refuse(refused)
	if v := requireKind(t, r, epoch, false, "resync"); v.Reason != "encoding" {
		t.Fatal("refusal lost before it was reported")
	}
}
func TestStoreRejectsInvalidRawNameBeforeWrite(t *testing.T) {
	s, err := OpenStore(t.TempDir(), "session-uid", "tool-call")
	must(t, err)
	defer s.Close()
	r := record()
	r.Files = map[string]string{"bad\xffname": "stamp"}
	r.Watermark = watermark(r.Files)
	if s.Save(r) == nil {
		t.Fatal("non-round-trippable record accepted for writing")
	}
	if loaded, err := s.Load(); err != nil || loaded != nil {
		t.Fatal("invalid record touched committed file")
	}
}
