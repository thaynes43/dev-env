package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// SessionDirectory retains metadata without a source owner, expiry or content.
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
type SessionDirectory struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              SessionDirectorySpec `json:"spec"`
	// +optional
	Status SessionDirectoryStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type SessionDirectoryList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SessionDirectory `json:"items"`
}

// +kubebuilder:validation:MaxLength=256
type DirectoryText string

// +kubebuilder:validation:MaxLength=512
type DirectoryImage string

// +kubebuilder:validation:MaxLength=64
// +kubebuilder:validation:Pattern=`^[a-f0-9]{64}$`
type DirectoryDigest string

type DirectoryRequest struct {
	// +kubebuilder:validation:Enum=claude;codex;local;Unknown
	Provider string        `json:"provider"`
	CLI      DirectoryText `json:"cli"`
	Model    DirectoryText `json:"model"`
	Effort   DirectoryText `json:"effort"`
	// Local is explicit metadata; the runtime adapter stays disabled.
	// +optional
	Local *DirectoryLocalProvider `json:"local,omitempty"`
}

type DirectoryLocalProvider struct {
	// Backend is an explicit declaration, never inferred from OpenCode.
	// +kubebuilder:validation:Enum=local-model
	Backend string `json:"backend"`
	// +kubebuilder:validation:Enum=Unknown
	ActualModelState string `json:"actualModelState"`
	// +kubebuilder:validation:Enum=Unknown
	BackendReadinessState string `json:"backendReadinessState"`
	// +kubebuilder:validation:Enum=Unknown
	GPUReadinessState string `json:"gpuReadinessState"`
	// +kubebuilder:validation:Enum=Unknown
	HouseholdReservationState string        `json:"householdReservationState"`
	Pool                      DirectoryText `json:"pool"`
	RequestedModel            DirectoryText `json:"requestedModel"`
	// +kubebuilder:validation:Enum=Disabled
	RuntimeAdapter string `json:"runtimeAdapter"`
}

type DirectoryCommonSource struct {
	Namespace       DirectoryText   `json:"namespace"`
	Name            DirectoryText   `json:"name"`
	Key             DirectoryText   `json:"key"`
	UID             DirectoryText   `json:"uid"`
	ResourceVersion DirectoryText   `json:"resourceVersion"`
	SHA256          DirectoryDigest `json:"sha256"`
}

// +kubebuilder:validation:XValidation:rule="self.state == 'Unknown' ? !has(self.revision) && !has(self.snapshotDigest) && !has(self.common) && !has(self.header) : has(self.revision) && has(self.snapshotDigest) && has(self.common) && has(self.header)",message="common metadata requires the original complete binding"
type DirectoryCommon struct {
	// +kubebuilder:validation:Enum=Unknown;Bound
	State string `json:"state"`
	// +optional
	Revision DirectoryDigest `json:"revision,omitempty"`
	// +optional
	SnapshotDigest DirectoryDigest `json:"snapshotDigest,omitempty"`
	// +optional
	Common *DirectoryCommonSource `json:"common,omitempty"`
	// +optional
	Header *DirectoryCommonSource `json:"header,omitempty"`
}

type DirectoryRevisions struct {
	CanaryPolicy           DirectoryDigest `json:"canaryPolicy"`
	CaptureOutput          bool            `json:"captureOutput"`
	CatalogUID             DirectoryText   `json:"catalogUID"`
	CatalogResourceVersion DirectoryText   `json:"catalogResourceVersion"`
	Catalog                DirectoryDigest `json:"catalog"`
	Rules                  DirectoryDigest `json:"rules"`
	Template               DirectoryText   `json:"template"`
	Image                  DirectoryImage  `json:"image"`
	ProjectSnapshot        DirectoryDigest `json:"projectSnapshot"`
	Common                 DirectoryCommon `json:"common"`
}

// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="original directory identity and accepted request/revisions are immutable"
// +kubebuilder:validation:XValidation:rule="(self.request.provider == 'local') == has(self.request.local)",message="local provider metadata must be explicitly typed"
// +kubebuilder:validation:XValidation:rule="!has(self.request.local) || self.request.local.requestedModel == self.request.model",message="local requested model must equal the exact accepted request"
type SessionDirectorySpec struct {
	// +kubebuilder:validation:Enum=1
	Version         int             `json:"version"`
	Source          SessionIdentity `json:"source"`
	SourceCreatedAt metav1.Time     `json:"sourceCreatedAt"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`
	Project    string        `json:"project"`
	Repository DirectoryText `json:"repository"`
	Base       DirectoryText `json:"base"`
	// +kubebuilder:validation:MaxLength=317
	Parent    string             `json:"parent"`
	TaskKey   DirectoryText      `json:"taskKey"`
	Request   DirectoryRequest   `json:"request"`
	Revisions DirectoryRevisions `json:"revisions"`
}

// DirectoryWitness separates owned lifecycle facts from agent reports. It is
// neither a stop receipt nor proof of actual inference or native restoration.
type DirectoryWitness struct {
	// +kubebuilder:validation:Minimum=1
	SourceGeneration      int64         `json:"sourceGeneration"`
	SourceResourceVersion DirectoryText `json:"sourceResourceVersion"`
	ObservedAt            metav1.Time   `json:"observedAt"`
	PodName               DirectoryText `json:"podName"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	PodUID   string         `json:"podUID"`
	Node     DirectoryText  `json:"node"`
	Template DirectoryText  `json:"template"`
	Image    DirectoryImage `json:"image"`
	// +kubebuilder:validation:Enum=Pending;Running;Succeeded;Failed;Unknown
	PodPhase               string        `json:"podPhase"`
	Ready                  bool          `json:"ready"`
	ReportedState          DirectoryText `json:"reportedState"`
	DeclaredConversationID DirectoryText `json:"declaredConversationID"`
	// +optional
	ReportedHeartbeatAt *metav1.Time `json:"reportedHeartbeatAt,omitempty"`
	// +optional
	ReportedTaskFinishedAt *metav1.Time `json:"reportedTaskFinishedAt,omitempty"`
	// +optional
	ReportedTask *DirectoryTaskReport `json:"reportedTask,omitempty"`
}

type DirectoryTaskReport struct {
	ExitCode   int32         `json:"exitCode"`
	FinishedAt metav1.Time   `json:"finishedAt"`
	TimedOut   bool          `json:"timedOut"`
	Subtype    DirectoryText `json:"subtype"`
	IsError    bool          `json:"isError"`
}

// +kubebuilder:validation:XValidation:rule="!has(oldSelf.lastWitness) || (has(self.lastWitness) && self.lastWitness.podUID == oldSelf.lastWitness.podUID && self.lastWitness.observedAt >= oldSelf.lastWitness.observedAt)",message="original executor witness cannot be erased, replaced or replayed backwards"
type SessionDirectoryStatus struct {
	// +optional
	LastWitness *DirectoryWitness `json:"lastWitness,omitempty"`
	// +kubebuilder:validation:Enum=SourceMissing;Unknown;Present
	SourceState string      `json:"sourceState"`
	ObservedAt  metav1.Time `json:"observedAt"`
	// +kubebuilder:validation:Enum=Unknown
	StopState string `json:"stopState"`
	// +kubebuilder:validation:Enum=Unknown
	ActualModelState string `json:"actualModelState"`
	// +kubebuilder:validation:Enum=Unknown
	UsageState string `json:"usageState"`
	// +kubebuilder:validation:Enum=Unknown
	CostState string `json:"costState"`
}
