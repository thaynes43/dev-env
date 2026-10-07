package broker

import (
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

func TestCheckEgress(t *testing.T) {
	for _, c := range []struct {
		name string
		edit func(*v1alpha1.EgressGrant)
		want string
	}{
		{"valid mixed destinations", nil, ""},
		{"no destination", func(e *v1alpha1.EgressGrant) { e.FQDNs, e.CIDRs, e.Endpoints = nil, nil, nil }, "no destination"},
		{"no ports", func(e *v1alpha1.EgressGrant) { e.Ports = nil }, "ports"},
		{"zero port", func(e *v1alpha1.EgressGrant) { e.Ports[0].Port = 0 }, "port"},
		{"too large port", func(e *v1alpha1.EgressGrant) { e.Ports[0].Port = 65536 }, "port"},
		{"unknown protocol", func(e *v1alpha1.EgressGrant) { e.Ports[0].Protocol = "SCTP" }, "protocol"},
		{"CIDR malformed", func(e *v1alpha1.EgressGrant) { e.CIDRs = []string{"192.0.2.8"} }, "CIDR"},
		{"CIDR IPv6", func(e *v1alpha1.EgressGrant) { e.CIDRs = []string{"2001:db8::/64"} }, ""},
		{"DNS local", func(e *v1alpha1.EgressGrant) { e.FQDNs = []string{"service.frontend.svc.cluster.local"} }, "use endpoints"},
		{"DNS multiple wildcard", func(e *v1alpha1.EgressGrant) { e.FQDNs = []string{"**.example.com"} }, "DNS name"},
		{"DNS bare wildcard", func(e *v1alpha1.EgressGrant) { e.FQDNs = []string{"*"} }, "DNS name"},
		{"DNS embedded wildcard", func(e *v1alpha1.EgressGrant) { e.FQDNs = []string{"api.*.example.com"} }, "DNS name"},
		{"DNS invalid label", func(e *v1alpha1.EgressGrant) { e.FQDNs = []string{"api_.example.com"} }, "DNS name"},
		{"forbidden namespace", func(e *v1alpha1.EgressGrant) { e.Endpoints[0].Namespace = "dev-env-system" }, "no grant reaches"},
		{"custom broker namespace", func(e *v1alpha1.EgressGrant) { e.Endpoints[0].Namespace = "broker-home" }, "no grant reaches"},
		{"empty namespace", func(e *v1alpha1.EgressGrant) { e.Endpoints[0].Namespace = "" }, "namespace name"},
		{"meta namespace", func(e *v1alpha1.EgressGrant) {
			e.Endpoints[0].MatchLabels = map[string]v1alpha1.LabelValue{"io.kubernetes.pod.namespace": "dev-env-system"}
		}, "plain pod label"},
		{"Cilium meta label", func(e *v1alpha1.EgressGrant) {
			e.Endpoints[0].MatchLabels = map[string]v1alpha1.LabelValue{"io.cilium.k8s.policy.serviceaccount": "default"}
		}, "plain pod label"},
		{"Kubernetes meta prefix", func(e *v1alpha1.EgressGrant) {
			e.Endpoints[0].MatchLabels = map[string]v1alpha1.LabelValue{"k8s.io/namespace": "other"}
		}, "plain pod label"},
		{"Cilium source prefix", func(e *v1alpha1.EgressGrant) {
			e.Endpoints[0].MatchLabels = map[string]v1alpha1.LabelValue{"k8s:io.kubernetes.pod.namespace": "other"}
		}, "plain pod label"},
		{"reserved source", func(e *v1alpha1.EgressGrant) {
			e.Endpoints[0].MatchLabels = map[string]v1alpha1.LabelValue{"reserved:world": ""}
		}, "plain pod label"},
		{"invalid label value", func(e *v1alpha1.EgressGrant) {
			e.Endpoints[0].MatchLabels = map[string]v1alpha1.LabelValue{"app": "a/b"}
		}, "invalid value"},
		{"too many names", func(e *v1alpha1.EgressGrant) { e.FQDNs = make([]string, 17) }, "destination limits"},
	} {
		t.Run(c.name, func(t *testing.T) {
			g := egressGrant(v1alpha1.EgressGrant{
				FQDNs: []string{"api.example.com", "*.example.net"}, CIDRs: []string{"192.0.2.8/32"},
				Endpoints: []v1alpha1.EgressEndpoint{{Namespace: "frontend", MatchLabels: map[string]v1alpha1.LabelValue{"app.kubernetes.io/name": "api"}}},
				Ports:     []v1alpha1.GrantPort{{Port: 8443}, {Port: 53, Protocol: v1alpha1.ProtocolUDP}},
			})
			if c.edit != nil {
				c.edit(g.Spec.Egress)
			}
			err := check(g, "dev-agents", "broker-home")
			if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
				t.Errorf("check = %v, want %q", err, c.want)
			}
		})
	}
}
