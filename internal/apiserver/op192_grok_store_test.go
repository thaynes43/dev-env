package apiserver

import (
	"bytes"
	"context"
	"io"
	"reflect"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/internal/agentrun"
	"github.com/thaynes43/dev-env/internal/providerallowance"
	"github.com/thaynes43/dev-env/web/portal"
	"github.com/thaynes43/dev-env/web/portal/session"
)

// OP192 S1 (research R-07, release R1): the store accepts an optional Grok
// row that is only a typed Not connected gap status of one fixed shape, and a
// three-row document may replace a four-row one so a rollback can return to
// three rows before the operator is rolled back. Synthetic documents only.

func op192GapStatus(provider, servedAt string) providerallowance.SourceStatus {
	code := "source_unconfigured"
	completed := servedAt
	return providerallowance.SourceStatus{
		Provider: provider, ProducerRef: "collection-unconfigured", Availability: providerallowance.AvailabilityUnconfigured,
		LastAttempt: providerallowance.LastAttempt{AttemptedAt: servedAt, CompletedAt: &completed, Result: providerallowance.AttemptUnknown, Code: &code},
		Freshness:   providerallowance.Freshness{State: providerallowance.FreshUnknown, AsOf: servedAt, Basis: providerallowance.BasisNone, Reason: "source not configured"},
		Validation:  providerallowance.ValidationRecord{State: providerallowance.ValidationUnverified, ValidationRefs: []string{}},
		Binding:     providerallowance.BindingRecord{State: providerallowance.BindingUnknown},
	}
}

// op192Doc is a canonical gap-only document: the three required rows and,
// when grok is true, a fourth Grok row.
func op192Doc(servedAt string, grok bool) providerallowance.ReadResponse {
	providers := []string{"cursor", "anthropic", "openai"}
	if grok {
		providers = append(providers, "grok")
	}
	doc := providerallowance.ReadResponse{Schema: providerallowance.SchemaRead, SnapshotRevision: "op192-" + servedAt, ServedAt: servedAt, Items: []providerallowance.ReadItem{}}
	for _, provider := range providers {
		doc.SourceStatuses = append(doc.SourceStatuses, op192GapStatus(provider, servedAt))
	}
	return doc
}

func op192Store() (*AggregateAllowanceReportStore, *slotMem) {
	mem := &slotMem{cm: &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: AggregateAllowanceNamespace, Name: AggregateAllowanceName, ResourceVersion: "1"}, Data: map[string]string{"keep": "yes"}}}
	return &AggregateAllowanceReportStore{
		Writer: mem, Live: mem, Namespace: AggregateAllowanceNamespace, Name: AggregateAllowanceName, DataKey: AggregateAllowanceDataKey,
		Registry: providerallowance.NewStaticRegistry(providerallowance.RegistryParts{}), MaxAge: 30 * time.Minute,
	}, mem
}

func TestOP192StoreAcceptsOptionalGrokRowBothWays(t *testing.T) {
	store, mem := op192Store()
	now := time.Date(2026, 11, 5, 19, 0, 0, 0, time.UTC)
	steps := []struct {
		name string
		doc  providerallowance.ReadResponse
	}{
		{"three-row", op192Doc("2026-11-05T18:00:00Z", false)},
		{"four-row-after-three", op192Doc("2026-11-05T18:10:00Z", true)},
		// Rollback path: a three-row document replaces a stored four-row one.
		{"three-row-after-four", op192Doc("2026-11-05T18:20:00Z", false)},
		{"four-row-again", op192Doc("2026-11-05T18:30:00Z", true)},
	}
	for i, step := range steps {
		raw := canonicalAllowanceFixture(t, step.doc)
		if err := store.Put(context.Background(), raw, now); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		if mem.updates != i+1 || mem.cm.Data[AggregateAllowanceDataKey] != string(raw) || mem.cm.Data["keep"] != "yes" {
			t.Fatalf("%s: not stored (updates %d)", step.name, mem.updates)
		}
	}
	view, err := store.Read(context.Background())
	if err != nil || !view.Found || view.Suppressed {
		t.Fatalf("read %+v %v", view, err)
	}
}

func TestOP192StoreRefusesGrokBeyondNotConnected(t *testing.T) {
	now := time.Date(2026, 11, 5, 19, 0, 0, 0, time.UTC)
	base := op192Doc("2026-11-05T18:00:00Z", false)
	grokAt := func(doc *providerallowance.ReadResponse) *providerallowance.SourceStatus {
		for i := range doc.SourceStatuses {
			if doc.SourceStatuses[i].Provider == "grok" {
				return &doc.SourceStatuses[i]
			}
		}
		t.Fatal("no grok status")
		return nil
	}
	account, workspace, other, policy := "grok-fixture-account", "grok-fixture-workspace", "free text", "policy-1"
	cases := []struct {
		name   string
		mutate func(*providerallowance.ReadResponse)
	}{
		// No Grok account identifier is ever stored, verified or not.
		{"unknown-binding-with-account", func(d *providerallowance.ReadResponse) {
			grokAt(d).Binding.AccountRef = &account
		}},
		{"unknown-binding-with-workspace", func(d *providerallowance.ReadResponse) {
			grokAt(d).Binding.WorkspaceRef = &workspace
		}},
		{"unknown-binding-generation", func(d *providerallowance.ReadResponse) {
			grokAt(d).Binding.Generation = 1
		}},
		{"mismatch-binding-with-refs", func(d *providerallowance.ReadResponse) {
			grokAt(d).Binding = providerallowance.BindingRecord{State: providerallowance.BindingMismatch, AccountRef: &account, WorkspaceRef: &workspace}
		}},
		{"mismatch-binding", func(d *providerallowance.ReadResponse) {
			grokAt(d).Binding.State = providerallowance.BindingMismatch
		}},
		{"validation-source-fields", func(d *providerallowance.ReadResponse) {
			grokAt(d).Validation.State = providerallowance.ValidationSourceFields
		}},
		{"validation-display-only", func(d *providerallowance.ReadResponse) {
			grokAt(d).Validation.State = providerallowance.ValidationDisplayOnly
		}},
		{"validation-partial", func(d *providerallowance.ReadResponse) {
			grokAt(d).Validation.State = providerallowance.ValidationPartial
		}},
		{"validation-rejected", func(d *providerallowance.ReadResponse) {
			grokAt(d).Validation.State = providerallowance.ValidationRejected
		}},
		{"validation-conflict", func(d *providerallowance.ReadResponse) {
			grokAt(d).Validation.State = providerallowance.ValidationConflict
		}},
		{"validation-refs", func(d *providerallowance.ReadResponse) {
			grokAt(d).Validation.ValidationRefs = []string{"grok:weekly"}
		}},
		{"free-text-reason", func(d *providerallowance.ReadResponse) {
			grokAt(d).Freshness.Reason = other
		}},
		{"free-text-producer", func(d *providerallowance.ReadResponse) {
			grokAt(d).ProducerRef = "grok-user-1234"
		}},
		{"other-code", func(d *providerallowance.ReadResponse) {
			code := "acquisition_auth_needed"
			grokAt(d).LastAttempt.Code = &code
		}},
		{"no-code", func(d *providerallowance.ReadResponse) {
			grokAt(d).LastAttempt.Code = nil
		}},
		{"attempt-success", func(d *providerallowance.ReadResponse) {
			grokAt(d).LastAttempt.Result = providerallowance.AttemptSuccess
		}},
		{"second-timestamp", func(d *providerallowance.ReadResponse) {
			later := "2026-11-05T18:11:00Z"
			grokAt(d).LastAttempt.CompletedAt = &later
		}},
		{"policy-ref", func(d *providerallowance.ReadResponse) {
			grokAt(d).Freshness.PolicyRef = &policy
		}},
		{"provider-basis", func(d *providerallowance.ReadResponse) {
			grokAt(d).Freshness.Basis = providerallowance.BasisProvider
		}},
		{"expired", func(d *providerallowance.ReadResponse) {
			grokAt(d).Freshness.State = providerallowance.FreshExpired
		}},
		{"unavailable", func(d *providerallowance.ReadResponse) {
			grokAt(d).Availability = providerallowance.AvailabilityUnavailable
		}},
		{"auth-needed", func(d *providerallowance.ReadResponse) {
			grokAt(d).Availability = providerallowance.AvailabilityAuthNeeded
		}},
		{"available", func(d *providerallowance.ReadResponse) {
			grokAt(d).Availability = providerallowance.AvailabilityAvailable
		}},
		{"verified-binding", func(d *providerallowance.ReadResponse) {
			grokAt(d).Binding = providerallowance.BindingRecord{State: providerallowance.BindingVerified, AccountRef: &account}
		}},
		{"fresh", func(d *providerallowance.ReadResponse) {
			grokAt(d).Freshness.State = providerallowance.FreshFresh
		}},
		{"last-good", func(d *providerallowance.ReadResponse) {
			grokAt(d).LastGood = &providerallowance.LastGood{ObservationID: "grok-1", CapturedAt: "2026-11-05T17:00:00Z", ReceivedAt: "2026-11-05T17:00:01Z"}
		}},
		{"duplicate", func(d *providerallowance.ReadResponse) {
			d.SourceStatuses = append(d.SourceStatuses, *grokAt(d))
		}},
		{"grok-item", func(d *providerallowance.ReadResponse) {
			d.Items = append(d.Items, op192GrokItem())
		}},
		{"xai-status", func(d *providerallowance.ReadResponse) {
			grokAt(d).Provider = "xai"
		}},
		{"grok-replaces-cursor", func(d *providerallowance.ReadResponse) {
			kept := d.SourceStatuses[:0]
			for _, status := range d.SourceStatuses {
				if status.Provider != "cursor" {
					kept = append(kept, status)
				}
			}
			d.SourceStatuses = kept
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, mem := op192Store()
			raw := canonicalAllowanceFixture(t, base)
			if err := store.Put(context.Background(), raw, now); err != nil {
				t.Fatal(err)
			}
			held := mem.cm.Data[AggregateAllowanceDataKey]
			doc := op192Doc("2026-11-05T18:10:00Z", true)
			tc.mutate(&doc)
			refuseAllowancePut(t, store, mem, doc, now, held)
		})
	}
}

func op192GrokItem() providerallowance.ReadItem {
	decimal := "12.5"
	return providerallowance.ReadItem{
		Observation: providerallowance.Observation{
			Schema: providerallowance.SchemaObservation, ObservationID: "grok-1", ProducerRef: "op192-synthetic", ProducerIncarnation: "fixture-1", Sequence: 1,
			Provider: "grok", Source: providerallowance.Source{Kind: providerallowance.SourceSynthetic, Version: "fixture-v1", CapabilityRef: "synthetic:not-live"},
			Scope:      providerallowance.Scope{Products: []string{"grok"}, BindingState: providerallowance.BindingUnknown},
			CapturedAt: "2026-11-05T18:00:00Z", Availability: providerallowance.AvailabilityAvailable, Completeness: providerallowance.CompletenessPartial,
			Measures: []providerallowance.Measure{{ID: "grok-weekly", Category: providerallowance.CategoryIncluded, Window: providerallowance.Window{Ref: "grok-weekly", ResetPrecision: providerallowance.ResetUnknown}, Unit: providerallowance.UnitPercent, Direction: providerallowance.DirectionUsed, Value: providerallowance.MeasureValue{State: providerallowance.ValueKnown, Decimal: &decimal}}},
		},
		ReceivedAt: "2026-11-05T18:00:01Z",
		Validation: providerallowance.ValidationRecord{State: providerallowance.ValidationUnverified},
		Binding:    providerallowance.BindingRecord{State: providerallowance.BindingUnknown},
		Freshness:  providerallowance.Freshness{State: providerallowance.FreshUnknown, AsOf: "2026-11-05T18:00:01Z", Basis: providerallowance.BasisNone},
	}
}

func TestOP192UnconfiguredDocumentShowsGrokNotConnected(t *testing.T) {
	doc := UnconfiguredAggregateDocument()
	want := []string{"cursor", "anthropic", "openai", "grok"}
	if len(doc.SourceStatuses) != len(want) || len(doc.Items) != 0 {
		t.Fatalf("statuses %d items %d", len(doc.SourceStatuses), len(doc.Items))
	}
	for i, status := range doc.SourceStatuses {
		if status.Provider != want[i] || status.Availability != providerallowance.AvailabilityUnconfigured || status.LastGood != nil ||
			status.Binding.State != providerallowance.BindingUnknown || status.Freshness.State != providerallowance.FreshUnknown {
			t.Fatalf("status %d: %+v", i, status)
		}
	}
	store, mem := op192Store()
	if err := store.Put(context.Background(), canonicalAllowanceFixture(t, doc), time.Date(2026, 11, 5, 19, 0, 0, 0, time.UTC)); err != nil || mem.updates != 1 {
		t.Fatalf("empty-slot document refused: %v", err)
	}
}

// TestOP192StoredRowsServeOldAndNewReaders reads a stored four-row and a
// stored three-row document through the GET path. The Grok row reaches the
// reader unchanged, a three-row document gains no Grok status, and the
// Claude, Codex and Cursor rows a three-row reader shows are identical.
func TestOP192StoredRowsServeOldAndNewReaders(t *testing.T) {
	now := time.Date(2026, 11, 5, 19, 0, 0, 0, time.UTC)
	served := func(grok bool) providerallowance.ReadResponse {
		store, _ := op192Store()
		if err := store.Put(context.Background(), canonicalAllowanceFixture(t, op192Doc("2026-11-05T18:00:00Z", grok)), now); err != nil {
			t.Fatal(err)
		}
		h := &AggregateAllowanceReports{Enabled: true, Store: store, Now: func() time.Time { return now }}
		code, body, err := h.readStored(context.Background())
		doc, ok := body.(providerallowance.ReadResponse)
		if err != nil || code != 200 || !ok {
			t.Fatalf("grok=%v read %d %v", grok, code, err)
		}
		return doc
	}
	four, three := served(true), served(false)
	if len(four.SourceStatuses) != 4 || four.SourceStatuses[3].Provider != "grok" || four.SourceStatuses[3].Availability != providerallowance.AvailabilityUnconfigured || len(three.SourceStatuses) != 3 {
		t.Fatalf("served statuses four=%d three=%d", len(four.SourceStatuses), len(three.SourceStatuses))
	}
	if !reflect.DeepEqual(four.SourceStatuses[:3], three.SourceStatuses) {
		t.Fatal("the Grok row changed a required status")
	}
	rowsFour, rowsThree := session.MapProductAllowance(four), session.MapProductAllowance(three)
	if !reflect.DeepEqual(rowsFour[:3], rowsThree[:3]) {
		t.Fatalf("a three-row reader would see different rows:\n%+v\n%+v", rowsFour[:3], rowsThree[:3])
	}
	for _, rows := range [][]portal.ProductAllowance{rowsFour, rowsThree} {
		if len(rows) != 4 || rows[3].Provider != "Grok" || rows[3].Value != portal.AllowanceNotConnected {
			t.Fatalf("grok row %+v", rows)
		}
	}
}

// TestOP192StoreAcceptsEachProducerShape builds the Grok row from the real
// producers' unconfigured rows: the served empty slot, Collection.build (the
// R2 operator and agent-run capture path) and the agent-run local preflight.
// Each is accepted, so R2 needs no further operator release for this row.
func TestOP192StoreAcceptsEachProducerShape(t *testing.T) {
	now := time.Date(2026, 11, 5, 19, 0, 0, 0, time.UTC)
	asGrok := func(t *testing.T, doc providerallowance.ReadResponse, from, code string) providerallowance.ReadResponse {
		t.Helper()
		for _, status := range doc.SourceStatuses {
			if status.Provider == from {
				status.Provider = "grok"
				if code != "" {
					status.LastAttempt.Code = &code
				}
				doc.SourceStatuses = append(doc.SourceStatuses, status)
				return doc
			}
		}
		t.Fatalf("no %s status", from)
		return doc
	}
	bundle, err := providerallowance.AssembleCaptureBundle([]byte(`{"schema":"`+providerallowance.CaptureBundleSchema+`","captures":[]}`),
		providerallowance.AggregateTrust{Registry: providerallowance.NewStaticRegistry(providerallowance.RegistryParts{}), MaxAge: 1800}, now)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := agentrun.Run(context.Background(), []string{"usage", "-o", "json"}, agentrun.Env{Stdout: &out, Stderr: io.Discard, Now: func() time.Time { return now }}); code != 0 {
		t.Fatalf("preflight exit %d", code)
	}
	preflight, err := providerallowance.DecodeReadResponse(out.Bytes(), providerallowance.FixtureParseLimits)
	if err != nil {
		t.Fatal(err)
	}
	for name, doc := range map[string]providerallowance.ReadResponse{
		"empty-slot":      UnconfiguredAggregateDocument(),
		"collection":      asGrok(t, bundle, "cursor", ""),
		"local-preflight": asGrok(t, preflight, "cursor", "local_unconfigured_grok_no_provider_request"),
	} {
		t.Run(name, func(t *testing.T) {
			store, mem := op192Store()
			if err := store.Put(context.Background(), canonicalAllowanceFixture(t, doc), now); err != nil || mem.updates != 1 {
				t.Fatalf("producer shape refused: %v", err)
			}
		})
	}
	// A preflight Grok row must carry the Grok code, not another provider's.
	store, mem := op192Store()
	held := ""
	refuseAllowancePut(t, store, mem, asGrok(t, preflight, "cursor", ""), now, held)
}
