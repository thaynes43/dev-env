package apiserver

import (
	"context"
	"net/http"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// archivedInboxAckRetry confirms an already-stored archived acknowledgement.
// It is deliberately read-only: an archived message never reaches Inbox.Ack.
func (s *Server) archivedInboxAckRetry(ctx context.Context, c *caller, m *coord.InboxMessage) (int, any, error) {
	a, archivedAt, err := s.archivedInboxReadAck(ctx, m)
	if err != nil {
		return 0, nil, err
	}
	// Recheck the original recipient after shelf I/O. This cannot substitute a
	// resumed Pod or Session for the immutable archived recipient or actor.
	if err := s.inboxReadFence(ctx, c, m.Spec.Project, &m.Spec.Recipient); err != nil {
		return 0, nil, err
	}
	view := inboxAckView(a)
	view.Archived, view.ArchivedAt = true, &archivedAt
	return http.StatusOK, apiv1.InboxAckResult{Outcome: "Confirmed", Ack: view}, nil
}
