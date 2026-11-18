package coordinatorwait

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOverflowOnlyOnceAndThenPaced(t *testing.T) {
	r := record()
	r.Scan(map[string]string{"one": "stamp"}, true)
	requireKind(t, r, epoch, false, "resync")
	ack(t, r, false)
	r.Scan(map[string]string{"other-partial-entry": "stamp"}, true)
	if r.Take(epoch, false) != nil {
		t.Fatal("unchanged overfull directory returned another resync")
	}
	rt, c, calls := fakeRuntime(t)
	scans := 0
	rt.Scan = func() (Snapshot, error) { scans++; return Snapshot{Overflow: true}, nil }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runFake(ctx, rt, r)
	<-calls
	if d := <-c.timers; d != r.Options.ScanEvery {
		t.Fatalf("overfull directory not paced: %s", d)
	}
	c.Step(9 * time.Second)
	if scans != 1 {
		t.Fatal("overflow read repeated before scan interval")
	}
	cancel()
	out := <-done
	if out.err != nil || out.result.Kind != "stopped" {
		t.Fatal("overfull wait did not hold")
	}
	ack(t, r, false)
	r.Scan(map[string]string{"safe": "stamp"}, false)
	requireKind(t, r, epoch, false, "file")
	ack(t, r, false)
	r.Scan(map[string]string{"one": "stamp"}, true)
	requireKind(t, r, epoch, false, "resync")
}
func TestStoreWritesOnlyChangedRecord(t *testing.T) {
	home := t.TempDir()
	s, err := OpenStore(home, "session-uid", "tool-call")
	must(t, err)
	r := record()
	must(t, s.Save(r))
	s.Close()
	// Load must seed the unchanged check too, across invocations.
	s, err = OpenStore(home, "session-uid", "tool-call")
	must(t, err)
	defer s.Close()
	r, err = s.Load()
	must(t, err)
	syncs := 0
	s.sync = func(*os.File) error { syncs++; return nil }
	must(t, s.Save(r))
	must(t, s.Save(r))
	if syncs != 0 {
		t.Fatalf("unchanged record fsynced %d times", syncs)
	}
	r.Note("new", "43", false)
	must(t, s.Save(r))
	if syncs != 2 {
		t.Fatal("changed record not durably written")
	}
	must(t, s.Save(r))
	if syncs != 2 {
		t.Fatal("new saved record rewritten")
	}
}
func TestStoreCleansOnlyOwnRegularCrashTempUnderLock(t *testing.T) {
	for _, kind := range []string{"regular", "symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			home := t.TempDir()
			s, err := OpenStore(home, "session-uid", "tool-call")
			must(t, err)
			own := filepath.Join(home, ".dev-env-coordinator-waits", s.name+".tmp")
			other := own + "-other"
			must(t, os.WriteFile(other, nil, 0600))
			outside := filepath.Join(home, "outside")
			must(t, os.WriteFile(outside, []byte("keep"), 0600))
			switch kind {
			case "regular":
				must(t, os.WriteFile(own, nil, 0600))
			case "symlink":
				must(t, os.Symlink(outside, own))
			case "directory":
				must(t, os.Mkdir(own, 0700))
			}
			if second, err := OpenStore(home, "session-uid", "tool-call"); err == nil {
				second.Close()
				t.Fatal("cleanup ran without exclusive custody")
			}
			if _, err := os.Lstat(own); err != nil {
				t.Fatal("locked temp removed")
			}
			s.Close()
			next, err := OpenStore(home, "session-uid", "tool-call")
			if kind == "regular" {
				must(t, err)
				defer next.Close()
				if _, err = os.Lstat(own); !os.IsNotExist(err) {
					t.Fatal("own crash temp not removed")
				}
			} else {
				if err == nil {
					next.Close()
					t.Fatal("unsafe temp accepted")
				}
				if _, err = os.Lstat(own); err != nil {
					t.Fatal("unsafe temp removed")
				}
			}
			if _, err = os.Stat(other); err != nil {
				t.Fatal("unrelated file removed")
			}
			data, err := os.ReadFile(outside)
			must(t, err)
			if string(data) != "keep" {
				t.Fatal("temp target touched")
			}
		})
	}
}

func TestStoreRejectsBudgetBeyondStartBound(t *testing.T) {
	s, err := OpenStore(t.TempDir(), "session-uid", "tool-call")
	must(t, err)
	defer s.Close()
	r := record()
	r.Options.BudgetEnd = r.Started.Add(24*time.Hour + time.Second)
	if s.Save(r) == nil {
		t.Fatal("record allowed budget beyond start bound")
	}
}
func TestRefusalPendingBoundAndClosedReasons(t *testing.T) {
	r := record()
	r.Options.MaxPending = 1
	r.Refuse(map[string]string{digest("one"): "encoding", digest("two"): "control"})
	if len(r.Pending) != 1 {
		t.Fatal("refusals exceed pending bound")
	}
	s, err := OpenStore(t.TempDir(), "session-uid", "tool-call")
	must(t, err)
	defer s.Close()
	r.Pending[0].Reason = "untrusted-name"
	if s.Save(r) == nil {
		t.Fatal("open-ended refusal reason persisted")
	}
}
