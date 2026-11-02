package apiserver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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

func TestAggregateAllowanceRejectsFutureReceiptAndForeignStatus(t *testing.T) {
	_, second := assemblyCaptures(t)
	mem := &slotMem{cm: &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: AggregateAllowanceNamespace, Name: AggregateAllowanceName, ResourceVersion: "1"}, Data: map[string]string{"keep": "yes"}}}
	store := repairAllowanceStore(mem)
	now := time.Date(2026, 11, 2, 3, 10, 0, 0, time.UTC)
	base := cloneAllowanceReport(t, second)
	item := allowanceOpenAIItem(t, &base)
	observedAt, err := time.Parse(time.RFC3339Nano, *item.Observation.ProviderObservedAt)
	if err != nil || observedAt.After(now) || item.Freshness.State != providerallowance.FreshFresh {
		t.Fatalf("fixture is not a current provider time <= now: %+v", item.Freshness)
	}
	gap := canonicalAllowanceFixture(t, UnconfiguredAggregateDocument())
	if err := store.Put(context.Background(), gap, now); err != nil || mem.updates != 1 {
		t.Fatal("honest unknown gap", err, mem.updates)
	}
	held := mem.cm.Data[AggregateAllowanceDataKey]
	verifiedGap := UnconfiguredAggregateDocument()
	other := "other-account"
	verifiedGap.SourceStatuses[0].Binding = providerallowance.BindingRecord{State: providerallowance.BindingVerified, Generation: 1, AccountRef: &other}
	refuseAllowancePut(t, store, mem, verifiedGap, now, held)

	future := cloneAllowanceReport(t, second)
	futureItem := allowanceOpenAIItem(t, &future)
	futureItem.ReceivedAt = "2099-01-01T00:00:00Z"
	later := "2099-06-01T00:00:00Z"
	for i := range futureItem.Observation.Measures {
		if futureItem.Observation.Measures[i].Window.ResetPrecision == providerallowance.ResetInstant {
			futureItem.Observation.Measures[i].Window.ResetAt = &later
		}
	}
	futureStatus := allowanceOpenAIStatus(t, &future)
	if futureStatus.LastGood == nil {
		t.Fatal("fixture has no last good")
	}
	futureStatus.LastGood.ReceivedAt = futureItem.ReceivedAt
	refuseAllowancePut(t, store, mem, future, now, held)

	foreign := cloneAllowanceReport(t, second)
	foreignStatus := allowanceOpenAIStatus(t, &foreign)
	foreignStatus.Binding.AccountRef = &other
	refuseAllowancePut(t, store, mem, foreign, now, held)

	workspace := cloneAllowanceReport(t, second)
	workspaceName := "other-workspace"
	allowanceOpenAIStatus(t, &workspace).Binding.WorkspaceRef = &workspaceName
	refuseAllowancePut(t, store, mem, workspace, now, held)

	generation := cloneAllowanceReport(t, second)
	allowanceOpenAIStatus(t, &generation).Binding.Generation = 4
	refuseAllowancePut(t, store, mem, generation, now, held)

	producer := cloneAllowanceReport(t, second)
	allowanceOpenAIStatus(t, &producer).ProducerRef = "foreign-producer"
	refuseAllowancePut(t, store, mem, producer, now, held)

	incarnation := cloneAllowanceReport(t, second)
	incarnationItem := allowanceOpenAIItem(t, &incarnation)
	incarnationItem.Observation.ProducerIncarnation = "foreign-incarnation"
	incarnationItem.Freshness.State = providerallowance.FreshExpired
	for i := range incarnationItem.CurrentMeasures {
		incarnationItem.CurrentMeasures[i].State = providerallowance.CurrentUnknown
		incarnationItem.CurrentMeasures[i].Value = providerallowance.MeasureValue{State: providerallowance.ValueUnknown}
	}
	incarnationStatus := allowanceOpenAIStatus(t, &incarnation)
	incarnationStatus.Freshness.State = providerallowance.FreshExpired
	refuseAllowancePut(t, store, mem, incarnation, now, held)

	good := canonicalAllowanceFixture(t, second)
	if err := store.Put(context.Background(), good, now); err != nil || mem.updates != 2 {
		t.Fatal("current report", err, mem.updates)
	}
	stored := mem.cm.Data[AggregateAllowanceDataKey]
	if stored == held || !strings.Contains(stored, "fixture-account") {
		t.Fatal("current report was not stored")
	}
	again := cloneAllowanceReport(t, second)
	allowanceOpenAIStatus(t, &again).Binding.AccountRef = &other
	refuseAllowancePut(t, store, mem, again, now, stored)
}

func repairAllowanceStore(mem *slotMem) *AggregateAllowanceReportStore {
	account, workspace := "fixture-account", "fixture-workspace"
	return &AggregateAllowanceReportStore{
		Writer: mem, Live: mem, Namespace: AggregateAllowanceNamespace, Name: AggregateAllowanceName, DataKey: AggregateAllowanceDataKey,
		Contexts: []providerallowance.TrustedContext{{
			Provider: "openai", ProducerRef: "fixture-producer", ProducerIncarnation: "fixture-incarnation-1", BindingGeneration: 0,
			AccountRef: &account, WorkspaceRef: &workspace, BindingState: providerallowance.BindingVerified,
			SourceKind: providerallowance.SourceNativeRPC, SourceVersion: "fixture-v1",
		}},
		Registry: assemblyRegistry(), MaxAge: 30 * time.Minute,
	}
}

func refuseAllowancePut(t *testing.T, store *AggregateAllowanceReportStore, mem *slotMem, doc providerallowance.ReadResponse, now time.Time, held string) {
	t.Helper()
	updates := mem.updates
	raw := canonicalAllowanceFixture(t, doc)
	if err := store.Put(context.Background(), raw, now); !errors.Is(err, ErrAggregateRejected) || mem.updates != updates || mem.cm.Data[AggregateAllowanceDataKey] != held || mem.cm.Data["keep"] != "yes" {
		t.Fatalf("put refused as %v updates %d", err, mem.updates)
	}
}

func cloneAllowanceReport(t *testing.T, doc providerallowance.ReadResponse) providerallowance.ReadResponse {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	next, err := providerallowance.DecodeReadResponse(raw, providerallowance.FixtureParseLimits)
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func allowanceOpenAIItem(t *testing.T, doc *providerallowance.ReadResponse) *providerallowance.ReadItem {
	t.Helper()
	for i := range doc.Items {
		if doc.Items[i].Observation.Provider == "openai" {
			return &doc.Items[i]
		}
	}
	t.Fatal("missing openai item")
	return nil
}

func allowanceOpenAIStatus(t *testing.T, doc *providerallowance.ReadResponse) *providerallowance.SourceStatus {
	t.Helper()
	for i := range doc.SourceStatuses {
		if doc.SourceStatuses[i].Provider == "openai" {
			return &doc.SourceStatuses[i]
		}
	}
	t.Fatal("missing openai status")
	return nil
}
