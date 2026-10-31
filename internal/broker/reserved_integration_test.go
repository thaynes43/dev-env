package broker

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/grantattest"
)

func reservedBrokerCase(t *testing.T, class string) (*dgFixture, MaintenanceDecision) {
	t.Helper()
	f := dgCase(t, func(g *v1alpha1.AccessGrant, _ *v1alpha1.GrantPolicy) {
		switch class {
		case grantattest.ClassRD2:
			g.Spec.Type = v1alpha1.GrantBreakglass
			g.Spec.Kube = &v1alpha1.KubeGrant{Role: v1alpha1.RoleBreakglass}
		case grantattest.ClassRD3a:
			g.Spec.Kube = &v1alpha1.KubeGrant{Role: v1alpha1.RoleSecretsRead, Namespaces: []string{"frontend"}}
		case grantattest.ClassRD3b:
			g.Spec.Kube = &v1alpha1.KubeGrant{Role: v1alpha1.RoleSecretsRead, AllNamespaces: true}
		case grantattest.ClassRD4Storage:
			g.Spec.Kube = &v1alpha1.KubeGrant{Role: v1alpha1.RoleStorage, Namespaces: []string{"frontend"}, StorageTargets: []v1alpha1.GrantStorageTarget{{Kind: "PersistentVolumeClaim", Name: "data", UID: "object-uid"}}}
		}
	})
	f.b.ReservedDecisionClasses = []string{class}
	var requester v1alpha1.AgentSession
	if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: sessionNS, Name: "req"}, &requester); err != nil {
		t.Fatal(err)
	}
	requester.Status.Phase = v1alpha1.PhaseRunning
	if err := f.c.Update(context.Background(), &requester); err != nil {
		t.Fatal(err)
	}
	for _, ns := range []string{"frontend", "database", "dev-agents"} {
		if err := f.c.Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns, UID: types.UID("uid-" + ns)}}); err != nil {
			t.Fatal(err)
		}
	}
	g := f.grant()
	d := f.decision(true)
	var err error
	d.Reserved, err = NewReservedBinding(context.Background(), f.c, sessionNS, g, d, f.b.ReservedDecisionClasses, nil, "msg-tom-1")
	if err != nil {
		t.Fatal("bind", err)
	}
	d.Reach, err = NewReservedDelegateReach(context.Background(), f.c, sessionNS, g, d.Delegate, d.By, nil, ReservedAuthority(d.Reserved))
	if err != nil {
		t.Fatal("reach", err)
	}
	return f, d
}

func TestReservedBrokerCasesMaterializeAndCleanup(t *testing.T) {
	for _, class := range []string{grantattest.ClassRD2, grantattest.ClassRD3a, grantattest.ClassRD3b, grantattest.ClassRD4Storage} {
		t.Run(class, func(t *testing.T) {
			f, d := reservedBrokerCase(t, class)
			f.record(d)
			if _, err := f.step(3); err != nil {
				t.Fatal(err)
			}
			g := f.grant()
			if g.Status.Phase != v1alpha1.GrantActive || g.Status.Reserved == nil || g.Status.Reserved.EffectiveDigest != d.Reserved.EffectiveDigest || !strings.Contains(g.Status.Message, grantattest.ReservedWording) {
				t.Fatal("reserved decision did not become audited Active", g.Status)
			}
			var sa corev1.ServiceAccount
			if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: sessionNS, Name: g.Name}, &sa); err != nil || sa.Labels[v1alpha1.LabelPrefix+"grant-uid"] != string(g.UID) {
				t.Fatal("missing own UID-bound SA", err)
			}
			var crbs rbacv1.ClusterRoleBindingList
			if err := f.c.List(context.Background(), &crbs); err != nil {
				t.Fatal(err)
			}
			if (len(crbs.Items) == 1) != (class == grantattest.ClassRD2) {
				t.Fatal("wrong cluster binding", crbs.Items)
			}
			var rbs rbacv1.RoleBindingList
			if err := f.c.List(context.Background(), &rbs); err != nil {
				t.Fatal(err)
			}
			if class == grantattest.ClassRD3b && len(rbs.Items) != 2 {
				t.Fatal("snapshot did not produce individual namespace bindings", rbs.Items)
			}
			g.Spec.Release = true
			if err := f.c.Update(context.Background(), g); err != nil {
				t.Fatal(err)
			}
			if _, err := f.step(1); err != nil {
				t.Fatal(err)
			}
			if f.grant().Status.Phase != v1alpha1.GrantReleased {
				t.Fatal("release did not end grant")
			}
			if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: sessionNS, Name: g.Name}, &corev1.ServiceAccount{}); !apierrors.IsNotFound(err) {
				t.Fatal("release left own SA", err)
			}
			if err := f.c.List(context.Background(), &rbs); err != nil || len(rbs.Items) != 0 {
				t.Fatal("release left bindings", err, rbs.Items)
			}
			if err := f.c.List(context.Background(), &crbs); err != nil || len(crbs.Items) != 0 {
				t.Fatal("release left root binding", err, crbs.Items)
			}
		})
	}
}

func TestReservedBrokerRefusalsPreserveNoAuthority(t *testing.T) {
	for _, mode := range []string{"disabled", "unlisted", "wrong-actor", "wrong-requester", "wrong-grant", "wrong-digest", "wrong-ttl", "wrong-ref", "wrong-bounds", "changed-exclusions", "no-reach", "ordinary-reach", "self", "replaced-pod", "snapshot-replaced"} {
		t.Run(mode, func(t *testing.T) {
			f, d := reservedBrokerCase(t, grantattest.ClassRD3b)
			switch mode {
			case "disabled":
				f.b.ReservedDecisionClasses = nil
			case "unlisted":
				f.b.ReservedDecisionClasses = []string{grantattest.ClassRD2}
			case "wrong-actor":
				d.Reserved.ActorUID = "other"
			case "wrong-requester":
				d.Reserved.RequesterUID = "other"
			case "wrong-grant":
				d.Reserved.GrantUID = "other"
			case "wrong-digest":
				d.Digest = strings.Repeat("B", 43)
			case "wrong-ttl":
				d.TTL = 2 * time.Hour
			case "wrong-ref":
				d.Reserved.TomRef = "other-message"
			case "wrong-bounds":
				d.Reserved.BoundsRevision = "next"
			case "changed-exclusions":
				f.b.ReservedExcludedNamespaces = []string{"different"}
			case "no-reach":
				d.Reach = nil
			case "ordinary-reach":
				d.Reach.AuthorityKind = "delegatedPolicy"
			case "self":
				d.Delegate.Session = "req"
				d.Delegate.SessionUID = string(f.grant().Spec.Requester.SessionUID)
			case "replaced-pod":
				var p corev1.Pod
				if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: sessionNS, Name: "req"}, &p); err != nil {
					t.Fatal(err)
				}
				p.UID = "replacement"
				if err := f.c.Update(context.Background(), &p); err != nil {
					t.Fatal(err)
				}
			case "snapshot-replaced":
				var ns corev1.Namespace
				if err := f.c.Get(context.Background(), client.ObjectKey{Name: "frontend"}, &ns); err != nil {
					t.Fatal(err)
				}
				ns.UID = "replacement"
				if err := f.c.Update(context.Background(), &ns); err != nil {
					t.Fatal(err)
				}
			}
			f.record(d)
			if _, err := f.step(3); err != nil {
				t.Fatal(err)
			}
			if f.grant().Status.Phase == v1alpha1.GrantActive {
				t.Fatal("invalid reserved decision became Active")
			}
			var sa corev1.ServiceAccountList
			if err := f.c.List(context.Background(), &sa); err != nil || len(sa.Items) != 0 {
				t.Fatal("refusal created authority", err, sa.Items)
			}
		})
	}
}

func TestReservedSnapshotFutureAndActiveRevocation(t *testing.T) {
	for _, mode := range []string{"future", "replace", "remove-class", "expiry", "owner-collision"} {
		t.Run(mode, func(t *testing.T) {
			f, d := reservedBrokerCase(t, grantattest.ClassRD3b)
			f.record(d)
			if _, err := f.step(3); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "future":
				if err := f.c.Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "future", UID: "future-uid"}}); err != nil {
					t.Fatal(err)
				}
			case "replace":
				var ns corev1.Namespace
				if err := f.c.Get(context.Background(), client.ObjectKey{Name: "frontend"}, &ns); err != nil {
					t.Fatal(err)
				}
				ns.UID = "replacement"
				if err := f.c.Update(context.Background(), &ns); err != nil {
					t.Fatal(err)
				}
			case "remove-class":
				f.b.ReservedDecisionClasses = nil
			case "expiry":
				f.clk.Step(time.Hour)
			case "owner-collision":
				var rb rbacv1.RoleBinding
				if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: "frontend", Name: f.grant().Name}, &rb); err != nil {
					t.Fatal(err)
				}
				rb.Labels[v1alpha1.LabelPrefix+"grant-uid"] = "other"
				if err := f.c.Update(context.Background(), &rb); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.step(1); err != nil {
				t.Fatal(err)
			}
			g := f.grant()
			if mode == "future" {
				if g.Status.Phase != v1alpha1.GrantActive {
					t.Fatal("future namespace unnecessarily ended snapshot")
				}
				if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: "future", Name: g.Name}, &rbacv1.RoleBinding{}); !apierrors.IsNotFound(err) {
					t.Fatal("future namespace acquired authority", err)
				}
			} else if g.Status.Phase == v1alpha1.GrantActive {
				t.Fatal("changed or expired reserved authority remained Active")
			}
		})
	}
}

func TestReservedNewScopeNeverUsesStandingOrAttestedPolicies(t *testing.T) {
	f, _ := reservedBrokerCase(t, grantattest.ClassRD4Storage)
	g := f.grant()
	for _, attested := range []bool{false, true} {
		p := &v1alpha1.GrantPolicy{Spec: v1alpha1.GrantPolicySpec{Type: v1alpha1.GrantKube, Kube: &v1alpha1.KubePolicy{Roles: []string{v1alpha1.RoleStorage}, Namespaces: []string{"frontend"}}, MaxTTL: metav1.Duration{Duration: time.Hour}}}
		if attested {
			p.Spec.Attested = &v1alpha1.AttestedRequesters{Profiles: []string{"full"}}
		}
		if Matches(p, g) || MatchesAttested(p, g) || MatchAttested(g, []v1alpha1.GrantPolicy{*p}, func(*v1alpha1.GrantPolicy) bool { return true }) != nil {
			t.Fatal("reserved storage used standing/attested path")
		}
	}
}

func TestReservedDenialsRetainExactAuditAndMakeNoBindings(t *testing.T) {
	for _, class := range []string{grantattest.ClassRD2, grantattest.ClassRD3a, grantattest.ClassRD3b, grantattest.ClassRD4Storage} {
		t.Run(class, func(t *testing.T) {
			f, d := reservedBrokerCase(t, class)
			d.Approve = false
			var err error
			d.Reserved, err = NewReservedBinding(context.Background(), f.c, sessionNS, f.grant(), d, f.b.ReservedDecisionClasses, nil, "msg-tom-1")
			if err != nil {
				t.Fatal(err)
			}
			d.Reach, err = NewReservedDelegateReach(context.Background(), f.c, sessionNS, f.grant(), d.Delegate, d.By, nil, ReservedAuthority(d.Reserved))
			if err != nil {
				t.Fatal(err)
			}
			f.record(d)
			if _, err := f.step(3); err != nil {
				t.Fatal(err)
			}
			g := f.grant()
			if g.Status.Phase != v1alpha1.GrantDenied || g.Status.Reserved == nil || g.Status.DelegateReach == nil {
				t.Fatal("reserved denial lost audit", g.Status)
			}
			var accounts corev1.ServiceAccountList
			if err := f.c.List(context.Background(), &accounts); err != nil || len(accounts.Items) != 0 {
				t.Fatal("denial created authority", err)
			}
		})
	}
}

// Replace the Namespace in the precise gap between its pre-read and the
// RoleBinding Create. UID replacement uses a fake client only; no live deletion.
type reservedNamespaceRaceClient struct {
	client.Client
	replaced bool
}

func (c *reservedNamespaceRaceClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if _, ok := obj.(*rbacv1.RoleBinding); ok && obj.GetNamespace() == "frontend" && !c.replaced {
		var ns corev1.Namespace
		if err := c.Client.Get(ctx, client.ObjectKey{Name: "frontend"}, &ns); err != nil {
			return err
		}
		ns.UID = "replacement-during-create"
		if err := c.Client.Update(ctx, &ns); err != nil {
			return err
		}
		c.replaced = true
	}
	return c.Client.Create(ctx, obj, opts...)
}

func TestReservedSnapshotReplacementDuringCreateCleansOwnBinding(t *testing.T) {
	f, d := reservedBrokerCase(t, grantattest.ClassRD3b)
	f.record(d)
	race := &reservedNamespaceRaceClient{Client: f.c}
	f.b.Client = race
	if _, err := f.step(3); err != nil {
		t.Fatal(err)
	}
	g := f.grant()
	if !race.replaced || g.Status.Phase != v1alpha1.GrantFailed || g.Status.InstalledPodUID != "" {
		t.Fatal("namespace creation race escaped fence", g.Status)
	}
	var bindings rbacv1.RoleBindingList
	if err := f.c.List(context.Background(), &bindings); err != nil || len(bindings.Items) != 0 {
		t.Fatal("replacement retained own raced binding", err, bindings.Items)
	}
	if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: sessionNS, Name: g.Name}, &corev1.ServiceAccount{}); !apierrors.IsNotFound(err) {
		t.Fatal("creation race left the grant token identity", err)
	}
}

func TestReservedSnapshotNeverRecreatesAfterMaterialization(t *testing.T) {
	f, d := reservedBrokerCase(t, grantattest.ClassRD3b)
	f.record(d)
	if _, err := f.step(3); err != nil {
		t.Fatal(err)
	}
	g := f.grant()
	if g.Status.ServiceAccount != g.Name {
		t.Fatal("no durable pre-token fence")
	}
	// A missing binding and empty installed status also model a crash after
	// mint/install but before the installation receipt. The earlier materialized
	// marker must still prevent any new namespace binding.
	rb := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Namespace: "frontend", Name: g.Name}}
	if err := f.c.Delete(context.Background(), rb); err != nil {
		t.Fatal(err)
	}
	if _, err := f.step(1); err != nil {
		t.Fatal(err)
	}
	if f.grant().Status.Phase != v1alpha1.GrantFailed {
		t.Fatal("missing snapshot binding was recreated")
	}
	if err := f.c.Get(context.Background(), client.ObjectKeyFromObject(rb), rb); !apierrors.IsNotFound(err) {
		t.Fatal("snapshot gained fresh authority", err)
	}
}
