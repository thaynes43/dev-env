package protocol

import (
	"fmt"
	"log/slog"
	"regexp"
	"time"
)

// Kube grants in the session pod (D-63). The operator gives every session pod a
// memory-backed volume at GrantsDir and points KUBECONFIG at the kubeconfig
// agentd keeps there. The broker installs a grant by exec: `agentd ctl
// grant-install`, with the token on the exec's stdin, never in argv or env.
const (
	// GrantsDir is where the pod's grants volume is mounted.
	GrantsDir = "/run/dev-env/grants"
	// GrantsDirEnv names that directory in the container's environment.
	GrantsDirEnv = "DEV_ENV_GRANTS_DIR"
	// PodUIDEnv fences an exec against a replacement pod of the same name.
	PodUIDEnv = "DEV_ENV_POD_UID"
	// PodNamespaceEnv preserves the baseline kubectl namespace.
	PodNamespaceEnv = "POD_NAMESPACE"
	// KubeconfigName is the kubeconfig agentd keeps in the grants directory.
	// It exists only while a grant is installed; without it kubectl and
	// client-go use the pod's in-cluster config.
	KubeconfigName = "kubeconfig"
	// KubeconfigEnv is the variable kubectl and client-go read.
	KubeconfigEnv = "KUBECONFIG"
	// DefaultContext is the kubeconfig context of the pod's own
	// ServiceAccount: what kubectl uses with no grant chosen.
	DefaultContext = "default"
	// ExitNoGrantsDir is the exit code of `agentd ctl grant-*` when the grants
	// directory is missing: the pod was built before D-63. The broker stops
	// trying such a pod after three attempts.
	ExitNoGrantsDir = 3
	// ExitNoCredential is credential-available's silent result when no live
	// grant is installed. Helpers may then use their baseline read credential.
	// credential-use may also return it when expiry raced the preflight, but
	// its callers must propagate all execution exits without retrying.
	ExitNoCredential = 4
)

// MaxGrantNameLength is a grant name's limit: it names a ServiceAccount, a
// kube context and a directory (D-54).
const MaxGrantNameLength = 63

var grantName = regexp.MustCompile(`^grant-[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// ValidGrantName checks a grant's name as the AccessGrant schema does:
// grant-<id>, a DNS label. agentd builds paths from it, so it checks before it
// touches the disk.
func ValidGrantName(name string) error {
	if len(name) > MaxGrantNameLength || !grantName.MatchString(name) {
		return fmt.Errorf("%q is not a grant name (grant-<id>, a DNS label of at most %d characters)", name, MaxGrantNameLength)
	}
	return nil
}

// GrantInstallCommand is what the broker runs in the session's container to
// install a kube grant. The token goes on the exec's stdin, one line.
func GrantInstallCommand(name, role string, namespaces []string, expires time.Time, podUID string) []string {
	cmd := []string{"agentd", "ctl", "grant-install", "--name", name, "--expires", expires.UTC().Format(time.RFC3339), "--role", role, "--pod-uid", podUID}
	for _, ns := range namespaces {
		cmd = append(cmd, "--namespace", ns)
	}
	return cmd
}

// GrantRemoveCommand is what the broker runs there when it revokes a grant.
func GrantRemoveCommand(name, podUID string) []string {
	return []string{"agentd", "ctl", "grant-remove", "--name", name, "--pod-uid", podUID}
}

// InstalledGrant is one kube grant in the pod: its grant.json, which holds no
// token, and an entry of `agentd ctl grant-list -o json`.
type InstalledGrant struct {
	Name       string    `json:"name"`
	Role       string    `json:"role"`
	Namespaces []string  `json:"namespaces,omitempty"`
	Expires    time.Time `json:"expires"`
	// InstalledAt is when the broker last installed it here.
	InstalledAt time.Time `json:"installedAt"`
	// Expired and Current are grant-list's, never stored: the grant is past
	// Expires, and it is the kubeconfig's current context.
	Expired bool `json:"expired,omitempty"`
	Current bool `json:"current,omitempty"`
}

// CredentialInstallCommand installs a provider credential through exec. The
// bounded CredentialPayload goes on stdin; argv contains metadata only.
func CredentialInstallCommand(name, grantUID, podUID string, expires time.Time) []string {
	return []string{"agentd", "ctl", "credential-install", "--name", name, "--grant-uid", grantUID, "--pod-uid", podUID, "--expires", expires.UTC().Format(time.RFC3339)}
}

// CredentialRemoveCommand fences cleanup against both replacement grants and
// replacement pods. A stale grant UID must not delete the replacement's file.
func CredentialRemoveCommand(name, grantUID, podUID string) []string {
	return []string{"agentd", "ctl", "credential-remove", "--name", name, "--grant-uid", grantUID, "--pod-uid", podUID}
}

// CredentialPayload is the private stdin wire document. Never serialize it
// into a status, log, or command argument. Its formatting methods redact it.
type CredentialPayload struct {
	Version     int    `json:"version"`
	Credential  string `json:"credential"`
	TokenID     string `json:"tokenID"`
	TokenSecret string `json:"tokenSecret"`
}

func (CredentialPayload) String() string   { return "CredentialPayload{redacted}" }
func (CredentialPayload) GoString() string { return "CredentialPayload{redacted}" }
func (CredentialPayload) LogValue() slog.Value {
	return slog.StringValue("CredentialPayload{redacted}")
}

// InstalledCredential is public metadata only. The private store keeps it
// together with the payload in one atomic file, so they cannot drift apart.
type InstalledCredential struct {
	Name        string    `json:"name"`
	GrantUID    string    `json:"grantUID"`
	PodUID      string    `json:"podUID"`
	Credential  string    `json:"credential"`
	Expires     time.Time `json:"expires"`
	InstalledAt time.Time `json:"installedAt"`
}
