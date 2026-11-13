package summonquota

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/go-logr/logr"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

const (
	// DefaultReadTimeout bounds one read of the readings. A read that runs
	// past it is a read error, and the session is admitted.
	DefaultReadTimeout = 5 * time.Second
	// DefaultReuse is how long one read's result, the readings or the read
	// failure, answers every decision: half the leader's 60-second admission
	// interval. The seam (controller.Reconciler.SummonQuota) gets no pass id
	// or pass time, so this is the bound: at most one read per 30 seconds,
	// which is one per pass whenever a pass takes less (the coordinator's
	// answer to op783-q-1, gap A; story 743b can make it exact when it
	// reshapes the seam).
	DefaultReuse = 30 * time.Second
)

// ReadFunc returns each provider's newest reading, keyed by agent ("claude",
// "codex"); a provider with no reading is absent. It must honour ctx.
type ReadFunc func(ctx context.Context) (map[string]Reading, error)

// Gate is the leader's quota hook: its Admits is Reconciler.SummonQuota.
// The admission pass calls it last in the hold switch, only for a Queued
// session with spec.caller whose lane and pool are free.
type Gate struct {
	Lines Lines
	Read  ReadFunc
	// Now is the gate's clock; nil is time.Now.
	Now func() time.Time
	Log logr.Logger
	// ReadTimeout and Reuse are zero for DefaultReadTimeout and DefaultReuse.
	ReadTimeout time.Duration
	Reuse       time.Duration

	mu sync.Mutex
	// held says at, readings and failure hold the last read's result.
	held     bool
	at       time.Time
	readings map[string]Reading
	failure  Why
	// staleAt is, per provider, the newest readAt the gate has judged
	// stale. A reading at or before it stays stale whatever the clock says
	// later; a strictly newer one is judged normally (story 783 fix round 2,
	// coordinator rulings in PR 1101 comment 6839647832). It is memory only:
	// each operator process starts with none, so a restart or a new leader
	// knows nothing of the clock before it.
	staleAt map[string]time.Time
}

// Admits reports whether the quota lets s start now. A session without
// spec.caller, an urgent session and a priority with no line set read
// nothing; every decision on a normal or bulk session logs one line.
//
// The decision's clock is read after the reading returns, past the read and
// any wait for the lock, so staleness and a window's reset are judged at the
// time the session would start (ruling R2 on PR 1101). The 30-second reuse
// keeps the time taken before the read.
//
// A reading Decide finds stale marks its provider: from then on that reading,
// and any older one, reaches Decide as Missing WhyStale, so a clock that goes
// back cannot make it fresh again (story 783 fix round 2). A caller that took
// the same reading before the mark was written decides on its own clock.
func (g *Gate) Admits(s *v1alpha1.AgentSession) bool {
	if s == nil || s.Spec.Caller == "" {
		return true
	}
	var p v1alpha1.SummonPriority
	if s.Spec.Summon != nil {
		p = s.Spec.Summon.Priority
	}
	agent := string(s.Spec.Agent)
	now := g.now()
	r := Reading{Missing: WhyNoReading}
	if g.Lines.For(p) {
		r = g.reading(agent, now)
		now = g.now()
	}
	d := Decide(p, r, g.Lines, now)
	if d.HaveReading && d.Reason == WhyStale {
		g.markStale(agent, r.ReadAt)
	}
	if p != v1alpha1.SummonPriorityUrgent {
		g.log(s, d)
	}
	return d.Admit
}

func (g *Gate) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

// reading is agent's newest reading from the last read, read again when that
// read is Reuse old or the clock went back past it. The lock is held through
// the read, so two callers never read at once. A reading at or before the
// provider's stale mark is returned as Missing WhyStale.
func (g *Gate) reading(agent string, now time.Time) Reading {
	g.mu.Lock()
	defer g.mu.Unlock()
	reuse := g.Reuse
	if reuse <= 0 {
		reuse = DefaultReuse
	}
	if since := now.Sub(g.at); !g.held || since < 0 || since >= reuse {
		g.readings, g.failure = g.readOnce()
		g.held, g.at = true, now
	}
	if g.failure != "" {
		return Reading{Missing: g.failure}
	}
	r, ok := g.readings[agent]
	if !ok || r.ReadAt.IsZero() {
		return Reading{Missing: WhyNoReading}
	}
	if mark, ok := g.staleAt[agent]; ok && !r.ReadAt.After(mark) {
		return Reading{Missing: WhyStale}
	}
	return r
}

// markStale records readAt as agent's stale mark unless the mark is already
// at or after it.
func (g *Gate) markStale(agent string, readAt time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if mark, ok := g.staleAt[agent]; ok && !readAt.After(mark) {
		return
	}
	if g.staleAt == nil {
		g.staleAt = map[string]time.Time{}
	}
	g.staleAt[agent] = readAt
}

// readOnce is one read bounded by ReadTimeout. The seam carries no context,
// so the read gets its own.
func (g *Gate) readOnce() (map[string]Reading, Why) {
	if g.Read == nil {
		return nil, WhyReadError
	}
	timeout := g.ReadTimeout
	if timeout <= 0 {
		timeout = DefaultReadTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	readings, err := g.Read(ctx)
	switch {
	case err != nil && ctx.Err() != nil:
		return nil, WhyReadTimeout
	case err != nil:
		return nil, WhyReadError
	}
	return readings, ""
}

// log writes the decision's one line. Every value is the operator's own or a
// checked reading's number: the session's name and lane, its priority, the
// fixed reason, each window's canonical percent and line, and the age. A
// negative age (a reading from the future) is written exactly, so it never
// reads as zero.
func (g *Gate) log(s *v1alpha1.AgentSession, d Decision) {
	msg := "quota gate admitted a summoned session"
	if !d.Admit {
		msg = "quota gate held a summoned session"
	}
	age := "none"
	switch {
	case d.HaveReading && d.Age < 0:
		age = d.Age.String()
	case d.HaveReading:
		age = d.Age.Truncate(time.Second).String()
	}
	g.Log.Info(msg, "session", s.Name, "lane", string(s.Spec.Lane), "priority", string(d.Priority), "reason", string(d.Reason),
		"fiveHourUsed", used(d.FiveHour), "fiveHourLine", line(d.FiveHour), "sevenDayUsed", used(d.SevenDay), "sevenDayLine", line(d.SevenDay),
		"readingAge", age)
}

func used(w Window) string {
	if w.Used == "" {
		return "none"
	}
	return w.Used
}

func line(w Window) string {
	if w.Line <= 0 {
		return "off"
	}
	return strconv.Itoa(w.Line)
}
