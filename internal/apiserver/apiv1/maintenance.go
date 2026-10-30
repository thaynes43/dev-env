package apiv1

// MaintenanceIdentity pins an already authenticated session and original pod.
type MaintenanceIdentity struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	UID string `json:"uid"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	PodUID string `json:"podUID"`
}

// MaintenanceTask is a user-approved envelope, not a command allowlist. Commands
// are chosen by its agents. SYS_ADMIN does not enforce semantic task intent.
type MaintenanceTask struct {
	Coordinator MaintenanceIdentity `json:"coordinator"`
	// Workers are explicitly delegated direct children, sharing the same lease.
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=8
	// +optional
	Workers []MaintenanceIdentity `json:"workers,omitempty"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Node string `json:"node"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	NodeUID string `json:"nodeUID"`
	// Resource is an exact device/resource identity shown for approval.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Resource string `json:"resource"`
	// Operation is audit intent; arbitrary commands cannot be semantically confined.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	Operation string `json:"operation"`
	// +listType=set
	// +kubebuilder:validation:MaxItems=2
	// +kubebuilder:validation:items:Enum=SYS_ADMIN;SYS_RAWIO
	// +optional
	Capabilities []string `json:"capabilities,omitempty"`
	// Image must be digest pinned; broker repeats the syntax check.
	// +kubebuilder:validation:MaxLength=512
	Image string `json:"image"`
	// Devices are exact device files; cgroup/device runtime support is a held prerequisite.
	// +listType=set
	// +kubebuilder:validation:MaxItems=8
	// +kubebuilder:validation:items:MaxLength=253
	// +optional
	Devices []string `json:"devices,omitempty"`
	// ReadOnlyMounts are explicit host files under /var/lib/maintenance-inputs only.
	// +listType=set
	// +kubebuilder:validation:MaxItems=8
	// +kubebuilder:validation:items:MaxLength=253
	// +optional
	ReadOnlyMounts []string `json:"readOnlyMounts,omitempty"`
	// +kubebuilder:validation:Minimum=10
	// +kubebuilder:validation:Maximum=2000
	CPUMillis int64 `json:"cpuMillis"`
	// +kubebuilder:validation:Minimum=16
	// +kubebuilder:validation:Maximum=4096
	MemoryMiB int64 `json:"memoryMiB"`
	// Restore is the authorized restore/readback procedure, not automatic rollback.
	// +kubebuilder:validation:MaxLength=1000
	// +optional
	Restore string `json:"restore,omitempty"`
	// ExtensionOf requests more time via a new separately approved immutable grant.
	// +kubebuilder:validation:MaxLength=63
	// +optional
	ExtensionOf string `json:"extensionOf,omitempty"`
}

// MaintenanceStatus never equates API deletion with node termination.
type MaintenanceStatus struct {
	// +kubebuilder:validation:MaxLength=64
	// +optional
	PodUID string `json:"podUID,omitempty"`
	// +kubebuilder:validation:Enum=Running;StopPending;Terminated;Unknown;NotCreated
	// +optional
	Executor string `json:"executor,omitempty"`
	// RestoreEvidence is an attributed agent report, not independently proven hardware state.
	// +kubebuilder:validation:MaxLength=1000
	// +optional
	RestoreEvidence string `json:"restoreEvidence,omitempty"`
}

// MaintenanceDecisionRequest echoes the reviewed immutable grant identity.
type MaintenanceDecisionRequest struct {
	GrantUID string `json:"grantUID"`
	Digest   string `json:"digest"`
	Approve  bool   `json:"approve"`
	TTL      string `json:"ttl,omitempty"`
}

// MaintenanceCommand runs arbitrary argv inside the approved executor only.
type MaintenanceCommand struct {
	Command         []string `json:"command"`
	RestoreEvidence string   `json:"restoreEvidence,omitempty"`
}
type MaintenanceCommandResult struct {
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
	Error  string `json:"error,omitempty"`
}
