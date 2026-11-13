package keeper

import (
	"context"
	"crypto/ed25519"
	"errors"
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/sessionraise"
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
	quietUntil       time.Time
	lastQuiet        time.Time
	profileLogged    map[string]struct{}
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

// announce sends a pending raise whose record counts, and a take-back from
// the operator ledger row. It reads the ledger first and fetches, by name,
// only the sessions whose rows need a notice. It does not look at the time
// of day, and a missing object never fails the keeper.
func (p *RaisePager) announce(ctx context.Context) {
	if p.Client == nil || p.Sender == nil || p.quiet() {
		return
	}
	store := p.store()
	priv, err := sessionraise.Ensure(ctx, store, p.secretName(), sessionraise.KeeperPublicName, true)
	if err != nil {
		p.noteQuiet("raise notice key was not published", err)
		return
	}
	ledObj, err := store.Get(ctx, sessionraise.LedgerName)
	if err != nil {
		p.noteQuiet("raise notice ledger was not read", err)
		return
	}
	led, err := sessionraise.Unmarshal(ledObj.Data[sessionraise.LedgerField])
	if err != nil {
		p.noteQuiet("raise notice ledger was not read", err)
		return
	}
	notices, err := p.readNotices(ctx)
	if err != nil {
		p.noteQuiet("raise notice ledger was not read", err)
		return
	}
	now := p.now().UTC().Truncate(time.Second)
	var need []sessionraise.SessionEntry
	for _, e := range led.Sessions {
		if rowNeedsNotice(now, e, notices) {
			need = append(need, e)
		}
	}
	if len(need) == 0 {
		return
	}
	pub, pubErr := sessionraise.ReadPublic(ctx, store, sessionraise.OperatorPublicName)
	if pubErr != nil {
		p.noteQuietLog("raise operator key was not read", pubErr)
	}
	found, gone := p.sessionsFor(ctx, need)
	p.dropNotices(ctx, gone, p.countingIDs(found, pub, led))
	for i := range found {
		p.announceOne(ctx, priv, pub, pubErr, led, &found[i])
	}
}

func (p *RaisePager) quiet() bool {
	return !p.quietUntil.IsZero() && p.now().Before(p.quietUntil)
}

func (p *RaisePager) noteQuiet(msg string, err error) {
	p.quietUntil = p.now().Add(sessionraise.QuietRetry)
	p.noteQuietLog(msg, err)
}

func (p *RaisePager) noteQuietLog(msg string, err error) {
	now := p.now()
	if !p.lastQuiet.IsZero() && now.Sub(p.lastQuiet) < sessionraise.QuietLog {
		return
	}
	p.lastQuiet = now
	if err != nil {
		p.Log.Info(msg, "error", err.Error())
		return
	}
	p.Log.Info(msg)
}

func rowNeedsNotice(now time.Time, e sessionraise.SessionEntry, notices *sessionraise.NoticeLedger) bool {
	kind := ""
	switch e.State {
	case sessionraise.StatePending:
		kind = sessionraise.KindRaise
	case sessionraise.StateTakenBack:
		kind = sessionraise.KindTakeBack
	default:
		return false
	}
	if e.ID == "" || e.UID == "" {
		return false
	}
	_, due := noticeDue(now, notices.Find(e.ID, kind))
	return due
}

// sessionsFor fetches each needed row by name. It lists AgentSessions only
// when a needed row has no name, or the named session has a different UID.
func (p *RaisePager) sessionsFor(ctx context.Context, need []sessionraise.SessionEntry) ([]v1alpha1.AgentSession, []string) {
	var found []v1alpha1.AgentSession
	var gone []string
	var unnamed []sessionraise.SessionEntry
	for _, e := range need {
		if e.Name == "" {
			unnamed = append(unnamed, e)
			continue
		}
		var sess v1alpha1.AgentSession
		err := p.reader().Get(ctx, client.ObjectKey{Namespace: p.sessionNamespace(), Name: e.Name}, &sess)
		if apierrors.IsNotFound(err) {
			gone = append(gone, e.UID)
			continue
		}
		if err != nil {
			p.noteQuiet("raise notice session was not read", err)
			continue
		}
		if string(sess.UID) != e.UID {
			unnamed = append(unnamed, e)
			continue
		}
		found = append(found, sess)
	}
	if len(unnamed) == 0 {
		return found, gone
	}
	var list v1alpha1.AgentSessionList
	if err := p.reader().List(ctx, &list, client.InNamespace(p.sessionNamespace())); err != nil {
		p.noteQuiet("raise notice list", err)
		return found, gone
	}
	byUID := map[string]v1alpha1.AgentSession{}
	for i := range list.Items {
		byUID[string(list.Items[i].UID)] = list.Items[i]
	}
	for _, e := range unnamed {
		sess, ok := byUID[e.UID]
		if !ok {
			gone = append(gone, e.UID)
			continue
		}
		found = append(found, sess)
	}
	return found, gone
}

// countingIDs is the id of a record that counts, by session UID. An
// annotation that does not count is absent, so it cannot reset an attempt.
func (p *RaisePager) countingIDs(sessions []v1alpha1.AgentSession, pub ed25519.PublicKey, led *sessionraise.Ledger) map[string]string {
	out := map[string]string{}
	if len(pub) == 0 {
		return out
	}
	for i := range sessions {
		uid := string(sessions[i].UID)
		rec, err := sessionraise.Parse(sessions[i].Annotations[v1alpha1.AnnotationRaise])
		if err != nil {
			continue
		}
		if sessionraise.Counts(rec, uid, pub, led.Find(uid)) != "" {
			continue
		}
		out[uid] = rec.ID
	}
	return out
}

// dropNotices removes a notice whose session is known gone, and a notice
// whose id is not the id of the counting record for that session. Anything
// else stays, including a notice for an annotation that does not count.
func (p *RaisePager) dropNotices(ctx context.Context, gone []string, counting map[string]string) {
	if len(gone) == 0 && len(counting) == 0 {
		return
	}
	goneSet := map[string]bool{}
	for _, uid := range gone {
		if uid != "" {
			goneSet[uid] = true
		}
	}
	store := p.store()
	obj, err := store.Get(ctx, sessionraise.NoticeLedgerName)
	if err != nil {
		return
	}
	led, err := sessionraise.UnmarshalNotice(obj.Data[sessionraise.LedgerField])
	if err != nil || led == nil || len(led.Items) == 0 {
		return
	}
	kept := make([]sessionraise.Notice, 0, len(led.Items))
	for _, n := range led.Items {
		if goneSet[n.SessionUID] {
			continue
		}
		if id, ok := counting[n.SessionUID]; ok && n.ID != id {
			continue
		}
		kept = append(kept, n)
	}
	if len(kept) == len(led.Items) {
		return
	}
	led.Items = kept
	raw, err := led.Marshal()
	if err != nil {
		return
	}
	data := make(map[string][]byte, len(obj.Data)+1)
	for k, v := range obj.Data {
		data[k] = append([]byte(nil), v...)
	}
	data[sessionraise.LedgerField] = raw
	if err := store.Update(ctx, sessionraise.NoticeLedgerName, data, obj.RV); err != nil {
		p.Log.Info("raise notice ledger was not pruned", "error", err.Error())
	}
}

func (p *RaisePager) announceOne(ctx context.Context, priv ed25519.PrivateKey, pub ed25519.PublicKey, pubErr error, led *sessionraise.Ledger, sess *v1alpha1.AgentSession) {
	entry := led.Find(string(sess.UID))
	if entry == nil || entry.UID != string(sess.UID) || entry.ID == "" {
		return
	}
	// A take-back is decided from the operator ledger row. It does not wait
	// on the annotation or the operator public key.
	if entry.State == sessionraise.StateTakenBack {
		p.sendNotice(ctx, priv, sess, entry, sessionraise.KindTakeBack, sessionraise.Record{})
		return
	}
	if entry.State != sessionraise.StatePending || pubErr != nil {
		return
	}
	rec, err := sessionraise.Parse(sess.Annotations[v1alpha1.AnnotationRaise])
	if err != nil || sessionraise.Counts(rec, string(sess.UID), pub, entry) != "" {
		return
	}
	if rec.Profile != "" {
		p.logProfile(rec.ID)
		return
	}
	p.sendNotice(ctx, priv, sess, entry, sessionraise.KindRaise, rec)
}

func (p *RaisePager) logProfile(id string) {
	if p.profileLogged == nil {
		p.profileLogged = map[string]struct{}{}
	}
	if _, ok := p.profileLogged[id]; ok {
		return
	}
	p.profileLogged[id] = struct{}{}
	p.Log.Info("profile raises need slice (b)", "id", id)
}

func (p *RaisePager) sendNotice(ctx context.Context, priv ed25519.PrivateKey, sess *v1alpha1.AgentSession, entry *sessionraise.SessionEntry, kind string, rec sessionraise.Record) {
	notices, err := p.readNotices(ctx)
	if err != nil {
		p.noteQuiet("raise notice ledger was not read", err)
		return
	}
	atTime := p.now().UTC().Truncate(time.Second)
	attempt, ok := noticeDue(atTime, notices.Find(entry.ID, kind))
	if !ok {
		return
	}
	text := sessionraise.NoticeText{
		Session: sess.Name, Title: sess.Annotations[v1alpha1.AnnotationTitle],
		TakeBack: kind == sessionraise.KindTakeBack, TakenBy: entry.TakenBackBy, TakenAt: entry.TakenBackAt,
	}
	if kind == sessionraise.KindRaise {
		text.Performer = rec.PerformedBy
		text.From = rec.FromProfile
		text.Profile = rec.Profile
		text.StartSessions = rec.StartSessions
		text.Words = rec.Owner.Words
		text.Where = rec.Owner.Where
		text.SaidAt = rec.Owner.SaidAt
		text.Effect = atTime.Add(sessionraise.EffectDelay).Format(time.RFC3339)
	}
	title, body := sessionraise.Message(text, summonRedact)
	at := atTime.Format(time.RFC3339)
	if err := p.writeNotice(ctx, priv, sessionraise.Notice{
		ID: entry.ID, SessionUID: entry.UID, Kind: kind, Attempt: attempt,
		Outcome: sessionraise.OutcomeReserved, At: at,
	}); err != nil {
		p.Log.Info("raise notice reservation was not written", "session", sess.Name, "error", err.Error())
		return
	}
	outcome := classifyRaiseSend(ctx, p.Sender, Notice{Title: title, Message: body})
	sent := sessionraise.Notice{
		ID: entry.ID, SessionUID: entry.UID, Kind: kind, Attempt: attempt,
		Outcome: outcome, At: at,
	}
	if outcome == sessionraise.OutcomeSent {
		sent.SentAt = p.now().UTC().Truncate(time.Second).Format(time.RFC3339)
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
