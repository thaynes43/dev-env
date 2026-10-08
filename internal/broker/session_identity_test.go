package broker

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/egress"
)

func TestBrokerRequesterSessionUID(t *testing.T) {
	for _, typ := range []v1alpha1.GrantType{v1alpha1.GrantKube, v1alpha1.GrantEgress, v1alpha1.GrantBreakglass} {
		for _, phase := range []v1alpha1.GrantPhase{v1alpha1.GrantPending, v1alpha1.GrantActive} {
			for _, bound := range []bool{true, false} {
				identity := "uid-bound"
				if !bound {
					identity = "legacy-unbound"
				}
				t.Run(string(typ)+"/"+string(phase)+"/"+identity, func(t *testing.T) {
					ctx := context.Background()
					inst := &recorder{}
					b, g, pod, _, mints := installationFixture(t, inst, 0)
					var original v1alpha1.AgentSession
					key := client.ObjectKeyFromObject(pod)
					if err := b.Client.Get(ctx, key, &original); err != nil {
						t.Fatal(err)
					}
					g.Spec.Type = typ
					if bound {
						g.Spec.Requester.SessionUID = original.UID
					}
					switch typ {
					case v1alpha1.GrantEgress:
						g.Spec.Kube = nil
						g.Spec.Egress = &v1alpha1.EgressGrant{CIDRs: []string{"192.0.2.8/32"}, Ports: tcp(8443)}
					case v1alpha1.GrantBreakglass:
						g.Spec.Kube.Role = v1alpha1.RoleBreakglass
						g.Spec.Kube.Namespaces = nil
					}
					if err := b.Client.Update(ctx, g); err != nil {
						t.Fatal(err)
					}
					g.Status.Phase = phase
					if err := b.Client.Status().Update(ctx, g); err != nil {
						t.Fatal(err)
					}
					// An active grant must work for the original UID before replacement.
					reconcileInstallation(t, b, g)
					if g.Status.Phase != phase {
						t.Fatal("grant did not retain its phase for the original session")
					}
					if err := b.Client.Delete(ctx, &original); err != nil {
						t.Fatal(err)
					}
					if err := b.Client.Delete(ctx, pod); err != nil {
						t.Fatal(err)
					}
					replacement := original.DeepCopy()
					replacement.ResourceVersion, replacement.UID = "", "replacement-session-uid"
					if err := b.Client.Create(ctx, replacement); err != nil {
						t.Fatal(err)
					}
					pod.ResourceVersion, pod.UID = "", "replacement-pod-uid"
					pod.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(replacement, v1alpha1.GroupVersion.WithKind("AgentSession"))}
					if err := b.Client.Create(ctx, pod); err != nil {
						t.Fatal(err)
					}
					if bound {
						// A stale cached original cannot override the live replacement UID.
						b.Client = interceptor.NewClient(b.Client.(client.WithWatch), interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
							if s, ok := obj.(*v1alpha1.AgentSession); ok {
								original.DeepCopyInto(s)
								return nil
							}
							return c.Get(ctx, key, obj, opts...)
						}})
					}
					reconcileInstallation(t, b, g)
					want := phase
					if bound {
						want = v1alpha1.GrantDenied
						if phase == v1alpha1.GrantActive {
							want = v1alpha1.GrantReleased
						}
					}
					if g.Status.Phase != want {
						t.Fatalf("replacement phase %s, want %s", g.Status.Phase, want)
					}
					var resources []client.Object
					if typ == v1alpha1.GrantEgress {
						resources = []client.Object{egress.Object(g.Namespace, g.Name)}
					} else {
						resources = []client.Object{&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: g.Namespace, Name: g.Name}}}
						if typ == v1alpha1.GrantBreakglass {
							resources = append(resources, &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: g.Name}})
						} else {
							resources = append(resources, &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Namespace: "frontend", Name: g.Name}})
						}
					}
					for _, obj := range resources {
						err := b.APIReader.Get(ctx, client.ObjectKeyFromObject(obj), obj)
						if !bound && phase == v1alpha1.GrantActive {
							if err != nil {
								t.Fatalf("legacy grant lost %T: %v", obj, err)
							}
						} else if !apierrors.IsNotFound(err) {
							t.Fatalf("ended or pending grant retained %T: %v", obj, err)
						}
					}
					installs, removes := inst.of(g.Name)
					wantMints := 0
					if typ != v1alpha1.GrantEgress && phase == v1alpha1.GrantActive {
						wantMints = 1
						if !bound {
							wantMints = 2
							if g.Status.InstalledPodUID != string(pod.UID) {
								t.Fatal("unbound historical grant did not retain installation compatibility")
							}
						}
					}
					if *mints != wantMints || len(installs) != wantMints || len(removes) != 0 {
						t.Fatal("replacement inherited a UID-bound token or received predecessor cleanup")
					}
				})
			}
		}
	}
}

func TestBrokerRequesterUIDCleanupDeletingOriginal(t *testing.T) {
	ctx := context.Background()
	inst := &recorder{}
	b, g, pod, _, _ := installationFixture(t, inst, 0)
	var s v1alpha1.AgentSession
	if err := b.Client.Get(ctx, client.ObjectKeyFromObject(pod), &s); err != nil {
		t.Fatal(err)
	}
	g.Spec.Requester.SessionUID = s.UID
	if err := b.Client.Update(ctx, g); err != nil {
		t.Fatal(err)
	}
	reconcileInstallation(t, b, g)
	s.Finalizers = []string{"fixture/rescue"}
	if err := b.Client.Update(ctx, &s); err != nil {
		t.Fatal(err)
	}
	if err := b.Client.Delete(ctx, &s); err != nil {
		t.Fatal(err)
	}
	reconcileInstallation(t, b, g)
	_, removes := inst.of(g.Name)
	if g.Status.Phase != v1alpha1.GrantReleased || len(removes) != 1 || removes[0] != pod.UID {
		t.Fatal("UID fence prevented token cleanup from the deleting original session")
	}
}
