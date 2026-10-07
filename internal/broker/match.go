package broker

import (
	"net/netip"
	"slices"
	"sort"
	"strings"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// ProfileOps is the requester profile no policy ever approves (D-18): every
// grant an ops session asks for goes to Tom.
const ProfileOps = "ops"

// Match returns the first GrantPolicy, in name order, that approves g at once,
// or nil when none does and the grant waits for Tom (DESIGN-001 6.12, D-58). A
// policy being deleted approves nothing. Match is a pure function: the broker
// passes it the policies from its cache.
func Match(g *v1alpha1.AccessGrant, policies []v1alpha1.GrantPolicy) *v1alpha1.GrantPolicy {
	sorted := make([]*v1alpha1.GrantPolicy, 0, len(policies))
	for i := range policies {
		if policies[i].DeletionTimestamp.IsZero() {
			sorted = append(sorted, &policies[i])
		}
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	for _, p := range sorted {
		if Matches(p, g) {
			return p
		}
	}
	return nil
}

// Matches reports whether policy p approves grant g: the same type; each
// requester list p sets holds the requester's value; the requester's profile is
// not ops; the TTL is at most p's maxTTL; and the grant's scope lies inside p's.
// Break-glass never matches, nor do the roles dev-env-grant-breakglass and
// dev-env-grant-secrets-read, whatever p says: the schema refuses such a policy,
// and the broker does not trust that it ran (D-27, D-54).
func Matches(p *v1alpha1.GrantPolicy, g *v1alpha1.AccessGrant) bool {
	ps, gs := &p.Spec, &g.Spec
	if gs.Type == v1alpha1.GrantBreakglass || ps.Type == v1alpha1.GrantBreakglass || gs.Type != ps.Type {
		return false
	}
	r := gs.Requester
	if r.Profile == ProfileOps {
		return false
	}
	// A policy names its requesters (the schema asks for profiles, repos or
	// both); one that names none approves nobody.
	if len(ps.Profiles) == 0 && len(ps.Repos) == 0 {
		return false
	}
	if len(ps.Profiles) > 0 && !slices.Contains(ps.Profiles, r.Profile) {
		return false
	}
	if len(ps.Repos) > 0 && !slices.Contains(ps.Repos, r.Repo) {
		return false
	}
	if len(ps.Agents) > 0 && !slices.Contains(ps.Agents, r.Agent) {
		return false
	}
	if gs.TTL.Duration <= 0 || gs.TTL.Duration > ps.MaxTTL.Duration {
		return false
	}
	switch gs.Type {
	case v1alpha1.GrantKube:
		return kubeInside(gs.Kube, ps.Kube)
	case v1alpha1.GrantEgress:
		return egressInside(gs.Egress, ps.Egress)
	case v1alpha1.GrantCredential:
		return gs.Credential != nil && ps.Credential != nil && slices.Contains(ps.Credential.Names, gs.Credential.Name)
	}
	return false
}

// kubeInside: the role is one of the policy's, and every granted namespace is
// one the policy names. A cluster-wide role takes no namespaces.
func kubeInside(g *v1alpha1.KubeGrant, p *v1alpha1.KubePolicy) bool {
	if g == nil || p == nil || !slices.Contains(p.Roles, g.Role) {
		return false
	}
	if g.Role == v1alpha1.RoleBreakglass || g.Role == v1alpha1.RoleSecretsRead {
		return false
	}
	if v1alpha1.ClusterWideRole(g.Role) {
		return len(g.Namespaces) == 0
	}
	if len(g.Namespaces) == 0 {
		return false
	}
	for _, ns := range g.Namespaces {
		if !slices.Contains(p.Namespaces, ns) {
			return false
		}
	}
	return true
}

// egressInside: every destination and every port of the grant is one the
// policy allows.
func egressInside(g *v1alpha1.EgressGrant, p *v1alpha1.EgressPolicy) bool {
	if g == nil || p == nil || len(g.FQDNs)+len(g.CIDRs)+len(g.Endpoints) == 0 || len(g.Ports) == 0 {
		return false
	}
	for _, f := range g.FQDNs {
		if !slices.ContainsFunc(p.FQDNs, func(pf string) bool { return fqdnInside(f, pf) }) {
			return false
		}
	}
	for _, c := range g.CIDRs {
		if !slices.ContainsFunc(p.CIDRs, func(pc string) bool { return cidrInside(c, pc) }) {
			return false
		}
	}
	for i := range g.Endpoints {
		if !slices.ContainsFunc(p.Endpoints, func(pe v1alpha1.EgressEndpoint) bool { return endpointInside(&g.Endpoints[i], &pe) }) {
			return false
		}
	}
	for _, port := range g.Ports {
		if !slices.ContainsFunc(p.Ports, func(pp v1alpha1.GrantPort) bool {
			return pp.Port == port.Port && protocol(pp.Protocol) == protocol(port.Protocol)
		}) {
			return false
		}
	}
	return true
}

// fqdnInside reports whether a grant's DNS name lies inside a policy's: equal,
// or under the policy's "*.suffix". A grant "*.a.x" lies inside a policy
// "*.x"; the bare "x" does not, because "*." means a subdomain (D-54).
func fqdnInside(grant, policy string) bool {
	if grant == policy {
		return true
	}
	suffix, ok := strings.CutPrefix(policy, "*")
	if !ok || !strings.HasPrefix(suffix, ".") {
		return false
	}
	return strings.HasSuffix(strings.TrimPrefix(grant, "*."), suffix)
}

// cidrInside reports whether a grant's address range lies inside a policy's:
// the same address family, a prefix at least as long, and the grant's network
// address inside the policy's range.
func cidrInside(grant, policy string) bool {
	g, err := netip.ParsePrefix(grant)
	if err != nil {
		return false
	}
	p, err := netip.ParsePrefix(policy)
	if err != nil {
		return false
	}
	g, p = g.Masked(), p.Masked()
	if g.Addr().Is4() != p.Addr().Is4() {
		return false
	}
	return p.Bits() <= g.Bits() && p.Contains(g.Addr())
}

// endpointInside: the same namespace, and the policy's labels a subset of the
// grant's, so the grant selects no pod the policy does not.
func endpointInside(g, p *v1alpha1.EgressEndpoint) bool {
	if g.Namespace != p.Namespace {
		return false
	}
	for k, v := range p.MatchLabels {
		if gv, ok := g.MatchLabels[k]; !ok || gv != v {
			return false
		}
	}
	return true
}

// protocol is a port's protocol, TCP when unset (the schema's default).
func protocol(p v1alpha1.GrantProtocol) v1alpha1.GrantProtocol {
	if p == "" {
		return v1alpha1.ProtocolTCP
	}
	return p
}
