package apiv1

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// ThreadLaunchVersion identifies `agent-run thread start`'s result (DESIGN-057).
// The host's agentd prints it; the operator's thread route (section 10) relays
// the same document to a caller with no exec of its own.
const ThreadLaunchVersion = "thread-launch.v1"

// SessionThreadsPath is a Codex host session's thread route, POST (DESIGN-057
// section 10): the operator runs agentd ctl thread-start in that host for the
// caller, as it delivers a message (D-65). It answers 200 with one
// ThreadLaunchResult once it has refused the host or run the exec; any other
// status is an Error, sent before any exec.
func SessionThreadsPath(name string) string { return SessionPath(name) + "/threads" }

// Thread start bounds, one rule for every caller and every provider.
const (
	// ThreadMaxHandoffBytes caps the handoff, the thread's first message.
	ThreadMaxHandoffBytes = 4096
	// ThreadMaxWait caps --wait, the whole launch.
	ThreadMaxWait = 10 * time.Minute
)

// ThreadStartRequest is the body of POST /v1/sessions/{name}/threads. The
// operator takes the caller from the token; nothing here names one.
type ThreadStartRequest struct {
	// Model is a full Codex model id and Effort a level it takes.
	Model  string `json:"model"`
	Effort string `json:"effort"`
	// Name is the thread's one-use tmux session name on the host (ValidThreadName).
	Name string `json:"name"`
	// Repo is the host's ~/repos/<repo>; empty is the host's own repo.
	Repo string `json:"repo,omitempty"`
	// Wait is a positive Go duration, at most ThreadMaxWait, for example 5m0s.
	Wait string `json:"wait"`
	// Handoff travels to the host on the exec's stdin only (ValidThreadHandoff).
	Handoff string `json:"handoff"`
}

var threadName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// ValidThreadName is the rule for a thread's name on its host.
func ValidThreadName(s string) bool { return threadName.MatchString(s) }

// ValidThreadHandoff is the one handoff rule: 1 to ThreadMaxHandoffBytes UTF-8
// bytes, not blank, and no control character but newline and tab (C0, DEL and
// C1 are refused, as a message's are, D-65).
func ValidThreadHandoff(s string) bool {
	if len(s) == 0 || len(s) > ThreadMaxHandoffBytes || !utf8.ValidString(s) || strings.TrimSpace(s) == "" {
		return false
	}
	for _, r := range s {
		if r != '\n' && r != '\t' && (r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f)) {
			return false
		}
	}
	return true
}

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
