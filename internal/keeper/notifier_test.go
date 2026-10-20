package keeper

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr/funcr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clocktesting "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// Synthetic values, never real credentials. Every output is scanned for them.
	noticeTokenCanary = "NOTICETOKENCANARY0123456789abcdef"
	noticeUserCanary  = "NOTICEUSERCANARY0123456789abcdef"
)

var noticeLapse = time.Date(2026, 11, 16, 17, 51, 0, 0, time.UTC)

// pushoverFake records every request and answers with status.
type pushoverFake struct {
	srv  *httptest.Server
	mu   sync.Mutex
	reqs []recordedPush
	// status answers each request; 200 when zero. When echo is set the error
	// body repeats the request body, as a careless API might.
	status int
	echo   bool
}

type recordedPush struct {
	method, path, rawQuery string
	header                 http.Header
	form                   url.Values
}

func newPushoverFake(t *testing.T) *pushoverFake {
	t.Helper()
	f := &pushoverFake{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(body))
		f.mu.Lock()
		f.reqs = append(f.reqs, recordedPush{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Clone(), form})
		status, echo := f.status, f.echo
		f.mu.Unlock()
		if status == 0 {
			status = 200
		}
		w.WriteHeader(status)
		if echo {
			_, _ = w.Write(body)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *pushoverFake) setStatus(s int) { f.mu.Lock(); f.status = s; f.mu.Unlock() }
func (f *pushoverFake) count() int      { f.mu.Lock(); defer f.mu.Unlock(); return len(f.reqs) }
func (f *pushoverFake) last() recordedPush {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reqs[len(f.reqs)-1]
}

func (f *pushoverFake) titles() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.reqs {
		out = append(out, r.form.Get("title")+"|"+r.form.Get("priority"))
	}
	return out
}

// memStore is a NoticeStore that survives a "restart": two notifiers share it.
type memStore struct {
	mu    sync.Mutex
	state NoticeState
	fail  bool
	saves int
}

func (m *memStore) Load(context.Context) (NoticeState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return NoticeState{}, errNoticeState
	}
	raw, _ := jsonRoundTrip(m.state)
	return raw, nil
}

func (m *memStore) Save(_ context.Context, s NoticeState) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return errNoticeState
	}
	m.state, _ = jsonRoundTrip(s)
	m.saves++
	return nil
}

func jsonRoundTrip(s NoticeState) (NoticeState, error) {
	raw, err := marshalState(s)
	if err != nil {
		return NoticeState{}, err
	}
	return unmarshalState(raw)
}

type noticeFix struct {
	t     *testing.T
	fake  *pushoverFake
	hub   *LoginHub
	store *memStore
	clk   *clocktesting.FakeClock
	logs  *bytes.Buffer
	dir   string
	n     *Notifier
	alt   NoticeStore // used instead of store when set
}

func newNoticeFix(t *testing.T, start time.Time) *noticeFix {
	t.Helper()
	f := &noticeFix{t: t, fake: newPushoverFake(t), hub: NewLoginHub(ProviderClaude, ProviderCodex), store: &memStore{}, clk: clocktesting.NewFakeClock(start), logs: &bytes.Buffer{}, dir: t.TempDir()}
	for name, v := range map[string]string{"token": noticeTokenCanary, "user": noticeUserCanary} {
		if err := os.WriteFile(filepath.Join(f.dir, name), []byte(v+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f.n = f.newNotifier()
	return f
}

// newNotifier makes a fresh notifier over the same store and hub: a restart.
func (f *noticeFix) newNotifier() *Notifier {
	f.t.Helper()
	log := funcr.New(func(prefix, args string) { f.logs.WriteString(prefix + args + "\n") }, funcr.Options{Verbosity: 2})
	var store NoticeStore = f.store
	if f.alt != nil {
		store = f.alt
	}
	n, err := NewNotifier(NoticeOptions{
		TokenFile: filepath.Join(f.dir, "token"), UserFile: filepath.Join(f.dir, "user"), URL: "https://example.invalid/unused",
	}, f.hub, store, nil, f.clk, log)
	if err != nil {
		f.t.Fatal(err)
	}
	n.Sender = &PushoverSender{URL: f.fake.srv.URL, TokenFile: filepath.Join(f.dir, "token"), UserFile: filepath.Join(f.dir, "user")}
	return n
}

func (f *noticeFix) tick() { f.n.Tick(context.Background()) }

func (f *noticeFix) at(ts string) {
	f.t.Helper()
	tm, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		f.t.Fatal(err)
	}
	f.clk.SetTime(tm)
}

func TestWarningOncePerLocalDayInsideTheWindow(t *testing.T) {
	// 2026-11-16 17:51 UTC is the lapse. New York is on EST (UTC-5) from Nov 1,
	// so the 09:00 to 21:00 window is 14:00Z to 02:00Z. The warning starts when
	// the whole days left (rounded down) are 7 or fewer, as agent-run counts.
	f := newNoticeFix(t, time.Date(2026, 11, 8, 15, 0, 0, 0, time.UTC))
	f.hub.Report(ProviderClaude, LoginReady, noticeLapse)

	f.tick() // 8 days 2h out: 8 whole days
	f.at("2026-11-08T17:00:00Z")
	f.tick() // 8 days 0h51m: still 8
	if f.fake.count() != 0 {
		t.Fatalf("a warning went out with 8 whole days left: %v", f.fake.titles())
	}

	f.at("2026-11-08T18:00:00Z") // 13:00 local, 7 days 23h51m left: 7 whole days
	f.tick()
	if f.fake.count() != 1 {
		t.Fatalf("no warning at 7 whole days: %v", f.fake.titles())
	}
	req := f.fake.last()
	if req.form.Get("priority") != "0" || req.form.Get("title") != "Claude login lapses in 7 days" ||
		!strings.Contains(req.form.Get("message"), "renew from the standby") || !strings.Contains(req.form.Get("message"), "2026-11-16 17:51 UTC") ||
		req.form.Get("url") != NoticeLinkURL || req.form.Get("url_title") != NoticeLinkTitle {
		t.Fatalf("warning = %v", req.form)
	}
	f.tick()
	f.at("2026-11-09T01:30:00Z") // 20:30 local, same local day, inside the window
	f.tick()
	if f.fake.count() != 1 {
		t.Fatalf("a second warning in the same local day: %v", f.fake.titles())
	}

	f.at("2026-11-09T03:00:00Z") // 22:00 local: outside the window
	f.tick()
	f.at("2026-11-09T13:59:00Z") // 08:59 local, next local day, window not yet open
	f.tick()
	if f.fake.count() != 1 {
		t.Fatalf("a warning went out outside 09:00-21:00 local: %v", f.fake.titles())
	}

	f.at("2026-11-09T14:00:00Z") // 09:00 local, next day, 7 days 3h left
	f.tick()
	if f.fake.count() != 2 || f.fake.last().form.Get("title") != "Claude login lapses in 7 days" {
		t.Fatalf("the next day's warning: %v", f.fake.titles())
	}
	f.at("2026-11-10T14:00:00Z") // 6 days 3h left
	f.tick()
	if f.fake.count() != 3 || f.fake.last().form.Get("title") != "Claude login lapses in 6 days" {
		t.Fatalf("six days: %v", f.fake.titles())
	}
	f.at("2026-11-15T15:00:00Z") // 1 day 2h left
	f.tick()
	if f.fake.count() != 4 || f.fake.last().form.Get("title") != "Claude login lapses in 1 day" {
		t.Fatalf("one day: %v", f.fake.titles())
	}
	f.at("2026-11-16T15:00:00Z") // 10:00 local, under a day left
	f.tick()
	if f.fake.count() != 5 || f.fake.last().form.Get("title") != "Claude login lapses in under a day" {
		t.Fatalf("the last day's warning: %v", f.fake.titles())
	}
	f.at("2026-11-16T18:00:00Z") // lapsed: NeedsLogin announces that, not a warning
	f.tick()
	if f.fake.count() != 5 {
		t.Fatalf("a warning for an already lapsed login: %v", f.fake.titles())
	}
}

// The notice and agent-run count days the same way: the time left divided by
// 24 hours, rounded down, and 7 or fewer warns.
func TestWholeDaysLeftRule(t *testing.T) {
	for _, tc := range []struct {
		left time.Duration
		want int
	}{
		{-time.Hour, 0}, {0, 0}, {23*time.Hour + 59*time.Minute, 0}, {24 * time.Hour, 1},
		{7 * 24 * time.Hour, 7}, {7*24*time.Hour + 23*time.Hour + 59*time.Minute, 7}, {8 * 24 * time.Hour, 8},
	} {
		if got := WholeDaysLeft(tc.left); got != tc.want {
			t.Errorf("WholeDaysLeft(%v) = %d, want %d", tc.left, got, tc.want)
		}
	}
}

// The window is the local wall clock. On a day the clocks change, elapsed time
// since midnight is an hour off.
func TestWindowFollowsTheWallClockOnDSTDays(t *testing.T) {
	f := newNoticeFix(t, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))
	ny, _ := time.LoadLocation("America/New_York")
	at := func(y int, m time.Month, d, h, min int) time.Time { return time.Date(y, m, d, h, min, 0, 0, ny) }
	for _, tc := range []struct {
		name string
		t    time.Time
		want bool
	}{
		{"fall-back day 08:30", at(2026, 11, 1, 8, 30), false},
		{"fall-back day 09:00", at(2026, 11, 1, 9, 0), true},
		{"fall-back day 20:30", at(2026, 11, 1, 20, 30), true},
		{"fall-back day 21:00", at(2026, 11, 1, 21, 0), false},
		{"spring-forward day 08:30", at(2027, 3, 14, 8, 30), false},
		{"spring-forward day 09:00", at(2027, 3, 14, 9, 0), true},
		{"spring-forward day 20:30", at(2027, 3, 14, 20, 30), true},
		{"spring-forward day 21:00", at(2027, 3, 14, 21, 0), false},
		{"ordinary day 09:00", at(2026, 11, 10, 9, 0), true},
		{"ordinary day 21:00", at(2026, 11, 10, 21, 0), false},
	} {
		if got := f.n.inWindow(tc.t.In(ny)); got != tc.want {
			t.Errorf("%s: inWindow = %v, want %v", tc.name, got, tc.want)
		}
	}
	// End to end on the fall-back day: no warning at 08:30 local, one at 09:00.
	f.hub.Report(ProviderClaude, LoginReady, time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC))
	f.clk.SetTime(at(2026, 11, 1, 8, 30).UTC())
	f.tick()
	if f.fake.count() != 0 {
		t.Fatal("warned at 08:30 local on the fall-back day")
	}
	f.clk.SetTime(at(2026, 11, 1, 9, 0).UTC())
	f.tick()
	if f.fake.count() != 1 {
		t.Fatal("no warning at 09:00 local on the fall-back day")
	}
}

func TestWarningResetsOnlyForALaterLapse(t *testing.T) {
	f := newNoticeFix(t, time.Date(2026, 11, 12, 16, 0, 0, 0, time.UTC))
	f.hub.Report(ProviderClaude, LoginReady, noticeLapse)
	f.tick()
	if f.fake.count() != 1 {
		t.Fatal("no first warning")
	}
	// The provider moves the date by half an hour on a refresh: not a renewal.
	f.hub.Report(ProviderClaude, LoginReady, noticeLapse.Add(30*time.Minute))
	f.tick()
	if f.fake.count() != 1 {
		t.Fatal("a small date move caused a second warning in the day")
	}
	// A clearly later date within the window resets it once.
	f.hub.Report(ProviderClaude, LoginReady, noticeLapse.Add(3*time.Hour))
	f.tick()
	if f.fake.count() != 2 {
		t.Fatalf("a later lapse date did not reset the warning: %v", f.fake.titles())
	}
	f.tick()
	if f.fake.count() != 2 {
		t.Fatal("reset warned twice")
	}
	// Renewed for 30 days: outside the 7 day window, nothing.
	f.hub.Report(ProviderClaude, LoginReady, noticeLapse.Add(30*24*time.Hour))
	f.at("2026-11-13T16:00:00Z")
	f.tick()
	if f.fake.count() != 2 {
		t.Fatal("warned for a renewed login")
	}
}

func TestNoWarningWhenTheLapseIsUnknown(t *testing.T) {
	f := newNoticeFix(t, time.Date(2026, 11, 12, 16, 0, 0, 0, time.UTC))
	f.hub.Report(ProviderClaude, LoginReady, time.Time{})
	f.hub.Report(ProviderCodex, LoginReady, time.Time{})
	f.tick()
	if f.fake.count() != 0 {
		t.Fatalf("a warning with no lapse date: %v", f.fake.titles())
	}
}

func TestCriticalOncePerIncidentAndOneResolved(t *testing.T) {
	// 03:00 local: a critical notice goes out at any hour.
	f := newNoticeFix(t, time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC))
	f.hub.Report(ProviderClaude, LoginReady, time.Time{})
	f.tick()
	if f.fake.count() != 0 {
		t.Fatal("a notice while Ready")
	}
	f.hub.Report(ProviderClaude, LoginNeedsLogin, time.Time{})
	f.tick()
	if f.fake.count() != 1 {
		t.Fatalf("critical: %v", f.fake.titles())
	}
	req := f.fake.last()
	if req.form.Get("priority") != "1" || req.form.Get("title") != "Keeper needs login (claude)" || req.form.Get("url") != NoticeLinkURL ||
		!strings.Contains(req.form.Get("message"), "agent-run keeper-login claude url") {
		t.Fatalf("critical = %v", req.form)
	}
	for i := 0; i < 5; i++ {
		f.clk.Step(30 * time.Second)
		f.tick()
	}
	f.clk.Step(48 * time.Hour)
	f.tick()
	if f.fake.count() != 1 {
		t.Fatalf("critical repeated within one incident: %v", f.fake.titles())
	}
	// A ceremony in flight does not end the incident or start a new one.
	f.hub.Report(ProviderClaude, LoginInProgress, time.Time{})
	f.tick()
	f.hub.Report(ProviderClaude, LoginNeedsLogin, time.Time{})
	f.tick()
	if f.fake.count() != 1 {
		t.Fatalf("an in-progress ceremony changed the incident: %v", f.fake.titles())
	}
	f.hub.Report(ProviderClaude, LoginReady, time.Time{})
	f.tick()
	f.tick()
	if f.fake.count() != 2 || f.fake.last().form.Get("priority") != "0" || f.fake.last().form.Get("title") != "Keeper login restored (claude)" {
		t.Fatalf("resolved: %v", f.fake.titles())
	}
	f.tick()
	if f.fake.count() != 2 {
		t.Fatal("resolved repeated")
	}
	// A new incident is announced again.
	f.hub.Report(ProviderClaude, LoginNeedsLogin, time.Time{})
	f.tick()
	if f.fake.count() != 3 || f.fake.last().form.Get("priority") != "1" {
		t.Fatalf("a second incident: %v", f.fake.titles())
	}
	// The Codex incident is its own.
	f.hub.Report(ProviderCodex, LoginNeedsLogin, time.Time{})
	f.tick()
	if f.fake.count() != 4 || f.fake.last().form.Get("title") != "Keeper needs login (codex)" {
		t.Fatalf("codex incident: %v", f.fake.titles())
	}
}

func TestNothingRepeatsAfterARestart(t *testing.T) {
	f := newNoticeFix(t, time.Date(2026, 11, 12, 16, 0, 0, 0, time.UTC))
	f.hub.Report(ProviderClaude, LoginReady, noticeLapse)
	f.hub.Report(ProviderCodex, LoginNeedsLogin, time.Time{})
	f.tick()
	if f.fake.count() != 2 {
		t.Fatalf("setup: %v", f.fake.titles())
	}
	// The keeper restarts: a fresh notifier over the stored state, same hour.
	f.n = f.newNotifier()
	f.tick()
	f.clk.Step(time.Minute)
	f.tick()
	if f.fake.count() != 2 {
		t.Fatalf("a restart repeated notices: %v", f.fake.titles())
	}
	// Still one resolved notice for the incident that began before the restart.
	f.hub.Report(ProviderCodex, LoginReady, time.Time{})
	f.n = f.newNotifier()
	f.tick()
	f.n = f.newNotifier()
	f.tick()
	if f.fake.count() != 3 || f.fake.last().form.Get("title") != "Keeper login restored (codex)" {
		t.Fatalf("resolved across restarts: %v", f.fake.titles())
	}
	// And the next local day warns again, once.
	f.at("2026-11-13T16:00:00Z")
	f.n = f.newNotifier()
	f.tick()
	f.tick()
	if f.fake.count() != 4 {
		t.Fatalf("next day: %v", f.fake.titles())
	}
}

func TestFailedSendsBackOffAndAreNotRecorded(t *testing.T) {
	f := newNoticeFix(t, time.Date(2026, 11, 12, 16, 0, 0, 0, time.UTC))
	f.fake.setStatus(500)
	f.hub.Report(ProviderClaude, LoginNeedsLogin, time.Time{})
	f.tick()
	for i := 0; i < 8; i++ {
		f.clk.Step(30 * time.Second)
		f.tick()
	}
	if f.fake.count() != 1 {
		t.Fatalf("a failing send is retried in a tight loop: %d requests in 4 minutes", f.fake.count())
	}
	f.fake.setStatus(200)
	f.clk.Step(2 * time.Minute) // past the 5 minute backoff
	f.tick()
	if f.fake.count() != 2 {
		t.Fatalf("not retried after the backoff: %d", f.fake.count())
	}
	f.clk.Step(10 * time.Minute)
	f.tick()
	if f.fake.count() != 2 {
		t.Fatal("repeated after success")
	}
}

func TestRedirectIsNotFollowedAndNon2xxIsAnError(t *testing.T) {
	f := newNoticeFix(t, time.Date(2026, 11, 12, 16, 0, 0, 0, time.UTC))
	other := newPushoverFake(t)
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.srv.URL, http.StatusTemporaryRedirect)
	}))
	defer redir.Close()
	s := &PushoverSender{URL: redir.URL, TokenFile: filepath.Join(f.dir, "token"), UserFile: filepath.Join(f.dir, "user")}
	if err := s.Send(context.Background(), Notice{Title: "t", Message: "m"}); err == nil {
		t.Fatal("a redirect counted as sent")
	}
	if other.count() != 0 {
		t.Fatal("the redirect was followed with the credentials in the body")
	}
}

// An unusable store must not silence a notice, the critical one above all. The
// process keeps its own dedup in memory, says why once, and keeps trying the store.
func TestUnusableStateFallsBackToMemoryAndKeepsRetrying(t *testing.T) {
	f := newNoticeFix(t, time.Date(2026, 11, 12, 16, 0, 0, 0, time.UTC))
	f.store.fail = true
	f.hub.Report(ProviderClaude, LoginNeedsLogin, time.Time{})
	f.tick()
	if f.fake.count() != 1 || f.fake.last().form.Get("priority") != "1" {
		t.Fatalf("the critical notice was silenced by an unusable state: %v", f.fake.titles())
	}
	for i := 0; i < 5; i++ {
		f.clk.Step(time.Minute)
		f.tick()
	}
	if f.fake.count() != 1 {
		t.Fatalf("the in-memory dedup repeated the critical: %v", f.fake.titles())
	}
	if got := strings.Count(f.logs.String(), "notifier using in-memory state: notice state is unavailable"); got != 1 {
		t.Fatalf("the fallback was logged %d times over 6 ticks, want once:\n%s", got, f.logs.String())
	}
	if got := strings.Count(f.logs.String(), "notifier enabled"); got != 1 {
		t.Fatalf("the enabled line was logged %d times, want once:\n%s", got, f.logs.String())
	}
	if got := strings.Count(f.logs.String(), "notice state not stored yet"); got > 1 {
		t.Fatalf("the save line was logged %d times, want at most once", got)
	}
	// The store comes back: it is read again, the process's record is merged in and
	// stored, and a restart now repeats nothing.
	f.store.fail = false
	f.tick()
	if f.store.saves == 0 || !strings.Contains(f.logs.String(), "notice state is stored again") {
		t.Fatalf("the state was not stored once the store returned (saves=%d)", f.store.saves)
	}
	f.n = f.newNotifier()
	f.tick()
	if f.fake.count() != 1 {
		t.Fatalf("a restart after recovery repeated the critical: %v", f.fake.titles())
	}
	f.hub.Report(ProviderClaude, LoginReady, time.Time{})
	f.tick()
	if f.fake.count() != 2 || f.fake.last().form.Get("title") != "Keeper login restored (claude)" {
		t.Fatalf("resolved after recovery: %v", f.fake.titles())
	}
}

type forbiddenClient struct{ client.Client }

func (forbiddenClient) Get(_ context.Context, key client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
	return apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, key.Name, errors.New("synthetic RBAC refusal"))
}

func TestMissingForbiddenAndCorruptConfigMapsDoNotSilenceTheCritical(t *testing.T) {
	name := types.NamespacedName{Namespace: "system", Name: DefaultNoticeStateConfigMap}
	for _, tc := range []struct {
		name   string
		store  func(t *testing.T) (*ConfigMapNoticeStore, client.Client)
		reason string
	}{
		{"missing", func(t *testing.T) (*ConfigMapNoticeStore, client.Client) {
			c := claudeClient(t)
			return &ConfigMapNoticeStore{Client: c, Name: name}, c
		}, "the notice ConfigMap does not exist"},
		{"forbidden", func(t *testing.T) (*ConfigMapNoticeStore, client.Client) {
			return &ConfigMapNoticeStore{Client: forbiddenClient{claudeClient(t)}, Name: name}, nil
		}, "the keeper may not use the notice ConfigMap"},
		{"corrupt", func(t *testing.T) (*ConfigMapNoticeStore, client.Client) {
			c := claudeClient(t, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: name.Namespace, Name: name.Name}, Data: map[string]string{noticeStateKey: "{not json"}})
			return &ConfigMapNoticeStore{Client: c, Name: name}, c
		}, "the notice state in the ConfigMap cannot be read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newNoticeFix(t, time.Date(2026, 11, 12, 16, 0, 0, 0, time.UTC))
			store, c := tc.store(t)
			f.alt = store
			f.n = f.newNotifier()
			f.hub.Report(ProviderClaude, LoginNeedsLogin, time.Time{})
			f.tick()
			f.tick()
			if f.fake.count() != 1 || f.fake.last().form.Get("priority") != "1" {
				t.Fatalf("critical not sent with a %s ConfigMap: %v", tc.name, f.fake.titles())
			}
			if got := strings.Count(f.logs.String(), "notifier using in-memory state: "+tc.reason); got != 1 {
				t.Fatalf("reason %q logged %d times:\n%s", tc.reason, got, f.logs.String())
			}
			switch tc.name {
			case "corrupt":
				// Saving repairs it: the keeper is its only writer.
				var cm corev1.ConfigMap
				if err := c.Get(context.Background(), name, &cm); err != nil {
					t.Fatal(err)
				}
				if _, err := unmarshalState([]byte(cm.Data[noticeStateKey])); err != nil {
					t.Fatalf("the corrupt state was not repaired: %q", cm.Data[noticeStateKey])
				}
				f.n = f.newNotifier()
				f.tick()
				if f.fake.count() != 1 {
					t.Fatal("repeated after the state was repaired")
				}
			case "missing":
				// Created later: found on a later pass, and the record is stored.
				if err := c.Create(context.Background(), &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: name.Namespace, Name: name.Name}}); err != nil {
					t.Fatal(err)
				}
				f.tick()
				var cm corev1.ConfigMap
				_ = c.Get(context.Background(), name, &cm)
				if !strings.Contains(cm.Data[noticeStateKey], "claude") {
					t.Fatalf("the in-memory record was not stored once the ConfigMap existed: %q", cm.Data[noticeStateKey])
				}
				if f.fake.count() != 1 {
					t.Fatal("repeated when the ConfigMap appeared")
				}
			}
			for _, text := range []string{f.logs.String()} {
				if strings.Contains(text, "synthetic RBAC refusal") || strings.Contains(text, "CANARY") {
					t.Fatal("the log carries text from the API server or a credential")
				}
			}
		})
	}
}

// A Pushover 4xx (except 429) refuses the request itself; retrying every few
// minutes for the whole incident only invites a block. 429 and 5xx keep retrying.
func TestPushover4xxIsNotRetriedUntilTheStateChanges(t *testing.T) {
	f := newNoticeFix(t, time.Date(2026, 11, 12, 16, 0, 0, 0, time.UTC))
	f.fake.setStatus(401)
	f.hub.Report(ProviderClaude, LoginNeedsLogin, time.Time{})
	f.tick()
	for i := 0; i < 12; i++ { // an hour of ticks, well past the backoff
		f.clk.Step(5 * time.Minute)
		f.tick()
	}
	if f.fake.count() != 1 {
		t.Fatalf("a refused critical was retried: %d requests in an hour", f.fake.count())
	}
	if got := strings.Count(f.logs.String(), "refused by Pushover (status 401)"); got != 1 {
		t.Fatalf("the refusal was logged %d times, want once", got)
	}
	// The same incident ending and a new one starting is a state change.
	f.fake.setStatus(200)
	f.hub.Report(ProviderClaude, LoginReady, time.Time{})
	f.tick()
	f.clk.Step(time.Minute)
	f.hub.Report(ProviderClaude, LoginNeedsLogin, time.Time{})
	f.tick()
	if f.fake.count() != 2 || f.fake.last().form.Get("priority") != "1" {
		t.Fatalf("a new incident was not tried: %v", f.fake.titles())
	}
	// A restart tries again once.
	g := newNoticeFix(t, time.Date(2026, 11, 12, 16, 0, 0, 0, time.UTC))
	g.fake.setStatus(400)
	g.hub.Report(ProviderClaude, LoginNeedsLogin, time.Time{})
	g.tick()
	g.n = g.newNotifier()
	g.tick()
	if g.fake.count() != 2 {
		t.Fatalf("a restart should try the refused notice again once: %d", g.fake.count())
	}
}

func TestPushover4xxWarningWaitsForALaterLapseDate(t *testing.T) {
	f := newNoticeFix(t, time.Date(2026, 11, 12, 16, 0, 0, 0, time.UTC))
	f.fake.setStatus(403)
	f.hub.Report(ProviderClaude, LoginReady, noticeLapse)
	f.tick()
	f.at("2026-11-13T16:00:00Z") // the next local day
	f.tick()
	f.at("2026-11-14T16:00:00Z")
	f.tick()
	if f.fake.count() != 1 {
		t.Fatalf("a refused warning was retried: %d", f.fake.count())
	}
	f.fake.setStatus(200)
	f.hub.Report(ProviderClaude, LoginReady, noticeLapse.Add(3*time.Hour)) // the login was renewed
	f.tick()
	if f.fake.count() != 2 {
		t.Fatalf("a later lapse date did not lift the block: %d", f.fake.count())
	}
}

func TestPushover429And5xxKeepTheBoundedRetry(t *testing.T) {
	for _, status := range []int{429, 500, 503} {
		f := newNoticeFix(t, time.Date(2026, 11, 12, 16, 0, 0, 0, time.UTC))
		f.fake.setStatus(status)
		f.hub.Report(ProviderClaude, LoginNeedsLogin, time.Time{})
		f.tick()
		f.clk.Step(6 * time.Minute)
		f.tick()
		f.clk.Step(6 * time.Minute)
		f.tick()
		if f.fake.count() != 3 {
			t.Errorf("status %d: %d requests over 12 minutes, want 3 (one per backoff)", status, f.fake.count())
		}
		f.fake.setStatus(200)
		f.clk.Step(6 * time.Minute)
		f.tick()
		if f.fake.count() != 4 {
			t.Errorf("status %d: not sent once Pushover recovered", status)
		}
	}
}

func TestSaveFailureDoesNotCauseARepeat(t *testing.T) {
	f := newNoticeFix(t, time.Date(2026, 11, 12, 16, 0, 0, 0, time.UTC))
	f.hub.Report(ProviderClaude, LoginNeedsLogin, time.Time{})
	f.tick() // loads fine, sends, saves
	f.store.fail = true
	f.hub.Report(ProviderClaude, LoginReady, time.Time{})
	f.tick() // sends resolved; the save fails
	f.tick()
	f.tick()
	if f.fake.count() != 2 {
		t.Fatalf("a failed save repeated a notice: %v", f.fake.titles())
	}
	f.store.fail = false
	f.tick() // the pending change is stored now
	f.n = f.newNotifier()
	f.tick()
	if f.fake.count() != 2 {
		t.Fatalf("repeat after the state was finally stored: %v", f.fake.titles())
	}
}

func TestDisabledWithoutCredentialsStillTracks(t *testing.T) {
	f := newNoticeFix(t, time.Date(2026, 11, 12, 16, 0, 0, 0, time.UTC))
	for _, name := range []string{"token", "user"} {
		if err := os.Remove(filepath.Join(f.dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	f.hub.Report(ProviderClaude, LoginNeedsLogin, time.Time{})
	f.tick()
	f.tick()
	f.tick()
	if f.fake.count() != 0 || f.store.saves != 0 {
		t.Fatal("a disabled notifier sent or stored something")
	}
	if got := strings.Count(f.logs.String(), "notifier disabled: no Pushover credentials"); got != 1 {
		t.Fatalf("disabled line logged %d times, want once:\n%s", got, f.logs.String())
	}
	if series(t, f.hub, "dev_env_keeper_login_state")["provider=claude,state=NeedsLogin"] != 1 {
		t.Fatal("the gauges stopped following the journal")
	}
	// An empty file is no credential either.
	if err := os.WriteFile(filepath.Join(f.dir, "token"), []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, "user"), []byte(noticeUserCanary), 0o600); err != nil {
		t.Fatal(err)
	}
	f.tick()
	if f.fake.count() != 0 {
		t.Fatal("sent with an empty token file")
	}
	// Credentials that arrive later switch it on.
	if err := os.WriteFile(filepath.Join(f.dir, "token"), []byte(noticeTokenCanary), 0o600); err != nil {
		t.Fatal(err)
	}
	f.tick()
	if f.fake.count() != 1 {
		t.Fatal("not enabled once the credentials appeared")
	}
}

func TestCredentialsAppearOnlyInTheRequestFormFields(t *testing.T) {
	f := newNoticeFix(t, time.Date(2026, 11, 12, 16, 0, 0, 0, time.UTC))
	f.fake.echo = true
	f.hub.Report(ProviderClaude, LoginReady, noticeLapse)
	f.hub.Report(ProviderCodex, LoginNeedsLogin, time.Time{})
	f.tick()
	f.fake.setStatus(400) // the API errors and repeats the request body
	f.hub.Report(ProviderClaude, LoginNeedsLogin, time.Time{})
	f.clk.Step(time.Hour)
	f.tick()
	if f.fake.count() < 2 {
		t.Fatal("setup: expected a failing request")
	}
	// The error text a sender returns carries no body.
	err := (&PushoverSender{URL: f.fake.srv.URL, TokenFile: filepath.Join(f.dir, "token"), UserFile: filepath.Join(f.dir, "user")}).Send(context.Background(), Notice{Title: "t", Message: "m"})
	if err == nil {
		t.Fatal("expected an error")
	}
	var metricsText strings.Builder
	for _, sample := range []string{"dev_env_keeper_login_state", "dev_env_keeper_login_lapse_timestamp_seconds"} {
		for k, v := range series(t, f.hub, sample) {
			fmt.Fprintf(&metricsText, "%s{%s} %v\n", sample, k, v)
		}
	}
	f.fake.mu.Lock()
	defer f.fake.mu.Unlock()
	var requests strings.Builder
	for _, r := range f.fake.reqs {
		if r.form.Get("token") != noticeTokenCanary || r.form.Get("user") != noticeUserCanary {
			t.Fatalf("form fields token/user missing: %v", r.form)
		}
		if strings.Contains(r.path+r.rawQuery, "CANARY") {
			t.Fatal("a credential reached the URL")
		}
		for k, vs := range r.header {
			for _, v := range vs {
				if strings.Contains(k+v, "CANARY") {
					t.Fatalf("a credential reached header %s", k)
				}
			}
		}
		for k, vs := range r.form {
			if k == "token" || k == "user" {
				continue
			}
			requests.WriteString(strings.Join(vs, " ") + "\n")
		}
	}
	for name, text := range map[string]string{"logs": f.logs.String(), "error": err.Error(), "metrics": metricsText.String(), "notice text": requests.String()} {
		if strings.Contains(text, noticeTokenCanary) || strings.Contains(text, noticeUserCanary) {
			t.Errorf("a credential leaked into the %s", name)
		}
	}
}

func TestConfigMapNoticeStoreRoundTrip(t *testing.T) {
	name := types.NamespacedName{Namespace: "system", Name: DefaultNoticeStateConfigMap}
	c := claudeClient(t, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: name.Namespace, Name: name.Name}})
	s := &ConfigMapNoticeStore{Client: c, Name: name}
	got, err := s.Load(context.Background())
	if err != nil || len(got.Incidents) != 0 {
		t.Fatalf("empty ConfigMap: %+v %v", got, err)
	}
	want := NoticeState{Version: 1, Warnings: map[string]warnRecord{"claude": {Day: "2026-11-09", Lapse: noticeLapse.Unix()}}, Incidents: map[string]incidentRecord{"codex": {ID: "codex-1", Opened: 1, Notified: true}}}
	if err := s.Save(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	got, err = s.Load(context.Background())
	if err != nil || got.Warnings["claude"] != want.Warnings["claude"] || got.Incidents["codex"] != want.Incidents["codex"] {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	var cm corev1.ConfigMap
	_ = c.Get(context.Background(), name, &cm)
	if strings.Contains(cm.Data[noticeStateKey], "CANARY") || strings.Contains(strings.ToLower(cm.Data[noticeStateKey]), "token") {
		t.Fatal("the stored state names a credential")
	}
	// A missing ConfigMap is an error, never a create.
	missing := &ConfigMapNoticeStore{Client: claudeClient(t), Name: name}
	if _, err := missing.Load(context.Background()); err == nil {
		t.Fatal("loaded a ConfigMap that does not exist")
	}
	if err := missing.Save(context.Background(), want); err == nil {
		t.Fatal("saved to a ConfigMap that does not exist")
	}
}

func TestTestNoticeIsFixedAndTokenFree(t *testing.T) {
	f := newNoticeFix(t, time.Date(2026, 11, 12, 16, 0, 0, 0, time.UTC))
	s := &PushoverSender{URL: f.fake.srv.URL, TokenFile: filepath.Join(f.dir, "token"), UserFile: filepath.Join(f.dir, "user")}
	if err := SendTestNotice(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	r := f.fake.last()
	if r.form.Get("title") != "dev-env test notice" || r.form.Get("message") != "dev-env test notice" || r.form.Get("priority") != "0" ||
		r.form.Get("url") != NoticeLinkURL || r.form.Get("url_title") != NoticeLinkTitle {
		t.Fatalf("test notice = %v", r.form)
	}
	s.TokenFile = filepath.Join(f.dir, "absent")
	err := SendTestNotice(context.Background(), s)
	if !errors.Is(err, errPushoverCredentials) || strings.Contains(err.Error(), "CANARY") {
		t.Fatalf("missing credentials: %v", err)
	}
}

func TestNoticeOptionsValidation(t *testing.T) {
	if err := (NoticeOptions{}).Validate(); err != nil {
		t.Fatalf("defaults: %v", err)
	}
	for name, o := range map[string]NoticeOptions{
		"zone":   {Zone: "Mars/Olympus"},
		"start":  {WindowStart: "9am"},
		"end":    {WindowEnd: "25:00"},
		"order":  {WindowStart: "21:00", WindowEnd: "09:00"},
		"scheme": {URL: "http://api.pushover.net/1/messages.json"},
	} {
		if err := o.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
