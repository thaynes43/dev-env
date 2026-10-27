package session

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// commandReservation retains only a digest of the text and the target. The
// request text and identity credentials must never be written to storage.
type commandReservation struct {
	RequestID, SessionName, SessionUID string
	Digest                             [32]byte
	ExpiresAt                          time.Time
}

// commandReceipts is separate from Store so ordinary authentication cannot
// accidentally gain command authority. A false created result never permits
// another execution, even if the reservation has no terminal receipt.
type commandReceipts interface {
	reserveCommand(context.Context, Handle, commandReservation) (created bool, err error)
	readCommand(context.Context, Handle, string) (commandReservation, *apiv1.PortalMessageReceipt, error)
	finishCommand(context.Context, Handle, commandReservation, apiv1.PortalMessageReceipt) error
}

func commandKey(handle Handle, id, purpose string) Handle {
	h := sha256.New()
	_, _ = h.Write([]byte("dev-env/portal-command/v1/" + purpose + "\x00"))
	_, _ = h.Write(handle[:])
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(id)))
	_, _ = h.Write(n[:])
	_, _ = h.Write([]byte(id))
	var key Handle
	copy(key[:], h.Sum(nil))
	return key
}

func validCommandID(id string) bool {
	if len(id) < 16 || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

func (commandReservation) String() string     { return "[portal command reservation]" }
func (r commandReservation) GoString() string { return r.String() }

// Terminal reasons are fixed codes. Never persist provider errors or text.
func validCommandReason(status apiv1.PortalTransportStatus, reason string) bool {
	switch status {
	case apiv1.PortalTransportDelivered:
		return reason == "delivered"
	case apiv1.PortalTransportRefused:
		return reason == "refused"
	case apiv1.PortalTransportUnknown:
		return reason == "unknown"
	default:
		return false
	}
}

func sameCommandReservation(a, b commandReservation) bool {
	return a.RequestID == b.RequestID && a.SessionName == b.SessionName && a.SessionUID == b.SessionUID && a.Digest == b.Digest && a.ExpiresAt.Equal(b.ExpiresAt)
}
func sameCommandReceipt(a, b apiv1.PortalMessageReceipt) bool {
	return a.RequestID == b.RequestID && a.SessionName == b.SessionName && a.SessionUID == b.SessionUID && a.ResolvedPodUID == b.ResolvedPodUID && a.ObservedAt.Equal(b.ObservedAt) && a.TransportStatus == b.TransportStatus && a.Reason == b.Reason
}
