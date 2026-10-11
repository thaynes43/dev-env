package apiv1

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"time"
	"unicode/utf8"
)

const PublishedContextsPath = "/v1/published-contexts"
const MaxContextBytes = 64 << 10
const MaxContextRequestBytes = 6*MaxContextBytes + 4096

var contextKeyPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]{0,126}[a-z0-9])?$`)
var contextUIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,128}$`)
var contextNamePattern = regexp.MustCompile(`^published-context\.[a-f0-9]{32}\.[a-f0-9]{32}$`)

func ValidContextRequest(r PublishContextRequest) bool {
	return (r.Kind == "transcript-excerpt" || r.Kind == "result" || r.Kind == "memory" || r.Kind == "handoff") &&
		contextKeyPattern.MatchString(r.Key) && len(r.Text) > 0 && len(r.Text) <= MaxContextBytes && utf8.ValidString(r.Text)
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
type PublishContextRequest struct {
	Kind string `json:"kind"`
	Key  string `json:"key"`
	Text string `json:"text"`
}

type ContextAttribution struct {
	Provider           string `json:"provider"`
	RequestedModel     string `json:"requestedModel"`
	ObservedModelState string `json:"observedModelState"`
	UsageState         string `json:"usageState"`
	CostState          string `json:"costState"`
}

// Metadata lists and publication acknowledgements never include the text.
type ContextMetadata struct {
	Name             string              `json:"name"`
	UID              string              `json:"uid"`
	Kind             string              `json:"kind"`
	Key              string              `json:"key"`
	Source           TaskClaimOwner      `json:"source"`
	SourceGeneration int64               `json:"sourceGeneration"`
	SourceParent     string              `json:"sourceParent"`
	Project          string              `json:"project"`
	Repository       string              `json:"repository"`
	Actor            TaskClaimActor      `json:"actor"`
	Provenance       TaskClaimProvenance `json:"provenance"`
	Attribution      ContextAttribution  `json:"attribution"`
	ContentSHA256    string              `json:"contentSHA256"`
	ByteCount        int32               `json:"byteCount"`
	PublishedAt      time.Time           `json:"publishedAt"`
	Origin           string              `json:"origin"`
	Completeness     string              `json:"completeness"`
	StopState        string              `json:"stopState"`
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
