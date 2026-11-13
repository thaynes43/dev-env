package keeper

import (
	"encoding/json"
	"fmt"
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

// Story 782, fix round 1: the proofs for the coordinator's rulings on the
// second-family review of PR 1102 (rulings in PR 1102 comment 6839324435; not
// a ruling by Tom). Every send goes to the fake Pushover endpoint on the fake
// clock; writes are interleaved by hooks, never by goroutine timing.

func (f *summonFix) failedSession(name string, closedAgo time.Duration) *v1alpha1.AgentSession {
	s := f.close(f.session(name, v1alpha1.LaneUpgrade), v1alpha1.OutcomeFailed, summonoutcome.Report, "failed", "")
	s.Status.Summon.Closed.At = metav1.NewTime(f.clock.Now().Add(-closedAgo))
	return s
}

func (f *summonFix) setLabel(s *v1alpha1.AgentSession, on bool) {
	f.t.Helper()
	var fresh v1alpha1.AgentSession
	if err := f.c.Get(f.ctx, client.ObjectKeyFromObject(s), &fresh); err != nil {
		f.t.Fatal(err)
	}
	if on {
		fresh.Labels = map[string]string{summonoutcome.LabelSummoned: summonoutcome.LabelSummonedValue}
	} else {
		fresh.Labels = nil
	}
	f.update(&fresh)
}

// sentFor counts the pages whose message names the session.
func (f *summonFix) sentFor(name string) int {
	f.push.mu.Lock()
	defer f.push.mu.Unlock()
	n := 0
	for _, form := range f.push.forms {
		if strings.Contains(form.Get("message"), "session "+name+" ") {
			n++
		}
	}
	return n
}

// assertRollingCap fails when any 60 minutes, on the fake clock, held more
// than six sends.
func (f *summonFix) assertRollingCap() {
	f.t.Helper()
	f.push.mu.Lock()
	times := append([]time.Time(nil), f.push.times...)
	f.push.mu.Unlock()
	for i := range times {
		n := 0
		for _, at := range times {
			if !at.Before(times[i]) && at.Before(times[i].Add(time.Hour)) {
				n++
			}
		}
		if n > summonPagesCap {
			f.t.Fatalf("%d sends in the 60 minutes from %s: %v", n, times[i].Format(time.RFC3339), times)
		}
	}
}

// F3: a session missing from the labelled list is not a deleted session. A
// sent, an ambiguous and a capped entry whose session lost its label survive
// a save after the hour; with the label back, across a restart, nothing is
// sent or counted twice.
func TestSummonPagesLabelAbsenceIsNotDeletion(t *testing.T) {
	f := newSummonFix(t)
	var all []*v1alpha1.AgentSession
	for i := 1; i <= 4; i++ {
		all = append(all, f.failedSession(fmt.Sprintf("wo-filler-%d", i), time.Duration(30-i)*time.Minute))
	}
	sent := f.failedSession("wo-sent", 20*time.Minute)
	ambiguous := f.failedSession("wo-ambiguous", 19*time.Minute)
	capped := f.failedSession("wo-capped", 18*time.Minute)
	all = append(all, sent, ambiguous, capped)
	f.add(all...)
	// The sixth send, the ambiguous session's, gets a server error.
	f.push.statusFor = func(n int) int {
		if n == 6 {
			return 503
		}
		return 0
	}
	f.tick()
	f.push.statusFor = nil
	if f.push.count() != 6 || f.sentFor("wo-sent") != 1 || f.sentFor("wo-ambiguous") != 1 || f.sentFor("wo-capped") != 0 {
		t.Fatalf("first tick: %v", f.push.titles())
	}
	want := map[string]string{summonKey(sent, "failed"): string(pages.StateSent), summonKey(ambiguous, "failed"): string(pages.StateAmbiguous), summonKey(capped, "failed"): summonStateCapped}
	check := func(when string) {
		t.Helper()
		l := f.ledger()
		for key, state := range want {
			if e, ok := l.State.Events[key]; !ok || e.State != state {
				t.Fatalf("%s: entry %s = %+v (present %v), want %s", when, key, e, ok, state)
			}
		}
	}
	check("after the first tick")
	if f.ledger().State.Capped != 1 {
		t.Fatalf("capped %d", f.ledger().State.Capped)
	}
	for _, s := range []*v1alpha1.AgentSession{sent, ambiguous, capped} {
		f.setLabel(s, false)
	}
	// After the hour, a new event elsewhere saves the ledger (with the
	// summary of the capped one).
	f.restart()
	f.clock.Step(61 * time.Minute)
	f.add(f.failedSession("wo-new", time.Minute))
	f.tick()
	if f.push.count() != 8 || f.push.form(6).Get("title") != "[dev-env] more-events" || f.sentFor("wo-new") != 1 {
		t.Fatalf("after the hour: %v", f.push.titles())
	}
	check("after a save past the hour")
	for _, s := range []*v1alpha1.AgentSession{sent, ambiguous, capped} {
		f.setLabel(s, true)
	}
	for i := 0; i < 2; i++ {
		f.restart()
		f.clock.Step(2 * time.Hour)
		f.tick()
	}
	if f.push.count() != 8 || f.sentFor("wo-sent") != 1 || f.sentFor("wo-ambiguous") != 1 || f.sentFor("wo-capped") != 0 {
		t.Fatalf("a restored label paged again: %v", f.push.titles())
	}
	check("after the label came back")
	if l := f.ledger(); l.State.Capped != 0 || l.State.More != 1 {
		t.Fatalf("the capped entry was counted twice: capped %d more %d", l.State.Capped, l.State.More)
	}
}

// F3: an entry is pruned on proof that its session is gone: a read of the
// stored name finds nothing, or finds a session with another UID. A name
// reused by a new session pages for the new one.
func TestSummonPagesPrunesOnProofOfDeletion(t *testing.T) {
	f := newSummonFix(t)
	gone := f.failedSession("wo-gone", 2*time.Minute)
	reused := f.failedSession("wo-reused", time.Minute)
	f.add(gone, reused)
	f.tick()
	for _, s := range []*v1alpha1.AgentSession{gone, reused} {
		if err := f.c.Delete(f.ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	// The same name, another UID, no label yet.
	again := f.session("wo-reused", v1alpha1.LaneUpgrade)
	again.Labels = nil
	f.add(again)
	f.clock.Step(61 * time.Minute)
	f.add(f.failedSession("wo-next", time.Minute))
	f.tick()
	l := f.ledger()
	for _, key := range []string{summonKey(gone, "failed"), summonKey(reused, "failed")} {
		if _, ok := l.State.Events[key]; ok {
			t.Fatalf("entry %s kept after its session was proved gone", key)
		}
	}
	// The new session with the reused name pages its own close once.
	var fresh v1alpha1.AgentSession
	if err := f.c.Get(f.ctx, client.ObjectKeyFromObject(again), &fresh); err != nil {
		t.Fatal(err)
	}
	f.close(&fresh, v1alpha1.OutcomeFailed, summonoutcome.Report, "failed", "")
	fresh.Labels = map[string]string{summonoutcome.LabelSummoned: summonoutcome.LabelSummonedValue}
	f.update(&fresh)
	f.tick()
	f.tick()
	if f.sentFor("wo-reused") != 2 || f.push.count() != 4 {
		t.Fatalf("pages %v", f.push.titles())
	}
}

// F3: at most 20 proof reads a tick; the rest wait for a later tick. A tick
// that saves nothing reads nothing.
func TestSummonPagesProofReadsBounded(t *testing.T) {
	f := newSummonFix(t)
	// The digest's watermark is two hours before the first tick, so that
	// tick's early digest lists the nineteen capped events at once, as before
	// story 792 fix round 1: its one-hour floor would otherwise hold their
	// entries from pruning until the second tick's digest (coordinator answer
	// op792-fix1-q-1, not a ruling by Tom).
	writeSummonLedger(t, f, fmt.Sprintf(`{"version":1,"events":{},"window":[],"capped":0,"more":0,"digest":{"since":%d,"lastAt":0,"pending":[],"listed":{}},"overflow":{}}`,
		f.clock.Now().Add(-2*time.Hour).Unix()), false)
	var gone []*v1alpha1.AgentSession
	for i := 1; i <= 25; i++ {
		gone = append(gone, f.failedSession(fmt.Sprintf("wo-gone-%d", i), time.Duration(60-i)*time.Minute))
	}
	f.add(gone...)
	f.tick() // six sent, nineteen capped
	for _, s := range gone {
		if err := f.c.Delete(f.ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	remaining := func() int {
		n := 0
		for _, s := range gone {
			if _, ok := f.ledger().State.Events[summonKey(s, "failed")]; ok {
				n++
			}
		}
		return n
	}
	f.clock.Step(61 * time.Minute)
	f.sessionGets = 0
	f.tick() // the summary of the nineteen capped is a new entry: it saves
	if f.sessionGets != summonPagesProofReads || remaining() != 25-summonPagesProofReads {
		t.Fatalf("reads %d, entries left %d", f.sessionGets, remaining())
	}
	f.sessionGets = 0
	f.clock.Step(time.Minute)
	f.tick() // nothing to save: nothing read
	if f.sessionGets != 0 {
		t.Fatalf("a tick that saves nothing read %d sessions", f.sessionGets)
	}
	f.add(f.failedSession("wo-next", time.Minute))
	f.tick()
	if f.sessionGets != 25-summonPagesProofReads || remaining() != 0 {
		t.Fatalf("next tick: reads %d, entries left %d", f.sessionGets, remaining())
	}
}

// F4: the window is pruned against the current time, never the reserved
// one. The review's schedule: a four-second first save, a restart, and two
// events at the hour's boundary; no 60 minutes hold more than six attempts.
func TestSummonPagesWindowPrunedAtNow(t *testing.T) {
	f := newSummonFix(t)
	for i := 1; i <= 5; i++ {
		f.add(f.failedSession(fmt.Sprintf("wo-upgrade-%d", i), time.Duration(10-i)*time.Minute))
	}
	start := f.clock.Now()
	f.beforeUpdate = func(n int) {
		if n == 1 {
			f.clock.Step(4 * time.Second) // the first reservation's save takes four seconds
		}
	}
	f.tick()
	f.beforeUpdate = nil
	if f.push.count() != 5 {
		t.Fatalf("first burst %d", f.push.count())
	}
	f.restart()
	f.clock.SetTime(start.Add(time.Hour))
	f.add(f.failedSession("wo-boundary-1", 0), f.failedSession("wo-boundary-2", 0))
	f.tick()
	if f.push.count() != 6 {
		t.Fatalf("at the boundary %d sends: %v", f.push.count(), f.push.titles())
	}
	f.assertRollingCap()
	if l := f.ledger(); l.State.Capped != 1 {
		t.Fatalf("capped %d", l.State.Capped)
	}
}

// summonPopulatedLedger has every member and every nested member filled.
const summonPopulatedLedger = `{"version":1,` +
	`"events":{"00000000-0000-4000-8000-000000000099/failed":{"at":1762948800,"state":"sent","session":"wo-upgrade-99"}},` +
	`"window":[1762948800],"capped":2,"more":1,` +
	`"digest":{"since":1762948800,"lastAt":1762950000,"pending":[{"key":"00000000-0000-4000-8000-000000000009/digest","session":"wo-cigar-curate-9","lane":"curation","state":"done","reason":"done","at":1762949000}],"listed":{"00000000-0000-4000-8000-000000000008/digest":1762950000}},` +
	`"overflow":{"upgrade-agent.alert-responder":{"lastPagedCount":3,"lastPagedAt":1762950000}}}`

// summonMutate deletes the member at path, or sets it to null.
func summonMutate(t *testing.T, path []any, null bool) string {
	t.Helper()
	var doc any
	if err := json.Unmarshal([]byte(summonPopulatedLedger), &doc); err != nil {
		t.Fatal(err)
	}
	parent := doc
	for _, step := range path[:len(path)-1] {
		switch p := parent.(type) {
		case map[string]any:
			parent = p[step.(string)]
		case []any:
			parent = p[step.(int)]
		}
	}
	last := path[len(path)-1]
	switch p := parent.(type) {
	case map[string]any:
		if _, ok := p[last.(string)]; !ok {
			t.Fatalf("no member %v", path)
		}
		if null {
			p[last.(string)] = nil
		} else {
			delete(p, last.(string))
		}
	case []any:
		if !null {
			t.Fatalf("an array element is only nulled: %v", path)
		}
		p[last.(int)] = nil
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// F5: every required member is present and not null. A populated ledger
// that lacks one, or holds it as null, is "state unavailable": no scan, no
// send.
func TestSummonPagesLedgerRequiresEveryMember(t *testing.T) {
	const event, line, listed, caller = "00000000-0000-4000-8000-000000000099/failed", 0, "00000000-0000-4000-8000-000000000008/digest", "upgrade-agent.alert-responder"
	members := [][]any{
		{"version"}, {"events"}, {"window"}, {"capped"}, {"more"}, {"digest"}, {"overflow"},
		{"events", event, "at"}, {"events", event, "state"},
		{"digest", "since"}, {"digest", "lastAt"}, {"digest", "pending"}, {"digest", "listed"},
		{"digest", "pending", line, "key"}, {"digest", "pending", line, "session"}, {"digest", "pending", line, "lane"},
		{"digest", "pending", line, "state"}, {"digest", "pending", line, "reason"}, {"digest", "pending", line, "at"},
		{"overflow", caller, "lastPagedCount"}, {"overflow", caller, "lastPagedAt"},
	}
	// Elements are not members; a null one is refused all the same.
	elements := [][]any{{"events", event}, {"window", 0}, {"digest", "pending", line}, {"digest", "listed", listed}, {"overflow", caller}}
	type tc struct {
		name, raw string
	}
	var cases []tc
	for _, m := range members {
		cases = append(cases, tc{fmt.Sprintf("missing %v", m), summonMutate(t, m, false)}, tc{fmt.Sprintf("null %v", m), summonMutate(t, m, true)})
	}
	for _, e := range elements {
		cases = append(cases, tc{fmt.Sprintf("null element %v", e), summonMutate(t, e, true)})
	}
	set := func(f *summonFix, raw string) {
		var cm corev1.ConfigMap
		if err := f.c.Get(f.ctx, summonLedgerName, &cm); err != nil {
			t.Fatal(err)
		}
		cm.Data = map[string]string{summonPagesStateKey: raw}
		if err := f.c.Update(f.ctx, &cm); err != nil {
			t.Fatal(err)
		}
	}
	// The populated ledger itself is read, scanned and sent from: the summary
	// of its two capped events, then the session's page, then (story 792) the
	// digest of its pending line, whose last digest was a year before.
	f := newSummonFix(t)
	f.add(f.failedSession("wo-upgrade-1", time.Minute))
	set(f, summonPopulatedLedger)
	f.tick()
	if f.push.count() != 3 || f.lists != 1 {
		t.Fatalf("the populated ledger was not read: sends %d lists %d logs %s", f.push.count(), f.lists, f.logs.String())
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newSummonFix(t)
			f.add(f.failedSession("wo-upgrade-1", time.Minute))
			set(f, c.raw)
			f.tick()
			if f.push.count() != 0 || f.lists != 0 || !strings.Contains(f.logs.String(), "summon page state unavailable") {
				t.Fatalf("sends %d lists %d logs %s; ledger %s", f.push.count(), f.lists, f.logs.String(), c.raw)
			}
		})
	}
}

// F6: a dotted host or address whose last label is two or more letters is
// redacted, with or without a port, path, query or fragment; a dotted number
// stays; every line and paragraph separator becomes a space.
func TestSummonPagesNoteRedactsHostsAndSeparators(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"see evil.example now", "see [link removed] now"},
		{"see evil.example:443 now", "see [link removed] now"},
		{"see evil.example:443/x?y#z now", "see [link removed] now"},
		{"see evil.example/path now", "see [link removed] now"},
		{"see evil.example?q=1 now", "see [link removed] now"},
		{"see evil.example#frag now", "see [link removed] now"},
		{"see EVIL.Example.", "see [link removed]."},
		{"edited main.go", "edited [link removed]"},
		{"edited internal/keeper/run.go", "edited internal/keeper/[link removed]"},
		{"(a.b.example)", "([link removed])"},
		{"release 2.105.0 is out", "release 2.105.0 is out"},
		{"v2.105.0-rc.1 and go1.27.3", "v2.105.0-rc.1 and go1.27.3"},
		{"10.0.0.1/admin", "[link removed]"},
		// The coordinator's answer op782-fix1-a-1: a dotted number with a port,
		// and a punycode last label.
		{"host 10.0.0.1:8080, then", "host [link removed], then"},
		{"see evil.xn--p1ai now", "see [link removed] now"},
		{"see evil.xn--p1ai:443/x now", "see [link removed] now"},
		{"2.105.0: done", "2.105.0: done"},
		{"e.g. this", "e.g. this"},
		{"one\u2028two\u2029three", "one two three"},
		{"one\u0085two\r\nthree\vfour\ffive", "one two  three four five"},
		{"evil\u2028.example", "evil .example"},
		// Story 782 fix round 2, F6: labels of any script, with and without a
		// port, path, query or fragment; a dotted number of any script stays.
		{"see é.example now", "see [link removed] now"},
		{"see é.example:443 now", "see [link removed] now"},
		{"see 例.example now", "see [link removed] now"},
		{"see 例.example:8443/x?y#z now", "see [link removed] now"},
		{"see пример.xn--p1ai now", "see [link removed] now"},
		{"see пример.xn--p1ai:443 now", "see [link removed] now"},
		{"see пример.рф/путь now", "see [link removed] now"},
		{"see ПРИМЕР.РФ.", "see [link removed]."},
		{"see उदाहरण.भारत now", "see [link removed] now"},
		{"see ตัวอย่าง.ไทย:443 now", "see [link removed] now"},
		{"see a_b.example now", "see a_[link removed] now"},
		{"see a.example-b now", "see [link removed]-b now"},
		{"see a.example.123 now", "see [link removed].123 now"},
		{"версия 2.105.0 и ٢.١٠٥.٠ готова", "версия 2.105.0 и ٢.١٠٥.٠ готова"},
		{"пример.рф1x", "пример.рф1x"},
		{"т.е. это", "т.е. это"},
	} {
		if got := summonNote(tc.in); got != tc.want {
			t.Errorf("summonNote(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	// A cut at the 200th character cannot leave a host behind it: the end of
	// "evil.co1x" is no host, its first 7 characters are.
	pad := strings.Repeat("a", 192) + " "
	for _, tc := range []struct{ in, want string }{
		{pad + "evil.co1x", pad + "[link r"},
		{pad + "x.co", pad + "[link r"},
		{strings.Repeat("b", 199) + " evil.example", strings.Repeat("b", 199) + " "},
		{strings.Repeat("c", 200) + "\u2028evil.example", strings.Repeat("c", 200)},
	} {
		got := summonNote(tc.in)
		if got != tc.want || utf8.RuneCountInString(got) > summonNoteMax {
			t.Errorf("summonNote(%q...) = %q, want %q", tc.in[180:], got, tc.want)
		}
	}
}

// F6: the proof in the fake endpoint's message.
func TestSummonPagesNoteRedactionInTheMessage(t *testing.T) {
	f := newSummonFix(t)
	// After redaction the prefix is redacted; the d's then put the 200th
	// character just after "evil.co" of "evil.co1x", which is no host.
	const redacted = "fixed in [link removed]; ask [link removed] or [link removed] line two para three, release 2.105.0, [link removed] [link removed]. "
	note := "fixed in main.go; ask evil.example:443 or evil.example\u2028line two\u2029para three, release 2.105.0, 10.0.0.1:8080 evil.xn--p1ai. " +
		strings.Repeat("d", summonNoteMax-len(redacted)-len(" evil.co")) + " evil.co1x and more"
	s := f.close(f.session("wo-upgrade-1", v1alpha1.LaneUpgrade), v1alpha1.OutcomeFailed, summonoutcome.Report, "failed", note)
	f.add(s)
	f.tick()
	if f.push.count() != 1 {
		t.Fatalf("sent %d", f.push.count())
	}
	msg := f.push.form(0).Get("message")
	_, shown, ok := strings.Cut(msg, "\nsession note: ")
	if !ok || strings.ContainsAny(shown, "\n\r\u2028\u2029") || strings.Contains(shown, "evil") || strings.Contains(shown, "main.go") {
		t.Fatalf("note in the message: %q", shown)
	}
	if !strings.HasPrefix(shown, redacted) ||
		!strings.HasSuffix(shown, "[link r") || utf8.RuneCountInString(shown) != summonNoteMax {
		t.Fatalf("note %q (%d characters)", shown, utf8.RuneCountInString(shown))
	}
	if strings.Count(msg, "\n") != 2 {
		t.Fatalf("the message has other lines: %q", msg)
	}
}

// F8: a used drill id is never forgotten. Distinct ids up to the bound are
// sent; one more is refused and logged, and nothing is sent; the first id,
// configured again after a restart, is not sent.
func TestSummonPagesDrillIDsRemembered(t *testing.T) {
	f := newSummonFix(t)
	drill := func(id string) {
		f.opts.Drill = id
		f.restart()
		f.clock.Step(time.Hour) // the window has room for each
		f.tick()
	}
	for i := 1; i <= summonPagesMaxDrills; i++ {
		drill(fmt.Sprintf("drill-%d", i))
	}
	if f.push.count() != summonPagesMaxDrills {
		t.Fatalf("sent %d of %d drills", f.push.count(), summonPagesMaxDrills)
	}
	drill(fmt.Sprintf("drill-%d", summonPagesMaxDrills+1))
	if f.push.count() != summonPagesMaxDrills || !strings.Contains(f.logs.String(), "summon page drill refused") {
		t.Fatalf("one drill past the bound: sent %d, logs %s", f.push.count(), f.logs.String())
	}
	if _, ok := f.ledger().State.Events[fmt.Sprintf("drill/drill-%d", summonPagesMaxDrills+1)]; ok {
		t.Fatal("a refused drill was recorded")
	}
	drill("drill-1")
	if f.push.count() != summonPagesMaxDrills {
		t.Fatalf("the first drill id was sent again: %d", f.push.count())
	}
	l := f.ledger()
	for i := 1; i <= summonPagesMaxDrills; i++ {
		if e, ok := l.State.Events[fmt.Sprintf("drill/drill-%d", i)]; !ok || e.State != string(pages.StateSent) {
			t.Fatalf("drill-%d forgotten: %+v", i, e)
		}
	}
	// A session's page still goes beside a full drill ledger.
	f.add(f.failedSession("wo-upgrade-1", time.Minute))
	f.tick()
	if f.sentFor("wo-upgrade-1") != 1 {
		t.Fatalf("pages %v", f.push.titles())
	}
}

// The review's proof table: two pagers competing for one reservation send
// once.
func TestSummonPagesCompetingPagersSendOnce(t *testing.T) {
	f := newSummonFix(t)
	s := f.failedSession("wo-upgrade-1", time.Minute)
	f.add(s)
	other := newSummonPager(f.opts, &kubeSummonSessions{reader: f.c, namespace: summonTestNS},
		&configMapSummonPageStore{client: f.c, name: summonLedgerName},
		&PushoverSender{URL: f.push.srv.URL, TokenFile: f.tokens[0], UserFile: f.tokens[1]}, f.clock, f.p.log)
	// The other pager reads, reserves and sends between this pager's read and
	// its reservation.
	f.beforeUpdate = func(n int) {
		if n == 1 {
			other.tick(f.ctx)
		}
	}
	f.tick()
	f.beforeUpdate = nil
	f.restart()
	f.tick()
	r := f.ledger().Pages.Find(summonRecordKey(summonKey(s, "failed")))
	if f.push.count() != 1 || r == nil || r.State != pages.StateSent || r.Attempts != 1 {
		t.Fatalf("sends %d record %+v", f.push.count(), r)
	}
	if !strings.Contains(f.logs.String(), "summon page reservation unconfirmed") {
		t.Fatalf("the losing pager did not log its refused reservation: %s", f.logs.String())
	}
}

// The review's proof table: a reservation whose save commits but answers an
// error sends nothing, and the page is never attempted again.
func TestSummonPagesCommittedButErroredReservation(t *testing.T) {
	f := newSummonFix(t)
	s := f.failedSession("wo-upgrade-1", time.Minute)
	f.add(s)
	f.commitThenFail = func(n int) bool { return n == 1 }
	f.tick()
	f.commitThenFail = nil
	for i := 0; i < 2; i++ {
		f.restart()
		f.clock.Step(2 * time.Hour)
		f.tick()
	}
	l := f.ledger()
	r := l.Pages.Find(summonRecordKey(summonKey(s, "failed")))
	if f.push.count() != 0 || r == nil || r.State != pages.StateRecorded || r.Attempts != 1 || l.State.Events[summonKey(s, "failed")].State != string(pages.StateRecorded) {
		t.Fatalf("sends %d record %+v entry %+v", f.push.count(), r, l.State.Events[summonKey(s, "failed")])
	}
}

// The review's proof table: a reservation saved after its dispatch deadline
// sends nothing, is recorded failed, and is never attempted again.
func TestSummonPagesLateReservationSendsNothing(t *testing.T) {
	f := newSummonFix(t)
	s := f.failedSession("wo-upgrade-1", time.Minute)
	f.add(s)
	f.beforeUpdate = func(n int) {
		if n == 1 {
			f.clock.Step(noticeStoreTimeout + time.Second)
		}
	}
	f.tick()
	f.beforeUpdate = nil
	for i := 0; i < 2; i++ {
		f.restart()
		f.clock.Step(2 * time.Hour)
		f.tick()
	}
	l := f.ledger()
	r := l.Pages.Find(summonRecordKey(summonKey(s, "failed")))
	if f.push.count() != 0 || r == nil || r.State != pages.StateFailed || r.Attempts != 1 || l.State.Events[summonKey(s, "failed")].State != string(pages.StateFailed) {
		t.Fatalf("sends %d record %+v", f.push.count(), r)
	}
}
