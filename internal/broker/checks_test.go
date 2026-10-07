package broker

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

func TestCheck(t *testing.T) {
	cases := []struct {
		name  string
		edit  func(*v1alpha1.AccessGrant)
		fails string // a part of the error; "" passes
	}{
		{"a namespaced role", nil, ""},
		{"nodes, cluster-wide", func(g *v1alpha1.AccessGrant) { g.Spec.Kube.Role, g.Spec.Kube.Namespaces = v1alpha1.RoleNodes, nil }, ""},
		{"break-glass", func(g *v1alpha1.AccessGrant) {
			g.Spec.Type, g.Spec.Kube.Role, g.Spec.Kube.Namespaces = v1alpha1.GrantBreakglass, v1alpha1.RoleBreakglass, nil
		}, ""},
		{"not grant-<id>", func(g *v1alpha1.AccessGrant) { g.Name = "dev-env-workbench" }, "not grant-<id>"},
		{"too long a name", func(g *v1alpha1.AccessGrant) { g.Name = "grant-" + strings.Repeat("a", 60) }, "not grant-<id>"},
		{"no session", func(g *v1alpha1.AccessGrant) { g.Spec.Requester.Session = "" }, "no requesting session"},
		{"unknown type", func(g *v1alpha1.AccessGrant) { g.Spec.Type = "lease" }, "unknown grant type"},
		{"kube with no kube", func(g *v1alpha1.AccessGrant) { g.Spec.Kube = nil }, "kube is set"},
		{"kube with egress too", func(g *v1alpha1.AccessGrant) { g.Spec.Egress = &v1alpha1.EgressGrant{} }, "egress is set"},
		{"a role outside the catalog", func(g *v1alpha1.AccessGrant) { g.Spec.Kube.Role = "cluster-admin" }, "not in the grant role catalog"},
		{"edit", func(g *v1alpha1.AccessGrant) { g.Spec.Kube.Role = "edit" }, "not in the grant role catalog"},
		{"break-glass role under type kube", func(g *v1alpha1.AccessGrant) {
			g.Spec.Kube.Role, g.Spec.Kube.Namespaces = v1alpha1.RoleBreakglass, nil
		}, "go together"},
		{"type breakglass with another role", func(g *v1alpha1.AccessGrant) {
			g.Spec.Type, g.Spec.Kube.Role, g.Spec.Kube.Namespaces = v1alpha1.GrantBreakglass, v1alpha1.RoleNodes, nil
		}, "go together"},
		{"nodes with a namespace", func(g *v1alpha1.AccessGrant) { g.Spec.Kube.Role = v1alpha1.RoleNodes }, "cluster-wide"},
		{"workloads with no namespace", func(g *v1alpha1.AccessGrant) { g.Spec.Kube.Namespaces = nil }, "cluster-wide"},
		{"dev-agents", func(g *v1alpha1.AccessGrant) { g.Spec.Kube.Namespaces = []string{"frontend", "dev-agents"} }, "no grant reaches namespace dev-agents"},
		{"dev-env-system", func(g *v1alpha1.AccessGrant) { g.Spec.Kube.Namespaces = []string{"dev-env-system"} }, "no grant reaches"},
		{"dev-tools", func(g *v1alpha1.AccessGrant) { g.Spec.Kube.Namespaces = []string{"dev-tools"} }, "no grant reaches"},
		{"the broker's own namespace by another name", func(g *v1alpha1.AccessGrant) { g.Spec.Kube.Namespaces = []string{"broker-home"} }, "no grant reaches"},
		{"not a namespace name", func(g *v1alpha1.AccessGrant) { g.Spec.Kube.Namespaces = []string{"Frontend"} }, "not a namespace name"},
		{"ttl too short", func(g *v1alpha1.AccessGrant) { g.Spec.TTL.Duration = 9 * time.Minute }, "outside"},
		{"ttl too long", func(g *v1alpha1.AccessGrant) { g.Spec.TTL.Duration = 9 * time.Hour }, "outside"},
		{"ttl 8h", func(g *v1alpha1.AccessGrant) { g.Spec.TTL.Duration = 8 * time.Hour }, ""},
		{"break-glass over 1h", func(g *v1alpha1.AccessGrant) {
			g.Spec.Type, g.Spec.Kube.Role, g.Spec.Kube.Namespaces = v1alpha1.GrantBreakglass, v1alpha1.RoleBreakglass, nil
			g.Spec.TTL.Duration = 61 * time.Minute
		}, "outside"},
		{"egress into a dev-env namespace", func(g *v1alpha1.AccessGrant) {
			g.Spec.Type, g.Spec.Kube = v1alpha1.GrantEgress, nil
			g.Spec.Egress = &v1alpha1.EgressGrant{Endpoints: []v1alpha1.EgressEndpoint{{Namespace: "dev-tools"}}, Ports: tcp(443)}
		}, "no grant reaches"},
		{"egress, not built yet", func(g *v1alpha1.AccessGrant) {
			g.Spec.Type, g.Spec.Kube = v1alpha1.GrantEgress, nil
			g.Spec.Egress = &v1alpha1.EgressGrant{CIDRs: []string{"192.168.40.21/32"}, Ports: tcp(443)}
		}, "not built yet"},
		{"credential over 4h", func(g *v1alpha1.AccessGrant) {
			g.Spec.Type, g.Spec.Kube = v1alpha1.GrantCredential, nil
			g.Spec.Credential = &v1alpha1.CredentialGrant{Name: v1alpha1.CredentialHWSSH}
			g.Spec.TTL.Duration = 5 * time.Hour
		}, "outside"},
		{"credential, not built yet", func(g *v1alpha1.AccessGrant) {
			g.Spec.Type, g.Spec.Kube = v1alpha1.GrantCredential, nil
			g.Spec.Credential = &v1alpha1.CredentialGrant{Name: v1alpha1.CredentialHWSSH}
		}, "not built yet"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := kubeGrant(v1alpha1.RoleWorkloads, "frontend")
			if c.edit != nil {
				c.edit(g)
			}
			err := check(g, "dev-agents", "broker-home")
			switch {
			case c.fails == "" && err != nil:
				t.Errorf("refused: %v", err)
			case c.fails != "" && (err == nil || !strings.Contains(err.Error(), c.fails)):
				t.Errorf("error %v, want one with %q", err, c.fails)
			}
		})
	}
	g := kubeGrant(v1alpha1.RoleWorkloads, "frontend")
	g.Spec.Type, g.Spec.Kube = v1alpha1.GrantCredential, nil
	g.Spec.Credential = &v1alpha1.CredentialGrant{Name: v1alpha1.CredentialProxmox}
	if err := check(g); !errors.Is(err, errNotBuilt) {
		t.Errorf("a credential grant: %v, want errNotBuilt", err)
	}
}

func TestTokenSeconds(t *testing.T) {
	for _, c := range []struct {
		left time.Duration
		want int64
	}{
		{0, MinTokenSeconds},
		{-time.Minute, MinTokenSeconds},
		{9*time.Minute + 59*time.Second, MinTokenSeconds},
		{10*time.Minute + 500*time.Millisecond, 601},
		{time.Hour, 3600},
		{3607 * time.Second, 3608}, // never the kubelet's extended expiry
		{3606*time.Second + time.Millisecond, 3608},
		{8 * time.Hour, 8 * 3600},
	} {
		if got := tokenSeconds(c.left); got != c.want {
			t.Errorf("tokenSeconds(%s) = %d, want %d", c.left, got, c.want)
		}
	}
}
