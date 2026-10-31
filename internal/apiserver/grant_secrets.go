package apiserver

import (
	"slices"
	"sort"

	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/broker"
)

// grantSecrets reads a kube request's named Secrets (DESIGN-042 5.1): role
// dev-env-grant-namespace-operator names 1 to broker.MaxGrantSecrets, each a
// Secret name (the CRD's DNS-1123 subdomain pattern and 253-character bound),
// none twice; every other role names none. It returns them sorted, so the
// stored spec, the scope digest and the view all carry the same list, and the
// order of the request does not change the digest. A duplicate is refused,
// never dropped: the approver must see exactly what the session asked for.
func grantSecrets(role string, secrets []string) ([]string, []apiv1.FieldError) {
	var fields []apiv1.FieldError
	add := func(format string, args ...any) { fields = append(fields, fieldError("secrets", format, args...)) }
	if role != v1alpha1.RoleNamespaceOperator {
		if len(secrets) > 0 {
			add("only for role %s: role %s names no Secrets", v1alpha1.RoleNamespaceOperator, role)
		}
		return nil, fields
	}
	if len(secrets) == 0 {
		add("required for role %s: the 1 to %d Secrets the grant will use, each by name", role, broker.MaxGrantSecrets)
		return nil, fields
	}
	if len(secrets) > broker.MaxGrantSecrets {
		add("at most %d Secrets, not %d", broker.MaxGrantSecrets, len(secrets))
	}
	out := slices.Clone(secrets)
	sort.Strings(out)
	for i, name := range out {
		if len(validation.IsDNS1123Subdomain(name)) > 0 {
			add("%q is not a Secret name: a DNS-1123 subdomain, with no wildcard or pattern", name)
		}
		if i > 0 && out[i-1] == name {
			add("%q is named twice", name)
		}
	}
	if len(fields) > 0 {
		return nil, fields
	}
	return out, nil
}
