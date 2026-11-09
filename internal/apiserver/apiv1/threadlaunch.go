package apiv1

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
)

// ThreadLaunchVersion identifies `agent-run thread start`'s result (DESIGN-057).
// It is a client and in-host observation contract, not an operator API.
const ThreadLaunchVersion = "thread-launch.v1"

// ThreadLaunchMaxBytes bounds one encoded result, newline included.
const ThreadLaunchMaxBytes = 32 << 10

// Thread launch phases, in launch order (DESIGN-057 section 8).
const (
	ThreadPhaseHostReady     = "host_ready"
	ThreadPhasePaired        = "paired"
	ThreadPhaseClientStarted = "client_started"
	ThreadPhaseThreadCreated = "thread_created"
	ThreadPhaseSourceVisible = "source_visible"
	ThreadPhaseHandoff       = "handoff"
	ThreadPhaseFirstResponse = "first_response"
)

// ThreadLaunchPhases lists every phase a result carries.
var ThreadLaunchPhases = []string{ThreadPhaseHostReady, ThreadPhasePaired, ThreadPhaseClientStarted, ThreadPhaseThreadCreated, ThreadPhaseSourceVisible, ThreadPhaseHandoff, ThreadPhaseFirstResponse}

// Thread launch outcomes and the exit code each carries. Only responded is a
// success: a live pod, a running client or an accepted handoff never is.
const (
	ThreadResponded      = "responded"
	ThreadVisible        = "visible"
	ThreadActionRequired = "action_required"
	ThreadTimedOut       = "timed_out"
	ThreadHidden         = "hidden"
	ThreadUnconfirmed    = "unconfirmed"
	ThreadFailed         = "failed"
	ThreadBlocked        = "blocked"
)

// ThreadLaunchExit is the process exit code for an outcome, in agent-run's
// convention: 0 ok, 1 failed, 6 unconfirmed, 7 pending or owner action. An
// unknown outcome is a failure.
func ThreadLaunchExit(outcome string) int {
	switch outcome {
	case ThreadResponded:
		return 0
	case ThreadVisible, ThreadActionRequired, ThreadTimedOut:
		return 7
	case ThreadUnconfirmed:
		return 6
	default:
		return 1
	}
}

// ThreadLaunchResult is one launch's bounded metadata. It never carries the
// handoff, the response, a pairing code or native message text. Link is the
// provider link when one exists (Claude Remote Control), else null.
type ThreadLaunchResult struct {
	Link           *string                  `json:"link"`
	Version        string                   `json:"version"`
	Outcome        string                   `json:"outcome"`
	Provider       string                   `json:"provider"`
	Host           string                   `json:"host,omitempty"`
	SessionName    string                   `json:"sessionName,omitempty"`
	Name           string                   `json:"name,omitempty"`
	ThreadID       string                   `json:"threadId,omitempty"`
	Source         string                   `json:"source,omitempty"`
	Cwd            string                   `json:"cwd,omitempty"`
	RequestedModel string                   `json:"requestedModel,omitempty"`
	Permission     *ThreadPermission        `json:"permission"`
	Phases         map[string]RCLaunchPhase `json:"phases"`
	TotalMs        int64                    `json:"totalMs"`
	DeadlineMs     int64                    `json:"deadlineMs"`
	Error          *RCLaunchError           `json:"error"`
}

// Permission modes. Always-allow is the launch contract for every qualified
// provider (Tom, 2026-11-09): the caller never picks a flag.
const (
	ThreadPermissionAlwaysAllow  = "always-allow"
	ThreadPermissionNotQualified = "not-qualified"
)

// ThreadPermission is the effective permission mode of a launch, reported from
// preflight on. Flag is the one provider flag the launch always passes.
// NotBypassed names the known prompts that mode does not cover.
type ThreadPermission struct {
	Mode        string   `json:"mode"`
	Flag        string   `json:"flag,omitempty"`
	Source      string   `json:"source"`
	NotBypassed []string `json:"notBypassed"`
}

var threadLaunchToken = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:/-]{0,255}$`)

// DecodeThreadLaunch reads exactly one result document, as agent-run receives
// it from a host. It refuses unknown fields, trailing data, an unknown version
// or outcome, a missing phase and an error that disagrees with the outcome.
func DecodeThreadLaunch(raw []byte) (ThreadLaunchResult, error) {
	var r ThreadLaunchResult
	if len(raw) == 0 || len(raw) > ThreadLaunchMaxBytes {
		return r, errors.New("thread launch result size")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return ThreadLaunchResult{}, errors.New("thread launch result is not valid JSON")
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		return ThreadLaunchResult{}, errors.New("thread launch result has trailing data")
	}
	if r.Version != ThreadLaunchVersion || !threadLaunchToken.MatchString(r.Provider) || len(r.Phases) != len(ThreadLaunchPhases) {
		return ThreadLaunchResult{}, errors.New("thread launch result is not " + ThreadLaunchVersion)
	}
	switch r.Outcome {
	case ThreadResponded, ThreadVisible, ThreadActionRequired, ThreadTimedOut, ThreadHidden, ThreadUnconfirmed, ThreadFailed, ThreadBlocked:
	default:
		return ThreadLaunchResult{}, errors.New("thread launch result outcome")
	}
	for _, p := range ThreadLaunchPhases {
		if _, ok := r.Phases[p]; !ok {
			return ThreadLaunchResult{}, errors.New("thread launch result phase missing")
		}
	}
	if r.Permission == nil || (r.Permission.Mode != ThreadPermissionAlwaysAllow && r.Permission.Mode != ThreadPermissionNotQualified) || (r.Permission.Mode == ThreadPermissionAlwaysAllow) != (r.Permission.Flag != "") {
		return ThreadLaunchResult{}, errors.New("thread launch result permission mode")
	}
	if r.Outcome == ThreadResponded && r.Permission.Mode != ThreadPermissionAlwaysAllow {
		return ThreadLaunchResult{}, errors.New("thread launch result responded without always-allow")
	}
	if r.Outcome == ThreadResponded && (r.Phases[ThreadPhaseFirstResponse].State != "ok" || r.ThreadID == "") {
		return ThreadLaunchResult{}, errors.New("thread launch result responded without an observed response")
	}
	if (r.Outcome == ThreadResponded) != (r.Error == nil) {
		return ThreadLaunchResult{}, errors.New("thread launch result error disagrees with outcome")
	}
	for _, s := range []string{r.Host, r.SessionName, r.Name, r.ThreadID, r.Source, r.RequestedModel} {
		if s != "" && !threadLaunchToken.MatchString(s) {
			return ThreadLaunchResult{}, errors.New("thread launch result field")
		}
	}
	return r, nil
}
