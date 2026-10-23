package agentrun

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func TestShowCapturedUsage(t *testing.T) {
	h := newHarness(t)
	before := len(h.api.requests())
	h.mustRun(ExitUsage, "show", "parent", "--captured-usage")
	h.mustRun(ExitUsage, "show", "parent", "--captured-usage", "--once", "--json")
	if len(h.api.requests()) != before {
		t.Fatal("refused show called the API")
	}
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.Header.Get(apiv1.UsageCaptureHeader) != "" {
			t.Errorf("ordinary show sent %s", r.Header.Get(apiv1.UsageCaptureHeader))
		}
		if r.Header.Get(apiv1.AttributionHeader) != apiv1.AttributionProvider {
			t.Errorf("attribution %q", r.Header.Get(apiv1.AttributionHeader))
		}
		writeTestJSON(w, http.StatusOK, apiv1.Session{Name: "parent", Phase: "Running"})
	}
	h.mustRun(ExitOK, "show", "parent", "-o", "json")
	if strings.Contains(h.stdout.String(), "capturedUsage") {
		t.Fatalf("legacy show included capture: %s", h.stdout.String())
	}
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.Header.Get(apiv1.UsageCaptureHeader) != apiv1.UsageCaptureDirectV1 || r.Header.Get(apiv1.AttributionHeader) != apiv1.AttributionProvider {
			t.Errorf("headers capture %q attribution %q", r.Header.Get(apiv1.UsageCaptureHeader), r.Header.Get(apiv1.AttributionHeader))
		}
		writeTestJSON(w, http.StatusOK, apiv1.Session{Name: "parent", Phase: "Running"})
	}
	h.mustRun(ExitOK, "show", "parent", "--captured-usage", "-o", "json")
	if strings.Contains(h.stdout.String(), `"receipts"`) || !strings.Contains(h.stdout.String(), `"reason": "unavailable"`) {
		t.Fatalf("old server view: %s", h.stdout.String())
	}
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		writeTestJSON(w, http.StatusOK, validCaptureSession())
	}
	h.mustRun(ExitOK, "show", "parent", "--captured-usage", "-o", "json")
	if !strings.Contains(h.stdout.String(), `"amount": 0`) || !strings.Contains(h.stdout.String(), `"child"`) {
		t.Fatalf("valid capture view: %s", h.stdout.String())
	}
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		sess := validCaptureSession()
		sess.CapturedUsage.Receipts[0].Key = strings.Repeat("a", 64)
		sess.CapturedUsage.Receipts[0].ChildName = "hidden-child"
		writeTestJSON(w, http.StatusOK, sess)
	}
	h.mustRun(ExitOK, "show", "parent", "--captured-usage", "-o", "json")
	if strings.Contains(h.stdout.String(), "hidden-child") || strings.Contains(h.stdout.String(), `"receipts"`) || !strings.Contains(h.stdout.String(), `"malformed"`) {
		t.Fatalf("malformed view was printed: %s", h.stdout.String())
	}
	if len(h.api.requests()) != before+4 {
		t.Fatalf("requests %d", len(h.api.requests()))
	}
}

func validCaptureSession() apiv1.Session {
	parent, child := "parent-uid", "child-uid"
	src := protocol.UsageSource{SessionUID: child, PodUID: "pod-uid", Provider: protocol.AgentClaude, RequestedModel: "claude-opus-5-5", AttemptID: strings.Repeat("ab", 32)}
	obs := protocol.UnknownUsage()
	obs.Source = src
	obs.ID = protocol.UsageObservationID(src)
	obs.ObservedAt = time.Date(2026, 10, 23, 1, 0, 0, 0, time.UTC)
	obs.ResultState = "Final"
	zero := int64(0)
	obs.InputTokens = protocol.TokenObservation{State: protocol.UsageReported, Amount: &zero}
	obs.Cost = protocol.CostObservation{State: protocol.UsageReported, Amount: "0", Unit: "USD", Kind: protocol.UsageCLIEstimate}
	row := apiv1.CapturedUsageReceipt{Key: apiv1.CapturedUsageKey(parent, child, src.PodUID, src.AttemptID, obs.ID), ParentUID: parent, ChildName: "child", CapturedAt: obs.ObservedAt, Observation: &obs}
	view := &apiv1.CapturedUsage{Available: true, Scope: apiv1.CapturedUsageScope, Coverage: apiv1.CapturedUsageCoverage, Retention: apiv1.CapturedUsageRetention, Receipts: []apiv1.CapturedUsageReceipt{row}}
	return apiv1.Session{Name: "parent", UID: parent, Phase: "Running", CapturedUsage: view}
}
