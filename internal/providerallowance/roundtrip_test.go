package providerallowance_test

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/providerallowance"
)

func TestObservationExamplesRoundTrip(t *testing.T) {
	examples := loadExamples(t)
	if len(examples) != 9 {
		t.Fatalf("examples = %d", len(examples))
	}
	labels := map[string]string{}
	for _, item := range examples {
		labels[item.Observation.ObservationID] = item.Label
		encoded, err := json.Marshal(item.Observation)
		if err != nil {
			t.Fatal(err)
		}
		again, err := providerallowance.DecodeObservation(encoded, providerallowance.FixtureParseLimits)
		if err != nil {
			t.Fatalf("%s: %v", item.Observation.ObservationID, err)
		}
		if !reflect.DeepEqual(item.Observation, again) {
			t.Fatalf("%s roundtrip changed the document", item.Observation.ObservationID)
		}
	}
	if labels["historical-codex-188"] != "historical_native_evidence" || labels["historical-cursor-188"] != "historical_display_evidence" || labels["claude-no-retained-output"] != "historical_capability_gap" {
		t.Fatalf("labels = %#v", labels)
	}
	for _, id := range []string{"synthetic-multiple-windows", "synthetic-supported-over-limit", "synthetic-openrouter", "synthetic-ollama", "synthetic-zai-three-windows", "synthetic-advisory"} {
		if labels[id] != "synthetic_only" {
			t.Fatalf("%s label = %s", id, labels[id])
		}
	}
}

func TestHistoricalAndSyntheticFieldsStayLiteral(t *testing.T) {
	codex := byID(t, "historical-codex-188").Observation
	if codex.ProviderObservedAt != nil || codex.Scope.AccountRef != nil || codex.Scope.BindingState != providerallowance.BindingUnknown {
		t.Fatal("codex account or provider time was filled in")
	}
	if codex.CapturedAt != "2026-10-24T16:02:43.684834Z" {
		t.Fatal(codex.CapturedAt)
	}
	primary := measureByID(codex, "codex-primary")
	if primary.Value.Decimal == nil || *primary.Value.Decimal != "50" || primary.Window.DurationMinutes == nil || *primary.Window.DurationMinutes != 10080 {
		t.Fatalf("primary = %#v", primary.Value)
	}
	secondary := measureByID(codex, "codex-secondary")
	if secondary.Value.State != providerallowance.ValueNotReported || secondary.Value.Decimal != nil {
		t.Fatalf("secondary = %#v", secondary.Value)
	}
	cursor := byID(t, "historical-cursor-188").Observation
	if len(cursor.Measures) != 0 || len(cursor.Derivations) != 0 {
		t.Fatal("cursor display gained a normalized measure")
	}
	got := map[string]string{}
	for _, field := range cursor.DirectFields {
		if field.LexicalValue == nil {
			t.Fatalf("%s lexical is null", field.Field)
		}
		got[field.Field] = *field.LexicalValue
	}
	want := map[string]string{
		"plan": "Super", "periodHeading": "Monthly plan and on-demand usage",
		"included": "75% used", "auto": "75% used", "api": "0% used",
		"onDemand": "$0 / $200", "remainingLabel": "$200 remaining", "resetLabel": "Oct 28",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cursor strings = %#v", got)
	}
	claude := byID(t, "claude-no-retained-output").Observation
	if claude.ErrorCode == nil || *claude.ErrorCode != "NO_RETAINED_SANITIZED_OBSERVATION" {
		t.Fatal("claude error code changed")
	}
	for _, measure := range claude.Measures {
		if measure.Value.State != providerallowance.ValueUnknown || measure.Value.Decimal != nil {
			t.Fatalf("%s = %#v", measure.ID, measure.Value)
		}
	}
	multi := byID(t, "synthetic-multiple-windows").Observation
	states := map[string]providerallowance.ValueState{}
	decimals := map[string]string{}
	for _, measure := range multi.Measures {
		states[measure.ID] = measure.Value.State
		if measure.Value.Decimal != nil {
			decimals[measure.ID] = *measure.Value.Decimal
		}
		if measure.Window.Ref == "" {
			t.Fatal("window ref missing")
		}
	}
	if decimals["five-hour"] != "0.36" || decimals["seven-day"] != "0" {
		t.Fatalf("decimals = %#v", decimals)
	}
	if states["unknown-meter"] != providerallowance.ValueUnknown || states["not-reported"] != providerallowance.ValueNotReported || states["not-applicable"] != providerallowance.ValueNotApplicable || states["uncapped"] != providerallowance.ValueUncapped {
		t.Fatalf("states = %#v", states)
	}
	if decimals["five-hour"] == "36" {
		t.Fatal("percent was scaled")
	}
	over := measureByID(byID(t, "synthetic-supported-over-limit").Observation, "over-limit")
	if over.Value.Decimal == nil || *over.Value.Decimal != "101.25" {
		t.Fatalf("over-limit = %#v", over.Value.Decimal)
	}
	money := measureByID(byID(t, "synthetic-openrouter").Observation, "key-budget")
	if money.Currency == nil || *money.Currency != "USD" || money.Value.Decimal == nil || *money.Value.Decimal != "2.50" {
		t.Fatalf("money = %#v", money)
	}
}

func TestReadEnvelopeRoundTrip(t *testing.T) {
	doc, httpBody, cliBody := loadRead(t)
	if doc.AgentRun.Command != providerallowance.UsageJSONCommand {
		t.Fatal(doc.AgentRun.Command)
	}
	if !strings.Contains(doc.Status, "SYNTHETIC") {
		t.Fatal("cache times are not labeled synthetic")
	}
	if !reflect.DeepEqual(httpBody, cliBody) {
		t.Fatal("HTTP and CLI documents differ")
	}
	encoded, err := json.Marshal(httpBody)
	if err != nil {
		t.Fatal(err)
	}
	again, err := providerallowance.DecodeReadResponse(encoded, providerallowance.FixtureParseLimits)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(httpBody, again) {
		t.Fatal("read roundtrip changed the document")
	}
	if len(httpBody.Items) != 4 {
		t.Fatalf("items = %d", len(httpBody.Items))
	}
	codex := httpBody.Items[0]
	if codex.Observation.ObservationID != "historical-codex-188" || codex.Observation.ProviderObservedAt != nil {
		t.Fatal("codex provider time was filled in")
	}
	if codex.ReceivedAt == httpBody.ServedAt || codex.Observation.CapturedAt == httpBody.ServedAt || httpBody.SourceStatuses[0].LastAttempt.AttemptedAt == httpBody.ServedAt {
		t.Fatal("servedAt replaced another timestamp")
	}
	if codex.Validation.State != providerallowance.ValidationSourceFields || codex.Binding.State != providerallowance.BindingUnknown || codex.Freshness.State != providerallowance.FreshUnknown {
		t.Fatal("validation, binding, and freshness were collapsed")
	}
	if len(httpBody.Items[1].Observation.Measures) != 0 {
		t.Fatal("cursor read gained a measure")
	}
	for _, current := range httpBody.Items[2].CurrentMeasures {
		if current.State != providerallowance.CurrentUnknown || current.Value.State != providerallowance.ValueUnknown || current.Value.Decimal != nil {
			t.Fatalf("claude current = %#v", current)
		}
	}
	expired := httpBody.Items[3]
	if expired.Observation.Measures[0].Value.Decimal == nil || *expired.Observation.Measures[0].Value.Decimal != "0.36" {
		t.Fatal("historical 0.36 was not retained")
	}
	if expired.CurrentMeasures[0].State != providerallowance.CurrentExpired || expired.CurrentMeasures[0].Value.Decimal != nil || expired.CurrentMeasures[0].Value.State != providerallowance.ValueUnknown {
		t.Fatalf("current = %#v", expired.CurrentMeasures[0])
	}
	status := httpBody.SourceStatuses[3]
	if status.LastGood == nil || status.LastGood.CapturedAt != "2026-10-23T10:00:00Z" || status.LastAttempt.Result != providerallowance.AttemptError {
		t.Fatal("last good did not survive the failed attempt")
	}
	if status.LastAttempt.AttemptedAt == status.LastGood.CapturedAt || status.Freshness.AsOf != httpBody.ServedAt {
		t.Fatal("attempt time or freshness asOf was rewritten")
	}
	// Decoding the fixture again is not a collector restart or a provider read.
	second, err := providerallowance.DecodeReadResponse(doc.HTTP.Body, providerallowance.FixtureParseLimits)
	if err != nil {
		t.Fatal(err)
	}
	if second.ServedAt != httpBody.ServedAt || second.Items[3].Observation.CapturedAt != expired.Observation.CapturedAt || second.Items[3].ReceivedAt != expired.ReceivedAt {
		t.Fatal("a second decode freshened timestamps")
	}
}

func TestParseLimitsAndAmbiguousJSON(t *testing.T) {
	raw := byID(t, "historical-codex-188").Raw
	if _, err := providerallowance.DecodeObservation(raw, providerallowance.ParseLimits{}); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatal(err)
	}
	limits := providerallowance.FixtureParseLimits
	limits.MaxBytes = len(raw) - 1
	if _, err := providerallowance.DecodeObservation(raw, limits); err == nil {
		t.Fatal("oversize document accepted")
	}
	limits = providerallowance.FixtureParseLimits
	limits.MaxDepth = 2
	if _, err := providerallowance.DecodeObservation(raw, limits); err == nil {
		t.Fatal("deep document accepted")
	}
	limits = providerallowance.FixtureParseLimits
	limits.MaxArrayItems = 1
	if _, err := providerallowance.DecodeObservation(raw, limits); err == nil {
		t.Fatal("long array accepted")
	}
	limits = providerallowance.FixtureParseLimits
	limits.MaxObjectKeys = 2
	if _, err := providerallowance.DecodeObservation(raw, limits); err == nil {
		t.Fatal("wide object accepted")
	}
	limits = providerallowance.FixtureParseLimits
	limits.MaxStringBytes = 4
	if _, err := providerallowance.DecodeObservation(raw, limits); err == nil {
		t.Fatal("long string accepted")
	}
	dup := loadJSON(t, "negatives/observation-duplicate-key.json")
	if _, err := providerallowance.DecodeObservation(dup, providerallowance.FixtureParseLimits); err == nil || !strings.Contains(err.Error(), "duplicate_key") {
		t.Fatal(err)
	}
	uni := loadJSON(t, "negatives/observation-unicode-duplicate.json")
	if _, err := providerallowance.DecodeObservation(uni, providerallowance.FixtureParseLimits); err == nil || !strings.Contains(err.Error(), "duplicate_key") {
		t.Fatal(err)
	}
	boolean := loadJSON(t, "negatives/observation-bool-sequence.json")
	if _, err := providerallowance.DecodeObservation(boolean, providerallowance.FixtureParseLimits); err == nil || !strings.Contains(err.Error(), "invalid_type") {
		t.Fatal(err)
	}
	badTime := loadJSON(t, "negatives/observation-bad-timestamp.json")
	if _, err := providerallowance.DecodeObservation(badTime, providerallowance.FixtureParseLimits); err == nil || !strings.Contains(err.Error(), "invalid_time") {
		t.Fatal(err)
	}
	offset := bytesReplace(raw, `"capturedAt": "2026-10-24T16:02:43.684834Z"`, `"capturedAt": "2026-10-24T12:02:43.684834-04:00"`)
	if _, err := providerallowance.DecodeObservation(offset, providerallowance.FixtureParseLimits); err != nil {
		t.Fatal(err)
	}
	fraction := bytesReplace(raw, `"sequence": 1`, `"sequence": 1.5`)
	if _, err := providerallowance.DecodeObservation(fraction, providerallowance.FixtureParseLimits); err == nil {
		t.Fatal("fractional integer accepted")
	}
	whole := bytesReplace(raw, `"sequence": 1`, `"sequence": 1.0`)
	obs, err := providerallowance.DecodeObservation(whole, providerallowance.FixtureParseLimits)
	if err != nil || obs.Sequence != 1 {
		t.Fatal(err, obs.Sequence)
	}
	cred := loadJSON(t, "negatives/observation-credential.json")
	if _, err := providerallowance.DecodeObservation(cred, providerallowance.FixtureParseLimits); err == nil || !strings.Contains(err.Error(), "unknown_field") {
		t.Fatal(err)
	}
}

func bytesReplace(in []byte, old, new string) []byte {
	return []byte(strings.Replace(string(in), old, new, 1))
}

func TestNonTestImportsStayInTheStandardLibrary(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	banned := []string{`"net`, `"os"`, `"database/`, `"k8s.io/`, `"google.golang.org/`, `"github.com/`, `"internal/agentd`, `"internal/apiserver`, `"internal/agentrun`}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		textBytes, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		text := string(textBytes)
		for _, item := range banned {
			if strings.Contains(text, item) {
				t.Fatalf("%s contains %s", name, item)
			}
		}
	}
}
