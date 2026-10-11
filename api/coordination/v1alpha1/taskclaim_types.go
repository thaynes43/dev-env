package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// TaskClaim has no lifecycle controller or Session owner reference. Losing its
// owner never releases ownership. Operator permissions need only get/create/list.
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
type TaskClaim struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              TaskClaimSpec `json:"spec"`
}

// +kubebuilder:object:root=true
type TaskClaimList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TaskClaim `json:"items"`
}

// TaskClaimSpec contains only bounded attribution, never prompt or rules text.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="task ownership and provenance are immutable"
type TaskClaimSpec struct {
	Identity   TaskIdentity    `json:"identity"`
	Owner      SessionIdentity `json:"owner"`
	Actor      ClaimActor      `json:"actor"`
	Provenance ClaimProvenance `json:"provenance"`
}

type TaskIdentity struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`
	Project string `json:"project"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`
	Repository string `json:"repository"`
	// Issue numbers are canonical positive decimal; task keys are explicit opaque identifiers.
	// +kubebuilder:validation:MaxLength=133
	// +kubebuilder:validation:Pattern=`^(issue:[1-9][0-9]{0,18}|task:[a-z0-9]([a-z0-9._-]{0,126}[a-z0-9])?)$`
	TaskKey string `json:"taskKey"`
}

type SessionIdentity struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`
	Namespace string `json:"namespace"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`
	Name string `json:"name"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9-]+$`
	UID string `json:"uid"`
}

type ClaimActor struct {
	// +kubebuilder:validation:Enum=session;coordinator
	Kind string `json:"kind"`
	// +kubebuilder:validation:MinLength=3
	// +kubebuilder:validation:MaxLength=317
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9-]*[a-z0-9])?/[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`
	ServiceAccount string `json:"serviceAccount"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`
	PodName string `json:"podName"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9-]+$`
	PodUID string `json:"podUID"`
}

// +kubebuilder:validation:XValidation:rule="has(self.reportedHead) == has(self.reportedAt)",message="reported HEAD requires its observation timestamp"
// +kubebuilder:validation:XValidation:rule="!self.github.endsWith('/.') && !self.github.endsWith('/..')",message="GitHub repository is not a path"
// +kubebuilder:validation:XValidation:rule="!self.github.endsWith('.git')",message="GitHub identity omits the transport suffix"
type ClaimProvenance struct {
	// +kubebuilder:validation:Pattern=`^[a-f0-9]{64}$`
	// +kubebuilder:validation:MaxLength=64
	CatalogRevision string `json:"catalogRevision"`
	// +kubebuilder:validation:Pattern=`^[a-f0-9]{64}$`
	// +kubebuilder:validation:MaxLength=64
	RulesRevision string `json:"rulesRevision"`
	// +kubebuilder:validation:MaxLength=140
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9-]{0,37}[a-z0-9])?/[a-z0-9_.-]{1,100}$`
	GitHub string `json:"github"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=255
	DefaultBranch string `json:"defaultBranch"`
	// This first unit has no bound startup pin receipt. A heartbeat cannot prove it.
	// +kubebuilder:validation:Enum=Unknown
	InitialPinState string `json:"initialPinState"`
	// +kubebuilder:validation:Pattern=`^[a-f0-9]{40}([a-f0-9]{24})?$`
	// +kubebuilder:validation:MaxLength=64
	// +optional
	ReportedHead string `json:"reportedHead,omitempty"`
	// +optional
	ReportedAt *metav1.Time `json:"reportedAt,omitempty"`
}
