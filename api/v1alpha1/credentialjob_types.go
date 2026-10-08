package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// CredentialCleanupFinalizer holds a job until provider cleanup has completed.
const CredentialCleanupFinalizer = LabelPrefix + "credential-cleanup"

// CredentialJobName binds the private execution request to an immutable grant UID.
func CredentialJobName(uid types.UID) string { return "credential-" + string(uid) }

// CredentialObjectReference identifies an object including its replacement fence.
type CredentialObjectReference struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Namespace string `json:"namespace"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:MaxLength=36
	// +kubebuilder:validation:Pattern=`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`
	UID types.UID `json:"uid"`
}

// CredentialJobSpec is written by the broker, after approval. Keeper rechecks
// the live grant before acting; a job is never itself an approval.
// +kubebuilder:validation:XValidation:rule="self.credential == 'proxmox'",message="only proxmox credential execution is implemented"
// +kubebuilder:validation:XValidation:rule="self.grant == oldSelf.grant && self.session == oldSelf.session && self.credential == oldSelf.credential && self.expiresAt == oldSelf.expiresAt",message="credential request is immutable; only release may change"
// +kubebuilder:validation:XValidation:rule="!(has(oldSelf.release) && oldSelf.release) || (has(self.release) && self.release)",message="release cannot be taken back"
type CredentialJobSpec struct {
	Grant   CredentialObjectReference `json:"grant"`
	Session CredentialObjectReference `json:"session"`
	// General hw-ssh grants await the connection and revocation contract.
	// +kubebuilder:validation:Enum=proxmox
	Credential CredentialName `json:"credential"`
	ExpiresAt  metav1.Time    `json:"expiresAt"`
	// +optional
	Release bool `json:"release,omitempty"`
}

// CredentialJobPhase describes execution, never the grant's approval.
// +kubebuilder:validation:Enum=Pending;Installed;CleanupPending;Revoked
type CredentialJobPhase string

const (
	CredentialPending        CredentialJobPhase = "Pending"
	CredentialInstalled      CredentialJobPhase = "Installed"
	CredentialCleanupPending CredentialJobPhase = "CleanupPending"
	CredentialRevoked        CredentialJobPhase = "Revoked"
)

// CredentialFailureCode deliberately carries no raw provider or exec error.
// +kubebuilder:validation:Enum=InvalidRequest;BackendUnavailable;MintFailed;AmbiguousMint;IncompatiblePod
type CredentialFailureCode string

const (
	CredentialInvalidRequest     CredentialFailureCode = "InvalidRequest"
	CredentialBackendUnavailable CredentialFailureCode = "BackendUnavailable"
	CredentialMintFailed         CredentialFailureCode = "MintFailed"
	CredentialAmbiguousMint      CredentialFailureCode = "AmbiguousMint"
	CredentialIncompatiblePod    CredentialFailureCode = "IncompatiblePod"
)

// CredentialJobStatus is exclusively keeper-owned. Material lives only in its
// named private Secret journal and the target pod's memory-backed grant store.
type CredentialJobStatus struct {
	// +optional
	Phase CredentialJobPhase `json:"phase,omitempty"`
	// +optional
	FailureCode CredentialFailureCode `json:"failureCode,omitempty"`
	// +kubebuilder:validation:MaxLength=128
	// +optional
	ProviderID string `json:"providerID,omitempty"`
	// +kubebuilder:validation:MaxLength=64
	// +optional
	InstalledPodUID string `json:"installedPodUID,omitempty"`
	// +optional
	InstalledAt *metav1.Time `json:"installedAt,omitempty"`
	// +optional
	RevokedAt *metav1.Time `json:"revokedAt,omitempty"`
}

// CredentialJob is a broker request and keeper receipt in dev-env-system.
// RBAC separates main/status writers. Admission must additionally limit keeper
// main-resource updates to its cleanup finalizer; CEL cannot identify a writer.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=cjob
// +kubebuilder:printcolumn:name="Credential",type=string,JSONPath=`.spec.credential`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Expires",type=date,JSONPath=`.spec.expiresAt`
type CredentialJob struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              CredentialJobSpec `json:"spec"`
	// +optional
	Status CredentialJobStatus `json:"status,omitempty"`
}

// CredentialJobList is a list of private execution requests.
// +kubebuilder:object:root=true
type CredentialJobList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CredentialJob `json:"items"`
}
