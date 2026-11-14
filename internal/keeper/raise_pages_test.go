package keeper

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/sessionraise"
)

type fakeRaiseSender struct {
	n    int
	last Notice
}

func (f *fakeRaiseSender) Send(context.Context, Notice) error {
	return nil
}

func (f *fakeRaiseSender) SendReceipt(_ context.Context, n Notice) (Receipt, error) {
	f.n++
	f.last = n
	return Receipt{Status: 1, Confirmed: true}, nil
}

func raiseScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func raiseObjects(t *testing.T, c client.Client) {
	t.Helper()
	ctx := context.Background()
	ns := sessionraise.ObjectNamespace
	for _, name := range []string{sessionraise.OperatorSecretName, sessionraise.KeeperSecretName} {
		if err := c.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{sessionraise.OperatorPublicName, sessionraise.LedgerName, sessionraise.KeeperPublicName, sessionraise.NoticeLedgerName} {
		if err := c.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRaiseNoticeSendsAtAnyHour(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(raiseScheme(t)).Build()
	raiseObjects(t, c)
	ctx := context.Background()
	store := sessionraise.Kube{Reader: c, Writer: c, Namespace: sessionraise.ObjectNamespace, Secrets: map[string]bool{sessionraise.OperatorSecretName: true}}
	priv, err := sessionraise.Ensure(ctx, store, sessionraise.OperatorSecretName, sessionraise.OperatorPublicName, true)
	if err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 11, 13, 3, 15, 0, 0, time.UTC)
	at := when.Format(time.RFC3339)
	const uid = "uid-raise-night"
	hash := sessionraise.WordsHash("yes please")
	id := sessionraise.RecordID(uid, at, hash)
	rec, err := sessionraise.Sign(sessionraise.Record{
		ID: id, SessionUID: uid, State: sessionraise.StatePending, StartSessions: true, Depth: "2",
		Owner:       sessionraise.Owner{Words: "yes please", WordsHash: hash, Where: sessionraise.WherePhone, Session: "bench", SaidAt: at},
		PerformedBy: "session/bench", PerformedAt: at,
	}, priv)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := rec.Encode()
	if err != nil {
		t.Fatal(err)
	}
	sess := &v1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{Name: "raised", Namespace: "dev-agents", UID: uid, Annotations: map[string]string{
			v1alpha1.AnnotationRaise: string(raw),
			v1alpha1.AnnotationTitle: "night title",
		}},
		Spec: v1alpha1.AgentSessionSpec{Repo: "haynes-ops", Agent: v1alpha1.AgentClaude, Mode: v1alpha1.ModeRemote, Model: "claude-opus-5-5", Prompt: "x"},
	}
	if err := c.Create(ctx, sess); err != nil {
		t.Fatal(err)
	}
	led := &sessionraise.Ledger{Sessions: []sessionraise.SessionEntry{{
		Name: "raised", UID: uid, ID: id, State: sessionraise.StatePending, ChangedAt: when,
		StartSessions: true, Depth: "2", WordsHash: hash, PerformedBy: "session/bench", PerformedAt: at,
	}}}
	body, err := led.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, sessionraise.LedgerName, map[string][]byte{sessionraise.LedgerField: body}, mustRV(t, c, sessionraise.LedgerName)); err != nil {
		t.Fatal(err)
	}
	sender := &fakeRaiseSender{}
	p := &RaisePager{Client: c, Live: c, Namespace: sessionraise.ObjectNamespace, SessionNamespace: "dev-agents", Sender: sender, Now: func() time.Time { return when }}
	p.announce(ctx)
	if sender.n != 1 {
		t.Fatalf("sends: %d", sender.n)
	}
	if sender.last.Title == "" || sender.last.Message == "" {
		t.Fatal("empty notice")
	}
	if !strings.Contains(sender.last.Message, "two minutes") || !strings.Contains(sender.last.Message, "agent-run raise raised --take-back") {
		t.Fatalf("message: %s", sender.last.Message)
	}
	obj, err := p.store().Get(ctx, sessionraise.NoticeLedgerName)
	if err != nil {
		t.Fatal(err)
	}
	notices, err := sessionraise.UnmarshalNotice(obj.Data[sessionraise.LedgerField])
	if err != nil {
		t.Fatal(err)
	}
	got := notices.Find(id, sessionraise.KindRaise)
	if got == nil || got.Outcome != sessionraise.OutcomeSent || got.SentAt != at {
		t.Fatalf("notice: %+v", got)
	}
	kpub, err := sessionraise.ReadPublic(ctx, p.store(), sessionraise.KeeperPublicName)
	if err != nil {
		t.Fatal(err)
	}
	if !sessionraise.VerifyNotice(*got, kpub) {
		t.Fatal("notice signature does not verify")
	}
}

func TestProfileRaiseIsNotSent(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(raiseScheme(t)).Build()
	raiseObjects(t, c)
	ctx := context.Background()
	store := sessionraise.Kube{Reader: c, Writer: c, Namespace: sessionraise.ObjectNamespace, Secrets: map[string]bool{sessionraise.OperatorSecretName: true}}
	priv, err := sessionraise.Ensure(ctx, store, sessionraise.OperatorSecretName, sessionraise.OperatorPublicName, true)
	if err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 11, 13, 3, 15, 0, 0, time.UTC)
	at := when.Format(time.RFC3339)
	const uid = "uid-raise-profile"
	hash := sessionraise.WordsHash("yes please")
	id := sessionraise.RecordID(uid, at, hash)
	rec, err := sessionraise.Sign(sessionraise.Record{
		ID: id, SessionUID: uid, State: sessionraise.StatePending, Profile: "workbench", Depth: "2",
		Owner:       sessionraise.Owner{Words: "yes please", WordsHash: hash, Where: sessionraise.WherePhone, Session: "bench", SaidAt: at},
		PerformedBy: "session/bench", PerformedAt: at,
	}, priv)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := rec.Encode()
	if err != nil {
		t.Fatal(err)
	}
	sess := &v1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{Name: "raised", Namespace: "dev-agents", UID: uid, Annotations: map[string]string{v1alpha1.AnnotationRaise: string(raw)}},
		Spec:       v1alpha1.AgentSessionSpec{Repo: "haynes-ops", Agent: v1alpha1.AgentClaude, Mode: v1alpha1.ModeRemote, Model: "claude-opus-5-5", Prompt: "x"},
	}
	if err := c.Create(ctx, sess); err != nil {
		t.Fatal(err)
	}
	led := &sessionraise.Ledger{Sessions: []sessionraise.SessionEntry{{
		Name: "raised", UID: uid, ID: id, State: sessionraise.StatePending, ChangedAt: when,
		Profile: "workbench", Depth: "2", WordsHash: hash,
	}}}
	body, err := led.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, sessionraise.LedgerName, map[string][]byte{sessionraise.LedgerField: body}, mustRV(t, c, sessionraise.LedgerName)); err != nil {
		t.Fatal(err)
	}
	sender := &fakeRaiseSender{}
	p := &RaisePager{Client: c, Live: c, Namespace: sessionraise.ObjectNamespace, SessionNamespace: "dev-agents", Sender: sender, Now: func() time.Time { return when }}
	p.announce(ctx)
	if sender.n != 0 {
		t.Fatalf("sent a profile raise: %d", sender.n)
	}
}

func TestRaiseNoticeDropsAGoneSession(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(raiseScheme(t)).Build()
	raiseObjects(t, c)
	ctx := context.Background()
	store := sessionraise.Kube{Reader: c, Writer: c, Namespace: sessionraise.ObjectNamespace, Secrets: map[string]bool{sessionraise.KeeperSecretName: true}}
	when := time.Date(2026, 11, 13, 3, 15, 0, 0, time.UTC)
	old := when.Add(-sessionraise.WordRetention - time.Hour).Format(time.RFC3339)
	led := &sessionraise.NoticeLedger{}
	led.Put(sessionraise.Notice{ID: "gonenotice000001", SessionUID: "gone", Kind: sessionraise.KindRaise, Attempt: 1, Outcome: sessionraise.OutcomeFailed, At: old})
	led.Put(sessionraise.Notice{ID: "orphannotice0001", SessionUID: "orphan", Kind: sessionraise.KindRaise, Attempt: 4, Outcome: sessionraise.OutcomeFailed, At: old})
	raw, err := led.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, sessionraise.NoticeLedgerName, map[string][]byte{sessionraise.LedgerField: raw}, mustRV(t, c, sessionraise.NoticeLedgerName)); err != nil {
		t.Fatal(err)
	}
	op := &sessionraise.Ledger{Sessions: []sessionraise.SessionEntry{{
		Name: "missing", UID: "gone", ID: "gonenotice000001", State: sessionraise.StatePending, ChangedAt: when,
	}}}
	body, err := op.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	opStore := sessionraise.Kube{Reader: c, Writer: c, Namespace: sessionraise.ObjectNamespace}
	if err := opStore.Update(ctx, sessionraise.LedgerName, map[string][]byte{sessionraise.LedgerField: body}, mustRV(t, c, sessionraise.LedgerName)); err != nil {
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
	if got.Find("gonenotice000001", sessionraise.KindRaise) != nil {
		t.Fatal("a notice for a session that is gone was kept")
	}
	if got.Find("orphannotice0001", sessionraise.KindRaise) != nil {
		t.Fatal("a notice whose session is not in the operator ledger was kept")
	}
}

func TestRaiseNoticeDoesNotRepeatAfterRetention(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(raiseScheme(t)).Build()
	raiseObjects(t, c)
	ctx := context.Background()
	store := sessionraise.Kube{Reader: c, Writer: c, Namespace: sessionraise.ObjectNamespace, Secrets: map[string]bool{sessionraise.OperatorSecretName: true}}
	priv, err := sessionraise.Ensure(ctx, store, sessionraise.OperatorSecretName, sessionraise.OperatorPublicName, true)
	if err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 11, 13, 3, 15, 0, 0, time.UTC)
	now := when
	at := when.Format(time.RFC3339)
	const uid = "uid-raise-keep"
	hash := sessionraise.WordsHash("yes please")
	id := sessionraise.RecordID(uid, at, hash)
	rec, err := sessionraise.Sign(sessionraise.Record{
		ID: id, SessionUID: uid, State: sessionraise.StatePending, StartSessions: true, Depth: "2",
		Owner:       sessionraise.Owner{Words: "yes please", WordsHash: hash, Where: sessionraise.WherePhone, Session: "bench", SaidAt: at},
		PerformedBy: "session/bench", PerformedAt: at,
	}, priv)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := rec.Encode()
	if err != nil {
		t.Fatal(err)
	}
	sess := &v1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{Name: "raised", Namespace: "dev-agents", UID: uid, Annotations: map[string]string{v1alpha1.AnnotationRaise: string(raw)}},
		Spec:       v1alpha1.AgentSessionSpec{Repo: "haynes-ops", Agent: v1alpha1.AgentClaude, Mode: v1alpha1.ModeRemote, Model: "claude-opus-5-5", Prompt: "x"},
	}
	if err := c.Create(ctx, sess); err != nil {
		t.Fatal(err)
	}
	led := &sessionraise.Ledger{Sessions: []sessionraise.SessionEntry{{
		Name: "raised", UID: uid, ID: id, State: sessionraise.StatePending, ChangedAt: when,
		StartSessions: true, Depth: "2", WordsHash: hash, PerformedBy: "session/bench", PerformedAt: at,
	}}}
	body, err := led.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, sessionraise.LedgerName, map[string][]byte{sessionraise.LedgerField: body}, mustRV(t, c, sessionraise.LedgerName)); err != nil {
		t.Fatal(err)
	}
	sender := &fakeRaiseSender{}
	p := &RaisePager{Client: c, Live: c, Namespace: sessionraise.ObjectNamespace, SessionNamespace: "dev-agents", Sender: sender, Now: func() time.Time { return now }}
	p.announce(ctx)
	if sender.n != 1 {
		t.Fatalf("sends: %d", sender.n)
	}
	now = when.Add(sessionraise.WordRetention + time.Hour)
	p.announce(ctx)
	if sender.n != 1 {
		t.Fatalf("paged again after the word retention: %d", sender.n)
	}
}

func mustRV(t *testing.T, c client.Client, name string) string {
	t.Helper()
	cm := &corev1.ConfigMap{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: sessionraise.ObjectNamespace, Name: name}, cm); err != nil {
		t.Fatal(err)
	}
	return cm.ResourceVersion
}
