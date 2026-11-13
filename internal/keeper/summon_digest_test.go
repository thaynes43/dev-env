package keeper

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/pages"
	"github.com/thaynes43/dev-env/internal/summonoutcome"
)

// Story 792 (slice b of 742, DESIGN-064 6.8): the daily digest of summoned
// outcomes that did not page; coordinator rulings on item 742 comment 7457 and
// answer op792-q-1, not rulings by Tom. Every send goes to the fake Pushover
// endpoint on the fake clock; writes are interleaved by hooks, never by
// goroutine timing. No test here reaches a cluster: the client is the fake
// client and nothing reads the process's cluster environment.

const summonDigestTitle = "[dev-env] digest"

// quiet is a curation session that reported done: a digest row. Its note must
// never reach a digest (ruling Q6).
func (f *summonFix) quiet(name string) *v1alpha1.AgentSession {
	return f.close(f.session(name, v1alpha1.LaneCuration), v1alpha1.OutcomeDone, summonoutcome.Report, "done", "NOTE-TEXT-"+name)
}

// start runs the pager's first tick, which sets the watermark, and steps two
// minutes past it, so a close made next (a minute before the clock) is after it.
func (f *summonFix) start() time.Time {
	f.t.Helper()
	f.tick()
	since := f.ledger().State.Digest.Since
	if since != f.clock.Now().Unix() {
		f.t.Fatalf("the first run set the watermark %d, want %d", since, f.clock.Now().Unix())
	}
	f.clock.Step(2 * time.Minute)
	return time.Unix(since, 0)
}

// digests are the digest messages the fake endpoint received.
func (f *summonFix) digests() []url.Values {
	f.push.mu.Lock()
	defer f.push.mu.Unlock()
	var out []url.Values
	for _, form := range f.push.forms {
		if form.Get("title") == summonDigestTitle {
			out = append(out, form)
		}
	}
	return out
}

// digestLines are the outcome and capped lines of a digest message: every
// line after the two head lines, the footer excluded.
func digestLines(msg string) []string {
	lines := strings.Split(msg, "\n")
	if len(lines) <= 2 {
		return nil
	}
	lines = lines[2:]
	if n := len(lines); strings.Contains(lines[n-1], " left out to fit;") {
		lines = lines[:n-1]
	}
	return lines
}

// digestRecords are the ledger's digest records, oldest first.
func digestRecords(l *summonPageLedger) []pages.Record {
	var out []pages.Record
	for _, r := range l.Pages.Records {
		if strings.HasPrefix(r.Key, "summon/digest/") {
			out = append(out, r)
		}
	}
	return out
}

// goConsts returns the string value of every constant in a Go file whose name
// starts with prefix, read from the source, so a constant added later is
// walked without editing this test.
func goConsts(t *testing.T, path, prefix string) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, d := range file.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok || g.Tok != token.CONST {
			continue
		}
		for _, spec := range g.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, name := range vs.Names {
				if !strings.HasPrefix(name.Name, prefix) || i >= len(vs.Values) {
					continue
				}
				if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					v, err := strconv.Unquote(lit.Value)
					if err != nil {
						t.Fatal(err)
					}
					out = append(out, v)
				}
			}
		}
	}
	if len(out) == 0 {
		t.Fatalf("no %s constants in %s", prefix, path)
	}
	return out
}

// Proof 1: a walk of every lane, terminal state, source and reason (every
// reason constant and every report verb), each asked of the table, never
// copied from it. A close whose row is the digest gets one outcome line, in
// exactly one digest attempt; a close whose row pages gets none (a capped
// page's line is its event's, not an outcome's).
func TestSummonDigestEveryDigestRowOneLine(t *testing.T) {
	f := newSummonFix(t)
	f.start()
	lanes := goConsts(t, "../../api/v1alpha1/agentsession_types.go", "Lane")
	reasons := append(goConsts(t, "../summonoutcome/reasons.go", "Reason"), "done", "failed", "escalate")
	type cell struct {
		s      *v1alpha1.AgentSession
		digest bool
		what   string
	}
	var cells []cell
	digestRows := 0
	for _, lane := range lanes {
		for _, state := range []v1alpha1.OutcomeState{v1alpha1.OutcomeDone, v1alpha1.OutcomeFailed, v1alpha1.OutcomeEscalated} {
			for _, source := range []string{summonoutcome.Report, summonoutcome.Unreported} {
				for _, reason := range reasons {
					s := f.close(f.session(fmt.Sprintf("walk-%d", len(cells)), v1alpha1.Lane(lane)), state, source, reason, "a note")
					digest := summonoutcome.TerminalPage(v1alpha1.Lane(lane), state, source, reason) == summonoutcome.TerminalDigest
					if digest {
						digestRows++
					}
					cells = append(cells, cell{s, digest, fmt.Sprintf("%s %s %s %s", lane, state, source, reason)})
					f.add(s)
				}
			}
		}
	}
	if digestRows == 0 || digestRows == len(cells) {
		t.Fatalf("the walk found %d digest rows of %d cells", digestRows, len(cells))
	}
	// At most 256 lines are pending at once, so the walk takes a few ticks, an
	// hour apart: an early digest waits an hour from the last one (story 792
	// fix round 1, ruling 1).
	for i := 0; i < 6; i++ {
		f.clock.Step(time.Hour)
		f.tick()
	}
	l := f.ledger()
	if len(l.State.Digest.Pending) != 0 {
		t.Fatalf("%d lines still pending", len(l.State.Digest.Pending))
	}
	for _, c := range cells {
		key := summonKey(c.s, summonEventDigest)
		_, entry := l.State.Events[key]
		_, listed := l.State.Digest.Listed[key]
		if c.digest != entry || c.digest != listed {
			t.Errorf("%s (digest row %v): entry %v, listed %v", c.what, c.digest, entry, listed)
		}
	}
	// Each digest record counts its lines; together they count every line once.
	lines := 0
	for _, r := range digestRecords(l) {
		var n, left int
		if _, err := fmt.Sscanf(r.Body, "digest of %d lines, %d left out", &n, &left); err != nil {
			t.Fatalf("record body %q: %v", r.Body, err)
		}
		lines += n
	}
	capped := 0
	for _, e := range l.State.Events {
		if e.State == summonStateCapped {
			capped++
		}
	}
	if lines != digestRows+capped || len(l.State.Digest.Listed) != digestRows+capped {
		t.Fatalf("records count %d lines, listed %d; want %d outcomes and %d capped events", lines, len(l.State.Digest.Listed), digestRows, capped)
	}
}

// Proof 2: the first digest goes 24 hours after the pager's first run, never
// at 23 hours 59 minutes nor one second before (op792-q-1); each later one 24
// hours after the last, not one second before; early once 8 lines are
// pending and an hour has passed since the last digest (fix round 1, ruling
// 1); never while nothing is pending.
func TestSummonDigestTiming(t *testing.T) {
	f := newSummonFix(t)
	since := f.start()
	f.add(f.quiet("wo-cigar-curate-1"))
	for _, at := range []time.Duration{2 * time.Minute, 23*time.Hour + 59*time.Minute, 24*time.Hour - time.Second} {
		f.clock.SetTime(since.Add(at))
		f.tick()
		if len(f.digests()) != 0 {
			t.Fatalf("a digest at %s after the first run", at)
		}
	}
	if n := len(f.ledger().State.Digest.Pending); n != 1 {
		t.Fatalf("%d lines pending", n)
	}
	f.clock.SetTime(since.Add(24 * time.Hour))
	f.tick()
	if len(f.digests()) != 1 || digestLines(f.digests()[0].Get("message"))[0] != "wo-cigar-curate-1 curation done done" {
		t.Fatalf("the first digest at 24 hours: %v", f.push.titles())
	}
	last := f.clock.Now()
	if f.ledger().State.Digest.LastAt != last.Unix() {
		t.Fatalf("lastAt %d", f.ledger().State.Digest.LastAt)
	}
	f.clock.Step(2 * time.Minute)
	f.add(f.quiet("wo-cigar-curate-2"))
	for _, at := range []time.Duration{3 * time.Minute, 24*time.Hour - time.Second} {
		f.clock.SetTime(last.Add(at))
		f.tick()
		if len(f.digests()) != 1 {
			t.Fatalf("a second digest %s after the first", at)
		}
	}
	f.clock.SetTime(last.Add(24 * time.Hour))
	f.tick()
	if len(f.digests()) != 2 {
		t.Fatal("no second digest 24 hours after the first")
	}
	second := f.clock.Now()
	// Early: seven pending lines wait; with the eighth they go once an hour
	// has passed since the last digest.
	f.clock.Step(2 * time.Minute)
	for i := 3; i <= 9; i++ {
		f.add(f.quiet(fmt.Sprintf("wo-cigar-curate-%d", i)))
	}
	f.tick()
	if len(f.digests()) != 2 || len(f.ledger().State.Digest.Pending) != 7 {
		t.Fatalf("seven lines: %d digests, %d pending", len(f.digests()), len(f.ledger().State.Digest.Pending))
	}
	f.clock.Step(time.Minute)
	f.add(f.quiet("wo-cigar-curate-10"))
	f.tick()
	if len(f.digests()) != 2 || len(f.ledger().State.Digest.Pending) != 8 {
		t.Fatalf("eight lines sent within an hour of the last digest: %d digests", len(f.digests()))
	}
	f.clock.SetTime(second.Add(time.Hour))
	f.tick()
	if len(f.digests()) != 3 || len(digestLines(f.digests()[2].Get("message"))) != 8 {
		t.Fatalf("eight lines did not send early: %d digests", len(f.digests()))
	}
	// Nothing pending: nothing is sent, however long.
	for _, step := range []time.Duration{24 * time.Hour, 48 * time.Hour, time.Minute} {
		f.clock.Step(step)
		f.tick()
	}
	if len(f.digests()) != 3 || f.push.count() != 3 {
		t.Fatalf("sent with nothing pending: %v", f.push.titles())
	}
}

// Proof 3: priority -1, the fixed template, no note text, the one link, and
// the cut at 950 characters with the footer and its count. The record is
// content-free: its time, state, line count and request id.
func TestSummonDigestText(t *testing.T) {
	t.Run("template", func(t *testing.T) {
		f := newSummonFix(t)
		f.start()
		f.add(f.quiet("wo-cigar-curate-1"))
		f.clock.Step(time.Second)
		stopped := f.close(f.session("wo-upgrade-2", v1alpha1.LaneUpgrade), v1alpha1.OutcomeFailed, summonoutcome.Unreported, summonoutcome.ReasonStopped, "stopped by https://evil.example")
		rem := f.close(f.session("rem-3", v1alpha1.LaneRemediation), v1alpha1.OutcomeEscalated, summonoutcome.Unreported, "Weird_Reason!", "")
		f.add(stopped, rem)
		f.clock.Step(24 * time.Hour)
		f.tick()
		d := f.digests()
		if len(d) != 1 {
			t.Fatalf("pages %v", f.push.titles())
		}
		want := "[dev-env] digest: 3 lines about summoned sessions that did not page.\n" +
			"Each line: session, lane, state, reason. A line with state capped is a page the hourly cap held back; its last word is that page's event.\n" +
			"wo-cigar-curate-1 curation done done\n" +
			"rem-3 remediation escalated unknown\n" +
			"wo-upgrade-2 upgrade failed stopped"
		if got := d[0].Get("message"); got != want {
			t.Fatalf("message\n%q\nwant\n%q", got, want)
		}
		if d[0].Get("priority") != "-1" || d[0].Get("url") != summonConsole || d[0].Get("url_title") != "Open dev-env sessions" {
			t.Fatalf("priority %q url %q %q", d[0].Get("priority"), d[0].Get("url"), d[0].Get("url_title"))
		}
		l := f.ledger()
		r := digestRecords(l)
		if len(r) != 1 || r[0].Title != summonDigestTitle || r[0].Body != "digest of 3 lines, 0 left out" || r[0].Priority != -1 || r[0].URL != "" ||
			r[0].State != pages.StateSent || r[0].RequestID != "req-1" || r[0].Key != fmt.Sprintf("summon/digest/%d", f.clock.Now().Unix()) {
			t.Fatalf("record %+v", r)
		}
		// Nothing of a note, a link or the text is stored or logged; what stays
		// of the lines once sent is their keys and the attempt's time.
		raw := f.rawLedger()
		for _, forbidden := range []string{"NOTE-TEXT", "evil", "console.example", "Each line", summonTokenCanary, summonUserCanary} {
			if strings.Contains(raw, forbidden) || strings.Contains(f.logs.String(), forbidden) {
				t.Fatalf("%q stored or logged", forbidden)
			}
		}
		if len(l.State.Digest.Pending) != 0 || len(l.State.Digest.Listed) != 3 || l.State.Digest.Listed[summonKey(rem, summonEventDigest)] != f.clock.Now().Unix() {
			t.Fatalf("digest state %+v", l.State.Digest)
		}
		for _, s := range []*v1alpha1.AgentSession{stopped, rem} {
			if e := l.State.Events[summonKey(s, summonEventDigest)]; e.State != string(pages.StateSent) || e.Session != s.Name {
				t.Fatalf("entry %+v", e)
			}
		}
	})
	t.Run("cut-at-950", func(t *testing.T) {
		f := newSummonFix(t)
		f.start()
		var names []string
		for i := 0; i < 14; i++ {
			name := fmt.Sprintf("wo-cigar-curate-%02d-%s", i, strings.Repeat("x", 40))
			names = append(names, name)
			f.add(f.quiet(name))
			f.clock.Step(time.Second)
		}
		f.clock.Step(time.Hour) // an early digest waits an hour from the watermark
		f.tick()
		d := f.digests()
		if len(d) != 1 {
			t.Fatalf("pages %v", f.push.titles())
		}
		msg := d[0].Get("message")
		shown := digestLines(msg)
		left := len(names) - len(shown)
		footer := fmt.Sprintf("\n%d more lines left out to fit; they are not sent again.", left)
		if utf8.RuneCountInString(msg) > summonDigestBodyMax || left < 2 || !strings.HasSuffix(msg, footer) ||
			!strings.HasPrefix(msg, "[dev-env] digest: 14 lines about summoned sessions") {
			t.Fatalf("%d characters, %d shown: %q", utf8.RuneCountInString(msg), len(shown), msg)
		}
		for i, line := range shown {
			if line != names[i]+" curation done done" {
				t.Fatalf("line %d %q", i, line)
			}
		}
		// The cut keeps the longest run of whole lines that fits: one more would not.
		withOne := strings.TrimSuffix(msg, footer) + "\n" + names[len(shown)] + " curation done done" +
			fmt.Sprintf("\n%d more lines left out to fit; they are not sent again.", left-1)
		if utf8.RuneCountInString(withOne) <= summonDigestBodyMax {
			t.Fatalf("the cut left out a line that fits: %d", utf8.RuneCountInString(withOne))
		}
		// The lines left out are in this attempt: listed, counted, never sent.
		l := f.ledger()
		if r := digestRecords(l); len(r) != 1 || r[0].Body != fmt.Sprintf("digest of 14 lines, %d left out", left) {
			t.Fatalf("record %+v", r)
		}
		if len(l.State.Digest.Listed) != 14 || len(l.State.Digest.Pending) != 0 {
			t.Fatalf("listed %d pending %d", len(l.State.Digest.Listed), len(l.State.Digest.Pending))
		}
		f.clock.Step(48 * time.Hour)
		f.tick()
		if len(f.digests()) != 1 {
			t.Fatal("a line left out was sent again")
		}
	})
}

// summonCrashStore stops the keeper right after save number crashAt commits:
// a panic the tick recovers, so nothing after that write runs.
type summonCrashStore struct {
	summonPageStore
	n, crashAt int
}

func (s *summonCrashStore) save(ctx context.Context, l *summonPageLedger) error {
	s.n++
	err := s.summonPageStore.save(ctx, l)
	if err == nil && s.n == s.crashAt {
		panic("the keeper stopped here")
	}
	return err
}

// Proof 4: each outcome is in exactly one digest attempt. The reservation is
// durable before the send; across a keeper restart between the write-ahead
// and the send, after an unknown or refused send, with two pagers competing
// for one reservation, after a save that commits but answers an error, and
// after a save that lands past the dispatch deadline, no digest goes twice
// and no line is in two attempts.
func TestSummonDigestOnceAcrossRestartsAndRaces(t *testing.T) {
	// eight adds eight lines and steps an hour, so the next tick's early
	// digest is due (fix round 1, ruling 1).
	eight := func(f *summonFix) []*v1alpha1.AgentSession {
		var out []*v1alpha1.AgentSession
		for i := 1; i <= 8; i++ {
			s := f.quiet(fmt.Sprintf("wo-cigar-curate-%d", i))
			out = append(out, s)
			f.add(s)
		}
		f.clock.Step(time.Hour)
		return out
	}
	// after runs two restarts and ticks, a day apart, and checks one attempt:
	// sends digests sent, its record in state, every line listed once.
	after := func(t *testing.T, f *summonFix, lines []*v1alpha1.AgentSession, sends int, state pages.State) {
		t.Helper()
		for i := 0; i < 2; i++ {
			f.restart()
			f.clock.Step(25 * time.Hour)
			f.tick()
		}
		l := f.ledger()
		r := digestRecords(l)
		if len(f.digests()) != sends || len(r) != 1 || r[0].State != state || r[0].Attempts != 1 {
			t.Fatalf("digests %d records %+v, want %d sent and one %s attempt", len(f.digests()), r, sends, state)
		}
		if state != pages.StateSent && r[0].RequestID != "" {
			t.Fatal("a request id without an accepted send")
		}
		if len(l.State.Digest.Pending) != 0 || len(l.State.Digest.Listed) != len(lines) {
			t.Fatalf("pending %d listed %d", len(l.State.Digest.Pending), len(l.State.Digest.Listed))
		}
		for _, s := range lines {
			if l.State.Digest.Listed[summonKey(s, summonEventDigest)] != l.State.Digest.LastAt {
				t.Fatalf("%s not listed in the one attempt: %+v", s.Name, l.State.Digest)
			}
		}
	}
	t.Run("write-ahead-before-send", func(t *testing.T) {
		f := newSummonFix(t)
		f.start()
		lines := eight(f)
		f.push.before = func() {
			l := f.ledger()
			r := digestRecords(l)
			if len(l.State.Digest.Pending) != 0 || len(l.State.Digest.Listed) != 8 || len(r) != 1 || r[0].State != pages.StateRecorded {
				t.Errorf("the send came before the durable reservation: %+v %+v", l.State.Digest, r)
			}
		}
		f.tick()
		f.push.before = nil
		after(t, f, lines, 1, pages.StateSent)
	})
	t.Run("restart-between-write-ahead-and-send", func(t *testing.T) {
		f := newSummonFix(t)
		f.start()
		lines := eight(f)
		f.p.store = &summonCrashStore{summonPageStore: f.p.store, crashAt: 1}
		f.tick()
		if !strings.Contains(f.logs.String(), "summon page tick failed") || f.push.count() != 0 {
			t.Fatalf("the stop did not land between the write-ahead and the send: %d sends", f.push.count())
		}
		after(t, f, lines, 0, pages.StateRecorded)
	})
	for _, tc := range []struct {
		name   string
		status int
		body   string
		closed bool
		state  pages.State
	}{
		{name: "server-error", status: 503, state: pages.StateAmbiguous},
		{name: "unconfirmed-200", status: 200, body: `{"status":0}`, state: pages.StateAmbiguous},
		{name: "transport-error", closed: true, state: pages.StateAmbiguous},
		{name: "refused-400", status: 400, state: pages.StateFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSummonFix(t)
			f.start()
			lines := eight(f)
			f.push.status, f.push.body = tc.status, tc.body
			if tc.closed {
				f.push.srv.Close()
			}
			f.tick()
			sent := len(f.digests())
			f.push.status, f.push.body = 0, ""
			after(t, f, lines, sent, tc.state)
			for _, s := range lines {
				if e := f.ledger().State.Events[summonKey(s, summonEventDigest)]; e.State != string(tc.state) {
					t.Fatalf("entry %+v", e)
				}
			}
		})
	}
	t.Run("competing-pagers", func(t *testing.T) {
		f := newSummonFix(t)
		f.start()
		lines := eight(f)
		other := newSummonPager(f.opts, &kubeSummonSessions{reader: f.c, namespace: summonTestNS},
			&configMapSummonPageStore{client: f.c, name: summonLedgerName},
			&PushoverSender{URL: f.push.srv.URL, TokenFile: f.tokens[0], UserFile: f.tokens[1]}, f.clock, f.p.log)
		// The other pager reads, reserves and sends between this pager's read
		// and its reservation.
		first := f.updates + 1
		f.beforeUpdate = func(n int) {
			if n == first {
				other.tick(f.ctx)
			}
		}
		f.tick()
		f.beforeUpdate = nil
		if !strings.Contains(f.logs.String(), "summon digest reservation unconfirmed") {
			t.Fatalf("the losing pager did not log its refused reservation: %s", f.logs.String())
		}
		after(t, f, lines, 1, pages.StateSent)
	})
	t.Run("save-commits-then-errors", func(t *testing.T) {
		f := newSummonFix(t)
		f.start()
		lines := eight(f)
		first := f.updates + 1
		f.commitThenFail = func(n int) bool { return n == first }
		f.tick()
		f.commitThenFail = nil
		after(t, f, lines, 0, pages.StateRecorded)
	})
	t.Run("outcome-never-stored", func(t *testing.T) {
		f := newSummonFix(t)
		f.start()
		lines := eight(f)
		second := f.updates + 2
		f.failUpdate = func(n int) bool { return n == second }
		f.tick()
		f.failUpdate = nil
		if len(f.digests()) != 1 || !strings.Contains(f.logs.String(), "summon digest outcome unconfirmed") {
			t.Fatalf("digests %d logs %s", len(f.digests()), f.logs.String())
		}
		after(t, f, lines, 1, pages.StateRecorded)
	})
	t.Run("late-reservation", func(t *testing.T) {
		f := newSummonFix(t)
		f.start()
		lines := eight(f)
		first := f.updates + 1
		f.beforeUpdate = func(n int) {
			if n == first {
				f.clock.Step(noticeStoreTimeout + time.Second)
			}
		}
		f.tick()
		f.beforeUpdate = nil
		after(t, f, lines, 0, pages.StateFailed)
	})
}

// Proof 5: a close before the pager's first run is in no digest; one at its
// second or after is. A ledger story 782's keeper wrote (watermark 0) gets its
// watermark at the first run of this keeper, so its earlier closes and capped
// events stay out.
func TestSummonDigestWatermark(t *testing.T) {
	t.Run("fresh-ledger", func(t *testing.T) {
		f := newSummonFix(t)
		before := f.quiet("wo-cigar-curate-before")
		before.Status.Summon.Closed.At = metav1.NewTime(f.clock.Now().Add(-time.Second))
		at := f.quiet("wo-cigar-curate-at")
		at.Status.Summon.Closed.At = metav1.NewTime(f.clock.Now())
		f.add(before, at)
		since := f.start()
		later := f.quiet("wo-cigar-curate-after")
		f.add(later)
		f.clock.SetTime(since.Add(24 * time.Hour))
		f.tick()
		d := f.digests()
		if len(d) != 1 || strings.Join(digestLines(d[0].Get("message")), ",") != "wo-cigar-curate-at curation done done,wo-cigar-curate-after curation done done" {
			t.Fatalf("digests %v", d)
		}
		if _, ok := f.ledger().State.Events[summonKey(before, summonEventDigest)]; ok {
			t.Fatal("a close before the first run has an entry")
		}
		f.clock.Step(48 * time.Hour)
		f.tick()
		if len(f.digests()) != 1 {
			t.Fatal("the close before the first run was sent later")
		}
	})
	t.Run("story-782-ledger", func(t *testing.T) {
		f := newSummonFix(t)
		old := f.quiet("wo-cigar-curate-old")
		f.add(old)
		// Story 782's keeper: an entry it sent, an event it capped and the
		// summary that counted it, its page record, and the digest members
		// written empty.
		capKey := "00000000-0000-4000-8000-000000000777/failed"
		writeSummonLedger(t, f, fmt.Sprintf(`{"version":1,"events":{%q:{"at":%d,"state":"sent","session":"wo-upgrade-776"},%q:{"at":%d,"state":"capped","session":"wo-upgrade-777"},"more/1":{"at":%d,"state":"sent"}},"window":[],"capped":0,"more":1,`+
			`"digest":{"since":0,"lastAt":0,"pending":[],"listed":{}},"overflow":{}}`,
			"00000000-0000-4000-8000-000000000776/failed", f.clock.Now().Add(-time.Hour).Unix(), capKey, f.clock.Now().Add(-30*time.Minute).Unix(), f.clock.Now().Add(-20*time.Minute).Unix()), true)
		since := f.start()
		l := f.ledger()
		if l.State.Digest.Since != since.Unix() || len(l.State.Digest.Pending) != 0 || len(l.State.Events) != 3 || len(l.Pages.Records) != 1 || f.push.count() != 0 {
			t.Fatalf("the 782 ledger was not kept with a new watermark: %+v", l.State)
		}
		fresh := f.quiet("wo-cigar-curate-new")
		f.add(fresh)
		f.clock.SetTime(since.Add(24 * time.Hour))
		f.tick()
		d := f.digests()
		if len(d) != 1 || strings.Join(digestLines(d[0].Get("message")), ",") != "wo-cigar-curate-new curation done done" {
			t.Fatalf("digests %v", f.push.titles())
		}
		if _, ok := f.ledger().State.Digest.Listed[capKey]; ok || strings.Contains(d[0].Get("message"), "wo-upgrade-777") {
			t.Fatalf("an event capped before the first run was listed: %+v", f.ledger().State)
		}
	})
}

// writeSummonLedger replaces the ledger's state.json, and with records adds one
// page record as story 782's keeper writes it.
func writeSummonLedger(t *testing.T, f *summonFix, state string, records bool) {
	t.Helper()
	var cm corev1.ConfigMap
	if err := f.c.Get(f.ctx, summonLedgerName, &cm); err != nil {
		t.Fatal(err)
	}
	cm.Data = map[string]string{summonPagesStateKey: state}
	if records {
		doc := pages.New()
		if _, err := doc.Begin(pages.Page{Key: "summon/00000000-0000-4000-8000-000000000776/failed", Sender: summonPageSender, Source: summonPageSource, Title: "[dev-env] failed"}, f.clock.Now().Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
		doc.Finish("summon/00000000-0000-4000-8000-000000000776/failed", pages.OutcomeSent, 200, "req-776", f.clock.Now().Add(-time.Hour))
		raw, err := doc.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		cm.Data[pages.ConfigMapKey] = string(raw)
	}
	if err := f.c.Update(f.ctx, &cm); err != nil {
		t.Fatal(err)
	}
}

// Proof 6: the events the hourly cap left out are listed in the next digest,
// a terminal page and a lifecycle event alike, each once, told apart from
// outcome lines by the state word capped and with the event last. They stay
// capped in the dedupe and are still counted in the more-events page.
func TestSummonDigestListsCappedEvents(t *testing.T) {
	f := newSummonFix(t)
	since := f.start()
	var failed []*v1alpha1.AgentSession
	for i := 1; i <= 7; i++ {
		s := f.failedSession(fmt.Sprintf("wo-upgrade-%d", i), time.Duration(20-i)*time.Second)
		failed = append(failed, s)
		f.add(s)
	}
	esc := f.session("esc-8", v1alpha1.LaneEscalation)
	esc.Spec.Mode = v1alpha1.ModeRemote
	esc.Status.Summon = &v1alpha1.SummonStatus{Pages: []v1alpha1.SummonPage{{Event: v1alpha1.PageEscalationHeld, At: metav1.NewTime(f.clock.Now().Add(-time.Second))}}}
	f.add(esc, f.quiet("wo-cigar-curate-9"))
	f.tick()
	if f.push.count() != 6 || len(f.digests()) != 0 {
		t.Fatalf("pages %v", f.push.titles())
	}
	l := f.ledger()
	if l.State.Capped != 2 || len(l.State.Digest.Pending) != 3 {
		t.Fatalf("capped %d pending %+v", l.State.Capped, l.State.Digest.Pending)
	}
	f.clock.SetTime(since.Add(24 * time.Hour))
	f.tick()
	d := f.digests()
	if len(d) != 1 {
		t.Fatalf("pages %v", f.push.titles())
	}
	if got := strings.Join(digestLines(d[0].Get("message")), ","); got != "wo-cigar-curate-9 curation done done,wo-upgrade-7 upgrade capped failed,esc-8 escalation capped escalation-held" {
		t.Fatalf("lines %q", got)
	}
	l = f.ledger()
	for _, key := range []string{summonKey(failed[6], "failed"), summonKey(esc, "escalation-held")} {
		if e := l.State.Events[key]; e.State != summonStateCapped || l.State.Digest.Listed[key] != f.clock.Now().Unix() {
			t.Fatalf("capped entry %s: %+v listed %d", key, e, l.State.Digest.Listed[key])
		}
	}
	if f.sentFor("wo-upgrade-7") != 0 || f.push.count() != 8 {
		t.Fatalf("a capped event was paged: %v", f.push.titles())
	}
	// The summary that went when the window had room still counted both.
	if !strings.HasPrefix(f.push.form(6).Get("message"), "[dev-env] more-events: 2 more events were not paged") {
		t.Fatalf("summary %q", f.push.form(6).Get("message"))
	}
	f.clock.Step(48 * time.Hour)
	f.tick()
	if len(f.digests()) != 1 {
		t.Fatal("a capped event was listed twice")
	}
}

// Proof 7: the digest is outside the hourly cap. It takes no slot of the
// window, and a window a full hour of pages has filled does not delay it.
// Each early digest is an hour after the last (fix round 1, ruling 1).
func TestSummonDigestOutsideTheCap(t *testing.T) {
	f := newSummonFix(t)
	f.start()
	for i := 1; i <= 8; i++ {
		f.add(f.quiet(fmt.Sprintf("wo-cigar-curate-%d", i)))
	}
	f.clock.Step(time.Hour)
	f.tick()
	if len(f.digests()) != 1 || len(f.ledger().State.Window) != 0 {
		t.Fatalf("the digest took a slot: %+v", f.ledger().State.Window)
	}
	f.clock.Step(time.Hour)
	for i := 1; i <= 6; i++ {
		f.add(f.failedSession(fmt.Sprintf("wo-upgrade-%d", i), time.Duration(10-i)*time.Second))
	}
	for i := 9; i <= 16; i++ {
		f.add(f.quiet(fmt.Sprintf("wo-cigar-curate-%d", i)))
	}
	f.tick()
	l := f.ledger()
	if f.push.count() != 8 || len(f.digests()) != 2 || len(l.State.Window) != summonPagesCap || l.State.room(f.clock.Now()) || l.State.Capped != 0 {
		t.Fatalf("six pages and a digest: %v window %v capped %d", f.push.titles(), l.State.Window, l.State.Capped)
	}
	if f.push.form(7).Get("title") != summonDigestTitle || f.push.form(7).Get("priority") != "-1" {
		t.Fatalf("the digest was not sent beside a full window: %v", f.push.titles())
	}
	// Pages alone keep to six in any 60 minutes; the two digests are beside them.
	f.push.mu.Lock()
	var pageTimes []time.Time
	for i, form := range f.push.forms {
		if form.Get("title") != summonDigestTitle {
			pageTimes = append(pageTimes, f.push.times[i])
		}
	}
	f.push.mu.Unlock()
	if len(pageTimes) != summonPagesCap || !pageTimes[len(pageTimes)-1].Before(pageTimes[0].Add(time.Hour)) {
		t.Fatalf("page times %v", pageTimes)
	}
}

// Proof 8, second half: a ledger story 782's keeper wrote, with a page sent
// and the digest members empty, loads and works: pages go on, the digest
// starts an hour after the watermark (fix round 1, ruling 1), and nothing is
// reset.
func TestSummonDigestReadsAStory782Ledger(t *testing.T) {
	f := newSummonFix(t)
	writeSummonLedger(t, f, fmt.Sprintf(`{"version":1,"events":{"00000000-0000-4000-8000-000000000776/failed":{"at":%d,"state":"sent","session":"wo-upgrade-776"}},"window":[%d],"capped":0,"more":0,`+
		`"digest":{"since":0,"lastAt":0,"pending":[],"listed":{}},"overflow":{}}`, f.clock.Now().Add(-10*time.Minute).Unix(), f.clock.Now().Add(-10*time.Minute).Unix()), true)
	since := f.start()
	f.add(f.failedSession("wo-upgrade-1", time.Second))
	for i := 1; i <= 8; i++ {
		f.add(f.quiet(fmt.Sprintf("wo-cigar-curate-%d", i)))
	}
	f.tick()
	l := f.ledger()
	if f.push.count() != 1 || len(f.digests()) != 0 || l.State.Digest.Since != since.Unix() || len(l.State.Window) != 2 ||
		l.Pages.Find("summon/00000000-0000-4000-8000-000000000776/failed") == nil || len(l.State.Digest.Pending) != 8 {
		t.Fatalf("pages %v state %+v", f.push.titles(), l.State)
	}
	f.clock.SetTime(since.Add(time.Hour))
	f.tick()
	l = f.ledger()
	if f.push.count() != 2 || len(f.digests()) != 1 || l.Pages.Find("summon/00000000-0000-4000-8000-000000000776/failed") == nil || len(digestRecords(l)) != 1 {
		t.Fatalf("pages %v state %+v", f.push.titles(), l.State)
	}
	if strings.Contains(f.logs.String(), "summon page state unavailable") {
		t.Fatalf("logs %s", f.logs.String())
	}
}

// Pruning keeps to story 782's rule, an entry goes only on proof, and never
// takes an entry a digest still needs: a pending line's stays until it is
// listed, however gone its session. Once listed, the entry is pruned on proof
// with the next entry added, and its listed key goes at the next digest.
func TestSummonDigestPruneKeepsWhatTheDigestNeeds(t *testing.T) {
	f := newSummonFix(t)
	since := f.start()
	gone := f.quiet("wo-cigar-curate-gone")
	f.add(gone)
	f.tick()
	key := summonKey(gone, summonEventDigest)
	pendedAt := f.ledger().State.Events[key].At
	if err := f.c.Delete(f.ctx, gone); err != nil {
		t.Fatal(err)
	}
	// Two hours on, a new entry is added (so proof reads are made) while the
	// gone session's line is still pending: its entry stays as it was, not
	// pruned and written again.
	f.clock.Step(2 * time.Hour)
	f.add(f.failedSession("wo-upgrade-1", time.Second))
	f.tick()
	l := f.ledger()
	if e, ok := l.State.Events[key]; !ok || e.At != pendedAt || len(l.State.Digest.Pending) != 1 {
		t.Fatalf("a pending line's entry was pruned: %+v", l.State)
	}
	f.clock.SetTime(since.Add(24 * time.Hour))
	f.tick()
	if len(f.digests()) != 1 || digestLines(f.digests()[0].Get("message"))[0] != "wo-cigar-curate-gone curation done done" {
		t.Fatalf("the gone session's line was not sent: %v", f.push.titles())
	}
	// Listed now: the next tick that adds an entry proves it gone and prunes it.
	f.clock.Step(2 * time.Hour)
	next := f.quiet("wo-cigar-curate-next")
	f.add(next)
	f.tick()
	l = f.ledger()
	if _, ok := l.State.Events[key]; ok {
		t.Fatal("a listed line's entry of a gone session stayed")
	}
	if _, ok := l.State.Digest.Listed[key]; !ok {
		t.Fatal("a listed key went before the next digest")
	}
	f.clock.Step(25 * time.Hour)
	f.tick()
	l = f.ledger()
	if _, ok := l.State.Digest.Listed[key]; ok || len(f.digests()) != 2 {
		t.Fatalf("listed %v digests %d", l.State.Digest.Listed, len(f.digests()))
	}
	if _, ok := l.State.Digest.Listed[summonKey(next, summonEventDigest)]; !ok {
		t.Fatal("the next line was not listed")
	}
	// A capped event not yet listed (its line could not be pended, as when the
	// pending lines were full) is kept, though its session is gone, and pended
	// in the tick that keeps it. A held entry is no prune candidate, so no
	// proof read is spent on it.
	g := newSummonFix(t)
	capKey := "00000000-0000-4000-8000-000000000777/failed"
	writeSummonLedger(t, g, fmt.Sprintf(`{"version":1,"events":{%q:{"at":%d,"state":"capped","session":"wo-upgrade-777"}},"window":[],"capped":0,"more":0,`+
		`"digest":{"since":%d,"lastAt":0,"pending":[],"listed":{}},"overflow":{}}`, capKey, g.clock.Now().Add(-2*time.Hour).Unix(), g.clock.Now().Add(-3*time.Hour).Unix()), false)
	g.add(g.failedSession("wo-upgrade-1", time.Second))
	g.tick()
	l = g.ledger()
	if g.sessionGets != 0 || l.State.Events[capKey].State != summonStateCapped || len(l.State.Digest.Pending) != 1 || l.State.Digest.Pending[0].Key != capKey ||
		l.State.Digest.Pending[0].Session != "wo-upgrade-777" || l.State.Digest.Pending[0].Lane != "unknown" {
		t.Fatalf("proof reads %d, a capped event not yet listed: %+v", g.sessionGets, l.State)
	}
}

// A pending line whose entry a keeper without the digest pruned gets its entry
// back, so the dedupe is never empty beside the digest's record; and the store
// never saves a dedupe load would refuse as a reset.
func TestSummonDigestKeepsTheDedupeBesideItsRecords(t *testing.T) {
	f := newSummonFix(t)
	line := `{"key":"00000000-0000-4000-8000-000000000009/digest","session":"wo-cigar-curate-9","lane":"curation","state":"done","reason":"done","at":%d}`
	writeSummonLedger(t, f, fmt.Sprintf(`{"version":1,"events":{},"window":[],"capped":0,"more":0,"digest":{"since":%d,"lastAt":%d,"pending":[`+line+`],"listed":{}},"overflow":{}}`,
		f.clock.Now().Add(-48*time.Hour).Unix(), f.clock.Now().Add(-25*time.Hour).Unix(), f.clock.Now().Add(-26*time.Hour).Unix()), false)
	f.tick()
	l := f.ledger()
	if len(f.digests()) != 1 || len(digestRecords(l)) != 1 || l.State.Events["00000000-0000-4000-8000-000000000009/digest"].State != string(pages.StateSent) {
		t.Fatalf("digests %d state %+v", len(f.digests()), l.State)
	}
	f.restart()
	f.tick()
	if strings.Contains(f.logs.String(), "summon page state unavailable") {
		t.Fatalf("the ledger became unreadable: %s", f.logs.String())
	}
	// The store's own guard.
	l = f.ledger()
	l.State.Events = map[string]summonPageEntry{}
	if err := (&configMapSummonPageStore{client: f.c, name: summonLedgerName}).save(f.ctx, l); err == nil {
		t.Fatal("an empty dedupe beside page records was saved")
	}
}

// More closes than the pending lines' bound in one tick: 256 wait at once and
// go in one digest; the rest go in the next, each line in one attempt. While
// the one-hour floor makes the lines wait (fix round 1, ruling 1), the bound
// holds and its line is logged once a tick.
func TestSummonDigestPendingBound(t *testing.T) {
	f := newSummonFix(t)
	since := f.start()
	var all []client.Object
	for i := 0; i < summonPagesMaxLines+20; i++ {
		s := f.quiet(fmt.Sprintf("wo-cigar-curate-%03d", i))
		all = append(all, s)
		f.add(s)
	}
	const full = "summon digest pending capacity reached"
	for i, at := range []time.Duration{2 * time.Minute, 30 * time.Minute, time.Hour - time.Second} {
		f.clock.SetTime(since.Add(at))
		f.tick()
		if len(f.digests()) != 0 || len(f.ledger().State.Digest.Pending) != summonPagesMaxLines || strings.Count(f.logs.String(), full) != i+1 {
			t.Fatalf("%s: digests %d pending %d logs %s", at, len(f.digests()), len(f.ledger().State.Digest.Pending), f.logs.String())
		}
	}
	f.clock.SetTime(since.Add(time.Hour))
	f.tick()
	if len(f.digests()) != 1 || strings.Count(f.logs.String(), full) != 4 {
		t.Fatalf("digests %d logs %s", len(f.digests()), f.logs.String())
	}
	first := f.clock.Now()
	f.clock.Step(time.Minute)
	f.tick()
	if len(f.digests()) != 1 || len(f.ledger().State.Digest.Pending) != 20 || strings.Count(f.logs.String(), full) != 4 {
		t.Fatalf("the rest: digests %d pending %d", len(f.digests()), len(f.ledger().State.Digest.Pending))
	}
	f.clock.SetTime(first.Add(time.Hour - time.Second))
	f.tick()
	if len(f.digests()) != 1 {
		t.Fatal("the rest went within an hour of the first digest")
	}
	f.clock.SetTime(first.Add(time.Hour))
	f.tick()
	l := f.ledger()
	r := digestRecords(l)
	if len(f.digests()) != 2 || len(r) != 2 || r[0].Body != fmt.Sprintf("digest of %d lines, %d left out", summonPagesMaxLines, summonPagesMaxLines-len(digestLines(f.digests()[0].Get("message")))) ||
		!strings.HasPrefix(r[1].Body, "digest of 20 lines,") || len(l.State.Digest.Listed) != len(all) || len(l.State.Digest.Pending) != 0 {
		t.Fatalf("records %+v listed %d", r, len(l.State.Digest.Listed))
	}
}
