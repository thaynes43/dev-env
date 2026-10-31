package pages

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
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
	// ClaimOwner is copied from the joined TaskClaim, never inferred from Owner.
	ClaimOwner  apiv1.TaskClaimOwner
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

// ResponderOrderSource reads one bounded snapshot for exact incident bindings.
// Missing entries are unobserved (Unavailable); an observed nil Order is Empty.
// It must never derive an association from a reusable order key alone.
type ResponderOrderSource interface {
	FindOrders(context.Context, []AlertIdentity) (map[AlertIdentity]OrderObservation, error)
}

// ClaimLookup preserves both incident identity and the existing order attempt.
type ClaimLookup struct {
	Identity AlertIdentity
	Order    OrderReference
}

// CanonicalClaimSource reads one bounded snapshot of canonical evidence for
// joined attempts. Missing entries are Unavailable; an observed nil Claim is Empty.
type CanonicalClaimSource interface {
	FindClaims(context.Context, []ClaimLookup) (map[ClaimLookup]ClaimObservation, error)
}

// ResponseSources has no production adapters. Zero values are unconfigured.
// MaxAge must be positive; it bounds observation age, not incident duration.
type ResponseSources struct {
	Orders ResponderOrderSource
	Claims CanonicalClaimSource
	MaxAge time.Duration
	// EvidenceOrigins lists exact HTTPS origins, without paths or trailing
	// slashes. Empty denies all links. Existing URL syntax checks still apply.
	EvidenceOrigins []string
}

// ResponseProjection is the shared portal/MCP answer. Conditions explain why a
// field is Unknown without turning empty, failed or stale into resolution.
// State is the responder's recorded workflow state (JSON responseState), never
// the alert's own status: "resolved" can sit beside an alert still listed.
// Every Unknown field is the literal "Unknown", so an Unconfigured projection
// costs about 513 bytes per alert in MCP JSON, guarded at 520 (DESIGN-031 13).
type ResponseProjection struct {
	ClaimOwner      apiv1.TaskClaimOwner    `json:"claimOwner"`
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

// ProjectResponse is the single-identity convenience wrapper. Snapshot callers
// must use ProjectResponses so source reads do not multiply with alert count.
func ProjectResponse(ctx context.Context, identity AlertIdentity, sources ResponseSources, at time.Time) ResponseProjection {
	return ProjectResponses(ctx, []AlertIdentity{identity}, sources, at)[identity]
}

// MaxResponseBatch bounds one snapshot's lookups independently of callers; the
// Alertmanager parser already refuses larger snapshots.
const MaxResponseBatch = 1000

var errResponseBatchTooLarge = errors.New("response batch exceeds MaxResponseBatch")

// ProjectResponses reads at most one order batch and one claim batch per
// snapshot. There is no write, dispatch, retry or wall-clock read. Results are
// indexed by the original identity, including for invalid identities. A batch
// over MaxResponseBatch makes no lookup and every requested alert is Unavailable.
// Adapters receive copies, so one cannot rewrite the keys results are read by.
func ProjectResponses(ctx context.Context, identities []AlertIdentity, sources ResponseSources, at time.Time) map[AlertIdentity]ResponseProjection {
	out := make(map[AlertIdentity]ResponseProjection, len(identities))
	requests := make([]AlertIdentity, 0, len(identities))
	for _, identity := range identities {
		if _, seen := out[identity]; seen {
			continue
		}
		out[identity] = projectResponse(identity, sources, at, OrderObservation{}, nil, ClaimObservation{}, nil)
		if validResponseIdentity(identity) && knownResponseTime(at) && sources.MaxAge > 0 {
			requests = append(requests, identity)
		}
	}
	if sources.Orders == nil || len(requests) == 0 {
		return out
	}
	if len(requests) > MaxResponseBatch {
		for _, identity := range requests {
			out[identity] = projectResponse(identity, sources, at, OrderObservation{}, errResponseBatchTooLarge, ClaimObservation{}, nil)
		}
		return out
	}
	orders, orderErr := sources.Orders.FindOrders(ctx, slices.Clone(requests))
	lookups := make([]ClaimLookup, 0, len(requests))
	for _, identity := range requests {
		response := projectResponse(identity, sources, at, orders[identity], orderErr, ClaimObservation{}, nil)
		out[identity] = response
		if response.OrderCondition == "Available" && sources.Claims != nil {
			lookups = append(lookups, ClaimLookup{Identity: identity, Order: response.Order})
		}
	}
	if len(lookups) == 0 {
		return out
	}
	claims, claimErr := sources.Claims.FindClaims(ctx, slices.Clone(lookups))
	for _, lookup := range lookups {
		out[lookup.Identity] = projectResponse(lookup.Identity, sources, at, orders[lookup.Identity], orderErr, claims[lookup], claimErr)
	}
	return out
}

// projectResponse validates only supplied observations; it performs no I/O.
func projectResponse(identity AlertIdentity, sources ResponseSources, at time.Time, order OrderObservation, orderErr error, claim ClaimObservation, claimErr error) ResponseProjection {
	out := unknownResponse(identity)
	if sources.Claims != nil {
		// Claims are looked up only after the order joins; until then say so
		// rather than calling a configured claim source Unconfigured.
		out.ClaimCondition = "NotChecked"
	}
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
	out.OrderCondition = observationCondition(order.SourceObservation, orderErr, at, sources.MaxAge)
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
	if !validOrderKey(o.Order.Key) || !validOrderStatus(o.Status) || !createdOK || !updatedOK || updated.Before(created) || updated.After(order.ObservedAt) {
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
	out.ClaimCondition = observationCondition(claim.SourceObservation, claimErr, at, sources.MaxAge)
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
	if !validClaimReference(c.Reference) || !responseText(c.Owner) || !validResponseState(c.State) || !knownResponseTime(c.StateAt) || c.StateAt.Before(created) || c.StateAt.After(claim.ObservedAt) || !safeResponseURL(c.EvidenceURL, sources.EvidenceOrigins) {
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
	if c.Reference.Kind == "TaskClaim" && validClaimOwner(c.ClaimOwner) {
		out.ClaimOwner = c.ClaimOwner
	}
	out.State, out.StateAt, out.EvidenceURL = c.State, responseTime(c.StateAt), c.EvidenceURL
	return out
}

func unknownResponse(identity AlertIdentity) ResponseProjection {
	return ResponseProjection{
		ClaimOwner: apiv1.TaskClaimOwner{Namespace: timeUnknown, Name: timeUnknown, UID: timeUnknown},
		Identity:   identity, Order: OrderReference{Key: timeUnknown, Created: timeUnknown},
		Claim: CanonicalClaimReference{Kind: timeUnknown, ID: timeUnknown},
		Owner: timeUnknown, State: timeUnknown, StateAt: timeUnknown, EvidenceURL: timeUnknown,
		OrderCondition: "Unconfigured", ClaimCondition: "Unconfigured",
		OrderSource: timeUnknown, ClaimSource: timeUnknown, OrderObservedAt: timeUnknown, ClaimObservedAt: timeUnknown,
	}
}

func validClaimOwner(owner apiv1.TaskClaimOwner) bool {
	if !claimNamespace.MatchString(owner.Namespace) || !claimName.MatchString(owner.Name) || !claimUID.MatchString(owner.UID) ||
		!responseText(owner.Namespace) || !responseText(owner.Name) || !responseText(owner.UID) {
		return false
	}
	for _, label := range strings.Split(owner.Name, ".") {
		if !claimNamespace.MatchString(label) {
			return false
		}
	}
	return true
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

// validOrderStatus is V1's recorded order vocabulary; anything else, such as
// "working" (order-status.sh working only bumps updated), is an invalid order.
func validOrderStatus(status string) bool {
	switch status {
	case "pending", "claimed", "done", "failed", "escalated":
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
// single-label and .localhost names. The exact-origin allowlist is an additional
// gate; an empty list denies every evidence link.
var (
	evidenceURLChars = regexp.MustCompile(`^https://[A-Za-z0-9._~/%:@,;=!$*-]+$`)
	evidenceHost     = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`)
)

func safeResponseURL(value string, origins []string) bool {
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
	allowed := false
	for _, origin := range origins {
		if origin == "https://"+u.Host {
			allowed = true
			break
		}
	}
	if !allowed {
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
