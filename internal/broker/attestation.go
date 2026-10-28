package broker

// Agent attestation (DESIGN-001 D-88, Tom 2026-10-27 23:27Z: "just accept what the
// agent says; add guidance for the agent to seek coordinator or user approval
// first"). An attested GrantPolicy (spec.attested) approves a request only when
// the requesting session has recorded, through the operator's API, a statement
// that it obtained the user's or a coordinator's approval for exactly this grant.
// The statement is the agent's claim under delegated trust. It is NOT verified
// human identity, and nothing here or in any message built from it claims that
// it is.
//
// Everything is default off: without --enable-attested-grants the broker never
// approves through an attested policy (it skips it), and standing policies are
// untouched. The grant keeps approvedBy policy/<name>, so credentialApproval and
// the keeper's checks work as for any policy.

import (
	"context"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/grantattest"
)

// The grant API and the broker use one pending window (a compile-time check).
var _ = [1]struct{}{}[PendingTimeout-grantattest.PendingWindow]

// attestForm says when a statement is verified: before approval, the grant is
// Pending and the statement must name the session's current Running pod; at
// execution the grant is Active and the pod may have rolled on the same session
// (Tom's ruling and the existing reinstall flow).
type attestForm int

const (
	attestAtApproval attestForm = iota
	attestAtExecution
)

// verifyAttestation checks the grant's attestation against live objects and
// returns it. sess is the live requesting session and pod its current Running pod
// (needed only at approval). It never trusts a field the statement merely claims:
// the attestor, grant UID, session UID, scope digest and TTL are recomputed from
// the grant and session.
func verifyAttestation(g *v1alpha1.AccessGrant, sess *v1alpha1.AgentSession, pod *corev1.Pod, form attestForm, now time.Time) (grantattest.Statement, error) {
	st, err := grantattest.Parse(g.Annotations[grantattest.Annotation])
	if err != nil {
		return st, err
	}
	rq := g.Spec.Requester
	switch {
	case g.UID == "" || st.GrantUID != string(g.UID):
		return st, errors.New("the attestation is for another grant")
	case sess == nil || rq.SessionUID == "" || st.Attestor.SessionUID != string(rq.SessionUID) || st.Attestor.SessionUID != string(sess.UID) || st.Attestor.Session != rq.Session:
		return st, errors.New("the attestation is not from this grant's requesting session")
	case st.ScopeDigest != grantattest.ScopeDigest(g):
		return st, errors.New("the grant's scope or TTL differs from what was attested")
	case st.TTL != g.Spec.TTL.Duration.String():
		return st, errors.New("the grant's TTL differs from what was attested")
	case st.AttestedAt.Before(g.CreationTimestamp.Time):
		return st, errors.New("the attestation predates the grant")
	case st.AttestedAt.After(now.Add(grantattest.MaxClockSkew)):
		return st, errors.New("the attestation is dated in the future")
	}
	switch form {
	case attestAtApproval:
		switch {
		case g.Status.Phase != "" && g.Status.Phase != v1alpha1.GrantPending:
			return st, errors.New("the grant is not pending")
		case g.Spec.Release || !now.Before(g.CreationTimestamp.Add(PendingTimeout)):
			return st, errors.New("the grant was released or timed out")
		case pod == nil || st.Attestor.PodUID != string(pod.UID):
			return st, errors.New("the attestation is not from the session's current running pod")
		}
	case attestAtExecution:
		if g.Status.ApprovedAt == nil || st.AttestedAt.After(g.Status.ApprovedAt.Add(grantattest.MaxClockSkew)) {
			return st, errors.New("the attestation is later than the approval")
		}
	}
	return st, nil
}

// attestedApproval returns the policy to approve g through, if any, and the
// attestation that backs it. With the feature off it returns the standing match
// only: an attested policy is skipped and so never approves.
func (b *Broker) attestedApproval(ctx context.Context, g *v1alpha1.AccessGrant, sess *v1alpha1.AgentSession, policies []v1alpha1.GrantPolicy, now time.Time) (*v1alpha1.GrantPolicy, *grantattest.Statement) {
	if !b.EnableAttestedGrants || sess == nil {
		return Match(g, policies), nil
	}
	var verified *grantattest.Statement
	p := MatchAttested(g, policies, func(*v1alpha1.GrantPolicy) bool {
		if verified != nil {
			return true
		}
		st, err := verifyAttestation(g, sess, b.sessionPod(ctx, sess, true), attestAtApproval, now)
		if err != nil {
			return false
		}
		verified = &st
		return true
	})
	if p != nil && p.Spec.Attested == nil {
		verified = nil
	}
	return p, verified
}

// attestedPolicyForExecution re-checks an attested policy at execution: the
// feature is on, the exact approving policy still fits, and the grant's
// statement still verifies in its execution form.
func (b *Broker) attestedPolicyForExecution(ctx context.Context, g *v1alpha1.AccessGrant, p *v1alpha1.GrantPolicy) error {
	if !b.EnableAttestedGrants {
		return errors.New("attested approvals are disabled")
	}
	if !MatchesAttested(p, g) {
		return errors.New("the credential approving policy does not permit this request")
	}
	sess, err := b.requestingSession(ctx, g)
	if err != nil {
		return fmt.Errorf("read the requesting session: %w", err)
	}
	if _, err := verifyAttestation(g, sess, nil, attestAtExecution, b.Clock.Now()); err != nil {
		return fmt.Errorf("agent attestation does not verify: %w", err)
	}
	return nil
}
