package keeper

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clocktesting "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/pages"
)

type askNoticeReaderFake struct {
	asks       map[string]apiv1.Ask
	reads      int
	incomplete bool
	err        error
	beforeGet  func(string)
}

func (r *askNoticeReaderFake) ListNamespace(context.Context) ([]apiv1.Ask, bool, error) {
	r.reads++
	var out []apiv1.Ask
	for _, a := range r.asks {
		out = append(out, a)
	}
	return out, r.incomplete, r.err
}
func (r *askNoticeReaderFake) Get(_ context.Context, id string) (apiv1.Ask, error) {
	r.reads++
	if r.beforeGet != nil {
		r.beforeGet(id)
	}
	a, ok := r.asks[id]
	if !ok {
		return a, errors.New("missing fixture")
	}
	return a, r.err
}

type askNoticeSenderFake struct {
	notices []Notice
	rc      Receipt
	err     error
	before  func()
}

func (s *askNoticeSenderFake) Send(ctx context.Context, n Notice) error {
	_, err := s.SendReceipt(ctx, n)
	return err
}
func (s *askNoticeSenderFake) SendReceipt(_ context.Context, n Notice) (Receipt, error) {
	if s.before != nil {
		s.before()
	}
	s.notices = append(s.notices, n)
	return s.rc, s.err
}

type askNoticeFixture struct {
	ctx    context.Context
	clock  *clocktesting.FakeClock
	reader *askNoticeReaderFake
	sender *askNoticeSenderFake
	store  *configMapAskNotificationStore
	opts   AskNotificationOptions
	n      *askNotifier
	logs   bytes.Buffer
}

func newAskNoticeFixture(t *testing.T) *askNoticeFixture {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	name := types.NamespacedName{Namespace: "dev-env-system", Name: DefaultAskNotificationConfigMap}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: name.Namespace, Name: name.Name}}).Build()
	f := &askNoticeFixture{ctx: context.Background(),
		clock:  clocktesting.NewFakeClock(time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)),
		reader: &askNoticeReaderFake{asks: map[string]apiv1.Ask{}},
		sender: &askNoticeSenderFake{rc: Receipt{Status: 200, Confirmed: true}},
		store:  &configMapAskNotificationStore{client: c, name: name},
		opts:   AskNotificationOptions{Enabled: true, PortalOrigin: "https://portal.example"},
	}
	f.restart(t)
	return f
}

func (f *askNoticeFixture) restart(t *testing.T) {
	t.Helper()
	n, err := newAskNotifier(f.opts, f.reader, f.store, f.sender, f.clock, logr.FromSlogHandler(slog.NewJSONHandler(&f.logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	f.n = n
}

func (f *askNoticeFixture) add(i int, uid string) string {
	id := fmt.Sprintf("ask-%016x", i)
	f.reader.asks[id] = apiv1.Ask{ID: id, State: apiv1.AskStateOpen, Type: apiv1.AskTypeInfo,
		Question: "private-question-never-in-a-push", AllowFreeText: true,
		Context:   apiv1.AskContext{SessionName: "fixture-session", SessionUID: uid, Links: []string{"https://github.com/private-pr-title"}},
		CreatedAt: f.clock.Now(), ExpiresAt: f.clock.Now().Add(24 * time.Hour),
	}
	return id
}

func (f *askNoticeFixture) ledger(t *testing.T) *askNotificationLedger {
	t.Helper()
	l, err := f.store.load(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestAskNotificationsCoalescePrivacyAndRestart(t *testing.T) {
	f := newAskNoticeFixture(t)
	first := f.add(1, "requester-private-uid")
	for i := 2; i <= 5; i++ {
		f.add(i, "requester-private-uid")
	}
	f.sender.before = func() {
		l := f.ledger(t)
		if len(l.State.Seen) != 5 || len(l.Pages.Records) != 1 || l.Pages.Records[0].State != pages.StateRecorded {
			t.Fatal("send preceded durable dedupe, rate and page reservation")
		}
	}
	f.n.tick(f.ctx)
	f.sender.before = nil
	if !f.n.NeedLeaderElection() || len(f.sender.notices) != 1 {
		t.Fatal("missing leader gate or coalescing")
	}
	n := f.sender.notices[0]
	if n.Title != askNoticeText || n.Message != askNoticeText || n.Priority != 0 || n.URL != "https://portal.example"+askNoticePath+"?ask="+first {
		t.Fatalf("unexpected push: %+v", n)
	}
	l := f.ledger(t)
	if len(l.State.Seen) != 5 || l.Pages.Records[0].State != pages.StateSent || l.Pages.Records[0].Correlation != first || l.Pages.Records[0].Source != askNoticeKind {
		t.Fatal("missing correlation/outcome")
	}
	raw, _ := json.Marshal(l)
	for _, forbidden := range []string{"private-question", "private-pr-title", "requester-private-uid", "fixture-session", "https://portal.example"} {
		if bytes.Contains(raw, []byte(forbidden)) || strings.Contains(f.logs.String(), forbidden) {
			t.Fatalf("private content persisted/logged: %s", forbidden)
		}
	}
	if l.Pages.Records[0].Title != "" || l.Pages.Records[0].Body != "" || l.Pages.Records[0].URL != "" {
		t.Fatal("page record included push content")
	}
	f.restart(t)
	f.n.tick(f.ctx)
	if len(f.sender.notices) != 1 {
		t.Fatal("restart repeated a push")
	}
	f.add(6, "requester-private-uid")
	f.clock.Step(5*time.Minute + noticeStoreTimeout - time.Second)
	f.n.tick(f.ctx)
	if len(f.sender.notices) != 1 {
		t.Fatal("requester interval violated")
	}
	f.clock.Step(time.Second)
	f.n.tick(f.ctx)
	if len(f.sender.notices) != 2 {
		t.Fatal("pending Ask not sent at requester boundary")
	}
	f.clock.Step(time.Hour)
	f.restart(t)
	f.n.tick(f.ctx)
	if len(f.sender.notices) != 2 {
		t.Fatal("dedupe was pruned for a live Ask")
	}
}

func TestAskNotificationsRecipientCapSurvivesRestart(t *testing.T) {
	f := newAskNoticeFixture(t)
	for i := 1; i <= 7; i++ {
		f.add(i, fmt.Sprintf("requester-%d", i))
	}
	f.n.tick(f.ctx)
	if len(f.sender.notices) != 6 {
		t.Fatalf("first hour: %d", len(f.sender.notices))
	}
	f.restart(t)
	f.clock.Step(time.Hour + noticeStoreTimeout - time.Second)
	f.n.tick(f.ctx)
	if len(f.sender.notices) != 6 {
		t.Fatal("restart lost recipient cap")
	}
	f.clock.Step(time.Second)
	f.n.tick(f.ctx)
	if len(f.sender.notices) != 7 {
		t.Fatal("seventh Ask was not deferred to the next slot")
	}
}

func TestAskNotificationOutcomesAndRetryBound(t *testing.T) {
	for _, tc := range []struct {
		name     string
		rc       Receipt
		err      error
		state    pages.State
		attempts int
	}{
		{"sent", Receipt{Status: 200, Confirmed: true}, nil, pages.StateSent, 1},
		{"timeout", Receipt{}, errors.New("private-question-token-should-not-be-logged"), pages.StateAmbiguous, 1},
		{"unconfirmed-2xx", Receipt{Status: 200}, nil, pages.StateAmbiguous, 1},
		{"server-error", Receipt{}, &SendError{Status: 500}, pages.StateAmbiguous, 1},
		{"refusal", Receipt{}, &SendError{Status: 400}, pages.StateFailed, 1},
		{"rate-refusal", Receipt{}, &SendError{Status: 429}, pages.StateFailed, 1},
		{"before-send", Receipt{}, errPushoverCredentials, pages.StateFailed, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAskNoticeFixture(t)
			f.add(1, "requester")
			f.sender.rc, f.sender.err = tc.rc, tc.err
			f.n.tick(f.ctx)
			f.restart(t)
			f.clock.Step(5*time.Minute + noticeStoreTimeout - time.Second)
			f.n.tick(f.ctx)
			if len(f.sender.notices) != 1 {
				t.Fatal("retried too early")
			}
			f.clock.Step(time.Second)
			f.n.tick(f.ctx)
			f.clock.Step(time.Hour)
			f.restart(t)
			f.n.tick(f.ctx)
			if len(f.sender.notices) != tc.attempts {
				t.Fatalf("got %d attempts, want %d", len(f.sender.notices), tc.attempts)
			}
			l := f.ledger(t)
			if l.Pages.Records[0].State != tc.state || l.Pages.Records[0].Attempts != tc.attempts {
				t.Fatalf("dishonest page: %+v", l.Pages.Records[0])
			}
			if strings.Contains(f.logs.String(), "private-question") {
				t.Fatal("sender error leaked")
			}
		})
	}
}

func TestAskNotificationsRetryCanSucceedOrBeCancelled(t *testing.T) {
	for _, closed := range []bool{false, true} {
		t.Run(fmt.Sprint(closed), func(t *testing.T) {
			f := newAskNoticeFixture(t)
			id := f.add(1, "requester")
			f.sender.err = errPushoverCredentials
			f.n.tick(f.ctx)
			f.sender.err = nil
			f.clock.Step(5*time.Minute + noticeStoreTimeout)
			if closed {
				a := f.reader.asks[id]
				a.State = apiv1.AskStateAnswered
				f.reader.asks[id] = a
			}
			f.restart(t)
			f.n.tick(f.ctx)
			want := 2
			if closed {
				want = 1
			}
			if len(f.sender.notices) != want {
				t.Fatal("wrong retry count")
			}
			if !closed && f.ledger(t).Pages.Records[0].State != pages.StateSent {
				t.Fatal("successful retry was not sent")
			}
		})
	}
}

type failingAskNotificationStore struct {
	askNotificationStore
	saves  int
	failAt int
	commit bool
}

func (s *failingAskNotificationStore) save(ctx context.Context, l *askNotificationLedger) error {
	s.saves++
	if s.saves == s.failAt {
		if s.commit {
			if err := s.askNotificationStore.save(ctx, l); err != nil {
				return err
			}
		}
		return errAskNotifications
	}
	return s.askNotificationStore.save(ctx, l)
}

func TestAskNotificationsWriteAheadFailureAndUnknown(t *testing.T) {
	for _, tc := range []struct {
		name   string
		failAt int
		commit bool
		sends  int
		page   pages.State
	}{
		{"reservation-refused", 1, false, 0, pages.StateSent},
		{"reservation-unconfirmed", 1, true, 0, pages.StateRecorded},
		{"outcome-lost", 2, false, 1, pages.StateRecorded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAskNoticeFixture(t)
			f.add(1, "requester")
			f.n.store = &failingAskNotificationStore{askNotificationStore: f.store, failAt: tc.failAt, commit: tc.commit}
			f.n.tick(f.ctx)
			if len(f.sender.notices) != tc.sends {
				t.Fatal("sent without confirmed reservation")
			}
			f.restart(t)
			f.clock.Step(time.Hour)
			f.n.tick(f.ctx)
			want := tc.sends
			if tc.failAt == 1 && !tc.commit {
				want = 1
			} // definitely no stored reservation or send
			if len(f.sender.notices) != want || f.ledger(t).Pages.Records[0].State != tc.page {
				t.Fatal("Unknown attempt replayed or incorrectly recorded")
			}
		})
	}
}

func TestAskNotificationQuietWindowDST(t *testing.T) {
	for _, tc := range []struct{ name, window, now, next string }{
		{"empty-all-hours", "", "2026-11-01T04:30:00Z", "2026-11-01T04:30:00Z"},
		{"fall-back-next-morning", "09:00-21:00", "2026-11-01T02:30:00Z", "2026-11-01T14:00:00Z"},
		{"spring-forward-next-morning", "09:00-21:00", "2026-03-08T03:30:00Z", "2026-03-08T13:00:00Z"},
		{"nonexistent-start", "02:30-04:00", "2026-03-08T06:45:00Z", "2026-03-08T07:00:00Z"},
		{"first-repeated-start", "01:30-02:30", "2026-11-01T05:10:00Z", "2026-11-01T05:30:00Z"},
		{"second-repeated-start", "01:30-02:30", "2026-11-01T06:10:00Z", "2026-11-01T06:30:00Z"},
		{"overnight", "21:00-09:00", "2026-10-29T14:00:00Z", "2026-10-30T01:00:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := AskNotificationOptions{QuietHours: tc.window, Timezone: "America/New_York"}
			w, err := o.window()
			if err != nil {
				t.Fatal(err)
			}
			now, _ := time.Parse(time.RFC3339, tc.now)
			if got := w.next(now).UTC().Format(time.RFC3339); got != tc.next {
				t.Fatalf("next=%s want=%s", got, tc.next)
			}
		})
	}
	f := newAskNoticeFixture(t)
	f.opts.QuietHours = "09:00-21:00"
	f.opts.Timezone = "America/New_York"
	f.clock.SetTime(time.Date(2026, 11, 1, 2, 30, 0, 0, time.UTC))
	f.add(1, "requester")
	f.restart(t)
	f.n.tick(f.ctx)
	if f.reader.reads != 0 || len(f.sender.notices) != 0 || len(f.ledger(t).State.Seen) != 0 {
		t.Fatal("quiet hours consumed work/caps")
	}
	f.clock.SetTime(time.Date(2026, 11, 1, 14, 0, 0, 0, time.UTC))
	f.n.tick(f.ctx)
	if len(f.sender.notices) != 1 {
		t.Fatal("did not defer to DST-aware window start")
	}
}

func TestAskNotificationsOffAndUnavailable(t *testing.T) {
	n, err := newAskNotifier(AskNotificationOptions{}, nil, nil, nil, nil, logr.Discard())
	if err != nil || n != nil {
		t.Fatal("off flag created a runnable")
	}
	for _, tc := range []string{"incomplete", "list-error", "corrupt-state", "missing-cm", "closed-at-get", "expired", "approval-expired"} {
		t.Run(tc, func(t *testing.T) {
			f := newAskNoticeFixture(t)
			id := f.add(1, "requester")
			switch tc {
			case "incomplete":
				f.reader.incomplete = true
			case "list-error":
				f.reader.err = errors.New("unavailable")
			case "corrupt-state":
				cm := &corev1.ConfigMap{}
				if err := f.store.client.Get(f.ctx, f.store.name, cm); err != nil {
					t.Fatal(err)
				}
				cm.Data = map[string]string{askNotificationStateKey: "damaged"}
				if err := f.store.client.Update(f.ctx, cm); err != nil {
					t.Fatal(err)
				}
			case "missing-cm":
				if err := f.store.client.Delete(f.ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: f.store.name.Name, Namespace: f.store.name.Namespace}}); err != nil {
					t.Fatal(err)
				}
			case "closed-at-get":
				f.reader.beforeGet = func(id string) { a := f.reader.asks[id]; a.State = apiv1.AskStateWithdrawn; f.reader.asks[id] = a }
			case "expired":
				a := f.reader.asks[id]
				a.ExpiresAt = f.clock.Now()
				f.reader.asks[id] = a
			case "approval-expired":
				a := f.reader.asks[id]
				a.Type = apiv1.AskTypeApproval
				a.Scope = &apiv1.AskScope{ExpiresAt: f.clock.Now()}
				f.reader.asks[id] = a
			}
			f.n.tick(f.ctx)
			if len(f.sender.notices) != 0 {
				t.Fatal("sent while unavailable or no longer open")
			}
		})
	}
}

func TestAskNotificationsCannotOverwriteLoginNoticeState(t *testing.T) {
	for _, name := range []string{DefaultNoticeStateConfigMap, "custom-login-notices"} {
		err := Run(context.Background(), nil, Options{LeaderElect: true,
			AskNotifications: AskNotificationOptions{Enabled: true, PortalOrigin: "https://portal.example", StateConfigMap: name},
			Notices:          NoticeOptions{StateConfigMap: name},
		})
		if err == nil || !strings.Contains(err.Error(), "ConfigMap separate from login notices") {
			t.Fatal("shared ledger was accepted", err)
		}
	}
}

func TestAskNotificationPartialStateFailsClosed(t *testing.T) {
	for _, raw := range []string{`{}`, `null`, `{"version":1}`, `{"version":1,"seen":{},"groups":{}}`} {
		t.Run(raw, func(t *testing.T) {
			f := newAskNoticeFixture(t)
			f.add(1, "requester")
			f.n.tick(f.ctx)
			var cm corev1.ConfigMap
			if err := f.store.client.Get(f.ctx, f.store.name, &cm); err != nil {
				t.Fatal(err)
			}
			cm.Data[askNotificationStateKey] = raw
			if err := f.store.client.Update(f.ctx, &cm); err != nil {
				t.Fatal(err)
			}
			f.restart(t)
			f.clock.Step(time.Hour)
			f.n.tick(f.ctx)
			if len(f.sender.notices) != 1 {
				t.Fatal("partial state replayed a previous attempt")
			}
			if _, err := f.store.load(f.ctx); err == nil {
				t.Fatal("partial/reset state was accepted")
			}
		})
	}
	f := newAskNoticeFixture(t)
	l := f.ledger(t)
	l.State.Groups["ask-0000000000000001"] = askNotificationAttempt{Requester: strings.Repeat("a", 64), Times: []int64{f.clock.Now().Unix()}}
	if l.State.valid() {
		t.Fatal("orphan attempt accepted, could erase rate history")
	}
}

type delayedAskNotificationStore struct {
	askNotificationStore
	clock *clocktesting.FakeClock
	delay time.Duration
}

func (s *delayedAskNotificationStore) save(ctx context.Context, l *askNotificationLedger) error {
	if s.delay != 0 {
		s.clock.Step(s.delay)
		s.delay = 0
	}
	return s.askNotificationStore.save(ctx, l)
}

func TestAskNotificationsSlowReadsAndReservationBoundaries(t *testing.T) {
	t.Run("slow-get-does-not-backdate-cap", func(t *testing.T) {
		f := newAskNoticeFixture(t)
		f.add(1, "requester")
		f.reader.beforeGet = func(string) { f.clock.Step(4*time.Minute + 50*time.Second); f.reader.beforeGet = nil }
		f.n.tick(f.ctx)
		if len(f.sender.notices) != 1 {
			t.Fatal("first send missing")
		}
		first := f.clock.Now()
		f.add(2, "requester")
		f.clock.Step(10 * time.Second)
		f.n.tick(f.ctx)
		if len(f.sender.notices) != 1 {
			t.Fatal("slow GET shortened requester interval")
		}
		f.clock.SetTime(first.Add(5*time.Minute + noticeStoreTimeout))
		f.n.tick(f.ctx)
		if len(f.sender.notices) != 2 {
			t.Fatal("deferred send missing")
		}
	})
	for _, phase := range []string{"get", "save"} {
		t.Run("window-closes-during-"+phase, func(t *testing.T) {
			f := newAskNoticeFixture(t)
			f.clock.SetTime(time.Date(2026, 10, 30, 0, 59, 59, 0, time.UTC)) // 20:59:59 New York
			f.add(1, "requester")
			f.opts.QuietHours = "09:00-21:00"
			f.restart(t)
			if phase == "get" {
				f.reader.beforeGet = func(string) { f.clock.Step(2 * time.Second); f.reader.beforeGet = nil }
			} else {
				f.n.store = &delayedAskNotificationStore{askNotificationStore: f.store, clock: f.clock, delay: 2 * time.Second}
			}
			f.n.tick(f.ctx)
			if len(f.sender.notices) != 0 {
				t.Fatal("sent after allowed window closed")
			}
			f.clock.SetTime(time.Date(2026, 10, 30, 13, 0, 0, 0, time.UTC))
			f.n.tick(f.ctx)
			if len(f.sender.notices) != 1 {
				t.Fatal("definite no-send not deferred to next window")
			}
		})
	}
	t.Run("late-save-cannot-dispatch", func(t *testing.T) {
		f := newAskNoticeFixture(t)
		f.add(1, "requester")
		f.n.store = &delayedAskNotificationStore{askNotificationStore: f.store, clock: f.clock, delay: noticeStoreTimeout + time.Second}
		f.n.tick(f.ctx)
		if len(f.sender.notices) != 0 || f.ledger(t).Pages.Records[0].State != pages.StateFailed {
			t.Fatal("late save sent a backdated reservation")
		}
		f.clock.Step(5*time.Minute + noticeStoreTimeout)
		f.n.tick(f.ctx)
		if len(f.sender.notices) != 1 {
			t.Fatal("late reservation did not use bounded pre-send retry")
		}
	})
}

func TestAskNotificationLedgerBoundsAndCAS(t *testing.T) {
	f := newAskNoticeFixture(t)
	l := f.ledger(t)
	for i := 0; i < askNotifyMaxIDs; i++ {
		id := fmt.Sprintf("ask-%016x", i)
		l.State.Seen[id] = id
		l.State.Groups[id] = askNotificationAttempt{Requester: strings.Repeat("a", 64), Times: []int64{f.clock.Now().Unix(), f.clock.Now().Add(5 * time.Minute).Unix()}}
	}
	if err := f.store.save(f.ctx, l); err != nil {
		t.Fatal("namespace-capacity ledger does not fit", err)
	}
	stale := f.ledger(t)
	newer := f.ledger(t)
	if err := f.store.save(f.ctx, newer); err != nil {
		t.Fatal(err)
	}
	// Ensure the concurrent write changes resourceVersion in the fake client.
	cm := &corev1.ConfigMap{}
	if err := f.store.client.Get(f.ctx, f.store.name, cm); err != nil {
		t.Fatal(err)
	}
	cm.Annotations = map[string]string{"writer": "newer"}
	if err := f.store.client.Update(f.ctx, cm); err != nil {
		t.Fatal(err)
	}
	if err := f.store.save(f.ctx, stale); err == nil {
		t.Fatal("stale ledger CAS accepted")
	}
	newer = f.ledger(t)
	live := map[string]bool{"ask-0000000000000000": true}
	newer.prune(live, f.clock.Now().Add(time.Hour))
	if len(newer.State.Seen) != askNotifyMaxIDs {
		t.Fatal("pruned recent rate state")
	}
	newer.prune(live, f.clock.Now().Add(65*time.Minute))
	if len(newer.State.Seen) != 1 || len(newer.State.Groups) != 1 {
		t.Fatal("did not bound absent IDs after rate window")
	}
}
