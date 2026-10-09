package projectsync

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/thaynes43/dev-env/internal/agentd"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
)

// Run starts one explicitly enabled GitOps Job operation. A durable Started
// record never authorizes replay. Only an exact confirmed Terminal record can
// return its historical public result without performing the operation again.
func Run(ctx context.Context, config Config, settings agentd.Settings, reader Reader, runner agentd.Runner) (Result, error) {
	a := actor{config: config, settings: settings, reader: reader, runner: runner, now: time.Now,
		checkMounts: agentd.CheckProjectSyncMounts, prepareStorage: agentd.PrepareProjectSyncStorage, prepareGit: agentd.PrepareProjectSyncGit, checkToken: checkKeeperToken, sync: agentd.SyncProjects}
	return a.run(ctx)
}

type actor struct {
	config         Config
	settings       agentd.Settings
	reader         Reader
	runner         agentd.Runner
	now            func() time.Time
	checkMounts    func(agentd.Settings) error
	prepareStorage func(agentd.Settings, bool) error
	prepareGit     func(agentd.Runner, agentd.Settings, string, string) (agentd.Runner, error)
	checkToken     func() error
	sync           func(context.Context, agentd.Runner, agentd.Settings, *projectcatalog.Catalog, agentd.ProjectSyncOptions) (agentd.ProjectSyncReport, error)
}

func (a actor) run(ctx context.Context) (Result, error) {
	var result Result
	c, s := a.config, a.settings
	if !c.Enabled {
		return result, errors.New("project synchronization is disabled")
	}
	if a.reader == nil || a.runner == nil || len(validation.IsDNS1123Label(c.Namespace)) != 0 || len(validation.IsDNS1123Label(c.CatalogNamespace)) != 0 || len(validation.IsDNS1123Subdomain(c.CatalogName)) != 0 || len(validation.IsDNS1123Subdomain(c.PodName)) != 0 || c.PodUID == "" || !projectcatalog.ValidName(c.WorkspaceID) || !cloneOwnerPattern.MatchString(c.CloneOwner) || strings.Contains(c.CloneOwner, "--") || s.WorkspaceID != c.WorkspaceID || s.PodUID != string(c.PodUID) || s.StateDir != filepath.Join(s.Home, ".agentd") {
		return result, errors.New("sync management configuration is incomplete or inconsistent")
	}
	ctx, cancel := context.WithTimeout(ctx, Budget)
	defer cancel()
	b, err := bind(ctx, a.reader, c, s, a.now())
	if err != nil {
		return result, err
	}
	if err := a.checkMounts(s); err != nil {
		return result, err
	}
	operationDir := filepath.Join(s.StateDir, "project-sync", string(b.jobUID))
	if filepath.Base(operationDir) != string(b.jobUID) || len(validation.IsDNS1123Label(string(b.jobUID))) != 0 {
		return result, errors.New("sync Job operation identity is invalid")
	}
	if err := privateDirectory(operationDir, false); err != nil {
		return result, err
	}
	path := filepath.Join(operationDir, "receipt.json")
	old, err := readReceipt(path)
	if err == nil {
		if old.Workspace != c.WorkspaceID || old.JobUID != string(b.jobUID) || old.PodUID != string(c.PodUID) || !old.Deadline.Equal(b.deadline) {
			return result, errors.New("sync operation receipt belongs to another binding")
		}
		if old.State != "Terminal" {
			return result, errors.New("sync operation already started; ambiguous work must not be replayed")
		}
		if err := b.confirm(ctx); err != nil {
			return result, err
		}
		return old, savedFailure(old)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return result, errors.New("sync operation receipt is unconfirmed; refusing replay")
	}
	if !a.now().Before(b.deadline) {
		return result, errors.New("sync Job original deadline has elapsed")
	}
	ctx, originalCancel := context.WithDeadline(ctx, b.deadline)
	defer originalCancel()
	read := func(ctx context.Context) (agentd.ProjectCatalogResource, error) {
		if err := b.confirm(ctx); err != nil {
			return agentd.ProjectCatalogResource{}, err
		}
		cm, err := a.reader.Catalog(ctx, c.CatalogNamespace, c.CatalogName)
		if err != nil || cm == nil || cm.Namespace != c.CatalogNamespace || cm.Name != c.CatalogName || cm.UID == "" || cm.ResourceVersion == "" || cm.DeletionTimestamp != nil {
			return agentd.ProjectCatalogResource{}, errors.New("accepted sync catalog identity is unconfirmed")
		}
		return agentd.ProjectCatalogResource{Namespace: cm.Namespace, Name: cm.Name, UID: string(cm.UID), ResourceVersion: cm.ResourceVersion, Data: []byte(cm.Data["catalog.json"])}, nil
	}
	accepted, err := read(ctx)
	if err != nil {
		return result, err
	}
	catalog, err := projectcatalog.Parse(accepted.Data)
	if err != nil {
		return result, errors.New("accepted sync catalog is invalid")
	}
	for _, repo := range catalog.Repositories() {
		owner, _, _ := strings.Cut(repo.GitHub, "/")
		if owner != c.CloneOwner {
			return result, errors.New("accepted repository does not match the configured canonical clone owner")
		}
	}
	if err := privateDirectory(operationDir, true); err != nil {
		return result, err
	}
	result = Result{Version: 1, Workspace: c.WorkspaceID, JobUID: string(b.jobUID), PodUID: string(c.PodUID), CatalogUID: accepted.UID, CatalogResourceVersion: accepted.ResourceVersion, CatalogRevision: catalog.Revision(), StartedAt: a.now().UTC(), Deadline: b.deadline.UTC(), State: "Started"}
	if err := writeReceipt(path, result, true); err != nil {
		return Result{}, err
	}
	if err := b.confirm(ctx); err != nil {
		return Result{}, err
	}
	if err := a.prepareStorage(s, c.Initialize); err != nil {
		return Result{}, err
	}
	if err := b.confirm(ctx); err != nil {
		return Result{}, err
	}
	if err := a.checkToken(); err != nil {
		return Result{}, err
	}
	runner, err := a.prepareGit(a.runner, s, operationDir, GitHubTokenFile)
	if err != nil {
		return Result{}, err
	}
	guarded := actorGitRunner{Runner: runner, confirm: b.confirm}
	confirmedRead := func(ctx context.Context) (agentd.ProjectCatalogResource, error) {
		current, err := read(ctx)
		if err != nil {
			return current, err
		}
		if current.UID != accepted.UID || current.ResourceVersion != accepted.ResourceVersion || !bytes.Equal(current.Data, accepted.Data) {
			return current, errors.New("accepted catalog changed during sync operation")
		}
		return current, nil
	}
	options := agentd.ProjectSyncOptions{Enabled: true, AcceptedCatalog: &agentd.ProjectCatalogSource{Namespace: c.CatalogNamespace, Name: c.CatalogName, Read: confirmedRead}, BeforeMutation: b.confirm}
	report, syncErr := a.sync(ctx, guarded, s, catalog, options)
	result.State, result.Report = "Terminal", report
	if syncErr != nil {
		result.Failure = "sync stopped with preserved partial work"
	}
	for _, finding := range report.Findings {
		if finding.State == "preserved" {
			result.Failure = "sync completed with preserved partial work"
		}
	}
	if err := b.confirm(ctx); err != nil {
		return Result{}, err
	}
	if err := writeReceipt(path, result, false); err != nil {
		return Result{}, err
	}
	return result, savedFailure(result)
}

func checkKeeperToken() error {
	fi, err := os.Stat(GitHubTokenFile)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() == 0 || fi.Size() > 64<<10 {
		return errors.New("keeper GitHub token projection is unavailable")
	}
	return nil
}

func savedFailure(result Result) error {
	if result.Failure != "" {
		return errors.New(result.Failure)
	}
	return nil
}

type actorGitRunner struct {
	agentd.Runner
	confirm func(context.Context) error
}

func (r actorGitRunner) Run(ctx context.Context, command agentd.Cmd) (agentd.Result, error) {
	if err := r.confirm(ctx); err != nil {
		return agentd.Result{}, err
	}
	return r.Runner.Run(ctx, command)
}
