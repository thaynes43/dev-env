package keeper

import (
	"context"
	"crypto/ed25519"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/sessionraise"
)

type quietSink struct{ n int }

func (s *quietSink) Init(logr.RuntimeInfo) {}
func (s *quietSink) Enabled(int) bool      { return true }
func (s *quietSink) Info(int, string, ...any) {
	s.n++
}
func (s *quietSink) Error(error, string, ...any)    {}
func (s *quietSink) WithValues(...any) logr.LogSink { return s }
func (s *quietSink) WithName(string) logr.LogSink   { return s }

type countGet struct {
	client.Client
	gets int
}

func (c *countGet) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	c.gets++
	return c.Client.Get(ctx, key, obj, opts...)
}

type countList struct {
	client.Client
	lists int
}

func (c *countList) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	c.lists++
	return c.Client.List(ctx, list, opts...)
}

func TestRaisePagerDoesNotListWhenNothingNeedsANotice(t *testing.T) {
	base := fake.NewClientBuilder().WithScheme(raiseScheme(t)).Build()
	raiseObjects(t, base)
	counter := &countList{Client: base}
	when := time.Date(2026, 11, 13, 3, 15, 0, 0, time.UTC)
	p := &RaisePager{
		Client: base, Live: counter, Namespace: sessionraise.ObjectNamespace, SessionNamespace: "dev-agents",
		Sender: &fakeRaiseSender{}, Now: func() time.Time { return when }, Log: logr.Discard(),
	}
	p.announce(context.Background())
	if counter.lists != 0 {
		t.Fatalf("listed sessions when no row needs a notice: %d", counter.lists)
	}
}

func TestNoticeForAUIDNotInTheLedgerIsDropped(t *testing.T) {
	base := fake.NewClientBuilder().WithScheme(raiseScheme(t)).Build()
	raiseObjects(t, base)
	when := time.Date(2026, 11, 13, 3, 15, 0, 0, time.UTC)
	at := when.Format(time.RFC3339)
	writeLedger(t, base, sessionraise.SessionEntry{
		Name: "kept", UID: "uid-keep", ID: "keepnotice000001", State: sessionraise.StateActive, ChangedAt: when,
	})
	notes := &sessionraise.NoticeLedger{}
	notes.Put(sessionraise.Notice{ID: "keepnotice000001", SessionUID: "uid-keep", Kind: sessionraise.KindRaise, Attempt: 1, Outcome: sessionraise.OutcomeSent, At: at, SentAt: at})
	notes.Put(sessionraise.Notice{ID: "oldnotice0000001", SessionUID: "uid-old", Kind: sessionraise.KindRaise, Attempt: 2, Outcome: sessionraise.OutcomeFailed, At: at})
	raw, err := notes.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	store := sessionraise.Kube{Reader: base, Writer: base, Namespace: sessionraise.ObjectNamespace}
	if err := store.Update(context.Background(), sessionraise.NoticeLedgerName, map[string][]byte{sessionraise.LedgerField: raw}, mustRV(t, base, sessionraise.NoticeLedgerName)); err != nil {
		t.Fatal(err)
	}
	counter := &countList{Client: base}
	p := &RaisePager{
		Client: base, Live: counter, Namespace: sessionraise.ObjectNamespace, SessionNamespace: "dev-agents",
		Sender: &fakeRaiseSender{}, Now: func() time.Time { return when }, Log: logr.Discard(),
	}
	p.announce(context.Background())
	if counter.lists != 0 {
		t.Fatalf("listed sessions while dropping a ledger-less notice: %d", counter.lists)
	}
	obj, err := store.Get(context.Background(), sessionraise.NoticeLedgerName)
	if err != nil {
		t.Fatal(err)
	}
	got, err := sessionraise.UnmarshalNotice(obj.Data[sessionraise.LedgerField])
	if err != nil {
		t.Fatal(err)
	}
	if got.Find("oldnotice0000001", sessionraise.KindRaise) != nil {
		t.Fatal("a notice whose session is not in the operator ledger was kept")
	}
	kept := got.Find("keepnotice000001", sessionraise.KindRaise)
	if kept == nil || kept.Attempt != 1 || kept.Outcome != sessionraise.OutcomeSent {
		t.Fatalf("the notice for a session still in the ledger was changed: %+v", kept)
	}
}

func TestRaisePagerIsQuietWhenTheKeyIsMissing(t *testing.T) {
	base := fake.NewClientBuilder().WithScheme(raiseScheme(t)).Build()
	counter := &countGet{Client: base}
	when := time.Date(2026, 11, 13, 3, 15, 0, 0, time.UTC)
	now := when
	sink := &quietSink{}
	p := &RaisePager{
		Client: base, Live: counter, Namespace: sessionraise.ObjectNamespace, SessionNamespace: "dev-agents",
		Sender: &fakeRaiseSender{}, Now: func() time.Time { return now }, Log: logr.New(sink),
	}
	p.announce(context.Background())
	got := counter.gets
	p.announce(context.Background())
	if counter.gets != got || sink.n != 1 {
		t.Fatalf("retried inside 5 minutes: gets %d logs %d", counter.gets, sink.n)
	}
	now = when.Add(5 * time.Minute)
	p.announce(context.Background())
	if counter.gets == got || sink.n != 1 {
		t.Fatalf("after 5 minutes gets %d logs %d", counter.gets, sink.n)
	}
	now = now.Add(time.Hour)
	p.announce(context.Background())
	if sink.n != 2 {
		t.Fatalf("logs after an hour %d", sink.n)
	}
}

type failUpdate struct {
	client.Client
	name string
}

func (f failUpdate) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	if obj.GetName() == f.name {
		return errors.New("reservation failed")
	}
	return f.Client.Update(ctx, obj, opts...)
}

func plantRaise(t *testing.T, c client.Client, name, uid string, state string, profile string) (string, ed25519.PrivateKey) {
	t.Helper()
	ctx := context.Background()
	store := sessionraise.Kube{Reader: c, Writer: c, Namespace: sessionraise.ObjectNamespace, Secrets: map[string]bool{sessionraise.OperatorSecretName: true}}
	priv, err := sessionraise.Ensure(ctx, store, sessionraise.OperatorSecretName, sessionraise.OperatorPublicName, true)
	if err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 11, 13, 3, 15, 0, 0, time.UTC)
	at := when.Format(time.RFC3339)
	hash := sessionraise.WordsHash("yes please")
	id := sessionraise.RecordID(uid, at, hash)
	rec, err := sessionraise.Sign(sessionraise.Record{
		ID: id, SessionUID: uid, State: state, Profile: profile, StartSessions: profile == "", Depth: "2",
		Owner:       sessionraise.Owner{Words: "yes please", WordsHash: hash, Where: sessionraise.WherePhone, Session: "bench", SaidAt: at},
		PerformedBy: "session/bench", PerformedAt: at,
		TakenBackBy: "human/tom", TakenBackAt: at,
	}, priv)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := rec.Encode()
	if err != nil {
		t.Fatal(err)
	}
	sess := &v1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "dev-agents", UID: types.UID(uid), Annotations: map[string]string{v1alpha1.AnnotationRaise: string(raw)}},
		Spec:       v1alpha1.AgentSessionSpec{Repo: "haynes-ops", Agent: v1alpha1.AgentClaude, Mode: v1alpha1.ModeRemote, Model: "claude-opus-5-5", Prompt: "x"},
	}
	if err := c.Create(ctx, sess); err != nil {
		t.Fatal(err)
	}
	return id, priv
}

func writeLedger(t *testing.T, c client.Client, rows ...sessionraise.SessionEntry) {
	t.Helper()
	body, err := (&sessionraise.Ledger{Sessions: rows}).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	store := sessionraise.Kube{Reader: c, Writer: c, Namespace: sessionraise.ObjectNamespace}
	if err := store.Update(context.Background(), sessionraise.LedgerName, map[string][]byte{sessionraise.LedgerField: body}, mustRV(t, c, sessionraise.LedgerName)); err != nil {
		t.Fatal(err)
	}
}

func TestBadSignatureAndFailedReservationSendNothing(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(raiseScheme(t)).Build()
	raiseObjects(t, c)
	ctx := context.Background()
	id, _ := plantRaise(t, c, "raised", "uid-bad", sessionraise.StatePending, "")
	sess := &v1alpha1.AgentSession{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: "dev-agents", Name: "raised"}, sess); err != nil {
		t.Fatal(err)
	}
	raw := sess.Annotations[v1alpha1.AnnotationRaise]
	sess.Annotations[v1alpha1.AnnotationRaise] = raw[:len(raw)-4] + "AAAA"
	if err := c.Update(ctx, sess); err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 11, 13, 3, 15, 0, 0, time.UTC)
	writeLedger(t, c, sessionraise.SessionEntry{
		Name: "raised", UID: "uid-bad", ID: id, State: sessionraise.StatePending, ChangedAt: when,
		StartSessions: true, Depth: "2", WordsHash: sessionraise.WordsHash("yes please"), PerformedBy: "session/bench",
	})
	sender := &fakeRaiseSender{}
	p := &RaisePager{Client: c, Live: c, Namespace: sessionraise.ObjectNamespace, SessionNamespace: "dev-agents", Sender: sender, Now: func() time.Time { return when }}
	p.announce(ctx)
	if sender.n != 0 {
		t.Fatalf("bad signature sent %d", sender.n)
	}
	sess.Annotations[v1alpha1.AnnotationRaise] = raw
	if err := c.Update(ctx, sess); err != nil {
		t.Fatal(err)
	}
	p.Client = failUpdate{Client: c, name: sessionraise.NoticeLedgerName}
	p.announce(ctx)
	if sender.n != 0 {
		t.Fatalf("failed reservation sent %d", sender.n)
	}
}

func TestTakeBackNoticeDoesNotNeedTheAnnotation(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(raiseScheme(t)).Build()
	raiseObjects(t, c)
	id, _ := plantRaise(t, c, "raised", "uid-back", sessionraise.StatePending, "")
	when := time.Date(2026, 11, 13, 3, 15, 0, 0, time.UTC)
	at := when.Format(time.RFC3339)
	writeLedger(t, c, sessionraise.SessionEntry{
		Name: "raised", UID: "uid-back", ID: id, State: sessionraise.StateTakenBack, ChangedAt: when,
		TakenBackBy: "human/tom", TakenBackAt: at,
	})
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: sessionraise.ObjectNamespace, Name: sessionraise.OperatorPublicName}}
	if err := c.Delete(context.Background(), cm); err != nil {
		t.Fatal(err)
	}
	sender := &fakeRaiseSender{}
	p := &RaisePager{Client: c, Live: c, Namespace: sessionraise.ObjectNamespace, SessionNamespace: "dev-agents", Sender: sender, Now: func() time.Time { return when }, Log: logr.Discard()}
	p.announce(context.Background())
	p.announce(context.Background())
	if sender.n != 1 || !strings.Contains(sender.last.Title, "taken back") {
		t.Fatalf("take-back notices %d %q", sender.n, sender.last.Title)
	}
}

func TestProfileRaiseDoesNotBlockATakeBack(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(raiseScheme(t)).Build()
	raiseObjects(t, c)
	id, priv := plantRaise(t, c, "raised", "uid-prof", sessionraise.StatePending, "workbench")
	when := time.Date(2026, 11, 13, 3, 15, 0, 0, time.UTC)
	at := when.Format(time.RFC3339)
	backID := sessionraise.RecordID("uid-back", at, sessionraise.WordsHash("yes please"))
	rec, err := sessionraise.Sign(sessionraise.Record{
		ID: backID, SessionUID: "uid-back", State: sessionraise.StateTakenBack, Depth: "2",
		Owner:       sessionraise.Owner{Words: "yes please", WordsHash: sessionraise.WordsHash("yes please"), Where: sessionraise.WherePhone, SaidAt: at},
		PerformedBy: "session/bench", PerformedAt: at, TakenBackBy: "human/tom", TakenBackAt: at,
	}, priv)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := rec.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Create(context.Background(), &v1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "dev-agents", UID: "uid-back", Annotations: map[string]string{v1alpha1.AnnotationRaise: string(raw)}},
		Spec:       v1alpha1.AgentSessionSpec{Repo: "haynes-ops", Agent: v1alpha1.AgentClaude, Mode: v1alpha1.ModeRemote, Model: "claude-opus-5-5", Prompt: "x"},
	}); err != nil {
		t.Fatal(err)
	}
	writeLedger(t, c,
		sessionraise.SessionEntry{Name: "raised", UID: "uid-prof", ID: id, State: sessionraise.StatePending, Profile: "workbench", ChangedAt: when, WordsHash: sessionraise.WordsHash("yes please")},
		sessionraise.SessionEntry{Name: "other", UID: "uid-back", ID: backID, State: sessionraise.StateTakenBack, ChangedAt: when, TakenBackBy: "human/tom", TakenBackAt: at},
	)
	sink := &quietSink{}
	sender := &fakeRaiseSender{}
	p := &RaisePager{Client: c, Live: c, Namespace: sessionraise.ObjectNamespace, SessionNamespace: "dev-agents", Sender: sender, Now: func() time.Time { return when }, Log: logr.New(sink)}
	p.announce(context.Background())
	p.announce(context.Background())
	if sender.n != 1 || !strings.Contains(sender.last.Title, "taken back") || sink.n == 0 {
		t.Fatalf("sends %d title %q logs %d", sender.n, sender.last.Title, sink.n)
	}
}

func TestThreeNoticesCarryTheirOwnSentAt(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(raiseScheme(t)).Build()
	raiseObjects(t, c)
	when := time.Date(2026, 11, 13, 3, 15, 0, 0, time.UTC)
	now := when
	var rows []sessionraise.SessionEntry
	var ids []string
	for i, name := range []string{"one", "two", "three"} {
		uid := "uid-" + name
		id, _ := plantRaise(t, c, name, uid, sessionraise.StatePending, "")
		ids = append(ids, id)
		at := when.Format(time.RFC3339)
		rows = append(rows, sessionraise.SessionEntry{
			Name: name, UID: uid, ID: id, State: sessionraise.StatePending, ChangedAt: when,
			StartSessions: true, Depth: "2", WordsHash: sessionraise.WordsHash("yes please"),
			PerformedBy: "session/bench", PerformedAt: at,
		})
		_ = i
	}
	writeLedger(t, c, rows...)
	sender := &slowSender{now: &now}
	p := &RaisePager{Client: c, Live: c, Namespace: sessionraise.ObjectNamespace, SessionNamespace: "dev-agents", Sender: sender, Now: func() time.Time { return now }}
	p.announce(context.Background())
	if len(sender.returned) != 3 {
		t.Fatalf("sends %d", len(sender.returned))
	}
	obj, err := p.store().Get(context.Background(), sessionraise.NoticeLedgerName)
	if err != nil {
		t.Fatal(err)
	}
	notices, err := sessionraise.UnmarshalNotice(obj.Data[sessionraise.LedgerField])
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for i, id := range ids {
		got := notices.Find(id, sessionraise.KindRaise)
		if got == nil || got.SentAt == "" {
			t.Fatalf("notice %s: %+v", id, got)
		}
		sent, err := time.Parse(time.RFC3339, got.SentAt)
		if err != nil {
			t.Fatal(err)
		}
		if sent.Before(sender.returned[i]) {
			t.Fatalf("sentAt %s is earlier than the push returned %s", got.SentAt, sender.returned[i].Format(time.RFC3339))
		}
		seen[got.SentAt] = true
	}
	if len(seen) != 3 {
		t.Fatalf("sentAt values were shared: %v", seen)
	}
}

type slowSender struct {
	now      *time.Time
	returned []time.Time
}

func (s *slowSender) Send(ctx context.Context, n Notice) error {
	_, err := s.SendReceipt(ctx, n)
	return err
}

func (s *slowSender) SendReceipt(context.Context, Notice) (Receipt, error) {
	*s.now = s.now.Add(3 * time.Second)
	s.returned = append(s.returned, s.now.UTC().Truncate(time.Second))
	return Receipt{Status: 1, Confirmed: true}, nil
}

func TestUncountedAnnotationDoesNotResetAttempts(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(raiseScheme(t)).Build()
	raiseObjects(t, c)
	ctx := context.Background()
	store := sessionraise.Kube{Reader: c, Writer: c, Namespace: sessionraise.ObjectNamespace, Secrets: map[string]bool{sessionraise.OperatorSecretName: true}}
	op, err := sessionraise.Ensure(ctx, store, sessionraise.OperatorSecretName, sessionraise.OperatorPublicName, true)
	if err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 11, 13, 3, 15, 0, 0, time.UTC)
	at := when.Format(time.RFC3339)
	const uid = "uid-keep"
	hash := sessionraise.WordsHash("yes please")
	id := sessionraise.RecordID(uid, at, hash)
	other, err := sessionraise.Sign(sessionraise.Record{
		ID: "differentid0001", SessionUID: uid, State: sessionraise.StatePending, Depth: "2",
		Owner:       sessionraise.Owner{Words: "yes please", WordsHash: hash, Where: sessionraise.WherePhone, SaidAt: at},
		PerformedBy: "session/bench", PerformedAt: at,
	}, op)
	if err != nil {
		t.Fatal(err)
	}
	other.Sig = ""
	raw, err := other.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Create(ctx, &v1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{Name: "raised", Namespace: "dev-agents", UID: uid, Annotations: map[string]string{v1alpha1.AnnotationRaise: string(raw)}},
		Spec:       v1alpha1.AgentSessionSpec{Repo: "haynes-ops", Agent: v1alpha1.AgentClaude, Mode: v1alpha1.ModeRemote, Model: "claude-opus-5-5", Prompt: "x"},
	}); err != nil {
		t.Fatal(err)
	}
	writeLedger(t, c, sessionraise.SessionEntry{
		Name: "raised", UID: uid, ID: id, State: sessionraise.StatePending, ChangedAt: when,
		StartSessions: true, WordsHash: hash, PerformedBy: "session/bench", PerformedAt: at,
	})
	old := when.Add(-time.Hour).Format(time.RFC3339)
	led := &sessionraise.NoticeLedger{}
	led.Put(sessionraise.Notice{ID: id, SessionUID: uid, Kind: sessionraise.KindRaise, Attempt: 5, Outcome: sessionraise.OutcomeFailed, At: old})
	body, err := led.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, sessionraise.NoticeLedgerName, map[string][]byte{sessionraise.LedgerField: body}, mustRV(t, c, sessionraise.NoticeLedgerName)); err != nil {
		t.Fatal(err)
	}
	p := &RaisePager{Client: c, Live: c, Namespace: sessionraise.ObjectNamespace, SessionNamespace: "dev-agents", Sender: &fakeRaiseSender{}, Now: func() time.Time { return when }}
	p.announce(ctx)
	obj, err := p.store().Get(ctx, sessionraise.NoticeLedgerName)
	if err != nil {
		t.Fatal(err)
	}
	got, err := sessionraise.UnmarshalNotice(obj.Data[sessionraise.LedgerField])
	if err != nil {
		t.Fatal(err)
	}
	row := got.Find(id, sessionraise.KindRaise)
	if row == nil || row.Attempt != 5 {
		t.Fatalf("attempt was reset: %+v", row)
	}
}
