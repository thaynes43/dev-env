package broker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// ParseGrantPolicyDelegates uses the bounded delegate parser and requires a subset.
func ParseGrantPolicyDelegates(raw string, delegates []string) ([]string, error) {
	out, err := ParseGrantDelegates(raw)
	if err != nil {
		return nil, fmt.Errorf("--grant-policy-delegates: %w", err)
	}
	for _, ref := range out {
		if !slices.Contains(delegates, ref) {
			return nil, fmt.Errorf("--grant-policy-delegates: %s is absent from --grant-delegates", ref)
		}
	}
	return out, nil
}

func reachDigest(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// DelegatedPolicyDigest binds the effective delegated scope, excluding metadata
// and the independent attested approval mode. Conservative ordering changes revoke.
func DelegatedPolicyDigest(p *v1alpha1.GrantPolicy) string {
	spec := p.Spec.DeepCopy()
	spec.Description = ""
	spec.Attested = nil
	return reachDigest(spec)
}

func reachRead(ctx context.Context, r client.Reader, key types.NamespacedName, obj client.Object) error {
	if err := r.Get(ctx, key, obj); err != nil {
		if apierrors.IsNotFound(err) || apierrors.IsForbidden(err) {
			return fmt.Errorf("%w: reach identity or policy unavailable: %w", ErrRefused, err)
		}
		return fmt.Errorf("read reach identity or policy: %w", err)
	}
	return nil
}

// ancestry reads a complete bounded chain; no missing, repeated or UID-less
// ancestor may become authority. Names/UIDs/parents are hashed into the record.
func ancestry(ctx context.Context, r client.Reader, ns string, s *v1alpha1.AgentSession) ([]v1alpha1.AgentSession, error) {
	depth, err := strconv.Atoi(s.Labels[v1alpha1.LabelDepth])
	if err != nil || depth < 0 || depth > 2 {
		return nil, fmt.Errorf("%w: ambiguous ancestry depth", ErrRefused)
	}
	chain := []v1alpha1.AgentSession{*s}
	seen := map[string]bool{s.Name: true}
	for parent := s.Spec.Parent; parent != "" && !strings.Contains(parent, "/"); {
		if len(chain) >= maxReachDepth || seen[parent] {
			return nil, fmt.Errorf("%w: ambiguous or excessive session ancestry", ErrRefused)
		}
		var a v1alpha1.AgentSession
		if err := reachRead(ctx, r, types.NamespacedName{Namespace: ns, Name: parent}, &a); err != nil {
			return nil, err
		}
		if a.UID == "" || !a.DeletionTimestamp.IsZero() {
			return nil, fmt.Errorf("%w: unbound or deleting ancestor", ErrRefused)
		}
		seen[parent] = true
		nextDepth, e := strconv.Atoi(a.Labels[v1alpha1.LabelDepth])
		if e != nil || nextDepth != depth-1 {
			return nil, fmt.Errorf("%w: inconsistent ancestry depth", ErrRefused)
		}
		depth = nextDepth
		chain = append(chain, a)
		parent = a.Spec.Parent
	}
	last := chain[len(chain)-1]
	if roots, e := ParseGrantDelegates(last.Spec.Parent); e != nil || len(roots) != 1 || depth != 0 {
		return nil, fmt.Errorf("%w: ancestry has no unambiguous creator", ErrRefused)
	}
	return chain, nil
}
func ancestryDigest(chain []v1alpha1.AgentSession) string {
	type link struct {
		Name   string
		UID    types.UID
		Parent string
		Depth  string
	}
	links := make([]link, len(chain))
	for i := range chain {
		links[i] = link{chain[i].Name, chain[i].UID, chain[i].Spec.Parent, chain[i].Labels[v1alpha1.LabelDepth]}
	}
	return reachDigest(links)
}

// NewDelegateReachIdentity binds live requester and actor ancestry and checks
// separation. It grants NO scope authority. Only an independently validated
// covering policy or enabled exact reserved case may authorize its result. Actor
// eligibility must also be checked by the authenticated caller.
func NewDelegateReachIdentity(ctx context.Context, r client.Reader, sessionNamespace string, g *v1alpha1.AccessGrant, actor *DelegateIdentity, by string, policyDelegates []string) (*v1alpha1.DelegateReachBinding, error) {
	if r == nil || actor == nil {
		return nil, fmt.Errorf("%w: reach requires live identities and policy namespace", ErrRefused)
	}
	var requester v1alpha1.AgentSession
	if err := reachRead(ctx, r, types.NamespacedName{Namespace: sessionNamespace, Name: g.Spec.Requester.Session}, &requester); err != nil {
		return nil, err
	}
	if requester.UID == "" || requester.UID != g.Spec.Requester.SessionUID || !requester.DeletionTimestamp.IsZero() || requester.Status.Phase != v1alpha1.PhaseRunning || requester.Status.ArchivedAt != nil || (requester.Spec.OperatingMode != "" && requester.Spec.OperatingMode != v1alpha1.OperatingModeRunning) {
		return nil, fmt.Errorf("%w: requester replaced or inactive", ErrRefused)
	}
	var pod corev1.Pod
	if err := reachRead(ctx, r, types.NamespacedName{Namespace: sessionNamespace, Name: requester.Name}, &pod); err != nil {
		return nil, err
	}
	owner := metav1.GetControllerOf(&pod)
	if pod.UID == "" || owner == nil || owner.APIVersion != v1alpha1.GroupVersion.String() || owner.Kind != "AgentSession" || owner.Name != requester.Name || owner.UID != requester.UID || !pod.DeletionTimestamp.IsZero() || pod.Status.Phase != corev1.PodRunning {
		return nil, fmt.Errorf("%w: requester pod replaced or inactive", ErrRefused)
	}
	if err := delegateSeparation(g, actor, by, &pod); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRefused, err)
	}
	rqChain, err := ancestry(ctx, r, sessionNamespace, &requester)
	if err != nil {
		return nil, err
	}
	var actorChain []v1alpha1.AgentSession
	if actor.Session != "" {
		var sess v1alpha1.AgentSession
		ns, _, _ := strings.Cut(strings.TrimPrefix(by, DelegateApprover), "/")
		if err := reachRead(ctx, r, types.NamespacedName{Namespace: ns, Name: actor.Session}, &sess); err != nil {
			return nil, err
		}
		if string(sess.UID) != actor.SessionUID || sess.UID == "" {
			return nil, fmt.Errorf("%w: actor session replaced", ErrRefused)
		}
		actorChain, err = ancestry(ctx, r, ns, &sess)
		if err != nil {
			return nil, err
		}
		for _, a := range actorChain {
			if a.Name == requester.Name || a.UID == requester.UID {
				return nil, fmt.Errorf("%w: actor is requester or its descendant", ErrRefused)
			}
		}
	}
	tree := actor.Form == 1 && requester.Spec.Parent == strings.TrimPrefix(by, DelegateApprover)
	if actor.Form == 2 || actor.Form == 3 {
		for _, a := range rqChain[1:] {
			if a.Name == actor.Session && string(a.UID) == actor.SessionUID {
				tree = true
			}
		}
	}
	mode := "tree"
	if !tree {
		if !delegateListed(by, policyDelegates) {
			return nil, fmt.Errorf("%w: requester outside delegate tree", ErrRefused)
		}
		mode = "policy"
	}
	return &v1alpha1.DelegateReachBinding{Mode: mode, Designated: delegateListed(by, policyDelegates), RequesterUID: string(requester.UID), RequesterPodUID: string(pod.UID), RequesterAncestryDigest: ancestryDigest(rqChain), ActorAncestryDigest: ancestryDigest(actorChain)}, nil
}

// NewDelegateReach binds ordinary policy authority after checking live identities.
func NewDelegateReach(ctx context.Context, r client.Reader, sessionNamespace, policyNamespace string, g *v1alpha1.AccessGrant, actor *DelegateIdentity, by string, policyDelegates []string) (*v1alpha1.DelegateReachBinding, error) {
	binding, err := NewDelegateReachIdentity(ctx, r, sessionNamespace, g, actor, by, policyDelegates)
	if err != nil {
		return nil, err
	}
	if policyNamespace == "" {
		return nil, fmt.Errorf("%w: missing policy namespace", ErrRefused)
	}
	var policies v1alpha1.GrantPolicyList
	if err := r.List(ctx, &policies, client.InNamespace(policyNamespace)); err != nil {
		return nil, fmt.Errorf("list delegated reach policies: %w", err)
	}
	p := MatchDelegated(g, policies.Items)
	if p == nil || p.UID == "" {
		return nil, fmt.Errorf("%w: no UID-bound delegated policy covers requester and scope", ErrRefused)
	}
	binding.AuthorityKind = "delegatedPolicy"
	binding.PolicyName = p.Name
	binding.PolicyUID = string(p.UID)
	binding.PolicyDigest = DelegatedPolicyDigest(p)
	return binding, nil
}

// CheckDelegateReach repeats exactly the recorded identity, ancestry and policy
// binding. A transient read never authorizes access and is never a refusal clear.
func CheckDelegateReach(ctx context.Context, r client.Reader, sessionNamespace, policyNamespace string, g *v1alpha1.AccessGrant, d MaintenanceDecision, policyDelegates []string) error {
	if d.Reach == nil || d.Reach.AuthorityKind != "delegatedPolicy" || d.Reach.ReservedCase != "" || d.Reach.BoundsRevision != "" || d.Reach.BoundsDigest != "" {
		return fmt.Errorf("%w: missing or non-policy reach authority", ErrRefused)
	}
	// Re-read the named policy, not another policy which happens to cover now.
	var p v1alpha1.GrantPolicy
	if err := reachRead(ctx, r, types.NamespacedName{Namespace: policyNamespace, Name: d.Reach.PolicyName}, &p); err != nil {
		return err
	}
	if p.UID == "" || string(p.UID) != d.Reach.PolicyUID || !p.DeletionTimestamp.IsZero() || !MatchesDelegated(&p, g) || DelegatedPolicyDigest(&p) != d.Reach.PolicyDigest {
		return fmt.Errorf("%w: reviewed delegated policy replaced or changed", ErrRefused)
	}
	return CheckDelegateReachIdentity(ctx, r, sessionNamespace, g, d, policyDelegates)
}

// CheckDelegateReachIdentity checks identity, separation, bounded ancestry and
// designated membership only. It grants NO authority. S7 must first validate an
// explicitly enabled exact reserved case and bound revision/digest on every call;
// ordinary callers use CheckDelegateReach, never policy-failure fallback.
func CheckDelegateReachIdentity(ctx context.Context, r client.Reader, sessionNamespace string, g *v1alpha1.AccessGrant, d MaintenanceDecision, policyDelegates []string) error {
	if d.Reach == nil {
		return fmt.Errorf("%w: missing reach binding", ErrRefused)
	}
	got, err := NewDelegateReachIdentity(ctx, r, sessionNamespace, g, d.Delegate, d.By, policyDelegates)
	if err != nil {
		return err
	}
	if got.Designated != d.Reach.Designated || got.Mode != d.Reach.Mode || got.RequesterUID != d.Reach.RequesterUID || got.RequesterPodUID != d.Reach.RequesterPodUID || got.RequesterAncestryDigest != d.Reach.RequesterAncestryDigest || got.ActorAncestryDigest != d.Reach.ActorAncestryDigest {
		return fmt.Errorf("%w: reach or requester identity changed", ErrRefused)
	}
	return nil
}

// ReservedReachAuthority is a supplied already-validated S7 case. This seam does
// not enable cases or validate their bounds; S7 must do that on every call.
type ReservedReachAuthority struct{ Case, Revision, Digest string }

func (a ReservedReachAuthority) valid() bool {
	_, err := hex.DecodeString(a.Digest)
	return a.Case != "" && len(a.Case) <= 32 && a.Revision != "" && len(a.Revision) <= 128 && len(a.Digest) == 64 && err == nil
}

// NewReservedDelegateReach binds explicit reserved authority to the SAME live
// identity and separation checker. Never call as fallback from a failed policy.
func NewReservedDelegateReach(ctx context.Context, r client.Reader, ns string, g *v1alpha1.AccessGrant, actor *DelegateIdentity, by string, designated []string, a ReservedReachAuthority) (*v1alpha1.DelegateReachBinding, error) {
	if !a.valid() {
		return nil, fmt.Errorf("%w: missing explicit reserved authority", ErrRefused)
	}
	binding, err := NewDelegateReachIdentity(ctx, r, ns, g, actor, by, designated)
	if err != nil {
		return nil, err
	}
	binding.AuthorityKind = "reserved"
	binding.ReservedCase, binding.BoundsRevision, binding.BoundsDigest = a.Case, a.Revision, a.Digest
	return binding, nil
}

// CheckReservedDelegateReach requires exact S7 authority supplied after enabled
// case validation, then repeats the same identity/separation/ancestry checks.
func CheckReservedDelegateReach(ctx context.Context, r client.Reader, ns string, g *v1alpha1.AccessGrant, d MaintenanceDecision, designated []string, a ReservedReachAuthority) error {
	if !a.valid() || d.Reach == nil || d.Reach.AuthorityKind != "reserved" || d.Reach.PolicyName != "" || d.Reach.PolicyUID != "" || d.Reach.PolicyDigest != "" || d.Reach.ReservedCase != a.Case || d.Reach.BoundsRevision != a.Revision || d.Reach.BoundsDigest != a.Digest {
		return fmt.Errorf("%w: reserved authority changed or absent", ErrRefused)
	}
	return CheckDelegateReachIdentity(ctx, r, ns, g, d, designated)
}
