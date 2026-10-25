package agentrun

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

const exportUIDA = "11111111-1111-1111-1111-111111111111"
const exportUIDB = "22222222-2222-2222-2222-222222222222"

func exportSession(name, uid string) apiv1.Session {
	s := testSession(name, "Running")
	s.UID, s.Prompt = uid, "CANARY private prompt must not escape"
	u := protocol.UnknownUsage()
	u.Source = protocol.UsageSource{SessionUID: uid, PodUID: "pod-uid", Provider: protocol.AgentClaude, RequestedModel: s.Model, AttemptID: strings.Repeat("a", 64)}
	u.ID, u.ObservedAt, u.ResultState = protocol.UsageObservationID(u.Source), testNow, "Final"
	s.UsageObservation = &u
	s.Conditions = []apiv1.Condition{{Type: "CANARY", Status: "True", Message: "CANARY condition must not escape"}}
	return s
}

func exportManifest(t *testing.T, h *harness, selections ...usageExportSelection) string {
	t.Helper()
	raw, err := json.Marshal(usageExportManifest{Selections: selections})
	if err != nil {
		t.Fatal(err)
	}
	return h.write("manifest.json", string(raw))
}

func TestUsageExportSelectedReadsArePrivateAndBounded(t *testing.T) {
	h := newHarness(t)
	manifest := exportManifest(t, h, usageExportSelection{Name: "selected-a", ExpectedUID: exportUIDA, Project: "project", Repository: "repo", Task: "task"})
	out := filepath.Join(h.dir, "private-export")
	s := exportSession("selected-a", exportUIDA)
	h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if r.Header.Get(apiv1.AttributionHeader) != apiv1.AttributionProvider || r.Header.Get(apiv1.UsageCaptureHeader) != apiv1.UsageCaptureDirectV1 {
			t.Errorf("missing attribution/capture headers: %q %q", r.Header.Get(apiv1.AttributionHeader), r.Header.Get(apiv1.UsageCaptureHeader))
		}
		writeTestJSON(w, http.StatusOK, s)
	}
	h.mustRun(ExitOK, "usage-export", "--manifest", manifest, "--output-dir", out)
	reqs := h.api.requests()
	if len(reqs) != 1 || reqs[0].method != http.MethodGet || reqs[0].path != apiv1.SessionPath("selected-a") || reqs[0].auth != "Bearer "+testToken || len(h.sleeps) != 0 {
		t.Fatal("export was not one bounded selected read", reqs, h.sleeps)
	}
	for _, name := range []string{"report.json", "summary.txt"} {
		info, err := os.Stat(filepath.Join(out, name))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatal("private output permission", name, info, err)
		}
	}
	info, err := os.Stat(out)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatal("private directory permission", info, err)
	}
	report, err := os.ReadFile(filepath.Join(out, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"CANARY private prompt", "CANARY condition"} {
		if strings.Contains(string(report), forbidden) || strings.Contains(h.stdout.String(), forbidden) || strings.Contains(h.stderr.String(), forbidden) {
			t.Fatal("private session content escaped", forbidden)
		}
	}
	if !strings.Contains(string(report), `"allowanceInput": "Unconfigured"`) || !strings.Contains(string(report), `"project": "project"`) || !strings.Contains(string(report), `"requestedModel"`) {
		t.Fatal("expected projected evidence missing", string(report))
	}
}

func TestUsageExportRejectsManifestBeforeConnecting(t *testing.T) {
	h := newHarness(t)
	manifest := h.write("bad-manifest.json", `{"selections":[{"name":"selected-a","expectedUID":"`+exportUIDA+`"},{"name":"selected-a","expectedUID":"`+exportUIDB+`"}]}`)
	envReads := 0
	h.env.Getenv = func(k string) string { envReads++; return h.vars[k] }
	out := filepath.Join(h.dir, "must-not-exist")
	h.mustRun(ExitUsage, "usage-export", "--manifest", manifest, "--output-dir", out)
	if envReads != 0 || len(h.api.requests()) != 0 || len(h.ran) != 0 {
		t.Fatal("invalid manifest connected", envReads, h.api.requests(), h.ran)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("invalid manifest created private output", err)
	}
}

func TestUsageExportRefusalStopsWithoutRawError(t *testing.T) {
	h := newHarness(t)
	manifest := exportManifest(t, h,
		usageExportSelection{Name: "selected-a", ExpectedUID: exportUIDA},
		usageExportSelection{Name: "selected-b", ExpectedUID: exportUIDB},
	)
	out := filepath.Join(h.dir, "private-export")
	h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		apiError(w, http.StatusForbidden, apiv1.CodeForbidden, "CANARY backend error body must not escape")
	}
	h.mustRun(ExitFailed, "usage-export", "--manifest", manifest, "--output-dir", out)
	if len(h.api.requests()) != 1 || len(h.sleeps) != 0 {
		t.Fatal("refusal retried or continued", h.api.requests(), h.sleeps)
	}
	report, err := os.ReadFile(filepath.Join(out, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(report), "CANARY backend") || strings.Contains(h.stdout.String(), "CANARY backend") || strings.Contains(h.stderr.String(), "CANARY backend") || !strings.Contains(string(report), `"selected_session_read"`) {
		t.Fatal("unsafe failure output", h.stdout.String(), h.stderr.String(), string(report))
	}
}

func TestUsageExportCaptureOverlapAndConflictRemainVisible(t *testing.T) {
	h := newHarness(t)
	manifest := exportManifest(t, h,
		usageExportSelection{Name: "selected-a", ExpectedUID: exportUIDA},
		usageExportSelection{Name: "selected-b", ExpectedUID: exportUIDB},
	)
	out := filepath.Join(h.dir, "private-export")
	parent, child := exportSession("selected-a", exportUIDA), exportSession("selected-b", exportUIDB)
	captured := *child.UsageObservation
	captured.ObservedAt = time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	parent.CapturedUsage = &apiv1.CapturedUsage{Available: true, Scope: apiv1.CapturedUsageScope, Coverage: apiv1.CapturedUsageCoverage, Retention: apiv1.CapturedUsageRetention, Receipts: []apiv1.CapturedUsageReceipt{{
		Key: apiv1.CapturedUsageKey(exportUIDA, exportUIDB, captured.Source.PodUID, captured.Source.AttemptID, captured.ID), ParentUID: exportUIDA, CapturedAt: captured.ObservedAt, Observation: &captured,
	}}}
	h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		switch r.URL.Path {
		case apiv1.SessionPath("selected-a"):
			writeTestJSON(w, http.StatusOK, parent)
		case apiv1.SessionPath("selected-b"):
			writeTestJSON(w, http.StatusOK, child)
		default:
			t.Fatal("unexpected route", r.URL.Path)
		}
	}
	h.mustRun(ExitOK, "usage-export", "--manifest", manifest, "--output-dir", out)
	report, err := os.ReadFile(filepath.Join(out, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc usageExportDocument
	if err := json.Unmarshal(report, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Selection.Reads) != 2 || len(doc.Selection.Reads[0].Capture.Receipts) != 1 || doc.Selection.Reads[0].Capture.Receipts[0].SourceUID != exportUIDB {
		t.Fatal("capture provenance not retained", doc.Selection)
	}
	if len(doc.Report.Attribution.Rows) != 1 || len(doc.Report.Attribution.WithheldConflicts) != 1 || doc.Report.Attribution.WithheldConflicts[0].SessionUID != exportUIDB {
		t.Fatal("direct/captured conflict should be withheld", doc.Report.Attribution)
	}
}

func TestUsageExportRefusesOverwriteAndSymlinkOutput(t *testing.T) {
	h := newHarness(t)
	manifest := exportManifest(t, h, usageExportSelection{Name: "selected-a", ExpectedUID: exportUIDA})
	existing := filepath.Join(h.dir, "existing")
	if err := os.Mkdir(existing, 0o700); err != nil {
		t.Fatal(err)
	}
	h.mustRun(ExitUsage, "usage-export", "--manifest", manifest, "--output-dir", existing)
	link := filepath.Join(h.dir, "link")
	if err := os.Symlink(h.dir, link); err != nil {
		t.Fatal(err)
	}
	h.mustRun(ExitUsage, "usage-export", "--manifest", manifest, "--output-dir", filepath.Join(link, "private-export"))
	if len(h.api.requests()) != 0 {
		t.Fatal("unsafe output connected", h.api.requests())
	}
}

func TestUsageExportPreservesProvidedAllowanceSnapshot(t *testing.T) {
	h := newHarness(t)
	manifest := exportManifest(t, h, usageExportSelection{Name: "selected-a", ExpectedUID: exportUIDA})
	fixture, err := os.ReadFile(filepath.Join("..", "providerallowance", "testdata", "read-interface-examples.json"))
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		HTTP struct {
			Body json.RawMessage `json:"body"`
		} `json:"http"`
	}
	if err := json.Unmarshal(fixture, &envelope); err != nil {
		t.Fatal(err)
	}
	allowance := h.write("allowance.json", string(envelope.HTTP.Body))
	out := filepath.Join(h.dir, "private-export")
	h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		writeTestJSON(w, http.StatusOK, exportSession("selected-a", exportUIDA))
	}
	h.mustRun(ExitOK, "usage-export", "--manifest", manifest, "--output-dir", out, "--allowance-read", allowance)
	report, err := os.ReadFile(filepath.Join(out, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(report), `"allowanceInput": "CallerProvidedValidatedSnapshots"`) || !strings.Contains(string(report), `"servedAt": "2026-10-24T17:00:00Z"`) || !strings.Contains(string(report), `"decimal": "0.36"`) {
		t.Fatal("allowance snapshot was changed or omitted", string(report))
	}
}
