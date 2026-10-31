package broker

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// MaintenanceDecision is the set-once decision annotation
// (MaintenanceDecisionAnnotation). The operator's authenticated human route
// writes it on a maintenance grant; with --grant-delegates its delegate route
// (DESIGN-042 6.2) writes it on kube, egress and maintenance grants. The held
// admission packet protects it. It is not agent attestation, and a delegate
// decision is not a human decision.
type MaintenanceDecision struct {
	GrantUID string `json:"grantUID"`
	// Digest is MaintenanceDigest for a human decision and
	// grantattest.ScopeDigest for a delegate decision.
	Digest  string        `json:"digest"`
	By      string        `json:"by"`
	Approve bool          `json:"approve"`
	TTL     time.Duration `json:"ttl"`
	At      time.Time     `json:"at"`
	// Ref, Reason and Delegate are set by the delegate route only, so a human
	// decision encodes exactly as before.
	Ref      string            `json:"ref,omitempty"`
	Reason   string            `json:"reason,omitempty"`
	Delegate *DelegateIdentity `json:"delegate,omitempty"`
}

// DelegateIdentity is the delegate's pod-bound identity as the operator
// authenticated it: its form (1, a coordinator host; 2, a delegate session),
// its pod, and the AgentSession that controls that pod, when one does. The
// broker checks it against the requester again before it decides, its reach
// included.
type DelegateIdentity struct {
	Form       int    `json:"form"`
	PodName    string `json:"podName"`
	HostID     string `json:"hostID,omitempty"`
	PodUID     string `json:"podUID"`
	Session    string `json:"session,omitempty"`
	SessionUID string `json:"sessionUID,omitempty"`
}

// MaxDecisionBytes bounds the annotation value; the broker reads no longer one.
const MaxDecisionBytes = 2048

// parseDecision reads the decision annotation, or reports none.
func parseDecision(g *v1alpha1.AccessGrant) (MaintenanceDecision, bool) {
	var d MaintenanceDecision
	raw := g.Annotations[MaintenanceDecisionAnnotation]
	if raw == "" || len(raw) > MaxDecisionBytes || json.Unmarshal([]byte(raw), &d) != nil {
		return d, false
	}
	return d, true
}

func (b *Broker) maintenanceDecision(ctx context.Context, g *v1alpha1.AccessGrant) (bool, error) {
	raw := g.Annotations[MaintenanceDecisionAnnotation]
	if raw == "" {
		return false, nil
	}
	var d MaintenanceDecision
	if len(raw) > MaxDecisionBytes || json.Unmarshal([]byte(raw), &d) != nil || d.GrantUID != string(g.UID) || d.Digest != MaintenanceDigest(g) || d.By != b.maintenanceHumanApprover() || d.At.Before(g.CreationTimestamp.Time) || d.At.After(b.now().Time) || !b.now().Time.Before(d.At.Add(PendingTimeout)) {
		return true, fmt.Errorf("%w: invalid maintenance decision binding", ErrRefused)
	}
	err := b.Decide(ctx, g.Name, Decision{Approve: d.Approve, By: d.By, TTL: d.TTL, MaintenanceUID: d.GrantUID, MaintenanceDigest: d.Digest})
	return true, err
}

func (b *Broker) maintenanceHumanApprover() string {
	ref := b.MaintenanceHumanServiceAccount
	if ref == "" {
		ref = "dev-env-system/dev-env-human"
	}
	return "human/" + ref
}

// isDelegateDecision reports whether d is a delegate's decision this broker
// reads: delegates are configured and d names one by its prefix. Whether the
// named delegate is on the list is checked with the rest of the binding.
func (b *Broker) isDelegateDecision(d MaintenanceDecision) bool {
	return len(b.GrantDelegates) > 0 && strings.HasPrefix(d.By, DelegateApprover)
}
