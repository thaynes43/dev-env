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
	for _, percent := range []string{"100", "100.0", "100.00", "100.000"} {
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
		{"near one hundred", []ClaudeUsageLimit{claudeExhaustionLimit("weekly_all", "99.999999999999999999999999", &reset, nil)}},
		{"missing limits", nil},
		{"scoped model alone", []ClaudeUsageLimit{claudeExhaustionLimit("weekly_all", "100", &reset, scope)}},
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
		assertKindNotice(t, doc.Items[0], "weekly_all", true)
		assertKindNotice(t, doc.Items[0], "session", true)
	})
}

func TestProjectCachedReadExhaustionLabelsEachWindow(t *testing.T) {
	at := claudeExhaustionAt()
	reset := at.Add(2 * time.Hour).Format(time.RFC3339)
	passed := at.Format(time.RFC3339)
	model := ClaudeUsageLabel{DisplayName: "Opus"}
	scope := &ClaudeUsageScope{Model: &model}
	now := at.Add(time.Minute)
	cases := []struct {
		name    string
		limits  []ClaudeUsageLimit
		weekly  bool
		session bool
	}{
		{"weekly then session", []ClaudeUsageLimit{claudeExhaustionLimit("weekly_all", "100", &reset, nil), claudeExhaustionLimit("session", "20", &reset, nil)}, true, false},
		{"session then weekly", []ClaudeUsageLimit{claudeExhaustionLimit("session", "20", &reset, nil), claudeExhaustionLimit("weekly_all", "100.00", &reset, nil)}, true, false},
		{"session exhausted", []ClaudeUsageLimit{claudeExhaustionLimit("session", "100.000", &reset, nil), claudeExhaustionLimit("weekly_all", "20", &reset, nil)}, false, true},
		{"sibling reset passed", []ClaudeUsageLimit{claudeExhaustionLimit("weekly_all", "100", &reset, nil), claudeExhaustionLimit("session", "100", &passed, nil)}, true, false},
		{"advisory sibling", []ClaudeUsageLimit{claudeExhaustionLimit("weekly_all", "100", &reset, nil), claudeExhaustionLimit("weekly_all", "0", &reset, scope)}, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc, trust := claudeExhaustionDoc(t, tc.limits, at)
			before := string(mustJSON(t, doc.Items[0].Observation))
			ProjectCachedRead(&doc, trust.Contexts, trust.Registry, 30*time.Minute, true, now)
			if string(mustJSON(t, doc.Items[0].Observation)) != before || doc.Items[0].Observation.ProviderObservedAt != nil {
				t.Fatal("mixed window rewrote the observation or provider clock")
			}
			if tc.name == "advisory sibling" {
				assertMeasureNotice(t, doc.Items[0], measureIDByKind(t, doc.Items[0], "weekly_all", false), true)
				assertMeasureNotice(t, doc.Items[0], measureIDByKind(t, doc.Items[0], "weekly_all", true), false)
				return
			}
			assertKindNotice(t, doc.Items[0], "weekly_all", tc.weekly)
			assertKindNotice(t, doc.Items[0], "session", tc.session)
		})
	}
	t.Run("same measure conflict", func(t *testing.T) {
		doc, trust := claudeExhaustionDoc(t, []ClaudeUsageLimit{claudeExhaustionLimit("weekly_all", "100", &reset, nil), claudeExhaustionLimit("weekly_all", "20", &reset, nil)}, at)
		ProjectCachedRead(&doc, trust.Contexts, trust.Registry, 30*time.Minute, true, now)
		for _, current := range doc.Items[0].CurrentMeasures {
			if strings.Contains(current.Reason, reportedExhaustionNotice) || current.Value.Decimal != nil || current.State == CurrentKnown {
				t.Fatalf("same-measure conflict was labelled: %+v", current)
			}
		}
	})
	t.Run("conflict blocked weekly still suppresses its sibling", func(t *testing.T) {
		doc, trust := claudeExhaustionDoc(t, []ClaudeUsageLimit{claudeExhaustionLimit("weekly_all", "100", &reset, nil), claudeExhaustionLimit("weekly_all", "20", &reset, nil)}, at)
		item := &doc.Items[0]
		if len(item.CurrentMeasures) != 2 || len(item.Observation.Measures) != 2 || item.CurrentMeasures[1].Value.State != ValueUnknown || item.CurrentMeasures[1].Value.Decimal != nil {
			t.Fatalf("precondition %+v", item.CurrentMeasures)
		}
		if item.Observation.Measures[0].Value.Decimal == nil || *item.Observation.Measures[0].Value.Decimal != "100" || item.Observation.Measures[1].Value.Decimal == nil || *item.Observation.Measures[1].Value.Decimal != "20" || item.Observation.Measures[1].ID != item.CurrentMeasures[1].MeasureID {
			t.Fatal("rows are not weekly_all 100 then weekly_all 20")
		}
		item.CurrentMeasures[1].State = CurrentConflict
		before := string(mustJSON(t, item.Observation))
		ProjectCachedRead(&doc, trust.Contexts, trust.Registry, 30*time.Minute, true, now)
		if string(mustJSON(t, doc.Items[0].Observation)) != before || doc.Items[0].Observation.ProviderObservedAt != nil {
			t.Fatal("conflict projection rewrote the observation or provider clock")
		}
		if doc.Items[0].CurrentMeasures[1].State != CurrentConflict || doc.Items[0].CurrentMeasures[1].Value.State != ValueUnknown || doc.Items[0].CurrentMeasures[1].Value.Decimal != nil {
			t.Fatalf("conflict row changed value %+v", doc.Items[0].CurrentMeasures[1])
		}
		for _, current := range doc.Items[0].CurrentMeasures {
			if strings.Contains(current.Reason, reportedExhaustionNotice) || current.Value.Decimal != nil || current.State == CurrentKnown {
				t.Fatalf("conflict-blocked weekly sibling was labelled: %+v", current)
			}
		}
	})
	t.Run("session conflict does not veto weekly", func(t *testing.T) {
		doc, trust := claudeExhaustionDoc(t, []ClaudeUsageLimit{claudeExhaustionLimit("weekly_all", "100", &reset, nil), claudeExhaustionLimit("session", "100", &reset, nil)}, at)
		session := measureIDByKind(t, doc.Items[0], "session", false)
		for i := range doc.Items[0].CurrentMeasures {
			if doc.Items[0].CurrentMeasures[i].MeasureID != session {
				continue
			}
			if doc.Items[0].CurrentMeasures[i].Value.State != ValueUnknown || doc.Items[0].CurrentMeasures[i].Value.Decimal != nil {
				t.Fatalf("session precondition %+v", doc.Items[0].CurrentMeasures[i])
			}
			doc.Items[0].CurrentMeasures[i].State = CurrentConflict
		}
		before := string(mustJSON(t, doc.Items[0].Observation))
		ProjectCachedRead(&doc, trust.Contexts, trust.Registry, 30*time.Minute, true, now)
		if string(mustJSON(t, doc.Items[0].Observation)) != before {
			t.Fatal("session conflict rewrote the observation")
		}
		assertKindNotice(t, doc.Items[0], "weekly_all", true)
		current, ok := currentForMeasure(&doc.Items[0], session)
		if !ok || current.State != CurrentConflict || current.Value.Decimal != nil || strings.Contains(current.Reason, reportedExhaustionNotice) {
			t.Fatalf("session conflict vetoed or was labelled: %+v", current)
		}
	})
	t.Run("conflict row does not veto sibling", func(t *testing.T) {
		doc, trust := claudeExhaustionDoc(t, []ClaudeUsageLimit{claudeExhaustionLimit("weekly_all", "100", &reset, nil), claudeExhaustionLimit("session", "100", &reset, nil)}, at)
		weekly := measureIDByKind(t, doc.Items[0], "weekly_all", false)
		for i := range doc.Items[0].CurrentMeasures {
			if doc.Items[0].CurrentMeasures[i].MeasureID == weekly {
				doc.Items[0].CurrentMeasures[i].State = CurrentConflict
			}
		}
		ProjectCachedRead(&doc, trust.Contexts, trust.Registry, 30*time.Minute, true, now)
		assertMeasureNotice(t, doc.Items[0], weekly, false)
		assertKindNotice(t, doc.Items[0], "session", true)
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
		{"account", func(ctx *TrustedContext, _ RegistryParts) {
			ctx.AccountRef = stringRef("anthropic-org-sha256:" + strings.Repeat("d", 64))
		}},
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

func assertKindNotice(t *testing.T, item ReadItem, kind string, on bool) {
	t.Helper()
	assertMeasureNotice(t, item, measureIDByKind(t, item, kind, false), on)
}

func measureIDByKind(t *testing.T, item ReadItem, kind string, scoped bool) string {
	t.Helper()
	var found string
	n := 0
	for _, measure := range item.Observation.Measures {
		got, ok := directLexical(item.Observation, measure.ID+"-kind")
		if !ok || got != kind {
			continue
		}
		_, model := directLexical(item.Observation, measure.ID+"-model")
		if model != scoped {
			continue
		}
		found = measure.ID
		n++
	}
	if n != 1 {
		t.Fatalf("kind %s scoped=%t matched %d measures", kind, scoped, n)
	}
	return found
}

func assertMeasureNotice(t *testing.T, item ReadItem, id string, on bool) {
	t.Helper()
	current, ok := currentForMeasure(&item, id)
	if !ok {
		t.Fatalf("current row %s missing or duplicated", id)
	}
	want := reasonTimeMissing
	if on {
		want = appendReportedExhaustion(reasonTimeMissing)
	}
	if current.Reason != want || current.State == CurrentKnown || current.Value.Decimal != nil || current.Value.State != ValueUnknown {
		t.Fatalf("measure %s current %+v, want reason %q", id, current, want)
	}
	if on && strings.Count(current.Reason, reportedExhaustionNotice) != 1 {
		t.Fatalf("measure %s notice count: %q", id, current.Reason)
	}
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
