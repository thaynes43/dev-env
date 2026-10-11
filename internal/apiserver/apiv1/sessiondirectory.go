package apiv1

import "time"

const SessionDirectoryPath = "/v1/session-directory"

// These views expose only the directory's deliberately whitelisted metadata.
type DirectoryCurrent struct {
	SourceState           string            `json:"sourceState"`
	PlacementState        string            `json:"placementState"`
	ObservedAt            time.Time         `json:"observedAt"`
	SourceResourceVersion string            `json:"sourceResourceVersion,omitempty"`
	Witness               *DirectoryWitness `json:"witness,omitempty"`
	StopState             string            `json:"stopState"`
}
type SessionDirectory struct {
	Name            string            `json:"name"`
	UID             string            `json:"uid"`
	ResourceVersion string            `json:"resourceVersion"`
	Original        DirectoryOriginal `json:"original"`
	Retained        DirectoryRetained `json:"retained"`
	Current         DirectoryCurrent  `json:"current"`
}

// Directory metadata is projected by the server from bounded accepted records.
// These wire types deliberately import no Kubernetes object or registration.
type DirectoryOriginal struct {
	Version         int                `json:"version"`
	Source          TaskClaimOwner     `json:"source"`
	SourceCreatedAt time.Time          `json:"sourceCreatedAt"`
	Project         string             `json:"project"`
	Repository      string             `json:"repository"`
	Base            string             `json:"base"`
	Parent          string             `json:"parent"`
	TaskKey         string             `json:"taskKey"`
	Request         DirectoryRequest   `json:"request"`
	Revisions       DirectoryRevisions `json:"revisions"`
}

type DirectoryRequest struct {
	Provider string                  `json:"provider"`
	CLI      string                  `json:"cli"`
	Model    string                  `json:"model"`
	Effort   string                  `json:"effort"`
	Local    *DirectoryLocalProvider `json:"local,omitempty"`
}

type DirectoryLocalProvider struct {
	Backend                   string `json:"backend"`
	ActualModelState          string `json:"actualModelState"`
	BackendReadinessState     string `json:"backendReadinessState"`
	GPUReadinessState         string `json:"gpuReadinessState"`
	HouseholdReservationState string `json:"householdReservationState"`
	Pool                      string `json:"pool"`
	RequestedModel            string `json:"requestedModel"`
	RuntimeAdapter            string `json:"runtimeAdapter"`
}

type DirectoryCommonSource struct {
	Namespace       string `json:"namespace"`
	Name            string `json:"name"`
	Key             string `json:"key"`
	UID             string `json:"uid"`
	ResourceVersion string `json:"resourceVersion"`
	SHA256          string `json:"sha256"`
}

type DirectoryCommon struct {
	State          string                 `json:"state"`
	Revision       string                 `json:"revision,omitempty"`
	SnapshotDigest string                 `json:"snapshotDigest,omitempty"`
	Common         *DirectoryCommonSource `json:"common,omitempty"`
	Header         *DirectoryCommonSource `json:"header,omitempty"`
}

type DirectoryRevisions struct {
	CanaryPolicy           string          `json:"canaryPolicy"`
	CaptureOutput          bool            `json:"captureOutput"`
	CatalogUID             string          `json:"catalogUID"`
	CatalogResourceVersion string          `json:"catalogResourceVersion"`
	Catalog                string          `json:"catalog"`
	Rules                  string          `json:"rules"`
	Template               string          `json:"template"`
	Image                  string          `json:"image"`
	ProjectSnapshot        string          `json:"projectSnapshot"`
	Common                 DirectoryCommon `json:"common"`
}

type DirectoryWitness struct {
	SourceGeneration       int64                `json:"sourceGeneration"`
	SourceResourceVersion  string               `json:"sourceResourceVersion"`
	ObservedAt             time.Time            `json:"observedAt"`
	PodName                string               `json:"podName"`
	PodUID                 string               `json:"podUID"`
	Node                   string               `json:"node"`
	Template               string               `json:"template"`
	Image                  string               `json:"image"`
	PodPhase               string               `json:"podPhase"`
	Ready                  bool                 `json:"ready"`
	ReportedState          string               `json:"reportedState"`
	DeclaredConversationID string               `json:"declaredConversationID"`
	ReportedHeartbeatAt    *time.Time           `json:"reportedHeartbeatAt,omitempty"`
	ReportedTaskFinishedAt *time.Time           `json:"reportedTaskFinishedAt,omitempty"`
	ReportedTask           *DirectoryTaskReport `json:"reportedTask,omitempty"`
}

type DirectoryTaskReport struct {
	ExitCode   int32      `json:"exitCode"`
	FinishedAt *time.Time `json:"finishedAt"`
	TimedOut   bool       `json:"timedOut"`
	Subtype    string     `json:"subtype"`
	IsError    bool       `json:"isError"`
}

type DirectoryRetained struct {
	LastWitness      *DirectoryWitness `json:"lastWitness,omitempty"`
	SourceState      string            `json:"sourceState"`
	ObservedAt       *time.Time        `json:"observedAt"`
	StopState        string            `json:"stopState"`
	ActualModelState string            `json:"actualModelState"`
	UsageState       string            `json:"usageState"`
	CostState        string            `json:"costState"`
}

type SessionDirectoryList struct {
	Items           []SessionDirectory `json:"items"`
	ResourceVersion string             `json:"resourceVersion"`
	Continue        string             `json:"continue,omitempty"`
}
type DirectoryContextReference struct {
	Name            string                    `json:"name"`
	UID             string                    `json:"uid"`
	ResourceVersion string                    `json:"resourceVersion"`
	Kind            string                    `json:"kind"`
	Key             string                    `json:"key"`
	ContentSHA256   string                    `json:"contentSHA256"`
	ByteCount       int32                     `json:"byteCount"`
	Origin          string                    `json:"origin"`
	Completeness    string                    `json:"completeness"`
	PublishedAt     time.Time                 `json:"publishedAt"`
	DeclaredThread  *DirectoryThreadAssertion `json:"declaredThread,omitempty"`
}

// An assertion retains publisher provenance; it proves no provider restoration.
type DirectoryThreadAssertion struct {
	ConversationID  string    `json:"conversationID"`
	Invocation      string    `json:"invocation"`
	SourceNamespace string    `json:"sourceNamespace"`
	SourceUID       string    `json:"sourceUID"`
	PodName         string    `json:"podName"`
	PodUID          string    `json:"podUID"`
	PublishedAt     time.Time `json:"publishedAt"`
}
type DirectoryContextReferences struct {
	Items                    []DirectoryContextReference `json:"items"`
	DirectoryResourceVersion string                      `json:"directoryResourceVersion"`
	Continue                 string                      `json:"continue,omitempty"`
}
