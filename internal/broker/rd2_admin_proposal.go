package broker

// RD2 proposal compiler only. No broker, manager, API route or command calls
// this helper. It grants nothing and writes nothing. The bool is default off.
// TODO(RD2 activation): persist an immutable binding after authorized decision,
// render BOTH reviewed guards, cap TokenRequest lifetime, and wire owned revoke.

import (
	"context"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// rd2ProposalBinding is an input to offline proposal compilation, not an
// approval assertion. All UIDs must originate from the authorized activation
// transaction. Reading a replacement SA never updates an existing binding.
type rd2ProposalBinding struct {
	GrantName         string
	GrantUID          types.UID
	ServiceAccountUID types.UID
	Requester         string
	RequesterUID      types.UID
	PodUID            types.UID
}

// rd2ProposalExemption returns literal false on EVERY error, including failed
// reads. The caller must discard older output. A static installed exemption has
// no request-time lookup: this is NOT an admission revocation controller.
func rd2ProposalExemption(ctx context.Context, enableRD2AdminProposal bool, r client.Reader, bound rd2ProposalBinding, now time.Time) (string, error) {
	if !enableRD2AdminProposal {
		return "false", nil
	}
	if r == nil || bound.GrantName == "" || bound.GrantUID == "" || bound.ServiceAccountUID == "" || bound.Requester == "" || bound.RequesterUID == "" || bound.PodUID == "" {
		return "false", errors.New("RD2 proposal requires an exact immutable binding and reader")
	}
	g := &v1alpha1.AccessGrant{}
	sa := &corev1.ServiceAccount{}
	s := &v1alpha1.AgentSession{}
	pod := &corev1.Pod{}
	for _, read := range []struct {
		key    client.ObjectKey
		object client.Object
	}{
		{client.ObjectKey{Namespace: "dev-agents", Name: bound.GrantName}, g},
		{client.ObjectKey{Namespace: "dev-agents", Name: bound.GrantName}, sa},
		{client.ObjectKey{Namespace: "dev-agents", Name: bound.Requester}, s},
		{client.ObjectKey{Namespace: "dev-agents", Name: bound.Requester}, pod},
	} {
		if err := r.Get(ctx, read.key, read.object); err != nil {
			return "false", fmt.Errorf("RD2 proposal binding lookup: %w", err)
		}
	}
	if g.UID != bound.GrantUID || sa.UID != bound.ServiceAccountUID || s.UID != bound.RequesterUID || pod.UID != bound.PodUID ||
		g.Spec.Requester.Session != bound.Requester || g.Spec.Requester.SessionUID != bound.RequesterUID ||
		sa.Labels[v1alpha1.LabelGrant] != bound.GrantName || g.Status.ServiceAccount != sa.Name ||
		(g.Status.InstalledPodUID != "" && g.Status.InstalledPodUID != string(bound.PodUID)) || pod.Labels[v1alpha1.LabelSession] != bound.Requester || pod.Status.Phase != corev1.PodRunning {
		return "false", errors.New("RD2 proposal identity differs from immutable binding")
	}
	for _, o := range []client.Object{g, sa, s, pod} {
		if !o.GetDeletionTimestamp().IsZero() {
			return "false", errors.New("RD2 proposal identity is deleting")
		}
	}
	if g.Spec.Type != v1alpha1.GrantBreakglass || g.Spec.Kube == nil || g.Spec.Kube.Role != v1alpha1.RoleBreakglass || len(g.Spec.Kube.Namespaces) != 0 ||
		g.Status.Phase != v1alpha1.GrantActive || g.Spec.Release || g.Status.EndedAt != nil || g.Status.ApprovedBy == "" ||
		g.Status.ApprovedAt == nil || g.Status.ApprovedAt.Time.After(now) || g.Status.ExpiresAt == nil || !now.Before(g.Status.ExpiresAt.Time) ||
		g.Spec.TTL.Duration <= 0 || g.Spec.TTL.Duration > 30*time.Minute ||
		g.Status.ExpiresAt.Time.After(g.Status.ApprovedAt.Add(30*time.Minute)) {
		return "false", errors.New("RD2 proposal requires an approved Active, unexpired, unreleased lease of at most 30 minutes")
	}
	return fmt.Sprintf("request.userInfo.username == %q && request.userInfo.uid == %q", "system:serviceaccount:dev-agents:"+sa.Name, string(sa.UID)), nil
}
