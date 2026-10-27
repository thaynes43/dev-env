package broker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/grantdecision"
)

func TestPendingHandoffBindsScopeAndDoesNotTrustStalePod(t *testing.T) {
	f := credentialCase(t)
	g := f.getGrant()
	g.Spec.Type = v1alpha1.GrantEgress
	g.Spec.Credential = nil
	g.Spec.Egress = &v1alpha1.EgressGrant{FQDNs: []string{"example.com"}, Ports: []v1alpha1.GrantPort{{Port: 443}}}
	if err := f.c.Update(context.Background(), g); err != nil {
		t.Fatal(err)
	}
	g = f.getGrant()
	g.Status.Phase = v1alpha1.GrantPending
	if err := f.c.Status().Update(context.Background(), g); err != nil {
		t.Fatal(err)
	}
	g = f.getGrant()
	binding, err := grantdecision.Bind(g, f.pod.UID)
	if err != nil {
		t.Fatal(err)
	}
	bad := grantdecision.Handoff{Binding: binding, Principal: "human/dev-env-system/dev-env-human", Decision: "approve"}
	bad.Binding.PodUID = "other-pod"
	encoded, _ := json.Marshal(bad)
	g.Annotations = map[string]string{grantdecision.HandoffAnnotation: string(encoded)}
	if err := f.c.Update(context.Background(), g); err != nil {
		t.Fatal(err)
	}
	g = f.getGrant()
	if handled, _, err := f.b.pendingHandoff(context.Background(), g); err != nil || !handled {
		t.Fatal("stale pod was not rejected and cleared")
	}
	if f.getGrant().Status.Phase == v1alpha1.GrantActive {
		t.Fatal("stale grant became active")
	}
	bad.Binding = binding
	encoded, _ = json.Marshal(bad)
	g = f.getGrant()
	if g.Annotations == nil {
		g.Annotations = map[string]string{}
	}
	g.Annotations[grantdecision.HandoffAnnotation] = string(encoded)
	if err := f.c.Update(context.Background(), g); err != nil {
		t.Fatal(err)
	}
	g = f.getGrant()
	if handled, _, err := f.b.pendingHandoff(context.Background(), g); err != nil || !handled {
		t.Fatalf("valid decision not handled: %v", err)
	}
	if got := f.getGrant(); got.Status.Phase != v1alpha1.GrantActive || got.Status.ApprovedBy != bad.Principal {
		t.Fatal("decision not recorded")
	}
	g = f.getGrant()
	if handled, _, err := f.b.pendingHandoff(context.Background(), g); err != nil || handled {
		t.Fatal("replay accepted")
	}
}

func TestPendingHandoffRejectsStaleAndUntrusted(t *testing.T) {
	for name, mutate := range map[string]func(*v1alpha1.AccessGrant, *grantdecision.Handoff){
		"request changed":     func(g *v1alpha1.AccessGrant, h *grantdecision.Handoff) { g.Spec.Reason = "changed" },
		"TTL changed":         func(g *v1alpha1.AccessGrant, h *grantdecision.Handoff) { g.Spec.TTL.Duration += time.Minute },
		"released":            func(g *v1alpha1.AccessGrant, h *grantdecision.Handoff) { g.Spec.Release = true },
		"grant replaced":      func(g *v1alpha1.AccessGrant, h *grantdecision.Handoff) { h.Binding.GrantUID = "different" },
		"session replaced":    func(g *v1alpha1.AccessGrant, h *grantdecision.Handoff) { h.Binding.SessionUID = "different" },
		"untrusted principal": func(g *v1alpha1.AccessGrant, h *grantdecision.Handoff) { h.Principal = "session/requester" },
		"overlong TTL":        func(g *v1alpha1.AccessGrant, h *grantdecision.Handoff) { h.TTL = "8h" },
	} {
		t.Run(name, func(t *testing.T) {
			f := credentialCase(t)
			g := f.getGrant()
			g.Spec.Type = v1alpha1.GrantEgress
			g.Spec.Credential = nil
			g.Spec.Egress = &v1alpha1.EgressGrant{FQDNs: []string{"example.com"}, Ports: []v1alpha1.GrantPort{{Port: 443}}}
			g.Status.Phase = v1alpha1.GrantPending
			binding, err := grantdecision.Bind(g, f.pod.UID)
			if err != nil {
				t.Fatal(err)
			}
			h := grantdecision.Handoff{Binding: binding, Principal: "human/dev-env-system/dev-env-human", Decision: "approve"}
			mutate(g, &h)
			encoded, _ := json.Marshal(h)
			g.Annotations = map[string]string{grantdecision.HandoffAnnotation: string(encoded)}
			if handled, _, err := f.b.pendingHandoff(context.Background(), g); err != nil || !handled {
				t.Fatal("invalid decision was not rejected and cleared")
			}
			if f.getGrant().Status.Phase == v1alpha1.GrantActive {
				t.Fatal("invalid decision activated grant")
			}
		})
	}
}

func TestDelegatedPolicyRequiresExactScopeAndDoesNotAutoApprove(t *testing.T) {
	f := credentialCase(t)
	g := f.getGrant()
	g.Spec.Type = v1alpha1.GrantEgress
	g.Spec.Credential = nil
	g.Spec.Egress = &v1alpha1.EgressGrant{FQDNs: []string{"example.com"}, Ports: []v1alpha1.GrantPort{{Port: 443}}}
	if err := f.c.Update(context.Background(), g); err != nil {
		t.Fatal(err)
	}
	g = f.getGrant()
	g.Status.Phase = v1alpha1.GrantPending
	if err := f.c.Status().Update(context.Background(), g); err != nil {
		t.Fatal(err)
	}
	g = f.getGrant()
	principal := "coordinator/dev-env-system/coordinator"
	sess := f.s.DeepCopy()
	sess.Spec.Parent = "dev-env-system/coordinator"
	if err := f.c.Update(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	policy := &v1alpha1.GrantPolicy{ObjectMeta: metav1.ObjectMeta{Namespace: systemNS, Name: "delegated", Annotations: map[string]string{grantdecision.ManualApproverAnnotation: principal}}, Spec: v1alpha1.GrantPolicySpec{Profiles: []string{"full"}, Repos: []string{"haynes-ops"}, Type: v1alpha1.GrantEgress, Egress: &v1alpha1.EgressPolicy{FQDNs: []string{"example.com"}, Ports: []v1alpha1.GrantPort{{Port: 443}}}, MaxTTL: metav1.Duration{Duration: time.Hour}}}
	if Match(g, []v1alpha1.GrantPolicy{*policy}) != nil {
		t.Fatal("delegation auto-approved")
	}
	empty := policy.DeepCopy()
	empty.Annotations[grantdecision.ManualApproverAnnotation] = ""
	if Match(g, []v1alpha1.GrantPolicy{*empty}) != nil {
		t.Fatal("empty delegation auto-approved")
	}
	if err := f.c.Create(context.Background(), policy); err != nil {
		t.Fatal(err)
	}
	binding, err := grantdecision.Bind(g, f.pod.UID)
	if err != nil {
		t.Fatal(err)
	}
	h := grantdecision.Handoff{Binding: binding, Principal: principal, Decision: "approve", RelayRef: "question/1-answer/1"}
	encoded, _ := json.Marshal(h)
	g.Annotations = map[string]string{grantdecision.HandoffAnnotation: string(encoded)}
	if err := f.c.Update(context.Background(), g); err != nil {
		t.Fatal(err)
	}
	g = f.getGrant()
	if handled, _, err := f.b.pendingHandoff(context.Background(), g); err != nil || !handled {
		t.Fatalf("scoped delegate was not accepted: %v", err)
	}
	if f.getGrant().Status.ApprovedBy != principal {
		t.Fatal("delegate identity not recorded")
	}
}

func TestParentSessionRelayRequiresLiveActorAndExplicitPolicy(t *testing.T) {
	f := credentialCase(t)
	ctx := context.Background()
	parent := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: "parent-one", UID: "parent-uid"}}
	if err := f.c.Create(ctx, parent); err != nil {
		t.Fatal(err)
	}
	parentPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: parent.Name, UID: "parent-pod", Labels: map[string]string{v1alpha1.LabelSession: parent.Name}, OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(parent, v1alpha1.GroupVersion.WithKind("AgentSession"))}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	if err := f.c.Create(ctx, parentPod); err != nil {
		t.Fatal(err)
	}
	parentPod.Status.Phase = corev1.PodRunning
	if err := f.c.Status().Update(ctx, parentPod); err != nil {
		t.Fatal(err)
	}
	child := f.s.DeepCopy()
	child.Spec.Parent = parent.Name
	if err := f.c.Update(ctx, child); err != nil {
		t.Fatal(err)
	}
	g := f.getGrant()
	g.Spec.Type = v1alpha1.GrantEgress
	g.Spec.Credential = nil
	g.Spec.Egress = &v1alpha1.EgressGrant{FQDNs: []string{"example.com"}, Ports: []v1alpha1.GrantPort{{Port: 443}}}
	if err := f.c.Update(ctx, g); err != nil {
		t.Fatal(err)
	}
	g = f.getGrant()
	g.Status.Phase = v1alpha1.GrantPending
	if err := f.c.Status().Update(ctx, g); err != nil {
		t.Fatal(err)
	}
	g = f.getGrant()
	binding, err := grantdecision.Bind(g, f.pod.UID)
	if err != nil {
		t.Fatal(err)
	}
	principal := "session/parent-one/parent-uid/parent-pod"
	h := grantdecision.Handoff{Binding: binding, Principal: principal, Decision: "approve", RelayRef: "question/one-answer/one"}
	encoded, _ := json.Marshal(h)
	g.Annotations = map[string]string{grantdecision.HandoffAnnotation: string(encoded)}
	if err := f.c.Update(ctx, g); err != nil {
		t.Fatal(err)
	}
	g = f.getGrant()
	if handled, _, err := f.b.pendingHandoff(ctx, g); err != nil || !handled {
		t.Fatal("undelegated parent was not rejected and cleared")
	}
	policy := &v1alpha1.GrantPolicy{ObjectMeta: metav1.ObjectMeta{Namespace: systemNS, Name: "parent-delegation", Annotations: map[string]string{grantdecision.ManualApproverAnnotation: principal}}, Spec: v1alpha1.GrantPolicySpec{Profiles: []string{"full"}, Repos: []string{"haynes-ops"}, Type: v1alpha1.GrantEgress, Egress: &v1alpha1.EgressPolicy{FQDNs: []string{"example.com"}, Ports: []v1alpha1.GrantPort{{Port: 443}}}, MaxTTL: metav1.Duration{Duration: time.Hour}}}
	if err := f.c.Create(ctx, policy); err != nil {
		t.Fatal(err)
	}
	g = f.getGrant()
	if g.Annotations == nil {
		g.Annotations = map[string]string{}
	}
	g.Annotations[grantdecision.HandoffAnnotation] = string(encoded)
	if err := f.c.Update(ctx, g); err != nil {
		t.Fatal(err)
	}
	// Transient reads of the parent session, parent pod, or requester pod
	// must retain the authenticated handoff for a retry.
	original := f.b.APIReader.(client.WithWatch)
	for _, target := range []string{"parent-session", "parent-pod", "requester-pod"} {
		f.b.APIReader = interceptor.NewClient(original, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			_, isSession := obj.(*v1alpha1.AgentSession)
			_, isPod := obj.(*corev1.Pod)
			if (target == "parent-session" && isSession && key.Name == parent.Name) || (target == "parent-pod" && isPod && key.Name == parent.Name) || (target == "requester-pod" && isPod && key.Name == f.s.Name) {
				return errors.New("temporary read failure")
			}
			return c.Get(ctx, key, obj, opts...)
		}})
		g = f.getGrant()
		if handled, _, err := f.b.pendingHandoff(ctx, g); !handled || err == nil {
			t.Fatalf("%s: transient error was not retried", target)
		}
		if got := f.getGrant(); got.Annotations[grantdecision.HandoffAnnotation] != string(encoded) || got.Status.Phase == v1alpha1.GrantActive {
			t.Fatalf("%s: handoff changed", target)
		}
	}
	f.b.APIReader = original
	g = f.getGrant()
	if handled, _, err := f.b.pendingHandoff(ctx, g); err != nil || !handled {
		t.Fatalf("delegated parent rejected: %v", err)
	}
	if got := f.getGrant(); got.Status.ApprovedBy != principal || got.Status.Phase != v1alpha1.GrantActive {
		t.Fatal("relay not attributed to parent")
	}
}

func TestHandoffCannotApproveBreakglassWithoutFreshAccountAuthentication(t *testing.T) {
	f := credentialCase(t)
	g := f.getGrant()
	g.Spec.Type = v1alpha1.GrantBreakglass
	g.Spec.Credential = nil
	g.Spec.Kube = &v1alpha1.KubeGrant{Role: v1alpha1.RoleBreakglass}
	g.Status.Phase = v1alpha1.GrantPending
	binding, err := grantdecision.Bind(g, f.pod.UID)
	if err != nil {
		t.Fatal(err)
	}
	h := grantdecision.Handoff{Binding: binding, Principal: "human/dev-env-system/dev-env-human", Decision: "approve"}
	raw, _ := json.Marshal(h)
	g.Annotations = map[string]string{grantdecision.HandoffAnnotation: string(raw)}
	if handled, _, err := f.b.pendingHandoff(context.Background(), g); err != nil || !handled {
		t.Fatal("breakglass handoff not refused")
	}
	if f.getGrant().Status.Phase == v1alpha1.GrantActive {
		t.Fatal("breakglass became active without freshness")
	}
}
