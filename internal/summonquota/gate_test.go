package summonquota

// The gate's proofs of story 783, with story 790's read bound: the one read
// per admission pass, the bounded read, the read that never happens, the
// decision the seam returns, and the one log line per decision. A fake read
// and a clock the test moves; the timeout test waits on the read's own
// context, never on a sleep.

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
	ctx   context.Context
}

// admits is the gate's decision on s, under the world's context.
func (w *gateWorld) admits(s *v1alpha1.AgentSession) bool { return w.g.DecideSession(w.ctx, s).Admit }

func newGate(lines Lines, readings map[string]Reading) *gateWorld {
	w := &gateWorld{now: t0, store: &fakeStore{readings: readings}, logs: &logLines{}, ctx: context.Background()}
	w.g = &Gate{Lines: lines, Read: w.store.read, Now: func() time.Time { return w.now }, Log: w.logs.logger()}
	return w
}

func claudeAt(windows ...any) map[string]Reading {
	return map[string]Reading{protocol.AgentClaude: reading(windows...)}
}

// Story 790 replaces slice a's 30-second reuse with an exact bound: every
// decision under one admission pass's context (WithPass) shares one read,
// made by the first decision that needs it, however long the pass takes; the
// next pass reads again at once; a decision outside a pass is a pass of its
// own.
func TestGateReadsAtMostOncePerPass(t *testing.T) {
	w := newGate(installLines, claudeAt(FiveHourMinutes, "81"))
	w.ctx = WithPass(context.Background())
	for _, s := range []*v1alpha1.AgentSession{summoned("a", v1alpha1.SummonPriorityBulk), summoned("b", v1alpha1.SummonPriorityBulk), summoned("c", v1alpha1.SummonPriorityNormal), summoned("d", v1alpha1.SummonPriorityBulk)} {
		w.admits(s)
	}
	if w.store.reads != 1 {
		t.Fatalf("four Queued sessions in one pass: %d reads, want 1", w.store.reads)
	}
	w.now = t0.Add(time.Minute)
	w.admits(summoned("e", v1alpha1.SummonPriorityBulk))
	if w.store.reads != 1 {
		t.Fatalf("a minute later in the same pass: %d reads, want 1", w.store.reads)
	}
	w.now = t0.Add(time.Minute + time.Second)
	w.ctx = WithPass(context.Background())
	w.admits(summoned("a", v1alpha1.SummonPriorityBulk))
	w.admits(summoned("b", v1alpha1.SummonPriorityBulk))
	if w.store.reads != 2 {
		t.Fatalf("the next pass, a second later: %d reads, want 2", w.store.reads)
	}
	// A clock that went back is just another pass.
	w.now = t0.Add(-time.Minute)
	w.ctx = WithPass(context.Background())
	w.admits(summoned("a", v1alpha1.SummonPriorityBulk))
	if w.store.reads != 3 {
		t.Fatalf("clock back a minute, a new pass: %d reads, want 3", w.store.reads)
	}
	w.ctx = context.Background()
	w.admits(summoned("a", v1alpha1.SummonPriorityBulk))
	w.admits(summoned("b", v1alpha1.SummonPriorityBulk))
	if w.store.reads != 5 {
		t.Fatalf("two decisions outside a pass: %d reads, want 5", w.store.reads)
	}
}

// The read runs under the pass's context, bounded by the read timeout: a
// value the pass carries reaches the store, and so does a deadline no later
// than ReadTimeout. A pass whose context is already done admits.
func TestGateReadsUnderThePassContext(t *testing.T) {
	type key struct{}
	w := newGate(installLines, claudeAt(FiveHourMinutes, "81"))
	var got context.Context
	w.g.Read = func(ctx context.Context) (map[string]Reading, error) {
		got = ctx
		return w.store.read(ctx)
	}
	w.ctx = WithPass(context.WithValue(context.Background(), key{}, "pass"))
	if w.admits(summoned("a", v1alpha1.SummonPriorityBulk)) {
		t.Fatal("bulk at 81 %, line 80, admitted")
	}
	if got == nil || got.Value(key{}) != "pass" {
		t.Fatal("the read did not run under the pass's context")
	}
	if dl, ok := got.Deadline(); !ok || dl.After(time.Now().Add(DefaultReadTimeout)) {
		t.Fatalf("the read's deadline %v, %v", dl, ok)
	}
	done, cancel := context.WithCancel(context.Background())
	cancel()
	w = newGate(installLines, claudeAt(FiveHourMinutes, "81"))
	w.store.block = true
	w.ctx = WithPass(done)
	if !w.admits(summoned("a", v1alpha1.SummonPriorityBulk)) || w.store.reads != 1 || !strings.Contains(w.logs.lines[0], `"reason"="read-error"`) {
		t.Fatalf("a pass already done: %d reads, logs %q", w.store.reads, w.logs.lines)
	}
}

// Story 790's seam: the decision names what it read. A hold names its
// window, the percentage, the line and the reading's readAt; an admit on an
// unusable reading names why, with no readAt.
func TestGateDecisionCarriesWhatItRead(t *testing.T) {
	weekly := installLines
	weekly.BulkSevenDay = 90
	r := reading(FiveHourMinutes, "81", SevenDayMinutes, "91")
	w := newGate(weekly, map[string]Reading{protocol.AgentClaude: r})
	d := w.g.DecideSession(w.ctx, summoned("a", v1alpha1.SummonPriorityBulk))
	if d.Admit || d.Reason != WhyFiveHourPast || d.FiveHour.Used != "81" || d.FiveHour.Line != 80 || !d.ReadAt.Equal(r.ReadAt) || d.ReadAt.IsZero() {
		t.Fatalf("5-hour hold: %+v", d)
	}
	r = reading(FiveHourMinutes, "10", SevenDayMinutes, "91")
	w = newGate(weekly, map[string]Reading{protocol.AgentClaude: r})
	d = w.g.DecideSession(w.ctx, summoned("a", v1alpha1.SummonPriorityBulk))
	if d.Admit || d.Reason != WhySevenDayPast || d.SevenDay.Used != "91" || d.SevenDay.Line != 90 || !d.ReadAt.Equal(r.ReadAt) {
		t.Fatalf("7-day hold: %+v", d)
	}
	stale := reading(FiveHourMinutes, "99")
	stale.ReadAt = t0.Add(-16 * time.Minute)
	w = newGate(weekly, map[string]Reading{protocol.AgentClaude: stale})
	d = w.g.DecideSession(w.ctx, summoned("a", v1alpha1.SummonPriorityBulk))
	if !d.Admit || d.Reason != WhyStale || !d.ReadAt.Equal(stale.ReadAt) {
		t.Fatalf("stale admit: %+v", d)
	}
	w = newGate(weekly, nil)
	w.store.err = errors.New("store unavailable")
	d = w.g.DecideSession(w.ctx, summoned("a", v1alpha1.SummonPriorityBulk))
	if !d.Admit || d.Reason != WhyReadError || !d.ReadAt.IsZero() || d.HaveReading {
		t.Fatalf("read-error admit: %+v", d)
	}
	if d := w.g.DecideSession(w.ctx, summoned("u", v1alpha1.SummonPriorityUrgent)); !d.Admit || d.Reason != WhyUrgent {
		t.Fatalf("urgent: %+v", d)
	}
}

// Proof 5: with every line unset nothing is read and everything admits.
// (main.go does not even build the gate then: TestSummonQuotaHookOffWithoutLines.)
func TestGateWithEveryLineUnsetReadsNothing(t *testing.T) {
	w := newGate(Lines{MaxAge: time.Minute}, claudeAt(FiveHourMinutes, "100", SevenDayMinutes, "100"))
	for _, p := range []v1alpha1.SummonPriority{v1alpha1.SummonPriorityUrgent, v1alpha1.SummonPriorityNormal, v1alpha1.SummonPriorityBulk} {
		if !w.admits(summoned("a", p)) {
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
	if !w.admits(summoned("u", v1alpha1.SummonPriorityUrgent)) || !w.admits(summoned("n", v1alpha1.SummonPriorityNormal)) || w.store.reads != 0 {
		t.Fatalf("urgent or normal with only the bulk line set: %d reads", w.store.reads)
	}
	if w.admits(summoned("b", v1alpha1.SummonPriorityBulk)) || w.store.reads != 1 {
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
	if !w.admits(s) || w.store.reads != 0 || len(w.logs.lines) != 0 {
		t.Fatalf("a session without spec.caller: %d reads, logs %q", w.store.reads, w.logs.lines)
	}
}

// Proof 4 at the seam: a read error, a timeout and a missing reading each
// admit and log why; a failed read answers the rest of its pass like a good
// one.
func TestGateReadFailuresAdmitAndLogWhy(t *testing.T) {
	t.Run("read error", func(t *testing.T) {
		w := newGate(installLines, nil)
		w.store.err = errors.New("store unavailable")
		w.ctx = WithPass(context.Background())
		for _, name := range []string{"a", "b"} {
			if !w.admits(summoned(name, v1alpha1.SummonPriorityBulk)) {
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
		if !w.admits(summoned("a", v1alpha1.SummonPriorityNormal)) {
			t.Fatal("held on a read timeout")
		}
		if w.store.reads != 1 || len(w.logs.lines) != 1 || !strings.Contains(w.logs.lines[0], `"reason"="read-timeout"`) {
			t.Fatalf("%d reads, logs %q", w.store.reads, w.logs.lines)
		}
	})
	t.Run("no reading for the agent", func(t *testing.T) {
		w := newGate(installLines, map[string]Reading{protocol.AgentCodex: reading(FiveHourMinutes, "100")})
		if !w.admits(summoned("a", v1alpha1.SummonPriorityBulk)) || !strings.Contains(w.logs.lines[0], `"reason"="no-reading"`) {
			t.Fatalf("logs %q", w.logs.lines)
		}
	})
	t.Run("nil read", func(t *testing.T) {
		w := newGate(installLines, nil)
		w.g.Read = nil
		if !w.admits(summoned("a", v1alpha1.SummonPriorityBulk)) || !strings.Contains(w.logs.lines[0], `"reason"="read-error"`) {
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
	if w.admits(summoned("held-one", v1alpha1.SummonPriorityBulk)) {
		t.Fatal("bulk past both lines admitted")
	}
	if !w.admits(summoned("normal-one", v1alpha1.SummonPriorityNormal)) {
		t.Fatal("normal at 81 %% held")
	}
	w.admits(summoned("urgent-one", v1alpha1.SummonPriorityUrgent))
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
	if w.admits(summoned("before", v1alpha1.SummonPriorityBulk)) || w.store.reads != 1 {
		t.Fatalf("bulk at 99 %%, line 80, before the clock went back: admitted, or %d reads", w.store.reads)
	}
	w.now = t0.Add(-time.Hour)
	if !w.admits(summoned("after", v1alpha1.SummonPriorityBulk)) {
		t.Fatal("the clock went back an hour behind a 99 % reading and the session was held")
	}
	if w.store.reads != 2 {
		t.Fatalf("the clock went back: %d reads, want a new read (2)", w.store.reads)
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
	if !w.admits(summoned("ahead", v1alpha1.SummonPriorityNormal)) {
		t.Fatal("a reading half a second in the future held")
	}
	if l := w.logs.lines[0]; !strings.Contains(l, `"reason"="reading-from-future"`) || !strings.Contains(l, `"readingAge"="-500ms"`) {
		t.Fatalf("log %q", l)
	}
	r.ReadAt = t0
	w = newGate(installLines, map[string]Reading{protocol.AgentClaude: r})
	if w.admits(summoned("at", v1alpha1.SummonPriorityNormal)) {
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
		if !w.admits(summoned("a", v1alpha1.SummonPriorityBulk)) {
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
		if !w.admits(summoned("a", v1alpha1.SummonPriorityBulk)) {
			t.Fatal("held on a reading that went stale during the read")
		}
		if l := w.logs.lines[0]; !strings.Contains(l, `"reason"="stale"`) || !strings.Contains(l, `"readingAge"="15m3s"`) {
			t.Fatalf("log %q", l)
		}
	})
	t.Run("in one pass each decision is on its own clock after the one read", func(t *testing.T) {
		r := reading(FiveHourMinutes, "99")
		r.ReadAt = t0.Add(-15*time.Minute + 10*time.Second)
		w := newGate(installLines, map[string]Reading{protocol.AgentClaude: r})
		w.ctx = WithPass(context.Background())
		w.store.during = func() { w.now = w.now.Add(4 * time.Second) }
		if w.admits(summoned("a", v1alpha1.SummonPriorityBulk)) {
			t.Fatal("a 99 % reading 14m54s old after the read held nothing")
		}
		w.store.during = nil
		w.now = t0.Add(11 * time.Second)
		if !w.admits(summoned("b", v1alpha1.SummonPriorityBulk)) {
			t.Fatal("later in the pass the reading is 15m1s old and still held")
		}
		if w.store.reads != 1 || !strings.Contains(w.logs.lines[1], `"readingAge"="15m1s"`) {
			t.Fatalf("%d reads, logs %q", w.store.reads, w.logs.lines)
		}
	})
}

// The review's gaps: the log lines of a stale reading and of a missing window
// name their reason, and a reading that passes the max age later in its pass
// admits without another read.
func TestGateLogsStaleAndMissingWindow(t *testing.T) {
	stale := reading(FiveHourMinutes, "99")
	stale.ReadAt = t0.Add(-15*time.Minute - time.Second)
	w := newGate(installLines, map[string]Reading{protocol.AgentClaude: stale})
	if !w.admits(summoned("stale-one", v1alpha1.SummonPriorityBulk)) {
		t.Fatal("held on a stale reading")
	}
	for _, want := range []string{`"msg"="quota gate admitted a summoned session"`, `"session"="stale-one"`, `"reason"="stale"`,
		`"fiveHourUsed"="99"`, `"fiveHourLine"="80"`, `"readingAge"="15m1s"`} {
		if !strings.Contains(w.logs.lines[0], want) {
			t.Errorf("stale line %q lacks %s", w.logs.lines[0], want)
		}
	}
	w = newGate(installLines, claudeAt(SevenDayMinutes, "99"))
	if !w.admits(summoned("no-window", v1alpha1.SummonPriorityBulk)) {
		t.Fatal("held with no 5-hour window")
	}
	for _, want := range []string{`"msg"="quota gate admitted a summoned session"`, `"reason"="window-missing"`,
		`"fiveHourUsed"="none"`, `"fiveHourLine"="80"`, `"sevenDayUsed"="99"`, `"sevenDayLine"="off"`, `"readingAge"="2m0s"`} {
		if !strings.Contains(w.logs.lines[0], want) {
			t.Errorf("missing-window line %q lacks %s", w.logs.lines[0], want)
		}
	}
}

func TestGateReadingGoesStaleLaterInItsPass(t *testing.T) {
	r := reading(FiveHourMinutes, "99")
	r.ReadAt = t0.Add(-15*time.Minute + 10*time.Second)
	w := newGate(installLines, map[string]Reading{protocol.AgentClaude: r})
	w.ctx = WithPass(context.Background())
	if w.admits(summoned("a", v1alpha1.SummonPriorityBulk)) {
		t.Fatal("bulk at 99 %, a fresh reading, admitted")
	}
	w.now = t0.Add(20 * time.Second)
	if !w.admits(summoned("a", v1alpha1.SummonPriorityBulk)) {
		t.Fatal("the pass's reading passed the max age and still held")
	}
	if w.store.reads != 1 || !strings.Contains(w.logs.lines[1], `"reason"="stale"`) || !strings.Contains(w.logs.lines[1], `"readingAge"="15m10s"`) {
		t.Fatalf("%d reads, logs %q", w.store.reads, w.logs.lines)
	}
}

// Story 783 fix round 2 (coordinator rulings in PR 1101 comment 6839647832):
// a reading the gate has once judged stale never holds again, whatever the
// clock says later. The clock first sees a 99 % reading 16 minutes old, then
// goes back to a time still after the reading, where the same data would
// look a minute old. It admits as stale on that call, later in its pass and
// after a later read; an older reading stays stale too, even when the clock
// goes back before its time; a strictly newer one is judged normally.
func TestGateReadingOnceJudgedStaleNeverHoldsAgain(t *testing.T) {
	watermark := t0.Add(-16 * time.Minute)
	at := func(readAt time.Time) map[string]Reading {
		r := reading(FiveHourMinutes, "99")
		r.ReadAt = readAt
		return map[string]Reading{protocol.AgentClaude: r}
	}
	w := newGate(installLines, at(watermark))
	// step decides one bulk session at now, in a new pass when pass is
	// set, else later in the last one.
	step := func(name string, pass bool, now time.Time, reads int, admit bool, reason Why) {
		t.Helper()
		if pass {
			w.ctx = WithPass(context.Background())
		}
		w.now = now
		n := len(w.logs.lines)
		if got := w.admits(summoned(name, v1alpha1.SummonPriorityBulk)); got != admit {
			t.Fatalf("%s: admit %v, want %v; logs %q", name, got, admit, w.logs.lines[n:])
		}
		if w.store.reads != reads {
			t.Fatalf("%s: %d reads, want %d", name, w.store.reads, reads)
		}
		if len(w.logs.lines) != n+1 || !strings.Contains(w.logs.lines[n], `"reason"="`+string(reason)+`"`) {
			t.Fatalf("%s: logs %q, want one line with reason %s", name, w.logs.lines[n:], reason)
		}
	}
	step("first-seen-stale", true, t0, 1, true, WhyStale)
	// The clock goes back 15 minutes: a new read, the same data, a minute old
	// on this clock. Before the fix it held with five-hour-past-line.
	back := t0.Add(-15 * time.Minute)
	step("rollback-call", true, back, 2, true, WhyStale)
	// The marked reading reaches Decide as Missing: no age, no percent.
	if l := w.logs.lines[1]; !strings.Contains(l, `"readingAge"="none"`) || !strings.Contains(l, `"fiveHourUsed"="none"`) || !strings.Contains(l, `"fiveHourLine"="80"`) {
		t.Fatalf("rollback-call log %q", l)
	}
	step("same-pass", false, back.Add(10*time.Second), 2, true, WhyStale)
	step("later-read", true, back.Add(30*time.Second), 3, true, WhyStale)
	// The clock goes back before the reading's own time: still stale, not
	// reading-from-future.
	step("clock-before-the-reading", true, watermark.Add(-time.Minute), 4, true, WhyStale)
	// An older reading, fresh on this clock, is stale too.
	w.store.readings = at(watermark.Add(-time.Minute))
	step("older-reading", true, watermark.Add(-30*time.Second), 5, true, WhyStale)
	// A reading one nanosecond newer than the mark is judged normally: fresh
	// and past the line, it holds, and so does the next decision of its pass.
	w.store.readings = at(watermark.Add(time.Nanosecond))
	step("newer-reading", true, watermark.Add(time.Minute), 6, false, WhyFiveHourPast)
	step("newer-reading-same-pass", false, watermark.Add(time.Minute+10*time.Second), 6, false, WhyFiveHourPast)
	// A newer reading from the future keeps its own reason.
	w.store.readings = at(watermark.Add(5 * time.Minute))
	step("future-reading", true, watermark.Add(2*time.Minute), 7, true, WhyFromFuture)
}

// The mark is per provider: a Codex reading judged stale does not make a
// Claude reading with the same time stale.
func TestGateStaleMarkIsPerProvider(t *testing.T) {
	stale := t0.Add(-16 * time.Minute)
	claude, codex := reading(FiveHourMinutes, "99"), reading(FiveHourMinutes, "99")
	claude.ReadAt, codex.ReadAt = stale, stale
	w := newGate(installLines, map[string]Reading{protocol.AgentCodex: codex})
	cx := summoned("codex-one", v1alpha1.SummonPriorityBulk)
	cx.Spec.Agent = v1alpha1.AgentCodex
	if !w.admits(cx) || !strings.Contains(w.logs.lines[0], `"reason"="stale"`) {
		t.Fatalf("a stale Codex reading: logs %q", w.logs.lines)
	}
	w.store.readings = map[string]Reading{protocol.AgentCodex: codex, protocol.AgentClaude: claude}
	w.now = t0.Add(-15 * time.Minute)
	w.ctx = WithPass(context.Background())
	if w.admits(summoned("claude-one", v1alpha1.SummonPriorityBulk)) || !strings.Contains(w.logs.lines[1], `"reason"="five-hour-past-line"`) {
		t.Fatalf("a fresh Claude reading at 99 %% was not held after a Codex reading went stale: logs %q", w.logs.lines)
	}
	if !w.admits(cx) || !strings.Contains(w.logs.lines[2], `"reason"="stale"`) {
		t.Fatalf("the Codex reading held again after it was judged stale: logs %q", w.logs.lines)
	}
	if w.store.reads != 2 {
		t.Fatalf("%d reads, want 2", w.store.reads)
	}
}

// With every line unset nothing is read, however the clock moves, so no
// reading is ever judged stale.
func TestGateWithEveryLineUnsetReadsNothingAcrossARollback(t *testing.T) {
	stale := reading(FiveHourMinutes, "100")
	stale.ReadAt = t0.Add(-16 * time.Minute)
	w := newGate(Lines{MaxAge: 15 * time.Minute}, map[string]Reading{protocol.AgentClaude: stale})
	for _, now := range []time.Time{t0, t0.Add(-15 * time.Minute), t0.Add(time.Hour)} {
		w.now = now
		for _, p := range []v1alpha1.SummonPriority{v1alpha1.SummonPriorityUrgent, v1alpha1.SummonPriorityNormal, v1alpha1.SummonPriorityBulk} {
			if !w.admits(summoned("a", p)) {
				t.Fatalf("%s held with no line set at %s", p, now)
			}
		}
	}
	if w.store.reads != 0 {
		t.Fatalf("%d reads with every line unset", w.store.reads)
	}
}
