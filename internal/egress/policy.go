// Package egress builds the namespaced CiliumNetworkPolicy of an egress grant
// (DESIGN-001 6.12, plan 07 step 5, D-64). The broker and the operator's expiry
// backstop share the policy's identity and source-selector checks. They read
// policies directly from the API server; neither keeps a Cilium informer.
package egress

import (
	"strconv"
	"strings"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// ManagedBy is the identity label on the broker's grant policies.
const ManagedBy = "dev-env-broker"

// GVK is namespaced CiliumNetworkPolicy. Baseline clusterwide policies never
// pass through this package.
var GVK = schema.GroupVersionKind{Group: "cilium.io", Version: "v2", Kind: "CiliumNetworkPolicy"}

// Object makes a policy reference suitable for a direct Get or Delete.
func Object(namespace, name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(GVK)
	u.SetNamespace(namespace)
	u.SetName(name)
	return u
}

// source selects the requesting session's agent pod, including a replacement
// after a resume, but not its hold pod: rescue holds carry the session label
// too (D-55).
func source(session string) map[string]any {
	return map[string]any{
		"matchLabels": map[string]any{"k8s:" + v1alpha1.LabelSession: session},
		"matchExpressions": []any{map[string]any{
			"key": "k8s:" + v1alpha1.LabelHold, "operator": "NotIn", "values": []any{"true"},
		}},
	}
}

// Policy builds a previously validated egress grant. Every destination opens
// the grant's complete port list. FQDNs cannot share an egress rule with other
// L3 selectors, so the three destination kinds use separate rules. DNS
// inspection is already supplied by the platform tier's clusterwide policy.
func Policy(namespace string, g *v1alpha1.AccessGrant) *unstructured.Unstructured {
	u := Object(namespace, g.Name)
	u.SetLabels(map[string]string{
		v1alpha1.LabelGrant: g.Name, v1alpha1.LabelSession: g.Spec.Requester.Session,
		v1alpha1.LabelManagedBy: ManagedBy,
	})
	e := g.Spec.Egress
	ports := make([]any, 0, len(e.Ports))
	for _, p := range e.Ports {
		protocol := p.Protocol
		if protocol == "" {
			protocol = v1alpha1.ProtocolTCP
		}
		ports = append(ports, map[string]any{"port": strconv.FormatInt(int64(p.Port), 10), "protocol": string(protocol)})
	}
	rules := make([]any, 0, 3)
	add := func(field string, destinations []any) {
		if len(destinations) != 0 {
			rules = append(rules, map[string]any{field: destinations, "toPorts": []any{map[string]any{"ports": ports}}})
		}
	}
	fqdns := make([]any, 0, len(e.FQDNs))
	for _, f := range e.FQDNs {
		field := "matchName"
		if strings.HasPrefix(f, "*.") {
			field = "matchPattern"
		}
		fqdns = append(fqdns, map[string]any{field: f})
	}
	add("toFQDNs", fqdns)
	cidrs := make([]any, 0, len(e.CIDRs))
	for _, cidr := range e.CIDRs {
		cidrs = append(cidrs, cidr)
	}
	add("toCIDR", cidrs)
	endpoints := make([]any, 0, len(e.Endpoints))
	for _, ep := range e.Endpoints {
		labels := map[string]any{"k8s:io.kubernetes.pod.namespace": ep.Namespace}
		for k, v := range ep.MatchLabels {
			labels["k8s:"+k] = string(v)
		}
		endpoints = append(endpoints, map[string]any{"matchLabels": labels})
	}
	add("toEndpoints", endpoints)
	u.Object["spec"] = map[string]any{"endpointSelector": source(g.Spec.Requester.Session), "egress": rules}
	return u
}

// OwnedBy checks the policy's namespace, name, identity labels and source
// selector. A policy of the same name for another session is a collision, even
// if it carries a grant label. Both deletion paths leave such objects alone.
func OwnedBy(u *unstructured.Unstructured, namespace string, g *v1alpha1.AccessGrant) bool {
	labels := u.GetLabels()
	if u.GroupVersionKind() != GVK || u.GetNamespace() != namespace || u.GetName() != g.Name ||
		labels[v1alpha1.LabelGrant] != g.Name || labels[v1alpha1.LabelSession] != g.Spec.Requester.Session ||
		labels[v1alpha1.LabelManagedBy] != ManagedBy {
		return false
	}
	if _, hasSpecs := u.Object["specs"]; hasSpecs {
		return false
	}
	selector, found, err := unstructured.NestedMap(u.Object, "spec", "endpointSelector")
	return err == nil && found && apiequality.Semantic.DeepEqual(selector, source(g.Spec.Requester.Session))
}

// Matches also verifies the destinations and ports, so an existing broader
// policy is never accepted as the requested grant.
func Matches(u *unstructured.Unstructured, namespace string, g *v1alpha1.AccessGrant) bool {
	return OwnedBy(u, namespace, g) && apiequality.Semantic.DeepEqual(u.Object["spec"], Policy(namespace, g).Object["spec"])
}
