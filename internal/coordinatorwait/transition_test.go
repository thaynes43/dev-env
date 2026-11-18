package coordinatorwait

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDirectoryReturnToEarlierStateReportsEveryTransition(t *testing.T) {
	rt, c, _ := fakeRuntime(t)
	r := record()
	ids := map[string]bool{}
	for _, files := range []map[string]string{{"A": "stamp"}, {}, {"B": "stamp"}, {}} {
		c.Step(r.Options.ScanEvery)
		r.Scan(files, false)
		v, err := rt.finish(r, c.Now(), false)
		must(t, err)
		if v == nil || v.Kind != "file" || ids[v.ID] {
			t.Fatal("return to earlier state was suppressed or reused an ID")
		}
		ids[v.ID] = true
		must(t, r.Bind(r.Options, "", false, false))
		c.Step(time.Second)
		replay, err := rt.finish(r, c.Now(), false)
		must(t, err)
		if !reflect.DeepEqual(v, replay) {
			t.Fatal("unacknowledged transition changed")
		}
		ack(t, r, false)
	}
}

func TestFileTransitionCounterSurvivesStopResumeAndRejectsRollback(t *testing.T) {
	s, err := OpenStore(t.TempDir(), "session-uid", "tool-call")
	must(t, err)
	defer s.Close()
	r := record()
	r.Scan(map[string]string{"A": "a"}, false)
	r.Scan(map[string]string{"B": "b"}, false)
	stopped := requireKind(t, r, epoch, true, "stopped")
	must(t, s.Save(r))
	r, err = s.Load()
	must(t, err)
	if r.Transitions != 2 {
		t.Fatal("transition counter not persisted")
	}
	must(t, r.Bind(r.Options, stopped.ID, true, false))
	must(t, s.Save(r))
	if r.Transitions != 2 {
		t.Fatal("resume reset transition counter")
	}
	r.Transitions = 1
	if s.Save(r) == nil {
		t.Fatal("counter below pending transition saved")
	}
	data, err := json.Marshal(r)
	must(t, err)
	f, err := s.root.OpenFile(s.name+".json", os.O_WRONLY|os.O_TRUNC, 0600)
	must(t, err)
	_, err = f.Write(data)
	must(t, err)
	must(t, f.Close())
	if _, err = s.Load(); err == nil {
		t.Fatal("counter below pending transition loaded")
	}
}
func TestFileTransitionCounterBoundResyncs(t *testing.T) {
	r := record()
	r.Transitions = 1 << 63
	r.Scan(map[string]string{"new": "stamp"}, false)
	requireKind(t, r, epoch, false, "resync")
	if r.Transitions != 1<<63 {
		t.Fatal("transition counter wrapped or moved backwards")
	}
	r.Transitions++
	if r.validate() == nil {
		t.Fatal("out-of-bound transition counter accepted")
	}
}

func TestOutstandingFileCounterRollbackRefused(t *testing.T) {
	s, err := OpenStore(t.TempDir(), "session-uid", "tool-call")
	must(t, err)
	defer s.Close()
	r := record()
	r.Scan(map[string]string{"A": "a"}, false)
	requireKind(t, r, epoch, false, "file")
	must(t, s.Save(r))
	r.Transitions = 0
	if s.Save(r) == nil {
		t.Fatal("outstanding file counter rollback saved")
	}
	data, err := json.Marshal(r)
	must(t, err)
	f, err := s.root.OpenFile(s.name+".json", os.O_WRONLY|os.O_TRUNC, 0600)
	must(t, err)
	_, err = f.Write(data)
	must(t, err)
	must(t, f.Close())
	if _, err = s.Load(); err == nil {
		t.Fatal("counter rollback treated as unreadable-result recovery")
	}
}

func TestStoreRefusesFileWithZeroTransition(t *testing.T) {
	s, err := OpenStore(t.TempDir(), "session-uid", "tool-call")
	must(t, err)
	defer s.Close()
	r := record()
	r.Scan(map[string]string{"A": "a"}, false)
	r.Pending[0].ID = strings.Repeat("0", 64)
	if s.Save(r) == nil {
		t.Fatal("file without a transition accepted")
	}
}
