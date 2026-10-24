package providerallowance_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v5"

	"github.com/thaynes43/dev-env/internal/providerallowance"
)

// schemaEngine is github.com/santhosh-tekuri/jsonschema/v5 v5.3.1.
// It implements Draft 2020-12, asserts date-time when AssertFormat is set,
// and embeds its metaschema. Tests refuse URL and file loads.
func compileSchema(t *testing.T, file, id string) *jsonschema.Schema {
	t.Helper()
	compiler := jsonschema.NewCompiler()
	compiler.Draft = jsonschema.Draft2020
	compiler.AssertFormat = true
	compiler.LoadURL = func(url string) (io.ReadCloser, error) {
		return nil, fmt.Errorf("refused load %s", url)
	}
	if err := compiler.AddResource(id, bytes.NewReader(loadJSON(t, file))); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile(id)
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func instance(t *testing.T, raw []byte) any {
	t.Helper()
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestReviewedFixtureHashes(t *testing.T) {
	want := map[string]string{
		"observation.schema.json":      "f7222f44f5cbd86c4c68b25edc595ba99b85deba9e0903ea04f7d0e18f5c89fd",
		"read-response.schema.json":    "0689bd9b20778d06e7aa4327849c08c082d9188178aac687a6fe6003dcbe31e8",
		"examples.json":                "96024fa068dc23f2bebb25499d25d6d5ef1c820a56dfadef41c72bbf253deea1",
		"read-interface-examples.json": "be5afb0c1500a2c731825174957060e03afcb19f2df4ab52e68237ded0a8bd6e",
		"transition-fixtures.json":     "98165e4141c635fd350422829c0db4233e3be853e428c542ca3daf71ff9544e4",
	}
	for name, sum := range want {
		got := sha256.Sum256(loadJSON(t, name))
		if hex.EncodeToString(got[:]) != sum {
			t.Fatalf("%s hash changed", name)
		}
	}
}

func TestDraft202012SchemasAcceptReviewedFixtures(t *testing.T) {
	observation := compileSchema(t, "observation.schema.json", "urn:dev-env:providerallowance:observation:v1:draft")
	readSchema := compileSchema(t, "read-response.schema.json", "urn:dev-env:providerallowance:read:v1:draft")
	for _, item := range loadExamples(t) {
		if err := observation.Validate(instance(t, item.Raw)); err != nil {
			t.Fatalf("%s: %v", item.Observation.ObservationID, err)
		}
	}
	doc, body, cli := loadRead(t)
	if err := readSchema.Validate(instance(t, doc.HTTP.Body)); err != nil {
		t.Fatal(err)
	}
	if err := readSchema.Validate(instance(t, doc.AgentRun.StdoutJSON)); err != nil {
		t.Fatal(err)
	}
	if body.Schema != providerallowance.SchemaRead || cli.Schema != providerallowance.SchemaRead {
		t.Fatal(body.Schema, cli.Schema)
	}
	offset := bytesReplace(byID(t, "historical-codex-188").Raw, `"capturedAt": "2026-10-24T16:02:43.684834Z"`, `"capturedAt": "2026-10-24T12:02:43.684834-04:00"`)
	if err := observation.Validate(instance(t, offset)); err != nil {
		t.Fatal(err)
	}
}

func TestDraft202012SchemasRejectShapeNegatives(t *testing.T) {
	observation := compileSchema(t, "observation.schema.json", "urn:dev-env:providerallowance:observation:v1:draft")
	readSchema := compileSchema(t, "read-response.schema.json", "urn:dev-env:providerallowance:read:v1:draft")
	for _, name := range []string{
		"negatives/observation-credential.json",
		"negatives/observation-bool-sequence.json",
		"negatives/observation-bad-timestamp.json",
	} {
		if err := observation.Validate(instance(t, loadJSON(t, name))); err == nil {
			t.Fatalf("%s passed schema", name)
		}
	}
	for _, name := range []string{"negatives/read-missing-served-at.json", "negatives/read-bad-freshness.json", "negatives/read-bad-timestamp.json"} {
		if err := readSchema.Validate(instance(t, loadJSON(t, name))); err == nil {
			t.Fatalf("%s passed schema", name)
		}
	}
	knownNull := loadJSON(t, "negatives/observation-known-null.json")
	if err := observation.Validate(instance(t, knownNull)); err != nil {
		t.Fatal(err)
	}
	missingRefs := cloneObservation(t, byID(t, "synthetic-multiple-windows").Observation)
	raw, err := json.Marshal(missingRefs)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	doc["derivations"] = []any{map[string]any{
		"id": "remaining", "formula": "complement_percent", "unit": "percent", "precision": "exact",
		"value": map[string]any{"state": "known", "decimal": "100"},
	}}
	encoded, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := observation.Validate(instance(t, encoded)); err == nil {
		t.Fatal("missing inputRefs passed schema")
	}
	if _, err := providerallowance.DecodeObservation(encoded, providerallowance.FixtureParseLimits); err == nil || !strings.Contains(err.Error(), "missing_field") {
		t.Fatal(err)
	}
	doc["derivations"].([]any)[0].(map[string]any)["inputRefs"] = []any{}
	doc["derivations"].([]any)[0].(map[string]any)["formula"] = "trust_me"
	encoded, err = json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := observation.Validate(instance(t, encoded)); err == nil {
		t.Fatal("unknown formula passed schema")
	}
}

func TestSchemaSuccessDoesNotProveTrust(t *testing.T) {
	observation := compileSchema(t, "observation.schema.json", "urn:dev-env:providerallowance:observation:v1:draft")
	knownNull := loadJSON(t, "negatives/observation-known-null.json")
	if err := observation.Validate(instance(t, knownNull)); err != nil {
		t.Fatal(err)
	}
	decoded, err := providerallowance.DecodeObservation(knownNull, providerallowance.FixtureParseLimits)
	if err != nil {
		t.Fatal(err)
	}
	if assessment := providerallowance.ValidateObservation(decoded, trustLike(decoded, providerallowance.BindingUnknown), reviewedRegistry()); !hasCode(assessment.Issues, providerallowance.IssueKnownDecimal) {
		t.Fatal(assessment.Issues)
	}
	forged := complementObservation(t, "0", "99")
	raw, err := json.Marshal(forged)
	if err != nil {
		t.Fatal(err)
	}
	if err := observation.Validate(instance(t, raw)); err != nil {
		t.Fatal(err)
	}
	result := providerallowance.Derive(forged, trustLike(forged, providerallowance.BindingVerified), reviewedRegistry())
	if result.Confirmed || !hasCode(result.Issues, providerallowance.IssueDerivationForged) {
		t.Fatalf("%#v", result)
	}
	empty := complementObservation(t, "0", "100")
	empty.Derivations[0].InputRefs = []string{}
	raw, err = json.Marshal(empty)
	if err != nil {
		t.Fatal(err)
	}
	if err := observation.Validate(instance(t, raw)); err != nil {
		t.Fatal(err)
	}
	result = providerallowance.Derive(empty, trustLike(empty, providerallowance.BindingVerified), reviewedRegistry())
	if result.Confirmed || !hasCode(result.Issues, providerallowance.IssueDerivationInputMissing) {
		t.Fatalf("%#v", result)
	}
}
