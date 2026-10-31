package v1alpha1_test

// The envtest cases for DESIGN-042 5.1 (S5): a kube grant of role
// dev-env-grant-namespace-operator names the one to 16 Secrets its new pods
// and Jobs will use. A policy for the role names the namespaces and may deny
// Secrets; it does not list the ones it allows. No standing policy approves
// the role, and an attested one opts into mode (a) with allowSecretUse. The
// schema accepts these forms and refuses a wildcard, a pattern, Secrets on
// another role, and a standing policy for the role.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// namespaceOperatorGrant is DESIGN-042 A2's request: the namespace operator in
// downloads, with the two Secrets the Prowlarr cookie Job uses, for 30 minutes.
func namespaceOperatorGrant() *v1alpha1.AccessGrant {
	g := kubeGrant()
	g.Spec.Kube = &v1alpha1.KubeGrant{
		Role:       v1alpha1.RoleNamespaceOperator,
		Namespaces: []string{"downloads"},
		Secrets:    []string{"myanonamouse-secret", "prowlarr-secret"},
	}
	g.Spec.TTL = ttl(30 * time.Minute)
	g.Spec.Reason = "apply the saved MAM cookie to Prowlarr indexer 17; restore: re-run with the previous value"
	return g
}

// namespaceOperatorPolicy is DESIGN-042 9.1's kube-namespace-operator: it
// names the namespaces, opts into mode (a), and denies two kinds of Secret.
func namespaceOperatorPolicy() *v1alpha1.GrantPolicy {
	p := workloadsPolicy()
	p.Spec.Description = "agent-attested namespace operator in downloads and network; each grant names its Secrets"
	p.Spec.Repos = nil
	p.Spec.Attested = &v1alpha1.AttestedRequesters{Profiles: []string{"full", "workbench"}, AllowSecretUse: true}
	p.Spec.Kube = &v1alpha1.KubePolicy{
		Roles:         []string{v1alpha1.RoleNamespaceOperator},
		Namespaces:    []string{"downloads", "network"},
		DeniedSecrets: []string{"cloudflare-token", "sh.helm.release.*"},
	}
	p.Spec.MaxTTL = ttl(time.Hour)
	return p
}

func TestAccessGrantNamedSecretsAccepts(t *testing.T) {
	cases := []struct {
		title  string
		mutate func(*v1alpha1.AccessGrant)
	}{
		{"the namespace operator with two named Secrets", nil},
		{"the namespace operator with one Secret", func(g *v1alpha1.AccessGrant) { g.Spec.Kube.Secrets = []string{"prowlarr-secret"} }},
		{"a Secret name with dots (a DNS-1123 subdomain)", func(g *v1alpha1.AccessGrant) {
			g.Spec.Kube.Secrets = []string{"sh.helm.release.v1.prowlarr.v3"}
		}},
		{"sixteen Secrets in two namespaces", func(g *v1alpha1.AccessGrant) {
			g.Spec.Kube.Namespaces = []string{"downloads", "media"}
			g.Spec.Kube.Secrets = nil
			for i := range 16 {
				g.Spec.Kube.Secrets = append(g.Spec.Kube.Secrets, fmt.Sprintf("app-%d-secret", i))
			}
		}},
		{"a 253-character Secret name", func(g *v1alpha1.AccessGrant) {
			g.Spec.Kube.Secrets = []string{strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 61)}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.title, func(t *testing.T) {
			g := namespaceOperatorGrant()
			if tc.mutate != nil {
				tc.mutate(g)
			}
			if err := k8s.Create(ctx(t), g); err != nil {
				t.Fatalf("create: %v", err)
			}
		})
	}
}

func TestAccessGrantNamedSecretsRejects(t *testing.T) {
	const onlyRole = "dev-env-grant-namespace-operator names the Secrets it will use, one to 16, and only that role takes secrets"
	cases := []struct {
		title  string
		create func() *v1alpha1.AccessGrant
		mutate func(*v1alpha1.AccessGrant)
		want   string
	}{
		{"a wildcard Secret", namespaceOperatorGrant, func(g *v1alpha1.AccessGrant) { g.Spec.Kube.Secrets = []string{"*"} }, "spec.kube.secrets[0]"},
		{"a Secret pattern", namespaceOperatorGrant, func(g *v1alpha1.AccessGrant) {
			g.Spec.Kube.Secrets = []string{"prowlarr-secret", "prowlarr-*"}
		}, "spec.kube.secrets[1]"},
		{"a regular expression", namespaceOperatorGrant, func(g *v1alpha1.AccessGrant) { g.Spec.Kube.Secrets = []string{".*"} }, "spec.kube.secrets[0]"},
		{"an upper-case Secret name", namespaceOperatorGrant, func(g *v1alpha1.AccessGrant) { g.Spec.Kube.Secrets = []string{"Prowlarr"} }, "spec.kube.secrets[0]"},
		{"a namespace-qualified Secret", namespaceOperatorGrant, func(g *v1alpha1.AccessGrant) {
			g.Spec.Kube.Secrets = []string{"downloads/prowlarr-secret"}
		}, "spec.kube.secrets[0]"},
		{"an empty Secret name", namespaceOperatorGrant, func(g *v1alpha1.AccessGrant) { g.Spec.Kube.Secrets = []string{""} }, "spec.kube.secrets[0]"},
		{"the namespace operator with no Secret", namespaceOperatorGrant, func(g *v1alpha1.AccessGrant) { g.Spec.Kube.Secrets = nil }, onlyRole},
		{"the namespace operator with an empty list", namespaceOperatorGrant, func(g *v1alpha1.AccessGrant) { g.Spec.Kube.Secrets = []string{} }, onlyRole},
		{"a 254-character Secret name", namespaceOperatorGrant, func(g *v1alpha1.AccessGrant) {
			g.Spec.Kube.Secrets = []string{strings.Repeat("a", 254)}
		}, "spec.kube.secrets[0]"},
		{"seventeen Secrets", namespaceOperatorGrant, func(g *v1alpha1.AccessGrant) {
			g.Spec.Kube.Secrets = nil
			for i := range 17 {
				g.Spec.Kube.Secrets = append(g.Spec.Kube.Secrets, fmt.Sprintf("app-%d-secret", i))
			}
		}, "spec.kube.secrets"},
		{"a Secret named twice", namespaceOperatorGrant, func(g *v1alpha1.AccessGrant) {
			g.Spec.Kube.Secrets = []string{"prowlarr-secret", "prowlarr-secret"}
		}, "spec.kube.secrets[1]"},
		{"the namespace operator in dev-agents", namespaceOperatorGrant, func(g *v1alpha1.AccessGrant) {
			g.Spec.Kube.Namespaces = []string{"dev-agents"}
		}, "no grant reaches the dev-env namespaces"},
		{"the namespace operator with no namespace", namespaceOperatorGrant, func(g *v1alpha1.AccessGrant) {
			g.Spec.Kube.Namespaces = nil
		}, "the other roles name at least one"},
		{"Secrets on a workloads grant", kubeGrant, func(g *v1alpha1.AccessGrant) {
			g.Spec.Kube.Secrets = []string{"prowlarr-secret"}
		}, onlyRole},
		{"Secrets on a secrets-read grant", kubeGrant, func(g *v1alpha1.AccessGrant) {
			g.Spec.Kube = &v1alpha1.KubeGrant{Role: v1alpha1.RoleSecretsRead, Namespaces: []string{"downloads"}, Secrets: []string{"prowlarr-secret"}}
		}, onlyRole},
		{"Secrets on a nodes grant", kubeGrant, func(g *v1alpha1.AccessGrant) {
			g.Spec.Kube = &v1alpha1.KubeGrant{Role: v1alpha1.RoleNodes, Secrets: []string{"prowlarr-secret"}}
		}, onlyRole},
		{"Secrets on break-glass", breakglassGrant, func(g *v1alpha1.AccessGrant) {
			g.Spec.Kube.Secrets = []string{"prowlarr-secret"}
		}, onlyRole},
	}
	for _, tc := range cases {
		t.Run(tc.title, func(t *testing.T) {
			g := tc.create()
			tc.mutate(g)
			wantInvalid(t, k8s.Create(ctx(t), g), tc.want)
		})
	}
}

// A grant's Secrets are part of its immutable kube scope.
func TestAccessGrantNamedSecretsImmutable(t *testing.T) {
	for title, mutate := range map[string]func(*v1alpha1.KubeGrant){
		"add a Secret":    func(k *v1alpha1.KubeGrant) { k.Secrets = append(k.Secrets, "qbittorrent-secret") },
		"remove a Secret": func(k *v1alpha1.KubeGrant) { k.Secrets = k.Secrets[:1] },
		"rename a Secret": func(k *v1alpha1.KubeGrant) { k.Secrets[0] = "other-secret" },
	} {
		t.Run(title, func(t *testing.T) {
			g := namespaceOperatorGrant()
			if err := k8s.Create(ctx(t), g); err != nil {
				t.Fatalf("create: %v", err)
			}
			mutate(g.Spec.Kube)
			wantInvalid(t, k8s.Update(ctx(t), g), "immutable after create; only release may change")
		})
	}
}

func TestGrantPolicyNamedSecretsAccepts(t *testing.T) {
	cases := []struct {
		title  string
		mutate func(*v1alpha1.GrantPolicy)
	}{
		{"kube-namespace-operator, attested, opted in, with a deny list", nil},
		{"delegated-only namespace operator", func(p *v1alpha1.GrantPolicy) {
			p.Spec.Attested = nil
			p.Spec.Delegated = &v1alpha1.DelegatedRequesters{Profiles: []string{"full", "workbench"}}
		}},
		{"both approval paths with no attested Secret opt-in", func(p *v1alpha1.GrantPolicy) {
			p.Spec.Attested.AllowSecretUse = false
			p.Spec.Delegated = &v1alpha1.DelegatedRequesters{Profiles: []string{"full"}}
		}},
		{"without a deny list", func(p *v1alpha1.GrantPolicy) { p.Spec.Kube.DeniedSecrets = nil }},
		{"without the mode (a) opt-in (delegate decisions only)", func(p *v1alpha1.GrantPolicy) { p.Spec.Attested.AllowSecretUse = false }},
		{"the namespace operator beside workloads in one attested policy", func(p *v1alpha1.GrantPolicy) {
			p.Spec.Kube.Roles = []string{v1alpha1.RoleWorkloads, v1alpha1.RoleNamespaceOperator}
		}},
		{"thirty-two deny entries", func(p *v1alpha1.GrantPolicy) {
			p.Spec.Kube.DeniedSecrets = nil
			for i := range 32 {
				p.Spec.Kube.DeniedSecrets = append(p.Spec.Kube.DeniedSecrets, fmt.Sprintf("app-%d-*", i))
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.title, func(t *testing.T) {
			p := namespaceOperatorPolicy()
			if tc.mutate != nil {
				tc.mutate(p)
			}
			if err := k8s.Create(ctx(t), p); err != nil {
				t.Fatalf("create: %v", err)
			}
		})
	}
}

func TestGrantPolicyNamedSecretsRejects(t *testing.T) {
	const denies = "only a policy for dev-env-grant-namespace-operator takes deniedSecrets"
	const optIn = "attested.allowSecretUse is only for a policy that names dev-env-grant-namespace-operator"
	cases := []struct {
		title  string
		create func() *v1alpha1.GrantPolicy
		mutate func(*v1alpha1.GrantPolicy)
		want   string
	}{
		{"a standing policy for the namespace operator", namespaceOperatorPolicy, func(p *v1alpha1.GrantPolicy) {
			p.Spec.Attested = nil
			p.Spec.Profiles = []string{"full"}
		}, "no standing policy approves dev-env-grant-namespace-operator"},
		{"a standing policy naming it beside workloads", namespaceOperatorPolicy, func(p *v1alpha1.GrantPolicy) {
			p.Spec.Attested = nil
			p.Spec.Repos = []string{"haynes-ops"}
			p.Spec.Kube.Roles = []string{v1alpha1.RoleWorkloads, v1alpha1.RoleNamespaceOperator}
		}, "no standing policy approves dev-env-grant-namespace-operator"},
		{"the namespace operator without a namespace", namespaceOperatorPolicy, func(p *v1alpha1.GrantPolicy) {
			p.Spec.Kube.Namespaces = nil
		}, "name the namespaces, one by one"},
		{"a deny list on a workloads policy", workloadsPolicy, func(p *v1alpha1.GrantPolicy) {
			p.Spec.Kube.DeniedSecrets = []string{"prowlarr-secret"}
		}, denies},
		{"a deny list on an attested nodes policy", workloadsPolicy, func(p *v1alpha1.GrantPolicy) {
			p.Spec.Repos = nil
			p.Spec.Attested = &v1alpha1.AttestedRequesters{Profiles: []string{"full"}}
			p.Spec.Kube = &v1alpha1.KubePolicy{Roles: []string{v1alpha1.RoleNodes}, DeniedSecrets: []string{"prowlarr-secret"}}
		}, denies},
		{"the mode (a) opt-in on a workloads policy", workloadsPolicy, func(p *v1alpha1.GrantPolicy) {
			p.Spec.Repos = nil
			p.Spec.Attested = &v1alpha1.AttestedRequesters{Profiles: []string{"full"}, AllowSecretUse: true}
		}, optIn},
		{"the mode (a) opt-in on a credential policy", workloadsPolicy, func(p *v1alpha1.GrantPolicy) {
			p.Spec.Repos = nil
			p.Spec.Attested = &v1alpha1.AttestedRequesters{Profiles: []string{"full"}, AllowSecretUse: true}
			p.Spec.Type, p.Spec.Kube = v1alpha1.GrantCredential, nil
			p.Spec.Credential = &v1alpha1.CredentialPolicy{Names: []v1alpha1.CredentialName{v1alpha1.CredentialHWSSH}}
		}, optIn},
		{"a bare wildcard deny entry", namespaceOperatorPolicy, func(p *v1alpha1.GrantPolicy) { p.Spec.Kube.DeniedSecrets = []string{"*"} }, "spec.kube.deniedSecrets[0]"},
		{"a suffix deny entry", namespaceOperatorPolicy, func(p *v1alpha1.GrantPolicy) {
			p.Spec.Kube.DeniedSecrets = []string{"cloudflare-token", "*-tls"}
		}, "spec.kube.deniedSecrets[1]"},
		{"a regular-expression deny entry", namespaceOperatorPolicy, func(p *v1alpha1.GrantPolicy) {
			p.Spec.Kube.DeniedSecrets = []string{"app-.*-secret"}
		}, "spec.kube.deniedSecrets[0]"},
		{"thirty-three deny entries", namespaceOperatorPolicy, func(p *v1alpha1.GrantPolicy) {
			p.Spec.Kube.DeniedSecrets = nil
			for i := range 33 {
				p.Spec.Kube.DeniedSecrets = append(p.Spec.Kube.DeniedSecrets, fmt.Sprintf("app-%d-secret", i))
			}
		}, "spec.kube.deniedSecrets"},
		{"the namespace operator in dev-env-system", namespaceOperatorPolicy, func(p *v1alpha1.GrantPolicy) {
			p.Spec.Kube.Namespaces = []string{"downloads", "dev-env-system"}
		}, "no grant reaches the dev-env namespaces"},
	}
	for _, tc := range cases {
		t.Run(tc.title, func(t *testing.T) {
			p := tc.create()
			tc.mutate(p)
			wantInvalid(t, k8s.Create(ctx(t), p), tc.want)
		})
	}
}
