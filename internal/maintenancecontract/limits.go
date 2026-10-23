package maintenancecontract

import "time"

// Proposed source-contract defaults. They are fixture bounds for this offline
// checker, not production SLOs, alert policy, or a change to deployed limits.
const (
	// MaxDocumentBytes is the proposed maximum size of one JSON document.
	MaxDocumentBytes = 64 * 1024
	// MaxEntries is the proposed maximum for services, predicates, and evidence.
	MaxEntries = 32
	// MaxIdentifierBytes is the proposed maximum length of one identifier.
	MaxIdentifierBytes = 128
	// MaxURLBytes is the proposed maximum length of one URL.
	MaxURLBytes = 512
	// MaxLabels is the proposed maximum number of labels on one predicate.
	MaxLabels = 8
	// MaxCounter is the proposed maximum for a snapshot counter field.
	MaxCounter = 10000
)

// Clock and Activity bounds. MaxStaleness and ClockTolerance are proposed
// source-contract defaults. The Activity durations are existing implemented
// constraints and are not changed here.
const (
	// MaxStaleness is the proposed maximum owner-observation age.
	MaxStaleness = 120 * time.Second
	// ClockTolerance is the proposed allowance for a timestamp ahead of the
	// injected clock. Deadlines and expiries may be further ahead than this.
	ClockTolerance = 5 * time.Second
	// DefaultActivityTTL is the existing Activity default. Explicit envelope
	// expiry is not rewritten to it.
	DefaultActivityTTL = 45 * time.Minute
	// ActivityAPIMaximum is the existing Activity maximum.
	ActivityAPIMaximum = 8 * time.Hour
	// ClusterScopeMaximum is the existing Activity cap for scope cluster.
	// This contract rejects cluster-wide deferral matchers, so a named-service
	// window is not silently shortened to this cap.
	ClusterScopeMaximum = 2 * time.Hour
)

const (
	// ContractVersion is the required envelope version.
	ContractVersion = "upgrade-shepherd-envelope/v1"
	// RoleVersion is the required role-contract version.
	RoleVersion = "upgrade-shepherd-role/v1"
	// DecisionCandidate means the supplied unverified input meets the contract.
	DecisionCandidate = "candidate"
	// DecisionRespond means the supplied evidence requires a response.
	DecisionRespond = "respond"
	// DecisionInsufficient means the supplied evidence cannot prove a match.
	DecisionInsufficient = "insufficient-evidence"
	// OfflineEvidenceUnverified marks every result as unverified input.
	OfflineEvidenceUnverified = "unverified"
	// ProvenanceUnknown is the retained value for missing actual provenance.
	ProvenanceUnknown = "Unknown"
)

// Notice is included in every result. A candidate is not operational deferral.
const Notice = "Offline evidence cannot authorize a runtime decision. A candidate only means the supplied unverified input meets this source contract. It is not alert deferral, alert suppression, or operational authorization."
