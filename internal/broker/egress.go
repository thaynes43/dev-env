package broker

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/egress"
)

// checkEgress repeats D-54's scope checks even against an older CRD. In
// particular, a meta-label must not replace the endpoint's namespace selector,
// and an empty destination/port list must never turn into an unrestricted rule.
func checkEgress(e *v1alpha1.EgressGrant, forbidden []string) error {
	if len(e.FQDNs)+len(e.CIDRs)+len(e.Endpoints) == 0 {
		return fmt.Errorf("egress names no destination")
	}
	if len(e.FQDNs) > 16 || len(e.CIDRs) > 16 || len(e.Endpoints) > 8 {
		return fmt.Errorf("egress exceeds the destination limits")
	}
	if len(e.Ports) == 0 || len(e.Ports) > 16 {
		return fmt.Errorf("egress names between 1 and 16 ports")
	}
	for _, p := range e.Ports {
		if p.Port < 1 || p.Port > 65535 || (p.Protocol != "" && p.Protocol != v1alpha1.ProtocolTCP && p.Protocol != v1alpha1.ProtocolUDP) {
			return fmt.Errorf("egress port %d must be 1 to 65535 with protocol TCP or UDP", p.Port)
		}
	}
	for _, f := range e.FQDNs {
		name := strings.TrimPrefix(f, "*.")
		if len(f) > 253 || !strings.Contains(name, ".") || len(validation.IsDNS1123Subdomain(name)) > 0 {
			return fmt.Errorf("egress name %q is not a DNS name, optionally with one leading '*.'", f)
		}
		if strings.HasSuffix(name, ".local") {
			return fmt.Errorf("egress name %q is in-cluster or .local: use endpoints, or cidrs for a LAN host", f)
		}
	}
	for _, cidr := range e.CIDRs {
		if _, err := netip.ParsePrefix(cidr); err != nil || len(cidr) > 43 {
			return fmt.Errorf("egress cidr %q must be a CIDR", cidr)
		}
	}
	for _, ep := range e.Endpoints {
		if len(validation.IsDNS1123Label(ep.Namespace)) > 0 {
			return fmt.Errorf("egress namespace %q is not a namespace name", ep.Namespace)
		}
		if slices.Contains(forbidden, ep.Namespace) {
			return fmt.Errorf("no grant reaches namespace %s (DESIGN-001 6.12)", ep.Namespace)
		}
		if len(ep.MatchLabels) > 8 {
			return fmt.Errorf("egress endpoint labels exceed the limit of 8")
		}
		for k, v := range ep.MatchLabels {
			if len(k) > 253 || len(validation.IsQualifiedName(k)) > 0 ||
				strings.HasPrefix(k, "io.kubernetes.") || strings.HasPrefix(k, "io.cilium.") || strings.HasPrefix(k, "k8s.io/") {
				return fmt.Errorf("egress endpoint label %q must be a plain pod label, never a Kubernetes or Cilium meta-label", k)
			}
			if len(validation.IsValidLabelValue(string(v))) > 0 {
				return fmt.Errorf("egress endpoint label %q has an invalid value", k)
			}
		}
	}
	return nil
}

func (b *Broker) ensureEgress(ctx context.Context, g *v1alpha1.AccessGrant) ([]string, error) {
	want := egress.Policy(b.SessionNamespace, g)
	created, err := b.ensureOne(ctx, g, want, egress.Object(b.SessionNamespace, g.Name), func(o client.Object) bool {
		return egress.Matches(o.(*unstructured.Unstructured), b.SessionNamespace, g)
	})
	if err != nil || !created {
		return nil, err
	}
	return []string{"ciliumnetworkpolicy " + describe(want)}, nil
}
