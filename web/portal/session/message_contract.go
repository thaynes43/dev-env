package session

import (
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/web/portal"
)

// NewCommandForms constructs an explicitly configured source-only send seam.
// Production does not call it; activation requires separately approved scope.
func NewCommandForms(engine *Engine, authorizer *CurrentAuthorizer, store *PostgresStore, tokenPath string, ca []byte, now func() time.Time) (SendForms, error) {
	if engine == nil || authorizer == nil || store == nil || store.pool == nil || authorizer.client == nil || authorizer.now == nil || now == nil || engine.config.Store != store || authorizer.store != store || engine.config.Verifier == nil || authorizer.issuer != engine.config.Verifier.issuer {
		return nil, portal.ErrUnavailable
	}
	transport, err := newCommandClient(tokenPath, ca, now, nil)
	if err != nil {
		return nil, err
	}
	return &commandHandler{engine: engine, authorizer: authorizer, receipts: store, transport: transport, render: renderCommandReceipt, now: now}, nil
}

// renderCommandReceipt maps wire transport state and static reason codes to
// the UI's receipt contract. It never forwards text or provider errors.
func renderCommandReceipt(r apiv1.PortalMessageReceipt) ([]byte, error) {
	status := "Unknown"
	switch r.TransportStatus {
	case apiv1.PortalTransportDelivered:
		status = "Delivered"
	case apiv1.PortalTransportRefused:
		status = "Refused"
	case apiv1.PortalTransportPending:
		status = "Pending"
	case apiv1.PortalTransportUnknown:
		status = "Unknown"
	}
	return portal.RenderReceipt(portal.ProductReceipt{RequestID: r.RequestID, Session: r.SessionName, SessionUID: r.SessionUID, ResolvedPodUID: r.ResolvedPodUID, TransportStatus: status, ReasonCode: r.Reason, ObservedAt: r.ObservedAt})
}
