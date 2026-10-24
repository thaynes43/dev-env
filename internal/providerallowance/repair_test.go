package providerallowance_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/providerallowance"
)

func TestCanonicalNamesRejectFoldedAliases(t *testing.T) {
	obs := byID(t, "synthetic-multiple-windows")
	if _, err := providerallowance.DecodeObservation(obs.Raw, providerallowance.FixtureParseLimits); err != nil {
		t.Fatal(err)
	}
	body := readBody(t)
	if _, err := providerallowance.DecodeReadResponse(body, providerallowance.FixtureParseLimits); err != nil {
		t.Fatal(err)
	}
	preserved := mustReplace(t, obs.Raw, `"id": "five-hour"`, `"id": "Provider"`)
	preserved = mustReplace(t, preserved, `"ref": "five-hour-window"`, `"ref": "\u017fource"`)
	decoded, err := providerallowance.DecodeObservation(preserved, providerallowance.FixtureParseLimits)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Measures[0].ID != "Provider" || decoded.Measures[0].Window.Ref != "\u017fource" {
		t.Fatalf("values were rewritten: id %q ref %q", decoded.Measures[0].ID, decoded.Measures[0].Window.Ref)
	}

	cases := []struct {
		name   string
		raw    []byte
		read   bool
		old    string
		after  string
		before string
	}{
		{
			name: "root provider", raw: obs.Raw,
			old:    `"provider": "synthetic-provider"`,
			after:  `"provider": "synthetic-provider", "Provider": "synthetic-provider"`,
			before: `"Provider": "synthetic-provider", "provider": "synthetic-provider"`,
		},
		{
			name: "root long-s schema", raw: obs.Raw,
			old:    `"schema": "providerallowance.v1"`,
			after:  `"schema": "providerallowance.v1", "\u017fchema": "providerallowance.v1"`,
			before: `"\u017fchema": "providerallowance.v1", "schema": "providerallowance.v1"`,
		},
		{
			name: "nested source kind", raw: obs.Raw,
			old:    `"kind": "synthetic"`,
			after:  `"kind": "synthetic", "Kind": "synthetic"`,
			before: `"Kind": "synthetic", "kind": "synthetic"`,
		},
		{
			name: "nested source version fold", raw: obs.Raw,
			old:    `"version": "fixture-v1"`,
			after:  `"version": "fixture-v1", "ver\u017fion": "fixture-v1"`,
			before: `"ver\u017fion": "fixture-v1", "version": "fixture-v1"`,
		},
		{
			name: "measure window", raw: obs.Raw,
			old:    `"ref": "five-hour-window"`,
			after:  `"ref": "five-hour-window", "Ref": "five-hour-window"`,
			before: `"Ref": "five-hour-window", "ref": "five-hour-window"`,
		},
		{
			name: "read servedAt", raw: body, read: true,
			old:    `"servedAt": "2026-10-24T17:00:00Z"`,
			after:  `"servedAt": "2026-10-24T17:00:00Z", "ServedAt": "2026-10-24T17:00:00Z"`,
			before: `"ServedAt": "2026-10-24T17:00:00Z", "servedAt": "2026-10-24T17:00:00Z"`,
		},
		{
			name: "read attempt code", raw: body, read: true,
			old:    `"code": "SYNTHETIC_SOURCE_READ_FAILED"`,
			after:  `"code": "SYNTHETIC_SOURCE_READ_FAILED", "Code": "SYNTHETIC_SOURCE_READ_FAILED"`,
			before: `"Code": "SYNTHETIC_SOURCE_READ_FAILED", "code": "SYNTHETIC_SOURCE_READ_FAILED"`,
		},
	}
	for _, tc := range cases {
		for _, replacement := range []string{tc.after, tc.before} {
			t.Run(tc.name, func(t *testing.T) {
				raw := mustReplace(t, tc.raw, tc.old, replacement)
				var err error
				if tc.read {
					_, err = providerallowance.DecodeReadResponse(raw, providerallowance.FixtureParseLimits)
				} else {
					_, err = providerallowance.DecodeObservation(raw, providerallowance.FixtureParseLimits)
				}
				if err == nil || !strings.Contains(err.Error(), "unknown_field") {
					t.Fatalf("%v", err)
				}
			})
		}
	}

	lone := mustReplace(t, obs.Raw, `"provider": "synthetic-provider"`, `"Provider": "synthetic-provider"`)
	if _, err := providerallowance.DecodeObservation(lone, providerallowance.FixtureParseLimits); err == nil || !strings.Contains(err.Error(), "unknown_field") {
		t.Fatal(err)
	}
	nestedDup := mustReplace(t, obs.Raw, `"kind": "synthetic"`, `"kind": "synthetic", "kind": "other"`)
	if _, err := providerallowance.DecodeObservation(nestedDup, providerallowance.FixtureParseLimits); err == nil || !strings.Contains(err.Error(), "duplicate_key") {
		t.Fatal(err)
	}
	escapedDup := mustReplace(t, obs.Raw, `"kind": "synthetic"`, `"kind": "synthetic", "\u006bind": "other"`)
	if _, err := providerallowance.DecodeObservation(escapedDup, providerallowance.FixtureParseLimits); err == nil || !strings.Contains(err.Error(), "duplicate_key") {
		t.Fatal(err)
	}
	fresh := mustReplaceFirst(t, body, `"state": "expired"`, `"State": "expired", "state": "expired"`)
	if _, err := providerallowance.DecodeReadResponse(fresh, providerallowance.FixtureParseLimits); err == nil || !strings.Contains(err.Error(), "unknown_field") {
		t.Fatal(err)
	}
	fresh = mustReplaceFirst(t, body, `"state": "expired"`, `"state": "expired", "State": "expired"`)
	if _, err := providerallowance.DecodeReadResponse(fresh, providerallowance.FixtureParseLimits); err == nil || !strings.Contains(err.Error(), "unknown_field") {
		t.Fatal(err)
	}
}

func TestNullAndMissingStayDistinct(t *testing.T) {
	raw := byID(t, "synthetic-multiple-windows").Raw
	decoded, err := providerallowance.DecodeObservation(raw, providerallowance.FixtureParseLimits)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Measures[0].Window.StartAt != nil || decoded.ProviderObservedAt == nil {
		t.Fatal("null window start or provider time was rewritten")
	}
	assessment := providerallowance.ValidateObservation(decoded, trustLike(decoded, providerallowance.BindingVerified), reviewedRegistry())
	if !assessment.ShapeOK || !assessmentByID(t, assessment, "five-hour").Actionable {
		t.Fatal(assessment.Issues)
	}
	missing := bytes.Replace(raw, []byte(`"startAt": null,`), nil, 1)
	if _, err := providerallowance.DecodeObservation(missing, providerallowance.FixtureParseLimits); err == nil || !strings.Contains(err.Error(), "missing_field") || !strings.Contains(err.Error(), "startAt") {
		t.Fatal(err)
	}
}

func TestPolicyRefusalClearsActionableMeasures(t *testing.T) {
	obs := byID(t, "synthetic-multiple-windows").Observation
	reg := reviewedRegistry()
	positive := providerallowance.ValidateObservation(obs, trustLike(obs, providerallowance.BindingVerified), reg)
	if !assessmentByID(t, positive, "five-hour").Actionable || !assessmentByID(t, positive, "seven-day").Actionable || !assessmentByID(t, positive, "uncapped").Actionable {
		t.Fatalf("positive = %#v", positive.Measures)
	}
	unknown := assessmentByID(t, positive, "unknown-meter")
	if unknown.Actionable || unknown.SemanticsEvidenced || decimalsOf(obs)["five-hour"] != "0.36" || decimalsOf(obs)["seven-day"] != "0" {
		t.Fatal("partial coverage was collapsed")
	}

	bad := cloneObservation(t, obs)
	bad.DirectFields = append(bad.DirectFields, providerallowance.DirectField{
		Field: "untrusted", OriginalType: providerallowance.TypeString, LexicalValue: sp("not-allowlisted"),
	})
	before := snapshot(t, bad)
	refused := providerallowance.ValidateObservation(bad, trustLike(bad, providerallowance.BindingVerified), reg)
	assertUnchanged(t, before, bad)
	if !hasCode(refused.Issues, providerallowance.IssueFieldNotAllowlisted) {
		t.Fatal(refused.Issues)
	}
	assertNoneActionable(t, refused)
	five := assessmentByID(t, refused, "five-hour")
	if !five.SemanticsEvidenced || !five.InRange || five.Actionable {
		t.Fatalf("allowlist diagnostic = %#v", five)
	}

	codex := byID(t, "historical-codex-188").Observation
	historical := providerallowance.ValidateObservation(codex, trustLike(codex, providerallowance.BindingUnknown), reg)
	primary := assessmentByID(t, historical, "codex-primary")
	if !primary.SemanticsEvidenced || !primary.HistoricalOnly || primary.Actionable {
		t.Fatalf("historical = %#v", primary)
	}
	secondary := measureByID(codex, "codex-secondary")
	if secondary.Value.State != providerallowance.ValueNotReported || secondary.Value.Decimal != nil {
		t.Fatal("historical unknown was rewritten")
	}

	inventory := inventoryRegistry()
	held := cloneObservation(t, obs)
	held.DirectFields = []providerallowance.DirectField{{
		Field: "resetCredits.availableCount", OriginalType: providerallowance.TypeDecimal, LexicalValue: sp("0"),
	}}
	allowed := providerallowance.ValidateObservation(held, trustLike(held, providerallowance.BindingVerified), inventory)
	if !assessmentByID(t, allowed, "five-hour").Actionable || hasCode(allowed.Issues, providerallowance.IssueResetCreditMisuse) {
		t.Fatal(allowed.Issues)
	}
	misuse := cloneObservation(t, held)
	budget := measureByID(misuse, "five-hour")
	budget.ID = "reset-as-budget"
	budget.Category = providerallowance.CategoryRateLimit
	budget.Direction = providerallowance.DirectionLimit
	budget.Value = providerallowance.MeasureValue{State: providerallowance.ValueUnknown}
	budget.ValidationRef = nil
	budget.EvidenceFields = []string{"resetCredits.availableCount"}
	misuse.Measures = append(misuse.Measures, budget)
	blocked := providerallowance.ValidateObservation(misuse, trustLike(misuse, providerallowance.BindingVerified), inventory)
	if !hasCode(blocked.Issues, providerallowance.IssueResetCreditMisuse) || blocked.ResetCredit.RateLimitBudget || blocked.ResetCredit.BlocksAccountUse || blocked.ResetCredit.GrantsCapacity || blocked.ResetCredit.CountLexical != "0" {
		t.Fatalf("reset = %#v issues=%v", blocked.ResetCredit, blocked.Issues)
	}
	assertNoneActionable(t, blocked)
	if !assessmentByID(t, blocked, "five-hour").SemanticsEvidenced || *misuse.Measures[0].Value.Decimal != "0.36" {
		t.Fatal("reset policy erased the measure")
	}

	cursor := cloneObservation(t, byID(t, "historical-cursor-188").Observation)
	cursor.Measures = []providerallowance.Measure{measureByID(obs, "five-hour")}
	display := providerallowance.ValidateObservation(cursor, trustLike(cursor, providerallowance.BindingUnknown), reg)
	if !hasCode(display.Issues, providerallowance.IssueDisplayPromotion) {
		t.Fatal(display.Issues)
	}
	assertNoneActionable(t, display)

	restricted := noDerivationRegistry()
	open := providerallowance.ValidateObservation(obs, trustLike(obs, providerallowance.BindingVerified), restricted)
	if !assessmentByID(t, open, "five-hour").Actionable || hasCode(open.Issues, providerallowance.IssueDerivationFormula) {
		t.Fatal(open.Issues)
	}
	derived := cloneObservation(t, obs)
	derived.Derivations = []providerallowance.Derivation{{
		ID: "remaining", Formula: providerallowance.FormulaComplement, InputRefs: []string{"five-hour"},
		Unit: providerallowance.UnitPercent, Precision: "exact",
		Value: providerallowance.MeasureValue{State: providerallowance.ValueKnown, Decimal: sp("99.64")},
	}}
	policy := providerallowance.ValidateObservation(derived, trustLike(derived, providerallowance.BindingVerified), restricted)
	if !hasCode(policy.Issues, providerallowance.IssueDerivationFormula) {
		t.Fatal(policy.Issues)
	}
	assertNoneActionable(t, policy)
}

func TestTypedShapeRejectsConstructedWireErrors(t *testing.T) {
	base := byID(t, "synthetic-multiple-windows").Observation
	reg := reviewedRegistry()
	positive := providerallowance.ValidateObservation(base, trustLike(base, providerallowance.BindingVerified), reg)
	if !positive.ShapeOK || !assessmentByID(t, positive, "five-hour").Actionable {
		t.Fatal(positive.Issues)
	}
	for _, item := range loadExamples(t) {
		assessment := providerallowance.ValidateObservation(item.Observation, trustLike(item.Observation, item.Observation.Scope.BindingState), reg)
		if !assessment.ShapeOK {
			t.Fatalf("%s shape: %v", item.Observation.ObservationID, assessment.Issues)
		}
	}
	codex := byID(t, "historical-codex-188").Observation
	if codex.ProviderObservedAt != nil {
		t.Fatal(codex.ProviderObservedAt)
	}

	cases := []struct {
		name string
		code string
		edit func(*providerallowance.Observation)
	}{
		{"schema", providerallowance.IssueInvalidSchema, func(obs *providerallowance.Observation) { obs.Schema = "unsupported-version" }},
		{"availability", providerallowance.IssueInvalidEnum, func(obs *providerallowance.Observation) { obs.Availability = "fresh" }},
		{"binding", providerallowance.IssueInvalidEnum, func(obs *providerallowance.Observation) {
			obs.Scope.BindingState = "yes"
		}},
		{"source kind", providerallowance.IssueInvalidEnum, func(obs *providerallowance.Observation) {
			obs.Source.Kind = "rpc"
		}},
		{"capturedAt", providerallowance.IssueInvalidTime, func(obs *providerallowance.Observation) {
			obs.CapturedAt = "2026-10-24T16:00:00"
		}},
		{"provider time", providerallowance.IssueInvalidTime, func(obs *providerallowance.Observation) {
			obs.ProviderObservedAt = sp("yesterday")
		}},
		{"sequence", providerallowance.IssueInvalidCount, func(obs *providerallowance.Observation) { obs.Sequence = -1 }},
		{"generation", providerallowance.IssueInvalidCount, func(obs *providerallowance.Observation) {
			obs.BindingGeneration = -1
		}},
		{"duration", providerallowance.IssueInvalidCount, func(obs *providerallowance.Observation) {
			n := providerallowance.Count(-5)
			obs.Measures[0].Window.DurationMinutes = &n
		}},
		{"category", providerallowance.IssueInvalidEnum, func(obs *providerallowance.Observation) {
			obs.Measures[0].Category = "quota"
		}},
		{"direction", providerallowance.IssueInvalidEnum, func(obs *providerallowance.Observation) {
			obs.Measures[0].Direction = "up"
		}},
		{"reset", providerallowance.IssueInvalidEnum, func(obs *providerallowance.Observation) {
			obs.Measures[0].Window.ResetPrecision = "soon"
		}},
		{"value state", providerallowance.IssueInvalidEnum, func(obs *providerallowance.Observation) {
			obs.Measures[0].Value.State = "maybe"
		}},
		{"decimal", providerallowance.IssueKnownDecimal, func(obs *providerallowance.Observation) {
			obs.Measures[0].Value.Decimal = sp("01")
		}},
		{"known null", providerallowance.IssueKnownDecimal, func(obs *providerallowance.Observation) {
			obs.Measures[0].Value.Decimal = nil
		}},
		{"formula", providerallowance.IssueInvalidEnum, func(obs *providerallowance.Observation) {
			obs.Derivations = []providerallowance.Derivation{{
				ID: "remaining", Formula: "trust_me", InputRefs: []string{"five-hour"},
				Unit: providerallowance.UnitPercent, Precision: "exact",
				Value: providerallowance.MeasureValue{State: providerallowance.ValueKnown, Decimal: sp("100")},
			}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			obs := cloneObservation(t, base)
			tc.edit(&obs)
			assessment := providerallowance.ValidateObservation(obs, trustLike(obs, providerallowance.BindingVerified), reg)
			if assessment.ShapeOK || !hasCode(assessment.Issues, tc.code) {
				t.Fatalf("shape %v issues %v", assessment.ShapeOK, assessment.Issues)
			}
			assertNoneActionable(t, assessment)
		})
	}
}

func inventoryRegistry() *providerallowance.StaticRegistry {
	base := reviewedRegistry()
	sources := base.Sources()
	for i := range sources {
		if sources[i].Provider != "synthetic-provider" {
			continue
		}
		sources[i].DirectFields = map[string]providerallowance.DirectFieldRule{
			"resetCredits.availableCount": {OriginalType: providerallowance.TypeDecimal, Class: providerallowance.ClassInventory},
		}
	}
	return providerallowance.NewStaticRegistry(providerallowance.RegistryParts{
		Sources: sources, Metrics: base.Metrics(), Accounts: base.Accounts(),
	})
}

func noDerivationRegistry() *providerallowance.StaticRegistry {
	base := reviewedRegistry()
	sources := base.Sources()
	for i := range sources {
		if sources[i].Provider == "synthetic-provider" {
			sources[i].AllowDerivations = false
		}
	}
	return providerallowance.NewStaticRegistry(providerallowance.RegistryParts{
		Sources: sources, Metrics: base.Metrics(), Accounts: base.Accounts(),
	})
}

func assertNoneActionable(t *testing.T, assessment providerallowance.Assessment) {
	t.Helper()
	for _, measure := range assessment.Measures {
		if measure.Actionable {
			t.Fatalf("%s stayed actionable", measure.ID)
		}
	}
}

func readBody(t *testing.T) []byte {
	t.Helper()
	doc, _, _ := loadRead(t)
	return doc.HTTP.Body
}

func mustReplace(t *testing.T, raw []byte, old, replacement string) []byte {
	t.Helper()
	count := bytes.Count(raw, []byte(old))
	if count != 1 {
		t.Fatalf("anchor %q count = %d", old, count)
	}
	return bytes.Replace(raw, []byte(old), []byte(replacement), 1)
}

func mustReplaceFirst(t *testing.T, raw []byte, old, replacement string) []byte {
	t.Helper()
	if bytes.Count(raw, []byte(old)) < 1 {
		t.Fatalf("anchor %q missing", old)
	}
	return bytes.Replace(raw, []byte(old), []byte(replacement), 1)
}
