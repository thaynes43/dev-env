// Package homeretention verifies caller-supplied HOME preservation attestations
// and classifies offline records. It performs no collection or lifecycle action.
package homeretention

import "time"

const (
	Version                  = "home-retention.v1"
	MaxSnapshotBytes         = 4 << 20
	MaxRecords               = 1000
	MaxTrustBytes            = 64 << 10
	MaxTrustedKeys           = 64
	Retention                = 168 * time.Hour
	DefaultWarningLead       = 24 * time.Hour
	DefaultObservationMaxAge = 5 * time.Minute
)

type Snapshot struct {
	Version string   `json:"version"`
	Records []Record `json:"records"`
}

// ExecutorBinding identifies one actual executor attempt. Revisions are absent
// here so late writes cannot silently reset a completed attempt's clock.
type ExecutorBinding struct {
	SessionUID string `json:"sessionUID"`
	PVCUID     string `json:"pvcUID"`
	PodUID     string `json:"podUID"`
	Attempt    string `json:"executorAttempt"`
}

type Binding struct {
	ExecutorBinding
	WriterGeneration uint64 `json:"writerGeneration"`
	HomeRevision     string `json:"homeRevision"`
}

type Completion struct {
	Executor   ExecutorBinding `json:"executor"`
	FinishedAt string          `json:"finishedAt"`
}

type TerminalRecord struct {
	Executor   ExecutorBinding `json:"executor"`
	ObservedAt string          `json:"observedAt"`
	// Only actual_executor_terminal is a clock. Absence/phase are not clocks.
	Source string `json:"source"`
}

type Record struct {
	// UID is the immutable HOME PVCUID, never a display name or caller alias.
	// Legacy UID-only records remain visible but cannot qualify immediately.
	UID     string  `json:"uid"`
	Binding Binding `json:"binding"`
	// State is active, stopped, or unknown. Active always protects the HOME.
	State string `json:"state"`
	Hold  string `json:"hold"`
	// Empty or normal is ordinary work. Named canary, pilot, fixture, and
	// rescue_hold dispositions are protected; unknown values stay in the queue.
	Disposition    string                      `json:"disposition"`
	TaskCompletion *Completion                 `json:"taskCompletion"`
	TerminalRecord *TerminalRecord             `json:"terminalRecord"`
	LatestProofID  string                      `json:"latestProofID"`
	Proof          *Signed[ProofPayload]       `json:"proof"`
	Observation    *Signed[ObservationPayload] `json:"observation"`
}

type Signature struct {
	KeyID string `json:"keyID"`
	Value string `json:"value"`
}

type Signed[T any] struct {
	Payload   T         `json:"payload"`
	Signature Signature `json:"signature"`
}

type Clock struct {
	Source string `json:"source"`
	At     string `json:"at"`
}

type ProofPayload struct {
	Version    string    `json:"version"`
	ID         string    `json:"id"`
	Binding    Binding   `json:"binding"`
	CapturedAt string    `json:"capturedAt"`
	Clock      Clock     `json:"clock"`
	Inventory  Inventory `json:"inventory"`
}

type ObservationPayload struct {
	Version        string  `json:"version"`
	Binding        Binding `json:"binding"`
	ObservedAt     string  `json:"observedAt"`
	Terminal       bool    `json:"terminal"`
	MountsComplete bool    `json:"mountsComplete"`
	// Every active mount must be listed, including other/replacement pods.
	ActiveMountPodUIDs []string `json:"activeMountPodUIDs"`
	LatestProofID      string   `json:"latestProofID"`
}

type Requirements struct {
	// The collector must trace requirements to an existing authoritative decision
	// and its immutable revision. These references do not grant authority.
	Source   string `json:"source"`
	Revision string `json:"revision"`
	// Each value must explicitly be none, required, or unknown.
	Handoff  string `json:"handoff"`
	Result   string `json:"result"`
	History  string `json:"history"`
	Recovery string `json:"recovery"`
}

type Inventory struct {
	Complete     bool         `json:"complete"`
	Repositories []Repository `json:"repositories"`
	Requirements Requirements `json:"requirements"`
	Artifacts    []Artifact   `json:"artifacts"`
}

type Repository struct {
	// Identity is the collector's canonical repository identity, scoped by trust.
	Identity          string     `json:"identity"`
	WorktreesComplete bool       `json:"worktreesComplete"`
	RefsComplete      bool       `json:"refsComplete"`
	StashesComplete   bool       `json:"stashesComplete"`
	FetchSucceeded    bool       `json:"fetchSucceeded"`
	Worktrees         []Worktree `json:"worktrees"`
	Refs              []Ref      `json:"refs"`
	Stashes           []string   `json:"stashes"`
	UnpushedRefs      []string   `json:"unpushedRefs"`
}

type Worktree struct {
	Path string `json:"path"`
	// Every worktree has a distinct HEAD:<path> entry, even on a branch.
	HeadRef             string `json:"headRef"`
	Staged              bool   `json:"staged"`
	Unstaged            bool   `json:"unstaged"`
	Untracked           bool   `json:"untracked"`
	OperationInProgress bool   `json:"operationInProgress"`
	NestedRepository    bool   `json:"nestedRepository"`
	SubmodulesComplete  bool   `json:"submodulesComplete"`
	SubmodulesClean     bool   `json:"submodulesClean"`
}

type Ref struct {
	Name string `json:"name"`
	// Kind is branch, tag, or head. Object preserves annotated tag objects;
	// Commit is the peeled commit. For branch/head Object equals Commit.
	Kind     string                       `json:"kind"`
	Commit   string                       `json:"commit"`
	Object   string                       `json:"object"`
	Receipts []Signed[PublicationPayload] `json:"receipts"`
}

type PublicationPayload struct {
	Version      string  `json:"version"`
	Binding      Binding `json:"binding"`
	Repository   string  `json:"repository"`
	LocalRef     string  `json:"localRef"`
	Commit       string  `json:"commit"`
	Object       string  `json:"object"`
	Remote       string  `json:"remote"`
	RemoteRef    string  `json:"remoteRef"`
	RemoteCommit string  `json:"remoteCommit"`
	RemoteObject string  `json:"remoteObject"`
	// Only authenticated_push or authenticated_remote_query are accepted.
	Method     string `json:"method"`
	VerifiedAt string `json:"verifiedAt"`
}

type Artifact struct {
	ID       string                       `json:"id"`
	Kind     string                       `json:"kind"`
	SHA256   string                       `json:"sha256"`
	Receipts []Signed[DurableCopyPayload] `json:"receipts"`
}

type DurableCopyPayload struct {
	Version     string  `json:"version"`
	Binding     Binding `json:"binding"`
	ArtifactID  string  `json:"artifactID"`
	Kind        string  `json:"kind"`
	SHA256      string  `json:"sha256"`
	Destination string  `json:"destination"`
	Method      string  `json:"method"`
	VerifiedAt  string  `json:"verifiedAt"`
}

// TrustedKey is external policy, never supplied within a snapshot. A collector
// key may sign proof and observation domains. Publisher and durable-copy keys
// must use different public keys and exact external authority scopes.
type TrustedKey struct {
	ID          string `json:"id"`
	Purpose     string `json:"purpose"`
	PublicKey   string `json:"publicKey"`
	Repository  string `json:"repository"`
	Remote      string `json:"remote"`
	Destination string `json:"destination"`
}

type TrustConfig struct {
	Version string       `json:"version"`
	Keys    []TrustedKey `json:"keys"`
}

type Policy struct {
	WarningLead       time.Duration
	ObservationMaxAge time.Duration
}

func DefaultPolicy() Policy {
	return Policy{WarningLead: DefaultWarningLead, ObservationMaxAge: DefaultObservationMaxAge}
}

type Entry struct {
	UID              string   `json:"uid"`
	Binding          Binding  `json:"binding"`
	Status           string   `json:"status"`
	ClockSource      string   `json:"clockSource"`
	CompletedAt      string   `json:"completedAt,omitempty"`
	WarningAt        string   `json:"warningAt,omitempty"`
	Deadline         string   `json:"deadline,omitempty"`
	DecisionRequired bool     `json:"decisionRequired"`
	Reasons          []string `json:"reasons"`
}

type Report struct {
	Version             string  `json:"version"`
	DryRun              bool    `json:"dryRun"`
	DeleteEnabled       bool    `json:"deleteEnabled"`
	EvaluatedAt         string  `json:"evaluatedAt"`
	Retention           string  `json:"retention"`
	WarningLead         string  `json:"warningLead"`
	ObservationMaxAge   string  `json:"observationMaxAge"`
	ImmediateCandidates []Entry `json:"immediateCandidates"`
	// Protected active/special records remain visible here with status protected.
	StoppedWorkQueue []Entry `json:"stoppedWorkQueue"`
}
