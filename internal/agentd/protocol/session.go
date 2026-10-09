// Package protocol holds what agentd and the operator exchange: the session
// agentd is started for, and (with the heartbeat and rescue) what it reports
// back. It depends on nothing but the standard library, so both sides import it
// without pulling the other's code.
package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// SessionEnv is the environment variable the operator sets on the session
// pod's container: the session as one JSON document (D-40). SessionFileEnv,
// when set, names a file holding the same document instead.
const (
	SessionEnv     = "AGENTD_SESSION"
	SessionFileEnv = "AGENTD_SESSION_FILE"
)

// Agent kinds and modes, as in the AgentSession spec (DESIGN-001 3.3).
const (
	AgentClaude   = "claude"
	AgentCodex    = "codex"
	AgentOpencode = "opencode"

	ModeTask   = "task"
	ModeLocal  = "local"
	ModeRemote = "remote"
)

// MaxPromptBytes caps the prompt. An environment string can hold 128 KiB
// (Linux MAX_ARG_STRLEN), and the rest of the document must fit beside it.
const MaxPromptBytes = 64 << 10

// Session is the part of an AgentSession that agentd needs. The JSON names are
// the spec's (DESIGN-001 3.3), plus the session's name. Unknown fields are
// ignored, so a newer operator can add fields an older agentd does not read.
type Session struct {
	// Workspace is the operator's explicit shared-storage binding. SessionUID
	// distinguishes an existing session from another created with the same name.
	Workspace *WorkspaceBinding `json:"workspace,omitempty"`
	// Name is the AgentSession's name: the pod name, the hostname, the
	// worktree directory and the branch suffix (agent/<name>).
	Name string `json:"name"`
	// Repo is the repository under the GitHub owner, for example haynes-ops.
	Repo string `json:"repo"`
	// Base is the ref the worktree branches from. Empty means origin's
	// default branch.
	Base   string `json:"base,omitempty"`
	Agent  string `json:"agent"`
	Mode   string `json:"mode"`
	Model  string `json:"model"`
	Effort string `json:"effort,omitempty"`
	// Prompt is the task. Task mode only.
	Prompt string  `json:"prompt,omitempty"`
	Limits *Limits `json:"limits,omitempty"`
	// Restore names a rescue on the shared volume, <session>/<name> (D-67).
	// On the first boot, after the clone, agentd fetches that rescue's bundle
	// for this repo into refs/rescued/*.
	Restore string `json:"restore,omitempty"`
}

// WorkspaceBinding is immutable session metadata, not a storage provisioning request.
type WorkspaceBinding struct {
	ID         string `json:"id"`
	SessionUID string `json:"sessionUID"`
}

// Limits caps a task session (V-02).
type Limits struct {
	// Timeout is a Go duration string, as metav1.Duration marshals it
	// ("40m0s"). Empty means no wall-clock limit.
	Timeout string `json:"timeout,omitempty"`
	// MaxTurns caps the agent's turns. 0 means no cap.
	MaxTurns int32 `json:"maxTurns,omitempty"`
}

// TimeoutDuration parses Limits.Timeout. Zero means no limit.
func (s Session) TimeoutDuration() time.Duration {
	if s.Limits == nil || s.Limits.Timeout == "" {
		return 0
	}
	d, err := time.ParseDuration(s.Limits.Timeout)
	if err != nil {
		return 0
	}
	return d
}

// MaxTurns returns the turn cap, 0 for none.
func (s Session) MaxTurns() int32 {
	if s.Limits == nil {
		return 0
	}
	return s.Limits.MaxTurns
}

// ParseSession decodes and validates a session document.
func ParseSession(data []byte) (Session, error) {
	var s Session
	if err := json.Unmarshal(data, &s); err != nil {
		return Session{}, fmt.Errorf("session document: %w", err)
	}
	if err := s.Validate(); err != nil {
		return Session{}, err
	}
	return s, nil
}

var (
	// dnsLabel is RFC 1123: the session name is a pod name and a hostname.
	dnsLabel = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	// repoName is a GitHub repository name. It becomes a directory under
	// ~/repos, so it may not be "." or "..".
	repoName = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	// claudeModelID is a full Claude model id: claude-<family>-<version...>,
	// with an optional [1m] context suffix. Aliases (opus, sonnet, fable,
	// default, opusplan) never match.
	claudeModelID = regexp.MustCompile(`^claude-[a-z0-9]+(-[a-z0-9.]+)+(\[1m\])?$`)
	// gitRef rejects option-like and whitespace-bearing refs; git checks the rest.
	gitRef = regexp.MustCompile(`^[^-\s][^\s]*$`)
	// effortLevel is a level name such as xhigh; the API checks it per model.
	effortLevel = regexp.MustCompile(`^[a-z]+$`)
)

// Validate checks the fields agentd relies on.
func (s Session) Validate() error {
	var errs []error
	if s.Workspace != nil {
		if len(s.Workspace.ID) == 0 || len(s.Workspace.ID) > 63 || !dnsLabel.MatchString(s.Workspace.ID) {
			errs = append(errs, errors.New("workspace.id must be a DNS label of at most 63 characters"))
		}
		if len(s.Workspace.SessionUID) == 0 || len(s.Workspace.SessionUID) > 128 || !repoName.MatchString(s.Workspace.SessionUID) {
			errs = append(errs, errors.New("workspace.sessionUID must identify the existing session"))
		}
	}
	if len(s.Name) == 0 || len(s.Name) > 63 || !dnsLabel.MatchString(s.Name) {
		errs = append(errs, fmt.Errorf("name %q is not a DNS label of at most 63 characters", s.Name))
	}
	if !repoName.MatchString(s.Repo) || s.Repo == "." || s.Repo == ".." {
		errs = append(errs, fmt.Errorf("repo %q is not a repository name", s.Repo))
	}
	if s.Base != "" && !gitRef.MatchString(s.Base) {
		errs = append(errs, fmt.Errorf("base %q is not a ref", s.Base))
	}
	if IsRescueSnapshot(s.Base) {
		errs = append(errs, fmt.Errorf("base %q is a rescued snapshot of uncommitted work, which can hold secrets; a session never branches from one (D-67)", s.Base))
	}
	if s.Restore != "" {
		if _, _, err := ParseRescueID(s.Restore); err != nil {
			errs = append(errs, err)
		}
	}
	switch s.Agent {
	case AgentClaude:
		if err := ValidateClaudeModel(s.Model); err != nil {
			errs = append(errs, err)
		}
	case AgentCodex, AgentOpencode:
		if strings.TrimSpace(s.Model) == "" {
			errs = append(errs, errors.New("model is empty"))
		}
	default:
		errs = append(errs, fmt.Errorf("agent %q is not claude, codex or opencode", s.Agent))
	}
	switch s.Mode {
	case ModeTask:
		if strings.TrimSpace(s.Prompt) == "" {
			errs = append(errs, errors.New("task mode needs a prompt"))
		}
	case ModeLocal, ModeRemote:
		if s.Prompt != "" {
			errs = append(errs, fmt.Errorf("a prompt is for task mode only, not %s", s.Mode))
		}
	default:
		errs = append(errs, fmt.Errorf("mode %q is not task, local or remote", s.Mode))
	}
	if len(s.Prompt) > MaxPromptBytes {
		errs = append(errs, fmt.Errorf("prompt is %d bytes, more than %d", len(s.Prompt), MaxPromptBytes))
	}
	if s.Effort != "" && !effortLevel.MatchString(s.Effort) {
		errs = append(errs, fmt.Errorf("effort %q is not a level name", s.Effort))
	}
	if s.Limits != nil {
		if s.Limits.Timeout != "" {
			if d, err := time.ParseDuration(s.Limits.Timeout); err != nil || d <= 0 {
				errs = append(errs, fmt.Errorf("limits.timeout %q is not a positive duration", s.Limits.Timeout))
			}
		}
		if s.Limits.MaxTurns < 0 {
			errs = append(errs, fmt.Errorf("limits.maxTurns %d is negative", s.Limits.MaxTurns))
		}
	}
	return errors.Join(errs...)
}

// ValidateClaudeModel accepts full Claude model ids only (CLAUDE.md: full model
// ids, never aliases). An alias resolves on the client against the pinned CLI
// and can silently serve an older model.
func ValidateClaudeModel(id string) error {
	if !claudeModelID.MatchString(id) || !strings.ContainsAny(id, "0123456789") {
		return fmt.Errorf("model %q is not a full Claude model id (for example claude-opus-5-5); aliases are refused", id)
	}
	return nil
}
