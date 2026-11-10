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

// MeatballServiceAccount is the only caller account a parent note stores.
// The kubebuilder enum on InboxCaller.ServiceAccount repeats this value;
// markers cannot reference a Go constant.
const MeatballServiceAccount = "dev-env-system/dev-env-meatball"

// InboxCaller is the Meatball account and the CallerPolicy that admitted it.
// The server sets both fields. serviceAccount is namespace/name, at most 317
// characters, and policyUID is the CallerPolicy UID, at most 128 characters.
type InboxCaller struct {
	// +kubebuilder:validation:Enum="dev-env-system/dev-env-meatball"
	// +kubebuilder:validation:MaxLength=317
	ServiceAccount string `json:"serviceAccount"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9-]+$`
	PolicyUID string `json:"policyUID"`
}

// Existing objects keep a session sender and a session recipient. A new object
// uses exactly one sender form and exactly one recipient form (DESIGN-062, ADR-023).
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="message intent and original attribution are immutable"
// +kubebuilder:validation:XValidation:rule="(has(self.sender) && self.actor.kind == 'session') || (has(self.callerSender) && self.actor.kind == 'policy')",message="actor kind is session for a session sender and policy for a caller sender"
// +kubebuilder:validation:XValidation:rule="has(self.sender) != has(self.callerSender)",message="exactly one of sender and callerSender"
// +kubebuilder:validation:XValidation:rule="has(self.recipient) != has(self.callerRecipient)",message="exactly one of recipient and callerRecipient"
// +kubebuilder:validation:XValidation:rule="has(self.inReplyTo) == (has(self.kind) && self.kind == 'reply')",message="every reply, and only a reply, names the question it answers"
// +kubebuilder:validation:XValidation:rule="!has(self.kind) && has(self.callerSender) ? has(self.recipient) : true",message="a plain message from the Meatball account has a session recipient"
type InboxMessageSpec struct {
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`
	Project string `json:"project"`
	// +kubebuilder:validation:MaxLength=128
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9._-]{0,126}[a-z0-9])?$`
	Key string `json:"key"`
	// Sender is the session that wrote the message. Absent when callerSender
	// is set.
	// +optional
	Sender SessionIdentity `json:"sender,omitzero"`
	// CallerSender is set when the Meatball account writes. Exactly one of
	// sender and callerSender is set.
	// +optional
	CallerSender *InboxCaller `json:"callerSender,omitempty"`
	// Recipient is the session the message is for. Absent when callerRecipient
	// is set.
	// +optional
	Recipient SessionIdentity `json:"recipient,omitzero"`
	// CallerRecipient is set when the parent is the Meatball account. Exactly
	// one of recipient and callerRecipient is set.
	// +optional
	CallerRecipient *InboxCaller    `json:"callerRecipient,omitempty"`
	Actor           ClaimActor      `json:"actor"`
	Provenance      ClaimProvenance `json:"provenance"`
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`
	Repository string `json:"repository"`
	// Configured provider and requested model, never actual model execution.
	// +kubebuilder:validation:Enum=claude;codex;cursor;opencode
	Provider string `json:"provider"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	RequestedModel string `json:"requestedModel"`
	// CEL bytes enforces UTF-8 bytes rather than the schema's character count.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=16384
	// +kubebuilder:validation:XValidation:rule="size(bytes(self)) <= 16384",message="message exceeds 16 KiB UTF-8"
	Text string `json:"text"`
	// Kind marks a child note or its reply (DESIGN-014, DESIGN-062). Absent is
	// a plain message. A reply is not an approval, and neither is a note.
	// +kubebuilder:validation:Enum=question;result;blocker;reply
	// +kubebuilder:validation:MaxLength=8
	// +optional
	Kind string `json:"kind,omitempty"`
	// InReplyTo is the note a reply answers. The API binds it from the live
	// note, whose sender is the reply's recipient.
	// +optional
	InReplyTo *InboxMessageRef `json:"inReplyTo,omitempty"`
}

// InboxMessageRef names one exact message: its full-digest name and UID.
type InboxMessageRef struct {
	// +kubebuilder:validation:MaxLength=80
	// +kubebuilder:validation:Pattern=`^message\.[a-f0-9]{32}\.[a-f0-9]{32}$`
	Name string `json:"name"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9-]+$`
	UID string `json:"uid"`
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
