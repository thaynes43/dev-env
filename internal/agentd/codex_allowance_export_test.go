package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/thaynes43/dev-env/internal/agentrun"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/providerallowance"
	"github.com/thaynes43/dev-env/internal/usageconsolidation"
)

// TestNativeCaptureRetainedByUsageExport is the capture-to-export seam.
// It uses the existing qualified-host capture and ReadCodexNativeAllowance,
// retains that document at mode 0600, and consumes it through agentrun.Run
// usage-export --allowance-read. The TLS server, token, and session are
// synthetic. This is not a live API, provider, HOME, scheduler, or current-source call.
func TestNativeCaptureRetainedByUsageExport(t *testing.T) {
	f := exportSeamHost(t)
	s, o, d, _ := captureFixture(t)
	s.CodexHome = f.settings.CodexHome
	bin := filepath.Join(s.CodexHome, "packages", "app-server-daemon", "current", "bin", "codex")
	if err := os.MkdirAll(filepath.Dir(bin), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	live := codexHostProcess{State: "Live", PID: os.Getpid(), Identity: codexHostIdentity{BootID: "fixture", StartTicks: 1}}
	d.host.process = func(Settings) (codexHostProcess, error) { return live, nil }
	d.lifecycle = func(Settings) (codexHostLifecycle, error) {
		return codexHostLifecycle{Phase: "Confirmed", PodUID: s.PodUID, WorkspaceID: s.WorkspaceID, Process: live}, nil
	}
	d.read = ReadCodexNativeAllowance
	d.nonce = func() (string, error) { return "seamnonce", nil }

	captured, err := captureCodexNativeAllowance(context.Background(), s, o, d)
	if err != nil {
		t.Fatal(err)
	}
	calls := f.recorded()
	wantMethods := []string{"initialize", "initialized", "account/read", "account/rateLimits/read"}
	if len(calls) != len(wantMethods) {
		t.Fatalf("methods %+v", calls)
	}
	for i, method := range wantMethods {
		if calls[i].Method != method {
			t.Fatalf("methods %+v", calls)
		}
	}
	if !reflect.DeepEqual(calls[2].Params, map[string]any{"refreshToken": false}) || !reflect.DeepEqual(calls[3].Params, map[string]any{"excludeResetCreditDetails": true, "supportsLunaReserve": false}) {
		t.Fatal("unexpected allowance RPC parameters")
	}

	root := t.TempDir()
	allowancePath := filepath.Join(root, "retained-allowance.json")
	allowanceFile, err := os.OpenFile(allowancePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(allowanceFile).Encode(captured); err != nil {
		_ = allowanceFile.Close()
		t.Fatal(err)
	}
	if err := allowanceFile.Close(); err != nil {
		t.Fatal(err)
	}
	allowanceInfo, err := os.Stat(allowancePath)
	if err != nil || !allowanceInfo.Mode().IsRegular() || allowanceInfo.Mode().Perm() != 0o600 {
		t.Fatal("retained allowance mode", allowanceInfo, err)
	}
	allowanceRaw, err := os.ReadFile(allowancePath)
	if err != nil {
		t.Fatal(err)
	}
	retained := decodeExportRead(t, allowanceRaw)
	assertExportSeamRead(t, retained)
	forbidExportText(t, "retained allowance", string(allowanceRaw))

	const (
		sessionName = "selected-a"
		sessionUID  = "11111111-1111-1111-1111-111111111111"
		token       = "synthetic-seam-token"
	)
	manifestPath := filepath.Join(root, "manifest.json")
	manifestRaw, err := json.Marshal(map[string]any{"selections": []map[string]string{{
		"name": sessionName, "expectedUID": sessionUID, "project": "synthetic-project", "repository": "synthetic-repo", "task": "synthetic-task",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, manifestRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	tokenPath := filepath.Join(root, "api-token")
	if err := os.WriteFile(tokenPath, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var requestMu sync.Mutex
	var requests []struct{ method, path, auth string }
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestMu.Lock()
		requests = append(requests, struct{ method, path, auth string }{r.Method, r.URL.Path, r.Header.Get("Authorization")})
		requestMu.Unlock()
		if r.Method != http.MethodGet || r.URL.Path != apiv1.SessionPath(sessionName) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(apiv1.Session{
			Name: sessionName, UID: sessionUID, Repo: "synthetic-repo", Agent: "codex", Mode: "task", Model: "synthetic-model",
			CreatedAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), Phase: "Running", Prompt: "SYNTHETIC_SESSION_PRIVATE",
		})
	}))
	t.Cleanup(srv.Close)
	caPath := filepath.Join(root, "api-ca.crt")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(root, "private-export")
	var stdout, stderr bytes.Buffer
	env := agentrun.Env{
		Stdout: &stdout, Stderr: &stderr, Stdin: strings.NewReader(""),
		Getenv: func(string) string { return "" },
		Now:    func() time.Time { return time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC) },
		Sleep: func(context.Context, time.Duration) error {
			t.Fatal("usage-export retried or slept")
			return errors.New("sleep")
		},
		NewKey:            func() string { return "synthetic-key" },
		SessionTokenFile:  filepath.Join(root, "absent-session-token"),
		ServiceAccountDir: filepath.Join(root, "absent-serviceaccount"),
		LookPath:          func(string) (string, error) { return "", errors.New("not found") },
		Run: func(context.Context, []string, io.Reader) (int, error) {
			t.Fatal("usage-export started a process")
			return 0, errors.New("exec")
		},
		Output: func(context.Context, []string) ([]byte, error) {
			t.Fatal("usage-export captured a process")
			return nil, errors.New("output")
		},
		ExecOutput: func(context.Context, []string) (int, string, string, error) {
			t.Fatal("usage-export started a process")
			return 0, "", "", errors.New("exec")
		},
		PortForward: func(context.Context, []string) (string, func(), error) {
			t.Fatal("usage-export opened a port-forward")
			return "", func() {}, errors.New("forward")
		},
	}
	code := agentrun.Run(context.Background(), []string{
		"usage-export", "--manifest", manifestPath, "--output-dir", outDir, "--allowance-read", allowancePath,
		"--api-url", srv.URL, "--token-file", tokenPath, "--ca-file", caPath,
	}, env)
	if code != agentrun.ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
	requestMu.Lock()
	gotRequests := append([]struct{ method, path, auth string }(nil), requests...)
	requestMu.Unlock()
	if len(gotRequests) != 1 || gotRequests[0].method != http.MethodGet || gotRequests[0].path != apiv1.SessionPath(sessionName) || gotRequests[0].auth != "Bearer "+token {
		t.Fatalf("export requests %+v", gotRequests)
	}
	dirInfo, err := os.Stat(outDir)
	if err != nil || dirInfo.Mode().Perm() != 0o700 {
		t.Fatal("export directory mode", dirInfo, err)
	}
	reportRaw, err := os.ReadFile(filepath.Join(outDir, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	summaryRaw, err := os.ReadFile(filepath.Join(outDir, "summary.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"report.json", "summary.txt"} {
		info, err := os.Stat(filepath.Join(outDir, name))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatal("export file mode", name, info, err)
		}
	}
	for _, item := range []struct{ label, text string }{
		{"report", string(reportRaw)}, {"summary", string(summaryRaw)}, {"stdout", stdout.String()}, {"stderr", stderr.String()},
	} {
		forbidExportText(t, item.label, item.text)
	}
	var doc struct {
		Report    usageconsolidation.Report `json:"report"`
		Selection struct {
			AllowanceInput string `json:"allowanceInput"`
			Reads          []struct {
				DirectObservation string `json:"directObservation"`
				Capture           struct {
					State string `json:"state"`
				} `json:"capture"`
			} `json:"reads"`
		} `json:"selection"`
	}
	if err := json.Unmarshal(reportRaw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Selection.AllowanceInput != "CallerProvidedSnapshots" || len(doc.Selection.Reads) != 1 || doc.Selection.Reads[0].DirectObservation != "Unavailable" || doc.Selection.Reads[0].Capture.State != "Unavailable" {
		t.Fatalf("selection %#v", doc.Selection)
	}
	if doc.Report.Allowance.CurrentnessAtReportGeneration != usageconsolidation.Unknown || doc.Report.Allowance.AvailabilityRecommendation != usageconsolidation.Unknown || len(doc.Report.Allowance.Snapshots) != 1 || len(doc.Report.Attribution.Rows) != 0 || len(doc.Report.Attribution.WithheldConflicts) != 0 {
		t.Fatalf("report allowance/attribution %#v %#v", doc.Report.Allowance.CurrentnessAtReportGeneration, doc.Report.Attribution)
	}
	if strings.Contains(string(reportRaw), "inputTokens") || strings.Contains(string(reportRaw), "outputTokens") || strings.Contains(string(summaryRaw), "allowance input is Unconfigured") {
		t.Fatal("export aggregated tokens or dropped the supplied snapshot")
	}
	for _, want := range []string{"allowance snapshots: CallerProvidedSnapshots", "allowance currentness: Unknown", "attribution rows: 0"} {
		if !strings.Contains(string(summaryRaw), want) {
			t.Fatalf("summary missing %q\n%s", want, summaryRaw)
		}
	}
	exportedRaw, err := json.Marshal(doc.Report.Allowance.Snapshots[0])
	if err != nil {
		t.Fatal(err)
	}
	exported := decodeExportRead(t, exportedRaw)
	if !reflect.DeepEqual(retained, exported) {
		left, _ := json.Marshal(retained)
		right, _ := json.Marshal(exported)
		t.Fatalf("usage-export changed the retained read\nretained %s\nexported %s", left, right)
	}
}

func exportSeamHost(t *testing.T) *allowanceFixture {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "allowance-export-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	dir := filepath.Join(home, "app-server-control")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(dir, "app-server-control.sock"))
	if err != nil {
		t.Fatal(err)
	}
	f := &allowanceFixture{settings: Settings{CodexHome: home}}
	window := func(used, mins, reset string) map[string]any {
		return map[string]any{"usedPercent": json.Number(used), "windowDurationMins": json.Number(mins), "resetsAt": json.Number(reset)}
	}
	limit := func(primary, secondary map[string]any) map[string]any {
		return map[string]any{"limitId": "SYNTHETIC_LIMIT_ID", "primary": primary, "secondary": secondary, "rateLimitReachedType": "SYNTHETIC_REACHED"}
	}
	server := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, e := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if e != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		for {
			var req allowanceRequest
			if conn.ReadJSON(&req) != nil {
				return
			}
			f.mu.Lock()
			f.calls = append(f.calls, req)
			f.mu.Unlock()
			if req.ID == nil {
				continue
			}
			var result any
			switch req.Method {
			case "initialize":
				result = map[string]any{"userAgent": "codex/" + managedCodexVersion + " fixture"}
			case "account/read":
				result = map[string]any{"account": map[string]string{"email": "SYNTHETIC_PRIVATE", "token": "SYNTHETIC_SECRET", "entitlement": "SYNTHETIC_ENTITLEMENT"}}
			case "account/rateLimits/read":
				result = map[string]any{
					"rateLimits": limit(window("44.5", "300", "1767225600"), window("55.75", "10080", "1767830400")),
					"rateLimitsByLimitId": map[string]any{
						"aaa":   limit(window("11.5", "300", "1767225600"), window("22.25", "10080", "1767830400")),
						"codex": limit(window("33", "60", "1767312000"), window("0", "1440", "1767398400")),
					},
					"rateLimitResetCredits": map[string]any{"availableCount": json.Number("4")},
				}
			default:
				return
			}
			if conn.WriteJSON(map[string]any{"id": *req.ID, "result": result}) != nil {
				return
			}
		}
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return f
}

func decodeExportRead(t *testing.T, raw []byte) providerallowance.ReadResponse {
	t.Helper()
	read, err := providerallowance.DecodeReadResponse(raw, providerallowance.FixtureParseLimits)
	if err != nil {
		t.Fatal(err)
	}
	return read
}

func assertExportSeamRead(t *testing.T, read providerallowance.ReadResponse) {
	t.Helper()
	const at = "2026-10-28T01:00:00Z"
	if read.Schema != providerallowance.SchemaRead || read.SnapshotRevision != "capture-seamnonce" || read.ServedAt != at || len(read.Items) != 1 || len(read.SourceStatuses) != 0 || read.NextCursor != nil {
		t.Fatalf("read envelope %#v", read)
	}
	item := read.Items[0]
	obs := item.Observation
	if obs.Schema != providerallowance.SchemaObservation || obs.ObservationID != "capture-seamnonce" || obs.ProducerRef != "agentd-on-demand" || obs.ProducerIncarnation != "capture-seamnonce" || obs.Sequence != 1 || obs.Provider != "openai" || obs.CapturedAt != at || obs.ProviderObservedAt != nil {
		t.Fatalf("observation identity %#v", obs)
	}
	if obs.Source.Kind != providerallowance.SourceNativeRPC || obs.Source.Version != "app-server-reported-"+managedCodexVersion || obs.Source.CapabilityRef != "story188:codex-native-field-schema" {
		t.Fatalf("source %#v", obs.Source)
	}
	if obs.Scope.BindingState != providerallowance.BindingUnknown || obs.Scope.AccountRef != nil || obs.Scope.WorkspaceRef != nil || obs.Scope.EntitlementRef != nil || !reflect.DeepEqual(obs.Scope.Products, []string{"codex"}) {
		t.Fatalf("scope %#v", obs.Scope)
	}
	if !reflect.DeepEqual(obs.EvidenceRefs, []string{"capture-seamnonce"}) || len(obs.Derivations) != 0 || obs.ErrorCode != nil || obs.Availability != providerallowance.AvailabilityAvailable || obs.Completeness != providerallowance.CompletenessPartial {
		t.Fatalf("observation coverage %#v", obs)
	}
	if item.ReceivedAt != at || item.Freshness.State != providerallowance.FreshUnknown || item.Freshness.AsOf != at || item.Freshness.Basis != providerallowance.BasisCapture || item.Freshness.PolicyRef != nil || item.Freshness.Reason != "Native account/rate read has no provider observation timestamp; fetch is not provider freshness" {
		t.Fatalf("freshness %#v", item.Freshness)
	}
	if item.Binding.State != providerallowance.BindingUnknown || item.Binding.Generation != 0 || item.Binding.AccountRef != nil || item.Binding.WorkspaceRef != nil {
		t.Fatalf("binding %#v", item.Binding)
	}
	if item.Validation.State != providerallowance.ValidationPartial || len(item.Validation.ValidationRefs) != 0 || len(item.CurrentMeasures) != 0 {
		t.Fatalf("validation/current %#v %#v", item.Validation, item.CurrentMeasures)
	}
	if len(obs.DirectFields) != 3 || obs.DirectFields[0].Field != "accountScopePresent" || obs.DirectFields[0].OriginalType != providerallowance.TypeBoolean || obs.DirectFields[0].LexicalValue == nil || *obs.DirectFields[0].LexicalValue != "true" {
		t.Fatal("account presence")
	}
	if obs.DirectFields[1].Field != "resetCredits.availableCount" || obs.DirectFields[1].OriginalType != providerallowance.TypeDecimal || obs.DirectFields[1].LexicalValue == nil || *obs.DirectFields[1].LexicalValue != "4" {
		t.Fatal("reset credit count")
	}
	if obs.DirectFields[2].Field != "resetCredits.detailsState" || obs.DirectFields[2].OriginalType != providerallowance.TypeString || obs.DirectFields[2].LexicalValue == nil || *obs.DirectFields[2].LexicalValue != "not_requested" {
		t.Fatal("reset credit details")
	}
	wants := []struct {
		id, decimal, reset string
		minutes            providerallowance.Count
	}{
		{"bucket.aaa.primary", "11.5", "2026-01-01T00:00:00Z", 300},
		{"bucket.aaa.secondary", "22.25", "2026-01-08T00:00:00Z", 10080},
		{"bucket.codex.primary", "33", "2026-01-02T00:00:00Z", 60},
		{"bucket.codex.secondary", "0", "2026-01-03T00:00:00Z", 1440},
		{"legacy.primary", "44.5", "2026-01-01T00:00:00Z", 300},
		{"legacy.secondary", "55.75", "2026-01-08T00:00:00Z", 10080},
	}
	if len(obs.Measures) != len(wants) {
		t.Fatalf("measures %#v", obs.Measures)
	}
	for i, want := range wants {
		m := obs.Measures[i]
		if m.ID != want.id || m.Category != providerallowance.CategoryIncluded || m.Unit != providerallowance.UnitPercent || m.Direction != providerallowance.DirectionUsed || m.Currency != nil || m.ValidationRef != nil || len(m.EvidenceFields) != 0 {
			t.Fatalf("measure %d meta %#v", i, m)
		}
		if m.Value.State != providerallowance.ValueKnown || m.Value.Decimal == nil || *m.Value.Decimal != want.decimal {
			t.Fatalf("measure %d value %#v", i, m.Value)
		}
		if m.Window.Ref != want.id+".window" || m.Window.DurationMinutes == nil || *m.Window.DurationMinutes != want.minutes || m.Window.StartAt != nil || m.Window.ResetAt == nil || *m.Window.ResetAt != want.reset || m.Window.ResetPrecision != providerallowance.ResetInstant {
			t.Fatalf("measure %d window %#v", i, m.Window)
		}
	}
}

func forbidExportText(t *testing.T, label, text string) {
	t.Helper()
	for _, secret := range []string{"SYNTHETIC_PRIVATE", "SYNTHETIC_SECRET", "SYNTHETIC_ENTITLEMENT", "SYNTHETIC_REACHED", "SYNTHETIC_LIMIT_ID", "SYNTHETIC_SESSION_PRIVATE", "codex.get_usage_limits.current_source.v1", "synthetic-seam-token"} {
		if strings.Contains(text, secret) {
			t.Fatalf("%s contains %s", label, secret)
		}
	}
}
