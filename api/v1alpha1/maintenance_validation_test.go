package v1alpha1_test

import (
	"context"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

func maintenanceGrant() *v1alpha1.AccessGrant {
	g := kubeGrant()
	g.Spec.Type = v1alpha1.GrantMaintenance
	g.Spec.Kube = nil
	g.Spec.TTL = ttl(30 * time.Minute)
	g.Spec.Requester.SessionUID = types.UID("00000000-0000-0000-0000-000000000001")
	g.Spec.Maintenance = &v1alpha1.MaintenanceTask{Coordinator: v1alpha1.MaintenanceIdentity{Name: g.Spec.Requester.Session, UID: string(g.Spec.Requester.SessionUID), PodUID: "pod-uid"}, Node: "fixture-node", NodeUID: "node-uid", Resource: "fixture-device", Operation: "bounded maintenance", Image: "registry.example/tool@sha256:" + strings.Repeat("a", 64), CPUMillis: 100, MemoryMiB: 128, Capabilities: []string{"SYS_ADMIN"}}
	return g
}
func TestMaintenanceSchemaImmutableEnvelope(t *testing.T) {
	ctx := context.Background()
	g := maintenanceGrant()
	if err := k8s.Create(ctx, g); err != nil {
		t.Fatal(err)
	}
	for _, mut := range []func(*v1alpha1.AccessGrant){
		func(g *v1alpha1.AccessGrant) { g.Spec.Maintenance.NodeUID = "replaced" },
		func(g *v1alpha1.AccessGrant) { g.Spec.Maintenance.Coordinator.PodUID = "replaced" },
		func(g *v1alpha1.AccessGrant) { g.Spec.Maintenance.Capabilities = []string{"SYS_RAWIO"} },
		func(g *v1alpha1.AccessGrant) { g.Spec.TTL = ttl(time.Hour) },
		func(g *v1alpha1.AccessGrant) { g.Spec.Maintenance = nil },
	} {
		copy := g.DeepCopy()
		mut(copy)
		if err := k8s.Update(ctx, copy); !apierrors.IsInvalid(err) {
			t.Fatalf("scope mutation accepted: %v", err)
		}
	}
	g.Spec.Release = true
	if err := k8s.Update(ctx, g); err != nil {
		t.Fatal(err)
	}
	for _, mut := range []func(*v1alpha1.AccessGrant){
		func(g *v1alpha1.AccessGrant) { g.Spec.TTL = ttl(2 * time.Hour) },
		func(g *v1alpha1.AccessGrant) { g.Spec.Requester.SessionUID = "" },
		func(g *v1alpha1.AccessGrant) { g.Spec.Maintenance.Coordinator.UID = "other" },
		func(g *v1alpha1.AccessGrant) { g.Spec.Maintenance.Capabilities = []string{"SYS_PTRACE"} },
	} {
		bad := maintenanceGrant()
		mut(bad)
		if err := k8s.Create(ctx, bad); !apierrors.IsInvalid(err) {
			t.Fatalf("invalid envelope accepted: %v", err)
		}
	}
}
