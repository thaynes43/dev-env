package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/inbox"
)

// archivedInboxMessage reads one complete, verified archived InboxMessage. It
// deliberately does not join a sender or recipient Session: those identities
// and all attribution are immutable message data.
func (s *Server) archivedInboxMessage(ctx context.Context, name string) (*coord.InboxMessage, time.Time, error) {
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
	result, err := s.CoordinationArchive.Get(ctx, protocol.CoordArchiveGetQuery{Resource: "inboxmessages", Name: name})
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
	if manifest.Resource != "inboxmessages" || manifest.Kind != "InboxMessage" || manifest.APIVersion != protocol.CoordArchiveAPIVersion || manifest.Namespace != s.Inbox.Namespace || manifest.Name != name || result.Manifest.ArchivedAt.IsZero() || result.Manifest.ArchivedAt.Location() != time.UTC {
		return nil, time.Time{}, inboxArchiveUnavailable()
	}
	var message coord.InboxMessage
	if json.Unmarshal(canonical, &message) != nil || message.APIVersion != manifest.APIVersion || message.Kind != manifest.Kind || message.Namespace != manifest.Namespace || message.Name != manifest.Name || string(message.UID) != manifest.UID || message.ResourceVersion != manifest.ResourceVersion || message.Spec.Project != manifest.Project || message.Spec.Recipient.UID != manifest.RecipientUID || message.Name != inbox.MessageName(message.Spec.Sender, message.Spec.Key) || !inbox.ValidMessage(message.Spec) || message.CreationTimestamp.IsZero() || !message.DeletionTimestamp.IsZero() {
		return nil, time.Time{}, inboxArchiveUnavailable()
	}
	if ctx.Err() != nil {
		return nil, time.Time{}, inboxArchiveUnavailable()
	}
	return &message, result.Manifest.ArchivedAt, nil
}

func inboxArchiveUnavailable() error {
	return newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "coordination archive unavailable")
}
