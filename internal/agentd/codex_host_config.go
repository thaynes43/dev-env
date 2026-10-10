package agentd

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/pelletier/go-toml/v2"

	"github.com/thaynes43/dev-env/internal/projectcatalog"
)

type codexHostConfigReceipt struct {
	Version           int      `json:"version"`
	CatalogDigest     string   `json:"catalogDigest"`
	GuardDigest       string   `json:"guardDigest"`
	OriginalDeveloper string   `json:"originalDeveloper"`
	AppliedDeveloper  string   `json:"appliedDeveloper"`
	TrustRoots        []string `json:"trustRoots"`
	OwnedTrustRoots   []string `json:"ownedTrustRoots"`
}

func codexHostPreflight(s Settings) error {
	if s.Home == "" || s.WorkspaceID == "" || s.PodUID == "" || s.CodexAccessFile == "" || s.APIURL == "" {
		return errCodexHostUnknown
	}
	if workspaceStoragePreflight(s) != nil || privateCodexHome(s) != nil {
		return errCodexHostUnknown
	}
	for _, private := range []string{s.StateDir, s.CodexHome, filepath.Join(s.CodexHome, "app-server-daemon"), filepath.Join(s.CodexHome, "app-server-control"), filepath.Join(s.CodexHome, "packages")} {
		if !strings.HasPrefix(private, s.Home+string(filepath.Separator)) {
			return errCodexHostUnknown
		}
		for _, shared := range []string{s.workspaceDir(), s.WorkDir(), s.ReposDir(), filepath.Join(s.Home, "codex")} {
			if private == shared || strings.HasPrefix(private, shared+string(filepath.Separator)) {
				return errCodexHostUnknown
			}
		}
		if err := noSymlinkComponents(private); err != nil && !errors.Is(err, os.ErrNotExist) {
			return errCodexHostUnknown
		}
		if st, err := os.Lstat(private); err == nil && (!st.IsDir() || ((private == s.StateDir || private == s.CodexHome) && st.Mode().Perm()&0o077 != 0)) {
			return errCodexHostUnknown
		}
	}
	mounts, err := readWorkspaceMountInfo()
	if err != nil {
		return errCodexHostUnknown
	}
	ro := map[string]string{}
	homeWritable := false
	scan := bufio.NewScanner(strings.NewReader(string(mounts)))
	for scan.Scan() {
		parts := strings.Split(scan.Text(), " - ")
		if len(parts) != 2 {
			return errCodexHostUnknown
		}
		left := strings.Fields(parts[0])
		if len(left) < 6 {
			return errCodexHostUnknown
		}
		if left[4] == s.Home && slices.Contains(strings.Split(left[5], ","), "rw") {
			homeWritable = true
		}
		if slices.Contains(strings.Split(left[5], ","), "ro") {
			ro[left[4]] = left[2] + "/" + left[3]
		}
	}
	if scan.Err() != nil || !homeWritable {
		return errCodexHostUnknown
	}
	for _, path := range []string{s.workspaceDir(), s.ReposDir(), filepath.Join(s.Home, "codex"), s.WorkDir(), "/work"} {
		if ro[path] == "" {
			return errCodexHostUnknown
		}
	}
	if ro["/work"] != ro[s.WorkDir()] {
		return errCodexHostUnknown
	}
	return nil
}

func codexHostSettings(s Settings, prepare bool) (uint32, error) {
	path := filepath.Join(s.CodexHome, "app-server-daemon", "settings.json")
	data, err := boundedHostFile(path, 64<<10, true)
	settings := map[string]any{}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, errCodexHostUnknown
	}
	if err == nil && json.Unmarshal(data, &settings) != nil {
		return 0, errCodexHostUnknown
	}
	if settings == nil {
		return 0, errCodexHostUnknown
	}
	grace := uint32(60)
	if value, ok := settings["shutdownGraceSeconds"]; ok {
		n, ok := value.(float64)
		if !ok || n < 0 || n > 300 || n != float64(uint32(n)) {
			return 0, errCodexHostUnknown
		}
		grace = uint32(n)
	}
	if value, ok := settings["remoteControlEnabled"]; ok {
		if _, ok := value.(bool); !ok {
			return 0, errCodexHostUnknown
		}
	}
	if value, ok := settings["featureOverrides"]; ok {
		features, ok := value.(map[string]any)
		if !ok {
			return 0, errCodexHostUnknown
		}
		for _, value := range features {
			if _, ok := value.(bool); !ok {
				return 0, errCodexHostUnknown
			}
		}
	}
	updater := map[string]any{}
	if value, ok := settings["updater"]; ok {
		updater, ok = value.(map[string]any)
		if !ok {
			return 0, errCodexHostUnknown
		}
	}
	if value, ok := updater["updateIntervalMinutes"]; ok {
		n, ok := value.(float64)
		if !ok || n <= 0 || n > 1<<32-1 || n != float64(uint32(n)) {
			return 0, errCodexHostUnknown
		}
	}
	if value, ok := updater["autoUpdateEnabled"]; ok {
		if _, ok := value.(bool); !ok {
			return 0, errCodexHostUnknown
		}
	}
	if !prepare {
		if updater["autoUpdateEnabled"] != false || settings["remoteControlEnabled"] != true {
			return 0, errCodexHostUnknown
		}
		return grace, nil
	}
	updater["autoUpdateEnabled"] = false
	settings["updater"] = updater
	out, err := json.Marshal(settings)
	if err != nil {
		return 0, errCodexHostUnknown
	}
	if writeCodexPrivateAtomic(path, out) != nil {
		return 0, errCodexHostUnknown
	}
	return grace, nil
}

// Config is composed only while absence is proven. Public catalog files are
// host trust input, never API task-admission authority. Private provenance owns
// only generated entries; all unrelated settings/MCP/history remain untouched.
func codexHostConfig(s Settings, o CodexHostOptions, prepare bool) error {
	catalogBytes, err := boundedHostProjection(o.CatalogFile, projectcatalog.MaxBytes)
	if err != nil {
		return errCodexHostUnknown
	}
	catalog, err := projectcatalog.Parse(catalogBytes)
	if err != nil {
		return errCodexHostUnknown
	}
	guard, err := boundedHostProjection(o.InstructionsFile, 16<<10)
	if err != nil || !utf8.Valid(guard) || strings.TrimSpace(string(guard)) == "" {
		return errCodexHostUnknown
	}
	for _, r := range string(guard) {
		if r < 0x20 && r != '\n' && r != '\t' || r == 0x7f {
			return errCodexHostUnknown
		}
	}
	cfg := map[string]any{}
	path := filepath.Join(s.CodexHome, "config.toml")
	data, err := boundedHostFile(path, 256<<10, true)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return errCodexHostUnknown
	}
	if err == nil && toml.Unmarshal(data, &cfg) != nil {
		return errCodexHostUnknown
	}
	// Existing active profile/foreign provider or replacement instructions need an
	// explicit reviewed migration. Never silently override their meaning.
	for _, key := range []string{"profile", "model_instructions_file", "openai_base_url"} {
		if value, ok := cfg[key]; ok && value != "" {
			return errCodexHostUnknown
		}
	}
	if provider, ok := cfg["model_provider"]; ok && provider != "openai" {
		return errCodexHostUnknown
	}
	if backend, ok := cfg["chatgpt_base_url"]; ok && backend != "https://chatgpt.com/backend-api/" {
		return errCodexHostUnknown
	}
	if mode, ok := cfg["forced_login_method"]; ok && mode != "chatgpt" {
		return errCodexHostUnknown
	}
	if store, ok := cfg["cli_auth_credentials_store"]; ok && store != "file" {
		return errCodexHostUnknown
	}
	if sqlite, ok := cfg["sqlite_home"]; ok && sqlite != s.CodexHome {
		return errCodexHostUnknown
	}
	if providers, ok := cfg["model_providers"]; ok {
		m, ok := providers.(map[string]any)
		if !ok {
			return errCodexHostUnknown
		}
		if openai, ok := m["openai"]; ok {
			m, ok := openai.(map[string]any)
			if !ok {
				return errCodexHostUnknown
			}
			for _, key := range []string{"base_url", "env_key", "experimental_bearer_token", "wire_api"} {
				if _, ok := m[key]; ok {
					return errCodexHostUnknown
				}
			}
		}
	}
	developer := ""
	if value, ok := cfg["developer_instructions"]; ok {
		developer, ok = value.(string)
		if !ok {
			return errCodexHostUnknown
		}
	}
	fallbacks := []string{}
	if value, ok := cfg["project_doc_fallback_filenames"]; ok {
		items, ok := value.([]any)
		if !ok {
			return errCodexHostUnknown
		}
		for _, item := range items {
			text, ok := item.(string)
			if !ok || text == "" || len(text) > 255 {
				return errCodexHostUnknown
			}
			fallbacks = append(fallbacks, text)
		}
	}
	projects := map[string]any{}
	if value, ok := cfg["projects"]; ok {
		projects, ok = value.(map[string]any)
		if !ok {
			return errCodexHostUnknown
		}
	}
	var prior codexHostConfigReceipt
	receipt := s.statePath("codex-host-config.json")
	if data, err := boundedHostFile(receipt, 256<<10, true); err == nil {
		if decodeHostJSON(data, &prior) != nil || prior.Version != 1 || prior.AppliedDeveloper != developer {
			return errCodexHostUnknown
		}
		developer = prior.OriginalDeveloper
	} else if !errors.Is(err, os.ErrNotExist) {
		return errCodexHostUnknown
	}
	next := codexHostConfigReceipt{Version: 1, CatalogDigest: projectcatalog.Digest(catalogBytes), GuardDigest: projectcatalog.Digest(guard), OriginalDeveloper: developer}
	next.AppliedDeveloper = strings.TrimSpace(developer + "\n\n" + string(guard))
	for _, project := range catalog.ProjectNames() {
		next.TrustRoots = append(next.TrustRoots, filepath.Join(s.Home, "codex", project))
	}
	if !prepare {
		if cfg["model_provider"] != "openai" || cfg["forced_login_method"] != "chatgpt" || cfg["cli_auth_credentials_store"] != "file" || cfg["chatgpt_base_url"] != "https://chatgpt.com/backend-api/" {
			return errCodexHostUnknown
		}
		if prior.CatalogDigest != next.CatalogDigest || prior.GuardDigest != next.GuardDigest || !slices.Equal(prior.TrustRoots, next.TrustRoots) || cfg["model"] != "gpt-6-astra" || cfg["model_reasoning_effort"] != "max" || cfg["developer_instructions"] != next.AppliedDeveloper || !slices.Contains(fallbacks, "CLAUDE.md") {
			return errCodexHostUnknown
		}
		features, ok := cfg["features"].(map[string]any)
		if !ok || features["default_mode_request_user_input"] != true {
			return errCodexHostUnknown
		}
		for _, root := range next.TrustRoots {
			table, ok := projects[root].(map[string]any)
			if !ok || table["trust_level"] != "trusted" {
				return errCodexHostUnknown
			}
		}
		return nil
	}
	for _, root := range prior.OwnedTrustRoots {
		if slices.Contains(next.TrustRoots, root) {
			continue
		}
		table, ok := projects[root].(map[string]any)
		if !ok || table["trust_level"] != "trusted" {
			return errCodexHostUnknown
		}
		delete(table, "trust_level")
		if len(table) == 0 {
			delete(projects, root)
		}
	}
	for _, root := range next.TrustRoots {
		table := map[string]any{}
		if value, ok := projects[root]; ok {
			table, ok = value.(map[string]any)
			if !ok {
				return errCodexHostUnknown
			}
			if level, ok := table["trust_level"]; ok && level != "trusted" {
				return errCodexHostUnknown
			}
		}
		if _, exists := table["trust_level"]; !exists || slices.Contains(prior.OwnedTrustRoots, root) {
			next.OwnedTrustRoots = append(next.OwnedTrustRoots, root)
		}
		table["trust_level"] = "trusted"
		projects[root] = table
	}
	features := map[string]any{}
	if value, ok := cfg["features"]; ok {
		features, ok = value.(map[string]any)
		if !ok {
			return errCodexHostUnknown
		}
	}
	features["default_mode_request_user_input"] = true
	cfg["features"] = features
	if !slices.Contains(fallbacks, "CLAUDE.md") {
		fallbacks = append(fallbacks, "CLAUDE.md")
	}
	cfg["model"], cfg["model_reasoning_effort"], cfg["model_provider"] = "gpt-6-astra", "max", "openai"
	cfg["openai_base_url"], cfg["chatgpt_base_url"], cfg["forced_login_method"], cfg["cli_auth_credentials_store"] = "", "https://chatgpt.com/backend-api/", "chatgpt", "file"
	cfg["developer_instructions"], cfg["project_doc_fallback_filenames"], cfg["projects"] = next.AppliedDeveloper, fallbacks, projects
	out, err := toml.Marshal(cfg)
	if err != nil || len(out) > 256<<10 {
		return errCodexHostUnknown
	}
	saved, err := json.Marshal(next)
	if err != nil || len(saved) > 256<<10 {
		return errCodexHostUnknown
	}
	// Intent precedes config replacement. A crash/mismatch refuses future start;
	// it does not guess or regenerate an unknown earlier developer instruction.
	if writeCodexPrivateAtomic(receipt, saved) != nil || writeCodexPrivateAtomic(path, out) != nil {
		return errCodexHostUnknown
	}
	return nil
}

// Explicit GitOps projection inputs permit kubelet AtomicWriter symlinks. Their
// opened targets must be bounded regular files; caller-controlled native/private
// paths continue to use NOFOLLOW. These files supply no task admission authority.
func boundedHostProjection(path string, bound int) ([]byte, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errCodexHostUnknown
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errCodexHostUnknown
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > int64(bound) {
		return nil, errCodexHostUnknown
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(bound)+1))
	if err != nil || len(data) > bound {
		return nil, errCodexHostUnknown
	}
	return data, nil
}
