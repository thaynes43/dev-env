package broker

// Attested maintenance approval (ADR-013, Tom 2026-10-31: "I or an agent can
// approve with a message to the agent requesting the grant"). This is path (a) of
// ADR-013: the requesting session's own agent attestation (D-88), through an
// attested GrantPolicy whose maintenance scope matches the envelope exactly
// (maintenanceInside). The broker verifies the attestation's binding; the
// attestation itself is a claim, not verified human identity.
//
// Default off in code. pending() calls this only after the authenticated human
// route (maintenanceDecision) left the grant alone, and only with
// --enable-attested-grants and --enable-attested-maintenance; Reconcile has
// already refused every new maintenance grant without
// --enable-maintenance-grants. No standing policy approves maintenance (Matches).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/grantattest"
)

// attestedMaintenanceApproval approves pending maintenance grant g through an
// attested policy when one matches and g's attestation verifies in its approval
// form: the statement is for g's UID, from g's requesting session UID and that
// session's current Running pod, with g's scope digest, which covers the whole
// envelope. It also requires:
//
//   - a top-level requesting session (topLevelSession): a child task session
//     never approves SYS_ADMIN for itself;
//   - no extensionOf: an extension needs the human route (ADR-012);
//   - the identity, worker and node checks of the human route
//     (CheckMaintenanceIdentities);
//   - no other maintenance lease on the same node UID and resource
//     (maintenanceTargetBusy).
//
// approved reports that an approval was written, or attempted.
func (b *Broker) attestedMaintenanceApproval(ctx context.Context, g *v1alpha1.AccessGrant, sess *v1alpha1.AgentSession, now metav1.Time) (approved bool, err error) {
	if !b.EnableMaintenanceGrants || !b.EnableAttestedGrants || !b.EnableAttestedMaintenance || sess == nil || g.Spec.Type != v1alpha1.GrantMaintenance || g.Spec.Maintenance == nil {
		return false, nil
	}
	wait := func(why string) (bool, error) {
		log.FromContext(ctx).V(1).Info("attested maintenance: the grant waits for the human route", append(grantFields(g), "reason", why)...)
		return false, nil
	}
	if !topLevelSession(sess) {
		return wait("the requesting session is a child session")
	}
	if g.Spec.Maintenance.ExtensionOf != "" {
		return wait("an extension needs the human route")
	}
	var policies v1alpha1.GrantPolicyList
	if err := b.Client.List(ctx, &policies, client.InNamespace(b.PolicyNamespace)); err != nil {
		return false, fmt.Errorf("list the grant policies: %w", err)
	}
	p, st := b.attestedApproval(ctx, g, sess, policies.Items, now.Time)
	if p == nil || st == nil || p.Spec.Attested == nil {
		return false, nil
	}
	if !attestorIsCoordinator(*st, g.Spec.Maintenance) {
		return wait("the attestor is not the envelope's coordinator pod")
	}
	if err := CheckMaintenanceIdentities(ctx, b.APIReader, b.SessionNamespace, g); err != nil {
		return wait("identity or target check: " + err.Error())
	}
	// A policy bounds each lease, not how many run at once: this route approves
	// one lease per node UID and resource at a time.
	if busy, err := b.maintenanceTargetBusy(ctx, g); err != nil || busy {
		if busy {
			return wait("another maintenance lease holds this target")
		}
		return false, err
	}
	approve(g, PolicyApprover+p.Name, g.Spec.TTL.Duration, now)
	written, err := b.writeStatus(ctx, g)
	if written {
		b.decided(ctx, g, grantattest.Message(p.Name, *st))
	}
	return true, err
}

// topLevelSession reports whether s has no parent session. A session that a
// human, client or coordinator caller created names that caller's ServiceAccount
// as its parent (namespace/name) and carries depth 0; a session that another
// session created names that session (a DNS label, no slash) and carries depth 1
// or more. A session with neither, which only kubectl makes, is top-level too.
func topLevelSession(s *v1alpha1.AgentSession) bool {
	if s == nil {
		return false
	}
	if d, ok := s.Labels[v1alpha1.LabelDepth]; ok && d != "0" {
		return false
	}
	return s.Spec.Parent == "" || strings.Contains(s.Spec.Parent, "/")
}

// attestorIsCoordinator reports whether the statement's attestor is the
// envelope's coordinator, pod included. verifyAttestation and
// CheckMaintenanceIdentities already imply it (the attestor is the requester's
// current pod, and the coordinator is the requester with that pod); this is a
// second, explicit guard, kept so that a change to either cannot quietly let
// another pod's statement approve.
func attestorIsCoordinator(st grantattest.Statement, m *v1alpha1.MaintenanceTask) bool {
	return m != nil && st.Attestor.Session == m.Coordinator.Name && st.Attestor.SessionUID == m.Coordinator.UID && st.Attestor.PodUID == m.Coordinator.PodUID
}

// maintenanceTargetBusy reports whether another maintenance grant that has not
// ended holds g's node UID and resource, however it was approved: one that is
// Active (its stop path included, which keeps it Active until the executor is
// proven stopped), one that waits with a human approval about to be applied, or
// any grant whose executor is not known to be gone. It reads the API server, not
// the cache, so a lease approved a moment ago counts.
func (b *Broker) maintenanceTargetBusy(ctx context.Context, g *v1alpha1.AccessGrant) (bool, error) {
	var grants v1alpha1.AccessGrantList
	if err := b.APIReader.List(ctx, &grants, client.InNamespace(b.SessionNamespace)); err != nil {
		return false, fmt.Errorf("list the grants: %w", err)
	}
	m := g.Spec.Maintenance
	for i := range grants.Items {
		o := &grants.Items[i]
		if o.UID == g.UID || o.Spec.Type != v1alpha1.GrantMaintenance || o.Spec.Maintenance == nil {
			continue
		}
		if o.Spec.Maintenance.NodeUID != m.NodeUID || o.Spec.Maintenance.Resource != m.Resource {
			continue
		}
		if maintenanceLeaseHeld(o) {
			return true, nil
		}
	}
	return false, nil
}

// maintenanceLeaseHeld: o is Active, or Pending with a human approval recorded,
// or its executor is running, stopping or unknown whatever its phase.
func maintenanceLeaseHeld(o *v1alpha1.AccessGrant) bool {
	if st := o.Status.Maintenance; st != nil {
		switch st.Executor {
		case "Running", "StopPending", "Unknown":
			return true
		}
	}
	switch {
	case o.Status.Phase == v1alpha1.GrantActive:
		return true
	case o.Status.Phase.Ended():
		return false
	}
	var d MaintenanceDecision
	raw := o.Annotations[MaintenanceDecisionAnnotation]
	return raw != "" && len(raw) <= 2048 && json.Unmarshal([]byte(raw), &d) == nil && d.Approve
}

// attestedMaintenanceHolds re-checks an Active maintenance grant that a policy
// approved, on every pass of activeMaintenance: before its network policy and
// executor exist, and while they run. The flags are on; the requesting session
// is still top-level; the approving policy (read from the API server by name) is
// attested and still fits the envelope; and the statement still verifies in its
// execution form. A policy deleted and recreated under the same name is not told
// apart (ADR-013 follow-up). A grant the human route
// approved is not affected. A refusal is permanent: the caller ends the lease
// through the existing stop path. Any other error is a read to retry.
func (b *Broker) attestedMaintenanceHolds(ctx context.Context, g *v1alpha1.AccessGrant) error {
	name, ok := strings.CutPrefix(g.Status.ApprovedBy, PolicyApprover)
	if !ok {
		return nil
	}
	if name == "" {
		return permanent{errors.New("the approval names no policy")}
	}
	if !b.EnableAttestedGrants || !b.EnableAttestedMaintenance {
		return permanent{errors.New("attested maintenance approvals are disabled")}
	}
	var p v1alpha1.GrantPolicy
	if err := b.APIReader.Get(ctx, types.NamespacedName{Namespace: b.PolicyNamespace, Name: name}, &p); err != nil {
		if apierrors.IsNotFound(err) {
			return permanent{errors.New("the approving policy is absent")}
		}
		return fmt.Errorf("read the approving policy: %w", err)
	}
	if !p.DeletionTimestamp.IsZero() || p.Spec.Attested == nil || !MatchesAttested(&p, g) {
		return permanent{errors.New("the approving policy does not permit this lease")}
	}
	sess, err := b.requestingSession(ctx, g)
	if err != nil {
		return err
	}
	if sess != nil && !topLevelSession(sess) {
		return permanent{errors.New("the requesting session is a child session")}
	}
	if _, err := verifyAttestation(g, sess, nil, attestAtExecution, b.Clock.Now()); err != nil {
		return permanent{fmt.Errorf("agent attestation does not verify: %w", err)}
	}
	return nil
}
