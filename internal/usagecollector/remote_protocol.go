package usagecollector

import (
	"bytes"
	"encoding/json"
	"io"
	"regexp"
	"time"

	"github.com/thaynes43/dev-env/internal/providerallowance"
)

const RemoteSchemaVersion = 1
const RemoteMaxBytes = 128 << 10

type ProviderBinding struct {
	Provider      string  `json:"provider"`
	AccountRef    string  `json:"accountRef"`
	WorkspaceRef  *string `json:"workspaceRef"`
	SourceVersion string  `json:"sourceVersion"`
}

// RemoteRequest selects only fixed source readers and public owner metadata.
// There are no executable, shell, prompt, credential or arbitrary path fields.
type RemoteRequest struct {
	Version     int               `json:"version"`
	Operation   string            `json:"operation"`
	RootName    string            `json:"rootName"`
	SessionUID  string            `json:"sessionUID"`
	PodUID      string            `json:"podUID"`
	Fingerprint string            `json:"fingerprint"`
	Cycle       int64             `json:"cycle"`
	Nonce       string            `json:"nonce"`
	HelperSHA   string            `json:"helperSHA256"`
	ValidUntil  string            `json:"validUntil"`
	Providers   []ProviderBinding `json:"providers"`
}

type RemoteCapture struct {
	Provider   string          `json:"provider"`
	CapturedAt string          `json:"capturedAt"`
	ReceivedAt string          `json:"receivedAt"`
	Payload    json.RawMessage `json:"payload"`
}

type RemoteAttempt struct {
	Provider    string    `json:"provider"`
	StartedAt   time.Time `json:"startedAt"`
	CompletedAt time.Time `json:"completedAt"`
	Success     bool      `json:"success"`
}

// RemoteReceipt is a machine acquisition/cleanup receipt, never a native-agent
// acknowledgement. Original samples and attempt times survive transport replay.
type RemoteReceipt struct {
	Version       int               `json:"version"`
	State         string            `json:"state"`
	RootName      string            `json:"rootName"`
	SessionUID    string            `json:"sessionUID"`
	PodUID        string            `json:"podUID"`
	Fingerprint   string            `json:"fingerprint"`
	Cycle         int64             `json:"cycle"`
	Nonce         string            `json:"nonce"`
	HelperSHA     string            `json:"helperSHA256"`
	CleanupProven bool              `json:"cleanupProven"`
	Captures      []RemoteCapture   `json:"captures"`
	Attempts      []RemoteAttempt   `json:"attempts"`
	Bindings      []ProviderBinding `json:"bindings"`
}

var remoteUUID = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)
var remoteHash = regexp.MustCompile(`^[a-f0-9]{64}$`)
var remoteAccount = regexp.MustCompile(`^(openai|anthropic|cursor)-email-sha256:[a-f0-9]{64}$`)

func ParseRemoteRequest(raw []byte, now time.Time) (RemoteRequest, error) {
	fail := func() (RemoteRequest, error) { return RemoteRequest{}, ErrCapture }
	if now.IsZero() || len(raw) > 16<<10 || !providerallowance.ValidJSONDocument(raw, 16<<10) || !providerallowance.ExactObjectKeys(raw, []string{"version", "operation", "rootName", "sessionUID", "podUID", "fingerprint", "cycle", "nonce", "helperSHA256", "validUntil", "providers"}) {
		return fail()
	}
	var request RemoteRequest
	var children struct {
		Providers []json.RawMessage `json:"providers"`
	}
	if json.Unmarshal(raw, &children) != nil {
		return fail()
	}
	for _, child := range children.Providers {
		if !providerallowance.ExactObjectKeys(child, []string{"provider", "accountRef", "workspaceRef", "sourceVersion"}) {
			return fail()
		}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&request) != nil || dec.Decode(new(any)) != io.EOF || request.Version != RemoteSchemaVersion || request.Operation != "capture" && request.Operation != "status" || !token.MatchString(request.RootName) || !remoteUUID.MatchString(request.SessionUID) || !remoteUUID.MatchString(request.PodUID) || !remoteHash.MatchString(request.Fingerprint) || request.Cycle < 1 || request.Cycle > 1_000_000 || !remoteUUID.MatchString(request.Nonce) || !remoteHash.MatchString(request.HelperSHA) || request.Providers == nil {
		return fail()
	}
	until, err := time.Parse(time.RFC3339Nano, request.ValidUntil)
	if err != nil || request.Operation == "capture" && (!until.After(now) || until.After(now.Add(7*24*time.Hour))) {
		return fail()
	}
	if request.Operation == "status" {
		if len(request.Providers) != 0 {
			return fail()
		}
		return request, nil
	}
	expected := map[string]string{"openai": providerallowance.CodexRPCSourceVersion, "anthropic": providerallowance.ClaudeSDKSourceVersion, "cursor": providerallowance.CursorCLISourceVersion}
	if len(request.Providers) != len(expected) {
		return fail()
	}
	for _, source := range request.Providers {
		version, ok := expected[source.Provider]
		if !ok || source.SourceVersion != version || !remoteAccount.MatchString(source.AccountRef) || len(source.AccountRef) < len(source.Provider)+1 || source.AccountRef[:len(source.Provider)+1] != source.Provider+"-" || source.WorkspaceRef != nil && !token.MatchString(*source.WorkspaceRef) {
			return fail()
		}
		delete(expected, source.Provider)
	}
	return request, nil
}

// ParseRemoteReceipt validates transport framing and owner/intent identity.
// Caller registry and original-capture validation remain separate requirements.
func ParseRemoteReceipt(raw []byte, request RemoteRequest) (RemoteReceipt, error) {
	fail := func() (RemoteReceipt, error) { return RemoteReceipt{}, ErrCapture }
	if len(raw) > RemoteMaxBytes || !providerallowance.ValidJSONDocument(raw, RemoteMaxBytes) {
		return fail()
	}
	if providerallowance.ExactObjectKeys(raw, []string{"version", "state"}) {
		var busy struct {
			Version int    `json:"version"`
			State   string `json:"state"`
		}
		if json.Unmarshal(raw, &busy) == nil && busy.Version == RemoteSchemaVersion && busy.State == "busy" {
			return RemoteReceipt{Version: RemoteSchemaVersion, State: "busy"}, nil
		}
		return fail()
	}
	if !providerallowance.ExactObjectKeys(raw, []string{"version", "state", "rootName", "sessionUID", "podUID", "fingerprint", "cycle", "nonce", "helperSHA256", "cleanupProven", "captures", "attempts", "bindings"}) {
		return fail()
	}
	var children struct {
		Captures []json.RawMessage `json:"captures"`
		Attempts []json.RawMessage `json:"attempts"`
		Bindings []json.RawMessage `json:"bindings"`
	}
	if json.Unmarshal(raw, &children) != nil {
		return fail()
	}
	for _, child := range children.Captures {
		if !providerallowance.ExactObjectKeys(child, []string{"provider", "capturedAt", "receivedAt", "payload"}) {
			return fail()
		}
	}
	for _, child := range children.Attempts {
		if !providerallowance.ExactObjectKeys(child, []string{"provider", "startedAt", "completedAt", "success"}) {
			return fail()
		}
	}
	for _, child := range children.Bindings {
		if !providerallowance.ExactObjectKeys(child, []string{"provider", "accountRef", "workspaceRef", "sourceVersion"}) {
			return fail()
		}
	}
	var receipt RemoteReceipt
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&receipt) != nil || dec.Decode(new(any)) != io.EOF || receipt.Version != RemoteSchemaVersion || receipt.RootName != request.RootName || receipt.SessionUID != request.SessionUID || receipt.PodUID != request.PodUID || receipt.Fingerprint != request.Fingerprint || receipt.Cycle != request.Cycle || receipt.Nonce != request.Nonce || receipt.HelperSHA != request.HelperSHA || receipt.Captures == nil || receipt.Attempts == nil || receipt.Bindings == nil {
		return fail()
	}
	switch receipt.State {
	case "complete":
		if !receipt.CleanupProven || len(receipt.Attempts) != 3 || len(receipt.Captures) > 3 || len(receipt.Bindings) != len(receipt.Captures) {
			return fail()
		}
	case "never_started":
		if !receipt.CleanupProven || len(receipt.Attempts) != 0 || len(receipt.Captures) != 0 || len(receipt.Bindings) != 0 {
			return fail()
		}
	case "uncertain":
		if receipt.CleanupProven || len(receipt.Attempts) != 0 || len(receipt.Captures) != 0 || len(receipt.Bindings) != 0 {
			return fail()
		}
	default:
		return fail()
	}
	return receipt, nil
}
