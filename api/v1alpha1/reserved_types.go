package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// ReservedNamespace is one namespace present at an RD3b decision. Its UID
// prevents a replacement with the same name inheriting Secret-read authority.
type ReservedNamespace struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=36
	UID string `json:"uid"`
}

// GrantStorageTarget is one exact object reviewed for an RD4 storage delete.
// A kind/name without its immutable UID never authorizes a deletion.
type GrantStorageTarget struct {
	// +kubebuilder:validation:Enum=PersistentVolumeClaim;VolumeSnapshot;StatefulSet
	Kind string `json:"kind"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`
	Name string `json:"name"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=36
	UID string `json:"uid"`
}

// ReservedDecisionBinding is the audit of an authenticated delegate's S7
// assertion. TomRef names a message the agent claims to have received; neither
// this record nor its timestamp proves human consent. It grants no standing or
// attested authority. Live actor, reach, case configuration and scope checks are
// required before application and for the lifetime of the original lease.
type ReservedDecisionBinding struct {
	// +kubebuilder:validation:Enum=rd2;rd3a;rd3b;rd4-storage
	Case string `json:"case"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	BoundsRevision string `json:"boundsRevision"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=36
	GrantUID string `json:"grantUID"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=36
	RequesterUID string `json:"requesterUID"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=36
	RequesterPodUID string `json:"requesterPodUID"`
	// ActorUID is the TokenReview-bound actor Pod UID, including host actors
	// that have no AgentSession. ActorSessionUID, when present, binds it too.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=36
	ActorUID string `json:"actorUID"`
	// +kubebuilder:validation:MaxLength=36
	// +optional
	ActorSessionUID string `json:"actorSessionUID,omitempty"`
	// +kubebuilder:validation:MinLength=43
	// +kubebuilder:validation:MaxLength=43
	ScopeDigest string `json:"scopeDigest"`
	// EffectiveDigest additionally binds the namespace snapshot and revision.
	// +kubebuilder:validation:MinLength=43
	// +kubebuilder:validation:MaxLength=43
	EffectiveDigest string `json:"effectiveDigest"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9_.:/#-]{1,128}$`
	TomRef string `json:"tomRef"`
	// Ref retains request/reply correlation separately from the Tom assertion.
	// +kubebuilder:validation:MaxLength=128
	Ref string `json:"ref"`
	// ExcludedNamespaces is the exact reviewed configuration at decision time.
	// Empty is explicit configuration, never a lookup-failure fallback.
	// +listType=set
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=63
	ExcludedNamespaces []string        `json:"excludedNamespaces"`
	TTL                metav1.Duration `json:"ttl"`
	DecidedAt          metav1.Time     `json:"decidedAt"`
	// Namespaces is frozen at approval for RD3b, excluding Never namespaces.
	// Future namespaces are excluded. The finite storage bound fails closed
	// rather than widening to a ClusterRoleBinding.
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=64
	// +optional
	Namespaces []ReservedNamespace `json:"namespaces,omitempty"`
}
