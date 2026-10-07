package broker

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

func kubeGrant(role string, ns ...string) *v1alpha1.AccessGrant {
	return &v1alpha1.AccessGrant{
		ObjectMeta: metav1.ObjectMeta{Name: "grant-1007-120000-abcd", Namespace: "dev-agents"},
		Spec: v1alpha1.AccessGrantSpec{
			Requester: v1alpha1.GrantRequester{Session: "s1", Repo: "haynesnetwork", Profile: "full", Agent: v1alpha1.AgentClaude},
			Type:      v1alpha1.GrantKube,
			Kube:      &v1alpha1.KubeGrant{Role: role, Namespaces: ns},
			TTL:       metav1.Duration{Duration: time.Hour},
			Reason:    "test",
		},
	}
}

func egressGrant(e v1alpha1.EgressGrant) *v1alpha1.AccessGrant {
	g := kubeGrant("")
	g.Spec.Type, g.Spec.Kube, g.Spec.Egress = v1alpha1.GrantEgress, nil, &e
	return g
}

func kubePolicy(name string, roles []string, ns ...string) v1alpha1.GrantPolicy {
	return v1alpha1.GrantPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "dev-env-system"},
		Spec: v1alpha1.GrantPolicySpec{
			Description: "test",
			Profiles:    []string{"full"},
			Type:        v1alpha1.GrantKube,
			Kube:        &v1alpha1.KubePolicy{Roles: roles, Namespaces: ns},
			MaxTTL:      metav1.Duration{Duration: 2 * time.Hour},
		},
	}
}

func egressPolicy(e v1alpha1.EgressPolicy) v1alpha1.GrantPolicy {
	p := kubePolicy("egress", nil)
	p.Spec.Type, p.Spec.Kube, p.Spec.Egress = v1alpha1.GrantEgress, nil, &e
	return p
}

func tcp(ports ...int32) []v1alpha1.GrantPort {
	out := make([]v1alpha1.GrantPort, 0, len(ports))
	for _, p := range ports {
		out = append(out, v1alpha1.GrantPort{Port: p})
	}
	return out
}

func TestMatchesKube(t *testing.T) {
	workloads := []string{v1alpha1.RoleWorkloads}
	cases := []struct {
		name   string
		grant  func(*v1alpha1.AccessGrant)
		policy func(*v1alpha1.GrantPolicy)
		want   bool
	}{
		{"inside", nil, nil, true},
		{"a namespace outside", func(g *v1alpha1.AccessGrant) { g.Spec.Kube.Namespaces = []string{"frontend", "media"} }, nil, false},
		{"a role outside", func(g *v1alpha1.AccessGrant) { g.Spec.Kube.Role = v1alpha1.RoleStorage }, nil, false},
		{"type differs", nil, func(p *v1alpha1.GrantPolicy) { p.Spec.Type = v1alpha1.GrantEgress }, false},
		{"ttl above maxTTL", func(g *v1alpha1.AccessGrant) { g.Spec.TTL.Duration = 3 * time.Hour }, nil, false},
		{"ttl at maxTTL", func(g *v1alpha1.AccessGrant) { g.Spec.TTL.Duration = 2 * time.Hour }, nil, true},
		{"profile ops, never", func(g *v1alpha1.AccessGrant) { g.Spec.Requester.Profile = ProfileOps }, func(p *v1alpha1.GrantPolicy) {
			p.Spec.Profiles = []string{ProfileOps, "full"}
		}, false},
		{"profile ops matched by repo, never", func(g *v1alpha1.AccessGrant) { g.Spec.Requester.Profile = ProfileOps }, func(p *v1alpha1.GrantPolicy) {
			p.Spec.Profiles, p.Spec.Repos = nil, []string{"haynesnetwork"}
		}, false},
		{"profile not listed", func(g *v1alpha1.AccessGrant) { g.Spec.Requester.Profile = "lean" }, nil, false},
		{"repos listed and matching", nil, func(p *v1alpha1.GrantPolicy) { p.Spec.Repos = []string{"haynes-ops", "haynesnetwork"} }, true},
		{"repos listed and not matching", nil, func(p *v1alpha1.GrantPolicy) { p.Spec.Repos = []string{"haynes-ops"} }, false},
		{"repos only", nil, func(p *v1alpha1.GrantPolicy) { p.Spec.Profiles, p.Spec.Repos = nil, []string{"haynesnetwork"} }, true},
		{"no requester list approves nobody", nil, func(p *v1alpha1.GrantPolicy) { p.Spec.Profiles = nil }, false},
		{"agents listed and matching", nil, func(p *v1alpha1.GrantPolicy) { p.Spec.Agents = []v1alpha1.AgentKind{v1alpha1.AgentClaude} }, true},
		{"agents listed and not matching", nil, func(p *v1alpha1.GrantPolicy) { p.Spec.Agents = []v1alpha1.AgentKind{v1alpha1.AgentCodex} }, false},
		{"break-glass, never", func(g *v1alpha1.AccessGrant) {
			g.Spec.Type, g.Spec.Kube.Role, g.Spec.Kube.Namespaces = v1alpha1.GrantBreakglass, v1alpha1.RoleBreakglass, nil
		}, func(p *v1alpha1.GrantPolicy) {
			// A policy the schema refuses, shaped to match.
			p.Spec.Type, p.Spec.Kube.Roles = v1alpha1.GrantBreakglass, []string{v1alpha1.RoleBreakglass}
		}, false},
		{"break-glass role under type kube, never", func(g *v1alpha1.AccessGrant) {
			g.Spec.Kube.Role, g.Spec.Kube.Namespaces = v1alpha1.RoleBreakglass, nil
		}, func(p *v1alpha1.GrantPolicy) { p.Spec.Kube.Roles = []string{v1alpha1.RoleBreakglass} }, false},
		{"secrets-read, never", func(g *v1alpha1.AccessGrant) { g.Spec.Kube.Role = v1alpha1.RoleSecretsRead }, func(p *v1alpha1.GrantPolicy) {
			p.Spec.Kube.Roles = []string{v1alpha1.RoleSecretsRead}
		}, false},
		{"cluster-wide nodes", func(g *v1alpha1.AccessGrant) { g.Spec.Kube.Role, g.Spec.Kube.Namespaces = v1alpha1.RoleNodes, nil }, func(p *v1alpha1.GrantPolicy) {
			p.Spec.Kube.Roles, p.Spec.Kube.Namespaces = []string{v1alpha1.RoleNodes}, nil
		}, true},
		{"cluster-wide role with namespaces", func(g *v1alpha1.AccessGrant) { g.Spec.Kube.Role = v1alpha1.RoleNodes }, func(p *v1alpha1.GrantPolicy) {
			p.Spec.Kube.Roles = []string{v1alpha1.RoleNodes}
		}, false},
		{"namespaced role with no namespaces", func(g *v1alpha1.AccessGrant) { g.Spec.Kube.Namespaces = nil }, nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := kubeGrant(v1alpha1.RoleWorkloads, "frontend")
			p := kubePolicy("p", workloads, "frontend", "home-automation")
			if c.grant != nil {
				c.grant(g)
			}
			if c.policy != nil {
				c.policy(&p)
			}
			if got := Matches(&p, g); got != c.want {
				t.Errorf("Matches = %v, want %v", got, c.want)
			}
		})
	}
}

func TestMatchesEgress(t *testing.T) {
	policy := v1alpha1.EgressPolicy{
		FQDNs: []string{"api.example.com", "*.example.org"},
		CIDRs: []string{"192.168.40.0/24", "fd00::/8"},
		Endpoints: []v1alpha1.EgressEndpoint{
			{Namespace: "home-automation", MatchLabels: map[string]v1alpha1.LabelValue{"app": "z2m"}},
			{Namespace: "media"},
		},
		Ports: []v1alpha1.GrantPort{{Port: 443}, {Port: 53, Protocol: v1alpha1.ProtocolUDP}, {Port: 8080, Protocol: v1alpha1.ProtocolTCP}},
	}
	cases := []struct {
		name  string
		grant v1alpha1.EgressGrant
		want  bool
	}{
		{"exact fqdn", v1alpha1.EgressGrant{FQDNs: []string{"api.example.com"}, Ports: tcp(443)}, true},
		{"another name", v1alpha1.EgressGrant{FQDNs: []string{"www.example.com"}, Ports: tcp(443)}, false},
		{"under a wildcard", v1alpha1.EgressGrant{FQDNs: []string{"a.b.example.org"}, Ports: tcp(443)}, true},
		{"a wildcard under a wildcard", v1alpha1.EgressGrant{FQDNs: []string{"*.a.example.org"}, Ports: tcp(443)}, true},
		{"the same wildcard", v1alpha1.EgressGrant{FQDNs: []string{"*.example.org"}, Ports: tcp(443)}, true},
		{"the wildcard's apex", v1alpha1.EgressGrant{FQDNs: []string{"example.org"}, Ports: tcp(443)}, false},
		{"a look-alike suffix", v1alpha1.EgressGrant{FQDNs: []string{"badexample.org"}, Ports: tcp(443)}, false},
		{"a wider wildcard", v1alpha1.EgressGrant{FQDNs: []string{"*.org"}, Ports: tcp(443)}, false},
		{"a wildcard over an exact name", v1alpha1.EgressGrant{FQDNs: []string{"*.api.example.com"}, Ports: tcp(443)}, false},
		{"one host inside", v1alpha1.EgressGrant{CIDRs: []string{"192.168.40.21/32"}, Ports: tcp(443)}, true},
		{"the whole range", v1alpha1.EgressGrant{CIDRs: []string{"192.168.40.0/24"}, Ports: tcp(443)}, true},
		{"a wider range", v1alpha1.EgressGrant{CIDRs: []string{"192.168.0.0/16"}, Ports: tcp(443)}, false},
		{"a range outside", v1alpha1.EgressGrant{CIDRs: []string{"192.168.41.0/28"}, Ports: tcp(443)}, false},
		{"an unmasked range inside", v1alpha1.EgressGrant{CIDRs: []string{"192.168.40.77/28"}, Ports: tcp(443)}, true},
		{"IPv6 inside", v1alpha1.EgressGrant{CIDRs: []string{"fd12::1/128"}, Ports: tcp(443)}, true},
		{"IPv4-mapped IPv6 is not IPv4", v1alpha1.EgressGrant{CIDRs: []string{"::ffff:192.168.40.21/128"}, Ports: tcp(443)}, false},
		{"not a CIDR", v1alpha1.EgressGrant{CIDRs: []string{"192.168.40.21"}, Ports: tcp(443)}, false},
		{"an endpoint with the policy's labels and more", v1alpha1.EgressGrant{Endpoints: []v1alpha1.EgressEndpoint{
			{Namespace: "home-automation", MatchLabels: map[string]v1alpha1.LabelValue{"app": "z2m", "tier": "web"}},
		}, Ports: tcp(8080)}, true},
		{"an endpoint wider than the policy's", v1alpha1.EgressGrant{Endpoints: []v1alpha1.EgressEndpoint{
			{Namespace: "home-automation"},
		}, Ports: tcp(8080)}, false},
		{"an endpoint with another label value", v1alpha1.EgressGrant{Endpoints: []v1alpha1.EgressEndpoint{
			{Namespace: "home-automation", MatchLabels: map[string]v1alpha1.LabelValue{"app": "mqtt"}},
		}, Ports: tcp(8080)}, false},
		{"any endpoint of a whole namespace", v1alpha1.EgressGrant{Endpoints: []v1alpha1.EgressEndpoint{
			{Namespace: "media", MatchLabels: map[string]v1alpha1.LabelValue{"app": "plex"}},
		}, Ports: tcp(8080)}, true},
		{"an endpoint in another namespace", v1alpha1.EgressGrant{Endpoints: []v1alpha1.EgressEndpoint{{Namespace: "frontend"}}, Ports: tcp(8080)}, false},
		{"UDP 53", v1alpha1.EgressGrant{CIDRs: []string{"192.168.40.1/32"}, Ports: []v1alpha1.GrantPort{{Port: 53, Protocol: v1alpha1.ProtocolUDP}}}, true},
		{"TCP 53 is not UDP 53", v1alpha1.EgressGrant{CIDRs: []string{"192.168.40.1/32"}, Ports: tcp(53)}, false},
		{"UDP 443 is not TCP 443", v1alpha1.EgressGrant{CIDRs: []string{"192.168.40.1/32"}, Ports: []v1alpha1.GrantPort{{Port: 443, Protocol: v1alpha1.ProtocolUDP}}}, false},
		{"one port outside", v1alpha1.EgressGrant{FQDNs: []string{"api.example.com"}, Ports: tcp(443, 22)}, false},
		{"one destination outside", v1alpha1.EgressGrant{FQDNs: []string{"api.example.com", "evil.example.net"}, Ports: tcp(443)}, false},
		{"no destination", v1alpha1.EgressGrant{Ports: tcp(443)}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := egressPolicy(policy)
			if got := Matches(&p, egressGrant(c.grant)); got != c.want {
				t.Errorf("Matches = %v, want %v", got, c.want)
			}
		})
	}
}

func TestMatchesCredential(t *testing.T) {
	g := kubeGrant("")
	g.Spec.Type, g.Spec.Kube, g.Spec.Credential = v1alpha1.GrantCredential, nil, &v1alpha1.CredentialGrant{Name: v1alpha1.CredentialHWSSH}
	p := kubePolicy("cred", nil)
	p.Spec.Type, p.Spec.Kube, p.Spec.Credential = v1alpha1.GrantCredential, nil, &v1alpha1.CredentialPolicy{Names: []v1alpha1.CredentialName{v1alpha1.CredentialHWSSH}}
	if !Matches(&p, g) {
		t.Error("hw-ssh under an hw-ssh policy")
	}
	p.Spec.Credential.Names = []v1alpha1.CredentialName{v1alpha1.CredentialProxmox}
	if Matches(&p, g) {
		t.Error("hw-ssh under a proxmox policy")
	}
}

func TestMatchFirstInNameOrder(t *testing.T) {
	g := kubeGrant(v1alpha1.RoleWorkloads, "frontend")
	wl := []string{v1alpha1.RoleWorkloads}
	deleting := kubePolicy("a-deleting", wl, "frontend")
	deleting.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	policies := []v1alpha1.GrantPolicy{
		kubePolicy("zeta", wl, "frontend"),
		kubePolicy("beta", wl, "frontend"),
		kubePolicy("alpha", wl, "media"),
		deleting,
	}
	if p := Match(g, policies); p == nil || p.Name != "beta" {
		t.Errorf("Match = %v, want beta", p)
	}
	if p := Match(kubeGrant(v1alpha1.RoleWorkloads, "kube-system"), policies); p != nil {
		t.Errorf("Match = %s, want none", p.Name)
	}
	if policies[0].Name != "zeta" {
		t.Error("Match reordered its argument")
	}
}
