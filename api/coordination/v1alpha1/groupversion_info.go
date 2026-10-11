// Package v1alpha1 holds protected coordination records, separate from the
// dev-env group that existing workers can read.
// +kubebuilder:object:generate=true
// +groupName=coordination.dev-env.haynesops.com
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	GroupVersion  = schema.GroupVersion{Group: "coordination.dev-env.haynesops.com", Version: "v1alpha1"}
	SchemeBuilder = runtime.NewSchemeBuilder(func(s *runtime.Scheme) error {
		s.AddKnownTypes(GroupVersion, &TaskClaim{}, &TaskClaimList{}, &PublishedContext{}, &PublishedContextList{}, &InboxMessage{}, &InboxMessageList{}, &InboxReadAck{}, &InboxReadAckList{}, &SessionDirectory{}, &SessionDirectoryList{})
		metav1.AddToGroupVersion(s, GroupVersion)
		return nil
	})
	AddToScheme = SchemeBuilder.AddToScheme
)
