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

// Ask is one agent question, approval request, or grant that needs a person
// (OpenProject Epic #254, ADR-021). The spec is immutable. The status carries
// the lifecycle, and its answer is set once. v1 stores no inbox delivery: the
// requester reads the answer from the ask. A root rule stops a status write
// from dropping a stored answer. The status type's own rule does not run once
// status itself is gone.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:validation:XValidation:rule="self.metadata.name == self.spec.id",message="the ask's name is its id"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.status) || !has(oldSelf.status.answer) || (has(self.status) && has(self.status.answer))",message="an ask answer cannot be dropped with status"
// +kubebuilder:validation:XValidation:rule="!has(self.status) || !has(self.status.answer) || self.spec.type != 'grant' || has(self.status.answer.answeredBy.username)",message="a grant answer has a username"
// +kubebuilder:validation:XValidation:rule="!has(self.status) || !has(self.status.answer) || self.spec.type == 'grant' || !has(self.status.answer.answeredBy.username)",message="an info or approval answer has no username"
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
// A grant ask carries grant and omits expiresAt and the approval scope.
// An info or approval ask omits grant and keeps expiresAt. Stored info and
// approval asks already have expiresAt and no grant, so these rules do not
// make them invalid.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="ask spec is immutable"
// +kubebuilder:validation:XValidation:rule="self.type == 'grant' || !has(self.grant)",message="an info or approval ask has no grant"
// +kubebuilder:validation:XValidation:rule="self.type != 'grant' || has(self.grant)",message="a grant ask has a typed scope"
// +kubebuilder:validation:XValidation:rule="self.type != 'grant' || !has(self.scope)",message="a grant ask has no approval scope"
// +kubebuilder:validation:XValidation:rule="self.type != 'grant' || !has(self.expiresAt)",message="a grant ask has no expiry"
// +kubebuilder:validation:XValidation:rule="self.type == 'grant' || has(self.expiresAt)",message="an info or approval ask has an expiry"
type AskSpec struct {
	// +kubebuilder:validation:MinLength=20
	// +kubebuilder:validation:MaxLength=20
	// +kubebuilder:validation:Pattern=`^ask-[0-9a-f]{16}$`
	ID string `json:"id"`
	// Type is info, approval or grant.
	// +kubebuilder:validation:Enum=info;approval;grant
	// +kubebuilder:validation:MaxLength=8
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
	Scope *AskScope `json:"scope,omitempty"`
	// Grant is the typed scope of a grant ask. Info and approval asks omit it.
	// +optional
	Grant     *AskGrant   `json:"grant,omitempty"`
	CreatedAt metav1.Time `json:"createdAt"`
	// ExpiresAt is the answer deadline of an info or approval ask. A grant ask
	// omits it. The field stays optional so a grant can leave it unset; every
	// stored info or approval ask already sets it.
	// +optional
	ExpiresAt metav1.Time `json:"expiresAt,omitempty"`
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
// A grant ask omits it too; the grant object is that ask's scope.
type AskScope struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=800
	Action string `json:"action"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=800
	Target    string      `json:"target"`
	ExpiresAt metav1.Time `json:"expiresAt"`
}

// AskGrant is the typed scope of a grant ask (ADR-021). It names the
// AccessGrant the ask is about. It is not an approval scope, and it is not
// part of the AccessGrant spec.
type AskGrant struct {
	// UID is the AccessGrant object's metadata.uid.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9]([A-Za-z0-9._:-]*[A-Za-z0-9])?$`
	UID string `json:"uid"`
	// ScopeDigest is grantattest.ScopeDigest of that object: 43 base64url characters.
	// +kubebuilder:validation:MinLength=43
	// +kubebuilder:validation:MaxLength=43
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9_-]{43}$`
	ScopeDigest string `json:"scopeDigest"`
	// Type is the grant's type.
	// +kubebuilder:validation:Enum=kube;egress;credential;breakglass;maintenance
	// +kubebuilder:validation:MaxLength=11
	Type string `json:"type"`
	// Target is one token, 1 to 200 runes. The byte ceiling is four bytes per rune.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=800
	Target string `json:"target"`
	// TTL is the duration the grant asked, stored as a duration string of at
	// most 16 characters. The broker checks the 10m to 8h bounds.
	// +kubebuilder:validation:MinLength=2
	// +kubebuilder:validation:MaxLength=16
	// +kubebuilder:validation:Pattern=`^([0-9]+(h|m|s))+$`
	TTL string `json:"ttl"`
	// Detail is optional display, 0 to 200 runes. It is not part of the binding.
	// +kubebuilder:validation:MaxLength=800
	// +optional
	Detail string `json:"detail,omitempty"`
}

// AskStatus is the lifecycle. An answer is written once and never replaced
// (contract v1.2.1). Open can become answered, withdrawn or expired; those
// three do not change again.
// An optional answer is absent, not null. The set-once rule uses has() and
// requires the new object to still carry the answer, so clearing the field
// does not skip the comparison.
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.answer) || (has(self.answer) && self.answer == oldSelf.answer)",message="an ask answer is set once"
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
	// Username is the portal preferred_username. A grant answer requires it.
	// An info or approval answer omits it. Session responses omit it.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9][A-Za-z0-9._@+-]{0,62}$`
	// +optional
	Username string `json:"username,omitempty"`
}
