package providerallowance_test

import (
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/providerallowance"
)

func TestDateTimeAgreesWithSchemaFormat(t *testing.T) {
	observation := compileSchema(t, "observation.schema.json", "urn:dev-env:providerallowance:observation:v1:draft")
	readSchema := compileSchema(t, "read-response.schema.json", "urn:dev-env:providerallowance:read:v1:draft")
	raw := byID(t, "synthetic-multiple-windows").Raw
	body := readBody(t)
	reg := reviewedRegistry()
	base := singleKnown(t)
	open := providerallowance.ValidateObservation(base, trustLike(base, providerallowance.BindingVerified), reg)
	if !open.ShapeOK || !open.CompleteAllowance || !assessmentByID(t, open, "five-hour").Actionable {
		t.Fatalf("available control = %#v", open.Issues)
	}
	derived := cloneObservation(t, base)
	derived.Derivations = []providerallowance.Derivation{{
		ID: "remaining", Formula: providerallowance.FormulaComplement, InputRefs: []string{"five-hour"},
		Unit: providerallowance.UnitPercent, Precision: "exact",
		Value: providerallowance.MeasureValue{State: providerallowance.ValueKnown, Decimal: sp("99.64")},
	}}
	confirmed := providerallowance.Derive(derived, trustLike(derived, providerallowance.BindingVerified), reg)
	if !confirmed.Confirmed || len(confirmed.Values) != 1 || confirmed.Values[0].Lexical != "99.64" {
		t.Fatalf("complement control = %#v", confirmed)
	}

	invalid := []string{
		"2026-10-24T16:02:43+24:00",
		"2026-10-24T16:02:43-24:00",
		"2026-10-24T16:02:43+00:60",
		"2026-10-24T16:02:43-00:60",
		"2026-10-24T6:02:43Z",
		"2026-10-24T16:02:43,5Z",
		"2026-10-24T24:00:00Z",
		"2026-10-24T16:60:00Z",
		"2026-02-31T16:02:43Z",
		"2026-10-24T16:02:43",
		" 2026-10-24T16:02:43Z",
		"2026-10-24T16:02:43Z ",
		"2026-10-24T16:02:43.Z",
		"2016-12-31T12:30:60Z",
		"2016-12-31T23:59:60+01:00",
	}
	for _, stamp := range invalid {
		t.Run("invalid "+stamp, func(t *testing.T) {
			captured := editJSON(t, raw, func(doc map[string]any) { doc["capturedAt"] = stamp })
			if err := observation.Validate(instance(t, captured)); err == nil {
				t.Fatal("schema accepted capturedAt")
			}
			if _, err := providerallowance.DecodeObservation(captured, providerallowance.FixtureParseLimits); err == nil || !strings.Contains(err.Error(), "invalid_time") {
				t.Fatal(err)
			}
			obs := cloneObservation(t, base)
			obs.ProviderObservedAt = sp(stamp)
			assessment := providerallowance.ValidateObservation(obs, trustLike(obs, providerallowance.BindingVerified), reg)
			if assessment.ShapeOK || assessment.CompleteAllowance || assessmentByID(t, assessment, "five-hour").Actionable || !hasCode(assessment.Issues, providerallowance.IssueInvalidTime) {
				t.Fatalf("advice = %#v", assessment)
			}
			withDerivation := cloneObservation(t, derived)
			withDerivation.ProviderObservedAt = sp(stamp)
			refused := providerallowance.Derive(withDerivation, trustLike(withDerivation, providerallowance.BindingVerified), reg)
			if refused.Confirmed || len(refused.Values) != 0 || !hasCode(refused.Issues, providerallowance.IssueInvalidTime) {
				t.Fatalf("derived = %#v", refused)
			}
		})
	}
	representative := "2026-10-24T16:02:43+24:00"
	provider := editJSON(t, raw, func(doc map[string]any) { doc["providerObservedAt"] = representative })
	if err := observation.Validate(instance(t, provider)); err == nil {
		t.Fatal("schema accepted provider time")
	}
	if _, err := providerallowance.DecodeObservation(provider, providerallowance.FixtureParseLimits); err == nil {
		t.Fatal("decode accepted provider time")
	}
	window := editJSON(t, raw, func(doc map[string]any) {
		doc["measures"].([]any)[0].(map[string]any)["window"].(map[string]any)["startAt"] = representative
	})
	if err := observation.Validate(instance(t, window)); err == nil {
		t.Fatal("schema accepted window start")
	}
	if _, err := providerallowance.DecodeObservation(window, providerallowance.FixtureParseLimits); err == nil {
		t.Fatal("decode accepted window start")
	}
	served := editJSON(t, body, func(doc map[string]any) { doc["servedAt"] = representative })
	if err := readSchema.Validate(instance(t, served)); err == nil {
		t.Fatal("schema accepted servedAt")
	}
	if _, err := providerallowance.DecodeReadResponse(served, providerallowance.FixtureParseLimits); err == nil {
		t.Fatal("decode accepted servedAt")
	}
	attempt := editJSON(t, body, func(doc map[string]any) {
		doc["sourceStatuses"].([]any)[0].(map[string]any)["lastAttempt"].(map[string]any)["attemptedAt"] = representative
	})
	if err := readSchema.Validate(instance(t, attempt)); err == nil {
		t.Fatal("schema accepted attemptedAt")
	}
	if _, err := providerallowance.DecodeReadResponse(attempt, providerallowance.FixtureParseLimits); err == nil {
		t.Fatal("decode accepted attemptedAt")
	}

	valid := []string{
		"2026-10-24T16:02:43Z",
		"2026-10-24T16:02:43.684834Z",
		"2026-10-24T12:02:43.684834-04:00",
		"2026-10-24T16:02:43+23:59",
		"2026-10-24T16:02:43-23:59",
		"2026-10-24T16:02:43+00:00",
		"2026-10-24T16:02:43-00:00",
		"2026-10-24T16:02:43.123456789012Z",
		"2026-10-24t16:02:43z",
		"2016-12-31T23:59:60Z",
		"2017-01-01T00:59:60+01:00",
	}
	for _, stamp := range valid {
		t.Run("valid "+stamp, func(t *testing.T) {
			captured := editJSON(t, raw, func(doc map[string]any) { doc["capturedAt"] = stamp })
			if err := observation.Validate(instance(t, captured)); err != nil {
				t.Fatal(err)
			}
			decoded, err := providerallowance.DecodeObservation(captured, providerallowance.FixtureParseLimits)
			if err != nil || decoded.CapturedAt != stamp {
				t.Fatalf("captured = %q err=%v", decoded.CapturedAt, err)
			}
		})
	}
	lowercase := "2026-10-24t16:02:43.684834z"
	providerOK := editJSON(t, raw, func(doc map[string]any) { doc["providerObservedAt"] = lowercase })
	if err := observation.Validate(instance(t, providerOK)); err != nil {
		t.Fatal(err)
	}
	decodedProvider, err := providerallowance.DecodeObservation(providerOK, providerallowance.FixtureParseLimits)
	if err != nil || decodedProvider.ProviderObservedAt == nil || *decodedProvider.ProviderObservedAt != lowercase {
		t.Fatalf("provider = %#v err=%v", decodedProvider.ProviderObservedAt, err)
	}
	windowOK := editJSON(t, raw, func(doc map[string]any) {
		doc["measures"].([]any)[0].(map[string]any)["window"].(map[string]any)["startAt"] = lowercase
	})
	if err := observation.Validate(instance(t, windowOK)); err != nil {
		t.Fatal(err)
	}
	decodedWindow, err := providerallowance.DecodeObservation(windowOK, providerallowance.FixtureParseLimits)
	if err != nil || decodedWindow.Measures[0].Window.StartAt == nil || *decodedWindow.Measures[0].Window.StartAt != lowercase {
		t.Fatalf("start = %#v err=%v", decodedWindow.Measures[0].Window.StartAt, err)
	}
	servedOK := editJSON(t, body, func(doc map[string]any) { doc["servedAt"] = lowercase })
	if err := readSchema.Validate(instance(t, servedOK)); err != nil {
		t.Fatal(err)
	}
	decodedRead, err := providerallowance.DecodeReadResponse(servedOK, providerallowance.FixtureParseLimits)
	if err != nil || decodedRead.ServedAt != lowercase {
		t.Fatalf("served = %q err=%v", decodedRead.ServedAt, err)
	}
	attemptOK := editJSON(t, body, func(doc map[string]any) {
		doc["sourceStatuses"].([]any)[0].(map[string]any)["lastAttempt"].(map[string]any)["attemptedAt"] = lowercase
	})
	if err := readSchema.Validate(instance(t, attemptOK)); err != nil {
		t.Fatal(err)
	}
	decodedAttempt, err := providerallowance.DecodeReadResponse(attemptOK, providerallowance.FixtureParseLimits)
	if err != nil || decodedAttempt.SourceStatuses[0].LastAttempt.AttemptedAt != lowercase {
		t.Fatalf("attempt = %q err=%v", decodedAttempt.SourceStatuses[0].LastAttempt.AttemptedAt, err)
	}
	lowerObs := cloneObservation(t, derived)
	lowerObs.ProviderObservedAt = sp(lowercase)
	lowerAssessment := providerallowance.ValidateObservation(lowerObs, trustLike(lowerObs, providerallowance.BindingVerified), reg)
	lowerDerived := providerallowance.Derive(lowerObs, trustLike(lowerObs, providerallowance.BindingVerified), reg)
	if !lowerAssessment.ShapeOK || !assessmentByID(t, lowerAssessment, "five-hour").Actionable || !lowerDerived.Confirmed || lowerDerived.Values[0].Lexical != "99.64" || *lowerObs.ProviderObservedAt != lowercase {
		t.Fatalf("lowercase advice = %#v derived=%#v", lowerAssessment.Issues, lowerDerived)
	}

	nullProvider := editJSON(t, raw, func(doc map[string]any) { doc["providerObservedAt"] = nil })
	if err := observation.Validate(instance(t, nullProvider)); err != nil {
		t.Fatal(err)
	}
	if decoded, err := providerallowance.DecodeObservation(nullProvider, providerallowance.FixtureParseLimits); err != nil || decoded.ProviderObservedAt != nil {
		t.Fatal(err)
	}
	nullWindow := editJSON(t, raw, func(doc map[string]any) {
		window := doc["measures"].([]any)[0].(map[string]any)["window"].(map[string]any)
		window["startAt"] = nil
		window["resetAt"] = nil
	})
	if err := observation.Validate(instance(t, nullWindow)); err != nil {
		t.Fatal(err)
	}
	if decoded, err := providerallowance.DecodeObservation(nullWindow, providerallowance.FixtureParseLimits); err != nil || decoded.Measures[0].Window.StartAt != nil || decoded.Measures[0].Window.ResetAt != nil {
		t.Fatal(err)
	}
	nullCompleted := editJSON(t, body, func(doc map[string]any) {
		doc["sourceStatuses"].([]any)[0].(map[string]any)["lastAttempt"].(map[string]any)["completedAt"] = nil
	})
	if err := readSchema.Validate(instance(t, nullCompleted)); err != nil {
		t.Fatal(err)
	}
	if decoded, err := providerallowance.DecodeReadResponse(nullCompleted, providerallowance.FixtureParseLimits); err != nil || decoded.SourceStatuses[0].LastAttempt.CompletedAt != nil {
		t.Fatal(err)
	}
	nullCaptured := editJSON(t, raw, func(doc map[string]any) { doc["capturedAt"] = nil })
	if err := observation.Validate(instance(t, nullCaptured)); err == nil {
		t.Fatal("schema accepted null capturedAt")
	}
	if _, err := providerallowance.DecodeObservation(nullCaptured, providerallowance.FixtureParseLimits); err == nil || !strings.Contains(err.Error(), "invalid_type") {
		t.Fatal(err)
	}
	nullServed := editJSON(t, body, func(doc map[string]any) { doc["servedAt"] = nil })
	if err := readSchema.Validate(instance(t, nullServed)); err == nil {
		t.Fatal("schema accepted null servedAt")
	}
	if _, err := providerallowance.DecodeReadResponse(nullServed, providerallowance.FixtureParseLimits); err == nil || !strings.Contains(err.Error(), "invalid_type") {
		t.Fatal(err)
	}
}
