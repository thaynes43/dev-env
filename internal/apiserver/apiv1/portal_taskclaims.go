package apiv1

import (
	"errors"
	"regexp"
	"strings"
	"time"
)

// Portal task-claims read (DESIGN-045, 2026-11-02). One project's existing
// TaskClaims for the portal reader, as an allowlist: no actor, revisions,
// default branch, pin state, reported head, owner namespace or stop state.
// A claim is not a grant, an approval or permission to act, and an Unknown
// state never means that no claims exist.
const (
	PortalTaskClaimsPath = "/v1/portal/task-claims"
	// PortalTaskClaimsVersion is the only response version a consumer accepts.
	PortalTaskClaimsVersion = "portaltaskclaims.v1"

	PortalTaskClaimsQueryProject = "project"
	PortalTaskClaimsQueryLimit   = "limit"
	PortalTaskClaimsQueryCursor  = "cursor"

	PortalTaskClaimsDefaultLimit = 50
	PortalTaskClaimsMaxLimit     = 100
	// PortalTaskClaimsCursorMax bounds the opaque base64url continuation.
	PortalTaskClaimsCursorMax = 2048
	// PortalTaskClaimsMaxBytes is the response cap, including the encoder's
	// trailing newline. A larger page is Unknown/Unavailable, never truncated.
	PortalTaskClaimsMaxBytes = 256 << 10
	// PortalTaskClaimsFreshForSeconds is the operator's portalFreshFor. A
	// heartbeat within it is observed; an older one is stale.
	PortalTaskClaimsFreshForSeconds int64 = 900

	PortalTaskClaimsStateAvailable = "Available"
	PortalTaskClaimsStateEmpty     = "Empty"
	PortalTaskClaimsStateUnknown   = "Unknown"

	PortalTaskClaimsReasonComplete         = "Complete"
	PortalTaskClaimsReasonTruncated        = "Truncated"
	PortalTaskClaimsReasonUnconfigured     = "Unconfigured"
	PortalTaskClaimsReasonScopeUnconfirmed = "ScopeUnconfirmed"
	PortalTaskClaimsReasonUnavailable      = "Unavailable"

	// Owner states: a same-UID Session's phase, or Deleting when it has a
	// deletion time. Replaced means the name now has another UID.
	PortalClaimOwnerPending   = "Pending"
	PortalClaimOwnerRunning   = "Running"
	PortalClaimOwnerIdle      = "Idle"
	PortalClaimOwnerDraining  = "Draining"
	PortalClaimOwnerSuspended = "Suspended"
	PortalClaimOwnerArchived  = "Archived"
	PortalClaimOwnerFailed    = "Failed"
	PortalClaimOwnerDeleting  = "Deleting"
	PortalClaimOwnerReplaced  = "Replaced"
	PortalClaimOwnerNotFound  = "NotFound"
	PortalClaimOwnerUnknown   = "Unknown"

	PortalClaimFreshnessObserved = "observed"
	PortalClaimFreshnessStale    = "stale"
	PortalClaimFreshnessUnknown  = "unknown"
)

// PortalTaskClaimOwner is the claim's owner and the operator's observation of
// it. HeartbeatAt is absent when the heartbeat is missing or in the future.
type PortalTaskClaimOwner struct {
	Name        string     `json:"name"`
	UID         string     `json:"uid"`
	State       string     `json:"state"`
	Freshness   string     `json:"freshness"`
	HeartbeatAt *time.Time `json:"heartbeatAt,omitempty"`
}

// PortalTaskClaimItem is one claim's allowlisted fields. GitHub is the
// provenance owner/repo, sent only so the portal can build a link.
type PortalTaskClaimItem struct {
	Name       string               `json:"name"`
	UID        string               `json:"uid"`
	Repository string               `json:"repository"`
	TaskKey    string               `json:"taskKey"`
	GitHub     string               `json:"github"`
	CreatedAt  time.Time            `json:"createdAt"`
	Owner      PortalTaskClaimOwner `json:"owner"`
}

// PortalTaskClaims is one store page. NextCursor is set only when Reason is
// Truncated. Empty comes only from a complete first page with no items.
type PortalTaskClaims struct {
	Version    string                `json:"version"`
	Project    string                `json:"project"`
	State      string                `json:"state"`
	Reason     string                `json:"reason"`
	Items      []PortalTaskClaimItem `json:"items"`
	Limit      int                   `json:"limit"`
	NextCursor string                `json:"nextCursor,omitempty"`
	Complete   bool                  `json:"complete"`
	ObservedAt time.Time             `json:"observedAt"`
}

// The JSON key sets a strict consumer accepts. Optional keys are listed last
// and named by the Optional sets.
var (
	PortalTaskClaimsKeys         = []string{"version", "project", "state", "reason", "items", "limit", "complete", "observedAt"}
	PortalTaskClaimsOptional     = []string{"nextCursor"}
	PortalTaskClaimItemKeys      = []string{"name", "uid", "repository", "taskKey", "github", "createdAt", "owner"}
	PortalTaskClaimOwnerKeys     = []string{"name", "uid", "state", "freshness"}
	PortalTaskClaimOwnerOptional = []string{"heartbeatAt"}
)

// ErrPortalTaskClaimsInvalid is the one refusal of ValidatePortalTaskClaims.
var ErrPortalTaskClaimsInvalid = errors.New("portal task claims page invalid")

// The grammars mirror internal/taskclaim and projectcatalog for the
// allowlisted fields only; apiv1 imports the standard library alone.
var (
	portalClaimName    = regexp.MustCompile(`^task-claim\.[a-f0-9]{32}\.[a-f0-9]{32}$`)
	portalClaimUID     = regexp.MustCompile(`^[A-Za-z0-9-]{1,128}$`)
	portalClaimProject = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
	portalClaimKey     = regexp.MustCompile(`^(issue:[1-9][0-9]{0,18}|task:[a-z0-9]([a-z0-9._-]{0,126}[a-z0-9])?)$`)
	portalClaimDNS     = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$`)
	portalClaimGitHub  = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,37}[a-z0-9])?/[a-z0-9_.-]{1,100}$`)
	portalClaimCursor  = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	portalClaimIssue   = regexp.MustCompile(`^issue:([1-9][0-9]{0,18})$`)
)

// ValidPortalTaskClaimsProject is projectcatalog.ValidName's grammar.
func ValidPortalTaskClaimsProject(project string) bool {
	return portalClaimProject.MatchString(project)
}

// ValidPortalTaskClaimsCursor is the opaque continuation's grammar.
func ValidPortalTaskClaimsCursor(cursor string) bool {
	return len(cursor) <= PortalTaskClaimsCursorMax && portalClaimCursor.MatchString(cursor)
}

// ValidPortalClaimGitHub is the TaskClaim provenance owner/repo grammar.
func ValidPortalClaimGitHub(identity string) bool {
	if len(identity) > 140 || !portalClaimGitHub.MatchString(identity) {
		return false
	}
	owner, _, _ := strings.Cut(identity, "/")
	return !strings.Contains(owner, "--") && !strings.HasSuffix(identity, "/.") && !strings.HasSuffix(identity, "/..") && !strings.HasSuffix(identity, ".git")
}

// PortalClaimIssueNumber returns n for an exact issue:<n> task key.
func PortalClaimIssueNumber(taskKey string) (string, bool) {
	match := portalClaimIssue.FindStringSubmatch(taskKey)
	if match == nil {
		return "", false
	}
	return match[1], true
}

func portalClaimOwnerState(state string) bool {
	switch state {
	case PortalClaimOwnerPending, PortalClaimOwnerRunning, PortalClaimOwnerIdle, PortalClaimOwnerDraining, PortalClaimOwnerSuspended,
		PortalClaimOwnerArchived, PortalClaimOwnerFailed, PortalClaimOwnerDeleting, PortalClaimOwnerReplaced, PortalClaimOwnerNotFound, PortalClaimOwnerUnknown:
		return true
	default:
		return false
	}
}

// ValidatePortalTaskClaims checks one page against the contract: version,
// state and reason pairs, paging, bounds and every item. It reads no clock;
// freshness is checked against the page's own ObservedAt.
//
// Rules the operator must follow so its pages pass:
//   - Items is never null: an Unknown or Empty page sends [] with Complete
//     false (Unknown) and the request's Limit.
//   - An owner that is Replaced, NotFound or Unknown has Freshness unknown and
//     no HeartbeatAt: no same-UID Session was read.
//   - Freshness is computed against the exact ObservedAt the page reports, so
//     take ObservedAt after the owner reads, or compute against it.
//   - NextCursor and HeartbeatAt are omitted when absent, never "".
//   - A complete first page with no items is Empty, not Available.
//
// A CreatedAt later than ObservedAt is accepted; the portal shows it as Unknown.
func ValidatePortalTaskClaims(page PortalTaskClaims) error {
	if page.Version != PortalTaskClaimsVersion || !portalClaimProject.MatchString(page.Project) || page.ObservedAt.IsZero() ||
		page.Limit < 1 || page.Limit > PortalTaskClaimsMaxLimit || page.Items == nil || len(page.Items) > page.Limit {
		return ErrPortalTaskClaimsInvalid
	}
	switch {
	case page.State == PortalTaskClaimsStateAvailable && page.Reason == PortalTaskClaimsReasonComplete:
		if !page.Complete || page.NextCursor != "" {
			return ErrPortalTaskClaimsInvalid
		}
	case page.State == PortalTaskClaimsStateAvailable && page.Reason == PortalTaskClaimsReasonTruncated:
		if page.Complete || !ValidPortalTaskClaimsCursor(page.NextCursor) {
			return ErrPortalTaskClaimsInvalid
		}
	case page.State == PortalTaskClaimsStateEmpty && page.Reason == PortalTaskClaimsReasonComplete:
		if !page.Complete || page.NextCursor != "" || len(page.Items) != 0 {
			return ErrPortalTaskClaimsInvalid
		}
	case page.State == PortalTaskClaimsStateUnknown && (page.Reason == PortalTaskClaimsReasonUnconfigured || page.Reason == PortalTaskClaimsReasonScopeUnconfirmed || page.Reason == PortalTaskClaimsReasonUnavailable):
		if page.Complete || page.NextCursor != "" || len(page.Items) != 0 {
			return ErrPortalTaskClaimsInvalid
		}
	default:
		return ErrPortalTaskClaimsInvalid
	}
	names, uids := map[string]bool{}, map[string]bool{}
	for _, item := range page.Items {
		if !validPortalTaskClaimItem(item, page.ObservedAt) || names[item.Name] || uids[item.UID] {
			return ErrPortalTaskClaimsInvalid
		}
		names[item.Name], uids[item.UID] = true, true
	}
	return nil
}

func validPortalTaskClaimItem(item PortalTaskClaimItem, observedAt time.Time) bool {
	if !portalClaimName.MatchString(item.Name) || !portalClaimUID.MatchString(item.UID) || !portalClaimProject.MatchString(item.Repository) ||
		!portalClaimKey.MatchString(item.TaskKey) || !ValidPortalClaimGitHub(item.GitHub) || item.CreatedAt.IsZero() {
		return false
	}
	owner := item.Owner
	if !portalClaimDNS.MatchString(owner.Name) || len(owner.Name) > 253 || !portalClaimUID.MatchString(owner.UID) || !portalClaimOwnerState(owner.State) {
		return false
	}
	switch owner.State {
	case PortalClaimOwnerReplaced, PortalClaimOwnerNotFound, PortalClaimOwnerUnknown:
		// No same-UID Session was read, so there is no heartbeat to report.
		return owner.Freshness == PortalClaimFreshnessUnknown && owner.HeartbeatAt == nil
	}
	fresh := time.Duration(PortalTaskClaimsFreshForSeconds) * time.Second
	switch owner.Freshness {
	case PortalClaimFreshnessUnknown:
		return owner.HeartbeatAt == nil
	case PortalClaimFreshnessObserved:
		return owner.HeartbeatAt != nil && !owner.HeartbeatAt.IsZero() && !owner.HeartbeatAt.After(observedAt) && observedAt.Sub(*owner.HeartbeatAt) <= fresh
	case PortalClaimFreshnessStale:
		return owner.HeartbeatAt != nil && !owner.HeartbeatAt.IsZero() && observedAt.Sub(*owner.HeartbeatAt) > fresh
	default:
		return false
	}
}
