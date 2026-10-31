package broker

import (
	"context"
	"strings"
	"testing"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

func TestAttestedMaintenanceCreatorsApprove(t *testing.T) {
	for _, tc := range []struct {
		name, parent string
		creators     []string
	}{
		{"the exact 223503 top-level shape", amCreator, []string{amCreator}},
		{"an explicitly configured creator", "ops/maintenance-launcher", []string{"ops/maintenance-launcher"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := amCase(t, func(g *v1alpha1.AccessGrant, _ *v1alpha1.GrantPolicy) {
				g.Spec.Requester.Session = "haynes-ops-1029-223503"
				g.Spec.Maintenance.Coordinator.Name = g.Spec.Requester.Session
			})
			f.b.AttestedMaintenanceCreators = tc.creators
			f.liveRequester(func(s *v1alpha1.AgentSession) { s.Spec.Parent = tc.parent })
			if f.sess.Labels[v1alpha1.LabelDepth] != "0" {
				t.Fatalf("positive fixture has depth %q", f.sess.Labels[v1alpha1.LabelDepth])
			}
			wantPolicyApproved(t, f.pendingThenAttest())
			f.step(1)
			if g := f.grant(); g.Status.Phase != v1alpha1.GrantActive || !f.executorExists() {
				t.Fatalf("the listed creator did not pass the active recheck: %+v", g.Status)
			}
		})
	}
}

func TestAttestedMaintenanceCreatorsRefuse(t *testing.T) {
	for _, tc := range []struct {
		name, parent, depth string
		creators            []string
	}{
		{"the v1 client", "dev/dev-env", "0", []string{amCreator}},
		{"the workbench", "dev-agents/dev-env-workbench", "0", []string{amCreator}},
		{"a workbench client", "dev-env-system/dev-env-workbench-client", "0", []string{amCreator}},
		{"a coordinator caller", "dev-env-system/dev-env-coordinator", "0", []string{amCreator}},
		{"a coordinator host", "codex-host", "0", []string{amCreator}},
		{"a child", "haynes-ops-1029-223503", "1", []string{amCreator}},
		{"a child with depth zero", "haynes-ops-1029-223503", "0", []string{amCreator}},
		{"a listed creator at depth one", amCreator, "1", []string{amCreator}},
		{"no parent", "", "0", []string{amCreator}},
		{"no depth", amCreator, "", []string{amCreator}},
		{"a malformed depth", amCreator, "invalid", []string{amCreator}},
		{"a noncanonical zero depth", amCreator, "00", []string{amCreator}},
		{"another namespace", "other/dev-env-human", "0", []string{amCreator}},
		{"an unlisted human creator", amCreator, "0", []string{"ops/maintenance-launcher"}},
		{"a nil creator list", amCreator, "0", nil},
		{"an empty creator list", amCreator, "0", []string{}},
		{"an invalid parent even when listed", "dev-env-system/dev-env-human/extra", "0", []string{"dev-env-system/dev-env-human/extra"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := amCase(t)
			f.b.AttestedMaintenanceCreators = tc.creators
			f.liveRequester(func(s *v1alpha1.AgentSession) {
				s.Spec.Parent = tc.parent
				if tc.depth == "" {
					delete(s.Labels, v1alpha1.LabelDepth)
				} else {
					s.Labels[v1alpha1.LabelDepth] = tc.depth
				}
			})
			f.pendingThenAttest()
			wantWaiting(t, f)
			var np networkingv1.NetworkPolicy
			if err := f.c.Get(context.Background(), f.key, &np); !apierrors.IsNotFound(err) {
				t.Fatalf("network policy exists for a refused requester: %v", err)
			}
			// The creator restriction applies only to the attested route.
			f.human(true)
			f.step(2)
			if g := f.grant(); g.Status.Phase != v1alpha1.GrantActive || g.Status.ApprovedBy != amHuman || !f.executorExists() {
				t.Fatalf("the human route did not retain its authority: %+v", g.Status)
			}
		})
	}
}

func TestAttestedMaintenanceCreatorsActiveRecheck(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*amFixture)
	}{
		{"creator removed from a nonempty list", func(f *amFixture) { f.b.AttestedMaintenanceCreators = []string{"ops/another-creator"} }},
		{"nil list", func(f *amFixture) { f.b.AttestedMaintenanceCreators = nil }},
		{"empty list", func(f *amFixture) { f.b.AttestedMaintenanceCreators = []string{} }},
		{"parent removed", func(f *amFixture) {
			f.liveRequester(func(s *v1alpha1.AgentSession) { s.Spec.Parent = "" })
		}},
		{"depth removed", func(f *amFixture) {
			f.liveRequester(func(s *v1alpha1.AgentSession) { delete(s.Labels, v1alpha1.LabelDepth) })
		}},
		{"noncanonical zero depth", func(f *amFixture) {
			f.liveRequester(func(s *v1alpha1.AgentSession) { s.Labels[v1alpha1.LabelDepth] = "00" })
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := amCase(t)
			wantPolicyApproved(t, f.pendingThenAttest())
			tc.change(f)
			f.step(1)
			g := f.grant()
			if g.Status.Phase != v1alpha1.GrantReleased || !strings.Contains(g.Status.Message, "maintenance approval no longer holds") || f.executorExists() {
				t.Fatalf("the invalid creator binding retained execution authority: %+v", g.Status)
			}
			var np networkingv1.NetworkPolicy
			if err := f.c.Get(context.Background(), f.key, &np); !apierrors.IsNotFound(err) {
				t.Fatalf("network policy exists after the creator recheck failed: %v", err)
			}
		})
	}
}

func TestAttestedMaintenanceCreatorRemovalStopsExecutor(t *testing.T) {
	ctx := context.Background()
	f := amCase(t)
	wantPolicyApproved(t, f.pendingThenAttest())
	f.step(1)
	var executor corev1.Pod
	if err := f.c.Get(ctx, f.key, &executor); err != nil {
		t.Fatal(err)
	}
	executor.Status.Phase = corev1.PodRunning
	if err := f.c.Status().Update(ctx, &executor); err != nil {
		t.Fatal(err)
	}

	f.b.AttestedMaintenanceCreators = []string{"ops/another-creator"}
	f.step(1)
	g := f.grant()
	if g.Status.Phase != v1alpha1.GrantActive || g.Status.Maintenance == nil || g.Status.Maintenance.Executor != "StopPending" {
		t.Fatalf("creator removal did not enter the stop path: %+v", g.Status)
	}
	if err := f.c.Get(ctx, f.key, &executor); err != nil {
		t.Fatal(err)
	}
	if executor.Spec.ActiveDeadlineSeconds == nil || *executor.Spec.ActiveDeadlineSeconds != 1 {
		t.Fatalf("executor retained its deadline: %v", executor.Spec.ActiveDeadlineSeconds)
	}
	// The API's executor gate requires Running. The broker keeps StopPending
	// until the node and container evidence proves that execution has ended.
	f.step(1)
	if g := f.grant(); g.Status.Phase != v1alpha1.GrantActive || g.Status.Maintenance.Executor != "StopPending" {
		t.Fatalf("the running executor was prematurely declared stopped: %+v", g.Status)
	}

	// Supply the existing stop path's evidence; only then may the lease end.
	executor.Status.Phase = corev1.PodFailed
	executor.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "maintenance", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{FinishedAt: metav1.NewTime(f.clk.Now())}}}}
	if err := f.c.Status().Update(ctx, &executor); err != nil {
		t.Fatal(err)
	}
	var node corev1.Node
	if err := f.c.Get(ctx, client.ObjectKey{Name: amNode}, &node); err != nil {
		t.Fatal(err)
	}
	node.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}
	if err := f.c.Status().Update(ctx, &node); err != nil {
		t.Fatal(err)
	}
	lease := &coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{Namespace: "kube-node-lease", Name: node.Name, OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Node", Name: node.Name, UID: node.UID}}}, Spec: coordinationv1.LeaseSpec{HolderIdentity: ptr.To(node.Name), RenewTime: &metav1.MicroTime{Time: f.clk.Now()}}}
	if err := f.c.Create(ctx, lease); err != nil {
		t.Fatal(err)
	}
	f.step(1)
	g = f.grant()
	if g.Status.Phase != v1alpha1.GrantReleased || g.Status.Maintenance.Executor != "Terminated" || f.executorExists() {
		t.Fatalf("the removed creator's lease did not end after termination: %+v", g.Status)
	}
	var np networkingv1.NetworkPolicy
	if err := f.c.Get(ctx, f.key, &np); !apierrors.IsNotFound(err) {
		t.Fatalf("network policy survived executor termination: %v", err)
	}
}
