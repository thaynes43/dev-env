// Package protocol holds what agentd and the operator exchange: the session
// agentd is started for, and (with the heartbeat and rescue) what it reports
// back. It depends on nothing but the standard library, so both sides import it
// without pulling the other's code.
package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

// SessionEnv is the environment variable the operator sets on the session
// pod's container: the session as one JSON document (D-40). SessionFileEnv,
// when set, names a file holding the same document instead.
const (
	// Controller-owned deployment metadata, never a credential or approval.
	CodexCoordinatorHostEnv = "AGENTD_CODEX_COORDINATOR_HOST"
	CodexCoordinatorUIDEnv  = "AGENTD_CODEX_COORDINATOR_SESSION_UID"
	SessionEnv              = "AGENTD_SESSION"
	SessionFileEnv          = "AGENTD_SESSION_FILE"
)

// Agent kinds and modes, as in the AgentSession spec (DESIGN-001 3.3).
const (
	AgentClaude = "claude"
	AgentCodex  = "codex"
	// AgentCursor runs the Cursor CLI headless on one task (DESIGN-058).
	AgentCursor   = "cursor"
	AgentOpencode = "opencode"

	ModeTask   = "task"
	ModeLocal  = "local"
	ModeRemote = "remote"
)

// MaxPromptBytes caps the prompt. An environment string can hold 128 KiB
// (Linux MAX_ARG_STRLEN), and the rest of the document must fit beside it.
const MaxPromptBytes = 64 << 10

// MaxSessionBytes leaves room for the environment key and terminating NUL.
const MaxSessionBytes = (128 << 10) - 64

// The explicit retained-common route uses a source-owned DownwardAPI file.
// Its serialized document must also fit in the Pod's bounded annotations.
const MaxSessionFileBytes = 192 << 10
const SessionInputPath = "/opt/dev-env/session/session.json"
const SessionInputAnnotation = "dev-env.haynesops.com/retained-session-input"

// Session is the part of an AgentSession that agentd needs. The JSON names are
// the spec's (DESIGN-001 3.3), plus the session's name. Unknown fields are
// ignored, so a newer operator can add fields an older agentd does not read.
type Session struct {
	// PrivateHistoryRetained is the operator-owned retention latch; workspace absence alone is insufficient.
	PrivateHistoryRetained bool            `json:"privateHistoryRetained,omitempty"`
	CommonRules            json.RawMessage `json:"commonRules,omitempty"`
	CaptureOutput          bool            `json:"captureOutput,omitempty"`
	SessionUID             string          `json:"sessionUID,omitempty"`
	ProjectSnapshot        json.RawMessage `json:"projectSnapshot,omitempty"`
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
	Base  string `json:"base,omitempty"`
	Agent string `json:"agent"`
	Mode  string `json:"mode"`
	Model string `json:"model"`
	// Title is optional role text (DESIGN-065). Empty is omitted, so a session
	// without a title keeps today's document. An older agentd ignores the key.
	Title  string `json:"title,omitempty"`
	Effort string `json:"effort,omitempty"`
	// Prompt is the task. Required in task mode. A remote session may carry
	// one only when Caller is set, which is a summoned session (DESIGN-064
	// 5.6). Local mode refuses it.
	Prompt string `json:"prompt,omitempty"`
	// Caller is the CallerPolicy of a summoned session, copied from
	// spec.caller. Empty for an ordinary session.
	Caller string `json:"caller,omitempty"`
	// FallbackModel is a summoned session's fallback, a full Claude id,
	// copied from spec.summon.fallbackModel (DESIGN-064 6.9, OP791). A
	// summoned task's model pre-flight tries it once when Model did not
	// answer. Empty for an ordinary session, and for a document an older
	// operator wrote; an older agentd ignores the key.
	FallbackModel string  `json:"fallbackModel,omitempty"`
	Limits        *Limits `json:"limits,omitempty"`
	// Restore names a rescue on the shared volume, <session>/<name> (D-67).
	// On the first boot, after the clone, agentd fetches that rescue's bundle
	// for this repo into refs/rescued/*.
	Restore string `json:"restore,omitempty"`
}

// WorkspaceBinding is immutable session metadata, not a storage provisioning request.
type WorkspaceBinding struct {
	ID         string `json:"id"`
	SessionUID string `json:"sessionUID"`
	// StopProof is issued only by the controller for a distinct hold-rescue Pod.
	StopProof *WorkspaceStopProof `json:"stopProof,omitempty"`
}

// WorkspaceStopProof records the controller's uncached termination and node
// health verification. The old Pod remains available for a fresh recheck.
type WorkspaceStopProof struct {
	Version              int       `json:"version"`
	Workspace            string    `json:"workspace"`
	Task                 string    `json:"task"`
	SessionUID           string    `json:"sessionUID"`
	PodName              string    `json:"podName"`
	PodUID               string    `json:"podUID"`
	PodResourceVersion   string    `json:"podResourceVersion"`
	NodeName             string    `json:"nodeName"`
	NodeUID              string    `json:"nodeUID"`
	LeaseResourceVersion string    `json:"leaseResourceVersion"`
	LeaseRenewedAt       time.Time `json:"leaseRenewedAt"`
	VerifiedAt           time.Time `json:"verifiedAt"`
}

func (p WorkspaceStopProof) Validate(workspace, task, sessionUID, podUID string) error {
	if p.Version != 1 || p.Workspace != workspace || p.Task != task || p.SessionUID != sessionUID || p.PodName != task || p.PodUID != podUID ||
		p.PodUID == "" || p.PodResourceVersion == "" || p.NodeName == "" || p.NodeUID == "" || p.LeaseResourceVersion == "" ||
		p.VerifiedAt.IsZero() || p.LeaseRenewedAt.IsZero() || p.LeaseRenewedAt.After(p.VerifiedAt) || p.VerifiedAt.Sub(p.LeaseRenewedAt) > 40*time.Second {
		return errors.New("workspace stop proof does not verify the exact task, session, old Pod and fresh node lease")
	}
	return nil
}

// ReadWorkspaceStopProof reads the controller's bounded exec input. The
// immutable hold environment binds the task; it is never fresh stop proof.
func ReadWorkspaceStopProof(r io.Reader) (*WorkspaceStopProof, error) {
	const limit = 16 << 10
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil || len(data) > limit {
		return nil, errors.New("workspace stop proof input is unreadable or exceeds 16 KiB")
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	var proof WorkspaceStopProof
	if err := dec.Decode(&proof); err != nil {
		return nil, err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, errors.New("workspace stop proof input has trailing data")
	}
	return &proof, nil
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
	if len(data) > MaxSessionBytes {
		return Session{}, errors.New("session document exceeds the bounded transport")
	}
	return parseSession(data)
}

// ParseSessionFile extends only the explicitly bound common-input route.
func ParseSessionFile(data []byte) (Session, error) {
	if len(data) > MaxSessionFileBytes {
		return Session{}, errors.New("session file exceeds the bounded transport")
	}
	s, err := parseSession(data)
	if err != nil {
		return Session{}, err
	}
	if len(s.CommonRules) == 0 && len(data) > MaxSessionBytes {
		return Session{}, errors.New("legacy session file exceeds its transport")
	}
	return s, nil
}

func parseSession(data []byte) (Session, error) {
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
	// cursorModelID is the shape of a Cursor model id: letters, digits and
	// . _ : / -, starting with a letter or digit, so it can never read as a
	// CLI flag. It is a subset of the inbox's requested-model pattern, so a
	// cursor session's messages are not refused for their model. There is no
	// allowlist: the CLI refuses an id it does not know (DESIGN-058).
	cursorModelID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]*$`)
)

// MaxCursorModelBytes bounds a Cursor model id, as the spec bounds every model.
const MaxCursorModelBytes = 128

// Validate checks the fields agentd relies on.
func (s Session) Validate() error {
	var errs []error
	if s.CaptureOutput && (s.Mode != ModeTask || (s.Agent != AgentClaude && s.Agent != AgentCodex) || s.SessionUID == "" || len(s.ProjectSnapshot) == 0 || s.Workspace != nil) {
		errs = append(errs, errors.New("managed output requires an original private project task identity"))
	}
	if s.SessionUID != "" && (len(s.SessionUID) > 128 || !repoName.MatchString(s.SessionUID)) {
		errs = append(errs, errors.New("sessionUID must identify the existing session"))
	}
	if s.Workspace != nil {
		if len(s.Workspace.ID) == 0 || len(s.Workspace.ID) > 63 || !dnsLabel.MatchString(s.Workspace.ID) {
			errs = append(errs, errors.New("workspace.id must be a DNS label of at most 63 characters"))
		}
		if len(s.Workspace.SessionUID) == 0 || len(s.Workspace.SessionUID) > 128 || !repoName.MatchString(s.Workspace.SessionUID) {
			errs = append(errs, errors.New("workspace.sessionUID must identify the existing session"))
		}
		if proof := s.Workspace.StopProof; proof != nil {
			if err := proof.Validate(s.Workspace.ID, s.Name, s.Workspace.SessionUID, proof.PodUID); err != nil {
				errs = append(errs, err)
			}
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
	case AgentCodex:
		if err := ValidateCodexModel(s.Model); err != nil {
			errs = append(errs, err)
		}
		if err := ValidateCodexEffort(s.Model, s.Effort); err != nil {
			errs = append(errs, err)
		}
	case AgentCursor:
		if err := ValidateCursorModel(s.Model); err != nil {
			errs = append(errs, err)
		}
		if s.Effort != "" {
			errs = append(errs, errors.New(CursorEffortRefusal))
		}
		if s.Mode != ModeTask {
			errs = append(errs, errors.New(CursorModeRefusal))
		}
		if s.Workspace != nil {
			errs = append(errs, errors.New("a cursor task runs in its own private worktree, never a shared workspace"))
		}
	case AgentOpencode:
		if strings.TrimSpace(s.Model) == "" {
			errs = append(errs, errors.New("model is empty"))
		}
	default:
		errs = append(errs, fmt.Errorf("agent %q is not claude, codex, cursor or opencode", s.Agent))
	}
	switch s.Mode {
	case ModeTask:
		if strings.TrimSpace(s.Prompt) == "" {
			errs = append(errs, errors.New("task mode needs a prompt"))
		}
	case ModeLocal:
		if s.Prompt != "" {
			errs = append(errs, fmt.Errorf("a prompt is for task mode only, not %s", s.Mode))
		}
	case ModeRemote:
		// An ordinary remote session has no prompt. A summoned one carries
		// the platform-rendered brief (DESIGN-064 5.6).
		if s.Prompt != "" && s.Caller == "" {
			errs = append(errs, fmt.Errorf("a prompt is for task mode only, not %s", s.Mode))
		}
	default:
		errs = append(errs, fmt.Errorf("mode %q is not task, local or remote", s.Mode))
	}
	if s.FallbackModel != "" {
		if s.Caller == "" {
			errs = append(errs, errors.New("fallbackModel is for a summoned session only"))
		}
		if err := ValidateClaudeModel(s.FallbackModel); err != nil {
			errs = append(errs, fmt.Errorf("fallbackModel: %w", err))
		}
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

// ValidateCodexModel accepts the full pinned GPT ids, never client aliases.
func ValidateCodexModel(id string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("model is empty")
	}
	switch id {
	case "gpt-6-astra", "gpt-6.1-sol", "gpt-6.1-terra", "gpt-6-sol", "gpt-6-luna", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5":
		return nil
	default:
		return fmt.Errorf("model must be a full supported Codex model id")
	}
}

// CursorEffortRefusal and CursorModeRefusal are the one wording agentd, the
// API and agent-run give a cursor session that names an effort or another
// mode (DESIGN-058).
const (
	CursorEffortRefusal = "a cursor session takes no effort: Cursor encodes effort in the model id, so pick a model id that names it"
	CursorModeRefusal   = "a cursor session runs in task mode only (-p); --local and --interactive are not built for Cursor"
)

// ValidateCursorModel checks a Cursor model id for shape only (DESIGN-058).
// There is no allowlist: the Cursor CLI refuses an id it does not know, and a
// Grok or other vendor model is selected by its Cursor id.
func ValidateCursorModel(id string) error {
	if id == "" {
		return errors.New("model is empty: a cursor session needs a Cursor model id")
	}
	if len(id) > MaxCursorModelBytes || !cursorModelID.MatchString(id) {
		return fmt.Errorf("model %q is not a Cursor model id: at most %d letters, digits and . _ : / -, starting with a letter or digit", id, MaxCursorModelBytes)
	}
	return nil
}

// ValidateCodexEffort follows the pinned native model manifest. An empty
// effort preserves the model default; native aliases never receive a fallback.
func ValidateCodexEffort(model, effort string) error {
	if err := ValidateCodexModel(model); err != nil {
		return err
	}
	switch effort {
	case "", "low", "medium", "high", "xhigh":
		return nil
	case "max":
		if model != "gpt-5.5" {
			return nil
		}
	case "ultra":
		if model != "gpt-5.5" && model != "gpt-6-luna" && model != "gpt-5.6-luna" {
			return nil
		}
	}
	return fmt.Errorf("model %s does not support Codex reasoning effort %q", model, effort)
}
