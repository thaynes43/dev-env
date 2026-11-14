package apiserver

import (
	"context"
	"crypto/ed25519"
	"errors"
	"time"

	"k8s.io/apimachinery/pkg/types"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/sessionraise"
)

// readNotices reads the keeper's notice ledger and the keeper's public key.
// A missing ledger or key is an error, so nothing is activated from it.
func (s *Server) readNotices(ctx context.Context, cfg RaiseConfig) (*sessionraise.NoticeLedger, ed25519.PublicKey, error) {
	store := s.raiseStore(cfg)
	pub, err := sessionraise.ReadPublic(ctx, store, cfg.KeeperPublic)
	if err != nil {
		return nil, nil, err
	}
	obj, err := store.Get(ctx, cfg.KeeperLedger)
	if err != nil {
		return nil, nil, err
	}
	led, err := sessionraise.UnmarshalNotice(obj.Data[sessionraise.LedgerField])
	if err != nil {
		return nil, nil, err
	}
	return led, pub, nil
}

// activateRaises moves pending rows the keeper has announced. It runs when
// dispatch is on or a profile list is configured. One pass reads the
// operator ledger once. That read, like the informer cache, is only a hint
// for which rows to visit: a missing session is skipped, and a row is
// visited only when it is pending or the cached annotation's state differs
// from the row. The hint decides nothing. For each visited row the live
// session is read first and that session's ledger row second, and the
// repair, the activation or the unannounce is made from those two. The
// activator never writes an annotation whose state differs from the ledger
// row it read after the live session. On a conflict both are read again.
// The ledger is written before the annotation. A row that is no longer
// pending is left alone, so a take-back wins over a later Sent.
func (s *Server) activateRaises(ctx context.Context) {
	cfg, ok := s.raiseConfig()
	if !ok || s.Live == nil || s.Client == nil || (!cfg.Dispatch && len(cfg.Profiles) == 0) {
		return
	}
	led, err := s.readLedger(ctx, cfg)
	if err != nil || led == nil {
		if err != nil {
			s.Log.Error(err, "raise activation ledger")
		}
		return
	}
	ns := s.Policy.SessionNamespace
	var visit []sessionraise.SessionEntry
	for _, e := range led.Sessions {
		if e.Name == "" || e.UID == "" {
			continue
		}
		var cached v1alpha1.AgentSession
		if s.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: e.Name}, &cached) != nil || string(cached.UID) != e.UID {
			continue
		}
		if e.State != sessionraise.StatePending && annotationState(cached) == e.State {
			continue
		}
		visit = append(visit, e)
	}
	if len(visit) == 0 {
		return
	}
	notices, pub, err := s.readNotices(ctx, cfg)
	if err != nil {
		return
	}
	priv, err := s.raiseKey(ctx, cfg)
	if err != nil {
		return
	}
	now := s.now().UTC().Truncate(time.Second)
	for i := range visit {
		e := visit[i]
		hint := &v1alpha1.AgentSession{}
		hint.Namespace, hint.Name = ns, e.Name
		// The row that chose this visit is not the decision. Nil makes
		// activateOneDepth read the ledger after the live session.
		s.activateOneDepth(ctx, cfg, hint, nil, notices, pub, priv, now, 0)
	}
}

func (s *Server) activateOneDepth(ctx context.Context, cfg RaiseConfig, sess *v1alpha1.AgentSession, entry *sessionraise.SessionEntry, notices *sessionraise.NoticeLedger, pub ed25519.PublicKey, priv ed25519.PrivateKey, now time.Time, depth int) {
	if depth > 2 || sess == nil || s.Live == nil {
		return
	}
	var live v1alpha1.AgentSession
	if err := s.Live.Get(ctx, types.NamespacedName{Namespace: sess.Namespace, Name: sess.Name}, &live); err != nil {
		return
	}
	// Every caller passes nil, so the row is read here, after the live
	// session. A non-nil row was read before this Get. The conflict paths
	// pass nil: passing the earlier row skips this read and can sign a
	// state the ledger no longer has.
	loaded := entry == nil
	if entry != nil && entry.UID != string(live.UID) {
		return
	}
	if entry == nil {
		got, err := s.raiseEntry(ctx, cfg, string(live.UID))
		if err != nil || got == nil || got.ID == "" {
			return
		}
		entry = got
	}
	opPub := priv.Public().(ed25519.PublicKey)
	ann, annOK := verifiedRaiseWords(&live, opPub, entry.ID)
	if annOK && !raiseGrantMatches(ann, *entry) {
		s.logOnce(entry.ID, "grant fields differ", "raise ledger grant fields differ from the verified annotation")
		return
	}
	// The annotation and the ledger disagree. Re-sign from the ledger row
	// and the verified words, including a taken-back row. An annotation that
	// does not verify is left as it is and logged.
	// A pending row is not repaired here. The gate before Finish refuses an
	// annotation that verifies but is not pending, so that gate is the one
	// a test can remove. A row that has already moved is re-signed to match.
	if annOK && ann.State != entry.State && entry.State != sessionraise.StatePending {
		if sessionraise.WordsHash(ann.Owner.Words) != entry.WordsHash || sessionraise.WordsProblem(ann.Owner.Words) != "" {
			s.logOnce(entry.ID, "words do not verify", "raise words do not verify, so the annotation was not re-signed")
			return
		}
		// This call read the row after the live session. Read it again just
		// before the write, and write only when it still has the id and the
		// state just decided. If it does not, re-decide from fresh reads.
		// A row passed in from before this Get skips that re-read. The
		// conflict path passes nil, so the next call reads again. The
		// activator never writes an annotation whose state differs from the
		// ledger row it read after the live session.
		writeFrom := entry
		if loaded {
			fresh, err := s.raiseEntry(ctx, cfg, string(live.UID))
			if err != nil {
				return
			}
			if fresh == nil || fresh.ID != entry.ID || fresh.State != entry.State {
				s.activateOneDepth(ctx, cfg, &live, nil, notices, pub, priv, now, depth+1)
				return
			}
			writeFrom = fresh

		}
		if err := s.writeSignedFromLedger(ctx, &live, *writeFrom, ann, notices, pub, priv, now); errors.Is(err, errRaiseAnnotationMoved) {
			s.activateOneDepth(ctx, cfg, &live, nil, notices, pub, priv, now, depth+1)
		}
		return
	}
	if !annOK && annotationState(live) != entry.State {
		s.logOnce(entry.ID, "annotation does not verify", "raise annotation does not verify, so it was not re-signed")
	}
	if entry.State != sessionraise.StatePending {
		return
	}
	// Before the ledger swap: the stored annotation must verify, name this
	// row, and be pending. Otherwise the row stays pending.
	if !annOK || ann.State != sessionraise.StatePending {
		s.logOnce(entry.ID, "annotation is not pending", "raise annotation is not a verified pending record, so the ledger row stays pending")
		return
	}
	if sessionraise.WordsProblem(ann.Owner.Words) != "" || sessionraise.WordsHash(ann.Owner.Words) != entry.WordsHash {
		s.logOnce(entry.ID, "words do not verify", "raise words do not verify, so the ledger row stays pending")
		return
	}
	if sent, ok := sessionraise.SentTime(*entry, notices.Items, pub); ok && sessionraise.SentBeforePerformed(sent, entry.PerformedAt) {
		s.logOnce(entry.ID, "notice sent before the raise", "a raise notice was sent before the raise, so the row stays pending")
		return
	}
	action := sessionraise.Activation(now, *entry, notices.Items, pub)
	if action == sessionraise.ActionNone {
		return
	}
	state := sessionraise.StateUnannounced
	sentAt, activeFrom := "", ""
	if action == sessionraise.ActionActivate {
		var ok bool
		sentAt, activeFrom, ok = maySignRaiseActive(ann, notices, pub, now)
		if !ok {
			return
		}
		state = sessionraise.StateActive
	}
	var finished sessionraise.SessionEntry
	ok := false
	err := s.commitLedger(ctx, cfg, func(led *sessionraise.Ledger) (bool, error) {
		got, wrote := led.Finish(string(live.UID), entry.ID, state, sentAt, activeFrom, now)
		finished = got
		ok = wrote
		return wrote, nil
	})
	if err != nil {
		return
	}
	if !ok {
		s.activateOneDepth(ctx, cfg, &live, nil, notices, pub, priv, now, depth+1)
		return
	}
	// The audit line follows Finish, before the annotation write, so a
	// conflict that the repair branch then finishes is still logged.
	signed := recordFrom(finished, ann.Owner.Words)
	if state == sessionraise.StateUnannounced {
		s.logRaiseState("raise unannounced", signed)
	} else {
		s.logRaiseState("raise active", signed)
	}
	if err := s.writeSignedFromLedger(ctx, &live, finished, ann, notices, pub, priv, now); errors.Is(err, errRaiseAnnotationMoved) {
		s.activateOneDepth(ctx, cfg, &live, nil, notices, pub, priv, now, depth+1)
		return
	} else if err != nil {
		s.Log.Error(err, "raise activation annotation", "session", live.Name, "id", entry.ID)
	}
}

// annotationState is the state in the stored annotation, or "" when it does
// not parse. It is only used to see whether the annotation and the ledger
// disagree before a re-sign, which still requires a verified record.
func annotationState(sess v1alpha1.AgentSession) string {
	rec, err := sessionraise.Parse(sess.Annotations[v1alpha1.AnnotationRaise])
	if err != nil {
		return ""
	}
	return rec.State
}

// raiseGrantMatches compares every immutable grant field with authenticated
// evidence. Only the state and its notice/take-back times may come from a row.
func raiseGrantMatches(ann sessionraise.Record, entry sessionraise.SessionEntry) bool {
	want := recordFrom(entry, ann.Owner.Words)
	want.State, want.NoticeSentAt, want.ActiveFrom = ann.State, ann.NoticeSentAt, ann.ActiveFrom
	want.TakenBackBy, want.TakenBackAt = ann.TakenBackBy, ann.TakenBackAt
	want.KeyID, want.Sig = ann.KeyID, ann.Sig
	return want == ann
}

// maySignRaiseActive is shared by activation and repair. A ledger state is
// never activation evidence: only a verified raise Sent for this id and UID,
// at or after performedAt, followed by the full delay, permits active.
func maySignRaiseActive(ann sessionraise.Record, notices *sessionraise.NoticeLedger, pub ed25519.PublicKey, now time.Time) (string, string, bool) {
	if notices == nil {
		return "", "", false
	}
	entry := sessionraise.SessionEntry{ID: ann.ID, UID: ann.SessionUID}
	sent, ok := sessionraise.SentTime(entry, notices.Items, pub)
	at, err := time.Parse(time.RFC3339, sent)
	performed, performedErr := time.Parse(time.RFC3339, ann.PerformedAt)
	if !ok || err != nil || performedErr != nil || at.Before(performed) || now.Before(at.Add(sessionraise.EffectDelay)) {
		return "", "", false
	}
	return sent, at.Add(sessionraise.EffectDelay).UTC().Format(time.RFC3339), true
}

// writeSignedFromLedger starts from the verified annotation, retaining every
// grant field. A terminal annotation cannot be revived by editing the ledger.
func (s *Server) writeSignedFromLedger(ctx context.Context, sess *v1alpha1.AgentSession, entry sessionraise.SessionEntry, ann sessionraise.Record, notices *sessionraise.NoticeLedger, pub ed25519.PublicKey, priv ed25519.PrivateKey, now time.Time) error {
	if !raiseGrantMatches(ann, entry) {
		s.logOnce(entry.ID, "grant fields differ", "raise ledger grant fields differ from the verified annotation")
		return nil
	}
	allowed := ann.State == sessionraise.StatePending && (entry.State == sessionraise.StateActive || entry.State == sessionraise.StateUnannounced || entry.State == sessionraise.StateTakenBack) || (ann.State == sessionraise.StateActive || ann.State == sessionraise.StateUnannounced) && entry.State == sessionraise.StateTakenBack
	if !allowed {
		s.logOnce(entry.ID, "invalid state transition", "raise ledger transition from the verified annotation is refused")
		return nil
	}
	rec := ann
	rec.State = entry.State
	if entry.State == sessionraise.StateActive {
		var ok bool
		rec.NoticeSentAt, rec.ActiveFrom, ok = maySignRaiseActive(ann, notices, pub, now)
		if !ok {
			s.logOnce(entry.ID, "activation evidence missing", "raise active annotation needs a verified Sent and its full delay")
			return nil
		}
	}
	if entry.State == sessionraise.StateTakenBack {
		rec.TakenBackBy, rec.TakenBackAt = entry.TakenBackBy, entry.TakenBackAt
	}
	signed, err := sessionraise.Sign(rec, priv)
	if err != nil {
		s.Log.Error(err, "raise activation sign", "session", sess.Name, "id", entry.ID)
		return err
	}
	raw, err := signed.Encode()
	if err != nil {
		return err
	}
	err = s.writeRaiseAnnotationWhen(ctx, types.NamespacedName{Namespace: sess.Namespace, Name: sess.Name}, sess.UID, string(raw), func(cur *v1alpha1.AgentSession) bool {
		return cur.Annotations[v1alpha1.AnnotationRaise] == sess.Annotations[v1alpha1.AnnotationRaise]
	})
	if err == nil {
		s.logRaiseState(observedAudit(sess.Name, signed.State), signed)
	}
	return err
}

// RaiseActivator is the leader loop. It does nothing when the dispatch flag
// is off.
type RaiseActivator struct {
	Server *Server
	Every  time.Duration
}

// NeedLeaderElection is true: only the leader moves pending to active.
func (a *RaiseActivator) NeedLeaderElection() bool { return true }

// Start ticks until the manager stops it.
func (a *RaiseActivator) Start(ctx context.Context) error {
	every := a.Every
	if every <= 0 {
		every = 30 * time.Second
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	a.Server.activateRaises(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			a.Server.activateRaises(ctx)
		}
	}
}

// RaiseKeyOwner publishes the operator raise key on the leader. It calls
// Ensure on every tick, including after a success, so an emptied Secret is
// filled again. A missing Secret is logged at most once an hour. POST
// refuses until Ensure succeeds.
type RaiseKeyOwner struct {
	Server  *Server
	Every   time.Duration
	lastLog time.Time
}

// NeedLeaderElection is true: only the leader fills an empty key Secret.
func (k *RaiseKeyOwner) NeedLeaderElection() bool { return true }

func (k *RaiseKeyOwner) interval() time.Duration {
	if k.Every > 0 {
		return k.Every
	}
	return sessionraise.QuietRetry
}

// Start calls Ensure once, then again on every tick, until the manager stops.
func (k *RaiseKeyOwner) Start(ctx context.Context) error {
	tick := time.NewTicker(k.interval())
	defer tick.Stop()
	k.ensure(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			k.ensure(ctx)
		}
	}
}

func (k *RaiseKeyOwner) ensure(ctx context.Context) {
	if k == nil || k.Server == nil || k.Server.Raise == nil {
		return
	}
	cfg := k.Server.Raise.filled()
	if _, err := sessionraise.Ensure(ctx, k.Server.raiseStore(cfg), cfg.OperatorSecret, cfg.OperatorPublic, true); err != nil {
		k.note(err)
	}
}

func (k *RaiseKeyOwner) note(err error) {
	if k.Server == nil {
		return
	}
	now := time.Now()
	if k.Server.Now != nil {
		now = k.Server.Now()
	}
	if !k.lastLog.IsZero() && now.Sub(k.lastLog) < sessionraise.QuietLog {
		return
	}
	k.lastLog = now
	k.Server.Log.Info("raise key was not published", "error", err.Error())
}
