package grantattest

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// namespaceOperatorGrant is DESIGN-042 A2's request: the namespace operator in
// downloads with two named Secrets.
func namespaceOperatorGrant() *v1alpha1.AccessGrant {
	return &v1alpha1.AccessGrant{Spec: v1alpha1.AccessGrantSpec{
		Requester: v1alpha1.GrantRequester{Session: "s1", SessionUID: "11111111-1111-1111-1111-111111111111"},
		Type:      v1alpha1.GrantKube,
		Kube: &v1alpha1.KubeGrant{
			Role: v1alpha1.RoleNamespaceOperator, Namespaces: []string{"downloads"},
			Secrets: []string{"prowlarr-secret", "myanonamouse-secret"},
		},
		TTL: metav1.Duration{Duration: 30 * time.Minute},
	}}
}

// A grant that names no Secret digests byte for byte as before secrets joined
// the digest (DESIGN-042 5.1): a nil list and an empty one both give
// TestScopeDigestUnchangedWithoutMaintenance's pinned value, so no attestation
// recorded before S5 changes meaning.
func TestScopeDigestUnchangedWithoutSecrets(t *testing.T) {
	const want = "tsyAaGCwtSXeBzGftjA-UyPyQ7Y-pGKLpTyB2xZPTS0"
	for name, secrets := range map[string][]string{"nil": nil, "empty": {}} {
		g := grant()
		g.Spec.Kube.Secrets = secrets
		if got := ScopeDigest(g); got != want {
			t.Errorf("%s secrets: kube grant digest %q, want %q", name, got, want)
		}
	}
	if got, want := ScopeDigest(maintenanceGrant()), "h0HTRVZJUOqQNexGFDMcVrhtHAHbcUxuEyE9uTItlOM"; got != want {
		t.Errorf("maintenance grant digest %q, want %q", got, want)
	}
}

// The digest of a namespace-operator grant is pinned: renaming the field or
// changing its order rule would void every attestation and delegate decision
// already recorded for one, so it must be a deliberate change.
func TestScopeDigestNamedSecretsPinned(t *testing.T) {
	if got, want := ScopeDigest(namespaceOperatorGrant()), "FQt07vt2SFLj8XU-XVFtTEd4O_OZNYjywZU-Yxestr4"; got != want {
		t.Fatalf("namespace-operator grant digest %q, want %q", got, want)
	}
}

// The named Secrets are in the digest, sorted: their order does not change it,
// and adding, dropping or renaming one does. The digest does not sort the
// grant's own list.
func TestScopeDigestCoversNamedSecrets(t *testing.T) {
	d := ScopeDigest(namespaceOperatorGrant())
	reordered := namespaceOperatorGrant()
	reordered.Spec.Kube.Secrets = []string{"myanonamouse-secret", "prowlarr-secret"}
	if ScopeDigest(reordered) != d {
		t.Fatal("Secret order changed the digest")
	}
	if got := namespaceOperatorGrant().Spec.Kube.Secrets[0]; got != "prowlarr-secret" {
		t.Fatalf("the digest sorted the grant's own list: %s", got)
	}
	for name, edit := range map[string]func(*v1alpha1.KubeGrant){
		"add":    func(k *v1alpha1.KubeGrant) { k.Secrets = append(k.Secrets, "qbittorrent-secret") },
		"drop":   func(k *v1alpha1.KubeGrant) { k.Secrets = k.Secrets[:1] },
		"rename": func(k *v1alpha1.KubeGrant) { k.Secrets[1] = "other-secret" },
		"none":   func(k *v1alpha1.KubeGrant) { k.Secrets = nil },
		// A Secret moved into the namespace list (or back) is a different scope.
		"moved": func(k *v1alpha1.KubeGrant) {
			k.Namespaces, k.Secrets = []string{"downloads", "prowlarr-secret"}, []string{"myanonamouse-secret"}
		},
	} {
		g := namespaceOperatorGrant()
		edit(g.Spec.Kube)
		if ScopeDigest(g) == d {
			t.Errorf("%s did not change the digest", name)
		}
	}
}
