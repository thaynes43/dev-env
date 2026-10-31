package broker

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// namespaceOperatorGrant is DESIGN-042 A2's request: the namespace operator in
// downloads with the two Secrets the Prowlarr cookie Job uses.
func namespaceOperatorGrant(secrets ...string) *v1alpha1.AccessGrant {
	g := kubeGrant(v1alpha1.RoleNamespaceOperator, "downloads")
	g.Spec.Kube.Secrets = secrets
	return g
}

// namespaceOperatorPolicy is an attested kube-namespace-operator policy for
// full sessions, naming its namespaces and Secrets.
func namespaceOperatorPolicy(name string, secrets ...string) v1alpha1.GrantPolicy {
	p := kubePolicy(name, []string{v1alpha1.RoleNamespaceOperator}, "downloads", "network")
	p.Spec.Profiles = nil
	p.Spec.Attested = &v1alpha1.AttestedRequesters{Profiles: []string{"full", "workbench"}}
	p.Spec.Kube.Secrets = secrets
	return p
}

func TestNamespaceOperatorIsANamespacedCatalogRole(t *testing.T) {
	if !slices.Contains(catalog, v1alpha1.RoleNamespaceOperator) {
		t.Fatal("the broker's catalog does not hold the namespace operator, so it would refuse to bind it")
	}
	if v1alpha1.ClusterWideRole(v1alpha1.RoleNamespaceOperator) {
		t.Fatal("the namespace operator is bound per namespace, never cluster-wide")
	}
}

func TestCheckNamedSecrets(t *testing.T) {
	seventeen := make([]string, 17)
	for i := range seventeen {
		seventeen[i] = fmt.Sprintf("app-%d-secret", i)
	}
	cases := []struct {
		name  string
		grant *v1alpha1.AccessGrant
		fails string // a part of the error; "" passes
	}{
		{"named Secrets", namespaceOperatorGrant("prowlarr-secret", "myanonamouse-secret"), ""},
		{"no Secret", namespaceOperatorGrant(), ""},
		{"a dotted Secret name", namespaceOperatorGrant("sh.helm.release.v1.prowlarr.v3"), ""},
		{"sixteen Secrets", namespaceOperatorGrant(seventeen[:16]...), ""},
		{"seventeen Secrets", namespaceOperatorGrant(seventeen...), "more than 16"},
		{"a wildcard", namespaceOperatorGrant("*"), "not a Secret name"},
		{"a pattern", namespaceOperatorGrant("prowlarr-*"), "not a Secret name"},
		{"a namespace-qualified name", namespaceOperatorGrant("downloads/prowlarr-secret"), "not a Secret name"},
		{"an upper-case name", namespaceOperatorGrant("Prowlarr"), "not a Secret name"},
		{"an empty name", namespaceOperatorGrant(""), "not a Secret name"},
		{"a name twice", namespaceOperatorGrant("prowlarr-secret", "prowlarr-secret"), "named twice"},
		{"in dev-agents", func() *v1alpha1.AccessGrant {
			g := namespaceOperatorGrant("prowlarr-secret")
			g.Spec.Kube.Namespaces = []string{"dev-agents"}
			return g
		}(), "no grant reaches namespace dev-agents"},
		{"with no namespace", func() *v1alpha1.AccessGrant {
			g := namespaceOperatorGrant("prowlarr-secret")
			g.Spec.Kube.Namespaces = nil
			return g
		}(), "namespaced and names at least one"},
		{"Secrets on workloads", func() *v1alpha1.AccessGrant {
			g := kubeGrant(v1alpha1.RoleWorkloads, "downloads")
			g.Spec.Kube.Secrets = []string{"prowlarr-secret"}
			return g
		}(), "names no secrets"},
		{"Secrets on secrets-read", func() *v1alpha1.AccessGrant {
			g := kubeGrant(v1alpha1.RoleSecretsRead, "downloads")
			g.Spec.Kube.Secrets = []string{"prowlarr-secret"}
			return g
		}(), "names no secrets"},
		{"Secrets on nodes", func() *v1alpha1.AccessGrant {
			g := kubeGrant(v1alpha1.RoleNodes)
			g.Spec.Kube.Secrets = []string{"prowlarr-secret"}
			return g
		}(), "names no secrets"},
		{"Secrets on break-glass", func() *v1alpha1.AccessGrant {
			g := kubeGrant(v1alpha1.RoleBreakglass)
			g.Spec.Type, g.Spec.Kube.Secrets = v1alpha1.GrantBreakglass, []string{"prowlarr-secret"}
			return g
		}(), "names no secrets"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := check(c.grant, "broker-home")
			switch {
			case c.fails == "" && err != nil:
				t.Fatalf("check refused it: %v", err)
			case c.fails != "" && (err == nil || !strings.Contains(err.Error(), c.fails)):
				t.Fatalf("check = %v, want an error containing %q", err, c.fails)
			}
		})
	}
}

// No standing policy approves the namespace operator, and a standing policy
// that names it approves nothing at all, whatever else it names: the schema
// refuses such a policy, and the broker does not trust that it ran.
func TestMatchesNeverApprovesTheNamespaceOperatorStanding(t *testing.T) {
	standing := kubePolicy("standing", []string{v1alpha1.RoleNamespaceOperator}, "downloads")
	standing.Spec.Kube.Secrets = []string{"prowlarr-secret"}
	if Matches(&standing, namespaceOperatorGrant("prowlarr-secret")) {
		t.Error("a standing policy approved the namespace operator")
	}
	if Matches(&standing, namespaceOperatorGrant()) {
		t.Error("a standing policy approved the namespace operator without Secrets")
	}
	mixed := kubePolicy("mixed", []string{v1alpha1.RoleWorkloads, v1alpha1.RoleNamespaceOperator}, "downloads")
	mixed.Spec.Kube.Secrets = []string{"prowlarr-secret"}
	if Matches(&mixed, kubeGrant(v1alpha1.RoleWorkloads, "downloads")) {
		t.Error("a standing policy that names the namespace operator approved a workloads grant")
	}
	secretsOnly := kubePolicy("secrets-only", []string{v1alpha1.RoleWorkloads}, "downloads")
	secretsOnly.Spec.Kube.Secrets = []string{"prowlarr-secret"}
	if Matches(&secretsOnly, kubeGrant(v1alpha1.RoleWorkloads, "downloads")) {
		t.Error("a standing policy with Secrets but no namespace operator approved a workloads grant")
	}
	smuggled := kubeGrant(v1alpha1.RoleWorkloads, "downloads")
	smuggled.Spec.Kube.Secrets = []string{"prowlarr-secret"}
	plain := kubePolicy("plain", []string{v1alpha1.RoleWorkloads}, "downloads")
	if Matches(&plain, smuggled) {
		t.Error("a standing workloads policy approved a workloads grant that names Secrets")
	}
	// The attested policy is never a standing one either.
	attested := namespaceOperatorPolicy("attested", "prowlarr-secret")
	if Matches(&attested, namespaceOperatorGrant("prowlarr-secret")) {
		t.Error("Matches approved through an attested policy")
	}
}

// An attested policy fits a namespace-operator grant when it names the role,
// every granted namespace and every granted Secret.
func TestMatchesAttestedNamedSecrets(t *testing.T) {
	both := []string{"myanonamouse-secret", "prowlarr-secret"}
	cases := []struct {
		name   string
		grant  *v1alpha1.AccessGrant
		policy v1alpha1.GrantPolicy
		want   bool
	}{
		{"the named Secrets", namespaceOperatorGrant("prowlarr-secret", "myanonamouse-secret"), namespaceOperatorPolicy("p", both...), true},
		{"a subset of them", namespaceOperatorGrant("prowlarr-secret"), namespaceOperatorPolicy("p", both...), true},
		{"no Secret, under a policy that names some", namespaceOperatorGrant(), namespaceOperatorPolicy("p", both...), true},
		{"a Secret the policy does not name", namespaceOperatorGrant("prowlarr-secret", "qbittorrent-secret"), namespaceOperatorPolicy("p", both...), false},
		{"a policy that names no Secret", namespaceOperatorGrant(), namespaceOperatorPolicy("p"), false},
		{"a prefix is not a match", namespaceOperatorGrant("prowlarr"), namespaceOperatorPolicy("p", both...), false},
		{"a namespace the policy does not name", func() *v1alpha1.AccessGrant {
			g := namespaceOperatorGrant("prowlarr-secret")
			g.Spec.Kube.Namespaces = []string{"downloads", "media"}
			return g
		}(), namespaceOperatorPolicy("p", both...), false},
		{"another role's policy with the same names", namespaceOperatorGrant("prowlarr-secret"), func() v1alpha1.GrantPolicy {
			p := namespaceOperatorPolicy("p", both...)
			p.Spec.Kube.Roles = []string{v1alpha1.RoleWorkloads}
			return p
		}(), false},
		{"workloads under a policy naming both roles", kubeGrant(v1alpha1.RoleWorkloads, "downloads"), func() v1alpha1.GrantPolicy {
			p := namespaceOperatorPolicy("p", both...)
			p.Spec.Kube.Roles = []string{v1alpha1.RoleWorkloads, v1alpha1.RoleNamespaceOperator}
			return p
		}(), true},
		{"workloads that names Secrets", func() *v1alpha1.AccessGrant {
			g := kubeGrant(v1alpha1.RoleWorkloads, "downloads")
			g.Spec.Kube.Secrets = []string{"prowlarr-secret"}
			return g
		}(), func() v1alpha1.GrantPolicy {
			p := namespaceOperatorPolicy("p", both...)
			p.Spec.Kube.Roles = []string{v1alpha1.RoleWorkloads, v1alpha1.RoleNamespaceOperator}
			return p
		}(), false},
		{"a requester profile the policy does not name", func() *v1alpha1.AccessGrant {
			g := namespaceOperatorGrant("prowlarr-secret")
			g.Spec.Requester.Profile = ProfileOps
			return g
		}(), namespaceOperatorPolicy("p", both...), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := MatchesAttested(&c.policy, c.grant); got != c.want {
				t.Errorf("MatchesAttested = %v, want %v", got, c.want)
			}
		})
	}
}

// Inert until a policy names the role (DESIGN-042 S5): the standing and
// attested policies of today approve no namespace-operator grant, even with a
// verified attestation, and approve the grants they approved before.
func TestNamespaceOperatorInertWithoutAPolicy(t *testing.T) {
	wl := []string{v1alpha1.RoleWorkloads}
	nodes := kubePolicy("kube-nodes-attested", []string{v1alpha1.RoleNodes})
	nodes.Spec.Profiles, nodes.Spec.Attested = nil, &v1alpha1.AttestedRequesters{Profiles: []string{"full"}}
	attestedWorkloads := kubePolicy("kube-workloads-attested", wl, "downloads")
	attestedWorkloads.Spec.Profiles, attestedWorkloads.Spec.Attested = nil, &v1alpha1.AttestedRequesters{Profiles: []string{"full"}}
	today := []v1alpha1.GrantPolicy{kubePolicy("frontend-workloads", wl, "frontend", "downloads"), nodes, attestedWorkloads}
	verified := func(*v1alpha1.GrantPolicy) bool { return true }

	for _, g := range []*v1alpha1.AccessGrant{namespaceOperatorGrant("prowlarr-secret"), namespaceOperatorGrant()} {
		if p := Match(g, today); p != nil {
			t.Errorf("Match approved the namespace operator through %s", p.Name)
		}
		if p := MatchAttested(g, today, verified); p != nil {
			t.Errorf("MatchAttested approved the namespace operator through %s", p.Name)
		}
	}
	if p := Match(kubeGrant(v1alpha1.RoleWorkloads, "downloads"), today); p == nil || p.Name != "frontend-workloads" {
		t.Errorf("Match = %v, want frontend-workloads for a workloads grant, as before", p)
	}
	nodesGrant := kubeGrant(v1alpha1.RoleNodes)
	if p := MatchAttested(nodesGrant, today, verified); p == nil || p.Name != "kube-nodes-attested" {
		t.Errorf("MatchAttested = %v, want kube-nodes-attested for a nodes grant, as before", p)
	}
}
