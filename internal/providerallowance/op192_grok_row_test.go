package providerallowance_test

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/providerallowance"
	"github.com/thaynes43/dev-env/web/portal"
	"github.com/thaynes43/dev-env/web/portal/session"
)

// OP192 Grok row contract tests. Every input is synthetic: no provider,
// credential, network or wall clock. They pin the typed-Unknown and no-fold
// invariants that research R-07 (OP192 Grok usage source plan) relies on and
// change no collector acquisition behavior. No Grok source exists yet; "grok"
// here is the separate row the plan proposes, never a Cursor alias.

const op192ReasonAlias = "shared pool alias"

func TestOP192GrokNotConnectedRowIsWireCompatible(t *testing.T) {
	raw := loadJSON(t, "op192/grok-row-not-connected.read.json")
	doc, err := providerallowance.DecodeReadResponse(raw, providerallowance.FixtureParseLimits)
	if err != nil {
		t.Fatal(err)
	}
	readSchema := compileSchema(t, "read-response.schema.json", "urn:dev-env:providerallowance:read:v1:draft")
	if err := readSchema.Validate(instance(t, raw)); err != nil {
		t.Fatalf("a separate grok row needs no read.v1 schema change: %v", err)
	}
	if len(doc.Items) != 0 {
		t.Fatal("a not-connected row carries no item; Unknown is not zero")
	}
	for _, provider := range []string{"anthropic", "openai", "cursor", "grok"} {
		status := statusBy(t, doc, provider)
		if status.Availability != providerallowance.AvailabilityUnconfigured || status.LastGood != nil ||
			status.LastAttempt.Result != providerallowance.AttemptUnknown || deref(status.LastAttempt.Code) != "source_unconfigured" ||
			status.Freshness.State != providerallowance.FreshUnknown || status.Freshness.Basis != providerallowance.BasisNone ||
			status.Validation.State != providerallowance.ValidationUnverified ||
			status.Binding.State != providerallowance.BindingUnknown || status.Binding.AccountRef != nil || status.Binding.WorkspaceRef != nil {
			t.Fatalf("%s is not a typed not-connected row: %+v", provider, status)
		}
	}
	again, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	back, err := providerallowance.DecodeReadResponse(again, providerallowance.FixtureParseLimits)
	if err != nil || !reflect.DeepEqual(doc, back) {
		t.Fatalf("round trip changed the document: %v", err)
	}
	// The grok row never changes what the existing three rows show. The first
	// document has identical statuses, so the conflict variant gives Grok a
	// status that would turn a folded Cursor row into "Conflicting allowance
	// reports".
	conflict := doc
	conflict.SourceStatuses = append([]providerallowance.SourceStatus(nil), doc.SourceStatuses...)
	for i := range conflict.SourceStatuses {
		if conflict.SourceStatuses[i].Provider == "grok" {
			conflict.SourceStatuses[i].Availability = providerallowance.AvailabilityConflict
			conflict.SourceStatuses[i].Validation.State = providerallowance.ValidationConflict
		}
	}
	for name, variant := range map[string]providerallowance.ReadResponse{"not-connected": doc, "grok-conflict": conflict} {
		rows := session.MapProductAllowance(variant)
		if len(rows) != 3 {
			t.Fatalf("%s: rows %d", name, len(rows))
		}
		for _, row := range rows {
			if row.Value != portal.AllowanceNotConnected || row.Reason != "" || len(row.Windows) != 0 || len(row.LastReported) != 0 {
				t.Fatalf("%s: %s: %+v", name, row.Provider, row)
			}
		}
	}
}

// op192Registry is the reviewed test registry with no shared-pool rule. The
// production allowance collector refuses a trust that has any EntitlementRule
// (internal/allowancecollector ParseConfig), so this is the only registry
// shape production can serve today.
func op192Registry(t *testing.T) *providerallowance.StaticRegistry {
	t.Helper()
	reg := testRegistry(t, false)
	return providerallowance.NewStaticRegistry(providerallowance.RegistryParts{Sources: reg.Sources(), Metrics: reg.Metrics(), Accounts: reg.Accounts()})
}

func op192CursorAndGrok(t *testing.T, reg providerallowance.Registry, entitlement string) (providerallowance.ReadResponse, []byte, []providerallowance.TrustedContext) {
	t.Helper()
	clock := &fakeClock{now: collectionStart}
	cursorSpec := providerSpec("cursor", sp("fixture-account"))
	grokSpec := providerSpec("grok", sp("fixture-account"))
	steps := map[string][]acquireStep{
		"cursor": {successStep(clock, collectionObs(t, obsOpts{id: "op192-cursor", provider: "cursor", decimal: "11", entitlement: entitlement}))},
		"grok":   {successStep(clock, collectionObs(t, obsOpts{id: "op192-grok", provider: "grok", decimal: "12.5", entitlement: entitlement}))},
	}
	collection := mustCollection(t, collectionBounds(30*time.Minute, 1), []providerallowance.SourceSpec{cursorSpec, grokSpec}, reg, clock, &fakeWait{}, &scriptAcquire{t: t, steps: steps})
	if err := collection.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	doc, raw, err := collection.Snapshot(collectionServed)
	if err != nil {
		t.Fatal(err)
	}
	return doc, raw, []providerallowance.TrustedContext{cursorSpec.Trusted(), grokSpec.Trusted()}
}

func op192Current(t *testing.T, doc providerallowance.ReadResponse, provider string) providerallowance.CurrentMeasure {
	t.Helper()
	item := onlyItem(t, doc, provider)
	if len(item.CurrentMeasures) != 1 {
		t.Fatalf("%s current measures %+v", provider, item.CurrentMeasures)
	}
	return item.CurrentMeasures[0]
}

func TestOP192GrokNeverFoldsIntoCursorWithoutTrustedPool(t *testing.T) {
	for _, tc := range []struct{ name, entitlement string }{
		{name: "no-payload-claim"},
		// A payload entitlement ref is a claim, not proof of one shared pool.
		{name: "payload-claims-shared-pool", entitlement: "entitlement-E"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := op192Registry(t)
			doc, raw, trusts := op192CursorAndGrok(t, reg, tc.entitlement)
			cursor, grok := op192Current(t, doc, "cursor"), op192Current(t, doc, "grok")
			if cursor.State != providerallowance.CurrentKnown || deref(cursor.Value.Decimal) != "11" {
				t.Fatalf("cursor %+v", cursor)
			}
			if grok.State != providerallowance.CurrentKnown || deref(grok.Value.Decimal) != "12.5" || grok.Reason == op192ReasonAlias {
				t.Fatalf("grok was deduped without a trusted pool: %+v", grok)
			}
			rows := session.NewCachedAllowanceReader(raw, providerallowance.FixtureParseLimits, trusts, reg, session.AllowanceReadPolicy{MaxAge: time.Hour, Set: true}).Allowances(collectionServed)
			if len(rows) != 3 {
				t.Fatalf("rows %d", len(rows))
			}
			for _, row := range rows {
				for _, w := range row.Windows {
					if strings.Contains(w.Percent, "12.5") || strings.Contains(w.Percent, "23.5") {
						t.Fatalf("%s shows a Grok value: %+v", row.Provider, w)
					}
				}
				if row.Provider == "Cursor" && (row.Value != portal.AllowanceMeasured || len(row.Windows) != 1 || row.Windows[0].Percent != "11% used") {
					t.Fatalf("cursor row %+v", row)
				}
			}
		})
	}
	// Control: a caller-trusted, verified pool does demote the grok alias, so
	// the no-fold assertions above are not vacuous. Production cannot supply
	// that rule today.
	t.Run("control-verified-pool-demotes", func(t *testing.T) {
		doc, _, _ := op192CursorAndGrok(t, testRegistry(t, true), "entitlement-E")
		grok := op192Current(t, doc, "grok")
		if grok.State != providerallowance.CurrentNotApplicable || grok.Value.Decimal != nil || grok.Reason != op192ReasonAlias {
			t.Fatalf("control did not demote: %+v", grok)
		}
	})
}

// op192GrokOnce collects one synthetic grok observation through the existing
// collection and returns the served document and its bytes.
func op192GrokOnce(t *testing.T, reg providerallowance.Registry, raw []byte) (providerallowance.ReadResponse, []byte) {
	t.Helper()
	clock := &fakeClock{now: collectionStart}
	spec := providerSpec("grok", sp("fixture-account"))
	collection := mustCollection(t, collectionBounds(30*time.Minute, 1), []providerallowance.SourceSpec{spec}, reg, clock, &fakeWait{}, &scriptAcquire{t: t, steps: map[string][]acquireStep{"grok": {successStep(clock, raw)}}})
	if err := collection.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	doc, served, err := collection.Snapshot(collectionServed)
	if err != nil {
		t.Fatal(err)
	}
	return doc, served
}

func TestOP192GrokRowTypedStates(t *testing.T) {
	base := func(t *testing.T) []byte {
		return collectionObs(t, obsOpts{id: "op192-grok", provider: "grok", decimal: "12.5"})
	}
	t.Run("fractional-percent-kept-exact", func(t *testing.T) {
		doc, _ := op192GrokOnce(t, op192Registry(t), base(t))
		if c := op192Current(t, doc, "grok"); c.State != providerallowance.CurrentKnown || deref(c.Value.Decimal) != "12.5" {
			t.Fatalf("%+v", c)
		}
	})
	t.Run("past-reset-is-expired-not-zero", func(t *testing.T) {
		raw := collectionObs(t, obsOpts{id: "op192-grok", provider: "grok", decimal: "12.5", reset: "2026-11-02T03:05:30Z"})
		doc, _ := op192GrokOnce(t, op192Registry(t), raw)
		item := onlyItem(t, doc, "grok")
		c := op192Current(t, doc, "grok")
		if c.State != providerallowance.CurrentExpired || c.Value.Decimal != nil || deref(item.Observation.Measures[0].Value.Decimal) != "12.5" {
			t.Fatalf("current %+v historical %s", c, deref(item.Observation.Measures[0].Value.Decimal))
		}
	})
	t.Run("unknown-reset-is-not-invented", func(t *testing.T) {
		raw := mutateObs(t, base(t), func(o *providerallowance.Observation) {
			o.Measures[0].Window.ResetAt = nil
			o.Measures[0].Window.ResetPrecision = providerallowance.ResetUnknown
		})
		doc, _ := op192GrokOnce(t, op192Registry(t), raw)
		window := onlyItem(t, doc, "grok").Observation.Measures[0].Window
		if window.ResetAt != nil || window.ResetPrecision != providerallowance.ResetUnknown || window.ResetDisplayDate != nil || window.ResetDisplayText != nil {
			t.Fatalf("reset invented: %+v", window)
		}
	})
	t.Run("absent-percent-is-unknown-not-zero", func(t *testing.T) {
		raw := mutateObs(t, base(t), func(o *providerallowance.Observation) {
			o.Measures[0].Value = providerallowance.MeasureValue{State: providerallowance.ValueUnknown}
		})
		doc, served := op192GrokOnce(t, op192Registry(t), raw)
		c := op192Current(t, doc, "grok")
		if c.State == providerallowance.CurrentKnown || c.Value.Decimal != nil || strings.Contains(string(served), `"decimal":"0"`) {
			t.Fatalf("absent percent became a value: %+v", c)
		}
	})
	t.Run("paid-spend-stays-separate", func(t *testing.T) {
		reg := testRegistry(t, false)
		min, maxMoney, usd := "0", "1000", "USD"
		metrics := append(reg.Metrics(), providerallowance.MetricRule{
			ValidationRef: "fixture:grok-spend", Provider: "grok", SourceKind: providerallowance.SourceNativeRPC, SourceVersion: "fixture-v1",
			Category: providerallowance.CategorySpend, Unit: providerallowance.UnitMoney, Direction: providerallowance.DirectionUsed,
			Windows: []string{"spend-window"}, Currency: &usd, Min: &min, Max: &maxMoney,
		})
		spendReg := providerallowance.NewStaticRegistry(providerallowance.RegistryParts{Sources: reg.Sources(), Metrics: metrics, Accounts: reg.Accounts()})
		raw := mutateObs(t, base(t), func(o *providerallowance.Observation) {
			o.Measures = append(o.Measures, providerallowance.Measure{
				ID: "grok-spend", Category: providerallowance.CategorySpend, Unit: providerallowance.UnitMoney, Currency: &usd, Direction: providerallowance.DirectionUsed,
				Window:         providerallowance.Window{Ref: "spend-window", ResetPrecision: providerallowance.ResetNotApplicable},
				Value:          providerallowance.MeasureValue{State: providerallowance.ValueKnown, Decimal: sp("3.2")},
				ValidationRef:  sp("fixture:grok-spend"),
				EvidenceFields: []string{},
			})
		})
		doc, _ := op192GrokOnce(t, spendReg, raw)
		item := onlyItem(t, doc, "grok")
		if len(item.CurrentMeasures) != 2 || len(item.Observation.Derivations) != 0 {
			t.Fatalf("current %+v derivations %+v", item.CurrentMeasures, item.Observation.Derivations)
		}
		got := map[string]string{}
		for _, c := range item.CurrentMeasures {
			if c.State != providerallowance.CurrentKnown {
				t.Fatalf("%+v", c)
			}
			got[c.MeasureID] = deref(c.Value.Decimal)
		}
		if got["window"] != "12.5" || got["grok-spend"] != "3.2" {
			t.Fatalf("spend changed the included percent: %v", got)
		}
	})
	t.Run("account-switch-drops-old-values", func(t *testing.T) {
		clock := &fakeClock{now: collectionStart}
		reg := op192Registry(t)
		spec := providerSpec("grok", sp("fixture-account"))
		collection := mustCollection(t, collectionBounds(30*time.Minute, 1), []providerallowance.SourceSpec{spec}, reg, clock, &fakeWait{}, &scriptAcquire{t: t, steps: map[string][]acquireStep{"grok": {successStep(clock, base(t))}}})
		if err := collection.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
		next := providerSpec("grok", sp("other-account"))
		next.BindingGeneration = 1
		if err := collection.SetSources([]providerallowance.SourceSpec{next}); err != nil {
			t.Fatal(err)
		}
		doc, served, err := collection.Snapshot(collectionServed)
		if err != nil {
			t.Fatal(err)
		}
		if len(doc.Items) != 0 || strings.Contains(string(served), `"decimal":"12.5"`) || strings.Contains(string(served), "fixture-account") {
			t.Fatal("the previous account's grok value survived the switch")
		}
	})
}
