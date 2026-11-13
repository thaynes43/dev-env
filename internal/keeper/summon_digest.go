package keeper

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/pages"
	"github.com/thaynes43/dev-env/internal/summonoutcome"
)

// The daily digest of summoned outcomes that did not page (DESIGN-064 6.8;
// story 792, slice b of 742; coordinator rulings Q5, Q6, Q7, Q9 and Q10 on
// item 742 comment 7457, not rulings by Tom). It runs inside the pager's
// tick, with the pager's client, ledger and Pushover sender: no client,
// lock, transport or goroutine of its own.
//
//   - A close whose terminal row is the digest gets one line, once: a ledger
//     entry "<session UID>/digest" and a pending line, written together. Only
//     a close at or after the watermark digest.since, the pager's first run,
//     is in a digest (ruling Q6).
//   - An event the hourly cap left out (an entry recorded capped at or after
//     the watermark) gets a pending line too (ruling Q5). Its state word is
//     capped, which no outcome state uses, and its last word is the event.
//   - A digest is due 24 hours after the last one, or early once 8 lines are
//     pending and at least an hour has passed since the last one, at any hour
//     (ruling Q7; the hour is fix round 1's ruling 1), never while nothing is
//     pending. It is outside the hourly cap: it takes no slot and waits for
//     none.
//   - A watermark or last digest time ahead of the keeper's clock is said in
//     one fixed line a tick (fix round 1, ruling 2).
//   - Its reservation moves every pending line to digest.listed, sets
//     digest.lastAt and begins its content-free record, in one write before
//     the send (ruling Q9). A line is in exactly one digest attempt, ever; a
//     send whose result is unknown is recorded and never retried.
const (
	summonEventDigest = "digest"
	// v1's OPS_DIGEST_INTERVAL_H and OPS_DIGEST_MAX_PENDING (haynes-ops
	// upgrade-agent/dev-env-ops/app/helmrelease.yaml).
	summonDigestEvery = 24 * time.Hour
	summonDigestEarly = 8
	// summonDigestEarlyFloor is the least time from the last digest (before the
	// first, the watermark) to an early one: story 792 fix round 1, ruling 1 in
	// PR 1126 comment 6844882457, the coordinator's, not Tom's.
	summonDigestEarlyFloor = time.Hour
	// summonDigestBodyMax is the cut of the message, footer included: v1's
	// OPS_DIGEST_MAX_CHARS, under Pushover's 1024 (internal/pages MaxBody).
	summonDigestBodyMax = 950
	summonDigestPrio    = -1
)

// summonDigestOutcome is a summoned session's close as a digest line, when its
// row in the terminal table is the digest. Every word is kept in the shape a
// page shows it: the stored session name, or unknown, and each operator word
// only as summonWord allows. No note text (ruling Q6). A close without a time
// has At at or below zero, before every watermark: it is in no digest.
func summonDigestOutcome(s *v1alpha1.AgentSession) (summonDigestLine, bool) {
	ev, ok := summonoutcome.ClosedTerminalPage(s)
	if !ok || ev != summonoutcome.TerminalDigest {
		return summonDigestLine{}, false
	}
	c := s.Status.Summon.Closed
	return summonDigestLine{
		Key: string(s.UID) + "/" + summonEventDigest, Session: summonStoredName(s.Name),
		Lane: summonWord(string(s.Spec.Lane)), State: summonWord(string(s.Status.Outcome.State)), Reason: summonWord(c.Reason),
		At: c.At.Unix(),
	}, true
}

// summonDigestOutcomeKey reports whether a line or ledger key is an outcome's
// ("<uid>/digest"), not a capped event's.
func summonDigestOutcomeKey(key string) bool {
	owner, event, _ := cutSummonKey(key)
	return event == summonEventDigest && owner != "drill" && owner != "more"
}

// watermark sets digest.since at the pager's first run (ruling Q6) and reports
// whether it did. A ledger story 782's keeper wrote holds since 0.
func (s *summonPageState) watermark(now time.Time) bool {
	if s.Digest.Since > 0 {
		return false
	}
	s.Digest.Since = max(now.Unix(), 1)
	return true
}

// newDigestOutcomes are the digest-row closes this tick may add: at or after
// the watermark, not in the dedupe and not already in digest.listed (fix
// round 1, ruling 5: a listed key whose entry went still gets no second
// line). Oldest close first.
func (s summonPageState) newDigestOutcomes(outcomes []summonDigestLine) []summonDigestLine {
	var out []summonDigestLine
	for _, line := range outcomes {
		_, seen := s.Events[line.Key]
		_, listed := s.Digest.Listed[line.Key]
		if seen || listed || s.Digest.Since <= 0 || line.At < s.Digest.Since {
			continue
		}
		out = append(out, line)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.At != b.At {
			return a.At < b.At
		}
		if a.Session != b.Session {
			return a.Session < b.Session
		}
		return a.Key < b.Key
	})
	return out
}

// digestHolds are the dedupe keys prune keeps whatever a proof read says: a
// pending line's, and a capped event's that a digest has yet to list. A line
// is listed before its entry can go.
func (s summonPageState) digestHolds() map[string]bool {
	holds := map[string]bool{}
	for _, line := range s.Digest.Pending {
		holds[line.Key] = true
	}
	for key, e := range s.Events {
		if _, listed := s.Digest.Listed[key]; !listed && s.cappedForDigest(key, e) {
			holds[key] = true
		}
	}
	return holds
}

// cappedForDigest reports whether an entry is a capped event the digest lists:
// recorded capped at or after the watermark, for a session or a drill.
func (s summonPageState) cappedForDigest(key string, e summonPageEntry) bool {
	owner, _, _ := cutSummonKey(key)
	return e.State == summonStateCapped && s.Digest.Since > 0 && e.At >= s.Digest.Since && owner != "more"
}

// digestMissingEntries reports whether a pending line has lost its dedupe
// entry: a keeper without the digest prunes on proof alone. pend writes it
// again, so a digest record never stands beside an empty dedupe.
func (s summonPageState) digestMissingEntries() bool {
	for _, line := range s.Digest.Pending {
		if _, ok := s.Events[line.Key]; !ok {
			return true
		}
	}
	return false
}

// pend adds this tick's digest lines to the ledger in memory and reports
// whether it changed it. lanes maps a listed session's UID to its lane word,
// for a capped event's line. dedupeFull says the tick has already logged a
// full dedupe. Every line keeps a dedupe entry, so the dedupe is never empty
// beside a digest record. It runs once a tick, so it also says, in one fixed
// line with no value, when the ledger's digest times are ahead of the clock
// (fix round 1, ruling 2).
func (p *summonPager) pend(l *summonPageLedger, outcomes []summonDigestLine, lanes map[string]string, now time.Time, dedupeFull bool) bool {
	st, d := &l.State, &l.State.Digest
	if d.ahead(now) {
		p.log.Error(nil, "summon digest ledger time is ahead of the keeper's clock")
	}
	changed, pendingFull := false, false
	full := func() bool {
		if len(d.Pending) < summonPagesMaxLines {
			return false
		}
		if !pendingFull {
			pendingFull = true
			p.log.Error(nil, "summon digest pending capacity reached")
		}
		return true
	}
	// A pending line whose entry a keeper without the digest pruned gets it
	// back, while the dedupe has room.
	for _, line := range d.Pending {
		if _, ok := st.Events[line.Key]; ok || len(st.Events) >= summonPagesMaxEvents {
			continue
		}
		state := string(pages.StateRecorded)
		if !summonDigestOutcomeKey(line.Key) {
			state = summonStateCapped
		}
		st.Events[line.Key] = summonPageEntry{At: now.Unix(), State: state, Session: summonStoredName(line.Session)}
		changed = true
	}
	for _, line := range st.newDigestOutcomes(outcomes) {
		if full() {
			break
		}
		if len(st.Events) >= summonPagesMaxEvents {
			if !dedupeFull {
				p.log.Error(nil, "summon page dedupe capacity reached")
			}
			break
		}
		st.Events[line.Key] = summonPageEntry{At: now.Unix(), State: string(pages.StateRecorded), Session: line.Session}
		d.Pending = append(d.Pending, line)
		changed = true
	}
	pending := map[string]bool{}
	for _, line := range d.Pending {
		pending[line.Key] = true
	}
	var capped []string
	for key, e := range st.Events {
		if _, listed := d.Listed[key]; !listed && !pending[key] && st.cappedForDigest(key, e) {
			capped = append(capped, key)
		}
	}
	sort.Slice(capped, func(i, j int) bool {
		a, b := st.Events[capped[i]], st.Events[capped[j]]
		if a.At != b.At {
			return a.At < b.At
		}
		return capped[i] < capped[j]
	})
	for _, key := range capped {
		if full() {
			break
		}
		d.Pending = append(d.Pending, summonCappedLine(key, st.Events[key], lanes))
		changed = true
	}
	return changed
}

// summonCappedLine is a capped event's line: "<session> <lane> capped
// <event>". A drill's line is "<id> drill capped drill". A lane the tick did
// not list is unknown.
func summonCappedLine(key string, e summonPageEntry, lanes map[string]string) summonDigestLine {
	owner, event, _ := cutSummonKey(key)
	line := summonDigestLine{Key: key, Session: summonStoredName(e.Session), Lane: "unknown", State: summonStateCapped, Reason: summonWord(event), At: e.At}
	if owner == "drill" {
		line.Session, line.Lane, line.Reason = summonStoredName(event), summonEventDrill, summonEventDrill
	} else if lane, ok := lanes[owner]; ok {
		line.Lane = lane
	}
	return line
}

// due reports whether a digest goes now: something is pending, and either 24
// hours have passed since the last digest, or 8 lines are pending and an hour
// has (fix round 1, ruling 1), each to the second. Before the first digest the
// last one is the watermark (op792-q-1). A last digest ahead of the clock
// holds both until the clock has passed it by as long.
func (d summonDigestState) due(now time.Time) bool {
	if len(d.Pending) == 0 {
		return false
	}
	ref := d.LastAt
	if ref <= 0 {
		ref = d.Since
	}
	last := time.Unix(ref, 0)
	if len(d.Pending) >= summonDigestEarly && !now.Before(last.Add(summonDigestEarlyFloor)) {
		return true
	}
	return !now.Before(last.Add(summonDigestEvery))
}

// ahead reports whether the watermark or the last digest's time is after now:
// the keeper's clock is behind the ledger (fix round 1, ruling 2).
func (d summonDigestState) ahead(now time.Time) bool {
	return time.Unix(d.Since, 0).After(now) || time.Unix(d.LastAt, 0).After(now)
}

// sendDigest reserves one digest attempt and sends it once that write is
// confirmed, as attempt does for a page. The reservation moves every pending
// line to digest.listed with the attempt's time, so no line is in two
// attempts, and begins the record: its time, its state, its line count and,
// once sent, Pushover's request id, never its text (ruling Q9). Nothing is
// retried: a refused, conflicting or unconfirmed reservation sends nothing
// (the lines stay pending only if it did not commit), an unknown result is
// ambiguous, a reservation saved after its dispatch deadline is failed, and
// an attempt whose result was never stored stays recorded. The hourly window
// is not touched.
func (p *summonPager) sendDigest(ctx context.Context, l *summonPageLedger, now time.Time) {
	d := &l.State.Digest
	at := now.Unix()
	if at <= d.LastAt {
		at = d.LastAt + 1 // one record key per attempt
	}
	lines := d.Pending
	notice, record := p.renderDigest(lines, at)
	dispatchBy := now.Add(noticeStoreTimeout)
	// A listed key whose entry is gone was proved gone (an entry goes only on
	// proof): it can never be pending again.
	for key := range d.Listed {
		if _, ok := l.State.Events[key]; !ok {
			delete(d.Listed, key)
		}
	}
	for _, line := range lines {
		d.Listed[line.Key] = at
	}
	d.Pending, d.LastAt = []summonDigestLine{}, at
	if created, err := l.Pages.Begin(record, now); err != nil || !created {
		p.log.Error(nil, "summon digest record refused")
		return
	}
	if err := p.store.save(ctx, l); err != nil {
		p.log.Error(nil, "summon digest reservation unconfirmed")
		return
	}
	outcome, status, request := pages.OutcomeFailed, 0, ""
	if !p.clock.Now().After(dispatchBy) {
		outcome, status, request = summonSendOutcome(p.sender.SendReceipt(ctx, notice))
	}
	l.Pages.Finish(record.Key, outcome, status, request, p.clock.Now())
	state := map[pages.Outcome]pages.State{pages.OutcomeSent: pages.StateSent, pages.OutcomeFailed: pages.StateFailed, pages.OutcomeAmbiguous: pages.StateAmbiguous}[outcome]
	for _, line := range lines {
		if e, ok := l.State.Events[line.Key]; ok && summonDigestOutcomeKey(line.Key) {
			e.State = string(state)
			l.State.Events[line.Key] = e
		}
	}
	if err := p.store.save(ctx, l); err != nil {
		// The reservation stays recorded: the digest is never sent again.
		p.log.Error(nil, "summon digest outcome unconfirmed")
	}
}

// renderDigest builds the fixed-template digest and its content-free record.
// The message is a head, one line per pending line in the order they were
// pended, and, when the lines do not all fit in 950 characters, a footer that
// says how many were left out. Only whole lines are shown; a line left out is
// still in this attempt, listed and never sent again, and only the footer's
// number names it. The one link is the console's sessions list.
func (p *summonPager) renderDigest(lines []summonDigestLine, at int64) (Notice, pages.Page) {
	head := fmt.Sprintf("[dev-env] %s: %d %s about summoned sessions that did not page.\n"+
		"Each line: session, lane, state, reason. A line with state capped is a page the hourly cap held back; its last word is that page's event.",
		summonEventDigest, len(lines), summonPlural(len(lines), "line", "lines"))
	shown := make([]string, len(lines))
	for i, line := range lines {
		shown[i] = "\n" + strings.Join([]string{summonStoredName(line.Session), summonWord(line.Lane), summonWord(line.State), summonWord(line.Reason)}, " ")
	}
	footer := func(left int) string {
		if left == 0 {
			return ""
		}
		return fmt.Sprintf("\n%d more %s left out to fit; %s not sent again.", left, summonPlural(left, "line", "lines"), summonPlural(left, "it is", "they are"))
	}
	n, size := 0, utf8.RuneCountInString(head)
	for n < len(shown) {
		next := size + utf8.RuneCountInString(shown[n])
		if next+utf8.RuneCountInString(footer(len(shown)-n-1)) > summonDigestBodyMax {
			break
		}
		n, size = n+1, next
	}
	msg := head + strings.Join(shown[:n], "") + footer(len(shown)-n)
	console := p.origin + summonConsolePath
	notice := Notice{Title: "[dev-env] " + summonEventDigest, Message: msg, Priority: summonDigestPrio, URL: console, URLTitle: "Open dev-env sessions"}
	rec := pages.Page{
		Key: summonRecordKey(fmt.Sprintf("%s/%d", summonEventDigest, at)), Sender: summonPageSender, Source: summonPageSource,
		Priority: summonDigestPrio, Title: notice.Title,
		Body: fmt.Sprintf("digest of %d %s, %d left out", len(lines), summonPlural(len(lines), "line", "lines"), len(lines)-n),
	}
	return notice, rec
}

func summonPlural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
