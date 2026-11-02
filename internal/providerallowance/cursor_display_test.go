package providerallowance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCursorDisplayProjectsFractionalQuotaWithoutResetInstant(t *testing.T) {
	now := cursorDisplayNow()
	raw := cursorDisplayDoc(t, nil)
	item, err := CursorDisplayProject(raw, cursorDisplaySelection(), now)
	if err != nil {
		t.Fatal(err)
	}
	obs := item.Observation
	if obs.Provider != "cursor" || obs.Source.Kind != SourceNativeDisplay || obs.Source.Version != CursorDisplaySourceVersion || obs.Source.CapabilityRef != CursorDisplayCapability || obs.Sequence != 1 || obs.ProducerIncarnation != "fixture-incarnation-1" {
		t.Fatalf("source = %#v sequence=%d incarnation=%s", obs.Source, obs.Sequence, obs.ProducerIncarnation)
	}
	if obs.ProviderObservedAt != nil || obs.Scope.BindingState != BindingUnknown || obs.Scope.AccountRef != nil || obs.Scope.EntitlementRef != nil || len(obs.Scope.Products) != 1 || obs.Scope.Products[0] != "grok" {
		t.Fatalf("scope = %#v providerTime=%v", obs.Scope, obs.ProviderObservedAt)
	}
	if obs.CapturedAt != "2026-11-02T03:07:00Z" || item.Freshness.State != FreshUnknown || item.Freshness.Basis != BasisCapture || item.Validation.State != ValidationDisplayOnly {
		t.Fatalf("provenance freshness=%#v validation=%s captured=%s", item.Freshness, item.Validation.State, obs.CapturedAt)
	}
	if len(obs.Measures) != 1 || obs.Measures[0].Value.Decimal == nil || *obs.Measures[0].Value.Decimal != "12.5" || obs.Measures[0].Window.DurationMinutes != nil || obs.Measures[0].Window.ResetAt != nil || obs.Measures[0].Window.ResetPrecision != ResetDisplayOnly {
		t.Fatalf("measure = %#v", obs.Measures[0])
	}
	if len(obs.Derivations) != 1 || obs.Derivations[0].Formula != FormulaComplement || obs.Derivations[0].Value.Decimal == nil || *obs.Derivations[0].Value.Decimal != "87.5" {
		t.Fatalf("derivation = %#v", obs.Derivations)
	}
	if got := cursorDisplayLexical(obs, "resetDisplayDate"); got != "2026-12-01" || cursorDisplayLexical(obs, "windowLabel") != "weekly" || cursorDisplayLexical(obs, "plan") != "fixture-tier" {
		t.Fatalf("fields = %#v", obs.DirectFields)
	}
	if len(item.CurrentMeasures) != 1 || item.CurrentMeasures[0].State != CurrentUnknown || item.CurrentMeasures[0].Value.Decimal != nil {
		t.Fatalf("current = %#v", item.CurrentMeasures)
	}
	encoded, err := json.Marshal(item)
	if err != nil || bytes.Contains(encoded, []byte("2026-12-01T")) || bytes.Contains(encoded, []byte("T00:00:00")) {
		t.Fatalf("reset instant invented: %v %s", err, encoded)
	}
	scaled := cursorDisplayDoc(t, map[string]any{"usedPercent": "12.50"})
	scaleItem, err := CursorDisplayProject(scaled, cursorDisplaySelection(), now)
	if err != nil || scaleItem.Observation.Derivations[0].Value.Decimal == nil || *scaleItem.Observation.Derivations[0].Value.Decimal != "87.50" || *scaleItem.Observation.Measures[0].Value.Decimal != "12.50" {
		t.Fatal(err)
	}
	offset := cursorDisplayDoc(t, map[string]any{"capturedAt": "2026-11-02T00:07:00-03:00"})
	offsetItem, err := CursorDisplayProject(offset, cursorDisplaySelection(), now)
	if err != nil || offsetItem.Observation.CapturedAt != "2026-11-02T00:07:00-03:00" || offsetItem.Observation.ProviderObservedAt != nil || offsetItem.Freshness.State != FreshUnknown {
		t.Fatal(err)
	}
	later, err := CursorDisplayProject(raw, cursorDisplaySelection(), now.Add(time.Minute))
	if err != nil || later.Observation.ObservationID != item.Observation.ObservationID || later.Observation.Sequence != item.Observation.Sequence || later.Observation.ProducerIncarnation != item.Observation.ProducerIncarnation || later.Freshness.State != FreshUnknown || later.Observation.ProviderObservedAt != nil || CursorDisplayProviderRefresh(item, later) {
		t.Fatal(err)
	}
}

func TestCursorDisplayKeepsZeroAndOmitsMissing(t *testing.T) {
	now := cursorDisplayNow()
	zero, err := CursorDisplayProject(cursorDisplayDoc(t, map[string]any{"usedPercent": "0"}), cursorDisplaySelection(), now)
	if err != nil || zero.Observation.Measures[0].Value.State != ValueKnown || *zero.Observation.Measures[0].Value.Decimal != "0" || *zero.Observation.Derivations[0].Value.Decimal != "100" {
		t.Fatalf("zero = %v %#v", err, zero.Observation.Measures)
	}
	missing, err := CursorDisplayProject(cursorDisplayDoc(t, map[string]any{"usedPercent": nil}), cursorDisplaySelection(), now)
	if err != nil || missing.Observation.Measures[0].Value.State != ValueUnknown || missing.Observation.Measures[0].Value.Decimal != nil || len(missing.Observation.Derivations) != 0 {
		t.Fatalf("missing = %v %#v", err, missing.Observation)
	}
	unknown, err := CursorDisplayProject(cursorDisplayDoc(t, map[string]any{"usedPercent": nil, "errorCode": "display_unknown"}), cursorDisplaySelection(), now)
	if err != nil || unknown.Observation.Measures[0].Value.Decimal != nil || unknown.Observation.Availability != AvailabilityPartial {
		t.Fatal(err)
	}
	unavailable, err := CursorDisplayProject(cursorDisplayDoc(t, map[string]any{
		"usedPercent": nil, "availability": "unavailable", "errorCode": "display_unavailable",
	}), cursorDisplaySelection(), now)
	if err != nil || unavailable.Observation.Availability != AvailabilityUnavailable || unavailable.Observation.Measures[0].Value.State != ValueUnknown || unavailable.Observation.Measures[0].Value.Decimal != nil {
		t.Fatalf("unavailable = %v %#v", err, unavailable.Observation)
	}
	conflict, err := CursorDisplayProject(cursorDisplayDoc(t, map[string]any{
		"usedPercent": nil, "availability": "conflict",
	}), cursorDisplaySelection(), now)
	if err != nil || conflict.Observation.Availability != AvailabilityConflict || conflict.Observation.Measures[0].Value.Decimal != nil {
		t.Fatal(err)
	}
}

func TestCursorDisplayStaleAndFutureCapture(t *testing.T) {
	now := cursorDisplayNow()
	stale, err := CursorDisplayProject(cursorDisplayDoc(t, map[string]any{"capturedAt": "2026-10-30T03:07:00Z"}), cursorDisplaySelection(), now)
	if err != nil || stale.Freshness.State != FreshStale || stale.Freshness.Basis != BasisCapture || stale.Observation.Measures[0].Value.Decimal == nil || *stale.Observation.Measures[0].Value.Decimal != "12.5" || stale.CurrentMeasures[0].State != CurrentUnknown || stale.Observation.ProviderObservedAt != nil {
		t.Fatalf("stale = %v %#v", err, stale.Freshness)
	}
	marked, err := CursorDisplayProject(cursorDisplayDoc(t, map[string]any{"errorCode": "display_stale"}), cursorDisplaySelection(), now)
	if err != nil || marked.Freshness.State != FreshStale || marked.Observation.Measures[0].Value.Decimal == nil || *marked.Observation.Measures[0].Value.Decimal != "12.5" || marked.Freshness.Basis != BasisCapture {
		t.Fatal(err)
	}
	_, err = CursorDisplayProject(cursorDisplayDoc(t, map[string]any{"capturedAt": "2026-11-02T03:07:01Z"}), cursorDisplaySelection(), now)
	if !errors.Is(err, ErrCursorDisplayFuture) || err.Error() != "cursor_display_future_capture" || strings.Contains(err.Error(), "03:07:01") {
		t.Fatal(err)
	}
}

func TestCursorDisplayMissingAccountAndSwitch(t *testing.T) {
	now := cursorDisplayNow()
	missing, err := CursorDisplayProject(cursorDisplayDoc(t, nil), cursorDisplaySelection(), now)
	if err != nil || missing.Observation.Scope.AccountRef != nil || missing.Binding.AccountRef != nil || cursorDisplayLexical(missing.Observation, "accountLabel") != "" {
		t.Fatalf("missing account = %v %#v", err, missing.Observation.Scope)
	}
	left, err := CursorDisplayProject(cursorDisplayDoc(t, map[string]any{"accountRef": "fixture-account-a"}), cursorDisplaySelection(), now)
	if err != nil {
		t.Fatal(err)
	}
	right, err := CursorDisplayProject(cursorDisplayDoc(t, map[string]any{"accountRef": "fixture-account-b"}), cursorDisplaySelection(), now)
	if err != nil {
		t.Fatal(err)
	}
	if left.Observation.Scope.AccountRef != nil || right.Observation.Scope.AccountRef != nil || left.Observation.Scope.BindingState != BindingUnknown {
		t.Fatal("unvalidated account became a binding")
	}
	if cursorDisplayLexical(left.Observation, "accountLabel") != "fixture-account-a" || cursorDisplayLexical(right.Observation, "accountLabel") != "fixture-account-b" {
		t.Fatal("account label lost")
	}
	kept, err := CursorDisplayDedupe([]ReadItem{left, right})
	if err != nil || len(kept) != 2 || kept[0].Observation.Measures[0].Value.Decimal == nil || *kept[0].Observation.Measures[0].Value.Decimal != "12.5" || *kept[1].Observation.Measures[0].Value.Decimal != "12.5" {
		t.Fatalf("switch merged = %v %d", err, len(kept))
	}
}

func TestCursorDisplayResetDateOnlyPastAndRejected(t *testing.T) {
	now := cursorDisplayNow()
	past, err := CursorDisplayProject(cursorDisplayDoc(t, map[string]any{"resetDisplayDate": "2020-01-15"}), cursorDisplaySelection(), now)
	if err != nil || past.Observation.Measures[0].Window.ResetAt != nil || past.Observation.Measures[0].Window.ResetPrecision != ResetDisplayOnly || past.CurrentMeasures[0].State != CurrentUnknown || cursorDisplayLexical(past.Observation, "resetDisplayDate") != "2020-01-15" {
		t.Fatalf("past = %v %#v", err, past.Observation.Measures)
	}
	futureDate, err := CursorDisplayProject(cursorDisplayDoc(t, map[string]any{"resetDisplayDate": "2099-01-01"}), cursorDisplaySelection(), now)
	if err != nil || futureDate.Observation.Measures[0].Window.ResetAt != nil || futureDate.CurrentMeasures[0].State == CurrentExpired {
		t.Fatal(err)
	}
	for _, bad := range []string{"Nov 4", "2026-11-31", "2026-11-04T00:00:00Z", "2026-11-4", ""} {
		_, err := CursorDisplayProject(cursorDisplayDoc(t, map[string]any{"resetDisplayDate": bad}), cursorDisplaySelection(), now)
		if err == nil || !errors.Is(err, ErrCursorDisplayReset) {
			t.Fatalf("reset %q -> %v", bad, err)
		}
		if bad != "" && strings.Contains(err.Error(), bad) {
			t.Fatalf("reset %q leaked into %v", bad, err)
		}
	}
}

func TestCursorDisplayResetTextPreservesMonthDay(t *testing.T) {
	now := cursorDisplayNow()
	item, err := CursorDisplayProject(cursorDisplayDoc(t, map[string]any{
		"resetDisplayDate": nil, "resetDisplayText": "Mar 7",
	}), cursorDisplaySelection(), now)
	if err != nil {
		t.Fatal(err)
	}
	window := item.Observation.Measures[0].Window
	if window.ResetAt != nil || window.ResetPrecision != ResetDisplayOnly || item.Observation.ProviderObservedAt != nil || item.Freshness.State != FreshUnknown || item.CurrentMeasures[0].State != CurrentUnknown {
		t.Fatalf("month day = %#v freshness=%#v", window, item.Freshness)
	}
	if cursorDisplayLexical(item.Observation, "resetDisplayText") != "Mar 7" || cursorDisplayLexical(item.Observation, "resetDisplayDate") != "" {
		t.Fatalf("label = %#v", item.Observation.DirectFields)
	}
	encoded, err := json.Marshal(item)
	if err != nil || bytes.Contains(encoded, []byte("2026-03-07")) || bytes.Contains(encoded, []byte("T00:00:00")) || bytes.Contains(encoded, []byte("March 7")) || !bytes.Contains(encoded, []byte("Mar 7")) {
		t.Fatalf("month day rewritten: %v %s", err, encoded)
	}
	sample, err := CursorDisplayProject(cursorDisplayDoc(t, map[string]any{
		"resetDisplayDate": nil, "resetDisplayText": "Nov 4",
	}), cursorDisplaySelection(), now)
	if err != nil || sample.Observation.Measures[0].Window.ResetAt != nil || sample.Observation.ProviderObservedAt != nil || cursorDisplayLexical(sample.Observation, "resetDisplayText") != "Nov 4" {
		t.Fatalf("visible label = %v %#v", err, sample.Observation.DirectFields)
	}
	full, err := CursorDisplayProject(cursorDisplayDoc(t, map[string]any{
		"resetDisplayDate": nil, "resetDisplayText": "March 7",
	}), cursorDisplaySelection(), now)
	if err != nil || cursorDisplayLexical(full.Observation, "resetDisplayText") != "March 7" || full.Observation.Measures[0].Window.ResetAt != nil {
		t.Fatal(err)
	}
	leap, err := CursorDisplayProject(cursorDisplayDoc(t, map[string]any{
		"resetDisplayDate": nil, "resetDisplayText": "Feb 29",
	}), cursorDisplaySelection(), now)
	if err != nil || cursorDisplayLexical(leap.Observation, "resetDisplayText") != "Feb 29" || leap.Observation.ProviderObservedAt != nil {
		t.Fatal(err)
	}
	leapEncoded, err := json.Marshal(leap)
	if err != nil || bytes.Contains(leapEncoded, []byte("2024-02-29")) || bytes.Contains(leapEncoded, []byte("2028-02-29")) || bytes.Contains(leapEncoded, []byte("T00:00:00")) {
		t.Fatalf("leap day became a year: %v %s", err, leapEncoded)
	}
	dateOnly, err := CursorDisplayProject(cursorDisplayDoc(t, nil), cursorDisplaySelection(), now)
	if err != nil || cursorDisplayLexical(dateOnly.Observation, "resetDisplayDate") != "2026-12-01" || cursorDisplayLexical(dateOnly.Observation, "resetDisplayText") != "" || dateOnly.Observation.Measures[0].Window.ResetAt != nil || dateOnly.Observation.Measures[0].Window.ResetPrecision != ResetDisplayOnly {
		t.Fatal(err)
	}
	_, err = CursorDisplayProject(cursorDisplayDoc(t, map[string]any{
		"resetDisplayDate": "2026-12-01", "resetDisplayText": "Mar 7",
	}), cursorDisplaySelection(), now)
	if err == nil || !errors.Is(err, ErrCursorDisplayReset) || strings.Contains(err.Error(), "Mar 7") || strings.Contains(err.Error(), "2026-12-01") {
		t.Fatalf("both labels -> %v", err)
	}
	for _, bad := range []string{"Feb 30", "Nov 31", "Foo 1", "Mar 0", "Mar 32", "Mar 07", "2026-03-07", "March 7th", "Nov 4, 2026", "", "Mar\t7"} {
		_, err := CursorDisplayProject(cursorDisplayDoc(t, map[string]any{
			"resetDisplayDate": nil, "resetDisplayText": bad,
		}), cursorDisplaySelection(), now)
		if err == nil || !errors.Is(err, ErrCursorDisplayReset) {
			t.Fatalf("text %q -> %v", bad, err)
		}
		if bad != "" && strings.Contains(err.Error(), bad) {
			t.Fatalf("text %q leaked into %v", bad, err)
		}
	}
	for _, bad := range []string{"Mar 7 secret", "SECRET-CANARY"} {
		_, err := CursorDisplayProject(cursorDisplayDoc(t, map[string]any{
			"resetDisplayDate": nil, "resetDisplayText": bad,
		}), cursorDisplaySelection(), now)
		if err == nil || !errors.Is(err, ErrCursorDisplayCredential) || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "CANARY") || strings.Contains(err.Error(), "Mar 7") {
			t.Fatalf("secret text %q -> %v", bad, err)
		}
	}
	_, err = CursorDisplayProject(cursorDisplayDoc(t, map[string]any{
		"route": "xai_direct", "product": "grok", "plan": nil, "accountRef": nil,
		"availability": "unsupported", "errorCode": nil, "windowLabel": nil,
		"durationMinutes": nil, "usedPercent": nil, "resetDisplayDate": nil,
		"resetDisplayText": "Mar 7", "spendAmount": nil, "spendCurrency": nil,
	}), cursorDisplaySelection(), now)
	if err == nil || strings.Contains(err.Error(), "Mar 7") {
		t.Fatalf("xAI label -> %v", err)
	}
	trust := cursorDisplayTrust(cursorDisplaySelection(), "cursor")
	allowed := ValidateObservation(item.Observation, trust, cursorDisplayRegistry(true))
	if !allowed.ShapeOK || allowed.ProviderTimeKnown || allowed.CompleteAllowance || len(allowed.Issues) != 0 || allowed.Measures[0].Actionable {
		t.Fatalf("text validation = %#v", allowed)
	}
	dateOnlyReg := NewStaticRegistry(RegistryParts{Sources: []SourceRule{{
		Provider: "cursor", Kind: SourceNativeDisplay, Version: CursorDisplaySourceVersion,
		CapabilityRef: CursorDisplayCapability, AllowNormalizedMeasures: true, AllowDerivations: true,
		DirectFields: map[string]DirectFieldRule{
			"plan":             {OriginalType: TypeString, Class: ClassDisplay},
			"windowLabel":      {OriginalType: TypeString, Class: ClassDisplay},
			"resetDisplayDate": {OriginalType: TypeString, Class: ClassDisplay},
			"accountLabel":     {OriginalType: TypeString, Class: ClassDisplay},
		},
	}}})
	denied := ValidateObservation(item.Observation, trust, dateOnlyReg)
	if denied.Measures[0].Actionable || !cursorDisplayHasCode(denied.Issues, IssueFieldNotAllowlisted) {
		t.Fatalf("date-only allowlist accepted exact text: %#v", denied)
	}
}

func TestCursorDisplayTypedResetRoundTripsWithoutInstant(t *testing.T) {
	now := cursorDisplayNow()
	plain, err := CursorDisplayProject(cursorDisplayDoc(t, map[string]any{
		"resetDisplayDate": nil, "resetDisplayText": nil, "usedPercent": nil,
	}), cursorDisplaySelection(), now)
	if err != nil {
		t.Fatal(err)
	}
	plainRaw, err := json.Marshal(plain.Observation)
	if err != nil || plain.Observation.Measures[0].Window.ResetDisplayDate != nil || plain.Observation.Measures[0].Window.ResetDisplayText != nil || bytes.Contains(plainRaw, []byte("resetDisplayDate")) || bytes.Contains(plainRaw, []byte("resetDisplayText")) {
		t.Fatalf("absent labels were serialized: %v %s", err, plainRaw)
	}
	dateItem, err := CursorDisplayProject(cursorDisplayDoc(t, nil), cursorDisplaySelection(), now)
	if err != nil {
		t.Fatal(err)
	}
	dateWindow := dateItem.Observation.Measures[0].Window
	if dateWindow.ResetDisplayDate == nil || *dateWindow.ResetDisplayDate != "2026-12-01" || dateWindow.ResetDisplayText != nil || dateWindow.ResetAt != nil || dateWindow.ResetPrecision != ResetDisplayOnly || dateItem.Observation.ProviderObservedAt != nil {
		t.Fatalf("date window = %#v provider=%v", dateWindow, dateItem.Observation.ProviderObservedAt)
	}
	dateRaw, err := json.Marshal(dateItem.Observation)
	if err != nil || bytes.Contains(dateRaw, []byte("2026-12-01T")) || bytes.Contains(dateRaw, []byte("T00:00:00")) {
		t.Fatalf("date became an instant: %v %s", err, dateRaw)
	}
	dateDecoded, err := DecodeObservation(dateRaw, FixtureParseLimits)
	if err != nil || dateDecoded.Measures[0].Window.ResetDisplayDate == nil || *dateDecoded.Measures[0].Window.ResetDisplayDate != "2026-12-01" || dateDecoded.Measures[0].Window.ResetAt != nil || dateDecoded.ProviderObservedAt != nil {
		t.Fatalf("observation decode = %v %#v", err, dateDecoded.Measures)
	}
	dateSnap, err := CursorDisplaySnapshot([]ReadItem{dateItem}, now)
	if err != nil {
		t.Fatal(err)
	}
	dateSnapRaw, err := json.Marshal(dateSnap)
	if err != nil {
		t.Fatal(err)
	}
	dateRead, err := DecodeReadResponse(dateSnapRaw, FixtureParseLimits)
	if err != nil || dateRead.Items[0].Observation.Measures[0].Window.ResetDisplayDate == nil || *dateRead.Items[0].Observation.Measures[0].Window.ResetDisplayDate != "2026-12-01" || dateRead.Items[0].Observation.Measures[0].Window.ResetAt != nil || dateRead.Items[0].Observation.ProviderObservedAt != nil || bytes.Contains(dateSnapRaw, []byte("2026-12-01T")) || bytes.Contains(dateSnapRaw, []byte("T00:00:00")) {
		t.Fatalf("read decode = %v", err)
	}
	dateCollected, dateCollectedRaw := cursorDisplayCollect(t, [][]byte{cursorDisplayObservationBytes(t, dateItem.Observation)})
	dateKept := cursorDisplayCursorItem(t, dateCollected)
	if dateKept.Observation.Measures[0].Window.ResetDisplayDate == nil || *dateKept.Observation.Measures[0].Window.ResetDisplayDate != "2026-12-01" || dateKept.Observation.Measures[0].Window.ResetAt != nil || dateKept.Observation.ProviderObservedAt != nil || dateKept.CurrentMeasures[0].State != CurrentUnknown || bytes.Contains(dateCollectedRaw, []byte("2026-12-01T")) || bytes.Contains(dateCollectedRaw, []byte("T00:00:00")) {
		t.Fatalf("collector snapshot = %#v", dateKept.Observation.Measures[0].Window)
	}
	textItem, err := CursorDisplayProject(cursorDisplayDoc(t, map[string]any{
		"resetDisplayDate": nil, "resetDisplayText": "Feb 29",
	}), cursorDisplaySelection(), now)
	if err != nil {
		t.Fatal(err)
	}
	textWindow := textItem.Observation.Measures[0].Window
	if textWindow.ResetDisplayText == nil || *textWindow.ResetDisplayText != "Feb 29" || textWindow.ResetDisplayDate != nil || textWindow.ResetAt != nil || textWindow.ResetPrecision != ResetDisplayOnly || textItem.Observation.ProviderObservedAt != nil {
		t.Fatalf("text window = %#v", textWindow)
	}
	textRaw, err := json.Marshal(textItem)
	if err != nil || bytes.Contains(textRaw, []byte("2024-02-29")) || bytes.Contains(textRaw, []byte("2028-02-29")) || bytes.Contains(textRaw, []byte("T00:00:00")) || !bytes.Contains(textRaw, []byte("Feb 29")) {
		t.Fatalf("yearless label changed: %v %s", err, textRaw)
	}
	textSnap, err := CursorDisplaySnapshot([]ReadItem{textItem}, now)
	if err != nil {
		t.Fatal(err)
	}
	textSnapRaw, err := json.Marshal(textSnap)
	textRead, errRead := DecodeReadResponse(textSnapRaw, FixtureParseLimits)
	if err != nil || errRead != nil || textRead.Items[0].Observation.Measures[0].Window.ResetDisplayText == nil || *textRead.Items[0].Observation.Measures[0].Window.ResetDisplayText != "Feb 29" || textRead.Items[0].Observation.ProviderObservedAt != nil || bytes.Contains(textSnapRaw, []byte("2024-02-29")) || bytes.Contains(textSnapRaw, []byte("T00:00:00")) {
		t.Fatalf("yearless read = %v %v", err, errRead)
	}
	textCollected, textCollectedRaw := cursorDisplayCollect(t, [][]byte{cursorDisplayObservationBytes(t, textItem.Observation)})
	textKept := cursorDisplayCursorItem(t, textCollected)
	if textKept.Observation.Measures[0].Window.ResetDisplayText == nil || *textKept.Observation.Measures[0].Window.ResetDisplayText != "Feb 29" || textKept.Observation.Measures[0].Window.ResetAt != nil || textKept.CurrentMeasures[0].State != CurrentUnknown || bytes.Contains(textCollectedRaw, []byte("2024-02-29")) || bytes.Contains(textCollectedRaw, []byte("2028-02-29")) {
		t.Fatalf("yearless collector = %#v", textKept.Observation.Measures[0].Window)
	}
	verifiedDate, err := CursorDisplayProject(cursorDisplayDoc(t, map[string]any{"accountRef": "fixture-account"}), cursorDisplayVerified("fixture-account"), now)
	if err != nil {
		t.Fatal(err)
	}
	verifiedText, err := CursorDisplayProject(cursorDisplayDoc(t, map[string]any{
		"accountRef": "fixture-account", "product": "cursor", "resetDisplayDate": nil, "resetDisplayText": "Feb 29",
	}), cursorDisplayVerified("fixture-account"), now)
	if err != nil {
		t.Fatal(err)
	}
	pool := CursorDisplayPool(
		[]Observation{verifiedDate.Observation, verifiedText.Observation},
		[]TrustedContext{cursorDisplayTrust(cursorDisplayVerified("fixture-account"), "cursor"), cursorDisplayTrust(cursorDisplayVerified("fixture-account"), "cursor")},
		cursorDisplayPoolRegistry(),
	)
	if pool.Resolved || pool.Count != 0 || pool.EntitlementRef != "" || verifiedDate.Observation.ProviderObservedAt != nil || verifiedText.Observation.ProviderObservedAt != nil {
		t.Fatalf("display label resolved a pool: %#v", pool)
	}
	kept, err := CursorDisplayDedupe([]ReadItem{verifiedDate, verifiedText})
	if err != nil || len(kept) != 2 || kept[0].Observation.Scope.AccountRef == nil || *kept[0].Observation.Scope.AccountRef != "fixture-account" || *kept[1].Observation.Scope.AccountRef != "fixture-account" {
		t.Fatalf("accounts merged = %v %d", err, len(kept))
	}
	conflict, err := CursorDisplayProject(cursorDisplayDoc(t, map[string]any{
		"usedPercent": nil, "availability": "conflict", "resetDisplayDate": nil, "resetDisplayText": "Nov 4",
	}), cursorDisplaySelection(), now)
	if err != nil || conflict.Observation.Availability != AvailabilityConflict || conflict.Observation.Measures[0].Value.Decimal != nil || conflict.Observation.Measures[0].Window.ResetDisplayText == nil || *conflict.Observation.Measures[0].Window.ResetDisplayText != "Nov 4" || conflict.Observation.Measures[0].Window.ResetAt != nil || conflict.CurrentMeasures[0].State != CurrentUnknown || conflict.Observation.ProviderObservedAt != nil {
		t.Fatalf("conflict = %v %#v", err, conflict.Observation)
	}
	unknown, err := CursorDisplayProject(cursorDisplayDoc(t, map[string]any{
		"usedPercent": nil, "errorCode": "display_unknown", "resetDisplayDate": nil, "resetDisplayText": "Nov 4",
	}), cursorDisplaySelection(), now)
	if err != nil || unknown.Observation.Availability != AvailabilityPartial || unknown.Observation.Measures[0].Value.Decimal != nil || unknown.CurrentMeasures[0].State != CurrentUnknown || unknown.Observation.Scope.BindingState != BindingUnknown || unknown.Observation.Scope.AccountRef != nil {
		t.Fatalf("unknown = %v %#v", err, unknown.Observation)
	}
	trust := cursorDisplayTrust(cursorDisplayVerified("fixture-account"), "cursor")
	assessed := ValidateObservation(verifiedText.Observation, trust, cursorDisplayRegistry(true))
	if !assessed.ShapeOK || assessed.ProviderTimeKnown || assessed.CompleteAllowance || assessed.Measures[0].Actionable || assessed.ServedModel != ServedModelUnknown {
		t.Fatalf("label became authority: %#v", assessed)
	}
}

func TestCursorDisplaySpendStaysSeparateFromQuota(t *testing.T) {
	now := cursorDisplayNow()
	item, err := CursorDisplayProject(cursorDisplayDoc(t, map[string]any{
		"spendAmount": "0.00", "spendCurrency": "USD", "durationMinutes": 10080,
	}), cursorDisplaySelection(), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(item.Observation.Measures) != 2 || len(item.Observation.Derivations) != 1 {
		t.Fatalf("counts measures=%d derivations=%d", len(item.Observation.Measures), len(item.Observation.Derivations))
	}
	quota := item.Observation.Measures[0]
	spend := item.Observation.Measures[1]
	if quota.Category != CategoryIncluded || quota.Unit != UnitPercent || quota.Window.Ref != CursorDisplayIncludedWindow || quota.Window.DurationMinutes == nil || *quota.Window.DurationMinutes != 10080 || *quota.Value.Decimal != "12.5" {
		t.Fatalf("quota = %#v", quota)
	}
	if spend.Category != CategorySpend || spend.Unit != UnitMoney || spend.Currency == nil || *spend.Currency != "USD" || spend.Window.Ref != CursorDisplaySpendWindow || *spend.Value.Decimal != "0.00" || spend.Window.ResetAt != nil {
		t.Fatalf("spend = %#v", spend)
	}
	if *item.Observation.Derivations[0].Value.Decimal != "87.5" {
		t.Fatal("spend replaced the percent complement")
	}
	omitted, err := CursorDisplayProject(cursorDisplayDoc(t, nil), cursorDisplaySelection(), now)
	if err != nil || len(omitted.Observation.Measures) != 1 || omitted.Observation.Measures[0].Window.DurationMinutes != nil {
		t.Fatal("weekly label invented a duration or a zero spend")
	}
	_, err = CursorDisplayProject(cursorDisplayDoc(t, map[string]any{"spendAmount": "0", "spendCurrency": nil}), cursorDisplaySelection(), now)
	if !errors.Is(err, ErrCursorDisplaySpend) {
		t.Fatal(err)
	}
}

func TestCursorDisplayDedupesOneCaptureAndDoesNotMergePools(t *testing.T) {
	now := cursorDisplayNow()
	raw := cursorDisplayDoc(t, map[string]any{"accountRef": "fixture-account"})
	first, err := CursorDisplayProject(raw, cursorDisplayVerified("fixture-account"), now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := CursorDisplayProject(raw, cursorDisplayVerified("fixture-account"), now.Add(time.Minute))
	if err != nil || first.Observation.ObservationID != second.Observation.ObservationID || first.Observation.Sequence != second.Observation.Sequence || first.Observation.Sequence != 1 || first.Observation.ProducerIncarnation != second.Observation.ProducerIncarnation || CursorDisplayProviderRefresh(first, second) {
		t.Fatal(err)
	}
	kept, err := CursorDisplayDedupe([]ReadItem{first, second})
	if err != nil || len(kept) != 1 || kept[0].ReceivedAt != first.ReceivedAt {
		t.Fatalf("replay = %v %d", err, len(kept))
	}
	otherTime, err := CursorDisplayProject(cursorDisplayDoc(t, map[string]any{
		"accountRef": "fixture-account", "capturedAt": "2026-11-02T03:06:00Z",
	}), cursorDisplayVerified("fixture-account"), now)
	if err != nil || otherTime.Observation.ObservationID == first.Observation.ObservationID || CursorDisplayProviderRefresh(first, otherTime) {
		t.Fatal("a later screen read became one capture or a provider refresh")
	}
	cursorProduct, err := CursorDisplayProject(cursorDisplayDoc(t, map[string]any{
		"accountRef": "fixture-account", "product": "cursor",
	}), cursorDisplayVerified("fixture-account"), now)
	if err != nil {
		t.Fatal(err)
	}
	separate, err := CursorDisplayDedupe([]ReadItem{first, cursorProduct})
	if err != nil || len(separate) != 2 || *separate[0].Observation.Measures[0].Value.Decimal != "12.5" || *separate[1].Observation.Measures[0].Value.Decimal != "12.5" {
		t.Fatal("shared entitlement merged windows")
	}
	if first.Observation.ProviderObservedAt != nil || cursorProduct.Observation.Scope.EntitlementRef == nil || *cursorProduct.Observation.Scope.EntitlementRef != "fixture-entitlement" {
		t.Fatal("entitlement or provider time drifted")
	}
	unverified, err := CursorDisplayProject(cursorDisplayDoc(t, map[string]any{"product": "grok"}), cursorDisplaySelection(), now)
	if err != nil || unverified.Observation.Scope.EntitlementRef != nil {
		t.Fatal("payload product established an entitlement")
	}
	pool := CursorDisplayPool(
		[]Observation{first.Observation, cursorProduct.Observation},
		[]TrustedContext{cursorDisplayTrust(cursorDisplayVerified("fixture-account"), "cursor"), cursorDisplayTrust(cursorDisplayVerified("fixture-account"), "cursor")},
		cursorDisplayPoolRegistry(),
	)
	if pool.Resolved || pool.Count != 0 || pool.EntitlementRef != "" {
		t.Fatalf("display capture resolved a pool without provider time: %#v", pool)
	}
	readyGrok := cursorDisplayReady(t, first)
	readyCursor := cursorDisplayReady(t, cursorProduct)
	ready := CursorDisplayPool(
		[]Observation{readyGrok.Observation, readyCursor.Observation},
		[]TrustedContext{cursorDisplayTrust(cursorDisplayVerified("fixture-account"), "cursor"), cursorDisplayTrust(cursorDisplayVerified("fixture-account"), "cursor")},
		cursorDisplayPoolRegistry(),
	)
	if !ready.Resolved || ready.Count != 1 || ready.EntitlementRef != "fixture-entitlement" {
		t.Fatalf("caller-trusted pool = %#v", ready)
	}
	if first.Observation.ProviderObservedAt != nil || *readyGrok.Observation.Measures[0].Value.Decimal != "12.5" || *readyCursor.Observation.Measures[0].Value.Decimal != "12.5" {
		t.Fatal("pool count changed the adapter output or summed percents")
	}
	otherAccount, err := CursorDisplayProject(cursorDisplayDoc(t, map[string]any{
		"accountRef": "fixture-account-b", "product": "cursor",
	}), cursorDisplayVerified("fixture-account-b"), now)
	if err != nil {
		t.Fatal(err)
	}
	crossed := CursorDisplayPool(
		[]Observation{readyGrok.Observation, cursorDisplayReady(t, otherAccount).Observation},
		[]TrustedContext{cursorDisplayTrust(cursorDisplayVerified("fixture-account"), "cursor"), cursorDisplayTrust(cursorDisplayVerified("fixture-account-b"), "cursor")},
		cursorDisplayPoolRegistry(),
	)
	if crossed.Resolved || !cursorDisplayHasCode(crossed.Issues, IssueAccountMismatch) {
		t.Fatalf("accounts merged: %#v", crossed)
	}
	tampered := cursorDisplayCloneItem(t, first)
	used := "1"
	tampered.Observation.Measures[0].Value.Decimal = &used
	if _, err := CursorDisplayDedupe([]ReadItem{first, tampered}); !errors.Is(err, ErrCursorDisplayConflict) {
		t.Fatal(err)
	}
	snap, err := CursorDisplaySnapshot([]ReadItem{first, second}, now)
	if err != nil || snap.Schema != SchemaRead || len(snap.Items) != 1 || len(snap.SourceStatuses) != 1 || snap.SourceStatuses[0].Freshness.State != FreshUnknown || snap.SourceStatuses[0].Availability != AvailabilityPartial {
		t.Fatalf("snapshot = %v %#v", err, snap)
	}
	encoded, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeReadResponse(encoded, FixtureParseLimits); err != nil {
		t.Fatal(err)
	}
	_, err = CursorDisplayProject(cursorDisplayDoc(t, map[string]any{"entitlementRef": "fixture-entitlement", "products": []string{"cursor", "grok"}}), cursorDisplaySelection(), now)
	if !errors.Is(err, ErrCursorDisplayUnknown) {
		t.Fatal(err)
	}
}

func TestCursorDisplayRejectsMalformedAndCredentials(t *testing.T) {
	now := cursorDisplayNow()
	cases := []struct {
		name string
		raw  []byte
		want error
	}{
		{name: "empty", raw: nil, want: ErrCursorDisplayJSON},
		{name: "fragment", raw: []byte("{"), want: ErrCursorDisplayJSON},
		{name: "trailing", raw: []byte(`{}{}`), want: ErrCursorDisplayJSON},
		{name: "duplicate", raw: []byte(`{"schema":"cursor.native_display.sanitized.v1","schema":"cursor.native_display.sanitized.v1"}`), want: ErrCursorDisplayDuplicate},
		{name: "unknown", raw: cursorDisplayDoc(t, map[string]any{"extra": 1}), want: ErrCursorDisplayUnknown},
		{name: "folded", raw: bytes.Replace(cursorDisplayDoc(t, nil), []byte(`"plan"`), []byte(`"Plan"`), 1), want: ErrCursorDisplayUnknown},
		{name: "bool", raw: bytes.Replace(cursorDisplayDoc(t, nil), []byte(`"usedPercent":"12.5"`), []byte(`"usedPercent":true`), 1), want: ErrCursorDisplayType},
		{name: "number", raw: bytes.Replace(cursorDisplayDoc(t, nil), []byte(`"usedPercent":"12.5"`), []byte(`"usedPercent":12.5`), 1), want: ErrCursorDisplayType},
		{name: "over", raw: cursorDisplayDoc(t, map[string]any{"usedPercent": "101"}), want: ErrCursorDisplayPercent},
		{name: "negative", raw: cursorDisplayDoc(t, map[string]any{"usedPercent": "-1"}), want: ErrCursorDisplayPercent},
		{name: "cookie", raw: cursorDisplayDoc(t, map[string]any{"cookie": "SECRET-CANARY"}), want: ErrCursorDisplayCredential},
		{name: "plan-canary", raw: cursorDisplayDoc(t, map[string]any{"plan": "SECRET-CANARY"}), want: ErrCursorDisplayCredential},
		{name: "bearer", raw: cursorDisplayDoc(t, map[string]any{"plan": "Bearer-fixture"}), want: ErrCursorDisplayCredential},
		{name: "oversize", raw: bytes.Repeat([]byte("a"), cursorDisplayMaxBytes+1), want: ErrCursorDisplayLimit},
		{name: "binary", raw: []byte{0xff}, want: ErrCursorDisplayJSON},
	}
	for _, tc := range cases {
		item, err := CursorDisplayProject(tc.raw, cursorDisplaySelection(), now)
		if !errors.Is(err, tc.want) || strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "CANARY") || strings.Contains(err.Error(), "Bearer") || strings.Contains(err.Error(), "cookie") {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if item.Observation.ObservationID != "" {
			t.Fatalf("%s returned an item", tc.name)
		}
	}
	var fields map[string]any
	if err := json.Unmarshal(cursorDisplayDoc(t, nil), &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "capturedAt")
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CursorDisplayProject(raw, cursorDisplaySelection(), now); !errors.Is(err, ErrCursorDisplayMissing) {
		t.Fatal(err)
	}
}

func TestCursorDisplayDirectXAIStaysUnknown(t *testing.T) {
	now := cursorDisplayNow()
	raw := cursorDisplayDoc(t, map[string]any{
		"route": "xai_direct", "product": "grok", "plan": nil, "accountRef": nil,
		"availability": "unsupported", "errorCode": nil, "windowLabel": nil,
		"durationMinutes": nil, "usedPercent": nil, "resetDisplayDate": nil,
		"spendAmount": nil, "spendCurrency": nil,
	})
	item, err := CursorDisplayProject(raw, cursorDisplaySelection(), now)
	if err != nil {
		t.Fatal(err)
	}
	if item.Observation.Provider != "xai" || item.Observation.Source.CapabilityRef != CursorDisplayXAICapability || item.Observation.Availability != AvailabilityUnsupported || item.Observation.Scope.EntitlementRef != nil || len(item.Observation.Measures) != 0 || len(item.Observation.Derivations) != 0 || item.Observation.ProviderObservedAt != nil || item.Binding.State != BindingUnknown || item.Observation.Sequence != 1 || item.Observation.ProducerIncarnation != "fixture-incarnation-1" {
		t.Fatalf("xAI = %#v", item.Observation)
	}
	if item.Observation.ErrorCode == nil || *item.Observation.ErrorCode != "direct_xai_unknown" || item.Freshness.State != FreshUnknown || item.Freshness.Basis != BasisNone {
		t.Fatalf("xAI status = %#v", item.Freshness)
	}
	cursorItem, err := CursorDisplayProject(cursorDisplayDoc(t, nil), cursorDisplaySelection(), now)
	if err != nil {
		t.Fatal(err)
	}
	kept, err := CursorDisplayDedupe([]ReadItem{item, cursorItem})
	if err != nil || len(kept) != 2 || kept[0].Observation.Provider == kept[1].Observation.Provider {
		t.Fatal("direct xAI merged into the Cursor capture")
	}
	pool := CursorDisplayPool([]Observation{item.Observation}, []TrustedContext{cursorDisplayTrust(cursorDisplaySelection(), "xai")}, cursorDisplayPoolRegistry())
	if pool.Resolved || pool.Count != 0 {
		t.Fatalf("xAI pool = %#v", pool)
	}
	_, err = CursorDisplayProject(cursorDisplayDoc(t, map[string]any{
		"route": "xai_direct", "usedPercent": "1", "availability": "unsupported",
		"plan": nil, "windowLabel": nil, "resetDisplayDate": nil,
	}), cursorDisplaySelection(), now)
	if err == nil {
		t.Fatal("direct xAI meter accepted")
	}
	_, err = CursorDisplayProject(raw, cursorDisplayVerified("fixture-account"), now)
	if !errors.Is(err, ErrCursorDisplayBinding) {
		t.Fatal(err)
	}
}

func TestCursorDisplayAcquisitionDisabled(t *testing.T) {
	if CursorDisplayAcquisitionEnabled {
		t.Fatal("acquisition enabled")
	}
	var gate CursorDisplayAcquisition
	if reflect.TypeOf(gate).NumField() != 0 {
		t.Fatal("acquisition gained fields")
	}
	if err := gate.Run(); !errors.Is(err, ErrCursorDisplayAcquisitionDisabled) || err.Error() != "cursor_display_acquisition_disabled" {
		t.Fatal(err)
	}
}

func TestCursorDisplayValidationStaysNonActionable(t *testing.T) {
	if CursorDisplaySourceVersion == "2026.10.22-27dcf5f" {
		t.Fatal("historical display-only source reused")
	}
	if !strings.Contains(CursorDisplayResetExtension, "resetDisplayDate") || !strings.Contains(CursorDisplayResetExtension, "resetDisplayText") || !strings.Contains(CursorDisplayResetExtension, "display_only") || !strings.Contains(CursorDisplayResetExtension, "projectMeasure") || !strings.Contains(CursorDisplayResetExtension, "midnight") || !strings.Contains(CursorDisplayResetExtension, "exact") || !strings.Contains(CursorDisplayResetExtension, "does not solve the yearless sample") {
		t.Fatal("reset extension is incomplete")
	}
	if !strings.Contains(CursorDisplayRegistryExtension, "2026.10.22-27dcf5f") || !strings.Contains(CursorDisplayRegistryExtension, "AllowNormalizedMeasures") || !strings.Contains(CursorDisplayRegistryExtension, "caller-trusted") || !strings.Contains(CursorDisplayRegistryExtension, "resetDisplayText") {
		t.Fatal("registry extension is incomplete")
	}
	item, err := CursorDisplayProject(cursorDisplayDoc(t, nil), cursorDisplaySelection(), cursorDisplayNow())
	if err != nil {
		t.Fatal(err)
	}
	trust := cursorDisplayTrust(cursorDisplaySelection(), "cursor")
	allowed := ValidateObservation(item.Observation, trust, cursorDisplayRegistry(true))
	if !allowed.ShapeOK || allowed.ServedModel != ServedModelUnknown || len(allowed.Issues) != 0 || len(allowed.Measures) != 1 || !allowed.Measures[0].SemanticsEvidenced || allowed.Measures[0].Actionable || allowed.CompleteAllowance {
		t.Fatalf("allowed = %#v", allowed)
	}
	derived := Derive(item.Observation, trust, cursorDisplayRegistry(true))
	if derived.Confirmed || len(derived.Values) != 0 || !cursorDisplayHasCode(derived.Issues, IssueHistoricalNotCurrent) {
		t.Fatalf("derived = %#v", derived)
	}
	blocked := ValidateObservation(item.Observation, trust, cursorDisplayRegistry(false))
	if blocked.Measures[0].Actionable || !cursorDisplayHasCode(blocked.Issues, IssueDisplayPromotion) {
		t.Fatalf("blocked = %#v", blocked)
	}
	historical := ValidateObservation(item.Observation, trust, NewStaticRegistry(RegistryParts{
		Sources: []SourceRule{{
			Provider: "cursor", Kind: SourceNativeDisplay, Version: "2026.10.22-27dcf5f",
			CapabilityRef: "story188:cursor-display-only", AllowNormalizedMeasures: false,
		}},
	}))
	if historical.Measures[0].Actionable || !cursorDisplayHasCode(historical.Issues, IssueSourceUntrusted) {
		t.Fatalf("historical = %#v", historical)
	}
}

func TestCursorDisplayForgedBindingDoesNotCreateCallerAuthority(t *testing.T) {
	now := cursorDisplayNow()
	selection := cursorDisplaySelection()
	grok, err := CursorDisplayProject(cursorDisplayDoc(t, nil), selection, now)
	if err != nil {
		t.Fatal(err)
	}
	cursorItem, err := CursorDisplayProject(cursorDisplayDoc(t, map[string]any{"product": "cursor"}), selection, now)
	if err != nil {
		t.Fatal(err)
	}
	forgedGrok := cursorDisplayForgeVerified(t, grok)
	forgedCursor := cursorDisplayForgeVerified(t, cursorItem)
	trust := cursorDisplayTrust(selection, "cursor")
	if trust.BindingState != BindingUnknown || trust.AccountRef != nil || trust.WorkspaceRef != nil || trust.BindingGeneration != 0 || trust.Provider != "cursor" || trust.SourceKind != SourceNativeDisplay || trust.SourceVersion != CursorDisplaySourceVersion {
		t.Fatalf("caller trust = %#v", trust)
	}
	assessment := ValidateObservation(forgedGrok.Observation, trust, cursorDisplayRegistry(true))
	if assessment.Measures[0].Actionable || assessment.CompleteAllowance || !cursorDisplayHasCode(assessment.Issues, IssueBindingForged) {
		t.Fatalf("forged validation = %#v", assessment)
	}
	pool := CursorDisplayPool(
		[]Observation{forgedGrok.Observation, forgedCursor.Observation},
		[]TrustedContext{trust, cursorDisplayTrust(selection, "cursor")},
		cursorDisplayPoolRegistry(),
	)
	if pool.Resolved || pool.Count != 0 || pool.EntitlementRef != "" {
		t.Fatalf("forged pool = %#v", pool)
	}
}

func TestCursorDisplayRecurringCapturesAdmitAndReplay(t *testing.T) {
	now := cursorDisplayNow()
	firstSel := cursorDisplaySelection()
	secondSel := cursorDisplaySelection()
	secondSel.Sequence = 2
	if firstSel.ProducerIncarnation != secondSel.ProducerIncarnation || firstSel.ProducerIncarnation != "fixture-incarnation-1" {
		t.Fatal("producer incarnation changed between captures")
	}
	firstRaw := cursorDisplayDoc(t, map[string]any{"usedPercent": "12.5", "capturedAt": "2026-11-02T03:00:00Z"})
	secondRaw := cursorDisplayDoc(t, map[string]any{"usedPercent": "40", "capturedAt": "2026-11-02T03:04:00Z"})
	first, err := CursorDisplayProject(firstRaw, firstSel, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := CursorDisplayProject(secondRaw, secondSel, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if first.Observation.Sequence != 1 || second.Observation.Sequence != 2 || first.Observation.ObservationID == second.Observation.ObservationID {
		t.Fatalf("sequences=%d/%d ids=%s %s", first.Observation.Sequence, second.Observation.Sequence, first.Observation.ObservationID, second.Observation.ObservationID)
	}
	if first.Observation.ProducerIncarnation != second.Observation.ProducerIncarnation || first.Observation.ProviderObservedAt != nil || second.Observation.ProviderObservedAt != nil {
		t.Fatalf("incarnation=%s/%s", first.Observation.ProducerIncarnation, second.Observation.ProducerIncarnation)
	}
	if first.Observation.CapturedAt != "2026-11-02T03:00:00Z" || second.Observation.CapturedAt != "2026-11-02T03:04:00Z" {
		t.Fatal("capture time rewritten")
	}
	replayed, err := CursorDisplayProject(firstRaw, firstSel, now.Add(2*time.Minute))
	if err != nil || replayed.Observation.ObservationID != first.Observation.ObservationID || replayed.Observation.Sequence != 1 || replayed.Observation.ProducerIncarnation != first.Observation.ProducerIncarnation || replayed.Observation.CapturedAt != first.Observation.CapturedAt || replayed.Observation.ProviderObservedAt != nil {
		t.Fatalf("replay identity changed: %v", err)
	}
	sameScreen, err := CursorDisplayProject(firstRaw, secondSel, now.Add(2*time.Minute))
	if err != nil || sameScreen.Observation.ObservationID == first.Observation.ObservationID || sameScreen.Observation.Sequence != 2 || sameScreen.Observation.ProducerIncarnation != first.Observation.ProducerIncarnation || sameScreen.Observation.CapturedAt != first.Observation.CapturedAt {
		t.Fatalf("repeated screen kept the first capture id: %v", err)
	}
	if _, err := CursorDisplayProject(cursorDisplayDoc(t, map[string]any{"sequence": 9}), firstSel, now); !errors.Is(err, ErrCursorDisplayUnknown) {
		t.Fatalf("payload sequence accepted: %v", err)
	}

	firstBytes := cursorDisplayObservationBytes(t, first.Observation)
	secondBytes := cursorDisplayObservationBytes(t, second.Observation)
	sameBytes := cursorDisplayObservationBytes(t, sameScreen.Observation)
	oneDoc, _ := cursorDisplayCollect(t, [][]byte{firstBytes})
	oneItem := cursorDisplayCursorItem(t, oneDoc)
	oneStatus := cursorDisplayCursorStatus(t, oneDoc)
	if oneItem.Observation.ObservationID != first.Observation.ObservationID || oneItem.Observation.Sequence != 1 || oneItem.Observation.ProducerIncarnation != first.Observation.ProducerIncarnation || oneItem.ReceivedAt != now.Add(time.Second).UTC().Format(time.RFC3339Nano) || oneStatus.Availability != AvailabilityPartial || oneStatus.LastAttempt.Result != AttemptSuccess {
		t.Fatalf("first capture was not admitted: received=%s obs=%#v status=%#v", oneItem.ReceivedAt, oneItem.Observation, oneStatus)
	}

	doc, raw := cursorDisplayCollect(t, [][]byte{firstBytes, secondBytes})
	item := cursorDisplayCursorItem(t, doc)
	status := cursorDisplayCursorStatus(t, doc)
	wantSecondReceived := now.Add(2 * time.Second).UTC().Format(time.RFC3339Nano)
	if item.Observation.ObservationID != second.Observation.ObservationID || item.Observation.Sequence != 2 || item.Observation.ProducerIncarnation != "fixture-incarnation-1" || item.Observation.CapturedAt != "2026-11-02T03:04:00Z" || item.Observation.ProviderObservedAt != nil {
		t.Fatalf("second capture was not admitted: %#v", item.Observation)
	}
	if item.Observation.Measures[0].Value.Decimal == nil || *item.Observation.Measures[0].Value.Decimal != "40" || item.ReceivedAt != wantSecondReceived || item.ReceivedAt == item.Observation.CapturedAt || item.ReceivedAt == doc.ServedAt {
		t.Fatalf("second receipt collapsed received=%s captured=%s served=%s", item.ReceivedAt, item.Observation.CapturedAt, doc.ServedAt)
	}
	if status.Availability != AvailabilityPartial || status.LastAttempt.Result != AttemptSuccess || status.LastAttempt.Code != nil || item.Validation.State == ValidationConflict || item.CurrentMeasures[0].State == CurrentConflict || item.Freshness.State != FreshUnknown || item.Freshness.Basis != BasisCapture {
		t.Fatalf("distinct sequences conflicted: status=%#v current=%#v freshness=%#v", status, item.CurrentMeasures, item.Freshness)
	}
	if status.LastGood == nil || status.LastGood.ObservationID != second.Observation.ObservationID || status.LastGood.ReceivedAt != item.ReceivedAt || status.LastGood.CapturedAt != second.Observation.CapturedAt || status.LastGood.ProviderObservedAt != nil {
		t.Fatalf("last good = %#v", status.LastGood)
	}
	if strings.Contains(string(raw), first.Observation.ObservationID) {
		t.Fatal("first capture id remained in the snapshot")
	}

	sameDoc, _ := cursorDisplayCollect(t, [][]byte{firstBytes, sameBytes})
	sameItem := cursorDisplayCursorItem(t, sameDoc)
	sameStatus := cursorDisplayCursorStatus(t, sameDoc)
	if sameItem.Observation.ObservationID != sameScreen.Observation.ObservationID || sameItem.Observation.Sequence != 2 || sameItem.Observation.ProducerIncarnation != first.Observation.ProducerIncarnation || sameItem.Observation.CapturedAt != first.Observation.CapturedAt || sameItem.Observation.ProviderObservedAt != nil || sameStatus.Availability != AvailabilityPartial || sameStatus.LastAttempt.Result != AttemptSuccess || sameItem.CurrentMeasures[0].State == CurrentConflict {
		t.Fatalf("repeated screen was not admitted: %#v status=%#v", sameItem.Observation, sameStatus)
	}

	replayDoc, _ := cursorDisplayCollect(t, [][]byte{firstBytes, append([]byte(nil), firstBytes...)})
	replayItem := cursorDisplayCursorItem(t, replayDoc)
	replayStatus := cursorDisplayCursorStatus(t, replayDoc)
	wantReplayReceived := now.Add(time.Second).UTC().Format(time.RFC3339Nano)
	wantReplayCompleted := now.Add(2 * time.Second).UTC().Format(time.RFC3339Nano)
	if replayItem.Observation.ObservationID != first.Observation.ObservationID || replayItem.Observation.Sequence != 1 || replayItem.Observation.CapturedAt != first.Observation.CapturedAt || replayItem.Observation.ProducerIncarnation != first.Observation.ProducerIncarnation || replayItem.Observation.ProviderObservedAt != nil || replayItem.ReceivedAt != wantReplayReceived {
		t.Fatalf("replay minted a capture: received=%s obs=%#v", replayItem.ReceivedAt, replayItem.Observation)
	}
	if replayItem.Observation.Measures[0].Value.Decimal == nil || *replayItem.Observation.Measures[0].Value.Decimal != "12.5" || replayStatus.Availability != AvailabilityPartial || replayStatus.LastAttempt.Result != AttemptSuccess || replayStatus.LastAttempt.Code != nil || replayItem.Freshness.State != FreshUnknown {
		t.Fatalf("replay status=%#v item=%#v", replayStatus, replayItem.Observation)
	}
	if replayStatus.LastGood == nil || replayStatus.LastGood.ObservationID != first.Observation.ObservationID || replayStatus.LastGood.ReceivedAt != replayItem.ReceivedAt || replayStatus.LastGood.CapturedAt != first.Observation.CapturedAt || replayStatus.LastAttempt.CompletedAt == nil || *replayStatus.LastAttempt.CompletedAt != wantReplayCompleted || replayItem.ReceivedAt == *replayStatus.LastAttempt.CompletedAt {
		t.Fatalf("replay times good=%#v attempt=%#v", replayStatus.LastGood, replayStatus.LastAttempt)
	}

	changed, err := CursorDisplayProject(secondRaw, firstSel, now)
	if err != nil || changed.Observation.Sequence != 1 || changed.Observation.ObservationID == first.Observation.ObservationID {
		t.Fatalf("same sequence did not stay a distinct body: %v", err)
	}
	conflictDoc, conflictRaw := cursorDisplayCollect(t, [][]byte{firstBytes, cursorDisplayObservationBytes(t, changed.Observation)})
	conflictItem := cursorDisplayCursorItem(t, conflictDoc)
	conflictStatus := cursorDisplayCursorStatus(t, conflictDoc)
	if conflictItem.Observation.ObservationID != first.Observation.ObservationID || conflictItem.Observation.Sequence != 1 || conflictItem.Observation.ProducerIncarnation != first.Observation.ProducerIncarnation || conflictItem.Observation.Measures[0].Value.Decimal == nil || *conflictItem.Observation.Measures[0].Value.Decimal != "12.5" {
		t.Fatalf("conflict replaced the first capture: %#v", conflictItem.Observation)
	}
	if conflictStatus.Availability != AvailabilityConflict || conflictStatus.LastAttempt.Code == nil || *conflictStatus.LastAttempt.Code != "acquisition_conflict" || conflictItem.CurrentMeasures[0].State != CurrentConflict || strings.Contains(string(conflictRaw), `"decimal":"40"`) {
		t.Fatalf("same sequence was admitted: status=%#v current=%#v", conflictStatus, conflictItem.CurrentMeasures)
	}
}

func TestCursorDisplaySequenceRejectsInvalidZeroAndOutOfRange(t *testing.T) {
	now := cursorDisplayNow()
	raw := cursorDisplayDoc(t, nil)
	cases := []struct {
		name string
		seq  int64
	}{
		{name: "invalid", seq: -1},
		{name: "zero", seq: 0},
		{name: "out-of-range", seq: cursorDisplayMaxSequence + 1},
	}
	for _, tc := range cases {
		sel := cursorDisplaySelection()
		sel.Sequence = tc.seq
		item, err := CursorDisplayProject(raw, sel, now)
		if !errors.Is(err, ErrCursorDisplaySelection) || err.Error() != "cursor_display_selection" || item.Observation.ObservationID != "" {
			t.Fatalf("%s sequence %d -> %v", tc.name, tc.seq, err)
		}
	}
	sel := cursorDisplaySelection()
	sel.Sequence = cursorDisplayMaxSequence
	item, err := CursorDisplayProject(raw, sel, now)
	if err != nil || item.Observation.Sequence != Count(cursorDisplayMaxSequence) || item.Observation.ProducerIncarnation != sel.ProducerIncarnation || item.Observation.ProviderObservedAt != nil {
		t.Fatalf("max sequence = %v seq=%d", err, item.Observation.Sequence)
	}
}

func TestCursorDisplayAgedFreshnessDedupeKeepsOriginalReceipt(t *testing.T) {
	sel := cursorDisplaySelection()
	sel.MaxAge = time.Hour
	raw := cursorDisplayDoc(t, map[string]any{"capturedAt": "2026-11-02T02:00:00Z"})
	earlyAt := time.Date(2026, 11, 2, 2, 30, 0, 0, time.UTC)
	midAt := time.Date(2026, 11, 2, 2, 40, 0, 0, time.UTC)
	lateAt := time.Date(2026, 11, 2, 3, 30, 0, 0, time.UTC)
	servedAt := time.Date(2026, 11, 2, 4, 0, 0, 0, time.UTC)
	early, err := CursorDisplayProject(raw, sel, earlyAt)
	if err != nil {
		t.Fatal(err)
	}
	mid, err := CursorDisplayProject(raw, sel, midAt)
	if err != nil {
		t.Fatal(err)
	}
	late, err := CursorDisplayProject(raw, sel, lateAt)
	if err != nil {
		t.Fatal(err)
	}
	if early.Freshness.State != FreshUnknown || early.Freshness.Reason != cursorDisplayReasonCapture || early.Freshness.Basis != BasisCapture || early.Freshness.AsOf != early.ReceivedAt {
		t.Fatalf("early freshness = %#v", early.Freshness)
	}
	if mid.Freshness.State != FreshUnknown || mid.Freshness.Reason != cursorDisplayReasonCapture || mid.ReceivedAt == early.ReceivedAt {
		t.Fatalf("mid freshness = %#v", mid.Freshness)
	}
	if late.Freshness.State != FreshStale || late.Freshness.Reason != cursorDisplayReasonStale || late.Freshness.AsOf != late.ReceivedAt || late.CurrentMeasures[0].Reason != cursorDisplayReasonStale {
		t.Fatalf("late freshness = %#v current=%#v", late.Freshness, late.CurrentMeasures)
	}
	if early.Observation.ObservationID != mid.Observation.ObservationID || early.Observation.ObservationID != late.Observation.ObservationID || early.Observation.Sequence != 1 || early.Observation.ProducerIncarnation != late.Observation.ProducerIncarnation || early.Observation.CapturedAt != "2026-11-02T02:00:00Z" || late.Observation.ProviderObservedAt != nil {
		t.Fatal("aged projections did not stay one capture")
	}
	if cursorDisplayLexical(early.Observation, "plan") != "fixture-tier" || cursorDisplayLexical(late.Observation, "resetDisplayDate") != "2026-12-01" || early.Observation.Measures[0].Value.Decimal == nil || *early.Observation.Measures[0].Value.Decimal != "12.5" {
		t.Fatal("capture body changed while freshness aged")
	}
	kept, err := CursorDisplayDedupe([]ReadItem{late, mid, early})
	if err != nil || len(kept) != 1 || !reflect.DeepEqual(kept[0], early) {
		t.Fatalf("reverse order kept a later receipt: %v %#v", err, kept)
	}
	forward, err := CursorDisplayDedupe([]ReadItem{early, mid, late})
	if err != nil || len(forward) != 1 || !reflect.DeepEqual(forward[0], early) {
		t.Fatalf("forward order freshened or replaced the receipt: %v", err)
	}
	snap, err := CursorDisplaySnapshot([]ReadItem{late, mid, early}, servedAt)
	if err != nil || len(snap.Items) != 1 || len(snap.SourceStatuses) != 1 || !reflect.DeepEqual(snap.Items[0], early) {
		t.Fatalf("snapshot = %v items=%d", err, len(snap.Items))
	}
	if snap.ServedAt != servedAt.UTC().Format(time.RFC3339Nano) || snap.Items[0].ReceivedAt != early.ReceivedAt || snap.Items[0].Freshness.State != FreshUnknown || snap.Items[0].Freshness.Reason != cursorDisplayReasonCapture || snap.Items[0].Freshness.AsOf != early.ReceivedAt {
		t.Fatalf("serving clock rewrote the capture served=%s item=%#v", snap.ServedAt, snap.Items[0].Freshness)
	}
	good := snap.SourceStatuses[0].LastGood
	if good == nil || good.ObservationID != early.Observation.ObservationID || good.ReceivedAt != early.ReceivedAt || good.CapturedAt != early.Observation.CapturedAt || good.ReceivedAt == late.ReceivedAt || good.ProviderObservedAt != nil {
		t.Fatalf("last good = %#v", good)
	}
	freshened := cursorDisplayCloneItem(t, late)
	freshened.Freshness.State = FreshUnknown
	freshened.Freshness.Reason = cursorDisplayReasonCapture
	freshened.Freshness.AsOf = freshened.ReceivedAt
	for i := range freshened.CurrentMeasures {
		freshened.CurrentMeasures[i].Reason = cursorDisplayReasonCapture
	}
	if _, err := CursorDisplayDedupe([]ReadItem{late, freshened}); !errors.Is(err, ErrCursorDisplayConflict) {
		t.Fatal("stale capture was freshened at the same receipt")
	}
	rewritten := cursorDisplayCloneItem(t, freshened)
	rewritten.ReceivedAt = servedAt.UTC().Format(time.RFC3339Nano)
	rewritten.Freshness.AsOf = rewritten.ReceivedAt
	if _, err := CursorDisplayDedupe([]ReadItem{late, rewritten}); !errors.Is(err, ErrCursorDisplayConflict) {
		t.Fatal("later receipt freshened the stale capture")
	}
	if _, err := CursorDisplaySnapshot([]ReadItem{rewritten, late}, servedAt); !errors.Is(err, ErrCursorDisplayConflict) {
		t.Fatal(err)
	}
	keptEarly, err := CursorDisplayDedupe([]ReadItem{rewritten, early})
	if err != nil || len(keptEarly) != 1 || !reflect.DeepEqual(keptEarly[0], early) {
		t.Fatalf("later unknown label replaced the original receipt: %v", err)
	}
	tampered := cursorDisplayCloneItem(t, late)
	used := "1"
	tampered.Observation.Measures[0].Value.Decimal = &used
	if tampered.Observation.ObservationID != early.Observation.ObservationID {
		t.Fatal("tamper changed the capture id")
	}
	if _, err := CursorDisplayDedupe([]ReadItem{early, tampered}); !errors.Is(err, ErrCursorDisplayConflict) {
		t.Fatal("aged freshness hid a different body")
	}
}

func TestCursorDisplayMalformedReceiptDoesNotWinDedupe(t *testing.T) {
	sel := cursorDisplaySelection()
	sel.MaxAge = time.Hour
	raw := cursorDisplayDoc(t, map[string]any{"capturedAt": "2026-11-02T02:00:00Z"})
	earlyAt := time.Date(2026, 11, 2, 2, 30, 0, 0, time.UTC)
	lateAt := time.Date(2026, 11, 2, 3, 30, 0, 0, time.UTC)
	early, err := CursorDisplayProject(raw, sel, earlyAt)
	if err != nil {
		t.Fatal(err)
	}
	late, err := CursorDisplayProject(raw, sel, lateAt)
	if err != nil {
		t.Fatal(err)
	}
	if early.ReceivedAt != "2026-11-02T02:30:00Z" || late.ReceivedAt != "2026-11-02T03:30:00Z" || early.Observation.ObservationID != late.Observation.ObservationID || early.Observation.Sequence != 1 {
		t.Fatalf("receipts early=%s late=%s seq=%d", early.ReceivedAt, late.ReceivedAt, early.Observation.Sequence)
	}
	kept, err := CursorDisplayDedupe([]ReadItem{late, early})
	if err != nil || len(kept) != 1 || kept[0].ReceivedAt != early.ReceivedAt || kept[0].Freshness.State != FreshUnknown || kept[0].Freshness.AsOf != early.Freshness.AsOf {
		t.Fatalf("ordered receipts did not keep the original: %v %#v", err, kept)
	}
	unknown := cursorDisplayCloneItem(t, early)
	unknown.ReceivedAt = "2026-11-02T02:30:00.000Z"
	unknown.Freshness.AsOf = unknown.ReceivedAt
	stale := cursorDisplayCloneItem(t, late)
	stale.ReceivedAt = early.ReceivedAt
	stale.Freshness.AsOf = stale.ReceivedAt
	if _, err := CursorDisplayDedupe([]ReadItem{unknown, stale}); !errors.Is(err, ErrCursorDisplayConflict) {
		t.Fatal("equal instants were ordered by timestamp text")
	}
	sooner := cursorDisplayCloneItem(t, early)
	sooner.ReceivedAt = "2026-11-02T02:29:00+00:00"
	sooner.Freshness.AsOf = sooner.ReceivedAt
	replaced, err := CursorDisplayDedupe([]ReadItem{early, sooner})
	if err != nil || len(replaced) != 1 || replaced[0].ReceivedAt != sooner.ReceivedAt || replaced[0].Observation.ObservationID != early.Observation.ObservationID || replaced[0].Observation.Sequence != early.Observation.Sequence {
		t.Fatalf("valid earlier receipt did not replace: %v %#v", err, replaced)
	}
	longReceipt := "2026-11-02T02:00:00." + strings.Repeat("0", 50) + "Z"
	for _, bad := range []string{"not-a-timestamp", "", "2026-11-02T02:00:00", longReceipt} {
		twin := cursorDisplayCloneItem(t, early)
		twin.ReceivedAt = bad
		twin.Freshness.AsOf = bad
		keptBad, err := CursorDisplayDedupe([]ReadItem{early, twin})
		if err != nil || len(keptBad) != 1 || keptBad[0].ReceivedAt != early.ReceivedAt || keptBad[0].Freshness.AsOf != early.Freshness.AsOf || keptBad[0].Observation.CapturedAt != early.Observation.CapturedAt {
			t.Fatalf("bad %q replaced the receipt: %v len=%d", bad, err, len(keptBad))
		}
		aged := cursorDisplayCloneItem(t, early)
		aged.ReceivedAt = bad
		aged.Freshness.AsOf = bad
		if _, err := CursorDisplayDedupe([]ReadItem{late, aged}); !errors.Is(err, ErrCursorDisplayConflict) {
			t.Fatalf("bad %q ordered an aged capture: %v", bad, err)
		}
		if _, err := CursorDisplaySnapshot([]ReadItem{late, aged}, lateAt); !errors.Is(err, ErrCursorDisplayConflict) {
			t.Fatalf("bad %q snapshot kept an unordered receipt: %v", bad, err)
		}
	}
}

func TestCursorDisplayFailedCaptureIsNotLastGood(t *testing.T) {
	now := cursorDisplayNow()
	good, err := CursorDisplayProject(cursorDisplayDoc(t, nil), cursorDisplaySelection(), now)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"unavailable", "auth", "conflict"} {
		failed := cursorDisplayFailed(t, kind, 1, now)
		snap, err := CursorDisplaySnapshot([]ReadItem{failed}, now)
		if err != nil || len(snap.Items) != 1 || len(snap.SourceStatuses) != 1 || snap.SourceStatuses[0].LastGood != nil {
			t.Fatalf("%s last good = %v %#v", kind, err, snap.SourceStatuses)
		}
		status := snap.SourceStatuses[0]
		if status.Availability != failed.Observation.Availability || status.LastAttempt.AttemptedAt != failed.ReceivedAt || snap.Items[0].Observation.ObservationID != failed.Observation.ObservationID {
			t.Fatalf("%s attempt = %#v", kind, status)
		}
		if !cursorDisplayAttemptMatches(kind, status) {
			t.Fatalf("%s result = %#v", kind, status.LastAttempt)
		}
	}
	for _, kind := range []string{"unavailable", "auth", "conflict"} {
		failed := cursorDisplayFailed(t, kind, 2, now.Add(time.Minute))
		if failed.Observation.Sequence != 2 || good.Observation.Sequence != 1 || failed.Observation.ObservationID == good.Observation.ObservationID || failed.Observation.ProducerIncarnation != good.Observation.ProducerIncarnation {
			t.Fatalf("%s sequence identity collapsed", kind)
		}
		snap, err := CursorDisplaySnapshot([]ReadItem{good, failed}, now.Add(time.Minute))
		if err != nil || len(snap.Items) != 2 || len(snap.SourceStatuses) != 2 {
			t.Fatalf("%s snapshot = %v items=%d", kind, err, len(snap.Items))
		}
		keptGood := snap.SourceStatuses[0]
		if keptGood.LastGood == nil || keptGood.LastGood.ObservationID != good.Observation.ObservationID || keptGood.LastGood.ReceivedAt != good.ReceivedAt || keptGood.LastAttempt.Result != AttemptSuccess || keptGood.Availability != AvailabilityPartial {
			t.Fatalf("%s previous good = %#v", kind, keptGood)
		}
		got := snap.SourceStatuses[1]
		if got.LastGood == nil || got.LastGood.ObservationID != good.Observation.ObservationID || got.LastGood.ReceivedAt != good.ReceivedAt || got.LastGood.CapturedAt != good.Observation.CapturedAt || got.LastGood.ProviderObservedAt != nil || got.LastGood.ObservationID == failed.Observation.ObservationID {
			t.Fatalf("%s retained %#v", kind, got.LastGood)
		}
		if got.Availability != failed.Observation.Availability || got.LastAttempt.AttemptedAt != failed.ReceivedAt || got.LastAttempt.AttemptedAt == got.LastGood.ReceivedAt || !cursorDisplayAttemptMatches(kind, got) {
			t.Fatalf("%s attempt was rewritten: %#v", kind, got.LastAttempt)
		}
		if snap.Items[1].Observation.ObservationID != failed.Observation.ObservationID || snap.Items[1].Observation.Availability != failed.Observation.Availability || snap.Items[0].Observation.ObservationID != good.Observation.ObservationID {
			t.Fatal("failed capture replaced the retained item")
		}
	}
	unavailable := cursorDisplayFailed(t, "unavailable", 2, now.Add(time.Minute))
	auth := cursorDisplayFailed(t, "auth", 3, now.Add(2*time.Minute))
	conflict := cursorDisplayFailed(t, "conflict", 4, now.Add(3*time.Minute))
	chain, err := CursorDisplaySnapshot([]ReadItem{good, unavailable, auth, conflict}, now.Add(3*time.Minute))
	if err != nil || len(chain.SourceStatuses) != 4 {
		t.Fatal(err)
	}
	for i, failed := range []ReadItem{unavailable, auth, conflict} {
		got := chain.SourceStatuses[i+1]
		if got.LastGood == nil || got.LastGood.ObservationID != good.Observation.ObservationID || got.LastGood.ReceivedAt != good.ReceivedAt || got.LastGood.ObservationID == failed.Observation.ObservationID || got.Availability != failed.Observation.Availability {
			t.Fatalf("chain %d = %#v", i, got)
		}
	}
	if chain.SourceStatuses[1].LastAttempt.Result != AttemptError || chain.SourceStatuses[2].LastAttempt.Result != AttemptError || chain.SourceStatuses[3].LastAttempt.Result != AttemptUnknown || chain.SourceStatuses[3].LastAttempt.Code != nil {
		t.Fatalf("chain attempts = %#v %#v %#v", chain.SourceStatuses[1].LastAttempt, chain.SourceStatuses[2].LastAttempt, chain.SourceStatuses[3].LastAttempt)
	}
	reversed, err := CursorDisplaySnapshot([]ReadItem{unavailable, good}, now.Add(time.Minute))
	if err != nil || reversed.SourceStatuses[0].LastGood != nil || reversed.SourceStatuses[0].LastAttempt.Result != AttemptError || reversed.SourceStatuses[1].LastGood == nil || reversed.SourceStatuses[1].LastGood.ObservationID != good.Observation.ObservationID || reversed.SourceStatuses[1].LastGood.ObservationID == unavailable.Observation.ObservationID {
		t.Fatalf("reversed = %v %#v", err, reversed.SourceStatuses)
	}
	staleSel := cursorDisplaySelection()
	staleSel.Sequence = 5
	stale, err := CursorDisplayProject(cursorDisplayDoc(t, map[string]any{"capturedAt": "2026-10-30T03:07:00Z"}), staleSel, now)
	if err != nil || stale.Freshness.State != FreshStale || stale.Observation.Availability != AvailabilityPartial {
		t.Fatal(err)
	}
	staleSnap, err := CursorDisplaySnapshot([]ReadItem{stale, unavailable}, now)
	if err != nil || staleSnap.SourceStatuses[0].LastGood == nil || staleSnap.SourceStatuses[0].LastGood.ObservationID != stale.Observation.ObservationID || staleSnap.SourceStatuses[0].LastAttempt.Result != AttemptSuccess || staleSnap.SourceStatuses[0].Freshness.State != FreshStale {
		t.Fatalf("stale good = %v %#v", err, staleSnap.SourceStatuses)
	}
	if staleSnap.SourceStatuses[1].LastGood == nil || staleSnap.SourceStatuses[1].LastGood.ObservationID != stale.Observation.ObservationID || staleSnap.SourceStatuses[1].LastGood.ReceivedAt != stale.ReceivedAt || staleSnap.SourceStatuses[1].LastGood.ObservationID == unavailable.Observation.ObservationID || staleSnap.SourceStatuses[1].LastAttempt.Result != AttemptError {
		t.Fatalf("stale retained = %#v", staleSnap.SourceStatuses[1])
	}
	marked, err := CursorDisplayProject(cursorDisplayDoc(t, map[string]any{"errorCode": "display_stale"}), cursorDisplaySelection(), now)
	if err != nil {
		t.Fatal(err)
	}
	markedSnap, err := CursorDisplaySnapshot([]ReadItem{marked}, now)
	if err != nil || markedSnap.SourceStatuses[0].LastGood == nil || markedSnap.SourceStatuses[0].LastGood.ObservationID != marked.Observation.ObservationID || markedSnap.SourceStatuses[0].LastAttempt.Result != AttemptSuccess || markedSnap.SourceStatuses[0].LastAttempt.Code == nil || *markedSnap.SourceStatuses[0].LastAttempt.Code != "display_stale" || markedSnap.SourceStatuses[0].Availability != AvailabilityPartial {
		t.Fatalf("marked = %v %#v", err, markedSnap.SourceStatuses)
	}
}

func TestCursorDisplayLastGoodCarriesOnlyMatchingBinding(t *testing.T) {
	now := cursorDisplayNow()
	goodSel := cursorDisplayVerified("fixture-account")
	good, err := CursorDisplayProject(cursorDisplayDoc(t, map[string]any{"accountRef": "fixture-account"}), goodSel, now)
	if err != nil {
		t.Fatal(err)
	}
	same := cursorDisplayVerified("fixture-account")
	same.Sequence = 2
	carried := cursorDisplayBoundFailure(t, same, "fixture-account", "unavailable", now.Add(time.Minute))
	otherAccount := cursorDisplayVerified("fixture-account-b")
	otherAccount.Sequence = 3
	foreign := cursorDisplayBoundFailure(t, otherAccount, "fixture-account-b", "auth", now.Add(2*time.Minute))
	otherWorkspace := cursorDisplayVerified("fixture-account")
	otherWorkspace.Sequence = 4
	otherWorkspace.WorkspaceRef = cursorDisplayPtr("fixture-workspace-b")
	shifted := cursorDisplayBoundFailure(t, otherWorkspace, "fixture-account", "conflict", now.Add(3*time.Minute))
	otherGeneration := cursorDisplayVerified("fixture-account")
	otherGeneration.Sequence = 5
	otherGeneration.BindingGeneration = 2
	rebound := cursorDisplayBoundFailure(t, otherGeneration, "fixture-account", "unavailable", now.Add(4*time.Minute))
	loose := cursorDisplayFailed(t, "conflict", 6, now.Add(5*time.Minute))
	if good.Observation.Sequence != 1 || carried.Observation.Sequence != 2 || foreign.Observation.Sequence != 3 || shifted.Observation.Sequence != 4 || rebound.Observation.Sequence != 5 || loose.Observation.Sequence != 6 {
		t.Fatal("caller sequence was rewritten")
	}
	if carried.Binding.State != BindingVerified || foreign.Binding.AccountRef == nil || *foreign.Binding.AccountRef != "fixture-account-b" || shifted.Binding.WorkspaceRef == nil || *shifted.Binding.WorkspaceRef != "fixture-workspace-b" || rebound.Binding.Generation != 2 || loose.Binding.State != BindingUnknown {
		t.Fatal("binding fixture collapsed")
	}
	cases := []struct {
		name  string
		item  ReadItem
		carry bool
		kind  string
	}{
		{name: "same binding", item: carried, carry: true, kind: "unavailable"},
		{name: "other account", item: foreign, carry: false, kind: "auth"},
		{name: "other workspace", item: shifted, carry: false, kind: "conflict"},
		{name: "other generation", item: rebound, carry: false, kind: "unavailable"},
		{name: "unverified", item: loose, carry: false, kind: "conflict"},
	}
	for _, tc := range cases {
		snap, err := CursorDisplaySnapshot([]ReadItem{good, tc.item}, now.Add(5*time.Minute))
		if err != nil || len(snap.Items) != 2 || len(snap.SourceStatuses) != 2 {
			t.Fatalf("%s snapshot = %v", tc.name, err)
		}
		if snap.Items[0].Observation.ObservationID != good.Observation.ObservationID || snap.Items[0].Observation.Sequence != 1 || snap.Items[1].Observation.ObservationID != tc.item.Observation.ObservationID || snap.Items[1].Observation.Sequence != tc.item.Observation.Sequence || snap.Items[1].ReceivedAt != tc.item.ReceivedAt {
			t.Fatalf("%s rewrote the captures", tc.name)
		}
		keptGood := snap.SourceStatuses[0]
		if keptGood.LastGood == nil || keptGood.LastGood.ObservationID != good.Observation.ObservationID || keptGood.LastGood.ReceivedAt != good.ReceivedAt || keptGood.LastAttempt.Result != AttemptSuccess {
			t.Fatalf("%s previous good = %#v", tc.name, keptGood)
		}
		got := snap.SourceStatuses[1]
		if !cursorDisplayAttemptMatches(tc.kind, got) || got.LastAttempt.AttemptedAt != tc.item.ReceivedAt {
			t.Fatalf("%s attempt = %#v", tc.name, got.LastAttempt)
		}
		if tc.carry {
			if got.LastGood == nil || got.LastGood.ObservationID != good.Observation.ObservationID || got.LastGood.ReceivedAt != good.ReceivedAt || got.LastGood.CapturedAt != good.Observation.CapturedAt || got.LastGood.ObservationID == tc.item.Observation.ObservationID || got.LastGood.ProviderObservedAt != nil {
				t.Fatalf("%s did not carry matching last good: %#v", tc.name, got.LastGood)
			}
			continue
		}
		if got.LastGood != nil {
			t.Fatalf("%s carried last good across a binding: %#v", tc.name, got.LastGood)
		}
	}
}

func cursorDisplayBoundFailure(t *testing.T, sel CursorDisplaySelection, account, kind string, now time.Time) ReadItem {
	t.Helper()
	fields := map[string]any{"usedPercent": nil, "accountRef": account}
	switch kind {
	case "unavailable":
		fields["availability"] = "unavailable"
		fields["errorCode"] = "display_unavailable"
	case "auth":
		fields["availability"] = "auth_needed"
		fields["errorCode"] = "display_auth_needed"
	case "conflict":
		fields["availability"] = "conflict"
	default:
		t.Fatalf("kind %s", kind)
	}
	item, err := CursorDisplayProject(cursorDisplayDoc(t, fields), sel, now)
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func cursorDisplayFailed(t *testing.T, kind string, seq int64, now time.Time) ReadItem {
	t.Helper()
	sel := cursorDisplaySelection()
	sel.Sequence = seq
	fields := map[string]any{"usedPercent": nil}
	switch kind {
	case "unavailable":
		fields["availability"] = "unavailable"
		fields["errorCode"] = "display_unavailable"
	case "auth":
		fields["availability"] = "auth_needed"
		fields["errorCode"] = "display_auth_needed"
	case "conflict":
		fields["availability"] = "conflict"
	default:
		t.Fatalf("kind %s", kind)
	}
	item, err := CursorDisplayProject(cursorDisplayDoc(t, fields), sel, now)
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func cursorDisplayAttemptMatches(kind string, status SourceStatus) bool {
	switch kind {
	case "unavailable":
		return status.Availability == AvailabilityUnavailable && status.LastAttempt.Result == AttemptError && status.LastAttempt.Code != nil && *status.LastAttempt.Code == "display_unavailable"
	case "auth":
		return status.Availability == AvailabilityAuthNeeded && status.LastAttempt.Result == AttemptError && status.LastAttempt.Code != nil && *status.LastAttempt.Code == "display_auth_needed"
	case "conflict":
		return status.Availability == AvailabilityConflict && status.LastAttempt.Result == AttemptUnknown && status.LastAttempt.Code == nil
	default:
		return false
	}
}

func cursorDisplayTrust(selected CursorDisplaySelection, provider string) TrustedContext {
	return CursorDisplayTrustedContext(selected, CursorDisplayCallerSource{
		Provider: provider,
		Kind:     SourceNativeDisplay,
		Version:  CursorDisplaySourceVersion,
	})
}

func cursorDisplayForgeVerified(t *testing.T, item ReadItem) ReadItem {
	t.Helper()
	forged := cursorDisplayCloneItem(t, item)
	at := forged.Observation.CapturedAt
	forged.Observation.BindingGeneration = 1
	forged.Observation.Scope.BindingState = BindingVerified
	forged.Observation.Scope.AccountRef = cursorDisplayPtr("fixture-account")
	forged.Observation.Scope.WorkspaceRef = cursorDisplayPtr("fixture-workspace")
	forged.Observation.Scope.EntitlementRef = cursorDisplayPtr("fixture-entitlement")
	forged.Observation.ProviderObservedAt = &at
	forged.Binding.State = BindingVerified
	forged.Binding.Generation = 1
	forged.Binding.AccountRef = cursorDisplayPtr("fixture-account")
	forged.Binding.WorkspaceRef = cursorDisplayPtr("fixture-workspace")
	return forged
}

func cursorDisplayNow() time.Time {
	return time.Date(2026, 11, 2, 3, 7, 0, 0, time.UTC)
}

// cursorDisplaySelection is one trusted caller selection. Sequence 1 is that
// capture's caller-owned sequence. A later distinct capture supplies the next
// positive sequence and keeps the same producer incarnation.
func cursorDisplaySelection() CursorDisplaySelection {
	return CursorDisplaySelection{
		ProducerRef:         "fixture-producer",
		ProducerIncarnation: "fixture-incarnation-1",
		Sequence:            1,
		BindingState:        BindingUnknown,
		MaxAge:              24 * time.Hour,
	}
}

func cursorDisplayVerified(account string) CursorDisplaySelection {
	sel := cursorDisplaySelection()
	sel.BindingState = BindingVerified
	sel.BindingGeneration = 1
	sel.AccountRef = cursorDisplayPtr(account)
	sel.WorkspaceRef = cursorDisplayPtr("fixture-workspace")
	sel.EntitlementRef = cursorDisplayPtr("fixture-entitlement")
	return sel
}

func cursorDisplayDoc(t *testing.T, overrides map[string]any) []byte {
	t.Helper()
	fields := map[string]any{
		"schema":           CursorDisplayInputSchema,
		"capturedAt":       "2026-11-02T03:07:00Z",
		"route":            "cursor_plan_usage",
		"product":          "grok",
		"plan":             "fixture-tier",
		"accountRef":       nil,
		"availability":     "partial",
		"errorCode":        nil,
		"windowLabel":      "weekly",
		"durationMinutes":  nil,
		"usedPercent":      "12.5",
		"resetDisplayDate": "2026-12-01",
		"resetDisplayText": nil,
		"spendAmount":      nil,
		"spendCurrency":    nil,
	}
	for key, value := range overrides {
		fields[key] = value
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func cursorDisplayLexical(obs Observation, field string) string {
	for _, item := range obs.DirectFields {
		if item.Field == field && item.LexicalValue != nil {
			return *item.LexicalValue
		}
	}
	return ""
}

func cursorDisplayCloneItem(t *testing.T, item ReadItem) ReadItem {
	t.Helper()
	blob, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	var out ReadItem
	if err := json.Unmarshal(blob, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func cursorDisplayReady(t *testing.T, item ReadItem) ReadItem {
	t.Helper()
	clone := cursorDisplayCloneItem(t, item)
	at := clone.Observation.CapturedAt
	clone.Observation.ProviderObservedAt = &at
	return clone
}

type cursorDisplayClock struct {
	now time.Time
}

func (c *cursorDisplayClock) Now() time.Time { return c.now }

func (c *cursorDisplayClock) advance(d time.Duration) { c.now = c.now.Add(d) }

type cursorDisplayWait struct{}

func (cursorDisplayWait) Wait(context.Context, time.Duration) error { return nil }

type cursorDisplayAcquire struct {
	clock *cursorDisplayClock
	raws  [][]byte
	n     int
}

func (a *cursorDisplayAcquire) Acquire(context.Context, SourceSpec) (Acquisition, error) {
	if a.n >= len(a.raws) {
		return Acquisition{}, ErrCursorDisplaySelection
	}
	raw := append([]byte(nil), a.raws[a.n]...)
	a.n++
	a.clock.advance(time.Second)
	return Acquisition{Observation: raw}, nil
}

func cursorDisplayCollect(t *testing.T, raws [][]byte) (ReadResponse, []byte) {
	t.Helper()
	if len(raws) < 1 || len(raws) > 8 {
		t.Fatalf("cycles=%d", len(raws))
	}
	clock := &cursorDisplayClock{now: cursorDisplayNow()}
	bounds := Bounds{
		Enabled: true, MaxCycles: len(raws), MaxSources: 1, MaxBytes: 32 * 1024,
		CallTimeout: 2 * time.Second, Cadence: time.Second, Backoff: time.Second,
		MaxAge: 24 * time.Hour, Parse: FixtureParseLimits,
	}
	spec := SourceSpec{
		Provider: "cursor", ProducerRef: "fixture-producer", ProducerIncarnation: "fixture-incarnation-1",
		BindingGeneration: 0, BindingState: BindingUnknown,
		SourceKind: SourceNativeDisplay, SourceVersion: CursorDisplaySourceVersion,
	}
	collection, err := NewCollection(bounds, []SourceSpec{spec}, cursorDisplayRegistry(true), clock, cursorDisplayWait{}, &cursorDisplayAcquire{clock: clock, raws: raws})
	if err != nil {
		t.Fatal(err)
	}
	if err := collection.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	doc, raw, err := collection.Snapshot(cursorDisplayNow().Add(10 * time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	return doc, raw
}

func cursorDisplayCursorItem(t *testing.T, doc ReadResponse) ReadItem {
	t.Helper()
	if len(doc.Items) != 1 || doc.Items[0].Observation.Provider != "cursor" {
		t.Fatalf("items=%d", len(doc.Items))
	}
	return doc.Items[0]
}

func cursorDisplayCursorStatus(t *testing.T, doc ReadResponse) SourceStatus {
	t.Helper()
	var found *SourceStatus
	for i := range doc.SourceStatuses {
		if doc.SourceStatuses[i].Provider != "cursor" {
			continue
		}
		if found != nil {
			t.Fatal("duplicate cursor status")
		}
		found = &doc.SourceStatuses[i]
	}
	if found == nil {
		t.Fatal("missing cursor status")
	}
	return *found
}

func cursorDisplayObservationBytes(t *testing.T, obs Observation) []byte {
	t.Helper()
	raw, err := json.Marshal(obs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeObservation(raw, FixtureParseLimits); err != nil {
		t.Fatal(err)
	}
	return raw
}

func cursorDisplayHasCode(issues []Issue, code string) bool {
	for _, issue := range issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}

func cursorDisplayRegistry(allowMeasures bool) *StaticRegistry {
	minZero := "0"
	maxPercent := "100"
	maxMoney := "1000000"
	usd := "USD"
	return NewStaticRegistry(RegistryParts{
		Sources: []SourceRule{{
			Provider: "cursor", Kind: SourceNativeDisplay, Version: CursorDisplaySourceVersion,
			CapabilityRef: CursorDisplayCapability, AllowNormalizedMeasures: allowMeasures, AllowDerivations: allowMeasures,
			DirectFields: map[string]DirectFieldRule{
				"plan":             {OriginalType: TypeString, Class: ClassDisplay},
				"windowLabel":      {OriginalType: TypeString, Class: ClassDisplay},
				"resetDisplayDate": {OriginalType: TypeString, Class: ClassDisplay},
				"resetDisplayText": {OriginalType: TypeString, Class: ClassDisplay},
				"accountLabel":     {OriginalType: TypeString, Class: ClassDisplay},
			},
		}},
		Metrics: []MetricRule{
			{
				ValidationRef: CursorDisplayIncludedValidation, Provider: "cursor",
				SourceKind: SourceNativeDisplay, SourceVersion: CursorDisplaySourceVersion,
				Category: CategoryIncluded, Unit: UnitPercent, Direction: DirectionUsed,
				Windows: []string{CursorDisplayIncludedWindow}, Min: &minZero, Max: &maxPercent,
				ComplementOf: "100", HistoricalOnly: true,
			},
			{
				ValidationRef: CursorDisplaySpendValidation, Provider: "cursor",
				SourceKind: SourceNativeDisplay, SourceVersion: CursorDisplaySourceVersion,
				Category: CategorySpend, Unit: UnitMoney, Direction: DirectionUsed,
				Windows: []string{CursorDisplaySpendWindow}, Currency: &usd, Min: &minZero, Max: &maxMoney,
				HistoricalOnly: true,
			},
		},
	})
}

func cursorDisplayPoolRegistry() *StaticRegistry {
	return NewStaticRegistry(RegistryParts{
		Accounts: []AccountRule{
			{AccountRef: "fixture-account", WorkspaceRef: "fixture-workspace", Generation: 1, ProducerRef: "fixture-producer", ProducerIncarnation: "fixture-incarnation-1"},
			{AccountRef: "fixture-account-b", WorkspaceRef: "fixture-workspace", Generation: 1, ProducerRef: "fixture-producer", ProducerIncarnation: "fixture-incarnation-1"},
		},
		Entitlements: []EntitlementRule{{
			EntitlementRef: "fixture-entitlement", Products: []string{"cursor", "grok"}, Verified: true,
			Bindings: []EntitlementBinding{
				{AccountRef: "fixture-account", WorkspaceRef: "fixture-workspace", Generation: 1, ProducerRef: "fixture-producer", ProducerIncarnation: "fixture-incarnation-1", Product: "grok"},
				{AccountRef: "fixture-account", WorkspaceRef: "fixture-workspace", Generation: 1, ProducerRef: "fixture-producer", ProducerIncarnation: "fixture-incarnation-1", Product: "cursor"},
				{AccountRef: "fixture-account-b", WorkspaceRef: "fixture-workspace", Generation: 1, ProducerRef: "fixture-producer", ProducerIncarnation: "fixture-incarnation-1", Product: "grok"},
				{AccountRef: "fixture-account-b", WorkspaceRef: "fixture-workspace", Generation: 1, ProducerRef: "fixture-producer", ProducerIncarnation: "fixture-incarnation-1", Product: "cursor"},
			},
		}},
	})
}
