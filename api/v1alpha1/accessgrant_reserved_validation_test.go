package v1alpha1_test

import (
	"context"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

func reservedSnapshotGrant() *v1alpha1.AccessGrant {
	g := kubeGrant()
	g.Spec.Kube = &v1alpha1.KubeGrant{Role: v1alpha1.RoleSecretsRead, AllNamespaces: true}
	return g
}

func reservedStorageGrant() *v1alpha1.AccessGrant {
	g := kubeGrant()
	g.Spec.Kube = &v1alpha1.KubeGrant{Role: v1alpha1.RoleStorage, Namespaces: []string{"database"}, StorageTargets: []v1alpha1.GrantStorageTarget{{Kind: "PersistentVolumeClaim", Name: "data", UID: "11111111-1111-1111-1111-111111111111"}}}
	return g
}

func TestReservedGrantSchemaRoundTrip(t *testing.T) {
	for _, create := range []func() *v1alpha1.AccessGrant{reservedSnapshotGrant, reservedStorageGrant} {
		g := create()
		if err := k8s.Create(context.Background(), g); err != nil {
			t.Fatal(err)
		}
		var got v1alpha1.AccessGrant
		if err := k8s.Get(context.Background(), client.ObjectKeyFromObject(g), &got); err != nil {
			t.Fatal(err)
		}
		if got.Spec.Kube.AllNamespaces != g.Spec.Kube.AllNamespaces || len(got.Spec.Kube.StorageTargets) != len(g.Spec.Kube.StorageTargets) {
			t.Fatal("reserved scope was pruned")
		}
	}
}

func TestReservedGrantSchemaRejectsWidening(t *testing.T) {
	for name, edit := range map[string]func(*v1alpha1.AccessGrant){
		"snapshot-workloads":        func(g *v1alpha1.AccessGrant) { g.Spec.Kube.Role = v1alpha1.RoleWorkloads },
		"snapshot-named-namespaces": func(g *v1alpha1.AccessGrant) { g.Spec.Kube.Namespaces = []string{"frontend"} },
		"snapshot-long-ttl":         func(g *v1alpha1.AccessGrant) { g.Spec.TTL = ttl(2 * time.Hour) },
	} {
		t.Run(name, func(t *testing.T) {
			g := reservedSnapshotGrant()
			edit(g)
			if err := k8s.Create(context.Background(), g); err == nil {
				t.Fatal("invalid snapshot scope accepted")
			}
		})
	}
	for name, edit := range map[string]func(*v1alpha1.AccessGrant){
		"wrong-role":       func(g *v1alpha1.AccessGrant) { g.Spec.Kube.Role = v1alpha1.RoleWorkloads },
		"second-namespace": func(g *v1alpha1.AccessGrant) { g.Spec.Kube.Namespaces = append(g.Spec.Kube.Namespaces, "frontend") },
		"no-uid":           func(g *v1alpha1.AccessGrant) { g.Spec.Kube.StorageTargets[0].UID = "" },
		"wrong-kind":       func(g *v1alpha1.AccessGrant) { g.Spec.Kube.StorageTargets[0].Kind = "Secret" },
		"duplicate-uid": func(g *v1alpha1.AccessGrant) {
			target := g.Spec.Kube.StorageTargets[0]
			target.Name = "other"
			g.Spec.Kube.StorageTargets = append(g.Spec.Kube.StorageTargets, target)
		},
		"long-ttl": func(g *v1alpha1.AccessGrant) { g.Spec.TTL = ttl(2 * time.Hour) },
	} {
		t.Run(name, func(t *testing.T) {
			g := reservedStorageGrant()
			edit(g)
			if err := k8s.Create(context.Background(), g); err == nil {
				t.Fatal("invalid storage target accepted")
			}
		})
	}
}
