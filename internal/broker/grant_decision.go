package broker

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/grantdecision"
)

// rejectHandoff clears a rejected or stale handoff so it cannot block a later
// authorized decision. It changes metadata only and records no untrusted text.
func (b *Broker) rejectHandoff(ctx context.Context, g *v1alpha1.AccessGrant) (bool, ctrl.Result, error) {
	orig := g.DeepCopy()
	delete(g.Annotations, grantdecision.HandoffAnnotation)
	if err := b.Client.Patch(ctx, g, client.MergeFromWithOptions(orig, client.MergeFromWithOptimisticLock{})); err != nil {
		return true, ctrl.Result{}, err
	}
	b.event(g, corev1.EventTypeWarning, "Decision", "HandoffRefused", "stale or unauthorized decision handoff was cleared")
	return true, ctrl.Result{}, nil
}

// pendingHandoff only consumes a decision on the same grant object and version
// that it updates. Its key must be admission-protected before enabling it.
func (b *Broker) pendingHandoff(ctx context.Context, g *v1alpha1.AccessGrant) (bool, ctrl.Result, error) {
	if g.Status.Phase != "" && g.Status.Phase != v1alpha1.GrantPending {
		return false, ctrl.Result{}, nil
	}
	raw := g.Annotations[grantdecision.HandoffAnnotation]
	if raw == "" {
		return false, ctrl.Result{}, nil
	}
	if len(raw) > 4096 {
		return b.rejectHandoff(ctx, g)
	}
	var h grantdecision.Handoff
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&h) != nil || dec.Decode(new(any)) != io.EOF || h.Principal == "" || (h.Decision != "approve" && h.Decision != "deny") || len(h.Reason) > 512 {
		return b.rejectHandoff(ctx, g)
	}
	if strings.HasPrefix(h.Principal, "coordinator/") || strings.HasPrefix(h.Principal, "session/") {
		if !grantdecision.ValidRelayRef(h.RelayRef) {
			return b.rejectHandoff(ctx, g)
		}
		var policies v1alpha1.GrantPolicyList
		if err := b.APIReader.List(ctx, &policies, client.InNamespace(b.PolicyNamespace)); err != nil {
			return true, ctrl.Result{}, err
		}
		allowed := false
		for i := range policies.Items {
			p := &policies.Items[i]
			if p.DeletionTimestamp.IsZero() && p.Annotations[grantdecision.ManualApproverAnnotation] == h.Principal && Matches(p, g) {
				allowed = true
				break
			}
		}
		if !allowed {
			return b.rejectHandoff(ctx, g)
		}
	} else if !strings.HasPrefix(h.Principal, "human/") || strings.TrimPrefix(h.Principal, "human/") == "" || h.RelayRef != "" {
		return b.rejectHandoff(ctx, g)
	}
	sess, err := b.requestingSession(ctx, g)
	if err != nil {
		return true, ctrl.Result{}, err
	}
	if sess == nil || !sess.DeletionTimestamp.IsZero() {
		return b.rejectHandoff(ctx, g)
	}
	if strings.HasPrefix(h.Principal, "coordinator/") && sess.Spec.Parent != strings.TrimPrefix(h.Principal, "coordinator/") {
		return b.rejectHandoff(ctx, g)
	}
	if strings.HasPrefix(h.Principal, "session/") {
		parts := strings.Split(h.Principal, "/")
		if len(parts) != 4 || parts[1] == "" || parts[2] == "" || parts[3] == "" || sess.Spec.Parent != parts[1] || string(sess.UID) == parts[2] || (sess.Spec.UsageCaptureParentUID != "" && sess.Spec.UsageCaptureParentUID != parts[2]) {
			return b.rejectHandoff(ctx, g)
		}
		var parent v1alpha1.AgentSession
		if err := b.APIReader.Get(ctx, types.NamespacedName{Namespace: b.SessionNamespace, Name: parts[1]}, &parent); err != nil || string(parent.UID) != parts[2] || !parent.DeletionTimestamp.IsZero() {
			return b.rejectHandoff(ctx, g)
		}
		parentPod := b.sessionPod(ctx, &parent, true)
		if parentPod == nil || string(parentPod.UID) != parts[3] {
			return b.rejectHandoff(ctx, g)
		}
	}
	pod := b.sessionPod(ctx, sess, true)
	if pod == nil || !h.Binding.Matches(g, pod.UID) {
		return b.rejectHandoff(ctx, g)
	}
	d := Decision{By: h.Principal, Approve: h.Decision == "approve", Reason: h.Reason}
	if h.TTL != "" {
		d.TTL, err = time.ParseDuration(h.TTL)
		if err != nil || d.TTL <= 0 {
			return b.rejectHandoff(ctx, g)
		}
	}
	if (d.Approve && h.Reason != "") || (!d.Approve && h.TTL != "") {
		return b.rejectHandoff(ctx, g)
	}
	now := b.now()
	ttl, err := b.decisionAllowed(g, d, now.Time)
	if err != nil {
		return b.rejectHandoff(ctx, g)
	}
	msg := ""
	if d.Approve {
		approve(g, d.By, ttl, now)
	} else {
		g.Status.Phase = v1alpha1.GrantDenied
		g.Status.DeniedBy = d.By
		msg = d.Reason
		if msg == "" {
			msg = "denied by " + d.By
		}
		g.Status.Message = truncate(msg)
		g.Status.EndedAt = &now
	}
	if err := b.Client.Status().Update(ctx, g); err != nil {
		return true, ctrl.Result{}, err
	}
	auditMessage := msg
	if h.RelayRef != "" {
		auditMessage = strings.TrimSpace(auditMessage + " relay-reference=" + h.RelayRef)
	}
	b.decided(log.IntoContext(ctx, b.logger()), g, auditMessage)
	return true, ctrl.Result{}, nil
}
