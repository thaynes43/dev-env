package broker

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/grantdecision"
)

func TestPendingHandoffBindsScopeAndDoesNotTrustStalePod(t *testing.T) {
	f := credentialCase(t)
	g := f.getGrant()
	g.Spec.Type = v1alpha1.GrantEgress
	g.Spec.Credential = nil
	g.Spec.Egress = &v1alpha1.EgressGrant{FQDNs:[]string{"example.com"}, Ports:[]v1alpha1.GrantPort{{Port:443}}}
	if err := f.c.Update(context.Background(), g); err != nil { t.Fatal(err) }
	g = f.getGrant()
	g.Status.Phase = v1alpha1.GrantPending
	if err := f.c.Status().Update(context.Background(), g); err != nil { t.Fatal(err) }
	g = f.getGrant()
	binding, err := grantdecision.Bind(g, f.pod.UID)
	if err != nil { t.Fatal(err) }
	bad := grantdecision.Handoff{Binding:binding, Principal:"human/dev-env-system/dev-env-human", Decision:"approve"}
	bad.Binding.PodUID = "other-pod"
	encoded, _ := json.Marshal(bad)
	g.Annotations = map[string]string{grantdecision.HandoffAnnotation:string(encoded)}
	if err := f.c.Update(context.Background(), g); err != nil { t.Fatal(err) }
	g = f.getGrant()
	if handled, _, err := f.b.pendingHandoff(context.Background(), g); err != nil || handled { t.Fatal("stale pod accepted") }
	if f.getGrant().Status.Phase == v1alpha1.GrantActive { t.Fatal("stale grant became active") }
	bad.Binding = binding
	encoded, _ = json.Marshal(bad)
	g = f.getGrant()
	g.Annotations[grantdecision.HandoffAnnotation] = string(encoded)
	if err := f.c.Update(context.Background(), g); err != nil { t.Fatal(err) }
	g = f.getGrant()
	if handled, _, err := f.b.pendingHandoff(context.Background(), g); err != nil || !handled { t.Fatalf("valid decision not handled: %v", err) }
	if got := f.getGrant(); got.Status.Phase != v1alpha1.GrantActive || got.Status.ApprovedBy != bad.Principal { t.Fatal("decision not recorded") }
	g = f.getGrant()
	if handled, _, err := f.b.pendingHandoff(context.Background(), g); err != nil || handled { t.Fatal("replay accepted") }
}

func TestPendingHandoffRejectsStaleAndUntrusted(t *testing.T) {
	for name, mutate := range map[string]func(*v1alpha1.AccessGrant, *grantdecision.Handoff){
		"request changed": func(g *v1alpha1.AccessGrant, h *grantdecision.Handoff) { g.Spec.Reason = "changed" },
		"TTL changed": func(g *v1alpha1.AccessGrant, h *grantdecision.Handoff) { g.Spec.TTL.Duration += time.Minute },
		"released": func(g *v1alpha1.AccessGrant, h *grantdecision.Handoff) { g.Spec.Release = true },
		"grant replaced": func(g *v1alpha1.AccessGrant, h *grantdecision.Handoff) { h.Binding.GrantUID = "different" },
		"session replaced": func(g *v1alpha1.AccessGrant, h *grantdecision.Handoff) { h.Binding.SessionUID = "different" },
		"untrusted principal": func(g *v1alpha1.AccessGrant, h *grantdecision.Handoff) { h.Principal = "session/requester" },
		"overlong TTL": func(g *v1alpha1.AccessGrant, h *grantdecision.Handoff) { h.TTL = "8h" },
	} {
		t.Run(name, func(t *testing.T) {
			f := credentialCase(t)
			g := f.getGrant()
			g.Spec.Type = v1alpha1.GrantEgress
			g.Spec.Credential = nil
			g.Spec.Egress = &v1alpha1.EgressGrant{FQDNs: []string{"example.com"}, Ports: []v1alpha1.GrantPort{{Port:443}}}
			g.Status.Phase = v1alpha1.GrantPending
			binding, err := grantdecision.Bind(g, f.pod.UID)
			if err != nil { t.Fatal(err) }
			h := grantdecision.Handoff{Binding:binding, Principal:"human/dev-env-system/dev-env-human", Decision:"approve"}
			mutate(g, &h)
			encoded, _ := json.Marshal(h)
			g.Annotations = map[string]string{grantdecision.HandoffAnnotation:string(encoded)}
			if handled, _, err := f.b.pendingHandoff(context.Background(), g); err != nil || handled { t.Fatal("invalid decision accepted") }
			if f.getGrant().Status.Phase == v1alpha1.GrantActive { t.Fatal("invalid decision activated grant") }
		})
	}
}

func TestDelegatedPolicyRequiresExactScopeAndDoesNotAutoApprove(t *testing.T) {
	f := credentialCase(t)
	g := f.getGrant()
	g.Spec.Type = v1alpha1.GrantEgress
	g.Spec.Credential = nil
	g.Spec.Egress = &v1alpha1.EgressGrant{FQDNs: []string{"example.com"}, Ports: []v1alpha1.GrantPort{{Port:443}}}
	if err := f.c.Update(context.Background(), g); err != nil { t.Fatal(err) }
	g = f.getGrant()
	g.Status.Phase = v1alpha1.GrantPending
	if err := f.c.Status().Update(context.Background(), g); err != nil { t.Fatal(err) }
	g = f.getGrant()
	principal := "coordinator/dev-env-system/coordinator"
	sess := f.s.DeepCopy()
	sess.Spec.Parent = "dev-env-system/coordinator"
	if err := f.c.Update(context.Background(), sess); err != nil { t.Fatal(err) }
	policy := &v1alpha1.GrantPolicy{ObjectMeta: metav1.ObjectMeta{Namespace:systemNS, Name:"delegated", Annotations:map[string]string{grantdecision.ManualApproverAnnotation:principal}}, Spec:v1alpha1.GrantPolicySpec{Profiles:[]string{"full"}, Repos:[]string{"haynes-ops"}, Type:v1alpha1.GrantEgress, Egress:&v1alpha1.EgressPolicy{FQDNs:[]string{"example.com"}, Ports:[]v1alpha1.GrantPort{{Port:443}}}, MaxTTL:metav1.Duration{Duration:time.Hour}}}
	if Match(g, []v1alpha1.GrantPolicy{*policy}) != nil { t.Fatal("delegation auto-approved") }
	empty := policy.DeepCopy()
	empty.Annotations[grantdecision.ManualApproverAnnotation] = ""
	if Match(g, []v1alpha1.GrantPolicy{*empty}) != nil { t.Fatal("empty delegation auto-approved") }
	if err := f.c.Create(context.Background(), policy); err != nil { t.Fatal(err) }
	binding, err := grantdecision.Bind(g, f.pod.UID)
	if err != nil { t.Fatal(err) }
	h := grantdecision.Handoff{Binding:binding, Principal:principal, Decision:"approve"}
	encoded, _ := json.Marshal(h)
	g.Annotations = map[string]string{grantdecision.HandoffAnnotation:string(encoded)}
	if err := f.c.Update(context.Background(), g); err != nil { t.Fatal(err) }
	g = f.getGrant()
	if handled, _, err := f.b.pendingHandoff(context.Background(), g); err != nil || !handled { t.Fatalf("scoped delegate was not accepted: %v", err) }
	if f.getGrant().Status.ApprovedBy != principal { t.Fatal("delegate identity not recorded") }
}
