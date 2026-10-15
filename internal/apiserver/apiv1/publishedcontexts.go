package apiv1

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

const PublishedContextsPath = "/v1/published-contexts"
const MaxContextBytes = 64 << 10
const MaxContextRequestBytes = 6*MaxContextBytes + 4096

var contextDigestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var managedConversationPattern = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)

var contextKeyPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]{0,126}[a-z0-9])?$`)
var contextUIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,128}$`)
var contextNamePattern = regexp.MustCompile(`^published-context\.[a-f0-9]{32}\.[a-f0-9]{32}$`)

func ValidContextRequest(r PublishContextRequest) bool {
	if m := r.ManagedOutput; m != nil && (r.Kind != "transcript-excerpt" || m.Ordinal < 0 || m.Ordinal > 3 || !managedConversationPattern.MatchString(m.ConversationID) || (m.Completeness != "PartialManagedOutput" && m.Completeness != "Unknown")) {
		return false
	}
	return (r.Kind == "transcript-excerpt" || r.Kind == "result" || r.Kind == "memory" || r.Kind == "handoff") &&
		contextKeyPattern.MatchString(r.Key) && len(r.Text) > 0 && len(r.Text) <= MaxContextBytes && utf8.ValidString(r.Text)
}

func ManagedInvocation(sessionUID, podUID string) string {
	return ContextHash(sessionUID + "\x00" + podUID)
}
func ManagedOutputKey(invocation string, ordinal int32) string {
	return "capture-" + invocation + "-" + strconv.Itoa(int(ordinal))
}
func ValidManagedOutput(m *ManagedOutputAssertion) bool {
	return m != nil && contextDigestPattern.MatchString(m.Invocation) && m.Ordinal >= 0 && m.Ordinal < 4 && managedConversationPattern.MatchString(m.ConversationID)
}

func ContextHash(text string) string {
	h := sha256.Sum256([]byte(text))
	return hex.EncodeToString(h[:])
}

func ContextName(uid, kind, key string) string {
	data, _ := json.Marshal(struct {
		SessionUID string `json:"sessionUID"`
		Kind       string `json:"kind"`
		Key        string `json:"key"`
	}{uid, kind, key})
	d := ContextHash(string(data))
	return "published-context." + d[:32] + "." + d[32:]
}

func ValidContextUID(uid string) bool   { return contextUIDPattern.MatchString(uid) }
func ValidContextName(name string) bool { return contextNamePattern.MatchString(name) }

// The sender supplies no authority, attribution, path or artifact URL.
type ManagedOutputRequest struct {
	Ordinal        int32  `json:"ordinal"`
	ConversationID string `json:"conversationID"`
	Completeness   string `json:"completeness"`
}
type ManagedOutputAssertion struct {
	Invocation     string `json:"invocation"`
	Ordinal        int32  `json:"ordinal"`
	ConversationID string `json:"conversationID"`
}

type PublishContextRequest struct {
	ManagedOutput *ManagedOutputRequest `json:"managedOutput,omitempty"`
	Kind          string                `json:"kind"`
	Key           string                `json:"key"`
	Text          string                `json:"text"`
}

// AttributionHeader asks for provider-reported attribution (DESIGN-018). A
// reader without it gets the original shape: every state Unknown and no
// models or usage, which older strict decoders and validators accept.
const (
	AttributionHeader   = "Dev-Env-Attribution"
	AttributionProvider = "provider-v1"
)

type ContextAttribution struct {
	Provider           string `json:"provider"`
	RequestedModel     string `json:"requestedModel"`
	ObservedModelState string `json:"observedModelState"`
	UsageState         string `json:"usageState"`
	CostState          string `json:"costState"`
	// ObservedModels and Usage come from the source Session's retained
	// initial-invocation observation, joined at read time for a managed output
	// assertion of that same invocation only. The stored record never changes,
	// so the join lasts only while the source Session exists: once it is
	// reaped or replaced, the attribution reads Unknown again (DESIGN-018).
	ObservedModels []string      `json:"observedModels,omitempty"`
	Usage          *ContextUsage `json:"usage,omitempty"`
}

// ContextUsage is the invocation's provider-reported totals. The cost is a CLI
// list-price equivalent, never a charge; a plan-served launch says so in
// BillingBasis.
type ContextUsage struct {
	ObservationID            string                    `json:"observationID"`
	BillingBasis             string                    `json:"billingBasis"`
	InputTokens              protocol.TokenObservation `json:"inputTokens"`
	OutputTokens             protocol.TokenObservation `json:"outputTokens"`
	CacheReadInputTokens     protocol.TokenObservation `json:"cacheReadInputTokens"`
	CacheCreationInputTokens protocol.TokenObservation `json:"cacheCreationInputTokens"`
	Cost                     protocol.CostObservation  `json:"cost"`
}

// ValidContextAttribution accepts the original all-Unknown shape, or, for a
// managed output assertion only, provider-reported states that each have their
// evidence: models for the model state, a Reported counter for the usage
// state, a Reported cost for the cost state. Codex never asserts cost.
func ValidContextAttribution(a ContextAttribution, managed bool) bool {
	unknown := protocol.UsageUnknown
	state := func(s string) bool { return s == unknown || s == protocol.UsageProviderReported }
	if !state(a.ObservedModelState) || !state(a.UsageState) || !state(a.CostState) {
		return false
	}
	u := a.Usage
	if u == nil {
		return a.ObservedModelState == unknown && a.UsageState == unknown && a.CostState == unknown && len(a.ObservedModels) == 0
	}
	if !managed || !contextDigestPattern.MatchString(u.ObservationID) || (u.BillingBasis != protocol.UsageSubscription && u.BillingBasis != unknown) ||
		!u.InputTokens.Valid() || !u.OutputTokens.Valid() || !u.CacheReadInputTokens.Valid() || !u.CacheCreationInputTokens.Valid() || !u.Cost.Valid() ||
		len(a.ObservedModels) > protocol.MaxServedModels || (a.Provider == protocol.AgentCodex && u.Cost.State != unknown) {
		return false
	}
	for i, m := range a.ObservedModels {
		if !protocol.ValidUsageModelID(m) || (i > 0 && a.ObservedModels[i-1] >= m) {
			return false
		}
	}
	tokens := u.InputTokens.State == protocol.UsageReported || u.OutputTokens.State == protocol.UsageReported || u.CacheReadInputTokens.State == protocol.UsageReported || u.CacheCreationInputTokens.State == protocol.UsageReported
	models := len(a.ObservedModels) > 0
	cost := u.Cost.State == protocol.UsageReported
	return (a.ObservedModelState == protocol.UsageProviderReported) == models && (a.UsageState == protocol.UsageProviderReported) == tokens &&
		(a.CostState == protocol.UsageProviderReported) == cost && (models || tokens || cost)
}

// Metadata lists and publication acknowledgements never include the text.
type ContextMetadata struct {
	Archived         bool                    `json:"archived,omitempty"`
	ArchivedAt       *time.Time              `json:"archivedAt,omitempty"`
	ManagedOutput    *ManagedOutputAssertion `json:"managedOutput,omitempty"`
	Name             string                  `json:"name"`
	UID              string                  `json:"uid"`
	Kind             string                  `json:"kind"`
	Key              string                  `json:"key"`
	Source           TaskClaimOwner          `json:"source"`
	SourceGeneration int64                   `json:"sourceGeneration"`
	SourceParent     string                  `json:"sourceParent"`
	Project          string                  `json:"project"`
	Repository       string                  `json:"repository"`
	Actor            TaskClaimActor          `json:"actor"`
	Provenance       TaskClaimProvenance     `json:"provenance"`
	Attribution      ContextAttribution      `json:"attribution"`
	ContentSHA256    string                  `json:"contentSHA256"`
	ByteCount        int32                   `json:"byteCount"`
	PublishedAt      time.Time               `json:"publishedAt"`
	Origin           string                  `json:"origin"`
	Completeness     string                  `json:"completeness"`
	StopState        string                  `json:"stopState"`
}

type PublishedContext struct {
	Metadata ContextMetadata `json:"metadata"`
	Text     string          `json:"text"`
}
type PublishContextResult struct {
	Outcome  string          `json:"outcome"`
	Metadata ContextMetadata `json:"metadata"`
}
type PublishedContextList struct {
	Items    []ContextMetadata `json:"items"`
	Continue string            `json:"continue,omitempty"`
}
