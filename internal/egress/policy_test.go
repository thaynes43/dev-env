package egress

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// Cilium source-prefixed keys are not Kubernetes label keys. Remove the k8s:
// source before applying the selector to ordinary pod labels in these cases.
func podSelector(t *testing.T, selector map[string]any) labels.Selector {
	t.Helper()
	var ls metav1.LabelSelector
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(selector, &ls); err != nil {
		t.Fatal(err)
	}
	plain := make(map[string]string, len(ls.MatchLabels))
	for k, v := range ls.MatchLabels {
		key, ok := strings.CutPrefix(k, "k8s:")
		if !ok {
			t.Fatalf("selector label %q has no Kubernetes source", k)
		}
		plain[key] = v
	}
	ls.MatchLabels = plain
	for i := range ls.MatchExpressions {
		key, ok := strings.CutPrefix(ls.MatchExpressions[i].Key, "k8s:")
		if !ok {
			t.Fatal("selector expression has no Kubernetes source")
		}
		ls.MatchExpressions[i].Key = key
	}
	sel, err := metav1.LabelSelectorAsSelector(&ls)
	if err != nil {
		t.Fatal(err)
	}
	return sel
}

func testGrant() *v1alpha1.AccessGrant {
	return &v1alpha1.AccessGrant{
		ObjectMeta: metav1.ObjectMeta{Namespace: "dev-agents", Name: "grant-egress-test"},
		Spec: v1alpha1.AccessGrantSpec{
			Type: v1alpha1.GrantEgress, Requester: v1alpha1.GrantRequester{Session: "session-test"},
			Egress: &v1alpha1.EgressGrant{
				FQDNs: []string{"api.example.com", "*.example.net"}, CIDRs: []string{"192.0.2.8/32", "2001:db8::8/128"},
				Endpoints: []v1alpha1.EgressEndpoint{{Namespace: "frontend", MatchLabels: map[string]v1alpha1.LabelValue{"app.kubernetes.io/name": "service"}}},
				Ports:     []v1alpha1.GrantPort{{Port: 8443}, {Port: 53, Protocol: v1alpha1.ProtocolUDP}},
			},
		},
	}
}

func TestPolicyScope(t *testing.T) {
	g := testGrant()
	u := Policy(g.Namespace, g)
	selector, _, err := unstructured.NestedMap(u.Object, "spec", "endpointSelector")
	if err != nil {
		t.Fatal(err)
	}
	sel := podSelector(t, selector)
	for _, c := range []struct {
		name, session, hold string
		want                bool
	}{
		{"current or replacement agent pod", "session-test", "", true},
		{"other session", "another-session", "", false},
		{"hold pod", "session-test", "true", false},
		{"unlabelled pod", "", "", false},
	} {
		pod := labels.Set{v1alpha1.LabelSession: c.session}
		if c.hold != "" {
			pod[v1alpha1.LabelHold] = c.hold
		}
		if got := sel.Matches(pod); got != c.want {
			t.Errorf("%s selected = %t, want %t", c.name, got, c.want)
		}
	}
	rules, _, err := unstructured.NestedSlice(u.Object, "spec", "egress")
	if err != nil || len(rules) != 3 {
		t.Fatalf("mixed destinations need 3 rules: %v, %v", rules, err)
	}
	for _, rule := range rules {
		r := rule.(map[string]any)
		kinds := 0
		for _, field := range []string{"toFQDNs", "toCIDR", "toEndpoints"} {
			if _, ok := r[field]; ok {
				kinds++
			}
		}
		if kinds != 1 {
			t.Errorf("a rule combines destination kinds: %v", r)
		}
		ports, _, err := unstructured.NestedSlice(r["toPorts"].([]any)[0].(map[string]any), "ports")
		if err != nil || len(ports) != 2 {
			t.Fatalf("ports missing on a destination kind: %v, %v", ports, err)
		}
		first, second := ports[0].(map[string]any), ports[1].(map[string]any)
		if first["port"] != "8443" || first["protocol"] != "TCP" || second["port"] != "53" || second["protocol"] != "UDP" {
			t.Errorf("ports or default protocol changed: %v", ports)
		}
	}
	fqdns := rules[0].(map[string]any)["toFQDNs"].([]any)
	if fqdns[0].(map[string]any)["matchName"] != "api.example.com" || fqdns[1].(map[string]any)["matchPattern"] != "*.example.net" {
		t.Errorf("DNS names and patterns changed: %v", fqdns)
	}
	ep := rules[2].(map[string]any)["toEndpoints"].([]any)[0].(map[string]any)
	destSel := podSelector(t, ep)
	for _, ns := range []string{"frontend", "dev-env-system", "another-app"} {
		got := destSel.Matches(labels.Set{"io.kubernetes.pod.namespace": ns, "app.kubernetes.io/name": "service"})
		if got != (ns == "frontend") {
			t.Errorf("endpoint in %s selected = %t", ns, got)
		}
	}
}

func TestPolicyOwnershipAndDrift(t *testing.T) {
	g := testGrant()
	for _, c := range []struct {
		name string
		edit func(*unstructured.Unstructured)
		own  bool
	}{
		{"same", func(*unstructured.Unstructured) {}, true},
		{"broader ports", func(u *unstructured.Unstructured) {
			u.Object["spec"].(map[string]any)["egress"] = []any{map[string]any{"toEntities": []any{"all"}}}
		}, true},
		{"other session label", func(u *unstructured.Unstructured) {
			ls := u.GetLabels()
			ls[v1alpha1.LabelSession] = "other"
			u.SetLabels(ls)
		}, false},
		{"other manager", func(u *unstructured.Unstructured) {
			ls := u.GetLabels()
			ls[v1alpha1.LabelManagedBy] = "other"
			u.SetLabels(ls)
		}, false},
		{"other grant", func(u *unstructured.Unstructured) {
			ls := u.GetLabels()
			ls[v1alpha1.LabelGrant] = "grant-other"
			u.SetLabels(ls)
		}, false},
		{"no source scope", func(u *unstructured.Unstructured) {
			u.Object["spec"].(map[string]any)["endpointSelector"] = map[string]any{}
		}, false},
		{"extra policy specs", func(u *unstructured.Unstructured) { u.Object["specs"] = []any{u.Object["spec"]} }, false},
		{"other namespace", func(u *unstructured.Unstructured) { u.SetNamespace("dev-tools") }, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			u := Policy(g.Namespace, g)
			c.edit(u)
			if got := OwnedBy(u, g.Namespace, g); got != c.own {
				t.Errorf("owned = %t, want %t", got, c.own)
			}
			if got := Matches(u, g.Namespace, g); got != (c.name == "same") {
				t.Errorf("scope matches = %t", got)
			}
		})
	}
}
