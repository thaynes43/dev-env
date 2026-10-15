package protocol

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"regexp"
	"sort"
	"time"
)

const MaxOllamaReceiptBytes = 64 << 10

var ollamaLabel = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,95}$`)

// OllamaIdentity names one exact backend/model artifact, without credentials.
// It is independent of Session: adding a candidate never adds a provider kind.
type OllamaIdentity struct {
	Backend  string `json:"backend"`
	Endpoint string `json:"endpoint"`
	ModelID  string `json:"modelID"`
	Digest   string `json:"digest"`
}

type OllamaProvenance struct {
	Source     string    `json:"source"`
	ReceiptID  string    `json:"receiptID"`
	ObservedAt time.Time `json:"observedAt"`
}

type OllamaToolRequirement struct {
	Name      string `json:"name"`
	Transport string `json:"transport"`
}

type OllamaToolCapability struct {
	OllamaToolRequirement
	State string `json:"state"`
}

type OllamaModelCapability struct {
	ModelID       string                 `json:"modelID"`
	Digest        string                 `json:"digest"`
	ContextTokens int64                  `json:"contextTokens"`
	ContextState  string                 `json:"contextState"`
	Tools         []OllamaToolCapability `json:"tools"`
}

// OllamaCapabilityReceipt is a sanitized adapter input, not /api/tags itself.
// Observed means caller-reported observation, never authenticated authority.
type OllamaCapabilityReceipt struct {
	Version            int                     `json:"version"`
	Backend            string                  `json:"backend"`
	Endpoint           string                  `json:"endpoint"`
	ToolManifestSHA256 string                  `json:"toolManifestSHA256"`
	Provenance         OllamaProvenance        `json:"provenance"`
	Models             []OllamaModelCapability `json:"models"`
}

type OllamaCandidateRequirements struct {
	Identity           OllamaIdentity          `json:"identity"`
	Harness            string                  `json:"harness"`
	ContextTokens      int64                   `json:"contextTokens"`
	ToolManifestSHA256 string                  `json:"toolManifestSHA256"`
	Tools              []OllamaToolRequirement `json:"tools"`
}

// OllamaCandidate is isolated fixture data. Config is not a global config file
// and Argv is never executed here; an admitted future adapter must render it.
type OllamaCandidate struct {
	Profile        string                      `json:"profile"`
	Harness        string                      `json:"harness"`
	DesignSelected bool                        `json:"designSelected"`
	Argv           []string                    `json:"argv"`
	Config         OllamaCandidateRequirements `json:"config"`
	Provenance     OllamaProvenance            `json:"provenance"`
	ReceiptSHA256  string                      `json:"receiptSHA256"`
}

// There is deliberately no authorization/allow input or runnable launcher.
type OllamaPreflightResult struct {
	Candidate        *OllamaCandidate `json:"candidate,omitempty"`
	Reasons          []string         `json:"reasons"`
	MissingGates     []string         `json:"missingGates"`
	RuntimeAuthority string           `json:"runtimeAuthority"`
	Availability     string           `json:"availability"`
	List             string           `json:"list"`
	Observe          string           `json:"observe"`
	Steer            string           `json:"steer"`
	Cancel           string           `json:"cancel"`
	Cost             CostObservation  `json:"cost"`
}

func validOllamaOrigin(s string) bool {
	u, err := url.Parse(s)
	return err == nil && len(s) <= 256 && (u.Scheme == "http" || u.Scheme == "https") &&
		u.Hostname() != "" && u.User == nil && u.Path == "" && u.RawQuery == "" &&
		u.Fragment == "" && !u.ForceQuery && u.Opaque == "" && u.String() == s
}

func validOllamaDigest(s string) bool {
	return len(s) == 71 && s[:7] == "sha256:" && usageDigest.MatchString(s[7:])
}

func validOllamaEvidence(s string) bool {
	return s == UsageUnknown || s == "Advertised" || s == "Observed"
}

func validOllamaTool(t OllamaToolRequirement) bool {
	return ollamaLabel.MatchString(t.Name) && (t.Transport == "cli" || t.Transport == "api" ||
		t.Transport == "mcp-http" || t.Transport == "mcp-sse")
}

func (r OllamaCapabilityReceipt) validate() error {
	if r.Version != 1 || r.Backend != "ollama" || !validOllamaOrigin(r.Endpoint) ||
		!usageDigest.MatchString(r.ToolManifestSHA256) || r.Provenance.Source != "caller-sanitized" ||
		!ollamaLabel.MatchString(r.Provenance.ReceiptID) || r.Provenance.ObservedAt.IsZero() || len(r.Models) > 16 {
		return errors.New("invalid Ollama capability receipt")
	}
	seen := map[string]bool{}
	for _, m := range r.Models {
		if !ValidUsageModelID(m.ModelID) || !validOllamaDigest(m.Digest) || seen[m.ModelID] ||
			!validOllamaEvidence(m.ContextState) || m.ContextTokens < 0 || m.ContextTokens > 1_000_000 ||
			(m.ContextState == UsageUnknown && m.ContextTokens != 0) || len(m.Tools) > 64 {
			return errors.New("invalid or duplicate Ollama model evidence")
		}
		seen[m.ModelID] = true
		tools := map[OllamaToolRequirement]bool{}
		for _, t := range m.Tools {
			if !validOllamaTool(t.OllamaToolRequirement) || !validOllamaEvidence(t.State) || tools[t.OllamaToolRequirement] {
				return errors.New("invalid or duplicate Ollama tool evidence")
			}
			tools[t.OllamaToolRequirement] = true
		}
	}
	return nil
}

// ParseOllamaCapabilityReceipt rejects unknown fields, duplicate keys, trailing
// values, deep or oversized input before returning any partial observation.
func ParseOllamaCapabilityReceipt(raw []byte) (OllamaCapabilityReceipt, error) {
	var r OllamaCapabilityReceipt
	if len(raw) == 0 || len(raw) > MaxOllamaReceiptBytes {
		return r, errors.New("ollama receipt size refused")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if err := ollamaJSONValue(d, 0); err != nil {
		return r, err
	}
	if _, err := d.Token(); err != io.EOF {
		return r, errors.New("trailing Ollama receipt")
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&r); err != nil {
		return OllamaCapabilityReceipt{}, errors.New("malformed Ollama receipt")
	}
	if err := r.validate(); err != nil {
		return OllamaCapabilityReceipt{}, err
	}
	return r, nil
}

func ollamaJSONValue(d *json.Decoder, depth int) error {
	if depth > 16 {
		return errors.New("ollama receipt nesting refused")
	}
	t, err := d.Token()
	if err != nil {
		return errors.New("malformed Ollama receipt")
	}
	if delim, ok := t.(json.Delim); ok {
		if delim != '{' && delim != '[' {
			return errors.New("malformed Ollama receipt")
		}
		seen := map[string]bool{}
		for d.More() {
			if delim == '{' {
				k, e := d.Token()
				if e != nil {
					return errors.New("malformed Ollama receipt")
				}
				s, ok := k.(string)
				if !ok || seen[s] {
					return errors.New("duplicate Ollama receipt key")
				}
				switch s {
				case "version", "backend", "endpoint", "toolManifestSHA256", "provenance", "models", "source", "receiptID", "observedAt", "modelID", "digest", "contextTokens", "contextState", "tools", "name", "transport", "state":
				default:
					return errors.New("noncanonical Ollama receipt key")
				}
				seen[s] = true
			}
			if err := ollamaJSONValue(d, depth+1); err != nil {
				return err
			}
		}
		end, e := d.Token()
		if e != nil || (delim == '{' && end != json.Delim('}')) || (delim == '[' && end != json.Delim(']')) {
			return errors.New("malformed Ollama receipt")
		}
	}
	return nil
}

// PlanOllamaCandidate validates supplied evidence and returns data only.
// Catalog presence cannot authorize inference or prove provider availability.
func PlanOllamaCandidate(w OllamaCandidateRequirements, r OllamaCapabilityReceipt) (OllamaPreflightResult, error) {
	out := OllamaPreflightResult{
		Reasons: []string{}, MissingGates: []string{"lease", "household-reservation", "resource-budget", "access-network", "bounded-execution-spend"},
		RuntimeAuthority: UsageUnknown, Availability: UsageUnknown, List: UsageUnknown, Observe: UsageUnknown, Steer: UsageUnknown, Cancel: UsageUnknown,
		Cost: CostObservation{State: UsageUnknown, Kind: UsageUnknown},
	}
	if w.Identity.Backend != "ollama" || !validOllamaOrigin(w.Identity.Endpoint) || !ValidUsageModelID(w.Identity.ModelID) ||
		!validOllamaDigest(w.Identity.Digest) || (w.Harness != "opencode" && w.Harness != "codex-experimental") ||
		w.ContextTokens < 1 || w.ContextTokens > 1_000_000 || !usageDigest.MatchString(w.ToolManifestSHA256) || len(w.Tools) == 0 || len(w.Tools) > 64 {
		return out, errors.New("invalid exact Ollama candidate requirements")
	}
	seen := map[OllamaToolRequirement]bool{}
	for _, t := range w.Tools {
		if !validOllamaTool(t) || seen[t] {
			return out, errors.New("invalid or duplicate required tool")
		}
		seen[t] = true
	}
	if err := r.validate(); err != nil {
		return out, err
	}
	encoded, err := CanonicalJSON(r)
	if err != nil || len(encoded) > MaxOllamaReceiptBytes {
		return out, errors.New("ollama receipt size refused")
	}
	if r.Backend != w.Identity.Backend || r.Endpoint != w.Identity.Endpoint {
		out.Reasons = append(out.Reasons, "endpoint-backend-mismatch")
	}
	if r.ToolManifestSHA256 != w.ToolManifestSHA256 {
		out.Reasons = append(out.Reasons, "tool-manifest-mismatch")
	}
	var model *OllamaModelCapability
	for i := range r.Models {
		if r.Models[i].ModelID == w.Identity.ModelID {
			model = &r.Models[i]
		}
	}
	if model == nil {
		out.Reasons = append(out.Reasons, "exact-model-absent")
	} else {
		if model.Digest != w.Identity.Digest {
			out.Reasons = append(out.Reasons, "model-digest-mismatch")
		}
		if model.ContextState != "Observed" {
			out.Reasons = append(out.Reasons, "context-not-observed")
		} else if model.ContextTokens < w.ContextTokens {
			out.Reasons = append(out.Reasons, "context-insufficient")
		}
		for _, required := range w.Tools {
			observed := false
			for _, tool := range model.Tools {
				if tool.OllamaToolRequirement == required && tool.State == "Observed" {
					observed = true
				}
			}
			if !observed {
				out.Reasons = append(out.Reasons, "tool-not-observed:"+required.Transport+":"+required.Name)
			}
		}
	}
	sort.Strings(out.Reasons)
	if len(out.Reasons) > 0 {
		return out, nil
	}
	w.Tools = append([]OllamaToolRequirement(nil), w.Tools...)
	sort.Slice(w.Tools, func(i, j int) bool {
		if w.Tools[i].Name == w.Tools[j].Name {
			return w.Tools[i].Transport < w.Tools[j].Transport
		}
		return w.Tools[i].Name < w.Tools[j].Name
	})
	hash := sha256.Sum256(encoded)
	c := &OllamaCandidate{Profile: "dev-env-offline-ollama-candidate", Harness: w.Harness, DesignSelected: w.Harness == "opencode", Config: w, Provenance: r.Provenance, ReceiptSHA256: hex.EncodeToString(hash[:])}
	if w.Harness == "opencode" {
		c.Argv = []string{"opencode", "--model", "ollama/" + w.Identity.ModelID}
	} else {
		c.Argv = []string{"codex", "--oss", "--local-provider", "ollama", "--profile", c.Profile, "--model", w.Identity.ModelID}
	}
	out.Candidate = c
	return out, nil
}
