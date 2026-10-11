package agentd

import (
	"bufio"
	"context"
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

const privateHostPreparationFile = "codex-host-preparation.json"

type codexHostPrivateBinding struct {
	Namespace       string `json:"namespace"`
	Name            string `json:"name"`
	UID             string `json:"uid"`
	ResourceVersion string `json:"resourceVersion"`
	CatalogRevision string `json:"catalogRevision"`
	GuardDigest     string `json:"guardDigest"`
}

func (b codexHostPrivateBinding) valid(s Settings) bool {
	return b.Namespace+"/"+b.Name == s.ProjectCatalog && b.UID != "" && b.ResourceVersion != "" && b.CatalogRevision != "" && b.GuardDigest != ""
}

type codexHostPreparation struct {
	Version                           int
	PodUID                            string
	Binding                           codexHostPrivateBinding
	Roots                             map[string]string
	ConfigDigest, ConfigReceiptDigest string
}

func privateHostPreparationMatches(saved codexHostLifecycle) bool {
	var input codexHostPreparation
	if decodeHostJSON(saved.Preparation, &input) != nil || input.Version != 1 || input.PodUID != saved.PodUID || saved.Private == nil || input.Binding != *saved.Private || len(input.Roots) > projectcatalog.MaxProjects || len(input.ConfigDigest) != 64 || len(input.ConfigReceiptDigest) != 64 {
		return false
	}
	for name, digest := range input.Roots {
		if !projectcatalog.ValidName(name) || len(digest) != 64 {
			return false
		}
	}
	return true
}

type codexHostPrivateInput struct {
	authority     *projectCatalogAuthority
	catalog       *projectcatalog.Catalog
	binding       codexHostPrivateBinding
	projectProofs map[string]string
	preparation   []byte
}

// This validates configuration only; it neither reads authority nor creates paths.
func ValidatePrivateCodexHost(s Settings) error {
	if !s.PrivateCodexHost {
		return nil
	}
	if !s.PrivateProjectBootstrap || !s.PrivateProjectRoots || ValidatePrivateProjectBootstrap(s) != nil || s.PodUID == "" || s.Home == "/" || s.StateDir != filepath.Join(s.Home, ".agentd") || s.CodexHome != filepath.Join(s.Home, ".codex") || s.SharedDir != "" && s.SharedDir != filepath.Join(s.Home, ".shared") {
		return errCodexHostUnknown
	}
	return nil
}

func privateCodexHostPreflight(s Settings) error {
	if ValidatePrivateCodexHost(s) != nil || s.CodexAccessFile == "" || s.APIURL == "" || noSymlinkComponents(s.Home) != nil || privateCodexHome(s) != nil {
		return errCodexHostUnknown
	}
	paths := []string{s.StateDir, s.CodexHome, s.ReposDir(), s.WorkDir(), filepath.Join(s.Home, "codex"), filepath.Join(s.CodexHome, "app-server-daemon"), filepath.Join(s.CodexHome, "app-server-control"), filepath.Join(s.CodexHome, "packages")}
	for _, path := range append([]string{s.Home}, paths...) {
		if err := noSymlinkComponents(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return errCodexHostUnknown
		}
		st, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !st.IsDir() || st.Mode().Perm()&0o200 == 0 || st.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
			return errCodexHostUnknown
		}
		if (path == s.StateDir || path == s.CodexHome) && st.Mode().Perm()&0o077 != 0 {
			return errCodexHostUnknown
		}
	}
	for _, path := range []string{s.CodexHome, filepath.Join(s.CodexHome, "app-server-daemon"), filepath.Join(s.CodexHome, "app-server-control")} {
		f, err := os.Open(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return errCodexHostUnknown
		}
		entries, err := f.ReadDir(257)
		_ = f.Close()
		if err != nil && !errors.Is(err, io.EOF) || len(entries) > 256 {
			return errCodexHostUnknown
		}
		for _, entry := range entries {
			st, err := entry.Info()
			if err != nil || st.Mode()&os.ModeSymlink != 0 || st.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) || st.Mode().IsRegular() && st.Sys().(*syscall.Stat_t).Nlink != 1 {
				return errCodexHostUnknown
			}
		}
	}
	data, err := readWorkspaceMountInfo()
	if err != nil {
		return errCodexHostUnknown
	}
	type mount struct {
		path, device, root string
		writable           bool
	}
	var mounts []mount
	seen := map[string]bool{}
	scan := bufio.NewScanner(strings.NewReader(string(data)))
	decode := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	var home mount
	for scan.Scan() {
		parts := strings.Split(scan.Text(), " - ")
		if len(parts) != 2 {
			return errCodexHostUnknown
		}
		left, right := strings.Fields(parts[0]), strings.Fields(parts[1])
		if len(left) < 6 || len(right) < 3 {
			return errCodexHostUnknown
		}
		m := mount{decode.Replace(left[4]), left[2], decode.Replace(left[3]), slices.Contains(strings.Split(left[5], ","), "rw") && slices.Contains(strings.Split(right[2], ","), "rw")}
		if seen[m.path] {
			return errCodexHostUnknown
		}
		seen[m.path] = true
		mounts = append(mounts, m)
		if m.path == s.Home {
			home = m
		}
	}
	if scan.Err() != nil || home.path == "" || !home.writable {
		return errCodexHostUnknown
	}
	for _, m := range mounts {
		if m.path == s.Home {
			continue
		}
		within := func(path, parent string) bool {
			return path == parent || parent == "/" || strings.HasPrefix(path, parent+"/")
		}
		// Same-device ancestor/subroot binds expose part or all of the private
		// HOME through another mount even when its literal root differs.
		if m.device == home.device && (within(m.root, home.root) || within(home.root, m.root)) {
			return errCodexHostUnknown
		}
		for _, path := range paths {
			if m.path == path || strings.HasPrefix(m.path, path+"/") || strings.HasPrefix(path, m.path+"/") && strings.HasPrefix(m.path, s.Home+"/") {
				return errCodexHostUnknown
			}
		}
	}
	return nil
}

func privateHostRunner(o CodexHostOptions) Runner {
	if o.Runner != nil {
		return o.Runner
	}
	return ExecRunner{}
}
func capturePrivateCodexHostInput(ctx context.Context, s Settings, o CodexHostOptions) (*codexHostPrivateInput, error) {
	if ValidatePrivateCodexHost(s) != nil || o.CatalogFile != "" || o.AcceptedCatalog == nil || o.AcceptedCatalog.Read == nil || o.AcceptedCatalog.Namespace+"/"+o.AcceptedCatalog.Name != s.ProjectCatalog {
		return nil, errCodexHostUnknown
	}
	reader := &projectCatalogAuthority{source: *o.AcceptedCatalog}
	accepted, err := reader.read(ctx)
	if err != nil {
		return nil, errCodexHostUnknown
	}
	catalog, err := projectcatalog.Parse(accepted.Data)
	if err != nil {
		return nil, errCodexHostUnknown
	}
	authority, err := captureProjectCatalogAuthority(ctx, o.AcceptedCatalog, catalog)
	if err != nil || authority.accepted.UID != accepted.UID || authority.accepted.ResourceVersion != accepted.ResourceVersion {
		return nil, errCodexHostUnknown
	}
	for _, repo := range catalog.Repositories() {
		owner, _, _ := strings.Cut(repo.GitHub, "/")
		if !strings.EqualFold(owner, s.ProjectCloneOwner) {
			return nil, errCodexHostUnknown
		}
	}
	guard, err := boundedHostProjection(o.InstructionsFile, 16<<10)
	if err != nil || !utf8.Valid(guard) || strings.TrimSpace(string(guard)) == "" {
		return nil, errCodexHostUnknown
	}
	for _, r := range string(guard) {
		if r < 0x20 && r != '\n' && r != '\t' || r == 0x7f {
			return nil, errCodexHostUnknown
		}
	}
	return &codexHostPrivateInput{authority: authority, catalog: catalog, binding: codexHostPrivateBinding{accepted.Namespace, accepted.Name, accepted.UID, accepted.ResourceVersion, catalog.Revision(), projectcatalog.Digest(guard)}}, nil
}

// Read-only consumption repeats the same root/anchor proof used by publication.
// Ready operation receipts are checked separately so an interrupted refresh cannot
// promote the previous ownership snapshot into current host readiness.
func provePrivateCodexHostRoots(ctx context.Context, s Settings, o CodexHostOptions, input *codexHostPrivateInput) error {
	ctx, cancel := context.WithTimeout(ctx, projectSyncBudget)
	defer cancel()
	var report PrivateProjectReport
	if readWorkspaceJSON(s.statePath(privateProjectBootstrapFile), &report) != nil || report.Version != 1 || report.State != "Ready" || report.FinishedAt.IsZero() || report.CatalogNamespace != input.binding.Namespace || report.CatalogName != input.binding.Name || report.CatalogUID != input.binding.UID || report.CatalogResourceVersion != input.binding.ResourceVersion || report.CatalogRevision != input.binding.CatalogRevision || len(report.Projects) != len(input.catalog.ProjectNames()) {
		return errCodexHostUnknown
	}
	s.privateProjectGit = true
	proofs := map[string]string{}
	for i, project := range input.catalog.ProjectNames() {
		snapshot, err := input.catalog.ProjectSnapshot(project)
		if err != nil {
			return errCodexHostUnknown
		}
		parent := filepath.Join(s.StateDir, "project-roots")
		path := filepath.Join(parent, project+".json")
		var owned privateRootReceipt
		var operation PrivateProjectRootReport
		if readWorkspaceJSONBounded(path, &owned, privateRootReceiptLimit) != nil || readWorkspaceJSON(filepath.Join(parent, project+".operation.json"), &operation) != nil {
			return errCodexHostUnknown
		}
		retained, err := projectcatalog.ParseSnapshot(owned.Snapshot)
		observed := report.Projects[i]
		if err != nil || privateSnapshotDigest(retained) != privateSnapshotDigest(snapshot) || operation.Project != project || operation.State != "Ready" || operation.Head != owned.Head || operation.CatalogRevision != snapshot.CatalogRevision() || operation.RulesRevision != snapshot.RulesRevision() || observed.Project != project || observed.State != "Ready" || observed.Head != operation.Head || observed.CatalogRevision != operation.CatalogRevision || observed.RulesRevision != operation.RulesRevision || !observed.ObservedAt.Equal(operation.ObservedAt) || provePrivateRoot(ctx, privateHostRunner(o), s, owned, owned.Repositories, owned.Anchors) != nil {
			return errCodexHostUnknown
		}
		// Include immutable source pins and exact worktree identities, not merely
		// wrapper content. An explicit independent refresh requires absent reprepare.
		bytes, err := json.Marshal(struct {
			Ownership privateRootReceipt
			Outcome   PrivateProjectRootReport
		}{owned, operation})
		if err != nil {
			return errCodexHostUnknown
		}
		proofs[project] = projectcatalog.Digest(bytes)
	}
	if input.projectProofs != nil && !equalPrivateProjectProofs(input.projectProofs, proofs) {
		return errCodexHostUnknown
	}
	input.projectProofs = proofs
	if input.authority.confirm(ctx) != nil {
		return errCodexHostUnknown
	}
	return nil
}
func privateSnapshotDigest(snapshot projectcatalog.Snapshot) string {
	bytes, _ := json.Marshal(snapshot)
	return projectcatalog.Digest(bytes)
}
func equalPrivateProjectProofs(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// Only fields owned by host composition are checked. Native additions to unrelated
// MCP/config/history do not change this proof; managed field edits require review.
func privateHostOwnedConfig(cfg map[string]any, prior codexHostConfigReceipt) bool {
	if cfg["model"] != "gpt-6-astra" || cfg["model_reasoning_effort"] != "max" || cfg["model_provider"] != "openai" || cfg["forced_login_method"] != "chatgpt" || cfg["cli_auth_credentials_store"] != "file" || cfg["chatgpt_base_url"] != "https://chatgpt.com/backend-api/" || cfg["developer_instructions"] != prior.AppliedDeveloper {
		return false
	}
	features, ok := cfg["features"].(map[string]any)
	if !ok || features["default_mode_request_user_input"] != true {
		return false
	}
	fallbacks, ok := cfg["project_doc_fallback_filenames"].([]any)
	if !ok || !slices.Contains(fallbacks, any("CLAUDE.md")) {
		return false
	}
	projects, ok := cfg["projects"].(map[string]any)
	if !ok {
		return false
	}
	for _, root := range prior.TrustRoots {
		table, ok := projects[root].(map[string]any)
		if !ok || table["trust_level"] != "trusted" {
			return false
		}
	}
	return true
}

func preparePrivateCodexHost(ctx context.Context, s Settings, o CodexHostOptions) (*codexHostPrivateInput, error) {
	ctx, cancel := context.WithTimeout(ctx, projectSyncBudget)
	defer cancel()
	if o.privateAbsent == nil || o.privateAbsent() != nil {
		return nil, errCodexHostUnknown
	}
	input, err := capturePrivateCodexHostInput(ctx, s, o)
	if err != nil {
		return nil, errCodexHostUnknown
	}
	config, err := boundedHostFile(filepath.Join(s.CodexHome, "config.toml"), 256<<10, true)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, errCodexHostUnknown
	}
	var prior codexHostConfigReceipt
	priorBytes, err := boundedHostFile(s.statePath("codex-host-config.json"), 256<<10, true)
	if err == nil {
		cfg := map[string]any{}
		if decodeHostJSON(priorBytes, &prior) != nil || prior.Version != 1 || prior.Private == nil || toml.Unmarshal(config, &cfg) != nil || !privateHostOwnedConfig(cfg, prior) {
			return nil, errCodexHostUnknown
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, errCodexHostUnknown
	}
	rootInputs := map[string]string{}
	for _, name := range input.catalog.ProjectNames() {
		snapshot, _ := input.catalog.ProjectSnapshot(name)
		bytes, _ := json.Marshal(snapshot)
		rootInputs[name] = projectcatalog.Digest(bytes)
	}
	input.preparation, err = json.Marshal(codexHostPreparation{1, s.PodUID, input.binding, rootInputs, projectcatalog.Digest(config), projectcatalog.Digest(priorBytes)})
	if err != nil || o.privateAbsent() != nil || ensureWorkspaceDirectory(s.StateDir) != nil {
		return nil, errCodexHostUnknown
	}
	// An exclusive reservation survives a short write, failed fsync or failed
	// lifecycle publication. No later invocation may guess that it is safe to rearm.
	f, err := os.OpenFile(s.statePath(privateHostPreparationFile), os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, errCodexHostUnknown
	}
	_, writeErr := f.Write(input.preparation)
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil || syncHostStateDirectory(s) != nil {
		return nil, errCodexHostUnknown
	}
	retained, err := boundedHostFile(s.statePath(privateHostPreparationFile), 256<<10, true)
	if err != nil || string(retained) != string(input.preparation) {
		return nil, errCodexHostUnknown
	}
	lifecycle := codexHostLifecycle{Version: 1, Phase: "Preparing", PodUID: s.PodUID, CLI: managedCodexVersion, Private: &input.binding, Preparation: input.preparation}
	bytes, err := json.Marshal(lifecycle)
	if err != nil || writeCodexPrivateAtomic(s.statePath("codex-host-start-intent.json"), bytes) != nil {
		return nil, errCodexHostUnknown
	}
	if saved, err := hostLifecycle(s); err != nil || saved.Phase != "Preparing" || *saved.Private != input.binding {
		return nil, errCodexHostUnknown
	}
	if input.authority.confirm(ctx) != nil {
		return nil, errCodexHostUnknown
	}
	// Every authority confirmation gates the following bootstrap mutation on a
	// fresh absence observation, including after an API read that could stall.
	source := *o.AcceptedCatalog
	read := source.Read
	source.Read = func(ctx context.Context) (ProjectCatalogResource, error) {
		if o.privateAbsent() != nil {
			return ProjectCatalogResource{}, errCodexHostUnknown
		}
		resource, err := read(ctx)
		if err != nil || o.privateAbsent() != nil {
			return ProjectCatalogResource{}, errCodexHostUnknown
		}
		return resource, nil
	}
	if o.privateAbsent() != nil {
		return nil, errCodexHostUnknown
	}
	report, err := RefreshPrivateProjects(ctx, privateHostRunner(o), s, &source)
	if err != nil || report.State != "Ready" || provePrivateCodexHostRoots(ctx, s, o, input) != nil {
		return nil, errCodexHostUnknown
	}
	return input, nil
}
func syncHostStateDirectory(s Settings) error {
	f, err := os.Open(s.StateDir)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return f.Sync()
}

// Preparation binds original bytes through both config-publication boundaries.
// A later ordinary provider update is allowed only after confirmed preparation.
func privateHostConfigInputsMatch(s Settings, input *codexHostPrivateInput, publishedReceipt []byte) bool {
	var planned codexHostPreparation
	if input == nil || decodeHostJSON(input.preparation, &planned) != nil {
		return false
	}
	read := func(path string) ([]byte, bool) {
		data, err := boundedHostFile(path, 256<<10, true)
		return data, err == nil || errors.Is(err, os.ErrNotExist)
	}
	config, ok := read(filepath.Join(s.CodexHome, "config.toml"))
	if !ok || projectcatalog.Digest(config) != planned.ConfigDigest {
		return false
	}
	receipt, ok := read(s.statePath("codex-host-config.json"))
	if !ok {
		return false
	}
	if publishedReceipt != nil {
		return string(receipt) == string(publishedReceipt)
	}
	return projectcatalog.Digest(receipt) == planned.ConfigReceiptDigest
}

func finishPrivateCodexHostPreparation(s Settings, input *codexHostPrivateInput) error {
	retained, err := boundedHostFile(s.statePath(privateHostPreparationFile), 256<<10, true)
	if err != nil || input == nil || string(retained) != string(input.preparation) {
		return errCodexHostUnknown
	}
	saved, err := hostLifecycle(s)
	if err != nil || saved.Phase != "Confirming" || saved.PodUID != s.PodUID || saved.Private == nil || *saved.Private != input.binding {
		return errCodexHostUnknown
	}
	if os.Remove(s.statePath(privateHostPreparationFile)) != nil || syncHostStateDirectory(s) != nil {
		return errCodexHostUnknown
	}
	return nil
}
