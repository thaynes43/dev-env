package taskbudget

import (
	"context"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func kubeFixture(t *testing.T) (KubeStore, *Service, *Ledger) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	k := KubeStore{Client: c, Live: c, Namespace: "budget-system"}
	s := &Service{Store: k, Now: func() time.Time { return time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC) }}
	l, err := s.Create(context.Background(), "task-a", "host-a", "pod-a", "parent", Spec{SuccessCondition: "result", OverallSeconds: 2700, EffortSeconds: 3600, CheckpointSeconds: 600})
	if err != nil {
		t.Fatal(err)
	}
	return k, s, l
}

func TestRetainedConfigMapAndCAS(t *testing.T) {
	k, s, l := kubeFixture(t)
	ctx := context.Background()
	a, rv, err := k.Read(ctx, l.Binding.TaskUID)
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := k.Read(ctx, l.Binding.TaskUID)
	if err != nil {
		t.Fatal(err)
	}
	a.Reason = "first-write"
	if err := k.Update(ctx, a, rv); err != nil {
		t.Fatal(err)
	}
	b.Reason = "second-write"
	if err := k.Update(ctx, b, rv); !errors.Is(err, ErrConflict) {
		t.Fatal("stale CAS accepted", err)
	}
	var cm corev1.ConfigMap
	if err := k.Live.Get(ctx, client.ObjectKey{Namespace: k.Namespace, Name: ledgerName(l.Binding.TaskUID)}, &cm); err != nil {
		t.Fatal(err)
	}
	if len(cm.OwnerReferences) != 0 || cm.Labels[LedgerLabel] != "retained-v1" {
		t.Fatal("ephemeral ownership")
	}
	// Independent service instance reads the same retained object.
	s = &Service{Store: k, Now: s.Now}
	l, err = s.Observe(ctx, l.Binding)
	if err != nil || l.Reason != "first-write" {
		t.Fatal("restart lost history", err)
	}
}

func TestKubeStoreRejectsForeignOwnerAndUnknownWrite(t *testing.T) {
	k, _, l := kubeFixture(t)
	ctx := context.Background()
	var cm corev1.ConfigMap
	key := client.ObjectKey{Namespace: k.Namespace, Name: ledgerName(l.Binding.TaskUID)}
	if err := k.Client.Get(ctx, key, &cm); err != nil {
		t.Fatal(err)
	}
	cm.OwnerReferences = []metav1.OwnerReference{{APIVersion: "v1", Kind: "Pod", Name: "ephemeral", UID: "pod-uid"}}
	if err := k.Client.Update(ctx, &cm); err != nil {
		t.Fatal(err)
	}
	if _, _, err := k.Read(ctx, l.Binding.TaskUID); !errors.Is(err, ErrUnavailable) {
		t.Fatal("foreign owner accepted")
	}
	k, _, l = kubeFixture(t)
	_, rv, err := k.Read(ctx, l.Binding.TaskUID)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	base := k.Client
	k.Client = interceptor.NewClient(base.(client.WithWatch), interceptor.Funcs{Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
		calls++
		if err := c.Update(ctx, obj, opts...); err != nil {
			return err
		}
		return apierrors.NewTimeoutError("response lost", 1)
	}})
	if err := k.Update(ctx, l, rv); !errors.Is(err, ErrUnavailable) || calls != 1 {
		t.Fatal("unknown write retried")
	}
}
