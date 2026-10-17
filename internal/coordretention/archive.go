package coordretention

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/shelf"
)

const archivedBy = "dev-env-operator"

// Archive is the shelf coordination-archive transport. Callers take an
// ExecSlots slot; the shelf client does not.
type Archive interface {
	Put(context.Context, protocol.CoordArchivePutRequest, bool) (protocol.CoordArchivePutResult, error)
	Mark(context.Context, []protocol.CoordArchiveMarkRequest) ([]protocol.CoordArchiveMarkResult, error)
	Lookup(context.Context, []protocol.CoordArchiveIdentity) ([]protocol.CoordArchiveLookupResult, error)
	Get(context.Context, protocol.CoordArchiveGetQuery) (protocol.CoordArchiveGetResult, error)
}

// PodSource finds the Running Ready shelf pod.
type PodSource interface {
	Pod(context.Context) (string, error)
}

var (
	errArchiveInvalid  = errors.New("coordination retention: archive_invalid")
	errArchiveMissing  = errors.New("coordination retention: archive_missing")
	errArchiveMismatch = errors.New("coordination retention: archive_mismatch")
	errGraceStale      = errors.New("coordination retention: grace event is not the latest absence")
)

// coordinationProbe is a read-only lookup identity. It is not a live record.
// "When it answers no_archive" is this one lookup before any mark or delete.
var coordinationProbe = protocol.CoordArchiveIdentity{
	Resource: "taskclaims",
	Project:  "retention-probe",
	Name:     "task-claim." + strings.Repeat("0", 32) + "." + strings.Repeat("1", 32),
	UID:      "00000000-0000-4000-8000-000000000001",
}

func (r *Runnable) withSlot(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.Slots == nil {
		return nil, shelf.ErrNoShelf
	}
	release, ok := r.Slots.TryTake()
	if !ok {
		return nil, ErrLookupBusy
	}
	if err := ctx.Err(); err != nil {
		release()
		return nil, err
	}
	return release, nil
}

func (r *Runnable) lookupIDs(ctx context.Context, ids []protocol.CoordArchiveIdentity) ([]protocol.CoordArchiveLookupResult, error) {
	if r.Archive == nil {
		return nil, shelf.ErrNoShelf
	}
	if len(ids) == 0 || len(ids) > protocol.MaxCoordArchiveLookupItems {
		return nil, errors.New("coordination retention: lookup batch required")
	}
	release, err := r.withSlot(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return r.Archive.Lookup(ctx, ids)
}

func (r *Runnable) shelfReady(ctx context.Context) error {
	if r.Pods == nil || r.Archive == nil || r.Slots == nil {
		return shelf.ErrNoShelf
	}
	if _, err := r.Pods.Pod(ctx); err != nil {
		return err
	}
	_, err := r.lookupIDs(ctx, []protocol.CoordArchiveIdentity{coordinationProbe})
	return err
}

func (r *Runnable) markBatch(ctx context.Context, reqs []protocol.CoordArchiveMarkRequest) error {
	if len(reqs) == 0 {
		return nil
	}
	if r.Archive == nil {
		return shelf.ErrNoShelf
	}
	for start := 0; start < len(reqs); start += protocol.MaxCoordArchiveLookupItems {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := min(start+protocol.MaxCoordArchiveLookupItems, len(reqs))
		release, err := r.withSlot(ctx)
		if err != nil {
			return err
		}
		_, err = r.Archive.Mark(ctx, reqs[start:end])
		release()
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *Runnable) putArchive(ctx context.Context, rec liveRecord, event *protocol.CoordArchiveEvent) error {
	if r.Archive == nil {
		return shelf.ErrNoShelf
	}
	manifest, err := manifestFor(rec)
	if err != nil {
		return err
	}
	release, err := r.withSlot(ctx)
	if err != nil {
		return err
	}
	defer release()
	_, err = r.Archive.Put(ctx, protocol.CoordArchivePutRequest{Manifest: manifest, Object: rec.raw, Event: event}, event != nil)
	if historyCode(err) == protocol.HistoryErrConflict {
		return errArchiveMismatch
	}
	return err
}

type liveRecord struct {
	summary      Summary
	raw          []byte
	hash         string
	observations []protocol.CoordArchiveObservation
}

func (r *Runnable) readRecord(ctx context.Context, s Summary) (liveRecord, error) {
	item := kindResource(s.Kind)
	if item.kind == "" || r.Reader == nil {
		return liveRecord{}, errArchiveInvalid
	}
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(item.gvk)
	if err := r.Reader.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: s.Name}, u); err != nil {
		return liveRecord{}, err
	}
	if u.GetNamespace() != s.Namespace || u.GetName() != s.Name {
		return liveRecord{}, errArchiveInvalid
	}
	if u.GetAPIVersion() == "" {
		u.SetAPIVersion(item.gvk.GroupVersion().String())
	}
	if u.GetKind() == "" {
		u.SetKind(item.gvk.Kind)
	}
	raw, hash, err := canonicalBytes(u)
	if err != nil {
		return liveRecord{}, err
	}
	fresh := summarize(item, s.Namespace, u)
	return liveRecord{summary: fresh, raw: raw, hash: hash}, nil
}

func canonicalBytes(u *unstructured.Unstructured) ([]byte, string, error) {
	raw, err := protocol.CanonicalJSON(u.Object)
	if err != nil {
		return nil, "", err
	}
	if len(raw) == 0 || len(raw) > protocol.MaxCoordArchiveObjectBytes {
		return nil, "", errArchiveInvalid
	}
	sum := sha256.Sum256(raw)
	return raw, hex.EncodeToString(sum[:]), nil
}

func manifestFor(rec liveRecord) (protocol.CoordArchiveManifest, error) {
	s := rec.summary
	m := protocol.CoordArchiveManifest{
		Format: protocol.CoordArchiveFormat, Resource: s.Resource, Kind: string(s.Kind),
		APIVersion: protocol.CoordArchiveAPIVersion, Namespace: s.Namespace, Name: s.Name,
		UID: s.UID, ResourceVersion: s.ResourceVersion, Project: s.Project,
		Object:     protocol.CoordArchiveObjectDigest{SHA256: rec.hash, Bytes: int64(len(rec.raw))},
		ArchivedBy: archivedBy,
	}
	if s.Kind == InboxMessage || s.Kind == InboxReadAck {
		m.RecipientUID = s.Recipient.UID
	}
	if s.Kind == InboxReadAck {
		m.MessageUID = s.Message.UID
	}
	if _, err := protocol.CoordArchiveRevisionDir(m); err != nil {
		return protocol.CoordArchiveManifest{}, errArchiveInvalid
	}
	if _, err := protocol.VerifyCoordArchiveObject(m, bytes.NewReader(rec.raw)); err != nil {
		return protocol.CoordArchiveManifest{}, errArchiveInvalid
	}
	return m, nil
}

func identityOf(s Summary) protocol.CoordArchiveIdentity {
	return protocol.CoordArchiveIdentity{Resource: s.Resource, Project: s.Project, Name: s.Name, UID: s.UID}
}

func kindResource(kind Kind) resource {
	for _, item := range resources {
		if item.kind == kind {
			return item
		}
	}
	return resource{}
}

func ruleName(resource string) string {
	switch resource {
	case "taskclaims":
		return "owner-absent"
	case "publishedcontexts":
		return "source-absent"
	case "inboxmessages":
		return "sender-absent-recipient-done"
	case "inboxreadacks":
		return "message-released"
	default:
		return ""
	}
}

func absentEvent(resource string, at time.Time, obs []protocol.CoordArchiveObservation) (protocol.CoordArchiveEvent, error) {
	ev := protocol.CoordArchiveEvent{
		Format: protocol.CoordArchiveEventFormat, State: "absent", At: at.UTC(),
		Rule: ruleName(resource), Sessions: append([]protocol.CoordArchiveObservation(nil), obs...), By: archivedBy,
	}
	if err := ev.Validate(resource); err != nil {
		return protocol.CoordArchiveEvent{}, err
	}
	return ev, nil
}

func presentEvent(resource string, at time.Time, obs []protocol.CoordArchiveObservation) (protocol.CoordArchiveEvent, error) {
	ev := protocol.CoordArchiveEvent{
		Format: protocol.CoordArchiveEventFormat, State: "present", At: at.UTC(),
		Rule: ruleName(resource), Sessions: append([]protocol.CoordArchiveObservation(nil), obs...), By: archivedBy,
	}
	if err := ev.Validate(resource); err != nil {
		return protocol.CoordArchiveEvent{}, err
	}
	return ev, nil
}

func resetEvent(resource string, at time.Time) (protocol.CoordArchiveEvent, error) {
	ev := protocol.CoordArchiveEvent{
		Format: protocol.CoordArchiveEventFormat, State: "reset", At: at.UTC(),
		Rule: ruleName(resource), By: archivedBy,
	}
	if err := ev.Validate(resource); err != nil {
		return protocol.CoordArchiveEvent{}, err
	}
	return ev, nil
}

func ackAbsentEvent(message Summary, at time.Time) (protocol.CoordArchiveEvent, error) {
	return absentEvent("inboxreadacks", at, []protocol.CoordArchiveObservation{{
		Role: "message", Namespace: message.Namespace, Name: message.Name, UID: message.UID, Observed: "message-released",
	}})
}

func historyCode(err error) string {
	var refusal *protocol.HistoryError
	if errors.As(err, &refusal) {
		return refusal.Code
	}
	return ""
}

func deadlineErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func needsCompanionAck(obs []protocol.CoordArchiveObservation) bool {
	for _, item := range obs {
		if item.Role == "recipient" && item.Observed == "acknowledged" {
			return true
		}
	}
	return false
}
