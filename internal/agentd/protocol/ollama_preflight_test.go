package protocol

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func ollamaFixture() (OllamaCandidateRequirements, OllamaCapabilityReceipt) {
	d := "sha256:" + strings.Repeat("a", 64)
	tools := []OllamaToolRequirement{{"git", "cli"}, {"metadata", "api"}, {"catalog", "mcp-http"}}
	w := OllamaCandidateRequirements{Identity: OllamaIdentity{"ollama", "http://127.0.0.1:11434", "qwen3-coder-30b-a3b-instruct-q4_k_m", d}, Harness: "opencode", ContextTokens: 32768, ToolManifestSHA256: strings.Repeat("b", 64), Tools: tools}
	r := OllamaCapabilityReceipt{Version: 1, Backend: "ollama", Endpoint: w.Identity.Endpoint, ToolManifestSHA256: w.ToolManifestSHA256, Provenance: OllamaProvenance{"caller-sanitized", "synthetic-fixture", time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)}, Models: []OllamaModelCapability{{ModelID: w.Identity.ModelID, Digest: d, ContextTokens: 65536, ContextState: "Observed"}}}
	for _, tool := range tools {
		r.Models[0].Tools = append(r.Models[0].Tools, OllamaToolCapability{tool, "Observed"})
	}
	return w, r
}

func TestOllamaCandidateRoundTrip(t *testing.T) {
	for _, harness := range []string{"opencode", "codex-experimental"} {
		t.Run(harness, func(t *testing.T) {
			w, r := ollamaFixture()
			w.Harness = harness
			raw, _ := json.Marshal(r)
			parsed, err := ParseOllamaCapabilityReceipt(raw)
			if err != nil {
				t.Fatal(err)
			}
			got, err := PlanOllamaCandidate(w, parsed)
			if err != nil || got.Candidate == nil || len(got.Reasons) != 0 {
				t.Fatalf("%+v %v", got, err)
			}
			b, err := CanonicalJSON(got)
			if err != nil {
				t.Fatal(err)
			}
			var round OllamaPreflightResult
			if err = json.Unmarshal(b, &round); err != nil || !reflect.DeepEqual(got, round) {
				t.Fatalf("round trip %v", err)
			}
			again, _ := PlanOllamaCandidate(w, parsed)
			b2, _ := CanonicalJSON(again)
			if !bytes.Equal(b, b2) {
				t.Fatal("nondeterministic plan")
			}
			c := round.Candidate
			if c.Provenance != r.Provenance || c.Config.Identity != w.Identity || c.Config.ToolManifestSHA256 != r.ToolManifestSHA256 || len(c.ReceiptSHA256) != 64 || len(c.Config.Tools) != 3 {
				t.Fatal("lost binding")
			}
			if c.DesignSelected != (harness == "opencode") || c.Profile != "dev-env-offline-ollama-candidate" {
				t.Fatal("design/profile changed")
			}
			if got.RuntimeAuthority != UsageUnknown || got.Availability != UsageUnknown || got.List != UsageUnknown || got.Observe != UsageUnknown || got.Steer != UsageUnknown || got.Cancel != UsageUnknown || !got.Cost.Valid() || got.Cost.State != UsageUnknown || len(got.MissingGates) != 5 {
				t.Fatal("fixture fabricated runtime evidence")
			}
			if strings.Contains(string(b), "api_key") || strings.Contains(string(b), "Bearer") {
				t.Fatal("secret-bearing output")
			}
			if (Session{Agent: "opencode"}).Validate() == nil {
				t.Fatal("candidate changed production Session admission")
			}
		})
	}
}

func TestOllamaCandidateEvidenceRefusal(t *testing.T) {
	cases := []struct {
		name, reason string
		mutate       func(*OllamaCandidateRequirements, *OllamaCapabilityReceipt)
	}{
		{"endpoint", "endpoint-backend-mismatch", func(_ *OllamaCandidateRequirements, r *OllamaCapabilityReceipt) {
			r.Endpoint = "http://127.0.0.2:11434"
		}},
		{"digest", "model-digest-mismatch", func(_ *OllamaCandidateRequirements, r *OllamaCapabilityReceipt) {
			r.Models[0].Digest = "sha256:" + strings.Repeat("c", 64)
		}},
		{"absent-qwen", "exact-model-absent", func(_ *OllamaCandidateRequirements, r *OllamaCapabilityReceipt) {
			r.Models[0].ModelID = "muse-glimmer-30b"
		}},
		{"empty-catalog", "exact-model-absent", func(_ *OllamaCandidateRequirements, r *OllamaCapabilityReceipt) { r.Models = nil }},
		{"manifest", "tool-manifest-mismatch", func(_ *OllamaCandidateRequirements, r *OllamaCapabilityReceipt) {
			r.ToolManifestSHA256 = strings.Repeat("c", 64)
		}},
		{"advertised-context", "context-not-observed", func(_ *OllamaCandidateRequirements, r *OllamaCapabilityReceipt) {
			r.Models[0].ContextState = "Advertised"
		}},
		{"unknown-context", "context-not-observed", func(_ *OllamaCandidateRequirements, r *OllamaCapabilityReceipt) {
			r.Models[0].ContextState = UsageUnknown
			r.Models[0].ContextTokens = 0
		}},
		{"small-context", "context-insufficient", func(_ *OllamaCandidateRequirements, r *OllamaCapabilityReceipt) { r.Models[0].ContextTokens = 1024 }},
		{"advertised-tool", "tool-not-observed:cli:git", func(_ *OllamaCandidateRequirements, r *OllamaCapabilityReceipt) {
			r.Models[0].Tools[0].State = "Advertised"
		}},
		{"unknown-tool", "tool-not-observed:cli:git", func(_ *OllamaCandidateRequirements, r *OllamaCapabilityReceipt) {
			r.Models[0].Tools[0].State = UsageUnknown
		}},
		{"incomplete-access", "tool-not-observed:mcp-http:catalog", func(_ *OllamaCandidateRequirements, r *OllamaCapabilityReceipt) {
			r.Models[0].Tools = r.Models[0].Tools[:2]
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w, r := ollamaFixture()
			c.mutate(&w, &r)
			got, err := PlanOllamaCandidate(w, r)
			if err != nil || got.Candidate != nil || !strings.Contains(strings.Join(got.Reasons, ","), c.reason) {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
}

func TestOllamaReceiptBounds(t *testing.T) {
	_, r := ollamaFixture()
	b, _ := json.Marshal(r)
	cases := [][]byte{nil, []byte("{"), append(append([]byte{}, b...), []byte("{}")...), []byte(strings.Repeat(" ", MaxOllamaReceiptBytes+1)), bytes.Replace(b, []byte(`"version":1`), []byte(`"version":1,"version":1`), 1), bytes.Replace(b, []byte(`"version":1`), []byte(`"version":1,"credential":"synthetic-secret"`), 1)}
	cases = append(cases, bytes.Replace(b, []byte(`"state":"Observed"`), []byte(`"state":"Unknown","STATE":"Observed"`), 1))
	cases = append(cases, bytes.Replace(b, []byte(`"state":"Observed"`), []byte(`"state":"Unknown","ſtate":"Observed"`), 1))
	for i, raw := range cases {
		if got, err := ParseOllamaCapabilityReceipt(raw); err == nil || !reflect.DeepEqual(got, OllamaCapabilityReceipt{}) {
			t.Fatalf("accepted invalid receipt %d", i)
		}
	}
	for _, mutate := range []func(*OllamaCapabilityReceipt){
		func(r *OllamaCapabilityReceipt) { r.Endpoint = "http://user:synthetic-secret@127.0.0.1:11434" },
		func(r *OllamaCapabilityReceipt) { r.Endpoint += "?token=synthetic-secret" },
		func(r *OllamaCapabilityReceipt) { r.Backend = "llama-server" },
		func(r *OllamaCapabilityReceipt) { r.Models = append(r.Models, r.Models[0]) },
		func(r *OllamaCapabilityReceipt) { r.Models[0].Tools = append(r.Models[0].Tools, r.Models[0].Tools[0]) },
	} {
		_, r = ollamaFixture()
		mutate(&r)
		raw, _ := json.Marshal(r)
		if _, err := ParseOllamaCapabilityReceipt(raw); err == nil {
			t.Fatal("accepted invalid binding")
		}
	}
}

func TestOllamaRequirementsRefusalAndIsolation(t *testing.T) {
	for _, mutate := range []func(*OllamaCandidateRequirements){
		func(w *OllamaCandidateRequirements) { w.Identity.Endpoint = "" },
		func(w *OllamaCandidateRequirements) { w.Identity.ModelID = "" },
		func(w *OllamaCandidateRequirements) { w.Identity.Digest = "" },
		func(w *OllamaCandidateRequirements) { w.Harness = "claude" },
		func(w *OllamaCandidateRequirements) { w.ContextTokens = 0 },
		func(w *OllamaCandidateRequirements) { w.Tools = nil },
	} {
		w, r := ollamaFixture()
		mutate(&w)
		if got, err := PlanOllamaCandidate(w, r); err == nil || got.Candidate != nil {
			t.Fatal("accepted incomplete requirements")
		}
	}
	w, r := ollamaFixture()
	got, err := PlanOllamaCandidate(w, r)
	if err != nil {
		t.Fatal(err)
	}
	w.Tools[0].Name = "changed"
	if got.Candidate.Config.Tools[0].Name == "changed" {
		t.Fatal("candidate aliases caller slice")
	}
	if reflect.TypeOf(*got.Candidate).ConvertibleTo(reflect.TypeOf(Session{})) {
		t.Fatal("candidate is production session")
	}
}
