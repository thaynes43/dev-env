package pages

import (
	"context"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// AlertIdentity preserves startsAt exactly as supplied by Alertmanager. V1's
// incident hash uses those bytes; a refire is a different identity.
type AlertIdentity struct {
	Fingerprint string `json:"fingerprint"`
	StartsAt    string `json:"startsAt"`
}

// OrderReference names an existing V1 order attempt. Created is the V1 Unix
// seconds string: the key alone is reusable after a terminal attempt.
type OrderReference struct {
	Key     string `json:"key"`
	Created string `json:"created"`
}

// ResponderOrder is the needed subset of a V1 order plus an evidenced binding.
// Identity is an adapter assertion, not a field present in every V1 record.
type ResponderOrder struct {
	Identity AlertIdentity
	Order    OrderReference
	Status   string
	Updated  string
}

// CanonicalClaimReference identifies an existing record, never a new registry.
// TaskClaim IDs are namespace/name/UID; OpenProject IDs are positive decimals.
type CanonicalClaimReference struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// ClaimEvidence requires explicit workflow evidence. A TaskClaim's existence
// alone is insufficient to establish responding, needs-user or resolved.
type ClaimEvidence struct {
	Identity    AlertIdentity
	Order       OrderReference
	Reference   CanonicalClaimReference
	Owner       string
	State       string
	StateAt     time.Time
	EvidenceURL string
}

// SourceObservation distinguishes an available empty read from a failed read.
// Source and ObservedAt are assertions by the caller-authorized adapter.
type SourceObservation struct {
	Available  bool
	Source     string
	ObservedAt time.Time
}

// OrderObservation is one lookup; nil Order means an observed absent join.
type OrderObservation struct {
	SourceObservation
	Order *ResponderOrder
}

// ClaimObservation is one lookup; nil Claim means an observed absent claim.
type ClaimObservation struct {
	SourceObservation
	Claim *ClaimEvidence
}

// ResponderOrderSource reads an existing order by an exact incident binding.
// It must never derive an association from a reusable order key alone.
type ResponderOrderSource interface {
	FindOrder(context.Context, AlertIdentity) (OrderObservation, error)
}

// CanonicalClaimSource reads existing canonical ownership and workflow evidence.
type CanonicalClaimSource interface {
	FindClaim(context.Context, AlertIdentity, OrderReference) (ClaimObservation, error)
}

// ResponseSources has no production adapters. Zero values are unconfigured.
// MaxAge must be positive; it bounds observation age, not incident duration.
type ResponseSources struct {
	Orders ResponderOrderSource
	Claims CanonicalClaimSource
	MaxAge time.Duration
}

// ResponseProjection is the shared portal/MCP answer. Conditions explain why a
// field is Unknown without turning empty, failed or stale into resolution.
// State is the responder's recorded workflow state (JSON responseState), never
// the alert's own status: "resolved" can sit beside an alert still listed.
// Every Unknown field is the literal "Unknown", so an Unconfigured projection
// costs about 455 bytes per alert in MCP JSON (DESIGN-031 13).
type ResponseProjection struct {
	Identity        AlertIdentity           `json:"identity"`
	Order           OrderReference          `json:"order"`
	Claim           CanonicalClaimReference `json:"claim"`
	Owner           string                  `json:"owner"`
	State           string                  `json:"responseState"`
	StateAt         string                  `json:"responseStateAt"`
	EvidenceURL     string                  `json:"evidenceURL"`
	OrderCondition  string                  `json:"orderCondition"`
	ClaimCondition  string                  `json:"claimCondition"`
	OrderSource     string                  `json:"orderSource"`
	ClaimSource     string                  `json:"claimSource"`
	OrderObservedAt string                  `json:"orderObservedAt"`
	ClaimObservedAt string                  `json:"claimObservedAt"`
}

// ProjectResponse reads at most one order and one claim. There is no write,
// dispatch, retry, inferred owner, inferred resolution or wall-clock read.
func ProjectResponse(ctx context.Context, identity AlertIdentity, sources ResponseSources, at time.Time) ResponseProjection {
	out := unknownResponse(identity)
	if !validResponseIdentity(identity) {
		if !responseText(identity.Fingerprint) {
			out.Identity.Fingerprint = timeUnknown
		}
		if _, known := SnapshotTime(identity.StartsAt); !known {
			out.Identity.StartsAt = timeUnknown
		}
		out.OrderCondition = "Invalid"
		return out
	}
	if sources.Orders == nil {
		return out
	}
	if !knownResponseTime(at) || sources.MaxAge <= 0 {
		out.OrderCondition = "Invalid"
		return out
	}
	order, err := sources.Orders.FindOrder(ctx, identity)
	out.OrderCondition = observationCondition(order.SourceObservation, err, at, sources.MaxAge)
	if out.OrderCondition != "Available" {
		return out
	}
	out.OrderSource, out.OrderObservedAt = order.Source, responseTime(order.ObservedAt)
	if order.Order == nil {
		out.OrderCondition = "Empty"
		return out
	}
	o := *order.Order
	created, createdOK := orderTime(o.Order.Created)
	updated, updatedOK := orderTime(o.Updated)
	if !validOrderKey(o.Order.Key) || !createdOK || !updatedOK || updated.Before(created) || updated.After(order.ObservedAt) {
		out.OrderCondition = "Invalid"
		return out
	}
	if o.Identity != identity {
		out.OrderCondition = "Unjoined"
		return out
	}
	out.Order = o.Order
	if sources.Claims == nil {
		return out
	}
	claim, err := sources.Claims.FindClaim(ctx, identity, o.Order)
	out.ClaimCondition = observationCondition(claim.SourceObservation, err, at, sources.MaxAge)
	if out.ClaimCondition != "Available" {
		return out
	}
	out.ClaimSource, out.ClaimObservedAt = claim.Source, responseTime(claim.ObservedAt)
	if claim.Claim == nil {
		out.ClaimCondition = "Empty"
		return out
	}
	c := *claim.Claim
	if c.Identity != identity || c.Order != o.Order {
		out.ClaimCondition = "Unjoined"
		return out
	}
	if !validClaimReference(c.Reference) || !responseText(c.Owner) || !validResponseState(c.State) || !knownResponseTime(c.StateAt) || c.StateAt.Before(created) || c.StateAt.After(claim.ObservedAt) || !safeResponseURL(c.EvidenceURL) {
		out.ClaimCondition = "Invalid"
		return out
	}
	// A claim whose state disagrees with the order status confirms nothing, so
	// its owner and reference are withheld with the state, not shown unconfirmed.
	if !responseStateMatches(c.State, o.Status) {
		out.ClaimCondition = "Mismatched"
		return out
	}
	out.Claim, out.Owner = c.Reference, c.Owner
	out.State, out.StateAt, out.EvidenceURL = c.State, responseTime(c.StateAt), c.EvidenceURL
	return out
}

func unknownResponse(identity AlertIdentity) ResponseProjection {
	return ResponseProjection{
		Identity: identity, Order: OrderReference{Key: timeUnknown, Created: timeUnknown},
		Claim: CanonicalClaimReference{Kind: timeUnknown, ID: timeUnknown},
		Owner: timeUnknown, State: timeUnknown, StateAt: timeUnknown, EvidenceURL: timeUnknown,
		OrderCondition: "Unconfigured", ClaimCondition: "Unconfigured",
		OrderSource: timeUnknown, ClaimSource: timeUnknown, OrderObservedAt: timeUnknown, ClaimObservedAt: timeUnknown,
	}
}

func observationCondition(obs SourceObservation, err error, at time.Time, maxAge time.Duration) string {
	if err != nil || !obs.Available {
		return "Unavailable"
	}
	if !responseText(obs.Source) || !knownResponseTime(obs.ObservedAt) || obs.ObservedAt.After(at) {
		return "Invalid"
	}
	if at.Sub(obs.ObservedAt) > maxAge {
		return "Stale"
	}
	return "Available"
}

func validResponseState(state string) bool {
	switch state {
	case "claimed", "responding", "needs-user", "resolved":
		return true
	}
	return false
}

// responseStateMatches compares against the statuses V1 records: pending,
// claimed, done, failed and escalated. `order-status.sh working` is a
// heartbeat that bumps only `updated`, so "working" is never a status.
func responseStateMatches(state, status string) bool {
	switch state {
	case "claimed", "responding":
		return status == "claimed"
	case "needs-user":
		return status == "escalated" || status == "failed"
	case "resolved":
		return status == "done"
	}
	return false
}

func validResponseIdentity(identity AlertIdentity) bool {
	_, known := SnapshotTime(identity.StartsAt)
	return known && responseText(identity.Fingerprint)
}

func responseText(value string) bool {
	return value != timeUnknown && validToken(value, 512) && utf8.ValidString(value) && RedactText(value) == value && !strings.Contains(value, "://")
}

func validOrderKey(key string) bool {
	return responderOrderKey.MatchString(key)
}

var (
	responderOrderKey = regexp.MustCompile(`^(rem-responder|esc-responder|esc-rem)-[a-f0-9]{8}$`)
	claimNamespace    = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	claimName         = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$`)
	claimUID          = regexp.MustCompile(`^[A-Za-z0-9-]{1,128}$`)
)

func validClaimReference(ref CanonicalClaimReference) bool {
	switch ref.Kind {
	case "OpenProject":
		n, err := strconv.ParseUint(ref.ID, 10, 63)
		return err == nil && n > 0 && strconv.FormatUint(n, 10) == ref.ID
	case "TaskClaim":
		parts := strings.Split(ref.ID, "/")
		return len(parts) == 3 && claimNamespace.MatchString(parts[0]) && claimName.MatchString(parts[1]) && claimUID.MatchString(parts[2])
	}
	return false
}

func orderTime(raw string) (time.Time, bool) {
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || strconv.FormatInt(n, 10) != raw {
		return time.Time{}, false
	}
	stamp := time.Unix(n, 0).UTC()
	_, ok := SnapshotTime(responseTime(stamp))
	return stamp, ok
}

func responseTime(stamp time.Time) string { return stamp.UTC().Format(time.RFC3339Nano) }

func knownResponseTime(stamp time.Time) bool {
	_, known := SnapshotTime(responseTime(stamp))
	return known
}

// Evidence links are rendered as a portal href and returned as MCP
// evidenceURL; both must carry the same bytes. The character set excludes
// everything html/template would escape or normalize in an href (quotes,
// angle brackets, space, '&', '+', parentheses), and the input must already be
// Go's canonical form. The host must be a DNS name whose last label starts
// with a letter, which excludes IPv4 literals in every form a browser parses
// (127.1, 0x7f.1, 2130706433), IPv6 literals (no brackets), ports,
// single-label and .localhost names. This is not an origin allowlist; DESIGN-031
// 13 requires one before any real adapter lands.
var (
	evidenceURLChars = regexp.MustCompile(`^https://[A-Za-z0-9._~/%:@,;=!$*-]+$`)
	evidenceHost     = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`)
)

func safeResponseURL(value string) bool {
	if len(value) > 512 || !evidenceURLChars.MatchString(value) || RedactURL(value) != value || RedactText(value) != value {
		return false
	}
	u, err := url.Parse(value)
	if err != nil || u.String() != value || u.Scheme != "https" || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return false
	}
	if len(u.Host) > 253 || !evidenceHost.MatchString(u.Host) || strings.HasSuffix(u.Host, ".localhost") {
		return false
	}
	// Percent escapes must not decode into what the raw form excludes, and a
	// browser would resolve dot segments (including %2e) to a different path.
	for _, segment := range strings.Split(u.Path, "/") {
		if segment == "." || segment == ".." {
			return false
		}
	}
	return utf8.ValidString(u.Path) && !strings.ContainsFunc(u.Path, func(r rune) bool {
		return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.IsSpace(r) || strings.ContainsRune("\"'<>`\\", r)
	})
}
