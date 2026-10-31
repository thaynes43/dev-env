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

// UsageRollup sums the initial invocations of a Session's retained
// descendants (DESIGN-018): the descendant Sessions that still exist when it is
// read. Reaped and compute-reclaimed Sessions count while retained. A deleted
// Session is not counted, so Scope is RetainedDescendants and Coverage describes
// those Sessions only. A child's
// usage stays on the child; the Session's own observation is not part of it.
// Each invocation is counted once, keyed by its Session UID and observation ID.
// A metric is Reported only when coverage is Complete, every counted
// invocation reported it and the total stays within the token bound;
// otherwise it is Unknown, with no amount and no partial sum. Counted is the
// number of invocations counted. Invocations lists their keys; it is left out
// for a coordinator, which may read only its direct children.
type UsageRollup struct {
	Scope                    string                    `json:"scope"`
	Coverage                 string                    `json:"coverage"`
	Sessions                 int32                     `json:"sessions"`
	Counted                  int32                     `json:"counted"`
	Invocations              []UsageRollupKey          `json:"invocations,omitempty"`
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

// UsageRollupScope is the only rollup scope: the descendants that still exist.
const UsageRollupScope = "RetainedDescendants"

// UsageRollupKey identifies one counted invocation.
type UsageRollupKey struct {
	SessionUID    string `json:"sessionUID"`
	ObservationID string `json:"observationID"`
}

const (
	// UsageCaptureHeader opts a single Session read into the receipt view.
	UsageCaptureHeader   = "Dev-Env-Usage-Capture"
	UsageCaptureDirectV1 = "direct-v1"
)

// CapturedUsage is the opted-in receipt view. Available false with no receipts
// is not a claim of zero usage.
type CapturedUsage struct {
	Available bool                   `json:"available"`
	Reason    string                 `json:"reason,omitempty"`
	Scope     string                 `json:"scope,omitempty"`
	Coverage  string                 `json:"coverage,omitempty"`
	Retention string                 `json:"retention,omitempty"`
	Overflow  bool                   `json:"overflow,omitempty"`
	Receipts  []CapturedUsageReceipt `json:"receipts,omitempty"`
}

// CapturedUsageReceipt is one copied initial observation.
type CapturedUsageReceipt struct {
	Key         string            `json:"key"`
	ParentUID   string            `json:"parentUID"`
	ChildName   string            `json:"childName,omitempty"`
	CapturedAt  time.Time         `json:"capturedAt"`
	Observation *UsageObservation `json:"observation,omitempty"`
}
