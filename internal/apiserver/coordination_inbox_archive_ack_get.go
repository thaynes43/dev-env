package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/inbox"
)

// archivedInboxReadAck reads the full immutable acknowledgement for an
// already-authorized message. It never derives actor attribution from the
// current caller or joins a Session.
func (s *Server) archivedInboxReadAck(ctx context.Context, message *coord.InboxMessage) (*coord.InboxReadAck, time.Time, error) {
	if ctx.Err() != nil {
		return nil, time.Time{}, inboxArchiveUnavailable()
	}
	release, err := s.coordinationArchiveSlot(ctx)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer release()
	if ctx.Err() != nil {
		return nil, time.Time{}, inboxArchiveUnavailable()
	}
	name := inbox.AckName(string(message.UID), message.Spec.Recipient.UID)
	result, err := s.CoordinationArchive.Get(ctx, protocol.CoordArchiveGetQuery{Resource: "inboxreadacks", Name: name})
	if ctx.Err() != nil {
		return nil, time.Time{}, inboxArchiveUnavailable()
	}
	if err != nil {
		var refusal *protocol.HistoryError
		if errors.As(err, &refusal) && refusal.Code == protocol.HistoryErrNotFound {
			return nil, time.Time{}, inboxHidden()
		}
		return nil, time.Time{}, inboxArchiveUnavailable()
	}
	canonical, err := protocol.VerifyCoordArchiveObject(result.Manifest.CoordArchiveManifest, bytes.NewReader(result.Object))
	if err != nil || !bytes.Equal(canonical, result.Object) {
		return nil, time.Time{}, inboxArchiveUnavailable()
	}
	manifest := result.Manifest.CoordArchiveManifest
	if manifest.Resource != "inboxreadacks" || manifest.Kind != "InboxReadAck" || manifest.APIVersion != protocol.CoordArchiveAPIVersion || manifest.Namespace != s.Inbox.Namespace || manifest.Name != name || manifest.Project != message.Spec.Project || manifest.RecipientUID != message.Spec.Recipient.UID || manifest.MessageUID != string(message.UID) || result.Manifest.ArchivedAt.IsZero() || result.Manifest.ArchivedAt.Location() != time.UTC {
		return nil, time.Time{}, inboxArchiveUnavailable()
	}
	var ack coord.InboxReadAck
	if json.Unmarshal(canonical, &ack) != nil || ack.APIVersion != manifest.APIVersion || ack.Kind != manifest.Kind || ack.Namespace != manifest.Namespace || ack.Name != manifest.Name || string(ack.UID) != manifest.UID || ack.ResourceVersion != manifest.ResourceVersion || ack.Name != name || !inbox.ValidAck(ack.Spec) || ack.Spec.Project != message.Spec.Project || ack.Spec.MessageName != message.Name || ack.Spec.MessageUID != string(message.UID) || ack.Spec.Recipient != message.Spec.Recipient || ack.CreationTimestamp.IsZero() || !ack.DeletionTimestamp.IsZero() {
		return nil, time.Time{}, inboxArchiveUnavailable()
	}
	if ctx.Err() != nil {
		return nil, time.Time{}, inboxArchiveUnavailable()
	}
	return &ack, result.Manifest.ArchivedAt, nil
}
