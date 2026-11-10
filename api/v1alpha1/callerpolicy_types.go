package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// CallerPolicy is the first D-36 caller contract (DESIGN-060 7.1). Only the
// dev-env-system/meatball object is read. It admits no other caller or rights.
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
type CallerPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// +required
	Spec CallerPolicySpec `json:"spec"`
}

// CallerPolicySpec may tighten the first caller's bounds, never widen them.
// Effort and route rights are fixed by the API, not configurable policy fields.
type CallerPolicySpec struct {
	// +kubebuilder:validation:Enum="dev-env-system/dev-env-meatball"
	// +kubebuilder:validation:MaxLength=128
	ServiceAccount string `json:"serviceAccount"`
	// +kubebuilder:validation:Enum=task
	// +kubebuilder:validation:MaxLength=4
	Mode string `json:"mode"`
	// +kubebuilder:validation:Enum=dev
	// +kubebuilder:validation:MaxLength=3
	Profile string `json:"profile"`
	// Models has no default. The request must name one of these full IDs.
	// +listType=set
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=3
	// +kubebuilder:validation:items:MaxLength=64
	// +kubebuilder:validation:items:Enum=claude-opus-5-5;gpt-6-astra;claude-fable-5-2
	Models []string `json:"models"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=2
	MaxConcurrent int32 `json:"maxConcurrent"`
	// +kubebuilder:validation:MaxLength=32
	// +kubebuilder:validation:XValidation:rule="duration(self) > duration('0s') && duration(self) <= duration('4h')",message="maxTimeout must be a positive Go duration at most 4h"
	MaxTimeout string `json:"maxTimeout"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=4
	MaxWorkers int32 `json:"maxWorkers"`
}

// +kubebuilder:object:root=true
type CallerPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CallerPolicy `json:"items"`
}
