package providerallowance

import (
	"bytes"
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
	if obs.Provider != "cursor" || obs.Source.Kind != SourceNativeDisplay || obs.Source.Version != CursorDisplaySourceVersion || obs.Source.CapabilityRef != CursorDisplayCapability {
		t.Fatalf("source = %#v", obs.Source)
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
	if err != nil || later.Observation.ObservationID != item.Observation.ObservationID || later.Freshness.State != FreshUnknown || CursorDisplayProviderRefresh(item, later) {
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
	if err != nil || first.Observation.ObservationID != second.Observation.ObservationID || CursorDisplayProviderRefresh(first, second) {
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
	if item.Observation.Provider != "xai" || item.Observation.Source.CapabilityRef != CursorDisplayXAICapability || item.Observation.Availability != AvailabilityUnsupported || item.Observation.Scope.EntitlementRef != nil || len(item.Observation.Measures) != 0 || len(item.Observation.Derivations) != 0 || item.Observation.ProviderObservedAt != nil || item.Binding.State != BindingUnknown {
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

func cursorDisplaySelection() CursorDisplaySelection {
	return CursorDisplaySelection{
		ProducerRef:         "fixture-producer",
		ProducerIncarnation: "fixture-incarnation-1",
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
