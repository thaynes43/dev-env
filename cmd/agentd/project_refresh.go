package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/thaynes43/dev-env/internal/agentd"
	"github.com/thaynes43/dev-env/internal/projectsync"
)

func privateProjectCatalogSource(s agentd.Settings, getenv func(string) string) (*agentd.ProjectCatalogSource, error) {
	if err := agentd.ValidatePrivateProjectBootstrap(s); err != nil {
		return nil, err
	}
	if !s.PrivateProjectBootstrap {
		return nil, nil
	}
	namespace, name, _ := strings.Cut(s.ProjectCatalog, "/")
	var once sync.Once
	var reader projectsync.Reader
	var readerErr error
	// Construct lazily so an unavailable identity becomes a retained failed
	// bootstrap step, while the selected task's own preparation still runs.
	return &agentd.ProjectCatalogSource{Namespace: namespace, Name: name, Read: func(ctx context.Context) (agentd.ProjectCatalogResource, error) {
		once.Do(func() {
			reader, readerErr = projectsync.NewPrivateCatalogReader(getenv("KUBERNETES_SERVICE_HOST"), getenv("KUBERNETES_SERVICE_PORT"))
		})
		if readerErr != nil {
			return agentd.ProjectCatalogResource{}, errors.New("private catalog Kubernetes identity is unavailable")
		}
		cm, err := reader.Catalog(ctx, namespace, name)
		if err != nil || cm == nil || cm.Namespace != namespace || cm.Name != name || cm.UID == "" || cm.ResourceVersion == "" || cm.DeletionTimestamp != nil {
			return agentd.ProjectCatalogResource{}, errors.New("accepted private catalog API identity is unconfirmed")
		}
		return agentd.ProjectCatalogResource{Namespace: cm.Namespace, Name: cm.Name, UID: string(cm.UID), ResourceVersion: cm.ResourceVersion, Data: []byte(cm.Data["catalog.json"])}, nil
	}}, nil
}

func projectRefresh(ctx context.Context, args []string, stdout, stderr io.Writer, getenv func(string) string, runner agentd.Runner) int {
	if len(args) != 0 {
		_, _ = fmt.Fprintln(stderr, "agentd: project-refresh takes no arguments; authority comes from trusted pod configuration")
		return exitUsage
	}
	if getenv("AGENTD_ENABLE_PRIVATE_PROJECT_BOOTSTRAP") != "true" {
		_, _ = fmt.Fprintln(stderr, "agentd: private project bootstrap is disabled")
		return exitFailure
	}
	s, err := agentd.LoadSettings(getenv)
	if err != nil {
		return exitFailure
	}
	source, err := privateProjectCatalogSource(s, getenv)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return exitFailure
	}
	report, err := agentd.RefreshPrivateProjects(ctx, runner, s, source)
	if json.NewEncoder(stdout).Encode(report) != nil {
		return exitFailure
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return exitFailure
	}
	return exitOK
}
