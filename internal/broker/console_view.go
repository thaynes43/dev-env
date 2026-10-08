package broker

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

func (s *Console) grantView(ctx context.Context, g *v1alpha1.AccessGrant, identity ConsoleIdentity) (ApprovalGrant, error) {
	r := g.Spec.Requester
	v := ApprovalGrant{
		Name: g.Name, URL: "/approvals/" + g.Name, DecisionURL: "/approvals/" + g.Name + "/decision",
		Session: r.Session, Repo: r.Repo, Profile: r.Profile, Agent: string(r.Agent), Parent: r.Parent,
		Type: string(g.Spec.Type), TTL: g.Spec.TTL.Duration.String(), Reason: g.Spec.Reason,
		Phase: string(g.Status.Phase), Message: g.Status.Message, ApprovedBy: g.Status.ApprovedBy, DeniedBy: g.Status.DeniedBy,
		CreatedAt: g.CreationTimestamp.UTC().Format(time.RFC3339),
		Pending:   consolePending(g, s.Broker.now().Time), Breakglass: g.Spec.Type == v1alpha1.GrantBreakglass,
		FreshLogin: identity.fresh(s.Broker.now().Time),
	}
	if v.Phase == "" {
		v.Phase = string(v1alpha1.GrantPending)
	}
	if g.Status.ExpiresAt != nil {
		v.ExpiresAt = g.Status.ExpiresAt.UTC().Format(time.RFC3339)
	}
	if k := g.Spec.Kube; k != nil {
		v.Role, v.Namespaces = k.Role, slices.Clone(k.Namespaces)
		v.SecretsRead, v.Workloads = k.Role == v1alpha1.RoleSecretsRead, k.Role == v1alpha1.RoleWorkloads
		v.Scope = k.Role
		if len(k.Namespaces) > 0 {
			v.Scope += " in " + strings.Join(k.Namespaces, ", ")
		} else {
			v.Scope += " (cluster-wide)"
		}
	}
	if g.Spec.Egress != nil {
		b, err := json.MarshalIndent(g.Spec.Egress, "", "  ")
		if err != nil {
			return v, err
		}
		v.Scope = string(b)
	}
	if g.Spec.Credential != nil {
		v.Scope = string(g.Spec.Credential.Name)
	}
	if r.Parent != "" {
		var parent v1alpha1.AgentSession
		err := s.Broker.APIReader.Get(ctx, types.NamespacedName{Namespace: s.Broker.SessionNamespace, Name: r.Parent}, &parent)
		if err != nil && !apierrors.IsNotFound(err) {
			return v, err
		}
		if err == nil {
			v.ParentRepo = parent.Spec.Repo
		}
	}
	var err error
	v.PolicySnippet, err = s.policySnippet(g)
	return v, err
}

// policySnippet describes the exact request as code for a human to review and
// submit in haynes-ops. It neither applies a policy nor suggests forbidden ones.
func (s *Console) policySnippet(g *v1alpha1.AccessGrant) (string, error) {
	r := g.Spec.Requester
	if g.Spec.Type == v1alpha1.GrantBreakglass || r.Profile == "ops" || (r.Repo == "" && r.Profile == "") {
		return "", nil
	}
	if g.Spec.Kube != nil {
		if g.Spec.Kube.Role == v1alpha1.RoleSecretsRead || g.Spec.Kube.Role == v1alpha1.RoleBreakglass {
			return "", nil
		}
		for _, ns := range g.Spec.Kube.Namespaces {
			if slices.Contains(DevEnvNamespaces, ns) || ns == s.Broker.SessionNamespace || ns == s.Broker.PolicyNamespace {
				return "", nil
			}
		}
	}
	copy := g.DeepCopy()
	spec := v1alpha1.GrantPolicySpec{
		Description: fmt.Sprintf("Approve %s requests for %s; review the scope and risks before applying.", g.Spec.Type, r.Repo),
		Type:        g.Spec.Type, MaxTTL: g.Spec.TTL,
	}
	if r.Repo != "" {
		spec.Repos = []string{r.Repo}
	} else {
		spec.Profiles = []string{r.Profile}
	}
	if k := copy.Spec.Kube; k != nil {
		spec.Kube = &v1alpha1.KubePolicy{Roles: []string{k.Role}, Namespaces: k.Namespaces}
	}
	if e := copy.Spec.Egress; e != nil {
		spec.Egress = &v1alpha1.EgressPolicy{FQDNs: e.FQDNs, CIDRs: e.CIDRs, Endpoints: e.Endpoints, Ports: e.Ports}
	}
	if c := copy.Spec.Credential; c != nil {
		spec.Credential = &v1alpha1.CredentialPolicy{Names: []v1alpha1.CredentialName{c.Name}}
	}
	b, err := yaml.Marshal(map[string]any{
		"apiVersion": v1alpha1.GroupVersion.String(), "kind": "GrantPolicy",
		"metadata": map[string]string{"name": "approve-" + string(g.Spec.Type), "namespace": s.Broker.PolicyNamespace},
		"spec":     spec,
	})
	return string(b), err
}
