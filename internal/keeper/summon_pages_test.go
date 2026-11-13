package keeper

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	clocktesting "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/pages"
	"github.com/thaynes43/dev-env/internal/summonoutcome"
	"github.com/thaynes43/dev-env/internal/testsock"
)

// Story 782 (slice a of 742, DESIGN-064 6.8): the keeper's summoned-session
// pager. Every test sends to a fake Pushover endpoint with synthetic
// credentials, on a fake clock; writes are interleaved by hooks, never by
// goroutine timing.

const (
	summonTokenCanary = "SUMMONTOKENCANARY0123456789abcdef"
	summonUserCanary  = "SUMMONUSERCANARY0123456789abcdef"
	summonTestNS      = "dev-agents"
	summonTestCaller  = "upgrade-agent.upgrade-shepherd"
	summonConsole     = "https://console.example" + summonConsolePath
)

// summonPush is a fake Pushover endpoint. It answers {"status":1,"request":
// "req-N"} unless status or body say otherwise, and records when (on the fake
// clock) each request came.
type summonPush struct {
	srv    *httptest.Server
	clock  *clocktesting.FakeClock
	mu     sync.Mutex
	forms  []url.Values
	times  []time.Time
	status int
	body   string
	before func()
	// statusFor, when set, answers request n (1-based) with its status.
	statusFor func(n int) int
}

func newSummonPush(t *testing.T, clk *clocktesting.FakeClock) *summonPush {
	t.Helper()
	p := &summonPush{clock: clk}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(raw))
		p.mu.Lock()
		before := p.before
		p.mu.Unlock()
		if before != nil {
			before()
		}
		p.mu.Lock()
		p.forms = append(p.forms, form)
		p.times = append(p.times, p.clock.Now())
		n, status, body := len(p.forms), p.status, p.body
		if p.statusFor != nil {
			status = p.statusFor(n)
		}
		p.mu.Unlock()
		if status == 0 {
			status = http.StatusOK
		}
		if body == "" && status == http.StatusOK {
			body = fmt.Sprintf(`{"status":1,"request":"req-%d"}`, n)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *summonPush) count() int { p.mu.Lock(); defer p.mu.Unlock(); return len(p.forms) }
func (p *summonPush) form(i int) url.Values {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.forms[i]
}
func (p *summonPush) titles() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for _, f := range p.forms {
		out = append(out, f.Get("title"))
	}
	return out
}

type summonFix struct {
	t        *testing.T
	ctx      context.Context
	clock    *clocktesting.FakeClock
	c        client.WithWatch
	push     *summonPush
	tokens   [2]string // credential files
	opts     SummonPageOptions
	p        *summonPager
	logs     bytes.Buffer
	n        int
	lists    int
	cmGets   int
	updates  int
	listOpts []client.ListOption
	// hooks
	failUpdate   func(n int) bool // n is the 1-based update count
	beforeUpdate func(n int)
	// commitThenFail commits update n and then answers it with an error.
	commitThenFail func(n int) bool
	getErr         error
	listHook       func(list *v1alpha1.AgentSessionList, n int) error
	sessionGets    int
	// sessionGet, when set, sees each session read by name first; an error it
	// returns answers that read.
	sessionGet func(name string) error
}

var summonLedgerName = types.NamespacedName{Namespace: "dev-env-system", Name: DefaultSummonPagesConfigMap}

func newSummonFix(t *testing.T) *summonFix {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	f := &summonFix{t: t, ctx: context.Background(), clock: clocktesting.NewFakeClock(time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC))}
	f.c = fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: summonLedgerName.Namespace, Name: summonLedgerName.Name}}).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*corev1.ConfigMap); ok {
					f.cmGets++
					if f.getErr != nil {
						return f.getErr
					}
				}
				if _, ok := obj.(*v1alpha1.AgentSession); ok {
					f.sessionGets++
					if f.sessionGet != nil {
						if err := f.sessionGet(key.Name); err != nil {
							return err
						}
					}
				}
				return c.Get(ctx, key, obj, opts...)
			},
			List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if l, ok := list.(*v1alpha1.AgentSessionList); ok {
					f.lists++
					f.listOpts = opts
					if f.listHook != nil {
						if err := c.List(ctx, list, opts...); err != nil {
							return err
						}
						return f.listHook(l, f.lists)
					}
				}
				return c.List(ctx, list, opts...)
			},
			Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				if _, ok := obj.(*corev1.ConfigMap); ok {
					f.updates++
					if f.beforeUpdate != nil {
						f.beforeUpdate(f.updates)
					}
					if f.failUpdate != nil && f.failUpdate(f.updates) {
						return errors.New("update refused by the test")
					}
					if f.commitThenFail != nil && f.commitThenFail(f.updates) {
						if err := c.Update(ctx, obj, opts...); err != nil {
							return err
						}
						return errors.New("update committed, then answered with an error by the test")
					}
				}
				return c.Update(ctx, obj, opts...)
			},
		}).Build()
	f.push = newSummonPush(t, f.clock)
	dir := t.TempDir()
	f.tokens = [2]string{filepath.Join(dir, "token"), filepath.Join(dir, "user")}
	if err := os.WriteFile(f.tokens[0], []byte(summonTokenCanary), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.tokens[1], []byte(summonUserCanary), 0o600); err != nil {
		t.Fatal(err)
	}
	f.opts = SummonPageOptions{Enabled: true, PortalOrigin: "https://console.example", SessionNamespace: summonTestNS, StateConfigMap: DefaultSummonPagesConfigMap}
	f.restart()
	return f
}

// restart builds a new pager, a new store and a new reader over the same API
// state: a keeper restart, or a new leader in another pod.
func (f *summonFix) restart() {
	f.t.Helper()
	f.p = newSummonPager(f.opts, &kubeSummonSessions{reader: f.c, namespace: summonTestNS},
		&configMapSummonPageStore{client: f.c, name: summonLedgerName},
		&PushoverSender{URL: f.push.srv.URL, TokenFile: f.tokens[0], UserFile: f.tokens[1]}, f.clock,
		logr.FromSlogHandler(slog.NewJSONHandler(&f.logs, nil)))
	if f.p == nil {
		f.t.Fatal("valid options built no pager")
	}
}

func (f *summonFix) tick() { f.t.Helper(); f.p.tick(f.ctx) }

// session builds a summoned session with the label and spec.caller.
func (f *summonFix) session(name string, lane v1alpha1.Lane) *v1alpha1.AgentSession {
	f.n++
	return &v1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{Namespace: summonTestNS, Name: name, UID: types.UID(fmt.Sprintf("00000000-0000-4000-8000-%012d", f.n)),
			Labels: map[string]string{summonoutcome.LabelSummoned: summonoutcome.LabelSummonedValue}},
		Spec: v1alpha1.AgentSessionSpec{Caller: summonTestCaller, Lane: lane, Mode: v1alpha1.ModeTask},
	}
}

func (f *summonFix) close(s *v1alpha1.AgentSession, state v1alpha1.OutcomeState, source, reason, note string) *v1alpha1.AgentSession {
	at := metav1.NewTime(f.clock.Now().Add(-time.Minute))
	s.Status.Outcome = &v1alpha1.OutcomeStatus{State: state, Note: note, At: &at}
	if s.Status.Summon == nil {
		s.Status.Summon = &v1alpha1.SummonStatus{}
	}
	s.Status.Summon.Closed = &v1alpha1.SummonClosedStatus{At: at, Source: source, Reason: reason}
	return s
}

func (f *summonFix) add(objs ...*v1alpha1.AgentSession) {
	f.t.Helper()
	for _, s := range objs {
		if err := f.c.Create(f.ctx, s); err != nil {
			f.t.Fatal(err)
		}
	}
}

func (f *summonFix) update(s *v1alpha1.AgentSession) {
	f.t.Helper()
	if err := f.c.Update(f.ctx, s); err != nil {
		f.t.Fatal(err)
	}
}

func (f *summonFix) ledger() *summonPageLedger {
	f.t.Helper()
	l, err := (&configMapSummonPageStore{client: f.c, name: summonLedgerName}).load(f.ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	return l
}

func (f *summonFix) rawLedger() string {
	f.t.Helper()
	var cm corev1.ConfigMap
	if err := f.c.Get(f.ctx, summonLedgerName, &cm); err != nil {
		f.t.Fatal(err)
	}
	raw, _ := json.Marshal(cm.Data)
	return string(raw)
}

func summonKey(s *v1alpha1.AgentSession, event string) string { return string(s.UID) + "/" + event }

// Proof 1: one terminal page per closed session, one page per recorded
// lifecycle event; a second tick, a keeper restart and a new leader send
// nothing more.
func TestSummonPagesOncePerEvent(t *testing.T) {
	f := newSummonFix(t)
	lost := f.close(f.session("wo-upgrade-1", v1alpha1.LaneUpgrade), v1alpha1.OutcomeFailed, summonoutcome.Unreported, summonoutcome.ReasonPodTerminated, "unreported: pod-terminated")
	esc := f.session("esc-1", v1alpha1.LaneEscalation)
	esc.Spec.Mode = v1alpha1.ModeRemote
	esc.Status.Summon = &v1alpha1.SummonStatus{Pages: []v1alpha1.SummonPage{
		{Event: v1alpha1.PageEscalationHeld, At: metav1.NewTime(f.clock.Now().Add(-3 * time.Hour))},
		{Event: v1alpha1.PageEscalationReminder, At: metav1.NewTime(f.clock.Now().Add(-2 * time.Hour))},
	}}
	quiet := f.close(f.session("wo-cigar-curate-1", v1alpha1.LaneCuration), v1alpha1.OutcomeDone, summonoutcome.Report, "done", "all good")
	noCaller := f.close(f.session("no-caller", v1alpha1.LaneUpgrade), v1alpha1.OutcomeFailed, summonoutcome.Report, "failed", "")
	noCaller.Spec.Caller = ""
	noLabel := f.close(f.session("no-label", v1alpha1.LaneUpgrade), v1alpha1.OutcomeFailed, summonoutcome.Report, "failed", "")
	noLabel.Labels = nil
	f.add(lost, esc, quiet, noCaller, noLabel)

	f.tick()
	if got := f.push.titles(); strings.Join(got, ",") != "[dev-env] escalation-held,[dev-env] escalation-reminder,[dev-env] lost" {
		t.Fatalf("pages %v", got)
	}
	for _, step := range []string{"second tick", "keeper restart", "new leader"} {
		switch step {
		case "keeper restart", "new leader":
			f.restart()
		}
		f.clock.Step(time.Minute)
		f.tick()
		if f.push.count() != 3 {
			t.Fatalf("%s sent again: %d", step, f.push.count())
		}
	}
	// A lifecycle event and a close recorded later each page once.
	var fresh v1alpha1.AgentSession
	if err := f.c.Get(f.ctx, client.ObjectKeyFromObject(esc), &fresh); err != nil {
		t.Fatal(err)
	}
	f.close(&fresh, v1alpha1.OutcomeFailed, summonoutcome.Report, "failed", "")
	f.update(&fresh)
	f.tick()
	f.tick()
	if f.push.count() != 4 || f.push.form(3).Get("title") != "[dev-env] failed" {
		t.Fatalf("the later close: %v", f.push.titles())
	}
	l := f.ledger()
	for _, key := range []string{summonKey(lost, "lost"), summonKey(esc, "escalation-held"), summonKey(esc, "escalation-reminder"), summonKey(esc, "failed")} {
		e, ok := l.State.Events[key]
		if !ok || e.State != string(pages.StateSent) {
			t.Fatalf("ledger entry %s: %+v %v", key, e, ok)
		}
		r := l.Pages.Find(summonRecordKey(key))
		if r == nil || r.State != pages.StateSent || !strings.HasPrefix(r.RequestID, "req-") || r.Priority != 0 {
			t.Fatalf("page record %s: %+v", key, r)
		}
	}
	if len(l.State.Events) != 4 {
		t.Fatalf("ledger holds %d entries: %+v", len(l.State.Events), l.State.Events)
	}
}

// Proof 3, first half: the ledger write happens before the send, and a failed
// or conflicting ledger write sends nothing.
func TestSummonPagesWriteAheadBeforeSend(t *testing.T) {
	f := newSummonFix(t)
	s := f.close(f.session("wo-upgrade-1", v1alpha1.LaneUpgrade), v1alpha1.OutcomeFailed, summonoutcome.Report, "failed", "")
	f.add(s)
	key := summonKey(s, "failed")
	f.push.before = func() {
		l := f.ledger()
		r := l.Pages.Find(summonRecordKey(key))
		if l.State.Events[key].State != string(pages.StateRecorded) || r == nil || r.State != pages.StateRecorded || len(l.State.Window) != 1 {
			t.Errorf("the send came before the durable reservation: %+v %+v", l.State, r)
		}
	}
	f.tick()
	if f.push.count() != 1 {
		t.Fatalf("sent %d", f.push.count())
	}
}

func TestSummonPagesFailedLedgerWriteSendsNothing(t *testing.T) {
	t.Run("refused-write", func(t *testing.T) {
		f := newSummonFix(t)
		f.add(f.close(f.session("wo-upgrade-1", v1alpha1.LaneUpgrade), v1alpha1.OutcomeFailed, summonoutcome.Report, "failed", ""))
		f.failUpdate = func(int) bool { return true }
		f.tick()
		if f.push.count() != 0 || !strings.Contains(f.logs.String(), "summon page reservation unconfirmed") {
			t.Fatalf("a refused reservation sent %d; logs %s", f.push.count(), f.logs.String())
		}
		f.failUpdate = nil
		f.tick()
		if f.push.count() != 1 {
			t.Fatal("the page did not go once the ledger could be written")
		}
	})
	t.Run("conflicting-write", func(t *testing.T) {
		f := newSummonFix(t)
		f.add(f.close(f.session("wo-upgrade-1", v1alpha1.LaneUpgrade), v1alpha1.OutcomeFailed, summonoutcome.Report, "failed", ""))
		// Another writer (a second keeper) updates the ConfigMap between this
		// pager's read and its write: the optimistic lock refuses the write.
		f.beforeUpdate = func(n int) {
			if n != 1 {
				return
			}
			var cm corev1.ConfigMap
			if err := f.c.Get(f.ctx, summonLedgerName, &cm); err != nil {
				t.Fatal(err)
			}
			cm.Annotations = map[string]string{"touched": "yes"}
			if err := f.c.Update(f.ctx, &cm); err != nil {
				t.Fatal(err)
			}
		}
		f.tick()
		if f.push.count() != 0 {
			t.Fatal("a conflicting reservation sent")
		}
	})
}

// Proof 3, second half: a send whose result is unknown, and a permanent
// failure, are recorded and never sent again; nor is an attempt whose result
// was never stored.
func TestSummonPagesUnknownAndPermanentResultsNotRetried(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		state  pages.State
		setup  func(f *summonFix)
	}{
		{name: "server-error", status: 503, state: pages.StateAmbiguous},
		{name: "unconfirmed-200", status: 200, body: `{"status":0}`, state: pages.StateAmbiguous},
		{name: "refused-400", status: 400, state: pages.StateFailed},
		{name: "rate-limited-429", status: 429, state: pages.StateFailed},
		{name: "transport-error", state: pages.StateAmbiguous, setup: func(f *summonFix) { f.push.srv.Close() }},
		{name: "outcome-never-stored", state: pages.StateRecorded, setup: func(f *summonFix) {
			f.failUpdate = func(n int) bool { return n == 2 } // the reservation lands; its outcome does not
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSummonFix(t)
			s := f.close(f.session("wo-upgrade-1", v1alpha1.LaneUpgrade), v1alpha1.OutcomeFailed, summonoutcome.Report, "failed", "")
			f.add(s)
			f.push.status, f.push.body = tc.status, tc.body
			if tc.setup != nil {
				tc.setup(f)
			}
			f.tick()
			f.failUpdate = nil
			sent := f.push.count()
			for i := 0; i < 2; i++ {
				f.clock.Step(2 * time.Hour)
				f.restart()
				f.tick()
			}
			if f.push.count() != sent {
				t.Fatalf("retried: %d then %d", sent, f.push.count())
			}
			l := f.ledger()
			key := summonKey(s, "failed")
			r := l.Pages.Find(summonRecordKey(key))
			if r == nil || r.State != tc.state || r.Attempts != 1 {
				t.Fatalf("record %+v, want %s after one attempt", r, tc.state)
			}
			if tc.state != pages.StateRecorded && l.State.Events[key].State != string(tc.state) {
				t.Fatalf("ledger entry %+v", l.State.Events[key])
			}
			if tc.state != pages.StateSent && r.RequestID != "" {
				t.Fatal("a request id without an accepted send")
			}
		})
	}
}

// Proof 4: at most 6 attempts in any 60 minutes, the drill and the summary
// included. The seventh event is recorded capped and never paged; the summary
// goes when the oldest attempt leaves the window and takes a slot; a second
// burst after it is counted in the next summary, never twice; a restart in
// between loses no count (op782-q-2, option B).
func TestSummonPagesRollingCap(t *testing.T) {
	f := newSummonFix(t)
	var first []*v1alpha1.AgentSession
	for i := 1; i <= 8; i++ {
		s := f.close(f.session(fmt.Sprintf("wo-upgrade-%d", i), v1alpha1.LaneUpgrade), v1alpha1.OutcomeFailed, summonoutcome.Report, "failed", "")
		s.Status.Summon.Closed.At = metav1.NewTime(f.clock.Now().Add(time.Duration(i-20) * time.Minute))
		first = append(first, s)
		f.add(s)
	}
	f.tick()
	if f.push.count() != 6 {
		t.Fatalf("first burst sent %d", f.push.count())
	}
	l := f.ledger()
	for _, s := range first[6:] {
		if l.State.Events[summonKey(s, "failed")].State != summonStateCapped {
			t.Fatalf("%s not capped: %+v", s.Name, l.State.Events[summonKey(s, "failed")])
		}
	}
	if l.State.Capped != 2 {
		t.Fatalf("capped count %d", l.State.Capped)
	}
	f.restart() // a restart while two events wait to be counted
	f.clock.Step(time.Hour + noticeStoreTimeout - time.Second)
	f.tick()
	if f.push.count() != 6 {
		t.Fatal("sent before the oldest attempt left the window")
	}
	f.clock.Step(time.Second)
	f.tick()
	if f.push.count() != 7 || f.push.form(6).Get("title") != "[dev-env] more-events" ||
		!strings.HasPrefix(f.push.form(6).Get("message"), "[dev-env] more-events: 2 more events were not paged") {
		t.Fatalf("summary: %d %v", f.push.count(), f.push.form(min(6, f.push.count()-1)))
	}
	if l := f.ledger(); l.State.Capped != 0 || l.State.More != 1 {
		t.Fatalf("summary did not take the count: %+v", l.State)
	}
	// A second burst, after the summary: it took one of the six slots.
	var second []*v1alpha1.AgentSession
	for i := 9; i <= 14; i++ {
		s := f.close(f.session(fmt.Sprintf("wo-upgrade-%d", i), v1alpha1.LaneUpgrade), v1alpha1.OutcomeFailed, summonoutcome.Report, "failed", "")
		second = append(second, s)
		f.add(s)
	}
	f.tick()
	if f.push.count() != 12 {
		t.Fatalf("second burst: %d", f.push.count())
	}
	if f.ledger().State.Capped != 1 {
		t.Fatalf("second burst capped %d", f.ledger().State.Capped)
	}
	f.restart()
	f.clock.Step(time.Hour + noticeStoreTimeout - time.Second)
	f.tick()
	if f.push.count() != 12 {
		t.Fatal("the second summary went before its window had room")
	}
	f.clock.Step(time.Second)
	f.tick()
	f.tick()
	if f.push.count() != 13 || !strings.HasPrefix(f.push.form(12).Get("message"), "[dev-env] more-events: 1 more event was not paged") {
		t.Fatalf("next summary: %v", f.push.titles())
	}
	var capped []string
	for _, s := range append(first, second...) {
		if f.ledger().State.Events[summonKey(s, "failed")].State == summonStateCapped {
			capped = append(capped, s.Name)
		}
	}
	if len(capped) != 3 {
		t.Fatalf("capped %v", capped)
	}
	for i := 0; i < f.push.count(); i++ {
		msg := f.push.form(i).Get("message")
		for _, name := range capped {
			if strings.Contains(msg, "session "+name+" ") {
				t.Fatalf("a capped event was paged: %s", msg)
			}
		}
	}
	// No 60 minutes ever held more than six sends.
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
			t.Fatalf("%d sends in the hour from %s", n, times[i])
		}
	}
}

// Proof 5: the fixed text, the note cut and marked, a URL in a note replaced,
// and only the verified link or the console list.
func TestSummonPagesText(t *testing.T) {
	f := newSummonFix(t)
	note := "fixed it; see https://evil.example/approve?x=1 or www.evil.example and evil.example/path and mailto:x@evil.example. " + strings.Repeat("é", 300)
	failed := f.close(f.session("wo-upgrade-1", v1alpha1.LaneUpgrade), v1alpha1.OutcomeFailed, summonoutcome.Report, "failed", note)
	failed.Status.RemoteControl = &v1alpha1.RemoteControlStatus{URL: "https://claude.ai/code/session_01AbC-d_9"}
	failed.Status.Summon.Closed.At = metav1.NewTime(f.clock.Now().Add(-5 * time.Minute))
	escalated := f.close(f.session("wo-upgrade-2", v1alpha1.LaneUpgrade), v1alpha1.OutcomeEscalated, summonoutcome.Report, "escalate", "needs Tom")
	escalated.Status.RemoteControl = &v1alpha1.RemoteControlStatus{URL: "https://claude.ai/code/session_x?token=abc"}
	escalated.Status.Summon.Closed.At = metav1.NewTime(f.clock.Now().Add(-4 * time.Minute))
	unnamed := f.close(f.session("wo-upgrade-3", v1alpha1.LaneUpgrade), v1alpha1.OutcomeEscalated, summonoutcome.Unreported, "Weird_Reason!", "")
	unnamed.Status.Summon.Closed.At = metav1.NewTime(f.clock.Now().Add(-3 * time.Minute))
	lost := f.close(f.session("wo-upgrade-4", v1alpha1.LaneUpgrade), v1alpha1.OutcomeFailed, summonoutcome.Unreported, summonoutcome.ReasonSessionLost, "unreported: session-lost")
	lost.Status.RemoteControl = &v1alpha1.RemoteControlStatus{URL: "https://evil.example/code/session_abc"}
	lost.Status.Summon.Closed.At = metav1.NewTime(f.clock.Now().Add(-2 * time.Minute))
	f.add(failed, escalated, unnamed, lost)
	f.opts.Drill = "install-2026-11-12"
	f.restart()
	f.tick()
	if f.push.count() != 5 {
		t.Fatalf("pages %v", f.push.titles())
	}
	drill := f.push.form(0)
	if drill.Get("title") != "DRILL [dev-env] drill" || !strings.HasPrefix(drill.Get("message"), "DRILL [dev-env] drill install-2026-11-12: ") || drill.Get("url") != summonConsole {
		t.Fatalf("drill %v", drill)
	}
	p := f.push.form(1)
	msg := p.Get("message")
	if p.Get("title") != "[dev-env] failed" || !strings.HasPrefix(msg, "[dev-env] failed: summoned session wo-upgrade-1 reported failed.") || p.Get("priority") != "0" {
		t.Fatalf("failed page %v", p)
	}
	_, cut, ok := strings.Cut(msg, "\nsession note: ")
	if !ok || utf8.RuneCountInString(cut) != summonNoteMax || strings.Contains(cut, "\n") {
		t.Fatalf("note not cut to %d characters after its label: %q", summonNoteMax, cut)
	}
	if strings.Contains(msg, "evil") || strings.Count(cut, "[link removed]") != 4 {
		t.Fatalf("a link in the note survived: %q", cut)
	}
	if p.Get("url") != "https://claude.ai/code/session_01AbC-d_9" || p.Get("url_title") != "Open the session" {
		t.Fatalf("verified link %q %q", p.Get("url"), p.Get("url_title"))
	}
	e := f.push.form(2)
	if e.Get("title") != "[dev-env] escalated" || !strings.HasPrefix(e.Get("message"), "[dev-env] escalated: summoned session wo-upgrade-2 ") ||
		e.Get("url") != summonConsole || e.Get("url_title") != "Open dev-env sessions" {
		t.Fatalf("escalated page with an unverified link %v", e)
	}
	u := f.push.form(3).Get("message")
	if f.push.form(3).Get("title") != "[dev-env] failed" || !strings.HasPrefix(u, "[dev-env] failed: summoned session wo-upgrade-3 closed in a way the keeper's terminal table does not name") ||
		!strings.Contains(u, "lane upgrade, state escalated, source unreported, reason unknown") {
		t.Fatalf("unnamed close %q", u)
	}
	if lp := f.push.form(4); lp.Get("title") != "[dev-env] lost" || lp.Get("url") != summonConsole || !strings.Contains(lp.Get("message"), "(session-lost)") {
		t.Fatalf("lost page %v", lp)
	}
	for i := 0; i < f.push.count(); i++ {
		for k, v := range f.push.form(i) {
			if k != "token" && k != "user" && (strings.Contains(v[0], summonTokenCanary) || strings.Contains(v[0], summonUserCanary)) {
				t.Fatal("a credential in page text")
			}
		}
	}
	// Records are content-free: the fixed title, the session name and the
	// note's byte count (ruling Q9).
	raw := f.rawLedger()
	for _, forbidden := range []string{"evil", "fixed it", "needs Tom", "claude.ai", "console.example", "éé", summonTokenCanary, summonUserCanary} {
		if strings.Contains(raw, forbidden) || strings.Contains(f.logs.String(), forbidden) {
			t.Fatalf("%q stored or logged", forbidden)
		}
	}
	r := f.ledger().Pages.Find(summonRecordKey(summonKey(failed, "failed")))
	if r == nil || r.Title != "[dev-env] failed" || r.Body != "session wo-upgrade-1, note "+strconv.Itoa(len(note))+" bytes" || r.URL != "" {
		t.Fatalf("record %+v", r)
	}
}

func TestSummonPagesNoteRules(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", ""},
		{"plain note", "plain note"},
		{"see http://a.example/x.", "see [link removed]."},
		{"(see https://a.example/x)", "(see [link removed])"},
		{"HTTPS://A.EXAMPLE then", "[link removed] then"},
		{"ftp://host/file and git+ssh://x", "[link removed] and [link removed]"},
		{"www.example.org/x", "[link removed]"},
		{"host.example:8443/x and 10.0.0.1/admin", "[link removed] and [link removed]"},
		{"javascript:alert(1) tel:+1555", "[link removed] [link removed]"},
		{"edited internal/keeper/run.go", "edited internal/keeper/[link removed]"}, // F6: a file name is a dotted host
		{"tab\there", "tab here"},
	} {
		if got := summonNote(tc.in); got != tc.want {
			t.Errorf("summonNote(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if got := summonNote(strings.Repeat("a", 500)); utf8.RuneCountInString(got) != summonNoteMax {
		t.Fatalf("cut to %d", utf8.RuneCountInString(got))
	}
	for _, tc := range []struct{ in, want string }{{"upgrade", "upgrade"}, {"pod-terminated", "pod-terminated"}, {"Bad", "unknown"}, {"", "unknown"}, {strings.Repeat("a", 65), "unknown"}, {"a b", "unknown"}} {
		if got := summonWord(tc.in); got != tc.want {
			t.Errorf("summonWord(%q) = %q", tc.in, got)
		}
	}
	for _, tc := range []struct {
		url  string
		want bool
	}{{"https://claude.ai/code/session_01AbC-d_9", true}, {"https://claude.ai/code/session_", false}, {"http://claude.ai/code/session_a", false},
		{"https://claude.ai/code/session_a/x", false}, {"https://claude.ai.evil/code/session_a", false}, {"https://claude.ai/code/session_a?x=1", false}, {"https://user@claude.ai/code/session_a", false}} {
		if got := summonVerifiedLink.MatchString(tc.url); got != tc.want {
			t.Errorf("verified %q = %v", tc.url, got)
		}
	}
}

// The drill sends one page per id, recorded like any other, never twice for one id.
func TestSummonPagesDrillOncePerID(t *testing.T) {
	f := newSummonFix(t)
	f.opts.Drill = "drill-1"
	f.restart()
	f.tick()
	f.tick()
	f.restart()
	f.tick()
	if f.push.count() != 1 {
		t.Fatalf("drill-1 sent %d times", f.push.count())
	}
	r := f.ledger().Pages.Find(summonRecordKey("drill/drill-1"))
	if r == nil || r.State != pages.StateSent || r.RequestID == "" || r.Title != "DRILL [dev-env] drill" {
		t.Fatalf("drill record %+v", r)
	}
	f.opts.Drill = "drill-2"
	f.restart()
	f.tick()
	f.opts.Drill = "drill-1"
	f.restart()
	f.tick()
	if f.push.count() != 2 {
		t.Fatalf("drills sent %d", f.push.count())
	}
}

// Proof 6: nothing but the Pushover sender can send, and no email path
// exists; with the flag off nothing is listed or read.
func TestSummonPagesOnlyPushoverSends(t *testing.T) {
	field, ok := reflect.TypeOf(summonPager{}).FieldByName("sender")
	if !ok || field.Type != reflect.TypeOf(&PushoverSender{}) {
		t.Fatal("the pager's sender is not the Pushover sender")
	}
	for _, dir := range []string{".", "../../cmd/dev-env-keeper"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil || len(files) == 0 {
			t.Fatalf("no Go files in %s: %v", dir, err)
		}
		for _, path := range files {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatal(err)
			}
			for _, imp := range file.Imports {
				if p, _ := strconv.Unquote(imp.Path.Value); p == "net/smtp" || p == "net/mail" || strings.Contains(p, "gomail") || strings.Contains(p, "sendgrid") {
					t.Fatalf("%s imports %s: an email path", path, p)
				}
			}
		}
	}
}

func TestSummonPagesOffReadsNothing(t *testing.T) {
	// Every request the pager's client could make lands here.
	calls := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(api.Close)
	cfg := &rest.Config{Host: api.URL}
	for _, o := range []Options{
		{Namespace: "dev-env-system", GHTokenSecret: types.NamespacedName{Namespace: summonTestNS, Name: "gh"}, LeaderElect: true},
		{Namespace: "dev-env-system", GHTokenSecret: types.NamespacedName{Namespace: summonTestNS, Name: "gh"}, LeaderElect: true,
			SummonPages: SummonPageOptions{Enabled: true, PortalOrigin: "http://not-https.example", SessionNamespace: summonTestNS}},
	} {
		if r := summonPagerFor(o, cfg, logr.Discard()); r != nil {
			t.Fatal("an off or invalid pager was built")
		}
	}
	if calls != 0 {
		t.Fatalf("%d API calls with the pager off", calls)
	}
	// On, but with no Pushover credentials: nothing is read.
	f := newSummonFix(t)
	f.add(f.close(f.session("wo-upgrade-1", v1alpha1.LaneUpgrade), v1alpha1.OutcomeFailed, summonoutcome.Report, "failed", ""))
	if err := os.Remove(f.tokens[0]); err != nil {
		t.Fatal(err)
	}
	f.tick()
	if f.lists != 0 || f.cmGets != 0 || f.push.count() != 0 || !strings.Contains(f.logs.String(), "summon pages: no Pushover credentials") {
		t.Fatalf("read without credentials: lists %d gets %d sends %d logs %s", f.lists, f.cmGets, f.push.count(), f.logs.String())
	}
}

// A missing, refused, damaged or partial ledger logs "state unavailable",
// lists nothing and sends nothing.
func TestSummonPagesStateUnavailable(t *testing.T) {
	for _, tc := range []string{"missing", "forbidden", "corrupt", "empty-object", "null", "version-only", "reset-with-records", "unknown-field", "other-version"} {
		t.Run(tc, func(t *testing.T) {
			f := newSummonFix(t)
			f.add(f.close(f.session("wo-upgrade-1", v1alpha1.LaneUpgrade), v1alpha1.OutcomeFailed, summonoutcome.Report, "failed", ""))
			set := func(data map[string]string) {
				var cm corev1.ConfigMap
				if err := f.c.Get(f.ctx, summonLedgerName, &cm); err != nil {
					t.Fatal(err)
				}
				cm.Data = data
				if err := f.c.Update(f.ctx, &cm); err != nil {
					t.Fatal(err)
				}
			}
			switch tc {
			case "missing":
				if err := f.c.Delete(f.ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: summonLedgerName.Namespace, Name: summonLedgerName.Name}}); err != nil {
					t.Fatal(err)
				}
			case "forbidden":
				f.getErr = apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, summonLedgerName.Name, errors.New("denied"))
			case "corrupt":
				set(map[string]string{summonPagesStateKey: "damaged"})
			case "empty-object":
				set(map[string]string{summonPagesStateKey: `{}`})
			case "null":
				set(map[string]string{summonPagesStateKey: `null`})
			case "version-only":
				set(map[string]string{summonPagesStateKey: `{"version":1}`})
			case "reset-with-records":
				doc := pages.New()
				if _, err := doc.Begin(pages.Page{Key: "summon/x/failed", Sender: "dev-env-keeper", Source: "summon-pages"}, f.clock.Now()); err != nil {
					t.Fatal(err)
				}
				raw, _ := doc.Marshal()
				set(map[string]string{pages.ConfigMapKey: string(raw)})
			case "unknown-field":
				set(map[string]string{summonPagesStateKey: `{"version":1,"events":{},"window":[],"capped":0,"more":0,"digest":{"since":0,"lastAt":0,"pending":[],"listed":{}},"overflow":{},"later":1}`})
			case "other-version":
				set(map[string]string{summonPagesStateKey: `{"version":2,"events":{},"window":[],"capped":0,"more":0,"digest":{"since":0,"lastAt":0,"pending":[],"listed":{}},"overflow":{}}`})
			}
			f.lists = 0
			f.tick()
			if f.push.count() != 0 || f.lists != 0 || !strings.Contains(f.logs.String(), "summon page state unavailable") {
				t.Fatalf("sends %d lists %d logs %s", f.push.count(), f.lists, f.logs.String())
			}
		})
	}
}

// The whole schema, the digest and overflow members included, is written now
// and kept as read, so a rolled-back keeper can still read a later one's
// ledger (ruling Q11).
func TestSummonPagesLedgerSchemaRoundTrip(t *testing.T) {
	f := newSummonFix(t)
	later := `{"version":1,"events":{},"window":[],"capped":0,"more":0,` +
		`"digest":{"since":1762948800,"lastAt":1762950000,"pending":[{"key":"00000000-0000-4000-8000-000000000009/digest","session":"wo-cigar-curate-9","lane":"curation","state":"done","reason":"done","at":1762949000}],"listed":{"00000000-0000-4000-8000-000000000008/digest":1762950000}},` +
		`"overflow":{"upgrade-agent.alert-responder":{"lastPagedCount":3,"lastPagedAt":1762950000}}}`
	var cm corev1.ConfigMap
	if err := f.c.Get(f.ctx, summonLedgerName, &cm); err != nil {
		t.Fatal(err)
	}
	cm.Data = map[string]string{summonPagesStateKey: later}
	if err := f.c.Update(f.ctx, &cm); err != nil {
		t.Fatal(err)
	}
	f.add(f.close(f.session("wo-upgrade-1", v1alpha1.LaneUpgrade), v1alpha1.OutcomeFailed, summonoutcome.Report, "failed", ""))
	f.tick()
	// The page, then (story 792) the digest of the pending line, whose last
	// digest was a year before.
	if f.push.count() != 2 || f.push.form(1).Get("title") != "[dev-env] digest" {
		t.Fatalf("a ledger with digest and overflow members was not read: %v", f.push.titles())
	}
	l := f.ledger()
	// The overflow member is kept as read; the digest members are the
	// digest's own: the watermark kept, the line listed once.
	if l.State.Digest.Since != 1762948800 || len(l.State.Digest.Pending) != 0 || l.State.Digest.Listed["00000000-0000-4000-8000-000000000009/digest"] != f.clock.Now().Unix() ||
		l.State.Overflow["upgrade-agent.alert-responder"].LastPagedCount != 3 {
		t.Fatalf("later members not kept: %+v", l.State)
	}
	// The listed key whose dedupe entry is absent was proved gone, so the
	// reservation drops it on purpose (story 792 fix round 1, ruling 4).
	if _, ok := l.State.Digest.Listed["00000000-0000-4000-8000-000000000008/digest"]; ok {
		t.Fatalf("a listed key without its dedupe entry stayed after the reservation: %+v", l.State.Digest.Listed)
	}
	// A fresh ledger writes every member, empty.
	g := newSummonFix(t)
	g.opts.Drill = "d1"
	g.restart()
	g.tick()
	var fresh corev1.ConfigMap
	if err := g.c.Get(g.ctx, summonLedgerName, &fresh); err != nil {
		t.Fatal(err)
	}
	var generic map[string]json.RawMessage
	if err := json.Unmarshal([]byte(fresh.Data[summonPagesStateKey]), &generic); err != nil {
		t.Fatal(err)
	}
	for _, member := range []string{"version", "events", "window", "capped", "more", "digest", "overflow"} {
		if _, ok := generic[member]; !ok {
			t.Fatalf("member %s missing from %s", member, fresh.Data[summonPagesStateKey])
		}
	}
	// Story 792: the first run sets the digest's watermark.
	if string(generic["digest"]) != fmt.Sprintf(`{"since":%d,"lastAt":0,"pending":[],"listed":{}}`, g.clock.Now().Unix()) || string(generic["overflow"]) != `{}` {
		t.Fatalf("digest %s overflow %s", generic["digest"], generic["overflow"])
	}
}

// The list is bounded: the label selector, 500 a page, at most 20 pages; a
// list that does not finish sends nothing.
func TestSummonPagesScanBounded(t *testing.T) {
	f := newSummonFix(t)
	f.add(f.close(f.session("wo-upgrade-1", v1alpha1.LaneUpgrade), v1alpha1.OutcomeFailed, summonoutcome.Report, "failed", ""))
	f.listHook = func(l *v1alpha1.AgentSessionList, n int) error { l.Continue = "more-" + strconv.Itoa(n); return nil }
	f.tick()
	if f.lists != summonPagesListPages || f.push.count() != 0 || !strings.Contains(f.logs.String(), "summon page scan unavailable") {
		t.Fatalf("lists %d sends %d logs %s", f.lists, f.push.count(), f.logs.String())
	}
	lo := &client.ListOptions{}
	for _, o := range f.listOpts {
		o.ApplyToList(lo)
	}
	if lo.Limit != summonPagesListPage || lo.Namespace != summonTestNS || lo.LabelSelector == nil ||
		lo.LabelSelector.String() != summonoutcome.LabelSummoned+"="+summonoutcome.LabelSummonedValue {
		t.Fatalf("list options %+v", lo)
	}
	f.listHook = nil
	f.lists = 0
	f.tick()
	if f.push.count() != 1 || f.lists != 1 {
		t.Fatalf("a finished list: lists %d sends %d", f.lists, f.push.count())
	}
}

// A session proved gone (a read of its name finds nothing) loses its ledger
// entries an hour after they were written, saved with the next entry; a
// session still listed keeps them.
func TestSummonPagesPrunesGoneSessions(t *testing.T) {
	f := newSummonFix(t)
	gone := f.close(f.session("wo-upgrade-1", v1alpha1.LaneUpgrade), v1alpha1.OutcomeFailed, summonoutcome.Report, "failed", "")
	kept := f.close(f.session("wo-upgrade-2", v1alpha1.LaneUpgrade), v1alpha1.OutcomeFailed, summonoutcome.Report, "failed", "")
	f.add(gone, kept)
	f.tick()
	if err := f.c.Delete(f.ctx, gone); err != nil {
		t.Fatal(err)
	}
	f.clock.Step(30 * time.Minute)
	f.tick()
	if _, ok := f.ledger().State.Events[summonKey(gone, "failed")]; !ok {
		t.Fatal("pruned within the hour")
	}
	f.clock.Step(31 * time.Minute)
	f.tick()
	// The prune is saved with the next entry, never alone.
	if _, ok := f.ledger().State.Events[summonKey(gone, "failed")]; !ok {
		t.Fatal("a prune was saved alone")
	}
	other := f.close(f.session("wo-upgrade-3", v1alpha1.LaneUpgrade), v1alpha1.OutcomeFailed, summonoutcome.Report, "failed", "")
	f.add(other)
	f.tick()
	l := f.ledger()
	if _, ok := l.State.Events[summonKey(gone, "failed")]; ok {
		t.Fatal("a gone session's entry stayed")
	}
	if _, ok := l.State.Events[summonKey(kept, "failed")]; !ok {
		t.Fatal("a listed session's entry was pruned")
	}
	f.tick()
	if f.push.count() != 3 {
		t.Fatalf("pruning resent: %d", f.push.count())
	}
}

// Every session gone and every entry old: the ledger is never saved with an
// empty dedupe beside its page records, so it stays readable and the pager
// goes on.
func TestSummonPagesPruneNeverResetsTheLedger(t *testing.T) {
	f := newSummonFix(t)
	s := f.close(f.session("wo-upgrade-1", v1alpha1.LaneUpgrade), v1alpha1.OutcomeFailed, summonoutcome.Report, "failed", "")
	f.add(s)
	f.tick()
	if err := f.c.Delete(f.ctx, s); err != nil {
		t.Fatal(err)
	}
	f.clock.Step(48 * time.Hour)
	f.tick()
	f.restart()
	f.tick()
	if strings.Contains(f.logs.String(), "summon page state unavailable") {
		t.Fatalf("the ledger became unreadable: %s", f.logs.String())
	}
	f.add(f.close(f.session("wo-upgrade-2", v1alpha1.LaneUpgrade), v1alpha1.OutcomeFailed, summonoutcome.Report, "failed", ""))
	f.tick()
	if f.push.count() != 2 {
		t.Fatalf("sent %d", f.push.count())
	}
	if l := f.ledger(); len(l.State.Events) != 1 || len(l.Pages.Records) != 2 {
		t.Fatalf("events %v records %d", l.State.Events, len(l.Pages.Records))
	}
}

// Proof 7, unit half: a bad or incomplete flag set disables only the pager,
// logs why without the values, and once.
func TestSummonPagesInvalidConfiguration(t *testing.T) {
	ok := SummonPageOptions{Enabled: true, PortalOrigin: "https://private-marker.example", SessionNamespace: summonTestNS}
	for _, tc := range []struct {
		name    string
		mutate  func(*SummonPageOptions)
		notices NoticeOptions
		ask     AskNotificationOptions
		leader  bool
		reason  string
	}{
		{name: "no-origin", mutate: func(o *SummonPageOptions) { o.PortalOrigin = "" }, leader: true, reason: "portal origin"},
		{name: "http-origin", mutate: func(o *SummonPageOptions) { o.PortalOrigin = "http://private-marker.example" }, leader: true, reason: "portal origin"},
		{name: "origin-path", mutate: func(o *SummonPageOptions) { o.PortalOrigin = "https://private-marker.example/x" }, leader: true, reason: "portal origin"},
		{name: "origin-credentials", mutate: func(o *SummonPageOptions) { o.PortalOrigin = "https://private-marker@private-marker.example" }, leader: true, reason: "portal origin"},
		{name: "bad-drill", mutate: func(o *SummonPageOptions) { o.Drill = "Private_Marker" }, leader: true, reason: "drill id"},
		{name: "long-drill", mutate: func(o *SummonPageOptions) { o.Drill = strings.Repeat("a", 64) }, leader: true, reason: "drill id"},
		{name: "no-leader", reason: "leader election"},
		{name: "http-pushover", notices: NoticeOptions{URL: "http://private-marker.example"}, leader: true, reason: "Pushover URL"},
		{name: "notice-ledger", mutate: func(o *SummonPageOptions) { o.StateConfigMap = DefaultNoticeStateConfigMap }, leader: true, reason: "ledger"},
		{name: "ask-ledger", mutate: func(o *SummonPageOptions) { o.StateConfigMap = DefaultAskNotificationConfigMap }, leader: true, reason: "ledger"},
		{name: "custom-ask-ledger", mutate: func(o *SummonPageOptions) { o.StateConfigMap = "shared" }, ask: AskNotificationOptions{StateConfigMap: "shared"}, leader: true, reason: "ledger"},
		{name: "bad-ledger-name", mutate: func(o *SummonPageOptions) { o.StateConfigMap = "Private_Marker" }, leader: true, reason: "ledger"},
		{name: "no-session-namespace", mutate: func(o *SummonPageOptions) { o.SessionNamespace = "" }, leader: true, reason: "session namespace"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := ok
			if tc.mutate != nil {
				tc.mutate(&o)
			}
			var logs bytes.Buffer
			log := logr.FromSlogHandler(slog.NewJSONHandler(&logs, nil))
			o = o.DisableInvalid(tc.notices, tc.ask, tc.leader, log)
			o = o.DisableInvalid(tc.notices, tc.ask, tc.leader, log)
			if o.Enabled || strings.Count(logs.String(), `"msg":"summon pages disabled: invalid configuration"`) != 1 ||
				!strings.Contains(logs.String(), tc.reason) || strings.Contains(strings.ToLower(logs.String()), "private") {
				t.Fatalf("not safely disabled once: enabled=%v logs=%s", o.Enabled, logs.String())
			}
		})
	}
	var logs bytes.Buffer
	if o := ok.DisableInvalid(NoticeOptions{}, AskNotificationOptions{}, true, logr.FromSlogHandler(slog.NewJSONHandler(&logs, nil))); !o.Enabled || logs.Len() != 0 {
		t.Fatalf("valid options disabled: %s", logs.String())
	}
	if o := (SummonPageOptions{}).DisableInvalid(NoticeOptions{}, AskNotificationOptions{}, false, logr.FromSlogHandler(slog.NewJSONHandler(&logs, nil))); o.Enabled || logs.Len() != 0 {
		t.Fatal("off options logged")
	}
}

type summonPanicReader struct{}

func (summonPanicReader) list(context.Context) ([]v1alpha1.AgentSession, error) {
	panic("a private panic value")
}

func (summonPanicReader) uid(context.Context, string) (types.UID, error) {
	panic("a private panic value")
}

// Proof 7: Start never returns an error, and a fault in one tick does not
// escape it.
func TestSummonPagesStartNeverReturnsError(t *testing.T) {
	f := newSummonFix(t)
	f.p.reader = summonPanicReader{}
	f.tick()
	if !strings.Contains(f.logs.String(), "summon page tick failed") || strings.Contains(f.logs.String(), "private panic") {
		t.Fatalf("logs %s", f.logs.String())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := f.p.Start(ctx); err != nil {
		t.Fatalf("Start returned %v", err)
	}
	if !f.p.NeedLeaderElection() {
		t.Fatal("the pager is not leader-only")
	}
}

// Proof 7, envtest half: Run wired as the command wires it. A bad pager flag
// set disables only the pager: the manager starts, the token keeper mints, the
// Ask notifier ticks and the login worker starts; Run returns nil on stop. A
// valid pager whose ledger ConfigMap is missing logs "state unavailable" and
// the keeper runs on.
func TestSummonPagesKeeperRunsBesideThem(t *testing.T) {
	cfg := envConfig(t)
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	tokenFile, userFile := filepath.Join(dir, "token"), filepath.Join(dir, "user")
	for _, p := range []string{tokenFile, userFile} {
		if err := os.WriteFile(p, []byte(summonTokenCanary), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name   string
		pages  SummonPageOptions
		expect string
		absent string
		once   string // a line the keeper logs exactly once
	}{
		{name: "invalid", pages: SummonPageOptions{Enabled: true, PortalOrigin: "https://private-marker.example/path"},
			expect: "summon pages disabled: invalid configuration", absent: "summon page state unavailable"},
		{name: "ledger-missing", pages: SummonPageOptions{Enabled: true, PortalOrigin: "https://portal.example", SessionNamespace: "summon-pages-agents-ledger-missing"},
			expect: "summon page state unavailable", absent: "summon pages disabled", once: `"sessionNamespace":"summon-pages-agents-ledger-missing"`},
		// F7: no other option's namespace stands in for an empty one.
		{name: "no-namespace", pages: SummonPageOptions{Enabled: true, PortalOrigin: "https://portal.example"},
			expect: "the session namespace is empty", absent: "summon page state unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			namespace, publicNamespace := "summon-pages-keeper-"+tc.name, "summon-pages-agents-"+tc.name
			secret := types.NamespacedName{Namespace: publicNamespace, Name: "gh-token"}
			for _, obj := range []client.Object{
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}},
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: publicNamespace}},
				&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: publicNamespace, Name: secret.Name}},
			} {
				if err := c.Create(ctx, obj); err != nil {
					t.Fatal(err)
				}
			}
			k := keys(t)
			gh := newFakeGitHub(t, &k[0].PublicKey, time.Now)
			app := gh.app(writeAppDir(t, k[0]))
			loginDir := testsock.ShortDir(t, "sp")
			ctx, cancel := context.WithCancel(ctx)
			r := &running{cancel: cancel, done: make(chan error, 1), logs: &logBuffer{}}
			t.Cleanup(func() { r.stop(t) })
			go func() {
				r.done <- Run(ctx, cfg, Options{
					Namespace: namespace, GHTokenSecret: secret, GitHubApp: app,
					LeaderElect: true, MetricsAddr: "0", ProbeAddr: "0", Log: r.logs.logger(),
					Notices:          NoticeOptions{URL: "https://push.invalid", TokenFile: tokenFile, UserFile: userFile},
					AskNotifications: AskNotificationOptions{Enabled: true, PortalOrigin: "https://portal.example"},
					SummonPages:      tc.pages,
					CodexAuth:        CodexOptions{Enabled: true, LoginDir: loginDir},
				})
			}()
			onServer(t, "the token keeper to mint and the Ask notifier and the pager to tick", func() bool {
				var s corev1.Secret
				logs := r.logs.String()
				return c.Get(ctx, secret, &s) == nil && len(s.Data[GHTokenKey]) > 0 &&
					strings.Contains(logs, "ask notification state unavailable") && strings.Contains(logs, tc.expect)
			})
			r.stop(t)
			logs := r.logs.String()
			if strings.Count(logs, "summon pages disabled: invalid configuration") > 1 || strings.Contains(logs, tc.absent) ||
				strings.Contains(logs, "private-marker") || strings.Contains(logs, summonTokenCanary) ||
				(tc.once != "" && strings.Count(logs, tc.once) != 1) {
				t.Fatalf("logs: %s", logs)
			}
		})
	}
}
