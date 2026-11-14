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
	"github.com/thaynes43/dev-env/internal/sessiontitle"
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
	unnamedLogged    map[string]struct{}
	goneUID          map[string]struct{}
	keyReady         bool
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
// the operator ledger row. The first pass publishes the keeper key. Later
// passes read the operator ledger first, and read the key Secret only when
// a row needs a notice. A session is fetched by name. The pager does not
// list AgentSessions. It does not look at the time of day, and a missing
// object never fails the keeper.
func (p *RaisePager) announce(ctx context.Context) {
	if p.Client == nil || p.Sender == nil || p.quiet() {
		return
	}
	store := p.store()
	if !p.keyReady {
		if _, err := sessionraise.Ensure(ctx, store, p.secretName(), sessionraise.KeeperPublicName, true); err != nil {
			p.noteQuiet("raise notice key was not published", err)
			return
		}
		p.keyReady = true
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
	p.pruneMemory(led)
	var need []sessionraise.SessionEntry
	for _, e := range led.Sessions {
		// A gone session, and a nameless row already logged, need no notice.
		// Skip them before the key is loaded and before any further read.
		if e.Name == "" {
			if !p.unnamedSeen(e) {
				p.logUnnamed(e)
			}
			continue
		}
		if p.knownGone(e.UID) {
			continue
		}
		if rowNeedsNotice(now, e, notices) {
			need = append(need, e)
		}
	}
	// A UID that has left the operator ledger takes its notices with it.
	// Admit drops a gone session only when the ledger is already full, and
	// this matches that row. It does not list sessions, and it does not
	// treat an annotation that does not count as a new record.
	if absent := uidsAbsent(led, notices); len(absent) > 0 {
		p.dropNotices(ctx, absent, nil)
	}
	if len(need) == 0 {
		return
	}
	priv, err := sessionraise.Load(ctx, store, p.secretName(), sessionraise.KeeperPublicName)
	if err != nil {
		p.noteQuiet("raise notice key was not read", err)
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

// uidsAbsent is every notice session UID that the operator ledger does not
// hold. The caller has already read that ledger.
func uidsAbsent(led *sessionraise.Ledger, notices *sessionraise.NoticeLedger) []string {
	present := map[string]bool{}
	if led != nil {
		for _, e := range led.Sessions {
			if e.UID != "" {
				present[e.UID] = true
			}
		}
	}
	var absent []string
	seen := map[string]bool{}
	if notices == nil {
		return nil
	}
	for _, n := range notices.Items {
		if n.SessionUID == "" || present[n.SessionUID] || seen[n.SessionUID] {
			continue
		}
		seen[n.SessionUID] = true
		absent = append(absent, n.SessionUID)
	}
	return absent
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

// sessionsFor fetches each needed row by name. It does not list
// AgentSessions. NotFound, or a session whose UID is not the row's, means
// that session is gone: names are not reused for the same UID. The UID is
// remembered for this process so it is not fetched again. A row with no
// name is skipped and logged once. Any other read error leaves the row and
// its notices untouched.
func (p *RaisePager) sessionsFor(ctx context.Context, need []sessionraise.SessionEntry) ([]v1alpha1.AgentSession, []string) {
	var found []v1alpha1.AgentSession
	var gone []string
	for _, e := range need {
		if e.Name == "" {
			p.logUnnamed(e)
			continue
		}
		if p.knownGone(e.UID) {
			gone = append(gone, e.UID)
			continue
		}
		var sess v1alpha1.AgentSession
		err := p.reader().Get(ctx, client.ObjectKey{Namespace: p.sessionNamespace(), Name: e.Name}, &sess)
		if apierrors.IsNotFound(err) {
			p.rememberGone(e.UID)
			gone = append(gone, e.UID)
			continue
		}
		if err != nil {
			p.noteQuiet("raise notice session was not read", err)
			continue
		}
		if string(sess.UID) != e.UID {
			p.rememberGone(e.UID)
			gone = append(gone, e.UID)
			continue
		}
		found = append(found, sess)
	}
	return found, gone
}

func (p *RaisePager) knownGone(uid string) bool {
	if uid == "" || p.goneUID == nil {
		return false
	}
	_, ok := p.goneUID[uid]
	return ok
}

func (p *RaisePager) rememberGone(uid string) {
	if uid == "" {
		return
	}
	if p.goneUID == nil {
		p.goneUID = map[string]struct{}{}
	}
	if _, ok := p.goneUID[uid]; ok {
		return
	}
	p.goneUID[uid] = struct{}{}
	p.Log.Info("raise notice session is gone", "uid", uid)
}

func (p *RaisePager) unnamedSeen(e sessionraise.SessionEntry) bool {
	key := e.UID
	if key == "" {
		key = e.ID
	}
	if key == "" || p.unnamedLogged == nil {
		return false
	}
	_, ok := p.unnamedLogged[key]
	return ok
}

// pruneMemory drops remembered UIDs and ids that the operator ledger no
// longer holds. goneUID is keyed by UID. unnamedLogged is keyed by UID or
// id. profileLogged is keyed by id.
func (p *RaisePager) pruneMemory(led *sessionraise.Ledger) {
	uids := map[string]struct{}{}
	ids := map[string]struct{}{}
	if led != nil {
		for _, e := range led.Sessions {
			if e.UID != "" {
				uids[e.UID] = struct{}{}
			}
			if e.ID != "" {
				ids[e.ID] = struct{}{}
			}
		}
	}
	both := map[string]struct{}{}
	for k := range uids {
		both[k] = struct{}{}
	}
	for k := range ids {
		both[k] = struct{}{}
	}
	p.goneUID = keepMemory(p.goneUID, uids)
	p.unnamedLogged = keepMemory(p.unnamedLogged, both)
	p.profileLogged = keepMemory(p.profileLogged, ids)
}

func keepMemory(set, allow map[string]struct{}) map[string]struct{} {
	if len(set) == 0 {
		return set
	}
	for k := range set {
		if _, ok := allow[k]; !ok {
			delete(set, k)
		}
	}
	return set
}

func (p *RaisePager) logUnnamed(e sessionraise.SessionEntry) {
	key := e.UID
	if key == "" {
		key = e.ID
	}
	if key == "" {
		return
	}
	if p.unnamedLogged == nil {
		p.unnamedLogged = map[string]struct{}{}
	}
	if _, ok := p.unnamedLogged[key]; ok {
		return
	}
	p.unnamedLogged[key] = struct{}{}
	p.Log.Info("raise notice row has no session name", "id", e.ID)
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
		p.noteQuiet("raise notice ledger was not pruned", err)
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
	ann, present := sess.Annotations[v1alpha1.AnnotationTitle]
	effective, _ := sessiontitle.Effective(ann, present, sess.Spec.Title)
	text := sessionraise.NoticeText{
		Session: sess.Name, Title: effective,
		TakeBack: kind == sessionraise.KindTakeBack, TakenBy: entry.TakenBackBy, TakenAt: entry.TakenBackAt,
	}
	if kind == sessionraise.KindRaise {
		text.Performer = rec.PerformedBy
		text.Profile = rec.Profile
		text.Words = rec.Owner.Words
		text.Where = rec.Owner.Where
		text.SaidAt = rec.Owner.SaidAt
		text.Effect = atTime.Add(sessionraise.EffectDelay).Format(time.RFC3339)
	}
	title, body, err := sessionraise.Message(text, summonRedact)
	if err != nil {
		return
	}
	// A body over the phone's limit is rebuilt once, without the title
	// sentence. The words are never cut. A body that is still too long is
	// not sent; the outcome is Failed.
	if len(body) > sessionraise.NoticeBodyMax && text.Title != "" {
		text.Title = ""
		title, body, err = sessionraise.Message(text, summonRedact)
		if err != nil {
			return
		}
	}
	at := atTime.Format(time.RFC3339)
	if len(body) > sessionraise.NoticeBodyMax {
		p.Log.Info("raise notice is too long for the phone", "id", entry.ID)
		if err := p.writeNotice(ctx, priv, sessionraise.Notice{
			ID: entry.ID, SessionUID: entry.UID, Kind: kind, Attempt: attempt,
			Outcome: sessionraise.OutcomeFailed, At: at,
		}); err != nil {
			p.noteQuiet("raise notice outcome was not written", err)
		}
		return
	}
	if err := p.writeNotice(ctx, priv, sessionraise.Notice{
		ID: entry.ID, SessionUID: entry.UID, Kind: kind, Attempt: attempt,
		Outcome: sessionraise.OutcomeReserved, At: at,
	}); err != nil {
		p.noteQuiet("raise notice reservation was not written", err)
		return
	}
	outcome := classifyRaiseSend(ctx, p.Sender, Notice{Title: title, Message: body})
	sent := sessionraise.Notice{
		ID: entry.ID, SessionUID: entry.UID, Kind: kind, Attempt: attempt,
		Outcome: outcome, At: at,
	}
	if outcome == sessionraise.OutcomeSent {
		sent.SentAt = ceilSecond(p.now().UTC()).Format(time.RFC3339)
	}
	if err := p.writeNotice(ctx, priv, sent); err != nil {
		p.noteQuiet("raise notice outcome was not written", err)
	}
}

// ceilSecond rounds t up to the next whole second. A time already on a
// whole second stays there, so sentAt is never earlier than the push.
func ceilSecond(t time.Time) time.Time {
	t = t.UTC()
	if t.Nanosecond() == 0 {
		return t
	}
	return t.Add(time.Second).Truncate(time.Second)
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
		// The wait above already returned while the reservation is younger
		// than NoticeRetry. An older reservation counts as one Ambiguous
		// attempt, so a keeper that never records an outcome still stops at 12.
		if cur.Attempt >= sessionraise.NoticeAttempts {
			return 0, false
		}
		return cur.Attempt + 1, true
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
