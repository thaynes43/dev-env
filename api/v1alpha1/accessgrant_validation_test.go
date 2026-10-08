package v1alpha1_test

// The envtest suite for the AccessGrant and GrantPolicy CRDs (D-54): the schema
// accepts the grants and policies DESIGN-001 6.12 allows and refuses what no
// grant or policy may ever do. It shares TestMain and the helpers with the
// AgentSession suite.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// grantName returns a fresh grant name, grant-<id>.
func grantName() string {
	return fmt.Sprintf("grant-t-%d", seq.Add(1))
}

func ttl(d time.Duration) metav1.Duration { return metav1.Duration{Duration: d} }

func requester() v1alpha1.GrantRequester {
	return v1alpha1.GrantRequester{
		Session: "haynes-ops-1007-171500",
		Repo:    "haynes-ops",
		Profile: "full",
		Agent:   v1alpha1.AgentClaude,
		Parent:  "dev-env-system/dev-env-human",
	}
}

// kubeGrant is DESIGN-001 6.12's example: workloads in home-automation for 1h.
func kubeGrant() *v1alpha1.AccessGrant {
	return &v1alpha1.AccessGrant{
		ObjectMeta: metav1.ObjectMeta{Name: grantName(), Namespace: ns},
		Spec: v1alpha1.AccessGrantSpec{
			Requester: requester(),
			Type:      v1alpha1.GrantKube,
			Kube:      &v1alpha1.KubeGrant{Role: v1alpha1.RoleWorkloads, Namespaces: []string{"home-automation"}},
			TTL:       ttl(time.Hour),
			Reason:    "Patch zigbee2mqtt Deployment image to test 2.12.1 before the PR",
		},
	}
}

func breakglassGrant() *v1alpha1.AccessGrant {
	g := kubeGrant()
	g.Spec.Type = v1alpha1.GrantBreakglass
	g.Spec.Kube = &v1alpha1.KubeGrant{Role: v1alpha1.RoleBreakglass}
	g.Spec.TTL = ttl(30 * time.Minute)
	return g
}

func egressGrant() *v1alpha1.AccessGrant {
	g := kubeGrant()
	g.Spec.Type = v1alpha1.GrantEgress
	g.Spec.Kube = nil
	g.Spec.Egress = &v1alpha1.EgressGrant{
		CIDRs: []string{"192.168.40.21/32"},
		Ports: []v1alpha1.GrantPort{{Port: 80}},
	}
	g.Spec.TTL = ttl(2 * time.Hour)
	return g
}

func credentialGrant() *v1alpha1.AccessGrant {
	g := kubeGrant()
	g.Spec.Type = v1alpha1.GrantCredential
	g.Spec.Kube = nil
	g.Spec.Credential = &v1alpha1.CredentialGrant{Name: v1alpha1.CredentialProxmox}
	g.Spec.TTL = ttl(time.Hour)
	return g
}

func TestAccessGrantAccepts(t *testing.T) {
	cases := []struct {
		title  string
		create func() *v1alpha1.AccessGrant
		mutate func(*v1alpha1.AccessGrant)
	}{
		{"workloads in one namespace for 1h", kubeGrant, nil},
		{"storage in two namespaces for 8h", kubeGrant, func(g *v1alpha1.AccessGrant) {
			g.Spec.Kube = &v1alpha1.KubeGrant{Role: v1alpha1.RoleStorage, Namespaces: []string{"database", "observability"}}
			g.Spec.TTL = ttl(8 * time.Hour)
		}},
		{"secrets-read (Tom approves it on the page)", kubeGrant, func(g *v1alpha1.AccessGrant) {
			g.Spec.Kube.Role = v1alpha1.RoleSecretsRead
		}},
		{"nodes, cluster-wide, for 10m", kubeGrant, func(g *v1alpha1.AccessGrant) {
			g.Spec.Kube = &v1alpha1.KubeGrant{Role: v1alpha1.RoleNodes}
			g.Spec.TTL = ttl(10 * time.Minute)
		}},
		{"break-glass for 1h", breakglassGrant, func(g *v1alpha1.AccessGrant) { g.Spec.TTL = ttl(time.Hour) }},
		{"egress to a LAN host", egressGrant, nil},
		{"egress by name, exact and wildcard", egressGrant, func(g *v1alpha1.AccessGrant) {
			g.Spec.Egress = &v1alpha1.EgressGrant{
				FQDNs: []string{"printer.example.com", "*.pypi.org"},
				Ports: []v1alpha1.GrantPort{{Port: 443}, {Port: 53, Protocol: v1alpha1.ProtocolUDP}},
			}
		}},
		{"egress to in-cluster pods", egressGrant, func(g *v1alpha1.AccessGrant) {
			g.Spec.Egress = &v1alpha1.EgressGrant{
				Endpoints: []v1alpha1.EgressEndpoint{{Namespace: "database", MatchLabels: map[string]v1alpha1.LabelValue{"cnpg.io/cluster": "postgres"}}},
				Ports:     []v1alpha1.GrantPort{{Port: 5432}},
			}
		}},
		{"an IPv6 range", egressGrant, func(g *v1alpha1.AccessGrant) { g.Spec.Egress.CIDRs = []string{"2001:db8::/64"} }},
		{"a Proxmox credential for 4h", credentialGrant, func(g *v1alpha1.AccessGrant) { g.Spec.TTL = ttl(4 * time.Hour) }},
		{"a credential with its authenticated session UID", credentialGrant, func(g *v1alpha1.AccessGrant) {
			g.Spec.Requester.SessionUID = "11111111-1111-1111-1111-111111111111"
		}},
		{"an hw-ssh credential", credentialGrant, func(g *v1alpha1.AccessGrant) {
			g.Spec.Credential.Name = v1alpha1.CredentialHWSSH
		}},
	}
	for _, tc := range cases {
		t.Run(tc.title, func(t *testing.T) {
			g := tc.create()
			if tc.mutate != nil {
				tc.mutate(g)
			}
			if err := k8s.Create(ctx(t), g); err != nil {
				t.Fatalf("create: %v", err)
			}
		})
	}
}

func TestAccessGrantDefaultsPortProtocol(t *testing.T) {
	g := egressGrant()
	if err := k8s.Create(ctx(t), g); err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := g.Spec.Egress.Ports[0].Protocol; got != v1alpha1.ProtocolTCP {
		t.Fatalf("protocol = %q, want TCP by default", got)
	}
}

func TestAccessGrantRejects(t *testing.T) {
	const devEnv = "no grant reaches the dev-env namespaces"
	cases := []struct {
		title  string
		create func() *v1alpha1.AccessGrant
		mutate func(*v1alpha1.AccessGrant)
		want   string
	}{
		// The name is the grant's ServiceAccount.
		{"a name without the grant- prefix", kubeGrant, func(g *v1alpha1.AccessGrant) { g.Name = "g-1007-1715" }, "metadata.name is grant-<id>"},
		{"a 64-character name", kubeGrant, func(g *v1alpha1.AccessGrant) { g.Name = "grant-" + strings.Repeat("a", 58) }, "at most 63 characters"},

		// No grant reaches the dev-env namespaces.
		{"workloads in dev-agents", kubeGrant, func(g *v1alpha1.AccessGrant) { g.Spec.Kube.Namespaces = []string{"dev-agents"} }, devEnv},
		{"storage in dev-env-system", kubeGrant, func(g *v1alpha1.AccessGrant) {
			g.Spec.Kube = &v1alpha1.KubeGrant{Role: v1alpha1.RoleStorage, Namespaces: []string{"database", "dev-env-system"}}
		}, devEnv},
		{"secrets-read in dev-tools", kubeGrant, func(g *v1alpha1.AccessGrant) {
			g.Spec.Kube = &v1alpha1.KubeGrant{Role: v1alpha1.RoleSecretsRead, Namespaces: []string{"dev-tools"}}
		}, devEnv},
		{"egress to dev-tools pods", egressGrant, func(g *v1alpha1.AccessGrant) {
			g.Spec.Egress.Endpoints = []v1alpha1.EgressEndpoint{{Namespace: "dev-tools"}}
		}, devEnv},
		{"an endpoint label that moves the selector to another namespace", egressGrant, func(g *v1alpha1.AccessGrant) {
			g.Spec.Egress.Endpoints = []v1alpha1.EgressEndpoint{{Namespace: "database", MatchLabels: map[string]v1alpha1.LabelValue{"io.kubernetes.pod.namespace": "dev-env-system"}}}
		}, "may not use the io.kubernetes., io.cilium. or k8s.io/ meta labels"},
		{"an endpoint label on namespace labels", egressGrant, func(g *v1alpha1.AccessGrant) {
			g.Spec.Egress.Endpoints = []v1alpha1.EgressEndpoint{{Namespace: "database", MatchLabels: map[string]v1alpha1.LabelValue{"io.cilium.k8s.namespace.labels.kubernetes.io/metadata.name": "dev-agents"}}}
		}, "matchLabels"},
		{"a Cilium-sourced label key", egressGrant, func(g *v1alpha1.AccessGrant) {
			g.Spec.Egress.Endpoints = []v1alpha1.EgressEndpoint{{Namespace: "database", MatchLabels: map[string]v1alpha1.LabelValue{"k8s:io.kubernetes.pod.namespace": "dev-agents"}}}
		}, "matchLabels keys are plain label keys"},
		{"a reserved label", egressGrant, func(g *v1alpha1.AccessGrant) {
			g.Spec.Egress.Endpoints = []v1alpha1.EgressEndpoint{{Namespace: "database", MatchLabels: map[string]v1alpha1.LabelValue{"reserved:host": ""}}}
		}, "matchLabels keys are plain label keys"},
		{"a label value with a space", egressGrant, func(g *v1alpha1.AccessGrant) {
			g.Spec.Egress.Endpoints = []v1alpha1.EgressEndpoint{{Namespace: "database", MatchLabels: map[string]v1alpha1.LabelValue{"app": "a b"}}}
		}, "spec.egress.endpoints[0].matchLabels.app"},
		{"egress to an in-cluster name", egressGrant, func(g *v1alpha1.AccessGrant) {
			g.Spec.Egress.FQDNs = []string{"dev-env-keeper.dev-env-system.svc.cluster.local"}
		}, "not reached by name"},

		// Roles and namespaces.
		{"a role outside the catalog", kubeGrant, func(g *v1alpha1.AccessGrant) { g.Spec.Kube.Role = "cluster-admin" }, "spec.kube.role: Unsupported value"},
		{"the built-in edit role", kubeGrant, func(g *v1alpha1.AccessGrant) { g.Spec.Kube.Role = "edit" }, "spec.kube.role: Unsupported value"},
		{"a namespaced role with no namespace", kubeGrant, func(g *v1alpha1.AccessGrant) { g.Spec.Kube.Namespaces = nil }, "the other roles name at least one"},
		{"nodes in a namespace", kubeGrant, func(g *v1alpha1.AccessGrant) { g.Spec.Kube.Role = v1alpha1.RoleNodes }, "take no namespaces"},
		{"a wildcard namespace", kubeGrant, func(g *v1alpha1.AccessGrant) { g.Spec.Kube.Namespaces = []string{"*"} }, "spec.kube.namespaces[0]"},
		{"a namespace pattern", kubeGrant, func(g *v1alpha1.AccessGrant) { g.Spec.Kube.Namespaces = []string{"home-*"} }, "spec.kube.namespaces[0]"},

		// Break-glass.
		{"the break-glass role on a kube grant", kubeGrant, func(g *v1alpha1.AccessGrant) {
			g.Spec.Kube = &v1alpha1.KubeGrant{Role: v1alpha1.RoleBreakglass}
		}, "type breakglass and role dev-env-grant-breakglass go together"},
		{"break-glass with another role", breakglassGrant, func(g *v1alpha1.AccessGrant) {
			g.Spec.Kube = &v1alpha1.KubeGrant{Role: v1alpha1.RoleNodes}
		}, "type breakglass and role dev-env-grant-breakglass go together"},
		{"break-glass in a namespace", breakglassGrant, func(g *v1alpha1.AccessGrant) {
			g.Spec.Kube.Namespaces = []string{"frontend"}
		}, "take no namespaces"},
		{"break-glass for 61m", breakglassGrant, func(g *v1alpha1.AccessGrant) { g.Spec.TTL = ttl(61 * time.Minute) }, "break-glass lasts at most 1h"},

		// TTLs.
		{"a 9m grant", kubeGrant, func(g *v1alpha1.AccessGrant) { g.Spec.TTL = ttl(9 * time.Minute) }, "ttl runs from 10m to 8h"},
		{"a 9h grant", kubeGrant, func(g *v1alpha1.AccessGrant) { g.Spec.TTL = ttl(9 * time.Hour) }, "ttl runs from 10m to 8h"},
		{"a negative TTL", kubeGrant, func(g *v1alpha1.AccessGrant) { g.Spec.TTL = ttl(-time.Hour) }, "ttl runs from 10m to 8h"},
		{"a 5h credential", credentialGrant, func(g *v1alpha1.AccessGrant) { g.Spec.TTL = ttl(5 * time.Hour) }, "a credential grant lasts at most 4h"},

		// One shape per type.
		{"a kube grant with egress", kubeGrant, func(g *v1alpha1.AccessGrant) {
			g.Spec.Egress = &v1alpha1.EgressGrant{CIDRs: []string{"192.168.40.21/32"}, Ports: []v1alpha1.GrantPort{{Port: 22}}}
		}, "egress is set for type egress, and only there"},
		{"a kube grant without kube", kubeGrant, func(g *v1alpha1.AccessGrant) { g.Spec.Kube = nil }, "kube is set for types kube and breakglass"},
		{"an egress grant without egress", egressGrant, func(g *v1alpha1.AccessGrant) { g.Spec.Egress = nil }, "egress is set for type egress"},
		{"a credential grant without a credential", credentialGrant, func(g *v1alpha1.AccessGrant) { g.Spec.Credential = nil }, "credential is set for type credential"},
		{"an unknown type", kubeGrant, func(g *v1alpha1.AccessGrant) { g.Spec.Type = "lease" }, "spec.type: Unsupported value"},
		{"an unknown credential", credentialGrant, func(g *v1alpha1.AccessGrant) { g.Spec.Credential.Name = "unraid-root" }, "spec.credential.name: Unsupported value"},

		// Egress destinations.
		{"egress with no destination", egressGrant, func(g *v1alpha1.AccessGrant) { g.Spec.Egress.CIDRs = nil }, "name at least one destination"},
		{"egress with no port", egressGrant, func(g *v1alpha1.AccessGrant) { g.Spec.Egress.Ports = nil }, "spec.egress.ports"},
		{"a bare address", egressGrant, func(g *v1alpha1.AccessGrant) { g.Spec.Egress.CIDRs = []string{"192.168.40.21"} }, "must be a CIDR"},
		{"port 0", egressGrant, func(g *v1alpha1.AccessGrant) { g.Spec.Egress.Ports = []v1alpha1.GrantPort{{Port: 0}} }, "spec.egress.ports[0].port"},
		{"an unknown protocol", egressGrant, func(g *v1alpha1.AccessGrant) {
			g.Spec.Egress.Ports = []v1alpha1.GrantPort{{Port: 443, Protocol: "SCTP"}}
		}, "spec.egress.ports[0].protocol: Unsupported value"},
		{"a wildcard in the middle of a name", egressGrant, func(g *v1alpha1.AccessGrant) {
			g.Spec.Egress.FQDNs = []string{"api.*.example.com"}
		}, "spec.egress.fqdns[0]"},

		// The requester and the reason.
		{"no requester session", kubeGrant, func(g *v1alpha1.AccessGrant) { g.Spec.Requester.Session = "" }, "spec.requester.session"},
		{"no reason", kubeGrant, func(g *v1alpha1.AccessGrant) { g.Spec.Reason = "" }, "spec.reason"},
	}
	for _, tc := range cases {
		t.Run(tc.title, func(t *testing.T) {
			g := tc.create()
			tc.mutate(g)
			wantInvalid(t, k8s.Create(ctx(t), g), tc.want)
		})
	}
}

func TestAccessGrantSpecImmutableExceptRelease(t *testing.T) {
	const immutable = "immutable after create; only release may change"
	cases := []struct {
		title  string
		create func() *v1alpha1.AccessGrant
		mutate func(*v1alpha1.AccessGrantSpec)
		want   string // empty: the update is accepted
	}{
		{"release", kubeGrant, func(s *v1alpha1.AccessGrantSpec) { s.Release = true }, ""},
		{"change the requester", kubeGrant, func(s *v1alpha1.AccessGrantSpec) { s.Requester.Session = "other-session" }, immutable},
		{"add a requester UID after creation", credentialGrant, func(s *v1alpha1.AccessGrantSpec) {
			s.Requester.SessionUID = "11111111-1111-1111-1111-111111111111"
		}, immutable},
		{"change the role", kubeGrant, func(s *v1alpha1.AccessGrantSpec) { s.Kube.Role = v1alpha1.RoleStorage }, immutable},
		{"add a namespace", kubeGrant, func(s *v1alpha1.AccessGrantSpec) {
			s.Kube.Namespaces = append(s.Kube.Namespaces, "frontend")
		}, immutable},
		{"lengthen the TTL", kubeGrant, func(s *v1alpha1.AccessGrantSpec) { s.TTL = ttl(2 * time.Hour) }, immutable},
		{"change the reason", kubeGrant, func(s *v1alpha1.AccessGrantSpec) { s.Reason = "something else" }, immutable},
		{"add a CIDR", egressGrant, func(s *v1alpha1.AccessGrantSpec) {
			s.Egress.CIDRs = append(s.Egress.CIDRs, "192.168.40.0/24")
		}, immutable},
		{"change the credential", credentialGrant, func(s *v1alpha1.AccessGrantSpec) {
			s.Credential.Name = v1alpha1.CredentialHWSSH
		}, immutable},
	}
	for _, tc := range cases {
		t.Run(tc.title, func(t *testing.T) {
			g := tc.create()
			if err := k8s.Create(ctx(t), g); err != nil {
				t.Fatalf("create: %v", err)
			}
			tc.mutate(&g.Spec)
			err := k8s.Update(ctx(t), g)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("update refused: %v", err)
				}
				return
			}
			wantInvalid(t, err, tc.want)
		})
	}

	t.Run("release cannot be taken back", func(t *testing.T) {
		g := kubeGrant()
		g.Spec.Release = true
		if err := k8s.Create(ctx(t), g); err != nil {
			t.Fatalf("create: %v", err)
		}
		g.Spec.Release = false
		wantInvalid(t, k8s.Update(ctx(t), g), "release cannot be taken back")
	})
}

func TestAccessGrantStatusSubresource(t *testing.T) {
	g := kubeGrant()
	if err := k8s.Create(ctx(t), g); err != nil {
		t.Fatalf("create: %v", err)
	}
	now := metav1.NewTime(time.Now().Truncate(time.Second))
	later := metav1.NewTime(now.Add(time.Hour))

	// A main-resource update cannot set status: the broker writes it through
	// the status subresource, which the operator's API does not hold.
	g.Status = v1alpha1.AccessGrantStatus{Phase: v1alpha1.GrantActive, ApprovedBy: "policy/forged"}
	if err := k8s.Update(ctx(t), g); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got := getGrant(t, g).Status.Phase; got != "" {
		t.Fatalf("a main-resource update set status.phase to %q", got)
	}

	g = getGrant(t, g)
	g.Status = v1alpha1.AccessGrantStatus{
		Phase:           v1alpha1.GrantActive,
		ApprovedBy:      "authentik/tom",
		ApprovedAt:      &now,
		ApprovedTTL:     &metav1.Duration{Duration: 30 * time.Minute},
		ExpiresAt:       &later,
		NotifiedAt:      &now,
		ServiceAccount:  g.Name,
		InstalledPodUID: "f917d2f6-9ac2-4466-b60c-9d2282914b86",
		InstalledAt:     &now,
	}
	if err := k8s.Status().Update(ctx(t), g); err != nil {
		t.Fatalf("status update: %v", err)
	}
	got := getGrant(t, g)
	if got.Status.Phase != v1alpha1.GrantActive || got.Status.ApprovedBy != "authentik/tom" || got.Status.ServiceAccount != g.Name {
		t.Fatalf("status = %+v", got.Status)
	}
	if got.Spec.Kube.Role != v1alpha1.RoleWorkloads {
		t.Fatalf("a status update changed spec: %+v", got.Spec)
	}

	got.Status.Phase = "Approved"
	wantInvalid(t, k8s.Status().Update(ctx(t), got), "status.phase: Unsupported value")
}

func getGrant(t *testing.T, g *v1alpha1.AccessGrant) *v1alpha1.AccessGrant {
	t.Helper()
	var out v1alpha1.AccessGrant
	if err := k8s.Get(ctx(t), client.ObjectKeyFromObject(g), &out); err != nil {
		t.Fatalf("get: %v", err)
	}
	return &out
}

func TestGrantPhaseEnded(t *testing.T) {
	for _, p := range []v1alpha1.GrantPhase{v1alpha1.GrantDenied, v1alpha1.GrantExpired, v1alpha1.GrantReleased, v1alpha1.GrantFailed} {
		if !p.Ended() {
			t.Errorf("%s.Ended() = false", p)
		}
	}
	for _, p := range []v1alpha1.GrantPhase{"", v1alpha1.GrantPending, v1alpha1.GrantActive} {
		if p.Ended() {
			t.Errorf("%q.Ended() = true", p)
		}
	}
}

// workloadsPolicy approves workloads in frontend for haynesnetwork sessions,
// DESIGN-001 6.12's example.
func workloadsPolicy() *v1alpha1.GrantPolicy {
	return &v1alpha1.GrantPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("p-%d", seq.Add(1)), Namespace: ns},
		Spec: v1alpha1.GrantPolicySpec{
			Description: "haynesnetwork sessions may change frontend's workloads, and so read the Secrets they mount",
			Repos:       []string{"haynesnetwork"},
			Type:        v1alpha1.GrantKube,
			Kube:        &v1alpha1.KubePolicy{Roles: []string{v1alpha1.RoleWorkloads}, Namespaces: []string{"frontend"}},
			MaxTTL:      ttl(time.Hour),
		},
	}
}

func printerPolicy() *v1alpha1.GrantPolicy {
	p := workloadsPolicy()
	p.Spec.Description = "haynes-quest sessions reach the 3D printer"
	p.Spec.Repos = []string{"haynes-quest"}
	p.Spec.Type = v1alpha1.GrantEgress
	p.Spec.Kube = nil
	p.Spec.Egress = &v1alpha1.EgressPolicy{CIDRs: []string{"192.168.40.21/32"}, Ports: []v1alpha1.GrantPort{{Port: 80}, {Port: 443}}}
	p.Spec.MaxTTL = ttl(2 * time.Hour)
	return p
}

func TestGrantPolicyAccepts(t *testing.T) {
	cases := []struct {
		title  string
		create func() *v1alpha1.GrantPolicy
		mutate func(*v1alpha1.GrantPolicy)
	}{
		{"workloads in frontend for haynesnetwork", workloadsPolicy, nil},
		{"egress to the printer for haynes-quest", printerPolicy, nil},
		{"nodes, cluster-wide, by profile", workloadsPolicy, func(p *v1alpha1.GrantPolicy) {
			p.Spec.Repos, p.Spec.Profiles = nil, []string{"full"}
			p.Spec.Kube = &v1alpha1.KubePolicy{Roles: []string{v1alpha1.RoleNodes}}
		}},
		{"Proxmox for haynes-ops sessions", workloadsPolicy, func(p *v1alpha1.GrantPolicy) {
			p.Spec.Repos = []string{"haynes-ops"}
			p.Spec.Type = v1alpha1.GrantCredential
			p.Spec.Kube = nil
			p.Spec.Credential = &v1alpha1.CredentialPolicy{Names: []v1alpha1.CredentialName{v1alpha1.CredentialProxmox}}
			p.Spec.MaxTTL = ttl(4 * time.Hour)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.title, func(t *testing.T) {
			p := tc.create()
			if tc.mutate != nil {
				tc.mutate(p)
			}
			if err := k8s.Create(ctx(t), p); err != nil {
				t.Fatalf("create: %v", err)
			}
		})
	}
}

func TestGrantPolicyRejects(t *testing.T) {
	cases := []struct {
		title  string
		create func() *v1alpha1.GrantPolicy
		mutate func(*v1alpha1.GrantPolicy)
		want   string
	}{
		{"break-glass", workloadsPolicy, func(p *v1alpha1.GrantPolicy) {
			p.Spec.Type = v1alpha1.GrantBreakglass
		}, "no policy can approve break-glass"},
		{"the break-glass role", workloadsPolicy, func(p *v1alpha1.GrantPolicy) {
			p.Spec.Kube.Roles = []string{v1alpha1.RoleBreakglass}
		}, "no policy approves dev-env-grant-breakglass or dev-env-grant-secrets-read"},
		{"secrets-read", workloadsPolicy, func(p *v1alpha1.GrantPolicy) {
			p.Spec.Kube.Roles = []string{v1alpha1.RoleWorkloads, v1alpha1.RoleSecretsRead}
		}, "no policy approves dev-env-grant-breakglass or dev-env-grant-secrets-read"},
		{"workloads with a wildcard namespace", workloadsPolicy, func(p *v1alpha1.GrantPolicy) {
			p.Spec.Kube.Namespaces = []string{"*"}
		}, "spec.kube.namespaces[0]"},
		{"workloads with a namespace pattern", workloadsPolicy, func(p *v1alpha1.GrantPolicy) {
			p.Spec.Kube.Namespaces = []string{"frontend", "home-.*"}
		}, "spec.kube.namespaces[1]"},
		{"workloads with no namespace", workloadsPolicy, func(p *v1alpha1.GrantPolicy) {
			p.Spec.Kube.Namespaces = nil
		}, "name the namespaces, one by one"},
		{"a dev-env namespace", workloadsPolicy, func(p *v1alpha1.GrantPolicy) {
			p.Spec.Kube.Namespaces = []string{"dev-agents"}
		}, "no grant reaches the dev-env namespaces"},
		{"egress to dev-env-system pods", printerPolicy, func(p *v1alpha1.GrantPolicy) {
			p.Spec.Egress.Endpoints = []v1alpha1.EgressEndpoint{{Namespace: "dev-env-system"}}
		}, "no grant reaches the dev-env namespaces"},
		{"an endpoint label that moves the selector", printerPolicy, func(p *v1alpha1.GrantPolicy) {
			p.Spec.Egress.Endpoints = []v1alpha1.EgressEndpoint{{Namespace: "database", MatchLabels: map[string]v1alpha1.LabelValue{"io.cilium.k8s.policy.serviceaccount": "dev-env-keeper"}}}
		}, "may not use the io.kubernetes., io.cilium. or k8s.io/ meta labels"},
		{"profile ops", workloadsPolicy, func(p *v1alpha1.GrantPolicy) {
			p.Spec.Profiles = []string{"ops"}
		}, "no policy matches profile ops"},
		{"no requester", workloadsPolicy, func(p *v1alpha1.GrantPolicy) { p.Spec.Repos = nil }, "name the requesters"},
		{"a 9h maxTTL", workloadsPolicy, func(p *v1alpha1.GrantPolicy) { p.Spec.MaxTTL = ttl(9 * time.Hour) }, "maxTTL runs from 10m to 8h"},
		{"a 5h credential", workloadsPolicy, func(p *v1alpha1.GrantPolicy) {
			p.Spec.Type = v1alpha1.GrantCredential
			p.Spec.Kube = nil
			p.Spec.Credential = &v1alpha1.CredentialPolicy{Names: []v1alpha1.CredentialName{v1alpha1.CredentialHWSSH}}
			p.Spec.MaxTTL = ttl(5 * time.Hour)
		}, "a credential grant lasts at most 4h"},
		{"a kube policy without kube", workloadsPolicy, func(p *v1alpha1.GrantPolicy) { p.Spec.Kube = nil }, "kube is set for type kube"},
		{"egress with no destination", printerPolicy, func(p *v1alpha1.GrantPolicy) { p.Spec.Egress.CIDRs = nil }, "name at least one destination"},
		{"no description", workloadsPolicy, func(p *v1alpha1.GrantPolicy) { p.Spec.Description = "" }, "spec.description"},
		{"a 64-character name", workloadsPolicy, func(p *v1alpha1.GrantPolicy) { p.Name = strings.Repeat("p", 64) }, "metadata.name is a DNS label of at most 63 characters"},
		{"a name with a dot", workloadsPolicy, func(p *v1alpha1.GrantPolicy) { p.Name = "frontend.workloads" }, "metadata.name is a DNS label of at most 63 characters"},
	}
	for _, tc := range cases {
		t.Run(tc.title, func(t *testing.T) {
			p := tc.create()
			tc.mutate(p)
			wantInvalid(t, k8s.Create(ctx(t), p), tc.want)
		})
	}
}
