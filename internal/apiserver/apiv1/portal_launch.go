package apiv1

import (
	"regexp"
	"time"
)

// PortalLaunchPath is the portal reader's default-off launch endpoint. It
// exists only when the operator is given --portal-launch-repo.
const PortalLaunchPath = "/v1/portal/sessions"

// MaxPortalLaunchRepos bounds the repositories one operator may allow.
const MaxPortalLaunchRepos = 16

var repoName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)

// ValidRepoName is the AgentSession schema's rule for spec.repo: a name, not a
// path.
func ValidRepoName(s string) bool { return repoName.MatchString(s) && s != "." && s != ".." }

// PortalLaunchRequest is supplied only by the authenticated portal backend.
// ActorRef is an opaque backend-derived attribution, never a browser identity;
// it becomes the parent "portal/<actorRef>". The agent is always claude and
// the session always runs on the templates' default profile, so neither is a
// field. Prompt is required for a task and absent for local and remote.
type PortalLaunchRequest struct {
	RequestID string `json:"requestID"`
	ActorRef  string `json:"actorRef"`
	Repo      string `json:"repo"`
	Mode      string `json:"mode"`
	Model     string `json:"model"`
	Effort    string `json:"effort,omitempty"`
	Size      string `json:"size,omitempty"`
	Prompt    string `json:"prompt,omitempty"`
}

// Redact requests when accidentally formatted; Prompt is never receipt data.
func (PortalLaunchRequest) String() string     { return "[redacted portal launch]" }
func (r PortalLaunchRequest) GoString() string { return r.String() }

// PortalLaunchReceipt names the session a launch created or returned. Phase is
// what the object reports; Pending is expected, because the pod is starting.
type PortalLaunchReceipt struct {
	RequestID   string    `json:"requestID"`
	SessionName string    `json:"sessionName"`
	SessionUID  string    `json:"sessionUID"`
	Phase       string    `json:"phase"`
	ObservedAt  time.Time `json:"observedAt"`
}
