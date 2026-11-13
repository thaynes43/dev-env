package keeper

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/pages"
)

// Story 792 fix round 1: coordinator rulings in PR 1126 comment 6844882457,
// not rulings by Tom. Every send goes to the fake Pushover endpoint on the fake
// clock; no test here reaches a cluster.

// summonDigestClockLine is the one fixed line a tick logs while the ledger's
// digest times are ahead of the keeper's clock (ruling 2).
const summonDigestClockLine = "summon digest ledger time is ahead of the keeper's clock"

// digestRecordLines reads a digest record's line count from its body.
func digestRecordLines(t *testing.T, r pages.Record) int {
	t.Helper()
	var n, left int
	if _, err := fmt.Sscanf(r.Body, "digest of %d lines, %d left out", &n, &left); err != nil {
		if _, err := fmt.Sscanf(r.Body, "digest of %d line, %d left out", &n, &left); err != nil {
			t.Fatalf("record body %q: %v", r.Body, err)
		}
	}
	return n
}

// Ruling 1: an early digest needs at least one hour since the last digest, or
// before the first one since the watermark, to the second. Eight new lines
// every tick send one digest, and the next no sooner than an hour after it.
// Fewer than eight still wait 24 hours.
func TestSummonDigestEarlyFloor(t *testing.T) {
	f := newSummonFix(t)
	since := f.start()
	n := 0
	add := func(k int) {
		for i := 0; i < k; i++ {
			n++
			f.add(f.quiet(fmt.Sprintf("wo-cigar-curate-%03d", n)))
		}
	}
	for _, at := range []time.Duration{2 * time.Minute, 10 * time.Minute, 30 * time.Minute, time.Hour - time.Second} {
		f.clock.SetTime(since.Add(at))
		add(8)
		f.tick()
		if len(f.digests()) != 0 {
			t.Fatalf("an early digest %s after the watermark", at)
		}
	}
	f.clock.SetTime(since.Add(time.Hour))
	add(8)
	f.tick()
	l := f.ledger()
	r := digestRecords(l)
	if len(f.digests()) != 1 || len(r) != 1 || digestRecordLines(t, r[0]) != 40 || l.State.Digest.LastAt != since.Add(time.Hour).Unix() {
		t.Fatalf("one hour after the watermark: %d digests, records %+v, lastAt %d", len(f.digests()), r, l.State.Digest.LastAt)
	}
	first := f.clock.Now()
	for _, at := range []time.Duration{time.Second, time.Minute, 30 * time.Minute, time.Hour - time.Second} {
		f.clock.SetTime(first.Add(at))
		add(8)
		f.tick()
		if len(f.digests()) != 1 {
			t.Fatalf("a second early digest %s after the first", at)
		}
	}
	f.clock.SetTime(first.Add(time.Hour))
	add(8)
	f.tick()
	l = f.ledger()
	r = digestRecords(l)
	if len(f.digests()) != 2 || len(r) != 2 || digestRecordLines(t, r[1]) != 40 || l.State.Digest.LastAt != first.Add(time.Hour).Unix() {
		t.Fatalf("one hour after the first digest: %d digests, records %+v", len(f.digests()), r)
	}
	if len(l.State.Digest.Pending) != 0 || len(l.State.Digest.Listed) != n {
		t.Fatalf("pending %d listed %d of %d", len(l.State.Digest.Pending), len(l.State.Digest.Listed), n)
	}
	// Fewer than eight: the 24-hour rule, unchanged.
	second := f.clock.Now()
	f.clock.Step(time.Minute)
	add(7)
	for _, at := range []time.Duration{time.Hour, 2 * time.Hour, 24*time.Hour - time.Second} {
		f.clock.SetTime(second.Add(at))
		f.tick()
		if len(f.digests()) != 2 {
			t.Fatalf("seven lines sent %s after the last digest", at)
		}
	}
	f.clock.SetTime(second.Add(24 * time.Hour))
	f.tick()
	if r := digestRecords(f.ledger()); len(f.digests()) != 3 || len(r) != 3 || digestRecordLines(t, r[2]) != 7 {
		t.Fatalf("seven lines at 24 hours: %d digests", len(f.digests()))
	}
	f.clock.Step(48 * time.Hour)
	f.tick()
	if len(f.digests()) != 3 || f.push.count() != 3 {
		t.Fatalf("sent again: %v", f.push.titles())
	}
	if strings.Contains(f.logs.String(), summonDigestClockLine) {
		t.Fatal("the clock line with a clock that never stepped back")
	}
}

// Ruling 2: while digest.since or digest.lastAt is ahead of the keeper's
// clock, each tick logs one fixed line, with no name or value from a session.
// Nothing is sent twice, or more often than the rules allow.
func TestSummonDigestClockBehindTheLedger(t *testing.T) {
	ledger := func(since, lastAt int64) string {
		return fmt.Sprintf(`{"version":1,"events":{},"window":[],"capped":0,"more":0,"digest":{"since":%d,"lastAt":%d,"pending":[],"listed":{}},"overflow":{}}`, since, lastAt)
	}
	lines := func(f *summonFix) int { return strings.Count(f.logs.String(), summonDigestClockLine) }
	quiet := func(f *summonFix, prefix string, k int) {
		for i := 1; i <= k; i++ {
			f.add(f.quiet(fmt.Sprintf("%s-%d", prefix, i)))
		}
	}
	noSessionText := func(t *testing.T, f *summonFix) {
		t.Helper()
		for _, line := range strings.Split(f.logs.String(), "\n") {
			if strings.Contains(line, summonDigestClockLine) && (strings.Contains(line, "wo-cigar") || strings.Contains(line, "since") || strings.Contains(line, "lastAt")) {
				t.Fatalf("the clock line carries a value: %s", line)
			}
		}
	}
	t.Run("since-in-the-future", func(t *testing.T) {
		f := newSummonFix(t)
		t0 := f.clock.Now()
		since := t0.Add(2 * time.Hour)
		writeSummonLedger(t, f, ledger(since.Unix(), 0), false)
		quiet(f, "wo-cigar-curate-early", 9)
		for i, at := range []time.Duration{0, time.Hour, 2*time.Hour - time.Second} {
			f.clock.SetTime(t0.Add(at))
			f.tick()
			l := f.ledger()
			if f.push.count() != 0 || len(l.State.Digest.Pending) != 0 || len(l.State.Events) != 0 || l.State.Digest.Since != since.Unix() || lines(f) != i+1 {
				t.Fatalf("%s: sends %d, digest %+v, clock lines %d", at, f.push.count(), l.State.Digest, lines(f))
			}
		}
		// The clock reaches the watermark: no more clock lines, and only closes
		// at or after it are listed.
		f.clock.SetTime(since.Add(time.Minute))
		quiet(f, "wo-cigar-curate-late", 8)
		f.tick()
		if f.push.count() != 0 || len(f.ledger().State.Digest.Pending) != 8 || lines(f) != 3 {
			t.Fatalf("after the watermark: sends %d pending %d clock lines %d", f.push.count(), len(f.ledger().State.Digest.Pending), lines(f))
		}
		f.clock.SetTime(since.Add(time.Hour - time.Second))
		f.tick()
		if f.push.count() != 0 {
			t.Fatal("an early digest within an hour of the watermark")
		}
		f.clock.SetTime(since.Add(time.Hour))
		f.tick()
		d := f.digests()
		if len(d) != 1 || strings.Contains(d[0].Get("message"), "curate-early") || len(f.ledger().State.Digest.Listed) != 8 {
			t.Fatalf("digests %d listed %+v", len(d), f.ledger().State.Digest.Listed)
		}
		f.clock.Step(48 * time.Hour)
		f.tick()
		if f.push.count() != 1 || lines(f) != 3 {
			t.Fatalf("sends %d clock lines %d", f.push.count(), lines(f))
		}
		noSessionText(t, f)
	})
	t.Run("lastAt-in-the-future", func(t *testing.T) {
		f := newSummonFix(t)
		t0 := f.clock.Now()
		lastAt := t0.Add(2 * time.Hour)
		writeSummonLedger(t, f, ledger(t0.Add(-48*time.Hour).Unix(), lastAt.Unix()), false)
		quiet(f, "wo-cigar-curate", 8)
		for i, at := range []time.Duration{0, 2*time.Hour - time.Second} {
			f.clock.SetTime(t0.Add(at))
			f.tick()
			if f.push.count() != 0 || len(f.ledger().State.Digest.Pending) != 8 || lines(f) != i+1 {
				t.Fatalf("%s: sends %d pending %d clock lines %d", at, f.push.count(), len(f.ledger().State.Digest.Pending), lines(f))
			}
		}
		// At lastAt the clock is no longer behind; the floor still runs from it.
		for _, at := range []time.Duration{0, time.Hour - time.Second} {
			f.clock.SetTime(lastAt.Add(at))
			f.tick()
			if f.push.count() != 0 || lines(f) != 2 {
				t.Fatalf("lastAt+%s: sends %d clock lines %d", at, f.push.count(), lines(f))
			}
		}
		f.clock.SetTime(lastAt.Add(time.Hour))
		f.tick()
		r := digestRecords(f.ledger())
		if len(f.digests()) != 1 || len(r) != 1 || r[0].Key != fmt.Sprintf("summon/digest/%d", lastAt.Add(time.Hour).Unix()) {
			t.Fatalf("digests %d records %+v", len(f.digests()), r)
		}
		f.clock.Step(48 * time.Hour)
		f.tick()
		if f.push.count() != 1 || lines(f) != 2 {
			t.Fatalf("sends %d clock lines %d", f.push.count(), lines(f))
		}
		noSessionText(t, f)
	})
	t.Run("clock-steps-back-after-a-digest", func(t *testing.T) {
		f := newSummonFix(t)
		since := f.start()
		f.add(f.quiet("wo-cigar-curate-first"))
		f.clock.SetTime(since.Add(24 * time.Hour))
		f.tick()
		first := f.clock.Now()
		if len(f.digests()) != 1 || lines(f) != 0 {
			t.Fatalf("first digest: %d, clock lines %d", len(f.digests()), lines(f))
		}
		// The clock steps back two hours; eight new lines wait out the floor
		// from the last digest, not from the stepped-back clock.
		f.clock.SetTime(first.Add(-2 * time.Hour))
		quiet(f, "wo-cigar-curate-back", 8)
		for i, at := range []time.Duration{-2 * time.Hour, -time.Second} {
			f.clock.SetTime(first.Add(at))
			f.tick()
			if len(f.digests()) != 1 || len(f.ledger().State.Digest.Pending) != 8 || lines(f) != i+1 {
				t.Fatalf("first%s: digests %d pending %d clock lines %d", at, len(f.digests()), len(f.ledger().State.Digest.Pending), lines(f))
			}
		}
		for _, at := range []time.Duration{0, time.Hour - time.Second} {
			f.clock.SetTime(first.Add(at))
			f.tick()
			if len(f.digests()) != 1 || lines(f) != 2 {
				t.Fatalf("first+%s: digests %d clock lines %d", at, len(f.digests()), lines(f))
			}
		}
		f.clock.SetTime(first.Add(time.Hour))
		f.tick()
		l := f.ledger()
		r := digestRecords(l)
		if len(f.digests()) != 2 || len(r) != 2 || r[0].Key != fmt.Sprintf("summon/digest/%d", first.Unix()) || r[1].Key != fmt.Sprintf("summon/digest/%d", first.Add(time.Hour).Unix()) ||
			r[0].State != pages.StateSent || r[1].State != pages.StateSent || len(l.State.Digest.Listed) != 9 {
			t.Fatalf("digests %d records %+v listed %d", len(f.digests()), r, len(l.State.Digest.Listed))
		}
		f.clock.Step(48 * time.Hour)
		f.tick()
		if f.push.count() != 2 || lines(f) != 2 {
			t.Fatalf("sends %d clock lines %d", f.push.count(), lines(f))
		}
		noSessionText(t, f)
	})
	t.Run("record-key-when-the-attempt-is-not-after-lastAt", func(t *testing.T) {
		// The tick never reserves a digest at or before lastAt (both rules wait
		// at least an hour from it), so the reservation is called directly, as
		// a clock stepped back to lastAt, then behind it, would call it.
		f := newSummonFix(t)
		since := f.start()
		f.add(f.quiet("wo-cigar-curate-1"))
		f.clock.SetTime(since.Add(24 * time.Hour))
		f.tick()
		first := f.clock.Now()
		f.clock.Step(time.Minute)
		s2 := f.quiet("wo-cigar-curate-2")
		f.add(s2)
		f.tick()
		if len(f.digests()) != 1 || len(f.ledger().State.Digest.Pending) != 1 {
			t.Fatalf("digests %d pending %d", len(f.digests()), len(f.ledger().State.Digest.Pending))
		}
		f.clock.SetTime(first) // the attempt's second is lastAt's
		l := f.ledger()
		f.p.sendDigest(f.ctx, l, f.clock.Now())
		l = f.ledger()
		if l.State.Digest.LastAt != first.Unix()+1 || l.State.Digest.Listed[summonKey(s2, summonEventDigest)] != first.Unix()+1 {
			t.Fatalf("lastAt %d listed %+v", l.State.Digest.LastAt, l.State.Digest.Listed)
		}
		f.add(f.quiet("wo-cigar-curate-3"))
		f.tick() // pends the line; lastAt is a second ahead, so the clock line
		if lines(f) != 1 || len(f.ledger().State.Digest.Pending) != 1 {
			t.Fatalf("clock lines %d pending %d", lines(f), len(f.ledger().State.Digest.Pending))
		}
		f.clock.SetTime(first.Add(-time.Hour)) // the attempt's second is before lastAt's
		l = f.ledger()
		f.p.sendDigest(f.ctx, l, f.clock.Now())
		l = f.ledger()
		r := digestRecords(l)
		want := []int64{first.Unix(), first.Unix() + 1, first.Unix() + 2}
		if len(r) != len(want) || len(f.digests()) != len(want) || l.State.Digest.LastAt != first.Unix()+2 {
			t.Fatalf("records %+v digests %d lastAt %d", r, len(f.digests()), l.State.Digest.LastAt)
		}
		for i, at := range want {
			if r[i].Key != fmt.Sprintf("summon/digest/%d", at) || r[i].State != pages.StateSent || r[i].Attempts != 1 || digestRecordLines(t, r[i]) != 1 {
				t.Fatalf("record %d: %+v", i, r[i])
			}
		}
		// Back to the real time: nothing is pending, nothing more is sent.
		f.clock.SetTime(first.Add(48 * time.Hour))
		f.tick()
		f.clock.Step(48 * time.Hour)
		f.tick()
		if f.push.count() != 3 || len(f.ledger().State.Digest.Listed) != 3 {
			t.Fatalf("sends %d listed %d", f.push.count(), len(f.ledger().State.Digest.Listed))
		}
		noSessionText(t, f)
	})
}

// Ruling 5: digest.listed guards a line as the dedupe does. A key that is
// listed but whose dedupe entry is gone (pruned on proof while a list still
// showed the session) gets no second line; at the next reservation the listed
// key goes, as it always did.
func TestSummonDigestListedIsAGuard(t *testing.T) {
	f := newSummonFix(t)
	since := f.start()
	kept, lost := f.quiet("wo-cigar-curate-kept"), f.quiet("wo-cigar-curate-lost")
	f.add(kept, lost)
	f.clock.SetTime(since.Add(24 * time.Hour))
	f.tick()
	if len(f.digests()) != 1 || len(digestLines(f.digests()[0].Get("message"))) != 2 {
		t.Fatalf("first digest: %v", f.push.titles())
	}
	// The lost session's dedupe entry goes; its listed key stays.
	key := summonKey(lost, summonEventDigest)
	l := f.ledger()
	delete(l.State.Events, key)
	if err := f.p.store.save(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	for _, step := range []time.Duration{time.Minute, time.Hour, 25 * time.Hour} {
		f.clock.Step(step)
		f.tick()
		l = f.ledger()
		if len(l.State.Digest.Pending) != 0 || len(f.digests()) != 1 {
			t.Fatalf("a listed key got a second line: pending %+v digests %d", l.State.Digest.Pending, len(f.digests()))
		}
		if _, ok := l.State.Events[key]; ok {
			t.Fatal("a listed key's entry was written again")
		}
	}
	// The session is gone now; the next digest lists only the new close and
	// drops the listed key whose entry is gone.
	if err := f.c.Delete(f.ctx, lost); err != nil {
		t.Fatal(err)
	}
	next := f.clock.Now()
	f.add(f.quiet("wo-cigar-curate-next"))
	f.clock.SetTime(next.Add(24 * time.Hour))
	f.tick()
	d := f.digests()
	if len(d) != 2 || strings.Join(digestLines(d[1].Get("message")), ",") != "wo-cigar-curate-next curation done done" {
		t.Fatalf("second digest: %v", f.push.titles())
	}
	if _, ok := f.ledger().State.Digest.Listed[key]; ok {
		t.Fatal("the listed key without an entry stayed after the reservation")
	}
	f.clock.Step(48 * time.Hour)
	f.tick()
	if f.push.count() != 2 {
		t.Fatalf("sent again: %v", f.push.titles())
	}
}

// Ruling 1, as the coordinator's answer op792-fix1-q-1 asks: while the floor
// makes lines wait, their entries are held from pruning up to an hour longer,
// and the dedupe bound still holds in that hour (the pending bound's proof is
// TestSummonDigestPendingBound). With the dedupe all but full of entries too
// young to prune, the lines that fit wait out the hour, the rest wait for
// room, the dedupe never passes its bound, and each line is sent once.
func TestSummonDigestFloorKeepsTheDedupeBound(t *testing.T) {
	f := newSummonFix(t)
	t0 := f.clock.Now()
	since := t0.Add(-time.Minute)
	st := summonPageState{Version: 1, Events: map[string]summonPageEntry{}, Window: []int64{}, Overflow: map[string]summonOverflowEntry{},
		Digest: summonDigestState{Since: since.Unix(), Pending: []summonDigestLine{}, Listed: map[string]int64{}}}
	for i := 0; len(st.Events) < summonPagesMaxEvents-8; i++ {
		st.Events[fmt.Sprintf("00000000-0000-4000-9000-%012d/failed", i)] = summonPageEntry{At: t0.Unix(), State: string(pages.StateSent), Session: fmt.Sprintf("wo-young-%d", i)}
	}
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	writeSummonLedger(t, f, string(raw), false)
	for i := 1; i <= 20; i++ {
		f.add(f.quiet(fmt.Sprintf("wo-cigar-curate-%02d", i)))
	}
	const full = "summon page dedupe capacity reached"
	bounded := func(at string) *summonPageLedger {
		t.Helper()
		l := f.ledger()
		if len(l.State.Events) > summonPagesMaxEvents || len(l.State.Digest.Pending) > summonPagesMaxLines {
			t.Fatalf("%s: entries %d pending %d", at, len(l.State.Events), len(l.State.Digest.Pending))
		}
		return l
	}
	// The hour from the watermark: eight lines fit the dedupe and wait; the
	// full dedupe is logged once a tick, and no young entry is pruned.
	for i, at := range []time.Duration{0, 30 * time.Minute, time.Hour - time.Minute - time.Second} {
		f.clock.SetTime(t0.Add(at))
		f.tick()
		l := bounded(at.String())
		if f.push.count() != 0 || len(l.State.Events) != summonPagesMaxEvents || len(l.State.Digest.Pending) != 8 || strings.Count(f.logs.String(), full) != i+1 || f.sessionGets != 0 {
			t.Fatalf("%s: sends %d entries %d pending %d full logged %d proof reads %d", at, f.push.count(), len(l.State.Events), len(l.State.Digest.Pending), strings.Count(f.logs.String(), full), f.sessionGets)
		}
	}
	f.clock.SetTime(since.Add(time.Hour))
	f.tick()
	l := bounded("the first digest")
	if len(f.digests()) != 1 || len(digestLines(f.digests()[0].Get("message"))) != 8 || len(l.State.Events) != summonPagesMaxEvents || len(l.State.Digest.Listed) != 8 {
		t.Fatalf("the first digest: %d digests, entries %d listed %d", len(f.digests()), len(l.State.Events), len(l.State.Digest.Listed))
	}
	first := f.clock.Now()
	// The young entries are an hour old now: proof reads make room, and the
	// other twelve lines pend and wait out the hour from the first digest.
	f.clock.SetTime(t0.Add(time.Hour))
	f.tick()
	l = bounded("room")
	if len(f.digests()) != 1 || len(l.State.Digest.Pending) != 12 || f.sessionGets != summonPagesProofReads {
		t.Fatalf("room: digests %d pending %d proof reads %d", len(f.digests()), len(l.State.Digest.Pending), f.sessionGets)
	}
	f.clock.SetTime(first.Add(time.Hour - time.Second))
	f.tick()
	bounded("the floor")
	if len(f.digests()) != 1 {
		t.Fatal("the twelve went within an hour of the first digest")
	}
	f.clock.SetTime(first.Add(time.Hour))
	f.tick()
	l = bounded("the second digest")
	if len(f.digests()) != 2 || len(digestLines(f.digests()[1].Get("message"))) != 12 || len(l.State.Digest.Listed) != 20 || len(l.State.Digest.Pending) != 0 {
		t.Fatalf("the second digest: %d digests, listed %d", len(f.digests()), len(l.State.Digest.Listed))
	}
	f.clock.Step(48 * time.Hour)
	f.tick()
	if f.push.count() != 2 {
		t.Fatalf("sent again: %v", f.push.titles())
	}
}
