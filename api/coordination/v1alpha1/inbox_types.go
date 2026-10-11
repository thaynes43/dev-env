package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// InboxMessage is durable API content. It has no Session owner reference,
// status controller, expiry or provider-delivery side effect.
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
type InboxMessage struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              InboxMessageSpec `json:"spec"`
}

// +kubebuilder:object:root=true
type InboxMessageList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []InboxMessage `json:"items"`
}

// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="message intent and original attribution are immutable"
// +kubebuilder:validation:XValidation:rule="self.actor.kind == 'session'",message="only original Session senders publish"
type InboxMessageSpec struct {
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`
	Project string `json:"project"`
	// +kubebuilder:validation:MaxLength=128
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9._-]{0,126}[a-z0-9])?$`
	Key        string          `json:"key"`
	Sender     SessionIdentity `json:"sender"`
	Recipient  SessionIdentity `json:"recipient"`
	Actor      ClaimActor      `json:"actor"`
	Provenance ClaimProvenance `json:"provenance"`
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`
	Repository string `json:"repository"`
	// Configured provider and requested model, never actual model execution.
	// +kubebuilder:validation:Enum=claude;codex;opencode
	Provider string `json:"provider"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	RequestedModel string `json:"requestedModel"`
	// CEL bytes enforces UTF-8 bytes rather than the schema's character count.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=16384
	// +kubebuilder:validation:XValidation:rule="size(bytes(self)) <= 16384",message="message exceeds 16 KiB UTF-8"
	Text string `json:"text"`
}

// InboxReadAck records explicit API acknowledgement only. GET never creates it.
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
type InboxReadAck struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              InboxReadAckSpec `json:"spec"`
}

// +kubebuilder:object:root=true
type InboxReadAckList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []InboxReadAck `json:"items"`
}

// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="original message and recipient acknowledgement are immutable"
// +kubebuilder:validation:XValidation:rule="self.actor.kind == 'session'",message="only original Session recipients acknowledge"
type InboxReadAckSpec struct {
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`
	Project string `json:"project"`
	// +kubebuilder:validation:MaxLength=80
	// +kubebuilder:validation:Pattern=`^message\.[a-f0-9]{32}\.[a-f0-9]{32}$`
	MessageName string `json:"messageName"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9-]+$`
	MessageUID string          `json:"messageUID"`
	Recipient  SessionIdentity `json:"recipient"`
	Actor      ClaimActor      `json:"actor"`
}
