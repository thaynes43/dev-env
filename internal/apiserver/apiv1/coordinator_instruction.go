package apiv1

import "net/url"

// Coordinator instruction routes. Delivery and native consumption are different
// operations. Neither route is a grant, a human decision, or an inbox read ACK.
const (
	InstructionOutcomeCreated   = "Created"
	InstructionOutcomeConfirmed = "Confirmed"
	InstructionOutcomeConsumed  = "Consumed"
	InstructionPhaseActive      = "active"
	InstructionPhaseEnded       = "ended"
)

// SessionInstructionsPath is POST /v1/sessions/{name}/instructions.
func SessionInstructionsPath(name string) string {
	return SessionPath(url.PathEscape(name)) + "/instructions"
}

// SessionInstructionPath is GET /v1/sessions/{name}/instructions/{key}.
func SessionInstructionPath(name, key string) string {
	return SessionInstructionsPath(name) + "/" + url.PathEscape(key)
}

// SessionInstructionConsumePath is POST .../instructions/{key}/consume.
func SessionInstructionConsumePath(name, key string) string {
	return SessionInstructionPath(name, key) + "/consume"
}

// SessionInstructionAttemptPath is POST /v1/sessions/{name}/instruction-attempt.
func SessionInstructionAttemptPath(name string) string {
	return SessionPath(url.PathEscape(name)) + "/instruction-attempt"
}

// CoordinatorInstructionRequest is a designated parent's delivery. The server
// derives the sender. Scope fields must match the target's stored scope; they
// do not grant a wider one.
type CoordinatorInstructionRequest struct {
	Key              string `json:"key"`
	Project          string `json:"project"`
	Repository       string `json:"repository"`
	TaskKey          string `json:"taskKey"`
	TargetSessionUID string `json:"targetSessionUID"`
	TargetPodUID     string `json:"targetPodUID"`
	AttemptID        string `json:"attemptID"`
	Text             string `json:"text"`
}

// CoordinatorInstructionReceipt is acceptance of a delivery. It has no nonce
// and no instruction text. Consumed stays false until a separate consume call.
type CoordinatorInstructionReceipt struct {
	Outcome          string `json:"outcome"`
	Key              string `json:"key"`
	MessageName      string `json:"messageName"`
	MessageUID       string `json:"messageUID"`
	TextSHA256       string `json:"textSHA256"`
	TargetSessionUID string `json:"targetSessionUID"`
	TargetPodUID     string `json:"targetPodUID"`
	AttemptID        string `json:"attemptID"`
	Project          string `json:"project"`
	Repository       string `json:"repository"`
	TaskKey          string `json:"taskKey"`
	Consumed         bool   `json:"consumed"`
}

// CoordinatorInstructionView is a read. Text and nonce are returned only to
// the target session. SenderUID is the server-authored parent identity.
// ReadAck is the inbox read acknowledgement, which is not native consumption.
type CoordinatorInstructionView struct {
	CoordinatorInstructionReceipt
	SenderUID string `json:"senderUID,omitempty"`
	Text      string `json:"text,omitempty"`
	Nonce     string `json:"nonce,omitempty"`
	ReadAck   bool   `json:"readAck"`
}

// CoordinatorInstructionCheckpoint is the active recipient attempt's pending
// list. Pending is empty when nothing is unconsumed. The call does not consume.
type CoordinatorInstructionCheckpoint struct {
	Outcome    string                       `json:"outcome"`
	AttemptID  string                       `json:"attemptID"`
	SessionUID string                       `json:"sessionUID"`
	PodUID     string                       `json:"podUID"`
	Pending    []CoordinatorInstructionView `json:"pending"`
}

// CoordinatorInstructionConsumeRequest is the target task's consume call.
// Identity fields must match the live session, pod and registered attempt.
type CoordinatorInstructionConsumeRequest struct {
	AttemptID        string `json:"attemptID"`
	Nonce            string `json:"nonce"`
	TargetSessionUID string `json:"targetSessionUID"`
	TargetPodUID     string `json:"targetPodUID"`
}

// CoordinatorInstructionConsumeACK correlates one native consumption with one
// delivery. It is not an inbox read acknowledgement.
type CoordinatorInstructionConsumeACK struct {
	Outcome          string `json:"outcome"`
	Key              string `json:"key"`
	MessageUID       string `json:"messageUID"`
	AttemptID        string `json:"attemptID"`
	Nonce            string `json:"nonce"`
	TextSHA256       string `json:"textSHA256"`
	TargetSessionUID string `json:"targetSessionUID"`
	TargetPodUID     string `json:"targetPodUID"`
}

// CoordinatorInstructionAttemptRequest names the attempt the caller's own pod
// is running. The server copies session and pod identity from the token.
type CoordinatorInstructionAttemptRequest struct {
	AttemptID string `json:"attemptID"`
	Ended     bool   `json:"ended,omitempty"`
}

// CoordinatorInstructionAttemptResult is the attempt the server stored.
type CoordinatorInstructionAttemptResult struct {
	AttemptID  string `json:"attemptID"`
	Phase      string `json:"phase"`
	SessionUID string `json:"sessionUID"`
	PodUID     string `json:"podUID"`
}
