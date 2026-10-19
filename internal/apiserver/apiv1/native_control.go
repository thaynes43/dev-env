package apiv1

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"regexp"
	"time"
)

// The pinned native catalog fetch has its own five-second bounded fallback.
// Owner operations also perform fresh status/proof/one-use consumption before
// that fetch. Keep nested finite envelopes and cap each at the original budget
// deadline; cancellation and unknown-result no-replay remain unchanged.
const (
	NativeControlExecutionTimeout = 8 * time.Second
	NativeControlTransportTimeout = 9 * time.Second
	NativeControlRequestTimeout   = 10 * time.Second
)

// NativeControlRequest exposes a small supported native protocol, never arbitrary
// RPCs, filesystem paths, credentials, approval settings or provider overrides.
type NativeControlRequest struct {
	Binding   TaskBudgetBinding `json:"binding"`
	ID        string            `json:"id"`
	Operation string            `json:"operation"`
	ThreadID  string            `json:"threadID,omitempty"`
	TurnID    string            `json:"turnID,omitempty"`
	Text      string            `json:"text,omitempty"`
}

var nativeControlID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

func (r NativeControlRequest) Valid() bool {
	if !nativeControlID.MatchString(r.ID) || r.Binding.TaskUID == "" || r.Binding.HostID == "" || r.Binding.PodUID == "" || r.Binding.Epoch == 0 || r.Binding.Deadline.IsZero() {
		return false
	}
	thread := nativeControlID.MatchString(r.ThreadID)
	switch r.Operation {
	case "host/stop":
		return r.ThreadID == "" && r.TurnID == "" && r.Text == ""
	case "thread/start":
		return r.ThreadID == "" && r.TurnID == "" && r.Text == ""
	case "thread/read", "thread/resume":
		return thread && r.TurnID == "" && r.Text == ""
	case "turn/start":
		return thread && r.TurnID == "" && len(r.Text) > 0 && len(r.Text) <= 8192
	case "turn/interrupt":
		return thread && nativeControlID.MatchString(r.TurnID) && r.Text == ""
	}
	return false
}

type NativeControlResponse struct {
	Binding   TaskBudgetBinding `json:"binding"`
	ID        string            `json:"id"`
	Operation string            `json:"operation"`
	Result    json.RawMessage   `json:"result"`
}

func NativeControlDigest(r NativeControlRequest) string {
	raw, _ := json.Marshal(r)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

type NativeControlPermit struct {
	Binding       TaskBudgetBinding `json:"binding"`
	ID            string            `json:"id"`
	RequestSHA256 string            `json:"requestSHA256"`
	Consumed      bool              `json:"consumed"`
}

// DecodeNativeControlResponse validates the bounded envelope, not the native
// content's provenance. CRI stdout content is untrusted and must never advance
// budget, instruction-loading, model-identity or stop/continuation acceptance.
func DecodeNativeControlResponse(raw []byte, r NativeControlRequest) (NativeControlResponse, error) {
	var response NativeControlResponse
	if len(raw) > 256<<10 || !unambiguousJSON(raw) {
		return response, ErrTaskBudgetGate
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 4 || len(fields["binding"]) == 0 || len(fields["id"]) == 0 || len(fields["operation"]) == 0 || len(fields["result"]) == 0 || string(fields["result"]) == "null" {
		return response, ErrTaskBudgetGate
	}
	var binding map[string]json.RawMessage
	if json.Unmarshal(fields["binding"], &binding) != nil || len(binding) != 5 {
		return response, ErrTaskBudgetGate
	}
	for _, key := range []string{"taskUID", "epoch", "deadline", "hostID", "podUID"} {
		if len(binding[key]) == 0 {
			return response, ErrTaskBudgetGate
		}
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&response) != nil || d.Decode(new(any)) != io.EOF || !sameOwnedWireBinding(response.Binding, r.Binding) || response.ID != r.ID || response.Operation != r.Operation {
		return response, ErrTaskBudgetGate
	}
	return response, nil
}
