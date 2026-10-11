package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// PublishedContext is deliberately published text, not a native history backup.
// It has no Session owner reference, lifecycle controller or expiry.
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
type PublishedContext struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              PublishedContextSpec `json:"spec"`
}

// +kubebuilder:object:root=true
type PublishedContextList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PublishedContext `json:"items"`
}

type ContextIdentity struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9-]+$`
	SessionUID string `json:"sessionUID"`
	// +kubebuilder:validation:Enum=transcript-excerpt;result;memory;handoff
	// +kubebuilder:validation:MaxLength=32
	Kind string `json:"kind"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9._-]{0,126}[a-z0-9])?$`
	Key string `json:"key"`
}

// Provider-neutral attribution records the requested model only. This unit
// neither observes inference nor verifies provider output, usage or cost.
type ContextAttribution struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`
	Provider string `json:"provider"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	RequestedModel string `json:"requestedModel"`
	// +kubebuilder:validation:Enum=Unknown
	ObservedModelState string `json:"observedModelState"`
	// +kubebuilder:validation:Enum=Unknown
	UsageState string `json:"usageState"`
	// +kubebuilder:validation:Enum=Unknown
	CostState string `json:"costState"`
}

// The API and store enforce UTF-8 byte bounds too: CRD MaxLength counts characters.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="published context and original provenance are immutable"
// +kubebuilder:validation:XValidation:rule="self.identity.sessionUID == self.source.uid",message="source UID must equal original publication identity"
// +kubebuilder:validation:XValidation:rule="self.origin == 'ExplicitPublished' ? (self.completeness == 'ExplicitExcerpt' && !has(self.managedOutput)) : (has(self.managedOutput) && self.completeness in ['PartialManagedOutput', 'Unknown'] && self.attribution.provider in ['claude', 'codex'] && self.identity.kind == 'transcript-excerpt' && self.identity.key == 'capture-' + self.managedOutput.invocation + '-' + (self.managedOutput.ordinal == 0 ? '0' : self.managedOutput.ordinal == 1 ? '1' : self.managedOutput.ordinal == 2 ? '2' : '3'))",message="managed assertions require their bounded fixed identity and truthful partial state"
type PublishedContextSpec struct {
	ManagedOutput *ManagedOutputAssertion `json:"managedOutput,omitempty"`
	Identity      ContextIdentity         `json:"identity"`
	Source        SessionIdentity         `json:"source"`
	// +kubebuilder:validation:Minimum=1
	SourceGeneration int64 `json:"sourceGeneration"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=317
	SourceParent string `json:"sourceParent"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`
	Project string `json:"project"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`
	Repository  string             `json:"repository"`
	Actor       ClaimActor         `json:"actor"`
	Provenance  ClaimProvenance    `json:"provenance"`
	Attribution ContextAttribution `json:"attribution"`
	// +kubebuilder:validation:Enum=ExplicitPublished;ManagedOutputAssertion
	// +kubebuilder:validation:MaxLength=32
	Origin string `json:"origin"`
	// +kubebuilder:validation:Enum=ExplicitExcerpt;PartialManagedOutput;Unknown
	// +kubebuilder:validation:MaxLength=32
	Completeness string `json:"completeness"`
	// +kubebuilder:validation:Enum=Unknown
	StopState string `json:"stopState"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=65536
	Text string `json:"text"`
	// +kubebuilder:validation:Pattern=`^[a-f0-9]{64}$`
	// +kubebuilder:validation:MaxLength=64
	ContentSHA256 string `json:"contentSHA256"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65536
	ByteCount int32 `json:"byteCount"`
}

// ManagedOutputAssertion is publisher-declared output, not transcript attestation.
type ManagedOutputAssertion struct {
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^[a-f0-9]{64}$`
	Invocation string `json:"invocation"`
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=3
	Ordinal int32 `json:"ordinal"`
	// +kubebuilder:validation:MaxLength=36
	// +kubebuilder:validation:Pattern=`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`
	ConversationID string `json:"conversationID"`
}
