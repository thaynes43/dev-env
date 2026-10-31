package v1alpha1

// DelegateReachBinding is the source-only DESIGN-042 6.6 authorization snapshot.
// The broker rechecks the exact policy and identities before apply and while Active.
// +kubebuilder:validation:XValidation:rule="self.authorityKind != 'delegatedPolicy' || (size(self.policyName) > 0 && size(self.policyUID) > 0 && size(self.policyDigest) == 64)",message="policy reach requires exact policy name UID and digest"
// +kubebuilder:validation:XValidation:rule="self.authorityKind != 'reserved' || (has(self.reservedCase) && has(self.boundsRevision) && has(self.boundsDigest) && size(self.reservedCase) > 0 && size(self.boundsRevision) > 0 && size(self.boundsDigest) == 64)",message="reserved reach requires explicit case revision and digest"
type DelegateReachBinding struct {
	// AuthorityKind is an explicit union, never inferred from policy failure.
	// +kubebuilder:validation:Enum=delegatedPolicy;reserved
	AuthorityKind string `json:"authorityKind"`
	// Reserved fields are usable only after S7 validates enabled exact bounds.
	// +optional
	// +kubebuilder:validation:MaxLength=32
	ReservedCase string `json:"reservedCase,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=128
	BoundsRevision string `json:"boundsRevision,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=64
	BoundsDigest string `json:"boundsDigest,omitempty"`

	// +kubebuilder:validation:Enum=tree;policy
	Mode string `json:"mode"`
	// Designated retains explicit subset membership even for in-tree decisions.
	Designated bool `json:"designated"`
	// +kubebuilder:validation:MaxLength=128
	RequesterUID string `json:"requesterUID"`
	// +kubebuilder:validation:MaxLength=128
	RequesterPodUID string `json:"requesterPodUID"`
	// +kubebuilder:validation:MaxLength=253
	PolicyName string `json:"policyName"`
	// +kubebuilder:validation:MaxLength=128
	PolicyUID string `json:"policyUID"`
	// +kubebuilder:validation:MaxLength=64
	PolicyDigest string `json:"policyDigest"`
	// +kubebuilder:validation:MaxLength=64
	RequesterAncestryDigest string `json:"requesterAncestryDigest"`
	// +kubebuilder:validation:MaxLength=64
	ActorAncestryDigest string `json:"actorAncestryDigest"`
}
