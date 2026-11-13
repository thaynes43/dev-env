package keeper

import (
	"context"
	"crypto/ed25519"
	"errors"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/sessionraise"
	"github.com/thaynes43/dev-env/internal/templates"
)

// RaisePageOptions selects the keeper's raise notices. The zero value uses
// the DESIGN-066 object names. Notices go out at any hour: they do not
// follow the ask quiet window.
type RaisePageOptions struct {
	SessionNamespace string
	KeySecret        string
}

// RaisePager announces a pending raise and a take-back. It signs each
// outcome with the keeper key it read back. A missing Secret or ledger is
// logged and skipped; the keeper keeps running.
type RaisePager struct {
	Client           client.Client
	Live             client.Reader
	Namespace        string
	SessionNamespace string
	KeySecret        string
	Sender           NoticeSender
	Now              func() time.Time
	Log              logr.Logger
	Every            time.Duration
}

// NeedLeaderElection is true: only the leader sends and signs.
func (*RaisePager) NeedLeaderElection() bool { return true }

// Start announces once, then every 30 seconds, until the manager stops.
func (p *RaisePager) Start(ctx context.Context) error {
	p.announce(ctx)
	every := p.Every
	if every <= 0 {
		every = 30 * time.Second
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			p.announce(ctx)
		}
	}
}

func (p *RaisePager) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *RaisePager) store() sessionraise.Kube {
	ns := p.Namespace
	if ns == "" {
		ns = sessionraise.ObjectNamespace
	}
	return sessionraise.Kube{
		Reader: p.reader(), Writer: p.Client, Namespace: ns,
		Secrets: map[string]bool{p.secretName(): true},
	}
}

func (p *RaisePager) reader() client.Reader {
	if p.Live != nil {
		return p.Live
	}
	return p.Client
}

func (p *RaisePager) secretName() string {
	if p.KeySecret != "" {
		return p.KeySecret
	}
	return sessionraise.KeeperSecretName
}

func (p *RaisePager) sessionNamespace() string {
	if p.SessionNamespace != "" {
		return p.SessionNamespace
	}
	return "dev-agents"
}

// announce sends every pending raise and every take-back whose record
// counts and whose notice is due. It does not look at the time of day.
func (p *RaisePager) announce(ctx context.Context) {
	if p.Client == nil || p.Sender == nil {
		return
	}
	store := p.store()
	priv, err := sessionraise.Ensure(ctx, store, p.secretName(), sessionraise.KeeperPublicName, true)
	if err != nil {
		p.Log.Info("raise notice key was not published", "error", err.Error())
		return
	}
	pub, err := sessionraise.ReadPublic(ctx, store, sessionraise.OperatorPublicName)
	if err != nil {
		return
	}
	ledObj, err := store.Get(ctx, sessionraise.LedgerName)
	if err != nil {
		return
	}
	led, err := sessionraise.Unmarshal(ledObj.Data[sessionraise.LedgerField])
	if err != nil {
		return
	}
	var list v1alpha1.AgentSessionList
	if err := p.reader().List(ctx, &list, client.InNamespace(p.sessionNamespace())); err != nil {
		p.Log.Error(err, "raise notice list")
		return
	}
	now := p.now().UTC().Truncate(time.Second)
	for i := range list.Items {
		p.announceOne(ctx, priv, pub, led, &list.Items[i], now)
	}
}

func (p *RaisePager) announceOne(ctx context.Context, priv ed25519.PrivateKey, pub ed25519.PublicKey, led *sessionraise.Ledger, sess *v1alpha1.AgentSession, now time.Time) {
	raw := sess.Annotations[v1alpha1.AnnotationRaise]
	if raw == "" {
		return
	}
	rec, err := sessionraise.Parse(raw)
	if err != nil {
		return
	}
	entry := led.Find(string(sess.UID))
	if sessionraise.Counts(rec, string(sess.UID), pub, entry) != "" {
		return
	}
	kind := ""
	switch rec.State {
	case sessionraise.StatePending:
		kind = sessionraise.KindRaise
	case sessionraise.StateTakenBack:
		kind = sessionraise.KindTakeBack
	default:
		return
	}
	notices, err := p.readNotices(ctx)
	if err != nil {
		return
	}
	attempt, ok := noticeDue(now, notices.Find(rec.ID, kind))
	if !ok {
		return
	}
	super, known := p.superuser(ctx, rec.Profile)
	if !known {
		return
	}
	text := sessionraise.NoticeText{
		Session: sess.Name, Title: sess.Annotations[v1alpha1.AnnotationTitle],
		Performer: rec.PerformedBy, From: rec.FromProfile, Profile: rec.Profile,
		Superuser: super, StartSessions: rec.StartSessions,
		Words: rec.Owner.Words, Where: rec.Owner.Where, SaidAt: rec.Owner.SaidAt,
		Effect:   now.Add(sessionraise.EffectDelay).Format(time.RFC3339),
		TakeBack: kind == sessionraise.KindTakeBack, TakenBy: rec.TakenBackBy, TakenAt: rec.TakenBackAt,
	}
	title, body := sessionraise.Message(text, summonRedact)
	at := now.Format(time.RFC3339)
	if err := p.writeNotice(ctx, priv, sessionraise.Notice{
		ID: rec.ID, SessionUID: rec.SessionUID, Kind: kind, Attempt: attempt,
		Outcome: sessionraise.OutcomeReserved, At: at,
	}); err != nil {
		p.Log.Info("raise notice reservation was not written", "session", sess.Name, "error", err.Error())
		return
	}
	outcome := classifyRaiseSend(ctx, p.Sender, Notice{Title: title, Message: body})
	sent := sessionraise.Notice{
		ID: rec.ID, SessionUID: rec.SessionUID, Kind: kind, Attempt: attempt,
		Outcome: outcome, At: at,
	}
	if outcome == sessionraise.OutcomeSent {
		sent.SentAt = at
	}
	if err := p.writeNotice(ctx, priv, sent); err != nil {
		p.Log.Info("raise notice outcome was not written", "session", sess.Name, "error", err.Error())
	}
}

func noticeDue(now time.Time, cur *sessionraise.Notice) (int, bool) {
	if cur == nil {
		return 1, true
	}
	if cur.Outcome == sessionraise.OutcomeSent {
		return 0, false
	}
	if at, err := time.Parse(time.RFC3339, cur.At); err == nil && now.Sub(at) < sessionraise.NoticeRetry {
		return 0, false
	}
	if cur.Outcome == sessionraise.OutcomeReserved {
		if cur.Attempt < 1 {
			return 1, true
		}
		return cur.Attempt, true
	}
	if cur.Attempt >= sessionraise.NoticeAttempts {
		return 0, false
	}
	return cur.Attempt + 1, true
}

func (p *RaisePager) superuser(ctx context.Context, profile string) (bool, bool) {
	if profile == "" {
		return false, true
	}
	ns := p.Namespace
	if ns == "" {
		ns = sessionraise.ObjectNamespace
	}
	var cm corev1.ConfigMap
	if err := p.reader().Get(ctx, client.ObjectKey{Namespace: ns, Name: templates.DefaultName}, &cm); err != nil {
		return false, false
	}
	doc, err := templates.Parse(cm.Data)
	if err != nil {
		return false, false
	}
	_, prof, err := doc.Profile(profile)
	if err != nil {
		return false, false
	}
	return prof.ServiceAccount == templates.WorkbenchServiceAccount, true
}

func (p *RaisePager) readNotices(ctx context.Context) (*sessionraise.NoticeLedger, error) {
	obj, err := p.store().Get(ctx, sessionraise.NoticeLedgerName)
	if err != nil {
		return nil, err
	}
	return sessionraise.UnmarshalNotice(obj.Data[sessionraise.LedgerField])
}

func (p *RaisePager) writeNotice(ctx context.Context, priv ed25519.PrivateKey, n sessionraise.Notice) error {
	store := p.store()
	var err error
	for try := 0; try < 5; try++ {
		var obj sessionraise.Object
		obj, err = store.Get(ctx, sessionraise.NoticeLedgerName)
		if err != nil {
			return err
		}
		led, uerr := sessionraise.UnmarshalNotice(obj.Data[sessionraise.LedgerField])
		if uerr != nil {
			return uerr
		}
		signed, serr := sessionraise.SignNotice(n, priv)
		if serr != nil {
			return serr
		}
		led.Put(signed)
		raw, merr := led.Marshal()
		if merr != nil {
			return merr
		}
		data := make(map[string][]byte, len(obj.Data)+1)
		for k, v := range obj.Data {
			data[k] = v
		}
		data[sessionraise.LedgerField] = raw
		err = store.Update(ctx, sessionraise.NoticeLedgerName, data, obj.RV)
		if !errors.Is(err, sessionraise.ErrConflict) {
			return err
		}
	}
	return err
}

func classifyRaiseSend(ctx context.Context, sender NoticeSender, n Notice) string {
	var rc Receipt
	var err error
	if s, ok := sender.(ReceiptSender); ok {
		rc, err = s.SendReceipt(ctx, n)
	} else if sender != nil {
		err = sender.Send(ctx, n)
		rc.Confirmed = err == nil
	} else {
		return sessionraise.OutcomeFailed
	}
	var refused *SendError
	switch {
	case err == nil && rc.Confirmed:
		return sessionraise.OutcomeSent
	case errors.Is(err, errPushoverCredentials):
		return sessionraise.OutcomeFailed
	case errors.As(err, &refused) && refused.Status >= 400 && refused.Status < 500:
		return sessionraise.OutcomeFailed
	default:
		return sessionraise.OutcomeAmbiguous
	}
}
