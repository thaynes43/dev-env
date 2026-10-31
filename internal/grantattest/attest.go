// Package grantattest is the agent attestation of an access grant: a claim, made
// by the requesting session under delegated trust, that it obtained the user's
// or a coordinator's approval for exactly one grant. It is recorded as one
// annotation on the AccessGrant. It is NOT verified human identity: nothing here
// proves a human approved anything, and no text built from it may say so.
//
// The package is pure (no I/O, no Kubernetes client), so the operator's API,
// which writes the annotation, and the broker, which verifies it before it
// approves through an attested GrantPolicy, share one definition of the
// statement, its bounds and the scope digest.
package grantattest

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

const (
	// Annotation holds the statement on the AccessGrant. The operator's API is
	// its only writer; an agent has no write on grants.
	Annotation = v1alpha1.LabelPrefix + "approval-attestation"
	// Version is the statement's format.
	Version = 1
	// MaxBytes bounds the annotation value.
	MaxBytes = 2048
	// PendingWindow is how long a grant waits for an answer. The broker's
	// PendingTimeout is this value (a test pins that).
	PendingWindow = 30 * time.Minute
	// MaxClockSkew is how far apart the operator's and the broker's clocks may
	// be before a statement looks like it comes from the future.
	MaxClockSkew = time.Minute

	// ApproverUser and ApproverCoordinator are the claimed approver kinds.
	ApproverUser        = "user"
	ApproverCoordinator = "coordinator"
)

// Wording is the one phrase every message built from a statement carries.
const Wording = "agent attestation, not verified human identity"

var (
	refPattern      = regexp.MustCompile(`^[A-Za-z0-9_.:/#-]{1,128}$`)
	coordinatorExpr = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
)

// Attestor is who made the claim. The API derives it from the caller's token,
// never from the request body.
type Attestor struct {
	Session    string `json:"session"`
	SessionUID string `json:"sessionUID"`
	PodUID     string `json:"podUID"`
}

// Approver is who the attestor claims approved. Kind is user or coordinator; Ref
// names the coordinator and is checked for syntax only. It is an unverified
// claim.
type Approver struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref,omitempty"`
}

// String is the claim as the command line writes it.
func (a Approver) String() string {
	if a.Kind == ApproverCoordinator {
		return ApproverCoordinator + ":" + a.Ref
	}
	return a.Kind
}

// Statement is the recorded claim.
type Statement struct {
	V           int       `json:"v"`
	Attestor    Attestor  `json:"attestor"`
	Approver    Approver  `json:"approver"`
	Ref         string    `json:"ref"`
	GrantUID    string    `json:"grantUID"`
	ScopeDigest string    `json:"scopeDigest"`
	TTL         string    `json:"ttl"`
	AttestedAt  time.Time `json:"attestedAt"`
}

// ParseApprover reads "user" or "coordinator:<ref>".
func ParseApprover(s string) (Approver, error) {
	if s == ApproverUser {
		return Approver{Kind: ApproverUser}, nil
	}
	ref, ok := strings.CutPrefix(s, ApproverCoordinator+":")
	if !ok || !coordinatorExpr.MatchString(ref) {
		return Approver{}, errors.New("approvedBy is user, or coordinator:<ref> with a reference of letters, digits, '.', '_' or '-'")
	}
	return Approver{Kind: ApproverCoordinator, Ref: ref}, nil
}

// ValidRef reports whether ref is a bounded, nonsecret-shaped reference. It
// cannot know that a value is nonsecret; the shape only keeps out spaces, URLs
// with queries and long blobs.
func ValidRef(ref string) bool { return refPattern.MatchString(ref) }

// Encode returns the annotation value.
func (s Statement) Encode() (string, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	if len(b) > MaxBytes {
		return "", errors.New("the attestation is too large")
	}
	return string(b), nil
}

// Parse reads and bounds an annotation value. It accepts only the current
// version and refuses unknown fields.
func Parse(raw string) (Statement, error) {
	var s Statement
	if raw == "" || len(raw) > MaxBytes {
		return s, errors.New("no attestation, or it is too large")
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return Statement{}, fmt.Errorf("unreadable attestation: %w", err)
	}
	if dec.More() {
		return Statement{}, errors.New("unreadable attestation: trailing data")
	}
	switch {
	case s.V != Version:
		return Statement{}, errors.New("unknown attestation version")
	case s.Attestor.Session == "" || s.Attestor.SessionUID == "" || s.Attestor.PodUID == "" || s.GrantUID == "" || s.ScopeDigest == "" || s.TTL == "" || s.AttestedAt.IsZero():
		return Statement{}, errors.New("incomplete attestation")
	case !ValidRef(s.Ref):
		return Statement{}, errors.New("attestation reference is malformed")
	}
	if _, err := ParseApprover(s.Approver.String()); err != nil || (s.Approver.Kind == ApproverUser && s.Approver.Ref != "") {
		return Statement{}, errors.New("attestation approver is malformed")
	}
	return s, nil
}

// SameClaim reports whether two statements make the same claim about the same
// grant, ignoring when they were made: an identical retry.
func SameClaim(a, b Statement) bool {
	a.AttestedAt, b.AttestedAt = time.Time{}, time.Time{}
	return a == b
}

// ScopeDigest is the digest of what the grant asks for: its type, its credential
// or role and namespaces or egress destinations and ports, or its whole
// maintenance envelope (ADR-013), its TTL, and the requesting session. Lists are
// sorted and defaults made explicit, so equal requests give equal digests and
// any change in scope or TTL changes it. A grant without a maintenance envelope
// digests exactly as before the envelope was added. It is 43 base64url
// characters.
func ScopeDigest(g *v1alpha1.AccessGrant) string {
	type endpoint struct {
		Namespace string   `json:"namespace"`
		Labels    []string `json:"labels,omitempty"`
	}
	type port struct {
		Port     int32  `json:"port"`
		Protocol string `json:"protocol"`
	}
	doc := struct {
		Reserved       bool                          `json:"reserved,omitempty"`
		AllNamespaces  bool                          `json:"allNamespaces,omitempty"`
		StorageTargets []v1alpha1.GrantStorageTarget `json:"storageTargets,omitempty"`
		Type           string                        `json:"type"`
		Credential     string                        `json:"credential,omitempty"`
		Role           string                        `json:"role,omitempty"`
		Namespaces     []string                      `json:"namespaces,omitempty"`
		FQDNs          []string                      `json:"fqdns,omitempty"`
		CIDRs          []string                      `json:"cidrs,omitempty"`
		Endpoints      []endpoint                    `json:"endpoints,omitempty"`
		Ports          []port                        `json:"ports,omitempty"`
		// Maintenance is omitted when absent, so other grants keep their digest.
		Maintenance *v1alpha1.MaintenanceTask `json:"maintenance,omitempty"`
		TTL         string                    `json:"ttl"`
		Session     string                    `json:"session"`
		SessionUID  string                    `json:"sessionUID"`
	}{Type: string(g.Spec.Type), TTL: g.Spec.TTL.Duration.String(), Session: g.Spec.Requester.Session, SessionUID: string(g.Spec.Requester.SessionUID)}
	if c := g.Spec.Credential; c != nil {
		doc.Credential = string(c.Name)
	}
	if m := g.Spec.Maintenance; m != nil {
		doc.Maintenance = canonicalMaintenance(m)
	}
	if k := g.Spec.Kube; k != nil {
		doc.Reserved = k.Reserved
		doc.AllNamespaces = k.AllNamespaces
		doc.StorageTargets = slices.Clone(k.StorageTargets)
		slices.SortFunc(doc.StorageTargets, func(a, b v1alpha1.GrantStorageTarget) int {
			if a.Kind != b.Kind {
				return strings.Compare(a.Kind, b.Kind)
			}
			if a.Name != b.Name {
				return strings.Compare(a.Name, b.Name)
			}
			return strings.Compare(a.UID, b.UID)
		})
		doc.Role = k.Role
		doc.Namespaces = sorted(k.Namespaces)
	}
	if e := g.Spec.Egress; e != nil {
		doc.FQDNs, doc.CIDRs = sorted(e.FQDNs), sorted(e.CIDRs)
		for _, ep := range e.Endpoints {
			labels := make([]string, 0, len(ep.MatchLabels))
			for k, v := range ep.MatchLabels {
				labels = append(labels, k+"="+string(v))
			}
			sort.Strings(labels)
			doc.Endpoints = append(doc.Endpoints, endpoint{Namespace: ep.Namespace, Labels: labels})
		}
		sort.Slice(doc.Endpoints, func(i, j int) bool {
			a, b := doc.Endpoints[i], doc.Endpoints[j]
			if a.Namespace != b.Namespace {
				return a.Namespace < b.Namespace
			}
			return strings.Join(a.Labels, ",") < strings.Join(b.Labels, ",")
		})
		for _, p := range e.Ports {
			proto := string(p.Protocol)
			if proto == "" {
				proto = string(v1alpha1.ProtocolTCP)
			}
			doc.Ports = append(doc.Ports, port{Port: p.Port, Protocol: proto})
		}
		sort.Slice(doc.Ports, func(i, j int) bool {
			if doc.Ports[i].Port != doc.Ports[j].Port {
				return doc.Ports[i].Port < doc.Ports[j].Port
			}
			return doc.Ports[i].Protocol < doc.Ports[j].Protocol
		})
	}
	b, _ := json.Marshal(doc) // plain strings and numbers: Marshal cannot fail
	sum := sha256.Sum256(b)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// canonicalMaintenance is a copy of the envelope with its sets sorted, so the
// order in which a request named them does not change the digest.
func canonicalMaintenance(m *v1alpha1.MaintenanceTask) *v1alpha1.MaintenanceTask {
	c := m.DeepCopy()
	c.Capabilities, c.Devices, c.ReadOnlyMounts = sorted(c.Capabilities), sorted(c.Devices), sorted(c.ReadOnlyMounts)
	sort.Slice(c.Workers, func(i, j int) bool {
		a, b := c.Workers[i], c.Workers[j]
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if a.UID != b.UID {
			return a.UID < b.UID
		}
		return a.PodUID < b.PodUID
	})
	return c
}

func sorted(in []string) []string {
	out := slices.Clone(in)
	sort.Strings(out)
	return out
}

// Message is the audit text of an approval made through an attested policy. It
// never says a human approved.
func Message(policy string, s Statement) string {
	return fmt.Sprintf("approved by policy/%s on %s: session/%s claims %s approved (ref %s)", policy, Wording, s.Attestor.Session, s.Approver, s.Ref)
}
