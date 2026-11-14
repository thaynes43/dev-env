package apiserver

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/controller"
	"github.com/thaynes43/dev-env/internal/grantattest"
	"github.com/thaynes43/dev-env/internal/keeper"
	"github.com/thaynes43/dev-env/internal/projectcanary"
	"github.com/thaynes43/dev-env/internal/sessionraise"
	"github.com/thaynes43/dev-env/internal/taskbudget"
	"github.com/thaynes43/dev-env/internal/templates"
)

// RaiseConfig is DESIGN-066 on this operator. Both flags default off. With
// both off, POST is refused and depthExempt grants nothing. GET and DELETE
// stay on: a take-back is never switched off.
type RaiseConfig struct {
	Dispatch       bool
	Profiles       []string
	Namespace      string
	OperatorSecret string
	OperatorPublic string
	Ledger         string
	KeeperPublic   string
	KeeperLedger   string
}

func (c RaiseConfig) filled() RaiseConfig {
	if c.Namespace == "" {
		c.Namespace = sessionraise.ObjectNamespace
	}
	if c.OperatorSecret == "" {
		c.OperatorSecret = sessionraise.OperatorSecretName
	}
	if c.OperatorPublic == "" {
		c.OperatorPublic = sessionraise.OperatorPublicName
	}
	if c.Ledger == "" {
		c.Ledger = sessionraise.LedgerName
	}
	if c.KeeperPublic == "" {
		c.KeeperPublic = sessionraise.KeeperPublicName
	}
	if c.KeeperLedger == "" {
		c.KeeperLedger = sessionraise.NoticeLedgerName
	}
	return c
}

func (s *Server) raiseConfig() (RaiseConfig, bool) {
	if s == nil || s.Raise == nil {
		return RaiseConfig{}, false
	}
	return s.Raise.filled(), true
}

func (s *Server) raiseStore(cfg RaiseConfig) sessionraise.Kube {
	return sessionraise.Kube{
		Reader: s.Live, Writer: s.Client, Namespace: cfg.Namespace,
		Secrets: map[string]bool{cfg.OperatorSecret: true},
	}
}

const (
	raiseOff           = "This operator does not raise sessions."
	raiseNoKey         = "This operator has no raise key yet."
	raiseKeyUnreadable = "This operator cannot read its raise key."
	raiseKeyMismatch   = "This operator's raise key does not match its published key."
	raiseLedgerUnread  = "The operator cannot read its raise ledger, so it raises nothing now. Try again."
	raiseNeedsWords    = "A raise needs Tom's words: what he said, where and when."
	raiseWordsLong     = "Tom's words must fit in 300 characters so the notice shows them whole."
	raiseWordsControl  = "Tom's words must be plain text, with no control or format characters."
	raiseWordsTime     = "The time you give for Tom's words must be within the last 24 hours. Ask him again."
	raiseNoticeLong    = "Tom's words are too long for the phone notice once links are removed. Shorten them."
	raiseTooBig        = "This raise does not fit in its record. Shorten the words or the reference."
	raiseNeedsOne      = "A raise names a profile or the right to start sessions."
	raiseNeedsUID      = "A raise names the session UID it read. Read the session and send that UID."
	raiseOtherCaller   = "Only a superuser session, an always-on agent whose policy allows it, or Tom may raise a session."
	raisePolicy        = "This caller's policy does not allow raising sessions."
	raiseSelf          = "A session may not raise itself."
	// noticeBeforeRaise is the view reason when a verified Sent is earlier
	// than the raise. The row stays pending. The coordinator's ruling of
	// 2026-11-14 on the second slice (a) reviews.
	noticeBeforeRaise = "The notice time is before the raise. A take-back and a new raise are needed."
)

func raiseInvalid(sentence string) *apiError {
	return newError(http.StatusUnprocessableEntity, apiv1.CodeInvalid, "%s", sentence)
}

func raiseConflict(format string, args ...any) *apiError {
	return newError(http.StatusConflict, apiv1.CodeConflict, format, args...)
}

func raiseUnavailable(format string, args ...any) *apiError {
	return newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, format, args...)
}

func (s *Server) getRaise(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	if err := s.refuseRaiseCaller(ctx, c, r.PathValue("name")); err != nil {
		return 0, nil, err
	}
	sess, err := s.raiseTarget(ctx, r)
	if err != nil {
		return 0, nil, err
	}
	if err := s.authorizeRaise(ctx, c, sess, true); err != nil {
		return 0, nil, err
	}
	return http.StatusOK, s.raiseStatus(ctx, sess), nil
}

func (s *Server) postRaise(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	if err := s.refuseRaiseCaller(ctx, c, r.PathValue("name")); err != nil {
		return 0, nil, err
	}
	cfg, ok := s.raiseConfig()
	if !ok || (!cfg.Dispatch && len(cfg.Profiles) == 0) {
		return 0, nil, forbidden("%s", raiseOff)
	}
	var req apiv1.RaiseRequest
	if err := decodeRaiseBody(r, &req); err != nil {
		return 0, nil, err
	}
	if req.StartSessions && !cfg.Dispatch {
		return 0, nil, forbidden("%s", raiseOff)
	}
	sess, err := s.raiseTarget(ctx, r)
	if err != nil {
		return 0, nil, err
	}
	if err := s.authorizeRaise(ctx, c, sess, false); err != nil {
		return 0, nil, err
	}
	if err := s.expectSession(sess, req.ExpectUID); err != nil {
		return 0, nil, err
	}
	if err := s.eligibleTarget(ctx, sess, req.StartSessions); err != nil {
		return 0, nil, err
	}
	if err := s.checkRaiseWords(c, req.Owner); err != nil {
		return 0, nil, err
	}
	if err := s.checkRaiseProfile(ctx, cfg, req.Profile, req.StartSessions); err != nil {
		return 0, nil, err
	}
	priv, err := s.raiseKey(ctx, cfg)
	if err != nil {
		return 0, nil, err
	}
	pub := priv.Public().(ed25519.PublicKey)

	s.createMu.Lock()
	defer s.createMu.Unlock()
	fresh, err := s.raiseTarget(ctx, r)
	if err != nil {
		return 0, nil, err
	}
	if fresh.UID != sess.UID {
		return 0, nil, raiseConflict("Session %s was replaced: its UID is not the one you read. Read it again.", fresh.Name)
	}
	if err := s.eligibleTarget(ctx, fresh, req.StartSessions); err != nil {
		return 0, nil, err
	}
	now := s.now().UTC().Truncate(time.Second)
	performedAt := now.Format(time.RFC3339)
	hash := sessionraise.WordsHash(req.Owner.Words)
	saidAt, _ := time.Parse(time.RFC3339, req.Owner.SaidAt)
	in := sessionraise.AdmitInput{
		Name: fresh.Name, UID: string(fresh.UID),
		ID:      sessionraise.RecordID(string(fresh.UID), performedAt, hash),
		Profile: req.Profile, FromProfile: s.podProfile(ctx, fresh),
		StartSessions: req.StartSessions, Depth: fresh.Labels[v1alpha1.LabelDepth],
		WordsHash: hash, PerformedBy: c.String(), PerformedAt: performedAt,
		Where: req.Owner.Where, SaidAt: saidAt.UTC().Truncate(time.Second).Format(time.RFC3339),
		Ref: req.Owner.Ref, OwnerSession: ownerSession(c),
	}
	// Before the word is spent, check a placeholder title and a full time.
	// Redaction can lengthen the real title; keeper also checks the final
	// message and falls back to omitting that title before any send.
	fits, err := sessionraise.NoticeFits(fresh.Name, c.String(), in.Where, in.SaidAt, req.Owner.Words, keeper.NoticeRedact)
	if err != nil || !fits {
		return 0, nil, raiseInvalid(raiseNoticeLong)
	}
	var entry sessionraise.SessionEntry
	var same bool
	writeLedger := false
	err = s.commitLedger(ctx, cfg, func(led *sessionraise.Ledger) (bool, error) {
		result, err := led.Admit(now, in, s.liveUIDs(ctx))
		if err != nil {
			return false, err
		}
		entry, same = result.Entry, result.Same
		if same {
			return false, nil
		}
		rec := recordFrom(entry, req.Owner.Words)
		signed, err := sessionraise.Sign(rec, priv)
		if err != nil {
			return false, err
		}
		if big, err := signed.TooBig(); err != nil || big {
			return false, sessionraise.ErrTooBig
		}
		writeLedger = true
		return true, nil
	})
	if err != nil {
		return 0, nil, s.raiseLedgerErr(err, c, fresh.Name)
	}
	if same && s.annotationCounts(fresh, pub, &entry) {
		v := s.withUsageRollup(ctx, fresh, c, false)
		return http.StatusOK, v, nil
	}
	// A same-body request is not permission to promote an unsigned ledger
	// state or replay over an authenticated terminal annotation. Active
	// repair belongs to the activator's verified transition/evidence path.
	// Only a missing pending annotation can be recovered from this fresh,
	// authorized request, whose live grant fields must still match the row.
	if same && !pendingRequestRepair(fresh, entry, in, pub) {
		s.logOnce(entry.ID, "request repair lacks authenticated grant", "raise request did not re-sign an unsigned state or changed grant")
		// A pending row with no annotation for this id needs request
		// recovery. Refusing that recovery must not report a successful
		// retry; the word is already spent and the activator cannot supply
		// the missing grant. Existing records still belong to its repair path.
		if stored, err := sessionraise.Parse(fresh.Annotations[v1alpha1.AnnotationRaise]); entry.State == sessionraise.StatePending && (err != nil || stored.ID != entry.ID) {
			return 0, nil, raiseConflict("Session %s has a raise that was recorded but whose record could not be written, and this request does not match it. Take it back and raise again with a new word from Tom: agent-run raise %s --take-back", fresh.Name, fresh.Name)
		}
		return http.StatusOK, s.withUsageRollup(ctx, fresh, c, false), nil
	}
	// A retry of the same body uses the row's id and time, so the word is
	// not spent again, and rewrites a missing annotation.
	if same {
		in.ID = entry.ID
		in.PerformedAt = entry.PerformedAt
		in.Profile = entry.Profile
		in.FromProfile = entry.FromProfile
		in.StartSessions = entry.StartSessions
		in.Depth = entry.Depth
		in.Where = entry.Where
		in.SaidAt = entry.SaidAt
		in.Ref = entry.Ref
		in.OwnerSession = entry.OwnerSession
		in.PerformedBy = entry.PerformedBy
	}
	var rec sessionraise.Record
	if same {
		rec = recordFrom(entry, req.Owner.Words)
	} else {
		rec = recordFrom(sessionraise.SessionEntry{
			ID: in.ID, UID: in.UID, State: sessionraise.StatePending,
			Profile: in.Profile, FromProfile: in.FromProfile, StartSessions: in.StartSessions,
			Depth: in.Depth, WordsHash: hash, PerformedBy: in.PerformedBy, PerformedAt: in.PerformedAt,
			Where: in.Where, SaidAt: in.SaidAt, Ref: in.Ref, OwnerSession: in.OwnerSession,
		}, req.Owner.Words)
	}
	signed, err := sessionraise.Sign(rec, priv)
	if err != nil {
		return 0, nil, raiseUnavailable("%s", raiseKeyUnreadable)
	}
	if big, err := signed.TooBig(); err != nil || big {
		return 0, nil, raiseInvalid(raiseTooBig)
	}
	if !same && !writeLedger {
		return 0, nil, raiseUnavailable("%s", raiseLedgerUnread)
	}
	raw, err := signed.Encode()
	if err != nil {
		return 0, nil, raiseUnavailable("%s", raiseLedgerUnread)
	}
	// The raise is logged once the ledger write has succeeded. A failed
	// annotation write does not wait for a retry before the line exists.
	if !same && writeLedger {
		s.logRaiseState(raisedAudit(fresh.Name, entry.Profile), signed)
	}
	annotationWritten, err := s.writeRaiseAnnotation(ctx, client.ObjectKeyFromObject(fresh), fresh.UID, string(raw), pub)
	if err != nil {
		return 0, nil, raiseUnavailable("The raise was recorded but the session annotation could not be written. Send the same request again.")
	}
	// Recovery can first observe an applied ledger write whose response was
	// lost. A retry of an already-counting annotation makes no audit line.
	if same && annotationWritten && entry.State == sessionraise.StatePending {
		s.logRaiseState(raisedAudit(fresh.Name, entry.Profile), signed)
	}
	updated, err := s.raiseTarget(ctx, r)
	if err != nil {
		updated = fresh
	}
	status := http.StatusAccepted
	if same {
		status = http.StatusOK
	}
	return status, s.withUsageRollup(ctx, updated, c, false), nil
}

func (s *Server) deleteRaise(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	if err := s.refuseRaiseCaller(ctx, c, r.PathValue("name")); err != nil {
		return 0, nil, err
	}
	cfg, ok := s.raiseConfig()
	if !ok {
		return 0, nil, forbidden("%s", raiseOff)
	}
	var req apiv1.TakeBackRequest
	if err := decodeRaiseBody(r, &req); err != nil {
		return 0, nil, err
	}
	sess, err := s.raiseTarget(ctx, r)
	if err != nil {
		return 0, nil, err
	}
	if err := s.authorizeRaise(ctx, c, sess, true); err != nil {
		return 0, nil, err
	}
	if err := s.expectSession(sess, req.ExpectUID); err != nil {
		return 0, nil, err
	}
	if sess.Annotations[v1alpha1.AnnotationRaise] == "" {
		led, lerr := s.readLedger(ctx, cfg)
		if lerr != nil {
			return 0, nil, raiseUnavailable("%s", raiseLedgerUnread)
		}
		if led.Find(string(sess.UID)) == nil {
			return 0, nil, notFound("Session %s has no raise to take back.", sess.Name)
		}
	}
	s.createMu.Lock()
	defer s.createMu.Unlock()
	fresh, err := s.raiseTarget(ctx, r)
	if err != nil {
		return 0, nil, err
	}
	if fresh.UID != sess.UID {
		return 0, nil, raiseConflict("Session %s was replaced: its UID is not the one you read. Read it again.", fresh.Name)
	}
	now := s.now().UTC().Truncate(time.Second)
	at := now.Format(time.RFC3339)
	var entry sessionraise.SessionEntry
	var wrote bool
	err = s.commitLedger(ctx, cfg, func(led *sessionraise.Ledger) (bool, error) {
		// wrote belongs to this attempt. A later attempt that finds the
		// row already taken back did not commit it. Annotation repair below
		// may still log the transition it first observes.
		wrote = false
		cur := led.Find(string(fresh.UID))
		if cur == nil {
			return false, sessionraise.ErrNoEntry
		}
		if cur.State == sessionraise.StateTakenBack {
			entry = *cur
			return false, nil
		}
		got, err := led.TakeBack(string(fresh.UID), c.String(), at, now)
		if err != nil {
			return false, err
		}
		entry = got
		wrote = true
		return true, nil
	})
	if errors.Is(err, sessionraise.ErrNoEntry) {
		return 0, nil, notFound("Session %s has no raise to take back.", fresh.Name)
	}
	if err != nil {
		return 0, nil, s.raiseLedgerErr(err, c, fresh.Name)
	}
	// The ledger write is what stops the record counting. The grant release
	// and the suspend run after it, whatever the annotation write does.
	// The audit line follows the ledger write and carries the verified
	// words when the annotation had them, even if that write then fails.
	var annErr error
	words := ""
	priv, keyErr := s.raiseKey(ctx, cfg)
	if keyErr == nil {
		pub := priv.Public().(ed25519.PublicKey)
		if prev, ok := verifiedRaiseWords(fresh, pub, entry.ID); ok && sessionraise.WordsHash(prev.Owner.Words) == entry.WordsHash {
			words = prev.Owner.Words
		}
	}
	if wrote {
		s.logRaiseState("took back a raise of "+fresh.Name, recordFrom(entry, words))
	}
	if keyErr != nil {
		s.Log.Info("The take-back stands. The session annotation could not be re-signed.", "session", fresh.Name, "error", keyErr.Error())
	} else if !s.annotationTakenBack(fresh, priv.Public().(ed25519.PublicKey), &entry) {
		rec, verified := takenBackRecord(fresh, entry, priv.Public().(ed25519.PublicKey))
		if !verified {
			s.logOnce(entry.ID, "take-back source does not verify", "take-back stands but no changed or unverified grant was re-signed")
		} else {
			signed, err := sessionraise.Sign(rec, priv)
			if err != nil {
				annErr = err
			} else if raw, err := signed.Encode(); err != nil {
				annErr = err
			} else {
				annotationWritten, err := s.writeRaiseAnnotation(ctx, client.ObjectKeyFromObject(fresh), fresh.UID, string(raw), priv.Public().(ed25519.PublicKey))
				annErr = err
				if annotationWritten {
					s.logRaiseState("took back a raise of "+fresh.Name, signed)
				}
			}
		}
	}
	s.releaseRaisedGrants(ctx, fresh.UID, entry.Profile)
	if err := s.suspendIfRaisedAccount(ctx, fresh, entry.Profile, c.String()); err != nil {
		s.Log.Error(err, "raise take-back suspend", "session", fresh.Name)
	}
	if annErr != nil {
		return 0, nil, raiseUnavailable("The take-back is recorded but the session annotation could not be written. Send the take-back again.")
	}
	updated, err := s.raiseTarget(ctx, r)
	if err != nil {
		updated = fresh
	}
	return http.StatusOK, s.withUsageRollup(ctx, updated, c, false), nil
}

func recordFrom(entry sessionraise.SessionEntry, words string) sessionraise.Record {
	return sessionraise.Record{
		V: sessionraise.Version, ID: entry.ID, SessionUID: entry.UID, State: entry.State,
		Profile: entry.Profile, FromProfile: entry.FromProfile, StartSessions: entry.StartSessions,
		Depth: entry.Depth,
		Owner: sessionraise.Owner{
			Words: words, WordsHash: entry.WordsHash, Where: entry.Where,
			Session: entry.OwnerSession, SaidAt: entry.SaidAt, Ref: entry.Ref,
		},
		PerformedBy: entry.PerformedBy, PerformedAt: entry.PerformedAt,
		NoticeSentAt: entry.NoticeSentAt, ActiveFrom: entry.ActiveFrom,
		TakenBackBy: entry.TakenBackBy, TakenBackAt: entry.TakenBackAt,
	}
}

// pendingRequestRepair separates recovery of the first POST's missing
// annotation from repair of a stored record. The authorized request and live
// target supply the grant; only the persisted id/time survive a retry.
func pendingRequestRepair(sess *v1alpha1.AgentSession, entry sessionraise.SessionEntry, in sessionraise.AdmitInput, pub ed25519.PublicKey) bool {
	if entry.State != sessionraise.StatePending || entry.NoticeSentAt != "" || entry.ActiveFrom != "" || entry.TakenBackBy != "" || entry.TakenBackAt != "" {
		return false
	}
	if !pendingAnnotationMayBeWritten(sess, recordFrom(entry, ""), pub) {
		return false
	}
	if _, err := time.Parse(time.RFC3339, entry.PerformedAt); err != nil {
		return false
	}
	in.ID = sessionraise.RecordID(in.UID, entry.PerformedAt, in.WordsHash)
	in.PerformedAt = entry.PerformedAt
	want := sessionraise.SessionEntry{
		ID: in.ID, UID: in.UID, Profile: in.Profile, FromProfile: in.FromProfile,
		StartSessions: in.StartSessions, Depth: in.Depth, WordsHash: in.WordsHash,
		PerformedBy: in.PerformedBy, PerformedAt: in.PerformedAt, Where: in.Where,
		SaidAt: in.SaidAt, Ref: in.Ref, OwnerSession: in.OwnerSession,
	}
	return raiseGrantMatches(recordFrom(want, ""), entry)
}

// pendingAnnotationMayBeWritten is checked on every annotation attempt's
// live snapshot, not just the earlier request read. The same id's signed
// state is never replayed by POST. A different id can be the previous raise
// left behind by the new POST's failed annotation write.
func pendingAnnotationMayBeWritten(sess *v1alpha1.AgentSession, record sessionraise.Record, pub ed25519.PublicKey) bool {
	if sess.Annotations[v1alpha1.AnnotationRaise] == "" {
		return true
	}
	stored, err := sessionraise.Parse(sess.Annotations[v1alpha1.AnnotationRaise])
	if err != nil || stored.ID == record.ID || stored.SessionUID != record.SessionUID || !sessionraise.Verify(stored, pub) {
		return false
	}
	// Different ids alone do not establish order: an old row and its Sent
	// can be replayed. Authenticate the previous time and require a newer
	// grant. No old grant field supplies authority for the new request.
	// Verify the signature directly: an earlier partial POST's take-back
	// can legitimately have no words while retaining its signed time.
	previous, previousErr := time.Parse(time.RFC3339, stored.PerformedAt)
	next, nextErr := time.Parse(time.RFC3339, record.PerformedAt)
	return previousErr == nil && nextErr == nil && next.After(previous)
}

func takenBackRecord(sess *v1alpha1.AgentSession, entry sessionraise.SessionEntry, pub ed25519.PublicKey) (sessionraise.Record, bool) {
	prev, ok := verifiedRaiseWords(sess, pub, entry.ID)
	if !ok || !raiseGrantMatches(prev, entry) {
		return sessionraise.Record{}, false
	}
	// Revocation must not turn unsigned ledger fields into authenticated
	// grant metadata that request recovery could later use as evidence.
	prev.State = sessionraise.StateTakenBack
	prev.TakenBackBy, prev.TakenBackAt = entry.TakenBackBy, entry.TakenBackAt
	return prev, true
}

// verifiedRaiseWords is an annotation that parses as its own canonical
// bytes, verifies under the operator key, names id, and whose words pass
// the shape check and match their own hash.
func verifiedRaiseWords(sess *v1alpha1.AgentSession, pub ed25519.PublicKey, id string) (sessionraise.Record, bool) {
	if sess == nil || id == "" || len(pub) != ed25519.PublicKeySize {
		return sessionraise.Record{}, false
	}
	rec, err := sessionraise.Parse(sess.Annotations[v1alpha1.AnnotationRaise])
	if err != nil || rec.ID != id || !sessionraise.Verify(rec, pub) {
		return sessionraise.Record{}, false
	}
	if sessionraise.WordsProblem(rec.Owner.Words) != "" || sessionraise.WordsHash(rec.Owner.Words) != rec.Owner.WordsHash {
		return sessionraise.Record{}, false
	}
	return rec, true
}

// refuseRaiseCaller answers from the caller kind before the target session
// is read. A caller who may not see a session gets the same answer whether
// the name exists or not. A human, the target itself, and a session that
// was created as the workbench go on to the read.
func (s *Server) refuseRaiseCaller(ctx context.Context, c *caller, name string) error {
	if c == nil {
		return forbidden("%s", raiseOtherCaller)
	}
	if c.kind == kindPolicy {
		return forbidden("%s", raisePolicy)
	}
	switch c.kind {
	case kindHuman:
		return nil
	case kindSession:
		if c.session != nil && c.session.Name == name {
			return nil
		}
		if c.session != nil {
			if byCreation, _ := s.workbenchByCreation(ctx, c.session); byCreation {
				return nil
			}
			// This reads the caller's own record, not the target, so a missing
			// name gets the same answer as a name that exists.
			if raised, err := s.raisedToWorkbench(ctx, c.session); err != nil {
				return err
			} else if raised {
				return forbidden("%s was raised to workbench, not started on it, so it may not raise others.", c.session.Name)
			}
		}
		return forbidden("%s", raiseOtherCaller)
	default:
		return forbidden("%s", raiseOtherCaller)
	}
}

func (s *Server) annotationCounts(sess *v1alpha1.AgentSession, pub ed25519.PublicKey, entry *sessionraise.SessionEntry) bool {
	rec, err := sessionraise.Parse(sess.Annotations[v1alpha1.AnnotationRaise])
	if err != nil {
		return false
	}
	if reason := sessionraise.Counts(rec, string(sess.UID), pub, entry); reason != "" {
		s.logIgnored(rec.ID, reason)
		return false
	}
	return true
}

// annotationTakenBack reports whether the stored annotation is already the
// re-signed taken-back record. It does not log: nothing is being honoured.
// "raise record ignored" is only for a record a reader is asked to honour.
func (s *Server) annotationTakenBack(sess *v1alpha1.AgentSession, pub ed25519.PublicKey, entry *sessionraise.SessionEntry) bool {
	if sess == nil || entry == nil || entry.State != sessionraise.StateTakenBack {
		return false
	}
	rec, err := sessionraise.Parse(sess.Annotations[v1alpha1.AnnotationRaise])
	if err != nil {
		return false
	}
	return sessionraise.Counts(rec, string(sess.UID), pub, entry) == ""
}

func (s *Server) raiseTarget(ctx context.Context, r *http.Request) (*v1alpha1.AgentSession, error) {
	key, err := s.sessionKey(r)
	if err != nil {
		return nil, err
	}
	var sess v1alpha1.AgentSession
	if s.Live == nil {
		return nil, raiseUnavailable("%s", raiseLedgerUnread)
	}
	if err := s.Live.Get(ctx, key, &sess); err != nil {
		return nil, fromKubeError(err, "session "+key.Name)
	}
	return &sess, nil
}

func (s *Server) expectSession(sess *v1alpha1.AgentSession, uid string) error {
	if uid == "" {
		return raiseInvalid(raiseNeedsUID)
	}
	if uid != string(sess.UID) {
		return raiseConflict("Session %s was replaced: its UID is not the one you read. Read it again.", sess.Name)
	}
	return nil
}

// authorizeRaise is who may perform a raise, and, when takeBack is set, also
// the target itself. A record is not consulted except for the "raised to
// workbench" refusal, which requires one that counts.
func (s *Server) authorizeRaise(ctx context.Context, c *caller, target *v1alpha1.AgentSession, takeBack bool) error {
	if c == nil || target == nil {
		return forbidden("%s", raiseOtherCaller)
	}
	if c.kind == kindPolicy {
		return forbidden("%s", raisePolicy)
	}
	self := c.kind == kindSession && c.session != nil && (c.session.UID == target.UID || c.session.Name == target.Name)
	if self && !takeBack {
		return forbidden("%s", raiseSelf)
	}
	if self && takeBack {
		return nil
	}
	if c.kind == kindHuman {
		return nil
	}
	if c.kind != kindSession || c.session == nil {
		return forbidden("%s", raiseOtherCaller)
	}
	byCreation, err := s.workbenchByCreation(ctx, c.session)
	if err != nil {
		byCreation = false
	}
	if !byCreation {
		if raised, err := s.raisedToWorkbench(ctx, c.session); err != nil {
			return err
		} else if raised {
			return forbidden("%s was raised to workbench, not started on it, so it may not raise others.", c.session.Name)
		}
		return forbidden("%s", raiseOtherCaller)
	}
	switch s.sessionAncestry(ctx, c.session, target.Name, target.UID) {
	case ancestryFound:
		return forbidden("%s works under %s, so it may not raise it.", c.session.Name, target.Name)
	case ancestryUnverified:
		return forbidden("The parent chain of %s cannot be read, so the operator cannot tell that it is not under %s. Try again.", c.session.Name, target.Name)
	case ancestryTooDeep:
		return forbidden("The parent chain of %s is too deep to read, so the operator cannot tell that it is not under %s.", c.session.Name, target.Name)
	}
	return nil
}

func (s *Server) workbenchByCreation(ctx context.Context, sess *v1alpha1.AgentSession) (bool, error) {
	if s.Templates == nil || sess == nil {
		return false, nil
	}
	t, err := s.Templates(ctx)
	if err != nil || t == nil {
		return false, nil
	}
	_, profile, err := t.Profile(sess.Spec.Profile)
	if err != nil {
		return false, nil
	}
	return profile.ServiceAccount == templates.WorkbenchServiceAccount, nil
}

func (s *Server) raisedToWorkbench(ctx context.Context, sess *v1alpha1.AgentSession) (bool, error) {
	raw := sess.Annotations[v1alpha1.AnnotationRaise]
	if raw == "" {
		return false, nil
	}
	cfg, ok := s.raiseConfig()
	if !ok {
		return false, nil
	}
	rec, err := sessionraise.Parse(raw)
	if err != nil {
		return false, nil
	}
	pub, err := sessionraise.ReadPublic(ctx, s.raiseStore(cfg), cfg.OperatorPublic)
	if err != nil {
		return false, raiseUnavailable("%s", raiseLedgerUnread)
	}
	entry, err := s.raiseEntry(ctx, cfg, string(sess.UID))
	if err != nil {
		return false, raiseUnavailable("%s", raiseLedgerUnread)
	}
	if sessionraise.Counts(rec, string(sess.UID), pub, entry) != "" || rec.State != sessionraise.StateActive || rec.Profile == "" {
		return false, nil
	}
	if s.Templates == nil {
		return false, raiseUnavailable("The templates cannot be read, so the operator cannot tell what profile %s runs as.", rec.Profile)
	}
	t, err := s.Templates(ctx)
	if err != nil || t == nil {
		return false, raiseUnavailable("The templates cannot be read, so the operator cannot tell what profile %s runs as.", rec.Profile)
	}
	_, profile, err := t.Profile(rec.Profile)
	if err != nil {
		return false, nil
	}
	return profile.ServiceAccount == templates.WorkbenchServiceAccount, nil
}

func (s *Server) eligibleTarget(ctx context.Context, sess *v1alpha1.AgentSession, startSessions bool) error {
	if sess.Spec.Agent != v1alpha1.AgentClaude {
		return raiseConflict("Session %s is not a Claude session; only a Claude session is raised.", sess.Name)
	}
	if sess.Spec.Mode != v1alpha1.ModeRemote && sess.Spec.Mode != v1alpha1.ModeLocal {
		return raiseConflict("Session %s is not a remote or local session; only those are raised.", sess.Name)
	}
	if sess.Spec.Caller != "" {
		return raiseConflict("Session %s is a summoned session; summoned sessions are never raised.", sess.Name)
	}
	if sess.Spec.ProjectCanary != nil {
		return raiseConflict("Session %s is a project canary; a canary is never raised.", sess.Name)
	}
	if projectcanary.IsPilot(sess) {
		return raiseConflict("Session %s is a private pilot; a pilot is never raised.", sess.Name)
	}
	if sess.Annotations[taskbudget.UIDAnnotation] != "" {
		return raiseConflict("Session %s is bound to a task budget; a budget-bound session is never raised.", sess.Name)
	}
	if !sess.DeletionTimestamp.IsZero() {
		return raiseConflict("Session %s is being reaped; a session being reaped is never raised.", sess.Name)
	}
	if sess.Status.Phase == v1alpha1.PhaseArchived || sess.Status.ArchivedAt != nil {
		return raiseConflict("Session %s is archived; an archived session is never raised.", sess.Name)
	}
	named, err := s.namedCoordinatorSession(ctx, sess)
	if err != nil {
		return err
	}
	if named {
		return raiseConflict("Session %s runs as a named coordinator; a named coordinator is never raised.", sess.Name)
	}
	child, err := s.coordinatorChild(ctx, sess)
	if err != nil {
		return err
	}
	if child {
		return raiseConflict("Session %s is a coordinator host's child; that child is never raised.", sess.Name)
	}
	if startSessions {
		switch sess.Labels[v1alpha1.LabelDepth] {
		case "2":
		case "0", "1":
			return raiseInvalid(fmt.Sprintf("Session %s sits at depth %s and may already start sessions.", sess.Name, sess.Labels[v1alpha1.LabelDepth]))
		default:
			return raiseInvalid("Only a session at depth 2 may be raised to start sessions.")
		}
	}
	return nil
}

func (s *Server) namedCoordinatorSession(ctx context.Context, sess *v1alpha1.AgentSession) (bool, error) {
	if templates.IsCoordinatorServiceAccount(sess.Labels[templates.CoordinatorIdentityLabel]) {
		return true, nil
	}
	if s.Templates == nil {
		return false, raiseUnavailable("The templates cannot be read, so the operator cannot tell whether %s is a named coordinator.", sess.Name)
	}
	t, err := s.Templates(ctx)
	if err != nil || t == nil {
		return false, raiseUnavailable("The templates cannot be read, so the operator cannot tell whether %s is a named coordinator.", sess.Name)
	}
	_, profile, err := t.Profile(sess.Spec.Profile)
	if err != nil {
		return false, raiseUnavailable("The templates cannot be read, so the operator cannot tell whether %s is a named coordinator.", sess.Name)
	}
	return templates.IsCoordinatorServiceAccount(profile.ServiceAccount), nil
}

func (s *Server) coordinatorChild(ctx context.Context, sess *v1alpha1.AgentSession) (bool, error) {
	for _, host := range s.Policy.Coordinators {
		if host.ServiceAccount != "" && sess.Spec.Parent == host.ServiceAccount {
			return true, nil
		}
	}
	if s.Live == nil {
		return false, raiseUnavailable("The operator cannot read the session pod, so it cannot tell whether %s is a coordinator host's child.", sess.Name)
	}
	var pod corev1.Pod
	err := s.Live.Get(ctx, types.NamespacedName{Namespace: sess.Namespace, Name: sess.Name}, &pod)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, raiseUnavailable("The operator cannot read the session pod, so it cannot tell whether %s is a coordinator host's child.", sess.Name)
	}
	return pod.Annotations[CoordinatorHostAnnotation] != "", nil
}

func (s *Server) checkRaiseWords(c *caller, owner apiv1.RaiseOwner) error {
	if owner.Words == "" || owner.Where == "" || owner.SaidAt == "" {
		return raiseInvalid(raiseNeedsWords)
	}
	switch sessionraise.WordsProblem(owner.Words) {
	case "empty":
		return raiseInvalid(raiseNeedsWords)
	case "long":
		return raiseInvalid(raiseWordsLong)
	case "control":
		return raiseInvalid(raiseWordsControl)
	}
	if shape := sessionraise.SecretShape(owner.Words); shape != "" {
		return raiseInvalid(fmt.Sprintf("Tom's words look like they hold %s. Leave it out and send his words again.", shape))
	}
	allowed := sessionraise.WherePhone
	kind := "session"
	if c.kind == kindHuman {
		allowed = sessionraise.WhereOwn
		kind = "human"
	}
	if owner.Where != allowed {
		return raiseInvalid(fmt.Sprintf("A %s records only words Tom gave it directly (%s); have the agent he told perform the raise.", kind, allowed))
	}
	said, err := time.Parse(time.RFC3339, owner.SaidAt)
	if err != nil {
		return raiseInvalid(raiseWordsTime)
	}
	now := s.now()
	if said.After(now.Add(5*time.Minute)) || said.Before(now.Add(-24*time.Hour)) {
		return raiseInvalid(raiseWordsTime)
	}
	if owner.Ref != "" && !grantattest.ValidRef(owner.Ref) {
		return raiseInvalid("The reference must be 1 to 128 characters of letters, digits and . _ : / # -.")
	}
	return nil
}

func (s *Server) checkRaiseProfile(ctx context.Context, cfg RaiseConfig, profile string, start bool) error {
	if profile == "" && !start {
		return raiseInvalid(raiseNeedsOne)
	}
	if profile == "" {
		return nil
	}
	shown := profile
	if len(shown) > 63 || strings.ContainsAny(shown, "\r\n") {
		shown = "that name"
	}
	if len(validation.IsDNS1123Label(profile)) != 0 {
		return raiseInvalid(fmt.Sprintf("This caller may not raise sessions to profile %s on this operator.", shown))
	}
	if s.Templates == nil {
		return raiseUnavailable("The templates cannot be read, so the operator cannot tell what profile %s runs as.", shown)
	}
	t, err := s.Templates(ctx)
	if err != nil || t == nil {
		return raiseUnavailable("The templates cannot be read, so the operator cannot tell what profile %s runs as.", shown)
	}
	_, parsed, err := t.Profile(profile)
	if err == nil && templates.IsCoordinatorServiceAccount(parsed.ServiceAccount) {
		return raiseInvalid(fmt.Sprintf("Profile %s runs as a named coordinator identity; a raise never gives one.", shown))
	}
	if err != nil || !slices.Contains(cfg.Profiles, profile) {
		return raiseInvalid(fmt.Sprintf("This caller may not raise sessions to profile %s on this operator.", shown))
	}
	return nil
}

func ownerSession(c *caller) string {
	if c != nil && c.kind == kindSession && c.session != nil {
		return c.session.Name
	}
	return ""
}

func (s *Server) raiseKey(ctx context.Context, cfg RaiseConfig) (ed25519.PrivateKey, error) {
	priv, err := sessionraise.Load(ctx, s.raiseStore(cfg), cfg.OperatorSecret, cfg.OperatorPublic)
	if err == nil {
		return priv, nil
	}
	switch {
	case errors.Is(err, sessionraise.ErrNoKey):
		return nil, raiseUnavailable("%s", raiseNoKey)
	case errors.Is(err, sessionraise.ErrMismatch):
		return nil, raiseUnavailable("%s", raiseKeyMismatch)
	default:
		return nil, raiseUnavailable("%s", raiseKeyUnreadable)
	}
}

func (s *Server) raiseLedgerErr(err error, c *caller, name string) error {
	var rate *sessionraise.RateError
	if errors.As(err, &rate) {
		if rate.Target {
			secs := int((rate.Wait + time.Second - 1) / time.Second)
			if secs < 1 {
				secs = 1
			}
			return tooManyRequests("Session %s's raise changed less than a minute ago. Try again in %d seconds.", name, secs)
		}
		return tooManyRequests("%s has made three raises in the last hour. Try again after %s.", c.String(), rate.Until.UTC().Format(time.RFC3339))
	}
	var full *sessionraise.FullError
	if errors.As(err, &full) {
		if full.Bytes {
			return raiseUnavailable("This raise would make the operator's raise ledger too big. Take a raise back before recording another.")
		}
		if full.Sessions {
			return raiseUnavailable("The operator's raise ledger holds %d sessions, so it records no new raise.", sessionraise.MaxSessions)
		}
		return raiseUnavailable("The operator's raise ledger is full. Raises resume as old entries expire on %s.", full.Resume.UTC().Format("2006-01-02"))
	}
	var still *sessionraise.StillRaisedError
	if errors.As(err, &still) {
		return raiseConflict("Session %s already has a raise that is %s. Take it back first: agent-run raise %s --take-back", name, still.State, name)
	}
	if errors.Is(err, sessionraise.ErrUsedWords) {
		return raiseConflict("%s already used these words for a raise. A new raise needs a new word from Tom.", c.String())
	}
	if errors.Is(err, sessionraise.ErrTooBig) {
		return raiseInvalid(raiseTooBig)
	}
	if errors.Is(err, sessionraise.ErrUnreadable) || errors.Is(err, sessionraise.ErrConflict) {
		return raiseUnavailable("%s", raiseLedgerUnread)
	}
	return raiseUnavailable("%s", raiseLedgerUnread)
}

func (s *Server) readLedger(ctx context.Context, cfg RaiseConfig) (*sessionraise.Ledger, error) {
	obj, err := s.raiseStore(cfg).Get(ctx, cfg.Ledger)
	if err != nil {
		return nil, err
	}
	return sessionraise.Unmarshal(obj.Data[sessionraise.LedgerField])
}

func (s *Server) raiseEntry(ctx context.Context, cfg RaiseConfig, uid string) (*sessionraise.SessionEntry, error) {
	led, err := s.readLedger(ctx, cfg)
	if err != nil {
		return nil, err
	}
	cur := led.Find(uid)
	if cur == nil {
		return nil, nil
	}
	copy := *cur
	return &copy, nil
}

// commitLedger reads the ledger uncached and writes it back with its
// resourceVersion. write false leaves it unchanged. A conflict retries.
func (s *Server) commitLedger(ctx context.Context, cfg RaiseConfig, mut func(*sessionraise.Ledger) (bool, error)) error {
	store := s.raiseStore(cfg)
	var last error
	for range 5 {
		obj, err := store.Get(ctx, cfg.Ledger)
		if err != nil {
			return err
		}
		led, err := sessionraise.Unmarshal(obj.Data[sessionraise.LedgerField])
		if err != nil {
			return err
		}
		write, err := mut(led)
		if err != nil {
			return err
		}
		if !write {
			return nil
		}
		raw, err := led.Marshal()
		if err != nil {
			return err
		}
		last = store.Update(ctx, cfg.Ledger, map[string][]byte{sessionraise.LedgerField: raw}, obj.RV)
		if errors.Is(last, sessionraise.ErrConflict) {
			continue
		}
		return last
	}
	if last == nil {
		last = sessionraise.ErrConflict
	}
	return last
}

func (s *Server) liveUIDs(ctx context.Context) map[string]bool {
	if s.Live == nil {
		return nil
	}
	var list v1alpha1.AgentSessionList
	if err := s.Live.List(ctx, &list, client.InNamespace(s.Policy.SessionNamespace)); err != nil {
		return nil
	}
	out := make(map[string]bool, len(list.Items))
	for i := range list.Items {
		out[string(list.Items[i].UID)] = true
	}
	return out
}

var errRaiseAnnotationMoved = errors.New("raise annotation moved")

func (s *Server) writeRaiseAnnotation(ctx context.Context, key types.NamespacedName, uid types.UID, raw string, pub ed25519.PublicKey) (bool, error) {
	cfg, ok := s.raiseConfig()
	if !ok {
		return false, sessionraise.ErrUnreadable
	}
	record, err := sessionraise.Parse(raw)
	if err != nil {
		return false, err
	}
	var last error
	for range 5 {
		var sess v1alpha1.AgentSession
		if err := s.Live.Get(ctx, key, &sess); err != nil {
			return false, err
		}
		if sess.UID != uid {
			return false, raiseConflict("Session %s was replaced: its UID is not the one you read. Read it again.", key.Name)
		}
		// Read the live session first, then its row, on every attempt.
		// Another raise supersedes this request's annotation repair. Its
		// response still reports this request's completed ledger outcome.
		entry, err := s.raiseEntry(ctx, cfg, string(uid))
		if err != nil {
			return false, err
		}
		if entry == nil || entry.ID != record.ID || entry.State != record.State {
			return false, nil
		}
		if sess.Annotations[v1alpha1.AnnotationRaise] == raw {
			return false, nil
		}
		if record.State == sessionraise.StatePending && (!raiseGrantMatches(record, *entry) || !pendingAnnotationMayBeWritten(&sess, record, pub)) {
			s.logOnce(record.ID, "request snapshot moved", "raise request did not overwrite a changed grant or stored raise")
			return false, nil
		}
		if sess.Annotations == nil {
			sess.Annotations = map[string]string{}
		}
		sess.Annotations[v1alpha1.AnnotationRaise] = raw
		last = s.Client.Update(ctx, &sess)
		if apierrors.IsConflict(last) {
			continue
		}
		return last == nil, last
	}
	return false, last
}

// writeRaiseAnnotationWhen uses exactly the live snapshot the decision was
// made from. It performs no Get. Update's resourceVersion rejects any change
// to that object since the read; a conflict re-reads both session and ledger.
func (s *Server) writeRaiseAnnotationWhen(ctx context.Context, snapshot *v1alpha1.AgentSession, raw string) error {
	sess := snapshot.DeepCopy()
	if sess.Annotations == nil {
		sess.Annotations = map[string]string{}
	}
	sess.Annotations[v1alpha1.AnnotationRaise] = raw
	err := s.Client.Update(ctx, sess)
	if apierrors.IsConflict(err) {
		return errRaiseAnnotationMoved
	}
	return err
}

func (s *Server) podProfile(ctx context.Context, sess *v1alpha1.AgentSession) string {
	pod, ok := s.raisePod(ctx, sess)
	if !ok {
		return sess.Spec.Profile
	}
	if label := pod.Labels[v1alpha1.LabelProfile]; label != "" {
		return label
	}
	return sess.Spec.Profile
}

func (s *Server) raisePod(ctx context.Context, sess *v1alpha1.AgentSession) (*corev1.Pod, bool) {
	if s.Live == nil || sess == nil {
		return nil, false
	}
	var pod corev1.Pod
	if err := s.Live.Get(ctx, types.NamespacedName{Namespace: sess.Namespace, Name: sess.Name}, &pod); err != nil {
		return nil, false
	}
	return &pod, true
}

func (s *Server) suspendIfRaisedAccount(ctx context.Context, sess *v1alpha1.AgentSession, profile, by string) error {
	if profile == "" || s.Templates == nil {
		return nil
	}
	t, err := s.Templates(ctx)
	if err != nil || t == nil {
		return err
	}
	_, parsed, err := t.Profile(profile)
	if err != nil {
		return err
	}
	account := parsed.ServiceAccount
	if account == "" {
		account = controller.ServiceAccountName
	}
	pod, ok := s.raisePod(ctx, sess)
	if !ok || pod.Spec.ServiceAccountName != account {
		return nil
	}
	return s.suspendRaised(ctx, client.ObjectKeyFromObject(sess), sess.UID, by)
}

func (s *Server) suspendRaised(ctx context.Context, key types.NamespacedName, uid types.UID, by string) error {
	var last error
	for range 5 {
		var sess v1alpha1.AgentSession
		if err := s.Live.Get(ctx, key, &sess); err != nil {
			return err
		}
		if sess.UID != uid || !sess.DeletionTimestamp.IsZero() || sess.Spec.OperatingMode == v1alpha1.OperatingModeSuspended {
			return nil
		}
		orig := sess.DeepCopy()
		sess.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
		if sess.Annotations == nil {
			sess.Annotations = map[string]string{}
		}
		sess.Annotations[v1alpha1.AnnotationSuspendedBy] = by
		last = s.Client.Patch(ctx, &sess, client.MergeFromWithOptions(orig, client.MergeFromWithOptimisticLock{}))
		if apierrors.IsConflict(last) {
			continue
		}
		return last
	}
	return last
}

func (s *Server) releaseRaisedGrants(ctx context.Context, uid types.UID, profile string) {
	if profile == "" || s.Live == nil || s.Client == nil {
		return
	}
	var list v1alpha1.AccessGrantList
	if err := s.Live.List(ctx, &list, client.InNamespace(s.Policy.SessionNamespace)); err != nil {
		s.Log.Error(err, "raise take-back grant list")
		return
	}
	for i := range list.Items {
		g := &list.Items[i]
		if g.Spec.Requester.SessionUID != uid || g.Spec.Requester.Profile != profile || g.Spec.Release {
			continue
		}
		if g.Status.Phase != v1alpha1.GrantPending && g.Status.Phase != v1alpha1.GrantActive {
			continue
		}
		orig := g.DeepCopy()
		g.Spec.Release = true
		if err := s.Client.Patch(ctx, g, client.MergeFrom(orig)); err != nil {
			s.Log.Error(err, "raise take-back grant release", "grant", g.Name)
		}
	}
}

func decodeRaiseBody(r *http.Request, dest any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dest); err != nil {
		return badRequest("the raise body is not valid JSON")
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return badRequest("the raise body is not valid JSON")
	}
	return nil
}

// attachRaise adds the raise object when the annotation is present. It reads
// the ledger only then. An unraised session is left unchanged.
func (s *Server) attachRaise(ctx context.Context, sess *v1alpha1.AgentSession, v *apiv1.Session, podAccount bool) {
	if s == nil || sess == nil || v == nil {
		return
	}
	raw, ok := sess.Annotations[v1alpha1.AnnotationRaise]
	if !ok || raw == "" {
		return
	}
	view := &apiv1.SessionRaise{}
	rec, err := sessionraise.Parse(raw)
	if err != nil {
		view.State = "ignored"
		view.Ignored = sessionraise.IgnoreSignature
		v.Raise = view
		return
	}
	fillRaise(view, rec)
	cfg, configured := s.raiseConfig()
	var pub ed25519.PublicKey
	var entry *sessionraise.SessionEntry
	reason := sessionraise.IgnoreLedger
	if configured {
		var err error
		pub, err = sessionraise.ReadPublic(ctx, s.raiseStore(cfg), cfg.OperatorPublic)
		if err != nil {
			reason = sessionraise.IgnoreSignature
			if errors.Is(err, sessionraise.ErrUnreadable) {
				reason = sessionraise.IgnoreLedger
			}
		} else if entry, err = s.raiseEntry(ctx, cfg, string(sess.UID)); err != nil {
			reason = sessionraise.IgnoreLedger
		} else {
			reason = sessionraise.Counts(rec, string(sess.UID), pub, entry)
		}
	}
	// With both flags off, a record that would count and is pending or active
	// is shown as ignored. A taken-back or unannounced record is shown as it is.
	if reason == "" && (rec.State == sessionraise.StatePending || rec.State == sessionraise.StateActive) && s.raisingOff() {
		reason = "raising is switched off"
	}
	if reason != "" {
		view.Ignored = reason
		view.State = "ignored"
		s.logIgnored(rec.ID, reason)
	} else if rec.State == sessionraise.StatePending {
		s.overlayNotice(ctx, cfg, view, entry, pub)
	}
	if podAccount {
		if pod, ok := s.raisePod(ctx, sess); ok {
			view.PodAccount = pod.Spec.ServiceAccountName
		}
	}
	v.Raise = view
}

func fillRaise(view *apiv1.SessionRaise, rec sessionraise.Record) {
	view.ID = rec.ID
	view.State = rec.State
	view.Profile = rec.Profile
	view.FromProfile = rec.FromProfile
	view.StartSessions = rec.StartSessions
	view.PerformedBy = rec.PerformedBy
	view.PerformedAt = rec.PerformedAt
	view.NoticeSentAt = rec.NoticeSentAt
	view.ActiveFrom = rec.ActiveFrom
	view.TakenBackBy = rec.TakenBackBy
	view.TakenBackAt = rec.TakenBackAt
	view.Owner = &apiv1.RaiseOwner{
		Words: rec.Owner.Words, Where: rec.Owner.Where, Session: rec.Owner.Session,
		SaidAt: rec.Owner.SaidAt, Ref: rec.Owner.Ref,
	}
}

func (s *Server) overlayNotice(ctx context.Context, cfg RaiseConfig, view *apiv1.SessionRaise, entry *sessionraise.SessionEntry, pub ed25519.PublicKey) {
	if entry == nil {
		return
	}
	notices, keeperPub, err := s.readNotices(ctx, cfg)
	if err != nil || notices == nil {
		return
	}
	view.NoticeAttempt = sessionraise.AttemptCount(s.now(), *entry, notices.Items, keeperPub)
	sent, ok := sessionraise.SentTime(*entry, notices.Items, keeperPub)
	if !ok {
		return
	}
	at, err := time.Parse(time.RFC3339, sent)
	if err != nil {
		return
	}
	view.NoticeSentAt = sent
	if sessionraise.SentBeforePerformed(sent, entry.PerformedAt) {
		view.ActiveFrom = ""
		view.Ignored = noticeBeforeRaise
		return
	}
	view.ActiveFrom = at.Add(sessionraise.EffectDelay).UTC().Format(time.RFC3339)
	_ = pub
}

func (s *Server) raiseStatus(ctx context.Context, sess *v1alpha1.AgentSession) apiv1.RaiseStatus {
	v := view(sess, false)
	s.attachRaise(ctx, sess, &v, false)
	profile := sess.Spec.Profile
	if profile == "" && s.Templates != nil {
		if t, err := s.Templates(ctx); err == nil && t != nil {
			profile = t.DefaultProfile
		}
	}
	podProfile := ""
	if pod, ok := s.raisePod(ctx, sess); ok {
		podProfile = pod.Labels[v1alpha1.LabelProfile]
	}
	return apiv1.RaiseStatus{
		Name: sess.Name, UID: string(sess.UID), Depth: sess.Labels[v1alpha1.LabelDepth],
		Profile: profile, PodProfile: podProfile, Raise: v.Raise,
	}
}

// depthExempt is the only way a create passes the depth check above 2.
// The caller holds createMu. Every failed read returns false, so the create
// is refused rather than admitted.
func (s *Server) depthExempt(ctx context.Context, c *caller) bool {
	cfg, ok := s.raiseConfig()
	if !ok || !cfg.Dispatch || c == nil || c.session == nil || c.depth != maxSessionDepth+1 {
		return false
	}
	if c.session.Labels[v1alpha1.LabelDepth] != "2" || s.Live == nil {
		return false
	}
	var live v1alpha1.AgentSession
	if err := s.Live.Get(ctx, types.NamespacedName{Namespace: c.session.Namespace, Name: c.session.Name}, &live); err != nil {
		return false
	}
	if live.UID != c.session.UID || live.Labels[v1alpha1.LabelDepth] != "2" {
		return false
	}
	rec, err := sessionraise.Parse(live.Annotations[v1alpha1.AnnotationRaise])
	if err != nil {
		return false
	}
	pub, err := sessionraise.ReadPublic(ctx, s.raiseStore(cfg), cfg.OperatorPublic)
	if err != nil {
		return false
	}
	entry, err := s.raiseEntry(ctx, cfg, string(live.UID))
	if err != nil || entry == nil {
		return false
	}
	if reason := sessionraise.Counts(rec, string(live.UID), pub, entry); reason != "" {
		s.logIgnored(rec.ID, reason)
		return false
	}
	if rec.State != sessionraise.StateActive || !rec.StartSessions || rec.Depth != "2" {
		s.logIgnored(rec.ID, "does not grant start sessions")
		return false
	}
	activeFrom, err := time.Parse(time.RFC3339, rec.ActiveFrom)
	if err != nil || s.now().Before(activeFrom) {
		s.logIgnored(rec.ID, "activation time has not arrived")
		return false
	}
	return true
}

func (s *Server) depthRefusal(c *caller) *apiError {
	msg := fmt.Sprintf("session %s is %d levels deep and may not start sessions: two levels is the limit (DESIGN-001 3.4)", c.parent, c.depth-1)
	cfg, ok := s.raiseConfig()
	if ok && cfg.Dispatch && c.session != nil && c.session.Labels[v1alpha1.LabelDepth] == "2" {
		msg += ". A session at depth 2 can be raised to start sessions (agent-run help raise)."
	}
	return forbidden("%s", msg)
}

// raisingOff is both flags off, or no raise config at all.
func (s *Server) raisingOff() bool {
	cfg, ok := s.raiseConfig()
	return !ok || (!cfg.Dispatch && len(cfg.Profiles) == 0)
}

// refuseRaisedChild refuses a child admitted through the depth exemption
// when the child's profile, named or the templates' default, runs as the
// workbench account or a named coordinator. A failed templates read refuses
// the create. The sentence is design 8.2. The name it returns is the one
// that check resolved, from this single templates read.
func (s *Server) refuseRaisedChild(ctx context.Context, sess *v1alpha1.AgentSession) (string, error) {
	const sentence = "A raised session's child never runs as the workbench or a named coordinator; name another profile."
	if s.Templates == nil {
		return "", raiseUnavailable("The templates cannot be read, so the operator cannot tell what profile the child runs as.")
	}
	t, err := s.Templates(ctx)
	if err != nil || t == nil {
		return "", raiseUnavailable("The templates cannot be read, so the operator cannot tell what profile the child runs as.")
	}
	name := ""
	if sess != nil {
		name = sess.Spec.Profile
	}
	resolved, profile, err := t.Profile(name)
	if err != nil {
		return "", raiseUnavailable("The templates cannot be read, so the operator cannot tell what profile the child runs as.")
	}
	if profile.ServiceAccount == templates.WorkbenchServiceAccount || templates.IsCoordinatorServiceAccount(profile.ServiceAccount) {
		return "", forbidden("%s", sentence)
	}
	return resolved, nil
}

// raisedAudit is the line for a recorded raise. It does not say Tom is
// being told: the notice waits until the annotation counts.
func raisedAudit(name, profile string) string {
	target := "start sessions"
	if profile != "" {
		target = profile
	}
	return fmt.Sprintf("Raised %s to %s. The raise was recorded; it takes effect two minutes after the notice is accepted, unless taken back.", name, target)
}

// observedAudit is the line for a ledger state the repair brings the
// annotation up to. Pending is logged by the raise itself.
func observedAudit(name, state string) string {
	switch state {
	case sessionraise.StateActive:
		return "raise active"
	case sessionraise.StateUnannounced:
		return "raise unannounced"
	case sessionraise.StateTakenBack:
		return "took back a raise of " + name
	default:
		return ""
	}
}

// logRaiseState records transitions at least once, and once per operator
// process for each id and state. Memory deduplication resets on restart; it
// is not a global once guarantee. The write and repair both call it.
func (s *Server) logRaiseState(msg string, rec sessionraise.Record) {
	if s == nil || msg == "" || rec.ID == "" || rec.State == "" {
		return
	}
	if _, loaded := s.raiseLogged.LoadOrStore(rec.ID+"\n"+rec.State, struct{}{}); loaded {
		return
	}
	s.logRaise(msg, rec)
}

func (s *Server) logRaise(msg string, rec sessionraise.Record) {
	if s == nil || s.Log.GetSink() == nil {
		return
	}
	s.Log.Info(msg, raiseLogFields(rec)...)
}

func (s *Server) logIgnored(id, reason string) {
	s.logOnce(id, reason, "raise record ignored")
}

func (s *Server) logOnce(id, reason, msg string) {
	if s == nil || id == "" || reason == "" {
		return
	}
	if _, loaded := s.raiseIgnored.LoadOrStore(id+"\n"+reason, struct{}{}); loaded {
		return
	}
	if s.Log.GetSink() == nil {
		return
	}
	s.Log.Info(msg, "id", id, "reason", reason)
}

func raiseLogFields(rec sessionraise.Record) []any {
	words := rec.Owner.Words
	if sessionraise.WordsProblem(words) != "" {
		words = ""
	}
	return []any{
		"v", rec.V,
		"id", rec.ID,
		"sessionUID", rec.SessionUID,
		"state", rec.State,
		"profile", rec.Profile,
		"fromProfile", rec.FromProfile,
		"startSessions", rec.StartSessions,
		"depth", rec.Depth,
		"words", words,
		"wordsHash", rec.Owner.WordsHash,
		"where", rec.Owner.Where,
		"ownerSession", rec.Owner.Session,
		"saidAt", rec.Owner.SaidAt,
		"ref", rec.Owner.Ref,
		"performedBy", rec.PerformedBy,
		"performedAt", rec.PerformedAt,
		"noticeSentAt", rec.NoticeSentAt,
		"activeFrom", rec.ActiveFrom,
		"takenBackBy", rec.TakenBackBy,
		"takenBackAt", rec.TakenBackAt,
	}
}
