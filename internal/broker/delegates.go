package broker

// Delegate decisions (DESIGN-042 6.2, mode (b)). An allowlisted delegate
// identity decides another agent's grant through the operator's authenticated
// route, POST /v1/grants/{name}/decision. The operator writes the set-once
// decision annotation (MaintenanceDecisionAnnotation, now on kube and egress
// grants too); the broker checks its binding, finds a GrantPolicy whose
// spec.delegated covers the scope and the requester, and calls Decide with
// "delegate/<namespace>/<serviceaccount>". It never writes a human approver. A
// delegate decision is not a human decision.
//
// Default off: with an empty --grant-delegates the operator serves no decision
// route and the broker reads no delegate decision, so behaviour is unchanged.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/grantattest"
)

const (
	// DelegateApprover prefixes status.approvedBy or status.deniedBy for a
	// delegate decision: "delegate/<namespace>/<serviceaccount>".
	DelegateApprover = "delegate/"
	// MaxGrantDelegates bounds --grant-delegates.
	MaxGrantDelegates = 16
	// DelegateWording is the phrase every message of a delegate decision
	// carries.
	DelegateWording = "not a human decision"
)

// ParseGrantDelegates parses --grant-delegates: comma-separated ServiceAccounts,
// "<namespace>/<name>", at most MaxGrantDelegates, none repeated and none of
// refused. The operator and the broker take the same list (as they take the
// same human ServiceAccount); the result is sorted, so the same entries in any
// order parse alike. Empty is nil: no delegates.
func ParseGrantDelegates(raw string, refused ...string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var out []string
	for _, ref := range strings.Split(raw, ",") {
		ref = strings.TrimSpace(ref)
		ns, name, ok := strings.Cut(ref, "/")
		switch {
		case !ok || len(validation.IsDNS1123Label(ns)) != 0 || len(validation.IsDNS1123Subdomain(name)) != 0:
			return nil, fmt.Errorf("--grant-delegates: %q is not <namespace>/<serviceaccount>", ref)
		case slices.Contains(refused, ref):
			return nil, fmt.Errorf("--grant-delegates: %s may not be a delegate: it is the human, a client, the shared session or the workbench identity", ref)
		case slices.Contains(out, ref):
			return nil, fmt.Errorf("--grant-delegates: %s is named twice", ref)
		}
		out = append(out, ref)
	}
	if len(out) > MaxGrantDelegates {
		return nil, errors.New("--grant-delegates: more than 16 delegates")
	}
	slices.Sort(out)
	return out, nil
}

// DelegateRef is the approver string of a delegate ServiceAccount.
func DelegateRef(serviceAccount string) string { return DelegateApprover + serviceAccount }

// delegateListed reports whether by is "delegate/<ref>" for a ref on the list.
func delegateListed(by string, delegates []string) bool {
	ref, ok := strings.CutPrefix(by, DelegateApprover)
	return ok && ref != "" && slices.Contains(delegates, ref)
}

// delegateRecheck is how often an Active grant a delegate approved is checked
// again: a delegate taken off the list, or a delegated policy removed, ends it
// within this time even when nothing else wakes the grant.
const delegateRecheck = time.Minute

var errDelegateDecisionFuture = errors.New("the decision timestamp is ahead of the broker clock")

// delegateMessage is the audit text of a delegate decision. It never says a
// human decided.
func delegateMessage(d Decision) string {
	if d.Reserved != nil {
		verb := "denied"
		if d.Approve {
			verb = "approved"
		}
		return fmt.Sprintf("%s by %s under reserved/%s bounds/%s (ref %s, tomRef %s): %s", verb, d.By, d.Reserved.Case, d.Reserved.BoundsRevision, d.Ref, d.Reserved.TomRef, grantattest.ReservedWording)
	}

	if d.Approve {
		return fmt.Sprintf("approved by %s under policy/%s (ref %s): %s", d.By, d.Policy, d.Ref, DelegateWording)
	}
	msg := fmt.Sprintf("denied by %s under policy/%s (ref %s): %s", d.By, d.Policy, d.Ref, DelegateWording)
	if d.Reason != "" {
		msg += ": " + d.Reason
	}
	return msg
}

// delegateDecisionAllowed is Decide's check of a delegate decision: the
// delegate is listed, the decision is bound to this grant's UID and scope
// digest and carries a ref, the grant is of a type and role a delegate may
// decide, and the decision, approval or denial, names the delegated policy
// that covers it: a delegate decides only inside the scopes Tom delegated.
func (b *Broker) delegateDecisionAllowed(g *v1alpha1.AccessGrant, d Decision) error {
	if d.Reserved != nil {
		return b.reservedDecisionAllowed(g, d)
	}
	switch {
	case !delegateListed(d.By, b.GrantDelegates):
		return fmt.Errorf("%w: %s is not a configured grant delegate", ErrRefused, d.By)
	case d.GrantUID == "" || d.GrantUID != string(g.UID) || d.ScopeDigest != grantattest.ScopeDigest(g):
		return fmt.Errorf("%w: the delegate decision is bound to another grant or scope", ErrRefused)
	case !grantattest.ValidRef(d.Ref):
		return fmt.Errorf("%w: a delegate decision carries a ref", ErrRefused)
	case !DelegableType(g.Spec.Type):
		return fmt.Errorf("%w: a delegate never decides a %s grant", ErrRefused, g.Spec.Type)
	case g.Spec.Kube != nil && DelegateRefusedRole(g.Spec.Kube.Role):
		return fmt.Errorf("%w: a delegate never decides role %s", ErrRefused, g.Spec.Kube.Role)
	case d.Policy == "":
		return fmt.Errorf("%w: a delegate decision names the delegated policy that covers it", ErrRefused)
	}
	return nil
}

// delegatedPolicyCovers reads the named policy from the API server and checks
// that its spec.delegated still covers g. A missing, deleting or narrowed
// policy refuses the decision; a failed read is retried.
func (b *Broker) delegatedPolicyCovers(ctx context.Context, g *v1alpha1.AccessGrant, name string) error {
	var p v1alpha1.GrantPolicy
	if err := b.APIReader.Get(ctx, types.NamespacedName{Namespace: b.PolicyNamespace, Name: name}, &p); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("%w: delegated policy %s is absent", ErrRefused, name)
		}
		return fmt.Errorf("read delegated policy %s: %w", name, err)
	}
	if !p.DeletionTimestamp.IsZero() || !MatchesDelegated(&p, g) {
		return fmt.Errorf("%w: delegated policy %s no longer covers the grant", ErrRefused, name)
	}
	return nil
}

// delegateBinding checks a recorded delegate decision against the grant: the
// delegate is listed, the decision is for this grant's UID and scope digest,
// carries a ref and the delegate's pod, was made inside the pending window
// and asks no more time than the grant did.
func delegateBinding(g *v1alpha1.AccessGrant, d MaintenanceDecision, delegates []string, now time.Time) error {
	switch {
	case !delegateListed(d.By, delegates):
		return errors.New("the deciding identity is not a configured grant delegate")
	case g.UID == "" || d.GrantUID != string(g.UID):
		return errors.New("the decision is for another grant")
	case d.Digest != grantattest.ScopeDigest(g):
		return errors.New("the grant's scope or TTL differs from what the delegate decided")
	case !grantattest.ValidRef(d.Ref):
		return errors.New("the decision carries no ref")
	case d.Delegate == nil || d.Delegate.PodUID == "":
		return errors.New("the decision does not name the delegate's pod")
	case d.At.Before(g.CreationTimestamp.Time) || !now.Before(d.At.Add(PendingTimeout)):
		return errors.New("the decision is outside the grant's pending window")
	case d.Approve && (d.TTL < MinTTL || d.TTL > g.Spec.TTL.Duration):
		return errors.New("the decision's TTL is outside 10m to the requested TTL")
	case d.At.After(now.Add(grantattest.MaxClockSkew)):
		return errDelegateDecisionFuture
	}
	return nil
}

// delegateSeparation checks again what the recorded identity lets the broker
// check: the delegate is not the requesting session, not its current pod, not
// a session running as the delegate's own account, and not a coordinator or
// worker the maintenance envelope names. The live ancestry and deciding
// identity are rechecked at apply time and while the grant is active.
func delegateSeparation(g *v1alpha1.AccessGrant, d *DelegateIdentity, by string, requesterPod *corev1.Pod) error {
	rq := g.Spec.Requester
	if d.Session != "" && (d.Session == rq.Session || types.UID(d.SessionUID) == rq.SessionUID) {
		return errors.New("the delegate is the requesting session")
	}
	if requesterPod != nil && string(requesterPod.UID) == d.PodUID {
		return errors.New("the delegate is the requesting session's pod")
	}
	if d.Form != 3 && requesterPod != nil && DelegateRef(requesterPod.Namespace+"/"+requesterPod.Spec.ServiceAccountName) == by {
		return errors.New("the requesting session runs as the delegate's own account")
	}
	if m := g.Spec.Maintenance; m != nil {
		for _, id := range append([]v1alpha1.MaintenanceIdentity{m.Coordinator}, m.Workers...) {
			if id.PodUID == d.PodUID || (d.Session != "" && (id.Name == d.Session || id.UID == d.SessionUID)) {
				return errors.New("the delegate is named in the maintenance envelope")
			}
		}
	}
	return nil
}

// maxReachDepth bounds the walk up the requester's session parents: sessions
// sit at most two levels deep.
const maxReachDepth = 4

// delegateReach checks the delegate's reach again (lead ruling on #786): a
// coordinator host (form 1) decides only for its own child sessions, whose
// parent is its ServiceAccount; a delegate session (form 2) only for its strict
// descendants, by name and UID, read from the API server. Anything else, an
// unknown form included, is outside its tree.
func (b *Broker) delegateReach(ctx context.Context, d MaintenanceDecision, sess *v1alpha1.AgentSession) error {
	outside := fmt.Errorf("%w: the requester is not in the delegate's own tree", ErrRefused)
	if sess == nil || d.Delegate == nil {
		return outside
	}
	switch d.Delegate.Form {
	case 1:
		if sess.Spec.Parent != strings.TrimPrefix(d.By, DelegateApprover) {
			return outside
		}
		return nil
	case 2:
		parent := sess.Spec.Parent
		for i := 0; parent != "" && !strings.Contains(parent, "/") && i < maxReachDepth; i++ {
			var a v1alpha1.AgentSession
			if err := b.APIReader.Get(ctx, types.NamespacedName{Namespace: b.SessionNamespace, Name: parent}, &a); err != nil {
				if apierrors.IsNotFound(err) {
					return fmt.Errorf("%w: the requester's ancestry is absent", ErrRefused)
				}
				return fmt.Errorf("read requester ancestry: %w", err)
			}
			if a.Name == d.Delegate.Session && string(a.UID) == d.Delegate.SessionUID {
				return nil
			}
			parent = a.Spec.Parent
		}
	}
	return outside
}

// delegateDecision applies a pending grant's recorded delegate decision
// (DESIGN-042 6.2). handled is false when no delegate is configured or the
// grant carries no delegate decision: the caller goes on as before. Otherwise
// the binding and the separation are checked, the decision, approval or
// denial, needs a policy whose spec.delegated covers the scope and the
// requester (for maintenance, #781's exact envelope; an approval also needs
// the human route's identity checks and one lease per target), and Decide
// records it as delegate/<namespace>/<name>. A permanently refused record is
// cleared with a live comparison and resourceVersion CAS so a new decision can
// use the shared slot. Human records and replacements are never cleared. Read
// failures and busy maintenance targets retain the record for a later retry.
func (b *Broker) delegateDecision(ctx context.Context, g *v1alpha1.AccessGrant, sess *v1alpha1.AgentSession, now metav1.Time) (res ctrl.Result, handled bool, err error) {
	d, ok := parseDecision(g)
	if !ok || !b.isDelegateDecision(d) {
		return ctrl.Result{}, false, nil
	}
	deadline := g.CreationTimestamp.Add(PendingTimeout)
	refuse := func(why string) (ctrl.Result, bool, error) {
		cleared, err := b.clearRefusedDelegateDecision(ctx, g)
		if err != nil || !cleared {
			return ctrl.Result{RequeueAfter: time.Second}, true, err
		}
		log.FromContext(ctx).Info("a refused delegate decision was cleared", append(grantFields(g), "by", d.By, "reason", why)...)
		b.event(g, corev1.EventTypeWarning, "Decide", "DelegateDecisionRefused", d.By+" decided, but: "+why)
		// Kube/egress still notify Tom in this pass. Maintenance waits for a
		// new authenticated decision; it never falls through with the old record.
		return ctrl.Result{RequeueAfter: time.Second}, g.Spec.Type == v1alpha1.GrantMaintenance, nil
	}
	if err := delegateBinding(g, d, b.GrantDelegates, now.Time); err != nil {
		if errors.Is(err, errDelegateDecisionFuture) {
			// Clock skew can resolve without a new decision. Keep the record,
			// but never wait beyond the grant's existing Pending deadline.
			return ctrl.Result{RequeueAfter: min(d.At.Add(-grantattest.MaxClockSkew).Sub(now.Time), deadline.Sub(now.Time))}, true, nil
		}
		return refuse(err.Error())
	}
	if err := delegateSeparation(g, d.Delegate, d.By, b.sessionPod(ctx, sess, false)); err != nil {
		return refuse(err.Error())
	}
	if err := b.delegateGrantHolds(ctx, g, d); err != nil {
		if !errors.Is(err, ErrRefused) {
			return ctrl.Result{}, true, err
		}
		return refuse(err.Error())
	}
	dec := Decision{Approve: d.Approve, By: d.By, TTL: d.TTL, Reason: d.Reason, GrantUID: d.GrantUID, ScopeDigest: d.Digest, Ref: d.Ref, Reserved: d.Reserved}
	if g.Spec.Type == v1alpha1.GrantMaintenance {
		dec.MaintenanceUID, dec.MaintenanceDigest = string(g.UID), MaintenanceDigest(g)
	}
	// A delegate decides, either way, only inside a scope Tom delegated: a
	// denial outside every delegated policy would let one delegate end any
	// agent's request. Select live: a policy cache miss must never clear a
	// decision that the API server's policy still covers.
	if d.Reserved != nil {
		if _, err := b.checkReservedBinding(g, d, now.Time); err != nil {
			return refuse(err.Error())
		}
	} else {
		var policies v1alpha1.GrantPolicyList
		if err := b.APIReader.List(ctx, &policies, client.InNamespace(b.PolicyNamespace)); err != nil {
			return ctrl.Result{}, true, fmt.Errorf("list the grant policies: %w", err)
		}
		p := MatchDelegated(g, policies.Items)
		if d.Reach != nil {
			p = nil
			for i := range policies.Items {
				candidate := &policies.Items[i]
				if candidate.Name == d.Reach.PolicyName && string(candidate.UID) == d.Reach.PolicyUID && candidate.DeletionTimestamp.IsZero() && MatchesDelegated(candidate, g) && DelegatedPolicyDigest(candidate) == d.Reach.PolicyDigest {
					p = candidate
					break
				}
			}
		}
		if p == nil {
			return refuse("no policy with spec.delegated covers this grant's scope and requester")
		}
		dec.Policy = p.Name
	}
	if d.Approve && g.Spec.Type == v1alpha1.GrantMaintenance {
		busy, err := b.maintenanceTargetBusy(ctx, g)
		if err != nil {
			return ctrl.Result{}, true, err
		}
		if busy {
			// Transient: logged quietly, checked again in 5 seconds.
			log.FromContext(ctx).V(1).Info("a delegate decision waits", append(grantFields(g), "by", d.By, "reason", "another maintenance lease holds this target")...)
			return ctrl.Result{RequeueAfter: min(5*time.Second, deadline.Sub(now.Time))}, true, nil
		}
	}
	switch err := b.Decide(ctx, g.Name, dec); {
	case errors.Is(err, ErrNotPending):
		return ctrl.Result{}, true, nil
	case errors.Is(err, ErrRefused):
		return refuse(err.Error())
	default:
		return ctrl.Result{}, true, err
	}
}

// clearRefusedDelegateDecision removes only the exact delegate record refused
// above, on the same still-Pending grant. The live read avoids stale cache state;
// the optimistic lock fences any status, scope or decision change after it.
// A conflict is left for the next reconcile, never retried against a replacement.
func (b *Broker) clearRefusedDelegateDecision(ctx context.Context, g *v1alpha1.AccessGrant) (bool, error) {
	d, ok := parseDecision(g)
	if !ok || !b.isDelegateDecision(d) {
		return false, nil
	}
	var fresh v1alpha1.AccessGrant
	if err := b.APIReader.Get(ctx, client.ObjectKeyFromObject(g), &fresh); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("read refused delegate decision: %w", err)
	}
	if fresh.UID != g.UID || fresh.Status.Phase != v1alpha1.GrantPending ||
		!fresh.DeletionTimestamp.IsZero() || fresh.Spec.Release ||
		fresh.Annotations[MaintenanceDecisionAnnotation] != g.Annotations[MaintenanceDecisionAnnotation] {
		return false, nil
	}
	orig := fresh.DeepCopy()
	delete(fresh.Annotations, MaintenanceDecisionAnnotation)
	if err := b.Client.Patch(ctx, &fresh, client.MergeFromWithOptions(orig, client.MergeFromWithOptimisticLock{})); err != nil {
		if apierrors.IsConflict(err) || apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("clear refused delegate decision: %w", err)
	}
	*g = fresh
	return true, nil
}

// delegatedHolds re-checks an Active grant that a delegate approved, on every
// pass of active and activeMaintenance: the delegate is still listed; the
// decision annotation still binds this grant, its scope and that delegate;
// and a policy with spec.delegated (read from the API server) still covers
// it. A grant approved otherwise is not affected. A refusal is permanent: the
// caller ends the grant. Any other error is a read to retry.
func (b *Broker) delegatedHolds(ctx context.Context, g *v1alpha1.AccessGrant) error {
	if !strings.HasPrefix(g.Status.ApprovedBy, DelegateApprover) {
		return nil
	}
	if !delegateListed(g.Status.ApprovedBy, b.GrantDelegates) {
		return permanent{errors.New("the approving delegate is no longer configured")}
	}
	d, ok := parseDecision(g)
	if !ok || !d.Approve || d.By != g.Status.ApprovedBy || d.GrantUID != string(g.UID) || d.Digest != grantattest.ScopeDigest(g) {
		return permanent{errors.New("the delegate decision no longer binds this grant")}
	}
	if g.Status.DelegateReach != nil && (d.Reach == nil || *g.Status.DelegateReach != *d.Reach) {
		return permanent{errors.New("the recorded reach binding changed")}
	}
	if err := b.delegateGrantHolds(ctx, g, d); err != nil {
		if errors.Is(err, ErrRefused) {
			return permanent{err}
		}
		return err
	}
	if d.Reserved != nil {
		return nil
	}
	var policies v1alpha1.GrantPolicyList
	if err := b.APIReader.List(ctx, &policies, client.InNamespace(b.PolicyNamespace)); err != nil {
		return fmt.Errorf("list the grant policies: %w", err)
	}
	if MatchDelegated(g, policies.Items) == nil {
		return permanent{errors.New("no policy with spec.delegated covers this grant any more")}
	}
	return nil
}
