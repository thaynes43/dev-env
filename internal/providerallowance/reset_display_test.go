package providerallowance_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/providerallowance"
)

func TestLegacyResetDisplayOmissionKeepsDigest(t *testing.T) {
	loaded := byID(t, "synthetic-multiple-windows")
	if bytes.Contains(loaded.Raw, []byte("resetDisplayDate")) || bytes.Contains(loaded.Raw, []byte("resetDisplayText")) {
		t.Fatal("reviewed fixture already carries reset display fields")
	}
	decoded, err := providerallowance.DecodeObservation(loaded.Raw, providerallowance.FixtureParseLimits)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(canonical, []byte("resetDisplayDate")) || bytes.Contains(canonical, []byte("resetDisplayText")) {
		t.Fatalf("canonical legacy window grew reset display fields: %s", canonical)
	}
	sum := sha256.Sum256(canonical)
	injected := bytes.ReplaceAll(canonical, []byte(`"resetPrecision":`), []byte(`"resetDisplayDate":null,"resetDisplayText":null,"resetPrecision":`))
	if bytes.Equal(injected, canonical) {
		t.Fatal("null display keys were not inserted")
	}
	again, err := providerallowance.DecodeObservation(injected, providerallowance.FixtureParseLimits)
	if err != nil {
		t.Fatal(err)
	}
	remarshaled, err := json.Marshal(again)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(remarshaled, canonical) || sha256.Sum256(remarshaled) != sum {
		t.Fatal("null display keys changed the legacy digest")
	}
	trust := trustLike(decoded, providerallowance.BindingVerified)
	reg := reviewedRegistry()
	before := providerallowance.Derive(decoded, trust, reg)
	after := providerallowance.Derive(again, trust, reg)
	if before.Confirmed != after.Confirmed || len(before.Values) != len(after.Values) || len(before.Issues) != len(after.Issues) {
		t.Fatalf("derive changed: %#v vs %#v", before, after)
	}
	folded := bytes.Replace(canonical, []byte(`"resetPrecision":`), []byte(`"ResetDisplayDate":"2026-12-01","resetPrecision":`), 1)
	if _, err := providerallowance.DecodeObservation(folded, providerallowance.FixtureParseLimits); err == nil || !strings.Contains(err.Error(), "unknown_field") {
		t.Fatal(err)
	}
	lower := bytes.Replace(canonical, []byte(`"resetPrecision":`), []byte(`"resetDisplaydate":"2026-12-01","resetPrecision":`), 1)
	if _, err := providerallowance.DecodeObservation(lower, providerallowance.FixtureParseLimits); err == nil || !strings.Contains(err.Error(), "unknown_field") {
		t.Fatal(err)
	}
	dup := bytes.Replace(canonical, []byte(`"resetPrecision":`), []byte(`"resetDisplayDate":null,"resetDisplayDate":null,"resetPrecision":`), 1)
	if _, err := providerallowance.DecodeObservation(dup, providerallowance.FixtureParseLimits); err == nil || !strings.Contains(err.Error(), "duplicate_key") {
		t.Fatal(err)
	}
	unknown := bytes.Replace(canonical, []byte(`"resetPrecision":`), []byte(`"resetDisplayLabel":"Nov 4","resetPrecision":`), 1)
	if _, err := providerallowance.DecodeObservation(unknown, providerallowance.FixtureParseLimits); err == nil || !strings.Contains(err.Error(), "unknown_field") {
		t.Fatal(err)
	}
	if !bytes.Contains(canonical, []byte(`"startAt":null,`)) {
		t.Fatal("canonical window has no null startAt")
	}
	missing := bytes.Replace(canonical, []byte(`"startAt":null,`), nil, 1)
	if _, err := providerallowance.DecodeObservation(missing, providerallowance.FixtureParseLimits); err == nil || !strings.Contains(err.Error(), "missing_field") || !strings.Contains(err.Error(), "startAt") {
		t.Fatal(err)
	}
}

func TestResetDisplayDateAndYearlessRoundTrip(t *testing.T) {
	base := byID(t, "synthetic-multiple-windows").Observation
	dateObs := base
	dateObs.Measures = append([]providerallowance.Measure(nil), base.Measures...)
	dateObs.Measures[0].Window.ResetAt = nil
	dateObs.Measures[0].Window.ResetPrecision = providerallowance.ResetDisplayOnly
	dateObs.Measures[0].Window.ResetDisplayDate = sp("2026-12-01")
	raw, err := json.Marshal(dateObs)
	if err != nil {
		t.Fatal(err)
	}
	windowRaw, err := json.Marshal(dateObs.Measures[0].Window)
	if err != nil || bytes.Contains(windowRaw, []byte("2026-12-01T")) || bytes.Contains(windowRaw, []byte("T00:00:00")) || !bytes.Contains(windowRaw, []byte("2026-12-01")) {
		t.Fatalf("date window bytes: %v %s", err, windowRaw)
	}
	decoded, err := providerallowance.DecodeObservation(raw, providerallowance.FixtureParseLimits)
	if err != nil {
		t.Fatal(err)
	}
	got := decoded.Measures[0].Window
	if got.ResetDisplayDate == nil || *got.ResetDisplayDate != "2026-12-01" || got.ResetDisplayText != nil || got.ResetAt != nil || got.ResetPrecision != providerallowance.ResetDisplayOnly {
		t.Fatalf("date observation = %#v", got)
	}
	switch {
	case decoded.ProviderObservedAt == nil && base.ProviderObservedAt == nil:
	case decoded.ProviderObservedAt != nil && base.ProviderObservedAt != nil && *decoded.ProviderObservedAt == *base.ProviderObservedAt:
	default:
		t.Fatal("provider time changed")
	}
	textObs := base
	textObs.Measures = append([]providerallowance.Measure(nil), base.Measures...)
	textObs.Measures[0].Window.ResetAt = nil
	textObs.Measures[0].Window.ResetPrecision = providerallowance.ResetDisplayOnly
	textObs.Measures[0].Window.ResetDisplayText = sp("Feb 29")
	textRaw, err := json.Marshal(textObs.Measures[0].Window)
	if err != nil || bytes.Contains(textRaw, []byte("2024-02-29")) || bytes.Contains(textRaw, []byte("2028-02-29")) || bytes.Contains(textRaw, []byte("T00:00:00")) || !bytes.Contains(textRaw, []byte("Feb 29")) {
		t.Fatalf("yearless window bytes: %v %s", err, textRaw)
	}
	textDecodedWindow := decodeWindow(t, textRaw)
	if textDecodedWindow.ResetDisplayText == nil || *textDecodedWindow.ResetDisplayText != "Feb 29" || textDecodedWindow.ResetDisplayDate != nil || textDecodedWindow.ResetAt != nil {
		t.Fatalf("yearless window = %#v", textDecodedWindow)
	}
	_, body, _ := loadRead(t)
	if len(body.Items) == 0 || len(body.Items[0].Observation.Measures) == 0 {
		t.Fatal("read fixture has no measure")
	}
	body.Items[0].Observation.Measures[0].Window.ResetAt = nil
	body.Items[0].Observation.Measures[0].Window.ResetPrecision = providerallowance.ResetDisplayOnly
	body.Items[0].Observation.Measures[0].Window.ResetDisplayText = sp("Feb 29")
	readRaw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	readDecoded, err := providerallowance.DecodeReadResponse(readRaw, providerallowance.FixtureParseLimits)
	if err != nil {
		t.Fatal(err)
	}
	readWindow := readDecoded.Items[0].Observation.Measures[0].Window
	readWindowRaw, err := json.Marshal(readWindow)
	if err != nil || readWindow.ResetDisplayText == nil || *readWindow.ResetDisplayText != "Feb 29" || readWindow.ResetAt != nil || readWindow.ResetDisplayDate != nil || bytes.Contains(readWindowRaw, []byte("2024-02-29")) || bytes.Contains(readWindowRaw, []byte("T00:00:00")) {
		t.Fatalf("read window = %v %#v %s", err, readWindow, readWindowRaw)
	}
	leap := decodeWindow(t, windowWire(t, `"resetDisplayDate":"2024-02-29"`))
	if leap.ResetDisplayDate == nil || *leap.ResetDisplayDate != "2024-02-29" || leap.ResetAt != nil {
		t.Fatalf("leap date = %#v", leap)
	}
	leapRaw, err := json.Marshal(leap)
	if err != nil || bytes.Contains(leapRaw, []byte("T00:00:00")) || bytes.Contains(leapRaw, []byte("2024-02-29T")) {
		t.Fatalf("leap date expanded: %v %s", err, leapRaw)
	}
}

func TestResetDisplayRejectsInvalidWindows(t *testing.T) {
	for _, extra := range []string{
		`"resetDisplayDate":"2026-12-01"`,
		`"resetDisplayDate":"2024-02-29"`,
		`"resetDisplayText":"Feb 29"`,
		`"resetDisplayText":"March 7"`,
		`"resetDisplayText":"Nov 4"`,
		`"resetDisplayDate":null`,
		`"resetDisplayText":null`,
		`"resetDisplayDate":null,"resetDisplayText":null`,
	} {
		if _, err := decodeWindowErr(windowWire(t, extra)); err != nil {
			t.Fatalf("accepted form %s: %v", extra, err)
		}
	}
	legacy, err := decodeWindowErr([]byte(`{"ref":"w","durationMinutes":60,"startAt":null,"resetAt":"2026-11-02T14:00:00Z","resetPrecision":"instant"}`))
	if err != nil || legacy.ResetAt == nil || legacy.ResetDisplayDate != nil || legacy.ResetDisplayText != nil {
		t.Fatalf("legacy instant = %v %#v", err, legacy)
	}
	displayClock, err := decodeWindowErr([]byte(`{"ref":"w","durationMinutes":null,"startAt":null,"resetAt":"2026-11-02T14:00:00Z","resetPrecision":"display_only"}`))
	if err != nil || displayClock.ResetDisplayDate != nil || displayClock.ResetDisplayText != nil {
		t.Fatalf("legacy display clock = %v %#v", err, displayClock)
	}
	for _, extra := range []string{
		`"resetDisplayDate":""`,
		`"resetDisplayDate":"2026-02-31"`,
		`"resetDisplayDate":"2026-02-29"`,
		`"resetDisplayDate":"2026-11-31"`,
		`"resetDisplayDate":"2026-11-04T00:00:00Z"`,
		`"resetDisplayDate":"2026-11-4"`,
		`"resetDisplayDate":" 2026-12-01"`,
		`"resetDisplayText":""`,
		`"resetDisplayText":"Feb 30"`,
		`"resetDisplayText":"Nov 31"`,
		`"resetDisplayText":"Mar 07"`,
		`"resetDisplayText":"March 7th"`,
		`"resetDisplayText":"Nov 4, 2026"`,
		`"resetDisplayText":"` + strings.Repeat("a", 33) + `"`,
		`"resetDisplayText":"` + strings.Repeat("b", 300) + `"`,
	} {
		_, err := decodeWindowErr(windowWire(t, extra))
		if err == nil || !strings.Contains(err.Error(), "invalid_time") {
			t.Fatalf("form %s -> %v", extra, err)
		}
	}
	for _, raw := range []string{
		`{"ref":"w","durationMinutes":null,"startAt":null,"resetAt":null,"resetPrecision":"display_only","resetDisplayDate":"2026-12-01","resetDisplayText":"Mar 7"}`,
		`{"ref":"w","durationMinutes":null,"startAt":null,"resetAt":"2026-11-02T14:00:00Z","resetPrecision":"display_only","resetDisplayText":"Nov 4"}`,
		`{"ref":"w","durationMinutes":null,"startAt":null,"resetAt":null,"resetPrecision":"instant","resetDisplayDate":"2026-12-01"}`,
		`{"ref":"w","durationMinutes":null,"startAt":null,"resetAt":"2026-11-02T14:00:00Z","resetPrecision":"instant","resetDisplayDate":"2026-12-01"}`,
		`{"ref":"w","durationMinutes":null,"startAt":null,"resetAt":null,"resetPrecision":"unknown","resetDisplayText":"Feb 29"}`,
	} {
		_, err := decodeWindowErr([]byte(raw))
		if err == nil || !strings.Contains(err.Error(), "invalid_type") {
			t.Fatalf("constraint %s -> %v", raw, err)
		}
	}
	obs := byID(t, "synthetic-multiple-windows").Observation
	obs.Measures = append([]providerallowance.Measure(nil), obs.Measures...)
	obs.Measures[0].Window.ResetDisplayText = sp("Feb 30")
	bad := providerallowance.ValidateObservation(obs, trustLike(obs, providerallowance.BindingVerified), reviewedRegistry())
	if bad.ShapeOK || !hasCode(bad.Issues, providerallowance.IssueInvalidTime) {
		t.Fatalf("in-memory text = %#v", bad.Issues)
	}
	obs.Measures[0].Window.ResetAt = nil
	obs.Measures[0].Window.ResetPrecision = providerallowance.ResetDisplayOnly
	obs.Measures[0].Window.ResetDisplayText = sp("Feb 29")
	okShape := providerallowance.ValidateObservation(obs, trustLike(obs, providerallowance.BindingVerified), reviewedRegistry())
	if !okShape.ShapeOK || okShape.ProviderTimeKnown != (obs.ProviderObservedAt != nil) {
		t.Fatalf("yearless shape = %#v", okShape.Issues)
	}
}

func TestResetDisplaySchemasMatchTypedRules(t *testing.T) {
	observation := compileSchema(t, "observation.schema.json", "urn:dev-env:providerallowance:observation:reset-display")
	readSchema := compileSchema(t, "read-response.schema.json", "urn:dev-env:providerallowance:read:reset-display")
	raw := byID(t, "synthetic-multiple-windows").Raw
	doc, _, _ := loadRead(t)
	acceptObs := func(raw []byte) {
		t.Helper()
		if err := observation.Validate(instance(t, raw)); err != nil {
			t.Fatal(err)
		}
	}
	rejectObs := func(raw []byte) {
		t.Helper()
		if err := observation.Validate(instance(t, raw)); err == nil {
			t.Fatal("schema accepted a reset display document")
		}
	}
	setWindow := func(raw []byte, edit func(map[string]any)) []byte {
		t.Helper()
		return editJSON(t, raw, func(doc map[string]any) {
			edit(doc["measures"].([]any)[0].(map[string]any)["window"].(map[string]any))
		})
	}
	acceptObs(setWindow(raw, func(window map[string]any) {
		window["resetAt"] = nil
		window["resetPrecision"] = "display_only"
		window["resetDisplayDate"] = "2026-12-01"
	}))
	acceptObs(setWindow(raw, func(window map[string]any) {
		window["resetAt"] = nil
		window["resetPrecision"] = "display_only"
		window["resetDisplayText"] = "Feb 29"
	}))
	acceptObs(setWindow(raw, func(window map[string]any) {
		window["resetDisplayDate"] = nil
		window["resetDisplayText"] = nil
	}))
	for _, bad := range []struct {
		date, text, precision, reset any
	}{
		{"2026-02-31", nil, "display_only", nil},
		{"2026-02-29", nil, "display_only", nil},
		{"2026-11-04T00:00:00Z", nil, "display_only", nil},
		{nil, "Feb 30", "display_only", nil},
		{nil, "", "display_only", nil},
		{nil, strings.Repeat("a", 33), "display_only", nil},
		{"2026-12-01", "Mar 7", "display_only", nil},
		{"2026-12-01", nil, "instant", nil},
		{nil, "Nov 4", "display_only", "2026-11-02T14:00:00Z"},
	} {
		rejectObs(setWindow(raw, func(window map[string]any) {
			window["resetPrecision"] = bad.precision
			window["resetAt"] = bad.reset
			if bad.date != nil {
				window["resetDisplayDate"] = bad.date
			}
			if bad.text != nil {
				window["resetDisplayText"] = bad.text
			}
		}))
	}
	rejectObs(setWindow(raw, func(window map[string]any) {
		window["resetDisplayLabel"] = "Nov 4"
	}))
	readRaw := editJSON(t, doc.HTTP.Body, func(root map[string]any) {
		window := root["items"].([]any)[0].(map[string]any)["observation"].(map[string]any)["measures"].([]any)[0].(map[string]any)["window"].(map[string]any)
		window["resetAt"] = nil
		window["resetPrecision"] = "display_only"
		window["resetDisplayText"] = "Feb 29"
	})
	if err := readSchema.Validate(instance(t, readRaw)); err != nil {
		t.Fatal(err)
	}
	badRead := editJSON(t, doc.HTTP.Body, func(root map[string]any) {
		window := root["items"].([]any)[0].(map[string]any)["observation"].(map[string]any)["measures"].([]any)[0].(map[string]any)["window"].(map[string]any)
		window["resetAt"] = nil
		window["resetPrecision"] = "display_only"
		window["resetDisplayDate"] = "2026-12-01"
		window["resetDisplayText"] = "Mar 7"
	})
	if err := readSchema.Validate(instance(t, badRead)); err == nil {
		t.Fatal("read schema accepted both display fields")
	}
}

func windowWire(t *testing.T, extra string) []byte {
	t.Helper()
	body := `{"ref":"w","durationMinutes":null,"startAt":null,"resetAt":null,"resetPrecision":"display_only"`
	if extra != "" {
		body += "," + extra
	}
	return []byte(body + "}")
}

func decodeWindow(t *testing.T, raw []byte) providerallowance.Window {
	t.Helper()
	window, err := decodeWindowErr(raw)
	if err != nil {
		t.Fatal(err)
	}
	return window
}

func decodeWindowErr(raw []byte) (providerallowance.Window, error) {
	var window providerallowance.Window
	err := json.Unmarshal(raw, &window)
	return window, err
}
