package apiserver

import (
	"context"
	"crypto/ed25519"
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

// activateRaises moves pending rows the keeper has announced. It runs only
// when the dispatch flag is on, so a flags-off operator honours nothing.
// The ledger is written before the annotation. A row that is no longer
// pending is left alone, so a take-back wins over a later Sent.
func (s *Server) activateRaises(ctx context.Context) {
	cfg, ok := s.raiseConfig()
	if !ok || !cfg.Dispatch || s.Live == nil {
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
	raw := sess.Annotations[v1alpha1.AnnotationRaise]
	if raw == "" {
		return
	}
	rec, err := sessionraise.Parse(raw)
	if err != nil {
		return
	}
	entry, err := s.raiseEntry(ctx, cfg, string(sess.UID))
	if err != nil || entry == nil || entry.ID != rec.ID {
		return
	}
	// Finish an annotation the ledger already moved, so a lost write still
	// comes to match the row that counts.
	if rec.State == sessionraise.StatePending && (entry.State == sessionraise.StateActive || entry.State == sessionraise.StateUnannounced) {
		s.rewriteActivated(ctx, cfg, sess, rec, *entry, priv)
		return
	}
	if entry.State != sessionraise.StatePending {
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
		got, wrote := led.Finish(string(sess.UID), rec.ID, state, sentAt, activeFrom, now)
		finished = got
		ok = wrote
		return wrote, nil
	})
	if err != nil || !ok {
		return
	}
	s.rewriteActivated(ctx, cfg, sess, rec, finished, priv)
}

func (s *Server) rewriteActivated(ctx context.Context, cfg RaiseConfig, sess *v1alpha1.AgentSession, rec sessionraise.Record, entry sessionraise.SessionEntry, priv ed25519.PrivateKey) {
	rec.State = entry.State
	rec.NoticeSentAt = entry.NoticeSentAt
	rec.ActiveFrom = entry.ActiveFrom
	rec.Sig = ""
	signed, err := sessionraise.Sign(rec, priv)
	if err != nil {
		s.Log.Error(err, "raise activation sign", "session", sess.Name)
		return
	}
	raw, err := signed.Encode()
	if err != nil {
		return
	}
	if err := s.writeRaiseAnnotation(ctx, types.NamespacedName{Namespace: sess.Namespace, Name: sess.Name}, sess.UID, string(raw)); err != nil {
		s.Log.Error(err, "raise activation annotation", "session", sess.Name)
	}
	_ = cfg
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

// RaiseKeyOwner publishes the operator raise key on the leader. A missing
// Secret is logged and the operator keeps running. POST then refuses.
type RaiseKeyOwner struct {
	Server *Server
}

// NeedLeaderElection is true: only the leader fills an empty key Secret.
func (k *RaiseKeyOwner) NeedLeaderElection() bool { return true }

// Start publishes the key once, then waits until the manager stops.
func (k *RaiseKeyOwner) Start(ctx context.Context) error {
	if k.Server != nil && k.Server.Raise != nil {
		cfg := k.Server.Raise.filled()
		if _, err := sessionraise.Ensure(ctx, k.Server.raiseStore(cfg), cfg.OperatorSecret, cfg.OperatorPublic, true); err != nil {
			k.Server.Log.Info("raise key was not published", "error", err.Error())
		}
	}
	<-ctx.Done()
	return nil
}
