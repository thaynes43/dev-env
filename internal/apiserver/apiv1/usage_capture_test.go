package apiv1

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func TestCapturedUsageLegacyJSONOmitsTheField(t *testing.T) {
	var session Session
	if err := json.Unmarshal([]byte(`{"name":"parent","phase":"Running","createdAt":"2026-10-23T01:00:00Z"}`), &session); err != nil {
		t.Fatal(err)
	}
	if session.CapturedUsage != nil {
		t.Fatal("legacy session decoded a receipt view")
	}
	raw, err := json.Marshal(session)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "capturedUsage") || strings.Contains(string(raw), "usageCaptureParentUID") {
		t.Fatalf("legacy marshal added capture fields: %s", raw)
	}
}

func TestNormalizeCapturedUsageKeepsFactsAndDropsTheWholeView(t *testing.T) {
	parent, child := "parent-uid", "child-uid"
	row := validCaptureRow(parent, child)
	view := &CapturedUsage{Available: true, Scope: CapturedUsageScope, Coverage: CapturedUsageCoverage, Retention: CapturedUsageRetention, Receipts: []CapturedUsageReceipt{row}}
	got := NormalizeCapturedUsage(parent, view)
	if !got.Available || len(got.Receipts) != 1 || got.Receipts[0].Observation.InputTokens.Amount == nil || *got.Receipts[0].Observation.InputTokens.Amount != 0 || got.Receipts[0].Observation.OutputTokens.State != "Unknown" || got.Receipts[0].Observation.OutputTokens.Amount != nil || got.Receipts[0].Observation.Cost.Amount != "0" {
		t.Fatalf("valid zero/unknown view %+v", got)
	}
	if NormalizeCapturedUsage(parent, nil).Reason != "unavailable" || NormalizeCapturedUsage(parent, nil).Available {
		t.Fatal("nil view")
	}
	bad := *view
	bad.Receipts = append([]CapturedUsageReceipt{}, view.Receipts...)
	bad.Receipts[0].ChildName = "hidden-child"
	bad.Receipts[0].Key = strings.Repeat("a", 64)
	whole := NormalizeCapturedUsage(parent, &bad)
	raw, _ := json.Marshal(whole)
	if whole.Available || whole.Reason != "malformed" || len(whole.Receipts) != 0 || strings.Contains(string(raw), "hidden-child") {
		t.Fatalf("partial view kept: %s", raw)
	}
	codex := validCaptureRow(parent, "codex-uid")
	codex.Observation.Source.Provider = "codex"
	codex.Observation.Cost = protocol.CostObservation{State: protocol.UsageReported, Amount: "1", Unit: "USD", Kind: protocol.UsageCLIEstimate}
	codex.Observation.ID = protocol.UsageObservationID(codex.Observation.Source)
	codex.Key = CapturedUsageKey(parent, codex.Observation.Source.SessionUID, codex.Observation.Source.PodUID, codex.Observation.Source.AttemptID, codex.Observation.ID)
	if NormalizeCapturedUsage(parent, &CapturedUsage{Available: true, Scope: CapturedUsageScope, Coverage: CapturedUsageCoverage, Retention: CapturedUsageRetention, Receipts: []CapturedUsageReceipt{codex}}).Reason != "malformed" {
		t.Fatal("codex reported cost was accepted")
	}
	rows := make([]CapturedUsageReceipt, CapturedUsageMaxReceipts+1)
	for i := range rows {
		rows[i] = validCaptureRow(parent, fmt.Sprintf("child-%d", i))
	}
	if NormalizeCapturedUsage(parent, &CapturedUsage{Available: true, Scope: CapturedUsageScope, Coverage: CapturedUsageCoverage, Retention: CapturedUsageRetention, Receipts: rows}).Reason != "oversized" {
		t.Fatal("65 rows were not oversized")
	}
	closed := NormalizeCapturedUsage(parent, &CapturedUsage{Available: false, Reason: "disabled"})
	if closed.Available || closed.Reason != "disabled" || len(closed.Receipts) != 0 {
		t.Fatalf("disabled %+v", closed)
	}
}

func validCaptureRow(parent, child string) CapturedUsageReceipt {
	src := protocol.UsageSource{SessionUID: child, PodUID: "pod-uid", Provider: protocol.AgentClaude, RequestedModel: "claude-opus-5-5", AttemptID: strings.Repeat("ab", 32)}
	obs := protocol.UnknownUsage()
	obs.Source = src
	obs.ID = protocol.UsageObservationID(src)
	obs.ObservedAt = time.Date(2026, 10, 23, 1, 0, 0, 0, time.UTC)
	obs.ResultState = "Final"
	zero := int64(0)
	obs.InputTokens = protocol.TokenObservation{State: protocol.UsageReported, Amount: &zero}
	obs.Cost = protocol.CostObservation{State: protocol.UsageReported, Amount: "0", Unit: "USD", Kind: protocol.UsageCLIEstimate}
	return CapturedUsageReceipt{Key: CapturedUsageKey(parent, child, src.PodUID, src.AttemptID, obs.ID), ParentUID: parent, ChildName: "child", CapturedAt: obs.ObservedAt, Observation: &obs}
}
