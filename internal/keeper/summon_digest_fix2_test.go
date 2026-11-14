package keeper

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/thaynes43/dev-env/internal/pages"
)

// Story 792 fix round 2: the coordinator's rulings on PR 1126 review
// comment 6871037091, not rulings by Tom. These use the existing fake
// client, clock and Pushover endpoint; nothing reaches a live service.

// F1: an older recorded capped event gets a slot before a full batch of
// newer outcomes. It is held until listed, then pruned on proof while the
// one remaining outcome is admitted. Neither attempt repeats a line.
func TestSummonDigestCappedBeforeNewOutcomes(t *testing.T) {
	f := newSummonFix(t)
	since := f.start()
	const cappedKey = "00000000-0000-4000-9000-000000000001/failed"
	l := f.ledger()
	l.State.Events[cappedKey] = summonPageEntry{At: since.Add(time.Second).Unix(), State: summonStateCapped, Session: "wo-upgrade-older"}
	if err := f.p.store.save(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	f.clock.SetTime(since.Add(2 * time.Hour))
	var outcomes []string
	for i := 0; i < summonPagesMaxLines; i++ {
		s := f.quiet(fmt.Sprintf("wo-cigar-curate-%03d", i))
		outcomes = append(outcomes, summonKey(s, summonEventDigest))
		f.add(s)
	}
	f.tick()
	l = f.ledger()
	firstAt := f.clock.Now().Unix()
	if l.State.Digest.Listed[cappedKey] != firstAt {
		t.Fatal("the older capped key was not listed in the first reservation")
	}
	if len(f.digests()) != 1 || len(l.State.Digest.Listed) != summonPagesMaxLines || len(l.State.Digest.Pending) != 0 {
		t.Fatalf("first reservation: digests %d listed %d pending %d", len(f.digests()), len(l.State.Digest.Listed), len(l.State.Digest.Pending))
	}
	if l.State.Events[cappedKey].State != summonStateCapped || f.sessionGets != 0 {
		t.Fatalf("capped entry was not held: state %q proof reads %d", l.State.Events[cappedKey].State, f.sessionGets)
	}
	for _, key := range outcomes[:len(outcomes)-1] {
		if l.State.Digest.Listed[key] != firstAt {
			t.Fatal("an outcome that fit was not listed in the first reservation")
		}
	}
	waiting := outcomes[len(outcomes)-1]
	if _, ok := l.State.Events[waiting]; ok {
		t.Fatal("the newer outcome took the older capped event's slot")
	}
	if _, ok := l.State.Digest.Listed[waiting]; ok {
		t.Fatal("the newer outcome was listed before it had a slot")
	}
	shown := digestLines(f.digests()[0].Get("message"))
	if len(shown) == 0 || shown[0] != "wo-upgrade-older unknown capped failed" {
		t.Fatalf("the older capped line was not first: %v", shown)
	}

	// Once listed, the gone capped session can be proved gone. That prune
	// is saved together with admission of the one outcome that waited.
	f.clock.Step(time.Minute)
	f.tick()
	l = f.ledger()
	if f.sessionGets != 1 {
		t.Fatalf("listed capped event: proof reads %d, want 1", f.sessionGets)
	}
	if _, ok := l.State.Events[cappedKey]; ok {
		t.Fatal("the listed capped event was not proof-prunable")
	}
	if len(l.State.Digest.Pending) != 1 || l.State.Digest.Pending[0].Key != waiting {
		t.Fatal("the one newer outcome did not get the next pending slot")
	}
	f.restart()
	f.clock.Step(24 * time.Hour)
	f.tick()
	l = f.ledger()
	if len(f.digests()) != 2 || len(l.State.Digest.Listed) != len(outcomes) || len(l.State.Digest.Pending) != 0 {
		t.Fatalf("second reservation: digests %d listed %d pending %d", len(f.digests()), len(l.State.Digest.Listed), len(l.State.Digest.Pending))
	}
	for _, key := range outcomes[:len(outcomes)-1] {
		if l.State.Digest.Listed[key] != firstAt {
			t.Fatal("an outcome was listed in another attempt")
		}
	}
	if l.State.Digest.Listed[waiting] != f.clock.Now().Unix() {
		t.Fatal("the waiting outcome was not listed in the second attempt")
	}
	seen := map[string]bool{}
	for _, digest := range f.digests() {
		for _, line := range digestLines(digest.Get("message")) {
			if seen[line] {
				t.Fatal("a digest line was repeated")
			}
			seen[line] = true
		}
	}
	if lines := digestLines(f.digests()[1].Get("message")); len(lines) != 1 || lines[0] != "wo-cigar-curate-255 curation done done" {
		t.Fatalf("the second digest did not contain only the waiting outcome: %v", lines)
	}
}

// F2: a full pending buffer cannot admit an unseen outcome, so it must not
// spend proof reads whose deletions would be discarded. Once it drains,
// bounded pruning and admission resume in the same saved update.
func TestSummonDigestFullPendingSkipsProofReads(t *testing.T) {
	f := newSummonFix(t)
	since := f.start()
	for i := 0; i < summonPagesMaxLines; i++ {
		f.add(f.quiet(fmt.Sprintf("wo-cigar-curate-%03d", i)))
	}
	f.tick()
	l := f.ledger()
	var oldKeys []string
	for i := 0; i < summonPagesProofReads+5; i++ {
		key := fmt.Sprintf("00000000-0000-4000-9000-%012d/failed", i)
		oldKeys = append(oldKeys, key)
		l.State.Events[key] = summonPageEntry{At: since.Add(-2 * time.Hour).Unix(), State: string(pages.StateSent), Session: fmt.Sprintf("wo-gone-%03d", i)}
	}
	if err := f.p.store.save(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	next := f.quiet("wo-cigar-curate-next")
	f.add(next)
	before, writes := f.rawLedger(), f.updates
	for _, at := range []time.Duration{2 * time.Minute, 30 * time.Minute, time.Hour - time.Second} {
		f.clock.SetTime(since.Add(at))
		f.tick()
		if f.sessionGets != 0 || f.updates != writes {
			t.Fatalf("full pending at %s: proof reads %d, writes %d; want zero of each", at, f.sessionGets, f.updates-writes)
		}
		if f.rawLedger() != before || f.push.count() != 0 {
			t.Fatal("a tick inside the floor changed or sent the full pending batch")
		}
	}
	f.clock.SetTime(since.Add(time.Hour))
	f.tick()
	if f.sessionGets != 0 || len(f.digests()) != 1 || len(f.ledger().State.Digest.Pending) != 0 {
		t.Fatalf("drain: proof reads %d digests %d pending %d", f.sessionGets, len(f.digests()), len(f.ledger().State.Digest.Pending))
	}
	writes = f.updates
	f.clock.Step(time.Second)
	f.tick()
	l = f.ledger()
	if f.sessionGets != summonPagesProofReads || f.updates != writes+1 || len(l.State.Digest.Pending) != 1 || l.State.Digest.Pending[0].Key != summonKey(next, summonEventDigest) {
		t.Fatalf("admission: proof reads %d writes %d pending %d", f.sessionGets, f.updates-writes, len(l.State.Digest.Pending))
	}
	remaining := 0
	for _, key := range oldKeys {
		if _, ok := l.State.Events[key]; ok {
			remaining++
		}
	}
	if remaining != len(oldKeys)-summonPagesProofReads {
		t.Fatalf("saved pruning left %d old entries, want %d", remaining, len(oldKeys)-summonPagesProofReads)
	}
	// With no further admission or repair, the remaining old entries wait.
	writes = f.updates
	f.tick()
	if f.sessionGets != summonPagesProofReads || f.updates != writes {
		t.Fatal("an idle tick read proofs or wrote after admission")
	}
}

// F3: a footer can make a prefix too large even though the whole message
// fits exactly. All ten lines must be sent and recorded with zero omitted.
func TestSummonDigestWholeMessageFitsAt950(t *testing.T) {
	f := newSummonFix(t)
	f.start()
	var want []string
	for i, n := range []int{63, 63, 63, 63, 63, 63, 63, 63, 20, 19} {
		prefix := fmt.Sprintf("wo-%02d-", i)
		name := prefix + strings.Repeat("x", n-len(prefix))
		want = append(want, name+" curation done done")
		f.add(f.quiet(name))
		f.clock.Step(time.Second)
	}
	f.clock.Step(time.Hour)
	f.tick()
	if len(f.digests()) != 1 {
		t.Fatalf("digests %d, want 1", len(f.digests()))
	}
	msg := f.digests()[0].Get("message")
	if got := digestLines(msg); len(got) != len(want) {
		t.Fatalf("whole 950-character message: shown %d lines, want %d", len(got), len(want))
	}
	if strings.Join(digestLines(msg), "\n") != strings.Join(want, "\n") || utf8.RuneCountInString(msg) != 950 || strings.Contains(msg, "left out") {
		t.Fatal("the fitting whole message changed, exceeded 950, or had a footer")
	}
	l := f.ledger()
	if records := digestRecords(l); len(records) != 1 || records[0].Body != "digest of 10 lines, 0 left out" || records[0].State != pages.StateSent {
		t.Fatalf("record %+v", records)
	}
	if len(l.State.Digest.Listed) != len(want) || len(l.State.Digest.Pending) != 0 {
		t.Fatalf("listed %d pending %d", len(l.State.Digest.Listed), len(l.State.Digest.Pending))
	}
}
