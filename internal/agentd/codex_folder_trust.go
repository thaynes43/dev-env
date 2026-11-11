package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// errFolderNotQualified means the path is not a repository this session's
// bootstrap record names as Ready. errCodexConfigInvalid means the trust
// write did not happen. errCodexTrustUnconfirmed means a write was sent and
// not rejected, so trust may now be recorded.
var (
	errFolderNotQualified    = errors.New("folder not qualified")
	errCodexConfigInvalid    = errors.New("codex config invalid")
	errCodexTrustUnconfirmed = errors.New("codex trust unconfirmed")
)

// codexTrustVersionRetries is how many times a configVersionConflict is tried
// again. The attempt that still conflicts after that refuses.
const codexTrustVersionRetries = 3

// codexBootstrapBound is the largest boot record this check will read. A
// larger file is unreadable, and an unreadable record does not qualify.
const codexBootstrapBound = 1 << 20

// ensureCodexRepoTrust records the platform clone as trusted through the host
// daemon's config/value/write, before a client starts. write is that call;
// nil does not dial. A folder that does not qualify returns before write runs.
func ensureCodexRepoTrust(ctx context.Context, s Settings, pid int, cwd string, write func(context.Context, Settings, int, string) error) error {
	if err := qualifyCodexRepo(ctx, s, cwd); err != nil {
		return err
	}
	// A private host's composer rejects a trusted root outside ~/codex/<project>,
	// and that check runs on every status read. Writing ~/repos/<repo> would
	// make the host unreadable. Stop before any config call.
	if s.PrivateCodexHost {
		return errCodexConfigInvalid
	}
	if write == nil {
		return errCodexConfigInvalid
	}
	return write(ctx, s, pid, cwd)
}

// qualifyCodexRepo accepts only the resolved path of one repository this
// session's own bootstrap record names as Ready: ~/repos/<repo>, with no
// symlink and with that directory as the Git toplevel. The record is
// ~/.agentd/boot.json projectBootstrap, the value CollectStatus reports.
// A handoff path, a parent, a subdirectory, a symlink, a name the record
// does not list as Ready, and a record that cannot be read all fail here,
// before config is read.
func qualifyCodexRepo(ctx context.Context, s Settings, cwd string) error {
	if !filepath.IsAbs(cwd) || filepath.Clean(cwd) != cwd {
		return errFolderNotQualified
	}
	name := filepath.Base(cwd)
	if !apiv1.ValidRepoName(name) || s.ClonePath(name) != cwd {
		return errFolderNotQualified
	}
	rel, err := filepath.Rel(s.ReposDir(), cwd)
	if err != nil || rel != name {
		return errFolderNotQualified
	}
	// The key path quotes this directory. A character Go would escape is not
	// a path this writer can record as that directory.
	if strconv.Quote(cwd) != `"`+cwd+`"` {
		return errFolderNotQualified
	}
	info, err := os.Stat(cwd)
	if err != nil || !info.IsDir() || noSymlinkComponents(cwd) != nil {
		return errFolderNotQualified
	}
	resolved, err := filepath.EvalSymlinks(cwd)
	if err != nil || filepath.Clean(resolved) != cwd {
		return errFolderNotQualified
	}
	if err := proveCodexRepoRoot(ctx, cwd); err != nil {
		return err
	}
	return codexBootstrapReady(s, name)
}

// proveCodexRepoRoot asks Git, with this directory's own .git and no parent
// discovery, whether cwd is the work tree root. A file .git, a symlink .git
// and a subdirectory of a repository all fail.
func proveCodexRepoRoot(ctx context.Context, cwd string) error {
	gitDir := filepath.Join(cwd, ".git")
	info, err := os.Lstat(gitDir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || noSymlinkComponents(gitDir) != nil {
		return errFolderNotQualified
	}
	git, err := exec.LookPath("git")
	if err != nil {
		return errFolderNotQualified
	}
	cmdCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, git, "--git-dir", gitDir, "--work-tree", cwd, "rev-parse", "--show-toplevel")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "HOME=" + os.Getenv("HOME")}
	out, err := cmd.Output()
	if err != nil {
		return errFolderNotQualified
	}
	top := filepath.Clean(strings.TrimSpace(string(out)))
	if top != cwd {
		return errFolderNotQualified
	}
	return nil
}

// codexBootstrapReady reports whether repo is Ready in the boot record.
// Ready is the repository state that means the platform cloned it and the
// refresh left it usable. Preserved, any other state, a missing entry, and
// an unreadable record do not qualify. The catalog's own state is not the
// test: a Partial catalog can still hold a Ready repository.
func codexBootstrapReady(s Settings, repo string) error {
	path := s.statePath(bootFile)
	if noSymlinkComponents(filepath.Dir(path)) != nil {
		return errFolderNotQualified
	}
	data, err := boundedHostFile(path, codexBootstrapBound, false)
	if err != nil {
		return errFolderNotQualified
	}
	var boot bootRecord
	if json.Unmarshal(data, &boot) != nil || boot.ProjectBootstrap == nil {
		return errFolderNotQualified
	}
	for _, item := range boot.ProjectBootstrap.Repositories {
		if item.Name == repo && item.State == protocol.BootstrapReady {
			return nil
		}
	}
	return errFolderNotQualified
}

// writeCodexProjectTrust asks the host daemon to set
// projects."<folder>".trust_level to trusted. It reads the user-layer version
// first and sends it as expectedVersion. A version conflict is retried at
// most codexTrustVersionRetries times.
func writeCodexProjectTrust(ctx context.Context, s Settings, pid int, folder string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	conn, err := openCodexThreadObserver(ctx, s, pid)
	if err != nil {
		return errCodexConfigInvalid
	}
	defer func() { _ = conn.conn.Close() }()
	state, err := readCodexUserTrust(conn, folder)
	if err != nil {
		return err
	}
	if state.trusted {
		return nil
	}
	if state.hasUser && state.version == "" {
		return errCodexConfigInvalid
	}
	version := state.version
	for attempt := 0; attempt <= codexTrustVersionRetries; attempt++ {
		params := map[string]any{
			"keyPath":       codexTrustKeyPath(folder),
			"value":         "trusted",
			"mergeStrategy": "replace",
		}
		if version != "" {
			params["expectedVersion"] = version
		}
		raw, fault, sent, callErr := conn.callOutcome("config/value/write", params)
		if callErr != nil || !sent {
			if sent {
				return errCodexTrustUnconfirmed
			}
			return errCodexConfigInvalid
		}
		if fault != nil {
			code := fault.configWriteCode()
			if code == "configVersionConflict" && attempt < codexTrustVersionRetries {
				state, err = readCodexUserTrust(conn, folder)
				if err != nil {
					return errCodexConfigInvalid
				}
				if state.trusted {
					return nil
				}
				if !state.hasUser || state.version == "" {
					return errCodexConfigInvalid
				}
				version = state.version
				continue
			}
			if codexTrustWriteRejected(code) {
				return errCodexConfigInvalid
			}
			return errCodexTrustUnconfirmed
		}
		if codexTrustWriteOK(raw) {
			return nil
		}
		return errCodexTrustUnconfirmed
	}
	return errCodexConfigInvalid
}

// codexTrustKeyPath is the config/value/write key for one absolute folder.
// A quoted segment is one path key, so the directory is not split on dots.
func codexTrustKeyPath(folder string) string {
	return "projects." + strconv.Quote(folder) + ".trust_level"
}

type codexUserTrust struct {
	trusted bool
	hasUser bool
	version string
}

// readCodexUserTrust reads config/read with layers. The user layer's version
// is what expectedVersion must match. A missing layers list, a version that
// is not the 0.160.1 sha256 form, or more than one user layer refuses before
// a write. No user layer is a first write: version stays empty and the call
// omits expectedVersion, because there is no version to send.
func readCodexUserTrust(r *codexHostRPC, folder string) (codexUserTrust, error) {
	raw, fault, sent, err := r.callOutcome("config/read", map[string]any{"includeLayers": true})
	if err != nil || fault != nil || !sent {
		return codexUserTrust{}, errCodexConfigInvalid
	}
	var response struct {
		Config  json.RawMessage `json:"config"`
		Origins json.RawMessage `json:"origins"`
		Layers  *[]struct {
			Name struct {
				Type string `json:"type"`
			} `json:"name"`
			Version string `json:"version"`
		} `json:"layers"`
	}
	if json.Unmarshal(raw, &response) != nil || len(response.Config) == 0 || response.Config[0] != '{' || len(response.Origins) == 0 || response.Origins[0] != '{' || response.Layers == nil {
		return codexUserTrust{}, errCodexConfigInvalid
	}
	var config struct {
		Projects map[string]struct {
			TrustLevel string `json:"trust_level"`
		} `json:"projects"`
	}
	if json.Unmarshal(response.Config, &config) != nil {
		return codexUserTrust{}, errCodexConfigInvalid
	}
	out := codexUserTrust{}
	if project, ok := config.Projects[folder]; ok && project.TrustLevel == "trusted" {
		out.trusted = true
	}
	for _, layer := range *response.Layers {
		if layer.Name.Type != "user" {
			continue
		}
		if out.hasUser {
			return codexUserTrust{}, errCodexConfigInvalid
		}
		out.hasUser = true
		out.version = layer.Version
	}
	if out.hasUser && !codexConfigVersion(out.version) {
		return codexUserTrust{}, errCodexConfigInvalid
	}
	return out, nil
}

// codexConfigVersion is the user-layer version 0.160.1 puts on a config
// layer: sha256: and 64 lowercase hex digits.
func codexConfigVersion(v string) bool {
	rest, ok := strings.CutPrefix(v, "sha256:")
	if !ok || len(rest) != 64 {
		return false
	}
	for _, c := range rest {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// codexTrustWriteRejected is a config/value/write error the 0.160.1 daemon
// returns before it applies the edit. configVersionConflict is one of them.
func codexTrustWriteRejected(code string) bool {
	switch code {
	case "configVersionConflict", "configLayerReadonly", "configRequirementReadonly", "configValidationError", "configPathNotFound", "configSchemaUnknownKey":
		return true
	default:
		return false
	}
}

// codexTrustWriteOK is a ConfigWriteResponse whose status is ok. Any other
// result, including okOverridden, is not confirmation that this folder is
// trusted at the value we wrote.
func codexTrustWriteOK(raw json.RawMessage) bool {
	var response struct {
		FilePath string `json:"filePath"`
		Status   string `json:"status"`
		Version  string `json:"version"`
	}
	if json.Unmarshal(raw, &response) != nil {
		return false
	}
	return response.Status == "ok" && codexConfigVersion(response.Version) && filepath.IsAbs(response.FilePath)
}
