package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/thaynes43/dev-env/internal/codexauth"
)

func privateHostFixture(t *testing.T) (gitFixture, Settings, CodexHostOptions, *projectFixtureRunner) {
	t.Helper()
	g, s, r, source := privateRootsFixture(t, projectTestCatalog)
	s.PrivateCodexHost, s.CodexHome, s.CodexBin = true, filepath.Join(s.Home, ".codex"), "fake-codex"
	s.APIURL, s.CodexAccessFile = "https://fixture.invalid", filepath.Join(s.Home, "projection", "access.json")
	s.PodUID = "fixture-pod"
	if os.MkdirAll(s.CodexHome, 0o700) != nil {
		t.Fatal("provider home")
	}
	now := time.Now().UTC().Truncate(time.Second)
	access, err := codexauth.Encode(syntheticAgentCodexAccess(t, now, 1), now)
	if err != nil {
		t.Fatal(err)
	}
	writeHostFixture(t, s.CodexAccessFile, string(access))
	o := CodexHostOptions{Enabled: true, InstructionsFile: filepath.Join(s.Home, "guard.txt"), AcceptedCatalog: source, Runner: r}
	writeHostFixture(t, o.InstructionsFile, "ROOT_REVIEWED_GUARD")
	old := readWorkspaceMountInfo
	readWorkspaceMountInfo = func() ([]byte, error) {
		return []byte("1 0 8:1 /private-home " + s.Home + " rw - ext4 private rw"), nil
	}
	t.Cleanup(func() { readWorkspaceMountInfo = old })
	return g, s, o, r
}
func privateHostStartDeps(t *testing.T, s Settings, o CodexHostOptions) (codexHostDeps, *int) {
	t.Helper()
	d, _ := hostDeps(t, s, o)
	d.preflight = codexHostPreflight
	live := false
	starts := 0
	d.process = func(Settings) (codexHostProcess, error) {
		if !live {
			return codexHostProcess{State: "Absent"}, nil
		}
		return codexHostProcess{State: "Live", PID: 123, Identity: codexHostIdentity{BootID: "fixture", StartTicks: 45}}, nil
	}
	d.native = func(_ context.Context, _ Settings, _ string, args []string, _ io.Writer) ([]byte, error) {
		if slices.Equal(args, []string{"--version"}) {
			return []byte("codex-cli " + managedCodexVersion), nil
		}
		if !slices.Equal(args, []string{"remote-control", "start", "--json"}) {
			t.Fatalf("unexpected native call %v", args)
		}
		starts++
		live = true
		path := filepath.Join(s.CodexHome, "packages", "app-server-daemon", "current", "bin", "codex")
		writeHostFixture(t, path, "fixture executable")
		if os.Chmod(path, 0o700) != nil {
			t.Fatal("executable")
		}
		writeHostFixture(t, filepath.Join(s.CodexHome, "app-server-daemon", "settings.json"), `{"remoteControlEnabled":true,"updater":{"autoUpdateEnabled":false}}`)
		return nil, nil
	}
	return d, &starts
}
func TestPrivateCodexHostStartsWithTrustedRootsWithoutSharedState(t *testing.T) {
	_, s, o, r := privateHostFixture(t)
	writeHostFixture(t, filepath.Join(s.CodexHome, "config.toml"), "developer_instructions='ORIGINAL_RULES'\n[mcp_servers.original]\ncommand='retain'\n")
	writeHostFixture(t, filepath.Join(s.CodexHome, "history.sqlite"), "HISTORY_CANARY")
	d, starts := privateHostStartDeps(t, s, o)
	if codexHostPreflight(s) != nil || startCodexHost(context.Background(), s, o, d) != nil {
		t.Fatal("private start refused")
	}
	if *starts != 1 || r.cloneCalls != 1 || exists(s.workspaceDir()) || exists(filepath.Join(s.SharedDir, "locks")) {
		t.Fatal("private startup used shared state")
	}
	if exists(s.statePath(privateHostPreparationFile)) {
		t.Fatal("confirmed preparation remained pending")
	}
	lifecycle, err := hostLifecycle(s)
	if err != nil || lifecycle.Phase != "Confirmed" || lifecycle.Private == nil || lifecycle.Private.UID != "catalog-1" || len(lifecycle.Preparation) == 0 {
		t.Fatal("private causal binding missing")
	}
	var receipt codexHostConfigReceipt
	if decodeHostJSON(mustRead(t, s.statePath("codex-host-config.json")), &receipt) != nil || receipt.Private == nil || len(receipt.ProjectProofs) != 1 || receipt.ProjectProofs["sample"] == "" {
		t.Fatal("root provenance missing")
	}
	before := mustRead(t, filepath.Join(s.CodexHome, "config.toml"))
	var cfg map[string]any
	if toml.Unmarshal(before, &cfg) != nil || cfg["developer_instructions"] != "ORIGINAL_RULES\n\nROOT_REVIEWED_GUARD" {
		t.Fatal("config composition")
	}
	cfg["mcp_servers"].(map[string]any)["later"] = map[string]any{"command": "provider-added"}
	cfg["future_setting"] = "ordinary provider addition"
	changed, _ := toml.Marshal(cfg)
	writeHostFixture(t, filepath.Join(s.CodexHome, "config.toml"), string(changed))
	status, err := observeCodexHost(context.Background(), s, o, d)
	if err != nil || status.Code != "Observed" {
		t.Fatal("ordinary provider addition refused")
	}
	if !bytes.Equal(changed, mustRead(t, filepath.Join(s.CodexHome, "config.toml"))) || string(mustRead(t, filepath.Join(s.CodexHome, "history.sqlite"))) != "HISTORY_CANARY" {
		t.Fatal("passive config/history changed")
	}
	if startCodexHost(context.Background(), s, o, d) == nil || *starts != 1 {
		t.Fatal("live host restarted")
	}
}
func TestPrivateCodexHostPreflightRefusesSharedAndRedirectedPaths(t *testing.T) {
	_, s, _, _ := privateHostFixture(t)
	if codexHostPreflight(s) != nil {
		t.Fatal("private preflight")
	}
	for _, mode := range []string{"bootstrap disabled", "roots disabled", "workspace", "shared setting", "foreign codex home", "provider symlink", "nested mount", "home readonly", "home alias", "provider subroot alias", "home ancestor alias"} {
		t.Run(mode, func(t *testing.T) {
			bad := s
			old := readWorkspaceMountInfo
			defer func() { readWorkspaceMountInfo = old }()
			switch mode {
			case "bootstrap disabled":
				bad.PrivateProjectBootstrap = false
			case "roots disabled":
				bad.PrivateProjectRoots = false
			case "workspace":
				bad.WorkspaceID = "shared"
			case "shared setting":
				bad.SharedDir = "/work/shared"
			case "foreign codex home":
				bad.CodexHome = filepath.Join(s.Home, "codex", "provider")
			case "provider symlink":
				p := filepath.Join(s.CodexHome, "history.sqlite")
				if os.Symlink(filepath.Join(s.Home, "foreign-history"), p) != nil {
					t.Fatal("symlink")
				}
				defer func() {
					if err := os.Remove(p); err != nil {
						t.Fatal(err)
					}
				}()
			case "nested mount":
				readWorkspaceMountInfo = func() ([]byte, error) {
					return []byte("1 0 8:1 /private " + s.Home + " rw - ext4 private rw\n2 1 0:42 /shared " + s.ReposDir() + " rw - ceph shared rw"), nil
				}
			case "home readonly":
				readWorkspaceMountInfo = func() ([]byte, error) { return []byte("1 0 8:1 /private " + s.Home + " ro - ext4 private rw"), nil }
			case "provider subroot alias":
				readWorkspaceMountInfo = func() ([]byte, error) {
					return []byte("1 0 8:1 /private " + s.Home + " rw - ext4 private rw\n2 0 8:1 /private/.codex /outside/provider rw - ext4 private rw"), nil
				}
			case "home ancestor alias":
				readWorkspaceMountInfo = func() ([]byte, error) {
					return []byte("1 0 8:1 /private/home " + s.Home + " rw - ext4 private rw\n2 0 8:1 /private /outside/ancestor rw - ext4 private rw"), nil
				}
			case "home alias":
				readWorkspaceMountInfo = func() ([]byte, error) {
					return []byte("1 0 8:1 /private " + s.Home + " rw - ext4 private rw\n2 0 8:1 /private /work rw - ext4 private rw"), nil
				}
			}
			if codexHostPreflight(bad) == nil {
				t.Fatal("unsafe private path accepted")
			}
		})
	}
}
func TestPrivateCodexHostUnconfirmedOrNonAbsentNeverPrepares(t *testing.T) {
	for _, mode := range []string{"Live", "Unknown", "failed probe", "Preparing", "Attempting", "retained reservation", "absence changed"} {
		t.Run(mode, func(t *testing.T) {
			_, s, o, r := privateHostFixture(t)
			reads := 0
			read := o.AcceptedCatalog.Read
			o.AcceptedCatalog.Read = func(ctx context.Context) (ProjectCatalogResource, error) { reads++; return read(ctx) }
			d, _ := hostDeps(t, s, o)
			native := d.native
			d.native = func(ctx context.Context, s Settings, b string, args []string, w io.Writer) ([]byte, error) {
				if args[0] != "--version" {
					t.Fatal("native lifecycle invoked")
				}
				return native(ctx, s, b, args, w)
			}
			probes := 0
			d.process = func(Settings) (codexHostProcess, error) {
				probes++
				switch mode {
				case "Live":
					return codexHostProcess{State: "Live"}, nil
				case "Unknown":
					return codexHostProcess{State: "Unknown"}, nil
				case "failed probe":
					return codexHostProcess{}, errors.New("unknown")
				case "absence changed":
					if probes > 1 {
						return codexHostProcess{State: "Live"}, nil
					}
				}
				return codexHostProcess{State: "Absent"}, nil
			}
			if mode == "Preparing" || mode == "Attempting" {
				binding := codexHostPrivateBinding{"dev-env-system", "dev-env-project-catalog", "catalog-1", "1", "digest", "guard"}
				data, _ := json.Marshal(codexHostLifecycle{Version: 1, Phase: mode, PodUID: s.PodUID, CLI: managedCodexVersion, Private: &binding})
				writeHostFixture(t, s.statePath("codex-host-start-intent.json"), string(data))
			}
			if mode == "retained reservation" {
				writeHostFixture(t, s.statePath(privateHostPreparationFile), "unconfirmed partial")
			}
			if startCodexHost(context.Background(), s, o, d) == nil || reads != 0 || r.cloneCalls != 0 || exists(s.statePath(privateProjectBootstrapFile)) || exists(filepath.Join(s.CodexHome, "config.toml")) {
				t.Fatal("nonabsence/unconfirmed intent prepared")
			}
		})
	}
}
func TestPrivateCodexHostFailedPreparationRetainsIntentAndDoesNotRearm(t *testing.T) {
	_, s, o, r := privateHostFixture(t)
	r.failFetch = true
	d, _ := hostDeps(t, s, o)
	d.process = func(Settings) (codexHostProcess, error) { return codexHostProcess{State: "Absent"}, nil }
	if startCodexHost(context.Background(), s, o, d) == nil {
		t.Fatal("failed refresh started")
	}
	pending := mustRead(t, s.statePath(privateHostPreparationFile))
	lifecycle, err := hostLifecycle(s)
	if err != nil || lifecycle.Phase != "Preparing" || lifecycle.Private == nil || !bytes.Contains(pending, []byte("ConfigReceiptDigest")) {
		t.Fatal("preparation input/intent not retained")
	}
	reads := 0
	o.AcceptedCatalog.Read = func(context.Context) (ProjectCatalogResource, error) {
		reads++
		return ProjectCatalogResource{}, errors.New("must not read")
	}
	if startCodexHost(context.Background(), s, o, d) == nil || reads != 0 || !bytes.Equal(pending, mustRead(t, s.statePath(privateHostPreparationFile))) || exists(filepath.Join(s.CodexHome, "config.toml")) {
		t.Fatal("unconfirmed preparation retried")
	}
}
func TestPrivateCodexHostReadOnlyProofRefusesStaleOrPartialOwnership(t *testing.T) {
	for _, mode := range []string{"catalog RV", "catalog rules", "root rules", "operation Started", "global Started", "anchor alias", "foreign provider", "owned model", "foreign trusted root"} {
		t.Run(mode, func(t *testing.T) {
			g, s, o, _ := privateHostFixture(t)
			d, starts := privateHostStartDeps(t, s, o)
			if startCodexHost(context.Background(), s, o, d) != nil {
				t.Fatal("start fixture")
			}
			config := mustRead(t, filepath.Join(s.CodexHome, "config.toml"))
			root := privateRootPath(s)
			switch mode {
			case "catalog RV", "catalog rules":
				read := o.AcceptedCatalog.Read
				o.AcceptedCatalog.Read = func(ctx context.Context) (ProjectCatalogResource, error) {
					resource, err := read(ctx)
					if mode == "catalog RV" {
						resource.ResourceVersion = "2"
					} else {
						resource.Data = []byte(strings.Replace(string(resource.Data), "Exact rules.", "Changed rules.", 1))
					}
					return resource, err
				}
			case "root rules":
				writeFile(t, filepath.Join(root, "AGENTS.md"), "owner WIP\n")
			case "operation Started":
				var operation PrivateProjectRootReport
				path := filepath.Join(s.StateDir, "project-roots", "sample.operation.json")
				if readWorkspaceJSON(path, &operation) != nil {
					t.Fatal("outcome")
				}
				operation.State = "Started"
				if writeWorkspaceJSON(path, operation) != nil {
					t.Fatal("outcome")
				}
			case "global Started":
				var report PrivateProjectReport
				path := s.statePath(privateProjectBootstrapFile)
				if readWorkspaceJSON(path, &report) != nil {
					t.Fatal("report")
				}
				report.State = "Started"
				if writeWorkspaceJSON(path, report) != nil {
					t.Fatal("report")
				}
			case "anchor alias":
				peer := filepath.Join(s.Home, "peer")
				gitRun(t, g.env, s.ClonePath("demo"), "worktree", "add", "--detach", peer)
				gitDir := gitRun(t, g.env, peer, "rev-parse", "--absolute-git-dir")
				writeFile(t, filepath.Join(root, "demo", ".git"), "gitdir: "+gitDir+"\n")
			case "foreign provider":
				var cfg map[string]any
				if toml.Unmarshal(config, &cfg) != nil {
					t.Fatal("provider config fixture")
				}
				cfg["model_provider"] = "foreign"
				changed, _ := toml.Marshal(cfg)
				writeHostFixture(t, filepath.Join(s.CodexHome, "config.toml"), string(changed))
			case "foreign trusted root":
				var cfg map[string]any
				if toml.Unmarshal(config, &cfg) != nil {
					t.Fatal("trust config fixture")
				}
				cfg["projects"].(map[string]any)["/work"] = map[string]any{"trust_level": "trusted"}
				changed, _ := toml.Marshal(cfg)
				writeHostFixture(t, filepath.Join(s.CodexHome, "config.toml"), string(changed))
			case "owned model":
				writeHostFixture(t, filepath.Join(s.CodexHome, "config.toml"), strings.Replace(string(config), "gpt-6-astra", "owner-changed-model", 1))
			}
			before := mustRead(t, filepath.Join(s.CodexHome, "config.toml"))
			index := mustRead(t, filepath.Join(root, ".git", "index"))
			head := gitRun(t, g.env, root, "rev-parse", "HEAD")
			status, err := observeCodexHost(context.Background(), s, o, d)
			if err == nil || status.Code != "Unknown" || *starts != 1 {
				t.Fatal("stale/foreign state observed ready")
			}
			if !bytes.Equal(before, mustRead(t, filepath.Join(s.CodexHome, "config.toml"))) || !bytes.Equal(index, mustRead(t, filepath.Join(root, ".git", "index"))) || gitRun(t, g.env, root, "rev-parse", "HEAD") != head {
				t.Fatal("passive refusal changed state")
			}
		})
	}
}

func TestPrivateCodexHostConfirmationCleanupFailureCannotRearm(t *testing.T) {
	_, s, o, _ := privateHostFixture(t)
	o.privateAbsent = func() error { return nil }
	input, err := preparePrivateCodexHost(context.Background(), s, o)
	if err != nil {
		t.Fatal("preparation fixture")
	}
	live := codexHostProcess{State: "Live", PID: 123, Identity: codexHostIdentity{BootID: "fixture", StartTicks: 45}}
	if saveHostLifecycle(s, "Confirming", live) != nil {
		t.Fatal("confirmation fixture")
	}
	writeHostFixture(t, s.statePath(privateHostPreparationFile), "uncertain reservation")
	if finishPrivateCodexHostPreparation(s, input) == nil {
		t.Fatal("cleanup mismatch accepted")
	}
	saved, err := hostLifecycle(s)
	if err != nil || saved.Phase != "Confirming" || saved.Private == nil || *saved.Private != input.binding || !sameHostProcess(saved.Process, live) {
		t.Fatal("cleanup lost original causal binding")
	}
	d, _ := hostDeps(t, s, o)
	d.process = func(Settings) (codexHostProcess, error) { return codexHostProcess{State: "Absent"}, nil }
	reads := 0
	o.AcceptedCatalog.Read = func(context.Context) (ProjectCatalogResource, error) {
		reads++
		return ProjectCatalogResource{}, errors.New("must not read")
	}
	if startCodexHost(context.Background(), s, o, d) == nil || reads != 0 {
		t.Fatal("cleanup failure rearmed")
	}
}

func TestPrivateCodexHostAbsenceLostDuringAuthorityReadPreservesInputs(t *testing.T) {
	_, s, o, r := privateHostFixture(t)
	d, _ := hostDeps(t, s, o)
	absent := true
	d.process = func(Settings) (codexHostProcess, error) {
		if absent {
			return codexHostProcess{State: "Absent"}, nil
		}
		return codexHostProcess{State: "Live"}, nil
	}
	read := o.AcceptedCatalog.Read
	o.AcceptedCatalog.Read = func(ctx context.Context) (ProjectCatalogResource, error) {
		resource, err := read(ctx)
		absent = false
		return resource, err
	}
	if startCodexHost(context.Background(), s, o, d) == nil || r.cloneCalls != 0 || exists(s.statePath(privateHostPreparationFile)) || exists(s.statePath(privateProjectBootstrapFile)) || exists(filepath.Join(s.CodexHome, "config.toml")) {
		t.Fatal("authority read lost absence but still prepared")
	}
}

func TestPrivateCodexHostRequiresNamedReadableAuthority(t *testing.T) {
	_, s, o, r := privateHostFixture(t)
	for _, mode := range []string{"missing", "no reader", "wrong name", "caller file"} {
		t.Run(mode, func(t *testing.T) {
			bad := o
			source := *o.AcceptedCatalog
			bad.AcceptedCatalog = &source
			switch mode {
			case "missing":
				bad.AcceptedCatalog = nil
			case "no reader":
				source.Read = nil
			case "wrong name":
				source.Name = "caller"
			case "caller file":
				bad.CatalogFile = "/caller/catalog.json"
			}
			if _, err := capturePrivateCodexHostInput(context.Background(), s, bad); err == nil || r.cloneCalls != 0 || exists(s.statePath(privateHostPreparationFile)) {
				t.Fatal("untrusted catalog input accepted")
			}
		})
	}
}

func TestPrivateCodexHostMountAliasComponentBoundaries(t *testing.T) {
	_, s, _, _ := privateHostFixture(t)
	readWorkspaceMountInfo = func() ([]byte, error) {
		return []byte("1 0 8:1 /private " + s.Home + " rw - ext4 private rw\n2 0 8:1 /private-sibling /outside/sibling rw - ext4 private rw"), nil
	}
	if codexHostPreflight(s) != nil {
		t.Fatal("nonoverlapping sibling treated as alias")
	}
}

func TestPrivateCodexHostAbsenceLostAfterReceiptPreservesConfiguration(t *testing.T) {
	_, s, o, _ := privateHostFixture(t)
	original := "developer_instructions='ORIGINAL'\n[mcp_servers.original]\ncommand='retain'\n"
	path := filepath.Join(s.CodexHome, "config.toml")
	writeHostFixture(t, path, original)
	d, calls := hostDeps(t, s, o)
	d.process = func(Settings) (codexHostProcess, error) {
		if exists(s.statePath("codex-host-config.json")) {
			return codexHostProcess{State: "Live"}, nil
		}
		return codexHostProcess{State: "Absent"}, nil
	}
	if startCodexHost(context.Background(), s, o, d) == nil || !exists(s.statePath("codex-host-config.json")) || string(mustRead(t, path)) != original {
		t.Fatal("receipt I/O carried stale absence into config write")
	}
	for _, call := range *calls {
		if call != "--version" {
			t.Fatal("native start after lost absence")
		}
	}
	saved, err := hostLifecycle(s)
	if err != nil || saved.Phase != "Preparing" || !exists(s.statePath(privateHostPreparationFile)) {
		t.Fatal("lost absence discarded original intent")
	}
}

func TestPrivateCodexHostRefusesPreparationConfigInputDrift(t *testing.T) {
	for _, mode := range []string{"config during bootstrap", "receipt during bootstrap", "config after receipt"} {
		t.Run(mode, func(t *testing.T) {
			_, s, o, _ := privateHostFixture(t)
			path := filepath.Join(s.CodexHome, "config.toml")
			original := "developer_instructions='ORIGINAL'\n[mcp_servers.original]\ncommand='retain'\n"
			changed := original + "\n[mcp_servers.concurrent]\ncommand='owner-update'\n"
			writeHostFixture(t, path, original)
			d, calls := hostDeps(t, s, o)
			mutated := false
			d.process = func(Settings) (codexHostProcess, error) {
				if mode == "config after receipt" && !mutated && exists(s.statePath("codex-host-config.json")) {
					writeHostFixture(t, path, changed)
					mutated = true
				}
				return codexHostProcess{State: "Absent"}, nil
			}
			read := o.AcceptedCatalog.Read
			o.AcceptedCatalog.Read = func(ctx context.Context) (ProjectCatalogResource, error) {
				if mode != "config after receipt" && !mutated && exists(s.statePath(privateHostPreparationFile)) {
					if mode == "config during bootstrap" {
						writeHostFixture(t, path, changed)
					} else {
						writeHostFixture(t, s.statePath("codex-host-config.json"), "owner concurrent receipt")
					}
					mutated = true
				}
				return read(ctx)
			}
			if startCodexHost(context.Background(), s, o, d) == nil || !mutated {
				t.Fatal("preparation input drift accepted")
			}
			want := changed
			if mode == "receipt during bootstrap" {
				want = original
				if string(mustRead(t, s.statePath("codex-host-config.json"))) != "owner concurrent receipt" {
					t.Fatal("concurrent receipt overwritten")
				}
			}
			if string(mustRead(t, path)) != want {
				t.Fatal("concurrent provider config overwritten")
			}
			for _, call := range *calls {
				if call != "--version" {
					t.Fatal("native start after input drift")
				}
			}
			saved, err := hostLifecycle(s)
			if err != nil || saved.Phase != "Preparing" || !exists(s.statePath(privateHostPreparationFile)) {
				t.Fatal("input drift lost original intent")
			}
		})
	}
}
