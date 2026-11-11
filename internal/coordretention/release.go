package coordretention

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/shelf"
)

// release is the only caller of deleteRecord. TestOnlyRetentionDeletesCoordinationRecords
// reads this function: releaseRechecked and archiveVerified run before deleteRecord.
func (r *Runnable) release(ctx context.Context, c candidate) stepStatus {
	rec, st := r.releaseRechecked(ctx, c.item.Result.Summary, c.item.Result.Reply)
	if st.done || st.refused || st.stop != "" {
		return st
	}
	if err := r.archiveRevision(ctx, rec); err != nil {
		return r.refuseArchive(rec.summary.Kind, err)
	}
	if err := r.archiveVerified(ctx, rec); err != nil {
		return r.refuseArchive(rec.summary.Kind, err)
	}
	// A note released on its reply is archived with that reply. Both are
	// verified before either delete. A failure of either leaves both live.
	var replyRec *liveRecord
	if c.item.Result.Reply != nil {
		prepared, replyStatus := r.prepareBoundReply(ctx, rec, *c.item.Result.Reply)
		if replyStatus.stop != "" || replyStatus.refused {
			return replyStatus
		}
		if prepared == nil {
			r.countRefused(rec.summary.Kind, reasonUnknown)
			return stepStatus{refused: true}
		}
		if err := r.archiveVerified(ctx, *prepared); err != nil {
			return r.refuseArchive(prepared.summary.Kind, err)
		}
		replyRec = prepared
	}
	var ackRec *liveRecord
	if c.ack != nil {
		prepared, ackStatus := r.prepareAck(ctx, c.ack.Result.Summary, rec, c.item.Record)
		if ackStatus.stop != "" || ackStatus.refused {
			return ackStatus
		}
		if prepared != nil {
			if err := r.archiveVerified(ctx, *prepared); err != nil {
				return r.refuseArchive(prepared.summary.Kind, err)
			}
			ackRec = prepared
		}
	}
	messageDelete := r.finishDelete(ctx, rec.summary, r.deleteRecord(ctx, rec.summary))
	if !messageDelete.deleted {
		return messageDelete
	}
	if replyRec != nil {
		replyDelete := r.finishDelete(ctx, replyRec.summary, r.deleteRecord(ctx, replyRec.summary))
		if !replyDelete.deleted {
			return replyDelete
		}
		if ackRec == nil {
			return replyDelete
		}
	}
	if ackRec == nil {
		return messageDelete
	}
	ackDelete := r.finishDelete(ctx, ackRec.summary, r.deleteRecord(ctx, ackRec.summary))
	if !ackDelete.deleted {
		return ackDelete
	}
	return stepStatus{deleted: true}
}

func (r *Runnable) releaseRechecked(ctx context.Context, s Summary, reply *Summary) (liveRecord, stepStatus) {
	rec, err := r.readRecord(ctx, s)
	if apierrors.IsNotFound(err) {
		return liveRecord{}, stepStatus{done: true}
	}
	if err != nil {
		return liveRecord{}, r.refuseArchive(s.Kind, err)
	}
	if rec.summary.UID != s.UID {
		return liveRecord{}, stepStatus{done: true}
	}
	if !rec.summary.Valid {
		r.countRefused(s.Kind, reasonUnknown)
		return liveRecord{}, stepStatus{refused: true}
	}
	cfg := EvalConfig{Reader: r.Reader, OperatorNamespace: r.Namespace, ReconciledNamespaces: r.Reconciled}
	if rec.summary.Kind == InboxReadAck {
		evidence, evidenceErr := r.messageEvidence(ctx, rec.summary.Message)
		if evidenceErr != nil {
			return liveRecord{}, r.refuseArchive(s.Kind, evidenceErr)
		}
		cfg.Archive = map[MessageRef]ArchiveEvidence{rec.summary.Message: evidence}
	}
	// nil replies: this recheck has one record, so decide reads a stored reply itself.
	decision, _, observations, replyAck, proven := decide(ctx, cfg, rec.summary, map[string]memoSession{}, nil)
	if reply != nil {
		// The pass released this note on one reply. A malformed, replaced or
		// removed reply does not fall through to another release reason.
		if decision != Released || !replyAck || proven == nil || proven.UID != reply.UID || proven.Name != reply.Name {
			r.countRefused(s.Kind, reasonUnknown)
			return liveRecord{}, stepStatus{refused: true}
		}
	}
	rec.observations = observations
	switch decision {
	case Released:
		return rec, stepStatus{}
	case Kept:
		if err := r.appendHold(ctx, rec); err != nil {
			if stop := r.refuseArchive(rec.summary.Kind, err); stop.stop != "" {
				return liveRecord{}, stop
			}
			r.block(orderOf(rec.summary))
			r.countRefused(rec.summary.Kind, reasonOwnerPresent)
			return liveRecord{}, stepStatus{refused: true}
		}
		if err := r.appendPresent(ctx, rec); err != nil {
			if stop := r.refuseArchive(rec.summary.Kind, err); stop.stop != "" {
				return liveRecord{}, stop
			}
			r.block(orderOf(rec.summary))
		}
		r.countRefused(rec.summary.Kind, reasonOwnerPresent)
		return liveRecord{}, stepStatus{refused: true}
	default:
		r.countRefused(rec.summary.Kind, reasonUnknown)
		return liveRecord{}, stepStatus{refused: true}
	}
}

// prepareBoundReply archives the exact reply the pass named. It does not delete.
// The caller verifies the bytes and deletes only after that succeeds.
func (r *Runnable) prepareBoundReply(ctx context.Context, note liveRecord, want Summary) (*liveRecord, stepStatus) {
	rec, err := r.readRecord(ctx, want)
	if apierrors.IsNotFound(err) || (err == nil && (rec.summary.UID != want.UID || rec.summary.Name != want.Name)) {
		r.countRefused(note.summary.Kind, reasonUnknown)
		return nil, stepStatus{refused: true}
	}
	if err != nil {
		return nil, r.refuseArchive(want.Kind, err)
	}
	var msg coord.InboxMessage
	if err := r.Reader.Get(ctx, client.ObjectKey{Namespace: want.Namespace, Name: want.Name}, &msg); err != nil {
		if apierrors.IsNotFound(err) {
			r.countRefused(note.summary.Kind, reasonUnknown)
			return nil, stepStatus{refused: true}
		}
		return nil, r.refuseArchive(want.Kind, err)
	}
	if !validReply(note.summary, &msg, r.Namespace) || string(msg.UID) != want.UID {
		r.countRefused(note.summary.Kind, reasonUnknown)
		return nil, stepStatus{refused: true}
	}
	cfg := EvalConfig{Reader: r.Reader, OperatorNamespace: r.Namespace, ReconciledNamespaces: r.Reconciled}
	decision, _, observations, _, _ := decide(ctx, cfg, rec.summary, map[string]memoSession{}, &replyIndex{byNote: map[noteKey][]Summary{}})
	if decision != Released {
		r.countRefused(note.summary.Kind, reasonUnknown)
		return nil, stepStatus{refused: true}
	}
	ev, err := absentEvent(rec.summary.Resource, r.now(), observations)
	if err != nil {
		r.countRefused(rec.summary.Kind, reasonUnknown)
		return nil, stepStatus{refused: true}
	}
	if err := r.putArchive(ctx, rec, &ev); err != nil {
		return nil, r.refuseArchive(rec.summary.Kind, err)
	}
	return &rec, stepStatus{}
}

func (r *Runnable) messageEvidence(ctx context.Context, m MessageRef) (ArchiveEvidence, error) {
	id := protocol.CoordArchiveIdentity{Resource: "inboxmessages", Project: m.Project, Name: m.Name, UID: m.UID}
	if _, err := id.RecordDir(); err != nil {
		return ArchiveEvidence{}, errArchiveInvalid
	}
	got, err := r.lookupIDs(ctx, []protocol.CoordArchiveIdentity{id})
	if err != nil {
		return ArchiveEvidence{}, err
	}
	if len(got) != 1 || got[0].CoordArchiveIdentity != id {
		return ArchiveEvidence{}, errArchiveMissing
	}
	return ArchiveEvidence{Known: true, Holds: got[0].Holds}, nil
}

func (r *Runnable) archiveRevision(ctx context.Context, rec liveRecord) error {
	id := identityOf(rec.summary)
	got, err := r.lookupIDs(ctx, []protocol.CoordArchiveIdentity{id})
	if err != nil {
		return err
	}
	if len(got) != 1 || got[0].CoordArchiveIdentity != id {
		return errArchiveMissing
	}
	// An older shelf omits the lookup tail. Do not treat that as a rules miss.
	if got[0].GraceStart != nil && got[0].LatestState == "" {
		return errArchiveOutdated
	}
	// A hold, or any other non-absent tail, is not an open grace to delete.
	// Tail time equaling GraceStart is not a fence: while grace is open the
	// tail of an unmarked failure is still the absent that opened it. A hold
	// written when the owner was seen is what a later leader can see.
	if !absentGraceCurrent(got[0]) {
		return errGraceStale
	}
	if !got[0].Holds || len(got[0].Revisions) == 0 {
		ev, evErr := resetEvent(rec.summary.Resource, r.now())
		if evErr != nil {
			return errArchiveInvalid
		}
		if err := r.markBatch(ctx, []protocol.CoordArchiveMarkRequest{{CoordArchiveIdentity: id, Event: ev}}); err != nil {
			return err
		}
		return errArchiveMissing
	}
	for _, rev := range got[0].Revisions {
		if rev.ResourceVersion == rec.summary.ResourceVersion && rev.SHA256 == rec.hash {
			return nil
		}
	}
	return r.putArchive(ctx, rec, nil)
}

// absentGraceCurrent rejects a missing or non-absent tail. It does not detect
// a present that failed to append: that failure leaves this predicate true.
func absentGraceCurrent(got protocol.CoordArchiveLookupResult) bool {
	if got.GraceStart == nil || got.LatestAt == nil || got.LatestState != "absent" {
		return false
	}
	return !got.LatestAt.IsZero() && got.LatestAt.UTC().Equal(got.GraceStart.UTC())
}

func (r *Runnable) archiveVerified(ctx context.Context, rec liveRecord) error {
	if r.Archive == nil {
		return shelf.ErrNoShelf
	}
	query := protocol.CoordArchiveGetQuery{
		Resource: rec.summary.Resource, Name: rec.summary.Name, UID: rec.summary.UID,
		ResourceVersion: rec.summary.ResourceVersion, SHA256: rec.hash,
	}
	release, err := r.withSlot(ctx)
	if err != nil {
		return err
	}
	got, err := r.Archive.Get(ctx, query)
	release()
	if err != nil {
		switch historyCode(err) {
		case protocol.HistoryErrNotFound:
			return errArchiveMissing
		case protocol.HistoryErrConflict, protocol.HistoryErrCorrupt, protocol.HistoryErrAmbiguous:
			return errArchiveMismatch
		default:
			return err
		}
	}
	sum := sha256.Sum256(got.Object)
	objectHash := hex.EncodeToString(sum[:])
	stored := got.Manifest.Object.SHA256
	if stored != rec.hash || objectHash != rec.hash || got.Manifest.UID != rec.summary.UID || got.Manifest.ResourceVersion != rec.summary.ResourceVersion {
		return errArchiveMismatch
	}
	return nil
}

// deleteRecord is the only Delete of a coordination record. The call carries
// UID and resourceVersion preconditions. TestOnlyRetentionDeletesCoordinationRecords
// fails if a second delete appears or this one drops either precondition.
func (r *Runnable) deleteRecord(ctx context.Context, s Summary) error {
	if r.Client == nil {
		return errArchiveInvalid
	}
	item := kindResource(s.Kind)
	if item.kind == "" || s.Name == "" || s.UID == "" || s.ResourceVersion == "" {
		return errArchiveInvalid
	}
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(item.gvk)
	obj.SetNamespace(s.Namespace)
	obj.SetName(s.Name)
	uid := types.UID(s.UID)
	rv := s.ResourceVersion
	obj.SetUID(uid)
	obj.SetResourceVersion(rv)
	return r.Client.Delete(ctx, obj, client.Preconditions{UID: &uid, ResourceVersion: &rv})
}

func (r *Runnable) confirmDeleted(ctx context.Context, s Summary) error {
	item := kindResource(s.Kind)
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(item.gvk)
	err := r.Reader.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: s.Name}, u)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if string(u.GetUID()) != s.UID {
		return nil
	}
	return errors.New("coordination retention: record still present after delete")
}

func (r *Runnable) finishDelete(ctx context.Context, s Summary, err error) stepStatus {
	if err == nil || apierrors.IsNotFound(err) {
		if confirmErr := r.confirmDeleted(ctx, s); confirmErr != nil {
			if deadlineErr(confirmErr) {
				return stepStatus{stop: resultPartial}
			}
			return stepStatus{stop: resultFailed}
		}
		r.countDeleted(s.Kind)
		return stepStatus{deleted: true}
	}
	switch {
	case apierrors.IsConflict(err):
		r.countRefused(s.Kind, reasonPreconditionFailed)
		return stepStatus{refused: true}
	case apierrors.IsForbidden(err):
		r.countRefused(s.Kind, reasonForbidden)
		return stepStatus{refused: true}
	case deadlineErr(err):
		return stepStatus{stop: resultPartial}
	case historyCode(err) == protocol.HistoryErrNoArchive || errors.Is(err, shelf.ErrNoShelf):
		return stepStatus{stop: resultShelfUnavailable}
	default:
		return stepStatus{stop: resultFailed}
	}
}

func (r *Runnable) prepareAck(ctx context.Context, ack Summary, message liveRecord, looked *protocol.CoordArchiveLookupResult) (*liveRecord, stepStatus) {
	rec, err := r.readRecord(ctx, ack)
	if apierrors.IsNotFound(err) {
		return nil, stepStatus{}
	}
	if err != nil {
		return nil, r.refuseArchive(ack.Kind, err)
	}
	if rec.summary.UID != ack.UID {
		return nil, stepStatus{}
	}
	if !rec.summary.Valid || rec.summary.ResourceVersion != ack.ResourceVersion {
		r.countRefused(ack.Kind, reasonUnknown)
		return nil, stepStatus{refused: true}
	}
	at := r.now()
	if looked != nil && looked.GraceStart != nil {
		at = looked.GraceStart.UTC()
	}
	if err := r.ensureAckMark(ctx, rec, message.summary, at); err != nil {
		return nil, r.refuseArchive(ack.Kind, err)
	}
	fresh, err := r.readRecord(ctx, rec.summary)
	if err != nil {
		return nil, r.refuseArchive(ack.Kind, err)
	}
	if fresh.summary.UID != rec.summary.UID || fresh.summary.ResourceVersion != rec.summary.ResourceVersion {
		r.countRefused(ack.Kind, reasonUnknown)
		return nil, stepStatus{refused: true}
	}
	if err := r.archiveRevision(ctx, fresh); err != nil {
		return nil, r.refuseArchive(fresh.summary.Kind, err)
	}
	return &fresh, stepStatus{}
}

func (r *Runnable) ensureAckMark(ctx context.Context, ack liveRecord, message Summary, at time.Time) error {
	id := identityOf(ack.summary)
	got, err := r.lookupIDs(ctx, []protocol.CoordArchiveIdentity{id})
	if err != nil {
		return err
	}
	if len(got) == 1 && got[0].LatestState == "hold" {
		// A held ack is not a live grace. Reset it, then mark the new absence
		// at the reset time. The caller's absence time can be the message's
		// old grace start, and the journal refuses an event older than the tail.
		at = r.now().UTC()
		ev, err := resetEvent(ack.summary.Resource, at)
		if err != nil {
			return errArchiveInvalid
		}
		if err := r.markBatch(ctx, []protocol.CoordArchiveMarkRequest{{CoordArchiveIdentity: id, Event: ev}}); err != nil {
			return err
		}
	} else if len(got) == 1 && got[0].GraceStart != nil {
		return nil
	}
	// A reset or present tail is newer than the message's grace start and has
	// no grace of its own. The new absence cannot precede that tail.
	if len(got) == 1 && got[0].LatestAt != nil && at.Before(got[0].LatestAt.UTC()) {
		at = got[0].LatestAt.UTC()
	}
	ev, err := ackAbsentEvent(message, at)
	if err != nil {
		return errArchiveInvalid
	}
	if err := r.putArchive(ctx, ack, &ev); err != nil {
		return err
	}
	r.countMarked(ack.summary.Kind)
	return nil
}

func (r *Runnable) appendHold(ctx context.Context, rec liveRecord) error {
	ev, err := presentEvent(rec.summary.Resource, r.now(), rec.observations)
	if err != nil {
		return err
	}
	ev.State = "hold"
	if err := ev.Validate(rec.summary.Resource); err != nil {
		return errArchiveInvalid
	}
	return r.markBatch(ctx, []protocol.CoordArchiveMarkRequest{{CoordArchiveIdentity: identityOf(rec.summary), Event: ev}})
}

func (r *Runnable) appendPresent(ctx context.Context, rec liveRecord) error {
	ev, err := presentEvent(rec.summary.Resource, r.now(), rec.observations)
	if err != nil {
		return err
	}
	return r.markBatch(ctx, []protocol.CoordArchiveMarkRequest{{CoordArchiveIdentity: identityOf(rec.summary), Event: ev}})
}

func (r *Runnable) refuseArchive(kind Kind, err error) stepStatus {
	switch {
	case err == nil:
		return stepStatus{}
	case errors.Is(err, errArchiveInvalid):
		r.countRefused(kind, reasonArchiveInvalid)
		return stepStatus{refused: true}
	case errors.Is(err, errArchiveMissing):
		r.countRefused(kind, reasonArchiveMissing)
		return stepStatus{refused: true}
	case errors.Is(err, errArchiveMismatch):
		r.countRefused(kind, reasonArchiveMismatch)
		return stepStatus{refused: true}
	case errors.Is(err, errGraceStale):
		r.countRefused(kind, reasonUnknown)
		return stepStatus{refused: true}
	case errors.Is(err, errArchiveOutdated):
		r.countRefused(kind, reasonArchiveOutdated)
		return stepStatus{refused: true}
	case historyCode(err) == protocol.HistoryErrNoArchive || errors.Is(err, shelf.ErrNoShelf):
		return stepStatus{stop: resultShelfUnavailable}
	case deadlineErr(err):
		return stepStatus{stop: resultPartial}
	default:
		return stepStatus{stop: resultFailed}
	}
}
