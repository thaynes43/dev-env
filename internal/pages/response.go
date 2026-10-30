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
type ResponseProjection struct {
	Identity        AlertIdentity           `json:"identity"`
	Order           OrderReference          `json:"order"`
	Claim           CanonicalClaimReference `json:"claim"`
	Owner           string                  `json:"owner"`
	State           string                  `json:"state"`
	StateAt         string                  `json:"stateAt"`
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
	if !validClaimReference(c.Reference) || !responseText(c.Owner) || !knownResponseTime(c.StateAt) || c.StateAt.Before(created) || c.StateAt.After(claim.ObservedAt) || !safeResponseURL(c.EvidenceURL) {
		out.ClaimCondition = "Invalid"
		return out
	}
	out.Claim, out.Owner = c.Reference, c.Owner
	// Ownership can be evidenced even when no supported workflow state is.
	if !responseStateMatches(c.State, o.Status) {
		out.ClaimCondition = "Unjoined"
		return out
	}
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

func responseStateMatches(state, status string) bool {
	switch state {
	case "claimed", "responding":
		return status == "claimed" || status == "working"
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

func safeResponseURL(value string) bool {
	if value == "" || value == timeUnknown || len(value) > 512 || !utf8.ValidString(value) || RedactURL(value) != value || RedactText(value) != value {
		return false
	}
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(value, "#") {
		return false
	}
	decoded := u.Host + u.Path
	return utf8.ValidString(decoded) && !strings.ContainsFunc(value+decoded, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) })
}
