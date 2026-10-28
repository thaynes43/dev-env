package agentd

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/thaynes43/dev-env/internal/providerallowance"
)

type allowanceRequest struct {
	ID     *int           `json:"id"`
	Method string         `json:"method"`
	Params map[string]any `json:"params"`
}
type allowanceFixture struct {
	mu       sync.Mutex
	calls    []allowanceRequest
	settings Settings
}

func (f *allowanceFixture) recorded() []allowanceRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]allowanceRequest(nil), f.calls...)
}
func fakeAllowanceHost(t *testing.T, mode string) *allowanceFixture {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "allowance-fixture-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	dir := filepath.Join(home, "app-server-control")
	if err = os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(dir, "app-server-control.sock"))
	if err != nil {
		t.Fatal(err)
	}
	f := &allowanceFixture{settings: Settings{CodexHome: home}}
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
				ua := "codex/0.160.1 fixture"
				if mode == "wrong-version" {
					ua = "codex/0.159.1"
				}
				if mode == "missing-version" {
					ua = ""
				}
				if mode == "oversize" {
					ua = strings.Repeat("x", codexHostRPCBound+1)
				}
				result = map[string]any{"userAgent": ua}
			case "account/read":
				result = map[string]any{"account": map[string]any{"email": "SYNTHETIC_PRIVATE", "token": "SYNTHETIC_SECRET"}}
			case "account/rateLimits/read":
				var used any = 0
				var credits any
				switch mode {
				case "number-exponent":
					used = json.Number("2.5E1")
					credits = map[string]any{"availableCount": json.Number("1e2")}
				case "credits-empty":
					credits = map[string]any{}
				case "credits-null-count":
					credits = map[string]any{"availableCount": nil}
				case "credits-zero":
					credits = map[string]any{"availableCount": 0}
				case "numbers-canonical":
					used = json.Number("0.125")
					credits = map[string]any{"availableCount": json.Number("12.50")}
				}
				result = map[string]any{"rateLimits": map[string]any{"primary": map[string]any{"usedPercent": used, "windowDurationMins": 60}, "secondary": nil}, "rateLimitsByLimitId": map[string]any{"codex": map[string]any{"primary": map[string]any{"usedPercent": 25}}}}
				if mode == "credits-null-object" || (mode != "credits-missing" && credits != nil) {
					result.(map[string]any)["rateLimitResetCredits"] = credits
				}
			default:
				return
			}
			if mode == "rpc-error" {
				if conn.WriteJSON(map[string]any{"id": *req.ID, "error": map[string]string{"message": "synthetic"}}) != nil {
					return
				}
				continue
			}
			if mode == "malformed" {
				_ = conn.WriteMessage(websocket.TextMessage, []byte("{"))
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
func captureContext() CodexAllowanceCapture {
	return CodexAllowanceCapture{ObservationID: "observation-1", ProducerRef: "producer-1", ProducerIncarnation: "instance-1", Sequence: 1, EvidenceRef: "capture-1", SnapshotRevision: "revision-1"}
}
func TestCodexAllowanceReaderWireAndContext(t *testing.T) {
	f := fakeAllowanceHost(t, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	c := captureContext()
	read, err := ReadCodexNativeAllowance(ctx, f.settings, os.Getpid(), at, c)
	if err != nil {
		t.Fatal(err)
	}
	calls := f.recorded()
	if len(calls) != 4 {
		t.Fatalf("calls: %+v", calls)
	}
	for i, want := range []string{"initialize", "initialized", "account/read", "account/rateLimits/read"} {
		if calls[i].Method != want {
			t.Fatalf("call %d: %s", i, calls[i].Method)
		}
	}
	if !reflect.DeepEqual(calls[0].Params, map[string]any{"clientInfo": map[string]any{"name": "dev_env_allowance_reader", "version": managedCodexVersion}, "capabilities": map[string]any{"experimentalApi": true}}) || calls[1].ID != nil {
		t.Fatal("unexpected initialization")
	}
	if !reflect.DeepEqual(calls[2].Params, map[string]any{"refreshToken": false}) || !reflect.DeepEqual(calls[3].Params, map[string]any{"excludeResetCreditDetails": true, "supportsLunaReserve": false}) {
		t.Fatal("unexpected account parameters")
	}
	obs := read.Items[0].Observation
	if obs.ObservationID != c.ObservationID || obs.ProducerRef != c.ProducerRef || obs.ProducerIncarnation != c.ProducerIncarnation || obs.Sequence != c.Sequence || !reflect.DeepEqual(obs.EvidenceRefs, []string{c.EvidenceRef}) || read.SnapshotRevision != c.SnapshotRevision {
		t.Fatal("capture identity mismatch")
	}
	if obs.Source.Version != "app-server-reported-0.160.1" || obs.ProviderObservedAt != nil || read.Items[0].Freshness.State != providerallowance.FreshUnknown || obs.Scope.BindingState != providerallowance.BindingUnknown || len(obs.Measures) != 4 {
		t.Fatal("coverage/freshness mismatch")
	}
	if obs.Measures[2].Value.Decimal == nil || *obs.Measures[2].Value.Decimal != "0" || obs.Measures[3].Value.State != providerallowance.ValueUnknown {
		t.Fatal("zero and unknown lost")
	}
	encoded, _ := json.Marshal(read)
	if strings.Contains(string(encoded), "SYNTHETIC_PRIVATE") || strings.Contains(string(encoded), "SYNTHETIC_SECRET") {
		t.Fatal("private account data leaked")
	}
	if _, err := providerallowance.DecodeReadResponse(encoded, providerallowance.FixtureParseLimits); err != nil {
		t.Fatal(err)
	}
	c2 := CodexAllowanceCapture{ObservationID: "observation-2", ProducerRef: "producer-2", ProducerIncarnation: "instance-2", Sequence: 2, EvidenceRef: "capture-2", SnapshotRevision: "revision-2"}
	next, err := ReadCodexNativeAllowance(ctx, f.settings, os.Getpid(), at, c2)
	if err != nil {
		t.Fatal(err)
	}
	if next.Items[0].Observation.ObservationID != c2.ObservationID || next.Items[0].Observation.ProducerRef != c2.ProducerRef || next.Items[0].Observation.ProducerIncarnation != c2.ProducerIncarnation || next.Items[0].Observation.Sequence != c2.Sequence || !reflect.DeepEqual(next.Items[0].Observation.EvidenceRefs, []string{c2.EvidenceRef}) || next.SnapshotRevision != c2.SnapshotRevision {
		t.Fatal("second identity lost")
	}
}
func TestCodexAllowanceReaderRefusals(t *testing.T) {
	for _, mode := range []string{"invalid-context", "wrong-version", "missing-version", "rpc-error", "malformed", "oversize", "wrong-pid"} {
		t.Run(mode, func(t *testing.T) {
			f := fakeAllowanceHost(t, mode)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c := captureContext()
			pid := os.Getpid()
			if mode == "invalid-context" {
				c.ProducerRef = ""
			}
			if mode == "wrong-pid" {
				pid++
			}
			_, err := ReadCodexNativeAllowance(ctx, f.settings, pid, time.Now(), c)
			if err == nil {
				t.Fatal("expected refusal")
			}
			calls := f.recorded()
			if mode == "invalid-context" || mode == "wrong-pid" {
				if len(calls) != 0 {
					t.Fatalf("unexpected calls: %+v", calls)
				}
			} else {
				for _, call := range calls {
					if call.Method == "account/read" || call.Method == "account/rateLimits/read" {
						t.Fatalf("account call after invalid initialize: %+v", calls)
					}
				}
			}
		})
	}
}

func TestCodexAllowanceCaptureInvalidBeforeRPC(t *testing.T) {
	f := fakeAllowanceHost(t, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	changes := []func(*CodexAllowanceCapture){func(c *CodexAllowanceCapture) { c.ObservationID = "" }, func(c *CodexAllowanceCapture) { c.ProducerRef = "" }, func(c *CodexAllowanceCapture) { c.ProducerIncarnation = "" }, func(c *CodexAllowanceCapture) { c.Sequence = 0 }, func(c *CodexAllowanceCapture) { c.Sequence = -1 }, func(c *CodexAllowanceCapture) { c.EvidenceRef = "" }, func(c *CodexAllowanceCapture) { c.SnapshotRevision = "" }, func(c *CodexAllowanceCapture) { c.EvidenceRef = strings.Repeat("x", 129) }, func(c *CodexAllowanceCapture) { c.ObservationID = "bad\nname" }}
	for _, change := range changes {
		c := captureContext()
		change(&c)
		if _, err := ReadCodexNativeAllowance(ctx, f.settings, os.Getpid(), time.Now(), c); err == nil {
			t.Fatal("invalid capture accepted")
		}
	}
	if _, err := ReadCodexNativeAllowance(ctx, f.settings, os.Getpid(), time.Time{}, captureContext()); err == nil {
		t.Fatal("zero capture time accepted")
	}
	if len(f.recorded()) != 0 {
		t.Fatal("invalid capture performed RPC")
	}
}

func TestCodexAllowanceRPCNumericContract(t *testing.T) {
	for _, tc := range []struct {
		mode                             string
		wantUsed, wantCount, wantDetails string
	}{{"number-exponent", "", "", "unknown"}, {"credits-empty", "0", "", "unknown"}, {"credits-missing", "0", "", "unknown"}, {"credits-null-count", "0", "", "unknown"}, {"credits-null-object", "0", "", "unknown"}, {"credits-zero", "0", "0", "not_requested"}, {"numbers-canonical", "0.125", "12.50", "not_requested"}} {
		t.Run(tc.mode, func(t *testing.T) {
			f := fakeAllowanceHost(t, tc.mode)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			read, err := ReadCodexNativeAllowance(ctx, f.settings, os.Getpid(), time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC), captureContext())
			if err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(read)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := providerallowance.DecodeReadResponse(data, providerallowance.FixtureParseLimits)
			if err != nil {
				t.Fatal(err)
			}
			obs := decoded.Items[0].Observation
			assessment := providerallowance.ValidateObservation(obs, providerallowance.TrustedContext{}, nil)
			if !assessment.ShapeOK {
				t.Fatalf("invalid shape: %+v", assessment.Issues)
			}
			m := obs.Measures[2]
			if tc.wantUsed == "" {
				if m.Value.State != providerallowance.ValueUnknown || m.Value.Decimal != nil {
					t.Fatal("noncanonical used must be Unknown")
				}
			} else if m.Value.Decimal == nil || *m.Value.Decimal != tc.wantUsed {
				t.Fatal("canonical used lost")
			}
			count, details := "", ""
			for _, field := range obs.DirectFields {
				if field.Field == "resetCredits.availableCount" && field.LexicalValue != nil {
					count = *field.LexicalValue
				}
				if field.Field == "resetCredits.detailsState" && field.LexicalValue != nil {
					details = *field.LexicalValue
				}
			}
			if count != tc.wantCount || details != tc.wantDetails {
				t.Fatalf("reset count=%q details=%q", count, details)
			}
		})
	}
}
func TestCodexAllowanceConstructedNoncanonicalNumber(t *testing.T) {
	// 01 is not legal JSON; this is a constructed json.Number, not a wire case.
	n := json.Number("01")
	if json.Valid([]byte(`{"value":01}`)) {
		t.Fatal("01 must be invalid JSON wire")
	}
	m := codexMeasure("synthetic", &codexWindow{Used: &n})
	if m.Value.State != providerallowance.ValueUnknown || m.Value.Decimal != nil {
		t.Fatal("noncanonical number accepted")
	}
}
