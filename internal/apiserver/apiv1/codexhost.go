package apiv1

// Codex host selection (DESIGN-063 slice 2). The routes are off unless the
// operator was started with --enable-codex-host-select.

const (
	CodexHostsSelectPath  = "/v1/codex-hosts/select"
	CodexHostsReleasePath = "/v1/codex-hosts/release"
)

// CodexHostLeaseHeld is the release refusal when another caller holds the lease.
const CodexHostLeaseHeld = "lease_held"

// CodexHostLeaseHeldSentence is that refusal's one sentence.
const CodexHostLeaseHeldSentence = "Another caller holds this host's lease."

// CodexHostRouteUnsupported is what codex-host select prints when the operator
// does not serve the route.
const CodexHostRouteUnsupported = "The operator does not serve codex host selection."

// CodexHostSelectRequest is the body of POST /v1/codex-hosts/select.
type CodexHostSelectRequest struct {
	Repo    string `json:"repo"`
	Profile string `json:"profile,omitempty"`
}

// CodexHostSkip is one host selection did not choose.
type CodexHostSkip struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// CodexHostChoice is the section 5 object.
type CodexHostChoice struct {
	Host    string          `json:"host,omitempty"`
	Action  string          `json:"action,omitempty"`
	Ready   bool            `json:"ready"`
	Reason  string          `json:"reason,omitempty"`
	Skipped []CodexHostSkip `json:"skipped,omitempty"`
}

// CodexHostReleaseRequest names the host whose lease to clear.
type CodexHostReleaseRequest struct {
	Host string `json:"host"`
}

// CodexHostReleaseResponse says the caller's lease is gone.
type CodexHostReleaseResponse struct {
	Host     string `json:"host"`
	Released bool   `json:"released"`
}

// CodexHostSentence is the fixed sentence for a selection refusal. An action
// is not a refusal: the caller prints the object.
func CodexHostSentence(code string) (string, bool) {
	switch code {
	case "login_required":
		return "The shared login is lapsed. Nothing is resumed and nothing is created.", true
	case "caller_cannot_create":
		return "This caller may not create a Codex host.", true
	case "at_capacity":
		return "A create or a resume would exceed the running host cap.", true
	case "host_busy":
		return "Every suitable running host holds a live lease.", true
	default:
		return "", false
	}
}
