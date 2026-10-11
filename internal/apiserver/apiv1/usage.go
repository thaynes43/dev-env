package apiv1

import (
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// UsageObservation carries explicit presence and publisher provenance. The
// deprecated unqualified Usage field is never emitted by new API responses.
type UsageObservation = protocol.UsageObservation

type UsageConflict struct {
	State          string    `json:"state"`
	Reason         string    `json:"reason"`
	IncomingSHA256 string    `json:"incomingSHA256"`
	ObservedAt     time.Time `json:"observedAt"`
}
