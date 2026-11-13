package keeper

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/pages"
	"github.com/thaynes43/dev-env/internal/summonledger"
)

// Story 793 (slice c of 742, DESIGN-064 6.8 "Overflow (N6, R1)"): one overflow
// page per caller per hour for the urgent creates the API refused;
// coordinator rulings on item 742 comment 7457 and answer op793-q-1, not
// rulings by Tom. Every send goes to the fake Pushover endpoint on the fake
// clock, and the create ledger is a ConfigMap in the fake client; writes are
// interleaved by hooks, never by goroutine timing. No test here reaches a
// cluster: nothing reads the process's cluster environment.

const (
	summonOverflowTitle = "[dev-env] overflow"
	summonCallerA       = "monitoring.alert-responder"
	summonCallerB       = "upgrade-agent.upgrade-shepherd"
)

var summonCreatesName = types.NamespacedName{Namespace: summonledger.Namespace, Name: summonledger.Name}

// refusals is one caller's create ledger entry as the API writes it, with n
// urgent refusals, the last at last.
func refusals(t *testing.T, n int64, last time.Time) string {
	t.Helper()
	e := summonledger.Entry{Clock: last.UTC(), UrgentRefusals: n}
	if n > 0 {
		at := last.UTC()
		e.LastUrgentRefusal = &at
	}
	raw, err := summonledger.Encode(e)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// creates writes the create ledger's data, creating the ConfigMap the first
// time, as the API's Update would leave it.
func (f *summonFix) creates(data map[string]string) {
	f.t.Helper()
	var cm corev1.ConfigMap
	err := f.c.Get(f.ctx, summonCreatesName, &cm)
	switch {
	case apierrors.IsNotFound(err):
		cm = corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: summonCreatesName.Namespace, Name: summonCreatesName.Name}, Data: data}
		if err := f.c.Create(f.ctx, &cm); err != nil {
			f.t.Fatal(err)
		}
	case err != nil:
		f.t.Fatal(err)
	default:
		cm.Data = data
		if err := f.c.Update(f.ctx, &cm); err != nil {
			f.t.Fatal(err)
		}
	}
}

// refused sets one caller's count in the create ledger, keeping the others.
func (f *summonFix) refused(caller string, n int64) {
	f.t.Helper()
	data := map[string]string{}
	var cm corev1.ConfigMap
	if err := f.c.Get(f.ctx, summonCreatesName, &cm); err == nil {
		for k, v := range cm.Data {
			data[k] = v
		}
	}
	data[caller] = refusals(f.t, n, f.clock.Now().Add(-time.Second))
	f.creates(data)
}

// overflows are the overflow pages the fake endpoint received.
func (f *summonFix) overflows() []url.Values {
	f.push.mu.Lock()
	defer f.push.mu.Unlock()
	var out []url.Values
	for _, form := range f.push.forms {
		if form.Get("title") == summonOverflowTitle {
			out = append(out, form)
		}
	}
	return out
}

// overflowCounts is each overflow page's caller and count, in order, read
// from its fixed template.
func (f *summonFix) overflowCounts() []string {
	var out []string
	for _, form := range f.overflows() {
		var n int
		var rest string
		msg := form.Get("message")
		if _, err := fmt.Sscanf(msg, "[dev-env] overflow: %d urgent session %s", &n, &rest); err != nil {
			f.t.Fatalf("message %q: %v", msg, err)
		}
		i := strings.Index(msg, "from caller ")
		if i < 0 {
			f.t.Fatalf("message %q names no caller", msg)
		}
		caller := strings.Fields(msg[i+len("from caller "):])[0]
		out = append(out, fmt.Sprintf("%s=%d", caller, n))
	}
	return out
}

// overflowEntries are the overflow dedupe entries of the ledger.
func overflowEntries(l *summonPageLedger) map[string]summonPageEntry {
	out := map[string]summonPageEntry{}
	for k, e := range l.State.Events {
		if summonOverflowEntryKey(k) {
			out[k] = e
		}
	}
	return out
}

func overflowRecords(l *summonPageLedger) []pages.Record {
	var out []pages.Record
	for _, r := range l.Pages.Records {
		if r.Title == summonOverflowTitle {
			out = append(out, r)
		}
	}
	return out
}

// Proof 1: a caller's refused urgent creates produce one overflow page that
// names the caller and the count since its last page, at priority 0, with
// the console's sessions list as its one link and a content-free record. A
// second tick, a keeper restart and a new leader send nothing more.
func TestSummonOverflowPagesTheCallerAndTheCount(t *testing.T) {
	f := newSummonFix(t)
	f.refused(summonCallerA, 3)
	f.tick()
	if got := f.overflowCounts(); len(got) != 1 || got[0] != summonCallerA+"=3" || f.push.count() != 1 {
		t.Fatalf("pages %v", f.push.titles())
	}
	form := f.overflows()[0]
	want := "[dev-env] overflow: 3 urgent session creates from caller " + summonCallerA + " were refused and not yet paged. " +
		"Its queue of urgent sessions was full or its creates for the hour were spent, so no session was queued for them.\n" +
		"At most one overflow page goes for a caller in an hour; refusals after this one wait for the next."
	if form.Get("message") != want || form.Get("priority") != "0" || form.Get("url") != summonConsole || form.Get("url_title") != "Open dev-env sessions" {
		t.Fatalf("form %v", form)
	}
	l := f.ledger()
	if o := l.State.Overflow[summonCallerA]; o.LastPagedCount != 3 || o.LastPagedAt != f.clock.Now().Unix() {
		t.Fatalf("member %+v", l.State.Overflow)
	}
	entries := overflowEntries(l)
	recs := overflowRecords(l)
	if len(entries) != 1 || len(recs) != 1 {
		t.Fatalf("entries %+v records %+v", entries, recs)
	}
	for key, e := range entries {
		if key != summonOverflowKey(summonCallerA, f.clock.Now()) || e.State != string(pages.StateSent) || e.Session != "" {
			t.Fatalf("entry %s %+v", key, e)
		}
		r := recs[0]
		if r.Key != summonRecordKey(key) || r.State != pages.StateSent || r.Attempts != 1 || r.Priority != 0 || r.RequestID != "req-1" ||
			r.Correlation != summonCallerA || r.Body != "caller "+summonCallerA+", 3 urgent creates refused" || r.URL != "" {
			t.Fatalf("record %+v", r)
		}
	}
	// One refusal reads in the singular.
	g := newSummonFix(t)
	g.refused(summonCallerB, 1)
	g.tick()
	if msg := g.overflows()[0].Get("message"); !strings.HasPrefix(msg, "[dev-env] overflow: 1 urgent session create from caller "+summonCallerB+" was refused and not yet paged.") ||
		!strings.Contains(msg, "no session was queued for it.") {
		t.Fatalf("message %q", msg)
	}
	f.tick()
	f.restart()
	f.clock.Step(2 * time.Hour)
	f.tick()
	if f.push.count() != 1 {
		t.Fatalf("paged again: %v", f.push.titles())
	}
}

// Proof 2: at most one overflow page per caller per hour; the refusals in
// between are carried by the next page, none twice and none lost, over three
// hours.
func TestSummonOverflowOncePerCallerPerHour(t *testing.T) {
	f := newSummonFix(t)
	t0 := f.clock.Now()
	total := int64(0)
	for _, step := range []struct {
		at    time.Duration
		count int64
		want  []string // the overflow pages so far
	}{
		{0, 3, []string{summonCallerA + "=3"}},
		{10 * time.Minute, 5, nil},
		{59*time.Minute + 59*time.Second, 6, nil},
		{time.Hour, 6, []string{summonCallerA + "=3", summonCallerA + "=3"}},
		{time.Hour + 30*time.Minute, 6, nil},
		{2*time.Hour + 10*time.Minute, 7, []string{summonCallerA + "=3", summonCallerA + "=3", summonCallerA + "=1"}},
		{2*time.Hour + 40*time.Minute, 11, nil},
		{3*time.Hour + 9*time.Minute, 12, nil},
		{3*time.Hour + 10*time.Minute, 12, []string{summonCallerA + "=3", summonCallerA + "=3", summonCallerA + "=1", summonCallerA + "=5"}},
		{3*time.Hour + 11*time.Minute, 12, nil},
	} {
		f.clock.SetTime(t0.Add(step.at))
		f.refused(summonCallerA, step.count)
		before := len(f.overflows())
		f.tick()
		got := f.overflowCounts()
		if step.want == nil {
			if len(got) != before {
				t.Fatalf("%s: paged within the hour: %v", step.at, got)
			}
			continue
		}
		if strings.Join(got, ",") != strings.Join(step.want, ",") {
			t.Fatalf("%s: pages %v, want %v", step.at, got, step.want)
		}
	}
	for _, c := range f.overflowCounts() {
		var n int64
		if _, err := fmt.Sscanf(strings.TrimPrefix(c, summonCallerA+"="), "%d", &n); err != nil {
			t.Fatal(err)
		}
		total += n
	}
	if total != 12 {
		t.Fatalf("the pages carried %d refusals, the ledger counted 12", total)
	}
	// Each page was an hour or more after the one before.
	f.push.mu.Lock()
	times := append([]time.Time(nil), f.push.times...)
	f.push.mu.Unlock()
	for i := 1; i < len(times); i++ {
		if times[i].Sub(times[i-1]) < time.Hour {
			t.Fatalf("pages %s apart", times[i].Sub(times[i-1]))
		}
	}
}

// Proof 3: a count lower than the last paged one (its key reset or removed)
// pages the new count and makes it the reference. A removed key keeps its
// member, with its reference at 0 and its hour; a reset inside the hour waits
// for the hour and then pages everything its key holds.
func TestSummonOverflowLowerCountPagesTheNewCount(t *testing.T) {
	f := newSummonFix(t)
	t0 := f.clock.Now()
	f.refused(summonCallerA, 5)
	f.tick()
	// Reset to a lower count: the new count is new refusals.
	f.clock.SetTime(t0.Add(2 * time.Hour))
	f.refused(summonCallerA, 2)
	f.tick()
	if got := strings.Join(f.overflowCounts(), ","); got != summonCallerA+"=5,"+summonCallerA+"=2" {
		t.Fatalf("pages %s", got)
	}
	if o := f.ledger().State.Overflow[summonCallerA]; o.LastPagedCount != 2 {
		t.Fatalf("member %+v", o)
	}
	// The key removed: no page, the member kept with reference 0 and its hour.
	paged := f.clock.Now().Unix()
	f.clock.SetTime(t0.Add(3 * time.Hour))
	f.creates(map[string]string{summonCallerB: refusals(t, 0, f.clock.Now())})
	f.tick()
	o, kept := f.ledger().State.Overflow[summonCallerA]
	if !kept || o.LastPagedCount != 0 || o.LastPagedAt != paged || len(f.overflows()) != 2 {
		t.Fatalf("member %+v kept %v pages %v", o, kept, f.overflowCounts())
	}
	// The key back with one refusal: one page of one.
	f.clock.SetTime(t0.Add(4 * time.Hour))
	f.refused(summonCallerA, 1)
	f.tick()
	if got := f.overflowCounts(); len(got) != 3 || got[2] != summonCallerA+"=1" {
		t.Fatalf("pages %v", got)
	}
	t4 := f.clock.Now()
	// Reset to zero inside the hour, then two refusals: they wait for the
	// hour and are paged whole.
	f.clock.SetTime(t4.Add(10 * time.Minute))
	f.refused(summonCallerA, 0)
	f.tick()
	if o := f.ledger().State.Overflow[summonCallerA]; o.LastPagedCount != 0 || o.LastPagedAt != t4.Unix() {
		t.Fatalf("a reset to zero: member %+v", o)
	}
	f.clock.SetTime(t4.Add(20 * time.Minute))
	f.refused(summonCallerA, 2)
	f.tick()
	if len(f.overflows()) != 3 {
		t.Fatalf("paged within the hour: %v", f.overflowCounts())
	}
	f.clock.SetTime(t4.Add(time.Hour))
	f.tick()
	if got := f.overflowCounts(); len(got) != 4 || got[3] != summonCallerA+"=2" {
		t.Fatalf("pages %v", got)
	}
}

// Proof 4: two callers are paged independently, each with its own count and
// its own hour.
func TestSummonOverflowCallersPagedIndependently(t *testing.T) {
	f := newSummonFix(t)
	t0 := f.clock.Now()
	f.refused(summonCallerA, 2)
	f.refused(summonCallerB, 5)
	f.tick()
	got := f.overflowCounts()
	sort.Strings(got)
	if strings.Join(got, ",") != summonCallerA+"=2,"+summonCallerB+"=5" {
		t.Fatalf("pages %v", got)
	}
	// A rises within its hour; B's first rise is after B's hour.
	f.clock.SetTime(t0.Add(30 * time.Minute))
	f.refused(summonCallerA, 4)
	f.tick()
	if len(f.overflows()) != 2 {
		t.Fatalf("paged within the hour: %v", f.overflowCounts())
	}
	f.clock.SetTime(t0.Add(time.Hour))
	f.refused(summonCallerB, 6)
	f.tick()
	got = f.overflowCounts()[2:]
	sort.Strings(got)
	if strings.Join(got, ",") != summonCallerA+"=2,"+summonCallerB+"=1" {
		t.Fatalf("pages %v", f.overflowCounts())
	}
	// A third caller, first seen later, has its own hour from its own page.
	f.clock.SetTime(t0.Add(time.Hour + 30*time.Minute))
	f.refused("third.caller", 1)
	f.refused(summonCallerA, 5)
	f.tick()
	if got := f.overflowCounts(); len(got) != 5 || got[4] != "third.caller=1" {
		t.Fatalf("pages %v", got)
	}
	l := f.ledger()
	if l.State.Overflow[summonCallerA].LastPagedCount != 4 || l.State.Overflow[summonCallerB].LastPagedCount != 6 || l.State.Overflow["third.caller"].LastPagedCount != 1 {
		t.Fatalf("members %+v", l.State.Overflow)
	}
}

// Proof 5: a create ledger that is missing, refused or bad as a whole sends
// no overflow page, logs one fixed line a tick, and leaves the pages and the
// digest working. One caller's entry that summonledger.Decode refuses makes
// only that caller wait (op793-q-1, answer 2), with one fixed line a tick and
// no value from the entry; the other callers page, and so does the waiting
// caller once its entry reads.
func TestSummonOverflowUnavailableLedgerSendsNothing(t *testing.T) {
	const unavailable = "summon overflow: the create ledger is unavailable"
	const unreadable = "summon overflow: a caller's create ledger entry is unreadable; that caller waits"
	for _, tc := range []struct {
		name  string
		setup func(f *summonFix)
	}{
		{"missing", func(f *summonFix) {}},
		{"refused", func(f *summonFix) {
			f.refused(summonCallerA, 3)
			f.createsErr = apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, summonledger.Name, errors.New("the Role rule is not installed"))
		}},
		{"bad as a whole", func(f *summonFix) {
			f.refused(summonCallerA, 3)
			f.createsErr = errors.New("the response could not be decoded into a ConfigMap")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSummonFix(t)
			since := f.start()
			tc.setup(f)
			f.createsGets = 0
			f.add(f.failedSession("wo-upgrade-1", time.Minute), f.quiet("wo-cigar-curate-2"))
			f.tick()
			if f.push.count() != 1 || f.sentFor("wo-upgrade-1") != 1 || len(f.overflows()) != 0 {
				t.Fatalf("pages %v", f.push.titles())
			}
			f.clock.SetTime(since.Add(24 * time.Hour))
			f.tick()
			if len(f.digests()) != 1 || len(f.overflows()) != 0 || len(f.ledger().State.Overflow) != 0 {
				t.Fatalf("pages %v", f.push.titles())
			}
			// One read and one line a tick: the start, the page and the digest.
			if n := strings.Count(f.logs.String(), unavailable); n != 3 || f.createsGets != 2 || strings.Contains(f.logs.String(), "the Role rule") || strings.Contains(f.logs.String(), "could not be decoded") {
				t.Fatalf("logged %d times: %s", n, f.logs.String())
			}
		})
	}
	t.Run("one bad entry beside a good one", func(t *testing.T) {
		f := newSummonFix(t)
		f.refused(summonCallerB, 2)
		f.creates(map[string]string{summonCallerA: `{"schema":"dev-env.summon-creates.v1","urgentRefusals":"SECRETVALUE"}`, summonCallerB: refusals(t, 2, f.clock.Now())})
		f.add(f.failedSession("wo-upgrade-1", time.Minute))
		f.tick()
		f.clock.Step(time.Minute)
		f.tick()
		if got := f.overflowCounts(); len(got) != 1 || got[0] != summonCallerB+"=2" || f.sentFor("wo-upgrade-1") != 1 {
			t.Fatalf("pages %v", f.push.titles())
		}
		if n := strings.Count(f.logs.String(), unreadable); n != 2 || strings.Contains(f.logs.String(), "SECRETVALUE") || strings.Contains(f.logs.String(), summonCallerA) {
			t.Fatalf("logged %d times: %s", n, f.logs.String())
		}
		if _, ok := f.ledger().State.Overflow[summonCallerA]; ok {
			t.Fatal("the unreadable caller got a member")
		}
		// Its entry reads again: it pages its count.
		f.clock.Step(time.Minute)
		f.refused(summonCallerA, 4)
		f.tick()
		if got := f.overflowCounts(); len(got) != 2 || got[1] != summonCallerA+"=4" {
			t.Fatalf("pages %v", got)
		}
	})
	t.Run("the grants key and other keys", func(t *testing.T) {
		f := newSummonFix(t)
		f.creates(map[string]string{summonledger.GrantsKey: `{"schema":"dev-env.summon-grants.v1","grants":[]}`,
			"not_a_caller": refusals(t, 9, f.clock.Now()), "nodot": refusals(t, 9, f.clock.Now())})
		f.tick()
		if f.push.count() != 0 || strings.Contains(f.logs.String(), "summon overflow") {
			t.Fatalf("pages %v logs %s", f.push.titles(), f.logs.String())
		}
	})
}

// Proof 6: once. A keeper stopped between the write-ahead and the send, an
// unknown send result, two pagers competing and a save that commits but
// answers an error: no second page, and those refusals are never paged again;
// later refusals still page.
func TestSummonOverflowOnceAcrossRestartsAndRaces(t *testing.T) {
	later := func(t *testing.T, f *summonFix, sends int) {
		t.Helper()
		for i := 0; i < 2; i++ {
			f.restart()
			f.clock.Step(2 * time.Hour)
			f.tick()
		}
		if f.push.count() != sends {
			t.Fatalf("paged again: %v", f.push.titles())
		}
		f.refused(summonCallerA, 5)
		f.tick()
		if got := f.overflowCounts(); len(got) == 0 || got[len(got)-1] != summonCallerA+"=2" {
			t.Fatalf("later refusals: %v", got)
		}
	}
	record := func(t *testing.T, f *summonFix) pages.Record {
		t.Helper()
		recs := overflowRecords(f.ledger())
		if len(recs) == 0 {
			t.Fatal("no record")
		}
		return recs[0]
	}
	t.Run("stopped between the write-ahead and the send", func(t *testing.T) {
		f := newSummonFix(t)
		f.refused(summonCallerA, 3)
		f.p.store = &summonCrashStore{summonPageStore: f.p.store, crashAt: 1}
		f.tick()
		if f.push.count() != 0 || !strings.Contains(f.logs.String(), "summon page tick failed") {
			t.Fatalf("pages %v", f.push.titles())
		}
		if o := f.ledger().State.Overflow[summonCallerA]; o.LastPagedCount != 3 {
			t.Fatalf("the reservation was not durable: %+v", o)
		}
		later(t, f, 0)
		if r := record(t, f); r.State != pages.StateRecorded || r.Attempts != 1 {
			t.Fatalf("record %+v", r)
		}
	})
	t.Run("an unknown result", func(t *testing.T) {
		f := newSummonFix(t)
		f.refused(summonCallerA, 3)
		f.push.statusFor = func(n int) int {
			if n == 1 {
				return http.StatusBadGateway
			}
			return 0
		}
		f.tick()
		if f.push.count() != 1 {
			t.Fatalf("pages %v", f.push.titles())
		}
		if e := overflowEntries(f.ledger())[summonOverflowKey(summonCallerA, f.clock.Now())]; e.State != string(pages.StateAmbiguous) {
			t.Fatalf("entry %+v", e)
		}
		later(t, f, 1)
		if r := record(t, f); r.State != pages.StateAmbiguous || r.Attempts != 1 {
			t.Fatalf("record %+v", r)
		}
	})
	t.Run("two pagers competing", func(t *testing.T) {
		f := newSummonFix(t)
		f.refused(summonCallerA, 3)
		other := newSummonPager(f.opts, &kubeSummonSessions{reader: f.c, namespace: summonTestNS},
			&configMapSummonPageStore{client: f.c, name: summonLedgerName},
			&PushoverSender{URL: f.push.srv.URL, TokenFile: f.tokens[0], UserFile: f.tokens[1]}, f.clock, f.p.log)
		other.creates = &kubeSummonCreates{reader: f.c}
		f.beforeUpdate = func(n int) {
			if n == 1 {
				other.tick(f.ctx)
			}
		}
		f.tick()
		f.beforeUpdate = nil
		if f.push.count() != 1 || !strings.Contains(f.logs.String(), "summon page reservation unconfirmed") {
			t.Fatalf("pages %v logs %s", f.push.titles(), f.logs.String())
		}
		later(t, f, 1)
		if r := record(t, f); r.State != pages.StateSent || r.Attempts != 1 {
			t.Fatalf("record %+v", r)
		}
	})
	t.Run("a save that commits and answers an error", func(t *testing.T) {
		f := newSummonFix(t)
		f.refused(summonCallerA, 3)
		f.commitThenFail = func(n int) bool { return n == 1 }
		f.tick()
		f.commitThenFail = nil
		if f.push.count() != 0 {
			t.Fatalf("pages %v", f.push.titles())
		}
		later(t, f, 0)
		if r := record(t, f); r.State != pages.StateRecorded || r.Attempts != 1 {
			t.Fatalf("record %+v", r)
		}
	})
	t.Run("a refused save", func(t *testing.T) {
		f := newSummonFix(t)
		f.refused(summonCallerA, 3)
		f.failUpdate = func(n int) bool { return n == 1 }
		f.tick()
		f.failUpdate = nil
		if f.push.count() != 0 || len(f.ledger().State.Overflow) != 0 {
			t.Fatalf("pages %v", f.push.titles())
		}
		// Nothing was reserved, so the next tick pages the same refusals once.
		f.tick()
		if got := f.overflowCounts(); len(got) != 1 || got[0] != summonCallerA+"=3" {
			t.Fatalf("pages %v", got)
		}
	})
}

// Proof 7: the cap. An overflow page counts toward the 6 pages in any 60
// minutes (ruling Q5): it goes after the drill and before the session pages
// of its tick (op793-q-1, answer 1). Past the cap it is capped: never paged,
// counted in the more-events page, listed in the next digest as
// "<caller> unknown capped overflow". It takes the caller's hour and keeps its
// refusals (story 793 fix round 1, ruling 1): the caller's next event waits
// an hour and its page carries them with the later ones.
func TestSummonOverflowCountsTowardTheCap(t *testing.T) {
	f := newSummonFix(t)
	f.opts.Drill = "d1"
	f.restart()
	since := f.start()
	f.refused(summonCallerA, 2)
	for i := 1; i <= 6; i++ {
		f.add(f.failedSession(fmt.Sprintf("wo-upgrade-%d", i), time.Duration(20-i)*time.Second))
	}
	f.tick()
	// The drill went at the start; the overflow page next; then 4 session
	// pages, and 2 were capped.
	titles := f.push.titles()
	if len(titles) != 6 || titles[0] != "DRILL [dev-env] drill" || titles[1] != summonOverflowTitle || f.ledger().State.Capped != 2 {
		t.Fatalf("pages %v capped %d", titles, f.ledger().State.Capped)
	}
	// Within that hour, B's refusals find the window full: capped.
	f.clock.Step(10 * time.Minute)
	f.refused(summonCallerB, 4)
	f.tick()
	l := f.ledger()
	capped := overflowEntries(l)
	if f.push.count() != 6 || l.State.Capped != 3 || l.State.Overflow[summonCallerB].LastPagedCount != 0 || l.State.Overflow[summonCallerB].LastPagedAt != f.clock.Now().Unix() {
		t.Fatalf("pages %v capped %d members %+v", f.push.titles(), l.State.Capped, l.State.Overflow)
	}
	key := summonOverflowKey(summonCallerB, f.clock.Now())
	if e, ok := capped[key]; !ok || e.State != summonStateCapped || e.Session != "" {
		t.Fatalf("capped entry %+v", capped)
	}
	cappedAt := f.clock.Now()
	// The window has room again: the summary counts the three capped events;
	// B's hour, from its capped event, has not passed.
	f.clock.SetTime(since.Add(2*time.Minute + time.Hour))
	f.tick()
	if !strings.HasPrefix(f.push.form(6).Get("message"), "[dev-env] more-events: 3 more events were not paged") || len(f.overflows()) != 1 {
		t.Fatalf("pages %v", f.push.titles())
	}
	// B's next event waits for the hour from its capped event, and its page
	// carries the capped refusals with the later one.
	f.clock.SetTime(cappedAt.Add(time.Hour - time.Second))
	f.refused(summonCallerB, 5)
	f.tick()
	if len(f.overflows()) != 1 {
		t.Fatalf("paged within the hour of the capped event: %v", f.overflowCounts())
	}
	f.clock.SetTime(cappedAt.Add(time.Hour))
	f.tick()
	if got := f.overflowCounts(); len(got) != 2 || got[1] != summonCallerB+"=5" {
		t.Fatalf("pages %v", got)
	}
	// The next digest lists the capped overflow event once, by its caller.
	f.clock.SetTime(since.Add(24 * time.Hour))
	f.tick()
	d := f.digests()
	if len(d) != 1 || !strings.Contains(d[0].Get("message"), "\n"+summonCallerB+" unknown capped overflow") {
		t.Fatalf("digest %v", d)
	}
	if l := f.ledger(); l.State.Digest.Listed[key] == 0 {
		t.Fatalf("not listed: %+v", l.State.Digest.Listed)
	}
	f.clock.Step(48 * time.Hour)
	f.tick()
	if len(f.digests()) != 1 {
		t.Fatalf("listed twice: %v", f.digests())
	}
}

// Proof 8a: the overflow member keeps at most summonOverflowMaxCallers
// callers. At the bound a new caller waits (its refusals stay counted in the
// create ledger) and the keeper logs one fixed line a tick; a member whose
// caller's key is gone, with its hour passed, is dropped for it, the oldest
// first; a dropped caller that comes back counts from 0.
func TestSummonOverflowCallerBound(t *testing.T) {
	f := newSummonFix(t)
	t0 := f.clock.Now()
	st := newSummonPageState()
	data := map[string]string{}
	for i := 0; i < summonOverflowMaxCallers; i++ {
		caller := fmt.Sprintf("ns-%02d.caller", i)
		st.Overflow[caller] = summonOverflowEntry{LastPagedCount: 1, LastPagedAt: t0.Add(-2*time.Hour + time.Duration(i)*time.Second).Unix()}
		data[caller] = refusals(t, 1, t0.Add(-3*time.Hour))
	}
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	writeSummonLedger(t, f, string(raw), false)
	data[summonCallerA] = refusals(t, 2, t0)
	f.creates(data)
	const full = "summon overflow caller capacity reached"
	for i := 1; i <= 2; i++ {
		f.tick()
		f.clock.Step(time.Minute)
		if f.push.count() != 0 || strings.Count(f.logs.String(), full) != i || len(f.ledger().State.Overflow) != summonOverflowMaxCallers {
			t.Fatalf("tick %d: pages %v logged %d members %d", i, f.push.titles(), strings.Count(f.logs.String(), full), len(f.ledger().State.Overflow))
		}
	}
	// Two callers' keys go: the older member is dropped for A.
	delete(data, "ns-03.caller")
	delete(data, "ns-07.caller")
	f.creates(data)
	f.tick()
	l := f.ledger()
	_, three := l.State.Overflow["ns-03.caller"]
	seven, kept := l.State.Overflow["ns-07.caller"]
	if got := f.overflowCounts(); len(got) != 1 || got[0] != summonCallerA+"=2" || three || !kept || seven.LastPagedCount != 0 || len(l.State.Overflow) != summonOverflowMaxCallers {
		t.Fatalf("pages %v members %d (ns-03 %v, ns-07 %+v)", got, len(l.State.Overflow), three, seven)
	}
	// ns-03 comes back with one refusal and no member: it counts from 0, and
	// ns-07's spare member makes the room.
	data["ns-03.caller"] = refusals(t, 1, f.clock.Now())
	f.creates(data)
	f.tick()
	l = f.ledger()
	if got := f.overflowCounts(); len(got) != 2 || got[1] != "ns-03.caller=1" || len(l.State.Overflow) != summonOverflowMaxCallers {
		t.Fatalf("pages %v members %d", got, len(l.State.Overflow))
	}
	if _, ok := l.State.Overflow["ns-07.caller"]; ok {
		t.Fatal("the spare member was kept")
	}
	// A spare member younger than an hour is not dropped.
	g := newSummonFix(t)
	st = newSummonPageState()
	data = map[string]string{}
	for i := 0; i < summonOverflowMaxCallers; i++ {
		caller := fmt.Sprintf("ns-%02d.caller", i)
		st.Overflow[caller] = summonOverflowEntry{LastPagedCount: 1, LastPagedAt: g.clock.Now().Add(-30 * time.Minute).Unix()}
		if i > 0 {
			data[caller] = refusals(t, 1, g.clock.Now())
		}
	}
	raw, _ = json.Marshal(st)
	writeSummonLedger(t, g, string(raw), false)
	data[summonCallerA] = refusals(t, 2, g.clock.Now())
	g.creates(data)
	g.tick()
	if g.push.count() != 0 || !strings.Contains(g.logs.String(), full) {
		t.Fatalf("pages %v", g.push.titles())
	}
}

// Proof 8b: the ledger with every bound filled stays under the store's byte
// cap (item 793 comment 7633). Each count is at its bound and each value at
// the longest this code writes: 2048 dedupe entries (a session UID and the
// longest event, a 63-character session name, the longest state; an overflow
// entry carries no name and is smaller), 24 window entries, 256 pending
// lines (the longest key, a 127-character caller and 64-character words),
// 2304 listed keys and 64 overflow members of 127-character callers. Listed
// keys are at most 2304: a digest's reservation keeps only listed keys that
// still have a dedupe entry (at most 2048) and adds its lines (at most 256);
// the store accepts up to 4096, which only a ledger this code did not write
// can hold, and at that bound the ledger is over the cap even without an
// overflow member. At the cap, a save refuses, nothing is sent, the tick logs
// "summon page reservation unconfirmed", and the ledger is unchanged.
func TestSummonOverflowFullLedgerUnderTheByteCap(t *testing.T) {
	const at = int64(9999999999)
	uid := func(i int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", i) }
	longEvent := string(v1alpha1.PageEscalationReminder)
	full := func(listed int) summonPageState {
		st := newSummonPageState()
		for i := 0; i < summonPagesMaxEvents-summonPagesMaxDrills; i++ {
			st.Events[uid(i)+"/"+longEvent] = summonPageEntry{At: at, State: string(pages.StateAmbiguous), Session: strings.Repeat("s", 63)}
		}
		for i := 0; i < summonPagesMaxDrills; i++ {
			st.Events[fmt.Sprintf("drill/%063d", i)] = summonPageEntry{At: at, State: string(pages.StateAmbiguous)}
		}
		for i := 0; i < 4*summonPagesCap; i++ {
			st.Window = append(st.Window, at)
		}
		st.Capped, st.More = 1<<30, 1<<30
		st.Digest.Since, st.Digest.LastAt = at, at
		for i := 0; i < summonPagesMaxLines; i++ {
			st.Digest.Pending = append(st.Digest.Pending, summonDigestLine{Key: uid(100000+i) + "/" + longEvent, Session: fmt.Sprintf("%063d.%063d", i, i),
				Lane: strings.Repeat("l", 64), State: strings.Repeat("t", 64), Reason: strings.Repeat("r", 64), At: at})
		}
		for i := 0; i < listed; i++ {
			st.Digest.Listed[uid(200000+i)+"/"+longEvent] = at
		}
		for i := 0; i < summonOverflowMaxCallers; i++ {
			st.Overflow[fmt.Sprintf("%063d.%063d", i, i)] = summonOverflowEntry{LastPagedCount: 1<<63 - 1, LastPagedAt: at}
		}
		return st
	}
	st := full(summonPagesMaxEvents + summonPagesMaxLines)
	if !st.valid() || len(st.Events) != summonPagesMaxEvents {
		t.Fatal("the full ledger is not one the store accepts")
	}
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > summonPagesMaxBytes {
		t.Fatalf("the full ledger is %d bytes, over the cap of %d", len(raw), summonPagesMaxBytes)
	}
	t.Logf("the full ledger is %d bytes; the cap is %d", len(raw), summonPagesMaxBytes)
	// The largest overflow dedupe entry is smaller than a session's.
	ov, _ := json.Marshal(summonPageEntry{At: at, State: string(pages.StateAmbiguous)})
	se, _ := json.Marshal(summonPageEntry{At: at, State: string(pages.StateAmbiguous), Session: strings.Repeat("s", 63)})
	if len(summonOverflowKey(fmt.Sprintf("%063d.%063d", 0, 0), time.Unix(at, 0)))+len(ov) >= len(uid(0)+"/"+longEvent)+len(se) {
		t.Fatal("an overflow entry is larger than a session entry")
	}
	// At the store's own bound of 4096 listed keys the ledger is over the cap.
	over := full(summonPagesMaxListed)
	if raw, _ = json.Marshal(over); !over.valid() || len(raw) <= summonPagesMaxBytes {
		t.Fatalf("the ledger at the store's bounds is %d bytes, valid %v", len(raw), over.valid())
	}
	// At the cap: a stored ledger a little under it, which the overflow page's
	// entry and member take over it. The save refuses, nothing is sent, and the
	// ledger is unchanged.
	f := newSummonFix(t)
	now := f.clock.Now().Unix()
	near := newSummonPageState()
	near.Digest.Since, near.Digest.LastAt = now, now
	for i := 0; i < summonPagesMaxEvents-1; i++ {
		near.Events[uid(i)+"/"+longEvent] = summonPageEntry{At: now, State: string(pages.StateSent), Session: strings.Repeat("s", 63)}
	}
	for i := 0; i < summonPagesMaxListed-96; i++ {
		near.Digest.Listed[uid(200000+i)+"/"+longEvent] = now
	}
	// Short pending lines of existing entries, one at a time, to within 150
	// bytes of the cap; the overflow page adds about 160.
	for i := 0; ; i++ {
		near.Digest.Pending = append(near.Digest.Pending, summonDigestLine{Key: uid(i) + "/" + longEvent, Session: "s", Lane: "l", State: "t", Reason: "r", At: now})
		if raw, _ = json.Marshal(near); len(raw) > summonPagesMaxBytes-150 {
			break
		}
	}
	if len(raw) > summonPagesMaxBytes || !near.valid() {
		t.Fatalf("the near-full ledger is %d bytes, valid %v", len(raw), near.valid())
	}
	writeSummonLedger(t, f, string(raw), false)
	before := f.rawLedger()
	f.refused(summonCallerA, 3)
	f.tick()
	if f.push.count() != 0 || !strings.Contains(f.logs.String(), "summon page reservation unconfirmed") || f.rawLedger() != before {
		t.Fatalf("at the cap: pages %v, logs %s", f.push.titles(), f.logs.String())
	}
}

// Proof 10: a ledger written by story 782's or 792's keeper, whose overflow
// member is empty, loads and pages; and one this keeper wrote, with members
// and overflow entries, is a ledger those keepers accept.
func TestSummonOverflowReadsAndWritesTheSharedSchema(t *testing.T) {
	f := newSummonFix(t)
	story782 := `{"version":1,"events":{"00000000-0000-4000-8000-000000000776/failed":{"at":1762948800,"state":"sent","session":"wo-upgrade-776"}},"window":[],"capped":0,"more":0,` +
		`"digest":{"since":0,"lastAt":0,"pending":[],"listed":{}},"overflow":{}}`
	writeSummonLedger(t, f, story782, true)
	f.refused(summonCallerA, 3)
	f.tick()
	if got := f.overflowCounts(); len(got) != 1 || got[0] != summonCallerA+"=3" {
		t.Fatalf("pages %v", f.push.titles())
	}
	// What this keeper wrote is what the store's schema accepts: a keeper of
	// story 782 or 792 reads it with the same load and keeps the member.
	var cm corev1.ConfigMap
	if err := f.c.Get(f.ctx, summonLedgerName, &cm); err != nil {
		t.Fatal(err)
	}
	var st summonPageState
	if err := json.Unmarshal([]byte(cm.Data[summonPagesStateKey]), &st); err != nil || !st.valid() {
		t.Fatalf("written ledger refused: %v", err)
	}
	for k := range overflowEntries(f.ledger()) {
		if !summonEventKey.MatchString(k) {
			t.Fatalf("key %s is outside the shared key shape", k)
		}
	}
	if len(st.Overflow) != 1 || len(st.Overflow) > summonPagesMaxCaller {
		t.Fatalf("members %+v", st.Overflow)
	}
}

// A member whose last overflow event is ahead of the keeper's clock holds
// that caller's next event until the clock has passed it by the hour, and the
// keeper says so in one fixed line a tick, with no name or value. Nothing is
// sent twice.
func TestSummonOverflowClockBehindTheLedger(t *testing.T) {
	f := newSummonFix(t)
	t0 := f.clock.Now()
	st := newSummonPageState()
	st.Overflow[summonCallerA] = summonOverflowEntry{LastPagedCount: 2, LastPagedAt: t0.Add(2 * time.Hour).Unix()}
	raw, _ := json.Marshal(st)
	writeSummonLedger(t, f, string(raw), false)
	f.refused(summonCallerA, 5)
	const ahead = "summon overflow ledger time is ahead of the keeper's clock"
	for i := 1; i <= 2; i++ {
		f.tick()
		f.clock.Step(time.Minute)
		if f.push.count() != 0 || strings.Count(f.logs.String(), ahead) != i || strings.Contains(f.logs.String(), summonCallerA) {
			t.Fatalf("tick %d: pages %v logs %s", i, f.push.titles(), f.logs.String())
		}
	}
	f.clock.SetTime(t0.Add(3*time.Hour - time.Second))
	f.tick()
	if f.push.count() != 0 || strings.Count(f.logs.String(), ahead) != 2 {
		t.Fatalf("pages %v", f.push.titles())
	}
	f.clock.SetTime(t0.Add(3 * time.Hour))
	f.tick()
	if got := f.overflowCounts(); len(got) != 1 || got[0] != summonCallerA+"=3" {
		t.Fatalf("pages %v", got)
	}
}

// A capped overflow event's digest line names its caller through the member
// whose hash its key carries; when the bound has dropped that member, the
// line says unknown. Its lane is unknown: the create ledger's count does not
// say which lane was refused.
func TestSummonOverflowCappedLineCaller(t *testing.T) {
	st := newSummonPageState()
	st.Overflow[summonCallerA] = summonOverflowEntry{LastPagedCount: 1, LastPagedAt: 1}
	at := time.Unix(1762950000, 0)
	e := summonPageEntry{At: at.Unix(), State: summonStateCapped}
	line := summonCappedLine(summonOverflowKey(summonCallerA, at), e, map[string]string{}, st.overflowCallers())
	if line.Session != summonCallerA || line.Lane != "unknown" || line.State != summonStateCapped || line.Reason != summonEventOverflow {
		t.Fatalf("line %+v", line)
	}
	if line := summonCappedLine(summonOverflowKey(summonCallerB, at), e, map[string]string{}, st.overflowCallers()); line.Session != "unknown" {
		t.Fatalf("a dropped member's line %+v", line)
	}
	// A session's capped line is unchanged.
	s := summonCappedLine("00000000-0000-4000-8000-000000000001/failed", summonPageEntry{At: 1, State: summonStateCapped, Session: "wo-upgrade-1"},
		map[string]string{"00000000-0000-4000-8000-000000000001": "upgrade"}, st.overflowCallers())
	if s.Session != "wo-upgrade-1" || s.Lane != "upgrade" || s.Reason != "failed" {
		t.Fatalf("a session's line %+v", s)
	}
}

// overflowTotals sums the counts of the overflow pages sent, by caller.
func (f *summonFix) overflowTotals() map[string]int64 {
	out := map[string]int64{}
	for _, c := range f.overflowCounts() {
		caller, n, ok := strings.Cut(c, "=")
		var v int64
		if _, err := fmt.Sscanf(n, "%d", &v); !ok || err != nil {
			f.t.Fatalf("count %q: %v", c, err)
		}
		out[caller] += v
	}
	return out
}

// cappedOverflows counts the capped overflow dedupe entries of the ledger.
func cappedOverflows(l *summonPageLedger) int {
	n := 0
	for _, e := range overflowEntries(l) {
		if e.State == summonStateCapped {
			n++
		}
	}
	return n
}

// Story 793 fix round 1, ruling 1 (PR 1130 comment 6846451603; the
// coordinator's, not Tom's): a capped overflow event takes its caller's hour
// and keeps its refusals. It advances lastPagedAt and leaves lastPagedCount
// as it was, zero for a caller with no member yet; the next overflow page for
// that caller carries them. So across a capped hour and the page after it,
// the counts in the pages that were attempted add up to the create ledger's
// count, and a capped event still takes the hour: no capped event every
// tick.
func TestSummonOverflowCappedEventKeepsItsRefusals(t *testing.T) {
	f := newSummonFix(t)
	t0 := f.clock.Now()
	// A is paged once, with 2 refusals, before the window fills.
	f.refused(summonCallerA, 2)
	f.tick()
	if got := f.overflowCounts(); len(got) != 1 || got[0] != summonCallerA+"=2" {
		t.Fatalf("pages %v", got)
	}
	// An hour later six session pages fill the window.
	f.clock.SetTime(t0.Add(time.Hour + time.Minute))
	for i := 1; i <= summonPagesCap; i++ {
		f.add(f.failedSession(fmt.Sprintf("wo-upgrade-%d", i), time.Duration(20-i)*time.Second))
	}
	f.tick()
	if f.push.count() != 1+summonPagesCap || f.ledger().State.Capped != 0 {
		t.Fatalf("pages %v", f.push.titles())
	}
	// A minute on, A (paged before) and B (never paged) have refusals and
	// the window is full: both events are capped. Each takes the hour; A's
	// reference stays 2, and B's member starts at reference 0.
	capped := t0.Add(time.Hour + 2*time.Minute)
	f.clock.SetTime(capped)
	f.refused(summonCallerA, 5)
	f.refused(summonCallerB, 3)
	f.tick()
	l := f.ledger()
	a, b := l.State.Overflow[summonCallerA], l.State.Overflow[summonCallerB]
	if a.LastPagedCount != 2 || a.LastPagedAt != capped.Unix() || b.LastPagedCount != 0 || b.LastPagedAt != capped.Unix() {
		t.Fatalf("members %+v", l.State.Overflow)
	}
	if l.State.Capped != 2 || cappedOverflows(l) != 2 || f.push.count() != 1+summonPagesCap {
		t.Fatalf("capped %d entries %+v pages %v", l.State.Capped, overflowEntries(l), f.push.titles())
	}
	// More refusals within the hour, the window still full: no new event,
	// capped or paged, on any tick.
	for i, step := range []struct {
		at   time.Duration
		a, b int64
	}{{3 * time.Minute, 6, 3}, {20 * time.Minute, 6, 4}, {58 * time.Minute, 7, 4}} {
		f.clock.SetTime(t0.Add(time.Hour + step.at))
		f.refused(summonCallerA, step.a)
		f.refused(summonCallerB, step.b)
		f.tick()
		l = f.ledger()
		if l.State.Capped != 2 || cappedOverflows(l) != 2 || f.push.count() != 1+summonPagesCap {
			t.Fatalf("step %d: capped %d entries %+v pages %v", i, l.State.Capped, overflowEntries(l), f.push.titles())
		}
		if o := l.State.Overflow[summonCallerA]; o.LastPagedCount != 2 || o.LastPagedAt != capped.Unix() {
			t.Fatalf("step %d: A %+v", i, o)
		}
	}
	// The window has room: the more-events page counts the two capped
	// events. A and B wait for the hour from their capped events.
	f.clock.SetTime(capped.Add(time.Hour - time.Second))
	f.tick()
	if f.push.count() != 2+summonPagesCap || !strings.HasPrefix(f.push.form(1+summonPagesCap).Get("message"), "[dev-env] more-events: 2 more events were not paged") ||
		len(f.overflows()) != 1 {
		t.Fatalf("pages %v", f.push.titles())
	}
	// At the hour, each pages every refusal not yet in an attempted page,
	// the capped ones included.
	f.clock.SetTime(capped.Add(time.Hour))
	f.tick()
	got := f.overflowCounts()[1:]
	sort.Strings(got)
	if strings.Join(got, ",") != summonCallerA+"=5,"+summonCallerB+"=4" {
		t.Fatalf("pages %v", f.overflowCounts())
	}
	if totals := f.overflowTotals(); totals[summonCallerA] != 7 || totals[summonCallerB] != 4 {
		t.Fatalf("the pages carried %v; the create ledger counts %s=7 and %s=4", totals, summonCallerA, summonCallerB)
	}
	l = f.ledger()
	if l.State.Overflow[summonCallerA].LastPagedCount != 7 || l.State.Overflow[summonCallerB].LastPagedCount != 4 {
		t.Fatalf("members %+v", l.State.Overflow)
	}
	// Nothing more without new refusals.
	f.clock.Step(2 * time.Hour)
	f.tick()
	if len(f.overflows()) != 3 {
		t.Fatalf("paged again: %v", f.overflowCounts())
	}
}

// Story 793 fix round 1, ruling 5: two overflow callers in one tick with one
// slot left. The one with the older last refusal pages; the other is capped
// and, by ruling 1, keeps its refusals for its next page. Both orders are
// tried, so the order is the last refusal's time, not the caller's name or
// key.
func TestSummonOverflowOlderRefusalPagesFirst(t *testing.T) {
	for _, tc := range []struct {
		name           string
		older, newer   string
		olderN, newerN int64
	}{
		{"B older", summonCallerB, summonCallerA, 2, 3},
		{"A older", summonCallerA, summonCallerB, 4, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSummonFix(t)
			t0 := f.clock.Now()
			for i := 1; i < summonPagesCap; i++ {
				f.add(f.failedSession(fmt.Sprintf("wo-upgrade-%d", i), time.Duration(20-i)*time.Second))
			}
			f.tick()
			if f.push.count() != summonPagesCap-1 {
				t.Fatalf("pages %v", f.push.titles())
			}
			t1 := t0.Add(time.Minute)
			f.clock.SetTime(t1)
			f.creates(map[string]string{
				tc.older: refusals(t, tc.olderN, t1.Add(-10*time.Minute)),
				tc.newer: refusals(t, tc.newerN, t1.Add(-time.Second)),
			})
			f.tick()
			l := f.ledger()
			if got := f.overflowCounts(); len(got) != 1 || got[0] != fmt.Sprintf("%s=%d", tc.older, tc.olderN) || l.State.Capped != 1 {
				t.Fatalf("pages %v capped %d", got, l.State.Capped)
			}
			key := summonOverflowKey(tc.newer, t1)
			if e := l.State.Events[key]; e.State != summonStateCapped {
				t.Fatalf("entries %+v", overflowEntries(l))
			}
			if o := l.State.Overflow[tc.newer]; o.LastPagedCount != 0 || o.LastPagedAt != t1.Unix() {
				t.Fatalf("the capped caller's member %+v", o)
			}
			// The window frees and the capped caller's hour passes: the
			// more-events page, then its page with every refusal.
			f.clock.SetTime(t1.Add(time.Hour))
			f.tick()
			if got := f.overflowCounts(); len(got) != 2 || got[1] != fmt.Sprintf("%s=%d", tc.newer, tc.newerN) {
				t.Fatalf("pages %v", f.push.titles())
			}
			if !strings.HasPrefix(f.push.form(summonPagesCap).Get("message"), "[dev-env] more-events: 1 more event was not paged") {
				t.Fatalf("pages %v", f.push.titles())
			}
		})
	}
}

// Story 793 fix round 1, ruling 3: what the order costs session pages.
// Overflow pages go before session pages (op793-q-1, answer 1). In an hour
// whose window opens with a more-events page owed, two overflow callers and
// six session pages: the more-events page, both overflow pages (the older
// last refusal first) and three session pages are sent, oldest first; three
// session pages are capped and the next hour's more-events page counts them.
// This shows one hour's usual split, 6 - k - 1 slots for session pages with
// k = 2, not a guarantee for every 60 minutes: the window slides, the
// more-events page can go twice inside one 60 minutes, a drill takes a slot
// once, and a send deadline can overlap a caller's hour by seconds (answer
// op793-fix1-q-1, the coordinator's, not Tom's).
func TestSummonOverflowOrderCostsSessionPages(t *testing.T) {
	f := newSummonFix(t)
	t0 := f.clock.Now()
	// The hour before: seven session pages, one capped.
	for i := 1; i <= summonPagesCap+1; i++ {
		f.add(f.failedSession(fmt.Sprintf("wo-before-%d", i), time.Duration(30-i)*time.Second))
	}
	f.tick()
	if f.push.count() != summonPagesCap || f.ledger().State.Capped != 1 {
		t.Fatalf("pages %v", f.push.titles())
	}
	t1 := t0.Add(time.Hour + time.Minute)
	f.clock.SetTime(t1)
	f.creates(map[string]string{
		summonCallerA: refusals(t, 2, t1.Add(-time.Minute)),
		summonCallerB: refusals(t, 3, t1.Add(-5*time.Minute)),
	})
	for i := 1; i <= summonPagesCap; i++ {
		f.add(f.failedSession(fmt.Sprintf("wo-upgrade-%d", i), time.Duration(20-i)*time.Second))
	}
	f.tick()
	titles := f.push.titles()[summonPagesCap:]
	if len(titles) != summonPagesCap || titles[0] != "[dev-env] "+summonEventMore || titles[1] != summonOverflowTitle || titles[2] != summonOverflowTitle {
		t.Fatalf("pages %v", titles)
	}
	if got := strings.Join(f.overflowCounts(), ","); got != summonCallerB+"="+"3,"+summonCallerA+"=2" {
		t.Fatalf("overflow pages %s", got)
	}
	l := f.ledger()
	for i := 1; i <= summonPagesCap; i++ {
		name := fmt.Sprintf("wo-upgrade-%d", i)
		if sent := f.sentFor(name); (i <= 3) != (sent == 1) || sent > 1 {
			t.Fatalf("%s sent %d times; pages %v", name, sent, titles)
		}
	}
	if l.State.Capped != 3 {
		t.Fatalf("capped %d", l.State.Capped)
	}
	// The next hour: the more-events page counts the three capped session
	// pages; nothing else is due.
	f.clock.SetTime(t1.Add(time.Hour + time.Minute))
	f.tick()
	if f.push.count() != 2*summonPagesCap+1 || !strings.HasPrefix(f.push.form(2*summonPagesCap).Get("message"), "[dev-env] more-events: 3 more events were not paged") {
		t.Fatalf("pages %v", f.push.titles())
	}
	f.assertRollingCap()
}

// Story 793 fix round 1, the advisory review's finding on PR 1131: with
// ruling 1 a member can have reference 0 while its caller's entry still holds
// refusals (it has had only capped events, or its key was reset to a lower
// count above 0). Such a member is never a spare. At the bound, a new caller
// whose last refusal is older, and so sorts first in the tick, cannot take
// its slot: the member pages its refusals, and the new caller waits. A member
// whose entry cannot be read is not a spare either. A member whose key is
// gone or reset to zero still makes room.
func TestSummonOverflowSpareOwesNothing(t *testing.T) {
	const full = "summon overflow caller capacity reached"
	const owing = "ns-63.caller" // the member a capped event left: reference 0
	setup := func(t *testing.T, owingEntry func(t0 time.Time) string) (*summonFix, map[string]string) {
		f := newSummonFix(t)
		t0 := f.clock.Now()
		st := newSummonPageState()
		data := map[string]string{}
		for i := 0; i < summonOverflowMaxCallers-1; i++ {
			caller := fmt.Sprintf("ns-%02d.caller", i)
			st.Overflow[caller] = summonOverflowEntry{LastPagedCount: 1, LastPagedAt: t0.Add(-2 * time.Hour).Unix()}
			data[caller] = refusals(t, 1, t0.Add(-3*time.Hour))
		}
		st.Overflow[owing] = summonOverflowEntry{LastPagedCount: 0, LastPagedAt: t0.Add(-2 * time.Hour).Unix()}
		raw, err := json.Marshal(st)
		if err != nil {
			t.Fatal(err)
		}
		writeSummonLedger(t, f, string(raw), false)
		data[owing] = owingEntry(t0)
		data[summonCallerA] = refusals(t, 2, t0.Add(-30*time.Minute)) // older: sorts first
		f.creates(data)
		return f, data
	}
	t.Run("a member that owes refusals keeps its slot", func(t *testing.T) {
		f, _ := setup(t, func(t0 time.Time) string { return refusals(t, 3, t0.Add(-time.Minute)) })
		f.tick()
		l := f.ledger()
		if got := f.overflowCounts(); len(got) != 1 || got[0] != owing+"=3" {
			t.Fatalf("pages %v", got)
		}
		if _, ok := l.State.Overflow[summonCallerA]; ok || l.State.Overflow[owing].LastPagedCount != 3 || len(l.State.Overflow) != summonOverflowMaxCallers {
			t.Fatalf("members %d, A present %v, owing %+v", len(l.State.Overflow), ok, l.State.Overflow[owing])
		}
		if strings.Count(f.logs.String(), full) != 1 {
			t.Fatalf("logs %s", f.logs.String())
		}
	})
	t.Run("an unreadable entry keeps its slot", func(t *testing.T) {
		f, data := setup(t, func(time.Time) string { return "not a create ledger entry" })
		f.tick()
		if f.push.count() != 0 || !strings.Contains(f.logs.String(), full) {
			t.Fatalf("pages %v", f.overflowCounts())
		}
		if o, ok := f.ledger().State.Overflow[owing]; !ok || o.LastPagedCount != 0 {
			t.Fatalf("the member %+v kept %v", o, ok)
		}
		data[owing] = refusals(t, 3, f.clock.Now().Add(-time.Second))
		f.creates(data)
		f.clock.Step(time.Minute)
		f.tick()
		if got := f.overflowCounts(); len(got) != 1 || got[0] != owing+"=3" {
			t.Fatalf("pages %v", got)
		}
	})
	t.Run("a member reset to zero makes room", func(t *testing.T) {
		f, _ := setup(t, func(t0 time.Time) string { return refusals(t, 0, t0.Add(-time.Hour)) })
		f.tick()
		l := f.ledger()
		if got := f.overflowCounts(); len(got) != 1 || got[0] != summonCallerA+"=2" {
			t.Fatalf("pages %v", got)
		}
		if _, ok := l.State.Overflow[owing]; ok || len(l.State.Overflow) != summonOverflowMaxCallers {
			t.Fatalf("members %d, the spare kept %v", len(l.State.Overflow), ok)
		}
	})
}
