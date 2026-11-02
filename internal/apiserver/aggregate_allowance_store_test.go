package apiserver

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/internal/providerallowance"
)

func TestAggregateAllowanceStoreReplayAndRefusals(t *testing.T) {
	mem := &slotMem{cm: &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: AggregateAllowanceNamespace, Name: AggregateAllowanceName, ResourceVersion: "1"}, Data: map[string]string{"keep": "yes"}}}
	store := &AggregateAllowanceReportStore{
		Writer: mem, Live: mem, Namespace: AggregateAllowanceNamespace, Name: AggregateAllowanceName, DataKey: AggregateAllowanceDataKey,
		Registry: providerallowance.NewStaticRegistry(providerallowance.RegistryParts{}), MaxAge: 30 * time.Minute,
	}
	empty, err := store.Read(context.Background())
	if err != nil || empty.Found || empty.Suppressed || len(empty.Raw) != 0 {
		t.Fatalf("empty view %+v %v", empty, err)
	}
	raw, err := json.Marshal(UnconfiguredAggregateDocument())
	if err != nil {
		t.Fatal(err)
	}
	again, err := providerallowance.DecodeReadResponse(raw, providerallowance.FixtureParseLimits)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := json.Marshal(again); err != nil || string(second) != string(raw) {
		t.Fatal("unconfigured document is not canonical")
	}
	now := time.Date(2026, 11, 2, 3, 10, 0, 0, time.UTC)
	if err := store.Put(context.Background(), raw, now); err != nil || mem.updates != 1 || mem.cm.Data["keep"] != "yes" {
		t.Fatal(err, mem.updates)
	}
	if err := store.Put(context.Background(), raw, now); err != nil || mem.updates != 1 {
		t.Fatal("replay wrote", err, mem.updates)
	}
	held := mem.cm.Data[AggregateAllowanceDataKey]
	var missing providerallowance.ReadResponse
	if err := json.Unmarshal(raw, &missing); err != nil {
		t.Fatal(err)
	}
	filtered := make([]providerallowance.SourceStatus, 0, len(missing.SourceStatuses))
	for _, status := range missing.SourceStatuses {
		if status.Provider != "cursor" {
			filtered = append(filtered, status)
		}
	}
	missing.SourceStatuses = filtered
	bad := canonicalAllowanceFixture(t, missing)
	if err := store.Put(context.Background(), bad, now); !errors.Is(err, ErrAggregateRejected) || mem.updates != 1 || mem.cm.Data[AggregateAllowanceDataKey] != held {
		t.Fatal("missing cursor", err, mem.updates)
	}
	fresh := UnconfiguredAggregateDocument()
	fresh.SourceStatuses[2].Freshness.State = providerallowance.FreshFresh
	freshRaw := canonicalAllowanceFixture(t, fresh)
	if err := store.Put(context.Background(), freshRaw, now); !errors.Is(err, ErrAggregateRejected) || mem.cm.Data[AggregateAllowanceDataKey] != held {
		t.Fatal("fresh gap", err)
	}
	mem.cm.Data[AggregateAllowanceDataKey] = "{"
	if err := store.Put(context.Background(), raw, now); !errors.Is(err, ErrAggregateRejected) || mem.cm.Data[AggregateAllowanceDataKey] != "{" || mem.updates != 1 {
		t.Fatal("undecodable slot was repaired", err, mem.updates)
	}
	mem.cm.Data[AggregateAllowanceDataKey] = held
	clash := &rvClash{slotMem: mem}
	moved := UnconfiguredAggregateDocument()
	moved.ServedAt = "2026-11-02T03:10:00Z"
	movedRaw := canonicalAllowanceFixture(t, moved)
	clashStore := *store
	clashStore.Writer = clash
	if err := clashStore.Put(context.Background(), movedRaw, now); !errors.Is(err, ErrAggregateConflict) || mem.cm.Data[AggregateAllowanceDataKey] != held {
		t.Fatal("conflict", err)
	}
}

type rvClash struct{ *slotMem }

func canonicalAllowanceFixture(t *testing.T, doc providerallowance.ReadResponse) []byte {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	again, err := providerallowance.DecodeReadResponse(raw, providerallowance.FixtureParseLimits)
	if err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(again)
	if err != nil || string(out) != string(raw) {
		t.Fatal("fixture is not canonical")
	}
	return out
}

func (c rvClash) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	c.cm.ResourceVersion = "999"
	return c.slotMem.Update(ctx, obj, opts...)
}
