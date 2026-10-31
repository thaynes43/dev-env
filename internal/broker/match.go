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
// or nil when none does and the grant waits for Tom (DESIGN-001 6.12, D-61). A
// policy being deleted approves nothing. Match is a pure function: the broker
// passes it the policies from its cache.
func Match(g *v1alpha1.AccessGrant, policies []v1alpha1.GrantPolicy) *v1alpha1.GrantPolicy {
	return MatchAttested(g, policies, nil)
}

// MatchAttested is Match that also considers attested policies (spec.attested).
// A standing policy approves as always. An attested policy is considered only
// when attestedOK is non-nil, the request fits it (MatchesAttested), and
// attestedOK returns true for it (the caller has verified the grant's agent
// attestation). A nil attestedOK ignores every attested policy: it never
// approves through one.
func MatchAttested(g *v1alpha1.AccessGrant, policies []v1alpha1.GrantPolicy, attestedOK func(*v1alpha1.GrantPolicy) bool) *v1alpha1.GrantPolicy {
	sorted := make([]*v1alpha1.GrantPolicy, 0, len(policies))
	for i := range policies {
		if policies[i].DeletionTimestamp.IsZero() {
			sorted = append(sorted, &policies[i])
		}
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	for _, p := range sorted {
		if p.Spec.Attested != nil {
			if attestedOK != nil && MatchesAttested(p, g) && attestedOK(p) {
				return p
			}
			continue
		}
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
// and the broker does not trust that it ran (D-27, D-54). Maintenance never
// matches a standing policy either: only an attested policy approves it
// (ADR-013).
func Matches(p *v1alpha1.GrantPolicy, g *v1alpha1.AccessGrant) bool {
	if p.Spec.Attested != nil {
		return false // an attested policy never approves as a standing one
	}
	if p.Spec.Type == v1alpha1.GrantMaintenance || g.Spec.Type == v1alpha1.GrantMaintenance || p.Spec.Maintenance != nil {
		return false // no standing auto-approval for maintenance
	}
	return matches(p, g, p.Spec.Profiles, p.Spec.Repos, p.Spec.Agents)
}

// MatchesAttested reports whether attested policy p fits grant g: Matches with
// the requesters taken from spec.attested. It says nothing about the grant's
// attestation, which the caller must verify. A policy without spec.attested
// does not match here.
func MatchesAttested(p *v1alpha1.GrantPolicy, g *v1alpha1.AccessGrant) bool {
	a := p.Spec.Attested
	if a == nil || len(p.Spec.Profiles) != 0 || len(p.Spec.Repos) != 0 || len(p.Spec.Agents) != 0 {
		return false // a mixed policy is malformed: approve nothing
	}
	return matches(p, g, a.Profiles, a.Repos, a.Agents)
}

func matches(p *v1alpha1.GrantPolicy, g *v1alpha1.AccessGrant, profiles, repos []string, agents []v1alpha1.AgentKind) bool {
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
	if len(profiles) == 0 && len(repos) == 0 {
		return false
	}
	if len(profiles) > 0 && !slices.Contains(profiles, r.Profile) {
		return false
	}
	if len(repos) > 0 && !slices.Contains(repos, r.Repo) {
		return false
	}
	if len(agents) > 0 && !slices.Contains(agents, r.Agent) {
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
	case v1alpha1.GrantMaintenance:
		// Attested only: Matches refuses maintenance before it gets here.
		return ps.Attested != nil && gs.TTL.Duration <= MaintenanceMaxTTL && maintenanceInside(gs.Maintenance, ps.Maintenance)
	}
	return false
}

// maintenanceInside: the grant's envelope is the policy's exactly (ADR-013).
// Node, node UID, resource, runtime profile and image are equal; the
// capabilities are a subset; CPU and memory are at or below the caps; and
// every worker, device file and read-only mount the grant names is one the
// policy names exactly, so a policy that names none admits none. Operation and
// restore are audit intent and are not compared. The broker repeats the
// envelope's own bounds (ValidateMaintenance) before it decides.
func maintenanceInside(g *v1alpha1.MaintenanceTask, p *v1alpha1.MaintenancePolicy) bool {
	if g == nil || p == nil || !digestImage.MatchString(p.Image) {
		return false
	}
	if g.Node != p.Node || g.NodeUID != p.NodeUID || g.Resource != p.Resource || g.RuntimeProfile != p.RuntimeProfile || g.Image != p.Image {
		return false
	}
	if g.CPUMillis <= 0 || g.CPUMillis > p.MaxCPUMillis || g.MemoryMiB <= 0 || g.MemoryMiB > p.MaxMemoryMiB {
		return false
	}
	for _, pair := range [][2][]string{{g.Capabilities, p.Capabilities}, {g.Devices, p.Devices}, {g.ReadOnlyMounts, p.ReadOnlyMounts}} {
		for _, v := range pair[0] {
			if !slices.Contains(pair[1], v) {
				return false
			}
		}
	}
	for _, w := range g.Workers {
		if !slices.Contains(p.Workers, w) {
			return false
		}
	}
	return true
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
