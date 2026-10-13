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

// UsageRollup sums the initial invocations of a Session's descendants
// (DESIGN-018). A child's usage stays on the child; the Session's own
// observation is not part of it. Each invocation is counted once, keyed by its
// Session UID and observation ID. A metric is Reported only when coverage is
// Complete and every counted invocation reported it; otherwise it is Unknown,
// with no amount and no partial sum.
type UsageRollup struct {
	Scope                    string                    `json:"scope"`
	Coverage                 string                    `json:"coverage"`
	Sessions                 int32                     `json:"sessions"`
	Invocations              []UsageRollupKey          `json:"invocations"`
	Truncated                bool                      `json:"truncated,omitempty"`
	InputTokens              protocol.TokenObservation `json:"inputTokens"`
	OutputTokens             protocol.TokenObservation `json:"outputTokens"`
	CacheReadInputTokens     protocol.TokenObservation `json:"cacheReadInputTokens"`
	CacheCreationInputTokens protocol.TokenObservation `json:"cacheCreationInputTokens"`
	Cost                     protocol.CostObservation  `json:"cost"`
	BillingBasis             string                    `json:"billingBasis"`
	ObservedModelState       string                    `json:"observedModelState"`
	ObservedModels           []string                  `json:"observedModels,omitempty"`
}

// UsageRollupKey identifies one counted invocation.
type UsageRollupKey struct {
	SessionUID    string `json:"sessionUID"`
	ObservationID string `json:"observationID"`
}
