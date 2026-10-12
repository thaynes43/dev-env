package taskbudget

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
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

func TestKubeStoreRejectsForeignOwner(t *testing.T) {
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
}

// unknownWriteStore wraps the fixture client so its next ledger write has an
// unknown result: land says whether the write commits first, and between runs
// after the commit and before the lost response.
func unknownWriteStore(k KubeStore, calls *int, land bool, between func()) KubeStore {
	base := k.Client.(client.WithWatch)
	k.Client = interceptor.NewClient(base, interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			*calls++
			if land {
				if err := c.Create(ctx, obj, opts...); err != nil {
					return err
				}
			}
			if between != nil {
				between()
			}
			return apierrors.NewTimeoutError("response lost", 1)
		},
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			*calls++
			if land {
				if err := c.Update(ctx, obj, opts...); err != nil {
					return err
				}
			}
			if between != nil {
				between()
			}
			return apierrors.NewTimeoutError("response lost", 1)
		},
	})
	return k
}

// An unknown write is read back once and never replayed. Only this writer's
// exact data at a new resourceVersion confirms it.
func TestKubeStoreUnknownWriteNeedsExactReadback(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name       string
		land       bool
		supersede  bool
		wantErr    error
		wantWorker bool
	}{
		{name: "landed", land: true, wantWorker: true},
		{name: "not-landed", wantErr: ErrUnavailable},
		{name: "landed-then-superseded", land: true, supersede: true, wantErr: ErrUnavailable, wantWorker: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k, s, l := kubeFixture(t)
			base := k
			var between func()
			if tc.supersede {
				other := &Service{Store: base, Validator: receiptValidator{}, Now: func() time.Time { return s.Now().Add(time.Millisecond) }}
				between = func() {
					if _, err := other.Record(ctx, apiv1.TaskBudgetEvent{Binding: l.Binding, ID: "dispatch-other", Kind: "worker-start", WorkerID: "other"}); err != nil {
						t.Error("second writer", err)
					}
				}
			}
			calls := 0
			s.Store, s.Validator = unknownWriteStore(k, &calls, tc.land, between), receiptValidator{}
			_, err := s.Record(ctx, apiv1.TaskBudgetEvent{Binding: l.Binding, ID: "dispatch-child", Kind: "worker-start", WorkerID: "child"})
			if tc.wantErr == nil && err != nil || tc.wantErr != nil && !errors.Is(err, tc.wantErr) || calls != 1 {
				t.Fatalf("unknown write: err %v, %d calls", err, calls)
			}
			got, _, err := base.Read(ctx, l.Binding.TaskUID)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, w := range got.Workers {
				found = found || w.ID == "child"
			}
			if found != tc.wantWorker {
				t.Fatal("retained ledger does not match the write's real outcome", got.Workers)
			}
		})
	}
	t.Run("create", func(t *testing.T) {
		for _, land := range []bool{true, false} {
			k, _, _ := kubeFixture(t)
			calls := 0
			k = unknownWriteStore(k, &calls, land, nil)
			s := &Service{Store: k, Now: func() time.Time { return time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC) }}
			_, err := s.Create(ctx, "task-b", "host-b", "pod-b", "parent", Spec{SuccessCondition: "result", OverallSeconds: 2700, EffortSeconds: 3600, CheckpointSeconds: 600})
			if land && err != nil || !land && !errors.Is(err, ErrUnavailable) || calls != 1 {
				t.Fatalf("unknown create (landed %v): %v, %d calls", land, err, calls)
			}
		}
	})
}

// interleavedStore commits another writer's ledger change after this store's
// write is accepted and before the accepted response returns to the caller.
func interleavedStore(k KubeStore, interleaved *int, other func()) KubeStore {
	base := k.Client.(client.WithWatch)
	k.Client = interceptor.NewClient(base, interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if err := c.Create(ctx, obj, opts...); err != nil {
				return err
			}
			*interleaved++
			other()
			return nil
		},
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if err := c.Update(ctx, obj, opts...); err != nil {
				return err
			}
			if *interleaved == 0 {
				*interleaved++
				other()
			}
			return nil
		},
	})
	return k
}

// A write the API server accepted is durable. Another writer committing next
// supersedes it; it does not turn the accepted write into unknown authority.
func TestKubeStoreAcceptedWriteSurvivesInterleavedWriter(t *testing.T) {
	ctx := context.Background()
	t.Run("update", func(t *testing.T) {
		k, s, l := kubeFixture(t)
		other := &Service{Store: k, Validator: receiptValidator{}, Now: func() time.Time { return s.Now().Add(time.Millisecond) }}
		interleaved := 0
		s.Store = interleavedStore(k, &interleaved, func() {
			if _, err := other.Record(ctx, apiv1.TaskBudgetEvent{Binding: l.Binding, ID: "dispatch-sibling", Kind: "worker-start", WorkerID: "sibling"}); err != nil {
				t.Error("second writer", err)
			}
		})
		s.Validator = receiptValidator{}
		if _, err := s.Record(ctx, apiv1.TaskBudgetEvent{Binding: l.Binding, ID: "dispatch-child", Kind: "worker-start", WorkerID: "child"}); err != nil || interleaved != 1 {
			t.Fatal("accepted write reported unavailable", err, interleaved)
		}
		got, _, err := k.Read(ctx, l.Binding.TaskUID)
		if err != nil || len(got.Workers) != 2 || len(got.Events) != 2 {
			t.Fatalf("interleaved writers lost history: %+v %v", got, err)
		}
	})
	t.Run("create", func(t *testing.T) {
		k, _, _ := kubeFixture(t)
		now := func() time.Time { return time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC) }
		other := &Service{Store: k, Validator: receiptValidator{}, Now: func() time.Time { return now().Add(time.Millisecond) }}
		interleaved := 0
		s := &Service{Store: interleavedStore(k, &interleaved, func() {
			b := Binding{TaskUID: "task-b", HostID: "host-b", PodUID: "pod-b", Epoch: 1, Deadline: now().Add(2700 * time.Second)}
			if _, err := other.Record(ctx, apiv1.TaskBudgetEvent{Binding: b, ID: "dispatch-early", Kind: "worker-start", WorkerID: "early"}); err != nil {
				t.Error("second writer", err)
			}
		}), Now: now}
		created, err := s.Create(ctx, "task-b", "host-b", "pod-b", "parent", Spec{SuccessCondition: "result", OverallSeconds: 2700, EffortSeconds: 3600, CheckpointSeconds: 600})
		if err != nil || created == nil || interleaved != 1 {
			t.Fatal("accepted create reported unavailable", err)
		}
	})
}

// Heartbeat evidence is never validated. Large heartbeats must not consume the
// ledger's byte bound and then refuse the worker's stop receipt.
func TestLargeHeartbeatsCannotExhaustLedgerBytes(t *testing.T) {
	k, s, l := kubeFixture(t)
	ctx := context.Background()
	s.Validator = receiptValidator{}
	if _, err := s.Record(ctx, apiv1.TaskBudgetEvent{Binding: l.Binding, ID: "dispatch-w1", Kind: "worker-start", WorkerID: "w1"}); err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("x", 7000)
	for i := range 120 {
		hb := apiv1.TaskBudgetEvent{Binding: l.Binding, ID: fmt.Sprintf("heartbeat-%d", i), Kind: "heartbeat",
			Evidence: apiv1.TaskBudgetEvidence{ID: "hb", Kind: "agent", Reference: big}}
		if _, err := s.Record(ctx, hb); err != nil {
			t.Fatalf("heartbeat %d: %v", i, err)
		}
	}
	stop := apiv1.TaskBudgetEvent{Binding: l.Binding, ID: "stop-w1", Kind: "worker-stop", WorkerID: "w1",
		Evidence: apiv1.TaskBudgetEvidence{ID: "stop-w1", Kind: "supervised-tree-stop", Reference: "fixture:verified:w1"}}
	got, err := s.Record(ctx, stop)
	if err != nil || got.Workers[0].Active || len(got.Events) != 2 {
		t.Fatalf("stop receipt refused after heartbeats: %v", err)
	}
	if retained, _, err := k.Read(ctx, l.Binding.TaskUID); err != nil || len(retained.Events) != 2 || retained.Workers[0].Active {
		t.Fatal("heartbeats retained or stop lost", err)
	}
}
