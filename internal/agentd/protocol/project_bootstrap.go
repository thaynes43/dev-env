package protocol

import "slices"

// ProjectBootstrap is the published form of a private catalog bootstrap
// (DESIGN-006, reason codes): catalog names, states, steps and closed-set
// reason codes only. It never holds a path, a commit or object id, a URL or
// detail text; the private report in the session's home keeps those.
type ProjectBootstrap struct {
	// State is the catalog outcome: Ready, Partial or Failed.
	State string `json:"state"`
	// Reason is one of CatalogReasons, only when State is Failed.
	Reason string `json:"reason,omitempty"`
	// CatalogRevision is the accepted catalog's content digest, the value a
	// task source records.
	CatalogRevision string                       `json:"catalogRevision,omitempty"`
	Repositories    []ProjectBootstrapRepository `json:"repositories,omitempty"`
	Projects        []ProjectBootstrapProject    `json:"projects,omitempty"`
}

// ProjectBootstrapRepository is one catalog repository's reference refresh.
type ProjectBootstrapRepository struct {
	// Name is the repository's catalog name.
	Name string `json:"name"`
	// State is Ready or Preserved.
	State string `json:"state"`
	// Step and Reason say where and why a Preserved refresh stopped.
	Step   string `json:"step,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// ProjectBootstrapProject is one project root's refresh (DESIGN-007).
type ProjectBootstrapProject struct {
	// Name is the project's catalog name.
	Name string `json:"name"`
	// State is Ready or Failed.
	State string `json:"state"`
	// Reason is one of RootReasons when State is Failed.
	Reason string `json:"reason,omitempty"`
	// Repository names the member repository that caused it, when one did;
	// Cause is that repository's reason from RepositoryReasons.
	Repository string `json:"repository,omitempty"`
	Cause      string `json:"cause,omitempty"`
}

// Catalog bootstrap states.
const (
	BootstrapReady   = "Ready"
	BootstrapPartial = "Partial"
	BootstrapFailed  = "Failed"
	// BootstrapPreserved is a repository whose refresh stopped; its files,
	// branches and Git state are kept.
	BootstrapPreserved = "Preserved"
)

// Repository steps: where a reference refresh stopped.
const (
	StepClone   = "Clone"
	StepInspect = "Inspect"
	StepFetch   = "Fetch"
	StepAdvance = "Advance"
)

// Repository reasons (DESIGN-006).
const (
	ReasonCloneFailed             = "CloneFailed"
	ReasonStagingPreserved        = "StagingPreserved"
	ReasonReferenceUncertain      = "ReferenceUncertain"
	ReasonIdentityMismatch        = "IdentityMismatch"
	ReasonGitOperationPresent     = "GitOperationPresent"
	ReasonCatalogUnconfirmed      = "CatalogUnconfirmed"
	ReasonFetchFailed             = "FetchFailed"
	ReasonUntrackedFiles          = "UntrackedFiles"
	ReasonUnsupportedTrackedEntry = "UnsupportedTrackedEntry"
	ReasonIndexBoundExceeded      = "IndexBoundExceeded"
	ReasonWorkingTreeDiffers      = "WorkingTreeDiffers"
	ReasonStagedChanges           = "StagedChanges"
	ReasonNonDefaultBranch        = "NonDefaultBranch"
	ReasonBranchInOtherWorktree   = "BranchInOtherWorktree"
	ReasonLocalOnlyCommits        = "LocalOnlyCommits"
	ReasonChangedDuringProof      = "ChangedDuringProof"
	ReasonAdvanceFailed           = "AdvanceFailed"
	ReasonTimedOut                = "TimedOut"
	ReasonStopped                 = "Stopped"
	ReasonUncertain               = "Uncertain"
)

// Project root reasons (DESIGN-007), beside CatalogUnconfirmed, FetchFailed,
// TimedOut and Stopped above.
const (
	ReasonStateUncertain       = "StateUncertain"
	ReasonReceiptUnconfirmed   = "ReceiptUnconfirmed"
	ReasonRootUnproven         = "RootUnproven"
	ReasonAnchorRemoved        = "AnchorRemoved"
	ReasonRepositoryNotReady   = "RepositoryNotReady"
	ReasonAnchorNotFastForward = "AnchorNotFastForward"
	ReasonRootPrepareFailed    = "RootPrepareFailed"
	ReasonAnchorAddFailed      = "AnchorAddFailed"
	ReasonAnchorUnproven       = "AnchorUnproven"
	ReasonAnchorAdvanceFailed  = "AnchorAdvanceFailed"
	ReasonRootAdvanceFailed    = "RootAdvanceFailed"
	ReasonBudgetReserve        = "BudgetReserve"
)

// Catalog reasons, beside StateUncertain, CatalogUnconfirmed, TimedOut and
// Stopped.
const (
	ReasonConfigInvalid    = "ConfigInvalid"
	ReasonLockUnavailable  = "LockUnavailable"
	ReasonReportUnretained = "ReportUnretained"
	ReasonCatalogInvalid   = "CatalogInvalid"
	ReasonOwnerMismatch    = "OwnerMismatch"
)

// The closed sets. The AgentSession CRD's enums list the same values; the
// API server copies only members (DESIGN-006).
var (
	BootstrapStates           = []string{BootstrapReady, BootstrapPartial, BootstrapFailed}
	BootstrapRepositoryStates = []string{BootstrapReady, BootstrapPreserved}
	BootstrapProjectStates    = []string{BootstrapReady, BootstrapFailed}
	BootstrapSteps            = []string{StepClone, StepInspect, StepFetch, StepAdvance}
	RepositoryReasons         = []string{
		ReasonCloneFailed, ReasonStagingPreserved, ReasonReferenceUncertain, ReasonIdentityMismatch,
		ReasonGitOperationPresent, ReasonCatalogUnconfirmed, ReasonFetchFailed, ReasonUntrackedFiles,
		ReasonUnsupportedTrackedEntry, ReasonIndexBoundExceeded, ReasonWorkingTreeDiffers, ReasonStagedChanges,
		ReasonNonDefaultBranch, ReasonBranchInOtherWorktree, ReasonLocalOnlyCommits, ReasonChangedDuringProof,
		ReasonAdvanceFailed, ReasonTimedOut, ReasonStopped, ReasonUncertain,
	}
	RootReasons = []string{
		ReasonStateUncertain, ReasonReceiptUnconfirmed, ReasonCatalogUnconfirmed, ReasonRootUnproven,
		ReasonAnchorRemoved, ReasonRepositoryNotReady, ReasonFetchFailed, ReasonAnchorNotFastForward,
		ReasonRootPrepareFailed, ReasonAnchorAddFailed, ReasonAnchorUnproven, ReasonAnchorAdvanceFailed,
		ReasonRootAdvanceFailed, ReasonBudgetReserve, ReasonTimedOut, ReasonStopped,
	}
	CatalogReasons = []string{
		ReasonConfigInvalid, ReasonStateUncertain, ReasonLockUnavailable, ReasonReportUnretained,
		ReasonCatalogUnconfirmed, ReasonCatalogInvalid, ReasonOwnerMismatch, ReasonTimedOut, ReasonStopped,
	}
)

// Bounds at the catalog's own limits (projectcatalog.MaxRepositories,
// MaxProjects and ValidName's 63 bytes), which the CRD's MaxItems and
// MaxLength repeat. The API server's tests prove the catalog side and the
// CRD's validation test the schema side, so neither can drift silently.
const (
	MaxBootstrapRepositories = 128
	MaxBootstrapProjects     = 64
	MaxBootstrapNameLength   = 63
)

// InSet reports whether v is one of set.
func InSet(set []string, v string) bool { return slices.Contains(set, v) }
