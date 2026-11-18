package coordinatorwait

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestEntryCeilingAndEscapedRecordSave(t *testing.T) {
	o := options()
	o.MaxEntries = 1025
	if o.Validate() == nil {
		t.Fatal("entry ceiling not refused at start")
	}
	o.MaxEntries = 1024
	must(t, o.Validate())
	files := make(map[string]string, 1024)
	for i := 0; i < 1024; i++ {
		files[strings.Repeat("<", 251)+fmt.Sprintf("%04d", i)] = strings.Repeat("s", 128)
	}
	r := New(o, epoch, "42", files)
	s, err := OpenStore(t.TempDir(), o.SessionUID, o.WaitID)
	must(t, err)
	defer s.Close()
	must(t, s.Save(r))
	st, err := s.root.Stat(s.name + ".json")
	must(t, err)
	if st.Size() > 2<<20 {
		t.Fatal("escaped snapshot exceeds record bound")
	}
	loaded, err := s.Load()
	must(t, err)
	if len(loaded.Files) != 1024 {
		t.Fatal("ceiling snapshot lost entries")
	}
	t.Logf("escaped ceiling snapshot: %d bytes (record limit %d)", st.Size(), 2<<20)
	// The scan entry point enforces the same bound before touching a directory.
	d, err := OpenDirectory(t.TempDir())
	must(t, err)
	defer func() { _ = d.Close() }()
	if _, err := d.Scan(1025); err == nil {
		t.Fatal("scan ceiling missing")
	}
}

// Five ACKed calls, with a writer changing the directory every cycle. The
// parent's note source is deterministic by cursor, so ignored reads replay it.
func TestChangingDirectoryCannotStarveNotesOrTimer(t *testing.T) {
	for _, lane := range []string{"file", "refused", "overflow"} {
		t.Run(lane, func(t *testing.T) {
			rt, c, _ := fakeRuntime(t)
			r := record()
			r.NextTimer = epoch
			store, err := OpenStore(t.TempDir(), r.Options.SessionUID, r.Options.WaitID)
			must(t, err)
			defer store.Close()
			rt.Save = store.Save
			scans, reads := 0, 0
			rt.Scan = func() (Snapshot, error) {
				scans++
				snap := Snapshot{Files: map[string]string{}}
				switch lane {
				case "file":
					snap.Files["progress"] = fmt.Sprint(scans)
				case "refused":
					snap.Refused = map[string]string{digest(fmt.Sprint(scans)): "encoding"}
				case "overflow":
					snap.Overflow = true
				}
				return snap, nil
			}
			rt.Notes = func(_ context.Context, cursor string, d time.Duration) (Note, error) {
				reads++
				if r.directoryReady() && d != time.Second {
					t.Errorf("directory hold %s, want 1s", d)
				}
				return Note{Name: "child-" + cursor, Cursor: cursor + "1"}, nil
			}
			kinds := []string{}
			for i := 0; i < 5; i++ {
				out, err := rt.Run(context.Background(), r)
				must(t, err)
				kinds = append(kinds, out.Kind)
				beforeReads, beforeScans := reads, scans
				replay, err := rt.Run(context.Background(), r)
				must(t, err)
				if *replay != *out || reads != beforeReads || scans != beforeScans {
					t.Fatal("replay changed result or read a source")
				}
				loaded, err := store.Load()
				must(t, err)
				r = loaded
				if r.Outstanding == nil || *r.Outstanding != *out {
					t.Fatal("durable result changed")
				}
				ack(t, r, false)
				c.Step(5 * time.Second)
			}
			directoryKind := "file"
			if lane != "file" {
				directoryKind = "resync"
			}
			if kinds[0] != "timer" || kinds[1] != "note" || kinds[2] != directoryKind {
				t.Fatalf("three-lane order: %v", kinds)
			}
			if reads < 2 {
				t.Fatal("changing directory silenced held reads")
			}
		})
	}
}

func TestNotesEveryCallCannotStarveSingleDirectoryChange(t *testing.T) {
	rt, _, _ := fakeRuntime(t)
	r := record()
	rt.Scan = func() (Snapshot, error) { return Snapshot{Files: map[string]string{"file": "s"}}, nil }
	cursors := []string{}
	rt.Notes = func(_ context.Context, cursor string, d time.Duration) (Note, error) {
		cursors = append(cursors, cursor)
		if d != time.Second && cursor != "421" {
			t.Errorf("expected short hold: %s", d)
		}
		return Note{Name: "child-" + cursor, Cursor: cursor + "1"}, nil
	}
	first, err := rt.Run(context.Background(), r)
	must(t, err)
	if first.Kind != "note" || !r.DirectoryOwed {
		t.Fatal("first note did not leave directory owed")
	}
	ack(t, r, false)
	second, err := rt.Run(context.Background(), r)
	must(t, err)
	if second.Kind != "file" || r.DirectoryOwed || second.Cursor != first.Cursor {
		t.Fatal("second note beat owed directory or used its cursor")
	}
	ack(t, r, false)
	third, err := rt.Run(context.Background(), r)
	must(t, err)
	if third.Kind != "note" || third.Note != "child-"+first.Cursor || len(cursors) != 3 || cursors[1] != cursors[2] {
		t.Fatal("note ignored on directory call was lost")
	}
}

func TestDirectoryOwedSurvivesStopResumeAndMissingChangeResyncs(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprint(missing), func(t *testing.T) {
			rt, _, _ := fakeRuntime(t)
			r := record()
			rt.Scan = func() (Snapshot, error) { return Snapshot{Files: map[string]string{"file": "s"}}, nil }
			rt.Notes = func(_ context.Context, cursor string, _ time.Duration) (Note, error) {
				return Note{Name: "child-" + cursor, Cursor: cursor + "1"}, nil
			}
			first, err := rt.Run(context.Background(), r)
			must(t, err)
			if first.Kind != "note" {
				t.Fatal("note did not win first")
			}
			ack(t, r, false)
			stopped := requireKind(t, r, epoch, true, "stopped")
			s, err := OpenStore(t.TempDir(), r.Options.SessionUID, r.Options.WaitID)
			must(t, err)
			defer s.Close()
			must(t, s.Save(r))
			r, err = s.Load()
			must(t, err)
			if !r.DirectoryOwed {
				t.Fatal("owed state lost on save/load")
			}
			must(t, r.Bind(r.Options, stopped.ID, true, false))
			if missing {
				r.Pending = nil
			}
			out, err := rt.Run(context.Background(), r)
			must(t, err)
			if missing {
				if out.Kind != "resync" || out.Lane != "directory" {
					t.Fatal("orphan owed state did not resync directory")
				}
			} else if out.Kind != "file" {
				t.Fatal("owed change lost over stop/resume")
			}
		})
	}
}

func TestDirectoryShortReadBoundedAndLeavesCursor(t *testing.T) {
	rt, c, calls := fakeRuntime(t)
	r := record()
	rt.Scan = func() (Snapshot, error) { return Snapshot{Files: map[string]string{"file": "s"}}, nil }
	done := runFake(context.Background(), rt, r)
	<-calls
	if duration := <-c.timers; duration != time.Second {
		t.Fatalf("short read bound: %s", duration)
	}
	c.Step(time.Second)
	out := <-done
	if out.err != nil || out.result.Kind != "file" || out.result.Cursor != "42" {
		t.Fatal("short read did not end locally with original cursor")
	}
}

func TestResyncLaneSelectsReconciliation(t *testing.T) {
	for _, which := range []string{"refused", "entries", "changes", "notes", "mixed", "notes-then-file", "combined", "recovery", "legacy"} {
		t.Run(which, func(t *testing.T) {
			r := record()
			want := "directory"
			switch which {
			case "refused":
				r.Refuse(map[string]string{digest("bad"): "encoding"})
			case "entries":
				r.Scan(nil, true)
			case "changes":
				r.Options.MaxPending = 1
				r.Scan(map[string]string{"a": "s", "b": "s"}, false)
			case "notes":
				r.Note("", "43", true)
				want = "notes"
			case "mixed":
				r.Note("pending", "43", false)
				r.Scan(nil, true)
				want = "all"
			case "notes-then-file":
				r.Note("", "43", true)
				r.Scan(map[string]string{"file": "s"}, false)
				want = "all"
			case "combined":
				r.Scan(nil, true)
				r.Note("", "43", true)
				want = "all"
			case "recovery":
				r.Note("child", "43", false)
				r.Take(epoch, false)
				r.Outstanding = nil
				must(t, r.Bind(r.Options, r.LastID, false, true))
				want = "all"
			case "legacy":
				r.NeedsResync = true
				want = "all"
			}
			s, err := OpenStore(t.TempDir(), r.Options.SessionUID, r.Options.WaitID)
			must(t, err)
			defer s.Close()
			must(t, s.Save(r))
			r, err = s.Load()
			must(t, err)
			got := requireKind(t, r, epoch, false, "resync")
			if got.Lane != want {
				t.Fatalf("lane %q, want %q", got.Lane, want)
			}
			must(t, s.Save(r))
			loaded, err := s.Load()
			must(t, err)
			if replay := loaded.Take(epoch, false); *replay != *got {
				t.Fatal("resync lane changed on replay")
			}
		})
	}
}

func TestStoreRejectsInvalidLane(t *testing.T) {
	s, err := OpenStore(t.TempDir(), "session-uid", "tool-call")
	must(t, err)
	defer s.Close()
	for _, which := range []string{"checkpoint", "result", "nonresync"} {
		r := record()
		switch which {
		case "checkpoint":
			r.ResyncLane = "unknown"
		case "result":
			r.Note("", "43", true)
			r.Take(epoch, false)
			r.Outstanding.Lane = "unknown"
		case "nonresync":
			r.Note("child", "43", false)
			r.Take(epoch, false)
			r.Outstanding.Lane = "notes"
		}
		if s.Save(r) == nil {
			t.Fatalf("invalid lane saved: %s", which)
		}
	}
}

func TestNoteAndStopAcknowledgementsKeepPendingDirectoryResync(t *testing.T) {
	for _, stop := range []bool{false, true} {
		r := record()
		r.Scan(nil, true)
		if stop {
			requireKind(t, r, epoch, true, "stopped")
		} else {
			r.Note("child", "43", false)
			requireKind(t, r, epoch, false, "note")
			if !r.DirectoryOwed {
				t.Fatal("directory resync not owed")
			}
		}
		ack(t, r, false)
		got := requireKind(t, r, epoch, false, "resync")
		if got.Lane != "directory" {
			t.Fatal("ACK dropped pending directory reconciliation")
		}
	}
}

func TestDueTimerPrecedesPendingWorkWithoutSourceRead(t *testing.T) {
	rt, _, _ := fakeRuntime(t)
	r := record()
	r.Scan(map[string]string{"file": "s"}, false)
	r.Note("child", "43", false)
	r.DirectoryOwed = true
	r.NextTimer = epoch
	rt.Scan = func() (Snapshot, error) { t.Error("due timer scanned a source"); return Snapshot{}, nil }
	rt.Notes = func(context.Context, string, time.Duration) (Note, error) {
		t.Error("due timer read notes")
		return Note{Cursor: "44"}, nil
	}
	got, err := rt.Run(context.Background(), r)
	must(t, err)
	if got.Kind != "timer" || !r.DirectoryOwed || len(r.Pending) != 2 {
		t.Fatal("timer consumed pending work or alternation")
	}
}

func TestOwedDirectoryBeatsAlreadyQueuedNote(t *testing.T) {
	r := record()
	r.Scan(map[string]string{"file": "s"}, false)
	r.Note("first", "43", false)
	requireKind(t, r, epoch, false, "note")
	ack(t, r, false)
	r.Note("second", "44", false)
	requireKind(t, r, epoch, false, "file")
	ack(t, r, false)
	got := requireKind(t, r, epoch, false, "note")
	if got.Note != "second" {
		t.Fatal("already queued note lost")
	}
}
