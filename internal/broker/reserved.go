package broker

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/grantattest"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"slices"
	"time"
)

// ReservedClass classifies exact S7 scope. Legacy untargeted storage cannot
// acquire S7 approval or pass the proposed own-grant UID DELETE guard.
func ReservedClass(g *v1alpha1.AccessGrant) string {
	if g.Spec.Kube == nil {
		return ""
	}
	switch g.Spec.Kube.Role {
	case v1alpha1.RoleBreakglass:
		if g.Spec.Type == v1alpha1.GrantBreakglass {
			return grantattest.ClassRD2
		}
	case v1alpha1.RoleSecretsRead:
		if g.Spec.Type != v1alpha1.GrantKube {
			return ""
		}
		if g.Spec.Kube.AllNamespaces {
			return grantattest.ClassRD3b
		}
		return grantattest.ClassRD3a
	case v1alpha1.RoleStorage:
		if g.Spec.Type == v1alpha1.GrantKube && (g.Spec.Kube.Reserved || len(g.Spec.Kube.StorageTargets) > 0) {
			return grantattest.ClassRD4Storage
		}
	}
	return ""
}

func reservedInput(g *v1alpha1.AccessGrant, d MaintenanceDecision, exclusions []string, snapshot []v1alpha1.ReservedNamespace, now time.Time) grantattest.Input {
	k := g.Spec.Kube
	in := grantattest.Input{Classes: []string{ReservedClass(g)}, Exclusions: exclusions, Now: now.UTC().Truncate(time.Second),
		Grant:   grantattest.GrantView{Name: g.Name, UID: string(g.UID), Type: string(g.Spec.Type), TTL: g.Spec.TTL.Duration, RequesterSession: g.Spec.Requester.Session, RequesterUID: string(g.Spec.Requester.SessionUID), Phase: string(g.Status.Phase), Release: g.Spec.Release, Deleting: !g.DeletionTimestamp.IsZero(), ScopeDigest: grantattest.ScopeDigest(g), CreatedAt: g.CreationTimestamp.Time.UTC()},
		Request: grantattest.Request{Reserved: true, Approve: d.Approve, Class: ReservedClass(g), BoundsRevision: grantattest.BoundsRevision, GrantUID: d.GrantUID, RequesterUID: string(g.Spec.Requester.SessionUID), Digest: d.Digest, At: d.At.UTC(), TTL: d.TTL}}
	if k != nil {
		in.Grant.Role = k.Role
		in.Grant.Namespaces = k.Namespaces
		in.Request.Namespaces = k.Namespaces
		in.Request.AllNamespaces = k.AllNamespaces
		for _, t := range k.StorageTargets {
			ns := ""
			if len(k.Namespaces) == 1 {
				ns = k.Namespaces[0]
			}
			in.Request.Targets = append(in.Request.Targets, grantattest.StorageTarget{Namespace: ns, Kind: t.Kind, Name: t.Name, UID: t.UID})
		}
	}
	if d.Delegate != nil {
		in.Actor = grantattest.Actor{UID: d.Delegate.PodUID, Session: d.Delegate.Session, SessionUID: d.Delegate.SessionUID}
		in.Request.ActorUID = d.Delegate.PodUID
		in.Request.ActorSession = d.Delegate.Session
	}
	if d.Reserved != nil {
		in.Request.TomRef = d.Reserved.TomRef
	}
	for _, ns := range snapshot {
		in.Request.Snapshot = append(in.Request.Snapshot, grantattest.NamespaceBinding{Name: ns.Name, UID: ns.UID})
	}
	return in
}

// ValidateReservedRequestShape checks scope only, never actor or approval.
func ValidateReservedRequestShape(g *v1alpha1.AccessGrant, classes, exclusions []string) error {
	if g.Spec.Kube == nil || g.Spec.Egress != nil || g.Spec.Credential != nil || g.Spec.Maintenance != nil || len(g.Spec.Kube.Secrets) > 0 {
		return fmt.Errorf("%w: reserved grant contains a different scope type", ErrRefused)
	}
	class := ReservedClass(g)
	if class == "" || !slices.Contains(classes, class) {
		return fmt.Errorf("%w: reserved class %q is disabled or unlisted", ErrRefused, class)
	}
	return grantattest.ValidateReservedShape(reservedInput(g, MaintenanceDecision{}, exclusions, nil, time.Time{}))
}

// ReadReservedNamespaces reads a finite complete metadata inventory. A partial
// list or failed lookup refuses; no truncation or cluster-binding fallback.
func ReadReservedNamespaces(ctx context.Context, r client.Reader) ([]v1alpha1.ReservedNamespace, error) {
	var all corev1.NamespaceList
	if err := r.List(ctx, &all, client.Limit(257)); err != nil {
		return nil, fmt.Errorf("read namespace UID inventory: %w", err)
	}
	if all.Continue != "" || len(all.Items) > 256 {
		return nil, fmt.Errorf("%w: namespace UID inventory exceeds finite bound", ErrRefused)
	}
	out := make([]v1alpha1.ReservedNamespace, 0, len(all.Items))
	for _, ns := range all.Items {
		if ns.UID == "" || !ns.DeletionTimestamp.IsZero() {
			return nil, fmt.Errorf("%w: namespace identity absent or terminating", ErrRefused)
		}
		out = append(out, v1alpha1.ReservedNamespace{Name: ns.Name, UID: string(ns.UID)})
	}
	slices.SortFunc(out, func(a, b v1alpha1.ReservedNamespace) int {
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	})
	return out, nil
}

func reservedDigest(binding *v1alpha1.ReservedDecisionBinding) string {
	copy := binding.DeepCopy()
	copy.EffectiveDigest = ""
	raw, _ := json.Marshal(copy)
	sum := sha256.Sum256(raw)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// ReservedAuthority adapts the same SHA256 to the shared reach seam's hex shape.
// Call only after exact enabled-case validation; this function authorizes nothing.
func ReservedAuthority(binding *v1alpha1.ReservedDecisionBinding) ReservedReachAuthority {
	raw, err := base64.RawURLEncoding.DecodeString(binding.EffectiveDigest)
	if err != nil || len(raw) != sha256.Size {
		return ReservedReachAuthority{}
	}
	return ReservedReachAuthority{Case: binding.Case, Revision: binding.BoundsRevision, Digest: hex.EncodeToString(raw)}
}

// NewReservedBinding uses the released pure core. The API must also call the
// shared live actor/reach checker before its set-once CAS.
func NewReservedBinding(ctx context.Context, r client.Reader, ns string, g *v1alpha1.AccessGrant, d MaintenanceDecision, classes, exclusions []string, tomRef string) (*v1alpha1.ReservedDecisionBinding, error) {
	if err := ValidateReservedRequestShape(g, classes, exclusions); err != nil {
		return nil, err
	}
	excluded, err := grantattest.NormalizeReservedExclusions(exclusions)
	if err != nil {
		return nil, err
	}
	if ReservedClass(g) == grantattest.ClassRD2 {
		excluded = []string{}
	}
	var snapshot []v1alpha1.ReservedNamespace
	if ReservedClass(g) == grantattest.ClassRD3b {
		all, err := ReadReservedNamespaces(ctx, r)
		if err != nil {
			return nil, err
		}
		for _, entry := range all {
			if !slices.Contains(excluded, entry.Name) {
				snapshot = append(snapshot, entry)
			}
		}
	}
	var requester corev1.Pod
	if err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: g.Spec.Requester.Session}, &requester); err != nil {
		return nil, fmt.Errorf("read reserved requester pod: %w", err)
	}
	if d.Delegate == nil || requester.UID == "" {
		return nil, fmt.Errorf("%w: missing reserved actor/requester binding", ErrRefused)
	}
	binding := &v1alpha1.ReservedDecisionBinding{Case: ReservedClass(g), BoundsRevision: "1", GrantUID: string(g.UID), RequesterUID: string(g.Spec.Requester.SessionUID), RequesterPodUID: string(requester.UID), ActorUID: d.Delegate.PodUID, ActorSessionUID: d.Delegate.SessionUID, ScopeDigest: grantattest.ScopeDigest(g), TomRef: tomRef, Ref: d.Ref, TTL: metav1.Duration{Duration: d.TTL}, DecidedAt: metav1.NewTime(d.At), Namespaces: snapshot, ExcludedNamespaces: excluded}
	d.Reserved = binding
	if _, err := grantattest.EvaluateReserved(reservedInput(g, d, excluded, snapshot, d.At)); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRefused, err)
	}
	binding.EffectiveDigest = reservedDigest(binding)
	return binding, nil
}

// Active checks reconstruct the original pure binding at its recorded time.
// Real phase/expiry/release and shared live actor/reach are checked separately.
func (b *Broker) checkReservedBinding(g *v1alpha1.AccessGrant, d MaintenanceDecision, now time.Time) (grantattest.Record, error) {
	r := d.Reserved
	if r == nil || d.Delegate == nil || !delegateListed(d.By, b.GrantDelegates) || r.Case != ReservedClass(g) || !slices.Contains(b.ReservedDecisionClasses, r.Case) || r.BoundsRevision != "1" ||
		r.GrantUID != string(g.UID) || r.RequesterUID != string(g.Spec.Requester.SessionUID) || r.RequesterPodUID == "" || r.ActorUID != d.Delegate.PodUID || r.ActorSessionUID != d.Delegate.SessionUID ||
		r.ScopeDigest != grantattest.ScopeDigest(g) || r.ScopeDigest != d.Digest || r.Ref != d.Ref || !grantattest.ValidRef(r.Ref) || r.TTL.Duration != d.TTL || !r.DecidedAt.Time.Equal(d.At) || r.EffectiveDigest != reservedDigest(r) {
		return grantattest.Record{}, fmt.Errorf("%w: reserved decision binding or enabled case differs", ErrRefused)
	}
	excluded, err := grantattest.NormalizeReservedExclusions(b.ReservedExcludedNamespaces)
	if r.Case == grantattest.ClassRD2 {
		excluded = []string{}
	}
	if err != nil || !slices.Equal(excluded, r.ExcludedNamespaces) {
		return grantattest.Record{}, fmt.Errorf("%w: reserved namespace bounds changed", ErrRefused)
	}
	in := reservedInput(g, d, excluded, r.Namespaces, now)
	if g.Status.Phase == v1alpha1.GrantActive {
		if !d.Approve || g.Status.ApprovedTTL == nil || g.Status.ApprovedTTL.Duration != d.TTL || g.Status.ExpiresAt == nil || !now.Before(g.Status.ExpiresAt.Time) || g.Status.Reserved == nil || g.Status.Reserved.EffectiveDigest != r.EffectiveDigest || reservedDigest(g.Status.Reserved) != r.EffectiveDigest {
			return grantattest.Record{}, fmt.Errorf("%w: Active reserved lease differs or ended", ErrRefused)
		}
		in.Grant.Phase = string(v1alpha1.GrantPending)
		in.Now = d.At
	}
	record, err := grantattest.EvaluateReserved(in)
	if err != nil {
		return record, fmt.Errorf("%w: %w", ErrRefused, err)
	}
	return record, nil
}

func (b *Broker) reservedDecisionAllowed(g *v1alpha1.AccessGrant, d Decision) error {
	recorded, ok := parseDecision(g)
	if !ok || recorded.Reserved == nil || d.Reserved == nil || d.Reserved.EffectiveDigest != recorded.Reserved.EffectiveDigest || d.Policy != "" {
		return fmt.Errorf("%w: missing exact reserved annotation", ErrRefused)
	}
	_, err := b.checkReservedBinding(g, recorded, b.Clock.Now())
	return err
}

func (b *Broker) reservedNamespaceHolds(ctx context.Context, g *v1alpha1.AccessGrant, d MaintenanceDecision) error {
	record, err := b.checkReservedBinding(g, d, b.Clock.Now())
	if err != nil {
		return err
	}
	if record.Class != grantattest.ClassRD3b {
		return nil
	}
	live, err := ReadReservedNamespaces(ctx, b.APIReader)
	if err != nil {
		return err
	}
	var bindings []grantattest.NamespaceBinding
	for _, ns := range live {
		bindings = append(bindings, grantattest.NamespaceBinding{Name: ns.Name, UID: ns.UID})
	}
	if _, err := grantattest.RecheckSnapshot(record, bindings); err != nil {
		return fmt.Errorf("%w: %w", ErrRefused, err)
	}
	return nil
}

func (b *Broker) checkReservedOrLegacyCapabilities(g *v1alpha1.AccessGrant) error {
	if g.Spec.Kube != nil && (g.Spec.Kube.Reserved || g.Spec.Kube.AllNamespaces || len(g.Spec.Kube.StorageTargets) > 0) {
		classes := b.ReservedDecisionClasses
		if g.Status.Phase == v1alpha1.GrantActive {
			classes = []string{ReservedClass(g)}
		}
		return ValidateReservedRequestShape(g, classes, b.ReservedExcludedNamespaces)
	}
	return checkCapabilities(g, b.EnableProxmoxGrants || g.Status.Phase == v1alpha1.GrantActive, b.EnableHWSSHGrants || g.Status.Phase == v1alpha1.GrantActive, b.SessionNamespace, b.PolicyNamespace)
}

// Cleanup derives names from the retained frozen snapshot even when flags or
// current namespace inventory have changed. It never expands a binding scope.
func bindingNamespaces(g *v1alpha1.AccessGrant) []string {
	if g.Spec.Kube == nil {
		return nil
	}
	if g.Spec.Kube.AllNamespaces {
		if g.Status.Reserved == nil {
			return nil
		}
		out := make([]string, 0, len(g.Status.Reserved.Namespaces))
		for _, ns := range g.Status.Reserved.Namespaces {
			out = append(out, ns.Name)
		}
		return out
	}
	return g.Spec.Kube.Namespaces
}
func snapshotNamespaceUID(g *v1alpha1.AccessGrant, ns string) string {
	if g.Status.Reserved == nil || g.Status.Reserved.Case != grantattest.ClassRD3b {
		return ""
	}
	for _, entry := range g.Status.Reserved.Namespaces {
		if entry.Name == ns {
			return entry.UID
		}
	}
	return ""
}
func bindingLabels(g *v1alpha1.AccessGrant, ns string, base map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range base {
		out[k] = v
	}
	if uid := snapshotNamespaceUID(g, ns); uid != "" {
		out[v1alpha1.LabelPrefix+"grant-namespace-uid"] = uid
	}
	return out
}
