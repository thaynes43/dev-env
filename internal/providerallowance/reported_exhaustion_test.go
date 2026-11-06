package providerallowance

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestProjectCachedReadAnnotatesQualifiedReportedExhaustionOnce(t *testing.T) {
	at := claudeExhaustionAt()
	reset := "2026-11-09T18:00:00Z"
	for _, percent := range []string{"100", "100.0"} {
		t.Run(percent, func(t *testing.T) {
			doc, trust := claudeExhaustionDoc(t, []ClaudeUsageLimit{claudeExhaustionLimit("weekly_all", percent, &reset, nil)}, at)
			before, err := json.Marshal(doc.Items[0].Observation)
			if err != nil {
				t.Fatal(err)
			}
			now := at.Add(time.Minute)
			ProjectCachedRead(&doc, trust.Contexts, trust.Registry, 30*time.Minute, true, now)
			ProjectCachedRead(&doc, trust.Contexts, trust.Registry, 30*time.Minute, true, now)
			item := doc.Items[0]
			after, err := json.Marshal(item.Observation)
			if err != nil || string(before) != string(after) {
				t.Fatal("qualified observation was rewritten")
			}
			if item.Observation.ProviderObservedAt != nil || item.Observation.Measures[0].Window.ResetAt == nil || *item.Observation.Measures[0].Window.ResetAt != reset || item.Observation.Measures[0].Value.Decimal == nil || *item.Observation.Measures[0].Value.Decimal != percent {
				t.Fatalf("original clock, reset, or percent moved: %+v", item.Observation)
			}
			assertExhaustionNotice(t, item, reasonTimeMissing, true)
			if strings.Count(item.CurrentMeasures[0].Reason, reportedExhaustionNotice) != 1 {
				t.Fatalf("notice repeated: %q", item.CurrentMeasures[0].Reason)
			}
		})
	}
}

func TestProjectCachedReadKeepsPrimaryReasonAndDropsInjectedNotice(t *testing.T) {
	at := claudeExhaustionAt()
	reset := at.Add(2 * time.Hour).Format(time.RFC3339)
	doc, trust := claudeExhaustionDoc(t, []ClaudeUsageLimit{claudeExhaustionLimit("session", "100", &reset, nil)}, at)
	primary := "provider time missing; reported value is historical only."
	doc.Items[0].CurrentMeasures[0].Reason = appendReportedExhaustion(primary)
	now := at.Add(time.Minute)
	ProjectCachedRead(&doc, trust.Contexts, trust.Registry, 30*time.Minute, true, now)
	assertExhaustionNotice(t, doc.Items[0], primary, true)
	if strings.Count(doc.Items[0].CurrentMeasures[0].Reason, reportedExhaustionNotice) != 1 {
		t.Fatalf("injected notice was doubled: %q", doc.Items[0].CurrentMeasures[0].Reason)
	}
}

func TestProjectCachedReadExhaustionRespectsAcquisitionAndReset(t *testing.T) {
	at := claudeExhaustionAt()
	future := at.Add(2 * time.Hour).Format(time.RFC3339)
	base := func() (ReadResponse, AggregateTrust) {
		t.Helper()
		return claudeExhaustionDoc(t, []ClaudeUsageLimit{claudeExhaustionLimit("weekly_all", "100", &future, nil)}, at)
	}
	t.Run("ttl boundary", func(t *testing.T) {
		doc, trust := base()
		ProjectCachedRead(&doc, trust.Contexts, trust.Registry, 30*time.Minute, true, at.Add(30*time.Minute))
		assertExhaustionNotice(t, doc.Items[0], reasonTimeMissing, true)
	})
	t.Run("stale", func(t *testing.T) {
		doc, trust := base()
		doc.Items[0].CurrentMeasures[0].Reason = appendReportedExhaustion(reasonTimeMissing)
		ProjectCachedRead(&doc, trust.Contexts, trust.Registry, 30*time.Minute, true, at.Add(30*time.Minute+time.Nanosecond))
		assertExhaustionNotice(t, doc.Items[0], reasonTimeMissing, false)
		if doc.Items[0].CollectionFreshness == nil || doc.Items[0].CollectionFreshness.State != FreshStale {
			t.Fatalf("stale acquisition %+v", doc.Items[0].CollectionFreshness)
		}
	})
	t.Run("max age unset", func(t *testing.T) {
		doc, trust := base()
		doc.Items[0].CurrentMeasures[0].Reason = appendReportedExhaustion(reasonTimeMissing)
		ProjectCachedRead(&doc, trust.Contexts, trust.Registry, 30*time.Minute, false, at.Add(time.Minute))
		assertExhaustionNotice(t, doc.Items[0], reasonTimeMissing, false)
	})
	t.Run("future receipt", func(t *testing.T) {
		doc, trust := base()
		futureReceipt := at.Add(3 * time.Hour).Format(time.RFC3339Nano)
		doc.Items[0].ReceivedAt = futureReceipt
		doc.Items[0].CurrentMeasures[0].Reason = appendReportedExhaustion(reasonTimeMissing)
		ProjectCachedRead(&doc, trust.Contexts, trust.Registry, 30*time.Minute, true, at.Add(time.Minute))
		if doc.Items[0].ReceivedAt != futureReceipt || doc.Items[0].Observation.ProviderObservedAt != nil {
			t.Fatal("future receipt or provider clock was rewritten")
		}
		assertExhaustionNotice(t, doc.Items[0], reasonTimeMissing, false)
	})
	t.Run("future capture", func(t *testing.T) {
		doc, trust := base()
		doc.Items[0].Observation.CapturedAt = at.Add(time.Hour).Format(time.RFC3339Nano)
		doc.Items[0].CurrentMeasures[0].Reason = appendReportedExhaustion("unrelated capture note")
		ProjectCachedRead(&doc, trust.Contexts, trust.Registry, 30*time.Minute, true, at.Add(time.Minute))
		assertExhaustionNotice(t, doc.Items[0], "unrelated capture note", false)
	})
	t.Run("missing reset stays unknown", func(t *testing.T) {
		doc, trust := claudeExhaustionDoc(t, []ClaudeUsageLimit{claudeExhaustionLimit("weekly_all", "100", nil, nil)}, at)
		ProjectCachedRead(&doc, trust.Contexts, trust.Registry, 30*time.Minute, true, at.Add(time.Minute))
		window := doc.Items[0].Observation.Measures[0].Window
		if window.ResetAt != nil || window.ResetPrecision != ResetUnknown {
			t.Fatalf("missing reset was invented: %+v", window)
		}
		assertExhaustionNotice(t, doc.Items[0], reasonTimeMissing, true)
	})
	t.Run("reset after now", func(t *testing.T) {
		doc, trust := base()
		ProjectCachedRead(&doc, trust.Contexts, trust.Registry, 30*time.Minute, true, at.Add(time.Minute))
		assertExhaustionNotice(t, doc.Items[0], reasonTimeMissing, true)
	})
	now := at.Add(time.Minute)
	exact := now.Format(time.RFC3339)
	t.Run("reset at now", func(t *testing.T) {
		doc, trust := claudeExhaustionDoc(t, []ClaudeUsageLimit{claudeExhaustionLimit("weekly_all", "100", &exact, nil)}, at)
		doc.Items[0].CurrentMeasures[0].Reason = appendReportedExhaustion(reasonTimeMissing)
		ProjectCachedRead(&doc, trust.Contexts, trust.Registry, 30*time.Minute, true, now)
		assertExhaustionNotice(t, doc.Items[0], reasonTimeMissing, false)
	})
	before := now.Add(-time.Second).Format(time.RFC3339)
	t.Run("reset before now", func(t *testing.T) {
		doc, trust := claudeExhaustionDoc(t, []ClaudeUsageLimit{claudeExhaustionLimit("weekly_all", "100", &before, nil)}, at)
		doc.Items[0].CurrentMeasures[0].Reason = appendReportedExhaustion(reasonTimeMissing)
		ProjectCachedRead(&doc, trust.Contexts, trust.Registry, 30*time.Minute, true, now)
		assertExhaustionNotice(t, doc.Items[0], reasonTimeMissing, false)
	})
}

func TestProjectCachedReadExhaustionRejectsUnqualifiedEvidence(t *testing.T) {
	at := claudeExhaustionAt()
	reset := at.Add(2 * time.Hour).Format(time.RFC3339)
	model := ClaudeUsageLabel{DisplayName: "Opus"}
	scope := &ClaudeUsageScope{Model: &model}
	cases := []struct {
		name   string
		limits []ClaudeUsageLimit
	}{
		{"ninety nine point nine", []ClaudeUsageLimit{claudeExhaustionLimit("weekly_all", "99.9", &reset, nil)}},
		{"missing limits", nil},
		{"scoped model", []ClaudeUsageLimit{claudeExhaustionLimit("weekly_all", "100", &reset, scope)}},
		{"advisory beside exhaustion", []ClaudeUsageLimit{claudeExhaustionLimit("weekly_all", "100", &reset, nil), claudeExhaustionLimit("weekly_all", "100", &reset, scope)}},
		{"conflicting session", []ClaudeUsageLimit{claudeExhaustionLimit("weekly_all", "100", &reset, nil), claudeExhaustionLimit("session", "99.9", &reset, nil)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc, trust := claudeExhaustionDoc(t, tc.limits, at)
			for i := range doc.Items[0].CurrentMeasures {
				doc.Items[0].CurrentMeasures[i].Reason = appendReportedExhaustion(doc.Items[0].CurrentMeasures[i].Reason)
			}
			before := append([]byte(nil), mustJSON(t, doc.Items[0].Observation)...)
			ProjectCachedRead(&doc, trust.Contexts, trust.Registry, 30*time.Minute, true, at.Add(time.Minute))
			if string(mustJSON(t, doc.Items[0].Observation)) != string(before) {
				t.Fatal("unqualified observation was rewritten")
			}
			for _, current := range doc.Items[0].CurrentMeasures {
				if strings.Contains(current.Reason, reportedExhaustionNotice) || current.State == CurrentKnown || current.Value.Decimal != nil {
					t.Fatalf("unqualified evidence became a notice or current capacity: %+v", current)
				}
			}
		})
	}
	t.Run("both account windows", func(t *testing.T) {
		doc, trust := claudeExhaustionDoc(t, []ClaudeUsageLimit{claudeExhaustionLimit("weekly_all", "100", &reset, nil), claudeExhaustionLimit("session", "100.0", &reset, nil)}, at)
		ProjectCachedRead(&doc, trust.Contexts, trust.Registry, 30*time.Minute, true, at.Add(time.Minute))
		if len(doc.Items[0].CurrentMeasures) != 2 {
			t.Fatalf("measures dropped: %+v", doc.Items[0].CurrentMeasures)
		}
		assertExhaustionNotice(t, doc.Items[0], reasonTimeMissing, true)
	})
}

func TestProjectCachedReadExhaustionDoesNotSurviveTrustOrPaging(t *testing.T) {
	at := claudeExhaustionAt()
	reset := at.Add(2 * time.Hour).Format(time.RFC3339)
	doc, trust := claudeExhaustionDoc(t, []ClaudeUsageLimit{claudeExhaustionLimit("weekly_all", "100", &reset, nil)}, at)
	doc.Items[0].CurrentMeasures[0].Reason = appendReportedExhaustion("keep this primary")
	paged := doc
	cursor := "next"
	paged.NextCursor = &cursor
	ProjectCachedRead(&paged, trust.Contexts, trust.Registry, 30*time.Minute, true, at.Add(time.Minute))
	if paged.NextCursor == nil || *paged.NextCursor != "next" || strings.Contains(paged.Items[0].CurrentMeasures[0].Reason, reportedExhaustionNotice) || paged.Items[0].CurrentMeasures[0].Reason != "keep this primary" {
		t.Fatalf("paged notice or document changed: %+v", paged.Items[0].CurrentMeasures[0])
	}
	ProjectCachedRead(nil, trust.Contexts, trust.Registry, 30*time.Minute, true, at.Add(time.Minute))

	mismatches := []struct {
		name   string
		mutate func(*TrustedContext, RegistryParts)
	}{
		{"account", func(ctx *TrustedContext, _ RegistryParts) { ctx.AccountRef = stringRef("anthropic-org-sha256:" + strings.Repeat("d", 64)) }},
		{"workspace", func(ctx *TrustedContext, _ RegistryParts) { ctx.WorkspaceRef = stringRef("other-workspace") }},
		{"source", func(ctx *TrustedContext, _ RegistryParts) { ctx.SourceVersion = "other-source" }},
		{"incarnation", func(ctx *TrustedContext, _ RegistryParts) { ctx.ProducerIncarnation = "other-incarnation" }},
		{"generation", func(ctx *TrustedContext, _ RegistryParts) { ctx.BindingGeneration = 4 }},
		{"duplicate context", func(ctx *TrustedContext, _ RegistryParts) {}},
	}
	for _, tc := range mismatches {
		t.Run(tc.name, func(t *testing.T) {
			held, base := claudeExhaustionDoc(t, []ClaudeUsageLimit{claudeExhaustionLimit("weekly_all", "100", &reset, nil)}, at)
			held.Items[0].CurrentMeasures[0].Reason = appendReportedExhaustion("keep this primary")
			ctx := base.Contexts[0]
			parts := snapshotRegistry(base.Registry)
			tc.mutate(&ctx, parts)
			contexts := []TrustedContext{ctx}
			if tc.name == "duplicate context" {
				contexts = append(contexts, ctx)
			}
			ProjectCachedRead(&held, contexts, NewStaticRegistry(parts), 30*time.Minute, true, at.Add(time.Minute))
			if strings.Contains(held.Items[0].CurrentMeasures[0].Reason, reportedExhaustionNotice) || held.Items[0].CurrentMeasures[0].Reason != "keep this primary" {
				t.Fatalf("%s kept the notice: %q", tc.name, held.Items[0].CurrentMeasures[0].Reason)
			}
			if held.Items[0].Observation.ProviderObservedAt != nil || held.Items[0].CurrentMeasures[0].Value.Decimal != nil {
				t.Fatal("mismatch promoted capacity or a provider clock")
			}
		})
	}
}

func TestProjectCachedReadLeavesOtherProvidersUnnoticed(t *testing.T) {
	obs, ctx, reg := repairAllowanceFixture(t)
	doc := repairRead(obs, obs.CapturedAt, "2026-11-02T03:05:00Z", *obs.ProviderObservedAt, CurrentMeasure{
		MeasureID: "window", State: CurrentKnown, Value: MeasureValue{State: ValueKnown, Decimal: repairStr("55")}, Reason: appendReportedExhaustion("window still open"),
	})
	ProjectCachedRead(&doc, []TrustedContext{ctx}, reg, 30*time.Minute, true, time.Date(2026, 11, 2, 3, 10, 0, 0, time.UTC))
	got := doc.Items[0].CurrentMeasures[0]
	if strings.Contains(got.Reason, reportedExhaustionNotice) || got.Reason != "window still open" || got.State != CurrentKnown || got.Value.Decimal == nil || *got.Value.Decimal != "55" {
		t.Fatalf("other provider changed: %+v", got)
	}
}

func claudeExhaustionAt() time.Time {
	return time.Date(2026, 11, 2, 18, 2, 10, 0, time.UTC)
}

func claudeExhaustionLimit(kind, percent string, reset *string, scope *ClaudeUsageScope) ClaudeUsageLimit {
	group := "weekly"
	if kind == "session" {
		group = "session"
	}
	return ClaudeUsageLimit{Kind: kind, Group: group, Percent: json.Number(percent), ResetsAt: reset, Scope: scope, Severity: "normal", IsActive: true}
}

func claudeExhaustionDoc(t *testing.T, limits []ClaudeUsageLimit, at time.Time) (ReadResponse, AggregateTrust) {
	t.Helper()
	account := "anthropic-org-sha256:" + strings.Repeat("c", 64)
	received := at.Format(time.RFC3339Nano)
	payload, err := json.Marshal(ClaudeUsageCapture{Schema: ClaudeSDKCaptureSchema, ReceivedAt: received, Limits: limits})
	if err != nil {
		t.Fatal(err)
	}
	entry := captureEntry{Provider: "anthropic", ProducerRef: "official-fixture", ProducerIncarnation: "i-1", Sequence: 1, CapturedAt: received, ReceivedAt: received, Payload: payload}
	bundle, err := json.Marshal(map[string]any{"schema": CaptureBundleSchema, "captures": []captureEntry{entry}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := TrustedContext{Provider: "anthropic", ProducerRef: entry.ProducerRef, ProducerIncarnation: entry.ProducerIncarnation, BindingGeneration: 0, AccountRef: &account, BindingState: BindingVerified, SourceKind: SourceNativeRPC, SourceVersion: ClaudeSDKSourceVersion}
	fields := map[string]DirectFieldRule{}
	var metrics []MetricRule
	min, max := "0", "100"
	for i, row := range limits {
		id := "claude-sdk-" + strconv.Itoa(i)
		for _, name := range []string{"kind", "group", "severity"} {
			fields[id+"-"+name] = DirectFieldRule{OriginalType: TypeString, Class: ClassDisplay}
		}
		fields[id+"-active"] = DirectFieldRule{OriginalType: TypeBoolean, Class: ClassDisplay}
		if row.Scope != nil && row.Scope.Model != nil {
			fields[id+"-model"] = DirectFieldRule{OriginalType: TypeString, Class: ClassDisplay}
		}
		category := CategoryIncluded
		if row.Scope != nil || (row.Kind != "weekly_all" && row.Kind != "session") {
			category = CategoryAdvisory
		}
		metrics = append(metrics, MetricRule{
			ValidationRef: "claude-sdk:" + id + "-used-percent", Provider: "anthropic", SourceKind: SourceNativeRPC, SourceVersion: ClaudeSDKSourceVersion,
			Category: category, Unit: UnitPercent, Direction: DirectionUsed, Windows: []string{id + "-" + row.Kind},
			Min: &min, Max: &max, HistoricalOnly: true, ComplementOf: "100",
		})
	}
	trust := AggregateTrust{Contexts: []TrustedContext{ctx}, Registry: NewStaticRegistry(RegistryParts{
		Sources:  []SourceRule{{Provider: "anthropic", Kind: SourceNativeRPC, Version: ClaudeSDKSourceVersion, CapabilityRef: "claude-sdk:usage-report", AllowNormalizedMeasures: true, AllowDerivations: true, DirectFields: fields}},
		Accounts: []AccountRule{{AccountRef: account, AccountScopeOnly: true, ProducerRef: entry.ProducerRef, ProducerIncarnation: entry.ProducerIncarnation}},
		Metrics:  metrics,
	}), MaxAge: 1800}
	doc, err := AssembleCaptureBundle(bundle, trust, at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	return doc, trust
}

func assertExhaustionNotice(t *testing.T, item ReadItem, primary string, on bool) {
	t.Helper()
	want := primary
	if on {
		want = appendReportedExhaustion(primary)
	}
	if len(item.CurrentMeasures) == 0 {
		t.Fatal("no current measure")
	}
	for _, current := range item.CurrentMeasures {
		if current.Reason != want || current.State != CurrentUnknown || current.Value.State != ValueUnknown || current.Value.Decimal != nil {
			t.Fatalf("current %+v, want reason %q and unknown capacity", current, want)
		}
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
