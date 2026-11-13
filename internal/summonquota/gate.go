package summonquota

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/go-logr/logr"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// DefaultReadTimeout bounds one read of the readings. A read that runs past
// it is a read timeout, and the session is admitted.
const DefaultReadTimeout = 5 * time.Second

// ReadFunc returns each provider's newest reading, keyed by agent ("claude",
// "codex"); a provider with no reading is absent. It must honour ctx.
type ReadFunc func(ctx context.Context) (map[string]Reading, error)

// Gate is the leader's quota hook: its DecideSession is
// Reconciler.SummonQuota. The admission pass calls it last in the hold
// switch, only for a Queued session with spec.caller whose lane and pool are
// free, under the pass's context (WithPass).
type Gate struct {
	Lines Lines
	Read  ReadFunc
	// Now is the gate's clock; nil is time.Now.
	Now func() time.Time
	Log logr.Logger
	// ReadTimeout is zero for DefaultReadTimeout.
	ReadTimeout time.Duration

	mu sync.Mutex
	// staleAt is, per provider, the newest readAt the gate has judged
	// stale. A reading at or before it stays stale whatever the clock says
	// later; a strictly newer one is judged normally (story 783 fix round 2,
	// coordinator rulings in PR 1101 comment 6839647832). It is memory only:
	// each operator process starts with none, so a restart or a new leader
	// knows nothing of the clock before it.
	staleAt map[string]time.Time
}

// passKey is the context key of one admission pass's read.
type passKey struct{}

// passRead is one admission pass's read of the readings, made at most once:
// by the first decision of the pass that needs a reading. Every later
// decision of the pass is answered from it, the readings or the failure.
type passRead struct {
	mu       sync.Mutex
	done     bool
	readings map[string]Reading
	failure  Why
}

// WithPass returns ctx marked as one admission pass (story 790): every
// DecideSession under it shares one read of the readings, so the gate reads
// at most once per pass however many Queued sessions reach it and however
// long the pass takes, and the next pass reads again. The pass's attempts
// share it too. A decision whose context carries no pass reads for itself.
// The admission pass marks its context once, before its first attempt.
func WithPass(ctx context.Context) context.Context {
	return context.WithValue(ctx, passKey{}, &passRead{})
}

// DecideSession is the gate's decision on s now, as the admission pass
// needs it: admit or hold and what it read (Decision). A session without
// spec.caller is admitted with an empty reason; an urgent session and a
// priority with no line set read nothing; every decision on a normal or bulk
// session logs one line.
//
// The read runs under ctx, the pass's context, bounded by ReadTimeout. The
// decision's clock is read after the reading returns, past the read and any
// wait for the pass's lock, so staleness and a window's reset are judged at
// the time the session would start (ruling R2 on PR 1101), and each later
// decision of the pass on its own clock.
//
// A reading Decide finds stale marks its provider: from then on that reading,
// and any older one, reaches Decide as Missing WhyStale, so a clock that goes
// back cannot make it fresh again (story 783 fix round 2). A caller that took
// the same reading before the mark was written decides on its own clock.
func (g *Gate) DecideSession(ctx context.Context, s *v1alpha1.AgentSession) Decision {
	if s == nil || s.Spec.Caller == "" {
		return Decision{Admit: true}
	}
	var p v1alpha1.SummonPriority
	if s.Spec.Summon != nil {
		p = s.Spec.Summon.Priority
	}
	agent := string(s.Spec.Agent)
	r := Reading{Missing: WhyNoReading}
	if g.Lines.For(p) {
		r = g.reading(ctx, agent)
	}
	d := Decide(p, r, g.Lines, g.now())
	if d.HaveReading && d.Reason == WhyStale {
		g.markStale(agent, r.ReadAt)
	}
	if p != v1alpha1.SummonPriorityUrgent {
		g.log(s, d)
	}
	return d
}

func (g *Gate) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

// reading is agent's newest reading from the pass's one read, made now if
// the pass has none yet. The pass's lock is held through the read, so two
// decisions of one pass never read twice. A reading at or before the
// provider's stale mark is returned as Missing WhyStale.
func (g *Gate) reading(ctx context.Context, agent string) Reading {
	pr, _ := ctx.Value(passKey{}).(*passRead)
	if pr == nil {
		pr = &passRead{}
	}
	pr.mu.Lock()
	if !pr.done {
		pr.readings, pr.failure = g.readOnce(ctx)
		pr.done = true
	}
	readings, failure := pr.readings, pr.failure
	pr.mu.Unlock()
	if failure != "" {
		return Reading{Missing: failure}
	}
	r, ok := readings[agent]
	if !ok || r.ReadAt.IsZero() {
		return Reading{Missing: WhyNoReading}
	}
	g.mu.Lock()
	mark, marked := g.staleAt[agent]
	g.mu.Unlock()
	if marked && !r.ReadAt.After(mark) {
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

// readOnce is one read under the pass's context, bounded by ReadTimeout. A
// read cut off by that bound is a timeout; any other failure, a pass context
// that is done among them, is a read error. Both admit.
func (g *Gate) readOnce(ctx context.Context) (map[string]Reading, Why) {
	if g.Read == nil {
		return nil, WhyReadError
	}
	timeout := g.ReadTimeout
	if timeout <= 0 {
		timeout = DefaultReadTimeout
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	readings, err := g.Read(rctx)
	switch {
	case err != nil && errors.Is(rctx.Err(), context.DeadlineExceeded) && ctx.Err() == nil:
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
