package summonquota

// The gate's proofs of story 783: the one read per 30 seconds, the bounded
// read, the read that never happens, and the one log line per decision. A
// fake read and a clock the test moves; the timeout test waits on the read's
// own context, never on a sleep.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// summoned is a Queued summoned Claude session of the given priority.
func summoned(name string, p v1alpha1.SummonPriority) *v1alpha1.AgentSession {
	s := &v1alpha1.AgentSession{}
	s.Name = name
	s.Spec.Caller, s.Spec.Agent, s.Spec.Lane = "cigar-ns.cigar-curator", v1alpha1.AgentClaude, v1alpha1.LaneCuration
	s.Spec.Prompt = "SESSION-CONTROLLED-PROMPT"
	s.Spec.Summon = &v1alpha1.SessionSummon{DispatchID: "d-" + name, Priority: p, FallbackModel: "claude-opus-5", OnUnreported: v1alpha1.SummonOnUnreportedFailed}
	return s
}

// fakeStore is the read's spy: it counts reads and answers with readings or
// an error. block makes the read wait for its context; during, when set, runs
// inside the read (a test moves the clock there, as a slow read would).
type fakeStore struct {
	reads    int
	readings map[string]Reading
	err      error
	block    bool
	during   func()
}

func (f *fakeStore) read(ctx context.Context) (map[string]Reading, error) {
	f.reads++
	if f.during != nil {
		f.during()
	}
	if f.block {
		<-ctx.Done()
		return nil, errors.New("store unavailable")
	}
	return f.readings, f.err
}

// logLines collects the gate's log lines, one string each.
type logLines struct{ lines []string }

func (l *logLines) logger() logr.Logger {
	return funcr.New(func(prefix, args string) { l.lines = append(l.lines, prefix+" "+args) }, funcr.Options{})
}

type gateWorld struct {
	now   time.Time
	store *fakeStore
	logs  *logLines
	g     *Gate
}

func newGate(lines Lines, readings map[string]Reading) *gateWorld {
	w := &gateWorld{now: t0, store: &fakeStore{readings: readings}, logs: &logLines{}}
	w.g = &Gate{Lines: lines, Read: w.store.read, Now: func() time.Time { return w.now }, Log: w.logs.logger()}
	return w
}

func claudeAt(windows ...any) map[string]Reading {
	return map[string]Reading{protocol.AgentClaude: reading(windows...)}
}

// Proof 6 as the coordinator ruled it (op783-q-1, gap A): the boolean seam
// gets no pass id, so the bound is one read per 30 seconds on the gate's own
// clock. Several Queued sessions in one pass share one read.
func TestGateReadsAtMostOncePerThirtySeconds(t *testing.T) {
	w := newGate(installLines, claudeAt(FiveHourMinutes, "81"))
	for _, s := range []*v1alpha1.AgentSession{summoned("a", v1alpha1.SummonPriorityBulk), summoned("b", v1alpha1.SummonPriorityBulk), summoned("c", v1alpha1.SummonPriorityNormal), summoned("d", v1alpha1.SummonPriorityBulk)} {
		w.g.Admits(s)
	}
	if w.store.reads != 1 {
		t.Fatalf("four Queued sessions in one pass: %d reads, want 1", w.store.reads)
	}
	w.now = t0.Add(29 * time.Second)
	w.g.Admits(summoned("a", v1alpha1.SummonPriorityBulk))
	if w.store.reads != 1 {
		t.Fatalf("29 s later: %d reads, want 1", w.store.reads)
	}
	w.now = t0.Add(DefaultReuse)
	w.g.Admits(summoned("a", v1alpha1.SummonPriorityBulk))
	w.g.Admits(summoned("b", v1alpha1.SummonPriorityBulk))
	if w.store.reads != 2 {
		t.Fatalf("30 s later: %d reads, want 2", w.store.reads)
	}
	// A clock that went back reads again rather than keep an old read.
	w.now = t0.Add(-time.Minute)
	w.g.Admits(summoned("a", v1alpha1.SummonPriorityBulk))
	if w.store.reads != 3 {
		t.Fatalf("clock back a minute: %d reads, want 3", w.store.reads)
	}
}

// Proof 5: with every line unset nothing is read and everything admits.
// (main.go does not even build the gate then: TestSummonQuotaHookOffWithoutLines.)
func TestGateWithEveryLineUnsetReadsNothing(t *testing.T) {
	w := newGate(Lines{MaxAge: time.Minute}, claudeAt(FiveHourMinutes, "100", SevenDayMinutes, "100"))
	for _, p := range []v1alpha1.SummonPriority{v1alpha1.SummonPriorityUrgent, v1alpha1.SummonPriorityNormal, v1alpha1.SummonPriorityBulk} {
		if !w.g.Admits(summoned("a", p)) {
			t.Errorf("%s held with no line set", p)
		}
	}
	if w.store.reads != 0 {
		t.Errorf("%d reads with every line unset", w.store.reads)
	}
}

// A line is read only for the priority it belongs to, and urgent never
// reads.
func TestGateReadsOnlyForAPriorityWithALine(t *testing.T) {
	w := newGate(Lines{BulkFiveHour: 80, MaxAge: 15 * time.Minute}, claudeAt(FiveHourMinutes, "100"))
	if !w.g.Admits(summoned("u", v1alpha1.SummonPriorityUrgent)) || !w.g.Admits(summoned("n", v1alpha1.SummonPriorityNormal)) || w.store.reads != 0 {
		t.Fatalf("urgent or normal with only the bulk line set: %d reads", w.store.reads)
	}
	if w.g.Admits(summoned("b", v1alpha1.SummonPriorityBulk)) || w.store.reads != 1 {
		t.Fatalf("bulk at 100 %%, line 80: admitted or %d reads", w.store.reads)
	}
}

// Proof 8's guard at the seam: the pass calls the hook only for sessions
// with spec.caller (internal/controller/summon_admission.go, the queued list
// and the hold switch); a session without one is admitted here too, with no
// read and no log.
func TestGateSessionWithoutCallerReadsNothing(t *testing.T) {
	w := newGate(installLines, claudeAt(FiveHourMinutes, "100"))
	s := summoned("tom", v1alpha1.SummonPriorityBulk)
	s.Spec.Caller = ""
	if !w.g.Admits(s) || w.store.reads != 0 || len(w.logs.lines) != 0 {
		t.Fatalf("a session without spec.caller: %d reads, logs %q", w.store.reads, w.logs.lines)
	}
}

// Proof 4 at the seam: a read error, a timeout and a missing reading each
// admit and log why; a failed read is reused for 30 seconds like a good one.
func TestGateReadFailuresAdmitAndLogWhy(t *testing.T) {
	t.Run("read error", func(t *testing.T) {
		w := newGate(installLines, nil)
		w.store.err = errors.New("store unavailable")
		for _, name := range []string{"a", "b"} {
			if !w.g.Admits(summoned(name, v1alpha1.SummonPriorityBulk)) {
				t.Fatal("held on a read error")
			}
		}
		if w.store.reads != 1 || len(w.logs.lines) != 2 || !strings.Contains(w.logs.lines[0], `"reason"="read-error"`) || !strings.Contains(w.logs.lines[0], `"readingAge"="none"`) {
			t.Fatalf("%d reads, logs %q", w.store.reads, w.logs.lines)
		}
	})
	t.Run("read timeout", func(t *testing.T) {
		w := newGate(installLines, nil)
		w.store.block = true
		w.g.ReadTimeout = 20 * time.Millisecond
		if !w.g.Admits(summoned("a", v1alpha1.SummonPriorityNormal)) {
			t.Fatal("held on a read timeout")
		}
		if w.store.reads != 1 || len(w.logs.lines) != 1 || !strings.Contains(w.logs.lines[0], `"reason"="read-timeout"`) {
			t.Fatalf("%d reads, logs %q", w.store.reads, w.logs.lines)
		}
	})
	t.Run("no reading for the agent", func(t *testing.T) {
		w := newGate(installLines, map[string]Reading{protocol.AgentCodex: reading(FiveHourMinutes, "100")})
		if !w.g.Admits(summoned("a", v1alpha1.SummonPriorityBulk)) || !strings.Contains(w.logs.lines[0], `"reason"="no-reading"`) {
			t.Fatalf("logs %q", w.logs.lines)
		}
	})
	t.Run("nil read", func(t *testing.T) {
		w := newGate(installLines, nil)
		w.g.Read = nil
		if !w.g.Admits(summoned("a", v1alpha1.SummonPriorityBulk)) || !strings.Contains(w.logs.lines[0], `"reason"="read-error"`) {
			t.Fatalf("logs %q", w.logs.lines)
		}
	})
}

// Item 4 of the order: one log line per decision for a normal or bulk
// session, with the decision, the priority, each window's percentage and
// line and the reading's age; none for urgent; no session-controlled text.
func TestGateLogsOneLinePerDecision(t *testing.T) {
	weekly := installLines
	weekly.BulkSevenDay = 90
	w := newGate(weekly, claudeAt(FiveHourMinutes, "81", SevenDayMinutes, "91"))
	if w.g.Admits(summoned("held-one", v1alpha1.SummonPriorityBulk)) {
		t.Fatal("bulk past both lines admitted")
	}
	if !w.g.Admits(summoned("normal-one", v1alpha1.SummonPriorityNormal)) {
		t.Fatal("normal at 81 %% held")
	}
	w.g.Admits(summoned("urgent-one", v1alpha1.SummonPriorityUrgent))
	if len(w.logs.lines) != 2 {
		t.Fatalf("%d log lines, want 2: %q", len(w.logs.lines), w.logs.lines)
	}
	held, admitted := w.logs.lines[0], w.logs.lines[1]
	for _, want := range []string{`"msg"="quota gate held a summoned session"`, `"session"="held-one"`, `"lane"="curation"`, `"priority"="bulk"`, `"reason"="five-hour-past-line"`,
		`"fiveHourUsed"="81"`, `"fiveHourLine"="80"`, `"sevenDayUsed"="91"`, `"sevenDayLine"="90"`, `"readingAge"="2m0s"`} {
		if !strings.Contains(held, want) {
			t.Errorf("held line %q lacks %s", held, want)
		}
	}
	for _, want := range []string{`"msg"="quota gate admitted a summoned session"`, `"priority"="normal"`, `"reason"="within-lines"`, `"fiveHourLine"="95"`, `"sevenDayUsed"="91"`, `"sevenDayLine"="off"`} {
		if !strings.Contains(admitted, want) {
			t.Errorf("admitted line %q lacks %s", admitted, want)
		}
	}
	for _, l := range w.logs.lines {
		if strings.Contains(l, "SESSION-CONTROLLED") || strings.Contains(l, "cigar-curator") || strings.Contains(l, "d-held-one") {
			t.Errorf("log line carries session-controlled or caller text: %q", l)
		}
	}
}

// Ruling R1 (fix round 1 on PR 1101): the operator's clock goes back an hour
// behind a stored high reading. The gate reads again, the store returns the
// same reading, now an hour in the future, and the session is admitted with
// reason reading-from-future: the reading's time is unusable, so it fails
// open rather than calling the reading fresh.
func TestGateClockRolledBackBehindAHighReadingAdmits(t *testing.T) {
	stored := claudeAt(FiveHourMinutes, "99")
	w := newGate(installLines, stored)
	if w.g.Admits(summoned("before", v1alpha1.SummonPriorityBulk)) || w.store.reads != 1 {
		t.Fatalf("bulk at 99 %%, line 80, before the clock went back: admitted, or %d reads", w.store.reads)
	}
	w.now = t0.Add(-time.Hour)
	if !w.g.Admits(summoned("after", v1alpha1.SummonPriorityBulk)) {
		t.Fatal("the clock went back an hour behind a 99 % reading and the session was held")
	}
	if w.store.reads != 2 {
		t.Fatalf("the clock went back: %d reads, want a new read (2)", w.store.reads)
	}
	if got := w.g.readings[protocol.AgentClaude]; !got.ReadAt.Equal(stored[protocol.AgentClaude].ReadAt) {
		t.Fatalf("the new read returned another reading: %+v", got)
	}
	if len(w.logs.lines) != 2 {
		t.Fatalf("%d log lines, want 2: %q", len(w.logs.lines), w.logs.lines)
	}
	for _, want := range []string{`"msg"="quota gate admitted a summoned session"`, `"session"="after"`, `"reason"="reading-from-future"`,
		`"fiveHourUsed"="99"`, `"fiveHourLine"="80"`, `"readingAge"="-58m0s"`} {
		if !strings.Contains(w.logs.lines[1], want) {
			t.Errorf("admitted line %q lacks %s", w.logs.lines[1], want)
		}
	}
}

// R1 in the log: a negative age is never written as zero, even under a
// second. A reading exactly at the clock is used and holds.
func TestGateLogsAFutureReadingsNegativeAge(t *testing.T) {
	r := reading(FiveHourMinutes, "99")
	r.ReadAt = t0.Add(500 * time.Millisecond)
	w := newGate(installLines, map[string]Reading{protocol.AgentClaude: r})
	if !w.g.Admits(summoned("ahead", v1alpha1.SummonPriorityNormal)) {
		t.Fatal("a reading half a second in the future held")
	}
	if l := w.logs.lines[0]; !strings.Contains(l, `"reason"="reading-from-future"`) || !strings.Contains(l, `"readingAge"="-500ms"`) {
		t.Fatalf("log %q", l)
	}
	r.ReadAt = t0
	w = newGate(installLines, map[string]Reading{protocol.AgentClaude: r})
	if w.g.Admits(summoned("at", v1alpha1.SummonPriorityNormal)) {
		t.Fatal("a 99 % reading taken exactly at the clock admitted")
	}
	if l := w.logs.lines[0]; !strings.Contains(l, `"reason"="five-hour-past-line"`) || !strings.Contains(l, `"readingAge"="0s"`) {
		t.Fatalf("log %q", l)
	}
}

// Ruling R2: staleness and a window's reset are judged on the clock after the
// read returns. The read moves the fake clock four seconds, as a slow GET
// would; no sleep.
func TestGateDecidesOnTheClockAfterTheRead(t *testing.T) {
	t.Run("a window that resets during the read admits", func(t *testing.T) {
		r := reading(FiveHourMinutes, "81")
		r.Windows[0].ResetsAt = t0.Add(time.Second)
		w := newGate(installLines, map[string]Reading{protocol.AgentClaude: r})
		w.store.during = func() { w.now = w.now.Add(4 * time.Second) }
		if !w.g.Admits(summoned("a", v1alpha1.SummonPriorityBulk)) {
			t.Fatal("held on a window that reset during the read")
		}
		if l := w.logs.lines[0]; !strings.Contains(l, `"reason"="window-reset"`) || !strings.Contains(l, `"readingAge"="2m4s"`) {
			t.Fatalf("log %q", l)
		}
	})
	t.Run("a reading that crosses the max age during the read admits", func(t *testing.T) {
		r := reading(FiveHourMinutes, "99")
		r.ReadAt = t0.Add(-15*time.Minute + time.Second)
		w := newGate(installLines, map[string]Reading{protocol.AgentClaude: r})
		w.store.during = func() { w.now = w.now.Add(4 * time.Second) }
		if !w.g.Admits(summoned("a", v1alpha1.SummonPriorityBulk)) {
			t.Fatal("held on a reading that went stale during the read")
		}
		if l := w.logs.lines[0]; !strings.Contains(l, `"reason"="stale"`) || !strings.Contains(l, `"readingAge"="15m3s"`) {
			t.Fatalf("log %q", l)
		}
	})
	t.Run("the reuse is measured as before, from before the read", func(t *testing.T) {
		w := newGate(installLines, claudeAt(FiveHourMinutes, "81"))
		w.store.during = func() { w.now = w.now.Add(4 * time.Second) }
		w.g.Admits(summoned("a", v1alpha1.SummonPriorityBulk))
		w.store.during = nil
		w.now = t0.Add(29 * time.Second)
		w.g.Admits(summoned("a", v1alpha1.SummonPriorityBulk))
		if w.store.reads != 1 {
			t.Fatalf("29 s after the read began: %d reads, want 1", w.store.reads)
		}
		w.now = t0.Add(DefaultReuse)
		w.g.Admits(summoned("a", v1alpha1.SummonPriorityBulk))
		if w.store.reads != 2 {
			t.Fatalf("30 s after the read began (26 s after it ended): %d reads, want 2", w.store.reads)
		}
	})
}

// The review's gaps: the log lines of a stale reading and of a missing window
// name their reason, and a cached reading that passes the max age inside the
// 30-second reuse admits without another read.
func TestGateLogsStaleAndMissingWindow(t *testing.T) {
	stale := reading(FiveHourMinutes, "99")
	stale.ReadAt = t0.Add(-15*time.Minute - time.Second)
	w := newGate(installLines, map[string]Reading{protocol.AgentClaude: stale})
	if !w.g.Admits(summoned("stale-one", v1alpha1.SummonPriorityBulk)) {
		t.Fatal("held on a stale reading")
	}
	for _, want := range []string{`"msg"="quota gate admitted a summoned session"`, `"session"="stale-one"`, `"reason"="stale"`,
		`"fiveHourUsed"="99"`, `"fiveHourLine"="80"`, `"readingAge"="15m1s"`} {
		if !strings.Contains(w.logs.lines[0], want) {
			t.Errorf("stale line %q lacks %s", w.logs.lines[0], want)
		}
	}
	w = newGate(installLines, claudeAt(SevenDayMinutes, "99"))
	if !w.g.Admits(summoned("no-window", v1alpha1.SummonPriorityBulk)) {
		t.Fatal("held with no 5-hour window")
	}
	for _, want := range []string{`"msg"="quota gate admitted a summoned session"`, `"reason"="window-missing"`,
		`"fiveHourUsed"="none"`, `"fiveHourLine"="80"`, `"sevenDayUsed"="99"`, `"sevenDayLine"="off"`, `"readingAge"="2m0s"`} {
		if !strings.Contains(w.logs.lines[0], want) {
			t.Errorf("missing-window line %q lacks %s", w.logs.lines[0], want)
		}
	}
}

func TestGateCachedReadingGoesStaleInsideTheReuse(t *testing.T) {
	r := reading(FiveHourMinutes, "99")
	r.ReadAt = t0.Add(-15*time.Minute + 10*time.Second)
	w := newGate(installLines, map[string]Reading{protocol.AgentClaude: r})
	if w.g.Admits(summoned("a", v1alpha1.SummonPriorityBulk)) {
		t.Fatal("bulk at 99 %, a fresh reading, admitted")
	}
	w.now = t0.Add(20 * time.Second)
	if !w.g.Admits(summoned("a", v1alpha1.SummonPriorityBulk)) {
		t.Fatal("the cached reading passed the max age and still held")
	}
	if w.store.reads != 1 || !strings.Contains(w.logs.lines[1], `"reason"="stale"`) || !strings.Contains(w.logs.lines[1], `"readingAge"="15m10s"`) {
		t.Fatalf("%d reads, logs %q", w.store.reads, w.logs.lines)
	}
}
