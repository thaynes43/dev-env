package broker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/egress"
	"github.com/thaynes43/dev-env/internal/grantdecision"
)

func prepareManualGrant(t *testing.T, b *Broker, g *v1alpha1.AccessGrant, pod *corev1.Pod) {
	t.Helper()
	ctx := context.Background()
	var session v1alpha1.AgentSession
	if err := b.Client.Get(ctx, client.ObjectKeyFromObject(pod), &session); err != nil {
		t.Fatal(err)
	}
	g.Spec.Requester.SessionUID = session.UID
	binding, err := grantdecision.Bind(g, pod.UID)
	if err != nil {
		t.Fatal(err)
	}
	principal := "human/dev-env-system/dev-env-human"
	h := grantdecision.Handoff{Binding: binding, Principal: principal, Decision: "approve"}
	raw, _ := json.Marshal(h)
	g.Annotations = map[string]string{grantdecision.HandoffAnnotation: string(raw)}
	if err := b.Client.Update(ctx, g); err != nil {
		t.Fatal(err)
	}
	g.Status.ApprovedBy = principal
	if err := b.Client.Status().Update(ctx, g); err != nil {
		t.Fatal(err)
	}
}

func TestManualKubeReplacementBetweenPodReadsNeverMints(t *testing.T) {
	inst := &recorder{}
	b, g, pod, _, mints := installationFixture(t, inst, 0)
	prepareManualGrant(t, b, g, pod)
	replacement := pod.DeepCopy()
	replacement.UID = "replacement-pod"
	if _, _, err := b.install(context.Background(), g, replacement, b.now()); err == nil || *mints != 0 {
		t.Fatal("direct installation accepted a replacement pod")
	}
	reads := 0
	underlying := b.APIReader.(client.WithWatch)
	b.APIReader = interceptor.NewClient(underlying, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if p, ok := obj.(*corev1.Pod); ok {
			reads++
			if reads == 2 {
				if err := c.Get(ctx, key, p, opts...); err != nil {
					return err
				}
				p.UID = "replacement-pod"
				return nil
			}
		}
		return c.Get(ctx, key, obj, opts...)
	}})
	reconcileInstallation(t, b, g)
	if reads < 2 || *mints != 0 || g.Status.Phase != v1alpha1.GrantReleased {
		t.Fatal("replacement pod inherited a manual kube grant")
	}
	installs, _ := inst.of(g.Name)
	if len(installs) != 0 {
		t.Fatal("credential delivered to replacement")
	}
}

func TestManualEgressLabelsBeforePolicyAndReleasesOnReplacement(t *testing.T) {
	ctx := context.Background()
	b, g, pod, _, _ := installationFixture(t, nil, 0)
	g.Spec.Type = v1alpha1.GrantEgress
	g.Spec.Kube = nil
	g.Spec.Egress = &v1alpha1.EgressGrant{CIDRs: []string{"192.0.2.8/32"}, Ports: tcp(8443)}
	prepareManualGrant(t, b, g, pod)
	checked := false
	b.Client = interceptor.NewClient(b.Client.(client.WithWatch), interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		if u, ok := obj.(*unstructured.Unstructured); ok && u.GroupVersionKind() == egress.GVK {
			var current corev1.Pod
			if err := c.Get(ctx, client.ObjectKeyFromObject(pod), &current); err != nil {
				return err
			}
			if current.Labels[grantdecision.PodUIDLabel] != string(pod.UID) {
				t.Fatal("policy created before UID label")
			}
			checked = true
		}
		return c.Create(ctx, obj, opts...)
	}})
	reconcileInstallation(t, b, g)
	if !checked || g.Status.Phase != v1alpha1.GrantActive {
		t.Fatal("manual egress was not materialized")
	}
	var policy unstructured.Unstructured
	policy.SetGroupVersionKind(egress.GVK)
	if err := b.APIReader.Get(ctx, client.ObjectKeyFromObject(egress.Object(g.Namespace, g.Name)), &policy); err != nil || !egress.OwnedBy(&policy, g.Namespace, g) {
		t.Fatal("UID-bound policy missing")
	}
	if err := b.Client.Delete(ctx, pod); err != nil {
		t.Fatal(err)
	}
	replacement := pod.DeepCopy()
	replacement.UID, replacement.ResourceVersion = "replacement-pod", ""
	replacement.Labels = map[string]string{v1alpha1.LabelSession: g.Spec.Requester.Session}
	replacement.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(&v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: pod.Name, UID: g.Spec.Requester.SessionUID}}, v1alpha1.GroupVersion.WithKind("AgentSession"))}
	if err := b.Client.Create(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	reconcileInstallation(t, b, g)
	if g.Status.Phase != v1alpha1.GrantReleased {
		t.Fatal("replacement did not release manual egress")
	}
	if err := b.APIReader.Get(ctx, client.ObjectKeyFromObject(egress.Object(g.Namespace, g.Name)), &policy); !apierrors.IsNotFound(err) {
		t.Fatal("UID-bound policy was not removed")
	}
}

func TestManualActiveTransientPodReadDoesNotRelease(t *testing.T) {
	b, g, pod, _, _ := installationFixture(t, &recorder{}, 0)
	prepareManualGrant(t, b, g, pod)
	original := b.APIReader.(client.WithWatch)
	b.APIReader = interceptor.NewClient(original, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if _, ok := obj.(*corev1.Pod); ok {
			return errors.New("temporary read failure")
		}
		return c.Get(ctx, key, obj, opts...)
	}})
	var session v1alpha1.AgentSession
	if err := original.Get(context.Background(), client.ObjectKeyFromObject(pod), &session); err != nil {
		t.Fatal(err)
	}
	if _, err := b.active(context.Background(), g, &session, b.now()); err == nil {
		t.Fatal("transient failure did not retry")
	}
	if g.Status.Phase != v1alpha1.GrantActive {
		t.Fatal("transient failure released grant")
	}
}
