// Command dev-env-keeper is the only holder of dev-env v2's rotating credentials
// and GitHub App keys (DESIGN-001 3.1). It mints and refreshes them and writes
// the short-lived results into Secrets that session pods read. It runs as one
// replica, and a Lease keeps any second process waiting, so each rotating
// refresh token has exactly one owner.
//
// It is a binary of its own, shipped in the operator image (D-38). It links no
// /v1 API server and no controllers.
//
// Built so far (plan 01 step 6, D-52): the haynes-dev-bot installation token,
// minted every 40 minutes into dev-agents/dev-env-gh-token, with /healthz and
// /readyz. internal/keeper holds the work; this file reads the flags.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/thaynes43/dev-env/internal/keeper"
	"github.com/thaynes43/dev-env/internal/version"
)

const binaryName = "dev-env-keeper"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "validate-ssh-ca" {
		os.Exit(validateSSHCA(os.Args[2:], os.Stdout))
	}
	if len(os.Args) > 1 && (os.Args[1] == "codex-login" || os.Args[1] == "codex-auth-status" || os.Args[1] == "codex-auth-refresh-once" || os.Args[1] == "codex-login-idle") {
		os.Exit(codexLoginCommand(os.Args[1:], os.Stdin, os.Stdout))
	}
	if len(os.Args) == 2 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		fmt.Println(version.Get().String(binaryName))
		return
	}
	if err := run(os.Args[1:]); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "%s: %v\n", binaryName, err)
		os.Exit(1)
	}
}

// validateSSHCA exits before the normal keeper's Kubernetes and credential setup.
// Flag parsing and validation errors are represented only by fixed failure codes.
func validateSSHCA(args []string, out io.Writer) int {
	result := keeper.SSHCAValidation{Version: 1, FailureCode: keeper.SSHCAInvalidArguments}
	fs := flag.NewFlagSet("validate-ssh-ca", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	caDir := fs.String("ssh-ca-dir", keeper.DefaultSSHCAPath, "local CA projection")
	if fs.Parse(args) == nil && fs.NArg() == 0 && *caDir != "" {
		result = keeper.ValidateSSHCA(*caDir)
	}
	if json.NewEncoder(out).Encode(result) != nil || !result.Valid {
		return 1
	}
	return 0
}

type options struct {
	namespace       string
	secretNamespace string
	ghTokenSecret   string
	githubAppDir    string
	githubAPIURL    string
	permissions     map[string]string
	interval        time.Duration
	leaderElect     bool
	metricsAddr     string
	probeAddr       string
	proxmoxGrants   keeper.ProxmoxGrantOptions
	codexAuth       keeper.CodexOptions
}

func defaultPermissions() string {
	b, err := json.Marshal(keeper.DefaultDevBotPermissions)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func parseFlags(args []string) (options, error) {
	// The keeper's own namespace, from the downward API in the Deployment;
	// dev-env-system when unset (D-02).
	ownNamespace := os.Getenv("POD_NAMESPACE")
	if ownNamespace == "" {
		ownNamespace = "dev-env-system"
	}
	var o options
	fs := flag.NewFlagSet(binaryName, flag.ContinueOnError)
	fs.StringVar(&o.namespace, "namespace", ownNamespace, "the keeper's own namespace, where its leader-election Lease lives")
	fs.StringVar(&o.secretNamespace, "secret-namespace", "dev-agents", "namespace of the Secrets session pods mount (D-02)")
	fs.StringVar(&o.ghTokenSecret, "gh-token-secret", keeper.DefaultGHTokenSecret, "Secret the gh token goes into, key "+keeper.GHTokenKey+" (D-13); GitOps creates it empty")
	fs.StringVar(&o.githubAppDir, "github-app-dir", "/etc/dev-env-keeper/github-dev-bot", "directory of the haynes-dev-bot App's files: "+
		strings.Join([]string{keeper.AppFileClientID + " or " + keeper.AppFileAppID, keeper.AppFileInstallationID, keeper.AppFilePrivateKey}, ", ")+"; read at every mint")
	fs.StringVar(&o.githubAPIURL, "github-api-url", keeper.DefaultGitHubAPIURL, "GitHub's REST API")
	perms := fs.String("gh-token-permissions", defaultPermissions(), "JSON permission set that down-scopes the token; only permissions the App holds, or GitHub refuses the mint")
	fs.DurationVar(&o.interval, "refresh-interval", keeper.DefaultInterval, "longest wait between two mints; a token is also refreshed after two thirds of its life")
	fs.BoolVar(&o.leaderElect, "leader-elect", true, "hold the keeper's Lease before refreshing anything (one owner per credential)")
	fs.StringVar(&o.metricsAddr, "metrics-bind-address", ":8080", "address of the Prometheus metrics endpoint; 0 turns it off")
	fs.StringVar(&o.probeAddr, "health-probe-bind-address", ":8081", "address of /healthz and /readyz")
	fs.BoolVar(&o.proxmoxGrants.Enabled, "enable-proxmox-grants", false, "enable keeper PVE credential jobs; requires CA, pinned host trust and leader election")
	fs.BoolVar(&o.codexAuth.Enabled, "enable-codex-auth", false, "enable the dedicated keeper-only Codex auth worker; requires reviewed named-secret RBAC and isolated login helper")
	fs.StringVar(&o.codexAuth.JournalSecret, "codex-auth-journal-secret", keeper.DefaultCodexJournalSecret, "keeper-only private Codex auth journal Secret")
	fs.StringVar(&o.codexAuth.LiveSecret, "codex-live-secret", keeper.DefaultCodexLiveSecret, "access-only Codex projection Secret in secret-namespace")
	fs.StringVar(&o.codexAuth.LoginDir, "codex-login-dir", keeper.DefaultCodexLoginDir, "keeper/helper-only tmpfs staging and private control socket")
	fs.StringVar(&o.proxmoxGrants.SessionNamespace, "session-namespace", "dev-agents", "namespace containing credential grants and session pods")
	fs.StringVar(&o.proxmoxGrants.JournalSecret, "credential-journal-secret", keeper.DefaultCredentialJournalSecret, "keeper-only durable credential journal Secret in its own namespace")
	fs.StringVar(&o.proxmoxGrants.CADir, "ssh-ca-dir", keeper.DefaultSSHCAPath, "mounted Ed25519 CA directory (private-key/public-key)")
	fs.StringVar(&o.proxmoxGrants.TargetsFile, "proxmox-ssh-targets-file", keeper.DefaultSSHTargetsFile, "mounted JSON list of explicit pinned SSH host:22 targets")
	fs.StringVar(&o.proxmoxGrants.KnownHostsFile, "ssh-known-hosts-file", keeper.DefaultSSHKnownHostsFile, "mounted strict OpenSSH known-hosts file")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if fs.NArg() > 0 {
		return o, fmt.Errorf("unexpected arguments %q", fs.Args())
	}
	if o.namespace == "" || o.secretNamespace == "" || o.ghTokenSecret == "" || o.githubAppDir == "" || o.githubAPIURL == "" {
		return o, errors.New("--namespace, --secret-namespace, --gh-token-secret, --github-app-dir and --github-api-url must not be empty")
	}
	if !strings.HasPrefix(o.githubAPIURL, "https://") {
		return o, fmt.Errorf("--github-api-url %q: HTTPS only", o.githubAPIURL)
	}
	if o.interval < time.Minute {
		return o, fmt.Errorf("--refresh-interval %s: at least 1m", o.interval)
	}
	if o.proxmoxGrants.Enabled && (!o.leaderElect || o.proxmoxGrants.SessionNamespace == "" || o.proxmoxGrants.JournalSecret == "" || o.proxmoxGrants.CADir == "" || o.proxmoxGrants.TargetsFile == "" || o.proxmoxGrants.KnownHostsFile == "") {
		return o, errors.New("proxmox grants require leader election and nonempty credential configuration paths")
	}
	if o.codexAuth.Enabled && (!o.leaderElect || o.codexAuth.JournalSecret == "" || o.codexAuth.LiveSecret == "" || !filepath.IsAbs(o.codexAuth.LoginDir) || o.namespace == o.secretNamespace) {
		return o, errors.New("codex auth requires leader election, separate private/public namespaces and nonempty named configuration")
	}
	p, err := keeper.ParsePermissions(*perms)
	if err != nil {
		return o, fmt.Errorf("--gh-token-permissions: %w", err)
	}
	o.permissions = p
	return o, nil
}

func run(args []string) error {
	o, err := parseFlags(args)
	if err != nil {
		return err
	}
	log := logr.FromSlogHandler(slog.NewJSONHandler(os.Stderr, nil))
	ctrl.SetLogger(log)
	log = log.WithName(binaryName)
	info := version.Get()
	log.Info("starting", "version", info.String(binaryName), "namespace", o.namespace,
		"ghTokenSecret", o.secretNamespace+"/"+o.ghTokenSecret, "githubAppDir", o.githubAppDir)

	cfg, err := ctrl.GetConfig()
	if err != nil {
		return err
	}
	return keeper.Run(ctrl.SetupSignalHandler(), cfg, keeper.Options{
		Namespace:     o.namespace,
		GHTokenSecret: types.NamespacedName{Namespace: o.secretNamespace, Name: o.ghTokenSecret},
		GitHubApp: &keeper.GitHubApp{
			Dir:         o.githubAppDir,
			APIURL:      o.githubAPIURL,
			Permissions: o.permissions,
			UserAgent:   binaryName + "/" + info.Version,
		},
		ProxmoxGrants: o.proxmoxGrants,
		CodexAuth:     o.codexAuth,
		Interval:      o.interval,
		LeaderElect:   o.leaderElect,
		MetricsAddr:   o.metricsAddr,
		ProbeAddr:     o.probeAddr,
		Log:           log,
	})
}
