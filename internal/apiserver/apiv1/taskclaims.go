package apiv1

import "time"

const TaskClaimsPath = "/v1/task-claims"

type TaskClaimIdentity struct {
	Project    string `json:"project"`
	Repository string `json:"repository"`
	TaskKey    string `json:"taskKey"`
}

type TaskClaimOwner struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	UID       string `json:"uid"`
}

// Owner is mandatory for coordinator creation; a Session caller may omit it.
// Provenance fields are deliberately absent and unknown request fields refused.
type CreateTaskClaimRequest struct {
	Identity TaskClaimIdentity `json:"identity"`
	Owner    *TaskClaimOwner   `json:"owner,omitempty"`
}

type TaskClaimActor struct {
	Kind           string `json:"kind"`
	ServiceAccount string `json:"serviceAccount"`
	PodName        string `json:"podName"`
	PodUID         string `json:"podUID"`
}

type TaskClaimProvenance struct {
	CatalogRevision string     `json:"catalogRevision"`
	RulesRevision   string     `json:"rulesRevision"`
	GitHub          string     `json:"github"`
	DefaultBranch   string     `json:"defaultBranch"`
	InitialPinState string     `json:"initialPinState"`
	ReportedHead    string     `json:"reportedHead,omitempty"`
	ReportedAt      *time.Time `json:"reportedAt,omitempty"`
}

type TaskClaim struct {
	Name       string              `json:"name"`
	UID        string              `json:"uid"`
	Identity   TaskClaimIdentity   `json:"identity"`
	Owner      TaskClaimOwner      `json:"owner"`
	Actor      TaskClaimActor      `json:"actor"`
	Provenance TaskClaimProvenance `json:"provenance"`
	CreatedAt  time.Time           `json:"createdAt"`
	// Observed state is metadata, not stop proof or permission to transfer ownership.
	OwnerState string `json:"ownerState"`
	StopState  string `json:"stopState"`
}

type TaskClaimResult struct {
	Outcome string    `json:"outcome"`
	Claim   TaskClaim `json:"claim"`
}

type TaskClaimList struct {
	Items    []TaskClaim `json:"items"`
	Continue string      `json:"continue,omitempty"`
}
