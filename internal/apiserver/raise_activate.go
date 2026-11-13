package apiserver

import (
	"context"
	"crypto/ed25519"
	"errors"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

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
// dispatch is on or a profile list is configured. The ledger is written
// before the annotation. A row that is no longer pending is left alone, so
// a take-back wins over a later Sent.
func (s *Server) activateRaises(ctx context.Context) {
	cfg, ok := s.raiseConfig()
	if !ok || s.Live == nil || (!cfg.Dispatch && len(cfg.Profiles) == 0) {
		return
	}
	var list v1alpha1.AgentSessionList
	if err := s.Live.List(ctx, &list, client.InNamespace(s.Policy.SessionNamespace)); err != nil {
		s.Log.Error(err, "raise activation list")
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
	for i := range list.Items {
		s.activateOne(ctx, cfg, &list.Items[i], notices, pub, priv, now)
	}
}

func (s *Server) activateOne(ctx context.Context, cfg RaiseConfig, sess *v1alpha1.AgentSession, notices *sessionraise.NoticeLedger, pub ed25519.PublicKey, priv ed25519.PrivateKey, now time.Time) {
	s.activateOneDepth(ctx, cfg, sess, notices, pub, priv, now, 0)
}

func (s *Server) activateOneDepth(ctx context.Context, cfg RaiseConfig, sess *v1alpha1.AgentSession, notices *sessionraise.NoticeLedger, pub ed25519.PublicKey, priv ed25519.PrivateKey, now time.Time, depth int) {
	if depth > 2 || sess == nil || s.Live == nil {
		return
	}
	var live v1alpha1.AgentSession
	if err := s.Live.Get(ctx, types.NamespacedName{Namespace: sess.Namespace, Name: sess.Name}, &live); err != nil {
		return
	}
	entry, err := s.raiseEntry(ctx, cfg, string(live.UID))
	if err != nil || entry == nil || entry.ID == "" {
		return
	}
	opPub := priv.Public().(ed25519.PublicKey)
	ann, annOK := verifiedRaiseWords(&live, opPub, entry.ID)
	// The annotation and the ledger disagree. Re-sign from the ledger row
	// and the verified words, including a taken-back row. An annotation that
	// does not verify is left as it is and logged.
	if annOK && ann.State != entry.State {
		if sessionraise.WordsHash(ann.Owner.Words) != entry.WordsHash || sessionraise.WordsProblem(ann.Owner.Words) != "" {
			s.logOnce(entry.ID, "words do not verify", "raise words do not verify, so the annotation was not re-signed")
			return
		}
		if err := s.writeSignedFromLedger(ctx, &live, *entry, ann.Owner.Words, ann.State, priv); errors.Is(err, errRaiseAnnotationMoved) {
			s.activateOneDepth(ctx, cfg, &live, notices, pub, priv, now, depth+1)
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
		sent, ok := sessionraise.SentTime(*entry, notices.Items, pub)
		if !ok {
			return
		}
		at, err := time.Parse(time.RFC3339, sent)
		if err != nil {
			return
		}
		state = sessionraise.StateActive
		sentAt = sent
		activeFrom = at.Add(sessionraise.EffectDelay).UTC().Format(time.RFC3339)
	}
	var finished sessionraise.SessionEntry
	ok := false
	err = s.commitLedger(ctx, cfg, func(led *sessionraise.Ledger) (bool, error) {
		got, wrote := led.Finish(string(live.UID), entry.ID, state, sentAt, activeFrom, now)
		finished = got
		ok = wrote
		return wrote, nil
	})
	if err != nil {
		return
	}
	if !ok {
		s.activateOneDepth(ctx, cfg, &live, notices, pub, priv, now, depth+1)
		return
	}
	if err := s.writeSignedFromLedger(ctx, &live, finished, ann.Owner.Words, sessionraise.StatePending, priv); errors.Is(err, errRaiseAnnotationMoved) {
		s.activateOneDepth(ctx, cfg, &live, notices, pub, priv, now, depth+1)
		return
	} else if err != nil {
		s.Log.Error(err, "raise activation annotation", "session", live.Name, "id", entry.ID)
		return
	}
	signed := recordFrom(finished, ann.Owner.Words)
	if state == sessionraise.StateUnannounced {
		s.logRaise("raise unannounced", signed)
		return
	}
	s.logRaise("raise active", signed)
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

// writeSignedFromLedger signs the ledger row with words that have already
// been verified, and writes that annotation only while the stored annotation
// still has the same id and the state this decision was made from.
func (s *Server) writeSignedFromLedger(ctx context.Context, sess *v1alpha1.AgentSession, entry sessionraise.SessionEntry, words, fromState string, priv ed25519.PrivateKey) error {
	if sessionraise.WordsProblem(words) != "" || sessionraise.WordsHash(words) != entry.WordsHash {
		s.logOnce(entry.ID, "words do not verify", "raise words do not verify, so the annotation was not re-signed")
		return nil
	}
	signed, err := sessionraise.Sign(recordFrom(entry, words), priv)
	if err != nil {
		s.Log.Error(err, "raise activation sign", "session", sess.Name, "id", entry.ID)
		return err
	}
	raw, err := signed.Encode()
	if err != nil {
		return err
	}
	return s.writeRaiseAnnotationWhen(ctx, types.NamespacedName{Namespace: sess.Namespace, Name: sess.Name}, sess.UID, string(raw), func(cur *v1alpha1.AgentSession) bool {
		rec, err := sessionraise.Parse(cur.Annotations[v1alpha1.AnnotationRaise])
		return err == nil && rec.ID == entry.ID && rec.State == fromState
	})
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

// RaiseKeyOwner publishes the operator raise key on the leader. It retries
// Ensure until the key is published, then keeps waiting. A missing Secret
// is logged at most once an hour. POST refuses until Ensure succeeds.
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

// Start retries Ensure on a ticker, then waits until the manager stops.
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
