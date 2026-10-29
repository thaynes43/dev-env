package apiv1

import "time"

// RCLaunchVersion identifies the client-only observation contract. It is not an
// operator capability or proof that a provider is connected and ready for input.
const RCLaunchVersion = "rc-launch.v1"

// RCLaunchResult contains only bounded metadata, never handoff text or raw API
// messages. Link is first so authorized JSON consumers encounter it first.
type RCLaunchResult struct {
	Link           *string                  `json:"link"`
	Version        string                   `json:"version"`
	Outcome        string                   `json:"outcome"`
	SessionName    string                   `json:"sessionName,omitempty"`
	IdempotencyKey string                   `json:"idempotencyKey,omitempty"`
	Project        string                   `json:"project,omitempty"`
	Agent          string                   `json:"agent,omitempty"`
	RequestedModel string                   `json:"requestedModel,omitempty"`
	Repo           string                   `json:"repo,omitempty"`
	Base           string                   `json:"base,omitempty"`
	RCRegistered   bool                     `json:"rcRegistered"`
	Phases         map[string]RCLaunchPhase `json:"phases"`
	PollIntervalMs int64                    `json:"pollIntervalMs"`
	Polls          int                      `json:"polls"`
	TotalMs        int64                    `json:"totalMs"`
	DeadlineMs     int64                    `json:"deadlineMs"`
	Error          *RCLaunchError           `json:"error"`
}

// RCLaunchPhase reports when this invocation first observed the current state.
// SinceRequestMs uses the client's monotonic clock. The existing Session view
// supplies no source time for these observations; SourceAt remains null.
type RCLaunchPhase struct {
	State           string     `json:"state"`
	FirstObservedAt time.Time  `json:"firstObservedAt"`
	SinceRequestMs  int64      `json:"sinceRequestMs"`
	SourceAt        *time.Time `json:"sourceAt"`
	EvidenceSource  string     `json:"evidenceSource"`
	Reason          string     `json:"reason"`
}

// RCLaunchError is a fixed diagnostic, without reflected server or input text.
type RCLaunchError struct {
	Code  string `json:"code"`
	Phase string `json:"phase,omitempty"`
}
