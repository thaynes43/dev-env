package coordinatorwait

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	testingclock "k8s.io/utils/clock/testing"
)

var epoch = time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

func options() Options {
	return Options{SessionUID: "session-uid", ThreadID: "native-thread", WaitID: "tool-call", Directory: "/handoffs", Timer: time.Hour, ScanEvery: 10 * time.Second, MaxEntries: 256, MaxPending: 64, BudgetEnd: epoch.Add(4 * time.Hour)}
}
func record() *Record { return New(options(), epoch, "42", map[string]string{}) }
func requireKind(t *testing.T, r *Record, now time.Time, stop bool, kind string) *Result {
	t.Helper()
	v := r.Take(now, stop)
	if v == nil || v.Kind != kind {
		t.Fatalf("want %s, got %#v", kind, v)
	}
	return v
}
func ack(t *testing.T, r *Record, reconciled bool) {
	t.Helper()
	if err := r.Bind(r.Options, r.LastID, r.Stopped, false); err != nil {
		t.Fatal(err)
	}
}

func TestTimerAndMissedPeriodsCoalesce(t *testing.T) {
	r := record()
	if r.Take(epoch, false) != nil {
		t.Fatal("early timer")
	}
	v := requireKind(t, r, epoch.Add(3*time.Hour), false, "timer")
	if v.Time != epoch.Add(3*time.Hour) || r.NextTimer != epoch.Add(4*time.Hour) || v.Cursor != "42" {
		t.Fatal("bad timer/cursor")
	}
	ack(t, r, false)
	if r.Take(epoch.Add(3*time.Hour), false) != nil {
		t.Fatal("timer replayed missed periods")
	}
}
func TestNoteReplayAndPendingCoalescing(t *testing.T) {
	r := record()
	r.Note("note-one", "43", false)
	r.Note("note-one", "43", false)
	if len(r.Pending) != 1 {
		t.Fatal("duplicate pending note")
	}
	v := requireKind(t, r, epoch, false, "note")
	if v.Note != "note-one" || v.Cursor != "43" {
		t.Fatal("bad reference")
	}
	ack(t, r, false)
	r.Note("note-one", "44", false)
	if r.Take(epoch, false) != nil {
		t.Fatal("replayed note after acknowledgement")
	}
	if r.Cursor != "44" {
		t.Fatal("returned cursor not retained")
	}
}
func TestFileBurstCoalescesAndReplay(t *testing.T) {
	r := record()
	files := map[string]string{"one": "stamp", "two": "stamp"}
	r.Scan(files, false)
	v := requireKind(t, r, epoch, false, "file")
	if v.Path != "/handoffs" || v.Watermark == watermark(nil) || len(r.Pending) != 0 {
		t.Fatal("file burst not coalesced")
	}
	ack(t, r, false)
	r.Scan(files, false)
	if r.Take(epoch, false) != nil {
		t.Fatal("metadata replay")
	}
}
func TestOverflowToResync(t *testing.T) {
	for _, which := range []string{"pending", "changed-files", "entries", "receipts", "server"} {
		t.Run(which, func(t *testing.T) {
			r := record()
			r.Options.MaxPending = 1
			switch which {
			case "pending":
				r.Note("one", "43", false)
				r.Note("two", "44", false)
			case "changed-files":
				r.Scan(map[string]string{"one": "s", "two": "s"}, false)
			case "entries":
				r.Scan(map[string]string{"one": "s"}, true)
			case "receipts":
				r.Note("one", "43", false)
				requireKind(t, r, epoch, false, "note")
				ack(t, r, false)
				r.Note("two", "44", false)
			case "server":
				r.Note("", "90", true)
			}
			requireKind(t, r, epoch, false, "resync")
			if len(r.Pending) != 0 || len(r.Seen) > 1 {
				t.Fatal("unbounded pending/seen")
			}
			if r.Bind(r.Options, "wrong", false, false) == nil {
				t.Fatal("resync accepted wrong acknowledgement")
			}
			ack(t, r, true)
		})
	}
}
func TestLostAcknowledgementReplaysWholeResult(t *testing.T) {
	r := record()
	r.Note("one", "43", false)
	v := requireKind(t, r, epoch, false, "note")
	if err := r.Bind(r.Options, "", false, false); err != nil {
		t.Fatal(err)
	}
	if replay := r.Take(epoch.Add(time.Minute), true); !reflect.DeepEqual(v, replay) {
		t.Fatal("lost acknowledgement did not replay unchanged")
	}
	before, _ := json.Marshal(r)
	for _, id := range []string{"wrong"} {
		if r.Bind(r.Options, id, false, false) == nil {
			t.Fatal("lost ACK allowed replay")
		}
	}
	after, _ := json.Marshal(r)
	if string(before) != string(after) || r.LastID != v.ID {
		t.Fatal("failed binding mutated receipt")
	}
	ack(t, r, false)
}
func TestHeldQuestionPausesOnlyItsLane(t *testing.T) {
	// The owner question is a consumer's lane hold, never a wait-wide stop. An
	// unanswered question remains a note; unrelated file/timer events still run.
	r := record()
	r.Note("held-question", "43", false)
	requireKind(t, r, epoch, false, "note")
	ack(t, r, false)
	r.Scan(map[string]string{"unrelated-result": "stamp"}, false)
	requireKind(t, r, epoch, false, "file")
	ack(t, r, false)
	requireKind(t, r, epoch.Add(time.Hour), false, "timer")
	if r.Stopped || r.Ended {
		t.Fatal("question stopped unrelated work")
	}
}
func TestBudgetEndWinsAndCannotRearm(t *testing.T) {
	r := record()
	r.Note("one", "43", false)
	requireKind(t, r, r.Options.BudgetEnd, false, "budget_end")
	if !r.Ended || r.Bind(r.Options, r.LastID, false, false) == nil {
		t.Fatal("ended budget rearmed")
	}
}
func TestStopAndExplicitSameThreadResume(t *testing.T) {
	r := record()
	r.Note("one", "43", false)
	r.Scan(map[string]string{"file": "s"}, false)
	v := requireKind(t, r, epoch, true, "stopped")
	if !r.Stopped || v.Cursor != "43" || v.Watermark != r.Watermark || len(r.Pending) != 2 {
		t.Fatal("stop discarded cursors/pending")
	}
	if r.Bind(r.Options, r.LastID, false, false) == nil {
		t.Fatal("implicit resume")
	}
	for _, field := range []string{"thread", "session", "directory", "budget", "timer"} {
		o := r.Options
		switch field {
		case "thread":
			o.ThreadID = "other"
		case "session":
			o.SessionUID = "other"
		case "directory":
			o.Directory = "/other"
		case "budget":
			o.BudgetEnd = o.BudgetEnd.Add(time.Hour)
		case "timer":
			o.Timer = time.Minute
		}
		if r.Bind(o, r.LastID, true, false) == nil {
			t.Fatalf("rebound %s", field)
		}
	}
	ack(t, r, false)
	requireKind(t, r, epoch, false, "note")
}
func TestOptionsRejectTraversalAndMissingLimits(t *testing.T) {
	for _, path := range []string{"relative", "/handoffs/../elsewhere", "/handoffs/./child"} {
		o := options()
		o.Directory = path
		if o.Validate() == nil {
			t.Fatal("unsafe path accepted")
		}
	}
	o := options()
	o.ThreadID = ""
	if o.Validate() == nil {
		t.Fatal("implicit thread")
	}
	o = options()
	o.ScanEvery = time.Millisecond
	if o.Validate() == nil {
		t.Fatal("tight scans")
	}
	o = options()
	o.BudgetEnd = time.Time{}
	if o.Validate() == nil {
		t.Fatal("no budget")
	}
}

// A signalled fake clock makes every deciding ordering deterministic: no wall
// clock sleeps, real timers, stress or child processes.
type signalledClock struct {
	*testingclock.FakeClock
	timers chan time.Duration
}

func (c *signalledClock) NewTimer(d time.Duration) Timer {
	timer := c.FakeClock.NewTimer(d)
	c.timers <- d
	return timer
}
func fakeRuntime(t *testing.T) (Runtime, *signalledClock, chan string) {
	t.Helper()
	c := &signalledClock{FakeClock: testingclock.NewFakeClock(epoch), timers: make(chan time.Duration, 32)}
	calls := make(chan string, 32)
	rt := Runtime{Clock: c, Scan: func() (Snapshot, error) { return Snapshot{Files: map[string]string{}}, nil }, Stop: func() (bool, error) { return false, nil }, Save: func(*Record) error { return nil }, Notes: func(ctx context.Context, cursor string, d time.Duration) (Note, error) {
		calls <- cursor
		<-ctx.Done()
		return Note{}, ctx.Err()
	}}
	return rt, c, calls
}

type runAnswer struct {
	result *Result
	err    error
}

func runFake(ctx context.Context, rt Runtime, r *Record) <-chan runAnswer {
	done := make(chan runAnswer, 1)
	go func() { v, e := rt.Run(ctx, r); done <- runAnswer{v, e} }()
	return done
}
func TestRuntimeTimerCancelsOneHeldRead(t *testing.T) {
	rt, c, calls := fakeRuntime(t)
	r := record()
	r.Options.Timer = 10 * time.Second
	r.NextTimer = epoch.Add(10 * time.Second)
	done := runFake(context.Background(), rt, r)
	<-calls
	<-c.timers
	c.Step(10 * time.Second)
	out := <-done
	if out.err != nil || out.result.Kind != "timer" || len(calls) != 0 {
		t.Fatalf("timer: %#v", out)
	}
}
func TestRuntimeAddressedNoteAndUnrelatedIgnored(t *testing.T) {
	rt, c, _ := fakeRuntime(t)
	n := 0
	seen := make(chan string, 2)
	rt.Notes = func(_ context.Context, cursor string, _ time.Duration) (Note, error) {
		seen <- cursor
		n++
		if n == 1 {
			return Note{Cursor: "51"}, nil
		} // unrelated note never crosses addressed API
		return Note{Name: "addressed-child-note", Cursor: "52"}, nil
	}
	done := runFake(context.Background(), rt, record())
	if got := <-seen; got != "42" {
		t.Fatal(got)
	}
	// Consume both the initial timer and the waiting-response rearm timer.
	<-c.timers
	<-c.timers
	c.Step(10 * time.Second)
	if got := <-seen; got != "51" {
		t.Fatal("rearm did not use returned cursor")
	}
	out := <-done
	if out.err != nil || out.result.Kind != "note" || out.result.Note != "addressed-child-note" || n != 2 {
		t.Fatalf("addressed result %#v", out)
	}
}
func TestRuntimeNewFileCancelsHeldRead(t *testing.T) {
	rt, c, calls := fakeRuntime(t)
	scans := 0
	rt.Scan = func() (Snapshot, error) {
		scans++
		if scans == 1 {
			return Snapshot{Files: map[string]string{}}, nil
		}
		return Snapshot{Files: map[string]string{"new": "stamp"}}, nil
	}
	done := runFake(context.Background(), rt, record())
	<-calls
	<-c.timers
	c.Step(10 * time.Second)
	out := <-done
	if out.err != nil || out.result.Kind != "file" || scans != 2 {
		t.Fatalf("file %#v", out)
	}
}
func TestRuntimeBudgetAndCancellationPersistCursors(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(map[bool]string{false: "budget", true: "SIGTERM-context"}[stop], func(t *testing.T) {
			rt, c, calls := fakeRuntime(t)
			r := record()
			r.Options.BudgetEnd = epoch.Add(5 * time.Second)
			saved := ""
			rt.Save = func(r *Record) error { saved = r.LastID; return nil }
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := runFake(ctx, rt, r)
			<-calls
			<-c.timers
			if stop {
				cancel()
			} else {
				c.Step(5 * time.Second)
			}
			out := <-done
			kind := "budget_end"
			if stop {
				kind = "stopped"
			}
			if out.err != nil || out.result.Kind != kind || saved != out.result.ID || out.result.Cursor != "42" {
				t.Fatalf("stop %#v", out)
			}
		})
	}
}
func TestRuntimeRefusalDoesNotRetry(t *testing.T) {
	rt, _, _ := fakeRuntime(t)
	calls := 0
	rt.Notes = func(context.Context, string, time.Duration) (Note, error) {
		calls++
		return Note{}, errors.New("refused")
	}
	_, err := rt.Run(context.Background(), record())
	if err == nil || calls != 1 {
		t.Fatal("refusal retried")
	}
}
func TestRuntimeSaveFailurePrintsNoResult(t *testing.T) {
	rt, _, _ := fakeRuntime(t)
	rt.Save = func(r *Record) error {
		if r.LastID != "" {
			return errors.New("disk")
		}
		return nil
	}
	r := record()
	r.NextTimer = epoch
	v, err := rt.Run(context.Background(), r)
	if err == nil || v != nil {
		t.Fatal("result escaped before durable save")
	}
}

func TestDirectorySymlinkAndTraversalRefused(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "handoffs")
	must(t, os.Mkdir(dir, 0700))
	link := filepath.Join(parent, "link")
	must(t, os.Symlink(dir, link))
	for _, path := range []string{link, filepath.Join(parent, "link") + "/../handoffs", link + "/child"} {
		if d, err := OpenDirectory(path); err == nil {
			_ = d.Close()
			t.Fatal("symlink/traversal directory accepted")
		}
	}
	d, err := OpenDirectory(dir)
	must(t, err)
	defer func() { _ = d.Close() }()
	must(t, os.Symlink(filepath.Join(parent, "elsewhere"), filepath.Join(dir, "bad")))
	if snapshot, err := d.Scan(10); err != nil || len(snapshot.Refused) != 1 {
		t.Fatal("symlinked entry accepted")
	}
	for _, name := range []string{"..", "../outside", "/outside", "a/b", "a\\b", "bad\nname"} {
		if safeEntry(name) {
			t.Fatal("entry escapes directory")
		}
	}
}
func TestDirectoryMetadataBoundAndIdentity(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "handoffs")
	must(t, os.Mkdir(dir, 0700))
	d, err := OpenDirectory(dir)
	must(t, err)
	defer func() { _ = d.Close() }()
	must(t, os.WriteFile(filepath.Join(dir, "one"), []byte("PRIVATE_TEXT"), 0000)) // readable metadata, unreadable contents
	snapshot, err := d.Scan(1)
	must(t, err)
	if snapshot.Overflow || len(snapshot.Files) != 1 || strings.Contains(snapshot.Files["one"], "PRIVATE") {
		t.Fatal("metadata scan")
	}
	must(t, os.WriteFile(filepath.Join(dir, "two"), nil, 0600))
	snapshot, err = d.Scan(1)
	must(t, err)
	if !snapshot.Overflow || len(snapshot.Files) != 1 {
		t.Fatal("scan allocation unbounded")
	}
	must(t, os.Rename(dir, dir+"-old"))
	must(t, os.Mkdir(dir, 0700))
	if _, err = d.Scan(1); err == nil {
		t.Fatal("replacement directory adopted")
	}
}
func TestStoreAtomicPrivateLockAndStop(t *testing.T) {
	home := t.TempDir()
	s, err := OpenStore(home, "session-uid", "tool-call")
	must(t, err)
	defer s.Close()
	if other, err := OpenStore(home, "session-uid", "tool-call"); err == nil {
		other.Close()
		t.Fatal("second custodian")
	}
	if r, err := s.Load(); r != nil || err != nil {
		t.Fatal("missing record")
	}
	r := record()
	r.Note("pending", "43", false)
	requireKind(t, r, epoch, true, "stopped")
	must(t, s.Save(r))
	loaded, err := s.Load()
	must(t, err)
	if !reflect.DeepEqual(r, loaded) {
		t.Fatal("persisted cursors differ")
	}
	root := filepath.Join(home, ".dev-env-coordinator-waits")
	for _, name := range []string{s.name + ".json", s.name + ".lock"} {
		st, err := os.Stat(filepath.Join(root, name))
		must(t, err)
		if st.Mode().Perm() != 0600 {
			t.Fatal("nonprivate record")
		}
	}
	must(t, os.WriteFile(filepath.Join(root, s.name+".stop"), nil, 0600))
	stop, err := s.StopRequested()
	must(t, err)
	if !stop {
		t.Fatal("stop marker ignored")
	}
	must(t, os.Remove(filepath.Join(root, s.name+".stop")))
	must(t, os.Symlink("outside", filepath.Join(root, s.name+".stop")))
	if _, err = s.StopRequested(); err == nil {
		t.Fatal("symlinked stop marker")
	}
	// A stale temp link cannot be followed or damage the last committed record.
	must(t, os.Symlink("outside", filepath.Join(root, s.name+".tmp")))
	old := r.NextTimer
	r.NextTimer = r.NextTimer.Add(time.Second)
	if s.Save(r) == nil {
		t.Fatal("temp symlink followed")
	}
	r.NextTimer = old
	loaded, err = s.Load()
	must(t, err)
	if !reflect.DeepEqual(r, loaded) {
		t.Fatal("failed atomic save changed record")
	}
}
func TestStoreRefusesSymlinkAndPermissiveRecord(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".dev-env-coordinator-waits")
	must(t, os.Symlink(t.TempDir(), root))
	if s, err := OpenStore(home, "uid", "wait"); err == nil {
		s.Close()
		t.Fatal("symlinked store")
	}
	must(t, os.Remove(root))
	s, err := OpenStore(home, "uid", "wait")
	must(t, err)
	defer s.Close()
	must(t, s.Save(record()))
	path := filepath.Join(root, s.name+".json")
	must(t, os.Chmod(path, 0644))
	if _, err = s.Load(); err == nil {
		t.Fatal("permissive record")
	}
	must(t, os.Remove(path))
	must(t, os.Symlink("outside", path))
	if _, err = s.Load(); err == nil {
		t.Fatal("record symlink")
	}
}
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestStoreRejectsPendingOutsideDirectory(t *testing.T) {
	s, err := OpenStore(t.TempDir(), "session-uid", "tool-call")
	must(t, err)
	defer s.Close()
	r := record()
	r.Scan(map[string]string{"one": "stamp"}, false)
	r.Pending[0].Path = "/outside"
	if s.Save(r) == nil {
		t.Fatal("unsafe pending path saved")
	}
	data, err := json.Marshal(r)
	must(t, err)
	f, err := s.root.OpenFile(s.name+".json", os.O_CREATE|os.O_WRONLY, 0600)
	must(t, err)
	_, err = f.Write(data)
	must(t, err)
	must(t, f.Close())
	if _, err = s.Load(); err == nil {
		t.Fatal("saved outside path accepted as an event")
	}
}
func TestNoAckWithoutResult(t *testing.T) {
	r := record()
	if r.Bind(r.Options, "unknown-event", false, false) == nil {
		t.Fatal("phantom acknowledgement")
	}
}
func TestRuntimeStopMarkerNeverStartsRead(t *testing.T) {
	rt, _, calls := fakeRuntime(t)
	r := record()
	rt.Stop = func() (bool, error) { return true, nil }
	v, err := rt.Run(context.Background(), r)
	if err != nil || v.Kind != "stopped" || len(calls) != 0 {
		t.Fatal("stopped wait opened read")
	}
}
func TestRuntimePastBudgetNeverStartsRead(t *testing.T) {
	rt, _, calls := fakeRuntime(t)
	r := record()
	r.Options.BudgetEnd = epoch
	v, err := rt.Run(context.Background(), r)
	if err != nil || v.Kind != "budget_end" || len(calls) != 0 {
		t.Fatal("expired wait opened read")
	}
}
func TestRuntimeCancellationWinsLostAPIResponse(t *testing.T) {
	rt, _, _ := fakeRuntime(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rt.Notes = func(context.Context, string, time.Duration) (Note, error) {
		cancel()
		return Note{}, errors.New("lost response")
	}
	v, err := rt.Run(ctx, record())
	if err != nil || v.Kind != "stopped" {
		t.Fatal("cancel became an API error")
	}
}
func TestRuntimeCursorSavedBeforeOutput(t *testing.T) {
	rt, _, _ := fakeRuntime(t)
	r := record()
	saved := ""
	rt.Notes = func(context.Context, string, time.Duration) (Note, error) {
		return Note{Name: "child-note", Cursor: "100"}, nil
	}
	rt.Save = func(r *Record) error { saved = r.Cursor; return nil }
	v, err := rt.Run(context.Background(), r)
	if err != nil || v.Kind != "note" || saved != "100" || v.Cursor != "100" {
		t.Fatal("returned cursor not saved")
	}
}

func TestStoreSyncFailuresNeverConfirm(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		t.Run(map[int]string{1: "file", 2: "directory"}[failAt], func(t *testing.T) {
			s, err := OpenStore(t.TempDir(), "session-uid", "tool-call")
			must(t, err)
			defer s.Close()
			r := record()
			must(t, s.Save(r))
			before, err := s.Load()
			must(t, err)
			n := 0
			s.sync = func(*os.File) error {
				n++
				if n == failAt {
					return errors.New("sync failed")
				}
				return nil
			}
			r.Note("new-note", "43", false)
			requireKind(t, r, epoch, false, "note")
			if s.Save(r) == nil {
				t.Fatal("sync failure confirmed")
			}
			loaded, err := s.Load()
			must(t, err)
			if failAt == 1 && !reflect.DeepEqual(before, loaded) {
				t.Fatal("failed file sync changed committed record")
			}
			if failAt == 2 && loaded.LastID != r.LastID {
				t.Fatal("rename receipt lost after directory sync failure")
			}
		})
	}
}

func TestOptionsEachOperatingBound(t *testing.T) {
	for _, field := range []string{"timer-zero", "scan-zero", "timer-fraction", "scan-fraction", "entries-zero", "entries-large", "pending-zero", "pending-large", "budget"} {
		t.Run(field, func(t *testing.T) {
			o := options()
			switch field {
			case "timer-zero":
				o.Timer = 0
			case "scan-zero":
				o.ScanEvery = 0
			case "timer-fraction":
				o.Timer = time.Millisecond
			case "scan-fraction":
				o.ScanEvery = time.Millisecond
			case "entries-zero":
				o.MaxEntries = 0
			case "entries-large":
				o.MaxEntries = 4097
			case "pending-zero":
				o.MaxPending = 0
			case "pending-large":
				o.MaxPending = 1025
			case "budget":
				o.BudgetEnd = time.Time{}
			}
			if o.Validate() == nil {
				t.Fatal("operating bound removed")
			}
		})
	}
}

func TestDirectoryReplacementWithSameSizeAndTimeIsNew(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "handoff")
	replacement := filepath.Join(dir, "replacement")
	must(t, os.WriteFile(first, []byte("one"), 0600))
	must(t, os.Chtimes(first, epoch, epoch))
	d, err := OpenDirectory(dir)
	must(t, err)
	defer func() { _ = d.Close() }()
	before, err := d.Scan(2)
	must(t, err)
	// Allocate a distinct inode while the first is still live, then replace it
	// atomically with matching size/mode/mtime. No contents are read by the wait.
	must(t, os.WriteFile(replacement, []byte("two"), 0600))
	must(t, os.Chtimes(replacement, epoch, epoch))
	must(t, os.Rename(replacement, first))
	after, err := d.Scan(2)
	must(t, err)
	if before.Files["handoff"] == after.Files["handoff"] {
		t.Fatal("new file identity hidden by matching size and timestamp")
	}
}

func TestRuntimeImmediateWaitingReadPaced(t *testing.T) {
	rt, c, _ := fakeRuntime(t)
	seen := make(chan string, 2)
	rt.Notes = func(_ context.Context, cursor string, _ time.Duration) (Note, error) {
		seen <- cursor
		if cursor == "42" {
			return Note{Cursor: "43"}, nil
		}
		return Note{Name: "next-note", Cursor: "44"}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runFake(ctx, rt, record())
	if <-seen != "42" {
		t.Fatal("initial cursor")
	}
	<-c.timers // initial held read's timer
	if d := <-c.timers; d != 10*time.Second {
		t.Fatalf("next read scheduled after %s, want one scan interval", d)
	}
	// The next loop is now blocked on the fake timer. Advancing short of the
	// interval cannot release it, so a second read would violate the guard.
	c.Step(9 * time.Second)
	select {
	case cursor := <-seen:
		t.Fatalf("read before scan interval: %s", cursor)
	default:
	}
	c.Step(time.Second)
	if cursor := <-seen; cursor != "43" {
		t.Fatal("returned cursor not used")
	}
	out := <-done
	if out.err != nil || out.result.Kind != "note" {
		t.Fatalf("paced read: %#v", out)
	}
}
