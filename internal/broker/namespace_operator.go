package broker

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	rbacv1 "k8s.io/api/rbac/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// The namespace operator (DESIGN-042 5.1, class C2): a kube grant of
// dev-env-grant-namespace-operator that names the Secrets its new pods and Jobs
// will use, one to MaxGrantSecrets.
//
// Approval. No standing policy approves it. The policy names the namespaces,
// and may deny Secrets by name or prefix; it does not list the Secrets it
// allows. Each grant names its own, and the approver decides that exact list,
// which the scope digest covers: through an attested policy only when the
// policy opts in with attested.allowSecretUse (mode (a)), through a delegate
// decision (mode (b), S2), or by Tom (Decide).
//
// Use. Besides the catalog role, the broker gives the grant's ServiceAccount
// a Role and a RoleBinding in each granted namespace, both named
// secretUseName, that allow the custom verb `use` on exactly the named Secrets
// (resourceNames). Admission (haynes-ops P5, dev-env-grant-secret-names) asks
// the authorizer, per added Secret reference, whether the requester may `use`
// that Secret in that namespace. So it fails closed: without this grant's own
// Role, nothing is allowed, and no other grant's Role helps. Revoking the
// grant deletes them with its other bindings.

// MaxGrantSecrets is the most Secrets one grant names; the schema's MaxItems.
const MaxGrantSecrets = 16

// secretUseSuffix names a grant's secret-use Role and RoleBinding:
// <grant>-secret-use, beside the catalog RoleBinding <grant>.
const secretUseSuffix = "-secret-use"

func secretUseName(g *v1alpha1.AccessGrant) string { return g.Name + secretUseSuffix }

// checkSecrets repeats the schema's rules for a kube grant's named Secrets:
// the namespace operator names one to MaxGrantSecrets, each a Secret name (a
// DNS-1123 subdomain, so no wildcard or pattern), none twice; no other role
// names any.
func checkSecrets(k *v1alpha1.KubeGrant) error {
	if k.Role != v1alpha1.RoleNamespaceOperator {
		if len(k.Secrets) > 0 {
			return fmt.Errorf("role %s names no secrets: only %s does (DESIGN-042 5.1)", k.Role, v1alpha1.RoleNamespaceOperator)
		}
		return nil
	}
	if len(k.Secrets) == 0 {
		return fmt.Errorf("role %s names the Secrets it will use (DESIGN-042 5.1)", k.Role)
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
// namespace operator: the grant asks for it or names Secrets, or the policy
// names it or denies Secrets. No standing policy approves it (the schema
// refuses one), and the broker does not trust that the schema ran, so such a
// policy approves nothing at all.
func namesNamespaceOperator(p *v1alpha1.GrantPolicy, g *v1alpha1.AccessGrant) bool {
	if k := g.Spec.Kube; k != nil && (k.Role == v1alpha1.RoleNamespaceOperator || len(k.Secrets) > 0) {
		return true
	}
	return p.Spec.Kube != nil && (slices.Contains(p.Spec.Kube.Roles, v1alpha1.RoleNamespaceOperator) || len(p.Spec.Kube.DeniedSecrets) > 0)
}

// attestedAllowsSecretUse is mode (a)'s opt-in: an attested policy approves a
// namespace-operator grant only with attested.allowSecretUse.
func attestedAllowsSecretUse(p *v1alpha1.GrantPolicy, g *v1alpha1.AccessGrant) bool {
	if k := g.Spec.Kube; k == nil || (k.Role != v1alpha1.RoleNamespaceOperator && len(k.Secrets) == 0) {
		return true
	}
	return p.Spec.Attested != nil && p.Spec.Attested.AllowSecretUse
}

// secretsInside: a namespace-operator grant names one to MaxGrantSecrets
// Secrets, none of them denied by the policy; any other grant names none.
func secretsInside(g *v1alpha1.KubeGrant, p *v1alpha1.KubePolicy) bool {
	if g.Role != v1alpha1.RoleNamespaceOperator {
		return len(g.Secrets) == 0
	}
	if len(g.Secrets) == 0 || len(g.Secrets) > MaxGrantSecrets {
		return false
	}
	for _, s := range g.Secrets {
		if slices.ContainsFunc(p.DeniedSecrets, func(d string) bool { return secretDenied(s, d) }) {
			return false
		}
	}
	return true
}

// secretDenied reports whether a deny entry covers a Secret name: equal, or a
// prefix entry ("sh.helm.release.*") the name starts with.
func secretDenied(name, entry string) bool {
	if prefix, ok := strings.CutSuffix(entry, "*"); ok {
		return strings.HasPrefix(name, prefix)
	}
	return name == entry
}

// secretUseRole is the grant's Role in ns: `use` on exactly its Secrets.
func secretUseRole(g *v1alpha1.AccessGrant, ns string) *rbacv1.Role {
	names := slices.Clone(g.Spec.Kube.Secrets)
	sort.Strings(names)
	return &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: secretUseName(g), Labels: grantLabels(g)},
		Rules: []rbacv1.PolicyRule{{
			APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"use"}, ResourceNames: names,
		}},
	}
}

// secretUseBinding binds the grant's secret-use Role in ns to its
// ServiceAccount alone.
func (b *Broker) secretUseBinding(g *v1alpha1.AccessGrant, ns string) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: secretUseName(g), Labels: grantLabels(g)},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: secretUseName(g)},
		Subjects:   []rbacv1.Subject{b.subject(g)},
	}
}

// ensureSecretUse makes a namespace-operator grant's secret-use Role and
// RoleBinding in ns, and reports what it made. Other grants have none.
func (b *Broker) ensureSecretUse(ctx context.Context, g *v1alpha1.AccessGrant, ns string) ([]string, error) {
	if g.Spec.Kube == nil || g.Spec.Kube.Role != v1alpha1.RoleNamespaceOperator {
		return nil, nil
	}
	var made []string
	role := secretUseRole(g, ns)
	created, err := b.ensureOne(ctx, g, role, &rbacv1.Role{}, func(o client.Object) bool {
		return apiequality.Semantic.DeepEqual(o.(*rbacv1.Role).Rules, role.Rules)
	})
	if err != nil {
		return made, err
	}
	if created {
		made = append(made, "role "+ns+"/"+role.Name)
	}
	rb := b.secretUseBinding(g, ns)
	created, err = b.ensureOne(ctx, g, rb, &rbacv1.RoleBinding{}, func(o client.Object) bool {
		have := o.(*rbacv1.RoleBinding)
		return have.RoleRef == rb.RoleRef && apiequality.Semantic.DeepEqual(have.Subjects, rb.Subjects)
	})
	if err != nil {
		return made, err
	}
	if created {
		made = append(made, "rolebinding "+ns+"/"+rb.Name)
	}
	return made, nil
}

// secretUseObjects are what revoke deletes for a namespace-operator grant in
// ns, the binding before the Role.
func secretUseObjects(g *v1alpha1.AccessGrant, ns string) []client.Object {
	if g.Spec.Kube == nil || g.Spec.Kube.Role != v1alpha1.RoleNamespaceOperator {
		return nil
	}
	return []client.Object{
		&rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: secretUseName(g)}},
		&rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: secretUseName(g)}},
	}
}
