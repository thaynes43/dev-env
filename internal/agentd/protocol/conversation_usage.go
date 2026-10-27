package protocol

import (
	"encoding/json"
	"regexp"
	"time"
)

const (
	// ConversationUsageVersion is the first conversation usage wire version.
	ConversationUsageVersion int32 = 1
	// ConversationCoverage says what the figure covers: the one registered
	// interactive conversation of the Session, read from the provider's own
	// record of it. It is never the headless InitialManagedInvocation, and the
	// two figures are never added.
	ConversationCoverage = "RegisteredConversation"
	// ConversationCumulative says each counter is the running total for that
	// conversation up to ObservedAt. The conversation may keep growing.
	ConversationCumulative = "ConversationCumulative"
	// MaxConversationModels bounds the per-model rows.
	MaxConversationModels = MaxServedModels
	// MaxConversationCount bounds Turns and Skipped.
	MaxConversationCount int64 = 1_000_000_000

	// ConversationFinal means every line was read and counted: nothing skipped,
	// no counter reset. The conversation may still grow, so it is the complete
	// total as of ObservedAt, not the end of the conversation.
	ConversationFinal = "Final"
	// ConversationPartial means some of the record could not be counted. The
	// figure is a lower bound; ReasonCode says why.
	ConversationPartial = "Partial"
	// ConversationUnknown means no figure: nothing is reported, never zero.
	ConversationUnknown = "Unknown"

	// Reason codes. A Final result has none; Partial and Unknown always have one.
	ReasonSkippedRecords   = "skipped_records"     // malformed, oversize or unqualified records were not counted
	ReasonCounterReset     = "counter_reset"       // a cumulative counter went backwards; only the latest run is shown
	ReasonModelSplit       = "model_split_unknown" // several served models and no honest split of cumulative counters
	ReasonMissingCounters  = "missing_counters"    // some records lacked a counter others carried
	ReasonNoUsage          = "no_usage"            // the record held no usage figures
	ReasonNotInteractive   = "not_interactive"     // a headless task: its figure is the initial invocation's
	ReasonUnbound          = "unbound"             // the registration or current pod did not bind the Session
	ReasonUnsupportedAgent = "unsupported_agent"
	ReasonUnqualified      = "unqualified_format" // the record is not in the pinned CLI's format
	ReasonModelUnknown     = "model_unknown"      // no served model could be named
	ReasonTooManyModels    = "too_many_models"    // more served models than a bounded list keeps
	ReasonSourceLimit      = "source_limit"       // the provider record is over the read bound
	ReasonCountOverflow    = "count_overflow"     // a total passed the counter bound
	ReasonDeltaMismatch    = "delta_mismatch"     // a rise in the cumulative total did not equal the provider's per-turn figure
)

var conversationReasons = map[string]bool{
	ReasonSkippedRecords: true, ReasonCounterReset: true, ReasonModelSplit: true, ReasonMissingCounters: true,
	ReasonNoUsage: true, ReasonNotInteractive: true, ReasonUnbound: true, ReasonUnsupportedAgent: true,
	ReasonUnqualified: true, ReasonModelUnknown: true, ReasonTooManyModels: true, ReasonSourceLimit: true, ReasonCountOverflow: true, ReasonDeltaMismatch: true,
}

var conversationReasonShape = regexp.MustCompile(`^[a-z][a-z_]{0,31}$`)

// ConversationModelUsage is one served model's counters over the conversation,
// as the provider reported them. Each counter keeps its own presence: Unknown
// is not zero. Cache counters are their own counters and are never added to
// input. For Codex, input_tokens already includes the cached tokens, so its
// input and cache read overlap; a consumer must not add them, within a provider
// or across providers.
type ConversationModelUsage struct {
	Model                    string           `json:"model"`
	InputTokens              TokenObservation `json:"inputTokens"`
	OutputTokens             TokenObservation `json:"outputTokens"`
	CacheReadInputTokens     TokenObservation `json:"cacheReadInputTokens"`
	CacheCreationInputTokens TokenObservation `json:"cacheCreationInputTokens"`
}

func (m ConversationModelUsage) Valid() bool {
	return ValidUsageModelID(m.Model) && m.InputTokens.Valid() && m.OutputTokens.Valid() && m.CacheReadInputTokens.Valid() && m.CacheCreationInputTokens.Valid()
}

// ConversationUsage is the measured usage of one registered interactive
// conversation. It is separate from UsageObservation, which covers a headless
// task's first invocation, and carries no cost, no requested model and no text.
// Models is empty exactly when ResultState is Unknown.
type ConversationUsage struct {
	Version          int32                    `json:"version"`
	Provider         string                   `json:"provider"`
	ObservedAt       time.Time                `json:"observedAt"`
	Coverage         string                   `json:"coverage"`
	CounterSemantics string                   `json:"counterSemantics"`
	ResultState      string                   `json:"resultState"`
	ReasonCode       string                   `json:"reasonCode,omitempty"`
	Models           []ConversationModelUsage `json:"models"`
	// Turns counts the provider calls that were counted: distinct Claude
	// assistant messages, or Codex token snapshots whose total grew.
	Turns int64 `json:"turns"`
	// Skipped counts records that could not be counted.
	Skipped int64 `json:"skipped"`
}

// UnmarshalJSON keeps a conversation-usage type error from rejecting the
// heartbeat that carries it: a malformed object decodes to the zero value, which
// is not Valid, and the operator ignores it.
func (u *ConversationUsage) UnmarshalJSON(raw []byte) error {
	type plain ConversationUsage
	var decoded plain
	if err := json.Unmarshal(raw, &decoded); err != nil {
		*u = ConversationUsage{}
		return nil
	}
	*u = ConversationUsage(decoded)
	return nil
}

// UnknownConversationUsage is the figure when nothing could be measured.
func UnknownConversationUsage(provider, reason string, observed time.Time) ConversationUsage {
	return ConversationUsage{Version: ConversationUsageVersion, Provider: provider, ObservedAt: observed.UTC(), Coverage: ConversationCoverage,
		CounterSemantics: ConversationCumulative, ResultState: ConversationUnknown, ReasonCode: reason, Models: []ConversationModelUsage{}}
}

// Valid bounds an observation like UsageObservation.Validate: fixed words, at
// most MaxConversationModels strictly ordered rows with valid ids and counters,
// bounded counts, and a reason exactly where the result is not Final.
func (u ConversationUsage) Valid() bool {
	if u.Version != ConversationUsageVersion || (u.Provider != AgentClaude && u.Provider != AgentCodex) || u.ObservedAt.IsZero() ||
		u.Coverage != ConversationCoverage || u.CounterSemantics != ConversationCumulative ||
		u.Turns < 0 || u.Turns > MaxConversationCount || u.Skipped < 0 || u.Skipped > MaxConversationCount ||
		len(u.Models) > MaxConversationModels {
		return false
	}
	if u.ReasonCode != "" && (!conversationReasonShape.MatchString(u.ReasonCode) || !conversationReasons[u.ReasonCode]) {
		return false
	}
	for i, m := range u.Models {
		if !m.Valid() || (i > 0 && u.Models[i-1].Model >= m.Model) {
			return false
		}
	}
	switch u.ResultState {
	case ConversationFinal:
		return u.ReasonCode == "" && len(u.Models) > 0 && u.Skipped == 0
	case ConversationPartial:
		return u.ReasonCode != "" && len(u.Models) > 0
	case ConversationUnknown:
		return u.ReasonCode != "" && len(u.Models) == 0 && u.Turns == 0
	}
	return false
}
