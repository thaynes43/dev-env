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

const maxInboxArchiveListBytes = 4 << 20

func (s *Server) listArchivedInbox(ctx context.Context, c *caller, project string, recipient *coord.SessionIdentity, limit int64, rawContinue string) (int, any, error) {
	recipientUID := ""
	if recipient != nil {
		recipientUID = recipient.UID
	}
	scope := contextArchiveListCursor{Route: apiv1.InboxPath, Project: project, Recipient: recipientUID, Limit: limit}
	after, err := decodeContextArchiveListCursor(rawContinue, scope)
	if err != nil {
		return 0, nil, err
	}
	query := protocol.CoordArchiveListQuery{Resource: "inboxmessages", Project: project, RecipientUID: recipientUID, Limit: int(limit), After: after}
	if query.Validate() != nil {
		return 0, nil, badRequest("invalid archived inbox cursor")
	}
	lister, ok := s.CoordinationArchive.(CoordinationArchiveLister)
	if !ok || lister == nil || ctx.Err() != nil {
		return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "coordination archive unavailable")
	}
	release, err := s.coordinationArchiveSlot(ctx)
	if err != nil {
		return 0, nil, err
	}
	defer release()
	result, err := lister.List(ctx, query)
	if err != nil || ctx.Err() != nil || len(result.Items) > int(limit) {
		return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "coordination archive unavailable")
	}
	envelope, err := protocol.CanonicalJSON(result)
	if err != nil || len(envelope) > maxInboxArchiveListBytes {
		return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "coordination archive unavailable")
	}
	if result.Next != "" {
		nextQuery := query
		nextQuery.After = result.Next
		nextName, nextErr := protocol.CoordArchiveListCursorName(nextQuery)
		if nextErr != nil {
			return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "coordination archive unavailable")
		}
		if after != "" {
			afterName, afterErr := protocol.CoordArchiveListCursorName(query)
			if afterErr != nil || nextName >= afterName {
				return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "coordination archive unavailable")
			}
		}
	}
	items := make([]apiv1.InboxMetadata, 0, len(result.Items))
	for _, item := range result.Items {
		if item.Error == protocol.HistoryErrCorrupt {
			if !protocol.ValidKubeUID(item.UID) || item.Manifest != nil || len(item.Object) != 0 {
				return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "coordination archive unavailable")
			}
			continue
		}
		if item.Error != "" || item.Manifest == nil || !protocol.ValidKubeUID(item.UID) {
			return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "coordination archive unavailable")
		}
		message, archivedAt, verifyErr := s.archivedInboxMetadata(*item.Manifest, item.Object)
		if verifyErr != nil || item.UID != string(message.UID) || message.Spec.Project != project || (recipient != nil && message.Spec.Recipient != *recipient) {
			return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "coordination archive unavailable")
		}
		view := inboxMetadata(message)
		view.Archived, view.ArchivedAt = true, &archivedAt
		items = append(items, view)
	}
	if err := s.inboxReadFence(ctx, c, project, recipient); err != nil {
		return 0, nil, err
	}
	if ctx.Err() != nil {
		return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "coordination archive unavailable")
	}
	next, err := encodeContextArchiveListCursor(result.Next, scope)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, apiv1.InboxList{Items: items, Continue: next}, nil
}

func (s *Server) archivedInboxMetadata(stored protocol.CoordArchiveStoredManifest, object []byte) (*coord.InboxMessage, time.Time, error) {
	canonical, err := protocol.CanonicalJSON(json.RawMessage(object))
	if err != nil || !bytes.Equal(canonical, object) {
		return nil, time.Time{}, errors.New("invalid metadata projection")
	}
	var wire struct {
		Spec map[string]json.RawMessage `json:"spec"`
	}
	if json.Unmarshal(object, &wire) != nil || wire.Spec == nil || wire.Spec["text"] != nil {
		return nil, time.Time{}, errors.New("metadata projection carries text")
	}
	manifest := stored.CoordArchiveManifest
	if _, err := protocol.CoordArchiveRevisionDir(manifest); err != nil || manifest.Resource != "inboxmessages" || manifest.Kind != "InboxMessage" || manifest.APIVersion != protocol.CoordArchiveAPIVersion || manifest.Namespace != s.Inbox.Namespace || stored.ArchivedAt.IsZero() || stored.ArchivedAt.Location() != time.UTC {
		return nil, time.Time{}, errors.New("invalid coordination archive manifest")
	}
	var message coord.InboxMessage
	if json.Unmarshal(object, &message) != nil || message.APIVersion != manifest.APIVersion || message.Kind != manifest.Kind || message.Namespace != manifest.Namespace || message.Name != manifest.Name || string(message.UID) != manifest.UID || message.ResourceVersion != manifest.ResourceVersion || message.Spec.Project != manifest.Project || message.Spec.Recipient.UID != manifest.RecipientUID || message.Name != inbox.MessageName(message.Spec.Sender, message.Spec.Key) || !inbox.ValidArchiveMetadata(message.Spec) || message.CreationTimestamp.IsZero() || !message.DeletionTimestamp.IsZero() {
		return nil, time.Time{}, errors.New("invalid coordination archive inbox metadata")
	}
	return &message, stored.ArchivedAt, nil
}
