package grantattest

import (
	"github.com/thaynes43/dev-env/api/v1alpha1"
	"slices"
	"testing"
)

func TestReservedScopeDigestBindsMarkerSnapshotAndObjectUIDs(t *testing.T) {
	g := &v1alpha1.AccessGrant{Spec: v1alpha1.AccessGrantSpec{Kube: &v1alpha1.KubeGrant{Role: v1alpha1.RoleStorage, Namespaces: []string{"database"}, Reserved: true, StorageTargets: []v1alpha1.GrantStorageTarget{{Kind: "PersistentVolumeClaim", Name: "a", UID: "uid-a"}, {Kind: "StatefulSet", Name: "b", UID: "uid-b"}}}}}
	digest := ScopeDigest(g)
	for _, edit := range []func(*v1alpha1.AccessGrant){func(g *v1alpha1.AccessGrant) { g.Spec.Kube.Reserved = false }, func(g *v1alpha1.AccessGrant) { g.Spec.Kube.AllNamespaces = true }, func(g *v1alpha1.AccessGrant) { g.Spec.Kube.StorageTargets[0].UID = "replacement" }, func(g *v1alpha1.AccessGrant) { g.Spec.Kube.StorageTargets[0].Kind = "VolumeSnapshot" }, func(g *v1alpha1.AccessGrant) { g.Spec.Kube.Namespaces = []string{"other"} }} {
		copy := g.DeepCopy()
		edit(copy)
		if ScopeDigest(copy) == digest {
			t.Fatal("reserved scope change retained digest")
		}
	}
	copy := g.DeepCopy()
	slices.Reverse(copy.Spec.Kube.StorageTargets)
	if ScopeDigest(copy) != digest {
		t.Fatal("storage order changed set digest")
	}
	if g.Spec.Kube.StorageTargets[0].Name != "a" {
		t.Fatal("digest mutated owned scope")
	}
}
