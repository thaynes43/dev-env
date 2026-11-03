package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

const secretHome = "/home/dev-secret-name"

var headroomT0 = time.Date(2026, 11, 3, 22, 0, 0, 0, time.UTC)

// fsFake is a statfs seam that records its calls.
type fsFake struct {
	mu    sync.Mutex
	stat  syscall.Statfs_t
	err   error
	calls int
	paths []string
}

func (f *fsFake) fn(path string, out *syscall.Statfs_t) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.paths = append(f.paths, path)
	*out = f.stat
	return f.err
}

func (f *fsFake) count() int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls }

func (f *fsFake) set(mutate func(*syscall.Statfs_t)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	mutate(&f.stat)
}

// healthy is 10000 blocks of 4096 and 1000 inodes, 90% free in both.
func healthy() *fsFake {
	return &fsFake{stat: syscall.Statfs_t{Blocks: 10000, Bfree: 9000, Bavail: 9000, Bsize: 4096, Frsize: 4096, Files: 1000, Ffree: 900}}
}

func newMonitor(f *fsFake) *homeHeadroomMonitor {
	return &homeHeadroomMonitor{statfs: f.fn, timeout: time.Second}
}

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func sample(t *testing.T, m *homeHeadroomMonitor, at time.Time) protocol.HomeHeadroom {
	t.Helper()
	h := m.Sample(secretHome, at, discard())
	if h == nil {
		t.Fatal("no observation")
	}
	if !h.Valid() {
		t.Fatalf("not a valid observation: %+v", h)
	}
	raw, _ := json.Marshal(h)
	if strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "/home") {
		t.Fatalf("observation carries a path: %s", raw)
	}
	return *h
}

func TestHomeHeadroomHealthy(t *testing.T) {
	f := healthy()
	h := sample(t, newMonitor(f), headroomT0)
	if h.State != protocol.HeadroomOK || h.Reason != "" || !h.ObservedAt.Equal(headroomT0) {
		t.Fatalf("%+v", h)
	}
	if h.Bytes.Total != 10000*4096 || h.Bytes.Available != 9000*4096 || h.Inodes.Total != 1000 || h.Inodes.Available != 900 {
		t.Fatalf("figures %+v %+v", h.Bytes, h.Inodes)
	}
	if len(f.paths) != 1 || f.paths[0] != secretHome {
		t.Fatalf("statfs must be asked about the HOME path only: %v", f.paths)
	}
}

func TestHomeHeadroomBytesLowAndInodesFine(t *testing.T) {
	f := healthy()
	f.set(func(s *syscall.Statfs_t) { s.Bfree, s.Bavail = 1000, 1000 })
	h := sample(t, newMonitor(f), headroomT0)
	if h.State != protocol.HeadroomLow || h.Bytes.State != protocol.HeadroomLow || h.Inodes.State != protocol.HeadroomOK {
		t.Fatalf("%+v", h)
	}
}

func TestHomeHeadroomInodesExhaustedWhileBytesAreFree(t *testing.T) {
	f := healthy()
	f.set(func(s *syscall.Statfs_t) { s.Ffree = 0 })
	h := sample(t, newMonitor(f), headroomT0)
	if h.State != protocol.HeadroomExhausted || h.Bytes.State != protocol.HeadroomOK || h.Inodes.State != protocol.HeadroomExhausted || h.Inodes.Available != 0 {
		t.Fatalf("%+v", h)
	}
	if h.Bytes.Available != 9000*4096 {
		t.Fatalf("bytes must still be reported: %+v", h.Bytes)
	}
}

func TestHomeHeadroomThresholdBoundaries(t *testing.T) {
	type row struct {
		avail uint64
		want  string
	}
	bytesRows := []row{{1500, protocol.HeadroomOK}, {1499, protocol.HeadroomLow}, {300, protocol.HeadroomLow}, {299, protocol.HeadroomExhausted}, {0, protocol.HeadroomExhausted}}
	for _, r := range bytesRows {
		f := healthy()
		f.set(func(s *syscall.Statfs_t) { s.Bfree, s.Bavail = r.avail, r.avail })
		if got := sample(t, newMonitor(f), headroomT0).Bytes.State; got != r.want {
			t.Errorf("bytes avail %d of 10000: %s, want %s", r.avail, got, r.want)
		}
	}
	inodeRows := []row{{100, protocol.HeadroomOK}, {99, protocol.HeadroomLow}, {20, protocol.HeadroomLow}, {19, protocol.HeadroomExhausted}, {0, protocol.HeadroomExhausted}}
	for _, r := range inodeRows {
		f := healthy()
		f.set(func(s *syscall.Statfs_t) { s.Ffree = r.avail })
		if got := sample(t, newMonitor(f), headroomT0).Inodes.State; got != r.want {
			t.Errorf("inodes free %d of 1000: %s, want %s", r.avail, got, r.want)
		}
	}
}

func TestHomeHeadroomReservedBlocksAreNotAvailable(t *testing.T) {
	f := healthy()
	f.set(func(s *syscall.Statfs_t) { s.Bfree, s.Bavail = 5000, 0 })
	h := sample(t, newMonitor(f), headroomT0)
	if h.State != protocol.HeadroomExhausted || h.Bytes.Available != 0 {
		t.Fatalf("Bavail, not Bfree, is what the agent can use: %+v", h)
	}
}

func TestHomeHeadroomFragmentSizeFallsBackToBlockSize(t *testing.T) {
	f := healthy()
	f.set(func(s *syscall.Statfs_t) { s.Frsize, s.Bsize = 1024, 4096 })
	if h := sample(t, newMonitor(f), headroomT0); h.Bytes.Total != 10000*1024 {
		t.Fatalf("Frsize must win: %+v", h.Bytes)
	}
	f = healthy()
	f.set(func(s *syscall.Statfs_t) { s.Frsize, s.Bsize = 0, 2048 })
	if h := sample(t, newMonitor(f), headroomT0); h.Bytes.Total != 10000*2048 {
		t.Fatalf("Bsize fallback: %+v", h.Bytes)
	}
}

func TestHomeHeadroomUnreadableMapsErrnoToClosedReason(t *testing.T) {
	secretErr := &os.PathError{Op: "statfs", Path: secretHome + "/volume", Err: syscall.ENOENT}
	rows := []struct {
		err  error
		want string
	}{
		{syscall.EACCES, protocol.HeadroomReasonPermission},
		{syscall.EPERM, protocol.HeadroomReasonPermission},
		{syscall.ENOENT, protocol.HeadroomReasonNotFound},
		{secretErr, protocol.HeadroomReasonNotFound},
		{syscall.EIO, protocol.HeadroomReasonIO},
		{syscall.ESTALE, protocol.HeadroomReasonIO},
		{syscall.ENOTCONN, protocol.HeadroomReasonIO},
		{syscall.EINVAL, protocol.HeadroomReasonOther},
		{errors.New("boom " + secretHome), protocol.HeadroomReasonOther},
	}
	for _, r := range rows {
		f := healthy()
		f.err = r.err
		h := sample(t, newMonitor(f), headroomT0)
		if h.State != protocol.HeadroomUnreadable || h.Reason != r.want || h.Bytes != nil || h.Inodes != nil {
			t.Errorf("%v: %+v", r.err, h)
		}
	}
}

func TestHomeHeadroomZeroSizeFilesystem(t *testing.T) {
	f := healthy()
	f.set(func(s *syscall.Statfs_t) { s.Blocks, s.Bfree, s.Bavail = 0, 0, 0 })
	h := sample(t, newMonitor(f), headroomT0)
	if h.State != protocol.HeadroomUnknown || h.Reason != protocol.HeadroomReasonBytesInvalid || h.Bytes != nil || h.Inodes == nil {
		t.Fatalf("zero blocks: %+v", h)
	}
	f = healthy()
	f.set(func(s *syscall.Statfs_t) { s.Files, s.Ffree = 0, 0 })
	h = sample(t, newMonitor(f), headroomT0)
	if h.State != protocol.HeadroomUnknown || h.Reason != protocol.HeadroomReasonInodesInvalid || h.Inodes != nil || h.Bytes == nil {
		t.Fatalf("zero inodes: %+v", h)
	}
	f = &fsFake{}
	h = sample(t, newMonitor(f), headroomT0)
	if h.State != protocol.HeadroomUnknown || h.Reason != protocol.HeadroomReasonBothInvalid || h.Bytes != nil || h.Inodes != nil {
		t.Fatalf("all zero: %+v", h)
	}
	f = healthy()
	f.set(func(s *syscall.Statfs_t) { s.Frsize, s.Bsize = 0, 0 })
	if h = sample(t, newMonitor(f), headroomT0); h.Reason != protocol.HeadroomReasonBytesInvalid {
		t.Fatalf("no block size: %+v", h)
	}
}

func TestHomeHeadroomInvalidHalfDoesNotHideARealProblem(t *testing.T) {
	f := healthy()
	f.set(func(s *syscall.Statfs_t) { s.Blocks, s.Bfree, s.Bavail, s.Ffree = 0, 0, 0, 0 })
	h := sample(t, newMonitor(f), headroomT0)
	if h.State != protocol.HeadroomExhausted || h.Reason != "" || h.Bytes != nil || h.Inodes.State != protocol.HeadroomExhausted {
		t.Fatalf("exhausted inodes must outrank an unusable byte figure: %+v", h)
	}
}

func TestHomeHeadroomContradictoryAndOverflowingCountsAreInvalid(t *testing.T) {
	cases := map[string]func(*syscall.Statfs_t){
		"free above total":   func(s *syscall.Statfs_t) { s.Bfree = s.Blocks + 1 },
		"avail above free":   func(s *syscall.Statfs_t) { s.Bavail = s.Bfree + 1 },
		"negative block":     func(s *syscall.Statfs_t) { s.Frsize = -1; s.Bsize = -1 },
		"capacity overflows": func(s *syscall.Statfs_t) { s.Blocks, s.Bfree, s.Bavail, s.Frsize = math.MaxUint64, 1, 1, 4096 },
	}
	for name, mutate := range cases {
		f := healthy()
		f.set(mutate)
		h := sample(t, newMonitor(f), headroomT0)
		if h.State != protocol.HeadroomUnknown || h.Reason != protocol.HeadroomReasonBytesInvalid || h.Bytes != nil {
			t.Errorf("%s: %+v", name, h)
		}
	}
	f := healthy()
	f.set(func(s *syscall.Statfs_t) { s.Ffree = s.Files + 1 })
	if h := sample(t, newMonitor(f), headroomT0); h.Reason != protocol.HeadroomReasonInodesInvalid || h.Inodes != nil {
		t.Fatalf("free inodes above total: %+v", h)
	}
}

func TestHomeHeadroomHugeFilesystemDoesNotOverflowThePercentTest(t *testing.T) {
	f := healthy()
	// Frsize 1, so total is MaxUint64 bytes; a naive avail*100 would wrap.
	f.set(func(s *syscall.Statfs_t) {
		s.Frsize, s.Bsize, s.Blocks, s.Bfree, s.Bavail = 1, 1, math.MaxUint64, math.MaxUint64/10, math.MaxUint64/10
	})
	if h := sample(t, newMonitor(f), headroomT0); h.Bytes == nil || h.Bytes.State != protocol.HeadroomLow {
		t.Fatalf("10%% free of a huge filesystem is low: %+v", h)
	}
	f.set(func(s *syscall.Statfs_t) { s.Bfree, s.Bavail = math.MaxUint64/2, math.MaxUint64/2 })
	if h := sample(t, newMonitor(f), headroomT0.Add(time.Hour)); h.Bytes.State != protocol.HeadroomOK {
		t.Fatalf("50%% free of a huge filesystem is ok: %+v", h)
	}
}

func TestHomeHeadroomCadenceAndCache(t *testing.T) {
	f := healthy()
	m := newMonitor(f)
	first := sample(t, m, time.Time{}) // a zero clock must still sample once and then cache
	if f.count() != 1 {
		t.Fatal("first sample")
	}
	again := sample(t, m, time.Time{}.Add(homeHeadroomEvery-time.Nanosecond))
	if f.count() != 1 || !again.ObservedAt.Equal(first.ObservedAt) {
		t.Fatalf("sampled sooner than %v: calls %d", homeHeadroomEvery, f.count())
	}
	f.set(func(s *syscall.Statfs_t) { s.Bfree, s.Bavail = 100, 100 })
	cached := sample(t, m, time.Time{}.Add(homeHeadroomEvery-time.Nanosecond))
	if cached.State != protocol.HeadroomOK {
		t.Fatal("the cached observation must be returned inside the interval")
	}
	at := time.Time{}.Add(homeHeadroomEvery)
	fresh := sample(t, m, at)
	if f.count() != 2 || fresh.State != protocol.HeadroomExhausted || !fresh.ObservedAt.Equal(at) {
		t.Fatalf("calls %d %+v", f.count(), fresh)
	}
}

func TestHomeHeadroomClockSteps(t *testing.T) {
	f := healthy()
	m := newMonitor(f)
	sample(t, m, headroomT0)
	back := headroomT0.Add(-time.Hour)
	h := sample(t, m, back)
	if f.count() != 2 || !h.ObservedAt.Equal(back) {
		t.Fatalf("a clock stepped back must resample: calls %d %v", f.count(), h.ObservedAt)
	}
	sample(t, m, back.Add(time.Second))
	if f.count() != 2 {
		t.Fatalf("one second after the new base must be cached: calls %d", f.count())
	}
	h = sample(t, m, back.Add(1000*time.Hour))
	if f.count() != 3 || !h.ObservedAt.Equal(back.Add(1000*time.Hour)) {
		t.Fatalf("a big forward step must resample: calls %d", f.count())
	}
	if h.ObservedAt.Location() != time.UTC {
		t.Fatal("ObservedAt must be UTC")
	}
}

func TestHomeHeadroomHungStatfsIsBoundedAndNeverStacks(t *testing.T) {
	release := make(chan struct{})
	var calls atomic.Int32
	m := &homeHeadroomMonitor{timeout: 20 * time.Millisecond}
	m.statfs = func(_ string, out *syscall.Statfs_t) error {
		if calls.Add(1) == 1 {
			<-release
		}
		*out = healthy().stat
		return nil
	}
	start := time.Now()
	h := sample(t, m, headroomT0)
	if time.Since(start) > time.Second {
		t.Fatal("a hung statfs must not hold the heartbeat")
	}
	if h.State != protocol.HeadroomUnreadable || h.Reason != protocol.HeadroomReasonTimeout || h.Bytes != nil {
		t.Fatalf("%+v", h)
	}
	for i := 1; i <= 3; i++ {
		h = sample(t, m, headroomT0.Add(time.Duration(i)*homeHeadroomEvery))
		if h.Reason != protocol.HeadroomReasonTimeout {
			t.Fatalf("still stuck: %+v", h)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("a second statfs was started while one was stuck: %d", calls.Load())
	}
	close(release)
	for i := 0; i < 200 && m.inflight.Load(); i++ {
		time.Sleep(5 * time.Millisecond)
	}
	if m.inflight.Load() {
		t.Fatal("the stuck call never cleared")
	}
	h = sample(t, m, headroomT0.Add(10*homeHeadroomEvery))
	if h.State != protocol.HeadroomOK || calls.Load() != 2 {
		t.Fatalf("recovery: calls %d %+v", calls.Load(), h)
	}
}

func TestHomeHeadroomLogsOnlyTransitions(t *testing.T) {
	var out bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&out, nil))
	f := healthy()
	m := newMonitor(f)
	at := headroomT0
	step := func() { at = at.Add(homeHeadroomEvery); m.Sample(secretHome, at, log) }
	m.Sample(secretHome, at, log)
	step()
	if out.Len() != 0 {
		t.Fatalf("a healthy start must be silent: %s", &out)
	}
	f.set(func(s *syscall.Statfs_t) { s.Bfree, s.Bavail = 1000, 1000 })
	step()
	step()
	step()
	if n := strings.Count(out.String(), `"level":"WARN"`); n != 1 || !strings.Contains(out.String(), `"state":"low"`) || !strings.Contains(out.String(), `"availableBytes":4096000`) || !strings.Contains(out.String(), `"totalBytes":40960000`) {
		t.Fatalf("one warning on entering low, with numbers: %s", &out)
	}
	f.set(func(s *syscall.Statfs_t) { s.Bfree, s.Bavail = 100, 100 })
	step()
	f.mu.Lock()
	f.err = syscall.EIO
	f.mu.Unlock()
	step()
	step()
	if n := strings.Count(out.String(), `"level":"WARN"`); n != 3 || !strings.Contains(out.String(), `"reason":"io"`) {
		t.Fatalf("exhausted and unreadable each warn once: %s", &out)
	}
	f.mu.Lock()
	f.err = nil
	f.stat.Bfree, f.stat.Bavail = 9000, 9000
	f.mu.Unlock()
	step()
	step()
	if n := strings.Count(out.String(), `"level":"INFO"`); n != 1 || !strings.Contains(out.String(), `"state":"ok"`) {
		t.Fatalf("one info on recovery: %s", &out)
	}
	if strings.Contains(out.String(), "secret") || strings.Contains(out.String(), "/home") {
		t.Fatalf("the log carries a path: %s", &out)
	}
}

func TestHomeHeadroomVanishingHomeAndEntryChurn(t *testing.T) {
	// Real statfs on a real directory: no entry is listed or opened, so files
	// appearing and vanishing cannot change or break the sample.
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			p := filepath.Join(home, "f"+string(rune('a'+i%20)))
			_ = os.WriteFile(p, []byte("x"), 0o600)
			_ = os.Remove(p)
		}
	}()
	at := headroomT0
	for i := 0; i < 20; i++ {
		at = at.Add(homeHeadroomEvery)
		h := SampleHomeHeadroom(home, at)
		if h == nil || !h.Valid() || h.State == protocol.HeadroomUnreadable {
			t.Fatalf("churn: %+v", h)
		}
		if h.Bytes != nil && h.Bytes.Available > h.Bytes.Total {
			t.Fatalf("figures: %+v", h.Bytes)
		}
	}
	close(stop)
	wg.Wait()
	if err := os.RemoveAll(home); err != nil {
		t.Fatal(err)
	}
	h := SampleHomeHeadroom(home, at.Add(time.Minute))
	if h == nil || h.State != protocol.HeadroomUnreadable || h.Reason != protocol.HeadroomReasonNotFound || h.Bytes != nil {
		t.Fatalf("a vanished HOME is unreadable/not-found: %+v", h)
	}
	raw, _ := json.Marshal(h)
	if strings.Contains(string(raw), dir) {
		t.Fatalf("path leaked: %s", raw)
	}
}

func TestHomeHeadroomNeverReadsDirectoryContents(t *testing.T) {
	// A HOME that is a file, with unreadable content, still gets a sample from
	// statfs alone; nothing opens or lists it.
	dir := t.TempDir()
	f := filepath.Join(dir, "regular")
	if err := os.WriteFile(f, []byte("data"), 0o000); err != nil {
		t.Fatal(err)
	}
	if h := SampleHomeHeadroom(f, headroomT0); h == nil || h.State == protocol.HeadroomUnreadable || !h.Valid() {
		t.Fatalf("%+v", h)
	}
}

func TestHomeHeadroomDaemonHeartbeatCarriesItAndTheStatusRoute(t *testing.T) {
	f := healthy()
	now := headroomT0
	d := &Daemon{S: testSettings(t, t.TempDir()), R: &fakeRunner{}, Log: discard(), Session: cloneSession("h-1"), Now: func() time.Time { return now }}
	d.homeHeadroom.statfs = f.fn
	var sent []*protocol.HomeHeadroom
	d.Send = func(_ context.Context, st protocol.Status) error {
		sent = append(sent, st.HomeHeadroom)
		return nil
	}
	d.beat(context.Background())
	d.beat(context.Background())
	if len(sent) != 2 || sent[0] == nil || sent[0].State != protocol.HeadroomOK || !reflect.DeepEqual(sent[0], sent[1]) {
		t.Fatalf("heartbeats: %+v", sent)
	}
	if f.count() != 1 {
		t.Fatalf("two beats inside the interval must share one sample: %d", f.count())
	}
	if f.paths[0] != d.S.Home {
		t.Fatalf("sampled %q, want HOME", f.paths[0])
	}
	now = now.Add(time.Minute)
	f.set(func(s *syscall.Statfs_t) { s.Ffree = 0 })
	d.beat(context.Background())
	if f.count() != 2 || sent[2].State != protocol.HeadroomExhausted {
		t.Fatalf("the next beat reports the change: %+v", sent[2])
	}
}
