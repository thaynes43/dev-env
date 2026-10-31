package broker

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	clocktesting "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// namespaceOperatorGrant is DESIGN-042 A2's request: the namespace operator in
// downloads with the two Secrets the Prowlarr cookie Job uses.
func namespaceOperatorGrant(secrets ...string) *v1alpha1.AccessGrant {
	g := kubeGrant(v1alpha1.RoleNamespaceOperator, "downloads")
	g.Spec.Kube.Secrets = secrets
	return g
}

// namespaceOperatorPolicy is an attested kube-namespace-operator policy for
// full sessions: it names the namespaces, opts into mode (a) when allow is
// set, and denies the given Secrets.
func namespaceOperatorPolicy(name string, allow bool, denied ...string) v1alpha1.GrantPolicy {
	p := kubePolicy(name, []string{v1alpha1.RoleNamespaceOperator}, "downloads", "network")
	p.Spec.Profiles = nil
	p.Spec.Attested = &v1alpha1.AttestedRequesters{Profiles: []string{"full", "workbench"}, AllowSecretUse: allow}
	p.Spec.Kube.DeniedSecrets = denied
	return p
}

func TestNamespaceOperatorIsANamespacedCatalogRole(t *testing.T) {
	if !slices.Contains(catalog, v1alpha1.RoleNamespaceOperator) {
		t.Fatal("the broker's catalog does not hold the namespace operator, so it would refuse to bind it")
	}
	if v1alpha1.ClusterWideRole(v1alpha1.RoleNamespaceOperator) {
		t.Fatal("the namespace operator is bound per namespace, never cluster-wide")
	}
}

func TestCheckNamedSecrets(t *testing.T) {
	seventeen := make([]string, 17)
	for i := range seventeen {
		seventeen[i] = fmt.Sprintf("app-%d-secret", i)
	}
	cases := []struct {
		name  string
		grant *v1alpha1.AccessGrant
		fails string // a part of the error; "" passes
	}{
		{"named Secrets", namespaceOperatorGrant("prowlarr-secret", "myanonamouse-secret"), ""},
		{"a dotted Secret name", namespaceOperatorGrant("sh.helm.release.v1.prowlarr.v3"), ""},
		{"sixteen Secrets", namespaceOperatorGrant(seventeen[:16]...), ""},
		{"no Secret", namespaceOperatorGrant(), "names the Secrets it will use"},
		{"seventeen Secrets", namespaceOperatorGrant(seventeen...), "more than 16"},
		{"a wildcard", namespaceOperatorGrant("*"), "not a Secret name"},
		{"a pattern", namespaceOperatorGrant("prowlarr-*"), "not a Secret name"},
		{"a namespace-qualified name", namespaceOperatorGrant("downloads/prowlarr-secret"), "not a Secret name"},
		{"an upper-case name", namespaceOperatorGrant("Prowlarr"), "not a Secret name"},
		{"an empty name", namespaceOperatorGrant(""), "not a Secret name"},
		{"a name twice", namespaceOperatorGrant("prowlarr-secret", "prowlarr-secret"), "named twice"},
		{"in dev-agents", func() *v1alpha1.AccessGrant {
			g := namespaceOperatorGrant("prowlarr-secret")
			g.Spec.Kube.Namespaces = []string{"dev-agents"}
			return g
		}(), "no grant reaches namespace dev-agents"},
		{"with no namespace", func() *v1alpha1.AccessGrant {
			g := namespaceOperatorGrant("prowlarr-secret")
			g.Spec.Kube.Namespaces = nil
			return g
		}(), "namespaced and names at least one"},
		{"Secrets on workloads", func() *v1alpha1.AccessGrant {
			g := kubeGrant(v1alpha1.RoleWorkloads, "downloads")
			g.Spec.Kube.Secrets = []string{"prowlarr-secret"}
			return g
		}(), "names no secrets"},
		{"Secrets on secrets-read", func() *v1alpha1.AccessGrant {
			g := kubeGrant(v1alpha1.RoleSecretsRead, "downloads")
			g.Spec.Kube.Secrets = []string{"prowlarr-secret"}
			return g
		}(), "names no secrets"},
		{"Secrets on nodes", func() *v1alpha1.AccessGrant {
			g := kubeGrant(v1alpha1.RoleNodes)
			g.Spec.Kube.Secrets = []string{"prowlarr-secret"}
			return g
		}(), "names no secrets"},
		{"Secrets on break-glass", func() *v1alpha1.AccessGrant {
			g := kubeGrant(v1alpha1.RoleBreakglass)
			g.Spec.Type, g.Spec.Kube.Secrets = v1alpha1.GrantBreakglass, []string{"prowlarr-secret"}
			return g
		}(), "names no secrets"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := check(c.grant, "broker-home")
			switch {
			case c.fails == "" && err != nil:
				t.Fatalf("check refused it: %v", err)
			case c.fails != "" && (err == nil || !strings.Contains(err.Error(), c.fails)):
				t.Fatalf("check = %v, want an error containing %q", err, c.fails)
			}
		})
	}
}

// No standing policy approves the namespace operator, and a standing policy
// that names it or denies Secrets approves nothing at all, whatever else it
// names: the schema refuses such a policy, and the broker does not trust that
// it ran.
func TestMatchesNeverApprovesTheNamespaceOperatorStanding(t *testing.T) {
	standing := kubePolicy("standing", []string{v1alpha1.RoleNamespaceOperator}, "downloads")
	if Matches(&standing, namespaceOperatorGrant("prowlarr-secret")) {
		t.Error("a standing policy approved the namespace operator")
	}
	mixed := kubePolicy("mixed", []string{v1alpha1.RoleWorkloads, v1alpha1.RoleNamespaceOperator}, "downloads")
	if Matches(&mixed, kubeGrant(v1alpha1.RoleWorkloads, "downloads")) {
		t.Error("a standing policy that names the namespace operator approved a workloads grant")
	}
	denies := kubePolicy("denies", []string{v1alpha1.RoleWorkloads}, "downloads")
	denies.Spec.Kube.DeniedSecrets = []string{"prowlarr-secret"}
	if Matches(&denies, kubeGrant(v1alpha1.RoleWorkloads, "downloads")) {
		t.Error("a standing policy that denies Secrets approved a workloads grant")
	}
	smuggled := kubeGrant(v1alpha1.RoleWorkloads, "downloads")
	smuggled.Spec.Kube.Secrets = []string{"prowlarr-secret"}
	plain := kubePolicy("plain", []string{v1alpha1.RoleWorkloads}, "downloads")
	if Matches(&plain, smuggled) {
		t.Error("a standing workloads policy approved a workloads grant that names Secrets")
	}
	attested := namespaceOperatorPolicy("attested", true)
	if Matches(&attested, namespaceOperatorGrant("prowlarr-secret")) {
		t.Error("Matches approved through an attested policy")
	}
}

// An attested policy fits a namespace-operator grant only when it opts into
// mode (a), names the role and every granted namespace, and denies none of
// the grant's Secrets. The policy does not list the Secrets it allows: the
// grant names them (DESIGN-042 5.1, Q1).
func TestMatchesAttestedNamedSecrets(t *testing.T) {
	cases := []struct {
		name   string
		grant  *v1alpha1.AccessGrant
		policy v1alpha1.GrantPolicy
		want   bool
	}{
		{"any Secrets, under an opted-in policy", namespaceOperatorGrant("prowlarr-secret", "myanonamouse-secret"), namespaceOperatorPolicy("p", true), true},
		{"without the mode (a) opt-in", namespaceOperatorGrant("prowlarr-secret"), namespaceOperatorPolicy("p", false), false},
		{"no Secret", namespaceOperatorGrant(), namespaceOperatorPolicy("p", true), false},
		{"seventeen Secrets", namespaceOperatorGrant(strings.Split("a b c d e f g h i j k l m n o p q", " ")...), namespaceOperatorPolicy("p", true), false},
		{"a denied Secret", namespaceOperatorGrant("prowlarr-secret", "cloudflare-token"), namespaceOperatorPolicy("p", true, "cloudflare-token"), false},
		{"a Secret under a denied prefix", namespaceOperatorGrant("sh.helm.release.v1.prowlarr.v3"), namespaceOperatorPolicy("p", true, "sh.helm.release.*"), false},
		{"a Secret beside a denied prefix", namespaceOperatorGrant("prowlarr-secret"), namespaceOperatorPolicy("p", true, "sh.helm.release.*", "prowlarr"), true},
		{"a namespace the policy does not name", func() *v1alpha1.AccessGrant {
			g := namespaceOperatorGrant("prowlarr-secret")
			g.Spec.Kube.Namespaces = []string{"downloads", "media"}
			return g
		}(), namespaceOperatorPolicy("p", true), false},
		{"another role's policy", namespaceOperatorGrant("prowlarr-secret"), func() v1alpha1.GrantPolicy {
			p := namespaceOperatorPolicy("p", true)
			p.Spec.Kube.Roles = []string{v1alpha1.RoleWorkloads}
			return p
		}(), false},
		{"workloads under a policy naming both roles, no opt-in", kubeGrant(v1alpha1.RoleWorkloads, "downloads"), func() v1alpha1.GrantPolicy {
			p := namespaceOperatorPolicy("p", false)
			p.Spec.Kube.Roles = []string{v1alpha1.RoleWorkloads, v1alpha1.RoleNamespaceOperator}
			return p
		}(), true},
		{"workloads that names Secrets", func() *v1alpha1.AccessGrant {
			g := kubeGrant(v1alpha1.RoleWorkloads, "downloads")
			g.Spec.Kube.Secrets = []string{"prowlarr-secret"}
			return g
		}(), func() v1alpha1.GrantPolicy {
			p := namespaceOperatorPolicy("p", true)
			p.Spec.Kube.Roles = []string{v1alpha1.RoleWorkloads, v1alpha1.RoleNamespaceOperator}
			return p
		}(), false},
		{"profile ops", func() *v1alpha1.AccessGrant {
			g := namespaceOperatorGrant("prowlarr-secret")
			g.Spec.Requester.Profile = ProfileOps
			return g
		}(), namespaceOperatorPolicy("p", true), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := MatchesAttested(&c.policy, c.grant); got != c.want {
				t.Errorf("MatchesAttested = %v, want %v", got, c.want)
			}
		})
	}
}

// S2 delegation and S5 Secret scopes compose without enabling mode (a).
func TestMatchesDelegatedNamedSecrets(t *testing.T) {
	for _, both := range []bool{false, true} {
		for _, denied := range []bool{false, true} {
			t.Run(fmt.Sprintf("both=%t/denied=%t", both, denied), func(t *testing.T) {
				p := namespaceOperatorPolicy("p", false)
				if !both {
					p.Spec.Attested = nil
				}
				p.Spec.Delegated = &v1alpha1.DelegatedRequesters{Profiles: []string{"full"}}
				if denied {
					p.Spec.Kube.DeniedSecrets = []string{"private-*"}
				}
				g := namespaceOperatorGrant("private-token")
				if got := MatchesDelegated(&p, g); got == denied {
					t.Fatalf("delegated named-Secret match = %v, denied = %v", got, denied)
				}
				if MatchesAttested(&p, g) || Matches(&p, g) {
					t.Fatal("delegated coverage bypassed attested Secret opt-in or standing refusal")
				}
				g.Spec.Kube.Namespaces = []string{"media"}
				if MatchesDelegated(&p, g) {
					t.Fatal("delegated policy covered an unnamed namespace")
				}
			})
		}
	}
}

func TestSecretDenied(t *testing.T) {
	for _, c := range []struct {
		name, entry string
		want        bool
	}{
		{"cloudflare-token", "cloudflare-token", true},
		{"cloudflare-token-2", "cloudflare-token", false},
		{"sh.helm.release.v1.x.v1", "sh.helm.release.*", true},
		{"sh.helm", "sh.helm.release.*", false},
		{"prowlarr-secret", "prowlarr*", true},
	} {
		if got := secretDenied(c.name, c.entry); got != c.want {
			t.Errorf("secretDenied(%q, %q) = %v, want %v", c.name, c.entry, got, c.want)
		}
	}
}

// Inert until a policy names the role (DESIGN-042 S5): the standing and
// attested policies of today approve no namespace-operator grant, even with a
// verified attestation, and approve the grants they approved before.
func TestNamespaceOperatorInertWithoutAPolicy(t *testing.T) {
	wl := []string{v1alpha1.RoleWorkloads}
	nodes := kubePolicy("kube-nodes-attested", []string{v1alpha1.RoleNodes})
	nodes.Spec.Profiles, nodes.Spec.Attested = nil, &v1alpha1.AttestedRequesters{Profiles: []string{"full"}}
	attestedWorkloads := kubePolicy("kube-workloads-attested", wl, "downloads")
	attestedWorkloads.Spec.Profiles, attestedWorkloads.Spec.Attested = nil, &v1alpha1.AttestedRequesters{Profiles: []string{"full"}}
	today := []v1alpha1.GrantPolicy{kubePolicy("frontend-workloads", wl, "frontend", "downloads"), nodes, attestedWorkloads}
	verified := func(*v1alpha1.GrantPolicy) bool { return true }

	g := namespaceOperatorGrant("prowlarr-secret")
	if p := Match(g, today); p != nil {
		t.Errorf("Match approved the namespace operator through %s", p.Name)
	}
	if p := MatchAttested(g, today, verified); p != nil {
		t.Errorf("MatchAttested approved the namespace operator through %s", p.Name)
	}
	if p := Match(kubeGrant(v1alpha1.RoleWorkloads, "downloads"), today); p == nil || p.Name != "frontend-workloads" {
		t.Errorf("Match = %v, want frontend-workloads for a workloads grant, as before", p)
	}
	if p := MatchAttested(kubeGrant(v1alpha1.RoleNodes), today, verified); p == nil || p.Name != "kube-nodes-attested" {
		t.Errorf("MatchAttested = %v, want kube-nodes-attested for a nodes grant, as before", p)
	}
}

// fakeBroker is a broker on a fake client holding objs.
func fakeBroker(t *testing.T, objs ...client.Object) (*Broker, client.Client) {
	t.Helper()
	sc := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(sc); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(sc); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(sc).WithObjects(objs...).Build()
	return &Broker{Client: c, APIReader: c, Clock: clocktesting.NewFakeClock(time.Now()), SessionNamespace: "dev-agents", PolicyNamespace: "dev-env-system"}, c
}

// A namespace-operator grant's identity gets, per namespace, the catalog
// RoleBinding and a secret-use Role and RoleBinding that allow `use` on
// exactly its Secrets; revoking deletes all of them before the
// ServiceAccount. Other kube grants get no secret-use objects.
func TestNamespaceOperatorSecretUseObjects(t *testing.T) {
	ctx := context.Background()
	g := namespaceOperatorGrant("prowlarr-secret", "myanonamouse-secret")
	g.Spec.Kube.Namespaces = []string{"downloads", "network"}
	b, c := fakeBroker(t, g)
	made, err := b.ensure(ctx, g)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	for _, ns := range g.Spec.Kube.Namespaces {
		role := &rbacv1.Role{}
		if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: g.Name + "-secret-use"}, role); err != nil {
			t.Fatalf("secret-use Role in %s: %v (made %v)", ns, err, made)
		}
		want := rbacv1.PolicyRule{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"use"}, ResourceNames: []string{"myanonamouse-secret", "prowlarr-secret"}}
		if len(role.Rules) != 1 || fmt.Sprint(role.Rules[0]) != fmt.Sprint(want) {
			t.Errorf("Role %s/%s rules = %+v, want only %+v", ns, role.Name, role.Rules, want)
		}
		if role.Labels[v1alpha1.LabelGrant] != g.Name {
			t.Errorf("Role %s/%s is not labelled with its grant", ns, role.Name)
		}
		rb := &rbacv1.RoleBinding{}
		if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: g.Name + "-secret-use"}, rb); err != nil {
			t.Fatalf("secret-use RoleBinding in %s: %v", ns, err)
		}
		if rb.RoleRef.Kind != "Role" || rb.RoleRef.Name != role.Name || len(rb.Subjects) != 1 || rb.Subjects[0].Name != g.Name || rb.Subjects[0].Namespace != "dev-agents" {
			t.Errorf("RoleBinding %s/%s = %+v %+v, want the Role bound to dev-agents/%s alone", ns, rb.Name, rb.RoleRef, rb.Subjects, g.Name)
		}
		catalogRB := &rbacv1.RoleBinding{}
		if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: g.Name}, catalogRB); err != nil || catalogRB.RoleRef.Name != v1alpha1.RoleNamespaceOperator {
			t.Errorf("catalog RoleBinding in %s: %+v %v", ns, catalogRB.RoleRef, err)
		}
	}
	// A second pass makes nothing new.
	if again, err := b.ensure(ctx, g); err != nil || len(again) != 0 {
		t.Errorf("second ensure made %v, %v", again, err)
	}

	if err := b.revoke(ctx, g, "test"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	for _, o := range []client.Object{&rbacv1.Role{}, &rbacv1.RoleBinding{}} {
		for _, ns := range g.Spec.Kube.Namespaces {
			for _, name := range []string{g.Name, g.Name + "-secret-use"} {
				if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, o); !apierrors.IsNotFound(err) {
					t.Errorf("%T %s/%s left after revoke: %v", o, ns, name, err)
				}
			}
		}
	}

	// A workloads grant gets no secret-use objects.
	wl := kubeGrant(v1alpha1.RoleWorkloads, "downloads")
	wl.Name = "grant-1007-120000-wxyz"
	b2, c2 := fakeBroker(t, wl)
	if _, err := b2.ensure(ctx, wl); err != nil {
		t.Fatal(err)
	}
	var roles rbacv1.RoleList
	if err := c2.List(ctx, &roles); err != nil || len(roles.Items) != 0 {
		t.Errorf("a workloads grant made Roles %v, %v", roles.Items, err)
	}
}

// An existing secret-use Role that the broker did not make for this grant, or
// that allows other Secrets, fails the grant: the broker never adopts it.
func TestNamespaceOperatorSecretUseRefusesForeignRole(t *testing.T) {
	ctx := context.Background()
	g := namespaceOperatorGrant("prowlarr-secret")
	wider := secretUseRole(g, "downloads")
	wider.Rules[0].ResourceNames = []string{"prowlarr-secret", "cloudflare-token"}
	b, _ := fakeBroker(t, g, wider)
	_, err := b.ensure(ctx, g)
	if p := (permanent{}); err == nil || !errors.As(err, &p) {
		t.Fatalf("ensure over a wider Role = %v, want a permanent error", err)
	}
}
