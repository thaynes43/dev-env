package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"

	"k8s.io/apimachinery/pkg/types"

	"github.com/thaynes43/dev-env/internal/agentd"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/projectsync"
)

func projectSync(ctx context.Context, args []string, stdout, stderr io.Writer, getenv func(string) string, runner agentd.Runner) int {
	fs := flag.NewFlagSet("project-sync", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var config projectsync.Config
	var catalog, githubToken, apiToken, apiCA string
	fs.BoolVar(&config.Enabled, "enabled", false, "")
	fs.BoolVar(&config.Initialize, "initialize-new-workspace", false, "")
	fs.StringVar(&config.Namespace, "namespace", "", "")
	fs.StringVar(&catalog, "accepted-catalog", "", "")
	fs.StringVar(&config.WorkspaceID, "workspace-id", "", "")
	fs.StringVar(&config.CloneOwner, "project-clone-owner", "", "")
	fs.StringVar(&githubToken, "github-token-file", "", "")
	fs.StringVar(&apiToken, "kube-token-file", "", "")
	fs.StringVar(&apiCA, "kube-ca-file", "", "")
	if fs.Parse(args) != nil || fs.NArg() != 0 {
		return exitUsage
	}
	if !config.Enabled {
		_, _ = fmt.Fprintln(stderr, "project synchronization is disabled")
		return exitFailure
	}
	var ok bool
	config.CatalogNamespace, config.CatalogName, ok = strings.Cut(catalog, "/")
	if !ok || strings.Contains(config.CatalogName, "/") || githubToken != projectsync.GitHubTokenFile || apiToken != projectsync.APITokenFile || apiCA != projectsync.APICAFile || config.Namespace != getenv("DEV_ENV_POD_NAMESPACE") || getenv(protocol.SessionEnv) != "" || getenv(protocol.SessionFileEnv) != "" {
		_, _ = fmt.Fprintln(stderr, "project-sync requires its fixed management Job, named catalog and explicit read-only credential projections")
		return exitUsage
	}
	config.PodName, config.PodUID = getenv("DEV_ENV_POD_NAME"), types.UID(getenv("DEV_ENV_POD_UID"))
	settings, err := agentd.LoadSettings(getenv)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "project-sync private settings are invalid")
		return exitUsage
	}
	settings.GHTokenFile = projectsync.GitHubTokenFile
	if executable, ok := runner.(agentd.ExecRunner); ok {
		executable.BaseEnv = []string{"PATH=" + getenv("PATH"), "HOME=" + settings.Home, "TMPDIR=/tmp", "LANG=C", "LC_ALL=C"}
		runner = executable
	}
	if settings.WorkspaceID != config.WorkspaceID {
		_, _ = fmt.Fprintln(stderr, "project-sync workspace binding differs from the configured Job")
		return exitUsage
	}
	reader, err := projectsync.NewReader(getenv("KUBERNETES_SERVICE_HOST"), getenv("KUBERNETES_SERVICE_PORT"), apiToken, apiCA)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return exitFailure
	}
	result, err := projectsync.Run(ctx, config, settings, reader, runner)
	if result.Version != 0 {
		if encodeErr := json.NewEncoder(stdout).Encode(result); encodeErr != nil {
			return exitFailure
		}
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return exitFailure
	}
	return exitOK
}
