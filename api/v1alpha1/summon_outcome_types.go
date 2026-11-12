package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// SummonStatus is the operator's durable accounting for a summoned session.
// OP741 adds admission and reservation release; no admission reader uses this yet.
type SummonStatus struct {
	// Closed is written atomically with the first terminal outcome.
	// +optional
	Closed *SummonClosedStatus `json:"closed,omitempty"`
	// Escalation is the pending filing for a remediation ending escalated.
	// OP768 adds filing, blocked and abandoned transitions; this slice files nothing.
	// +optional
	Escalation *SummonEscalationStatus `json:"escalation,omitempty"`
}

// SummonClosedStatus identifies the one terminal transition (DESIGN-064 5.9).
type SummonClosedStatus struct {
	At metav1.Time `json:"at"`
	// +kubebuilder:validation:MaxLength=10
	// +kubebuilder:validation:Enum=report;unreported
	Source string `json:"source"`
	// Reason is the report verb or the guaranteed-close reason from section 6.6.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=32
	Reason string `json:"reason"`
}

// SummonEscalationStatus reserves a filing name in the terminal write.
// OP768 resolves name collisions and records the child; spec.summon.filedFor
// and the filing's charge, retry and abandonment are deliberately its seam.
type SummonEscalationStatus struct {
	// +kubebuilder:validation:MaxLength=16
	// +kubebuilder:validation:Enum=Pending
	State string `json:"state"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`
}
