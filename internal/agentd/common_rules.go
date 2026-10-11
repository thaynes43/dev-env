package agentd

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"syscall"
	"unicode/utf8"

	kjson "sigs.k8s.io/json"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/commonrules"
)

const commonOwnershipFile = "common-rule-ownership.json"
const commonProjectionFile = "common-launch-inputs.json"
const commonProjectionLimit = 2 << 20

// Metadata includes a bounded private path and original Kubernetes UID.
const commonOwnershipLimit = commonrules.MaxSnapshotBytes + (32 << 10)

type commonOwnership struct {
	Version          int             `json:"version"`
	State            string          `json:"state"`
	Session          string          `json:"session"`
	SessionUID       string          `json:"sessionUID"`
	Home             string          `json:"home"`
	Snapshot         json.RawMessage `json:"snapshot"`
	ProjectionState  string          `json:"projectionState"`
	ProjectionDigest string          `json:"projectionDigest"`
}

// CommonInstructionBinding distinguishes accepted source from the separately
// retained local instruction projection. Absence on legacy launches is Unknown.
type CommonInstructionBinding struct {
	CommonRevision  string `json:"commonRevision"`
	SnapshotDigest  string `json:"snapshotDigest"`
	EffectiveDigest string `json:"effectiveDigest"`
	Derivation      string `json:"derivation"`
}

type commonProjection struct {
	Version             int      `json:"version"`
	Session             string   `json:"session"`
	SessionUID          string   `json:"sessionUID"`
	Provider            string   `json:"provider"`
	SnapshotDigest      string   `json:"snapshotDigest"`
	ProjectDigest       string   `json:"projectDigest"`
	RenderedRulesDigest string   `json:"renderedRulesDigest"`
	ConfiguredDeveloper string   `json:"configuredDeveloper"`
	Fallbacks           []string `json:"fallbacks"`
	DocMaxBytes         int      `json:"docMaxBytes"`
	Guard               string   `json:"guard"`
}

func acceptedCommonTask(s Settings, sess protocol.Session) (commonrules.Snapshot, error) {
	if !s.RetainedCommonRules || len(sess.CommonRules) == 0 || sess.SessionUID == "" || len(sess.SessionUID) > 128 || len(s.Home) > 4096 || noSymlinkComponents(s.Home) != nil || sess.Mode != protocol.ModeTask || sess.Workspace != nil || s.WorkspaceID != "" || sess.Agent != protocol.AgentClaude && sess.Agent != protocol.AgentCodex || s.ClaudeConfigDir != filepath.Join(s.Home, ".claude") || s.CodexHome != filepath.Join(s.Home, ".codex") || privateManagedState(s) != nil {
		return commonrules.Snapshot{}, commonrules.ErrRefused
	}
	snapshot, err := commonrules.Parse(sess.CommonRules)
	if err != nil || snapshot.ProjectDigest != commonrules.Digest(sess.ProjectSnapshot) {
		return commonrules.Snapshot{}, commonrules.ErrRefused
	}
	canonical, _ := snapshot.Bytes()
	if !bytes.Equal(canonical, sess.CommonRules) {
		return commonrules.Snapshot{}, commonrules.ErrRefused
	}
	return snapshot, nil
}

func privateCommonDir(path string) error {
	if noSymlinkComponents(filepath.Dir(path)) != nil {
		return commonrules.ErrRefused
	}
	if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return commonrules.ErrRefused
	}
	st, err := os.Lstat(path)
	if err != nil || noSymlinkComponents(path) != nil || !st.IsDir() || st.Mode().Perm()&0077 != 0 || st.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
		return commonrules.ErrRefused
	}
	return nil
}

func readOwnedCommon(path string, bound int) ([]byte, error) {
	if noSymlinkComponents(filepath.Dir(path)) != nil {
		return nil, commonrules.ErrRefused
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	now, pathErr := os.Lstat(path)
	if err != nil || pathErr != nil || !os.SameFile(st, now) || !st.Mode().IsRegular() || st.Mode().Perm() != 0600 || st.Size() > int64(bound) || st.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) || st.Sys().(*syscall.Stat_t).Nlink != 1 {
		return nil, commonrules.ErrRefused
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(bound)+1))
	if err != nil || len(data) > bound {
		return nil, commonrules.ErrRefused
	}
	if f.Sync() != nil {
		return nil, commonrules.ErrRefused
	}
	parent, err := os.Open(filepath.Dir(path))
	if err != nil {
		return nil, commonrules.ErrRefused
	}
	defer func() { _ = parent.Close() }()
	if parent.Sync() != nil {
		return nil, commonrules.ErrRefused
	}
	return data, nil
}

func readCommonJSON(path string, bound int, out any) error {
	data, err := readOwnedCommon(path, bound)
	if err != nil {
		return err
	}
	strict, err := kjson.UnmarshalStrict(data, out)
	if err != nil || len(strict) != 0 {
		return commonrules.ErrRefused
	}
	return nil
}

func proveCommonGlobals(s Settings, snapshot commonrules.Snapshot) error {
	for path, want := range map[string]string{filepath.Join(s.ClaudeConfigDir, "CLAUDE.md"): snapshot.Common.Text, filepath.Join(s.CodexHome, "AGENTS.md"): snapshot.CodexGlobal()} {
		data, err := readOwnedCommon(path, commonrules.MaxCommonBytes+commonrules.MaxHeaderBytes+1)
		if err != nil || string(data) != want {
			return commonrules.ErrRefused
		}
	}
	return nil
}

func freshCommonOwnership(s Settings, sess protocol.Session, snapshot commonrules.Snapshot) commonOwnership {
	canonical, _ := snapshot.Bytes()
	return commonOwnership{Version: 2, State: "Confirmed", Session: sess.Name, SessionUID: sess.SessionUID, Home: s.Home, Snapshot: canonical, ProjectionState: "Fresh"}
}

// Fresh is explicit evidence that this receipt has never attempted projection
// creation. Older or interrupted receipts cannot acquire that evidence on retry.
func validateCommonOwnership(s Settings, sess protocol.Session, snapshot commonrules.Snapshot, owned commonOwnership) error {
	want := freshCommonOwnership(s, sess, snapshot)
	identity := owned
	identity.ProjectionState, identity.ProjectionDigest = want.ProjectionState, want.ProjectionDigest
	if !reflect.DeepEqual(identity, want) || proveCommonGlobals(s, snapshot) != nil {
		return commonrules.ErrRefused
	}
	switch owned.ProjectionState {
	case "Fresh":
		if owned.ProjectionDigest != "" {
			return commonrules.ErrRefused
		}
		for _, path := range []string{s.statePath(commonProjectionFile), s.statePath(launchFile)} {
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				return commonrules.ErrRefused
			}
		}
	case "Confirmed":
		if _, err := readCommonProjection(s, owned); err != nil {
			return err
		}
	default:
		return commonrules.ErrRefused
	}
	return nil
}

func readCommonProjection(s Settings, owned commonOwnership) (commonProjection, error) {
	var projection commonProjection
	if owned.ProjectionState != "Confirmed" || !commonrules.ValidDigest(owned.ProjectionDigest) || readCommonJSON(s.statePath(commonProjectionFile), commonProjectionLimit, &projection) != nil {
		return commonProjection{}, commonrules.ErrRefused
	}
	data, err := json.Marshal(projection)
	if err != nil || commonrules.Digest(data) != owned.ProjectionDigest {
		return commonProjection{}, commonrules.ErrRefused
	}
	return projection, nil
}

// Preparing precedes either global write. Partial publication is deliberately
// not repaired or adopted on retry; the original files and intent stay intact.
func prepareCommonTask(s Settings, sess protocol.Session) error {
	if len(sess.CommonRules) == 0 {
		if s.RetainedCommonRules {
			return commonrules.ErrRefused
		}
		return nil
	}
	snapshot, err := acceptedCommonTask(s, sess)
	if err != nil {
		return err
	}
	unlock, err := privateNativeLock(s, "common-rules.lock")
	if err != nil {
		return commonrules.ErrRefused
	}
	defer unlock()
	_, err = prepareCommonTaskLocked(s, sess, snapshot)
	return err
}

// The common-rules lock covers both globals and the projection state machine.
func prepareCommonTaskLocked(s Settings, sess protocol.Session, snapshot commonrules.Snapshot) (commonOwnership, error) {
	for _, dir := range []string{s.ClaudeConfigDir, s.CodexHome} {
		if privateCommonDir(dir) != nil {
			return commonOwnership{}, commonrules.ErrRefused
		}
	}
	want := freshCommonOwnership(s, sess, snapshot)
	path := s.statePath(commonOwnershipFile)
	var owned commonOwnership
	if err := readCommonJSON(path, commonOwnershipLimit, &owned); err == nil {
		if err := validateCommonOwnership(s, sess, snapshot, owned); err != nil {
			return commonOwnership{}, err
		}
		return owned, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return commonOwnership{}, commonrules.ErrRefused
	}
	if _, err := os.Lstat(s.statePath(launchFile)); !errors.Is(err, os.ErrNotExist) {
		return commonOwnership{}, commonrules.ErrRefused
	}
	for _, p := range []string{filepath.Join(s.ClaudeConfigDir, "CLAUDE.md"), filepath.Join(s.CodexHome, "AGENTS.md"), s.statePath(commonProjectionFile)} {
		if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
			return commonOwnership{}, commonrules.ErrRefused
		}
	}
	intent := want
	intent.State = "Preparing"
	data, _ := json.Marshal(intent)
	if projectSyncExclusiveFile(path, append(data, '\n')) != nil {
		return commonOwnership{}, commonrules.ErrRefused
	}
	var retained commonOwnership
	if readCommonJSON(path, commonOwnershipLimit, &retained) != nil || !reflect.DeepEqual(retained, intent) {
		return commonOwnership{}, commonrules.ErrRefused
	}
	for path, text := range map[string]string{filepath.Join(s.ClaudeConfigDir, "CLAUDE.md"): snapshot.Common.Text, filepath.Join(s.CodexHome, "AGENTS.md"): snapshot.CodexGlobal()} {
		if projectSyncExclusiveFile(path, []byte(text)) != nil {
			return commonOwnership{}, commonrules.ErrRefused
		}
	}
	if proveCommonGlobals(s, snapshot) != nil || writeWorkspaceJSON(s.statePath(commonOwnershipFile), want) != nil {
		return commonOwnership{}, commonrules.ErrRefused
	}
	if readCommonJSON(s.statePath(commonOwnershipFile), commonOwnershipLimit, &retained) != nil || !reflect.DeepEqual(retained, want) {
		return commonOwnership{}, commonrules.ErrRefused
	}
	return want, nil
}

type commonProjectionWrites struct {
	ownership  func(string, any) error
	projection func(string, []byte) error
}

// The original digest is durably acknowledged before projection creation.
// Preparing survives every uncertain boundary and is never repaired on retry.
func sealCommonProjection(s Settings, owned commonOwnership, projection commonProjection, writes commonProjectionWrites) error {
	data, err := json.Marshal(projection)
	if err != nil || len(data)+1 > commonProjectionLimit || owned.ProjectionState != "Fresh" || owned.ProjectionDigest != "" {
		return commonrules.ErrRefused
	}
	intent := owned
	intent.ProjectionState, intent.ProjectionDigest = "Preparing", commonrules.Digest(data)
	path := s.statePath(commonOwnershipFile)
	if writes.ownership(path, intent) != nil {
		return commonrules.ErrRefused
	}
	var observed commonOwnership
	if readCommonJSON(path, commonOwnershipLimit, &observed) != nil || !reflect.DeepEqual(observed, intent) {
		return commonrules.ErrRefused
	}
	if writes.projection(s.statePath(commonProjectionFile), append(data, '\n')) != nil {
		return commonrules.ErrRefused
	}
	var retained commonProjection
	if readCommonJSON(s.statePath(commonProjectionFile), commonProjectionLimit, &retained) != nil || !reflect.DeepEqual(retained, projection) {
		return commonrules.ErrRefused
	}
	confirmed := intent
	confirmed.ProjectionState = "Confirmed"
	if writes.ownership(path, confirmed) != nil || readCommonJSON(path, commonOwnershipLimit, &observed) != nil || !reflect.DeepEqual(observed, confirmed) {
		return commonrules.ErrRefused
	}
	return nil
}

// The fresh-home renderer would have seeded the mounted GitOps config. Keep
// only its effective instruction fields when no provider-owned config exists.
// Resume never reads either current configuration source.
func retainedCodexConfiguredInputs(s Settings) (string, []string, int, error) {
	path := filepath.Join(s.CodexHome, "config.toml")
	st, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return codexConfiguredInputsAt(filepath.Join(s.ConfigDir, "codex", "config.toml"))
	}
	if err != nil || noSymlinkComponents(path) != nil || !st.Mode().IsRegular() || st.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) || st.Sys().(*syscall.Stat_t).Nlink != 1 {
		return "", nil, 0, commonrules.ErrRefused
	}
	return codexConfiguredInputs(s)
}

func retainedCommonArgs(s Settings, sess protocol.Session, guard string, resume bool, snapshotRules string) ([]string, error) {
	accepted, err := acceptedCommonTask(s, sess)
	if err != nil {
		return nil, err
	}
	unlock, err := privateNativeLock(s, "common-rules.lock")
	if err != nil {
		return nil, commonrules.ErrRefused
	}
	defer unlock()
	owned, err := prepareCommonTaskLocked(s, sess, accepted)
	if err != nil {
		return nil, err
	}
	var projection commonProjection
	if owned.ProjectionState == "Fresh" {
		if resume {
			return nil, commonrules.ErrRefused
		}
		developer, fallbacks, docMax := "", []string(nil), 0
		if sess.Agent == protocol.AgentCodex {
			developer, fallbacks, docMax, err = retainedCodexConfiguredInputs(s)
			if docMax < 131072 {
				docMax = 131072
			}
			if err != nil {
				return nil, err
			}
		}
		projection = commonProjection{Version: 1, Session: sess.Name, SessionUID: sess.SessionUID, Provider: sess.Agent, SnapshotDigest: commonrules.Digest(sess.CommonRules), ProjectDigest: accepted.ProjectDigest, RenderedRulesDigest: commonrules.Digest([]byte(snapshotRules)), ConfiguredDeveloper: developer, Fallbacks: fallbacks, DocMaxBytes: docMax, Guard: guard}
		if sealCommonProjection(s, owned, projection, commonProjectionWrites{ownership: writeWorkspaceJSON, projection: projectSyncExclusiveFile}) != nil {
			return nil, commonrules.ErrRefused
		}
	} else {
		projection, err = readCommonProjection(s, owned)
		if err != nil {
			return nil, err
		}
	}
	if projection.Version != 1 || projection.Session != sess.Name || projection.SessionUID != sess.SessionUID || projection.Provider != sess.Agent || projection.SnapshotDigest != commonrules.Digest(sess.CommonRules) || projection.ProjectDigest != accepted.ProjectDigest || projection.RenderedRulesDigest != commonrules.Digest([]byte(snapshotRules)) || projection.Guard == "" || !utf8.ValidString(projection.ConfiguredDeveloper) || len(projection.Fallbacks) > 64 {
		return nil, commonrules.ErrRefused
	}
	project, err := acceptedProjectTask(s, sess)
	if err != nil {
		return nil, err
	}
	args, err := projectRuleInputs(sess.Agent, s.statePath("project-rules.md"), projection.ConfiguredDeveloper, projection.Guard, project, snapshotRules, projection.Fallbacks...)
	if err != nil {
		return nil, err
	}
	// Common + header may exceed Codex's default native discovery budget. This
	// explicit route keeps repository discovery enabled with the existing floor.
	if sess.Agent == protocol.AgentCodex {
		if projection.DocMaxBytes < 131072 {
			return nil, commonrules.ErrRefused
		}
		args = append(args, "-c", "project_doc_max_bytes="+strconv.Itoa(projection.DocMaxBytes))
	}
	for _, arg := range args {
		if len(arg) >= 128<<10 {
			return nil, commonrules.ErrRefused
		}
	}
	return args, nil
}

func commonLaunchBinding(s Settings, sess protocol.Session) (*CommonInstructionBinding, error) {
	if len(sess.CommonRules) == 0 {
		return nil, nil
	}
	snapshot, err := acceptedCommonTask(s, sess)
	if err != nil {
		return nil, err
	}
	unlock, err := privateNativeLock(s, "common-rules.lock")
	if err != nil {
		return nil, commonrules.ErrRefused
	}
	defer unlock()
	var owned commonOwnership
	if readCommonJSON(s.statePath(commonOwnershipFile), commonOwnershipLimit, &owned) != nil || owned.ProjectionState != "Confirmed" || validateCommonOwnership(s, sess, snapshot, owned) != nil {
		return nil, commonrules.ErrRefused
	}
	return &CommonInstructionBinding{CommonRevision: snapshot.CommonRevision, SnapshotDigest: commonrules.Digest(sess.CommonRules), EffectiveDigest: owned.ProjectionDigest, Derivation: "RetainedLocalInstructionProjection"}, nil
}

func validateCommonResume(s Settings, sess protocol.Session, first Launch) error {
	if len(sess.CommonRules) == 0 {
		if first.CommonInputs != nil {
			return commonrules.ErrRefused
		}
		return nil
	}
	current, err := commonLaunchBinding(s, sess)
	if err != nil || first.CommonInputs == nil || !reflect.DeepEqual(current, first.CommonInputs) {
		return commonrules.ErrRefused
	}
	return nil
}

// The explicit snapshot owns only global instruction files. In particular it
// never replaces provider config/MCP/auth/history while sealing local inputs.
func retainedCommonRender(s Settings) bool { return s.RetainedCommonRules }
