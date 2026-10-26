package broker

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/grantdecision"
)

// pendingHandoff only consumes a decision on the same grant object and version
// that it updates. Its key must be admission-protected before enabling it.
func (b *Broker) pendingHandoff(ctx context.Context, g *v1alpha1.AccessGrant) (bool, ctrl.Result, error) {
	raw := g.Annotations[grantdecision.HandoffAnnotation]
	if raw == "" || len(raw) > 4096 { return false, ctrl.Result{}, nil }
	var h grantdecision.Handoff
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&h) != nil || dec.Decode(new(any)) != io.EOF || h.Principal == "" || (h.Decision != "approve" && h.Decision != "deny") || len(h.Reason) > 512 {
		return false, ctrl.Result{}, nil
	}
	if strings.HasPrefix(h.Principal, "coordinator/") {
		var policies v1alpha1.GrantPolicyList
		if err := b.APIReader.List(ctx, &policies, client.InNamespace(b.PolicyNamespace)); err != nil { return true, ctrl.Result{}, err }
		allowed := false
		for i := range policies.Items {
			p := &policies.Items[i]
			if p.DeletionTimestamp.IsZero() && p.Annotations[grantdecision.ManualApproverAnnotation] == h.Principal && Matches(p, g) { allowed = true; break }
		}
		if !allowed { return false, ctrl.Result{}, nil }
	} else if !strings.HasPrefix(h.Principal, "human/") || strings.TrimPrefix(h.Principal, "human/") == "" {
		return false, ctrl.Result{}, nil
	}
	sess, err := b.requestingSession(ctx, g)
	if err != nil { return true, ctrl.Result{}, err }
	if sess == nil || !sess.DeletionTimestamp.IsZero() { return false, ctrl.Result{}, nil }
	if strings.HasPrefix(h.Principal, "coordinator/") && sess.Spec.Parent != strings.TrimPrefix(h.Principal, "coordinator/") { return false, ctrl.Result{}, nil }
	pod := b.sessionPod(ctx, sess, true)
	if pod == nil || !h.Binding.Matches(g, pod.UID) { return false, ctrl.Result{}, nil }
	d := Decision{By:h.Principal, Approve:h.Decision == "approve", Reason:h.Reason}
	if h.TTL != "" { d.TTL, err = time.ParseDuration(h.TTL); if err != nil || d.TTL <= 0 { return false, ctrl.Result{}, nil } }
	if (d.Approve && h.Reason != "") || (!d.Approve && h.TTL != "") { return false, ctrl.Result{}, nil }
	now := b.now()
	ttl, err := b.decisionAllowed(g, d, now.Time)
	if err != nil { return false, ctrl.Result{}, nil }
	msg := ""
	if d.Approve {
		approve(g, d.By, ttl, now)
	} else {
		g.Status.Phase = v1alpha1.GrantDenied
		g.Status.DeniedBy = d.By
		msg = d.Reason
		if msg == "" { msg = "denied by " + d.By }
		g.Status.Message = truncate(msg)
		g.Status.EndedAt = &now
	}
	if err := b.Client.Status().Update(ctx, g); err != nil { return true, ctrl.Result{}, err }
	b.decided(log.IntoContext(ctx, b.logger()), g, msg)
	return true, ctrl.Result{}, nil
}
