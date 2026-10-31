package broker

// Attested maintenance approval (ADR-013, Tom 2026-10-31: "I or an agent can
// approve with a message to the agent requesting the grant"). A maintenance grant
// may also be approved by the requesting session's agent attestation (D-88),
// through an attested GrantPolicy whose maintenance scope matches the envelope
// exactly (maintenanceInside). The attestation is a claim, not verified human
// identity.
//
// Default off. pending() calls this only after the authenticated human route
// (maintenanceDecision) left the grant alone, and only with
// --enable-attested-grants and --enable-attested-maintenance; Reconcile has
// already refused every new maintenance grant without
// --enable-maintenance-grants. No standing policy approves maintenance (Matches).

import (
	"context"
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
// envelope. The attesting pod must be the envelope's coordinator pod, and the
// identity, worker and node checks of the human route (CheckMaintenanceIdentities)
// must pass. approved reports that an approval was written, or attempted.
func (b *Broker) attestedMaintenanceApproval(ctx context.Context, g *v1alpha1.AccessGrant, sess *v1alpha1.AgentSession, now metav1.Time) (approved bool, err error) {
	if !b.EnableMaintenanceGrants || !b.EnableAttestedGrants || !b.EnableAttestedMaintenance || sess == nil || g.Spec.Type != v1alpha1.GrantMaintenance {
		return false, nil
	}
	var policies v1alpha1.GrantPolicyList
	if err := b.Client.List(ctx, &policies, client.InNamespace(b.PolicyNamespace)); err != nil {
		return false, fmt.Errorf("list the grant policies: %w", err)
	}
	p, st := b.attestedApproval(ctx, g, sess, policies.Items, now.Time)
	if p == nil || st == nil || p.Spec.Attested == nil {
		return false, nil
	}
	m := g.Spec.Maintenance
	if m == nil || st.Attestor.Session != m.Coordinator.Name || st.Attestor.SessionUID != m.Coordinator.UID || st.Attestor.PodUID != m.Coordinator.PodUID {
		log.FromContext(ctx).V(1).Info("attested maintenance: the attestor is not the envelope's coordinator pod", grantFields(g)...)
		return false, nil
	}
	if err := CheckMaintenanceIdentities(ctx, b.APIReader, b.SessionNamespace, g); err != nil {
		log.FromContext(ctx).V(1).Info("attested maintenance: identity or target check failed; the grant waits", append(grantFields(g), "reason", err.Error())...)
		return false, nil
	}
	// A policy bounds each lease, not how many run at once: this route approves
	// one lease per node UID and resource at a time.
	if busy, err := b.maintenanceTargetBusy(ctx, g); err != nil || busy {
		if busy {
			log.FromContext(ctx).V(1).Info("attested maintenance: another lease holds this target; the grant waits", grantFields(g)...)
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

// maintenanceTargetBusy reports whether another Active maintenance grant, however
// it was approved, holds g's node UID and resource. It reads the API server, not
// the cache, so a lease approved a moment ago counts.
func (b *Broker) maintenanceTargetBusy(ctx context.Context, g *v1alpha1.AccessGrant) (bool, error) {
	var grants v1alpha1.AccessGrantList
	if err := b.APIReader.List(ctx, &grants, client.InNamespace(b.SessionNamespace)); err != nil {
		return false, fmt.Errorf("list the grants: %w", err)
	}
	m := g.Spec.Maintenance
	for i := range grants.Items {
		o := &grants.Items[i]
		if o.UID == g.UID || o.Spec.Type != v1alpha1.GrantMaintenance || o.Spec.Maintenance == nil || o.Status.Phase != v1alpha1.GrantActive {
			continue
		}
		if o.Spec.Maintenance.NodeUID == m.NodeUID && o.Spec.Maintenance.Resource == m.Resource {
			return true, nil
		}
	}
	return false, nil
}

// attestedMaintenanceHolds re-checks an Active maintenance grant that a policy
// approved, on every pass of activeMaintenance: before its network policy and
// executor exist, and while they run. The flags are on, the approving policy
// (read from the API server) is attested and still fits the envelope, and the
// statement still verifies in its execution form. A grant the human route
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
	if _, err := verifyAttestation(g, sess, nil, attestAtExecution, b.Clock.Now()); err != nil {
		return permanent{fmt.Errorf("agent attestation does not verify: %w", err)}
	}
	return nil
}
