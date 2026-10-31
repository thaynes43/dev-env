// Reserved decision bindings implement a reserved grant decision (DESIGN-042 7.1, S7).
//
// A nil error means the binding is well formed. It does not authorize the
// caller, create a grant, delete an object, read a key, or prove a live
// policy check. The API and the broker recheck identity, the class list, and
// the grant. tomRef is an agent assertion, not verified human consent.
package grantattest

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

const (
	// ReservedVersion is the record format.
	ReservedVersion = 1
	// BoundsRevision is the bound revision. Any other value refuses.
	BoundsRevision = 1
	// ReservedWording is carried on every record. It is an agent assertion.
	ReservedWording = "agent assertion, not verified human consent"

	// ReservedMaxTTL is the reserved-class maximum. It is not a default, and
	// it is not the maintenance default of 30 minutes.
	ReservedMaxTTL = time.Hour
	// MinTTL is the existing grant floor. It is not a default.
	MinTTL = 10 * time.Minute
	// DecisionWindow is how long a pending grant remains decidable. It is
	// PendingWindow, not an approval TTL.
	DecisionWindow = PendingWindow
	// ReservedMaxClockSkew is how far a decision time may sit ahead of the server clock.
	ReservedMaxClockSkew = MaxClockSkew

	// MaxNamespaces matches KubeGrant.namespaces.
	MaxNamespaces = 16
	// MaxNamespaceSnapshot bounds an RD3b snapshot. A larger set refuses.
	MaxNamespaceSnapshot = 64
	// MaxStorageTargets bounds RD4 storage targets.
	MaxStorageTargets = 8
	// ReservedMaxBytes bounds an encoded record.
	ReservedMaxBytes = 16384

	ClassRD2         = "rd2"
	ClassRD3a        = "rd3a"
	ClassRD3b        = "rd3b"
	ClassRD4Storage  = "rd4-storage"
	ClassRD4Hardware = "rd4-hardware"

	// LeaseClassC4 is the only lease class EvaluateReservedHardware accepts.
	LeaseClassC4 = "C4"

	BindingPerGrantClusterRole = "per-grant-serviceaccount-clusterrolebinding"
	BindingNamespaceRole       = "namespace-rolebinding"
	BindingSnapshotRole        = "snapshot-namespace-rolebinding"
	BindingDeletionGuard       = "deletion-guard"
	BindingAuditIntent         = "audit-intent"

	KindPersistentVolumeClaim = "PersistentVolumeClaim"
	KindVolumeSnapshot        = "VolumeSnapshot"
	KindStatefulSet           = "StatefulSet"
)

var (
	// ErrDisabled means the class list is empty. The flag default is off.
	ErrDisabled = errors.New("reserved decisions are off")
	// ErrUnlisted means the class is known and not on the configured list.
	ErrUnlisted = errors.New("reserved decision class is not listed")
	// ErrReplay means a record is already present. An equal binding is included.
	ErrReplay = errors.New("reserved decision replay")
	// ErrRefused is every other refusal.
	ErrRefused = errors.New("reserved decision refused")
)

var (
	uidPattern         = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._:-]*[A-Za-z0-9])?$`)
	dnsLabel           = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	dnsName            = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)
	scopeDigestPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
)

// ReservedDefaultExclusions returns the three namespaces ordinary grants and RD3 stay
// outside. RD2 is not contained by this list. The strings match
// broker.DevEnvNamespaces; this package does not import the broker.
func ReservedDefaultExclusions() []string {
	return []string{"dev-env-system", "dev-agents", "dev-tools", "dev-env-budgets", "dev", "default", "flux-system", "kyverno", "external-secrets", "kube-system", "kube-public", "kube-node-lease", "cilium-secrets"}
}

// SecretVerbs returns the only verbs an RD3 record carries.
func SecretVerbs() []string { return []string{"get", "list"} }

// KnownClasses returns the class ids ParseReservedClasses accepts, in a new slice.
func KnownClasses() []string {
	return []string{ClassRD2, ClassRD3a, ClassRD3b, ClassRD4Storage, ClassRD4Hardware}
}

// Actor is the delegate the server has already authenticated.
type Actor struct {
	UID        string
	Session    string
	SessionUID string
}

// GrantView is the live grant fields the server copied. The helper does not
// read the API server.
type GrantView struct {
	Name             string
	UID              string
	Type             string
	Role             string
	Namespaces       []string
	TTL              time.Duration
	RequesterSession string
	RequesterUID     string
	Phase            string
	Release          bool
	Deleting         bool
	ScopeDigest      string
	CreatedAt        time.Time
}

// NamespaceBinding is one namespace name and its object UID.
type NamespaceBinding struct {
	Name string `json:"name"`
	UID  string `json:"uid"`
}

// StorageTarget is one object a storage deletion guard may later name.
type StorageTarget struct {
	Namespace string `json:"namespace"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	UID       string `json:"uid"`
}

// Request is the echoed reserved decision. Identity fields must match the
// server-filled Actor and GrantView.
type Request struct {
	Reserved               bool
	Approve                bool
	Class                  string
	BoundsRevision         int
	GrantUID               string
	RequesterUID           string
	ActorUID               string
	ActorSession           string
	Digest                 string
	TomRef                 string
	At                     time.Time
	TTL                    time.Duration
	Standing               bool
	Attested               bool
	RiskAccepted           bool
	Extend                 bool
	Namespaces             []string
	AllNamespaces          bool
	ClusterBindingFallback bool
	Snapshot               []NamespaceBinding
	Targets                []StorageTarget
	Verbs                  []string
	Operation              string
	Host                   string
	Device                 string
	Firmware               string
	LeaseClass             string
	LeaseUID               string
}

// Input is one grant-path evaluation.
type Input struct {
	// Exclusions is the explicit reviewed namespace bound. Nil uses proposed defaults;
	// a non-nil empty slice is an explicit configured exception, never a lookup fallback.
	Exclusions []string
	Classes    []string
	Actor      Actor
	Grant      GrantView
	Request    Request
	Existing   *Record
	Now        time.Time
}

// LeaseView is the caller's own C4 lease, as the server read it.
type LeaseView struct {
	Class     string
	UID       string
	OwnerUID  string
	Phase     string
	ExpiresAt time.Time
	Release   bool
	Ended     bool
}

// HardwareIntent is the audit text. It does not select a command target.
type HardwareIntent struct {
	Operation string
	Host      string
	Device    string
	Firmware  string
	TomRef    string
}

// HardwareInput is an RD4 hardware audit. It is not an S2 grant decision.
type HardwareInput struct {
	Classes        []string
	Actor          Actor
	Lease          LeaseView
	Intent         HardwareIntent
	At             time.Time
	Now            time.Time
	Existing       *Record
	BoundsRevision int
	RequesterUID   string
	ThroughS2      bool
	GrantUID       string
	ReadKey        bool
	CallHardware   bool
	EnforceCommand bool
	EnforceTarget  bool
	RiskAccepted   bool
	Standing       bool
	Attested       bool
	Extend         bool
	TTL            time.Duration
}

// Record is the stored binding. Authorizes is false on every accepted record.
type Record struct {
	Exclusions       []string      `json:"excludedNamespaces"`
	V                int           `json:"v"`
	Class            string        `json:"class"`
	BoundsRevision   int           `json:"boundsRevision"`
	GrantUID         string        `json:"grantUID,omitempty"`
	GrantName        string        `json:"grantName,omitempty"`
	RequesterUID     string        `json:"requesterUID,omitempty"`
	RequesterSession string        `json:"requesterSession,omitempty"`
	ActorUID         string        `json:"actorUID"`
	ActorSession     string        `json:"actorSession,omitempty"`
	ScopeDigest      string        `json:"scopeDigest,omitempty"`
	At               time.Time     `json:"at"`
	TTL              time.Duration `json:"ttl,omitempty"`
	TomRef           string        `json:"tomRef"`
	Wording          string        `json:"wording"`
	Digest           string        `json:"digest"`

	Namespaces    []string           `json:"namespaces,omitempty"`
	AllNamespaces bool               `json:"allNamespaces"`
	Snapshot      []NamespaceBinding `json:"snapshot,omitempty"`
	Targets       []StorageTarget    `json:"targets,omitempty"`
	Verbs         []string           `json:"verbs,omitempty"`
	Binding       string             `json:"binding"`

	LeaseUID       string    `json:"leaseUID,omitempty"`
	LeaseClass     string    `json:"leaseClass,omitempty"`
	LeaseExpiresAt time.Time `json:"leaseExpiresAt,omitempty"`
	Operation      string    `json:"operation,omitempty"`
	Host           string    `json:"host,omitempty"`
	Device         string    `json:"device,omitempty"`
	Firmware       string    `json:"firmware,omitempty"`

	Authorizes                     bool `json:"authorizes"`
	RiskAccepted                   bool `json:"riskAccepted"`
	LiveGrant                      bool `json:"liveGrant"`
	Deletes                        bool `json:"deletes"`
	ServerNamespaceQueryPerformed  bool `json:"serverNamespaceQueryPerformed"`
	EnforcesCommand                bool `json:"enforcesCommand"`
	EnforcesTarget                 bool `json:"enforcesTarget"`
	ReadsKey                       bool `json:"readsKey"`
	CallsHardware                  bool `json:"callsHardware"`
	PodExecCopyAddressed           bool `json:"podExecCopyAddressed"`
	StandingApproval               bool `json:"standingApproval"`
	AttestedApproval               bool `json:"attestedApproval"`
	ExtendsLease                   bool `json:"extendsLease"`
	ChangesCredentialPolicy        bool `json:"changesCredentialPolicy"`
	NeverContained                 bool `json:"neverContained"`
	ExcludesNever                  bool `json:"excludesNever"`
	RootEquivalent                 bool `json:"rootEquivalent"`
	EveryResourceVerb              bool `json:"everyResourceVerb"`
	DerivedImpersonationRecalled   bool `json:"derivedImpersonationRecalled"`
	DerivedTokensRecalled          bool `json:"derivedTokensRecalled"`
	SecretReadsBeyondNeverRecalled bool `json:"secretReadsBeyondNeverRecalled"`
	ClusterRoleBinding             bool `json:"clusterRoleBinding"`
	NamespaceRoleBindings          bool `json:"namespaceRoleBindings"`
	ServiceAccountPerGrant         bool `json:"serviceAccountPerGrant"`
}

// SnapshotRecheck is the comparison of a stored RD3b snapshot with a live
// name/UID list the server supplied. ServerNamespaceQueryPerformed is false.
type SnapshotRecheck struct {
	Bound                         []NamespaceBinding
	FutureExcluded                []string
	NeverExcluded                 []string
	ServerNamespaceQueryPerformed bool
}

// GuardInput is the later RD4 comparison. Other is an object the server saw
// that may collide with the bound target.
type GuardInput struct {
	Record        Record
	Grant         GrantView
	Target        StorageTarget
	Other         *StorageTarget
	OtherGrantUID string
	OtherEnded    bool
	OtherTomRef   string
}

// GuardResult reports a local match. Deletes and Authorizes stay false.
type GuardResult struct {
	Matches    bool
	Deletes    bool
	Authorizes bool
}

// ParseReservedClasses parses --reserved-decision-classes. Empty raw is the default
// and returns nil, nil. The result does not enable a route.
func ParseReservedClasses(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	known := KnownClasses()
	var out []string
	for _, part := range strings.Split(raw, ",") {
		class := strings.TrimSpace(part)
		switch {
		case class == "":
			return nil, fmt.Errorf("%w: empty reserved decision class", ErrRefused)
		case !slices.Contains(known, class):
			return nil, fmt.Errorf("%w: unknown reserved decision class %q", ErrRefused, class)
		case slices.Contains(out, class):
			return nil, fmt.Errorf("%w: reserved decision class %s is repeated", ErrRefused, class)
		}
		out = append(out, class)
	}
	slices.Sort(out)
	return out, nil
}

// EvaluateReserved binds a reserved grant decision. A nil error is not authorization.
func EvaluateReserved(in Input) (Record, error) {
	var err error
	in.Exclusions, err = NormalizeReservedExclusions(in.Exclusions)
	if err != nil {
		return Record{}, err
	}
	if err := gate(in.Classes, in.Request.Class); err != nil {
		return Record{}, err
	}
	if in.Request.Class == ClassRD4Hardware {
		return Record{}, refused("rd4-hardware is an audit intent, not a grant decision")
	}
	if in.Existing != nil {
		return Record{}, ErrReplay
	}
	if err := validateGrantRequest(in); err != nil {
		return Record{}, err
	}
	var (
		rec Record
	)
	switch in.Request.Class {
	case ClassRD2:
		rec, err = bindRD2(in)
	case ClassRD3a:
		rec, err = bindRD3a(in)
	case ClassRD3b:
		rec, err = bindRD3b(in)
	case ClassRD4Storage:
		rec, err = bindRD4Storage(in)
	default:
		err = refused("unknown reserved decision class")
	}
	if err != nil {
		return Record{}, err
	}
	return seal(rec)
}

// EvaluateReservedHardware binds an RD4 hardware audit intent. It does not enforce a
// command or a target, and it does not accept an S2 grant decision.
func EvaluateReservedHardware(in HardwareInput) (Record, error) {
	if err := gate(in.Classes, ClassRD4Hardware); err != nil {
		return Record{}, err
	}
	if in.Existing != nil {
		return Record{}, ErrReplay
	}
	if err := validateHardware(in); err != nil {
		return Record{}, err
	}
	rec := Record{
		V: ReservedVersion, Class: ClassRD4Hardware, BoundsRevision: BoundsRevision,
		RequesterUID: in.RequesterUID, ActorUID: in.Actor.UID, ActorSession: in.Actor.Session,
		At: in.At, TomRef: in.Intent.TomRef, Wording: ReservedWording, Binding: BindingAuditIntent,
		LeaseUID: in.Lease.UID, LeaseClass: LeaseClassC4, LeaseExpiresAt: in.Lease.ExpiresAt,
		Operation: in.Intent.Operation, Host: in.Intent.Host, Device: in.Intent.Device, Firmware: in.Intent.Firmware,
	}
	return seal(rec)
}

// RecheckSnapshot compares a stored RD3b snapshot with a live name/UID list.
// Names absent from the snapshot are excluded. A replaced or missing
// snapshotted UID refuses. This function does not query the API server.
func RecheckSnapshot(rec Record, live []NamespaceBinding) (SnapshotRecheck, error) {
	if err := rec.invariants(); err != nil {
		return SnapshotRecheck{}, err
	}
	if rec.Class != ClassRD3b {
		return SnapshotRecheck{}, refused("snapshot recheck requires an rd3b record")
	}
	if err := duplicateBindings(live); err != nil {
		return SnapshotRecheck{}, err
	}
	bound := map[string]string{}
	for _, item := range rec.Snapshot {
		bound[item.Name] = item.UID
	}
	seen := map[string]bool{}
	var future, neverExcluded []string
	for _, item := range live {
		if !validNamespace(item.Name) || !validUID(item.UID) {
			return SnapshotRecheck{}, refused("live namespace identity is malformed")
		}
		if slices.Contains(rec.Exclusions, item.Name) {
			neverExcluded = append(neverExcluded, item.Name)
			continue
		}
		uid, ok := bound[item.Name]
		if !ok {
			future = append(future, item.Name)
			continue
		}
		if uid != item.UID {
			return SnapshotRecheck{}, refused("snapshot namespace UID was replaced")
		}
		seen[item.Name] = true
	}
	for _, item := range rec.Snapshot {
		if !seen[item.Name] {
			return SnapshotRecheck{}, refused("snapshot namespace is absent")
		}
	}
	slices.Sort(future)
	slices.Sort(neverExcluded)
	return SnapshotRecheck{Bound: sortedSnapshot(rec.Snapshot), FutureExcluded: nilIfEmpty(future), NeverExcluded: nilIfEmpty(neverExcluded)}, nil
}

// DeletionGuard compares an RD4 storage record with one target. A match does
// not delete and does not authorize a delete.
func DeletionGuard(in GuardInput) (GuardResult, error) {
	if err := in.Record.invariants(); err != nil {
		return GuardResult{}, err
	}
	if in.Record.Class != ClassRD4Storage {
		return GuardResult{}, refused("deletion guard requires an rd4-storage record")
	}
	g := in.Grant
	if g.UID != in.Record.GrantUID || g.Name != in.Record.GrantName || g.Phase != string(v1alpha1.GrantActive) || g.Release || g.Deleting || v1alpha1.GrantPhase(g.Phase).Ended() {
		return GuardResult{}, refused("deletion guard requires the same active grant")
	}
	if !targetBound(in.Record.Targets, in.Target) {
		return GuardResult{}, refused("storage target UID is not bound")
	}
	if in.Other != nil {
		sameName := in.Other.Namespace == in.Target.Namespace && in.Other.Name == in.Target.Name
		if sameName && in.Other.UID != in.Target.UID {
			return GuardResult{}, refused("another UID has the same name and namespace")
		}
		if in.Other.UID == in.Target.UID && (in.Other.Name != in.Target.Name || in.Other.Namespace != in.Target.Namespace || in.Other.Kind != in.Target.Kind) {
			return GuardResult{}, refused("the bound UID identifies another object")
		}
	}
	if in.OtherGrantUID != "" && in.OtherGrantUID != in.Record.GrantUID {
		return GuardResult{}, refused("another grant UID cannot satisfy the deletion guard")
	}
	if in.OtherEnded {
		return GuardResult{}, refused("an ended grant cannot satisfy the deletion guard")
	}
	if in.OtherTomRef != "" && in.OtherTomRef != in.Record.TomRef {
		return GuardResult{}, refused("another ref cannot satisfy the deletion guard")
	}
	return GuardResult{Matches: true}, nil
}

// EncodeReserved returns the record JSON. The record must already satisfy invariants.
func EncodeReserved(r Record) (string, error) {
	if err := r.invariants(); err != nil {
		return "", err
	}
	b, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	if len(b) > ReservedMaxBytes {
		return "", refused("reserved decision record is too large")
	}
	return string(b), nil
}

// ParseReserved reads a record and refuses unknown fields, a bad digest, or a record
// that claims authorization. It does not check the clock or live objects.
func ParseReserved(raw string) (Record, error) {
	var rec Record
	if raw == "" || len(raw) > ReservedMaxBytes {
		return Record{}, refused("reserved decision record is missing or too large")
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rec); err != nil {
		return Record{}, fmt.Errorf("%w: unreadable reserved decision", ErrRefused)
	}
	if dec.More() {
		return Record{}, refused("unreadable reserved decision")
	}
	if err := rec.invariants(); err != nil {
		return Record{}, err
	}
	return rec, nil
}

// ReservedBindingDigest is the canonical digest of the binding. List order does not
// change it. The digest field is not an input.
func ReservedBindingDigest(r Record) string {
	doc := bindingDoc{
		Exclusions: sortedStrings(r.Exclusions),
		V:          r.V, Class: r.Class, BoundsRevision: r.BoundsRevision,
		GrantUID: r.GrantUID, GrantName: r.GrantName,
		RequesterUID: r.RequesterUID, RequesterSession: r.RequesterSession,
		ActorUID: r.ActorUID, ActorSession: r.ActorSession, ScopeDigest: r.ScopeDigest,
		At: r.At.UTC().Format(time.RFC3339), TomRef: r.TomRef, Wording: r.Wording,
		Namespaces: sortedStrings(r.Namespaces), AllNamespaces: r.AllNamespaces,
		Snapshot: sortedSnapshot(r.Snapshot), Targets: sortedTargets(r.Targets),
		Verbs: sortedStrings(r.Verbs), Binding: r.Binding,
		LeaseUID: r.LeaseUID, LeaseClass: r.LeaseClass,
		Operation: r.Operation, Host: r.Host, Device: r.Device, Firmware: r.Firmware,
		ExcludesNever: r.ExcludesNever, RootEquivalent: r.RootEquivalent, EveryResourceVerb: r.EveryResourceVerb,
		ClusterRoleBinding: r.ClusterRoleBinding, NamespaceRoleBindings: r.NamespaceRoleBindings,
		ServiceAccountPerGrant: r.ServiceAccountPerGrant,
	}
	if r.TTL != 0 {
		doc.TTL = r.TTL.String()
	}
	if !r.LeaseExpiresAt.IsZero() {
		doc.LeaseExpiresAt = r.LeaseExpiresAt.UTC().Format(time.RFC3339)
	}
	b, _ := json.Marshal(doc) // the document is strings, bools, and numbers
	sum := sha256.Sum256(b)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

type bindingDoc struct {
	Exclusions             []string           `json:"excludedNamespaces"`
	V                      int                `json:"v"`
	Class                  string             `json:"class"`
	BoundsRevision         int                `json:"boundsRevision"`
	GrantUID               string             `json:"grantUID,omitempty"`
	GrantName              string             `json:"grantName,omitempty"`
	RequesterUID           string             `json:"requesterUID,omitempty"`
	RequesterSession       string             `json:"requesterSession,omitempty"`
	ActorUID               string             `json:"actorUID"`
	ActorSession           string             `json:"actorSession,omitempty"`
	ScopeDigest            string             `json:"scopeDigest,omitempty"`
	At                     string             `json:"at"`
	TTL                    string             `json:"ttl,omitempty"`
	TomRef                 string             `json:"tomRef"`
	Wording                string             `json:"wording"`
	Namespaces             []string           `json:"namespaces,omitempty"`
	AllNamespaces          bool               `json:"allNamespaces"`
	Snapshot               []NamespaceBinding `json:"snapshot,omitempty"`
	Targets                []StorageTarget    `json:"targets,omitempty"`
	Verbs                  []string           `json:"verbs,omitempty"`
	Binding                string             `json:"binding"`
	LeaseUID               string             `json:"leaseUID,omitempty"`
	LeaseClass             string             `json:"leaseClass,omitempty"`
	LeaseExpiresAt         string             `json:"leaseExpiresAt,omitempty"`
	Operation              string             `json:"operation,omitempty"`
	Host                   string             `json:"host,omitempty"`
	Device                 string             `json:"device,omitempty"`
	Firmware               string             `json:"firmware,omitempty"`
	ExcludesNever          bool               `json:"excludesNever"`
	RootEquivalent         bool               `json:"rootEquivalent"`
	EveryResourceVerb      bool               `json:"everyResourceVerb"`
	ClusterRoleBinding     bool               `json:"clusterRoleBinding"`
	NamespaceRoleBindings  bool               `json:"namespaceRoleBindings"`
	ServiceAccountPerGrant bool               `json:"serviceAccountPerGrant"`
}

func seal(rec Record) (Record, error) {
	rec.Digest = ReservedBindingDigest(rec)
	if _, err := EncodeReserved(rec); err != nil {
		return Record{}, err
	}
	return rec, nil
}

func gate(classes []string, class string) error {
	if len(classes) == 0 {
		return ErrDisabled
	}
	if !slices.Contains(KnownClasses(), class) {
		return refused("unknown reserved decision class")
	}
	if !slices.Contains(classes, class) {
		return ErrUnlisted
	}
	return nil
}

func validateGrantRequest(in Input) error {
	req, g := in.Request, in.Grant
	switch {
	case !req.Reserved:
		return refused("reserved decision requires reserved true")
	case req.Standing || req.Attested:
		return refused("no standing or attested approval of a reserved class")
	case req.RiskAccepted:
		return refused("a reserved decision does not accept risk")
	case req.Extend:
		return refused("a reserved decision does not extend a grant or a lease")
	case req.ClusterBindingFallback:
		return refused("a reserved decision has no cluster binding fallback")
	case len(req.Verbs) != 0:
		return refused("verbs are the case contract, not a request field")
	case req.Operation != "" || req.Host != "" || req.Device != "" || req.Firmware != "" || req.LeaseClass != "" || req.LeaseUID != "":
		return refused("hardware fields are not a grant decision")
	case req.BoundsRevision != BoundsRevision:
		return refused("bounds revision does not match")
	case !ValidRef(req.TomRef):
		return refused("tomRef is not a nonsecret reference")
	case g.Type == string(v1alpha1.GrantCredential):
		return refused("credential decisions are not reserved grant decisions and are not sent through the S2 decision")
	case g.Type == string(v1alpha1.GrantMaintenance):
		return refused("maintenance is not a reserved class and its 30m default is not a reserved TTL")
	case !validGrantName(g.Name) || !validUID(g.UID) || !validUID(g.RequesterUID) || !validUID(in.Actor.UID):
		return refused("grant name or UID is malformed")
	case !validNamespace(g.RequesterSession):
		return refused("requester session is malformed")
	case req.GrantUID != g.UID || req.RequesterUID != g.RequesterUID || req.ActorUID != in.Actor.UID:
		return refused("grant, requester, or actor UID does not match the live values")
	case req.ActorSession != "" && req.ActorSession != in.Actor.Session:
		return refused("the request names another actor")
	case in.Actor.UID == g.RequesterUID || (in.Actor.Session != "" && in.Actor.Session == g.RequesterSession) || (in.Actor.SessionUID != "" && in.Actor.SessionUID == g.RequesterUID):
		return refused("a reserved grant decision cannot be a self grant")
	case !scopeDigestPattern.MatchString(g.ScopeDigest) || req.Digest != g.ScopeDigest:
		return refused("scope digest does not match the grant")
	case g.Release || g.Deleting || !undecided(g.Phase):
		return refused("grant is released, deleting, or no longer pending")
	}
	if err := validateTimes(g.CreatedAt, req.At, in.Now); err != nil {
		return err
	}
	if g.TTL < MinTTL || g.TTL > ReservedMaxTTL || req.TTL < MinTTL || req.TTL > g.TTL || req.TTL > ReservedMaxTTL {
		return refused("ttl is outside 10m to the reserved maximum of 1h")
	}
	return nil
}

func validateHardware(in HardwareInput) error {
	switch {
	case in.ThroughS2 || in.GrantUID != "":
		return refused("hardware audit is not an S2 grant decision")
	case in.ReadKey || in.CallHardware || in.EnforceCommand || in.EnforceTarget:
		return refused("hardware audit does not read a key, call hardware, or enforce a command or target")
	case in.Standing || in.Attested || in.RiskAccepted || in.Extend || in.TTL != 0:
		return refused("hardware audit does not approve, extend, or set a TTL")
	case in.BoundsRevision != BoundsRevision:
		return refused("bounds revision does not match")
	case !ValidRef(in.Intent.TomRef):
		return refused("tomRef is not a nonsecret reference")
	case in.Lease.Class != LeaseClassC4:
		return refused("hardware audit requires the caller's own C4 lease")
	case in.Lease.Phase != string(v1alpha1.GrantActive) || in.Lease.Release || in.Lease.Ended:
		return refused("hardware audit requires an active C4 lease")
	case !validUID(in.Lease.UID) || !validUID(in.Lease.OwnerUID) || !validUID(in.Actor.UID):
		return refused("lease or actor UID is malformed")
	case in.RequesterUID == "" || in.RequesterUID != in.Actor.UID || in.Lease.OwnerUID != in.Actor.UID:
		return refused("hardware audit requires the actor's own lease")
	case !validOperation(in.Intent.Operation) || !validExact(in.Intent.Host, 253) || !validExact(in.Intent.Device, 253) || !validExact(in.Intent.Firmware, 128):
		return refused("hardware intent requires an exact operation, host, device, and firmware")
	case !truncatedUTC(in.At) || !truncatedUTC(in.Now) || !truncatedUTC(in.Lease.ExpiresAt):
		return refused("times are UTC truncated to a second")
	case in.At.After(in.Now.Add(ReservedMaxClockSkew)):
		return refused("decision time is in the future")
	case !in.Now.Before(in.At.Add(DecisionWindow)):
		return refused("the audit intent has expired")
	case !in.Lease.ExpiresAt.After(in.Now):
		return refused("the C4 lease is expired")
	}
	return nil
}

func bindRD2(in Input) (Record, error) {
	g := in.Grant
	grantNS, err := plainSet(g.Namespaces)
	if err != nil {
		return Record{}, err
	}
	reqNS, err := plainSet(in.Request.Namespaces)
	if err != nil {
		return Record{}, err
	}
	if g.Type != string(v1alpha1.GrantBreakglass) || g.Role != v1alpha1.RoleBreakglass || len(grantNS) != 0 || len(reqNS) != 0 || in.Request.AllNamespaces || len(in.Request.Snapshot) != 0 || len(in.Request.Targets) != 0 {
		return Record{}, refused("rd2 requires breakglass cluster-wide with no namespaces")
	}
	rec := baseGrant(in)
	rec.Class = ClassRD2
	rec.Binding = BindingPerGrantClusterRole
	rec.RootEquivalent = true
	rec.EveryResourceVerb = true
	rec.ClusterRoleBinding = true
	rec.ServiceAccountPerGrant = true
	return rec, nil
}

func bindRD3a(in Input) (Record, error) {
	if in.Grant.Type != string(v1alpha1.GrantKube) || in.Grant.Role != v1alpha1.RoleSecretsRead || in.Request.AllNamespaces || len(in.Request.Snapshot) != 0 || len(in.Request.Targets) != 0 {
		return Record{}, refused("rd3a requires kube secrets-read in explicit namespaces")
	}
	grantNS, reqNS, err := echoedNamespaces(in.Grant.Namespaces, in.Request.Namespaces)
	if err != nil {
		return Record{}, err
	}
	if len(grantNS) < 1 || len(grantNS) > MaxNamespaces || !slices.Equal(grantNS, reqNS) {
		return Record{}, refused("rd3a namespaces must echo the grant's one to 16 names")
	}
	if err := rejectNever(grantNS, in.Exclusions); err != nil {
		return Record{}, err
	}
	rec := baseGrant(in)
	rec.Class = ClassRD3a
	rec.Binding = BindingNamespaceRole
	rec.Namespaces = grantNS
	rec.Verbs = SecretVerbs()
	rec.ExcludesNever = true
	rec.NamespaceRoleBindings = true
	rec.ServiceAccountPerGrant = true
	return rec, nil
}

func bindRD3b(in Input) (Record, error) {
	if in.Grant.Type != string(v1alpha1.GrantKube) || in.Grant.Role != v1alpha1.RoleSecretsRead || !in.Request.AllNamespaces || len(in.Request.Targets) != 0 || len(in.Request.Namespaces) != 0 || len(in.Grant.Namespaces) != 0 {
		return Record{}, refused("rd3b requires kube secrets-read with allNamespaces and no namespace list")
	}
	snap, err := normalizeSnapshot(in.Request.Snapshot, in.Exclusions)
	if err != nil {
		return Record{}, err
	}
	rec := baseGrant(in)
	rec.Class = ClassRD3b
	rec.Binding = BindingSnapshotRole
	rec.AllNamespaces = true
	rec.Snapshot = snap
	rec.Verbs = SecretVerbs()
	rec.ExcludesNever = true
	rec.NamespaceRoleBindings = true
	rec.ServiceAccountPerGrant = true
	return rec, nil
}

func bindRD4Storage(in Input) (Record, error) {
	if in.Grant.Type != string(v1alpha1.GrantKube) || in.Grant.Role != v1alpha1.RoleStorage || in.Request.AllNamespaces || len(in.Request.Snapshot) != 0 {
		return Record{}, refused("rd4-storage requires kube dev-env-grant-storage in one namespace")
	}
	grantNS, reqNS, err := echoedNamespaces(in.Grant.Namespaces, in.Request.Namespaces)
	if err != nil {
		return Record{}, err
	}
	if len(grantNS) != 1 || !slices.Equal(grantNS, reqNS) {
		return Record{}, refused("rd4-storage requires exactly one namespace")
	}
	if err := rejectNever(grantNS, in.Exclusions); err != nil {
		return Record{}, err
	}
	targets, err := normalizeTargets(in.Request.Targets, grantNS[0])
	if err != nil {
		return Record{}, err
	}
	rec := baseGrant(in)
	rec.Class = ClassRD4Storage
	rec.Binding = BindingDeletionGuard
	rec.Namespaces = grantNS
	rec.Targets = targets
	rec.ExcludesNever = true
	return rec, nil
}

func baseGrant(in Input) Record {
	return Record{
		V: ReservedVersion, BoundsRevision: BoundsRevision, Wording: ReservedWording, Exclusions: slices.Clone(in.Exclusions),
		GrantUID: in.Grant.UID, GrantName: in.Grant.Name,
		RequesterUID: in.Grant.RequesterUID, RequesterSession: in.Grant.RequesterSession,
		ActorUID: in.Actor.UID, ActorSession: in.Actor.Session,
		ScopeDigest: in.Grant.ScopeDigest, At: in.Request.At, TTL: in.Request.TTL, TomRef: in.Request.TomRef,
	}
}

func (r Record) invariants() error {
	if err := r.shape(); err != nil {
		return err
	}
	if r.Digest != ReservedBindingDigest(r) {
		return refused("binding digest does not match")
	}
	return nil
}

func (r Record) shape() error {
	if _, err := NormalizeReservedExclusions(r.Exclusions); err != nil {
		return err
	}
	if r.V != ReservedVersion || r.BoundsRevision != BoundsRevision || r.Wording != ReservedWording || r.Class == "" {
		return refused("record version, bounds, wording, or class is wrong")
	}
	if !ValidRef(r.TomRef) || !validUID(r.ActorUID) || !truncatedUTC(r.At) || !scopeDigestPattern.MatchString(r.Digest) {
		return refused("record identity, time, or digest is malformed")
	}
	if r.Authorizes || r.RiskAccepted || r.LiveGrant || r.Deletes || r.ServerNamespaceQueryPerformed || r.EnforcesCommand || r.EnforcesTarget || r.ReadsKey || r.CallsHardware || r.PodExecCopyAddressed || r.StandingApproval || r.AttestedApproval || r.ExtendsLease || r.ChangesCredentialPolicy || r.NeverContained || r.DerivedImpersonationRecalled || r.DerivedTokensRecalled || r.SecretReadsBeyondNeverRecalled {
		return refused("record claims authorization, risk acceptance, a live effect, or recall")
	}
	switch r.Class {
	case ClassRD2:
		return r.shapeRD2()
	case ClassRD3a, ClassRD3b:
		return r.shapeRD3()
	case ClassRD4Storage:
		return r.shapeStorage()
	case ClassRD4Hardware:
		return r.shapeHardware()
	default:
		return refused("unknown reserved decision class")
	}
}

func (r Record) shapeRD2() error {
	if r.Binding != BindingPerGrantClusterRole || !r.RootEquivalent || !r.EveryResourceVerb || !r.ClusterRoleBinding || !r.ServiceAccountPerGrant || r.NamespaceRoleBindings || r.ExcludesNever || r.AllNamespaces {
		return refused("rd2 binding flags are wrong")
	}
	if len(r.Namespaces) != 0 || len(r.Snapshot) != 0 || len(r.Targets) != 0 || len(r.Verbs) != 0 || r.LeaseUID != "" {
		return refused("rd2 carries no namespace, target, or lease list")
	}
	return r.shapeGrantIdentity()
}

func (r Record) shapeRD3() error {
	if !r.ExcludesNever || !r.NamespaceRoleBindings || !r.ServiceAccountPerGrant || r.ClusterRoleBinding || r.RootEquivalent || r.EveryResourceVerb || len(r.Targets) != 0 || r.LeaseUID != "" {
		return refused("rd3 binding flags are wrong")
	}
	if !slices.Equal(r.Verbs, SecretVerbs()) {
		return refused("rd3 verbs must be get and list")
	}
	if err := r.shapeGrantIdentity(); err != nil {
		return err
	}
	if r.Class == ClassRD3a {
		if r.Binding != BindingNamespaceRole || r.AllNamespaces || len(r.Snapshot) != 0 || len(r.Namespaces) < 1 || len(r.Namespaces) > MaxNamespaces {
			return refused("rd3a scope does not match its record")
		}
		return storedNamespaces(r.Namespaces, r.Exclusions)
	}
	if r.Binding != BindingSnapshotRole || !r.AllNamespaces || len(r.Namespaces) != 0 || len(r.Snapshot) < 1 || len(r.Snapshot) > MaxNamespaceSnapshot {
		return refused("rd3b scope does not match its record")
	}
	return storedSnapshot(r.Snapshot, r.Exclusions)
}

func (r Record) shapeStorage() error {
	if r.Binding != BindingDeletionGuard || !r.ExcludesNever || r.RootEquivalent || r.EveryResourceVerb || r.ClusterRoleBinding || r.NamespaceRoleBindings || r.ServiceAccountPerGrant || r.AllNamespaces || len(r.Snapshot) != 0 || len(r.Verbs) != 0 || r.LeaseUID != "" {
		return refused("rd4-storage binding flags are wrong")
	}
	if len(r.Namespaces) != 1 || len(r.Targets) < 1 || len(r.Targets) > MaxStorageTargets {
		return refused("rd4-storage scope does not match its record")
	}
	if err := r.shapeGrantIdentity(); err != nil {
		return err
	}
	if err := storedNamespaces(r.Namespaces, r.Exclusions); err != nil {
		return err
	}
	return storedTargets(r.Targets, r.Namespaces[0])
}

func (r Record) shapeHardware() error {
	if r.Binding != BindingAuditIntent || r.GrantUID != "" || r.GrantName != "" || r.ScopeDigest != "" || r.TTL != 0 || len(r.Namespaces) != 0 || len(r.Snapshot) != 0 || len(r.Targets) != 0 || r.AllNamespaces || r.RootEquivalent || r.EveryResourceVerb || r.ExcludesNever || r.ClusterRoleBinding || r.NamespaceRoleBindings || r.ServiceAccountPerGrant {
		return refused("hardware record is not an audit intent")
	}
	if r.LeaseClass != LeaseClassC4 || !validUID(r.LeaseUID) || !truncatedUTC(r.LeaseExpiresAt) || r.RequesterUID != r.ActorUID {
		return refused("hardware record is not the actor's own C4 lease")
	}
	if !validOperation(r.Operation) || !validExact(r.Host, 253) || !validExact(r.Device, 253) || !validExact(r.Firmware, 128) {
		return refused("hardware intent is malformed")
	}
	return nil
}

func (r Record) shapeGrantIdentity() error {
	if !validGrantName(r.GrantName) || !validUID(r.GrantUID) || !validUID(r.RequesterUID) || !validNamespace(r.RequesterSession) || !scopeDigestPattern.MatchString(r.ScopeDigest) {
		return refused("grant identity on the record is malformed")
	}
	if r.TTL < MinTTL || r.TTL > ReservedMaxTTL || r.RequesterUID == r.ActorUID {
		return refused("record ttl or actor binding is wrong")
	}
	return nil
}

func validateTimes(created, at, now time.Time) error {
	if !truncatedUTC(created) || !truncatedUTC(at) || !truncatedUTC(now) {
		return refused("times are UTC truncated to a second")
	}
	if at.Before(created) {
		return refused("decision time is before the grant existed")
	}
	if at.After(now.Add(ReservedMaxClockSkew)) {
		return refused("decision time is in the future")
	}
	if !now.Before(created.Add(DecisionWindow)) || !now.Before(at.Add(DecisionWindow)) {
		return refused("the decision time has expired")
	}
	return nil
}

func echoedNamespaces(grantNS, requestNS []string) ([]string, []string, error) {
	left, err := plainSet(grantNS)
	if err != nil {
		return nil, nil, err
	}
	right, err := plainSet(requestNS)
	if err != nil {
		return nil, nil, err
	}
	return left, right, nil
}

func plainSet(in []string) ([]string, error) {
	if hasDup(in) {
		return nil, refused("namespace list is duplicate or ambiguous")
	}
	out := append([]string(nil), in...)
	slices.Sort(out)
	for _, ns := range out {
		if !validNamespace(ns) {
			return nil, refused("namespace is not a DNS label")
		}
	}
	return nilIfEmpty(out), nil
}

func rejectNever(names []string, exclusions []string) error {
	for _, ns := range names {
		if slices.Contains(exclusions, ns) {
			return refused("namespace is outside the reserved scope")
		}
	}
	return nil
}

func storedNamespaces(in []string, exclusions []string) error {
	if hasDup(in) {
		return refused("namespace list is duplicate or ambiguous")
	}
	for _, ns := range in {
		if !validNamespace(ns) || slices.Contains(exclusions, ns) {
			return refused("namespace is outside the reserved scope")
		}
	}
	return nil
}

func normalizeSnapshot(in []NamespaceBinding, exclusions []string) ([]NamespaceBinding, error) {
	if len(in) < 1 || len(in) > MaxNamespaceSnapshot {
		return nil, refused("rd3b snapshot must name one to 64 namespaces")
	}
	if err := storedSnapshot(in, exclusions); err != nil {
		return nil, err
	}
	return sortedSnapshot(in), nil
}

func storedSnapshot(in []NamespaceBinding, exclusions []string) error {
	if err := duplicateBindings(in); err != nil {
		return err
	}
	for _, item := range in {
		switch {
		case !validNamespace(item.Name) || !validUID(item.UID):
			return refused("snapshot namespace identity is malformed")
		case slices.Contains(exclusions, item.Name):
			return refused("snapshot includes a never namespace")
		}
	}
	return nil
}

func normalizeTargets(in []StorageTarget, ns string) ([]StorageTarget, error) {
	if len(in) < 1 || len(in) > MaxStorageTargets {
		return nil, refused("rd4-storage requires one to eight targets")
	}
	if err := storedTargets(in, ns); err != nil {
		return nil, err
	}
	return sortedTargets(in), nil
}

func storedTargets(in []StorageTarget, ns string) error {
	seenUID := map[string]struct{}{}
	seenObj := map[string]struct{}{}
	for _, target := range in {
		if target.Namespace != ns || !validKind(target.Kind) || !validObjectName(target.Name) || !validUID(target.UID) {
			return refused("storage target identity is malformed")
		}
		obj := target.Kind + "\x00" + target.Namespace + "\x00" + target.Name
		if _, ok := seenUID[target.UID]; ok {
			return refused("storage target list is duplicate or ambiguous")
		}
		if _, ok := seenObj[obj]; ok {
			return refused("storage target list is duplicate or ambiguous")
		}
		seenUID[target.UID] = struct{}{}
		seenObj[obj] = struct{}{}
	}
	return nil
}

func duplicateBindings(in []NamespaceBinding) error {
	names := map[string]struct{}{}
	uids := map[string]struct{}{}
	for _, item := range in {
		if _, ok := names[item.Name]; ok {
			return refused("namespace list is duplicate or ambiguous")
		}
		if _, ok := uids[item.UID]; ok {
			return refused("namespace list is duplicate or ambiguous")
		}
		names[item.Name] = struct{}{}
		uids[item.UID] = struct{}{}
	}
	return nil
}

func targetBound(targets []StorageTarget, target StorageTarget) bool {
	return slices.ContainsFunc(targets, func(have StorageTarget) bool {
		return have == target
	})
}

func undecided(phase string) bool {
	return phase == "" || phase == string(v1alpha1.GrantPending)
}

func validUID(s string) bool {
	return len(s) <= 64 && uidPattern.MatchString(s)
}

func validNamespace(s string) bool {
	return len(s) >= 1 && len(s) <= 63 && dnsLabel.MatchString(s)
}

func validGrantName(s string) bool {
	return strings.HasPrefix(s, "grant-") && validNamespace(s)
}

func validObjectName(s string) bool {
	return len(s) >= 1 && len(s) <= 253 && dnsName.MatchString(s)
}

func validKind(s string) bool {
	return s == KindPersistentVolumeClaim || s == KindVolumeSnapshot || s == KindStatefulSet
}

func validExact(s string, maxLen int) bool {
	return len(s) >= 1 && len(s) <= maxLen && uidPattern.MatchString(s) && !strings.Contains(s, "*")
}

func validOperation(s string) bool {
	if s == "" || utf8.RuneCountInString(s) > 128 || !utf8.ValidString(s) || strings.Contains(s, "*") {
		return false
	}
	return !strings.ContainsFunc(s, func(r rune) bool {
		return unicode.IsControl(r) || unicode.In(r, unicode.Cf)
	})
}

func truncatedUTC(t time.Time) bool {
	return !t.IsZero() && t.Location() == time.UTC && t.Equal(t.Truncate(time.Second))
}

func hasDup(in []string) bool {
	seen := map[string]struct{}{}
	for _, s := range in {
		if _, ok := seen[s]; ok {
			return true
		}
		seen[s] = struct{}{}
	}
	return false
}

func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	slices.Sort(out)
	return nilIfEmpty(out)
}

func sortedSnapshot(in []NamespaceBinding) []NamespaceBinding {
	out := append([]NamespaceBinding(nil), in...)
	slices.SortFunc(out, func(a, b NamespaceBinding) int {
		if a.Name != b.Name {
			return strings.Compare(a.Name, b.Name)
		}
		return strings.Compare(a.UID, b.UID)
	})
	if len(out) == 0 {
		return nil
	}
	return out
}

func sortedTargets(in []StorageTarget) []StorageTarget {
	out := append([]StorageTarget(nil), in...)
	slices.SortFunc(out, func(a, b StorageTarget) int {
		if a.Namespace != b.Namespace {
			return strings.Compare(a.Namespace, b.Namespace)
		}
		if a.Kind != b.Kind {
			return strings.Compare(a.Kind, b.Kind)
		}
		if a.Name != b.Name {
			return strings.Compare(a.Name, b.Name)
		}
		return strings.Compare(a.UID, b.UID)
	})
	if len(out) == 0 {
		return nil
	}
	return out
}

func nilIfEmpty(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	return in
}

func refused(msg string) error {
	return fmt.Errorf("%w: %s", ErrRefused, msg)
}

// NormalizeReservedExclusions copies, validates and sorts explicit configured
// bounds. Nil means the proposed safe defaults; non-nil empty is explicit.
func NormalizeReservedExclusions(in []string) ([]string, error) {
	if in == nil {
		in = ReservedDefaultExclusions()
	}
	if len(in) > 64 {
		return nil, refused("too many excluded namespaces")
	}
	out, err := plainSet(in)
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []string{}
	}
	return out, nil
}

// ParseReservedExclusions parses an explicit flag value. Its caller supplies the
// proposed default string; empty explicitly selects an empty exclusion bound.
func ParseReservedExclusions(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return []string{}, nil
	}
	return NormalizeReservedExclusions(strings.Split(raw, ","))
}

// ValidateReservedShape shares the case scope checks before a decision exists.
// It checks no identity, approval, clock or reach and grants no authority.
func ValidateReservedShape(in Input) error {
	var err error
	in.Exclusions, err = NormalizeReservedExclusions(in.Exclusions)
	if err != nil {
		return err
	}
	if in.Grant.TTL < MinTTL || in.Grant.TTL > ReservedMaxTTL {
		return refused("reserved scope TTL is 10m to 1h")
	}
	switch in.Request.Class {
	case ClassRD2:
		_, err = bindRD2(in)
	case ClassRD3a:
		_, err = bindRD3a(in)
	case ClassRD3b:
		if !in.Request.AllNamespaces || in.Grant.Type != "kube" || in.Grant.Role != v1alpha1.RoleSecretsRead || len(in.Grant.Namespaces) != 0 || len(in.Request.Targets) != 0 {
			return refused("invalid reserved snapshot request")
		}
	case ClassRD4Storage:
		_, err = bindRD4Storage(in)
	default:
		err = refused("unknown reserved grant class")
	}
	return err
}
