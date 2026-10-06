// Package v1alpha1 holds the dev-env v2 API types, group dev-env.haynesops.com,
// version v1alpha1 (DESIGN-001 3.3).
//
// controller-gen writes zz_generated.deepcopy.go here and the CRDs to
// config/crd/; run `make generate` after changing a type. haynes-ops gets copies
// of the CRDs by PR and applies them from their own Flux Kustomization with
// `prune: disabled` (D-03).
//
// The package depends on k8s.io/apimachinery only, so agent-run and other
// clients can import the types without the controller toolkit.
//
// +kubebuilder:object:generate=true
// +groupName=dev-env.haynesops.com
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	// GroupVersion is the group and version of every type in this package.
	GroupVersion = schema.GroupVersion{Group: "dev-env.haynesops.com", Version: "v1alpha1"}

	// SchemeBuilder registers the types in this package with a scheme.
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

	// AddToScheme adds the types in this package to a scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

// addKnownTypes registers every kind in this package. Add each new kind here.
func addKnownTypes(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion,
		&AgentSession{},
		&AgentSessionList{},
	)
	metav1.AddToGroupVersion(s, GroupVersion)
	return nil
}
