package providerallowance_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/providerallowance"
	"github.com/thaynes43/dev-env/web/portal"
	"github.com/thaynes43/dev-env/web/portal/session"
)

const (
	collectionCanaryToken  = "token=super-secret-token"
	collectionCanaryCookie = "cookie=super-secret-cookie"
)

func TestCollectionDisabledMakesNoCalls(t *testing.T) {
	clock := &fakeClock{now: collectionStart}
	wait := &fakeWait{}
	acquire := &scriptAcquire{t: t}
	bounds := collectionBounds(time.Minute, 2)
	bounds.Enabled = false
	collection := mustCollection(t, bounds, nil, testRegistry(t, true), clock, wait, acquire)
	afterNew := clock.calls
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := collection.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if acquire.total != 0 || len(wait.calls) != 0 || clock.calls != afterNew {
		t.Fatalf("disabled run acquired=%d waited=%d clock=%d/%d", acquire.total, len(wait.calls), clock.calls, afterNew)
	}
	doc, raw, err := collection.Snapshot(collectionServed)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Schema != providerallowance.SchemaRead || doc.ServedAt != "2026-11-02T03:06:00Z" || len(doc.Items) != 0 {
		t.Fatalf("doc=%+v", doc)
	}
	if doc.ServedAt == doc.SourceStatuses[0].LastAttempt.AttemptedAt {
		t.Fatal("serving time collapsed into the idle attempt")
	}
	got := map[string]providerallowance.SourceStatus{}
	for _, status := range doc.SourceStatuses {
		got[status.Provider] = status
	}
	for _, provider := range []string{"cursor", "anthropic", "openai"} {
		status, ok := got[provider]
		if !ok || status.Availability != providerallowance.AvailabilityUnconfigured || status.Freshness.State != providerallowance.FreshUnknown || status.Binding.State != providerallowance.BindingUnknown {
			t.Fatalf("%s %#v", provider, status)
		}
	}
	if strings.Contains(string(raw), collectionCanaryToken) {
		t.Fatal("canary in disabled snapshot")
	}
}

func TestCollectionDistinctCyclesAndReplay(t *testing.T) {
	clock := &fakeClock{now: collectionStart}
	wait := &fakeWait{}
	account := "fixture-account"
	first := collectionObs(t, obsOpts{id: "obs-a", provider: "openai", decimal: "11", sequence: 1, captured: "2026-11-02T03:00:00Z"})
	second := collectionObs(t, obsOpts{id: "obs-b", provider: "openai", decimal: "22", sequence: 2, captured: "2026-11-02T03:02:00Z", observed: "2026-11-02T03:01:00Z"})
	acquire := &scriptAcquire{t: t, steps: map[string][]acquireStep{
		"openai": {
			func(context.Context, providerallowance.SourceSpec) (providerallowance.Acquisition, error) {
				clock.Advance(collectionSuccessLatency)
				return providerallowance.Acquisition{Observation: first}, nil
			},
			func(context.Context, providerallowance.SourceSpec) (providerallowance.Acquisition, error) {
				clock.Advance(collectionSuccessLatency)
				return providerallowance.Acquisition{Observation: second}, nil
			},
		},
	}}
	collection := mustCollection(t, collectionBounds(30*time.Minute, 2), []providerallowance.SourceSpec{openaiSpec(&account)}, testRegistry(t, true), clock, wait, acquire)
	account = "other-account"
	if err := collection.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if acquire.total != 2 || len(wait.calls) != 1 || wait.calls[0] != 5*time.Second {
		t.Fatalf("calls=%d waits=%v", acquire.total, wait.calls)
	}
	doc, raw, err := collection.Snapshot(collectionServed)
	if err != nil {
		t.Fatal(err)
	}
	item := onlyItem(t, doc, "openai")
	if item.Observation.ObservationID != "obs-b" || item.Observation.CapturedAt != "2026-11-02T03:02:00Z" || deref(item.Observation.ProviderObservedAt) != "2026-11-02T03:01:00Z" {
		t.Fatalf("second capture was not retained: %+v", item.Observation)
	}
	if item.ReceivedAt == item.Observation.CapturedAt || item.ReceivedAt == doc.ServedAt || deref(item.Observation.ProviderObservedAt) == doc.ServedAt {
		t.Fatalf("times collapsed received=%s captured=%s provider=%s served=%s", item.ReceivedAt, item.Observation.CapturedAt, deref(item.Observation.ProviderObservedAt), doc.ServedAt)
	}
	if deref(item.CurrentMeasures[0].Value.Decimal) != "22" || strings.Contains(string(raw), "obs-a") {
		t.Fatalf("cycle identity raw=%s", raw)
	}
	if deref(item.Binding.AccountRef) != "fixture-account" {
		t.Fatal("caller account string was aliased into the cache")
	}

	replayClock := &fakeClock{now: collectionStart}
	replayWait := &fakeWait{}
	replay := &scriptAcquire{t: t, steps: map[string][]acquireStep{
		"openai": {
			func(context.Context, providerallowance.SourceSpec) (providerallowance.Acquisition, error) {
				replayClock.Advance(collectionSuccessLatency)
				return providerallowance.Acquisition{Observation: append([]byte(nil), first...)}, nil
			},
			func(context.Context, providerallowance.SourceSpec) (providerallowance.Acquisition, error) {
				replayClock.Advance(collectionSuccessLatency)
				return providerallowance.Acquisition{Observation: append([]byte(nil), first...)}, nil
			},
		},
	}}
	replayed := mustCollection(t, collectionBounds(30*time.Minute, 2), []providerallowance.SourceSpec{openaiSpec(sp("fixture-account"))}, testRegistry(t, true), replayClock, replayWait, replay)
	if err := replayed.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	doc, _, err = replayed.Snapshot(collectionServed)
	if err != nil {
		t.Fatal(err)
	}
	item = onlyItem(t, doc, "openai")
	status := statusBy(t, doc, "openai")
	wantReceived := collectionStart.Add(collectionSuccessLatency).UTC().Format(time.RFC3339Nano)
	wantCompleted := collectionStart.Add(2 * collectionSuccessLatency).UTC().Format(time.RFC3339Nano)
	if item.Observation.ObservationID != "obs-a" || item.Observation.CapturedAt != "2026-11-02T03:00:00Z" || item.ReceivedAt != wantReceived {
		t.Fatalf("replay minted a capture: received=%s obs=%+v", item.ReceivedAt, item.Observation)
	}
	if status.LastGood == nil || status.LastGood.CapturedAt != item.Observation.CapturedAt || status.LastGood.ReceivedAt != item.ReceivedAt || deref(status.LastAttempt.CompletedAt) != wantCompleted {
		t.Fatalf("replay times %+v attempt=%+v", status.LastGood, status.LastAttempt)
	}
}

func TestCollectionConflictWithholdsSameIdentity(t *testing.T) {
	clock := &fakeClock{now: collectionStart}
	original := collectionObs(t, obsOpts{id: "obs-same", provider: "openai", decimal: "11", sequence: 4})
	changed := collectionObs(t, obsOpts{id: "obs-same", provider: "openai", decimal: "99", sequence: 4})
	acquire := &scriptAcquire{t: t, steps: map[string][]acquireStep{
		"openai": {
			successStep(clock, original),
			successStep(clock, changed),
		},
	}}
	collection := mustCollection(t, collectionBounds(30*time.Minute, 2), []providerallowance.SourceSpec{openaiSpec(sp("fixture-account"))}, testRegistry(t, true), clock, &fakeWait{}, acquire)
	if err := collection.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	doc, raw, err := collection.Snapshot(collectionServed)
	if err != nil {
		t.Fatal(err)
	}
	item := onlyItem(t, doc, "openai")
	status := statusBy(t, doc, "openai")
	if item.Observation.ObservationID != "obs-same" || deref(item.Observation.Measures[0].Value.Decimal) != "11" {
		t.Fatalf("original history changed: %+v", item.Observation.Measures)
	}
	if item.CurrentMeasures[0].State != providerallowance.CurrentConflict || item.CurrentMeasures[0].Value.Decimal != nil || status.Availability != providerallowance.AvailabilityConflict || deref(status.LastAttempt.Code) != "acquisition_conflict" {
		t.Fatalf("conflict item=%+v status=%+v", item.CurrentMeasures, status)
	}
	if strings.Contains(string(raw), `"decimal":"99"`) {
		t.Fatal("conflicting payload was stored")
	}
}

func TestCollectionFreshnessResetAndUnknownAccount(t *testing.T) {
	t.Run("stale", func(t *testing.T) {
		doc := collectOne(t, collectionBounds(time.Minute, 1), obsOpts{id: "stale", provider: "openai", decimal: "12.5", observed: "2026-11-02T02:59:00Z"})
		item := onlyItem(t, doc, "openai")
		if item.Freshness.State != providerallowance.FreshStale || deref(item.CurrentMeasures[0].Value.Decimal) != "12.5" {
			t.Fatalf("%+v %+v", item.Freshness, item.CurrentMeasures)
		}
	})
	t.Run("future", func(t *testing.T) {
		doc := collectOne(t, collectionBounds(30*time.Minute, 1), obsOpts{id: "future", provider: "openai", decimal: "12.5", observed: "2026-11-02T04:00:00Z"})
		item := onlyItem(t, doc, "openai")
		if item.Freshness.State != providerallowance.FreshExpired || item.CurrentMeasures[0].State != providerallowance.CurrentUnknown || item.CurrentMeasures[0].Value.Decimal != nil || deref(item.Observation.Measures[0].Value.Decimal) != "12.5" {
			t.Fatalf("%+v %+v", item.Freshness, item.CurrentMeasures)
		}
	})
	t.Run("missing-provider-time", func(t *testing.T) {
		doc := collectOne(t, collectionBounds(30*time.Minute, 1), obsOpts{id: "missing-time", provider: "openai", decimal: "12.5", omitObserved: true})
		item := onlyItem(t, doc, "openai")
		if item.Freshness.State != providerallowance.FreshUnknown || item.Freshness.Basis != providerallowance.BasisCapture || item.CurrentMeasures[0].Value.Decimal != nil || deref(item.Observation.Measures[0].Value.Decimal) != "12.5" {
			t.Fatalf("%+v current=%+v", item.Freshness, item.CurrentMeasures[0])
		}
	})
	t.Run("exact-reset", func(t *testing.T) {
		doc := collectOne(t, collectionBounds(30*time.Minute, 1), obsOpts{id: "reset-exact", provider: "openai", decimal: "12.5", reset: "2026-11-02T03:06:00Z", precision: providerallowance.ResetInstant})
		item := onlyItem(t, doc, "openai")
		if item.CurrentMeasures[0].State != providerallowance.CurrentExpired || item.CurrentMeasures[0].Value.Decimal != nil || deref(item.Observation.Measures[0].Value.Decimal) != "12.5" {
			t.Fatalf("reset replenished %+v obs=%s", item.CurrentMeasures[0], deref(item.Observation.Measures[0].Value.Decimal))
		}
	})
	t.Run("date-only-reset", func(t *testing.T) {
		doc := collectOne(t, collectionBounds(30*time.Minute, 1), obsOpts{id: "reset-date", provider: "openai", decimal: "12.5", reset: "2026-11-02T03:06:00Z", precision: providerallowance.ResetDisplayOnly})
		item := onlyItem(t, doc, "openai")
		if item.CurrentMeasures[0].State != providerallowance.CurrentKnown || deref(item.CurrentMeasures[0].Value.Decimal) != "12.5" {
			t.Fatalf("display reset was treated as an instant %+v", item.CurrentMeasures[0])
		}
	})
	t.Run("unknown-account", func(t *testing.T) {
		opts := obsOpts{id: "unknown-account", provider: "openai", decimal: "12.5", binding: providerallowance.BindingUnknown, omitAccount: true}
		doc := collectOne(t, collectionBounds(30*time.Minute, 1), opts)
		item := onlyItem(t, doc, "openai")
		if item.Binding.State != providerallowance.BindingUnknown || item.Validation.State == providerallowance.ValidationSourceFields || item.CurrentMeasures[0].State == providerallowance.CurrentKnown || deref(item.Observation.Measures[0].Value.Decimal) != "12.5" {
			t.Fatalf("unknown account became quota %+v %+v", item.Binding, item.CurrentMeasures[0])
		}
	})
	t.Run("forged-binding", func(t *testing.T) {
		clock := &fakeClock{now: collectionStart}
		raw := collectionObs(t, obsOpts{id: "forged", provider: "openai", decimal: "12.5", binding: providerallowance.BindingVerified})
		acquire := &scriptAcquire{t: t, steps: map[string][]acquireStep{"openai": {successStep(clock, raw)}}}
		spec := openaiSpec(sp("fixture-account"))
		spec.BindingState = providerallowance.BindingUnknown
		collection := mustCollection(t, collectionBounds(30*time.Minute, 1), []providerallowance.SourceSpec{spec}, testRegistry(t, true), clock, &fakeWait{}, acquire)
		if err := collection.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
		doc, _, err := collection.Snapshot(collectionServed)
		if err != nil {
			t.Fatal(err)
		}
		item := onlyItem(t, doc, "openai")
		status := statusBy(t, doc, "openai")
		if item.Binding.State != providerallowance.BindingUnknown || item.Validation.State != providerallowance.ValidationRejected || item.CurrentMeasures[0].Value.Decimal != nil || status.LastGood != nil {
			t.Fatalf("forged binding was trusted item=%+v status=%+v", item.Binding, status.LastGood)
		}
		if deref(item.Observation.Measures[0].Value.Decimal) != "12.5" {
			t.Fatal("historical decimal was dropped")
		}
	})
}

func TestCollectionFutureAtReceiptStaysUnknown(t *testing.T) {
	later := collectionServed.Add(2 * time.Minute)
	t.Run("future-provider", func(t *testing.T) {
		clock := &fakeClock{now: collectionStart}
		raw := collectionObs(t, obsOpts{id: "future-provider", provider: "openai", decimal: "11", observed: "2026-11-02T03:07:00Z"})
		acquire := &scriptAcquire{t: t, steps: map[string][]acquireStep{"openai": {successStep(clock, raw)}}}
		collection := mustCollection(t, collectionBounds(30*time.Minute, 1), []providerallowance.SourceSpec{openaiSpec(sp("fixture-account"))}, testRegistry(t, true), clock, &fakeWait{}, acquire)
		if err := collection.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
		calls := acquire.total
		first, _, err := collection.Snapshot(collectionServed)
		if err != nil {
			t.Fatal(err)
		}
		second, _, err := collection.Snapshot(later)
		if err != nil {
			t.Fatal(err)
		}
		if acquire.total != calls {
			t.Fatal("later read acquired")
		}
		assertReceiptFutureUnknown(t, first, "11", collectionCaptured, "2026-11-02T03:07:00Z")
		assertReceiptFutureUnknown(t, second, "11", collectionCaptured, "2026-11-02T03:07:00Z")
		if onlyItem(t, second, "openai").Freshness.State != providerallowance.FreshExpired || onlyItem(t, second, "openai").Freshness.Basis != providerallowance.BasisProvider {
			t.Fatalf("future provider time was reclassified %+v", onlyItem(t, second, "openai").Freshness)
		}
	})
	t.Run("future-capture", func(t *testing.T) {
		clock := &fakeClock{now: collectionStart}
		raw := collectionObs(t, obsOpts{id: "future-capture", provider: "openai", decimal: "11", captured: "2026-11-02T03:07:00Z", observed: collectionObserved})
		acquire := &scriptAcquire{t: t, steps: map[string][]acquireStep{"openai": {successStep(clock, raw)}}}
		collection := mustCollection(t, collectionBounds(30*time.Minute, 1), []providerallowance.SourceSpec{openaiSpec(sp("fixture-account"))}, testRegistry(t, true), clock, &fakeWait{}, acquire)
		if err := collection.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
		calls := acquire.total
		first, _, err := collection.Snapshot(collectionServed)
		if err != nil {
			t.Fatal(err)
		}
		second, _, err := collection.Snapshot(later)
		if err != nil {
			t.Fatal(err)
		}
		if acquire.total != calls {
			t.Fatal("later read acquired")
		}
		assertReceiptFutureUnknown(t, first, "11", "2026-11-02T03:07:00Z", collectionObserved)
		assertReceiptFutureUnknown(t, second, "11", "2026-11-02T03:07:00Z", collectionObserved)
		item := onlyItem(t, second, "openai")
		if item.Freshness.State != providerallowance.FreshExpired || item.Freshness.Basis != providerallowance.BasisCapture || item.Freshness.AsOf != "2026-11-02T03:07:00Z" {
			t.Fatalf("future capture was reclassified %+v", item.Freshness)
		}
	})
	t.Run("replay-keeps-receipt", func(t *testing.T) {
		clock := &fakeClock{now: collectionStart}
		raw := collectionObs(t, obsOpts{id: "future-replay", provider: "openai", decimal: "11", observed: "2026-11-02T03:07:00Z"})
		acquire := &scriptAcquire{t: t, steps: map[string][]acquireStep{
			"openai": {
				successStep(clock, raw),
				func(context.Context, providerallowance.SourceSpec) (providerallowance.Acquisition, error) {
					clock.Advance(4 * time.Minute)
					return providerallowance.Acquisition{Observation: append([]byte(nil), raw...)}, nil
				},
			},
		}}
		collection := mustCollection(t, collectionBounds(30*time.Minute, 2), []providerallowance.SourceSpec{openaiSpec(sp("fixture-account"))}, testRegistry(t, true), clock, &fakeWait{}, acquire)
		if err := collection.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
		calls := acquire.total
		doc, _, err := collection.Snapshot(collectionServed.Add(5 * time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if acquire.total != calls {
			t.Fatal("replay read acquired")
		}
		wantReceived := collectionStart.Add(collectionSuccessLatency).UTC().Format(time.RFC3339Nano)
		item := onlyItem(t, doc, "openai")
		status := statusBy(t, doc, "openai")
		if item.ReceivedAt != wantReceived || status.LastGood == nil || status.LastGood.ReceivedAt != wantReceived {
			t.Fatalf("replay moved the receipt item=%s lastGood=%+v", item.ReceivedAt, status.LastGood)
		}
		if deref(status.LastAttempt.CompletedAt) == wantReceived {
			t.Fatal("replay did not record the later attempt")
		}
		assertReceiptFutureUnknown(t, doc, "11", collectionCaptured, "2026-11-02T03:07:00Z")
		if item.Observation.ObservationID != "future-replay" {
			t.Fatal("replay replaced the original observation")
		}
	})
	t.Run("later-valid-recovers", func(t *testing.T) {
		clock := &fakeClock{now: collectionStart}
		first := collectionObs(t, obsOpts{id: "future-then", provider: "openai", decimal: "11", sequence: 1, observed: "2026-11-02T03:07:00Z"})
		second := collectionObs(t, obsOpts{id: "valid-later", provider: "openai", decimal: "22", sequence: 2, captured: "2026-11-02T03:02:00Z", observed: "2026-11-02T03:01:00Z"})
		acquire := &scriptAcquire{t: t, steps: map[string][]acquireStep{"openai": {successStep(clock, first), successStep(clock, second)}}}
		collection := mustCollection(t, collectionBounds(30*time.Minute, 2), []providerallowance.SourceSpec{openaiSpec(sp("fixture-account"))}, testRegistry(t, true), clock, &fakeWait{}, acquire)
		if err := collection.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
		doc, raw, err := collection.Snapshot(collectionServed)
		if err != nil {
			t.Fatal(err)
		}
		item := onlyItem(t, doc, "openai")
		wantReceived := collectionStart.Add(2 * collectionSuccessLatency).UTC().Format(time.RFC3339Nano)
		if item.Observation.ObservationID != "valid-later" || item.Observation.CapturedAt != "2026-11-02T03:02:00Z" || deref(item.Observation.ProviderObservedAt) != "2026-11-02T03:01:00Z" || item.ReceivedAt != wantReceived {
			t.Fatalf("valid acquisition did not replace the future receipt: received=%s obs=%+v", item.ReceivedAt, item.Observation)
		}
		if item.CurrentMeasures[0].State != providerallowance.CurrentKnown || deref(item.CurrentMeasures[0].Value.Decimal) != "22" || item.Freshness.State != providerallowance.FreshFresh {
			t.Fatalf("valid chronology stayed unknown %+v freshness=%+v", item.CurrentMeasures[0], item.Freshness)
		}
		if strings.Contains(string(raw), "future-then") || strings.Contains(string(raw), `"decimal":"11"`) {
			t.Fatal("the future observation stayed actionable or current")
		}
	})
}

func assertReceiptFutureUnknown(t *testing.T, doc providerallowance.ReadResponse, decimal, captured, observed string) {
	t.Helper()
	item := onlyItem(t, doc, "openai")
	if item.CurrentMeasures[0].State != providerallowance.CurrentUnknown || item.CurrentMeasures[0].Value.Decimal != nil {
		t.Fatalf("serving view became actionable: %+v", item.CurrentMeasures[0])
	}
	if item.Freshness.State == providerallowance.FreshFresh || item.Freshness.State == providerallowance.FreshStale {
		t.Fatalf("freshness became current: %+v", item.Freshness)
	}
	if deref(item.Observation.Measures[0].Value.Decimal) != decimal || item.Observation.CapturedAt != captured || deref(item.Observation.ProviderObservedAt) != observed {
		t.Fatalf("historical observation changed captured=%s observed=%s decimal=%s", item.Observation.CapturedAt, deref(item.Observation.ProviderObservedAt), deref(item.Observation.Measures[0].Value.Decimal))
	}
}

func TestCollectionRejectedCrossAccountReplayStaysRejected(t *testing.T) {
	reg := fixtureOnlyRegistry(t)
	t.Run("available", func(t *testing.T) {
		raw := mutateObs(t, collectionObs(t, obsOpts{id: "cross-account", provider: "openai", decimal: "12.5"}), func(obs *providerallowance.Observation) {
			obs.Scope.AccountRef = sp("other-account")
		})
		doc, _ := collectCycles(t, reg, 2, raw, raw)
		assertRejectedCrossAccount(t, doc, providerallowance.AvailabilityAvailable)
	})
	t.Run("partial", func(t *testing.T) {
		raw := mutateObs(t, collectionObs(t, obsOpts{id: "cross-account-partial", provider: "openai", decimal: "12.5"}), func(obs *providerallowance.Observation) {
			obs.Scope.AccountRef = sp("other-account")
			obs.Availability = providerallowance.AvailabilityPartial
		})
		doc, _ := collectCycles(t, reg, 2, raw, raw)
		assertRejectedCrossAccount(t, doc, providerallowance.AvailabilityPartial)
	})
	t.Run("accepted-replay-keeps-success", func(t *testing.T) {
		raw := collectionObs(t, obsOpts{id: "obs-a", provider: "openai", decimal: "11", sequence: 1, captured: "2026-11-02T03:00:00Z"})
		doc, _ := collectCycles(t, reg, 2, raw, raw)
		item := onlyItem(t, doc, "openai")
		status := statusBy(t, doc, "openai")
		wantReceived := collectionStart.Add(collectionSuccessLatency).UTC().Format(time.RFC3339Nano)
		wantCompleted := collectionStart.Add(2 * collectionSuccessLatency).UTC().Format(time.RFC3339Nano)
		if item.Observation.ObservationID != "obs-a" || item.ReceivedAt != wantReceived || deref(status.LastAttempt.CompletedAt) != wantCompleted {
			t.Fatalf("accepted replay moved the receipt received=%s completed=%s", item.ReceivedAt, deref(status.LastAttempt.CompletedAt))
		}
		if status.LastAttempt.Result != providerallowance.AttemptSuccess || status.Availability != providerallowance.AvailabilityAvailable || deref(status.LastAttempt.Code) != "" || status.LastGood == nil || status.LastGood.ReceivedAt != wantReceived {
			t.Fatalf("accepted replay was rejected: %+v availability=%s lastGood=%+v", status.LastAttempt, status.Availability, status.LastGood)
		}
		if item.Freshness.State != providerallowance.FreshFresh || item.Binding.State != providerallowance.BindingVerified || deref(item.Binding.AccountRef) != "fixture-account" || item.CurrentMeasures[0].State != providerallowance.CurrentKnown || deref(item.CurrentMeasures[0].Value.Decimal) != "11" {
			t.Fatalf("accepted replay lost the current quota freshness=%+v binding=%+v current=%+v", item.Freshness, item.Binding, item.CurrentMeasures[0])
		}
	})
	t.Run("later-distinct-valid-recovers", func(t *testing.T) {
		first := mutateObs(t, collectionObs(t, obsOpts{id: "rejected-cross", provider: "openai", decimal: "11", sequence: 1}), func(obs *providerallowance.Observation) {
			obs.Scope.AccountRef = sp("other-account")
		})
		second := collectionObs(t, obsOpts{id: "valid-later", provider: "openai", decimal: "22", sequence: 2, captured: "2026-11-02T03:02:00Z", observed: "2026-11-02T03:01:00Z"})
		doc, raw := collectCycles(t, reg, 2, first, second)
		item := onlyItem(t, doc, "openai")
		status := statusBy(t, doc, "openai")
		wantReceived := collectionStart.Add(2 * collectionSuccessLatency).UTC().Format(time.RFC3339Nano)
		if item.Observation.ObservationID != "valid-later" || item.Observation.CapturedAt != "2026-11-02T03:02:00Z" || deref(item.Observation.ProviderObservedAt) != "2026-11-02T03:01:00Z" || item.ReceivedAt != wantReceived {
			t.Fatalf("later valid capture did not recover received=%s obs=%+v", item.ReceivedAt, item.Observation)
		}
		if item.Freshness.State != providerallowance.FreshFresh || item.CurrentMeasures[0].State != providerallowance.CurrentKnown || deref(item.CurrentMeasures[0].Value.Decimal) != "22" {
			t.Fatalf("recovered view freshness=%+v current=%+v", item.Freshness, item.CurrentMeasures[0])
		}
		if item.Binding.State != providerallowance.BindingVerified || deref(item.Binding.AccountRef) != "fixture-account" || status.LastGood == nil || status.LastGood.ReceivedAt != wantReceived || status.LastAttempt.Result != providerallowance.AttemptSuccess {
			t.Fatalf("recovered binding=%+v lastGood=%+v attempt=%+v", item.Binding, status.LastGood, status.LastAttempt)
		}
		if strings.Contains(string(raw), "other-account") || strings.Contains(string(raw), "rejected-cross") || strings.Contains(string(raw), `"decimal":"11"`) {
			t.Fatal("rejected cross-account capture stayed in the snapshot")
		}
	})
	t.Run("same-capture-conflict-keeps-original", func(t *testing.T) {
		original := collectionObs(t, obsOpts{id: "obs-same", provider: "openai", decimal: "11", sequence: 4})
		changed := collectionObs(t, obsOpts{id: "obs-same", provider: "openai", decimal: "99", sequence: 4})
		doc, raw := collectCycles(t, reg, 2, original, changed)
		item := onlyItem(t, doc, "openai")
		status := statusBy(t, doc, "openai")
		wantReceived := collectionStart.Add(collectionSuccessLatency).UTC().Format(time.RFC3339Nano)
		if item.Observation.ObservationID != "obs-same" || deref(item.Observation.Measures[0].Value.Decimal) != "11" || item.ReceivedAt != wantReceived {
			t.Fatalf("conflict replaced the original receipt or decimal received=%s decimal=%s", item.ReceivedAt, deref(item.Observation.Measures[0].Value.Decimal))
		}
		if item.CurrentMeasures[0].State != providerallowance.CurrentConflict || status.Availability != providerallowance.AvailabilityConflict || deref(status.LastAttempt.Code) != "acquisition_conflict" || status.LastAttempt.Result != providerallowance.AttemptError {
			t.Fatalf("same capture was not a conflict item=%+v status=%+v", item.CurrentMeasures[0], status)
		}
		if strings.Contains(string(raw), `"decimal":"99"`) {
			t.Fatal("conflicting payload was stored")
		}
	})
}

func assertRejectedCrossAccount(t *testing.T, doc providerallowance.ReadResponse, payload providerallowance.Availability) {
	t.Helper()
	item := onlyItem(t, doc, "openai")
	status := statusBy(t, doc, "openai")
	wantReceived := collectionStart.Add(collectionSuccessLatency).UTC().Format(time.RFC3339Nano)
	wantCompleted := collectionStart.Add(2 * collectionSuccessLatency).UTC().Format(time.RFC3339Nano)
	if item.ReceivedAt != wantReceived || deref(status.LastAttempt.CompletedAt) != wantCompleted {
		t.Fatalf("replay replaced the receipt received=%s completed=%s", item.ReceivedAt, deref(status.LastAttempt.CompletedAt))
	}
	if item.Observation.Availability != payload || deref(item.Observation.Scope.AccountRef) != "other-account" || item.Observation.Scope.BindingState != providerallowance.BindingVerified || deref(item.Observation.Measures[0].Value.Decimal) != "12.5" {
		t.Fatalf("historical observation changed availability=%s account=%s", item.Observation.Availability, deref(item.Observation.Scope.AccountRef))
	}
	if status.LastAttempt.Result != providerallowance.AttemptError || status.Availability != providerallowance.AvailabilityUnavailable || deref(status.LastAttempt.Code) != "acquisition_rejected" || status.LastGood != nil {
		t.Fatalf("rejected replay laundered the attempt result=%s availability=%s code=%s lastGood=%+v", status.LastAttempt.Result, status.Availability, deref(status.LastAttempt.Code), status.LastGood)
	}
	if item.Validation.State != providerallowance.ValidationRejected || item.Freshness.State != providerallowance.FreshUnknown || item.Freshness.State == providerallowance.FreshFresh || item.CurrentMeasures[0].State == providerallowance.CurrentKnown || item.CurrentMeasures[0].Value.Decimal != nil {
		t.Fatalf("rejected capture became current validation=%s freshness=%+v current=%+v", item.Validation.State, item.Freshness, item.CurrentMeasures[0])
	}
	if item.Binding.State != providerallowance.BindingMismatch || item.Binding.AccountRef != nil || item.Binding.WorkspaceRef != nil || status.Binding.State != providerallowance.BindingMismatch || status.Binding.AccountRef != nil || deref(status.Binding.AccountRef) == "fixture-account" {
		t.Fatalf("trusted binding paired with foreign observation item=%+v status=%+v", item.Binding, status.Binding)
	}
}

func TestCollectionRejectedMismatchDoesNotPairTrustedBinding(t *testing.T) {
	reg := fixtureOnlyRegistry(t)
	t.Run("cross-account", func(t *testing.T) {
		raw := mutateObs(t, collectionObs(t, obsOpts{id: "mismatch-binding", provider: "openai", decimal: "12.5", captured: "2026-11-02T03:00:00Z", observed: "2026-11-02T02:59:00Z"}), func(obs *providerallowance.Observation) {
			obs.Scope.AccountRef = sp("other-account")
		})
		doc, _ := collectCycles(t, reg, 1, raw)
		item := onlyItem(t, doc, "openai")
		status := statusBy(t, doc, "openai")
		wantReceived := collectionStart.Add(collectionSuccessLatency).UTC().Format(time.RFC3339Nano)
		if item.ReceivedAt != wantReceived || deref(item.Observation.Scope.AccountRef) != "other-account" || item.Observation.Scope.BindingState != providerallowance.BindingVerified {
			t.Fatalf("observation account or receipt changed received=%s account=%s binding=%s", item.ReceivedAt, deref(item.Observation.Scope.AccountRef), item.Observation.Scope.BindingState)
		}
		if item.Freshness.State != providerallowance.FreshUnknown || item.Freshness.Reason != "binding not actionable" || status.Freshness.State != providerallowance.FreshUnknown || item.Freshness.State == providerallowance.FreshFresh {
			t.Fatalf("mismatched binding became fresh item=%+v status=%+v", item.Freshness, status.Freshness)
		}
		if item.Binding.State != providerallowance.BindingMismatch || item.Binding.AccountRef != nil || item.Binding.WorkspaceRef != nil || status.Binding.State != item.Binding.State || status.Binding.AccountRef != nil {
			t.Fatalf("trusted binding paired with foreign observation item=%+v status=%+v", item.Binding, status.Binding)
		}
		if item.Validation.State != providerallowance.ValidationRejected || item.CurrentMeasures[0].State == providerallowance.CurrentKnown || item.CurrentMeasures[0].Value.Decimal != nil || deref(item.Observation.Measures[0].Value.Decimal) != "12.5" || status.LastGood != nil || deref(status.LastAttempt.Code) != "acquisition_rejected" || status.Availability != providerallowance.AvailabilityUnavailable {
			t.Fatalf("validation=%s current=%+v attempt=%+v availability=%s", item.Validation.State, item.CurrentMeasures[0], status.LastAttempt, status.Availability)
		}
	})
	t.Run("matching-account-stays-fresh", func(t *testing.T) {
		raw := collectionObs(t, obsOpts{id: "matched-binding", provider: "openai", decimal: "12.5", captured: "2026-11-02T03:00:00Z", observed: "2026-11-02T02:59:00Z"})
		doc, _ := collectCycles(t, reg, 1, raw)
		item := onlyItem(t, doc, "openai")
		status := statusBy(t, doc, "openai")
		if item.Freshness.State != providerallowance.FreshFresh || item.Binding.State != providerallowance.BindingVerified || deref(item.Binding.AccountRef) != "fixture-account" || deref(item.Observation.Scope.AccountRef) != "fixture-account" {
			t.Fatalf("matching account lost its binding freshness=%+v binding=%+v", item.Freshness, item.Binding)
		}
		if item.CurrentMeasures[0].State != providerallowance.CurrentKnown || deref(item.CurrentMeasures[0].Value.Decimal) != "12.5" || status.LastGood == nil || status.LastAttempt.Result != providerallowance.AttemptSuccess {
			t.Fatalf("matching account was withheld current=%+v attempt=%+v", item.CurrentMeasures[0], status.LastAttempt)
		}
	})
	t.Run("workspace-mismatch", func(t *testing.T) {
		raw := mutateObs(t, collectionObs(t, obsOpts{id: "workspace-mismatch", provider: "openai", decimal: "12.5"}), func(obs *providerallowance.Observation) {
			obs.Scope.WorkspaceRef = sp("other-workspace")
		})
		doc, _ := collectCycles(t, reg, 1, raw)
		item := onlyItem(t, doc, "openai")
		status := statusBy(t, doc, "openai")
		if item.Freshness.State != providerallowance.FreshUnknown || item.Binding.State != providerallowance.BindingMismatch || item.Binding.AccountRef != nil || item.Binding.WorkspaceRef != nil || deref(status.Binding.WorkspaceRef) == "fixture-workspace" {
			t.Fatalf("workspace mismatch paired the trusted binding freshness=%+v binding=%+v status=%+v", item.Freshness, item.Binding, status.Binding)
		}
		if deref(item.Observation.Scope.WorkspaceRef) != "other-workspace" || deref(item.Observation.Scope.AccountRef) != "fixture-account" || item.Validation.State != providerallowance.ValidationRejected || item.CurrentMeasures[0].Value.Decimal != nil || status.LastGood != nil {
			t.Fatal("workspace mismatch rewrote the observation or published a current value")
		}
	})
}

func TestCollectionReceiptAheadOfServedAtWithholdsCurrent(t *testing.T) {
	t.Run("served-before-receipt", func(t *testing.T) {
		clock := &fakeClock{now: collectionStart}
		raw := collectionObs(t, obsOpts{id: "receipt-ahead", provider: "openai", decimal: "12.5", captured: "2026-11-02T03:04:00Z", observed: "2026-11-02T03:04:00Z"})
		acquire := &scriptAcquire{t: t, steps: map[string][]acquireStep{"openai": {successStep(clock, raw)}}}
		collection := mustCollection(t, collectionBounds(30*time.Minute, 1), []providerallowance.SourceSpec{openaiSpec(sp("fixture-account"))}, fixtureOnlyRegistry(t), clock, &fakeWait{}, acquire)
		if err := collection.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
		calls := acquire.total
		before, _, err := collection.Snapshot(time.Date(2026, 11, 2, 3, 4, 30, 0, time.UTC))
		if err != nil {
			t.Fatal(err)
		}
		equalAt := collectionStart.Add(collectionSuccessLatency)
		equal, _, err := collection.Snapshot(equalAt)
		if err != nil {
			t.Fatal(err)
		}
		after, _, err := collection.Snapshot(collectionServed)
		if err != nil {
			t.Fatal(err)
		}
		if acquire.total != calls {
			t.Fatal("serving read acquired")
		}
		assertReceiptAheadWithheld(t, before)
		assertReceiptServedCurrent(t, equal, equalAt.UTC().Format(time.RFC3339Nano))
		assertReceiptServedCurrent(t, after, equalAt.UTC().Format(time.RFC3339Nano))
	})
	t.Run("provider-after-receipt-stays-expired", func(t *testing.T) {
		clock := &fakeClock{now: collectionStart}
		raw := collectionObs(t, obsOpts{id: "future-provider", provider: "openai", decimal: "11", observed: "2026-11-02T03:07:00Z"})
		acquire := &scriptAcquire{t: t, steps: map[string][]acquireStep{"openai": {successStep(clock, raw)}}}
		collection := mustCollection(t, collectionBounds(30*time.Minute, 1), []providerallowance.SourceSpec{openaiSpec(sp("fixture-account"))}, fixtureOnlyRegistry(t), clock, &fakeWait{}, acquire)
		if err := collection.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
		later := collectionServed.Add(2 * time.Minute)
		doc, _, err := collection.Snapshot(later)
		if err != nil {
			t.Fatal(err)
		}
		assertReceiptFutureUnknown(t, doc, "11", collectionCaptured, "2026-11-02T03:07:00Z")
		item := onlyItem(t, doc, "openai")
		if item.Freshness.State != providerallowance.FreshExpired || item.Freshness.Basis != providerallowance.BasisProvider || item.Freshness.Reason != "provider time future" || item.Freshness.Reason == "receipt time future" {
			t.Fatalf("provider future was reclassified %+v", item.Freshness)
		}
	})
	t.Run("capture-after-receipt-stays-expired", func(t *testing.T) {
		clock := &fakeClock{now: collectionStart}
		raw := collectionObs(t, obsOpts{id: "future-capture", provider: "openai", decimal: "11", captured: "2026-11-02T03:07:00Z", observed: collectionObserved})
		acquire := &scriptAcquire{t: t, steps: map[string][]acquireStep{"openai": {successStep(clock, raw)}}}
		collection := mustCollection(t, collectionBounds(30*time.Minute, 1), []providerallowance.SourceSpec{openaiSpec(sp("fixture-account"))}, fixtureOnlyRegistry(t), clock, &fakeWait{}, acquire)
		if err := collection.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
		later := collectionServed.Add(2 * time.Minute)
		doc, _, err := collection.Snapshot(later)
		if err != nil {
			t.Fatal(err)
		}
		assertReceiptFutureUnknown(t, doc, "11", "2026-11-02T03:07:00Z", collectionObserved)
		item := onlyItem(t, doc, "openai")
		if item.Freshness.State != providerallowance.FreshExpired || item.Freshness.Basis != providerallowance.BasisCapture || item.Freshness.AsOf != "2026-11-02T03:07:00Z" || item.Freshness.Reason != "capture time future" {
			t.Fatalf("capture future was reclassified %+v", item.Freshness)
		}
	})
}

func assertReceiptAheadWithheld(t *testing.T, doc providerallowance.ReadResponse) {
	t.Helper()
	item := onlyItem(t, doc, "openai")
	status := statusBy(t, doc, "openai")
	wantReceived := collectionStart.Add(collectionSuccessLatency).UTC().Format(time.RFC3339Nano)
	if item.ReceivedAt != wantReceived || item.Observation.CapturedAt != "2026-11-02T03:04:00Z" || deref(item.Observation.ProviderObservedAt) != "2026-11-02T03:04:00Z" || deref(item.Observation.Measures[0].Value.Decimal) != "12.5" {
		t.Fatalf("receipt or historical observation changed received=%s captured=%s observed=%s", item.ReceivedAt, item.Observation.CapturedAt, deref(item.Observation.ProviderObservedAt))
	}
	if item.Freshness.State != providerallowance.FreshExpired || item.Freshness.Reason != "receipt time future" || item.Freshness.AsOf != wantReceived || item.Freshness.Basis != providerallowance.BasisCapture || item.Freshness.State == providerallowance.FreshFresh || item.Freshness.State == providerallowance.FreshStale {
		t.Fatalf("receipt ahead of serving became current %+v", item.Freshness)
	}
	if item.CurrentMeasures[0].State != providerallowance.CurrentUnknown || item.CurrentMeasures[0].Value.Decimal != nil || item.CurrentMeasures[0].Reason != "receipt time future" {
		t.Fatalf("current value was served %+v", item.CurrentMeasures[0])
	}
	if item.Validation.State == providerallowance.ValidationRejected || status.LastGood == nil || status.LastGood.ReceivedAt != wantReceived || status.LastAttempt.Result != providerallowance.AttemptSuccess {
		t.Fatalf("accepted sample was dropped validation=%s lastGood=%+v attempt=%+v", item.Validation.State, status.LastGood, status.LastAttempt)
	}
}

func assertReceiptServedCurrent(t *testing.T, doc providerallowance.ReadResponse, wantReceived string) {
	t.Helper()
	item := onlyItem(t, doc, "openai")
	status := statusBy(t, doc, "openai")
	if item.ReceivedAt != wantReceived || status.LastGood == nil || status.LastGood.ReceivedAt != wantReceived {
		t.Fatalf("serving moved the receipt item=%s lastGood=%+v", item.ReceivedAt, status.LastGood)
	}
	if item.Freshness.State != providerallowance.FreshFresh || item.CurrentMeasures[0].State != providerallowance.CurrentKnown || deref(item.CurrentMeasures[0].Value.Decimal) != "12.5" {
		t.Fatalf("receipt was still withheld freshness=%+v current=%+v", item.Freshness, item.CurrentMeasures[0])
	}
	if item.Observation.CapturedAt != "2026-11-02T03:04:00Z" || deref(item.Observation.ProviderObservedAt) != "2026-11-02T03:04:00Z" {
		t.Fatal("historical observation changed when the serving clock passed the receipt")
	}
}

func TestCollectionAccountChangeDropsQuota(t *testing.T) {
	clock := &fakeClock{now: collectionStart}
	raw := collectionObs(t, obsOpts{id: "acct-0", provider: "openai", decimal: "11", sequence: 1})
	acquire := &scriptAcquire{t: t, steps: map[string][]acquireStep{
		"openai": {
			successStep(clock, raw),
			func(context.Context, providerallowance.SourceSpec) (providerallowance.Acquisition, error) {
				return providerallowance.Acquisition{}, fmt.Errorf("%s %s", collectionCanaryToken, collectionCanaryCookie)
			},
		},
	}}
	spec := openaiSpec(sp("fixture-account"))
	collection := mustCollection(t, collectionBounds(30*time.Minute, 1), []providerallowance.SourceSpec{spec}, testRegistry(t, true), clock, &fakeWait{}, acquire)
	if err := collection.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	before, _, err := collection.Snapshot(collectionServed)
	if err != nil {
		t.Fatal(err)
	}
	if deref(onlyItem(t, before, "openai").Observation.Measures[0].Value.Decimal) != "11" {
		t.Fatal("first account quota missing")
	}
	next := openaiSpec(sp("other-account"))
	next.BindingGeneration = 1
	if err := collection.SetSources([]providerallowance.SourceSpec{next}); err != nil {
		t.Fatal(err)
	}
	cleared, rawCleared, err := collection.Snapshot(collectionServed)
	if err != nil {
		t.Fatal(err)
	}
	if len(cleared.Items) != 0 || strings.Contains(string(rawCleared), `"decimal":"11"`) {
		t.Fatal("previous quota survived the account change")
	}
	if err := collection.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, rawAfter, err := collection.Snapshot(collectionServed)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Items) != 0 || strings.Contains(string(rawAfter), `"decimal":"11"`) || strings.Contains(string(rawAfter), collectionCanaryToken) {
		t.Fatal("failed switch restored the previous quota or leaked the callback error")
	}
}

func TestCollectionPartialFailureKeepsOtherProvider(t *testing.T) {
	clock := &fakeClock{now: collectionStart}
	cursorRaw := collectionObs(t, obsOpts{id: "cursor-1", provider: "cursor", decimal: "11", sequence: 1})
	openaiRaw := collectionObs(t, obsOpts{id: "openai-1", provider: "openai", decimal: "21", sequence: 1})
	acquire := &scriptAcquire{t: t, steps: map[string][]acquireStep{
		"cursor": {successStep(clock, cursorRaw), successStep(clock, cursorRaw)},
		"openai": {
			successStep(clock, openaiRaw),
			func(context.Context, providerallowance.SourceSpec) (providerallowance.Acquisition, error) {
				return providerallowance.Acquisition{Observation: []byte(collectionCanaryToken)}, fmt.Errorf("%s %s", collectionCanaryToken, collectionCanaryCookie)
			},
		},
	}}
	specs := []providerallowance.SourceSpec{providerSpec("cursor", sp("fixture-account")), openaiSpec(sp("fixture-account"))}
	collection := mustCollection(t, collectionBounds(30*time.Minute, 2), specs, testRegistry(t, true), clock, &fakeWait{}, acquire)
	if err := collection.Run(context.Background()); err != nil {
		t.Fatalf("error leaked callback text: %v", err)
	}
	doc, raw, err := collection.Snapshot(collectionServed)
	if err != nil {
		t.Fatal(err)
	}
	cursorItem := onlyItem(t, doc, "cursor")
	openaiItem := onlyItem(t, doc, "openai")
	openaiStatus := statusBy(t, doc, "openai")
	if cursorItem.Observation.ObservationID != "cursor-1" || deref(cursorItem.Observation.Measures[0].Value.Decimal) != "11" {
		t.Fatal("cursor history was erased")
	}
	if openaiItem.Observation.ObservationID != "openai-1" || deref(openaiItem.Observation.Measures[0].Value.Decimal) != "21" || deref(openaiStatus.LastAttempt.Code) != "acquisition_error" || openaiStatus.LastGood == nil {
		t.Fatalf("openai last good lost: item=%s status=%+v", openaiItem.Observation.ObservationID, openaiStatus)
	}
	if strings.Contains(string(raw), collectionCanaryToken) || strings.Contains(string(raw), collectionCanaryCookie) {
		t.Fatal("callback error or rejected bytes entered the snapshot")
	}
	for _, provider := range []string{"cursor", "anthropic", "openai"} {
		if statusBy(t, doc, provider).Provider != provider {
			t.Fatalf("missing %s", provider)
		}
	}
}

func TestCollectionCancellationTimeoutAndBackoff(t *testing.T) {
	t.Run("cancel-before-next-source", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		clock := &fakeClock{now: collectionStart}
		acquire := &scriptAcquire{t: t, steps: map[string][]acquireStep{
			"cursor": {func(context.Context, providerallowance.SourceSpec) (providerallowance.Acquisition, error) {
				cancel()
				return providerallowance.Acquisition{Observation: collectionObs(t, obsOpts{id: "should-drop", provider: "cursor", decimal: "11"})}, nil
			}},
			"openai": {func(context.Context, providerallowance.SourceSpec) (providerallowance.Acquisition, error) {
				t.Fatal("later source was called after cancellation")
				return providerallowance.Acquisition{}, nil
			}},
		}}
		collection := mustCollection(t, collectionBounds(30*time.Minute, 2), []providerallowance.SourceSpec{providerSpec("cursor", sp("fixture-account")), openaiSpec(sp("fixture-account"))}, testRegistry(t, true), clock, &fakeWait{}, acquire)
		err := collection.Run(ctx)
		if !errors.Is(err, providerallowance.ErrCollectionCancelled) || err.Error() != "collection_cancelled" {
			t.Fatal(err)
		}
		doc, _, snapErr := collection.Snapshot(collectionServed)
		if snapErr != nil {
			t.Fatal(snapErr)
		}
		if len(doc.Items) != 0 || acquire.calls["openai"] != 0 {
			t.Fatalf("items=%d openaiCalls=%d", len(doc.Items), acquire.calls["openai"])
		}
	})
	t.Run("cancel-between-cycles", func(t *testing.T) {
		clock := &fakeClock{now: collectionStart}
		wait := &fakeWait{err: fmt.Errorf("%s", collectionCanaryToken)}
		raw := collectionObs(t, obsOpts{id: "kept", provider: "openai", decimal: "11"})
		acquire := &scriptAcquire{t: t, steps: map[string][]acquireStep{"openai": {successStep(clock, raw), successStep(clock, raw)}}}
		collection := mustCollection(t, collectionBounds(30*time.Minute, 2), []providerallowance.SourceSpec{openaiSpec(sp("fixture-account"))}, testRegistry(t, true), clock, wait, acquire)
		err := collection.Run(context.Background())
		if !errors.Is(err, providerallowance.ErrCollectionCancelled) || strings.Contains(err.Error(), "token") {
			t.Fatal(err)
		}
		doc, snapRaw, snapErr := collection.Snapshot(collectionServed)
		if snapErr != nil {
			t.Fatal(snapErr)
		}
		if onlyItem(t, doc, "openai").Observation.ObservationID != "kept" || acquire.total != 1 || len(wait.calls) != 1 || strings.Contains(string(snapRaw), collectionCanaryToken) {
			t.Fatalf("calls=%d waits=%v", acquire.total, wait.calls)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		clock := &fakeClock{now: collectionStart}
		bounds := collectionBounds(30*time.Minute, 1)
		acquire := &scriptAcquire{t: t, steps: map[string][]acquireStep{"openai": {func(ctx context.Context, _ providerallowance.SourceSpec) (providerallowance.Acquisition, error) {
			clock.Advance(bounds.CallTimeout)
			select {
			case <-ctx.Done():
				t.Fatal("provider clock closed Done")
			default:
			}
			if ctx.Err() != nil || errors.Is(ctx.Err(), providerallowance.ErrCollectionTimeout) {
				t.Fatalf("provider clock set %v", ctx.Err())
			}
			return providerallowance.Acquisition{Observation: collectionObs(t, obsOpts{id: "late", provider: "openai", decimal: "11"})}, fmt.Errorf("%s", collectionCanaryCookie)
		}}}}
		collection := mustCollection(t, bounds, []providerallowance.SourceSpec{openaiSpec(sp("fixture-account"))}, testRegistry(t, true), clock, &fakeWait{}, acquire)
		if err := collection.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
		doc, raw, err := collection.Snapshot(collectionServed)
		if err != nil {
			t.Fatal(err)
		}
		status := statusBy(t, doc, "openai")
		if len(doc.Items) != 0 || deref(status.LastAttempt.Code) != "acquisition_timeout" || strings.Contains(string(raw), "late") || strings.Contains(string(raw), collectionCanaryCookie) {
			t.Fatalf("%+v", status)
		}
	})
	t.Run("acquirer-waits-for-deadline", func(t *testing.T) {
		clock := &fakeClock{now: collectionStart}
		observed := make(chan error, 1)
		acquire := &scriptAcquire{t: t, steps: map[string][]acquireStep{"openai": {func(ctx context.Context, _ providerallowance.SourceSpec) (providerallowance.Acquisition, error) {
			var callErr error
			select {
			case <-ctx.Done():
				callErr = ctx.Err()
			case <-time.After(3 * time.Second):
				callErr = errors.New("deadline did not close Done")
			}
			observed <- callErr
			return providerallowance.Acquisition{Observation: collectionObs(t, obsOpts{id: "late", provider: "openai", decimal: "11"})}, callErr
		}}}}
		bounds := collectionBounds(30*time.Minute, 1)
		bounds.CallTimeout = 50 * time.Millisecond
		collection := mustCollection(t, bounds, []providerallowance.SourceSpec{openaiSpec(sp("fixture-account"))}, testRegistry(t, true), clock, &fakeWait{}, acquire)
		done := make(chan error, 1)
		go func() { done <- collection.Run(context.Background()) }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("Run did not return after the call deadline")
		}
		got := <-observed
		if !errors.Is(got, context.DeadlineExceeded) || errors.Is(got, providerallowance.ErrCollectionTimeout) {
			t.Fatalf("callback err=%v", got)
		}
		doc, raw, err := collection.Snapshot(collectionServed)
		if err != nil {
			t.Fatal(err)
		}
		status := statusBy(t, doc, "openai")
		if len(doc.Items) != 0 || deref(status.LastAttempt.Code) != "acquisition_timeout" || strings.Contains(string(raw), "late") {
			t.Fatalf("%+v", status)
		}
	})
	t.Run("backoff", func(t *testing.T) {
		clock := &fakeClock{now: collectionStart}
		wait := &fakeWait{}
		raw := collectionObs(t, obsOpts{id: "after-backoff", provider: "openai", decimal: "11"})
		acquire := &scriptAcquire{t: t, steps: map[string][]acquireStep{"openai": {
			func(context.Context, providerallowance.SourceSpec) (providerallowance.Acquisition, error) {
				return providerallowance.Acquisition{}, errors.New("temporary")
			},
			successStep(clock, raw),
		}}}
		collection := mustCollection(t, collectionBounds(30*time.Minute, 2), []providerallowance.SourceSpec{openaiSpec(sp("fixture-account"))}, testRegistry(t, true), clock, wait, acquire)
		if err := collection.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
		if acquire.total != 2 || len(wait.calls) != 1 || wait.calls[0] != time.Second {
			t.Fatalf("calls=%d waits=%v", acquire.total, wait.calls)
		}
		if onlyItem(t, docOf(t, collection), "openai").Observation.ObservationID != "after-backoff" {
			t.Fatal("backoff cycle did not keep the later capture")
		}
	})
}

func TestCollectionInflightGenerationAndLiveSnapshot(t *testing.T) {
	clock := &fakeClock{now: collectionStart}
	raw := collectionObs(t, obsOpts{id: "gen-hold", provider: "openai", decimal: "11", sequence: 1})
	parked := make(chan struct{})
	release := make(chan struct{})
	acquire := &scriptAcquire{t: t, steps: map[string][]acquireStep{
		"openai": {
			successStep(clock, raw),
			func(ctx context.Context, _ providerallowance.SourceSpec) (providerallowance.Acquisition, error) {
				close(parked)
				select {
				case <-release:
				case <-ctx.Done():
					return providerallowance.Acquisition{}, ctx.Err()
				}
				clock.Advance(collectionSuccessLatency)
				return providerallowance.Acquisition{Observation: append([]byte(nil), raw...)}, nil
			},
		},
	}}
	collection := mustCollection(t, collectionBounds(30*time.Minute, 1), []providerallowance.SourceSpec{openaiSpec(sp("fixture-account"))}, testRegistry(t, true), clock, &fakeWait{}, acquire)
	if err := collection.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	runDone := make(chan error, 1)
	go func() { runDone <- collection.Run(context.Background()) }()
	select {
	case <-parked:
	case <-time.After(2 * time.Second):
		t.Fatal("acquirer did not park")
	}
	type snapResult struct {
		doc providerallowance.ReadResponse
		err error
	}
	parkedSnap := make(chan snapResult, 1)
	go func() {
		doc, _, err := collection.Snapshot(collectionServed)
		parkedSnap <- snapResult{doc: doc, err: err}
	}()
	select {
	case got := <-parkedSnap:
		if got.err != nil {
			t.Fatal(got.err)
		}
		if deref(onlyItem(t, got.doc, "openai").Observation.Measures[0].Value.Decimal) != "11" {
			t.Fatal("parked snapshot lost the current quota")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("snapshot blocked on the parked acquirer")
	}
	next := openaiSpec(sp("other-account"))
	next.BindingGeneration = 1
	if err := collection.SetSources([]providerallowance.SourceSpec{next}); err != nil {
		t.Fatal(err)
	}
	cleared, rawCleared, err := collection.Snapshot(collectionServed)
	if err != nil {
		t.Fatal(err)
	}
	if len(cleared.Items) != 0 || strings.Contains(string(rawCleared), "gen-hold") {
		t.Fatal("generation change kept the previous quota while acquisition was in flight")
	}
	overlap := make(chan snapResult, 1)
	go func() {
		doc, _, err := collection.Snapshot(collectionServed)
		overlap <- snapResult{doc: doc, err: err}
	}()
	close(release)
	select {
	case got := <-overlap:
		if got.err != nil {
			t.Fatal(got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("snapshot did not return while the superseded result was recorded")
	}
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("run did not return")
	}
	finalDoc, rawFinal, err := collection.Snapshot(collectionServed)
	if err != nil {
		t.Fatal(err)
	}
	if len(finalDoc.Items) != 0 || strings.Contains(string(rawFinal), "gen-hold") || strings.Contains(string(rawFinal), `"decimal":"11"`) {
		t.Fatal("superseded acquisition restored the previous quota")
	}
}

func TestCollectionCallbackSecretsNeverSurface(t *testing.T) {
	cases := []struct {
		name string
		step acquireStep
		code string
	}{
		{name: "error", code: "acquisition_error", step: func(context.Context, providerallowance.SourceSpec) (providerallowance.Acquisition, error) {
			return providerallowance.Acquisition{Observation: []byte(collectionCanaryToken)}, fmt.Errorf("%s %s", collectionCanaryToken, collectionCanaryCookie)
		}},
		{name: "bad-availability", code: "acquisition_unavailable", step: func(context.Context, providerallowance.SourceSpec) (providerallowance.Acquisition, error) {
			return providerallowance.Acquisition{Unavailable: &providerallowance.Unavailable{Availability: providerallowance.Availability(collectionCanaryCookie)}}, nil
		}},
		{name: "malformed", code: "acquisition_rejected", step: func(context.Context, providerallowance.SourceSpec) (providerallowance.Acquisition, error) {
			return providerallowance.Acquisition{Observation: []byte(`{"token":"` + collectionCanaryToken + `"}`)}, nil
		}},
		{name: "oversize", code: "acquisition_bounds", step: func(context.Context, providerallowance.SourceSpec) (providerallowance.Acquisition, error) {
			return providerallowance.Acquisition{Observation: []byte(collectionCanaryToken + strings.Repeat("x", 8192))}, nil
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			collection := mustCollection(t, collectionBounds(30*time.Minute, 1), []providerallowance.SourceSpec{openaiSpec(sp("fixture-account"))}, testRegistry(t, true), &fakeClock{now: collectionStart}, &fakeWait{}, &scriptAcquire{t: t, steps: map[string][]acquireStep{"openai": {tc.step}}})
			err := collection.Run(context.Background())
			doc, raw, snapErr := collection.Snapshot(collectionServed)
			if snapErr != nil {
				t.Fatal(snapErr)
			}
			text := string(raw)
			if err != nil {
				text += err.Error()
			}
			if strings.Contains(text, collectionCanaryToken) || strings.Contains(text, collectionCanaryCookie) || deref(statusBy(t, doc, "openai").LastAttempt.Code) != tc.code {
				t.Fatalf("code=%s leaked=%q err=%v", deref(statusBy(t, doc, "openai").LastAttempt.Code), text, err)
			}
		})
	}
}

func TestCollectionAliasDedupeAndNoSum(t *testing.T) {
	t.Run("verified-cursor-grok-keeps-xai", func(t *testing.T) {
		doc := collectProviders(t, true, map[string]obsOpts{
			"cursor": {id: "cursor-pool", provider: "cursor", decimal: "11", entitlement: "entitlement-E", product: "cursor"},
			"grok":   {id: "grok-pool", provider: "grok", decimal: "22", entitlement: "entitlement-E", product: "grok"},
			"xai":    {id: "xai-direct", provider: "xai", decimal: "44", product: "xai"},
		})
		cursorItem := onlyItem(t, doc, "cursor")
		grokItem := onlyItem(t, doc, "grok")
		xaiItem := onlyItem(t, doc, "xai")
		if deref(cursorItem.CurrentMeasures[0].Value.Decimal) != "11" || cursorItem.CurrentMeasures[0].State != providerallowance.CurrentKnown {
			t.Fatalf("cursor %+v", cursorItem.CurrentMeasures)
		}
		if grokItem.CurrentMeasures[0].State != providerallowance.CurrentNotApplicable || grokItem.CurrentMeasures[0].Value.Decimal != nil || deref(grokItem.Observation.Measures[0].Value.Decimal) != "22" {
			t.Fatalf("grok %+v obs=%s", grokItem.CurrentMeasures[0], deref(grokItem.Observation.Measures[0].Value.Decimal))
		}
		if deref(xaiItem.CurrentMeasures[0].Value.Decimal) != "44" || xaiItem.CurrentMeasures[0].State != providerallowance.CurrentKnown {
			t.Fatalf("xai %+v", xaiItem.CurrentMeasures)
		}
	})
	t.Run("unverified-pool-stays-distinct", func(t *testing.T) {
		doc := collectProviders(t, false, map[string]obsOpts{
			"cursor": {id: "cursor-pool", provider: "cursor", decimal: "11", entitlement: "entitlement-E", product: "cursor"},
			"grok":   {id: "grok-pool", provider: "grok", decimal: "22", entitlement: "entitlement-E", product: "grok"},
		})
		if deref(onlyItem(t, doc, "grok").CurrentMeasures[0].Value.Decimal) != "22" || deref(onlyItem(t, doc, "cursor").CurrentMeasures[0].Value.Decimal) != "11" {
			t.Fatal("unverified registry collapsed cursor and grok")
		}
	})
	t.Run("projection-does-not-sum", func(t *testing.T) {
		clock := &fakeClock{now: collectionStart}
		raw := twoMeterObs(t, "proj", "11", "22")
		reg := testRegistry(t, true)
		parts := providerallowance.RegistryParts{Sources: reg.Sources(), Metrics: reg.Metrics(), Accounts: reg.Accounts(), Entitlements: reg.Entitlements(), Projections: []providerallowance.ProjectionRule{{
			ID: "window-pair", Provider: "openai", SourceVersion: "fixture-v1", MeasureIDs: []string{"window-a", "window-b"},
		}}}
		acquire := &scriptAcquire{t: t, steps: map[string][]acquireStep{"openai": {successStep(clock, raw)}}}
		collection := mustCollection(t, collectionBounds(30*time.Minute, 1), []providerallowance.SourceSpec{openaiSpec(sp("fixture-account"))}, providerallowance.NewStaticRegistry(parts), clock, &fakeWait{}, acquire)
		if err := collection.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
		item := onlyItem(t, docOf(t, collection), "openai")
		if len(item.CurrentMeasures) != 2 || deref(item.CurrentMeasures[0].Value.Decimal) != "11" || item.CurrentMeasures[1].State != providerallowance.CurrentNotApplicable {
			t.Fatalf("%+v", item.CurrentMeasures)
		}
	})
	t.Run("separate-windows-and-spend", func(t *testing.T) {
		clock := &fakeClock{now: collectionStart}
		raw := spendObs(t)
		acquire := &scriptAcquire{t: t, steps: map[string][]acquireStep{"openai": {successStep(clock, raw)}}}
		collection := mustCollection(t, collectionBounds(30*time.Minute, 1), []providerallowance.SourceSpec{openaiSpec(sp("fixture-account"))}, testRegistry(t, true), clock, &fakeWait{}, acquire)
		if err := collection.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
		item := onlyItem(t, docOf(t, collection), "openai")
		if len(item.CurrentMeasures) != 2 || deref(item.CurrentMeasures[0].Value.Decimal) != "40" || deref(item.CurrentMeasures[1].Value.Decimal) != "5" {
			t.Fatalf("%+v", item.CurrentMeasures)
		}
		if item.Observation.Measures[1].Category != providerallowance.CategorySpend || item.Observation.Measures[1].Unit != providerallowance.UnitMoney {
			t.Fatal("spend was rewritten as included percent")
		}
	})
}

func TestCollectionSnapshotRoundTripAndCachedReader(t *testing.T) {
	clock := &fakeClock{now: collectionStart}
	rawObs := collectionObs(t, obsOpts{id: "reader", provider: "openai", decimal: "12.5"})
	obs, err := providerallowance.DecodeObservation(rawObs, providerallowance.FixtureParseLimits)
	if err != nil {
		t.Fatal(err)
	}
	reg := testRegistry(t, true)
	trust := openaiSpec(sp("fixture-account")).Trusted()
	if issues := providerallowance.ValidateObservation(obs, trust, reg).Issues; len(issues) != 0 {
		t.Fatal(issues)
	}
	acquire := &scriptAcquire{t: t, steps: map[string][]acquireStep{"openai": {successStep(clock, rawObs)}}}
	collection := mustCollection(t, collectionBounds(30*time.Minute, 1), []providerallowance.SourceSpec{openaiSpec(sp("fixture-account"))}, reg, clock, &fakeWait{}, acquire)
	if err := collection.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	doc, raw, err := collection.Snapshot(collectionServed)
	if err != nil {
		t.Fatal(err)
	}
	again, err := providerallowance.DecodeReadResponse(raw, providerallowance.FixtureParseLimits)
	if err != nil {
		t.Fatal(err)
	}
	remarshaled, err := json.Marshal(again)
	if err != nil || string(remarshaled) != string(raw) {
		t.Fatalf("round trip %v", err)
	}
	saved := append([]byte(nil), raw...)
	raw[0] = 'X'
	if item := onlyItem(t, doc, "openai"); len(item.CurrentMeasures) == 1 && item.CurrentMeasures[0].Value.Decimal != nil {
		mutated := "99"
		item.CurrentMeasures[0].Value.Decimal = &mutated
		item.Observation.Measures[0].Value.Decimal = &mutated
	}
	doc2, raw2, err := collection.Snapshot(collectionServed)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw2) != string(saved) || deref(onlyItem(t, doc2, "openai").CurrentMeasures[0].Value.Decimal) != "12.5" {
		t.Fatal("snapshot aliased caller memory")
	}
	reader := session.NewCachedAllowanceReader(saved, providerallowance.FixtureParseLimits, []providerallowance.TrustedContext{trust}, reg, session.AllowanceReadPolicy{MaxAge: time.Hour, Set: true})
	rows := reader.Allowances(collectionServed)
	if len(rows) != 4 || rows[1].Provider != "Codex" || rows[1].Value != portal.AllowanceMeasured || len(rows[1].Windows) != 1 || rows[1].Windows[0].Percent != "12.5% used" || rows[3].Provider != "Grok" || rows[3].Value != portal.AllowanceNotConnected {
		t.Fatalf("%#v", rows)
	}
	if rows[0].Value == portal.AllowanceMeasured || rows[2].Value == portal.AllowanceMeasured {
		t.Fatal("missing providers were presented as measured quota")
	}
}

func TestCollectionRejectsUnboundedConfig(t *testing.T) {
	clock := &fakeClock{now: collectionStart}
	wait := &fakeWait{}
	reg := testRegistry(t, true)
	base := collectionBounds(time.Minute, 1)
	bad := base
	bad.MaxCycles = 9
	if _, err := providerallowance.NewCollection(bad, nil, reg, clock, wait, &scriptAcquire{t: t}); !errors.Is(err, providerallowance.ErrCollectionBounds) || strings.Contains(err.Error(), "9") {
		t.Fatal(err)
	}
	bad = base
	bad.CallTimeout = 0
	if _, err := providerallowance.NewCollection(bad, nil, reg, clock, wait, &scriptAcquire{t: t}); !errors.Is(err, providerallowance.ErrCollectionBounds) {
		t.Fatal(err)
	}
	dup := []providerallowance.SourceSpec{openaiSpec(sp("fixture-account")), openaiSpec(sp("fixture-account"))}
	if _, err := providerallowance.NewCollection(base, dup, reg, clock, wait, &scriptAcquire{t: t}); !errors.Is(err, providerallowance.ErrCollectionSources) {
		t.Fatal(err)
	}
	if _, err := providerallowance.NewCollection(base, []providerallowance.SourceSpec{openaiSpec(sp("fixture-account"))}, nil, clock, wait, &scriptAcquire{t: t}); !errors.Is(err, providerallowance.ErrCollectionRegistry) {
		t.Fatal(err)
	}
	if _, err := providerallowance.NewCollection(base, nil, reg, nil, wait, &scriptAcquire{t: t}); !errors.Is(err, providerallowance.ErrCollectionClock) {
		t.Fatal(err)
	}
}

const (
	collectionCaptured = "2026-11-02T03:00:00Z"
	collectionObserved = "2026-11-02T02:59:00Z"
	collectionReset    = "2026-11-02T06:00:00Z"
)

var (
	collectionStart  = time.Date(2026, 11, 2, 3, 5, 0, 0, time.UTC)
	collectionServed = time.Date(2026, 11, 2, 3, 6, 0, 0, time.UTC)
)

// collectionSuccessLatency is the provider time a successful callback consumes.
// collectionCallTimeout is the common per-call budget and stays strictly above it.
// The previous 50ms budget classified every 1s success as a timeout.
const (
	collectionSuccessLatency = time.Second
	collectionCallTimeout    = 2 * time.Second
)

type obsOpts struct {
	id, provider, decimal, captured, observed, reset, entitlement, product string
	sequence                                                               int
	binding                                                                providerallowance.BindingState
	precision                                                              providerallowance.ResetPrecision
	omitObserved, omitAccount                                              bool
}

type fakeClock struct {
	now   time.Time
	calls int
}

func (f *fakeClock) Now() time.Time {
	f.calls++
	return f.now
}

func (f *fakeClock) Advance(d time.Duration) { f.now = f.now.Add(d) }

type fakeWait struct {
	calls []time.Duration
	err   error
}

func (f *fakeWait) Wait(_ context.Context, d time.Duration) error {
	f.calls = append(f.calls, d)
	return f.err
}

type acquireStep func(context.Context, providerallowance.SourceSpec) (providerallowance.Acquisition, error)

type scriptAcquire struct {
	t     *testing.T
	steps map[string][]acquireStep
	index map[string]int
	calls map[string]int
	total int
}

func (s *scriptAcquire) Acquire(ctx context.Context, source providerallowance.SourceSpec) (providerallowance.Acquisition, error) {
	if s.index == nil {
		s.index = map[string]int{}
	}
	if s.calls == nil {
		s.calls = map[string]int{}
	}
	s.total++
	s.calls[source.Provider]++
	list := s.steps[source.Provider]
	if len(list) == 0 {
		s.t.Fatalf("unexpected acquisition %s", source.Provider)
	}
	i := s.index[source.Provider]
	s.index[source.Provider] = i + 1
	if i >= len(list) {
		i = len(list) - 1
	}
	return list[i](ctx, source)
}

func successStep(clock *fakeClock, raw []byte) acquireStep {
	return func(context.Context, providerallowance.SourceSpec) (providerallowance.Acquisition, error) {
		clock.Advance(collectionSuccessLatency)
		return providerallowance.Acquisition{Observation: append([]byte(nil), raw...)}, nil
	}
}

func collectionBounds(maxAge time.Duration, cycles int) providerallowance.Bounds {
	return providerallowance.Bounds{
		Enabled: true, MaxCycles: cycles, MaxSources: 8, MaxBytes: 8192,
		CallTimeout: collectionCallTimeout, Cadence: 5 * time.Second, Backoff: time.Second,
		MaxAge: maxAge, Parse: providerallowance.FixtureParseLimits,
	}
}

func mustCollection(t *testing.T, bounds providerallowance.Bounds, sources []providerallowance.SourceSpec, reg providerallowance.Registry, clock *fakeClock, wait *fakeWait, acquire providerallowance.Acquirer) *providerallowance.Collection {
	t.Helper()
	collection, err := providerallowance.NewCollection(bounds, sources, reg, clock, wait, acquire)
	if err != nil {
		t.Fatal(err)
	}
	return collection
}

func openaiSpec(account *string) providerallowance.SourceSpec {
	return providerSpec("openai", account)
}

func providerSpec(provider string, account *string) providerallowance.SourceSpec {
	spec := providerallowance.SourceSpec{
		Provider: provider, ProducerRef: "fixture-producer", ProducerIncarnation: "fixture-incarnation-1",
		BindingGeneration: 0, BindingState: providerallowance.BindingVerified,
		SourceKind: providerallowance.SourceNativeRPC, SourceVersion: "fixture-v1",
		AccountRef: account, WorkspaceRef: sp("fixture-workspace"),
	}
	if account == nil {
		spec.WorkspaceRef = nil
		spec.BindingState = providerallowance.BindingUnknown
	}
	return spec
}

func collectOne(t *testing.T, bounds providerallowance.Bounds, opts obsOpts) providerallowance.ReadResponse {
	t.Helper()
	clock := &fakeClock{now: collectionStart}
	raw := collectionObs(t, opts)
	spec := providerSpec(opts.provider, sp("fixture-account"))
	if opts.omitAccount {
		spec.AccountRef = nil
		spec.WorkspaceRef = nil
		spec.BindingState = providerallowance.BindingUnknown
	}
	if opts.binding != "" {
		spec.BindingState = opts.binding
	}
	acquire := &scriptAcquire{t: t, steps: map[string][]acquireStep{opts.provider: {successStep(clock, raw)}}}
	collection := mustCollection(t, bounds, []providerallowance.SourceSpec{spec}, testRegistry(t, true), clock, &fakeWait{}, acquire)
	if err := collection.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	return docOf(t, collection)
}

func collectProviders(t *testing.T, verified bool, inputs map[string]obsOpts) providerallowance.ReadResponse {
	t.Helper()
	clock := &fakeClock{now: collectionStart}
	order := []string{"cursor", "grok", "xai"}
	var specs []providerallowance.SourceSpec
	steps := map[string][]acquireStep{}
	for _, provider := range order {
		opts, ok := inputs[provider]
		if !ok {
			continue
		}
		specs = append(specs, providerSpec(provider, sp("fixture-account")))
		raw := collectionObs(t, opts)
		steps[provider] = []acquireStep{successStep(clock, raw)}
	}
	collection := mustCollection(t, collectionBounds(30*time.Minute, 1), specs, testRegistry(t, verified), clock, &fakeWait{}, &scriptAcquire{t: t, steps: steps})
	if err := collection.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	return docOf(t, collection)
}

func docOf(t *testing.T, collection *providerallowance.Collection) providerallowance.ReadResponse {
	t.Helper()
	doc, _, err := collection.Snapshot(collectionServed)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func onlyItem(t *testing.T, doc providerallowance.ReadResponse, provider string) providerallowance.ReadItem {
	t.Helper()
	var found *providerallowance.ReadItem
	for i := range doc.Items {
		if doc.Items[i].Observation.Provider != provider {
			continue
		}
		if found != nil {
			t.Fatalf("duplicate %s", provider)
		}
		found = &doc.Items[i]
	}
	if found == nil {
		t.Fatalf("missing %s", provider)
	}
	return *found
}

func statusBy(t *testing.T, doc providerallowance.ReadResponse, provider string) providerallowance.SourceStatus {
	t.Helper()
	var found *providerallowance.SourceStatus
	for i := range doc.SourceStatuses {
		if doc.SourceStatuses[i].Provider != provider {
			continue
		}
		if found != nil {
			t.Fatalf("duplicate status %s", provider)
		}
		found = &doc.SourceStatuses[i]
	}
	if found == nil {
		t.Fatalf("missing status %s", provider)
	}
	return *found
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func collectionObs(t *testing.T, opts obsOpts) []byte {
	t.Helper()
	if opts.captured == "" {
		opts.captured = collectionCaptured
	}
	if opts.observed == "" && !opts.omitObserved {
		opts.observed = collectionObserved
	}
	if opts.reset == "" {
		opts.reset = collectionReset
	}
	if opts.precision == "" {
		opts.precision = providerallowance.ResetInstant
	}
	if opts.binding == "" {
		opts.binding = providerallowance.BindingVerified
	}
	if opts.sequence == 0 {
		opts.sequence = 1
	}
	if opts.product == "" {
		opts.product = opts.provider
	}
	var observed *string
	if !opts.omitObserved {
		observed = sp(opts.observed)
	}
	var account, workspace, entitlement *string
	if !opts.omitAccount {
		account = sp("fixture-account")
		workspace = sp("fixture-workspace")
	}
	if opts.entitlement != "" {
		entitlement = sp(opts.entitlement)
	}
	reset := opts.reset
	minutes := providerallowance.Count(300)
	obs := providerallowance.Observation{
		Schema: providerallowance.SchemaObservation, ObservationID: opts.id, ProducerRef: "fixture-producer",
		ProducerIncarnation: "fixture-incarnation-1", Sequence: providerallowance.Count(opts.sequence), BindingGeneration: 0,
		Provider: opts.provider, Source: providerallowance.Source{Kind: providerallowance.SourceNativeRPC, Version: "fixture-v1", CapabilityRef: "synthetic:not-live"},
		Scope:      providerallowance.Scope{AccountRef: account, WorkspaceRef: workspace, EntitlementRef: entitlement, Products: []string{opts.product}, BindingState: opts.binding},
		CapturedAt: opts.captured, ProviderObservedAt: observed, Availability: providerallowance.AvailabilityAvailable, Completeness: providerallowance.CompletenessComplete,
		DirectFields: []providerallowance.DirectField{}, Measures: []providerallowance.Measure{includedMeasure("window", "five-hour-window", opts.decimal, &reset, opts.precision, &minutes)},
		Derivations: []providerallowance.Derivation{}, EvidenceRefs: []string{},
	}
	return remarshalObs(t, obs)
}

func includedMeasure(id, window, decimal string, reset *string, precision providerallowance.ResetPrecision, minutes *providerallowance.Count) providerallowance.Measure {
	return providerallowance.Measure{
		ID: id, Category: providerallowance.CategoryIncluded, Unit: providerallowance.UnitPercent, Direction: providerallowance.DirectionUsed,
		Window:         providerallowance.Window{Ref: window, DurationMinutes: minutes, ResetAt: reset, ResetPrecision: precision},
		Value:          providerallowance.MeasureValue{State: providerallowance.ValueKnown, Decimal: sp(decimal)},
		ValidationRef:  sp("fixture:verified-field-v1"),
		EvidenceFields: []string{},
	}
}

func twoMeterObs(t *testing.T, id, left, right string) []byte {
	t.Helper()
	obs := decodeObs(t, collectionObs(t, obsOpts{id: id, provider: "openai", decimal: left}))
	reset := collectionReset
	minutes := providerallowance.Count(300)
	second := includedMeasure("window-b", "five-hour-window", right, &reset, providerallowance.ResetInstant, &minutes)
	obs.Measures[0].ID = "window-a"
	obs.Measures = []providerallowance.Measure{obs.Measures[0], second}
	return remarshalObs(t, obs)
}

func spendObs(t *testing.T) []byte {
	t.Helper()
	obs := decodeObs(t, collectionObs(t, obsOpts{id: "spend", provider: "openai", decimal: "40"}))
	usd := "USD"
	minutes := providerallowance.Count(300)
	reset := collectionReset
	obs.Measures = append(obs.Measures, providerallowance.Measure{
		ID: "spend", Category: providerallowance.CategorySpend, Unit: providerallowance.UnitMoney, Currency: &usd, Direction: providerallowance.DirectionUsed,
		Window:         providerallowance.Window{Ref: "spend-window", DurationMinutes: &minutes, ResetAt: &reset, ResetPrecision: providerallowance.ResetInstant},
		Value:          providerallowance.MeasureValue{State: providerallowance.ValueKnown, Decimal: sp("5")},
		ValidationRef:  sp("fixture:spend"),
		EvidenceFields: []string{},
	})
	return remarshalObs(t, obs)
}

func decodeObs(t *testing.T, raw []byte) providerallowance.Observation {
	t.Helper()
	obs, err := providerallowance.DecodeObservation(raw, providerallowance.FixtureParseLimits)
	if err != nil {
		t.Fatal(err)
	}
	return obs
}

func remarshalObs(t *testing.T, obs providerallowance.Observation) []byte {
	t.Helper()
	raw, err := json.Marshal(obs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = providerallowance.DecodeObservation(raw, providerallowance.FixtureParseLimits); err != nil {
		t.Fatal(err)
	}
	return raw
}

func fixtureOnlyRegistry(t *testing.T) *providerallowance.StaticRegistry {
	t.Helper()
	reg := testRegistry(t, true)
	var accounts []providerallowance.AccountRule
	for _, account := range reg.Accounts() {
		if account.AccountRef == "fixture-account" && account.Generation == 0 {
			accounts = append(accounts, account)
		}
	}
	if len(accounts) != 1 {
		t.Fatalf("fixture account count %d", len(accounts))
	}
	return providerallowance.NewStaticRegistry(providerallowance.RegistryParts{
		Sources: reg.Sources(), Metrics: reg.Metrics(), Accounts: accounts, Entitlements: reg.Entitlements(),
	})
}

func collectCycles(t *testing.T, reg providerallowance.Registry, cycles int, raws ...[]byte) (providerallowance.ReadResponse, []byte) {
	t.Helper()
	clock := &fakeClock{now: collectionStart}
	steps := make([]acquireStep, 0, len(raws))
	for _, raw := range raws {
		raw := append([]byte(nil), raw...)
		steps = append(steps, successStep(clock, raw))
	}
	acquire := &scriptAcquire{t: t, steps: map[string][]acquireStep{"openai": steps}}
	collection := mustCollection(t, collectionBounds(30*time.Minute, cycles), []providerallowance.SourceSpec{openaiSpec(sp("fixture-account"))}, reg, clock, &fakeWait{}, acquire)
	if err := collection.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if acquire.total != len(raws) {
		t.Fatalf("acquire calls=%d", acquire.total)
	}
	doc, raw, err := collection.Snapshot(collectionServed)
	if err != nil {
		t.Fatal(err)
	}
	return doc, raw
}

func mutateObs(t *testing.T, raw []byte, fn func(*providerallowance.Observation)) []byte {
	t.Helper()
	obs := decodeObs(t, raw)
	fn(&obs)
	return remarshalObs(t, obs)
}

func testRegistry(t *testing.T, verifiedPool bool) *providerallowance.StaticRegistry {
	t.Helper()
	min, max, maxMoney := "0", "100", "1000"
	usd := "USD"
	providers := []string{"cursor", "grok", "xai", "openai", "anthropic"}
	sources := make([]providerallowance.SourceRule, 0, len(providers))
	metrics := make([]providerallowance.MetricRule, 0, len(providers)+1)
	for _, provider := range providers {
		provider := provider
		sources = append(sources, providerallowance.SourceRule{
			Provider: provider, Kind: providerallowance.SourceNativeRPC, Version: "fixture-v1", CapabilityRef: "synthetic:not-live",
			AllowNormalizedMeasures: true, AllowDerivations: true,
		})
		metrics = append(metrics, providerallowance.MetricRule{
			ValidationRef: "fixture:verified-field-v1", Provider: provider, SourceKind: providerallowance.SourceNativeRPC, SourceVersion: "fixture-v1",
			Category: providerallowance.CategoryIncluded, Unit: providerallowance.UnitPercent, Direction: providerallowance.DirectionUsed,
			Windows: []string{"five-hour-window", "seven-day-window"}, Min: &min, Max: &max,
		})
	}
	metrics = append(metrics, providerallowance.MetricRule{
		ValidationRef: "fixture:spend", Provider: "openai", SourceKind: providerallowance.SourceNativeRPC, SourceVersion: "fixture-v1",
		Category: providerallowance.CategorySpend, Unit: providerallowance.UnitMoney, Direction: providerallowance.DirectionUsed,
		Windows: []string{"spend-window"}, Currency: &usd, Min: &min, Max: &maxMoney,
	})
	return providerallowance.NewStaticRegistry(providerallowance.RegistryParts{
		Sources: sources, Metrics: metrics,
		Accounts: []providerallowance.AccountRule{
			{AccountRef: "fixture-account", WorkspaceRef: "fixture-workspace", Generation: 0, ProducerRef: "fixture-producer", ProducerIncarnation: "fixture-incarnation-1"},
			{AccountRef: "other-account", WorkspaceRef: "fixture-workspace", Generation: 1, ProducerRef: "fixture-producer", ProducerIncarnation: "fixture-incarnation-1"},
		},
		Entitlements: []providerallowance.EntitlementRule{
			{
				EntitlementRef: "entitlement-E", Products: []string{"cursor", "grok"}, Verified: verifiedPool,
				Bindings: []providerallowance.EntitlementBinding{
					{AccountRef: "fixture-account", WorkspaceRef: "fixture-workspace", Generation: 0, ProducerRef: "fixture-producer", ProducerIncarnation: "fixture-incarnation-1", Product: "cursor"},
					{AccountRef: "fixture-account", WorkspaceRef: "fixture-workspace", Generation: 0, ProducerRef: "fixture-producer", ProducerIncarnation: "fixture-incarnation-1", Product: "grok"},
				},
			},
			{
				EntitlementRef: "entitlement-X", Products: []string{"xai", "cursor"}, Verified: true,
				Bindings: []providerallowance.EntitlementBinding{
					{AccountRef: "fixture-account", WorkspaceRef: "fixture-workspace", Generation: 0, ProducerRef: "fixture-producer", ProducerIncarnation: "fixture-incarnation-1", Product: "xai"},
					{AccountRef: "fixture-account", WorkspaceRef: "fixture-workspace", Generation: 0, ProducerRef: "fixture-producer", ProducerIncarnation: "fixture-incarnation-1", Product: "cursor"},
				},
			},
		},
	})
}
