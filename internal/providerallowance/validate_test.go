package providerallowance_test

import (
	"testing"

	"github.com/thaynes43/dev-env/internal/providerallowance"
)

func TestReviewedExamplesAreNotCurrentAllowances(t *testing.T) {
	reg := reviewedRegistry()
	for _, item := range loadExamples(t) {
		obs := item.Observation
		before := snapshot(t, obs)
		assessment := providerallowance.ValidateObservation(obs, trustLike(obs, obs.Scope.BindingState), reg)
		assertUnchanged(t, before, obs)
		if assessment.CompleteAllowance {
			t.Fatalf("%s was treated as a complete current allowance", obs.ObservationID)
		}
		if assessment.ServedModel != providerallowance.ServedModelUnknown {
			t.Fatalf("%s served model = %s", obs.ObservationID, assessment.ServedModel)
		}
	}
}

func TestHistoricalCodexSemanticsStaySeparateFromCurrentEntitlement(t *testing.T) {
	obs := byID(t, "historical-codex-188").Observation
	reg := reviewedRegistry()
	original := providerallowance.ValidateObservation(obs, trustLike(obs, providerallowance.BindingUnknown), reg)
	if len(original.Issues) != 0 || original.ProviderTimeKnown || original.CompleteAllowance {
		t.Fatalf("original = %#v", original)
	}
	primary := assessmentByID(t, original, "codex-primary")
	if !primary.SemanticsEvidenced || !primary.HistoricalOnly || primary.Actionable || !primary.InRange {
		t.Fatalf("primary = %#v", primary)
	}
	secondary := assessmentByID(t, original, "codex-secondary")
	if secondary.Actionable || secondary.SemanticsEvidenced {
		t.Fatalf("secondary = %#v", secondary)
	}
	if !original.ResetCredit.Present || original.ResetCredit.CountLexical != "0" || original.ResetCredit.DetailsState != "unknown" {
		t.Fatalf("reset credit = %#v", original.ResetCredit)
	}
	if original.ResetCredit.DetailsAreEmptyList || original.ResetCredit.RateLimitBudget || original.ResetCredit.BlocksAccountUse || original.ResetCredit.GrantsCapacity {
		t.Fatalf("reset credit gained authority %#v", original.ResetCredit)
	}

	upgraded := cloneObservation(t, obs)
	upgraded.Scope.AccountRef = sp("fixture-account")
	upgraded.Scope.WorkspaceRef = sp("fixture-workspace")
	upgraded.Scope.BindingState = providerallowance.BindingVerified
	upgraded.ProviderObservedAt = sp("2026-10-24T16:02:43.684834Z")
	current := providerallowance.ValidateObservation(upgraded, trustLike(upgraded, providerallowance.BindingVerified), reg)
	if len(current.Issues) != 0 {
		t.Fatal(current.Issues)
	}
	primary = assessmentByID(t, current, "codex-primary")
	if !primary.SemanticsEvidenced || !primary.HistoricalOnly || primary.Actionable {
		t.Fatalf("upgraded primary = %#v", primary)
	}
	if current.CompleteAllowance {
		t.Fatal("historical field semantics became a current allowance")
	}
}

func TestResetCreditInventoryCannotBecomeABudget(t *testing.T) {
	obs := cloneObservation(t, byID(t, "historical-codex-188").Observation)
	obs.Measures = append(obs.Measures, providerallowance.Measure{
		ID: "reset-as-budget", Category: providerallowance.CategoryRateLimit,
		Window: obs.Measures[0].Window, Unit: providerallowance.UnitPercent,
		Direction:      providerallowance.DirectionLimit,
		Value:          providerallowance.MeasureValue{State: providerallowance.ValueUnknown},
		EvidenceFields: []string{"resetCredits.availableCount"},
	})
	assessment := providerallowance.ValidateObservation(obs, trustLike(obs, providerallowance.BindingUnknown), reviewedRegistry())
	if !hasCode(assessment.Issues, providerallowance.IssueResetCreditMisuse) {
		t.Fatal(assessment.Issues)
	}
	if assessment.ResetCredit.RateLimitBudget || assessment.ResetCredit.BlocksAccountUse || assessment.ResetCredit.GrantsCapacity || assessment.ResetCredit.CountLexical != "0" {
		t.Fatalf("inventory = %#v", assessment.ResetCredit)
	}
}

func TestSyntheticWindowsDoNotCollapse(t *testing.T) {
	obs := byID(t, "synthetic-multiple-windows").Observation
	before := snapshot(t, obs)
	assessment := providerallowance.ValidateObservation(obs, trustLike(obs, providerallowance.BindingVerified), reviewedRegistry())
	assertUnchanged(t, before, obs)
	if len(assessment.Issues) != 0 || assessment.CompleteAllowance {
		t.Fatalf("issues=%v complete=%v", assessment.Issues, assessment.CompleteAllowance)
	}
	want := map[string]bool{"five-hour": true, "seven-day": true, "uncapped": true, "unknown-meter": false, "not-reported": false, "not-applicable": false}
	for _, measure := range assessment.Measures {
		if measure.Actionable != want[measure.ID] {
			t.Fatalf("%s actionable = %v", measure.ID, measure.Actionable)
		}
	}
	if decimalsOf(obs)["five-hour"] != "0.36" || decimalsOf(obs)["seven-day"] != "0" {
		t.Fatal(decimalsOf(obs))
	}
}

func TestOverLimitStaysLexical(t *testing.T) {
	obs := byID(t, "synthetic-supported-over-limit").Observation
	before := snapshot(t, obs)
	held := providerallowance.ValidateObservation(obs, trustLike(obs, providerallowance.BindingUnknown), reviewedRegistry())
	assertUnchanged(t, before, obs)
	measure := assessmentByID(t, held, "over-limit")
	if !measure.SemanticsEvidenced || !measure.InRange || measure.Actionable || held.CompleteAllowance || hasCode(held.Issues, providerallowance.IssueRangeUnsupported) {
		t.Fatalf("held = %#v issues=%v", measure, held.Issues)
	}
	if *obs.Measures[0].Value.Decimal != "101.25" {
		t.Fatal(*obs.Measures[0].Value.Decimal)
	}

	ready := cloneObservation(t, obs)
	ready.Scope.AccountRef = sp("fixture-account")
	ready.Scope.WorkspaceRef = sp("fixture-workspace")
	ready.Scope.BindingState = providerallowance.BindingVerified
	ready.ProviderObservedAt = sp("2026-10-24T15:59:00Z")
	readyBefore := snapshot(t, ready)
	actionable := providerallowance.ValidateObservation(ready, trustLike(ready, providerallowance.BindingVerified), reviewedRegistry())
	assertUnchanged(t, readyBefore, ready)
	got := assessmentByID(t, actionable, "over-limit")
	if !got.Actionable || !got.InRange || actionable.CompleteAllowance || *ready.Measures[0].Value.Decimal != "101.25" {
		t.Fatalf("actionable = %#v complete=%v issues=%v", got, actionable.CompleteAllowance, actionable.Issues)
	}

	unsupported := cloneObservation(t, ready)
	unsupported.Measures[0].ValidationRef = sp("fixture:verified-field-v1")
	unsupported.Measures[0].Window.Ref = "five-hour-window"
	refused := providerallowance.ValidateObservation(unsupported, trustLike(unsupported, providerallowance.BindingVerified), reviewedRegistry())
	if !hasCode(refused.Issues, providerallowance.IssueRangeUnsupported) || assessmentByID(t, refused, "over-limit").InRange || *unsupported.Measures[0].Value.Decimal != "101.25" {
		t.Fatalf("refused = %#v decimal=%s", refused.Issues, *unsupported.Measures[0].Value.Decimal)
	}
}

func TestTrustedContextNegativesHavePositiveControls(t *testing.T) {
	reg := reviewedRegistry()
	base := byID(t, "synthetic-multiple-windows").Observation
	positive := providerallowance.ValidateObservation(base, trustLike(base, providerallowance.BindingVerified), reg)
	if len(positive.Issues) != 0 || !assessmentByID(t, positive, "five-hour").Actionable {
		t.Fatal(positive.Issues)
	}

	missing := providerallowance.ValidateObservation(base, trustLike(base, providerallowance.BindingVerified), nil)
	if !hasCode(missing.Issues, providerallowance.IssueRegistryMissing) || missing.CompleteAllowance || assessmentByID(t, missing, "five-hour").Actionable {
		t.Fatal(missing.Issues)
	}
	empty := providerallowance.ValidateObservation(base, trustLike(base, providerallowance.BindingVerified), blankRegistry{})
	if !hasCode(empty.Issues, providerallowance.IssueSourceUntrusted) || hasCode(empty.Issues, providerallowance.IssueRegistryMissing) {
		t.Fatal(empty.Issues)
	}

	forgedBinding := providerallowance.ValidateObservation(base, trustLike(base, providerallowance.BindingUnknown), reg)
	if !hasCode(forgedBinding.Issues, providerallowance.IssueBindingForged) || hasCode(forgedBinding.Issues, providerallowance.IssueBindingMismatch) || assessmentByID(t, forgedBinding, "five-hour").Actionable {
		t.Fatal(forgedBinding.Issues)
	}

	generation := trustLike(base, providerallowance.BindingVerified)
	generation.BindingGeneration = 1
	if assessment := providerallowance.ValidateObservation(base, generation, reg); !hasCode(assessment.Issues, providerallowance.IssueGenerationMismatch) {
		t.Fatal(assessment.Issues)
	}
	producer := trustLike(base, providerallowance.BindingVerified)
	producer.ProducerRef = "other-producer"
	if assessment := providerallowance.ValidateObservation(base, producer, reg); !hasCode(assessment.Issues, providerallowance.IssueProducerMismatch) {
		t.Fatal(assessment.Issues)
	}
	version := trustLike(base, providerallowance.BindingVerified)
	version.SourceVersion = "other-version"
	if assessment := providerallowance.ValidateObservation(base, version, reg); !hasCode(assessment.Issues, providerallowance.IssueSourceMismatch) {
		t.Fatal(assessment.Issues)
	}
	account := trustLike(base, providerallowance.BindingVerified)
	account.AccountRef = sp("other-account")
	if assessment := providerallowance.ValidateObservation(base, account, reg); !hasCode(assessment.Issues, providerallowance.IssueAccountMismatch) {
		t.Fatal(assessment.Issues)
	}

	wrongWindow := cloneObservation(t, base)
	wrongWindow.Measures[0].Window.Ref = "other-window"
	if assessment := providerallowance.ValidateObservation(wrongWindow, trustLike(wrongWindow, providerallowance.BindingVerified), reg); !hasCode(assessment.Issues, providerallowance.IssueMetricMismatch) || assessmentByID(t, assessment, "five-hour").Actionable {
		t.Fatal(assessment.Issues)
	}
	wrongUnit := cloneObservation(t, base)
	wrongUnit.Measures[0].Unit = "count"
	if assessment := providerallowance.ValidateObservation(wrongUnit, trustLike(wrongUnit, providerallowance.BindingVerified), reg); !hasCode(assessment.Issues, providerallowance.IssueMetricMismatch) {
		t.Fatal(assessment.Issues)
	}
	forgedRef := cloneObservation(t, base)
	forgedRef.Measures[0].ValidationRef = sp("fixture:forged")
	if assessment := providerallowance.ValidateObservation(forgedRef, trustLike(forgedRef, providerallowance.BindingVerified), reg); !hasCode(assessment.Issues, providerallowance.IssueMetricUntrusted) {
		t.Fatal(assessment.Issues)
	}

	cursor := byID(t, "historical-cursor-188").Observation
	if assessment := providerallowance.ValidateObservation(cursor, trustLike(cursor, providerallowance.BindingUnknown), reg); len(assessment.Issues) != 0 || assessment.ServedModel != providerallowance.ServedModelUnknown {
		t.Fatal(assessment.Issues)
	}
	selected := cloneObservation(t, cursor)
	selected.DirectFields = append(selected.DirectFields, providerallowance.DirectField{Field: "selectedModel", OriginalType: providerallowance.TypeString, LexicalValue: sp("grok-4.7-xhigh")})
	selectedAssessment := providerallowance.ValidateObservation(selected, trustLike(selected, providerallowance.BindingUnknown), reg)
	if !hasCode(selectedAssessment.Issues, providerallowance.IssueFieldNotAllowlisted) || selectedAssessment.ServedModel != providerallowance.ServedModelUnknown {
		t.Fatal(selectedAssessment.Issues, selectedAssessment.ServedModel)
	}
	promoted := cloneObservation(t, cursor)
	promoted.Measures = []providerallowance.Measure{base.Measures[0]}
	if assessment := providerallowance.ValidateObservation(promoted, trustLike(promoted, providerallowance.BindingUnknown), reg); !hasCode(assessment.Issues, providerallowance.IssueDisplayPromotion) {
		t.Fatal(assessment.Issues)
	}

	duplicate := cloneObservation(t, base)
	duplicate.Measures = append(duplicate.Measures, duplicate.Measures[0])
	if assessment := providerallowance.ValidateObservation(duplicate, trustLike(duplicate, providerallowance.BindingVerified), reg); !hasCode(assessment.Issues, providerallowance.IssueDuplicateID) || assessment.ShapeOK {
		t.Fatal(assessment.Issues)
	}
	knownNull, err := providerallowance.DecodeObservation(loadJSON(t, "negatives/observation-known-null.json"), providerallowance.FixtureParseLimits)
	if err != nil {
		t.Fatal(err)
	}
	if assessment := providerallowance.ValidateObservation(knownNull, trustLike(knownNull, providerallowance.BindingUnknown), reg); !hasCode(assessment.Issues, providerallowance.IssueKnownDecimal) {
		t.Fatal(assessment.Issues)
	}

	evidence := cloneObservation(t, base)
	evidence.EvidenceRefs = []string{"forged-trust"}
	withEvidence := providerallowance.ValidateObservation(evidence, trustLike(evidence, providerallowance.BindingVerified), reg)
	if len(withEvidence.Issues) != len(positive.Issues) || withEvidence.CompleteAllowance != positive.CompleteAllowance || !assessmentByID(t, withEvidence, "five-hour").Actionable {
		t.Fatal("evidenceRef changed trust")
	}

	parts := providerallowance.RegistryParts{Sources: append(reg.Sources(), reg.Sources()[0]), Metrics: reg.Metrics(), Accounts: reg.Accounts()}
	duplicated := providerallowance.NewStaticRegistry(parts)
	if assessment := providerallowance.ValidateObservation(byID(t, "historical-codex-188").Observation, trustLike(byID(t, "historical-codex-188").Observation, providerallowance.BindingUnknown), duplicated); !hasCode(assessment.Issues, providerallowance.IssueRegistryDuplicate) || assessmentByID(t, assessment, "codex-primary").SemanticsEvidenced {
		t.Fatal(assessment.Issues)
	}

	loose := providerallowance.MetricRule{
		ValidationRef: "fixture:explicit-overlimit-source", Provider: "synthetic-provider",
		SourceKind: providerallowance.SourceSynthetic, SourceVersion: "fixture-v1",
		Category: providerallowance.CategoryIncluded, Unit: providerallowance.UnitPercent, Direction: providerallowance.DirectionUsed,
		Windows: []string{"over-limit-window"}, Min: sp("0"), AllowOverLimit: true,
	}
	tight := loose
	tight.AllowOverLimit = false
	tight.Max = sp("100")
	conflict := providerallowance.NewStaticRegistry(providerallowance.RegistryParts{Sources: reg.Sources(), Metrics: []providerallowance.MetricRule{loose, tight}})
	over := byID(t, "synthetic-supported-over-limit").Observation
	conflictAssessment := providerallowance.ValidateObservation(over, trustLike(over, providerallowance.BindingUnknown), conflict)
	if !hasCode(conflictAssessment.Issues, providerallowance.IssueRegistryDuplicate) || assessmentByID(t, conflictAssessment, "over-limit").InRange || *over.Measures[0].Value.Decimal != "101.25" {
		t.Fatal(conflictAssessment.Issues)
	}

	noRange := providerallowance.NewStaticRegistry(providerallowance.RegistryParts{
		Sources: reg.Sources(),
		Metrics: []providerallowance.MetricRule{{
			ValidationRef: "fixture:explicit-overlimit-source", Provider: "synthetic-provider",
			SourceKind: providerallowance.SourceSynthetic, SourceVersion: "fixture-v1",
			Category: providerallowance.CategoryIncluded, Unit: providerallowance.UnitPercent, Direction: providerallowance.DirectionUsed,
			Windows: []string{"over-limit-window"},
		}},
	})
	if assessment := providerallowance.ValidateObservation(over, trustLike(over, providerallowance.BindingUnknown), noRange); !hasCode(assessment.Issues, providerallowance.IssueRangeMissing) || *over.Measures[0].Value.Decimal != "101.25" {
		t.Fatal(assessment.Issues)
	}
}

func TestReadValidationDoesNotGrantTrust(t *testing.T) {
	_, body, _ := loadRead(t)
	item := body.Items[0]
	if item.Validation.State != providerallowance.ValidationSourceFields {
		t.Fatal(item.Validation.State)
	}
	before := snapshot(t, item.Observation)
	assessment := providerallowance.ValidateObservation(item.Observation, providerallowance.TrustedContext{}, nil)
	assertUnchanged(t, before, item.Observation)
	if !hasCode(assessment.Issues, providerallowance.IssueRegistryMissing) || assessment.CompleteAllowance {
		t.Fatal(assessment.Issues)
	}
	expired := body.Items[3]
	if expired.CurrentMeasures[0].State != providerallowance.CurrentExpired || expired.Observation.Measures[0].Value.Decimal == nil || *expired.Observation.Measures[0].Value.Decimal != "0.36" {
		t.Fatal("expired current and historical value were collapsed")
	}
	historical := providerallowance.ValidateObservation(expired.Observation, trustLike(expired.Observation, providerallowance.BindingVerified), reviewedRegistry())
	if !historical.CompleteAllowance || historical.ServedModel != providerallowance.ServedModelUnknown {
		t.Fatalf("historical observation = complete %v issues %v", historical.CompleteAllowance, historical.Issues)
	}
	if expired.CurrentMeasures[0].State != providerallowance.CurrentExpired || expired.CurrentMeasures[0].Value.State != providerallowance.ValueUnknown || expired.CurrentMeasures[0].Value.Decimal != nil || *expired.Observation.Measures[0].Value.Decimal != "0.36" {
		t.Fatal("validating the historical observation freshened the current read")
	}
}

func TestDistinctProvidersAreNotSummed(t *testing.T) {
	reg := reviewedRegistry()
	for _, id := range []string{"synthetic-openrouter", "synthetic-ollama"} {
		obs := byID(t, id).Observation
		assessment := providerallowance.ValidateObservation(obs, trustLike(obs, providerallowance.BindingUnknown), reg)
		measure := assessment.Measures[0]
		if !measure.SemanticsEvidenced || !measure.InRange || measure.Actionable || assessment.CompleteAllowance || len(assessment.Issues) != 0 {
			t.Fatalf("%s = %#v issues=%v", id, measure, assessment.Issues)
		}
	}
	money := *byID(t, "synthetic-openrouter").Observation.Measures[0].Value.Decimal
	memory := *byID(t, "synthetic-ollama").Observation.Measures[0].Value.Decimal
	if money != "2.50" || memory != "4096" {
		t.Fatal(money, memory)
	}
}

func assessmentByID(t *testing.T, assessment providerallowance.Assessment, id string) providerallowance.MeasureAssessment {
	t.Helper()
	for _, measure := range assessment.Measures {
		if measure.ID == id {
			return measure
		}
	}
	t.Fatalf("missing assessment %s", id)
	return providerallowance.MeasureAssessment{}
}

func decimalsOf(obs providerallowance.Observation) map[string]string {
	out := map[string]string{}
	for _, measure := range obs.Measures {
		if measure.Value.Decimal != nil {
			out[measure.ID] = *measure.Value.Decimal
		}
	}
	return out
}
