package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// AccessGrant and GrantPolicy are the access broker's resources (DESIGN-001
// 6.12, D-25, D-54). An AccessGrant is one request for more than the baseline:
// a catalog role for a while (kube), a destination (egress), a short-lived
// credential (credential, Q-07) or break-glass (D-27). The operator's /v1 API
// creates it from an agent's request and takes the requester from the caller's
// token; the broker decides it, makes what it grants and revokes it. A
// GrantPolicy is a standing approval in git (haynes-ops): a request it matches
// is approved at once, under the policy's name.
//
// The schema refuses what no grant or policy may ever do, whoever writes it:
//
//   - no grant or policy targets dev-env-system, dev-agents or dev-tools (a
//     namespace, an endpoint, or an in-cluster name);
//   - a grant's TTL runs from 10 minutes (the shortest token TokenRequest issues)
//     to its type's longest: 8 h for kube and egress, 4 h for credential, 1 h for
//     break-glass;
//   - type breakglass goes with role dev-env-grant-breakglass, and only there;
//   - the namespaced catalog roles name their namespaces one by one; the
//     cluster-wide ones (nodes, break-glass) take none;
//   - only role dev-env-grant-namespace-operator names Secrets, one to 16, each
//     by name; no standing policy approves it, and an attested one only when it
//     opts in with attested.allowSecretUse (DESIGN-042 5.1);
//   - no policy approves break-glass, role dev-env-grant-breakglass or
//     dev-env-grant-secrets-read, or profile ops;
//   - no standing policy approves maintenance: only an attested policy with an
//     exact maintenance scope may (ADR-013);
//   - a grant's spec is immutable after create, except release, which only ever
//     goes from false to true.
//
// The broker checks the same rules again before it makes anything (6.12). The
// envtest suite in this package proves each rule against a real API server.

// GrantType is what a grant gives (DESIGN-001 6.12). The LLM lease of 8.3
// joins this list in plan 09.
// +kubebuilder:validation:Enum=kube;egress;credential;breakglass;maintenance
type GrantType string

const (
	// GrantKube binds a catalog role to a ServiceAccount made for the grant.
	GrantKube GrantType = "kube"
	// GrantMaintenance leases an isolated executor for one immutable task.
	GrantMaintenance GrantType = "maintenance"
	// GrantEgress opens destinations to the session's pod with a
	// CiliumNetworkPolicy.
	GrantEgress GrantType = "egress"
	// GrantCredential installs a short-lived credential in the session's pod
	// (Q-07).
	GrantCredential GrantType = "credential"
	// GrantBreakglass is a kube grant of dev-env-grant-breakglass cluster-wide,
	// approved by Tom only (D-27).
	GrantBreakglass GrantType = "breakglass"
)

// The grant role catalog (DESIGN-001 6.12): ClusterRoles in haynes-ops, and the
// only roles the broker may bind.
const (
	// RoleWorkloads writes Deployments, StatefulSets, DaemonSets, Jobs,
	// CronJobs, Services, ConfigMaps and pods in the granted namespaces.
	RoleWorkloads = "dev-env-grant-workloads"
	// RoleStorage creates and deletes PVCs and VolumeSnapshots, and deletes
	// StatefulSets, in the granted namespaces.
	RoleStorage = "dev-env-grant-storage"
	// RoleSecretsRead reads Secrets in the granted namespaces. No policy may
	// approve it.
	RoleSecretsRead = "dev-env-grant-secrets-read"
	// RoleNodes cordons, uncordons and drains nodes, cluster-wide.
	RoleNodes = "dev-env-grant-nodes"
	// RoleBreakglass is every verb on every resource minus the exclusions of
	// D-27, cluster-wide. Type breakglass only.
	RoleBreakglass = "dev-env-grant-breakglass"
	// RoleNamespaceOperator execs into pods, restarts workloads, and runs Jobs
	// that use the Secrets its grant names, in the granted namespaces
	// (DESIGN-042 5.1, class C2). It has no Secret get or list, which does not
	// mean it cannot read Secrets: exec reaches every Secret the namespace's
	// pods already hold, and their ServiceAccounts' tokens, and a command run
	// in a Job that uses a named Secret can print it. For grant identities,
	// rule 3 of the installed dev-env-identity-guard (not dev-env-agent-guard)
	// limits created Jobs to the namespace's default ServiceAccount. The residual
	// authority is that account's RBAC plus the existing exec reach; the named-Secret
	// guard does not bound it. This is a factual correction, not risk acceptance or
	// new hardening. No standing policy approves it; an attested one only with
	// attested.allowSecretUse.
	RoleNamespaceOperator = "dev-env-grant-namespace-operator"
)

// ClusterWideRole reports whether a catalog role is bound cluster-wide (a
// ClusterRoleBinding) rather than in named namespaces.
func ClusterWideRole(role string) bool {
	return role == RoleNodes || role == RoleBreakglass
}

// GrantPhase is where a grant stands.
// +kubebuilder:validation:Enum=Pending;Active;Denied;Expired;Released;Failed
type GrantPhase string

const (
	// GrantPending waits for a policy or for Tom. Unanswered after 30 minutes,
	// it ends Denied.
	GrantPending GrantPhase = "Pending"
	// GrantActive is approved and made; it lasts until status.expiresAt.
	GrantActive GrantPhase = "Active"
	// GrantDenied was refused by Tom, by the broker's checks, or by the
	// 30-minute timeout.
	GrantDenied GrantPhase = "Denied"
	// GrantExpired reached its TTL and was revoked.
	GrantExpired GrantPhase = "Expired"
	// GrantReleased was given back by its session before its TTL, or ended with
	// its session, and was revoked.
	GrantReleased GrantPhase = "Released"
	// GrantFailed was approved but could not be made, so nothing of it stands:
	// for example a Proxmox mint that Proxmox refused (Q-07 fails closed).
	GrantFailed GrantPhase = "Failed"
)

// Ended reports whether a grant phase is final.
func (p GrantPhase) Ended() bool {
	switch p {
	case GrantDenied, GrantExpired, GrantReleased, GrantFailed:
		return true
	}
	return false
}

// CredentialName is a credential a credential grant can install (Q-07).
// +kubebuilder:validation:Enum=proxmox;hw-ssh
type CredentialName string

const (
	// CredentialProxmox is a Proxmox API token for dev-env@pve that expires
	// with the grant, minted by the keeper over SSH with its own CA (Q-15).
	CredentialProxmox CredentialName = "proxmox"
	// CredentialHWSSH leases the existing static shared ed25519 hw-ssh key into
	// the session pod's tmpfs for the grant's TTL (D-89). It is not an SSH
	// certificate: the hosts trust no CA. Removing the lease at expiry, release
	// or session end is not revocation and not containment: anything in the pod
	// can copy the key while it is installed, and the key has no cryptographic
	// expiry. Real revocation means rotating the key on every host. A CA
	// certificate path is the stronger follow-up and needs host changes.
	CredentialHWSSH CredentialName = "hw-ssh"
)

// GrantProtocol is an egress port's protocol.
// +kubebuilder:validation:Enum=TCP;UDP
type GrantProtocol string

const (
	ProtocolTCP GrantProtocol = "TCP"
	ProtocolUDP GrantProtocol = "UDP"
)

// AccessGrantSpec is one request, as the /v1 API wrote it. The API takes the
// requester from the caller's token, never from the request body (D-25).
//
// +kubebuilder:validation:XValidation:rule="(self.type == 'kube' || self.type == 'breakglass') == has(self.kube)",message="kube is set for types kube and breakglass, and only there"
// +kubebuilder:validation:XValidation:rule="(self.type == 'egress') == has(self.egress)",message="egress is set for type egress, and only there"
// +kubebuilder:validation:XValidation:rule="(self.type == 'maintenance') == has(self.maintenance)",message="maintenance is set only for a maintenance task"
// +kubebuilder:validation:XValidation:rule="(self.type == 'credential') == has(self.credential)",message="credential is set for type credential, and only there"
// +kubebuilder:validation:XValidation:rule="!has(self.kube) || ((self.type == 'breakglass') == (self.kube.role == 'dev-env-grant-breakglass'))",message="type breakglass and role dev-env-grant-breakglass go together (D-27)"
// +kubebuilder:validation:XValidation:rule="duration(self.ttl) >= duration('10m') && duration(self.ttl) <= duration('8h')",message="ttl runs from 10m to 8h"
// +kubebuilder:validation:XValidation:rule="self.type != 'credential' || duration(self.ttl) <= duration('4h')",message="a credential grant lasts at most 4h"
// +kubebuilder:validation:XValidation:rule="self.type != 'breakglass' || duration(self.ttl) <= duration('1h')",message="break-glass lasts at most 1h (D-27)"
// +kubebuilder:validation:XValidation:rule="has(self.kube) == has(oldSelf.kube) && has(self.egress) == has(oldSelf.egress) && has(self.credential) == has(oldSelf.credential) && has(self.maintenance) == has(oldSelf.maintenance)",message="immutable after create; only release may change"
// +kubebuilder:validation:XValidation:rule="!(has(oldSelf.release) && oldSelf.release) || (has(self.release) && self.release)",message="release cannot be taken back"
// +kubebuilder:validation:XValidation:rule="self.type != 'maintenance' || (duration(self.ttl) <= duration('1h') && has(self.requester.sessionUID) && self.maintenance.coordinator.name == self.requester.session && self.maintenance.coordinator.uid == self.requester.sessionUID)",message="maintenance requires the authenticated coordinator UID and at most one hour"
type AccessGrantSpec struct {
	// Maintenance is an immutable task envelope.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="immutable task scope"
	// +optional
	Maintenance *MaintenanceTask `json:"maintenance,omitempty"`

	// Requester is the session the grant is for, as the API found it from the
	// caller's token.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="immutable after create; only release may change"
	// +required
	Requester GrantRequester `json:"requester"`

	// Type is what the grant gives.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="immutable after create; only release may change"
	// +required
	Type GrantType `json:"type"`

	// Kube is the role and namespaces of a kube or breakglass grant.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="immutable after create; only release may change"
	// +optional
	Kube *KubeGrant `json:"kube,omitempty"`

	// Egress is the destinations of an egress grant.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="immutable after create; only release may change"
	// +optional
	Egress *EgressGrant `json:"egress,omitempty"`

	// Credential is the credential of a credential grant.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="immutable after create; only release may change"
	// +optional
	Credential *CredentialGrant `json:"credential,omitempty"`

	// TTL is how long the grant lasts once approved. Tom may approve it for less
	// time (status.approvedTTL).
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="immutable after create; only release may change"
	// +required
	TTL metav1.Duration `json:"ttl"`

	// Reason is why the agent asks, in its own words. The approval page shows
	// it as agent-written.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=1000
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="immutable after create; only release may change"
	// +required
	Reason string `json:"reason"`

	// Release asks the broker to end the grant now: the session gave it back
	// (DELETE /v1/grants/{name}) or was reaped. It only ever goes from false to
	// true.
	// +optional
	Release bool `json:"release,omitempty"`
}

// GrantRequester is the session a grant is for, with what a GrantPolicy matches
// on and what the audit record keeps (DESIGN-001 6.12).
type GrantRequester struct {
	// Session is the requesting session's name.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +required
	Session string `json:"session"`

	// SessionUID is captured by the API from the authenticated session. It is
	// required for credential execution; older kube and egress requests omit it.
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:MaxLength=36
	// +kubebuilder:validation:Pattern=`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`
	// +optional
	SessionUID types.UID `json:"sessionUID,omitempty"`

	// Repo is the session's spec.repo.
	// +kubebuilder:validation:MaxLength=100
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9_.-]*$`
	// +optional
	Repo string `json:"repo,omitempty"`

	// Profile is the profile the session's pod was built with (D-18).
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +optional
	Profile string `json:"profile,omitempty"`

	// Agent is the session's agent CLI.
	// +optional
	Agent AgentKind `json:"agent,omitempty"`

	// Parent is the session's spec.parent: the session or caller that started
	// it.
	// +kubebuilder:validation:MaxLength=253
	// +optional
	Parent string `json:"parent,omitempty"`
}

// KubeGrant is a catalog role in named namespaces, or cluster-wide for the
// cluster-wide roles.
//
// +kubebuilder:validation:XValidation:rule="(self.role == 'dev-env-grant-nodes' || self.role == 'dev-env-grant-breakglass') == (!has(self.namespaces) || size(self.namespaces) == 0)",message="dev-env-grant-nodes and dev-env-grant-breakglass are cluster-wide and take no namespaces; the other roles name at least one"
// +kubebuilder:validation:XValidation:rule="(self.role == 'dev-env-grant-namespace-operator') == (has(self.secrets) && size(self.secrets) > 0)",message="dev-env-grant-namespace-operator names the Secrets it will use, one to 16, and only that role takes secrets (DESIGN-042 5.1)"
type KubeGrant struct {
	// Role is a ClusterRole from the grant role catalog.
	// +kubebuilder:validation:Enum=dev-env-grant-workloads;dev-env-grant-storage;dev-env-grant-secrets-read;dev-env-grant-nodes;dev-env-grant-breakglass;dev-env-grant-namespace-operator
	// +required
	Role string `json:"role"`

	// Secrets are the Secrets that the new pods and Jobs of a
	// dev-env-grant-namespace-operator grant may reference, each by name: no
	// wildcard, no pattern (DESIGN-042 5.1). A grant of that role names one to
	// 16; no other role takes them. They hold in every namespace the grant
	// names. The approver decides this exact list: the scope digest covers it.
	// A command run in a pod that uses a Secret can print it, so naming a
	// Secret lets the agent read it.
	// +listType=set
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:items:MaxLength=253
	// +kubebuilder:validation:items:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	// +optional
	Secrets []string `json:"secrets,omitempty"`

	// Namespaces are where a namespaced role is bound, each named.
	// +listType=set
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:items:MaxLength=63
	// +kubebuilder:validation:items:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +kubebuilder:validation:items:XValidation:rule="!(self in ['dev-env-system', 'dev-agents', 'dev-tools'])",message="no grant reaches the dev-env namespaces (DESIGN-001 6.12)"
	// +optional
	Namespaces []string `json:"namespaces,omitempty"`
}

// EgressGrant is destinations for the session's pod: public or LAN names and
// addresses, or in-cluster endpoints, on the listed ports (D-24's controlled
// tier).
//
// +kubebuilder:validation:XValidation:rule="(has(self.fqdns) ? size(self.fqdns) : 0) + (has(self.cidrs) ? size(self.cidrs) : 0) + (has(self.endpoints) ? size(self.endpoints) : 0) > 0",message="name at least one destination: fqdns, cidrs or endpoints"
type EgressGrant struct {
	// FQDNs are DNS names, each exact or with one leading "*." for any
	// subdomain. In-cluster names go by endpoints, never by name.
	// +listType=set
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:items:MaxLength=253
	// +kubebuilder:validation:items:Pattern=`^(\*\.)?([a-z0-9]([-a-z0-9]*[a-z0-9])?\.)+[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +kubebuilder:validation:items:XValidation:rule="!self.endsWith('.local')",message="in-cluster and .local names are not reached by name: use endpoints, or cidrs for a LAN host"
	// +optional
	FQDNs []string `json:"fqdns,omitempty"`

	// CIDRs are address ranges, such as 192.168.40.21/32 for one LAN host.
	// +listType=set
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:items:MaxLength=43
	// +kubebuilder:validation:items:XValidation:rule="isCIDR(self)",message="must be a CIDR such as 192.168.40.21/32"
	// +optional
	CIDRs []string `json:"cidrs,omitempty"`

	// Endpoints are in-cluster pods by namespace and labels.
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=8
	// +optional
	Endpoints []EgressEndpoint `json:"endpoints,omitempty"`

	// Ports are the ports opened to every destination above.
	// +listType=atomic
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	// +required
	Ports []GrantPort `json:"ports"`
}

// EgressEndpoint is in-cluster pods: every pod of a namespace, or the ones with
// these labels.
type EgressEndpoint struct {
	// Namespace is the pods' namespace.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +kubebuilder:validation:XValidation:rule="!(self in ['dev-env-system', 'dev-agents', 'dev-tools'])",message="no grant reaches the dev-env namespaces (DESIGN-001 6.12)"
	// +required
	Namespace string `json:"namespace"`

	// MatchLabels narrows the pods; empty means every pod in the namespace. The
	// keys are plain pod labels: a Cilium or Kubernetes meta label such as
	// io.kubernetes.pod.namespace or k8s:io.cilium.k8s.policy.serviceaccount
	// would let the selector leave Namespace, so those are refused.
	// +kubebuilder:validation:MaxProperties=8
	// +kubebuilder:validation:XValidation:rule="self.all(k, k.size() <= 253 && k.matches('^([a-z0-9]([-a-z0-9]*[a-z0-9])?([.][a-z0-9]([-a-z0-9]*[a-z0-9])?)*/)?[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$'))",message="matchLabels keys are plain label keys, such as app.kubernetes.io/name"
	// +kubebuilder:validation:XValidation:rule="self.all(k, !k.startsWith('io.kubernetes.') && !k.startsWith('io.cilium.') && !k.startsWith('k8s.io/'))",message="matchLabels may not use the io.kubernetes., io.cilium. or k8s.io/ meta labels: the endpoint stays in its namespace"
	// +optional
	MatchLabels map[string]LabelValue `json:"matchLabels,omitempty"`
}

// LabelValue is a label value: at most 63 letters, digits, '-', '_' and '.',
// starting and ending with a letter or digit, or empty.
// +kubebuilder:validation:MaxLength=63
// +kubebuilder:validation:Pattern=`^([A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?)?$`
type LabelValue string

// GrantPort is one port.
type GrantPort struct {
	// Port is the destination port.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +required
	Port int32 `json:"port"`

	// Protocol is TCP or UDP.
	// +kubebuilder:default=TCP
	// +optional
	Protocol GrantProtocol `json:"protocol,omitempty"`
}

// CredentialGrant names a short-lived credential (Q-07).
type CredentialGrant struct {
	// Name is the credential.
	// +required
	Name CredentialName `json:"name"`
}

// AccessGrantStatus is the broker's record of the grant. Only the broker
// writes it (accessgrants/status); the operator's API reads it.
type AccessGrantStatus struct {
	// DelegateReach records the reviewed policy and identity binding (DESIGN-042 6.6).
	// +optional
	DelegateReach *DelegateReachBinding `json:"delegateReach,omitempty"`

	// Maintenance keeps executor termination and restore evidence separate.
	// +optional
	Maintenance *MaintenanceStatus `json:"maintenance,omitempty"`

	// Phase is where the grant stands.
	// +optional
	Phase GrantPhase `json:"phase,omitempty"`

	// Message says why a grant was denied or failed, in a line.
	// +kubebuilder:validation:MaxLength=1024
	// +optional
	Message string `json:"message,omitempty"`

	// ApprovedBy is "policy/<name>" for a standing policy, or
	// "authentik/<username>" for Tom on the approval page.
	// +kubebuilder:validation:MaxLength=253
	// +optional
	ApprovedBy string `json:"approvedBy,omitempty"`

	// ApprovedAt is when it was approved.
	// +optional
	ApprovedAt *metav1.Time `json:"approvedAt,omitempty"`

	// ApprovedTTL is the TTL it was approved for: spec.ttl, or less when Tom
	// chose "approve for less time".
	// +optional
	ApprovedTTL *metav1.Duration `json:"approvedTTL,omitempty"`

	// DeniedBy is "authentik/<username>", "broker" (a check failed) or
	// "timeout" (unanswered for 30 minutes).
	// +kubebuilder:validation:MaxLength=253
	// +optional
	DeniedBy string `json:"deniedBy,omitempty"`

	// ExpiresAt is when an approved grant ends.
	// +optional
	ExpiresAt *metav1.Time `json:"expiresAt,omitempty"`

	// EndedAt is when it was denied, failed, expired or released.
	// +optional
	EndedAt *metav1.Time `json:"endedAt,omitempty"`

	// NotifiedAt is when Tom was sent the approval link.
	// +optional
	NotifiedAt *metav1.Time `json:"notifiedAt,omitempty"`

	// ServiceAccount is the grant's identity in dev-agents, for kube and
	// breakglass grants.
	// +kubebuilder:validation:MaxLength=253
	// +optional
	ServiceAccount string `json:"serviceAccount,omitempty"`

	// InstalledPodUID is the session pod the grant was last installed in. A new
	// pod (a resume, a drain) gets it installed again.
	// +kubebuilder:validation:MaxLength=64
	// +optional
	InstalledPodUID string `json:"installedPodUID,omitempty"`

	// InstalledAt is when it was installed there.
	// +optional
	InstalledAt *metav1.Time `json:"installedAt,omitempty"`
}

// AccessGrant is one time-boxed grant of more than the baseline for one
// session (DESIGN-001 6.12, D-25). Agents cannot write AccessGrants: they ask
// through the /v1 API, which records the requester from the caller's token.
// AccessGrants live in dev-agents beside their sessions (D-54), and the name
// is also the grant's ServiceAccount, bindings and network policy.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="Session",type=string,JSONPath=`.spec.requester.session`
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Approved-By",type=string,JSONPath=`.status.approvedBy`
// +kubebuilder:printcolumn:name="Expires",type=date,JSONPath=`.status.expiresAt`
// +kubebuilder:printcolumn:name="Role",type=string,JSONPath=`.spec.kube.role`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="self.metadata.name.size() <= 63",message="metadata.name must be at most 63 characters: it names the grant's ServiceAccount and bindings"
// +kubebuilder:validation:XValidation:rule="self.metadata.name.matches('^grant-[a-z0-9]([-a-z0-9]*[a-z0-9])?$')",message="metadata.name is grant-<id>, a DNS label: it names the grant's ServiceAccount, and the audit log shows it"
type AccessGrant struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +required
	Spec AccessGrantSpec `json:"spec"`

	// +optional
	Status AccessGrantStatus `json:"status,omitempty"`
}

// AccessGrantList is a list of AccessGrant.
//
// +kubebuilder:object:root=true
type AccessGrantList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AccessGrant `json:"items"`
}

// GrantPolicySpec is a standing approval. A request matches when its type is
// the policy's type, its requester matches every requester list the policy sets,
// its scope lies inside the policy's scope, and its TTL is at most maxTTL.
//
// +kubebuilder:validation:XValidation:rule="self.type != 'breakglass'",message="no policy approves break-glass: authenticated human approval is required (D-27)"
// +kubebuilder:validation:XValidation:rule="self.type != 'maintenance' || has(self.attested) || has(self.delegated)",message="no standing policy approves maintenance: only an attested or delegated policy may name a maintenance scope (ADR-013, DESIGN-042)"
// +kubebuilder:validation:XValidation:rule="!has(self.delegated) || self.type != 'credential'",message="no delegated policy approves a credential: credential execution re-checks a policy approver (DESIGN-042 6.2)"
// +kubebuilder:validation:XValidation:rule="!has(self.delegated) || !has(self.kube) || !self.kube.roles.exists(r, r == 'dev-env-grant-storage')",message="no delegated policy approves dev-env-grant-storage: destructive storage stays Tom's decision (DESIGN-042 RD4)"
// +kubebuilder:validation:XValidation:rule="(self.type == 'maintenance') == has(self.maintenance)",message="maintenance is set for type maintenance, and only there"
// +kubebuilder:validation:XValidation:rule="self.type != 'maintenance' || duration(self.maxTTL) <= duration('1h')",message="a maintenance grant lasts at most 1h"
// +kubebuilder:validation:XValidation:rule="(self.type == 'kube') == has(self.kube)",message="kube is set for type kube, and only there"
// +kubebuilder:validation:XValidation:rule="!has(self.kube) || !self.kube.roles.exists(r, r == 'dev-env-grant-namespace-operator') || has(self.attested) || has(self.delegated)",message="no standing policy approves dev-env-grant-namespace-operator: only an attested or delegated policy may (DESIGN-042 5.1)"
// +kubebuilder:validation:XValidation:rule="!has(self.attested) || !has(self.attested.allowSecretUse) || !self.attested.allowSecretUse || (has(self.kube) && self.kube.roles.exists(r, r == 'dev-env-grant-namespace-operator'))",message="attested.allowSecretUse is only for a policy that names dev-env-grant-namespace-operator (DESIGN-042 5.1)"
// +kubebuilder:validation:XValidation:rule="(self.type == 'egress') == has(self.egress)",message="egress is set for type egress, and only there"
// +kubebuilder:validation:XValidation:rule="(self.type == 'credential') == has(self.credential)",message="credential is set for type credential, and only there"
// +kubebuilder:validation:XValidation:rule="(has(self.attested) || has(self.delegated)) ? ((has(self.profiles) ? size(self.profiles) : 0) + (has(self.repos) ? size(self.repos) : 0) == 0 && (has(self.agents) ? size(self.agents) : 0) == 0 && (!has(self.attested) || (has(self.attested.profiles) ? size(self.attested.profiles) : 0) + (has(self.attested.repos) ? size(self.attested.repos) : 0) > 0) && (!has(self.delegated) || (has(self.delegated.profiles) ? size(self.delegated.profiles) : 0) + (has(self.delegated.repos) ? size(self.delegated.repos) : 0) > 0)) : ((has(self.profiles) ? size(self.profiles) : 0) + (has(self.repos) ? size(self.repos) : 0) > 0)",message="name the requesters: profiles, repos or both; an attested or delegated policy names them only under attested or delegated, in each block it carries, and leaves profiles, repos and agents empty, so a broker that predates either block matches nobody"
// +kubebuilder:validation:XValidation:rule="duration(self.maxTTL) >= duration('10m') && duration(self.maxTTL) <= duration('8h')",message="maxTTL runs from 10m to 8h"
// +kubebuilder:validation:XValidation:rule="self.type != 'credential' || duration(self.maxTTL) <= duration('4h')",message="a credential grant lasts at most 4h"
type GrantPolicySpec struct {
	// Description says what Tom accepts by this policy, for example that an
	// agent may read the Secrets frontend's workloads mount.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=1000
	// +required
	Description string `json:"description"`

	// Profiles are the requester profiles it matches. Profile ops never matches
	// (D-18: every grant an ops session asks for goes to Tom).
	// +listType=set
	// +kubebuilder:validation:MaxItems=8
	// +kubebuilder:validation:items:MaxLength=63
	// +kubebuilder:validation:items:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +kubebuilder:validation:items:XValidation:rule="self != 'ops'",message="no policy matches profile ops (D-18)"
	// +optional
	Profiles []string `json:"profiles,omitempty"`

	// Repos are the requester repos it matches.
	// +listType=set
	// +kubebuilder:validation:MaxItems=32
	// +kubebuilder:validation:items:MaxLength=100
	// +kubebuilder:validation:items:Pattern=`^[A-Za-z0-9_.-]+$`
	// +optional
	Repos []string `json:"repos,omitempty"`

	// Agents are the requester agent CLIs it matches; empty means any.
	// +listType=set
	// +kubebuilder:validation:MaxItems=3
	// +optional
	Agents []AgentKind `json:"agents,omitempty"`

	// Attested makes this an attested policy: it approves only a request whose
	// requesting session also recorded an agent attestation that it obtained the
	// user's or a coordinator's approval for exactly this grant. The attestation
	// is the agent's claim under delegated trust. It is not verified human
	// identity. The requesters are named here and nowhere else: profiles, repos
	// and agents above stay empty, which every broker that predates attestation
	// reads as "names no requester" and so matches nobody (fail closed on a
	// rollback). A broker approves through it only with --enable-attested-grants.
	// +optional
	Attested *AttestedRequesters `json:"attested,omitempty"`

	// Delegated makes this a delegated policy (DESIGN-042 6.2): it approves a
	// request only when an allowlisted delegate identity, authenticated by the
	// operator and never the requester or one of its ancestors, recorded a
	// decision bound to exactly this grant. A delegate decision is not a human
	// decision. The requesters are named here, as under attested, and the
	// top-level lists stay empty, so a broker that predates delegation matches
	// nobody. A policy may carry attested, delegated or both. A delegated
	// policy approves kube, egress or maintenance grants, never a credential or
	// dev-env-grant-storage. A broker approves through it only for a delegate
	// on its --grant-delegates.
	// +optional
	Delegated *DelegatedRequesters `json:"delegated,omitempty"`

	// Type is the grant type it approves.
	// +required
	Type GrantType `json:"type"`

	// Kube is the roles and namespaces it approves.
	// +optional
	Kube *KubePolicy `json:"kube,omitempty"`

	// Egress is the destinations it approves.
	// +optional
	Egress *EgressPolicy `json:"egress,omitempty"`

	// Credential is the credentials it approves.
	// +optional
	Credential *CredentialPolicy `json:"credential,omitempty"`

	// Maintenance is the one bounded maintenance envelope it approves
	// (ADR-013). Only an attested or delegated policy carries it: no standing
	// policy approves maintenance. A broker approves through its attested block
	// only with --enable-attested-grants and --enable-attested-maintenance,
	// and through its delegated block only for a listed delegate (DESIGN-042).
	// +optional
	Maintenance *MaintenancePolicy `json:"maintenance,omitempty"`

	// MaxTTL is the longest TTL it approves. A longer request goes to Tom.
	// +required
	MaxTTL metav1.Duration `json:"maxTTL"`
}

// AttestedRequesters are the requesters an attested policy matches, with the
// same meaning as the lists of GrantPolicySpec: the policy names its requesters
// (profiles, repos or both), a repo list narrows, an empty agents list is any.
type AttestedRequesters struct {
	// Profiles are the requester profiles it matches. Profile ops never
	// matches (D-18).
	// +listType=set
	// +kubebuilder:validation:MaxItems=8
	// +kubebuilder:validation:items:MaxLength=63
	// +kubebuilder:validation:items:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +kubebuilder:validation:items:XValidation:rule="self != 'ops'",message="no policy matches profile ops (D-18)"
	// +optional
	Profiles []string `json:"profiles,omitempty"`

	// Repos are the requester repos it matches.
	// +listType=set
	// +kubebuilder:validation:MaxItems=32
	// +kubebuilder:validation:items:MaxLength=100
	// +kubebuilder:validation:items:Pattern=`^[A-Za-z0-9_.-]+$`
	// +optional
	Repos []string `json:"repos,omitempty"`

	// Agents are the requester agent CLIs it matches; empty means any.
	// +listType=set
	// +kubebuilder:validation:MaxItems=3
	// +optional
	Agents []AgentKind `json:"agents,omitempty"`

	// AllowSecretUse lets this attested policy approve a
	// dev-env-grant-namespace-operator grant on the requester's own
	// attestation (mode (a), DESIGN-042 5.1). Without it, the policy approves
	// that role only through a delegate decision (mode (b)). The requester then
	// both names the Secrets and attests that someone approved them.
	// +optional
	AllowSecretUse bool `json:"allowSecretUse,omitempty"`
}

// DelegatedRequesters are the requesters a delegated policy matches (DESIGN-042
// 6.2), with the same meaning as AttestedRequesters: the policy names its
// requesters (profiles, repos or both), a repo list narrows, an empty agents
// list is any.
type DelegatedRequesters struct {
	// Profiles are the requester profiles it matches. Profile ops never
	// matches (D-18).
	// +listType=set
	// +kubebuilder:validation:MaxItems=8
	// +kubebuilder:validation:items:MaxLength=63
	// +kubebuilder:validation:items:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +kubebuilder:validation:items:XValidation:rule="self != 'ops'",message="no policy matches profile ops (D-18)"
	// +optional
	Profiles []string `json:"profiles,omitempty"`

	// Repos are the requester repos it matches.
	// +listType=set
	// +kubebuilder:validation:MaxItems=32
	// +kubebuilder:validation:items:MaxLength=100
	// +kubebuilder:validation:items:Pattern=`^[A-Za-z0-9_.-]+$`
	// +optional
	Repos []string `json:"repos,omitempty"`

	// Agents are the requester agent CLIs it matches; empty means any.
	// +listType=set
	// +kubebuilder:validation:MaxItems=3
	// +optional
	Agents []AgentKind `json:"agents,omitempty"`
}

// KubePolicy is the catalog roles a policy approves, in the namespaces it names.
//
// +kubebuilder:validation:XValidation:rule="self.roles.all(r, r == 'dev-env-grant-nodes') || (has(self.namespaces) && size(self.namespaces) > 0)",message="name the namespaces, one by one, for a namespaced role"
// +kubebuilder:validation:XValidation:rule="!has(self.deniedSecrets) || size(self.deniedSecrets) == 0 || self.roles.exists(r, r == 'dev-env-grant-namespace-operator')",message="only a policy for dev-env-grant-namespace-operator takes deniedSecrets (DESIGN-042 5.1)"
type KubePolicy struct {
	// Roles are catalog roles. No policy approves dev-env-grant-breakglass or
	// dev-env-grant-secrets-read.
	// +listType=set
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=4
	// +kubebuilder:validation:items:Enum=dev-env-grant-workloads;dev-env-grant-storage;dev-env-grant-secrets-read;dev-env-grant-nodes;dev-env-grant-breakglass;dev-env-grant-namespace-operator
	// +kubebuilder:validation:items:XValidation:rule="self != 'dev-env-grant-breakglass' && self != 'dev-env-grant-secrets-read'",message="no policy approves dev-env-grant-breakglass or dev-env-grant-secrets-read: Tom approves each one on the page"
	// +required
	Roles []string `json:"roles"`

	// DeniedSecrets are Secrets no dev-env-grant-namespace-operator grant
	// under this policy may name, in any of its namespaces: an exact name, or
	// a prefix ending in "*" such as "sh.helm.release.*" (DESIGN-042 5.1). The
	// policy does not list the Secrets it allows: each grant names its own,
	// and its approver decides them. So within the policy's namespaces an
	// approver may let an agent use any Secret not denied here.
	// +listType=set
	// +kubebuilder:validation:MaxItems=32
	// +kubebuilder:validation:items:MaxLength=253
	// +kubebuilder:validation:items:Pattern=`^([a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*|[a-z0-9][-a-z0-9.]*\*)$`
	// +optional
	DeniedSecrets []string `json:"deniedSecrets,omitempty"`

	// Namespaces are named one by one: no wildcard, no pattern (DESIGN-001
	// 6.12).
	// +listType=set
	// +kubebuilder:validation:MaxItems=32
	// +kubebuilder:validation:items:MaxLength=63
	// +kubebuilder:validation:items:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +kubebuilder:validation:items:XValidation:rule="!(self in ['dev-env-system', 'dev-agents', 'dev-tools'])",message="no grant reaches the dev-env namespaces (DESIGN-001 6.12)"
	// +optional
	Namespaces []string `json:"namespaces,omitempty"`
}

// EgressPolicy is the destinations a policy approves. A grant's FQDN matches an
// equal entry, or a "*." entry it is a subdomain of; a grant's CIDR must lie
// inside one of the policy's; an endpoint must equal one of the policy's; every
// port must be one of the policy's.
//
// +kubebuilder:validation:XValidation:rule="(has(self.fqdns) ? size(self.fqdns) : 0) + (has(self.cidrs) ? size(self.cidrs) : 0) + (has(self.endpoints) ? size(self.endpoints) : 0) > 0",message="name at least one destination: fqdns, cidrs or endpoints"
type EgressPolicy struct {
	// FQDNs are DNS names, each exact or with one leading "*.".
	// +listType=set
	// +kubebuilder:validation:MaxItems=32
	// +kubebuilder:validation:items:MaxLength=253
	// +kubebuilder:validation:items:Pattern=`^(\*\.)?([a-z0-9]([-a-z0-9]*[a-z0-9])?\.)+[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +kubebuilder:validation:items:XValidation:rule="!self.endsWith('.local')",message="in-cluster and .local names are not reached by name: use endpoints, or cidrs for a LAN host"
	// +optional
	FQDNs []string `json:"fqdns,omitempty"`

	// CIDRs are address ranges.
	// +listType=set
	// +kubebuilder:validation:MaxItems=32
	// +kubebuilder:validation:items:MaxLength=43
	// +kubebuilder:validation:items:XValidation:rule="isCIDR(self)",message="must be a CIDR such as 192.168.40.21/32"
	// +optional
	CIDRs []string `json:"cidrs,omitempty"`

	// Endpoints are in-cluster pods by namespace and labels.
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=16
	// +optional
	Endpoints []EgressEndpoint `json:"endpoints,omitempty"`

	// Ports are the ports it approves.
	// +listType=atomic
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	// +required
	Ports []GrantPort `json:"ports"`
}

// CredentialPolicy is the credentials a policy approves.
type CredentialPolicy struct {
	// Names are the credentials.
	// +listType=set
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=2
	// +required
	Names []CredentialName `json:"names"`
}

// MaintenancePolicy is the maintenance envelope an attested policy approves
// (ADR-013). A grant's envelope matches only when its node, node UID,
// resource, runtime profile and image equal the policy's; its capabilities are
// a subset of the policy's; its CPU and memory are at or below the caps; and
// every worker, device file and read-only mount it names is one the policy
// names exactly. Operation and restore stay the grant's audit intent: the
// policy does not approve command content.
//
// +kubebuilder:validation:XValidation:rule="!has(self.runtimeProfile) || (self.resource.matches('^GPU-[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12}$') && (!has(self.devices) || size(self.devices) == 0))",message="nvidia-utility requires an exact GPU UUID and no explicit devices"
type MaintenancePolicy struct {
	// Node is the exact target node name.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +required
	Node string `json:"node"`

	// NodeUID is the exact target node UID: a replaced node matches nothing.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	// +required
	NodeUID string `json:"nodeUID"`

	// Resource is the exact device or resource identity, such as a GPU UUID.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +required
	Resource string `json:"resource"`

	// RuntimeProfile is the grant's exact runtime profile; empty matches only
	// a grant without one.
	// +kubebuilder:validation:Enum=nvidia-utility
	// +optional
	RuntimeProfile string `json:"runtimeProfile,omitempty"`

	// Capabilities are the capabilities a grant may ask for: a grant's must be
	// a subset.
	// +listType=set
	// +kubebuilder:validation:MaxItems=2
	// +kubebuilder:validation:items:Enum=SYS_ADMIN;SYS_RAWIO
	// +optional
	Capabilities []string `json:"capabilities,omitempty"`

	// Image is the exact digest-pinned executor image.
	// +kubebuilder:validation:MaxLength=512
	// +kubebuilder:validation:Pattern=`^[a-zA-Z0-9./:_-]+@sha256:[a-f0-9]{64}$`
	// +required
	Image string `json:"image"`

	// MaxCPUMillis caps the grant's cpuMillis.
	// +kubebuilder:validation:Minimum=10
	// +kubebuilder:validation:Maximum=2000
	// +required
	MaxCPUMillis int64 `json:"maxCPUMillis"`

	// MaxMemoryMiB caps the grant's memoryMiB.
	// +kubebuilder:validation:Minimum=16
	// +kubebuilder:validation:Maximum=4096
	// +required
	MaxMemoryMiB int64 `json:"maxMemoryMiB"`

	// Workers are the delegated direct children a grant may name, each by
	// name, session UID and pod UID. Empty means a grant may name none.
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=8
	// +optional
	Workers []MaintenanceIdentity `json:"workers,omitempty"`

	// Devices are the exact device files a grant may name. Empty means none.
	// +listType=set
	// +kubebuilder:validation:MaxItems=8
	// +kubebuilder:validation:items:MaxLength=253
	// +kubebuilder:validation:items:Pattern=`^/dev/(nvidia[0-9]+|nvidiactl|nvidia-uvm|nvidia-uvm-tools|dri/renderD[0-9]+)$`
	// +optional
	Devices []string `json:"devices,omitempty"`

	// ReadOnlyMounts are the exact maintenance-inputs files a grant may name.
	// Empty means none.
	// +listType=set
	// +kubebuilder:validation:MaxItems=8
	// +kubebuilder:validation:items:MaxLength=253
	// +kubebuilder:validation:items:Pattern=`^/var/lib/maintenance-inputs/[^/]`
	// +optional
	ReadOnlyMounts []string `json:"readOnlyMounts,omitempty"`
}

// GrantPolicy is a standing approval, written in haynes-ops and applied by Flux
// to dev-env-system (DESIGN-001 6.12). The broker approves a matching request
// at once and records it under the policy's name.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Max-TTL",type=string,JSONPath=`.spec.maxTTL`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="self.metadata.name.size() <= 63 && self.metadata.name.matches('^[a-z0-9]([-a-z0-9]*[a-z0-9])?$')",message="metadata.name is a DNS label of at most 63 characters: a grant records it as policy/<name>"
type GrantPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +required
	Spec GrantPolicySpec `json:"spec"`
}

// GrantPolicyList is a list of GrantPolicy.
//
// +kubebuilder:object:root=true
type GrantPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []GrantPolicy `json:"items"`
}
