package allowancecollector

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"time"

	"github.com/thaynes43/dev-env/internal/providerallowance"
)

const journalMaxBytes = 128 << 10

type Capture struct {
	Provider            string          `json:"provider"`
	ProducerRef         string          `json:"producerRef"`
	ProducerIncarnation string          `json:"producerIncarnation"`
	Sequence            int64           `json:"sequence"`
	CapturedAt          string          `json:"capturedAt"`
	ReceivedAt          string          `json:"receivedAt"`
	Payload             json.RawMessage `json:"payload"`
}

type journal struct {
	Version     int       `json:"version"`
	Fingerprint string    `json:"fingerprint"`
	HelperSHA   string    `json:"helperSHA256"`
	Cycle       int64     `json:"cycle"`
	Nonce       string    `json:"nonce"`
	Phase       string    `json:"phase"`
	StartedAt   string    `json:"startedAt"`
	NotBefore   string    `json:"notBefore"`
	ReportSHA   string    `json:"reportSHA256"`
	Captures    []Capture `json:"captures"`
}

func digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func bundle(captures []Capture) ([]byte, error) {
	if captures == nil {
		captures = []Capture{}
	}
	return json.Marshal(struct {
		Schema   string    `json:"schema"`
		Captures []Capture `json:"captures"`
	}{providerallowance.CaptureBundleSchema, captures})
}

func readJournal(raw, report []byte, cfg *Config, now time.Time) (journal, error) {
	if cfg == nil || cfg.fingerprint == "" || now.IsZero() {
		return journal{}, ErrCollector
	}
	if len(raw) == 0 {
		if len(report) != 0 {
			return journal{}, ErrCollector
		}
		return journal{Version: 1, Fingerprint: cfg.fingerprint, Phase: "idle", ReportSHA: digest(nil), Captures: []Capture{}}, nil
	}
	if len(raw) > journalMaxBytes || !providerallowance.ValidJSONDocument(raw, journalMaxBytes) || !providerallowance.ExactObjectKeys(raw, []string{"version", "fingerprint", "helperSHA256", "cycle", "nonce", "phase", "startedAt", "notBefore", "reportSHA256", "captures"}) {
		return journal{}, ErrCollector
	}
	var children struct {
		Captures []json.RawMessage `json:"captures"`
	}
	if json.Unmarshal(raw, &children) != nil {
		return journal{}, ErrCollector
	}
	for _, child := range children.Captures {
		if !providerallowance.ExactObjectKeys(child, []string{"provider", "producerRef", "producerIncarnation", "sequence", "capturedAt", "receivedAt", "payload"}) {
			return journal{}, ErrCollector
		}
	}
	var held journal
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&held) != nil || dec.Decode(new(any)) != io.EOF || held.Version != 1 || held.Fingerprint != cfg.fingerprint || !remoteDigest(held.HelperSHA) || held.Cycle < 1 || held.Cycle > 1_000_000 || !uuid.MatchString(held.Nonce) || held.ReportSHA != digest(report) || held.Captures == nil || len(held.Captures) > 6 || held.Phase != "idle" && held.Phase != "inflight" {
		return journal{}, ErrCollector
	}
	started, err := time.Parse(time.RFC3339Nano, held.StartedAt)
	next, e := time.Parse(time.RFC3339Nano, held.NotBefore)
	if err != nil || e != nil || started.After(now) || next.Before(started) || next.After(now.Add(2*time.Hour)) {
		return journal{}, ErrCollector
	}
	for _, capture := range held.Captures {
		if capture.Sequence > held.Cycle {
			return journal{}, ErrCollector
		}
	}
	encoded, err := bundle(held.Captures)
	if err != nil {
		return journal{}, ErrCollector
	}
	doc, err := providerallowance.AssembleCaptureBundle(encoded, cfg.trust, now)
	if err != nil {
		return journal{}, ErrCollector
	}
	// Assembly conservatively labels clockless observations unverified for
	// current capacity. Apply the existing caller-trust projection to validate
	// reported source fields without supplying a provider clock/current value.
	providerallowance.ProjectCachedRead(&doc, cfg.trust.Contexts, cfg.trust.Registry, time.Duration(cfg.trust.MaxAge)*time.Second, true, now)
	for _, item := range doc.Items {
		if item.Binding.State != providerallowance.BindingVerified || item.Validation.State != providerallowance.ValidationSourceFields {
			return journal{}, ErrCollector
		}
	}
	return held, nil
}

func reserve(held journal, cfg *Config, nonce, helperSHA string, now time.Time) (journal, []byte, error) {
	if cfg == nil || !uuid.MatchString(nonce) || !remoteDigest(helperSHA) || now.IsZero() || held.Phase != "idle" || held.Cycle >= 1_000_000 || held.Nonce == nonce {
		return journal{}, nil, ErrCollector
	}
	if held.NotBefore != "" {
		ready, err := time.Parse(time.RFC3339Nano, held.NotBefore)
		if err != nil || now.Before(ready) {
			return journal{}, nil, ErrCollector
		}
	}
	held.Cycle++
	held.Nonce, held.Phase, held.HelperSHA = nonce, "inflight", helperSHA
	held.StartedAt, held.NotBefore = now.UTC().Format(time.RFC3339Nano), now.Add(RemoteLimit+intentMargin).UTC().Format(time.RFC3339Nano)
	raw, err := json.Marshal(held)
	if err != nil || len(raw) > journalMaxBytes {
		return journal{}, nil, ErrCollector
	}
	return held, raw, nil
}

// appendCapture retains at most two original samples per provider. Empty Claude
// envelopes retain the latest prior nonempty sample rather than evicting it on
// repeated empty reads. Assembly still validates every retained payload/binding.
func appendCapture(held []Capture, capture Capture) []Capture {
	out := append(append([]Capture(nil), held...), capture)
	count := 0
	for _, item := range out {
		if item.Provider == capture.Provider {
			count++
		}
	}
	if count <= 2 {
		return out
	}
	keep := -1
	if emptyClaudeCapture(capture) {
		for i, item := range out[:len(out)-1] {
			if item.Provider == capture.Provider && !emptyClaudeCapture(item) {
				keep = i
			}
		}
	}
	for i, item := range out {
		if item.Provider == capture.Provider && i != keep {
			return append(out[:i:i], out[i+1:]...)
		}
	}
	return out
}

// This is only an eviction preference, never payload validation or trust.
// The incoming capture remains in the bundle and must pass strict assembly.
func emptyClaudeCapture(capture Capture) bool {
	if capture.Provider != "anthropic" {
		return false
	}
	var payload providerallowance.ClaudeUsageCapture
	return json.Unmarshal(capture.Payload, &payload) == nil && payload.Schema == providerallowance.ClaudeSDKCaptureSchema && len(payload.Limits) == 0
}
