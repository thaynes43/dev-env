package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func init() {
	// Registered here so the Ask kind joins the coordination scheme without a
	// second writer editing groupversion_info.go. AddToScheme reads the
	// builder when it is called, after this init.
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(GroupVersion, &Ask{}, &AskList{})
		return nil
	})
}

// Ask is one agent question or approval request (Epic #254). The spec is
// immutable. The status carries the lifecycle, and its answer is set once.
// v1 stores no inbox delivery: the requester reads the answer from the ask.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:validation:XValidation:rule="self.metadata.name == self.spec.id",message="the ask's name is its id"
type Ask struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              AskSpec   `json:"spec"`
	Status            AskStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type AskList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Ask `json:"items"`
}

// AskSpec is the ask as it was created. Nothing in it changes.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="ask spec is immutable"
type AskSpec struct {
	// +kubebuilder:validation:MinLength=20
	// +kubebuilder:validation:MaxLength=20
	// +kubebuilder:validation:Pattern=`^ask-[0-9a-f]{16}$`
	ID string `json:"id"`
	// +kubebuilder:validation:Enum=info;approval
	Type string `json:"type"`
	// Question is 1 to 1000 runes. The byte ceiling is four bytes per rune.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=4000
	Question string `json:"question"`
	// +kubebuilder:validation:MaxItems=6
	// +optional
	Options []AskOption `json:"options,omitempty"`
	// +kubebuilder:validation:MaxLength=24
	// +kubebuilder:validation:Pattern=`^[a-z0-9-]{0,24}$`
	// +optional
	Recommended   string     `json:"recommended,omitempty"`
	AllowFreeText bool       `json:"allowFreeText"`
	Context       AskContext `json:"context"`
	// +optional
	Scope     *AskScope   `json:"scope,omitempty"`
	CreatedAt metav1.Time `json:"createdAt"`
	ExpiresAt metav1.Time `json:"expiresAt"`
	// +kubebuilder:validation:MinLength=8
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9-]{8,64}$`
	RequestID string `json:"requestId"`
	// RequestDigest is the server's sha256 of the normalized create body.
	// +kubebuilder:validation:MinLength=64
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^[a-f0-9]{64}$`
	RequestDigest string `json:"requestDigest"`
}

// AskOption is one choice on an ask.
type AskOption struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=24
	// +kubebuilder:validation:Pattern=`^[a-z0-9-]{1,24}$`
	ID string `json:"id"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=320
	Label string `json:"label"`
	// +kubebuilder:validation:MaxLength=1200
	// +optional
	Description string `json:"description,omitempty"`
}

// AskContext names the session that created the ask.
type AskContext struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	SessionName string `json:"sessionName"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9-]+$`
	SessionUID string `json:"sessionUID"`
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=2000000000
	// +optional
	WorkPackage int `json:"workPackage,omitempty"`
	// +kubebuilder:validation:MaxItems=5
	// +kubebuilder:validation:items:MinLength=1
	// +kubebuilder:validation:items:MaxLength=300
	// +optional
	Links []string `json:"links,omitempty"`
}

// AskScope is the single action an approval ask names. Info asks omit it.
type AskScope struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=800
	Action string `json:"action"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=800
	Target    string      `json:"target"`
	ExpiresAt metav1.Time `json:"expiresAt"`
}

// AskStatus is the lifecycle. An answer is written once and never replaced
// (contract v1.1 A12). Open can become answered, withdrawn or expired; those
// three do not change again.
// An optional answer is absent, not null, so this is has() rather than == null.
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.answer) || self.answer == oldSelf.answer",message="an ask answer is set once"
// +kubebuilder:validation:XValidation:rule="!(oldSelf.state in ['answered', 'withdrawn', 'expired']) || self.state == oldSelf.state",message="an answered, withdrawn or expired ask does not change state"
// +kubebuilder:validation:XValidation:rule="self.state != 'answered' || has(self.answer)",message="an answered ask has an answer"
// +kubebuilder:validation:XValidation:rule="self.state == 'answered' || !has(self.answer)",message="only an answered ask has an answer"
type AskStatus struct {
	// +kubebuilder:validation:Enum=open;answered;withdrawn;expired
	State string `json:"state"`
	// +optional
	Answer *AskAnswer `json:"answer,omitempty"`
}

// AskAnswer is the one answer recorded for an ask.
type AskAnswer struct {
	// +kubebuilder:validation:MaxLength=24
	// +kubebuilder:validation:Pattern=`^[a-z0-9-]{0,24}$`
	// +optional
	OptionID string `json:"optionId,omitempty"`
	// +kubebuilder:validation:MaxLength=8000
	// +optional
	Text string `json:"text,omitempty"`
	// +optional
	Approved   *bool       `json:"approved,omitempty"`
	AnsweredBy AskActor    `json:"answeredBy"`
	AnsweredAt metav1.Time `json:"answeredAt"`
}

// AskActor is the portal principal the operator was given. Session responses
// omit issuer and subject; the stored record keeps them.
type AskActor struct {
	// +kubebuilder:validation:MaxLength=1200
	// +optional
	Issuer string `json:"issuer,omitempty"`
	// +kubebuilder:validation:MaxLength=800
	// +optional
	Subject string `json:"subject,omitempty"`
	// +kubebuilder:validation:MaxLength=480
	// +optional
	DisplayName string `json:"displayName,omitempty"`
}
