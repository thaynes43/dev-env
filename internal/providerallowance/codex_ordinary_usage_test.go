package providerallowance

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCodexOrdinaryUsageLegacyAndExtendedDecode(t *testing.T) {
	legacy := `{"schema":"` + CodexRPCSchema + `","accountRef":"openai-email-sha256:` + strings.Repeat("a", 64) + `","identityBasis":"provider_email_sha256","planType":"pro","receivedAt":"2026-11-02T18:02:10Z","providerObservedAt":null,"primary":{"usedPercent":12,"windowDurationMins":10080,"resetsAt":1794224930},"secondary":null}`
	withUsage := func(value string) string {
		return strings.Replace(legacy, `"secondary":null}`, `"secondary":null,"ordinaryUsageAllowed":`+value+`}`, 1)
	}
	decoded, err := DecodeCodexAccountCapture([]byte(legacy))
	if err != nil || len(decoded.OrdinaryUsageAllowed) != 0 || decoded.ProviderObservedAt != nil || decoded.Primary == nil || decoded.Primary.UsedPercent != 12 {
		t.Fatalf("legacy capture %+v %v", decoded, err)
	}
	body, err := json.Marshal(decoded)
	if err != nil || bytes.Contains(body, []byte("ordinaryUsageAllowed")) {
		t.Fatalf("legacy remarshal invented the field %s %v", body, err)
	}
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{"null", withUsage("null"), "null"},
		{"false", withUsage("false"), "false"},
		{"true", withUsage("true"), "true"},
		{"spaced false", withUsage(" false"), "false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecodeCodexAccountCapture([]byte(tc.raw))
			if err != nil || string(got.OrdinaryUsageAllowed) != tc.want || got.ProviderObservedAt != nil {
				t.Fatalf("scalar %+v %v", got.OrdinaryUsageAllowed, err)
			}
			again, err := json.Marshal(got)
			if err != nil || !bytes.Contains(again, []byte(`"ordinaryUsageAllowed":`+tc.want)) {
				t.Fatalf("scalar not preserved %s %v", again, err)
			}
		})
	}
	for _, raw := range []string{
		withUsage(`"false"`),
		withUsage(`"true"`),
		withUsage("0"),
		withUsage("1"),
		withUsage("{}"),
		withUsage("[]"),
		strings.Replace(legacy, `"secondary":null}`, `"secondary":null,"ordinaryUsageAllowed":false,"ordinaryUsageAllowed":true}`, 1),
		strings.Replace(legacy, `"secondary":null}`, `"secondary":null,"OrdinaryUsageAllowed":false}`, 1),
		strings.Replace(legacy, `"secondary":null}`, `"secondary":null,"notes":true}`, 1),
	} {
		if _, err := DecodeCodexAccountCapture([]byte(raw)); err == nil {
			t.Fatalf("closed decoder accepted %s", raw)
		}
	}
}

func TestCodexOrdinaryUsageProjection(t *testing.T) {
	missingBundle, _, _ := ordinaryUsageAssembly(t, nil, nil)
	nullBundle, _, _ := ordinaryUsageAssembly(t, json.RawMessage("null"), nil)
	if bytes.Equal(missingBundle, nullBundle) || !bytes.Contains(nullBundle, []byte(`"ordinaryUsageAllowed":null`)) || bytes.Contains(missingBundle, []byte("ordinaryUsageAllowed")) {
		t.Fatal("explicit null collapsed into a missing capture")
	}
	missing := assembleOrdinary(t, nil, nil, time.Time{})
	nullDoc := assembleOrdinary(t, json.RawMessage("null"), nil, time.Time{})
	trueDoc := assembleOrdinary(t, json.RawMessage("true"), nil, time.Time{})
	falseDoc := assembleOrdinary(t, json.RawMessage("false"), nil, time.Time{})
	if missing.Items[0].Observation.ObservationID != nullDoc.Items[0].Observation.ObservationID {
		t.Fatal("null became a distinct normalized observation")
	}
	if trueDoc.Items[0].Observation.ObservationID == falseDoc.Items[0].Observation.ObservationID || trueDoc.Items[0].Observation.ObservationID == missing.Items[0].Observation.ObservationID || falseDoc.Items[0].Observation.ObservationID == missing.Items[0].Observation.ObservationID {
		t.Fatal("explicit boolean did not change the observation id")
	}
	if got, ok := ordinaryUsageLexical(trueDoc.Items[0].Observation); !ok || got != "true" {
		t.Fatalf("true lexical = %q %v", got, ok)
	}
	if got, ok := ordinaryUsageLexical(falseDoc.Items[0].Observation); !ok || got != "false" {
		t.Fatalf("false lexical = %q %v", got, ok)
	}
	if _, ok := ordinaryUsageLexical(nullDoc.Items[0].Observation); ok || len(nullDoc.Items[0].Observation.DirectFields) != 0 {
		t.Fatal("null became a normalized boolean")
	}
	if len(trueDoc.Items[0].Observation.DirectFields) != 1 || len(trueDoc.Items[0].Observation.Measures) != 2 {
		t.Fatal("flag replaced or multiplied measures")
	}
	assertProviderTimeUnchanged(t, trueDoc.Items[0])
	assertProviderTimeUnchanged(t, falseDoc.Items[0])
	if trueDoc.Items[0].Freshness.State != falseDoc.Items[0].Freshness.State || trueDoc.Items[0].Freshness.Basis != falseDoc.Items[0].Freshness.Basis || trueDoc.Items[0].Binding.State != falseDoc.Items[0].Binding.State || trueDoc.Items[0].Binding.State != BindingVerified || trueDoc.Items[0].CurrentMeasures[0].State != falseDoc.Items[0].CurrentMeasures[0].State {
		t.Fatal("boolean changed binding, freshness, or current status")
	}
	if *trueDoc.Items[0].Observation.Derivations[0].Value.Decimal != "88" || *falseDoc.Items[0].Observation.Derivations[0].Value.Decimal != "88" {
		t.Fatal("flag changed the reported percentage")
	}

	first, trust, at := assembleOrdinaryTrust(t, json.RawMessage("false"), nil, time.Time{})
	replay, err := AssembleCaptureBundle(mustBundle(t, json.RawMessage("false"), nil), trust, at.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if replay.Items[0].Observation.ObservationID != first.Items[0].Observation.ObservationID || replay.ServedAt == first.ServedAt {
		t.Fatal("serving clock changed the observation id")
	}
	if got, ok := ordinaryUsageLexical(replay.Items[0].Observation); !ok || got != "false" {
		t.Fatal("serving clock refreshed the boolean")
	}
	assertProviderTimeUnchanged(t, replay.Items[0])
	if replay.Items[0].Observation.CapturedAt != first.Items[0].Observation.CapturedAt || replay.Items[0].Observation.ProviderObservedAt != nil {
		t.Fatal("serving clock rewrote capture time")
	}

	published := assembleOrdinary(t, json.RawMessage("true"), nil, time.Time{})
	readerParts := snapshotRegistry(publishedTrust(t).Registry)
	readerParts.Sources[0].DirectFields = map[string]DirectFieldRule{}
	reader := NewStaticRegistry(readerParts)
	assessed := ValidateObservation(published.Items[0].Observation, publishedTrust(t).Contexts[0], reader)
	if !issueHas(assessed.Issues, IssueFieldNotAllowlisted) {
		t.Fatal("reader registry accepted a publisher-only field")
	}
	matching := ValidateObservation(published.Items[0].Observation, publishedTrust(t).Contexts[0], publishedTrust(t).Registry)
	if len(matching.Issues) != 0 {
		t.Fatalf("exact rule rejected the boolean %+v", matching.Issues)
	}
	cached := published
	ProjectCachedRead(&cached, publishedTrust(t).Contexts, reader, 30*time.Minute, true, publishedAt(t).Add(40*time.Minute))
	if got, ok := ordinaryUsageLexical(cached.Items[0].Observation); !ok || got != "true" {
		t.Fatal("mismatched reader stripped ordinaryUsageAllowed")
	}
	if cached.Items[0].Binding.State == BindingVerified || cached.Items[0].Observation.ProviderObservedAt != nil || cached.Items[0].Freshness.State != FreshUnknown || cached.Items[0].CurrentMeasures[0].Value.Decimal != nil {
		t.Fatal("publisher-only qualification was treated as current")
	}
	held := assembleOrdinary(t, json.RawMessage("false"), nil, time.Time{})
	trust = publishedTrust(t)
	ProjectCachedRead(&held, trust.Contexts, trust.Registry, 30*time.Minute, true, publishedAt(t).Add(2*time.Minute))
	if held.Items[0].Binding.State != BindingVerified || held.Items[0].Validation.State != ValidationSourceFields {
		t.Fatal("matching reader rejected the qualified boolean")
	}
	if got, ok := ordinaryUsageLexical(held.Items[0].Observation); !ok || got != "false" {
		t.Fatal("matching reader dropped explicit false")
	}
	assertProviderTimeUnchanged(t, held.Items[0])
}

func TestCodexOrdinaryUsageUnqualifiedRulesKeepLegacyID(t *testing.T) {
	cases := []struct {
		name     string
		alter    func(*RegistryParts, *TrustedContext)
		verified bool
	}{
		{name: "absent", verified: true, alter: func(parts *RegistryParts, _ *TrustedContext) {
			parts.Sources[0].DirectFields = map[string]DirectFieldRule{}
		}},
		{name: "wrong type", verified: true, alter: func(parts *RegistryParts, _ *TrustedContext) {
			parts.Sources[0].DirectFields[codexOrdinaryUsageField] = DirectFieldRule{OriginalType: TypeString, Class: ClassSignal}
		}},
		{name: "wrong class", verified: true, alter: func(parts *RegistryParts, _ *TrustedContext) {
			parts.Sources[0].DirectFields[codexOrdinaryUsageField] = DirectFieldRule{OriginalType: TypeBoolean, Class: ClassDisplay}
		}},
		{name: "wrong capability", alter: func(parts *RegistryParts, _ *TrustedContext) {
			parts.Sources[0].CapabilityRef = "codex-app-server:other"
		}},
		{name: "wrong version", alter: func(parts *RegistryParts, _ *TrustedContext) {
			parts.Sources[0].Version = "other-version"
		}},
		{name: "duplicate", alter: func(parts *RegistryParts, _ *TrustedContext) {
			parts.Sources = append(parts.Sources, parts.Sources[0])
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bundle, trust, at := ordinaryUsageAssembly(t, json.RawMessage("true"), tc.alter)
			if !bytes.Contains(bundle, []byte(`"ordinaryUsageAllowed":true`)) {
				t.Fatal("raw true was dropped before projection")
			}
			withFlag, err := AssembleCaptureBundle(bundle, trust, at.Add(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			plainBundle, plainTrust, _ := ordinaryUsageAssembly(t, nil, tc.alter)
			plain, err := AssembleCaptureBundle(plainBundle, plainTrust, at.Add(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			if withFlag.Items[0].Observation.ObservationID != plain.Items[0].Observation.ObservationID {
				t.Fatal("unqualified flag changed the observation id")
			}
			if len(withFlag.Items[0].Observation.DirectFields) != 0 {
				t.Fatal("unqualified rule emitted ordinaryUsageAllowed")
			}
			assertProviderTimeUnchanged(t, withFlag.Items[0])
			if withFlag.Items[0].Binding.State != plain.Items[0].Binding.State {
				t.Fatal("unqualified flag changed binding")
			}
			if tc.verified && withFlag.Items[0].Binding.State != BindingVerified {
				t.Fatal("missing exact rule stranded an otherwise valid capture")
			}
			if (tc.name == "wrong capability" || tc.name == "duplicate") && withFlag.Items[0].Binding.State == BindingVerified {
				t.Fatal("optional field rule implied a successful binding")
			}
		})
	}
}

func TestCodexOrdinaryUsageReaderRejectsInexactSignal(t *testing.T) {
	trust := publishedTrust(t)
	at := publishedAt(t)
	cases := []struct {
		name  string
		alter func(*RegistryParts)
	}{
		{name: "empty capability", alter: func(parts *RegistryParts) { parts.Sources[0].CapabilityRef = "" }},
		{name: "wrong capability", alter: func(parts *RegistryParts) { parts.Sources[0].CapabilityRef = "codex-app-server:other" }},
		{name: "display class", alter: func(parts *RegistryParts) {
			parts.Sources[0].DirectFields[codexOrdinaryUsageField] = DirectFieldRule{OriginalType: TypeBoolean, Class: ClassDisplay}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := assembleOrdinary(t, json.RawMessage("true"), nil, time.Time{})
			parts := snapshotRegistry(trust.Registry)
			tc.alter(&parts)
			ProjectCachedRead(&doc, trust.Contexts, NewStaticRegistry(parts), 30*time.Minute, true, at.Add(2*time.Minute))
			if got, ok := ordinaryUsageLexical(doc.Items[0].Observation); !ok || got != "true" {
				t.Fatal("reader stripped ordinaryUsageAllowed")
			}
			if doc.Items[0].Binding.State == BindingVerified || doc.Items[0].Observation.ProviderObservedAt != nil || doc.Items[0].Freshness.State != FreshUnknown || doc.Items[0].CurrentMeasures[0].Value.Decimal != nil {
				t.Fatal("inexact reader accepted ordinaryUsageAllowed as current")
			}
		})
	}
}

func TestCodexOrdinaryUsageReaderKeepsOtherSourceFieldTrusted(t *testing.T) {
	obs, ctx, reg := repairAllowanceFixture(t)
	lexical := "true"
	obs.DirectFields = append(obs.DirectFields, DirectField{Field: codexOrdinaryUsageField, OriginalType: TypeBoolean, LexicalValue: &lexical})
	parts := snapshotRegistry(reg)
	parts.Sources[0].DirectFields = map[string]DirectFieldRule{
		codexOrdinaryUsageField: {OriginalType: TypeBoolean, Class: ClassSignal},
	}
	reg = NewStaticRegistry(parts)
	if assessed := ValidateObservation(obs, ctx, reg); len(assessed.Issues) != 0 {
		t.Fatalf("fixture-v1 boolean signal %+v", assessed.Issues)
	}
	if !cachedOrdinaryUsageReader(obs, reg) {
		t.Fatal("fixture-v1 ordinaryUsageAllowed entered the account-capture reader gate")
	}
	observed := *obs.ProviderObservedAt
	captured := obs.CapturedAt
	doc := repairRead(obs, captured, "2026-11-02T03:05:00Z", observed, CurrentMeasure{
		MeasureID: "window", State: CurrentKnown, Value: MeasureValue{State: ValueKnown, Decimal: repairStr("55")},
	})
	ProjectCachedRead(&doc, []TrustedContext{ctx}, reg, 30*time.Minute, true, time.Date(2026, 11, 2, 3, 10, 0, 0, time.UTC))
	item := doc.Items[0]
	got, ok := ordinaryUsageLexical(item.Observation)
	if !ok || got != "true" || item.Binding.State != BindingVerified || item.Observation.CapturedAt != captured || item.Observation.ProviderObservedAt == nil || *item.Observation.ProviderObservedAt != observed {
		t.Fatalf("other source field or clock changed %+v", item)
	}
	if item.Freshness.State != FreshFresh || item.Freshness.Basis != BasisProvider || item.Freshness.AsOf != observed || item.ReceivedAt != "2026-11-02T03:05:00Z" {
		t.Fatalf("other source freshness changed %+v", item.Freshness)
	}
	if len(item.CurrentMeasures) != 1 || item.CurrentMeasures[0].State != CurrentKnown || item.CurrentMeasures[0].Value.Decimal == nil || *item.CurrentMeasures[0].Value.Decimal != "55" {
		t.Fatalf("other source current changed %+v", item.CurrentMeasures)
	}
	if item.Observation.Measures[0].Value.Decimal == nil || *item.Observation.Measures[0].Value.Decimal != "55" {
		t.Fatal("other source measure changed")
	}
}

func TestCodexOrdinaryUsageOtherVersionDoesNotHideExactRule(t *testing.T) {
	doc := assembleOrdinary(t, json.RawMessage("true"), func(parts *RegistryParts, _ *TrustedContext) {
		parts.Sources = append(parts.Sources, SourceRule{
			Provider: "openai", Kind: SourceNativeRPC, Version: "other-version", CapabilityRef: CodexRPCCapability,
			AllowNormalizedMeasures: true, AllowDerivations: true,
			DirectFields: map[string]DirectFieldRule{codexOrdinaryUsageField: {OriginalType: TypeString, Class: ClassDisplay}},
		})
	}, time.Time{})
	if got, ok := ordinaryUsageLexical(doc.Items[0].Observation); !ok || got != "true" || doc.Items[0].Binding.State != BindingVerified {
		t.Fatalf("exact rule was not selected %+v", doc.Items[0].Observation.DirectFields)
	}
	assertProviderTimeUnchanged(t, doc.Items[0])
}

func TestCodexOrdinaryUsageRuleDoesNotGrantIdentity(t *testing.T) {
	doc := assembleOrdinary(t, json.RawMessage("false"), func(parts *RegistryParts, _ *TrustedContext) {
		parts.Accounts[0].AccountScopeOnly = false
	}, time.Time{})
	if got, ok := ordinaryUsageLexical(doc.Items[0].Observation); !ok || got != "false" {
		t.Fatal("identity failure dropped the qualified boolean")
	}
	if doc.Items[0].Binding.State == BindingVerified {
		t.Fatal("field rule implied account identity")
	}
	assertProviderTimeUnchanged(t, doc.Items[0])
	bundle, trust, at := ordinaryUsageAssembly(t, json.RawMessage("true"), nil)
	workspace := "workspace-not-in-provider"
	trust.Contexts[0].WorkspaceRef = &workspace
	if _, err := AssembleCaptureBundle(bundle, trust, at.Add(time.Minute)); !errors.Is(err, ErrCaptureBundle) {
		t.Fatal("field rule bypassed the workspace guard", err)
	}
	other := "openai-email-sha256:" + strings.Repeat("c", 64)
	trust.Contexts[0].WorkspaceRef = nil
	trust.Contexts[0].AccountRef = &other
	if _, err := AssembleCaptureBundle(bundle, trust, at.Add(time.Minute)); !errors.Is(err, ErrCaptureBundle) {
		t.Fatal("field rule bypassed the account guard", err)
	}
	_, fresh, at2 := ordinaryUsageAssembly(t, nil, nil)
	bad := bytes.Replace(bundle, []byte(`"ordinaryUsageAllowed":true`), []byte(`"ordinaryUsageAllowed":"true"`), 1)
	if bytes.Equal(bad, bundle) {
		t.Fatal("fixture did not contain the scalar")
	}
	if _, err := AssembleCaptureBundle(bad, fresh, at2.Add(time.Minute)); !errors.Is(err, ErrCaptureBundle) {
		t.Fatal("wrong scalar type entered assembly", err)
	}
}

func ordinaryUsageAssembly(t *testing.T, usage json.RawMessage, alter func(*RegistryParts, *TrustedContext)) ([]byte, AggregateTrust, time.Time) {
	t.Helper()
	at := time.Date(2026, 11, 2, 18, 2, 10, 0, time.UTC)
	account := "openai-email-sha256:" + strings.Repeat("a", 64)
	used, duration, reset := 12, int64(10080), int64(1794224930)
	report := CodexAccountCapture{Schema: CodexRPCSchema, AccountRef: account, IdentityBasis: "provider_email_sha256", PlanType: "pro", ReceivedAt: at.Format(time.RFC3339Nano), Primary: &CodexWindow{UsedPercent: used, WindowDurationMins: &duration, ResetsAt: &reset}, OrdinaryUsageAllowed: usage}
	payload, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	entry := captureEntry{Provider: "openai", ProducerRef: "own-account", ProducerIncarnation: "own-1", Sequence: 1, CapturedAt: report.ReceivedAt, ReceivedAt: report.ReceivedAt, Payload: payload}
	bundle, err := json.Marshal(map[string]any{"schema": CaptureBundleSchema, "captures": []captureEntry{entry}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := TrustedContext{Provider: "openai", ProducerRef: entry.ProducerRef, ProducerIncarnation: entry.ProducerIncarnation, SourceKind: SourceNativeRPC, SourceVersion: CodexRPCSourceVersion, AccountRef: &account, BindingState: BindingVerified}
	min, max := "0", "100"
	parts := RegistryParts{Sources: []SourceRule{{
		Provider: "openai", Kind: SourceNativeRPC, Version: CodexRPCSourceVersion, CapabilityRef: CodexRPCCapability,
		AllowNormalizedMeasures: true, AllowDerivations: true,
		DirectFields: map[string]DirectFieldRule{codexOrdinaryUsageField: {OriginalType: TypeBoolean, Class: ClassSignal}},
	}}, Accounts: []AccountRule{{AccountRef: account, AccountScopeOnly: true, ProducerRef: entry.ProducerRef, ProducerIncarnation: entry.ProducerIncarnation}}}
	for _, name := range []string{"primary", "secondary"} {
		parts.Metrics = append(parts.Metrics, MetricRule{ValidationRef: "codex-app-server:" + name + "-used-percent", Provider: "openai", SourceKind: SourceNativeRPC, SourceVersion: CodexRPCSourceVersion, Category: CategoryIncluded, Unit: UnitPercent, Direction: DirectionUsed, Windows: []string{"codex-" + name}, Min: &min, Max: &max, ComplementOf: "100", HistoricalOnly: true})
	}
	if alter != nil {
		alter(&parts, &ctx)
	}
	return bundle, AggregateTrust{Contexts: []TrustedContext{ctx}, Registry: NewStaticRegistry(parts), MaxAge: 1800}, at
}

func assembleOrdinary(t *testing.T, usage json.RawMessage, alter func(*RegistryParts, *TrustedContext), now time.Time) ReadResponse {
	t.Helper()
	doc, _, _ := assembleOrdinaryTrust(t, usage, alter, now)
	return doc
}

func assembleOrdinaryTrust(t *testing.T, usage json.RawMessage, alter func(*RegistryParts, *TrustedContext), now time.Time) (ReadResponse, AggregateTrust, time.Time) {
	t.Helper()
	bundle, trust, at := ordinaryUsageAssembly(t, usage, alter)
	if now.IsZero() {
		now = at.Add(time.Minute)
	}
	doc, err := AssembleCaptureBundle(bundle, trust, now)
	if err != nil {
		t.Fatal(err)
	}
	return doc, trust, at
}

func mustBundle(t *testing.T, usage json.RawMessage, alter func(*RegistryParts, *TrustedContext)) []byte {
	t.Helper()
	bundle, _, _ := ordinaryUsageAssembly(t, usage, alter)
	return bundle
}

func publishedTrust(t *testing.T) AggregateTrust {
	t.Helper()
	_, trust, _ := ordinaryUsageAssembly(t, nil, nil)
	return trust
}

func publishedAt(t *testing.T) time.Time {
	t.Helper()
	_, _, at := ordinaryUsageAssembly(t, nil, nil)
	return at
}

func ordinaryUsageLexical(obs Observation) (string, bool) {
	var value string
	n := 0
	for _, field := range obs.DirectFields {
		if field.Field != codexOrdinaryUsageField {
			continue
		}
		n++
		if field.OriginalType != TypeBoolean || field.LexicalValue == nil || (*field.LexicalValue != "true" && *field.LexicalValue != "false") {
			return "", false
		}
		value = *field.LexicalValue
	}
	if n != 1 {
		return "", false
	}
	return value, true
}

func assertProviderTimeUnchanged(t *testing.T, item ReadItem) {
	t.Helper()
	if item.Observation.ProviderObservedAt != nil || item.Freshness.State != FreshUnknown || item.Freshness.Basis != BasisCapture {
		t.Fatalf("provider freshness changed %+v", item.Freshness)
	}
	if len(item.CurrentMeasures) == 0 || item.CurrentMeasures[0].Value.Decimal != nil {
		t.Fatal("flag promoted current capacity")
	}
	if len(item.Observation.Measures) == 0 || item.Observation.Measures[0].Value.Decimal == nil || *item.Observation.Measures[0].Value.Decimal != "12" {
		t.Fatal("used percent changed")
	}
}

func issueHas(issues []Issue, code string) bool {
	for _, item := range issues {
		if item.Code == code {
			return true
		}
	}
	return false
}
