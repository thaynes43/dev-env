package apiv1

import "time"

// GrantsPath is the route of access grants (DESIGN-001 6.12, D-56).
const GrantsPath = "/v1/grants"

// GrantPath is the route of one grant.
func GrantPath(name string) string { return GrantsPath + "/" + name }

// Grant types (DESIGN-001 6.12). The LLM lease joins them in plan 09.
const (
	// GrantTypeKube is a catalog role in named namespaces, or cluster-wide for
	// dev-env-grant-nodes.
	GrantTypeKube = "kube"
	// GrantTypeEgress is destinations for the session's pod.
	GrantTypeEgress = "egress"
	// GrantTypeCredential is a short-lived credential in the session's pod
	// (Q-07).
	GrantTypeCredential = "credential"
	// GrantTypeBreakglass is role dev-env-grant-breakglass cluster-wide, which
	// Tom approves on the page (D-27).
	GrantTypeBreakglass = "breakglass"
)

// BreakglassRole is the role of every break-glass grant. The API sets it; a
// request leaves role empty or names this one.
const BreakglassRole = "dev-env-grant-breakglass"

// Credentials a credential grant installs (Q-07).
const (
	CredentialProxmox = "proxmox"
	CredentialHWSSH   = "hw-ssh"
)

// The TTL a request gets when it names none (D-56).
const (
	DefaultGrantTTL      = time.Hour
	DefaultBreakglassTTL = 30 * time.Minute
)

// Grant phases. A grant the broker has not seen yet shows as Pending.
const (
	GrantPending  = "Pending"
	GrantActive   = "Active"
	GrantDenied   = "Denied"
	GrantExpired  = "Expired"
	GrantReleased = "Released"
	GrantFailed   = "Failed"
)

// CreateGrantRequest is the body of POST /v1/grants. Only a session calls it,
// for its own pod. Unknown fields are refused.
//
// The caller never names the requester: the API takes the session, its repo,
// profile, agent and parent from the caller's token (D-25). Each type takes its
// own fields, and a field of another type is refused: kube and breakglass take
// role and namespaces; egress takes fqdns, cidrs, endpoints and ports;
// credential takes credential.
type CreateGrantRequest struct {
	// Type is kube, egress, credential or breakglass.
	Type string `json:"type"`
	// Role is a role from the grant catalog, for type kube. Break-glass is
	// always dev-env-grant-breakglass: leave it empty.
	Role string `json:"role,omitempty"`
	// Namespaces are where a namespaced role is bound, each named. The
	// cluster-wide roles (nodes, break-glass) take none.
	Namespaces []string `json:"namespaces,omitempty"`
	// FQDNs are DNS names, exact or with one leading "*.".
	FQDNs []string `json:"fqdns,omitempty"`
	// CIDRs are address ranges, such as 192.168.40.21/32.
	CIDRs []string `json:"cidrs,omitempty"`
	// Endpoints are in-cluster pods by namespace and labels.
	Endpoints []GrantEndpoint `json:"endpoints,omitempty"`
	// Ports are opened to every destination; at least one.
	Ports []GrantPort `json:"ports,omitempty"`
	// Credential is proxmox or hw-ssh.
	Credential string `json:"credential,omitempty"`
	// TTL is how long the grant lasts once approved, a Go duration such as 45m.
	// Empty means DefaultGrantTTL, or DefaultBreakglassTTL for break-glass.
	TTL string `json:"ttl,omitempty"`
	// Reason is why the session asks, in its own words. Tom reads it on the
	// approval page, marked as agent-written. Required.
	Reason string `json:"reason"`
}

// GrantEndpoint is in-cluster pods: every pod of a namespace, or the ones with
// these labels.
type GrantEndpoint struct {
	Namespace   string            `json:"namespace"`
	MatchLabels map[string]string `json:"matchLabels,omitempty"`
}

// GrantPort is one destination port. Protocol is TCP or UDP; empty means TCP.
type GrantPort struct {
	Port     int32  `json:"port"`
	Protocol string `json:"protocol,omitempty"`
}

// Grant is one grant as the API shows it: GET /v1/grants/{name}, the items of
// GET /v1/grants, and the answer to a request or a release.
type Grant struct {
	Name string `json:"name"`

	// The requester, from the session's token.
	Session string `json:"session"`
	Repo    string `json:"repo,omitempty"`
	Profile string `json:"profile,omitempty"`
	Agent   string `json:"agent,omitempty"`
	Parent  string `json:"parent,omitempty"`

	// The request.
	Type       string          `json:"type"`
	Role       string          `json:"role,omitempty"`
	Namespaces []string        `json:"namespaces,omitempty"`
	FQDNs      []string        `json:"fqdns,omitempty"`
	CIDRs      []string        `json:"cidrs,omitempty"`
	Endpoints  []GrantEndpoint `json:"endpoints,omitempty"`
	Ports      []GrantPort     `json:"ports,omitempty"`
	Credential string          `json:"credential,omitempty"`
	TTL        string          `json:"ttl"`
	Reason     string          `json:"reason"`
	// Release is set once the session gave the grant back; the broker then
	// ends it.
	Release   bool      `json:"release,omitempty"`
	CreatedAt time.Time `json:"createdAt"`

	// The broker's record.
	Phase      string     `json:"phase"`
	Message    string     `json:"message,omitempty"`
	ApprovedBy string     `json:"approvedBy,omitempty"`
	ApprovedAt *time.Time `json:"approvedAt,omitempty"`
	// ApprovedTTL is the TTL it was approved for, when Tom chose less time.
	ApprovedTTL     string     `json:"approvedTTL,omitempty"`
	DeniedBy        string     `json:"deniedBy,omitempty"`
	ExpiresAt       *time.Time `json:"expiresAt,omitempty"`
	EndedAt         *time.Time `json:"endedAt,omitempty"`
	NotifiedAt      *time.Time `json:"notifiedAt,omitempty"`
	ServiceAccount  string     `json:"serviceAccount,omitempty"`
	InstalledPodUID string     `json:"installedPodUID,omitempty"`
	InstalledAt     *time.Time `json:"installedAt,omitempty"`

	// ApprovalURL is the broker's approval page for this grant, while it is
	// pending. A coordinator shows it to Tom bare (D-26).
	ApprovalURL string `json:"approvalURL,omitempty"`
}

// GrantList is the body of GET /v1/grants, newest first.
type GrantList struct {
	Items []Grant `json:"items"`
}

// Grant list filters: the query parameters of GET /v1/grants, with FilterMine.
// Each is optional; an unknown parameter is refused.
const (
	// FilterSession matches the requesting session.
	FilterSession = "session"
	// FilterPhase matches the phase exactly: Pending, Active, Denied, Expired,
	// Released or Failed.
	FilterPhase = "phase"
)
