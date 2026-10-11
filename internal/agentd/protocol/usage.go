package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"time"
)

const (
	UsageUnknown           = "Unknown"
	UsageReported          = "Reported"
	UsageInitial           = "InitialManagedInvocation"
	UsageFinalTotal        = "InvocationFinalTotal"
	UsageCLIEstimate       = "ProviderCLIEquivalentEstimate"
	MaxUsageTokens   int64 = 1_000_000_000_000_000
)

// UsageSource is saved before an initial managed invocation. It cannot be
// reconstructed for legacy results from a later pod or configuration.
type UsageSource struct {
	SessionUID     string `json:"sessionUID"`
	PodUID         string `json:"podUID"`
	Provider       string `json:"provider"`
	RequestedModel string `json:"requestedModel"`
	AttemptID      string `json:"attemptID"`
}

// TokenObservation distinguishes an explicit zero from an unavailable count.
type TokenObservation struct {
	State  string `json:"state"`
	Amount *int64 `json:"amount,omitempty"`
}

type CostObservation struct {
	State  string `json:"state"`
	Amount string `json:"amount,omitempty"`
	Unit   string `json:"unit,omitempty"`
	Kind   string `json:"kind"`
}

// UsageObservation is one publisher-reported initial invocation snapshot.
// Internal delegated usage inclusion is unknown; this object is never summed.
type UsageObservation struct {
	Version                  int32            `json:"version"`
	ID                       string           `json:"id"`
	Source                   UsageSource      `json:"source"`
	ObservedAt               time.Time        `json:"observedAt"`
	Coverage                 string           `json:"coverage"`
	CounterSemantics         string           `json:"counterSemantics"`
	ResultState              string           `json:"resultState"`
	InputTokens              TokenObservation `json:"inputTokens"`
	OutputTokens             TokenObservation `json:"outputTokens"`
	CacheReadInputTokens     TokenObservation `json:"cacheReadInputTokens"`
	CacheCreationInputTokens TokenObservation `json:"cacheCreationInputTokens"`
	Cost                     CostObservation  `json:"cost"`
	AggregationScope         string           `json:"aggregationScope"`
	ActualModelState         string           `json:"actualModelState"`
	SubscriptionQuotaState   string           `json:"subscriptionQuotaState"`
	APIBillingState          string           `json:"apiBillingState"`
	LocalResourceState       string           `json:"localResourceState"`
	// These fields are derived by the server from existing budget bindings.
	LogicalTaskUID   string `json:"logicalTaskUID,omitempty"`
	LogicalTaskEpoch string `json:"logicalTaskEpoch,omitempty"`
	WorkerID         string `json:"workerID,omitempty"`
	malformedDigest  string
}

// A usage-only type error must not reject the surrounding liveness heartbeat.
// Keep only its digest; validation converts the unclassified object to Unknown.
func (u *UsageObservation) UnmarshalJSON(raw []byte) error {
	type plain UsageObservation
	var decoded plain
	if err := json.Unmarshal(raw, &decoded); err != nil {
		h := sha256.Sum256(raw)
		*u = UsageObservation{malformedDigest: hex.EncodeToString(h[:])}
		return nil
	}
	*u = UsageObservation(decoded)
	return nil
}

func (u UsageObservation) MalformedDigest() string { return u.malformedDigest }

var usageDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)
var usageDecimal = regexp.MustCompile(`^(0|[1-9][0-9]*)(\.[0-9]*[1-9])?$`)
var usageEpoch = regexp.MustCompile(`^[1-9][0-9]*$`)

func UsageObservationID(source UsageSource) string {
	b, _ := json.Marshal(struct {
		Purpose         string
		Source          UsageSource
		TerminalOrdinal int
	}{"dev-env/initial-usage/v1", source, 0})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (s UsageSource) Valid() bool {
	return ValidTaskBudgetIdentifier(s.SessionUID) && ValidTaskBudgetIdentifier(s.PodUID) &&
		(s.Provider == AgentClaude || s.Provider == AgentCodex) && s.RequestedModel != "" && len(s.RequestedModel) <= 256 &&
		usageDigest.MatchString(s.AttemptID)
}

func (m TokenObservation) Valid() bool {
	return m.State == UsageUnknown && m.Amount == nil || m.State == UsageReported && m.Amount != nil && *m.Amount >= 0 && *m.Amount <= MaxUsageTokens
}

func (m CostObservation) Valid() bool {
	return m.State == UsageUnknown && m.Amount == "" && m.Unit == "" && m.Kind == UsageUnknown ||
		m.State == UsageReported && len(m.Amount) <= 64 && usageDecimal.MatchString(m.Amount) && m.Unit == "USD" && m.Kind == UsageCLIEstimate
}

func (u UsageObservation) Validate() error {
	if u.Version != 1 || !u.Source.Valid() || u.ID != UsageObservationID(u.Source) || u.ObservedAt.IsZero() ||
		u.Coverage != UsageInitial || u.CounterSemantics != UsageFinalTotal || (u.ResultState != "Final" && u.ResultState != "Partial") ||
		!u.InputTokens.Valid() || !u.OutputTokens.Valid() || !u.CacheReadInputTokens.Valid() || !u.CacheCreationInputTokens.Valid() || !u.Cost.Valid() ||
		(u.Source.Provider == AgentCodex && u.Cost.State != UsageUnknown) ||
		u.AggregationScope != UsageUnknown || u.ActualModelState != UsageUnknown || u.SubscriptionQuotaState != UsageUnknown || u.APIBillingState != UsageUnknown || u.LocalResourceState != UsageUnknown {
		return errors.New("initial usage observation is unconfirmed")
	}
	if (u.LogicalTaskUID == "") != (u.LogicalTaskEpoch == "") || (u.LogicalTaskUID == "") != (u.WorkerID == "") ||
		(u.LogicalTaskUID != "" && (!ValidTaskBudgetIdentifier(u.LogicalTaskUID) || !ValidTaskBudgetIdentifier(u.WorkerID) || len(u.LogicalTaskEpoch) > 20 || !usageEpoch.MatchString(u.LogicalTaskEpoch))) {
		return errors.New("initial usage task binding is unconfirmed")
	}
	return nil
}

func UnknownUsage() UsageObservation {
	return UsageObservation{Version: 1, Coverage: UsageInitial, CounterSemantics: UsageFinalTotal, ResultState: "Partial",
		InputTokens: TokenObservation{State: UsageUnknown}, OutputTokens: TokenObservation{State: UsageUnknown},
		CacheReadInputTokens: TokenObservation{State: UsageUnknown}, CacheCreationInputTokens: TokenObservation{State: UsageUnknown},
		Cost: CostObservation{State: UsageUnknown, Kind: UsageUnknown}, AggregationScope: UsageUnknown,
		ActualModelState: UsageUnknown, SubscriptionQuotaState: UsageUnknown, APIBillingState: UsageUnknown, LocalResourceState: UsageUnknown}
}
