package taskclaim

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/internal/testenv"
)

// The bounds use metadata-only, label-selected lists. This proves that path on
// a real API server with the generated CRDs, as the operator ServiceAccount
// holding only the get, create and list verbs haynes-ops grants it.
func TestEnvtestBoundsAndSameWorkUnderOperatorRole(t *testing.T) {
	env, err := testenv.Start()
	if err != nil {
		t.Fatalf("envtest: %v", err)
	}
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Log("envtest stop:", err)
		}
	})
	ctx := context.Background()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{coord.AddToScheme, corev1.AddToScheme, rbacv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	admin, err := client.New(env.Config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	const ns, sa = "dev-env-system", "dev-env-operator"
	for _, obj := range []client.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}},
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: sa}},
		&rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: sa}, Rules: []rbacv1.PolicyRule{{
			APIGroups: []string{coord.GroupVersion.Group}, Resources: []string{"taskclaims", "publishedcontexts", "inboxmessages"}, Verbs: []string{"get", "create", "list"}}}},
		&rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: sa}, RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: sa},
			Subjects: []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Namespace: ns, Name: sa}}},
	} {
		if err := admin.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	cfg := rest.CopyConfig(env.Config)
	cfg.Impersonate = rest.ImpersonationConfig{UserName: "system:serviceaccount:" + ns + ":" + sa}
	operator, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	store := KubeStore{Client: operator, Live: operator, Namespace: ns, Limits: Limits{PerSession: 2, PerProject: 3}}
	claim := func(owner, key string) coord.TaskClaimSpec {
		s := validSpec()
		s.Owner.Name, s.Owner.UID, s.Actor.PodName = owner, owner+"-uid", owner
		s.Identity.TaskKey = key
		return s
	}
	for _, key := range []string{"issue:1", "issue:2"} {
		if _, created, err := store.Create(ctx, claim("first", key)); err != nil || !created {
			t.Fatal("claim under bound refused", key, created, err)
		}
	}
	if _, _, err := store.Create(ctx, claim("first", "issue:3")); !errors.Is(err, ErrLimit) {
		t.Fatal("per-session bound not enforced", err)
	}
	if got, created, err := store.Create(ctx, claim("first", "issue:2")); err != nil || created || got.Spec.Owner.Name != "first" {
		t.Fatal("retry at the bound not confirmed", created, err)
	}
	if _, created, err := store.Create(ctx, claim("second", "issue:4")); err != nil || !created {
		t.Fatal("other owner refused under the project bound", created, err)
	}
	if _, _, err := store.Create(ctx, claim("second", "issue:5")); !errors.Is(err, ErrLimit) {
		t.Fatal("per-project bound not enforced", err)
	}
	// The same GitHub issue under a renamed repository key returns its owner.
	renamed := claim("second", "issue:1")
	renamed.Identity.Repository = "renamed"
	got, created, err := store.Create(ctx, renamed)
	if err != nil || created || got.Spec.Owner.Name != "first" || got.Spec.Identity.Repository != "repo" {
		t.Fatal("renamed repository key minted a second owner", created, err)
	}
	// A claim from before the work label is found by the scan of unlabelled claims.
	var old coord.TaskClaim
	if err := admin.Get(ctx, client.ObjectKey{Namespace: ns, Name: Name(claim("first", "issue:2").Identity)}, &old); err != nil {
		t.Fatal(err)
	}
	old.Labels = map[string]string{ProjectLabel: "sample"}
	if err := admin.Update(ctx, &old); err != nil {
		t.Fatal(err)
	}
	renamed.Identity.TaskKey = "issue:2"
	got, created, err = store.Create(ctx, renamed)
	if err != nil || created || got.UID != old.UID {
		t.Fatal("renamed repository key minted a second owner of a pre-label claim", created, err)
	}
	// The other bounded kinds resolve under the same role.
	for _, kind := range []string{"PublishedContextList", "InboxMessageList"} {
		if err := CheckRoom(ctx, operator, kind, ns, "sample", renamed.Owner, Limits{PerSession: 1, PerProject: 1}); err != nil {
			t.Fatal(kind, err)
		}
	}
}
