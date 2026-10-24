package providerallowance_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/providerallowance"
)

func TestAliasGroupsRejectPartialOverlap(t *testing.T) {
	base := reviewedRegistry()
	independent := projectionRegistry(base, providerallowance.ProjectionRule{
		ID: "elsewhere", Provider: "synthetic-provider", SourceVersion: "fixture-v1",
		MeasureIDs: []string{"part-c", "part-d"},
	})
	pair := compatibleSum(t, "0.10", "0.20", "0.30")
	got := providerallowance.Derive(pair, trustLike(pair, providerallowance.BindingVerified), independent)
	if !got.Confirmed || len(got.Values) != 1 || got.Values[0].Lexical != "0.30" {
		t.Fatalf("independent sum = %#v", got)
	}

	larger := threePartSum(t, "0.10", "0.20", "0.30", "0.60")
	subsetRule := projectionRegistry(base, providerallowance.ProjectionRule{
		ID: "pair", Provider: "synthetic-provider", SourceVersion: "fixture-v1",
		MeasureIDs: []string{"part-a", "part-b"},
	})
	got = providerallowance.Derive(larger, trustLike(larger, providerallowance.BindingVerified), subsetRule)
	if got.Confirmed || !hasCode(got.Issues, providerallowance.IssueProjectionAlias) || len(got.Values) != 0 {
		t.Fatalf("superset sum = %#v", got)
	}

	group := projectionRegistry(base, providerallowance.ProjectionRule{
		ID: "trio", Provider: "synthetic-provider", SourceVersion: "fixture-v1",
		MeasureIDs: []string{"part-a", "part-b", "part-c"},
	})
	got = providerallowance.Derive(pair, trustLike(pair, providerallowance.BindingVerified), group)
	if got.Confirmed || !hasCode(got.Issues, providerallowance.IssueProjectionAlias) || len(got.Values) != 0 {
		t.Fatalf("subset sum = %#v", got)
	}

	overlap := projectionRegistry(base,
		providerallowance.ProjectionRule{
			ID: "ab", Provider: "synthetic-provider", SourceVersion: "fixture-v1",
			MeasureIDs: []string{"part-a", "part-b"},
		},
		providerallowance.ProjectionRule{
			ID: "bc", Provider: "synthetic-provider", SourceVersion: "fixture-v1",
			MeasureIDs: []string{"part-b", "part-c"},
		},
	)
	ends := compatibleSum(t, "0.10", "0.20", "0.30")
	ends.Measures[1].ID = "part-c"
	ends.Derivations[0].InputRefs = []string{"part-a", "part-c"}
	got = providerallowance.Derive(ends, trustLike(ends, providerallowance.BindingVerified), overlap)
	if got.Confirmed || !hasCode(got.Issues, providerallowance.IssueProjectionAlias) || len(got.Values) != 0 {
		t.Fatalf("overlap sum = %#v", got)
	}
}

func TestProjectionsRejectAmbiguousGroups(t *testing.T) {
	pair := compatibleSum(t, "0.10", "0.20", "0.30")
	base := reviewedRegistry()
	one := providerallowance.ProjectionRule{
		ID: "legacy-bucket", Provider: "synthetic-provider", SourceVersion: "fixture-v1",
		MeasureIDs: []string{"part-a", "part-b"},
	}
	positive := providerallowance.Projections(pair, projectionRegistry(base, one))
	if !positive.Aliased || positive.Count != 1 || positive.ProjectionID != "legacy-bucket" || len(positive.Issues) != 0 {
		t.Fatalf("positive = %#v", positive)
	}

	duplicated := providerallowance.Projections(pair, projectionRegistry(base, one, one))
	if duplicated.Aliased || duplicated.Count != 0 || !hasCode(duplicated.Issues, providerallowance.IssueRegistryDuplicate) {
		t.Fatalf("duplicate id = %#v", duplicated)
	}
	sameSet := providerallowance.Projections(pair, projectionRegistry(base, one, providerallowance.ProjectionRule{
		ID: "other-name", Provider: "synthetic-provider", SourceVersion: "fixture-v1",
		MeasureIDs: []string{"part-b", "part-a"},
	}))
	if sameSet.Aliased || sameSet.Count != 0 || !hasCode(sameSet.Issues, providerallowance.IssueRegistryDuplicate) {
		t.Fatalf("same set = %#v", sameSet)
	}
	repeated := providerallowance.Projections(pair, projectionRegistry(base, providerallowance.ProjectionRule{
		ID: "repeated", Provider: "synthetic-provider", SourceVersion: "fixture-v1",
		MeasureIDs: []string{"part-a", "part-a"},
	}))
	if repeated.Aliased || repeated.Count != 0 || !hasCode(repeated.Issues, providerallowance.IssueRegistryDuplicate) {
		t.Fatalf("repeated member = %#v", repeated)
	}

	three := threePartSum(t, "0.10", "0.20", "0.30", "0.60")
	overlapping := providerallowance.Projections(three, projectionRegistry(base,
		providerallowance.ProjectionRule{ID: "ab", Provider: "synthetic-provider", SourceVersion: "fixture-v1", MeasureIDs: []string{"part-a", "part-b"}},
		providerallowance.ProjectionRule{ID: "bc", Provider: "synthetic-provider", SourceVersion: "fixture-v1", MeasureIDs: []string{"part-b", "part-c"}},
	))
	if overlapping.Aliased || overlapping.Count != 0 || !hasCode(overlapping.Issues, providerallowance.IssueRegistryDuplicate) {
		t.Fatalf("overlap = %#v", overlapping)
	}

	fourth := three.Measures[0]
	fourth.ID = "part-d"
	fourth.Value.Decimal = sp("0.40")
	disjointObs := three
	disjointObs.Measures = append(append([]providerallowance.Measure{}, three.Measures...), fourth)
	disjointObs.Derivations = nil
	disjoint := providerallowance.Projections(disjointObs, projectionRegistry(base,
		providerallowance.ProjectionRule{ID: "ab", Provider: "synthetic-provider", SourceVersion: "fixture-v1", MeasureIDs: []string{"part-a", "part-b"}},
		providerallowance.ProjectionRule{ID: "cd", Provider: "synthetic-provider", SourceVersion: "fixture-v1", MeasureIDs: []string{"part-c", "part-d"}},
	))
	if !disjoint.Aliased || disjoint.Count != 2 || disjoint.ProjectionID != "" || len(disjoint.Issues) != 0 {
		t.Fatalf("disjoint = %#v", disjoint)
	}

	scalarObs := three
	scalarObs.Derivations = nil
	scalar := providerallowance.Projections(scalarObs, projectionRegistry(base, one))
	if !scalar.Aliased || scalar.Count != 2 || scalar.ProjectionID != "" || len(scalar.Issues) != 0 {
		t.Fatalf("scalar = %#v", scalar)
	}
}

func TestNullValuesMatchSchema(t *testing.T) {
	observation := compileSchema(t, "observation.schema.json", "urn:dev-env:providerallowance:observation:v1:draft")
	readSchema := compileSchema(t, "read-response.schema.json", "urn:dev-env:providerallowance:read:v1:draft")
	raw := byID(t, "synthetic-multiple-windows").Raw
	body := readBody(t)
	if err := observation.Validate(instance(t, raw)); err != nil {
		t.Fatal(err)
	}
	if err := readSchema.Validate(instance(t, body)); err != nil {
		t.Fatal(err)
	}
	codex := byID(t, "historical-codex-188")
	decodedCodex, err := providerallowance.DecodeObservation(codex.Raw, providerallowance.FixtureParseLimits)
	if err != nil || decodedCodex.ProviderObservedAt != nil || decodedCodex.Scope.AccountRef != nil {
		t.Fatalf("nullable observation = %v %#v", err, decodedCodex.Scope)
	}
	readDoc, err := providerallowance.DecodeReadResponse(body, providerallowance.FixtureParseLimits)
	if err != nil || readDoc.NextCursor != nil {
		t.Fatalf("nullable read = %v", err)
	}
	start, err := providerallowance.DecodeObservation(raw, providerallowance.FixtureParseLimits)
	if err != nil || start.Measures[0].Window.StartAt != nil {
		t.Fatal(err)
	}

	rejectObservation := []struct {
		name string
		edit func(map[string]any)
	}{
		{"observationId", func(doc map[string]any) { doc["observationId"] = nil }},
		{"directFields", func(doc map[string]any) { doc["directFields"] = nil }},
		{"evidenceRefs element", func(doc map[string]any) { doc["evidenceRefs"] = []any{nil} }},
		{"products element", func(doc map[string]any) { doc["scope"].(map[string]any)["products"] = []any{nil} }},
		{"source version", func(doc map[string]any) { doc["source"].(map[string]any)["version"] = nil }},
		{"window ref", func(doc map[string]any) {
			doc["measures"].([]any)[0].(map[string]any)["window"].(map[string]any)["ref"] = nil
		}},
		{"evidenceFields element", func(doc map[string]any) {
			doc["measures"].([]any)[0].(map[string]any)["evidenceFields"] = []any{nil}
		}},
		{"inputRefs element", func(doc map[string]any) {
			doc["derivations"] = []any{map[string]any{
				"id": "combined", "formula": "sum_compatible_values", "inputRefs": []any{nil},
				"unit": "percent", "precision": "exact", "value": map[string]any{"state": "unknown", "decimal": nil},
			}}
		}},
	}
	for _, tc := range rejectObservation {
		t.Run(tc.name, func(t *testing.T) {
			next := editJSON(t, raw, tc.edit)
			if err := observation.Validate(instance(t, next)); err == nil {
				t.Fatal("schema accepted null")
			}
			if _, err := providerallowance.DecodeObservation(next, providerallowance.FixtureParseLimits); err == nil || !strings.Contains(err.Error(), "invalid_type") {
				t.Fatal(err)
			}
		})
	}
	rejectRead := []struct {
		name string
		edit func(map[string]any)
	}{
		{"snapshotRevision", func(doc map[string]any) { doc["snapshotRevision"] = nil }},
		{"items", func(doc map[string]any) { doc["items"] = nil }},
		{"validationRefs element", func(doc map[string]any) {
			doc["items"].([]any)[0].(map[string]any)["validation"].(map[string]any)["validationRefs"] = []any{nil}
		}},
	}
	for _, tc := range rejectRead {
		t.Run(tc.name, func(t *testing.T) {
			next := editJSON(t, body, tc.edit)
			if err := readSchema.Validate(instance(t, next)); err == nil {
				t.Fatal("schema accepted null")
			}
			if _, err := providerallowance.DecodeReadResponse(next, providerallowance.FixtureParseLimits); err == nil || !strings.Contains(err.Error(), "invalid_type") {
				t.Fatal(err)
			}
		})
	}

	empty := editJSON(t, raw, func(doc map[string]any) {
		doc["directFields"] = []any{}
		doc["measures"] = []any{}
		doc["derivations"] = []any{}
		doc["evidenceRefs"] = []any{}
		doc["scope"].(map[string]any)["products"] = []any{}
	})
	if err := observation.Validate(instance(t, empty)); err != nil {
		t.Fatal(err)
	}
	decodedEmpty, err := providerallowance.DecodeObservation(empty, providerallowance.FixtureParseLimits)
	if err != nil || decodedEmpty.DirectFields == nil || decodedEmpty.Measures == nil || decodedEmpty.EvidenceRefs == nil || decodedEmpty.Scope.Products == nil {
		t.Fatalf("empty arrays = %v %#v", err, decodedEmpty.DirectFields)
	}

	programmed := cloneObservation(t, byID(t, "synthetic-multiple-windows").Observation)
	programmed.DirectFields = nil
	programmed.Measures[0].EvidenceFields = nil
	programmed.Derivations = nil
	programmed.EvidenceRefs = nil
	programmed.Scope.Products = nil
	encoded, err := json.Marshal(programmed)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"directFields":null`, `"evidenceFields":null`, `"derivations":null`, `"evidenceRefs":null`, `"products":null`} {
		if bytes.Contains(encoded, []byte(key)) {
			t.Fatalf("nil slice serialized as null: %s", key)
		}
	}
	if err := observation.Validate(instance(t, encoded)); err != nil {
		t.Fatal(err)
	}
	again, err := providerallowance.DecodeObservation(encoded, providerallowance.FixtureParseLimits)
	if err != nil || again.DirectFields == nil || again.Measures[0].EvidenceFields == nil || again.EvidenceRefs == nil || again.Scope.Products == nil {
		t.Fatalf("nil slice roundtrip = %v", err)
	}
}

func TestOverLimitKeepsLowerBound(t *testing.T) {
	ready := cloneObservation(t, byID(t, "synthetic-supported-over-limit").Observation)
	ready.Scope.AccountRef = sp("fixture-account")
	ready.Scope.WorkspaceRef = sp("fixture-workspace")
	ready.Scope.BindingState = providerallowance.BindingVerified
	ready.ProviderObservedAt = sp("2026-10-24T15:59:00Z")
	positive := providerallowance.ValidateObservation(ready, trustLike(ready, providerallowance.BindingVerified), reviewedRegistry())
	got := assessmentByID(t, positive, "over-limit")
	if !got.Actionable || !got.InRange || *ready.Measures[0].Value.Decimal != "101.25" {
		t.Fatalf("positive = %#v", got)
	}

	negative := cloneObservation(t, ready)
	negative.Measures[0].Value.Decimal = sp("-1.25")
	negative.Completeness = providerallowance.CompletenessComplete
	negative.Derivations = []providerallowance.Derivation{{
		ID: "remaining", Formula: providerallowance.FormulaComplement, InputRefs: []string{"over-limit"},
		Unit: providerallowance.UnitPercent, Precision: "exact",
		Value: providerallowance.MeasureValue{State: providerallowance.ValueKnown, Decimal: sp("101.25")},
	}}
	open := missingMinRegistry(true)
	assessment := providerallowance.ValidateObservation(negative, trustLike(negative, providerallowance.BindingVerified), open)
	measure := assessmentByID(t, assessment, "over-limit")
	if measure.Actionable || measure.InRange || assessment.CompleteAllowance || !hasCode(assessment.Issues, providerallowance.IssueRangeMissing) || *negative.Measures[0].Value.Decimal != "-1.25" {
		t.Fatalf("missing min = %#v issues=%v", measure, assessment.Issues)
	}
	derived := providerallowance.Derive(negative, trustLike(negative, providerallowance.BindingVerified), open)
	if derived.Confirmed || len(derived.Values) != 0 || !hasCode(derived.Issues, providerallowance.IssueRangeMissing) {
		t.Fatalf("derived = %#v", derived)
	}
}

func TestInventoryCannotFeedAllowanceOrDerivation(t *testing.T) {
	reg := inventoryRegistry()
	obs := byID(t, "synthetic-multiple-windows").Observation
	held := cloneObservation(t, obs)
	held.DirectFields = []providerallowance.DirectField{
		{Field: "resetCredits.availableCount", OriginalType: providerallowance.TypeDecimal, LexicalValue: sp("0")},
		{Field: "resetCredits.detailsState", OriginalType: providerallowance.TypeString, LexicalValue: sp("unknown")},
	}
	allowed := providerallowance.ValidateObservation(held, trustLike(held, providerallowance.BindingVerified), reg)
	if !assessmentByID(t, allowed, "five-hour").Actionable || hasCode(allowed.Issues, providerallowance.IssueResetCreditMisuse) {
		t.Fatal(allowed.Issues)
	}
	if allowed.ResetCredit.CountLexical != "0" || allowed.ResetCredit.DetailsState != "unknown" || allowed.ResetCredit.DetailsAreEmptyList || allowed.ResetCredit.RateLimitBudget || allowed.ResetCredit.BlocksAccountUse || allowed.ResetCredit.GrantsCapacity {
		t.Fatalf("inventory = %#v", allowed.ResetCredit)
	}

	meter := measureByID(held, "five-hour")
	meter.EvidenceFields = []string{"resetCredits.availableCount"}
	meter.Value.Decimal = sp("0")
	promoted := cloneObservation(t, held)
	promoted.Measures = []providerallowance.Measure{meter}
	promoted.Completeness = providerallowance.CompletenessComplete
	promoted.Derivations = nil
	blocked := providerallowance.ValidateObservation(promoted, trustLike(promoted, providerallowance.BindingVerified), reg)
	if blocked.CompleteAllowance || !hasCode(blocked.Issues, providerallowance.IssueResetCreditMisuse) || blocked.ResetCredit.CountLexical != "0" || blocked.ResetCredit.GrantsCapacity || blocked.ResetCredit.RateLimitBudget || blocked.ResetCredit.BlocksAccountUse {
		t.Fatalf("promoted = %#v reset=%#v", blocked.Issues, blocked.ResetCredit)
	}
	assertNoneActionable(t, blocked)
	if !assessmentByID(t, blocked, "five-hour").SemanticsEvidenced || *promoted.Measures[0].Value.Decimal != "0" {
		t.Fatal("inventory promotion erased the meter")
	}
	derivedObs := cloneObservation(t, promoted)
	derivedObs.Derivations = []providerallowance.Derivation{{
		ID: "remaining", Formula: providerallowance.FormulaComplement, InputRefs: []string{"five-hour"},
		Unit: providerallowance.UnitPercent, Precision: "exact",
		Value: providerallowance.MeasureValue{State: providerallowance.ValueKnown, Decimal: sp("100")},
	}}
	derived := providerallowance.Derive(derivedObs, trustLike(derivedObs, providerallowance.BindingVerified), reg)
	if derived.Confirmed || len(derived.Values) != 0 || !hasCode(derived.Issues, providerallowance.IssueResetCreditMisuse) {
		t.Fatalf("derived inventory = %#v", derived)
	}

	for _, category := range []providerallowance.Category{
		providerallowance.CategoryIncluded, providerallowance.CategorySpend, providerallowance.CategoryRateLimit, providerallowance.CategoryLocal,
	} {
		ruled := categoryRegistry(reg, category)
		sample := cloneObservation(t, promoted)
		sample.Measures[0].Category = category
		sample.Measures[0].ValidationRef = sp("fixture:category")
		assessment := providerallowance.ValidateObservation(sample, trustLike(sample, providerallowance.BindingVerified), ruled)
		if assessmentByID(t, assessment, "five-hour").Actionable || !hasCode(assessment.Issues, providerallowance.IssueResetCreditMisuse) {
			t.Fatalf("%s = %#v", category, assessment.Issues)
		}
		plain := cloneObservation(t, sample)
		plain.Measures[0].EvidenceFields = nil
		open := providerallowance.ValidateObservation(plain, trustLike(plain, providerallowance.BindingVerified), ruled)
		if !assessmentByID(t, open, "five-hour").Actionable || hasCode(open.Issues, providerallowance.IssueResetCreditMisuse) {
			t.Fatalf("%s positive = %#v", category, open.Issues)
		}
	}
}

func TestAvailabilityConflictSuppressesAdvice(t *testing.T) {
	obs := singleKnown(t)
	obs.Availability = providerallowance.AvailabilityAvailable
	reg := reviewedRegistry()
	open := providerallowance.ValidateObservation(obs, trustLike(obs, providerallowance.BindingVerified), reg)
	if !open.ShapeOK || !open.CompleteAllowance || !assessmentByID(t, open, "five-hour").Actionable || *obs.Measures[0].Value.Decimal != "0.36" {
		t.Fatalf("available = complete %v issues %v", open.CompleteAllowance, open.Issues)
	}
	conflicted := cloneObservation(t, obs)
	conflicted.Availability = providerallowance.AvailabilityConflict
	before := snapshot(t, conflicted)
	closed := providerallowance.ValidateObservation(conflicted, trustLike(conflicted, providerallowance.BindingVerified), reg)
	assertUnchanged(t, before, conflicted)
	measure := assessmentByID(t, closed, "five-hour")
	if !closed.ShapeOK || !measure.SemanticsEvidenced || measure.Actionable || closed.CompleteAllowance || !hasCode(closed.Issues, providerallowance.IssueAvailabilityConflict) || *conflicted.Measures[0].Value.Decimal != "0.36" {
		t.Fatalf("conflict = %#v issues=%v", measure, closed.Issues)
	}

	derivedObs := cloneObservation(t, obs)
	derivedObs.Derivations = []providerallowance.Derivation{{
		ID: "remaining", Formula: providerallowance.FormulaComplement, InputRefs: []string{"five-hour"},
		Unit: providerallowance.UnitPercent, Precision: "exact",
		Value: providerallowance.MeasureValue{State: providerallowance.ValueKnown, Decimal: sp("99.64")},
	}}
	confirmed := providerallowance.Derive(derivedObs, trustLike(derivedObs, providerallowance.BindingVerified), reg)
	if !confirmed.Confirmed || len(confirmed.Values) != 1 || confirmed.Values[0].Lexical != "99.64" {
		t.Fatalf("available derivation = %#v", confirmed)
	}
	conflicted.Derivations = derivedObs.Derivations
	refused := providerallowance.Derive(conflicted, trustLike(conflicted, providerallowance.BindingVerified), reg)
	if refused.Confirmed || len(refused.Values) != 0 || !hasCode(refused.Issues, providerallowance.IssueAvailabilityConflict) {
		t.Fatalf("conflict derivation = %#v", refused)
	}
}

func TestSharedPoolsUseTrustedEntitlementBindings(t *testing.T) {
	reg := poolRegistry(true)
	cursorObs, cursorTrust := poolObservation(t, "cursor", "fixture-account")
	grokObs, grokTrust := poolObservation(t, "grok", "fixture-account")
	positive := providerallowance.SharedPools(
		[]providerallowance.Observation{cursorObs, grokObs},
		[]providerallowance.TrustedContext{cursorTrust, grokTrust},
		reg,
	)
	if !positive.Resolved || positive.Count != 1 || positive.EntitlementRef != "entitlement-E" || len(positive.Issues) != 0 {
		t.Fatalf("positive = %#v", positive)
	}

	otherCursor, otherCursorTrust := poolObservation(t, "cursor", "other-account")
	otherGrok, otherGrokTrust := poolObservation(t, "grok", "other-account")
	other := providerallowance.SharedPools(
		[]providerallowance.Observation{otherCursor, otherGrok},
		[]providerallowance.TrustedContext{otherCursorTrust, otherGrokTrust},
		reg,
	)
	if other.Resolved || other.Count != 0 || other.EntitlementRef != "" || !hasCode(other.Issues, providerallowance.IssueAccountMismatch) {
		t.Fatalf("other account = %#v", other)
	}

	self, selfTrust := poolObservation(t, "cursor", "fixture-account")
	self.Scope.Products = []string{"cursor", "grok"}
	selfTrust = trustLike(self, providerallowance.BindingVerified)
	claimed := providerallowance.SharedPools([]providerallowance.Observation{self, grokObs}, []providerallowance.TrustedContext{selfTrust, grokTrust}, reg)
	if claimed.Resolved || claimed.Count != 0 || claimed.EntitlementRef != "" || !hasCode(claimed.Issues, providerallowance.IssueAccountMismatch) {
		t.Fatalf("self-asserted aliases = %#v", claimed)
	}

	extra, extraTrust := poolObservation(t, "cursor", "fixture-account")
	extra.Scope.Products = []string{"cursor", "extra-alias"}
	extraTrust = trustLike(extra, providerallowance.BindingVerified)
	aliased := providerallowance.SharedPools([]providerallowance.Observation{extra, grokObs}, []providerallowance.TrustedContext{extraTrust, grokTrust}, reg)
	if aliased.Resolved || aliased.EntitlementRef != "" || !hasCode(aliased.Issues, providerallowance.IssueAccountMismatch) {
		t.Fatalf("extra alias = %#v", aliased)
	}

	generationObs, _ := poolObservation(t, "cursor", "fixture-account")
	generationObs.BindingGeneration = 1
	generationTrust := trustLike(generationObs, providerallowance.BindingVerified)
	generationReg := registryWithAccount(reg, providerallowance.AccountRule{
		AccountRef: "fixture-account", WorkspaceRef: "fixture-workspace", Generation: 1,
		ProducerRef: "fixture-producer", ProducerIncarnation: "fixture-incarnation-1",
	})
	generation := providerallowance.SharedPools([]providerallowance.Observation{generationObs}, []providerallowance.TrustedContext{generationTrust}, generationReg)
	if generation.Resolved || generation.EntitlementRef != "" || !hasCode(generation.Issues, providerallowance.IssueAccountMismatch) {
		t.Fatalf("generation = %#v", generation)
	}

	workspaceObs, _ := poolObservation(t, "cursor", "fixture-account")
	workspaceObs.Scope.WorkspaceRef = sp("other-workspace")
	workspaceTrust := trustLike(workspaceObs, providerallowance.BindingVerified)
	workspaceReg := registryWithAccount(reg, providerallowance.AccountRule{
		AccountRef: "fixture-account", WorkspaceRef: "other-workspace", Generation: 0,
		ProducerRef: "fixture-producer", ProducerIncarnation: "fixture-incarnation-1",
	})
	workspace := providerallowance.SharedPools([]providerallowance.Observation{workspaceObs}, []providerallowance.TrustedContext{workspaceTrust}, workspaceReg)
	if workspace.Resolved || workspace.EntitlementRef != "" || !hasCode(workspace.Issues, providerallowance.IssueAccountMismatch) {
		t.Fatalf("workspace = %#v", workspace)
	}

	producerObs, _ := poolObservation(t, "grok", "fixture-account")
	producerObs.ProducerRef = "other-producer"
	producerTrust := trustLike(producerObs, providerallowance.BindingVerified)
	producerReg := registryWithAccount(reg, providerallowance.AccountRule{
		AccountRef: "fixture-account", WorkspaceRef: "fixture-workspace", Generation: 0,
		ProducerRef: "other-producer", ProducerIncarnation: "fixture-incarnation-1",
	})
	producer := providerallowance.SharedPools(
		[]providerallowance.Observation{cursorObs, producerObs},
		[]providerallowance.TrustedContext{cursorTrust, producerTrust},
		producerReg,
	)
	if producer.Resolved || producer.Count != 0 || producer.EntitlementRef != "" || !hasCode(producer.Issues, providerallowance.IssueAccountMismatch) {
		t.Fatalf("producer alias = %#v", producer)
	}
}

func projectionRegistry(base *providerallowance.StaticRegistry, rules ...providerallowance.ProjectionRule) *providerallowance.StaticRegistry {
	return providerallowance.NewStaticRegistry(providerallowance.RegistryParts{
		Sources: base.Sources(), Metrics: base.Metrics(), Accounts: base.Accounts(), Projections: rules,
	})
}

func threePartSum(t *testing.T, a, b, c, declared string) providerallowance.Observation {
	t.Helper()
	obs := compatibleSum(t, a, b, declared)
	part := obs.Measures[0]
	part.ID = "part-c"
	part.Value.Decimal = sp(c)
	obs.Measures = append(obs.Measures, part)
	obs.Derivations[0].InputRefs = []string{"part-a", "part-b", "part-c"}
	return obs
}

func missingMinRegistry(over bool) *providerallowance.StaticRegistry {
	base := reviewedRegistry()
	metrics := base.Metrics()
	for i := range metrics {
		if metrics[i].ValidationRef != "fixture:explicit-overlimit-source" {
			continue
		}
		metrics[i].Min = nil
		metrics[i].AllowOverLimit = over
		metrics[i].ComplementOf = "100"
	}
	return providerallowance.NewStaticRegistry(providerallowance.RegistryParts{
		Sources: base.Sources(), Metrics: metrics, Accounts: base.Accounts(),
	})
}

func categoryRegistry(base *providerallowance.StaticRegistry, category providerallowance.Category) *providerallowance.StaticRegistry {
	metrics := base.Metrics()
	for _, metric := range base.Metrics() {
		if metric.ValidationRef != "fixture:verified-field-v1" {
			continue
		}
		metric.ValidationRef = "fixture:category"
		metric.Category = category
		metric.Windows = []string{"five-hour-window"}
		metrics = append(metrics, metric)
	}
	return providerallowance.NewStaticRegistry(providerallowance.RegistryParts{
		Sources: base.Sources(), Metrics: metrics, Accounts: base.Accounts(),
	})
}

func singleKnown(t *testing.T) providerallowance.Observation {
	t.Helper()
	obs := cloneObservation(t, byID(t, "synthetic-multiple-windows").Observation)
	obs.Measures = []providerallowance.Measure{measureByID(obs, "five-hour")}
	obs.Derivations = nil
	obs.Completeness = providerallowance.CompletenessComplete
	return obs
}

func registryWithAccount(base *providerallowance.StaticRegistry, account providerallowance.AccountRule) *providerallowance.StaticRegistry {
	return providerallowance.NewStaticRegistry(providerallowance.RegistryParts{
		Sources: base.Sources(), Metrics: base.Metrics(), Accounts: append(base.Accounts(), account), Entitlements: base.Entitlements(),
	})
}

func editJSON(t *testing.T, raw []byte, edit func(map[string]any)) []byte {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	edit(doc)
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
