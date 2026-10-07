package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Activity is one declaration that dev work is under way (DESIGN-001 6.9,
// D-17, D-66): "this is me, not a fault". The remediation lane reads it as
// evidence while it triages an alert, never as a mute. The operator's /v1 API
// creates it, in dev-env-system, from `declare-activity start` (agent-run),
// with the declarer taken from the caller's token and the expiry from the TTL
// it allows (45m by default, 8h at most, 2h for scope cluster); the operator
// deletes it when it expires, and `declare-activity end` deletes it sooner.
//
// The schema holds what makes a declaration useful: a description, a scope of
// one or more tokens (a namespace, an app, a node, or "cluster"), the declarer
// and an expiry. The spec is immutable: a different declaration is a new one.

// ScopeCluster is the wildcard scope: a declaration that may cover anything,
// which the API caps at 2h (D-17).
const ScopeCluster = "cluster"

// ActivitySpec is one declaration.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="an activity is immutable; end it and declare a new one"
type ActivitySpec struct {
	// Description says what is being done, for the remediation lane and Tom.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=512
	Description string `json:"description"`

	// Scope names what the work can disturb: namespaces, apps, nodes, or
	// "cluster". The remediation lane matches an alert's dimensions against it.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=32
	// +kubebuilder:validation:items:MinLength=1
	// +kubebuilder:validation:items:MaxLength=63
	// +listType=atomic
	Scope []string `json:"scope"`

	// DeclaredBy is the caller that declared it, from its token:
	// session/<name>, client/<namespace>/<name> or human/<namespace>/<name>.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	DeclaredBy string `json:"declaredBy"`

	// Session is the declaring session's name when a session declared it, so
	// the remediation lane can message it (agent-run msg).
	// +kubebuilder:validation:MaxLength=63
	// +optional
	Session string `json:"session,omitempty"`

	// ExpiresAt is when the declaration stops counting; the operator deletes it
	// then.
	ExpiresAt metav1.Time `json:"expiresAt"`
}

// Activity is a declaration of dev work (D-17, D-66).
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,shortName=act
// +kubebuilder:printcolumn:name="Scope",type=string,JSONPath=`.spec.scope`
// +kubebuilder:printcolumn:name="Expires",type=date,JSONPath=`.spec.expiresAt`
// +kubebuilder:printcolumn:name="By",type=string,JSONPath=`.spec.declaredBy`
// +kubebuilder:printcolumn:name="What",type=string,JSONPath=`.spec.description`
type Activity struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec ActivitySpec `json:"spec"`
}

// ActivityList is a list of activities.
// +kubebuilder:object:root=true
type ActivityList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Activity `json:"items"`
}
