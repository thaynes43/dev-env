package providerallowance_test

import (
	"testing"

	"github.com/thaynes43/dev-env/internal/providerallowance"
)

func TestEmptyDerivationIsNotAZeroAllowance(t *testing.T) {
	obs := byID(t, "synthetic-multiple-windows").Observation
	if len(obs.Derivations) != 0 {
		t.Fatal("fixture already has a derivation")
	}
	got := providerallowance.Derive(obs, trustLike(obs, providerallowance.BindingVerified), reviewedRegistry())
	if got.Confirmed || got.Values != nil || len(got.Issues) != 0 {
		t.Fatalf("%#v", got)
	}
}

func TestComplementConfirmsOnlyExactInRangeValues(t *testing.T) {
	reg := reviewedRegistry()
	positive := complementObservation(t, "0", "100")
	before := snapshot(t, positive)
	got := providerallowance.Derive(positive, trustLike(positive, providerallowance.BindingVerified), reg)
	assertUnchanged(t, before, positive)
	if !got.Confirmed || len(got.Values) != 1 || got.Values[0].Lexical != "100" || len(got.Issues) != 0 {
		t.Fatalf("%#v", got)
	}
	fraction := complementObservation(t, "0.36", "99.64")
	got = providerallowance.Derive(fraction, trustLike(fraction, providerallowance.BindingVerified), reg)
	if !got.Confirmed || got.Values[0].Lexical != "99.64" {
		t.Fatalf("%#v", got)
	}
	lexical := complementObservation(t, "0", "100.00")
	got = providerallowance.Derive(lexical, trustLike(lexical, providerallowance.BindingVerified), reg)
	if !got.Confirmed || got.Values[0].Lexical != "100.00" {
		t.Fatalf("%#v", got)
	}

	partial := complementObservation(t, "0", "100")
	partial.Completeness = providerallowance.CompletenessPartial
	if got = providerallowance.Derive(partial, trustLike(partial, providerallowance.BindingVerified), reg); got.Confirmed || !hasCode(got.Issues, providerallowance.IssueDerivationPartial) || len(got.Values) != 0 {
		t.Fatalf("%#v", got)
	}
	unknown := complementObservation(t, "0", "100")
	unknown.Measures[0].ID = "unknown-meter"
	unknown.Measures[0].Value = providerallowance.MeasureValue{State: providerallowance.ValueUnknown}
	unknown.Derivations[0].InputRefs = []string{"unknown-meter"}
	if got = providerallowance.Derive(unknown, trustLike(unknown, providerallowance.BindingVerified), reg); got.Confirmed || !hasCode(got.Issues, providerallowance.IssueDerivationUnknownInput) {
		t.Fatalf("%#v", got)
	}
	missing := complementObservation(t, "0", "100")
	missing.Derivations[0].InputRefs = nil
	if got = providerallowance.Derive(missing, trustLike(missing, providerallowance.BindingVerified), reg); got.Confirmed || !hasCode(got.Issues, providerallowance.IssueDerivationInputMissing) {
		t.Fatalf("%#v", got)
	}
	external := complementObservation(t, "0", "100")
	external.Derivations[0].InputRefs = []string{"other-account:seven-day"}
	if got = providerallowance.Derive(external, trustLike(external, providerallowance.BindingVerified), reg); got.Confirmed || !hasCode(got.Issues, providerallowance.IssueDerivationExternal) {
		t.Fatalf("%#v", got)
	}
	forged := complementObservation(t, "0", "99")
	if got = providerallowance.Derive(forged, trustLike(forged, providerallowance.BindingVerified), reg); got.Confirmed || !hasCode(got.Issues, providerallowance.IssueDerivationForged) || len(got.Values) != 0 {
		t.Fatalf("%#v", got)
	}
	repeated := complementObservation(t, "0", "100")
	repeated.Derivations[0].InputRefs = []string{"seven-day", "seven-day"}
	if got = providerallowance.Derive(repeated, trustLike(repeated, providerallowance.BindingVerified), reg); got.Confirmed || !hasCode(got.Issues, providerallowance.IssueDerivationDuplicateInput) {
		t.Fatalf("%#v", got)
	}
	untrusted := complementObservation(t, "0", "100")
	if got = providerallowance.Derive(untrusted, trustLike(untrusted, providerallowance.BindingUnknown), reg); got.Confirmed || !hasCode(got.Issues, providerallowance.IssueBindingForged) {
		t.Fatalf("%#v", got)
	}
	stale := complementObservation(t, "0", "100")
	stale.ProviderObservedAt = nil
	if got = providerallowance.Derive(stale, trustLike(stale, providerallowance.BindingVerified), reg); got.Confirmed || !hasCode(got.Issues, providerallowance.IssueFreshnessUnknown) {
		t.Fatalf("%#v", got)
	}
}

func TestNegativeComplementIsNotEmitted(t *testing.T) {
	obs := cloneObservation(t, byID(t, "synthetic-supported-over-limit").Observation)
	obs.Scope.AccountRef = sp("fixture-account")
	obs.Scope.WorkspaceRef = sp("fixture-workspace")
	obs.Scope.BindingState = providerallowance.BindingVerified
	obs.ProviderObservedAt = sp("2026-10-24T15:59:00Z")
	obs.Completeness = providerallowance.CompletenessComplete
	obs.Derivations = []providerallowance.Derivation{{
		ID: "remaining", Formula: providerallowance.FormulaComplement, InputRefs: []string{"over-limit"},
		Unit: providerallowance.UnitPercent, Precision: "exact",
		Value: providerallowance.MeasureValue{State: providerallowance.ValueKnown, Decimal: sp("-1.25")},
	}}
	reg := reviewedRegistry()
	unsupported := providerallowance.Derive(obs, trustLike(obs, providerallowance.BindingVerified), reg)
	if unsupported.Confirmed || !hasCode(unsupported.Issues, providerallowance.IssueDerivationFormula) || len(unsupported.Values) != 0 || *obs.Measures[0].Value.Decimal != "101.25" {
		t.Fatalf("%#v", unsupported)
	}
	supporting := providerallowance.NewStaticRegistry(providerallowance.RegistryParts{
		Sources: reg.Sources(), Accounts: reg.Accounts(),
		Metrics: []providerallowance.MetricRule{{
			ValidationRef: "fixture:explicit-overlimit-source", Provider: "synthetic-provider",
			SourceKind: providerallowance.SourceSynthetic, SourceVersion: "fixture-v1",
			Category: providerallowance.CategoryIncluded, Unit: providerallowance.UnitPercent, Direction: providerallowance.DirectionUsed,
			Windows: []string{"over-limit-window"}, Min: sp("0"), AllowOverLimit: true, ComplementOf: "100",
		}},
	})
	before := snapshot(t, obs)
	negative := providerallowance.Derive(obs, trustLike(obs, providerallowance.BindingVerified), supporting)
	assertUnchanged(t, before, obs)
	if negative.Confirmed || !hasCode(negative.Issues, providerallowance.IssueDerivationNegative) || len(negative.Values) != 0 {
		t.Fatalf("%#v", negative)
	}
}

func TestSumsStayInsideOneCompatibleWindow(t *testing.T) {
	obs := compatibleSum(t, "0.10", "0.20", "0.30")
	reg := reviewedRegistry()
	before := snapshot(t, obs)
	got := providerallowance.Derive(obs, trustLike(obs, providerallowance.BindingVerified), reg)
	assertUnchanged(t, before, obs)
	if !got.Confirmed || len(got.Values) != 1 || got.Values[0].Lexical != "0.30" {
		t.Fatalf("%#v", got)
	}
	crossed := compatibleSum(t, "0.10", "0.20", "0.30")
	crossed.Measures[1].Window.Ref = "seven-day-window"
	duration := providerallowance.Count(10080)
	crossed.Measures[1].Window.DurationMinutes = &duration
	if got = providerallowance.Derive(crossed, trustLike(crossed, providerallowance.BindingVerified), reg); got.Confirmed || !hasCode(got.Issues, providerallowance.IssueDerivationIncompatible) {
		t.Fatalf("%#v", got)
	}
	units := compatibleSum(t, "0.10", "0.20", "0.30")
	units.Measures[1].Unit = "bytes"
	if got = providerallowance.Derive(units, trustLike(units, providerallowance.BindingVerified), reg); got.Confirmed || !hasCode(got.Issues, providerallowance.IssueDerivationIncompatible) {
		t.Fatalf("%#v", got)
	}
	currencies := compatibleSum(t, "0.10", "0.20", "0.30")
	currencies.Measures[0].Unit = providerallowance.UnitMoney
	currencies.Measures[1].Unit = providerallowance.UnitMoney
	currencies.Measures[0].Currency = sp("USD")
	currencies.Measures[1].Currency = sp("EUR")
	if got = providerallowance.Derive(currencies, trustLike(currencies, providerallowance.BindingVerified), reg); got.Confirmed || !hasCode(got.Issues, providerallowance.IssueDerivationIncompatible) {
		t.Fatalf("%#v", got)
	}
	aliasedReg := providerallowance.NewStaticRegistry(providerallowance.RegistryParts{
		Sources: reg.Sources(), Metrics: reg.Metrics(), Accounts: reg.Accounts(),
		Projections: []providerallowance.ProjectionRule{{
			ID: "legacy-bucket", Provider: "synthetic-provider", SourceVersion: "fixture-v1",
			MeasureIDs: []string{"part-a", "part-b"},
		}},
	})
	aliased := compatibleSum(t, "0.10", "0.20", "0.30")
	if got = providerallowance.Derive(aliased, trustLike(aliased, providerallowance.BindingVerified), aliasedReg); got.Confirmed || !hasCode(got.Issues, providerallowance.IssueProjectionAlias) || len(got.Values) != 0 {
		t.Fatalf("%#v", got)
	}
	projection := providerallowance.Projections(aliased, aliasedReg)
	if !projection.Aliased || projection.Count != 1 || projection.ProjectionID != "legacy-bucket" {
		t.Fatalf("%#v", projection)
	}
	plain := providerallowance.Projections(aliased, reg)
	if plain.Aliased || plain.Count != 2 || plain.ProjectionID != "" {
		t.Fatalf("%#v", plain)
	}
	scoped := providerallowance.NewStaticRegistry(providerallowance.RegistryParts{
		Sources: reg.Sources(), Accounts: reg.Accounts(),
		Metrics: []providerallowance.MetricRule{
			modelRule("fixture:model-a", "selected-only"),
			modelRule("fixture:model-b", "other-scope"),
		},
	})
	models := compatibleSum(t, "0.10", "0.20", "0.30")
	models.Measures[0].ValidationRef = sp("fixture:model-a")
	models.Measures[1].ValidationRef = sp("fixture:model-b")
	got = providerallowance.Derive(models, trustLike(models, providerallowance.BindingVerified), scoped)
	assessed := providerallowance.ValidateObservation(models, trustLike(models, providerallowance.BindingVerified), scoped)
	if got.Confirmed || !hasCode(got.Issues, providerallowance.IssueModelIncompatible) || assessed.ServedModel != providerallowance.ServedModelUnknown {
		t.Fatalf("%#v served=%s", got, assessed.ServedModel)
	}
}

func TestSharedPoolsCountVerifiedAliasesOnce(t *testing.T) {
	verified := poolRegistry(true)
	cursorObs, cursorTrust := poolObservation(t, "cursor", "fixture-account")
	grokObs, grokTrust := poolObservation(t, "grok", "fixture-account")
	beforeCursor := snapshot(t, cursorObs)
	beforeGrok := snapshot(t, grokObs)
	got := providerallowance.SharedPools([]providerallowance.Observation{cursorObs, grokObs}, []providerallowance.TrustedContext{cursorTrust, grokTrust}, verified)
	assertUnchanged(t, beforeCursor, cursorObs)
	assertUnchanged(t, beforeGrok, grokObs)
	if !got.Resolved || got.Count != 1 || got.EntitlementRef != "entitlement-E" || len(got.Issues) != 0 {
		t.Fatalf("%#v", got)
	}
	unverified := providerallowance.SharedPools([]providerallowance.Observation{cursorObs, grokObs}, []providerallowance.TrustedContext{cursorTrust, grokTrust}, poolRegistry(false))
	if unverified.Resolved || unverified.Count != 0 || unverified.EntitlementRef != "" {
		t.Fatalf("%#v", unverified)
	}
	forgedTrust := cursorTrust
	forgedTrust.BindingState = providerallowance.BindingUnknown
	forged := providerallowance.SharedPools([]providerallowance.Observation{cursorObs, grokObs}, []providerallowance.TrustedContext{forgedTrust, grokTrust}, verified)
	if forged.Resolved || forged.EntitlementRef != "" {
		t.Fatalf("%#v", forged)
	}
	otherObs, otherTrust := poolObservation(t, "grok", "other-account")
	crossed := providerallowance.SharedPools(
		[]providerallowance.Observation{cursorObs, otherObs},
		[]providerallowance.TrustedContext{cursorTrust, otherTrust},
		poolRegistry(true),
	)
	if crossed.Resolved || crossed.EntitlementRef != "" || !hasCode(crossed.Issues, providerallowance.IssueAccountMismatch) {
		t.Fatalf("%#v", crossed)
	}
	single := providerallowance.SharedPools([]providerallowance.Observation{cursorObs}, []providerallowance.TrustedContext{cursorTrust}, verified)
	if single.Resolved || single.Count != 0 || single.EntitlementRef != "" {
		t.Fatalf("%#v", single)
	}
}

func complementObservation(t *testing.T, used, declared string) providerallowance.Observation {
	t.Helper()
	obs := cloneObservation(t, byID(t, "synthetic-multiple-windows").Observation)
	measure := measureByID(obs, "seven-day")
	measure.Value.Decimal = sp(used)
	obs.Measures = []providerallowance.Measure{measure}
	obs.Completeness = providerallowance.CompletenessComplete
	obs.Derivations = []providerallowance.Derivation{{
		ID: "remaining", Formula: providerallowance.FormulaComplement, InputRefs: []string{"seven-day"},
		Unit: providerallowance.UnitPercent, Precision: "exact",
		Value: providerallowance.MeasureValue{State: providerallowance.ValueKnown, Decimal: sp(declared)},
	}}
	return obs
}

func compatibleSum(t *testing.T, left, right, declared string) providerallowance.Observation {
	t.Helper()
	obs := cloneObservation(t, byID(t, "synthetic-multiple-windows").Observation)
	a := measureByID(obs, "five-hour")
	b := a
	a.ID = "part-a"
	b.ID = "part-b"
	a.Value.Decimal = sp(left)
	b.Value.Decimal = sp(right)
	obs.Measures = []providerallowance.Measure{a, b}
	obs.Completeness = providerallowance.CompletenessComplete
	obs.Derivations = []providerallowance.Derivation{{
		ID: "combined", Formula: providerallowance.FormulaSum, InputRefs: []string{"part-a", "part-b"},
		Unit: providerallowance.UnitPercent, Precision: "exact",
		Value: providerallowance.MeasureValue{State: providerallowance.ValueKnown, Decimal: sp(declared)},
	}}
	return obs
}

func modelRule(ref, scope string) providerallowance.MetricRule {
	return providerallowance.MetricRule{
		ValidationRef: ref, Provider: "synthetic-provider",
		SourceKind: providerallowance.SourceSynthetic, SourceVersion: "fixture-v1",
		Category: providerallowance.CategoryIncluded, Unit: providerallowance.UnitPercent, Direction: providerallowance.DirectionUsed,
		Windows: []string{"five-hour-window"}, Min: sp("0"), Max: sp("100"), ModelScope: scope,
	}
}

func poolRegistry(verified bool) *providerallowance.StaticRegistry {
	return providerallowance.NewStaticRegistry(providerallowance.RegistryParts{
		Accounts: []providerallowance.AccountRule{
			{AccountRef: "fixture-account", WorkspaceRef: "fixture-workspace", Generation: 0, ProducerRef: "fixture-producer", ProducerIncarnation: "fixture-incarnation-1"},
			{AccountRef: "other-account", WorkspaceRef: "fixture-workspace", Generation: 0, ProducerRef: "fixture-producer", ProducerIncarnation: "fixture-incarnation-1"},
		},
		Entitlements: []providerallowance.EntitlementRule{{
			EntitlementRef: "entitlement-E", Products: []string{"cursor", "grok"}, Verified: verified,
			Bindings: []providerallowance.EntitlementBinding{
				{AccountRef: "fixture-account", WorkspaceRef: "fixture-workspace", Generation: 0, ProducerRef: "fixture-producer", ProducerIncarnation: "fixture-incarnation-1", Product: "cursor"},
				{AccountRef: "fixture-account", WorkspaceRef: "fixture-workspace", Generation: 0, ProducerRef: "fixture-producer", ProducerIncarnation: "fixture-incarnation-1", Product: "grok"},
			},
		}},
	})
}

func poolObservation(t *testing.T, product, account string) (providerallowance.Observation, providerallowance.TrustedContext) {
	t.Helper()
	obs := cloneObservation(t, byID(t, "synthetic-multiple-windows").Observation)
	obs.Measures = nil
	obs.Derivations = nil
	obs.Scope.Products = []string{product}
	obs.Scope.EntitlementRef = sp("entitlement-E")
	obs.Scope.AccountRef = sp(account)
	return obs, trustLike(obs, providerallowance.BindingVerified)
}
