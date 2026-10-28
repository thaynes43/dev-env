package keeper

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/thaynes43/dev-env/internal/pages"
)

const acceptedBody = `{"status":1,"request":"5042853c-402d-4a18-abcc-e5aaf5a3ceba"}`

// memPages is a NoticeStore that also keeps the pages record, in memory, as
// the ConfigMap does: its own key, written whole.
type memPages struct {
	*memStore
	pmu      sync.Mutex
	raw      []byte
	failLoad error
	failSave bool
	loads    int
	saves    int
}

func (m *memPages) LoadPages(context.Context) (*pages.Document, error) {
	m.pmu.Lock()
	defer m.pmu.Unlock()
	m.loads++
	if m.failLoad != nil {
		return nil, m.failLoad
	}
	if m.raw == nil {
		return pages.New(), nil
	}
	return pages.Parse(m.raw)
}

func (m *memPages) SavePages(_ context.Context, d *pages.Document) error {
	m.pmu.Lock()
	defer m.pmu.Unlock()
	if m.failSave {
		return errNoticeState
	}
	raw, err := d.Marshal()
	if err != nil {
		return err
	}
	m.raw = raw
	m.saves++
	return nil
}

// stored is the record as a reader of the ConfigMap would see it.
func (m *memPages) stored(t *testing.T) *pages.Document {
	t.Helper()
	m.pmu.Lock()
	defer m.pmu.Unlock()
	if m.raw == nil {
		return pages.New()
	}
	d, err := pages.Parse(m.raw)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func newPagesFix(t *testing.T, start time.Time) (*noticeFix, *memPages) {
	t.Helper()
	f := newNoticeFix(t, start)
	mp := &memPages{memStore: f.store}
	f.alt = mp
	f.n = f.newNotifier()
	f.fake.okBody = acceptedBody
	return f, mp
}

func only(t *testing.T, d *pages.Document) pages.Record {
	t.Helper()
	if len(d.Records) != 1 {
		t.Fatalf("%d records: %+v", len(d.Records), d.Records)
	}
	return d.Records[0]
}

var pagesStart = time.Date(2026, 11, 12, 16, 0, 0, 0, time.UTC)

func TestPageIsRecordedBeforeTheSendAndSentAfter(t *testing.T) {
	f, mp := newPagesFix(t, pagesStart)
	// A sender that looks at the stored record at the moment of the request:
	// that is what a crash during the send would leave behind.
	var atSend []pages.Record
	real := f.n.Sender.(*PushoverSender)
	f.n.Sender = &spySender{inner: real, before: func() { atSend = append(atSend, mp.stored(t).Records...) }}
	f.hub.Report(ProviderClaude, LoginNeedsLogin, time.Time{})
	f.tick()
	if len(atSend) != 1 || atSend[0].State != pages.StateRecorded || atSend[0].Attempts != 1 {
		t.Fatalf("at the moment of the send the stored record was %+v", atSend)
	}
	r := only(t, mp.stored(t))
	if r.State != pages.StateSent || r.RequestID != "5042853c-402d-4a18-abcc-e5aaf5a3ceba" || r.HTTPStatus != 200 {
		t.Fatalf("after: %+v", r)
	}
	if r.Sender != "dev-env-keeper" || r.Source != "notices/critical" || r.Priority != 1 || r.URL != NoticeLinkURL ||
		!strings.HasPrefix(r.Key, "critical:claude:") || !strings.HasPrefix(r.Correlation, "claude-") || !strings.HasPrefix(r.Provenance, "dev-env-keeper ") {
		t.Fatalf("fields: %+v", r)
	}
	if !strings.Contains(r.Title, "Keeper needs login (claude)") || !r.RecordedAt.Equal(pagesStart) {
		t.Fatalf("title/time: %+v", r)
	}
}

type spySender struct {
	inner  *PushoverSender
	before func()
}

func (s *spySender) Send(ctx context.Context, n Notice) error {
	_, err := s.SendReceipt(ctx, n)
	return err
}
func (s *spySender) SendReceipt(ctx context.Context, n Notice) (Receipt, error) {
	s.before()
	return s.inner.SendReceipt(ctx, n)
}

func TestPageOutcomesAreHonest(t *testing.T) {
	for _, c := range []struct {
		name   string
		status int
		body   string
		want   pages.State
		http   int
	}{
		{"accepted", 200, acceptedBody, pages.StateSent, 200},
		{"2xx that does not confirm", 200, "<html>proxy</html>", pages.StateAmbiguous, 200},
		{"200 with status 0", 200, `{"status":0,"request":"x"}`, pages.StateAmbiguous, 200},
		{"4xx", 400, "", pages.StateFailed, 400},
		{"429", 429, "", pages.StateFailed, 429},
		{"5xx", 503, "", pages.StateAmbiguous, 503},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, mp := newPagesFix(t, pagesStart)
			f.fake.okBody = c.body
			f.fake.setStatus(c.status)
			f.hub.Report(ProviderClaude, LoginNeedsLogin, time.Time{})
			f.tick()
			r := only(t, mp.stored(t))
			if r.State != c.want || r.HTTPStatus != c.http {
				t.Fatalf("state %s status %d, want %s %d", r.State, r.HTTPStatus, c.want, c.http)
			}
			if c.want != pages.StateSent && r.RequestID != "" {
				t.Fatalf("request id %q on a page that was not confirmed", r.RequestID)
			}
		})
	}
}

func TestTransportErrorIsAmbiguous(t *testing.T) {
	f, mp := newPagesFix(t, pagesStart)
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := dead.URL
	dead.Close()
	f.n.Sender = &PushoverSender{URL: url, TokenFile: filepath.Join(f.dir, "token"), UserFile: filepath.Join(f.dir, "user")}
	f.hub.Report(ProviderClaude, LoginNeedsLogin, time.Time{})
	f.tick()
	if r := only(t, mp.stored(t)); r.State != pages.StateAmbiguous || r.HTTPStatus != 0 {
		t.Fatalf("%+v", r)
	}
}

func TestMissingCredentialsAreFailedNotAmbiguous(t *testing.T) {
	f, mp := newPagesFix(t, pagesStart)
	f.n.Sender = &PushoverSender{URL: f.fake.srv.URL, TokenFile: filepath.Join(f.dir, "nope"), UserFile: filepath.Join(f.dir, "user")}
	f.hub.Report(ProviderClaude, LoginNeedsLogin, time.Time{})
	f.tick()
	if r := only(t, mp.stored(t)); r.State != pages.StateFailed || r.HTTPStatus != 0 {
		t.Fatalf("%+v", r)
	}
	if f.fake.count() != 0 {
		t.Fatal("a request was made without credentials")
	}
}

// A retry after the backoff is the same record with another attempt, not a second record.
func TestRetryDoesNotDuplicateTheRecord(t *testing.T) {
	f, mp := newPagesFix(t, pagesStart)
	f.fake.setStatus(500)
	f.hub.Report(ProviderClaude, LoginNeedsLogin, time.Time{})
	f.tick()
	if r := only(t, mp.stored(t)); r.State != pages.StateAmbiguous || r.Attempts != 1 {
		t.Fatalf("%+v", r)
	}
	f.fake.setStatus(200)
	f.clk.Step(6 * time.Minute)
	f.tick()
	r := only(t, mp.stored(t))
	if r.State != pages.StateSent || r.Attempts != 2 || r.SentCount != 1 || r.AmbiguousCount != 1 {
		t.Fatalf("%+v", r)
	}
	// And a restart repeats nothing: not a send, not a record.
	f.n = f.newNotifier()
	f.clk.Step(10 * time.Minute)
	f.tick()
	if f.fake.count() != 2 || len(mp.stored(t).Records) != 1 {
		t.Fatalf("sends %d records %d", f.fake.count(), len(mp.stored(t).Records))
	}
}

// If the outcome cannot be stored, the record stays `recorded` (what a crash
// leaves), the send is not repeated, and the next pass stores the outcome.
func TestOutcomeNotStoredLeavesRecordedThenCatchesUp(t *testing.T) {
	f, mp := newPagesFix(t, pagesStart)
	real := f.n.Sender.(*PushoverSender)
	f.n.Sender = &spySender{inner: real, before: func() { mp.pmu.Lock(); mp.failSave = true; mp.pmu.Unlock() }}
	f.hub.Report(ProviderClaude, LoginNeedsLogin, time.Time{})
	f.tick() // the write-ahead was stored; the outcome save then fails
	if r := only(t, mp.stored(t)); r.State != pages.StateRecorded {
		t.Fatalf("a failed save changed the stored record: %+v", r)
	}
	mp.pmu.Lock()
	mp.failSave = false
	mp.pmu.Unlock()
	f.clk.Step(time.Minute)
	f.tick()
	if r := only(t, mp.stored(t)); r.State != pages.StateSent {
		t.Fatalf("not caught up: %+v", r)
	}
	if f.fake.count() != 1 {
		t.Fatalf("%d sends", f.fake.count())
	}
}

// A page record that cannot be read or written never stops, delays or repeats a page.
func TestUnusablePageRecordDoesNotChangeSending(t *testing.T) {
	run := func(mut func(*memPages)) []string {
		f, mp := newPagesFix(t, pagesStart)
		mut(mp)
		f.hub.Report(ProviderClaude, LoginNeedsLogin, time.Time{})
		f.tick()
		f.clk.Step(10 * time.Minute)
		f.tick()
		f.hub.Report(ProviderClaude, LoginReady, time.Time{})
		f.tick()
		return f.fake.titles()
	}
	base := run(func(*memPages) {})
	for name, mut := range map[string]func(*memPages){
		"load fails": func(m *memPages) { m.failLoad = errNoticeStateForbidden },
		"save fails": func(m *memPages) { m.failSave = true },
		"corrupt":    func(m *memPages) { m.raw = []byte(`{"version":9}`) },
	} {
		got := run(mut)
		if strings.Join(got, ",") != strings.Join(base, ",") || len(base) != 2 {
			t.Errorf("%s: sends %v, want %v", name, got, base)
		}
	}
}

// Today's behaviour is the baseline: with or without the record, the same
// requests go out with the same fields.
func TestSendsAreIdenticalWithAndWithoutTheRecord(t *testing.T) {
	var forms [2][]string
	for i, withPages := range []bool{false, true} {
		var f *noticeFix
		if withPages {
			f, _ = newPagesFix(t, pagesStart)
		} else {
			f = newNoticeFix(t, pagesStart)
		}
		f.fake.okBody = acceptedBody
		f.hub.Report(ProviderClaude, LoginNeedsLogin, time.Time{})
		f.tick()
		f.hub.Report(ProviderClaude, LoginReady, time.Time{})
		f.tick()
		f.hub.Report(ProviderClaude, LoginReady, pagesStart.Add(5*24*time.Hour))
		f.tick()
		f.fake.mu.Lock()
		for _, r := range f.fake.reqs {
			f2 := r.form
			f2.Del("token")
			f2.Del("user")
			forms[i] = append(forms[i], f2.Encode())
		}
		f.fake.mu.Unlock()
	}
	if len(forms[0]) < 3 || strings.Join(forms[0], "\n") != strings.Join(forms[1], "\n") {
		t.Fatalf("requests differ:\n%v\n%v", forms[0], forms[1])
	}
}

func TestRecordKeysCoverTheThreeNotices(t *testing.T) {
	f, mp := newPagesFix(t, pagesStart)
	f.hub.Report(ProviderClaude, LoginNeedsLogin, time.Time{})
	f.tick()
	f.hub.Report(ProviderClaude, LoginReady, pagesStart.Add(5*24*time.Hour))
	f.tick()
	d := mp.stored(t)
	if len(d.Records) != 3 {
		t.Fatalf("%+v", d.Records)
	}
	var kinds []string
	for _, r := range d.Records {
		kinds = append(kinds, strings.SplitN(r.Key, ":", 2)[0])
		if r.State != pages.StateSent {
			t.Errorf("%s %s", r.Key, r.State)
		}
	}
	if strings.Join(kinds, ",") != "critical,resolved,warning" {
		t.Fatalf("kinds %v", kinds)
	}
	w := d.Find("warning:claude:2026-11-12:lapse-2026-11-17")
	if w == nil || w.Correlation != "claude-lapse-2026-11-17" {
		t.Fatalf("warning record: %+v", d.Records)
	}
}

func TestPageRecordHoldsNoCredential(t *testing.T) {
	f, mp := newPagesFix(t, pagesStart)
	f.hub.Report(ProviderClaude, LoginNeedsLogin, time.Time{})
	f.tick()
	f.fake.setStatus(400)
	f.fake.echo = true // a careless API repeats the request, credentials and all
	f.hub.Report(ProviderClaude, LoginReady, pagesStart.Add(5*24*time.Hour))
	f.tick()
	mp.pmu.Lock()
	raw := string(mp.raw)
	mp.pmu.Unlock()
	for _, canary := range []string{noticeTokenCanary, noticeUserCanary} {
		if strings.Contains(raw, canary) {
			t.Fatalf("the stored record holds a credential")
		}
	}
	if strings.Contains(f.logs.String(), noticeTokenCanary) || strings.Contains(f.logs.String(), noticeUserCanary) {
		t.Fatal("a log line holds a credential")
	}
}

func TestSendReceiptReadsOnlyTheConfirmedAnswer(t *testing.T) {
	f := newNoticeFix(t, pagesStart)
	f.fake.okBody = acceptedBody
	s := &PushoverSender{URL: f.fake.srv.URL, TokenFile: filepath.Join(f.dir, "token"), UserFile: filepath.Join(f.dir, "user")}
	rc, err := s.SendReceipt(context.Background(), Notice{Title: "t", Message: "m"})
	if err != nil || !rc.Confirmed || rc.Request != "5042853c-402d-4a18-abcc-e5aaf5a3ceba" || rc.Status != 200 {
		t.Fatalf("%+v %v", rc, err)
	}
	f.fake.okBody = strings.Repeat("x", 1<<20)
	rc, err = s.SendReceipt(context.Background(), Notice{Title: "t", Message: "m"})
	if err != nil || rc.Confirmed || rc.Request != "" {
		t.Fatalf("%+v %v", rc, err)
	}
	f.fake.setStatus(400)
	if _, err := s.SendReceipt(context.Background(), Notice{Title: "t", Message: "m"}); err == nil {
		t.Fatal("a 400 was accepted")
	} else {
		var se *SendError
		if !errors.As(err, &se) || se.Status != 400 {
			t.Fatalf("%v", err)
		}
	}
}

func TestConfigMapPagesRoundTripLeavesTheNoticeStateAlone(t *testing.T) {
	name := types.NamespacedName{Namespace: "system", Name: DefaultNoticeStateConfigMap}
	c := claudeClient(t, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: name.Namespace, Name: name.Name}})
	s := &ConfigMapNoticeStore{Client: c, Name: name}
	ctx := context.Background()
	d, err := s.LoadPages(ctx)
	if err != nil || len(d.Records) != 0 {
		t.Fatalf("absent key: %+v %v", d, err)
	}
	st := NoticeState{Version: 1, Warnings: map[string]warnRecord{"claude": {Day: "2026-11-09", Lapse: noticeLapse.Unix()}}}
	if err := s.Save(ctx, st); err != nil {
		t.Fatal(err)
	}
	_, _ = d.Begin(pages.Page{Key: "critical:claude:1", Sender: "dev-env-keeper", Source: "notices/critical", Title: "t", Body: "b"}, pagesStart)
	if err := s.SavePages(ctx, d); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadPages(ctx)
	if err != nil || len(got.Records) != 1 || got.Records[0].Key != "critical:claude:1" {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	if ns, err := s.Load(ctx); err != nil || ns.Warnings["claude"] != st.Warnings["claude"] {
		t.Fatalf("saving pages changed the notice state: %+v %v", ns, err)
	}
	// And the other way: saving the state leaves the pages.
	if err := s.Save(ctx, st); err != nil {
		t.Fatal(err)
	}
	if got, err := s.LoadPages(ctx); err != nil || len(got.Records) != 1 {
		t.Fatalf("saving the state changed the pages: %+v %v", got, err)
	}
	// A damaged key is an error, never an empty history.
	var cm corev1.ConfigMap
	_ = c.Get(ctx, name, &cm)
	cm.Data[pages.ConfigMapKey] = "{broken"
	_ = c.Update(ctx, &cm)
	if _, err := s.LoadPages(ctx); !errors.Is(err, errPagesCorrupt) {
		t.Fatalf("damaged key: %v", err)
	}
	missing := &ConfigMapNoticeStore{Client: claudeClient(t), Name: name}
	if _, err := missing.LoadPages(ctx); err == nil {
		t.Fatal("loaded pages from a ConfigMap that does not exist")
	}
	if err := missing.SavePages(ctx, d); err == nil {
		t.Fatal("created a ConfigMap")
	}
}
