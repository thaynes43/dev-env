package coordinatorwait

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// The reviewer's max-entries=2 rename probe, including a stale queued report
// left by the previous implementation. Every stage is durably saved/reloaded.
func TestRefusalRenameProbeNeverBlocksNote(t *testing.T) {
	dir := t.TempDir()
	b1, b2, b3 := filepath.Join(dir, "b\xff1"), filepath.Join(dir, "b\xff2"), filepath.Join(dir, "b\xff3")
	must(t, os.WriteFile(b1, nil, 0600))
	must(t, os.WriteFile(b2, nil, 0600))
	d, err := OpenDirectory(dir)
	must(t, err)
	defer func() { _ = d.Close() }()
	s, err := OpenStore(t.TempDir(), "session-uid", "tool-call")
	must(t, err)
	defer s.Close()
	r := record()
	r.Options.Directory = dir
	r.Options.MaxEntries = 2
	initial, err := d.Scan(2)
	must(t, err)
	r.Refuse(initial.Refused)
	out := requireKind(t, r, epoch, false, "resync")
	if out.Count != 2 || len(r.Refused) != 2 {
		t.Fatal("scan refusals were not coalesced")
	}
	must(t, s.Save(r))
	r, err = s.Load()
	must(t, err)
	ack(t, r, false)
	must(t, os.Rename(b2, b3))
	renamed, err := d.Scan(2)
	must(t, err)
	r.Refuse(renamed.Refused)
	requireKind(t, r, epoch, false, "resync")
	must(t, s.Save(r))
	r, err = s.Load()
	must(t, err)
	ack(t, r, false)
	must(t, os.Rename(b3, b2))
	back, err := d.Scan(2)
	must(t, err)
	if !slices.Equal(sortedHashes(initial.Refused), sortedHashes(back.Refused)) {
		t.Fatal("rename did not restore identity hash")
	}
	// Reconstruct the review's exact old saved state: b1/b2 reported, b3 stale.
	r.Refused = sortedHashes(back.Refused)
	for hash, reason := range renamed.Refused {
		if _, ok := back.Refused[hash]; !ok {
			r.Pending = []Result{{Kind: "resync", ID: hash, Reason: reason}}
		}
	}
	must(t, s.Save(r))
	r, err = s.Load()
	must(t, err)
	rt, _, _ := fakeRuntime(t)
	rt.Scan = func() (Snapshot, error) { return d.Scan(2) }
	rt.Save = s.Save
	reads := 0
	rt.Notes = func(context.Context, string, time.Duration) (Note, error) {
		reads++
		return Note{Name: "child-note", Cursor: "44"}, nil
	}
	got, err := rt.Run(context.Background(), r)
	must(t, err)
	if got.Kind != "note" || reads != 1 || len(r.Refused) > 2 {
		t.Fatalf("stale refusal blocked note: %#v reads=%d", got, reads)
	}
	ack(t, r, false)
	requireKind(t, r, epoch.Add(time.Hour), false, "timer")
	must(t, s.Save(r))
}
func sortedHashes(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func TestRefusalBatchDropsGoneEntriesAndPreservesOtherEvents(t *testing.T) {
	r := record()
	first, gone, last := digest("first"), digest("gone"), digest("last")
	r.Refuse(map[string]string{first: "encoding", gone: "control"})
	r.Refuse(map[string]string{first: "encoding", last: "symlink"})
	got := requireKind(t, r, epoch, false, "resync")
	if got.Count != 2 || got.Path != "" || got.Note != "" || !refusalReason(got.Reason) || slices.Contains(r.Refused, gone) {
		t.Fatalf("bad batch: %#v %#v", got, r.Refused)
	}
	ack(t, r, false)
	r.Refuse(map[string]string{first: "encoding", last: "symlink"})
	r.Note("child-note", "44", false)
	requireKind(t, r, epoch, false, "note")
	ack(t, r, false)
	requireKind(t, r, epoch.Add(time.Hour), false, "timer")
}
func TestRefusalHistoryLimitResync(t *testing.T) {
	r := record()
	r.Options.MaxEntries = 2
	r.Refuse(map[string]string{digest("1"): "encoding", digest("2"): "encoding", digest("3"): "encoding"})
	got := requireKind(t, r, epoch, false, "resync")
	if got.Reason != "limit" || got.Count != 3 || len(r.Refused) != 2 {
		t.Fatalf("history limit: %#v", got)
	}
	s, err := OpenStore(t.TempDir(), "session-uid", "tool-call")
	must(t, err)
	defer s.Close()
	must(t, s.Save(r))
}
func TestRefusalsRetainedDuringEntryOverflow(t *testing.T) {
	r := record()
	a, b := digest("a"), digest("b")
	r.Refuse(map[string]string{a: "encoding", b: "control"})
	requireKind(t, r, epoch, false, "resync")
	ack(t, r, false)
	r.Scan(nil, true)
	r.Refuse(map[string]string{a: "encoding"})
	requireKind(t, r, epoch, false, "resync")
	ack(t, r, false)
	r.Scan(nil, true)
	r.Refuse(map[string]string{b: "control"})
	if r.Take(epoch, false) != nil || len(r.Refused) != 2 {
		t.Fatal("partial scan repeated or forgot a refusal")
	}
	r.Scan(nil, false)
	r.Refuse(map[string]string{a: "encoding", b: "control"})
	if r.Take(epoch, false) != nil {
		t.Fatal("overflow repeated reported refusal")
	}
}
func TestDirectoryPinAcrossCallsInPackage(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "handoffs")
	must(t, os.Mkdir(dir, 0700))
	r := record()
	r.Options.Directory = dir
	d, err := OpenDirectory(dir)
	must(t, err)
	must(t, r.BindDirectory(d))
	must(t, d.Close())
	s, err := OpenStore(t.TempDir(), "session-uid", "tool-call")
	must(t, err)
	defer s.Close()
	must(t, s.Save(r))
	r, err = s.Load()
	must(t, err)
	must(t, os.Rename(dir, dir+"-old"))
	must(t, os.Mkdir(dir, 0700))
	replacement, err := OpenDirectory(dir)
	must(t, err)
	defer func() { _ = replacement.Close() }()
	if r.BindDirectory(replacement) == nil {
		t.Fatal("replacement directory accepted")
	}
}
func TestMinimumScanIntervalFiveSeconds(t *testing.T) {
	for _, interval := range []time.Duration{time.Second, 4 * time.Second, 5 * time.Second, 6 * time.Second} {
		o := options()
		o.ScanEvery = interval
		if (o.Validate() == nil) != (interval >= 5*time.Second) {
			t.Fatalf("scan interval %s", interval)
		}
	}
}

func TestRefusalHistoryLimitAtTake(t *testing.T) {
	r := record()
	r.Options.MaxEntries = 2
	r.Refused = []string{digest("1"), digest("2")}
	r.PendingRefusals = []string{digest("stale")}
	r.Pending = []Result{{Kind: "resync", Reason: "encoding", Count: 1, ID: digest("batch")}}
	s, err := OpenStore(t.TempDir(), "session-uid", "tool-call")
	must(t, err)
	defer s.Close()
	must(t, s.Save(r))
	got := requireKind(t, r, epoch, false, "resync")
	if got.Reason != "limit" || len(r.Refused) != 2 {
		t.Fatalf("take exceeded history: %#v", got)
	}
	must(t, s.Save(r))
}
func TestStoreRejectsMalformedRefusalBatchMetadata(t *testing.T) {
	for _, kind := range []string{"hash", "hash-bound", "negative-count", "large-count", "note-count", "unreasoned-count", "persisted-diagnostic"} {
		t.Run(kind, func(t *testing.T) {
			r := record()
			r.Refuse(map[string]string{digest("one"): "encoding"})
			switch kind {
			case "hash":
				r.PendingRefusals = []string{"raw-name"}
			case "hash-bound":
				r.Options.MaxEntries = 1
				r.PendingRefusals = append(r.PendingRefusals, digest("two"))
			case "negative-count":
				r.Pending[0].Count = -1
			case "large-count":
				r.Pending[0].Count = 4097
			case "note-count":
				r.Pending[0] = Result{Kind: "note", ID: digest("note"), Note: "child-note", Count: 1}
			case "unreasoned-count":
				r.Note("", "43", true)
				requireKind(t, r, epoch, false, "resync")
				r.Outstanding.Count = 1
			case "persisted-diagnostic":
				r.Pending[0].DirectoryUnchecked = true
			}
			s, err := OpenStore(t.TempDir(), "session-uid", "tool-call")
			must(t, err)
			defer s.Close()
			if s.Save(r) == nil {
				t.Fatal("malformed batch metadata accepted")
			}
		})
	}
}

func TestRefusalBatchHonorsPendingBound(t *testing.T) {
	r := record()
	r.Options.MaxPending = 1
	r.Note("child-note", "43", false)
	r.Refuse(map[string]string{digest("unsafe"): "encoding"})
	if !r.NeedsResync || len(r.Pending) != 0 {
		t.Fatal("refusal batch exceeded pending limit")
	}
	requireKind(t, r, epoch, false, "resync")
	s, err := OpenStore(t.TempDir(), "session-uid", "tool-call")
	must(t, err)
	defer s.Close()
	must(t, s.Save(r))
}
func TestRefusalHistoryTracksCurrentCompleteScan(t *testing.T) {
	r := record()
	r.Options.MaxEntries = 2
	a, b, c := digest("a"), digest("b"), digest("c")
	r.Refuse(map[string]string{a: "encoding", b: "encoding"})
	requireKind(t, r, epoch, false, "resync")
	ack(t, r, false)
	r.Refuse(map[string]string{b: "encoding", c: "encoding"})
	if !slices.Equal(r.Refused, []string{b}) || !slices.Equal(r.PendingRefusals, []string{c}) {
		t.Fatal("gone history retained or new refusal lost")
	}
	got := requireKind(t, r, epoch, false, "resync")
	if got.Reason != "encoding" || got.Count != 1 {
		t.Fatal("unnecessary history-limit resync")
	}
}
func TestResyncDropsPendingRefusalHashes(t *testing.T) {
	r := record()
	r.Refuse(map[string]string{digest("unsafe"): "encoding"})
	r.Note("", "43", true)
	if len(r.PendingRefusals) != 0 {
		t.Fatal("resync retained stale queued hashes")
	}
}
