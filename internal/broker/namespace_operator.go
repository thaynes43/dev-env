package broker

import (
	"fmt"
	"slices"

	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// The namespace operator (DESIGN-042 5.1, class C2): a kube grant of
// dev-env-grant-namespace-operator that names the Secrets its new pods and Jobs
// may use. The broker binds the role like any namespaced catalog role. What it
// adds here is the check of the named Secrets and the match rule: only an
// attested policy approves the role, and only for Secrets the policy names.
// Using a named Secret is enforced at admission, by the identity guard's `use`
// clause and dev-env-grant-secret-names, both in haynes-ops (P5).

// MaxGrantSecrets is the most Secrets one grant names; the schema's MaxItems.
const MaxGrantSecrets = 16

// checkSecrets repeats the schema's rules for a kube grant's named Secrets:
// only the namespace operator names them, at most MaxGrantSecrets, each a
// Secret name (a DNS-1123 subdomain, so no wildcard or pattern), none twice.
func checkSecrets(k *v1alpha1.KubeGrant) error {
	if len(k.Secrets) == 0 {
		return nil
	}
	if k.Role != v1alpha1.RoleNamespaceOperator {
		return fmt.Errorf("role %s names no secrets: only %s does (DESIGN-042 5.1)", k.Role, v1alpha1.RoleNamespaceOperator)
	}
	if len(k.Secrets) > MaxGrantSecrets {
		return fmt.Errorf("the grant names %d secrets, more than %d", len(k.Secrets), MaxGrantSecrets)
	}
	for i, name := range k.Secrets {
		if len(validation.IsDNS1123Subdomain(name)) > 0 {
			return fmt.Errorf("secret %q is not a Secret name", name)
		}
		if slices.Contains(k.Secrets[:i], name) {
			return fmt.Errorf("secret %q is named twice", name)
		}
	}
	return nil
}

// namesNamespaceOperator reports whether a standing match would touch the
// namespace operator: the grant asks for it, or the policy names it. No
// standing policy approves it (the schema refuses one), and the broker does
// not trust that the schema ran, so such a policy approves nothing at all.
func namesNamespaceOperator(p *v1alpha1.GrantPolicy, g *v1alpha1.AccessGrant) bool {
	if k := g.Spec.Kube; k != nil && (k.Role == v1alpha1.RoleNamespaceOperator || len(k.Secrets) > 0) {
		return true
	}
	return p.Spec.Kube != nil && (slices.Contains(p.Spec.Kube.Roles, v1alpha1.RoleNamespaceOperator) || len(p.Spec.Kube.Secrets) > 0)
}

// secretsInside: a namespace-operator grant's Secrets all lie in a policy that
// names at least one; any other role names none.
func secretsInside(g *v1alpha1.KubeGrant, p *v1alpha1.KubePolicy) bool {
	if g.Role != v1alpha1.RoleNamespaceOperator {
		return len(g.Secrets) == 0
	}
	if len(p.Secrets) == 0 {
		return false
	}
	for _, s := range g.Secrets {
		if !slices.Contains(p.Secrets, s) {
			return false
		}
	}
	return true
}
