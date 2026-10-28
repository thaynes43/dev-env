package agentrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/providerallowance"
)

func TestUsageShowUnknownExplicitZeroAndInitialProvenance(t *testing.T) {
	for _, mode := range []string{"missing", "legacy-zero", "legacy-nonzero", "pruned", "zero", "nonzero", "token-only"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t)
			s := testSession("usage-show", "")
			u := protocol.UnknownUsage()
			u.Source = protocol.UsageSource{SessionUID: "original-session", PodUID: "original-pod", Provider: "claude", RequestedModel: "claude-opus-5-5", AttemptID: strings.Repeat("a", 64)}
			u.ID = protocol.UsageObservationID(u.Source)
			u.ObservedAt = testNow
			u.ResultState = "Final"
			zero := int64(0)
			u.InputTokens = protocol.TokenObservation{State: "Reported", Amount: &zero}
			switch mode {
			case "legacy-zero":
				s.Usage = &apiv1.Usage{CostUSD: "0", InputTokens: 7}
			case "legacy-nonzero":
				s.Usage = &apiv1.Usage{CostUSD: "99", InputTokens: 7}
			case "pruned":
				u.Cost = protocol.CostObservation{Amount: "0"}
				s.UsageObservation = &u
			case "zero", "nonzero":
				u.Cost = protocol.CostObservation{State: "Reported", Amount: "0", Unit: "USD", Kind: protocol.UsageCLIEstimate}
				if mode == "nonzero" {
					u.Cost.Amount = "1.25"
				}
				s.UsageObservation = &u
			case "token-only":
				u.Source.Provider = protocol.AgentCodex
				u.Source.RequestedModel = "gpt-6.1-sol"
				u.ID = protocol.UsageObservationID(u.Source)
				s.UsageObservation = &u
			}
			h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) { writeTestJSON(w, http.StatusOK, s) }
			h.mustRun(ExitOK, "show", s.Name)
			out := h.stdout.String()
			if strings.Contains(out, "$") {
				t.Fatal("unqualified dollars", out)
			}
			if mode == "zero" || mode == "nonzero" {
				if !strings.Contains(out, u.Cost.Amount+" USD (CLI equivalent estimate)") {
					t.Fatal("explicit estimate lost", out)
				}
			} else if !strings.Contains(out, "Unknown") {
				t.Fatal("unavailable cost classified", out)
			}
			if s.UsageObservation != nil && u.Validate() == nil {
				contains(t, "typed usage", out, "Usage scope:", "Initial managed invocation", "Actual model:", "Unknown", "Input tokens:", "0", "Output tokens:", "Unknown", "Cache read tokens:", "Unknown", "Cache create tokens:", "Unknown", "Requested model:", u.Source.RequestedModel)
			}
		})
	}
}

type failReader struct{}

func (failReader) Read([]byte) (int, error) { panic("unexpected stdin") }

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("private") }
func TestUsageLocalPreflight(t *testing.T) {
	var out, errout bytes.Buffer
	at := time.Date(2026, 10, 28, 20, 0, 0, 0, time.UTC)
	env := Env{Stdout: &out, Stderr: &errout, Stdin: failReader{}, Now: func() time.Time { return at }}
	if Run(context.Background(), []string{"usage", "-o", "json"}, env) != 0 {
		t.Fatal(errout.String())
	}
	read, err := providerallowance.DecodeReadResponse(out.Bytes(), providerallowance.FixtureParseLimits)
	if err != nil || len(read.Items) != 0 || len(read.SourceStatuses) != 3 || read.ServedAt != at.Format(time.RFC3339) {
		t.Fatal("invalid local read", err)
	}
	providers := map[string]bool{"cursor": false, "anthropic": false, "openai": false}
	for _, s := range read.SourceStatuses {
		seen, ok := providers[s.Provider]
		if !ok || seen {
			t.Fatal("unexpected or duplicate provider", s.Provider)
		}
		providers[s.Provider] = true
		if s.Availability != providerallowance.AvailabilityUnconfigured || s.LastGood != nil || s.LastAttempt.Result != providerallowance.AttemptUnavailable || s.LastAttempt.AttemptedAt != read.ServedAt || s.Freshness.State != providerallowance.FreshUnknown || s.Binding.State != providerallowance.BindingUnknown || s.LastAttempt.Code == nil || !strings.Contains(*s.LastAttempt.Code, "no_provider_request") {
			t.Fatal("invalid preflight", s)
		}
	}
	for provider, seen := range providers {
		if !seen {
			t.Fatal("missing provider", provider)
		}
	}
}
func TestUsageCacheAndRefusals(t *testing.T) {
	fixture, err := os.ReadFile("../providerallowance/testdata/read-interface-examples.json")
	if err != nil {
		t.Fatal(err)
	}
	var wrapped struct {
		HTTP struct {
			Body json.RawMessage `json:"body"`
		} `json:"http"`
	}
	if json.Unmarshal(fixture, &wrapped) != nil {
		t.Fatal("fixture")
	}
	original, err := providerallowance.DecodeReadResponse(wrapped.HTTP.Body, providerallowance.FixtureParseLimits)
	if err != nil {
		t.Fatal(err)
	}
	var fixtureOut bytes.Buffer
	fixtureEnv := Env{Stdout: &fixtureOut, Stderr: io.Discard, Stdin: bytes.NewReader(wrapped.HTTP.Body)}
	if Run(context.Background(), []string{"usage", "-o", "json", "--from-file", "-"}, fixtureEnv) != 0 {
		t.Fatal("fixture refused")
	}
	restored, err := providerallowance.DecodeReadResponse(fixtureOut.Bytes(), providerallowance.FixtureParseLimits)
	if err != nil || !reflect.DeepEqual(original, restored) {
		t.Fatal("fixture changed", err)
	}
	var good bytes.Buffer
	env := Env{Stdout: &good, Stderr: io.Discard, Now: func() time.Time { return time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC) }}
	if Run(context.Background(), []string{"usage", "-o", "json"}, env) != 0 {
		t.Fatal("default")
	}
	raw := append([]byte(nil), good.Bytes()...)
	file := filepath.Join(t.TempDir(), "cache")
	if os.WriteFile(file, raw, 0600) != nil {
		t.Fatal("write")
	}
	var out bytes.Buffer
	env.Stdout = &out
	env.Now = time.Now
	if Run(context.Background(), []string{"usage", "-o", "json", "--from-file", file}, env) != 0 || !bytes.Equal(out.Bytes(), raw) {
		t.Fatal("cached read changed")
	}
	link := file + "-link"
	if os.Symlink(file, link) != nil {
		t.Fatal("link")
	}
	for _, input := range []string{link, file + "-missing", filepath.Dir(file)} {
		out.Reset()
		var stderr bytes.Buffer
		env.Stderr = &stderr
		if Run(context.Background(), []string{"usage", "-o", "json", "--from-file", input}, env) == 0 || out.Len() != 0 || strings.Contains(stderr.String(), input) {
			t.Fatal("unsafe refusal")
		}
	}
	for _, bad := range []string{strings.Repeat("x", providerallowance.FixtureParseLimits.MaxBytes+1), "{", strings.Replace(string(raw), `"schema":`, `"schema":null,"schema":`, 1), strings.Replace(string(raw), `"schema":`, `"Schema":`, 1), strings.Replace(string(raw), `"schema":`, `"ſchema":`, 1), strings.Replace(string(raw), `"schema":`, `"schema":null,"extra":1,"duplicate":`, 1), strings.Replace(string(raw), `"items":[]`, `"items":null`, 1)} {
		env.Stdin = strings.NewReader(bad)
		out.Reset()
		if Run(context.Background(), []string{"usage", "-o", "json", "--from-file", "-"}, env) == 0 || out.Len() != 0 {
			t.Fatal("bad cache accepted")
		}
	}
	env.Stdin = failReader{}
	if Run(context.Background(), []string{"usage", "-o", "json", "--from-file", "-", "--bad"}, env) == 0 {
		t.Fatal("invalid flag accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if Run(ctx, []string{"usage", "-o", "json"}, env) == 0 {
		t.Fatal("cancel accepted")
	}
	env.Stdout = failWriter{}
	if Run(context.Background(), []string{"usage", "-o", "json"}, env) == 0 {
		t.Fatal("write failure accepted")
	}
}

func TestUsageClaudeStatusline(t *testing.T) {
	var out, stderr bytes.Buffer
	env := Env{Stdout: &out, Stderr: &stderr, Stdin: strings.NewReader(`{"private":"SECRET-CANARY","rate_limits":{"five_hour":{"used_percentage":0,"resets_at":1800000000}}}`), Now: func() time.Time { return time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC) }}
	if Run(context.Background(), []string{"usage", "-o", "json", "--claude-statusline"}, env) != 0 {
		t.Fatal(stderr.String())
	}
	doc, err := providerallowance.DecodeReadResponse(out.Bytes(), providerallowance.FixtureParseLimits)
	if err != nil || len(doc.Items) != 1 || len(doc.SourceStatuses) != 3 || doc.Items[0].Observation.Provider != "anthropic" || doc.Items[0].Freshness.State != providerallowance.FreshUnknown || strings.Contains(out.String(), "SECRET-CANARY") {
		t.Fatal("invalid callback read", err)
	}
	env.Stdin = failReader{}
	out.Reset()
	stderr.Reset()
	if Run(context.Background(), []string{"usage", "-o", "json", "--claude-statusline", "--from-file", "-"}, env) == 0 {
		t.Fatal("conflicting sources accepted")
	}
}
