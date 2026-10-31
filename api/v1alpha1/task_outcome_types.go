package v1alpha1

// TaskOutcomeStatus is first-write-wins evidence from the original task pod.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="original task outcome cannot change"
type TaskOutcomeStatus struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`
	SessionUID string `json:"sessionUID"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`
	PodUID string                  `json:"podUID"`
	Record TaskOutcomeRecordStatus `json:"record"`
}

// TaskOutcomeRecordStatus is the bounded task-authored allowlist.
type TaskOutcomeRecordStatus struct {
	// +optional
	// +kubebuilder:validation:MaxItems=3
	// +kubebuilder:validation:items:MaxLength=256
	// +kubebuilder:validation:items:Pattern=`^https://github\.com/thaynes43/(?:\.?[A-Za-z0-9_-][A-Za-z0-9._-]*|\.\.[A-Za-z0-9._-]+)/pull/[0-9]{1,6}$`
	PRURLs []string `json:"prURLs,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxItems=3
	// +kubebuilder:validation:items:MaxLength=40
	// +kubebuilder:validation:items:Pattern=`^[a-fA-F0-9]{40}$`
	HeadSHAs []string `json:"headSHAs,omitempty"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=200
	// +kubebuilder:validation:Pattern=`^[\p{L}\p{M}\p{N}\p{P}\p{S}\p{Zs}]{1,200}$`
	Summary string `json:"summary"`
}
